//go:build quota_pg

package data

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
)

func TestTenantSpaceClaimConcurrentAndLeaseTakeoverPostgres(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	ctx := appViewer.NewSystemViewerContext(context.Background())
	repo := newTenantRepoForLedgerTest(c)
	created, err := repo.Create(ctx, &identityV1.Tenant{Name: trans.Ptr("space-claim"), Code: trans.Ptr("space-claim")})
	require.NoError(t, err)
	type result struct {
		claim *TenantSpaceClaim
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, owner := range []string{"worker-a", "worker-b"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			<-start
			claim, err := repo.ClaimTenantSpace(ctx, owner, time.Minute)
			results <- result{claim, err}
		}(owner)
	}
	close(start)
	wg.Wait()
	close(results)
	var first *TenantSpaceClaim
	count := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.claim != nil {
			first = result.claim
			count++
		}
	}
	require.Equal(t, 1, count)
	require.Equal(t, created.GetId(), first.TenantID)

	_, err = c.DB().ExecContext(ctx, "UPDATE sys_tenants SET tenant_space_lease_until = statement_timestamp() - interval '1 second' WHERE id=$1", first.TenantID)
	require.NoError(t, err)
	changed, err := repo.DeferTenantSpace(ctx, first, 0)
	require.NoError(t, err)
	require.False(t, changed, "expired lease must not change the intent")
	second, err := repo.ClaimTenantSpace(ctx, "worker-c", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, first.OperationID, second.OperationID)
	require.Equal(t, first.ResourceTenantID, second.ResourceTenantID)
	require.Equal(t, first.LeaseGeneration+1, second.LeaseGeneration)
	changed, err = repo.DeferTenantSpace(ctx, first, 0)
	require.NoError(t, err)
	require.False(t, changed, "late worker must not change a newer claim")
	changed, err = repo.DeferTenantSpace(ctx, second, time.Minute)
	require.NoError(t, err)
	require.True(t, changed)
	row, err := c.Client().Tenant.Get(ctx, first.TenantID)
	require.NoError(t, err)
	require.Equal(t, tenant.TenantSpaceStateUNKNOWN, row.TenantSpaceState)
	require.Nil(t, row.TenantSpaceLeaseOwner)
	require.Nil(t, row.TenantSpaceLeaseUntil)
	deferred, err := repo.ClaimTenantSpace(ctx, "worker-d", time.Minute)
	require.NoError(t, err)
	require.Nil(t, deferred, "not-yet-due intent must not be dispatched")
	_, err = c.DB().ExecContext(ctx, "UPDATE sys_tenants SET tenant_space_next_attempt_at = statement_timestamp() - interval '1 second', status='OFF' WHERE id=$1", first.TenantID)
	require.NoError(t, err)
	stopped, err := repo.ClaimTenantSpace(ctx, "worker-d", time.Minute)
	require.NoError(t, err)
	require.Nil(t, stopped, "disabled tenants must not start provisioning")
	_, err = c.DB().ExecContext(ctx, "UPDATE sys_tenants SET status='ON' WHERE id=$1", first.TenantID)
	require.NoError(t, err)
	third, err := repo.ClaimTenantSpace(ctx, "worker-d", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, third)
	require.Equal(t, first.OperationID, third.OperationID)
	changed, err = repo.CompleteTenantSpace(ctx, second)
	require.NoError(t, err)
	require.False(t, changed, "stale worker must not mark a newer claim ready")
	changed, err = repo.CompleteTenantSpace(ctx, third)
	require.NoError(t, err)
	require.True(t, changed)
	row, err = c.Client().Tenant.Get(ctx, first.TenantID)
	require.NoError(t, err)
	require.Equal(t, tenant.TenantSpaceStateREADY, row.TenantSpaceState)
	claimed, err := repo.ClaimTenantSpace(ctx, "worker-e", time.Minute)
	require.NoError(t, err)
	require.Nil(t, claimed, "ready tenant must not be dispatched again")
}

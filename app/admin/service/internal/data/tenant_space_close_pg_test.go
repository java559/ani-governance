//go:build quota_pg

package data

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	kratosErrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
)

func TestTenantSpaceCloseStopsOpenDispatchAndKeepsIdentityPostgres(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	ctx := appViewer.NewSystemViewerContext(context.Background())
	repo := newTenantRepoForLedgerTest(c)
	created, err := repo.Create(ctx, &identityV1.Tenant{Name: trans.Ptr("space-close"), Code: trans.Ptr("space-close")})
	require.NoError(t, err)
	open, err := repo.ClaimTenantSpace(ctx, "open-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, open)
	intent, closed, err := repo.RequestTenantSpaceClose(ctx, created.GetId())
	require.NoError(t, err)
	require.False(t, closed)
	require.Equal(t, open.OperationID, intent.OperationID)
	updated, err := repo.CompleteTenantSpace(ctx, open)
	require.NoError(t, err)
	require.False(t, updated, "an in-flight open cannot mark a closing tenant ready")
	updated, err = repo.DeferTenantSpace(ctx, open, 0)
	require.NoError(t, err)
	require.False(t, updated, "an in-flight open cannot restore its lease")
	claim, err := repo.ClaimTenantSpaceClose(ctx, time.Minute)
	require.NoError(t, err)
	require.Equal(t, intent, claim)
	another, err := repo.ClaimTenantSpaceClose(ctx, time.Minute)
	require.NoError(t, err)
	require.Nil(t, another, "retry delay must be respected")
	changed := *intent
	changed.OperationID = uuid.NewString()
	updated, err = repo.CompleteTenantSpaceClose(ctx, &changed)
	require.NoError(t, err)
	require.False(t, updated, "unrelated operation cannot close this tenant")
	updated, err = repo.CompleteTenantSpaceClose(ctx, intent)
	require.NoError(t, err)
	require.True(t, updated)
	same, closed, err := repo.RequestTenantSpaceClose(ctx, created.GetId())
	require.NoError(t, err)
	require.True(t, closed)
	require.Equal(t, intent, same)
	row, err := c.Client().Tenant.Get(ctx, created.GetId())
	require.NoError(t, err)
	require.Equal(t, tenant.TenantSpaceStateCLOSED, row.TenantSpaceState)

	legacy, err := repo.Create(ctx, &identityV1.Tenant{Name: trans.Ptr("space-legacy"), Code: trans.Ptr("space-legacy")})
	require.NoError(t, err)
	_, err = c.DB().ExecContext(ctx, "UPDATE sys_tenants SET tenant_space_state='LEGACY',tenant_space_operation_id=NULL WHERE id=$1", legacy.GetId())
	require.NoError(t, err)
	legacyIntent, closed, err := repo.RequestTenantSpaceClose(ctx, legacy.GetId())
	require.NoError(t, err)
	require.False(t, closed)
	require.NotEmpty(t, legacyIntent.OperationID)
	replayed, closed, err := repo.RequestTenantSpaceClose(ctx, legacy.GetId())
	require.NoError(t, err)
	require.False(t, closed)
	require.Equal(t, legacyIntent, replayed)
}

func TestTenantDestructiveActionsWaitForSpaceClosePostgres(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	ctx := appViewer.NewSystemViewerContext(context.Background())
	repo := newTenantRepoForLedgerTest(c)
	usage := NewTenantUsageRepo(testutil.NewBootstrapContext(nil), c, nil)
	for _, kind := range []string{"delete", "cleanup"} {
		t.Run(kind, func(t *testing.T) {
			created, err := repo.Create(ctx, &identityV1.Tenant{Name: trans.Ptr("space-" + kind), Code: trans.Ptr("space-" + kind)})
			require.NoError(t, err)
			action := func() error {
				if kind == "delete" {
					return repo.Delete(ctx, deleteTenantReq(created.GetId()))
				}
				return usage.CleanupTenantData(ctx, created.GetId())
			}
			err = action()
			require.Error(t, err)
			row, err := c.Client().Tenant.Get(ctx, created.GetId())
			require.NoError(t, err)
			require.Equal(t, tenant.TenantSpaceStateCLOSING, row.TenantSpaceState)
			require.Equal(t, tenant.StatusOn, *row.Status)
			intent, closed, err := repo.RequestTenantSpaceClose(ctx, created.GetId())
			require.NoError(t, err)
			require.False(t, closed)
			completed, err := repo.CompleteTenantSpaceClose(ctx, intent)
			require.NoError(t, err)
			require.True(t, completed)
			require.NoError(t, action())
			if kind == "cleanup" {
				row, err = c.Client().Tenant.Get(ctx, created.GetId())
				require.NoError(t, err)
				require.Equal(t, tenant.StatusOff, *row.Status)
			} else {
				_, err = c.Client().Tenant.Get(ctx, created.GetId())
				require.Error(t, err)
			}
		})
	}
}

func TestTenantSpaceCloseWaitsForPendingSandboxCreatePostgres(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	ctx := appViewer.NewSystemViewerContext(context.Background())
	tenantID, _ := seedTenantPlan(t, c, "space-pending-create", 8)
	row, err := c.Client().Tenant.Get(ctx, tenantID)
	require.NoError(t, err)
	ledger := newTestRepo(c)
	tenantRepo := newTenantRepoForLedgerTest(c)
	create := SandboxCreateIntent{TenantID: tenantID, ResourceTenantID: row.ResourceTenantID, SandboxID: uuid.NewString(), OperationID: uuid.NewString(), IntentDigest: "digest", Actor: "governance:user:7", Command: []byte(`{}`)}
	_, _, err = ledger.RegisterSandboxCreateIntent(ctx, create)
	require.NoError(t, err)
	err = tenantRepo.Delete(ctx, deleteTenantReq(tenantID))
	require.Equal(t, "SANDBOX_HISTORY_PRESENT", kratosErrors.Reason(err), "physical deletion must explain retained sandbox history")
	_, _, err = tenantRepo.RequestTenantSpaceClose(ctx, tenantID)
	require.Error(t, err, "unknown create must block space close")
	row, err = c.Client().Tenant.Get(ctx, tenantID)
	require.NoError(t, err)
	require.NotEqual(t, tenant.TenantSpaceStateCLOSING, row.TenantSpaceState)
	require.NoError(t, ledger.AcknowledgeSandboxCreate(ctx, tenantID, create.SandboxID, create.OperationID))
	_, closed, err := tenantRepo.RequestTenantSpaceClose(ctx, tenantID)
	require.NoError(t, err)
	require.False(t, closed)
	newCreate := create
	newCreate.SandboxID = uuid.NewString()
	newCreate.OperationID = uuid.NewString()
	_, _, err = ledger.RegisterSandboxCreateIntent(ctx, newCreate)
	require.Error(t, err, "new create must be rejected once close is recorded")
	_, replayed, err := ledger.RegisterSandboxCreateIntent(ctx, create)
	require.NoError(t, err)
	require.True(t, replayed, "an accepted exact replay stays readable")
}

func TestTenantSpaceCloseRacesSandboxRegistrationPostgres(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	ctx := appViewer.NewSystemViewerContext(context.Background())
	ledger := newTestRepo(c)
	tenantRepo := newTenantRepoForLedgerTest(c)
	for i := 0; i < 10; i++ {
		tenantID, _ := seedTenantPlan(t, c, fmt.Sprintf("space-register-race-%d", i), 8)
		row, err := c.Client().Tenant.Get(ctx, tenantID)
		require.NoError(t, err)
		create := SandboxCreateIntent{TenantID: tenantID, ResourceTenantID: row.ResourceTenantID, SandboxID: uuid.NewString(), OperationID: uuid.NewString(), IntentDigest: "digest", Actor: "governance:user:7", Command: []byte(`{}`)}
		start := make(chan struct{})
		var createErr, closeErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, _, createErr = ledger.RegisterSandboxCreateIntent(ctx, create) }()
		go func() { defer wg.Done(); <-start; _, _, closeErr = tenantRepo.RequestTenantSpaceClose(ctx, tenantID) }()
		close(start)
		wg.Wait()
		if createErr == nil {
			require.Error(t, closeErr, "pending create must block close")
			require.EqualValues(t, 409, kratosErrors.Code(closeErr))
		} else {
			require.NoError(t, closeErr, "close must block a later registration")
			require.EqualValues(t, 409, kratosErrors.Code(createErr))
		}
	}
}

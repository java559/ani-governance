//go:build quota_pg

package data

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
)

func TestTenantSpaceIntentPostgres(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	ctx := appViewer.NewSystemViewerContext(context.Background())
	legacyID, _ := seedTenantPlan(t, c, "space-legacy", 8)
	legacy, err := c.Client().Tenant.Get(ctx, legacyID)
	require.NoError(t, err)
	require.Equal(t, tenant.TenantSpaceStateLEGACY, legacy.TenantSpaceState)
	require.Nil(t, legacy.TenantSpaceOperationID)

	repo := newTenantRepoForLedgerTest(c)
	created, err := repo.Create(ctx, &identityV1.Tenant{Name: trans.Ptr("space-new"), Code: trans.Ptr("space-new")})
	require.NoError(t, err)
	row, err := c.Client().Tenant.Get(ctx, created.GetId())
	require.NoError(t, err)
	require.Equal(t, tenant.TenantSpaceStateQUEUED, row.TenantSpaceState)
	require.NotNil(t, row.TenantSpaceOperationID)
	_, err = uuid.Parse(*row.TenantSpaceOperationID)
	require.NoError(t, err)

	// A queued row without a durable operation identity must not pass the DB
	// constraint, even if a caller bypasses the application repository.
	_, err = c.DB().ExecContext(ctx, "UPDATE sys_tenants SET tenant_space_state='QUEUED' WHERE id=$1", legacyID)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "23514", pgErr.Code)
	tx, err := c.Client().Tx(ctx)
	require.NoError(t, err)
	_, err = repo.CreateWithTx(ctx, tx, &identityV1.Tenant{Name: trans.Ptr("space-rollback"), Code: trans.Ptr("space-rollback")})
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	count, err := c.Client().Tenant.Query().Where(tenant.CodeEQ("space-rollback")).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

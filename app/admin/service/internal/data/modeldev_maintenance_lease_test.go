//go:build modeldev_pg

package data

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
)

func TestModelDevCreateMaintenanceLeaseExitUsesExactWorker(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	repo := NewModelDevAcceptanceRepo(client)
	_, _, err := repo.AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	claim, err := repo.ClaimDelivery(ctx, "maintenance-original", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	active, err := repo.HasDeliveryLease(ctx, "maintenance-original")
	require.NoError(t, err)
	require.True(t, active)
	active, err = repo.HasDeliveryLease(ctx, "maintenance-other")
	require.NoError(t, err)
	require.False(t, active)
	_, err = client.Client().ModelDevAcceptance.Update().Where(modeldevacceptance.OperationIDEQ(candidate.OperationID)).SetLeaseUntil(time.Now().UTC().Add(-time.Second)).Save(ctx)
	require.NoError(t, err)
	active, err = repo.HasDeliveryLease(ctx, "maintenance-original")
	require.NoError(t, err)
	require.False(t, active)
}

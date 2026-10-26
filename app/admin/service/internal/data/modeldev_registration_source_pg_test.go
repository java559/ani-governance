//go:build modeldev_pg

package data

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
)

func TestModelDevRegistrationSourceReadsOriginalWithoutReaccepting(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	repo := NewModelDevAcceptanceRepo(client)
	original, _, err := repo.AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	reader := NewModelDevAcceptanceRepo(newModelDevPGClient(t))
	actual, err := reader.FindRegistrationSource(ctx, scope.TenantID, scope.ResourceTenantID, candidate.ExecutionID)
	require.NoError(t, err)
	requireSameModelDevAcceptance(t, original, actual)
	// Current operator identity is deliberately not a lookup selector. It is
	// reauthorized separately; original actor, key and identities stay frozen.
	require.Equal(t, scope.Actor, actual.Scope.Actor)
	require.Equal(t, scope.IdempotencyKey, actual.Scope.IdempotencyKey)
	require.Equal(t, 1, client.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID)).CountX(ctx))
	for _, other := range []struct {
		tenant              uint32
		resource, execution string
	}{
		{scope.TenantID + 1, scope.ResourceTenantID, candidate.ExecutionID},
		{scope.TenantID, uuid.NewString(), candidate.ExecutionID},
		{scope.TenantID, scope.ResourceTenantID, uuid.NewString()},
	} {
		value, err := reader.FindRegistrationSource(ctx, other.tenant, other.resource, other.execution)
		require.ErrorIs(t, err, ErrModelDevAcceptanceNotFound)
		require.Nil(t, value)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = reader.FindRegistrationSource(canceled, scope.TenantID, scope.ResourceTenantID, candidate.ExecutionID)
	require.ErrorIs(t, err, context.Canceled)
}

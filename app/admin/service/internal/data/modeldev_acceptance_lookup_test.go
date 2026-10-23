//go:build modeldev_pg

package data

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
)

func TestModelDevAcceptanceFindsOriginalBeforeCatalogueResolution(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	repo := NewModelDevAcceptanceRepo(client)
	// The preliminary read needs only intent. A miss must work before any
	// current compute resolution and leave the key available for real acceptance.
	missing, err := repo.FindAccepted(ctx, scope, candidate.Intent)
	require.ErrorIs(t, err, ErrModelDevAcceptanceNotFound)
	require.Nil(t, missing)
	count, err := client.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)

	original, replayed, err := repo.AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.False(t, replayed)

	reader := newModelDevPGClient(t)
	actual, err := NewModelDevAcceptanceRepo(reader).FindAccepted(ctx, scope, candidate.Intent)
	require.NoError(t, err)
	require.NotNil(t, actual)
	require.Equal(t, original.Scope, actual.Scope)
	require.Equal(t, original.OperationID, actual.OperationID)
	require.Equal(t, original.ExecutionID, actual.ExecutionID)
	require.Equal(t, original.IntentHash, actual.IntentHash)
	require.Equal(t, fixtureSnapshotHash(t, candidate), actual.ExecutionSpecHash)
	require.Equal(t, "QUEUED", actual.DispatchState)
	require.True(t, original.AcceptedAt.Equal(actual.AcceptedAt))
	canonical, err := actual.Snapshot.Canonical()
	require.NoError(t, err)
	require.Equal(t, fixtureSnapshotBytes(t, candidate), canonical)
	count, err = reader.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count, "lookup must neither requeue nor create another acceptance")

}

func TestModelDevAcceptanceFindRejectsConflictingIntentWithoutMutation(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	original, replayed, err := NewModelDevAcceptanceRepo(client).AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.False(t, replayed)

	changed := candidate.Intent
	changed.Name = "different-training-name"
	_, _, err = cpup01.CanonicalIntent(changed)
	require.NoError(t, err)
	reader := newModelDevPGClient(t)
	repo := NewModelDevAcceptanceRepo(reader)
	actual, err := repo.FindAccepted(ctx, scope, changed)
	require.ErrorIs(t, err, ErrModelDevIdempotencyConflict)
	require.Nil(t, actual)
	actual, err = repo.FindAccepted(ctx, scope, candidate.Intent)
	require.NoError(t, err)
	requireSameModelDevAcceptance(t, original, actual)
	count, err := reader.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestModelDevAcceptanceFindRejectsInvalidScopeAndIntent(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	_, _, otherScope, _ := newModelDevAcceptanceFixture(t)
	original, replayed, err := NewModelDevAcceptanceRepo(client).AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.False(t, replayed)
	reader := newModelDevPGClient(t)
	repo := NewModelDevAcceptanceRepo(reader)
	tests := []struct {
		name   string
		change func(*ModelDevAdmissionScope, *cpup01.Intent)
	}{
		{"zero tenant", func(s *ModelDevAdmissionScope, _ *cpup01.Intent) { s.TenantID = 0 }},
		{"missing resource tenant", func(s *ModelDevAdmissionScope, _ *cpup01.Intent) { s.ResourceTenantID = "" }},
		{"missing actor", func(s *ModelDevAdmissionScope, _ *cpup01.Intent) { s.Actor = "" }},
		{"actor with leading whitespace", func(s *ModelDevAdmissionScope, _ *cpup01.Intent) { s.Actor = " governance:user:7" }},
		{"wrong action", func(s *ModelDevAdmissionScope, _ *cpup01.Intent) { s.Action = "modeldev.execution.stop" }},
		{"missing key", func(s *ModelDevAdmissionScope, _ *cpup01.Intent) { s.IdempotencyKey = "" }},
		{"invalid intent", func(_ *ModelDevAdmissionScope, i *cpup01.Intent) { i.Name = "" }},
		{"other real resource tenant", func(s *ModelDevAdmissionScope, _ *cpup01.Intent) { s.ResourceTenantID = otherScope.ResourceTenantID }},
		{"other real local tenant", func(s *ModelDevAdmissionScope, _ *cpup01.Intent) { s.TenantID = otherScope.TenantID }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalidScope, invalidIntent := scope, candidate.Intent
			tt.change(&invalidScope, &invalidIntent)
			actual, err := repo.FindAccepted(ctx, invalidScope, invalidIntent)
			require.ErrorIs(t, err, cpup01.ErrInvalidArgument)
			require.Nil(t, actual)
		})
	}
	actual, err := repo.FindAccepted(ctx, scope, candidate.Intent)
	require.NoError(t, err)
	requireSameModelDevAcceptance(t, original, actual)
	for tenantID, expected := range map[uint32]int{scope.TenantID: 1, otherScope.TenantID: 0} {
		count, err := reader.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(tenantID)).Count(ctx)
		require.NoError(t, err)
		require.Equal(t, expected, count)
	}
}

func TestModelDevAcceptanceFindIsolatesTenantActorAndKey(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	_, _, otherTenant, otherCandidate := newModelDevAcceptanceFixture(t)
	original, replayed, err := NewModelDevAcceptanceRepo(client).AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.False(t, replayed)
	otherTenant.Actor, otherTenant.IdempotencyKey = scope.Actor, scope.IdempotencyKey
	otherActor, otherKey := scope, scope
	otherActor.Actor = "governance:access-key:9"
	otherKey.IdempotencyKey = uuid.NewString()
	reader := newModelDevPGClient(t)
	repo := NewModelDevAcceptanceRepo(reader)
	groups := []struct {
		name      string
		scope     ModelDevAdmissionScope
		candidate ModelDevFrozenCandidate
	}{
		{"other tenant", otherTenant, otherCandidate},
		{"other actor", otherActor, candidate},
		{"other key", otherKey, candidate},
	}
	for _, group := range groups {
		t.Run(group.name, func(t *testing.T) {
			separate := group.candidate
			separate.OperationID, separate.ExecutionID = uuid.NewString(), uuid.NewString()
			separate.Intent.Name = "separate-training"
			separate = freezeModelDevFixture(t, group.scope, separate)
			requireValidModelDevCandidate(t, group.scope, separate)
			// A different valid scope must not expose the original, including by
			// reporting its different intent as a conflict. Current authorization
			// remains the caller's responsibility, separate from this DB boundary.
			missing, err := repo.FindAccepted(ctx, group.scope, separate.Intent)
			require.ErrorIs(t, err, ErrModelDevAcceptanceNotFound)
			require.Nil(t, missing)
			accepted, replayed, err := repo.AcceptFrozen(ctx, group.scope, separate)
			require.NoError(t, err)
			require.False(t, replayed)
			require.NotEqual(t, original.OperationID, accepted.OperationID)
			actual, err := repo.FindAccepted(ctx, group.scope, separate.Intent)
			require.NoError(t, err)
			requireSameModelDevAcceptance(t, accepted, actual)
		})
	}
	actual, err := repo.FindAccepted(ctx, scope, candidate.Intent)
	require.NoError(t, err)
	requireSameModelDevAcceptance(t, original, actual)
	for tenantID, expected := range map[uint32]int{scope.TenantID: 3, otherTenant.TenantID: 1} {
		count, err := reader.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(tenantID)).Count(ctx)
		require.NoError(t, err)
		require.Equal(t, expected, count)
	}
}

func requireSameModelDevAcceptance(t *testing.T, expected, actual *ModelDevAcceptance) {
	t.Helper()
	require.NotNil(t, expected)
	require.NotNil(t, actual)
	// PostgreSQL timestamptz preserves the instant, not Go's time.Location.
	// Normalize only location on copies; retain all fields and time precision.
	want, got := *expected, *actual
	want.AcceptedAt, got.AcceptedAt = want.AcceptedAt.UTC(), got.AcceptedAt.UTC()
	require.Equal(t, want, got)
}

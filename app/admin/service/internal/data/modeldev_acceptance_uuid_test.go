//go:build modeldev_pg

package data

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
)

func TestModelDevAcceptanceCanonicalUUIDIdentity(t *testing.T) {
	ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
	// Preserve random fixture isolation while guaranteeing alphabetic digits,
	// so the upper/lower representations are observably different strings.
	operationID := "abcd" + uuid.NewString()[4:]
	executionID := "fedc" + uuid.NewString()[4:]
	candidate.OperationID = strings.ToUpper(operationID)
	candidate.ExecutionID = strings.ToUpper(executionID)

	candidate = freezeModelDevFixture(t, scope, candidate)
	accepted, replayed, err := NewModelDevAcceptanceRepo(client).AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.False(t, replayed)
	require.Equal(t, operationID, accepted.OperationID)
	require.Equal(t, executionID, accepted.ExecutionID)

	reader := newModelDevPGClient(t)
	stored, err := reader.Client().ModelDevAcceptance.Query().Where(
		modeldevacceptance.TenantIDEQ(scope.TenantID),
		modeldevacceptance.OperationIDEQ(operationID),
		modeldevacceptance.ExecutionIDEQ(executionID),
	).Only(ctx)
	require.NoError(t, err, "committed identifiers must use the canonical UUID spelling")
	require.Equal(t, "QUEUED", string(stored.DispatchState))
	require.Equal(t, scope.IdempotencyKey, stored.IdempotencyKey)
}

func TestModelDevAcceptanceUUIDAliasesCannotQueueTwice(t *testing.T) {
	cases := []struct {
		name       string
		operation  bool
		upperFirst bool
	}{
		{name: "operation lower then upper", operation: true},
		{name: "operation upper then lower", operation: true, upperFirst: true},
		{name: "execution lower then upper"},
		{name: "execution upper then lower", upperFirst: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
			operationID := "abcd" + uuid.NewString()[4:]
			executionID := "fedc" + uuid.NewString()[4:]
			candidate.OperationID, candidate.ExecutionID = operationID, executionID
			if test.upperFirst {
				candidate.OperationID = strings.ToUpper(operationID)
				candidate.ExecutionID = strings.ToUpper(executionID)
			}
			candidate = freezeModelDevFixture(t, scope, candidate)
			repo := NewModelDevAcceptanceRepo(client)
			original, replayed, err := repo.AcceptFrozen(ctx, scope, candidate)
			require.NoError(t, err)
			require.False(t, replayed)

			otherScope, alias := scope, candidate
			otherScope.IdempotencyKey = uuid.NewString()
			alias.OperationID, alias.ExecutionID = uuid.NewString(), uuid.NewString()
			alias = freezeModelDevFixture(t, otherScope, alias)
			aliasedID := executionID
			if test.operation {
				aliasedID = operationID
			}
			if !test.upperFirst {
				aliasedID = strings.ToUpper(aliasedID)
			}
			if test.operation {
				alias.OperationID = aliasedID
			} else {
				alias.ExecutionID = aliasedID
			}
			// This is a valid shared envelope. Its failure must arise from reusing
			// a UUID identity, not an invalid shape, binding or public intent.
			alias = freezeModelDevFixture(t, otherScope, alias)
			_, intentHash, err := cpup01.CanonicalIntent(alias.Intent)
			require.NoError(t, err)
			specHash, err := alias.Snapshot.Digest()
			require.NoError(t, err)
			envelope := cpup01.AdmissionEnvelope{
				TenantID: scope.ResourceTenantID, Actor: scope.Actor,
				OperationID: alias.OperationID, ExecutionID: alias.ExecutionID,
				Intent: alias.Intent, IntentHash: intentHash,
				Snapshot: alias.Snapshot, SpecHash: specHash, AcceptedAt: alias.AcceptedAt,
			}
			_, _, err = envelope.CanonicalPayloads()
			require.NoError(t, err)

			rejected, replayed, err := repo.AcceptFrozen(ctx, otherScope, alias)
			require.Error(t, err, "a second key cannot create another queue entry for a UUID alias")
			require.Nil(t, rejected)
			require.False(t, replayed)
			reader := newModelDevPGClient(t)
			rows, err := reader.Client().ModelDevAcceptance.Query().Where(
				modeldevacceptance.TenantIDEQ(scope.TenantID),
			).All(ctx)
			require.NoError(t, err)
			require.Len(t, rows, 1, "alias rejection must not persist a second QUEUED row")
			require.Equal(t, scope.IdempotencyKey, rows[0].IdempotencyKey)
			require.Equal(t, operationID, rows[0].OperationID)
			require.Equal(t, executionID, rows[0].ExecutionID)
			require.Equal(t, original.ExecutionSpecHash, rows[0].ExecutionSpecHash)

			// The failed insert must roll back the key as well as the queue entry.
			alias.OperationID, alias.ExecutionID = uuid.NewString(), uuid.NewString()
			alias = freezeModelDevFixture(t, otherScope, alias)
			accepted, replayed, err := NewModelDevAcceptanceRepo(reader).AcceptFrozen(ctx, otherScope, alias)
			require.NoError(t, err)
			require.False(t, replayed)
			require.Equal(t, alias.OperationID, accepted.OperationID)
			require.Equal(t, alias.ExecutionID, accepted.ExecutionID)
			restored, replayed, err := NewModelDevAcceptanceRepo(reader).AcceptFrozen(ctx, scope, candidate)
			require.NoError(t, err)
			require.True(t, replayed)
			require.Equal(t, operationID, restored.OperationID)
			require.Equal(t, executionID, restored.ExecutionID)
			require.Equal(t, original.IntentHash, restored.IntentHash)
			require.Equal(t, original.ExecutionSpecHash, restored.ExecutionSpecHash)
			count, err := reader.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID)).Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 2, count)
		})
	}
}

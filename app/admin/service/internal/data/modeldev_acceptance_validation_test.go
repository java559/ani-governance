//go:build modeldev_pg

package data

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

func TestModelDevAcceptanceRejectsInvalidEnvelopeWithoutConsumingKey(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ModelDevAdmissionScope, *ModelDevFrozenCandidate)
	}{
		{"malformed resource tenant UUID", func(scope *ModelDevAdmissionScope, _ *ModelDevFrozenCandidate) {
			scope.ResourceTenantID = "not-a-uuid"
		}},
		{"zero resource tenant UUID", func(scope *ModelDevAdmissionScope, _ *ModelDevFrozenCandidate) {
			scope.ResourceTenantID = uuid.Nil.String()
		}},
		{"malformed operation UUID", func(_ *ModelDevAdmissionScope, candidate *ModelDevFrozenCandidate) {
			candidate.OperationID = "operation-1"
		}},
		{"zero operation UUID", func(_ *ModelDevAdmissionScope, candidate *ModelDevFrozenCandidate) {
			candidate.OperationID = uuid.Nil.String()
		}},
		{"malformed execution UUID", func(_ *ModelDevAdmissionScope, candidate *ModelDevFrozenCandidate) {
			candidate.ExecutionID = "execution-1"
		}},
		{"zero execution UUID", func(_ *ModelDevAdmissionScope, candidate *ModelDevFrozenCandidate) {
			candidate.ExecutionID = uuid.Nil.String()
		}},
		{"actor control character", func(scope *ModelDevAdmissionScope, _ *ModelDevFrozenCandidate) {
			scope.Actor = "governance:user:7\n"
		}},
		{"accepted time loses precision", func(_ *ModelDevAdmissionScope, candidate *ModelDevFrozenCandidate) {
			candidate.AcceptedAt = candidate.AcceptedAt.Add(time.Nanosecond)
		}},
		{"different preset", func(_ *ModelDevAdmissionScope, candidate *ModelDevFrozenCandidate) {
			candidate.Intent.ComputePresetID = uuid.NewString()
		}},
		{"different input", func(_ *ModelDevAdmissionScope, candidate *ModelDevFrozenCandidate) {
			candidate.Intent.DatasetVersionID = uuid.NewString()
		}},

		{"different explicitly selected learning rate", func(_ *ModelDevAdmissionScope, candidate *ModelDevFrozenCandidate) {
			rate := "0.02"
			p := *candidate.Intent.MLP
			p.LearningRate = &rate
			candidate.Intent.MLP = &p
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx, client, scope, candidate := newModelDevAcceptanceFixture(t)
			invalidScope, invalidCandidate := scope, candidate
			test.mutate(&invalidScope, &invalidCandidate)
			accepted, replayed, err := NewModelDevAcceptanceRepo(client).AcceptFrozen(ctx, invalidScope, invalidCandidate)
			require.ErrorIs(t, err, cpup01.ErrInvalidArgument)
			require.Nil(t, accepted)
			require.False(t, replayed)

			reader := newModelDevPGClient(t)
			count, err := reader.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(scope.TenantID)).Count(ctx)
			require.NoError(t, err)
			require.Zero(t, count, "rejected admission must not leave a queued record")
			accepted, replayed, err = NewModelDevAcceptanceRepo(reader).AcceptFrozen(ctx, scope, candidate)
			require.NoError(t, err, "a corrected admission can still claim the same key")
			require.False(t, replayed)
			require.Equal(t, candidate.OperationID, accepted.OperationID)
			require.Equal(t, candidate.ExecutionID, accepted.ExecutionID)
		})
	}
}

// Each scenario owns one tenant and removes only its own rows. IDs here are
// synthetic contract fixtures, never evidence of real ENV catalogue resources.
func newModelDevAcceptanceFixture(t *testing.T) (context.Context, *entCrud.EntClient[*ent.Client], ModelDevAdmissionScope, ModelDevFrozenCandidate) {
	t.Helper()
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 30*time.Second)
	t.Cleanup(cancel)
	client := newModelDevPGClient(t)
	fixtureID := uuid.NewString()
	tenant, err := client.Client().Tenant.Create().SetName("cpu-" + fixtureID).SetCode("cpu-" + fixtureID).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
		defer stop()
		_, err := client.Client().ModelDevAcceptance.Delete().Where(modeldevacceptance.TenantIDEQ(tenant.ID)).Exec(cleanup)
		require.NoError(t, err)
		require.NoError(t, client.Client().Tenant.DeleteOneID(tenant.ID).Exec(cleanup))
	})
	snapshot := conformance.SnapshotV1()
	intent := conformance.IntentV1()
	intent.ComputePresetID = snapshot.ComputeOrigin.ComputePresetID
	intent.DatasetVersionID = snapshot.Input.InputVersionID
	scope := ModelDevAdmissionScope{TenantID: tenant.ID, ResourceTenantID: tenant.ResourceTenantID, Actor: "governance:user:7", Action: ModelDevCreateAction, IdempotencyKey: uuid.NewString()}
	candidate := ModelDevFrozenCandidate{OperationID: uuid.NewString(), ExecutionID: uuid.NewString(), Intent: intent, Snapshot: snapshot, AcceptedAt: time.Now().UTC().Truncate(time.Microsecond)}
	candidate = freezeModelDevFixture(t, scope, candidate)
	return ctx, client, scope, candidate
}

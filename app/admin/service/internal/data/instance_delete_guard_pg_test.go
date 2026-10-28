//go:build quota_pg

package data

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	kratosErrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

// A06/A07: CPU and GPU use the same immutable CREATE fence. Definite version
// rejection reopens it, while replay keeps the rejected intent and operation.
func TestQuotaPostgresInstanceDeleteFenceRejectReplayAndConcurrentKeys(t *testing.T) {
	for _, compute := range []string{"cpu", "gpu"} {
		t.Run(compute, func(t *testing.T) {
			f := newInstanceDeletePGFixture(t, compute)
			first := f.deleteInput()
			accepted, err := f.repo.CreateDeleteOperation(f.ctx, first)
			require.NoError(t, err)
			require.False(t, accepted.Replayed)
			f.requireGate(t, true)
			queued := opByOperationID(t, f.client, accepted.OperationID)
			require.Equal(t, quotaoperation.DispatchStateQueued, queued.DispatchState)
			ok, err := f.repo.RejectInstanceDeleteVersion(f.ctx, f.tenant, accepted.OperationID, queued.LeaseGeneration)
			require.NoError(t, err)
			require.False(t, ok, "a queued command has no dispatcher lease")

			claimed := f.claim(t, accepted.OperationID)
			otherTenant, _ := seedTenantPlan(t, f.client, "instance-delete-other-"+compute, 8)
			ok, err = f.repo.RejectInstanceDeleteVersion(f.ctx, otherTenant, accepted.OperationID, claimed.LeaseGeneration)
			require.NoError(t, err)
			require.False(t, ok, "a tenant cannot reject another tenant's DELETE")
			ok, err = f.repo.RejectInstanceDeleteVersion(f.ctx, f.tenant, accepted.OperationID, claimed.LeaseGeneration+1)
			require.NoError(t, err)
			require.False(t, ok, "an unowned generation cannot clear the fence")
			require.Equal(t, quotaoperation.DispatchStateDispatching, opByOperationID(t, f.client, accepted.OperationID).DispatchState)
			f.requireGate(t, true)

			// Expire only the lease using the database clock, then let the real
			// claim transaction perform takeover and advance its generation.
			now := f.databaseNow(t)
			require.NoError(t, f.client.Client().QuotaOperation.UpdateOneID(queued.ID).SetLeaseUntil(now.Add(-time.Second)).Exec(f.sys))
			ok, err = f.repo.RejectInstanceDeleteVersion(f.ctx, f.tenant, accepted.OperationID, claimed.LeaseGeneration)
			require.NoError(t, err)
			require.False(t, ok, "an expired current lease cannot reject")
			f.requireGate(t, true)
			takeover := f.claim(t, accepted.OperationID)
			require.Greater(t, takeover.LeaseGeneration, claimed.LeaseGeneration)
			ok, err = f.repo.RejectInstanceDeleteVersion(f.ctx, f.tenant, accepted.OperationID, claimed.LeaseGeneration)
			require.NoError(t, err)
			require.False(t, ok, "the old dispatcher cannot reject after takeover")
			ok, err = f.repo.RejectInstanceDeleteVersion(f.ctx, f.tenant, accepted.OperationID, takeover.LeaseGeneration)
			require.NoError(t, err)
			require.True(t, ok)
			f.requireGate(t, false)
			rejected := opByOperationID(t, f.client, accepted.OperationID)
			require.Equal(t, quotaoperation.DispatchStateRejected, rejected.DispatchState)
			require.NotNil(t, rejected.LastErrorCode)
			require.Equal(t, "INSTANCE_VERSION_CONFLICT", *rejected.LastErrorCode)
			require.Nil(t, rejected.AckJSON)
			require.Nil(t, rejected.LeaseOwner)
			require.Nil(t, rejected.LeaseUntil)
			require.Nil(t, rejected.NextAttemptAt)
			require.Equal(t, first.CanonicalRequest, rejected.CanonicalRequest, "the frozen expected version must not be rewritten")
			require.Equal(t, first.IdempotencyKey, rejected.IdempotencyKey)
			require.Equal(t, first.RequestHash, rejected.RequestHash)
			ok, err = f.repo.AckDispatched(f.ctx, f.tenant, accepted.OperationID, takeover.LeaseGeneration, f.ack(t, accepted.OperationID, accepted.ResourceID))
			require.NoError(t, err)
			require.False(t, ok, "a late ACK cannot revive a rejected operation")
			f.requireOriginalAccounting(t)

			// Start both fresh-key transactions together. The original CREATE
			// lock must admit one and return the exact conflict for the other.
			type outcome struct {
				result *QuotaDeleteResult
				err    error
			}
			start := make(chan struct{})
			outcomes := make(chan outcome, 2)
			var wg sync.WaitGroup
			for range 2 {
				in := f.deleteInput()
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					result, err := f.repo.CreateDeleteOperation(f.ctx, in)
					outcomes <- outcome{result, err}
				}()
			}
			close(start)
			wg.Wait()
			close(outcomes)
			var admitted *QuotaDeleteResult
			conflicts := 0
			for got := range outcomes {
				if got.err == nil {
					require.Nil(t, admitted, "only one fresh key may be admitted")
					require.NotNil(t, got.result)
					admitted = got.result
				} else {
					require.Equal(t, "INSTANCE_DELETING", kratosErrors.FromError(got.err).Reason)
					conflicts++
				}
			}
			require.NotNil(t, admitted)
			require.Equal(t, 1, conflicts)
			require.NotEqual(t, accepted.OperationID, admitted.OperationID)
			require.Equal(t, f.original.OperationID, admitted.CreateOperationID)
			require.Equal(t, 1, f.client.Client().QuotaOperation.Query().Where(quotaoperation.ActionEQ(instanceCPUDeleteAction), quotaoperation.DispatchStateNEQ(quotaoperation.DispatchStateRejected)).CountX(f.sys))
			f.requireGate(t, true)

			// Replay precedes the now-closed fresh-intent fence. The old key
			// remains rejected and must never be turned into a new dispatch.
			replay, err := f.repo.CreateDeleteOperation(f.ctx, first)
			require.NoError(t, err)
			require.True(t, replay.Replayed)
			require.True(t, replay.OwnerRejected)
			require.Equal(t, accepted.OperationID, replay.OperationID)
			require.Equal(t, accepted.ResourceID, replay.ResourceID)
			afterReplay := opByOperationID(t, f.client, accepted.OperationID)
			require.Equal(t, rejected.CanonicalRequest, afterReplay.CanonicalRequest)
			require.Equal(t, rejected.AttemptCount, afterReplay.AttemptCount)
			require.Equal(t, rejected.LeaseGeneration, afterReplay.LeaseGeneration)
			require.Equal(t, quotaoperation.DispatchStateRejected, afterReplay.DispatchState)
			f.claim(t, admitted.OperationID)
			f.requireOriginalAccounting(t)
		})
	}
}

// A09: a timeout/ACK loss stays UNKNOWN. A late version rejection cannot
// overwrite UNKNOWN, the replacement dispatcher's state, or a durable ACK.
func TestQuotaPostgresInstanceDeleteUnknownAndAckKeepFence(t *testing.T) {
	for _, compute := range []string{"cpu", "gpu"} {
		t.Run(compute, func(t *testing.T) {
			f := newInstanceDeletePGFixture(t, compute)
			input := f.deleteInput()
			accepted, err := f.repo.CreateDeleteOperation(f.ctx, input)
			require.NoError(t, err)
			claimed := f.claim(t, accepted.OperationID)
			ok, err := f.repo.MarkUnknown(f.ctx, f.tenant, accepted.OperationID, claimed.LeaseGeneration, f.databaseNow(t).Add(-time.Second), "OWNER_ACK_UNCERTAIN", false)
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = f.repo.RejectInstanceDeleteVersion(f.ctx, f.tenant, accepted.OperationID, claimed.LeaseGeneration)
			require.NoError(t, err)
			require.False(t, ok, "UNKNOWN is not a definite owner rejection")
			unknown := opByOperationID(t, f.client, accepted.OperationID)
			require.Equal(t, quotaoperation.DispatchStateUnknown, unknown.DispatchState)
			require.NotNil(t, unknown.LastErrorCode)
			require.Equal(t, "OWNER_ACK_UNCERTAIN", *unknown.LastErrorCode)
			replay, err := f.repo.CreateDeleteOperation(f.ctx, input)
			require.NoError(t, err)
			require.True(t, replay.Replayed)
			require.False(t, replay.OwnerRejected)
			require.Equal(t, accepted.OperationID, replay.OperationID)
			f.requireGate(t, true)
			_, err = f.repo.CreateDeleteOperation(f.ctx, f.deleteInput())
			require.Error(t, err)
			require.Equal(t, "INSTANCE_DELETING", kratosErrors.FromError(err).Reason)
			f.requireOriginalAccounting(t)

			takeover := f.claim(t, accepted.OperationID)
			require.Greater(t, takeover.LeaseGeneration, claimed.LeaseGeneration)
			ok, err = f.repo.RejectInstanceDeleteVersion(f.ctx, f.tenant, accepted.OperationID, claimed.LeaseGeneration)
			require.NoError(t, err)
			require.False(t, ok)
			// A persisted ACK independently forbids rejection even if an
			// interrupted/recovered row still says DISPATCHING.
			ack := f.ack(t, accepted.OperationID, accepted.ResourceID)
			require.NoError(t, f.client.Client().QuotaOperation.UpdateOneID(takeover.ID).SetAckJSON(ack).Exec(f.sys))
			ok, err = f.repo.RejectInstanceDeleteVersion(f.ctx, f.tenant, accepted.OperationID, takeover.LeaseGeneration)
			require.NoError(t, err)
			require.False(t, ok, "an owner ACK cannot become REJECTED")
			require.Equal(t, quotaoperation.DispatchStateDispatching, opByOperationID(t, f.client, accepted.OperationID).DispatchState)
			ok, err = f.repo.AckDispatched(f.ctx, f.tenant, accepted.OperationID, takeover.LeaseGeneration, ack)
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = f.repo.RejectInstanceDeleteVersion(f.ctx, f.tenant, accepted.OperationID, takeover.LeaseGeneration)
			require.NoError(t, err)
			require.False(t, ok)
			acked := opByOperationID(t, f.client, accepted.OperationID)
			require.Equal(t, quotaoperation.DispatchStateAcked, acked.DispatchState)
			require.NotNil(t, acked.AckJSON)
			require.Equal(t, ack, *acked.AckJSON)
			require.Equal(t, input.CanonicalRequest, acked.CanonicalRequest)
			f.requireGate(t, true)
			f.requireOriginalAccounting(t)
		})
	}
}

type instanceDeletePGFixture struct {
	client   *entCrud.EntClient[*ent.Client]
	repo     *QuotaLedgerRepo
	ctx, sys context.Context
	tenant   uint32
	original *ent.QuotaOperation
	charges  []*ent.QuotaCharge
	deletion *QuotaDeleteInput
}

func newInstanceDeletePGFixture(t *testing.T, compute string) *instanceDeletePGFixture {
	t.Helper()
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	repo := newTestRepo(c)
	tenant, plan := seedTenantPlan(t, c, "instance-delete-fence-"+compute, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f := &instanceDeletePGFixture{client: c, repo: repo, tenant: tenant, ctx: ctx, sys: appViewer.NewSystemViewerContext(ctx)}
	var input *QuotaOccupyInput
	if compute == "gpu" {
		input = gpuLedgerInput(t, c.Client(), tenant, plan)
		f.deletion = gpuDelete(t, input)
	} else {
		input = occupyInput(c, tenant, "7", uuid.NewString(), 1)
		input.OwnerService, input.Action, input.Items = instanceCPUOwner, instanceCPUCreateAction, nil
		input.InstanceContractVersion = CurrentInstanceContractVersion
		input.CanonicalRequest = string(frozenCPUConfiguration(t))
		frozen, err := DecodeInstanceCPUCanonical([]byte(input.CanonicalRequest))
		require.NoError(t, err)
		frozen.DeleteExpectedVersion = 1
		raw, err := MarshalInstanceCPUCanonical(frozen)
		require.NoError(t, err)
		f.deletion = &QuotaDeleteInput{TenantID: tenant, ActorType: input.ActorType, ActorID: input.ActorID, OwnerService: instanceCPUOwner, Action: instanceCPUDeleteAction, RequestHash: "delete-version-1", CanonicalRequest: string(raw), ResourceID: input.ResourceID}
	}
	created, err := repo.Occupy(ctx, input)
	require.NoError(t, err)
	claimed := f.claim(t, created.OperationID)
	ok, err := repo.AckDispatched(ctx, tenant, created.OperationID, claimed.LeaseGeneration, f.ack(t, created.OperationID, created.ResourceID))
	require.NoError(t, err)
	require.True(t, ok)
	f.original = opByOperationID(t, c, created.OperationID)
	f.charges, err = repo.GetChargesForOperation(ctx, tenant, created.OperationID)
	require.NoError(t, err)
	if compute == "gpu" {
		require.Len(t, f.charges, 2, "GPU rejection must preserve a real mixed charge vector")
	} else {
		require.Empty(t, f.charges)
	}
	f.requireGate(t, false)
	return f
}

func (f *instanceDeletePGFixture) deleteInput() *QuotaDeleteInput {
	input := *f.deletion
	input.IdempotencyKey = uuid.NewString()
	return &input
}

func (f *instanceDeletePGFixture) claim(t *testing.T, operation string) ClaimedOperation {
	t.Helper()
	claimed, err := f.repo.ClaimDispatchable(f.ctx, "instance-delete-test", time.Minute, 8)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "rejected operations must not return to the dispatch queue")
	require.Equal(t, operation, claimed[0].OperationID)
	return claimed[0]
}

func (f *instanceDeletePGFixture) ack(t *testing.T, operation, resource string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"operation_id": operation, "resource_id": resource, "accepted": true})
	require.NoError(t, err)
	return string(raw)
}

func (f *instanceDeletePGFixture) databaseNow(t *testing.T) time.Time {
	t.Helper()
	var now time.Time
	require.NoError(t, f.client.DB().QueryRowContext(f.ctx, "SELECT CURRENT_TIMESTAMP").Scan(&now))
	return now
}

func (f *instanceDeletePGFixture) requireGate(t *testing.T, want bool) {
	t.Helper()
	blocked, err := f.repo.HasAcceptedInstanceDelete(f.ctx, f.tenant, f.original.ResourceID)
	require.NoError(t, err)
	require.Equal(t, want, blocked)
}

func (f *instanceDeletePGFixture) requireOriginalAccounting(t *testing.T) {
	t.Helper()
	original := opByOperationID(t, f.client, f.original.OperationID)
	require.Equal(t, f.original.CanonicalRequest, original.CanonicalRequest)
	require.Equal(t, f.original.RequestHash, original.RequestHash)
	require.Equal(t, f.original.AckJSON, original.AckJSON)
	require.Equal(t, f.original.DispatchState, original.DispatchState)
	require.Equal(t, f.original.AttemptCount, original.AttemptCount)
	charges, err := f.repo.GetChargesForOperation(f.ctx, f.tenant, f.original.OperationID)
	require.NoError(t, err)
	require.Len(t, charges, len(f.charges))
	for i, charge := range charges {
		require.Equal(t, f.charges[i].ChargeID, charge.ChargeID)
		require.Equal(t, f.charges[i].QuotaCode, charge.QuotaCode)
		require.Equal(t, f.charges[i].OriginalUnits, charge.OriginalUnits)
		require.Zero(t, charge.ReleasedUnits, "version rejection and transport uncertainty cannot refund")
		require.Equal(t, charge.OriginalUnits, accountOccupied(t, f.client, f.tenant, charge.QuotaCode))
	}
	require.Equal(t, len(f.charges), f.client.Client().QuotaCharge.Query().CountX(f.sys), "DELETE cannot create charges")
	require.Zero(t, f.client.Client().QuotaReleaseReceipt.Query().CountX(f.sys), "no owner cleanup fact was received")
}

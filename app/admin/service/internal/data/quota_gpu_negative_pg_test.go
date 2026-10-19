//go:build quota_pg

package data

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

func TestQuotaGpuReleaseNegativeVectorsAtomic(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	r := newTestRepo(c)
	ctx := context.Background()
	sys := appViewer.NewSystemViewerContext(ctx)
	tid, pid := seedTenantPlan(t, c, "gpu-negative-vectors", 20)
	in := gpuLedgerInput(t, c.Client(), tid, pid)
	accepted, e := r.Occupy(ctx, in)
	require.NoError(t, e)
	charges, e := r.GetChargesForOperation(ctx, tid, accepted.OperationID)
	require.NoError(t, e)
	var gpu, storage QuotaReleaseItemInput
	for _, ch := range charges {
		item := QuotaReleaseItemInput{ch.ChargeID, ch.QuotaCode, ch.OriginalUnits}
		if isGPUCode(ch.QuotaCode) {
			gpu = item
		} else {
			storage = item
		}
	}
	claimed, e := r.ClaimDispatchable(ctx, "negative-vectors", time.Minute, 1)
	require.NoError(t, e)
	require.Len(t, claimed, 1)
	del, e := r.CreateDeleteOperation(ctx, gpuDelete(t, in))
	require.NoError(t, e)
	require.False(t, del.LocalCanceled)
	otherInput := *in
	otherInput.IdempotencyKey = uuid.NewString()
	otherInput.ResourceID = uuid.NewString()
	otherInput.RequestHash = "another-original-create"
	other, e := r.Occupy(ctx, &otherInput)
	require.NoError(t, e)
	before, e := r.RecomputeInvariants(ctx, tid)
	require.NoError(t, e)
	receipts, e := c.Client().QuotaReleaseReceipt.Query().Count(sys)
	require.NoError(t, e)
	partial := gpu
	partial.ReleasedTotal--
	excess := gpu
	excess.ReleasedTotal++
	wrongID := gpu
	wrongID.ChargeID = uuid.NewString()
	wrongCode := gpu
	wrongCode.QuotaCode = storage.QuotaCode
	duplicateCode := gpu
	duplicateCode.ChargeID = storage.ChargeID
	cases := []struct {
		name, operation string
		items           []QuotaReleaseItemInput
	}{
		{"partial_gpu", accepted.OperationID, []QuotaReleaseItemInput{partial}},
		{"excess_gpu", accepted.OperationID, []QuotaReleaseItemInput{excess}},
		{"duplicate_charge", accepted.OperationID, []QuotaReleaseItemInput{gpu, gpu}},
		{"duplicate_code_wrong_id", accepted.OperationID, []QuotaReleaseItemInput{gpu, duplicateCode}},
		{"wrong_charge", accepted.OperationID, []QuotaReleaseItemInput{wrongID}},
		{"wrong_code", accepted.OperationID, []QuotaReleaseItemInput{wrongCode}},
		{"delete_is_not_original_create", del.OperationID, []QuotaReleaseItemInput{gpu}},
		{"unknown_original_create", uuid.NewString(), []QuotaReleaseItemInput{gpu}},
		{"other_known_original_create", other.OperationID, []QuotaReleaseItemInput{gpu}},
		{"valid_non_gpu_then_invalid_gpu", accepted.OperationID, []QuotaReleaseItemInput{storage, partial}},
		{"valid_gpu_then_invalid_non_gpu", accepted.OperationID, []QuotaReleaseItemInput{gpu, {storage.ChargeID, storage.QuotaCode, storage.ReleasedTotal + 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, e := r.Release(ctx, &QuotaReleaseInput{OwnerService: in.OwnerService, OperationID: tc.operation, ReleaseEventID: uuid.NewString(), Reason: "RESOURCE_RELEASED", PayloadHash: tc.name, PayloadJSON: `{"fixture":"negative-vector"}`, Items: tc.items})
			require.Error(t, e)
			after, e := r.RecomputeInvariants(ctx, tid)
			require.NoError(t, e)
			require.Equal(t, before, after)
			unchanged, e := r.GetChargesForOperation(ctx, tid, accepted.OperationID)
			require.NoError(t, e)
			// Compare all persisted fields; Ent results also carry private driver/hook state.
			beforeJSON, e := json.Marshal(charges)
			require.NoError(t, e)
			afterJSON, e := json.Marshal(unchanged)
			require.NoError(t, e)
			require.JSONEq(t, string(beforeJSON), string(afterJSON))
			count, e := c.Client().QuotaReleaseReceipt.Query().Count(sys)
			require.NoError(t, e)
			require.Equal(t, receipts, count)
		})
	}
}

func TestQuotaGpuPlanSwitchSerializesAdmissionAndDatabaseExpiry(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	r := newTestRepo(c)
	ctx := context.Background()
	sys := appViewer.NewSystemViewerContext(ctx)
	tid, pid := seedTenantPlan(t, c, "gpu-switch-locked", 20)
	in := gpuLedgerInput(t, c.Client(), tid, pid)
	first, e := r.Occupy(ctx, in)
	require.NoError(t, e)
	newPlan, e := c.Client().Plan.Create().SetNillableName(ptrStr("gpu-switch-target")).Save(sys)
	require.NoError(t, e)
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(newPlan.ID).SetQuotaCode("gpu.physical.count").SetQuotaValue(1).Exec(sys))
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(newPlan.ID).SetQuotaCode("storage.bytes").SetQuotaValue(100).Exec(sys))
	tx, e := c.DB().BeginTx(ctx, nil)
	require.NoError(t, e)
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, quotaPGSQL("switch_plan.sql"), tid, newPlan.ID)
	require.NoError(t, e)
	next := *in
	next.IdempotencyKey = uuid.NewString()
	next.ResourceID = uuid.NewString()
	next.RequestHash = "new-after-switch"
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() { close(started); _, e := r.Occupy(ctx, &next); result <- e }()
	<-started
	select {
	case e := <-result:
		t.Fatalf("admission escaped held tenant lock: %v", e)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, tx.Commit())
	select {
	case e := <-result:
		require.Error(t, e)
		require.Equal(t, "QUOTA_EXCEEDED", codeOf(t, e))
	case <-time.After(5 * time.Second):
		t.Fatal("admission did not resume")
	}
	require.Equal(t, int64(2), accountOccupied(t, c, tid, "gpu.physical.count"))
	require.Equal(t, int64(10), accountOccupied(t, c, tid, "storage.bytes"))
	replay, e := r.Occupy(ctx, in)
	require.NoError(t, e)
	require.Equal(t, first.OperationID, replay.OperationID)
	// Set expiry exactly to the database transaction's current time, not the test host clock.
	var equal bool
	require.NoError(t, c.DB().QueryRowContext(ctx, quotaPGSQL("expire_at_database_now.sql"), tid).Scan(&equal))
	require.True(t, equal)
	_, e = r.Occupy(ctx, &next)
	require.Error(t, e)
	require.Equal(t, "QUOTA_ADMISSION_DENIED", codeOf(t, e))
	require.Equal(t, int64(2), accountOccupied(t, c, tid, "gpu.physical.count"))
}

func TestQuotaGpuClaimRechecksDueAfterTenantLock(t *testing.T) {
	c := newLedgerPGClient(t)
	cleanLedger(t, c)
	r := newTestRepo(c)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tid, pid := seedTenantPlan(t, c, "gpu-claim-due-recheck", 20)
	in := gpuLedgerInput(t, c.Client(), tid, pid)
	accepted, e := r.Occupy(ctx, in)
	require.NoError(t, e)
	before, e := r.RecomputeInvariants(ctx, tid)
	require.NoError(t, e)
	tx, e := c.DB().BeginTx(ctx, nil)
	require.NoError(t, e)
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, quotaPGSQL("switch_plan.sql"), tid, pid)
	require.NoError(t, e)
	type result struct {
		claimed []ClaimedOperation
		err     error
	}
	done := make(chan result, 1)
	go func() { rows, e := r.ClaimDispatchable(ctx, "due-recheck", time.Minute, 1); done <- result{rows, e} }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		require.NoError(t, c.DB().QueryRowContext(ctx, quotaPGSQL("claim_waiting_on_tenant.sql")).Scan(&blocked))
		if blocked {
			break
		}
		require.True(t, time.Now().Before(deadline), "claim must actually wait on tenant lock after scanning candidate")
		time.Sleep(10 * time.Millisecond)
	}
	_, e = tx.ExecContext(ctx, quotaPGSQL("postpone_operation.sql"), tid, accepted.OperationID)
	require.NoError(t, e)
	require.NoError(t, tx.Commit())
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Empty(t, got.claimed)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	op, e := r.GetOperationForUser(ctx, tid, accepted.OperationID)
	require.NoError(t, e)
	require.Equal(t, "QUEUED", string(op.DispatchState))
	require.Zero(t, op.AttemptCount)
	require.Zero(t, op.LeaseGeneration)
	after, e := r.RecomputeInvariants(ctx, tid)
	require.NoError(t, e)
	require.Equal(t, before, after)
}

func TestQuotaGpuDeleteInvalidBindingAndChargesAtomic(t *testing.T) {
	c := newLedgerPGClient(t)
	r := newTestRepo(c)
	ctx := context.Background()
	sys := appViewer.NewSystemViewerContext(ctx)
	snapshot := func() string {
		operations, e := c.Client().QuotaOperation.Query().Order(ent.Asc("id")).All(sys)
		require.NoError(t, e)
		charges, e := c.Client().QuotaCharge.Query().Order(ent.Asc("id")).All(sys)
		require.NoError(t, e)
		accounts, e := c.Client().QuotaAccount.Query().Order(ent.Asc("id")).All(sys)
		require.NoError(t, e)
		acceptances, e := c.Client().GpuDeleteAcceptance.Query().Order(ent.Asc("id")).All(sys)
		require.NoError(t, e)
		receipts, e := c.Client().QuotaReleaseReceipt.Query().Order(ent.Asc("id")).All(sys)
		require.NoError(t, e)
		encoded, e := json.Marshal([]any{operations, charges, accounts, acceptances, receipts})
		require.NoError(t, e)
		return string(encoded)
	}
	for _, name := range []string{"wrong_owner", "unknown_resource", "known_resource_other_tenant", "missing_gpu_charge", "empty_original_charges", "wrong_original_units", "corrupt_frozen_vector"} {
		t.Run(name, func(t *testing.T) {
			cleanLedger(t, c)
			tid, pid := seedTenantPlan(t, c, "gpu-delete-negative-a", 20)
			other, _ := seedTenantPlan(t, c, "gpu-delete-negative-b", 20)
			in := gpuLedgerInput(t, c.Client(), tid, pid)
			accepted, e := r.Occupy(ctx, in)
			require.NoError(t, e)
			charges, e := r.GetChargesForOperation(ctx, tid, accepted.OperationID)
			require.NoError(t, e)
			input := gpuDelete(t, in)
			switch name {
			case "wrong_owner":
				input.OwnerService = "other-owner"
			case "unknown_resource":
				input.ResourceID = uuid.NewString()
			case "known_resource_other_tenant":
				input.TenantID = other
			case "missing_gpu_charge":
				for _, ch := range charges {
					if isGPUCode(ch.QuotaCode) {
						require.NoError(t, c.Client().QuotaCharge.DeleteOneID(ch.ID).Exec(sys))
					}
				}
			case "empty_original_charges":
				for _, ch := range charges {
					require.NoError(t, c.Client().QuotaCharge.DeleteOneID(ch.ID).Exec(sys))
				}
			case "wrong_original_units":
				_, e = c.DB().ExecContext(ctx, quotaPGSQL("corrupt_original_units.sql"), tid, charges[0].ChargeID)
				require.NoError(t, e)
			case "corrupt_frozen_vector":
				_, e = c.DB().ExecContext(ctx, quotaPGSQL("corrupt_frozen_vector.sql"), tid, accepted.OperationID)
				require.NoError(t, e)
			}
			// Snapshot follows deliberate fixture corruption: DELETE must not repair it,
			// create an acceptance/dispatch, or move any balance on a rejected request.
			before := snapshot()
			_, e = r.CreateDeleteOperation(ctx, input)
			require.Error(t, e)
			require.Equal(t, before, snapshot())
		})
	}
}

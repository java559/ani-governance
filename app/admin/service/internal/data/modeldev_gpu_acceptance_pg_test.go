//go:build quota_pg

package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data/ent/modeldevacceptance"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaaccount"
	"go-wind-admin/app/admin/service/internal/data/ent/quotacharge"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

func TestModelDevGPUAcceptanceOwnsOneDeliveryAndOriginalLedger(t *testing.T) {
	client := newLedgerPGClient(t)
	cleanLedger(t, client)
	tenantID, planID := seedTenantPlan(t, client, "modeldev-gpu-atomic-acceptance", 10)
	t.Cleanup(func() {
		ctx := appViewer.NewSystemViewerContext(context.Background())
		_, err := client.Client().ModelDevAcceptance.Delete().Where(modeldevacceptance.TenantIDEQ(tenantID)).Exec(ctx)
		require.NoError(t, err)
	})
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 15*time.Second)
	defer cancel()
	require.NoError(t, client.Client().PlanQuota.Create().SetPlanID(planID).SetQuotaCode(commandcontract.GpuPhysicalQuotaCode).SetQuotaValue(10).Exec(ctx))
	tenant := client.Client().Tenant.GetX(ctx, tenantID)
	scope := ModelDevAdmissionScope{TenantID: tenantID, ResourceTenantID: tenant.ResourceTenantID, Actor: "governance:user:7", Action: ModelDevCreateAction, IdempotencyKey: uuid.NewString()}
	candidate, resolved := modelDevQuotaFrozenFixture(t, scope, true)
	candidate.ResolvedGPU = resolved
	repo, ledger := NewModelDevAcceptanceRepo(client), newTestRepo(client)
	accepted, replayed, err := repo.AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.False(t, replayed)
	require.Equal(t, candidate.ExecutionID, accepted.ExecutionID)
	require.Equal(t, int64(1), accountOccupied(t, client, tenantID, commandcontract.GpuPhysicalQuotaCode))
	// The original accounting row must not become a second dispatch channel.
	claims, err := ledger.ClaimDispatchable(ctx, uuid.NewString(), time.Minute, 100)
	require.NoError(t, err)
	require.Empty(t, claims)
	op := client.Client().QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenantID), quotaoperation.OperationIDEQ(candidate.OperationID)).OnlyX(ctx)
	require.Equal(t, quotaoperation.DispatchStateQueued, op.DispatchState)
	require.Zero(t, op.AttemptCount)
	claim, err := repo.ClaimDelivery(ctx, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, accepted.OperationID, claim.Acceptance.OperationID)
	receipt := ModelDevOwnerReceipt{OperationID: accepted.OperationID, ExecutionID: accepted.ExecutionID, ExecutionSpecHash: accepted.ExecutionSpecHash, ComputeState: "ACCEPTED", DeliveryState: "PENDING", ResourceState: "NOT_APPLICABLE", CloseState: "OPEN", Revision: 1}
	ok, err := repo.AckDelivery(ctx, claim, receipt)
	require.NoError(t, err)
	require.True(t, ok)
	op = client.Client().QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenantID), quotaoperation.OperationIDEQ(candidate.OperationID)).OnlyX(ctx)
	require.Equal(t, quotaoperation.DispatchStateAcked, op.DispatchState)
	require.Equal(t, 1, op.AttemptCount)
	require.NotNil(t, op.AckJSON)
	// A new candidate observation on the original key cannot replace or charge
	// the first immutable acceptance, even if the new ACC observation is absent.
	candidate.ResolvedGPU = nil
	again, replayed, err := repo.AcceptFrozen(ctx, scope, candidate)
	require.NoError(t, err)
	require.True(t, replayed)
	require.Equal(t, accepted.ExecutionID, again.ExecutionID)
	require.Equal(t, int64(1), accountOccupied(t, client, tenantID, commandcontract.GpuPhysicalQuotaCode))
	require.Equal(t, 1, client.Client().QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenantID)).CountX(ctx))
	require.Equal(t, 1, client.Client().QuotaCharge.Query().Where(quotacharge.TenantIDEQ(tenantID)).CountX(ctx))
	require.Equal(t, 1, client.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(tenantID)).CountX(ctx))
	page, err := ledger.ScanGpuCreatePage(ctx, 0, 100)
	require.NoError(t, err)
	require.Contains(t, page, GpuCreateCandidate{ID: int64(op.ID), TenantID: tenantID, OperationID: candidate.OperationID})
	// A physical release is an external owner fact in this data test. The
	// production RPC authenticates its sole ModelDev SAN independently; amounts
	// are always read from these original persisted charges.
	payload := `{"fixture":"owner physical cleanup fact"}`
	sum := sha256.Sum256([]byte(payload))
	release := &QuotaReleaseInput{OwnerService: commandcontract.ModelDevOwner, ReleaseEventID: uuid.NewString(), OperationID: accepted.OperationID, ResourceID: accepted.ExecutionID, Complete: true, Reason: "RESOURCE_RELEASED", PayloadHash: hex.EncodeToString(sum[:]), PayloadJSON: payload}
	wrongOwner := *release
	wrongOwner.OwnerService = commandcontract.InferenceOwner
	_, err = ledger.Release(ctx, &wrongOwner)
	require.Error(t, err)
	require.Equal(t, int64(1), accountOccupied(t, client, tenantID, commandcontract.GpuPhysicalQuotaCode))
	refunded, err := ledger.Release(ctx, release)
	require.NoError(t, err)
	require.Len(t, refunded, 1)
	require.Equal(t, int64(1), refunded[0].AppliedDelta)
	require.Zero(t, accountOccupied(t, client, tenantID, commandcontract.GpuPhysicalQuotaCode))
	repeated, err := ledger.Release(ctx, release)
	require.NoError(t, err)
	require.Len(t, repeated, 1)
	require.Zero(t, repeated[0].AppliedDelta)
	_, charges, _, err := ledger.LoadGpuProjectionSource(ctx, tenantID, accepted.OperationID)
	require.NoError(t, err)
	require.Len(t, charges, 1)
	require.Equal(t, charges[0].OriginalUnits, charges[0].ReleasedUnits)
}

func TestModelDevGPUConcurrentOriginalKeyChargesOnce(t *testing.T) {
	client := newLedgerPGClient(t)
	cleanLedger(t, client)
	tenantID, planID := seedTenantPlan(t, client, "modeldev-gpu-concurrent-key", 10)
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 15*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup := appViewer.NewSystemViewerContext(context.Background())
		_, err := client.Client().ModelDevAcceptance.Delete().Where(modeldevacceptance.TenantIDEQ(tenantID)).Exec(cleanup)
		require.NoError(t, err)
	})
	require.NoError(t, client.Client().PlanQuota.Create().SetPlanID(planID).SetQuotaCode(commandcontract.GpuPhysicalQuotaCode).SetQuotaValue(10).Exec(ctx))
	tenant := client.Client().Tenant.GetX(ctx, tenantID)
	scope := ModelDevAdmissionScope{TenantID: tenantID, ResourceTenantID: tenant.ResourceTenantID, Actor: "governance:user:7", Action: ModelDevCreateAction, IdempotencyKey: uuid.NewString()}
	first, resolved := modelDevQuotaFrozenFixture(t, scope, true)
	first.ResolvedGPU = resolved
	second := first
	second.OperationID, second.ExecutionID = uuid.NewString(), uuid.NewString()
	second.Snapshot.IdentityRef.OperationID, second.Snapshot.IdentityRef.ExecutionID = second.OperationID, second.ExecutionID
	second.Snapshot.RuntimePlan.Name = cpup01.RuntimeName(second.Snapshot.Environment.NamespaceUID, second.ExecutionID)
	type outcome struct {
		accepted *ModelDevAcceptance
		replayed bool
		err      error
	}
	start, results := make(chan struct{}), make(chan outcome, 2)
	var workers sync.WaitGroup
	for _, candidate := range []ModelDevFrozenCandidate{first, second} {
		repo := NewModelDevAcceptanceRepo(newLedgerPGClient(t))
		workers.Add(1)
		go func(candidate ModelDevFrozenCandidate) {
			defer workers.Done()
			<-start
			accepted, replayed, err := repo.AcceptFrozen(ctx, scope, candidate)
			results <- outcome{accepted, replayed, err}
		}(candidate)
	}
	close(start)
	workers.Wait()
	close(results)
	var original *ModelDevAcceptance
	replayCount := 0
	for out := range results {
		require.NoError(t, out.err)
		if original == nil {
			original = out.accepted
		}
		require.Equal(t, original.OperationID, out.accepted.OperationID)
		require.Equal(t, original.ExecutionID, out.accepted.ExecutionID)
		if out.replayed {
			replayCount++
		}
	}
	require.Equal(t, 1, replayCount)
	require.Equal(t, 1, client.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(tenantID)).CountX(ctx))
	require.Equal(t, 1, client.Client().QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenantID)).CountX(ctx))
	require.Equal(t, 1, client.Client().QuotaCharge.Query().Where(quotacharge.TenantIDEQ(tenantID)).CountX(ctx))
	require.Equal(t, int64(1), accountOccupied(t, client, tenantID, commandcontract.GpuPhysicalQuotaCode))
}

func TestModelDevGPUAcceptanceRollsBackAccountingWhenAcceptanceExpires(t *testing.T) {
	client := newLedgerPGClient(t)
	cleanLedger(t, client)
	tenantID, planID := seedTenantPlan(t, client, "modeldev-gpu-expired-atomic", 10)
	ctx := appViewer.NewSystemViewerContext(context.Background())
	require.NoError(t, client.Client().PlanQuota.Create().SetPlanID(planID).SetQuotaCode(commandcontract.GpuPhysicalQuotaCode).SetQuotaValue(10).Exec(ctx))
	tenant := client.Client().Tenant.GetX(ctx, tenantID)
	scope := ModelDevAdmissionScope{TenantID: tenantID, ResourceTenantID: tenant.ResourceTenantID, Actor: "governance:user:7", Action: ModelDevCreateAction, IdempotencyKey: uuid.NewString()}
	candidate, resolved := modelDevQuotaFrozenFixture(t, scope, true)
	candidate.AcceptedAt = candidate.AcceptedAt.Add(-24 * time.Hour)
	candidate.Snapshot.Timing.AcceptedAt = candidate.AcceptedAt
	candidate.Snapshot.Timing.ExecutionDeadlineAt = candidate.AcceptedAt.Add(time.Duration(candidate.Snapshot.Timing.ExecutionTimeoutSeconds) * time.Second)
	candidate.Snapshot.DeadlineAt = candidate.Snapshot.Timing.ExecutionDeadlineAt
	candidate.ResolvedGPU = resolved
	_, _, err := NewModelDevAcceptanceRepo(client).AcceptFrozen(ctx, scope, candidate)
	require.ErrorIs(t, err, ErrModelDevAdmissionExpired)
	require.Zero(t, client.Client().ModelDevAcceptance.Query().Where(modeldevacceptance.TenantIDEQ(tenantID)).CountX(ctx))
	require.Zero(t, client.Client().QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenantID)).CountX(ctx))
	require.Zero(t, client.Client().QuotaCharge.Query().Where(quotacharge.TenantIDEQ(tenantID)).CountX(ctx))
	require.Zero(t, client.Client().QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(tenantID)).CountX(ctx))
}

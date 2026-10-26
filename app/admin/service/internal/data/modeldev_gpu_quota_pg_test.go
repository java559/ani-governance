//go:build quota_pg

package data

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	codec "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaaccount"
	"go-wind-admin/app/admin/service/internal/data/ent/quotacharge"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

func modelDevQuotaFrozenFixture(t *testing.T, scope ModelDevAdmissionScope, gpu bool) (ModelDevFrozenCandidate, *acc.ResolvedGpuRequest) {
	t.Helper()
	s := conformance.SnapshotV1()
	intent := conformance.IntentV1()
	intent.ComputePresetID, intent.DatasetVersionID = s.ComputeOrigin.ComputePresetID, s.Input.InputVersionID
	_, hash, err := cpup01.CanonicalIntent(intent)
	require.NoError(t, err)
	s.IdentityRef.TenantID, s.IdentityRef.Actor, s.IdentityRef.IntentHash = scope.ResourceTenantID, scope.Actor, hash
	accepted := time.Now().UTC().Truncate(time.Microsecond)
	s.Timing.AcceptedAt, s.Timing.ExecutionDeadlineAt = accepted, accepted.Add(time.Duration(s.Timing.ExecutionTimeoutSeconds)*time.Second)
	s.DeadlineAt = s.Timing.ExecutionDeadlineAt
	candidate := ModelDevFrozenCandidate{OperationID: s.IdentityRef.OperationID, ExecutionID: s.IdentityRef.ExecutionID, Intent: intent, Snapshot: s, AcceptedAt: accepted}
	if !gpu {
		return candidate, nil
	}
	selection := &compute.ComputeSelection{SchemaVersion: 1, PresetId: s.ComputeOrigin.ComputePresetID, Code: "gpu-t4", Name: "T4", ComputeType: "gpu", CpuMillis: s.ComputeOrigin.CPUMillis, MemoryMib: s.ComputeOrigin.MemoryMiB, Gpu: &compute.GpuSelection{ClusterId: s.Environment.ClusterID, PoolId: "13131313-1313-4313-8313-131313131313", ProfileId: "12121212-1212-4212-8212-121212121212", ProfileVersion: 3, SpecDigest: strings.Repeat("a", 64), DevicesPerUnit: 1, Mode: acc.SupplyMode_WHOLE_EXCLUSIVE, ModelKey: "nvidia-t4", MemoryMibPerDevice: 2048, CoreLimitPercent: 100}}
	selection.SelectionDigest, err = compute.SelectionDigest(selection)
	require.NoError(t, err)
	raw, err := compute.CanonicalSelection(selection)
	require.NoError(t, err)
	candidate.Snapshot.ComputeOrigin, err = codec.DecodeComputeSelection(raw, selection.SelectionDigest)
	require.NoError(t, err)
	e := &acc.GpuExecution{SchedulerName: "volcano", QueueName: "gpu-task", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": "14141414-1414-4414-8414-141414141414", "accelerator.ani.io/model-key": "nvidia-t4", "accelerator.ani.io/baseline-id": "15151515-1515-4515-8515-151515151515"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "2048", "volcano.sh/vgpu-cores": "100"}}
	e.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(e)
	require.NoError(t, err)
	d := &acc.GpuDisplay{ProfileId: selection.Gpu.ProfileId, ProfileVersion: selection.Gpu.ProfileVersion, ProfileName: "T4 fixture", ModelKey: "nvidia-t4", Mode: acc.SupplyMode_WHOLE_EXCLUSIVE}
	er, dr, err := codec.EncodeGPUPlanCanonical(e, d)
	require.NoError(t, err)
	candidate.Snapshot.GPU, err = codec.DecodeGPUPlanCanonical(er, dr)
	require.NoError(t, err)
	return candidate, &acc.ResolvedGpuRequest{Execution: e, Selection: d, QuotaItems: []*acc.GpuQuotaItem{{QuotaCode: commandcontract.GpuPhysicalQuotaCode, Units: 1}}}
}

func TestModelDevGPUCPUBypassesLedgerWithoutZeroOperation(t *testing.T) {
	client := newLedgerPGClient(t)
	cleanLedger(t, client)
	tenantID, _ := seedTenantPlan(t, client, "modeldev-cpu-no-ledger", 10)
	ctx := appViewer.NewSystemViewerContext(context.Background())
	tenant := client.Client().Tenant.GetX(ctx, tenantID)
	scope := ModelDevAdmissionScope{TenantID: tenantID, ResourceTenantID: tenant.ResourceTenantID, Actor: "governance:user:7", Action: ModelDevCreateAction, IdempotencyKey: uuid.NewString()}
	candidate, _ := modelDevQuotaFrozenFixture(t, scope, false)
	input, err := ModelDevGPUQuotaInput(scope, candidate, nil)
	require.NoError(t, err)
	require.Nil(t, input)
	// No transaction or even repository is needed for the CPU bypass.
	var repository *QuotaLedgerRepo
	out, err := repository.OccupyModelDevGPUInTx(ctx, nil, scope, candidate, nil)
	require.NoError(t, err)
	require.Nil(t, out)
	count, err := client.Client().QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	count, err = client.Client().QuotaCharge.Query().Where(quotacharge.TenantIDEQ(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	count, err = client.Client().QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestModelDevGPUQuotaSharesCallerRollbackAndStableOriginalKey(t *testing.T) {
	client := newLedgerPGClient(t)
	cleanLedger(t, client)
	tenantID, planID := seedTenantPlan(t, client, "modeldev-gpu-original-ledger", 10)
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 15*time.Second)
	defer cancel()
	require.NoError(t, client.Client().PlanQuota.Create().SetPlanID(planID).SetQuotaCode(commandcontract.GpuPhysicalQuotaCode).SetQuotaValue(10).Exec(ctx))
	tenant := client.Client().Tenant.GetX(ctx, tenantID)
	scope := ModelDevAdmissionScope{TenantID: tenantID, ResourceTenantID: tenant.ResourceTenantID, Actor: "governance:user:7", Action: ModelDevCreateAction, IdempotencyKey: uuid.NewString()}
	candidate, resolved := modelDevQuotaFrozenFixture(t, scope, true)
	input, err := ModelDevGPUQuotaInput(scope, candidate, resolved)
	require.NoError(t, err)
	require.Equal(t, candidate.OperationID, input.OperationID)
	require.Equal(t, candidate.ExecutionID, input.ResourceID)
	require.Equal(t, "user", input.ActorType)
	require.Equal(t, "7", input.ActorID)
	require.Equal(t, commandcontract.ModelDevOwner, input.OwnerService)
	require.Equal(t, []QuotaOccupyItem{{QuotaCode: resolved.QuotaItems[0].QuotaCode, Units: resolved.QuotaItems[0].Units}}, input.Items)
	repository := newTestRepo(client)
	abort := errors.New("caller aborts entire ModelDev acceptance")
	err = repository.transaction(ctx, func(tx *ent.Tx) error {
		out, err := repository.OccupyModelDevGPUInTx(ctx, tx, scope, candidate, resolved)
		if err != nil {
			return err
		}
		require.Equal(t, candidate.OperationID, out.OperationID)
		require.False(t, out.Replayed)
		account, err := tx.QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(tenantID), quotaaccount.QuotaCodeEQ(resolved.QuotaItems[0].QuotaCode)).Only(ctx)
		require.NoError(t, err)
		require.Equal(t, resolved.QuotaItems[0].Units, account.OccupiedUnits)
		return abort
	})
	require.ErrorIs(t, err, abort)
	// A separate read after rollback sees none of the original three row sets.
	count, err := client.Client().QuotaAccount.Query().Where(quotaaccount.TenantIDEQ(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	count, err = client.Client().QuotaCharge.Query().Where(quotacharge.TenantIDEQ(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	count, err = client.Client().QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
	var first, replay *QuotaOccupyResult
	require.NoError(t, repository.transaction(ctx, func(tx *ent.Tx) error {
		var err error
		first, err = repository.OccupyModelDevGPUInTx(ctx, tx, scope, candidate, resolved)
		return err
	}))
	require.NoError(t, repository.transaction(ctx, func(tx *ent.Tx) error {
		var err error
		replay, err = repository.OccupyModelDevGPUInTx(ctx, tx, scope, candidate, resolved)
		return err
	}))
	require.True(t, replay.Replayed)
	require.Equal(t, first.OperationID, replay.OperationID)
	require.Equal(t, first.ResourceID, replay.ResourceID)
	require.Equal(t, first.ChargeIDs, replay.ChargeIDs)
	require.Equal(t, int64(1), accountOccupied(t, client, tenantID, commandcontract.GpuPhysicalQuotaCode))
	operation, err := client.Client().QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenantID), quotaoperation.OperationIDEQ(candidate.OperationID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, input.CanonicalRequest, operation.CanonicalRequest)
	require.Equal(t, input.RequestHash, operation.RequestHash)
	changed := candidate
	changed.ExecutionID = uuid.NewString()
	changed.Snapshot.IdentityRef.ExecutionID = changed.ExecutionID
	changed.Snapshot.RuntimePlan.Name = cpup01.RuntimeName(changed.Snapshot.Environment.NamespaceUID, changed.ExecutionID)
	require.Error(t, repository.transaction(ctx, func(tx *ent.Tx) error {
		_, err := repository.OccupyModelDevGPUInTx(ctx, tx, scope, changed, resolved)
		return err
	}))
	count, err = client.Client().QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = client.Client().QuotaCharge.Query().Where(quotacharge.TenantIDEQ(tenantID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

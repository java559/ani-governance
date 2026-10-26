package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	codec "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
)

func TestModelDevGpuUsageDerivesOnlyOriginalLedgerAndFullRelease(t *testing.T) {
	vector, _, _ := gpuVector(t)
	snapshot, intent := conformance.SnapshotV1(), conformance.IntentV1()
	intent.ComputePresetID, intent.DatasetVersionID = snapshot.ComputeOrigin.ComputePresetID, snapshot.Input.InputVersionID
	_, intentHash, err := cpup01.CanonicalIntent(intent)
	require.NoError(t, err)
	snapshot.IdentityRef.Actor, snapshot.IdentityRef.IntentHash = "governance:user:7", intentHash
	vector.Request.ClusterId, vector.Request.ContainerName = snapshot.Environment.ClusterID, "node"
	selection := &compute.ComputeSelection{SchemaVersion: 1, PresetId: intent.ComputePresetID, Code: "gpu-shared", Name: "T4 shared", ComputeType: "gpu", CpuMillis: snapshot.ComputeOrigin.CPUMillis, MemoryMib: snapshot.ComputeOrigin.MemoryMiB, Gpu: &compute.GpuSelection{ClusterId: vector.Request.ClusterId, PoolId: vector.Request.PoolId, ProfileId: vector.Request.ProfileId, ProfileVersion: vector.Request.ProfileVersion, SpecDigest: vector.Profile.SpecDigest, DevicesPerUnit: 1, Mode: vector.Profile.Spec.Mode, ModelKey: vector.Profile.Spec.ModelKey, MemoryMibPerDevice: vector.Profile.Spec.MemoryMibPerDevice, CoreLimitPercent: vector.Profile.Spec.CoreLimitPercent}}
	selection.SelectionDigest, err = compute.SelectionDigest(selection)
	require.NoError(t, err)
	raw, err := compute.CanonicalSelection(selection)
	require.NoError(t, err)
	snapshot.ComputeOrigin, err = codec.DecodeComputeSelection(raw, selection.SelectionDigest)
	require.NoError(t, err)
	execution, display, err := codec.EncodeGPUPlanCanonical(vector.Resolved.Execution, vector.Resolved.Selection)
	require.NoError(t, err)
	snapshot.GPU, err = codec.DecodeGPUPlanCanonical(execution, display)
	require.NoError(t, err)
	specHash, err := snapshot.Digest()
	require.NoError(t, err)
	id := snapshot.IdentityRef
	frozen, err := commandcontract.NewModelDevGPUCanonical(cpup01.AdmissionEnvelope{TenantID: id.TenantID, Actor: id.Actor, OperationID: id.OperationID, ExecutionID: id.ExecutionID, Intent: intent, IntentHash: intentHash, Snapshot: snapshot, SpecHash: specHash, AcceptedAt: snapshot.Timing.AcceptedAt}, vector.Resolved)
	require.NoError(t, err)
	raw, err = frozen.Canonical()
	require.NoError(t, err)
	tenant := uint32(7)
	created := snapshot.Timing.AcceptedAt
	op := &ent.QuotaOperation{TenantID: &tenant, CreatedAt: &created, OperationID: id.OperationID, ResourceID: id.ExecutionID, ResourceTenantID: id.TenantID, OwnerService: commandcontract.ModelDevOwner, Action: commandcontract.ModelDevCreateAction, ActorType: "user", ActorID: "7", CanonicalRequest: string(raw), DispatchState: quotaoperation.DispatchStateAcked, AttemptCount: 1}
	charges := []*ent.QuotaCharge{{TenantID: &tenant, OperationID: id.OperationID, ChargeID: "original-charge", QuotaCode: commandcontract.GpuSharedQuotaCode, OriginalUnits: 8192}}
	declared, err := deriveGpuProjection(op, charges, false)
	require.NoError(t, err)
	require.Equal(t, acc.UsageState_DECLARED, declared.State)
	require.Equal(t, commandcontract.ModelDevOwner, declared.Ref.OwnerService)
	require.Equal(t, id.ExecutionID, declared.Ref.ResourceId)
	require.Equal(t, id.OperationID, declared.Ref.CreateOperationId)
	require.Equal(t, "node", declared.ContainerName)
	require.Equal(t, vector.Resolved.Execution.ExecutionDigest, declared.ExecutionDigest)
	charges[0].ReleasedUnits = 8191
	partial, err := deriveGpuProjection(op, charges, true)
	require.NoError(t, err)
	require.Equal(t, acc.UsageState_DECLARED, partial.State)
	charges[0].ReleasedUnits = 8192
	ended, err := deriveGpuProjection(op, charges, true)
	require.NoError(t, err)
	require.Equal(t, uint64(2), ended.Revision)
	require.Equal(t, acc.UsageState_ENDED, ended.State)
	require.Equal(t, acc.UsageEndReason_OWNER_RESOURCE_RELEASED, ended.EndReason)
	require.Equal(t, declared.SourceOperationCreatedAt, ended.SourceOperationCreatedAt)
	charges[0].QuotaCode = commandcontract.GpuPhysicalQuotaCode
	_, err = deriveGpuProjection(op, charges, true)
	require.Error(t, err, "different original charge vector cannot produce an ENDED projection")
}

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/pkg/middleware/auth"
)

func TestInferenceStoredCommandVariantsAndDeleteScope(t *testing.T) {
	for _, gpu := range []bool{false, true} {
		t.Run(map[bool]string{false: "CPU", true: "GPU"}[gpu], func(t *testing.T) {
			intent := inferencePublicIntent()
			selection := inferenceCPUSelection(t)
			var plan *acc.ResolvedGpuRequest
			if gpu {
				fixture, _, _ := gpuVector(t)
				fixture.Request.ContainerName = "kserve-container"
				selection.ComputeType = "gpu"
				selection.Gpu = &compute.GpuSelection{ProfileId: fixture.Request.ProfileId, ProfileVersion: fixture.Request.ProfileVersion, SpecDigest: fixture.Profile.SpecDigest, DevicesPerUnit: 1, ClusterId: fixture.Request.ClusterId, PoolId: fixture.Request.PoolId, Mode: fixture.Resolved.Selection.Mode, ModelKey: fixture.Resolved.Selection.ModelKey, MemoryMibPerDevice: 8192, CoreLimitPercent: 25}
				var e error
				selection.SelectionDigest, e = compute.SelectionDigest(selection)
				require.NoError(t, e)
				plan = fixture.Resolved
			}
			b := NewInferenceComputeBinding(nil, nil, &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Version: &modelv1.ModelVersion{Id: intent.ModelVersionId, Status: "ready"}}}, &inferenceComputeProbe{selection: selection})
			tenant := "11111111-1111-4111-8111-111111111111"
			createID := "20000000-0000-4000-8000-000000000001"
			resourceID := "20000000-0000-4000-8000-000000000002"
			prepared, e := b.PrepareManagedBusiness(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42}, tenant, intent)
			require.NoError(t, e)
			raw, e := prepared.freeze(tenant, "user", "7", createID, resourceID, plan)
			require.NoError(t, e)
			in := commandcontract.StoredCommand{Owner: commandcontract.InferenceOwner, Action: InferenceCreateAction, TenantID: tenant, ResourceID: resourceID, OperationID: createID, ActorType: "user", ActorID: "7", Canonical: raw, RequireChargeIDs: true}
			if gpu {
				in.Charges = []commandcontract.ChargeRef{{ChargeID: "original", QuotaCode: GpuSharedQuotaCode, ChargedUnits: 8192}}
			}
			facts, e := commandcontract.ValidateStoredCommand(in)
			require.NoError(t, e)
			require.Equal(t, !gpu, facts.CPU)
			wrong := in
			wrong.ActorID = "8"
			_, e = commandcontract.ValidateStoredCommand(wrong)
			require.Error(t, e)
			wrong = in
			wrong.Owner = InstanceOwner
			wrong.InstanceContractVersion = 2
			_, e = commandcontract.ValidateStoredCommand(wrong)
			require.Error(t, e)
			wrong = in
			wrong.TenantID = "11111111-1111-4111-8111-111111111112"
			_, e = commandcontract.ValidateStoredCommand(wrong)
			require.Error(t, e)
			wrong = in
			if gpu {
				wrong.Charges = nil
			} else {
				wrong.Charges = []commandcontract.ChargeRef{{ChargeID: "fake", QuotaCode: GpuSharedQuotaCode, ChargedUnits: 8192}}
			}
			_, e = commandcontract.ValidateStoredCommand(wrong)
			require.Error(t, e)
			c, e := commandcontract.DecodeInferenceCanonical(raw)
			require.NoError(t, e)
			c.DeleteScope = &runtimev1.ManagedDeleteScope{ResourceClass: c.ResourceClass, ClusterId: c.ClusterID, CreateCommandDigest: c.CreateCommandDigest}
			deleted, e := commandcontract.CanonicalJSON(c)
			require.NoError(t, e)
			_, e = commandcontract.DeleteSnapshot(commandcontract.InferenceOwner, string(raw), string(deleted))
			require.NoError(t, e)
			in.Action = InferenceDeleteAction
			in.CreateOperationID = createID
			in.OperationID = "20000000-0000-4000-8000-000000000003"
			in.ActorID = "8"
			in.Canonical = deleted
			_, e = commandcontract.ValidateStoredCommand(in)
			require.NoError(t, e, "a newly authorized actor may close the original resource")
			c.DeleteScope.CreateCommandDigest = "forged"
			mutated, e := commandcontract.CanonicalJSON(c)
			require.NoError(t, e)
			_, e = commandcontract.DeleteSnapshot(commandcontract.InferenceOwner, string(raw), string(mutated))
			require.Error(t, e)
			c.DeleteScope.CreateCommandDigest = c.CreateCommandDigest
			business, e := runtimev1.DecodeBusinessPayload(c.BusinessPayload)
			require.NoError(t, e)
			business.Engine.Command = "changed"
			c.BusinessPayload, e = runtimev1.CanonicalBusinessPayload(business)
			require.NoError(t, e)
			sum := sha256.Sum256(c.BusinessPayload)
			c.BusinessPayloadDigest = hex.EncodeToString(sum[:])
			mutated, e = commandcontract.CanonicalJSON(c)
			require.NoError(t, e)
			_, e = commandcontract.DeleteSnapshot(commandcontract.InferenceOwner, string(raw), string(mutated))
			require.Error(t, e, "DELETE cannot rewrite frozen business")
		})
	}
}

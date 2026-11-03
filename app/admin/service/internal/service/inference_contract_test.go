package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	contractv1 "github.com/liangzai006/ani-inference-service/api/inference/contract/v1"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	protocol "github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/pkg/middleware/auth"
)

func TestInferenceStoredGPUBlockMemory(t *testing.T) {
	for _, tc := range []struct {
		name, blocks, factor string
		invalid              bool
	}{
		{name: "legacyMiB", blocks: "24570"},
		{name: "tenMiBBlocks", blocks: "2457", factor: "10"},
		{name: "zeroFactor", blocks: "2457", factor: "0", invalid: true},
		{name: "negativeFactor", blocks: "2457", factor: "-10", invalid: true},
		{name: "nonnumericFactor", blocks: "2457", factor: "bad", invalid: true},
		{name: "overflowFactor", blocks: "2457", factor: "9223372036854775808", invalid: true},
		{name: "overflowProduct", blocks: "214748365", factor: "10", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, _, _ := gpuVector(t)
			fixture.Request.ContainerName = "kserve-container"
			execution := fixture.Resolved.Execution
			execution.Resources["volcano.sh/vgpu-memory"] = tc.blocks
			execution.Resources["volcano.sh/vgpu-cores"] = "50"
			if tc.factor != "" {
				execution.PodAnnotations["accelerator.ani.io/memory-block-mib"] = tc.factor
			}
			var err error
			execution.ExecutionDigest, err = protocol.GpuExecutionDigest(execution)
			require.NoError(t, err)
			fixture.Resolved.QuotaItems[0].Units = 24570
			selection := inferenceCPUSelection(t)
			selection.ComputeType = "gpu"
			selection.Gpu = &compute.GpuSelection{ProfileId: fixture.Request.ProfileId, ProfileVersion: fixture.Request.ProfileVersion, SpecDigest: fixture.Profile.SpecDigest, DevicesPerUnit: 1, ClusterId: fixture.Request.ClusterId, PoolId: fixture.Request.PoolId, Mode: fixture.Resolved.Selection.Mode, ModelKey: fixture.Resolved.Selection.ModelKey, MemoryMibPerDevice: 24570, CoreLimitPercent: 50}
			selection.SelectionDigest, err = compute.SelectionDigest(selection)
			require.NoError(t, err)
			intent := inferencePublicIntent()
			binding := NewInferenceComputeBinding(nil, nil, &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Version: &modelv1.ModelVersion{Id: intent.ModelVersionId, Status: "ready"}}}, &inferenceComputeProbe{selection: selection})
			tenant, createID, resourceID := "11111111-1111-4111-8111-111111111111", "20000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000002"
			prepared, err := binding.PrepareManagedBusiness(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 42}, tenant, intent)
			require.NoError(t, err)
			raw, err := prepared.freeze(tenant, "user", "7", createID, resourceID, fixture.Resolved)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			canonical, err := commandcontract.DecodeInferenceCanonical(raw)
			require.NoError(t, err)
			encoded, err := commandcontract.CanonicalJSON(canonical)
			require.NoError(t, err)
			require.Equal(t, raw, encoded, "reading a frozen execution must preserve canonical bytes")
			sum := sha256.Sum256(raw)
			if tc.name == "legacyMiB" {
				require.Equal(t, "d7422dfb83b16155c193d3523232c61a7dc85e32e7adb8bc700b4dbc4a5a4d82", hex.EncodeToString(sum[:]), "pre-fix legacy canonical golden must not change")
			}
			t.Logf("canonical SHA256 %s", hex.EncodeToString(sum[:]))
			stored := commandcontract.StoredCommand{Owner: commandcontract.InferenceOwner, Action: InferenceCreateAction, TenantID: tenant, ResourceID: resourceID, OperationID: createID, ActorType: "user", ActorID: "7", Canonical: raw, RequireChargeIDs: true, Charges: []commandcontract.ChargeRef{{ChargeID: "original", QuotaCode: GpuSharedQuotaCode, ChargedUnits: 24570}}}
			facts, err := commandcontract.ValidateStoredCommand(stored)
			require.NoError(t, err)
			business, err := contractv1.DecodeBusinessPayload(facts.BusinessPayload)
			require.NoError(t, err)
			require.Equal(t, tc.blocks, business.GpuExecution.Resources["volcano.sh/vgpu-memory"])
			require.Equal(t, tc.factor, business.GpuExecution.PodAnnotations["accelerator.ani.io/memory-block-mib"])
		})
	}
}

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
			business, e := contractv1.DecodeBusinessPayload(c.BusinessPayload)
			require.NoError(t, e)
			business.Engine.Command = "changed"
			c.BusinessPayload, e = contractv1.CanonicalBusinessPayload(business)
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

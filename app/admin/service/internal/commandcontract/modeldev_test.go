package commandcontract

import (
	"bytes"
	"strings"
	"testing"

	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01"
	"github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/conformance"
	codec "github.com/zhangzhe-ctrl/ani-modeldev-service/contract/cpup01/protobuf"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	"google.golang.org/protobuf/proto"
)

// Synthetic provider facts exercise only the pure frozen contract. They do not
// stand in for a runtime compiler, an ACC process or GPU environment acceptance.
func modelDevGPUEnvelope(t *testing.T, mode acc.SupplyMode) (cpup01.AdmissionEnvelope, *acc.ResolvedGpuRequest) {
	t.Helper()
	s := conformance.SnapshotV1()
	intent := conformance.IntentV1()
	intent.ComputePresetID, intent.DatasetVersionID = s.ComputeOrigin.ComputePresetID, s.Input.InputVersionID
	_, hash, err := cpup01.CanonicalIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	s.IdentityRef.Actor, s.IdentityRef.IntentHash = "governance:user:42", hash
	core, code, units := uint32(30), GpuSharedQuotaCode, int64(2048)
	if mode == acc.SupplyMode_WHOLE_EXCLUSIVE {
		core, code, units = 100, GpuPhysicalQuotaCode, 1
	}
	selection := &compute.ComputeSelection{SchemaVersion: 1, PresetId: s.ComputeOrigin.ComputePresetID, Code: "gpu-t4", Name: "T4", ComputeType: "gpu", CpuMillis: s.ComputeOrigin.CPUMillis, MemoryMib: s.ComputeOrigin.MemoryMiB, Gpu: &compute.GpuSelection{ClusterId: s.Environment.ClusterID, PoolId: "13131313-1313-4313-8313-131313131313", ProfileId: "12121212-1212-4212-8212-121212121212", ProfileVersion: 3, SpecDigest: strings.Repeat("a", 64), DevicesPerUnit: 1, Mode: mode, ModelKey: "nvidia-t4", MemoryMibPerDevice: 2048, CoreLimitPercent: core}}
	selection.SelectionDigest, err = compute.SelectionDigest(selection)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := compute.CanonicalSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	s.ComputeOrigin, err = codec.DecodeComputeSelection(raw, selection.SelectionDigest)
	if err != nil {
		t.Fatal(err)
	}
	e := &acc.GpuExecution{SchedulerName: "volcano", QueueName: "gpu-task", RuntimeClassName: "nvidia", NodeSelector: map[string]string{"accelerator.ani.io/supply-group": "14141414-1414-4414-8414-141414141414", "accelerator.ani.io/model-key": "nvidia-t4", "accelerator.ani.io/baseline-id": "15151515-1515-4515-8515-151515151515"}, PodAnnotations: map[string]string{"volcano.sh/vgpu-mode": "hami-core"}, Resources: map[string]string{"volcano.sh/vgpu-number": "1", "volcano.sh/vgpu-memory": "2048", "volcano.sh/vgpu-cores": "30"}}
	if core == 100 {
		e.Resources["volcano.sh/vgpu-cores"] = "100"
	}
	e.ExecutionDigest, err = gpuprotocol.GpuExecutionDigest(e)
	if err != nil {
		t.Fatal(err)
	}
	d := &acc.GpuDisplay{ProfileId: selection.Gpu.ProfileId, ProfileVersion: selection.Gpu.ProfileVersion, ProfileName: "T4 fixture", ModelKey: "nvidia-t4", Mode: mode}
	er, dr, err := codec.EncodeGPUPlanCanonical(e, d)
	if err != nil {
		t.Fatal(err)
	}
	s.GPU, err = codec.DecodeGPUPlanCanonical(er, dr)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := s.Digest()
	if err != nil {
		t.Fatal(err)
	}
	id := s.IdentityRef
	return cpup01.AdmissionEnvelope{TenantID: id.TenantID, Actor: id.Actor, OperationID: id.OperationID, ExecutionID: id.ExecutionID, Intent: intent, IntentHash: hash, Snapshot: s, SpecHash: spec, AcceptedAt: s.Timing.AcceptedAt}, &acc.ResolvedGpuRequest{Execution: e, Selection: d, QuotaItems: []*acc.GpuQuotaItem{{QuotaCode: code, Units: units}}}
}

func TestModelDevGPUCanonicalPreservesSameFormalWholeOrSharedVector(t *testing.T) {
	for _, mode := range []acc.SupplyMode{acc.SupplyMode_WHOLE_EXCLUSIVE, acc.SupplyMode_SHARED_FIXED} {
		t.Run(mode.String(), func(t *testing.T) {
			envelope, resolved := modelDevGPUEnvelope(t, mode)
			frozen, err := NewModelDevGPUCanonical(envelope, resolved)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := frozen.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := DecodeModelDevGPUCanonical(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(restored.Resolved, resolved) || len(restored.QuotaItems) != 1 || restored.QuotaItems[0].QuotaCode != resolved.QuotaItems[0].QuotaCode || restored.QuotaItems[0].Units != resolved.QuotaItems[0].Units {
				t.Fatal("original ACC meter or execution was replaced")
			}
			originalSnapshot, _ := envelope.Snapshot.Canonical()
			if !bytes.Equal(originalSnapshot, restored.SnapshotCanonical) {
				t.Fatal("frozen owner snapshot changed")
			}
			hash, err := ModelDevGPUCanonicalHash(raw)
			if err != nil || len(hash) != 64 {
				t.Fatal("missing immutable ledger digest", err)
			}
			resolved.QuotaItems[0].Units++
			resolved.Execution.QueueName = "mutated-provider-buffer"
			after, err := frozen.Canonical()
			if err != nil || !bytes.Equal(raw, after) {
				t.Fatal("provider buffer changed original ledger canonical", err)
			}
		})
	}
}

func TestModelDevGPUStoredCommandBindsOriginalIdentityActorAndAllCharges(t *testing.T) {
	envelope, resolved := modelDevGPUEnvelope(t, acc.SupplyMode_SHARED_FIXED)
	c, err := NewModelDevGPUCanonical(envelope, resolved)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	base := StoredCommand{Owner: ModelDevOwner, Action: ModelDevCreateAction, TenantID: envelope.TenantID, ResourceID: envelope.ExecutionID, OperationID: envelope.OperationID, ActorType: "user", ActorID: "42", Canonical: raw, RequireChargeIDs: true, Charges: []ChargeRef{{ChargeID: "original-charge", QuotaCode: GpuSharedQuotaCode, ChargedUnits: 2048}}}
	if !ManagedOwner(base.Owner) || Usage(base.Owner, base.Action) != "TRAINING" {
		t.Fatal("original ModelDev operation is not registered as training")
	}
	facts, err := ValidateStoredCommand(base)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(facts.BusinessPayload, c.SnapshotCanonical) || !proto.Equal(facts.Execution, resolved.Execution) || !proto.Equal(facts.Display, resolved.Selection) || len(facts.QuotaItems) != 1 || facts.QuotaItems[0].QuotaCode != resolved.QuotaItems[0].QuotaCode || facts.QuotaItems[0].Units != resolved.QuotaItems[0].Units {
		t.Fatal("registered validation changed the original owner payload or ACC facts")
	}
	for _, owner := range []string{"", "ani-modeldev", "ani-modeldev-service-extra"} {
		if ManagedOwner(owner) || Usage(owner, ModelDevCreateAction) != "" {
			t.Fatal("unregistered ModelDev owner alias accepted")
		}
		foreign := base
		foreign.Owner = owner
		if _, err := ValidateStoredCommand(foreign); err == nil {
			t.Fatal("unregistered owner reached managed command validation")
		}
	}
	for _, mutate := range []func(*StoredCommand){
		func(v *StoredCommand) { v.Owner = InstanceOwner }, func(v *StoredCommand) { v.Action = InstanceCreateAction }, func(v *StoredCommand) { v.CreateOperationID = envelope.OperationID },
		func(v *StoredCommand) { v.TenantID = "11111111-1111-4111-8111-111111111111" }, func(v *StoredCommand) { v.ResourceID = envelope.OperationID }, func(v *StoredCommand) { v.OperationID = envelope.ExecutionID },
		func(v *StoredCommand) { v.ActorType = "access_key" }, func(v *StoredCommand) { v.ActorID = "042" }, func(v *StoredCommand) { v.Charges = nil },
		func(v *StoredCommand) {
			v.Charges = []ChargeRef{{ChargeID: "original-charge", QuotaCode: GpuSharedQuotaCode, ChargedUnits: 1}}
		},
		func(v *StoredCommand) {
			v.Charges = []ChargeRef{{ChargeID: "original-charge", QuotaCode: GpuPhysicalQuotaCode, ChargedUnits: 2048}}
		},
		func(v *StoredCommand) { v.Charges = []ChargeRef{{QuotaCode: GpuSharedQuotaCode, ChargedUnits: 2048}} },
	} {
		v := base
		mutate(&v)
		if _, err := ValidateStoredCommand(v); err == nil {
			t.Fatal("foreign ModelDev ledger identity/vector accepted")
		}
	}
}

func TestModelDevGPUCanonicalRejectsChangedFormalFactsAndUnmeteredCPU(t *testing.T) {
	for _, mutate := range []func(*ModelDevGPUCanonical){
		func(v *ModelDevGPUCanonical) { v.Usage = "INFERENCE" }, func(v *ModelDevGPUCanonical) { v.ExecutionSpecHash = strings.Repeat("f", 64) },
		func(v *ModelDevGPUCanonical) { v.GpuRequest.ContainerName = "workspace" }, func(v *ModelDevGPUCanonical) { v.GpuRequest.Replicas = 2 },
		func(v *ModelDevGPUCanonical) { v.QuotaItems[0].Units++ }, func(v *ModelDevGPUCanonical) { v.Resolved.QuotaItems[0].Units++ },
		func(v *ModelDevGPUCanonical) {
			v.Resolved.QuotaItems = append(v.Resolved.QuotaItems, &acc.GpuQuotaItem{QuotaCode: "cpu.count", Units: 1})
		},
		func(v *ModelDevGPUCanonical) { v.Resolved.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01}) },
		func(v *ModelDevGPUCanonical) { v.Resolved.Selection.ProfileName = "different original display" },
		func(v *ModelDevGPUCanonical) {
			v.Resolved.Execution.QueueName = "other-queue"
			v.Resolved.Execution.ExecutionDigest, _ = gpuprotocol.GpuExecutionDigest(v.Resolved.Execution)
		},
	} {
		envelope, resolved := modelDevGPUEnvelope(t, acc.SupplyMode_SHARED_FIXED)
		v, err := NewModelDevGPUCanonical(envelope, resolved)
		if err != nil {
			t.Fatal(err)
		}
		mutate(v)
		if _, err := v.Canonical(); err == nil {
			t.Fatal("changed original formal facts accepted")
		}
	}
	envelope, _ := modelDevGPUEnvelope(t, acc.SupplyMode_WHOLE_EXCLUSIVE)
	if _, err := NewModelDevGPUCanonical(envelope, nil); err == nil {
		t.Fatal("missing formal ACC result accepted")
	}
	for _, actor := range []string{"governance:user:0", "governance:user:042", "governance:user:4294967296", "governance:access-key:42", "modeldev:owner"} {
		if _, _, err := ModelDevLedgerActor(actor); err == nil {
			t.Fatal("nonoriginal create actor accepted")
		}
	}
	envelope.Snapshot = conformance.SnapshotV1()
	if _, err := NewModelDevGPUCanonical(envelope, nil); err == nil {
		t.Fatal("CPU acquired GPU canonical")
	}
}

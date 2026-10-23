package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"

	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	protocol "github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/gpuprotocol"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/proto"
)

func TestInstanceAdmissionFactor10FreezesLogicalQuotaAndExecution(t *testing.T) {
	for _, scenario := range []struct {
		name, factor, blocks string
		memoryMiB, quota     int64
		devices              uint32
		whole                bool
	}{
		{name: "legacy_factor1_shared", blocks: "3840", memoryMiB: 3840, quota: 3840, devices: 1},
		{name: "factor10_shared", factor: "10", blocks: "384", memoryMiB: 3840, quota: 3840, devices: 1},
		{name: "factor10_whole", factor: "10", blocks: "1536", memoryMiB: 15360, quota: 2, devices: 2, whole: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			plan, _, _ := gpuVector(t)
			plan.Request.DevicesPerReplica = scenario.devices
			plan.Profile.Spec.MemoryMibPerDevice = scenario.memoryMiB
			plan.Resolved.Execution.Resources["volcano.sh/vgpu-memory"] = scenario.blocks
			plan.Resolved.Execution.Resources["volcano.sh/vgpu-number"] = strconv.FormatUint(uint64(scenario.devices), 10)
			plan.Resolved.QuotaItems[0].Units = scenario.quota
			if scenario.factor != "" {
				plan.Resolved.Execution.PodAnnotations["accelerator.ani.io/memory-block-mib"] = scenario.factor
			}
			if scenario.whole {
				plan.Profile.Spec.Mode = acc.SupplyMode_WHOLE_EXCLUSIVE
				plan.Profile.Spec.CoreLimitPercent = 100
				plan.Profile.Spec.MaxDevicesPerReplica = scenario.devices
				plan.Profile.Spec.IsolationClass = "WHOLE_DEVICE_EXCLUSIVE"
				plan.Resolved.Selection.Mode = acc.SupplyMode_WHOLE_EXCLUSIVE
				plan.Resolved.Execution.Resources["volcano.sh/vgpu-cores"] = "100"
				plan.Resolved.QuotaItems[0].QuotaCode = GpuPhysicalQuotaCode
			}
			var err error
			plan.Profile.SpecDigest, err = gpuSpecDigest(plan.Profile)
			if err != nil {
				t.Fatal(err)
			}
			plan.Resolved.Execution.ExecutionDigest, err = protocol.GpuExecutionDigest(plan.Resolved.Execution)
			if err != nil {
				t.Fatal(err)
			}
			execution := proto.Clone(plan.Resolved.Execution).(*acc.GpuExecution)
			business := instanceSettingsBusiness(t)
			business.ComputeType = "gpu"
			business.GpuSelection = &res.GpuPresetReference{ProfileId: plan.Profile.ProfileId, ProfileVersion: plan.Profile.ProfileVersion, SpecDigest: plan.Profile.SpecDigest, DevicesPerInstance: scenario.devices, ClusterId: plan.Request.ClusterId, PoolId: plan.Request.PoolId, Mode: plan.Profile.Spec.Mode, ModelKey: plan.Profile.Spec.ModelKey, MemoryMibPerDevice: scenario.memoryMiB, CoreLimitPercent: plan.Profile.Spec.CoreLimitPercent}
			owner := &instanceSettingsResolveProbe{template: business}
			binding := NewInstanceGpuBinding(owner, nil, business.ClusterId)
			principal := &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7}
			prepared, err := binding.PrepareManagedBusiness(context.Background(), principal, presetTenant, instanceSettingsData(business))
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(prepared.gpu, plan.Request) {
				t.Fatal("trusted Resource GPU selection changed")
			}
			raw, err := prepared.freeze(presetTenant, "user", "9", "operation", "resource", plan.Resolved)
			if err != nil {
				t.Fatal(err)
			}
			command, err := commandcontract.DecodeGpuCanonical(raw)
			if err != nil {
				t.Fatal(err)
			}
			if len(command.QuotaItems) != 1 || command.QuotaItems[0].Units != scenario.quota || command.QuotaItems[0].QuotaCode != plan.Resolved.QuotaItems[0].QuotaCode {
				t.Fatalf("raw blocks replaced logical quota: %+v", command.QuotaItems)
			}
			frozen, err := res.DecodeBusinessPayload(command.BusinessPayload)
			if err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(frozen.GpuExecution, execution) || frozen.GpuSelection != nil {
				t.Fatal("frozen execution was re-encoded or lost annotations/digest")
			}
			sum := sha256.Sum256(command.BusinessPayload)
			if command.BusinessPayloadDigest != hex.EncodeToString(sum[:]) {
				t.Fatal("frozen command digest differs from complete payload")
			}
			if scenario.factor != "" && !bytes.Contains(command.BusinessPayload, []byte(`"accelerator.ani.io/memory-block-mib":"10"`)) {
				t.Fatal("factor annotation missing from canonical command")
			}
			if scenario.factor == "10" && !scenario.whole {
				underMetered := proto.Clone(plan.Resolved).(*acc.ResolvedGpuRequest)
				underMetered.QuotaItems[0].Units = 384
				if _, err = commandcontract.GpuResolvedQuota(underMetered, plan.Request); err == nil {
					t.Fatal("raw-block quota undercharged logical MiB")
				}
			}
			plan.Resolved.Execution.Resources["volcano.sh/vgpu-memory"] = "1"
			plan.Resolved.Execution.PodAnnotations["accelerator.ani.io/memory-block-mib"] = "1"
			if !proto.Equal(frozen.GpuExecution, execution) {
				t.Fatal("mutable plan changed frozen bytes")
			}
		})
	}
}

func TestInstanceAdmissionCPUStillFreezesWithoutGPUQuota(t *testing.T) {
	business := instanceSettingsBusiness(t)
	owner := &instanceSettingsResolveProbe{template: business}
	binding := NewInstanceGpuBinding(owner, nil, business.ClusterId)
	principal := &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7}
	prepared, err := binding.PrepareManagedBusiness(context.Background(), principal, presetTenant, instanceSettingsData(business))
	if err != nil {
		t.Fatal(err)
	}
	if prepared.gpu != nil {
		t.Fatal("CPU admission acquired a GPU quota request")
	}
	raw, err := prepared.freeze(presetTenant, "user", "9", "operation", "resource", nil)
	if err != nil {
		t.Fatal(err)
	}
	command, err := commandcontract.DecodeInstanceCPUCanonical(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !commandcontract.AllowsInstanceCPUCommand(InstanceOwner, InstanceCreateAction, string(raw)) || bytes.Contains(raw, []byte("quota_items")) || bytes.Contains(raw, []byte(GpuSharedQuotaCode)) {
		t.Fatal("CPU command gained GPU metering")
	}
	frozen, err := res.DecodeBusinessPayload(command.BusinessPayload)
	if err != nil || frozen.ComputeType != "cpu" || frozen.GpuExecution != nil || frozen.GpuDisplay != nil {
		t.Fatalf("CPU command changed: %+v error=%v", frozen, err)
	}
	if _, err = prepared.freeze(presetTenant, "user", "9", "operation", "resource", &acc.ResolvedGpuRequest{}); err == nil {
		t.Fatal("CPU freeze accepted a GPU plan")
	}
}

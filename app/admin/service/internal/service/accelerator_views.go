package service

import (
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
)

func wireAcceleratorCluster(v *acc.Cluster) *view.AcceleratorCluster {
	return &view.AcceleratorCluster{ClusterId: v.ClusterId, DisplayName: v.DisplayName, ConnectionRef: v.ConnectionRef, Version: v.Version}
}
func wireAcceleratorPool(v *acc.Pool) *view.AcceleratorPool {
	return &view.AcceleratorPool{PoolId: v.PoolId, ClusterId: v.ClusterId, DisplayName: v.DisplayName}
}
func wireAcceleratorObservation(v *acc.Observation) *view.AcceleratorObservation {
	if v == nil {
		return &view.AcceleratorObservation{Complete: false, Issues: []string{"SOURCE_UNKNOWN"}}
	}
	issues, valid := acceleratorPublicReasons(v.Issues)
	return &view.AcceleratorObservation{Complete: v.Complete && valid, ObservedAt: v.ObservedAt, Issues: issues}
}

func acceleratorPublicReasons(input []string) ([]string, bool) {
	out := make([]string, 0, len(input))
	valid := true
	for _, reason := range input {
		if !acceleratorReason.MatchString(reason) {
			valid = false
			reason = "SOURCE_UNKNOWN"
		}
		out = append(out, reason)
	}
	return out, valid
}
func wireAcceleratorSpec(v *acc.ProfileSpec) *view.AcceleratorProfileSpec {
	if v == nil {
		return nil
	}
	return &view.AcceleratorProfileSpec{GroupId: v.GroupId, Mode: v.Mode.String(), ModelKey: v.ModelKey, MemoryMibPerDevice: v.MemoryMibPerDevice, CoreLimitPercent: v.CoreLimitPercent, MaxDevicesPerReplica: v.MaxDevicesPerReplica, IsolationClass: v.IsolationClass}
}
func acceleratorSpecInput(v *view.AcceleratorProfileSpec) (*acc.ProfileSpec, error) {
	mode, ok := acc.SupplyMode_value[v.Mode]
	if !ok || mode == 0 {
		return nil, acceleratorInvalid()
	}
	return &acc.ProfileSpec{GroupId: v.GroupId, Mode: acc.SupplyMode(mode), ModelKey: v.ModelKey, MemoryMibPerDevice: v.MemoryMibPerDevice, CoreLimitPercent: v.CoreLimitPercent, MaxDevicesPerReplica: v.MaxDevicesPerReplica, IsolationClass: v.IsolationClass}, nil
}
func wireAcceleratorProfile(v *acc.GpuProfile) *view.AcceleratorProfile {
	return &view.AcceleratorProfile{ProfileId: v.ProfileId, ProfileVersion: v.ProfileVersion, DisplayName: v.DisplayName, Spec: wireAcceleratorSpec(v.Spec), State: v.State.String(), Revision: v.Revision}
}
func wireAcceleratorProfiles(v *acc.ListProfilesResponse) (*view.AcceleratorProfiles, error) {
	if v == nil {
		return nil, acceleratorInvalidResponse()
	}
	out := &view.AcceleratorProfiles{NextPageToken: v.NextToken}
	for _, x := range v.Items {
		if x == nil {
			return nil, acceleratorInvalidResponse()
		}
		out.Items = append(out.Items, wireAcceleratorProfile(x))
	}
	return out, nil
}
func wireAcceleratorBaseline(v *acc.ProviderBaseline) *view.AcceleratorBaseline {
	if v == nil {
		return nil
	}
	return &view.AcceleratorBaseline{BaselineId: v.BaselineId, AdapterId: v.AdapterId, VolcanoVersion: v.VolcanoVersion, VolcanoImageDigest: v.VolcanoImageDigest, DevicePluginVersion: v.DevicePluginVersion, DevicePluginImageDigest: v.DevicePluginImageDigest, HamiCoreRevision: v.HamiCoreRevision, MemoryBlockMib: v.MemoryBlockMib, MemoryEncoding: v.MemoryEncoding, SplitCount: v.SplitCount, MemoryScalePercent: v.MemoryScalePercent, CoreScalePercent: v.CoreScalePercent, CoreBudgetAdditive: v.CoreBudgetAdditive, NodeConfigDigest: v.NodeConfigDigest, SchedulerConfigDigest: v.SchedulerConfigDigest, RecipeVersion: v.RecipeVersion, BaselineDigest: v.BaselineDigest, RuntimeClassName: v.RuntimeClassName}
}
func acceleratorBaselineInput(v *view.AcceleratorBaseline) *acc.ProviderBaseline {
	return &acc.ProviderBaseline{BaselineId: v.BaselineId, AdapterId: v.AdapterId, VolcanoVersion: v.VolcanoVersion, VolcanoImageDigest: v.VolcanoImageDigest, DevicePluginVersion: v.DevicePluginVersion, DevicePluginImageDigest: v.DevicePluginImageDigest, HamiCoreRevision: v.HamiCoreRevision, MemoryBlockMib: v.MemoryBlockMib, MemoryEncoding: v.MemoryEncoding, SplitCount: v.SplitCount, MemoryScalePercent: v.MemoryScalePercent, CoreScalePercent: v.CoreScalePercent, CoreBudgetAdditive: v.CoreBudgetAdditive, NodeConfigDigest: v.NodeConfigDigest, SchedulerConfigDigest: v.SchedulerConfigDigest, RecipeVersion: v.RecipeVersion, BaselineDigest: v.BaselineDigest, RuntimeClassName: v.RuntimeClassName}
}
func wireAcceleratorSupply(v *acc.SupplyGroup) *view.AcceleratorSupply {
	out := &view.AcceleratorSupply{GroupId: v.GroupId, PoolId: v.PoolId, ClusterId: v.ClusterId, DisplayName: v.DisplayName, Mode: v.Mode.String(), ModelKey: v.ModelKey, Baseline: wireAcceleratorBaseline(v.Baseline), Admission: v.Admission.String(), QueueName: v.QueueName, Version: v.Version, AdoptedAt: v.AdoptedAt}
	for _, n := range v.Nodes {
		out.Nodes = append(out.Nodes, &view.AcceleratorNode{Uid: n.GetUid(), Name: n.GetName()})
	}
	return out
}
func wireAcceleratorCapacity(v *acc.CapacityView, admin bool) *view.AcceleratorCapacity {
	out := &view.AcceleratorCapacity{ProfileId: v.ProfileId, ProfileVersion: v.ProfileVersion, Mode: v.Mode.String(), PhysicalDeviceCount: v.PhysicalDeviceCount, NominalUnits: v.NominalUnits, KnownCommittedUnits: v.KnownCommittedUnits, KnownFreeUnits: v.KnownFreeUnits, UnplacedRequestedUnits: v.UnplacedRequestedUnits, Observation: wireAcceleratorObservation(v.Observation)}
	if admin {
		for _, d := range v.Devices {
			if d == nil {
				continue
			}
			out.Devices = append(out.Devices, &view.AcceleratorDeviceCapacity{DeviceId: d.DeviceId, NominalUnits: d.NominalUnits, KnownCommittedUnits: d.KnownCommittedUnits, KnownFreeUnits: d.KnownFreeUnits, SafeMemoryBudgetMib: d.SafeMemoryBudgetMib, CommittedMemoryMib: d.CommittedMemoryMib, Complete: d.Complete, Issues: d.Issues})
		}
	}
	return out
}
func wireAcceleratorUsage(v *acc.GpuUsageProjection) *view.AcceleratorUsage {
	return &view.AcceleratorUsage{OwnerService: v.Ref.GetOwnerService(), ResourceId: v.Ref.GetResourceId(), CreateOperationId: v.Ref.GetCreateOperationId(), Revision: v.Revision, State: v.State.String(), EndReason: v.EndReason.String(), ProfileId: v.ProfileId, ProfileVersion: v.ProfileVersion, SourceOperationCreatedAt: v.SourceOperationCreatedAt}
}
func wireAcceleratorBinding(v *acc.GpuBinding) *view.AcceleratorBinding {
	return &view.AcceleratorBinding{BindingId: v.BindingId, ContainerName: v.Pod.GetContainerName(), State: v.State.String(), MemoryMib: v.MemoryMib, CoreLimitPercent: v.CoreLimitPercent, Observation: wireAcceleratorObservation(v.Observation)}
}

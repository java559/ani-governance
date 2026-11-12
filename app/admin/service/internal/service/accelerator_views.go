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
	return &view.AcceleratorProfileSpec{GroupId: v.GroupId, Mode: v.Mode.String(), ModelKey: v.ModelKey, MemoryMibPerDevice: v.MemoryMibPerDevice, CoreLimitPercent: v.CoreLimitPercent, MaxDevicesPerReplica: v.MaxDevicesPerReplica, IsolationClass: v.IsolationClass, AllocationType: v.AllocationType.String(), ResourceKey: v.ResourceKey, ComputeUnitsPerDevice: v.ComputeUnitsPerDevice}
}
func acceleratorSpecInput(v *view.AcceleratorProfileSpec) (*acc.ProfileSpec, error) {
	if v == nil {
		return nil, acceleratorInvalid()
	}
	allocation, ok := acc.AllocationType_value[v.AllocationType]
	if !ok || allocation == 0 {
		return nil, acceleratorInvalid()
	}
	mode, ok := acc.SupplyMode_value[v.Mode]
	if !ok || mode == 0 {
		return nil, acceleratorInvalid()
	}
	return &acc.ProfileSpec{GroupId: v.GroupId, Mode: acc.SupplyMode(mode), ModelKey: v.ModelKey, MemoryMibPerDevice: v.MemoryMibPerDevice, CoreLimitPercent: v.CoreLimitPercent, MaxDevicesPerReplica: v.MaxDevicesPerReplica, IsolationClass: v.IsolationClass, AllocationType: acc.AllocationType(allocation), ResourceKey: v.ResourceKey, ComputeUnitsPerDevice: v.ComputeUnitsPerDevice}, nil
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
	// Preserve nil, known false and known true for optional legacy metadata.
	out := &view.AcceleratorBaseline{BaselineId: v.BaselineId, AdapterId: v.AdapterId, MemoryBlockMib: v.MemoryBlockMib, SplitCount: v.SplitCount, CoreBudgetAdditive: v.CoreBudgetAdditive, RuntimeClassName: v.RuntimeClassName}
	for _, t := range v.ResourceTypes {
		if t == nil {
			continue
		}
		out.ResourceTypes = append(out.ResourceTypes, &view.AcceleratorResourceTypeConfig{AllocationType: t.AllocationType.String(), ResourceKey: t.ResourceKey, MemoryCapacityBytes: t.MemoryCapacityBytes, ComputeUnitsPerDevice: t.ComputeUnitsPerDevice, MaxDevicesPerReplica: t.MaxDevicesPerReplica, MaxMemoryMibPerDevice: t.MaxMemoryMibPerDevice})
	}
	return out
}
func acceleratorBaselineInput(v *view.AcceleratorBaseline) (*acc.ProviderBaseline, error) {
	if v == nil {
		return nil, acceleratorInvalid()
	}
	// Old request replay retains the supplied legacy value and its presence.
	out := &acc.ProviderBaseline{BaselineId: v.BaselineId, AdapterId: v.AdapterId, MemoryBlockMib: v.MemoryBlockMib, SplitCount: v.SplitCount, CoreBudgetAdditive: v.CoreBudgetAdditive, RuntimeClassName: v.RuntimeClassName}
	for _, t := range v.ResourceTypes {
		if t == nil {
			return nil, acceleratorInvalid()
		}
		allocation, ok := acc.AllocationType_value[t.AllocationType]
		if !ok || allocation == 0 {
			return nil, acceleratorInvalid()
		}
		out.ResourceTypes = append(out.ResourceTypes, &acc.ResourceTypeConfig{AllocationType: acc.AllocationType(allocation), ResourceKey: t.ResourceKey, MemoryCapacityBytes: t.MemoryCapacityBytes, ComputeUnitsPerDevice: t.ComputeUnitsPerDevice, MaxDevicesPerReplica: t.MaxDevicesPerReplica, MaxMemoryMibPerDevice: t.MaxMemoryMibPerDevice})
	}
	return out, nil
}
func wireAcceleratorSupply(v *acc.SupplyGroup) *view.AcceleratorSupply {
	out := &view.AcceleratorSupply{GroupId: v.GroupId, PoolId: v.PoolId, ClusterId: v.ClusterId, DisplayName: v.DisplayName, ModelKey: v.ModelKey, Baseline: wireAcceleratorBaseline(v.Baseline), Admission: v.Admission.String(), QueueName: v.QueueName, Version: v.Version, AdoptedAt: v.AdoptedAt, ProviderConfigRef: v.ProviderConfigRef, ProviderConfigVersion: v.ProviderConfigVersion}
	if v.Policy != nil {
		out.Policy = &view.AcceleratorSupplyGroupPolicy{PolicyKind: v.Policy.PolicyKind.String(), Parts: v.Policy.Parts, ResourceKey: v.Policy.ResourceKey}
		if v.Policy.NativeAllocationType != nil {
			value := v.Policy.NativeAllocationType.String()
			out.Policy.NativeAllocationType = &value
		}
	}
	for _, n := range v.Nodes {
		out.Nodes = append(out.Nodes, &view.AcceleratorNode{Uid: n.GetUid(), Name: n.GetName()})
	}
	return out
}

func wireAcceleratorDevice(v *acc.PhysicalDevice) *view.AcceleratorDevice {
	if v == nil {
		return nil
	}
	out := &view.AcceleratorDevice{DeviceId: v.DeviceId, ClusterId: v.ClusterId, GroupId: v.GroupId, ModelKey: v.ModelKey, Health: v.Health, Observation: wireAcceleratorObservation(v.Observation), DeviceUid: v.DeviceUid, ParentDeviceUid: v.ParentDeviceUid, DeviceKind: v.DeviceKind.String(), MemoryCapacityBytes: v.MemoryCapacityBytes, ComputeUnits: v.ComputeUnits, PciBdf: v.PciBdf, AdapterId: v.AdapterId, ResourceKey: v.ResourceKey}
	if v.Node != nil {
		out.Node = &view.AcceleratorNode{Uid: v.Node.Uid, Name: v.Node.Name}
	}
	return out
}
func wireAcceleratorCapacityTotals(v *acc.CapacityTotals) *view.AcceleratorCapacityTotals {
	if v == nil {
		return &view.AcceleratorCapacityTotals{}
	}
	return &view.AcceleratorCapacityTotals{TotalUnits: v.TotalUnits, AllocatedUnits: v.AllocatedUnits, UnallocatedUnits: v.UnallocatedUnits, TotalMemoryMib: v.TotalMemoryMib, AllocatedMemoryMib: v.AllocatedMemoryMib, UnallocatedMemoryMib: v.UnallocatedMemoryMib}
}
func wireAcceleratorCapacityDetail(v *acc.CapacityDetail) *view.AcceleratorCapacityDetail {
	if v == nil {
		return nil
	}
	return &view.AcceleratorCapacityDetail{NodeUid: v.NodeUid, NodeName: v.NodeName, DeviceUid: v.DeviceUid, Totals: wireAcceleratorCapacityTotals(v.Totals), NominalUnits: v.NominalUnits, EstimatedAvailableUnits: v.EstimatedAvailableUnits}
}
func wireAcceleratorProfileCapacity(v *acc.ProfileCapacity) *view.AcceleratorProfileCapacity {
	if v == nil {
		return nil
	}
	out := &view.AcceleratorProfileCapacity{ProfileId: v.ProfileId, ProfileVersion: v.ProfileVersion, AllocationType: v.AllocationType.String(), ResourceKey: v.ResourceKey, Basis: v.Basis.String(), Granularity: v.Granularity.String(), NominalUnits: v.NominalUnits, EstimatedAvailableUnits: v.EstimatedAvailableUnits, Totals: wireAcceleratorCapacityTotals(v.Totals), ObservedAt: v.ObservedAt, MissingFields: v.MissingFields}
	for _, d := range v.Details {
		out.Details = append(out.Details, wireAcceleratorCapacityDetail(d))
	}
	return out
}
func wireAcceleratorResourceTypeCapacity(v *acc.ResourceTypeCapacity) *view.AcceleratorResourceTypeCapacity {
	if v == nil {
		return nil
	}
	out := &view.AcceleratorResourceTypeCapacity{AllocationType: v.AllocationType.String(), ResourceKey: v.ResourceKey, Basis: v.Basis.String(), Granularity: v.Granularity.String(), Totals: wireAcceleratorCapacityTotals(v.Totals), NominalUnits: v.NominalUnits, EstimatedAvailableUnits: v.EstimatedAvailableUnits, ObservedAt: v.ObservedAt, MissingFields: v.MissingFields}
	for _, d := range v.Details {
		out.Details = append(out.Details, wireAcceleratorCapacityDetail(d))
	}
	return out
}
func wireAcceleratorSupplyGroupCapacity(v *acc.SupplyGroupCapacity) *view.AcceleratorSupplyGroupCapacity {
	if v == nil {
		return nil
	}
	out := &view.AcceleratorSupplyGroupCapacity{GroupId: v.GroupId, PhysicalDeviceCount: v.PhysicalDeviceCount, ObservedAt: v.ObservedAt}
	for _, t := range v.ResourceTypes {
		out.ResourceTypes = append(out.ResourceTypes, wireAcceleratorResourceTypeCapacity(t))
	}
	return out
}

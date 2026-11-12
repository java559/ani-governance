package service

import (
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	view "go-wind-admin/api/gen/go/catalog/service/v1"
)

func acceleratorInitializationInput(input *view.CreateAcceleratorInitializationData) (*acc.CreateInitializationRequest, []string, error) {
	if input == nil || input.DiscoveryRevision == "" || len(input.DiscoveryRevision) > 8192 || len(input.Pools) > 32 || len(input.Groups) > 32 || len(input.Profiles) > 128 || len(input.Pools)+len(input.Groups)+len(input.Profiles) == 0 {
		return nil, nil, acceleratorInvalid()
	}
	result := &acc.CreateInitializationRequest{DiscoveryRevision: input.DiscoveryRevision, ConnectionRef: input.ConnectionRef}
	actions := []string{}
	for _, pool := range input.Pools {
		if pool == nil {
			return nil, nil, acceleratorInvalid()
		}
		mapped := &acc.InitializationPoolInput{ClientKey: pool.ClientKey}
		switch target := pool.Pool.(type) {
		case *view.AcceleratorInitializationPoolInput_ExistingPoolId:
			mapped.Pool = &acc.InitializationPoolInput_ExistingPoolId{ExistingPoolId: target.ExistingPoolId}
		case *view.AcceleratorInitializationPoolInput_NewPool:
			if target.NewPool == nil {
				return nil, nil, acceleratorInvalid()
			}
			mapped.Pool = &acc.InitializationPoolInput_NewPool{NewPool: &acc.InitializationNewPool{DisplayName: target.NewPool.DisplayName}}
			actions = append(actions, "accelerator:pool:create")
		default:
			return nil, nil, acceleratorInvalid()
		}
		result.Pools = append(result.Pools, mapped)
	}
	newGroups := map[string]bool{}
	nodeCount := 0
	for _, group := range input.Groups {
		if group == nil {
			return nil, nil, acceleratorInvalid()
		}
		mapped := &acc.InitializationGroupInput{ClientKey: group.ClientKey}
		switch target := group.Group.(type) {
		case *view.AcceleratorInitializationGroupInput_ExistingGroup:
			if target.ExistingGroup == nil {
				return nil, nil, acceleratorInvalid()
			}
			mapped.Group = &acc.InitializationGroupInput_ExistingGroup{ExistingGroup: &acc.InitializationExistingGroup{GroupId: target.ExistingGroup.GroupId, ExpectedVersion: target.ExistingGroup.ExpectedVersion}}
		case *view.AcceleratorInitializationGroupInput_NewGroup:
			if target.NewGroup == nil {
				return nil, nil, acceleratorInvalid()
			}
			newGroup := target.NewGroup
			mappedNew := &acc.InitializationNewGroup{PoolKey: newGroup.PoolKey, DisplayName: newGroup.DisplayName, ProviderConfigRef: newGroup.ProviderConfigRef, ProviderConfigVersion: newGroup.ProviderConfigVersion, QueueName: newGroup.QueueName}
			policy, err := acceleratorGroupPolicyInput(newGroup.Policy)
			if err != nil {
				return nil, nil, err
			}
			mappedNew.Policy = policy
			for _, node := range newGroup.Nodes {
				if node == nil {
					return nil, nil, acceleratorInvalid()
				}
				mappedNew.Nodes = append(mappedNew.Nodes, &acc.NodeRef{Uid: node.Uid, Name: node.Name})
			}
			nodeCount += len(newGroup.Nodes)
			if nodeCount > 1024 {
				return nil, nil, acceleratorInvalid()
			}
			mapped.Group = &acc.InitializationGroupInput_NewGroup{NewGroup: mappedNew}
			newGroups[group.ClientKey] = true
		default:
			return nil, nil, acceleratorInvalid()
		}
		result.Groups = append(result.Groups, mapped)
	}
	if len(input.Groups) != 0 {
		actions = append(actions, "accelerator:supply-group:adopt")
	}
	for _, profile := range input.Profiles {
		if profile == nil {
			return nil, nil, acceleratorInvalid()
		}
		mappedProfile := &acc.InitializationProfileInput{ClientKey: profile.ClientKey, GroupKey: profile.GroupKey, DisplayName: profile.DisplayName, DeriveFromGroupPolicy: profile.DeriveFromGroupPolicy}
		if profile.DeriveFromGroupPolicy {
			if profile.MemoryMibPerDevice != 0 || profile.CoreLimitPercent != 0 || profile.MaxDevicesPerReplica != 0 || profile.AllocationType != "" || profile.ResourceKey != "" || profile.ComputeUnitsPerDevice != 0 {
				return nil, nil, acceleratorInvalid()
			}
		} else {
			allocation, ok := acc.AllocationType_value[profile.AllocationType]
			if !ok || allocation == 0 {
				return nil, nil, acceleratorInvalid()
			}
			mappedProfile.MemoryMibPerDevice = profile.MemoryMibPerDevice
			mappedProfile.CoreLimitPercent = profile.CoreLimitPercent
			mappedProfile.MaxDevicesPerReplica = profile.MaxDevicesPerReplica
			mappedProfile.AllocationType = acc.AllocationType(allocation)
			mappedProfile.ResourceKey = profile.ResourceKey
			mappedProfile.ComputeUnitsPerDevice = profile.ComputeUnitsPerDevice
		}
		result.Profiles = append(result.Profiles, mappedProfile)
		if newGroups[profile.GroupKey] {
			actions = append(actions, "accelerator:supply-group:set-admission")
		}
	}
	if len(input.Profiles) != 0 {
		actions = append(actions, "accelerator:profile:publish")
	}
	return result, actions, nil
}

func acceleratorInitializationView(input, output proto.Message) error {
	if input == nil || !input.ProtoReflect().IsValid() {
		return acceleratorInvalidResponse()
	}
	encoded, err := protojson.Marshal(input)
	if err != nil {
		return acceleratorInvalidResponse()
	}
	if err := (protojson.UnmarshalOptions{}).Unmarshal(encoded, output); err != nil {
		return acceleratorInvalidResponse()
	}
	return nil
}

func wireAcceleratorInitialization(input *acc.Initialization) (*view.AcceleratorInitialization, error) {
	output := &view.AcceleratorInitialization{}
	if err := acceleratorInitializationView(input, output); err != nil {
		return nil, err
	}
	for _, check := range output.Checks {
		if check == nil || !acceleratorInitializationReason(check.Reason) {
			return nil, acceleratorInvalidResponse()
		}
	}
	if input.LatestOperation != nil {
		operation, err := wireAcceleratorInitializationOperation(input.LatestOperation)
		if err != nil {
			return nil, err
		}
		output.LatestOperation = operation
	}
	return output, nil
}

func wireAcceleratorDiscoveredNodes(input *acc.DiscoveredNodes) (*view.AcceleratorDiscoveredNodes, error) {
	if input == nil {
		return nil, acceleratorInvalidResponse()
	}
	out := &view.AcceleratorDiscoveredNodes{Total: input.Total, DiscoveryRevision: input.DiscoveryRevision}
	for _, node := range input.Items {
		if node == nil || node.Node == nil {
			return nil, acceleratorInvalidResponse()
		}
		if _, valid := acceleratorPublicReasons(node.BlockingReasons); !valid {
			return nil, acceleratorInvalidResponse()
		}
		row := &view.AcceleratorDiscoveredNode{Node: &view.AcceleratorNode{Uid: node.Node.Uid, Name: node.Node.Name}, GroupId: node.GroupId, Selectable: node.Selectable, BlockingReasons: node.BlockingReasons}
		for _, device := range node.Devices {
			if device == nil {
				return nil, acceleratorInvalidResponse()
			}
			row.Devices = append(row.Devices, wireAcceleratorDevice(device))
		}
		for _, config := range node.ProviderConfigs {
			if config == nil {
				return nil, acceleratorInvalidResponse()
			}
			mapped := &view.AcceleratorDiscoveredProviderConfig{ProviderConfigRef: config.ProviderConfigRef, AdapterId: config.AdapterId, SourceKind: config.SourceKind.String(), ProviderConfigVersion: config.ProviderConfigVersion}
			for _, kind := range config.AllocationTypes {
				mapped.AllocationTypes = append(mapped.AllocationTypes, kind.String())
			}
			row.ProviderConfigs = append(row.ProviderConfigs, mapped)
		}
		out.Items = append(out.Items, row)
	}
	return out, nil
}

func wireAcceleratorInitializationOperation(input *acc.InitializationOperation) (*view.AcceleratorInitializationOperation, error) {
	output := &view.AcceleratorInitializationOperation{}
	if err := acceleratorInitializationView(input, output); err != nil {
		return nil, err
	}
	switch output.State {
	case "ACCEPTED", "RUNNING", "SUCCEEDED", "BLOCKED", "CONFLICT", "FAILED":
	default:
		return nil, acceleratorInvalidResponse()
	}
	if !acceleratorInitializationID(output.OperationId) || !acceleratorInitializationReason(output.Reason) {
		return nil, acceleratorInvalidResponse()
	}
	for _, step := range output.Steps {
		if step == nil || !acceleratorInitializationReason(step.Reason) {
			return nil, acceleratorInvalidResponse()
		}
	}
	for _, action := range output.RequiredActions {
		if _, known := acceleratorInitializationActions[action]; !known {
			return nil, acceleratorInvalidResponse()
		}
	}
	return output, nil
}

func acceleratorInitializationReason(reason string) bool {
	return reason == "" || acceleratorReason.MatchString(reason)
}

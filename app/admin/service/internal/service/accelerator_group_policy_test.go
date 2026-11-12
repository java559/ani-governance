package service

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
	"google.golang.org/grpc"
)

type groupPolicyBoundaryClient struct {
	acc.AcceleratorAdminServiceClient
	configuration  *acc.RegisteredSupplyConfiguration
	configRequest  *acc.GetSupplyConfigurationRequest
	preview        *acc.PreviewSupplyGroupPolicyResponse
	previewRequest *acc.PreviewSupplyGroupPolicyRequest
}

func (b *groupPolicyBoundaryClient) GetSupplyConfiguration(_ context.Context, input *acc.GetSupplyConfigurationRequest, _ ...grpc.CallOption) (*acc.RegisteredSupplyConfiguration, error) {
	b.configRequest = input
	return b.configuration, nil
}
func (b *groupPolicyBoundaryClient) PreviewSupplyGroupPolicy(_ context.Context, input *acc.PreviewSupplyGroupPolicyRequest, _ ...grpc.CallOption) (*acc.PreviewSupplyGroupPolicyResponse, error) {
	b.previewRequest = input
	return b.preview, nil
}
func TestAcceleratorGroupPolicyReadsAuthorizeAndPreserveFacts(t *testing.T) {
	service, _, ctx := initializationServiceFixture(t, "accelerator:discovery:list")
	maximum := uint32(2)
	parts := uint32(8)
	boundary := &groupPolicyBoundaryClient{
		configuration: &acc.RegisteredSupplyConfiguration{ConnectionRef: "managed", ProviderConfigRef: "supply", Version: 1, Baseline: &acc.ProviderBaseline{}, Capability: &acc.SupplyCapability{SourceKind: acc.SupplySourceKind_PLUGIN, WholeMaxDevicesPerReplica: &maximum}},
		preview: &acc.PreviewSupplyGroupPolicyResponse{ConnectionRef: "managed", ProviderConfigRef: "supply", ProviderConfigVersion: 1, DiscoveryRevision: "revision", Options: []*acc.SupplyGroupPolicyOption{
			{Policy: &acc.SupplyGroupPolicy{PolicyKind: acc.SupplyGroupPolicyKind_WHOLE}, Selectable: true, Shape: &acc.GroupPolicyShape{AllocationType: acc.AllocationType_PHYSICAL, Mode: acc.SupplyMode_WHOLE_EXCLUSIVE, MaxDevicesPerReplica: 2, MemoryMibPerDevice: 15360, CoreLimitPercent: 100}},
			{Policy: &acc.SupplyGroupPolicy{PolicyKind: acc.SupplyGroupPolicyKind_EQUAL_SHARE, Parts: &parts}, BlockingReasons: []string{"GROUP_POLICY_SPLIT_LIMIT"}, MissingFields: []string{"effective_memory_mib"}},
		}},
	}
	service.client.Admin = boundary
	configuration, err := service.GetSupplyConfiguration(ctx, &view.GetAcceleratorSupplyConfigurationRequest{ConnectionRef: "managed", ProviderConfigRef: "supply"})
	require.NoError(t, err)
	require.Equal(t, "PLUGIN", configuration.Capability.SourceKind)
	require.EqualValues(t, 2, configuration.Capability.GetWholeMaxDevicesPerReplica())
	require.Equal(t, "42", boundary.configRequest.Context.Actor.Id)
	input := &view.PreviewAcceleratorSupplyGroupPolicyRequest{Data: &view.PreviewAcceleratorSupplyGroupPolicyData{ConnectionRef: "managed", ProviderConfigRef: "supply", ProviderConfigVersion: 1, DiscoveryRevision: "revision", Nodes: []*view.AcceleratorNode{{Uid: "uid", Name: "node"}}}}
	result, err := service.PreviewSupplyGroupPolicy(ctx, input)
	require.NoError(t, err)
	require.Len(t, result.Options, 2)
	require.Equal(t, "WHOLE", result.Options[0].Policy.PolicyKind)
	require.EqualValues(t, 15360, result.Options[0].Shape.MemoryMibPerDevice)
	require.Equal(t, []string{"GROUP_POLICY_SPLIT_LIMIT"}, result.Options[1].BlockingReasons)
	require.Nil(t, result.Options[1].Shape)
	require.Equal(t, "42", boundary.previewRequest.Context.Actor.Id)

	boundary.configuration.ConnectionRef = "another-connection"
	_, err = service.GetSupplyConfiguration(ctx, &view.GetAcceleratorSupplyConfigurationRequest{ConnectionRef: "managed", ProviderConfigRef: "supply"})
	require.Equal(t, "ACCELERATOR_INVALID_RESPONSE", errors.FromError(err).Reason)
	input.Data.Nodes = append(input.Data.Nodes, &view.AcceleratorNode{Uid: "uid", Name: "other"})
	_, err = service.PreviewSupplyGroupPolicy(ctx, input)
	require.EqualValues(t, 400, errors.FromError(err).Code)

	service, _, ctx = initializationServiceFixture(t, "accelerator:initialization:read")
	service.client.Admin = boundary
	boundary.configRequest = nil
	_, err = service.GetSupplyConfiguration(ctx, &view.GetAcceleratorSupplyConfigurationRequest{ConnectionRef: "managed", ProviderConfigRef: "supply"})
	require.EqualValues(t, 403, errors.FromError(err).Code)
	require.Nil(t, boundary.configRequest)
}
func TestAcceleratorGroupPolicyInputRejectsUnusedPresence(t *testing.T) {
	zero := uint32(0)
	empty := ""
	physical := "PHYSICAL"
	parts := uint32(8)
	for _, input := range []*view.AcceleratorSupplyGroupPolicy{
		{PolicyKind: "WHOLE", Parts: &zero},
		{PolicyKind: "WHOLE", ResourceKey: &empty},
		{PolicyKind: "WHOLE", NativeAllocationType: &physical},
		{PolicyKind: "EQUAL_SHARE"},
		{PolicyKind: "EQUAL_SHARE", Parts: &parts, ResourceKey: &empty},
		{PolicyKind: "NATIVE_RESOURCE", ResourceKey: &empty, NativeAllocationType: &physical},
		{PolicyKind: "INVALID"},
	} {
		_, err := acceleratorGroupPolicyInput(input)
		require.EqualValues(t, 400, errors.FromError(err).Code)
	}
}

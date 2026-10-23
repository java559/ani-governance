package service

import (
	"context"
	"github.com/go-kratos/kratos/v2/errors"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	adminv1 "go-wind-admin/api/gen/go/admin/service/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"regexp"
	"strings"
)

// NetworkPlatformClient is separate from the tenant surface: no tenant resolver.
type NetworkPlatformClient interface {
	CreateVirtualSwitch(context.Context, string, *networkv1.CreateVirtualSwitchRequest) (*networkv1.CreateVirtualSwitchResponse, error)
	GetVirtualSwitch(context.Context, string, *networkv1.GetVirtualSwitchRequest) (*networkv1.GetVirtualSwitchResponse, error)
	ListVirtualSwitches(context.Context, string, *networkv1.ListVirtualSwitchesRequest) (*networkv1.ListVirtualSwitchesResponse, error)
	DeleteVirtualSwitch(context.Context, string, *networkv1.DeleteVirtualSwitchRequest) (*networkv1.DeleteVirtualSwitchResponse, error)
	RetireNetworkDevice(context.Context, string, *networkv1.RetireNetworkDeviceRequest) (*networkv1.RetireNetworkDeviceResponse, error)
	GetNetworkDeviceRetirement(context.Context, string, *networkv1.GetNetworkDeviceRetirementRequest) (*networkv1.GetNetworkDeviceRetirementResponse, error)
	CreateDefaultVPCEgress(context.Context, string, *networkv1.CreateDefaultVPCEgressRequest) (*networkv1.CreateDefaultVPCEgressResponse, error)
	GetDefaultVPCEgress(context.Context, string, *networkv1.GetDefaultVPCEgressRequest) (*networkv1.GetDefaultVPCEgressResponse, error)
	DeleteDefaultVPCEgress(context.Context, string, *networkv1.DeleteDefaultVPCEgressRequest) (*networkv1.DeleteDefaultVPCEgressResponse, error)
	ListNodeInterfaces(context.Context, string, *networkv1.ListNodeInterfacesRequest) (*networkv1.ListNodeInterfacesResponse, error)
	AdoptNetworkDevice(context.Context, string, *networkv1.AdoptNetworkDeviceRequest) (*networkv1.AdoptNetworkDeviceResponse, error)
	GetNetworkDevice(context.Context, string, *networkv1.GetNetworkDeviceRequest) (*networkv1.GetNetworkDeviceResponse, error)
	CreateVlanNetwork(context.Context, string, *networkv1.CreateVlanNetworkRequest) (*networkv1.CreateVlanNetworkResponse, error)
	GetVlanNetwork(context.Context, string, *networkv1.GetVlanNetworkRequest) (*networkv1.GetVlanNetworkResponse, error)
	ListVlanNetworks(context.Context, string, *networkv1.ListVlanNetworksRequest) (*networkv1.ListVlanNetworksResponse, error)
	DeleteVlanNetwork(context.Context, string, *networkv1.DeleteVlanNetworkRequest) (*networkv1.DeleteVlanNetworkResponse, error)
	CreateEgressGateway(context.Context, string, *networkv1.CreateEgressGatewayRequest) (*networkv1.CreateEgressGatewayResponse, error)
	GetEgressGateway(context.Context, string, *networkv1.GetEgressGatewayRequest) (*networkv1.GetEgressGatewayResponse, error)
	ListEgressGateways(context.Context, string, *networkv1.ListEgressGatewaysRequest) (*networkv1.ListEgressGatewaysResponse, error)
	DeleteEgressGateway(context.Context, string, *networkv1.DeleteEgressGatewayRequest) (*networkv1.DeleteEgressGatewayResponse, error)
	CreatePublicAddressPool(context.Context, string, *networkv1.CreatePublicAddressPoolRequest) (*networkv1.CreatePublicAddressPoolResponse, error)
	GetPublicAddressPool(context.Context, string, *networkv1.GetPublicAddressPoolRequest) (*networkv1.GetPublicAddressPoolResponse, error)
	ListPublicAddressPools(context.Context, string, *networkv1.ListPublicAddressPoolsRequest) (*networkv1.ListPublicAddressPoolsResponse, error)
	DeletePublicAddressPool(context.Context, string, *networkv1.DeletePublicAddressPoolRequest) (*networkv1.DeletePublicAddressPoolResponse, error)
	RecordPublicPoolVerification(context.Context, string, *networkv1.RecordPublicPoolVerificationRequest) (*networkv1.RecordPublicPoolVerificationResponse, error)
	SetPublicPoolAllocationEnabled(context.Context, string, *networkv1.SetPublicPoolAllocationEnabledRequest) (*networkv1.SetPublicPoolAllocationEnabledResponse, error)
	SetDefaultPublicPool(context.Context, string, *networkv1.SetDefaultPublicPoolRequest) (*networkv1.SetDefaultPublicPoolResponse, error)
	CreateIntranetAddressPool(context.Context, string, *networkv1.CreateIntranetAddressPoolRequest) (*networkv1.CreateIntranetAddressPoolResponse, error)
	GetIntranetAddressPool(context.Context, string, *networkv1.GetIntranetAddressPoolRequest) (*networkv1.GetIntranetAddressPoolResponse, error)
	ListIntranetAddressPools(context.Context, string, *networkv1.ListIntranetAddressPoolsRequest) (*networkv1.ListIntranetAddressPoolsResponse, error)
	DeleteIntranetAddressPool(context.Context, string, *networkv1.DeleteIntranetAddressPoolRequest) (*networkv1.DeleteIntranetAddressPoolResponse, error)
	RecordIntranetPoolVerification(context.Context, string, *networkv1.RecordIntranetPoolVerificationRequest) (*networkv1.RecordIntranetPoolVerificationResponse, error)
	SetIntranetPoolAllocationEnabled(context.Context, string, *networkv1.SetIntranetPoolAllocationEnabledRequest) (*networkv1.SetIntranetPoolAllocationEnabledResponse, error)
	SetDefaultIntranetPool(context.Context, string, *networkv1.SetDefaultIntranetPoolRequest) (*networkv1.SetDefaultIntranetPoolResponse, error)
	GetPlatformNetworkCapabilities(context.Context, string, *networkv1.GetPlatformNetworkCapabilitiesRequest) (*networkv1.GetPlatformNetworkCapabilitiesResponse, error)
	GetPlatformOperation(context.Context, string, *networkv1.GetPlatformOperationRequest) (*networkv1.GetPlatformOperationResponse, error)
}
type PlatformNetworkService struct {
	adminv1.UnimplementedPlatformNetworkServiceServer
	client NetworkPlatformClient
}

func NewPlatformNetworkService(client NetworkPlatformClient) *PlatformNetworkService {
	return &PlatformNetworkService{client: client}
}

func (s *PlatformNetworkService) actor(ctx context.Context, operation string) (string, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return "", errors.Unauthorized("INVALID_LOGIN", "login required")
	}
	policy, ok := auth.PlatformNetworkOperations["/admin.service.v1.PlatformNetworkService/"+operation]
	if !ok || p.Type != auth.SubjectUser || p.TenantID != 0 || p.Claims == nil || p.Claims.GetTenantId() != 0 || p.ID != p.Claims.GetUserId() || !hasPlatformRole(p.Roles) || !hasPermission(p.Claims, policy.Permission) {
		return "", errors.Forbidden("PLATFORM_NETWORK_DENIED", "platform network permission required")
	}
	if s.client == nil {
		return "", errors.ServiceUnavailable("NETWORK_UNAVAILABLE", "platform network access is not configured")
	}
	return p.Actor()
}

var platformResourceID = regexp.MustCompile(`^(device|vlan|egw|pool)_[0-9a-f]{32}$`)

func validPlatformResource(v *networkv1.PlatformResource, id, kind string) bool {
	if v == nil {
		return false
	}
	switch kind {
	case "device":
		if v.GetDevice() == nil || !strings.HasPrefix(v.Id, "device_") {
			return false
		}
	case "vlan":
		if v.GetVlan() == nil || !strings.HasPrefix(v.Id, "vlan_") {
			return false
		}
	case "gateway":
		if v.GetGateway() == nil || !strings.HasPrefix(v.Id, "egw_") {
			return false
		}
	case "public":
		if v.GetPool() == nil || v.GetPool().Scope != "public" || !strings.HasPrefix(v.Id, "pool_") {
			return false
		}
	case "intranet":
		if v.GetIntranetPool() == nil || v.GetIntranetPool().Scope != "intranet" || !strings.HasPrefix(v.Id, "pool_") {
			return false
		}
	default:
		return false
	}
	return v != nil && platformResourceID.MatchString(v.Id) && v.ClusterId != "" && (id == "" || v.Id == id) && v.State != networkv1.ResourceState_RESOURCE_STATE_UNSPECIFIED && networkv1.ResourceState_name[int32(v.State)] != "" && v.Configuration != nil
}
func platformListState(state string) (networkv1.ResourceState, error) {
	if state == "" {
		return networkv1.ResourceState_RESOURCE_STATE_UNSPECIFIED, nil
	}
	key := "RESOURCE_STATE_" + strings.TrimPrefix(strings.ToUpper(state), "RESOURCE_STATE_")
	v, ok := networkv1.ResourceState_value[key]
	if !ok {
		return 0, errors.BadRequest("INVALID_STATE", "invalid state filter")
	}
	return networkv1.ResourceState(v), nil
}
func validPlatformList(limit int32, cursor string) bool {
	return limit >= 0 && limit <= 100 && len(cursor) <= 4096
}
func publicPoolMode(mode string) (networkv1.PublicPoolMode, error) {
	switch strings.ToLower(mode) {
	case "overlay":
		return networkv1.PublicPoolMode_PUBLIC_POOL_MODE_OVERLAY, nil
	case "underlay":
		return networkv1.PublicPoolMode_PUBLIC_POOL_MODE_UNDERLAY, nil
	}
	return 0, errors.BadRequest("INVALID_PUBLIC_POOL_MODE", "mode must be overlay or underlay")
}

func wirePlatformResource(v *networkv1.PlatformResource) *catalogv1.PlatformResource {
	if v == nil {
		return nil
	}
	out := &catalogv1.PlatformResource{
		Id:               v.GetId(),
		ClusterId:        v.GetClusterId(),
		Name:             v.GetName(),
		Description:      v.GetDescription(),
		State:            strings.ToLower(strings.TrimPrefix(v.GetState().String(), "RESOURCE_STATE_")),
		Reason:           v.GetReason(),
		ReasonMessage:    v.GetReasonMessage(),
		Version:          v.GetVersion(),
		CreatedAt:        v.GetCreatedAt(),
		UpdatedAt:        v.GetUpdatedAt(),
		ObservedAt:       v.GetObservedAt(),
		ObservationStale: v.GetObservationStale(),
		LastOperationId:  v.GetLastOperationId(),
		Device:           wireNetworkDevice(v.GetDevice()),
		Vlan:             wireVlanNetwork(v.GetVlan()),
		Gateway:          wireEgressGateway(v.GetGateway()),
		Pool:             wirePublicAddressPool(v.GetPool()),
		IntranetPool:     wireIntranetAddressPool(v.GetIntranetPool()),
	}
	return out
}

func wireNodeInterface(v *networkv1.NodeInterface) *catalogv1.NodeInterface {
	if v == nil {
		return nil
	}
	out := &catalogv1.NodeInterface{
		NodeName:           v.GetNodeName(),
		NodeUid:            v.GetNodeUid(),
		Name:               v.GetName(),
		Kind:               v.GetKind(),
		Mac:                v.GetMac(),
		Mtu:                v.GetMtu(),
		LinkUp:             v.GetLinkUp(),
		Carrier:            v.GetCarrier(),
		Addresses:          v.GetAddresses(),
		Master:             v.GetMaster(),
		OvsManaged:         v.GetOvsManaged(),
		KcManaged:          v.GetKcManaged(),
		DefaultRoute:       v.GetDefaultRoute(),
		Management:         v.GetManagement(),
		Selectable:         v.GetSelectable(),
		UnavailableReasons: v.GetUnavailableReasons(),
		ObservedAt:         v.GetObservedAt(),
		VlanNetworkIds:     v.GetVlanNetworkIds(),
	}
	return out
}

func wireNetworkDevice(v *networkv1.NetworkDevice) *catalogv1.NetworkDevice {
	if v == nil {
		return nil
	}
	out := &catalogv1.NetworkDevice{
		DeviceName:           v.GetDeviceName(),
		InventoryFingerprint: v.GetInventoryFingerprint(),
	}
	for _, item := range v.GetNodes() {
		out.Nodes = append(out.Nodes, wireNodeInterface(item))
	}
	return out
}

func wireVlanNetwork(v *networkv1.VlanNetwork) *catalogv1.VlanNetwork {
	if v == nil {
		return nil
	}
	out := &catalogv1.VlanNetwork{
		DeviceId:        v.GetDeviceId(),
		VlanId:          v.GetVlanId(),
		VirtualSwitchId: v.GetVirtualSwitchId(),
		DeviceName:      v.GetDeviceName(),
		GroupName:       v.GetGroupName(),
	}
	for _, node := range v.GetNodes() {
		out.Nodes = append(out.Nodes, wireVirtualSwitchNode(node))
	}
	return out
}

func wireEgressGateway(v *networkv1.EgressGateway) *catalogv1.EgressGateway {
	if v == nil {
		return nil
	}
	out := &catalogv1.EgressGateway{}
	return out
}

func wirePublicAddressPool(v *networkv1.PublicAddressPool) *catalogv1.PublicAddressPool {
	if v == nil {
		return nil
	}
	out := &catalogv1.PublicAddressPool{
		Mode:                   strings.ToLower(strings.TrimPrefix(v.GetMode().String(), "PUBLIC_POOL_MODE_")),
		GatewayId:              v.GetGatewayId(),
		Cidr:                   v.GetCidr(),
		OvnGatewayIp:           v.GetOvnGatewayIp(),
		ExcludedIps:            v.GetExcludedIps(),
		VlanNetworkId:          v.GetVlanNetworkId(),
		UpstreamGatewayIp:      v.GetUpstreamGatewayIp(),
		AllocationEnabled:      v.GetAllocationEnabled(),
		IsDefault:              v.GetIsDefault(),
		ConfigRevision:         v.GetConfigRevision(),
		Verification:           wirePublicPoolVerification(v.GetVerification()),
		TopologyFingerprint:    v.GetTopologyFingerprint(),
		ObservedProviderImages: v.GetObservedProviderImages(),
		Scope:                  v.GetScope(),
	}
	return out
}

func wireIntranetAddressPool(v *networkv1.IntranetAddressPool) *catalogv1.IntranetAddressPool {
	if v == nil {
		return nil
	}
	out := &catalogv1.IntranetAddressPool{
		Cidr:                   v.GetCidr(),
		OvnGatewayIp:           v.GetOvnGatewayIp(),
		ExcludedIps:            v.GetExcludedIps(),
		DefaultVpcName:         v.GetDefaultVpcName(),
		DefaultVpcUid:          v.GetDefaultVpcUid(),
		IntranetNetworks:       v.GetIntranetNetworks(),
		AllocationEnabled:      v.GetAllocationEnabled(),
		IsDefault:              v.GetIsDefault(),
		ConfigRevision:         v.GetConfigRevision(),
		Verification:           wireIntranetPoolVerification(v.GetVerification()),
		TopologyFingerprint:    v.GetTopologyFingerprint(),
		ObservedProviderImages: v.GetObservedProviderImages(),
		Scope:                  v.GetScope(),
	}
	return out
}

func wireIntranetPoolVerification(v *networkv1.IntranetPoolVerification) *catalogv1.IntranetPoolVerification {
	if v == nil {
		return nil
	}
	out := &catalogv1.IntranetPoolVerification{
		ProviderSourceRevision: v.GetProviderSourceRevision(),
		ProviderImageDigests:   v.GetProviderImageDigests(),
		TopologyFingerprint:    v.GetTopologyFingerprint(),
		EvidenceReference:      v.GetEvidenceReference(),
		Scope:                  v.GetScope(),
		VerifiedAt:             v.GetVerifiedAt(),
		ExpiresAt:              v.GetExpiresAt(),
	}
	return out
}

func inputIntranetPoolVerification(v *catalogv1.IntranetPoolVerification) *networkv1.IntranetPoolVerification {
	if v == nil {
		return nil
	}
	out := &networkv1.IntranetPoolVerification{
		ProviderSourceRevision: v.GetProviderSourceRevision(),
		ProviderImageDigests:   v.GetProviderImageDigests(),
		TopologyFingerprint:    v.GetTopologyFingerprint(),
		EvidenceReference:      v.GetEvidenceReference(),
		Scope:                  v.GetScope(),
		VerifiedAt:             v.GetVerifiedAt(),
		ExpiresAt:              v.GetExpiresAt(),
	}
	return out
}

func wireCapabilityObservation(v *networkv1.CapabilityObservation) *catalogv1.CapabilityObservation {
	if v == nil {
		return nil
	}
	out := &catalogv1.CapabilityObservation{
		Reason:           v.GetReason(),
		ReasonMessage:    v.GetReasonMessage(),
		ObservedAt:       v.GetObservedAt(),
		ObservationStale: v.GetObservationStale(),
	}
	return out
}

func wirePlatformNetworkCapabilities(v *networkv1.PlatformNetworkCapabilities) *catalogv1.PlatformNetworkCapabilities {
	if v == nil {
		return nil
	}
	out := &catalogv1.PlatformNetworkCapabilities{
		BaseConnectivityReady: v.GetBaseConnectivityReady(),
		PublicAddressReady:    v.GetPublicAddressReady(),
		LoadBalancerReady:     v.GetLoadBalancerReady(),
		BaseConnectivity:      wireCapabilityObservation(v.GetBaseConnectivity()),
		PublicAddress:         wireCapabilityObservation(v.GetPublicAddress()),
		LoadBalancer:          wireCapabilityObservation(v.GetLoadBalancer()),
	}
	return out
}

func wirePublicPoolVerification(v *networkv1.PublicPoolVerification) *catalogv1.PublicPoolVerification {
	if v == nil {
		return nil
	}
	out := &catalogv1.PublicPoolVerification{
		ProviderSourceRevision: v.GetProviderSourceRevision(),
		ProviderImageDigests:   v.GetProviderImageDigests(),
		TopologyFingerprint:    v.GetTopologyFingerprint(),
		EvidenceReference:      v.GetEvidenceReference(),
		Scope:                  v.GetScope(),
		VerifiedAt:             v.GetVerifiedAt(),
		ExpiresAt:              v.GetExpiresAt(),
	}
	return out
}

func inputPublicPoolVerification(v *catalogv1.PublicPoolVerification) *networkv1.PublicPoolVerification {
	if v == nil {
		return nil
	}
	out := &networkv1.PublicPoolVerification{
		ProviderSourceRevision: v.GetProviderSourceRevision(),
		ProviderImageDigests:   v.GetProviderImageDigests(),
		TopologyFingerprint:    v.GetTopologyFingerprint(),
		EvidenceReference:      v.GetEvidenceReference(),
		Scope:                  v.GetScope(),
		VerifiedAt:             v.GetVerifiedAt(),
		ExpiresAt:              v.GetExpiresAt(),
	}
	return out
}

func wirePlatformOperation(v *networkv1.PlatformOperation) *catalogv1.PlatformOperation {
	if v == nil {
		return nil
	}
	out := &catalogv1.PlatformOperation{
		Id:            v.GetId(),
		ResourceId:    v.GetResourceId(),
		Kind:          strings.ToLower(strings.TrimPrefix(v.GetKind().String(), "OPERATION_KIND_")),
		State:         strings.ToLower(strings.TrimPrefix(v.GetState().String(), "OPERATION_STATE_")),
		Reason:        v.GetReason(),
		ReasonMessage: v.GetReasonMessage(),
		CreatedAt:     v.GetCreatedAt(),
		UpdatedAt:     v.GetUpdatedAt(),
		CompletedAt:   v.GetCompletedAt(),
		NextAttemptAt: v.GetNextAttemptAt(),
	}
	return out
}

func (s *PlatformNetworkService) ListNodeInterfaces(ctx context.Context, req *catalogv1.ListNodeInterfacesRequest) (*catalogv1.ListNodeInterfacesResponse, error) {
	actor, err := s.actor(ctx, "ListNodeInterfaces")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	reply, err := s.client.ListNodeInterfaces(ctx, actor, &networkv1.ListNodeInterfacesRequest{NodeName: req.GetNodeName(), ClusterId: req.GetClusterId(), VirtualSwitchId: req.GetVirtualSwitchId()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if reply.Total < 0 || reply.Total < int64(len(reply.Items)) {
		return nil, invalidNetworkReply()
	}
	out := &catalogv1.ListNodeInterfacesResponse{Total: reply.Total, InventoryFingerprint: reply.GetInventoryFingerprint()}
	for _, v := range reply.Items {
		if v == nil {
			return nil, invalidNetworkReply()
		}
		out.Items = append(out.Items, wireNodeInterface(v))
	}
	return out, nil
}

func (s *PlatformNetworkService) AdoptNetworkDevice(ctx context.Context, req *catalogv1.AdoptNetworkDeviceRequest) (*catalogv1.AdoptNetworkDeviceResponse, error) {
	actor, err := s.actor(ctx, "AdoptNetworkDevice")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	reply, err := s.client.AdoptNetworkDevice(ctx, actor, &networkv1.AdoptNetworkDeviceRequest{Name: req.Data.GetName(), DeviceName: req.Data.GetDeviceName(), InventoryFingerprint: req.Data.GetInventoryFingerprint(), IdempotencyKey: req.Data.GetIdempotencyKey()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, "", "device") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.AdoptNetworkDeviceResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) GetNetworkDevice(ctx context.Context, req *catalogv1.GetNetworkDeviceRequest) (*catalogv1.GetNetworkDeviceResponse, error) {
	actor, err := s.actor(ctx, "GetNetworkDevice")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !auth.ValidPlatformNetworkID("device_id", req.GetDeviceId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.GetNetworkDevice(ctx, actor, &networkv1.GetNetworkDeviceRequest{DeviceId: req.GetDeviceId()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetDeviceId(), "device") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetNetworkDeviceResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) CreateVlanNetwork(ctx context.Context, req *catalogv1.CreateVlanNetworkRequest) (*catalogv1.CreateVlanNetworkResponse, error) {
	actor, err := s.actor(ctx, "CreateVlanNetwork")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	// Keep both versions explicit. The existing HTTP validator rejects mixed or
	// incomplete v2 input, and Resource owns the same admission constraint.
	reply, err := s.client.CreateVlanNetwork(ctx, actor, &networkv1.CreateVlanNetworkRequest{Name: req.Data.GetName(), Description: req.Data.GetDescription(), DeviceId: req.Data.GetDeviceId(), VlanId: req.Data.GetVlanId(), IdempotencyKey: req.Data.GetIdempotencyKey(), ClusterId: req.Data.GetClusterId(), VirtualSwitchId: req.Data.GetVirtualSwitchId(), DeviceName: req.Data.GetDeviceName()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, "", "vlan") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.CreateVlanNetworkResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) GetVlanNetwork(ctx context.Context, req *catalogv1.GetVlanNetworkRequest) (*catalogv1.GetVlanNetworkResponse, error) {
	actor, err := s.actor(ctx, "GetVlanNetwork")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !auth.ValidPlatformNetworkID("vlan_network_id", req.GetVlanNetworkId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.GetVlanNetwork(ctx, actor, &networkv1.GetVlanNetworkRequest{VlanNetworkId: req.GetVlanNetworkId()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetVlanNetworkId(), "vlan") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetVlanNetworkResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) ListVlanNetworks(ctx context.Context, req *catalogv1.ListVlanNetworksRequest) (*catalogv1.ListVlanNetworksResponse, error) {
	actor, err := s.actor(ctx, "ListVlanNetworks")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !validPlatformList(req.GetLimit(), req.GetCursor()) {
		return nil, errors.BadRequest("INVALID_PAGINATION", "invalid limit or cursor")
	}
	state, err := platformListState(req.GetState())
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListVlanNetworks(ctx, actor, &networkv1.ListVlanNetworksRequest{Name: req.GetName(), State: state, Limit: req.GetLimit(), Cursor: req.GetCursor()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if reply.Total < 0 || reply.Total < int64(len(reply.Items)) {
		return nil, invalidNetworkReply()
	}
	out := &catalogv1.ListVlanNetworksResponse{Total: reply.Total, NextCursor: reply.GetNextCursor()}
	limit := req.GetLimit()
	if limit == 0 {
		limit = 20
	}
	if len(reply.Items) > int(limit) || len(reply.NextCursor) > 4096 || (reply.NextCursor != "" && len(reply.Items) == 0) {
		return nil, invalidNetworkReply()
	}
	for _, v := range reply.Items {
		if !validPlatformResource(v, "", "vlan") {
			return nil, invalidNetworkReply()
		}
		out.Items = append(out.Items, wirePlatformResource(v))
	}
	return out, nil
}

func (s *PlatformNetworkService) DeleteVlanNetwork(ctx context.Context, req *catalogv1.DeleteVlanNetworkRequest) (*catalogv1.DeleteVlanNetworkResponse, error) {
	actor, err := s.actor(ctx, "DeleteVlanNetwork")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !auth.ValidPlatformNetworkID("vlan_network_id", req.GetVlanNetworkId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.DeleteVlanNetwork(ctx, actor, &networkv1.DeleteVlanNetworkRequest{VlanNetworkId: req.GetVlanNetworkId()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetVlanNetworkId(), "vlan") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.DeleteVlanNetworkResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) CreateEgressGateway(ctx context.Context, req *catalogv1.CreateEgressGatewayRequest) (*catalogv1.CreateEgressGatewayResponse, error) {
	actor, err := s.actor(ctx, "CreateEgressGateway")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	reply, err := s.client.CreateEgressGateway(ctx, actor, &networkv1.CreateEgressGatewayRequest{Name: req.Data.GetName(), Description: req.Data.GetDescription(), IdempotencyKey: req.Data.GetIdempotencyKey()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, "", "gateway") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.CreateEgressGatewayResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) GetEgressGateway(ctx context.Context, req *catalogv1.GetEgressGatewayRequest) (*catalogv1.GetEgressGatewayResponse, error) {
	actor, err := s.actor(ctx, "GetEgressGateway")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !auth.ValidPlatformNetworkID("gateway_id", req.GetGatewayId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.GetEgressGateway(ctx, actor, &networkv1.GetEgressGatewayRequest{GatewayId: req.GetGatewayId()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetGatewayId(), "gateway") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetEgressGatewayResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) ListEgressGateways(ctx context.Context, req *catalogv1.ListEgressGatewaysRequest) (*catalogv1.ListEgressGatewaysResponse, error) {
	actor, err := s.actor(ctx, "ListEgressGateways")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !validPlatformList(req.GetLimit(), req.GetCursor()) {
		return nil, errors.BadRequest("INVALID_PAGINATION", "invalid limit or cursor")
	}
	state, err := platformListState(req.GetState())
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListEgressGateways(ctx, actor, &networkv1.ListEgressGatewaysRequest{Name: req.GetName(), State: state, Limit: req.GetLimit(), Cursor: req.GetCursor()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if reply.Total < 0 || reply.Total < int64(len(reply.Items)) {
		return nil, invalidNetworkReply()
	}
	out := &catalogv1.ListEgressGatewaysResponse{Total: reply.Total, NextCursor: reply.GetNextCursor()}
	limit := req.GetLimit()
	if limit == 0 {
		limit = 20
	}
	if len(reply.Items) > int(limit) || len(reply.NextCursor) > 4096 || (reply.NextCursor != "" && len(reply.Items) == 0) {
		return nil, invalidNetworkReply()
	}
	for _, v := range reply.Items {
		if !validPlatformResource(v, "", "gateway") {
			return nil, invalidNetworkReply()
		}
		out.Items = append(out.Items, wirePlatformResource(v))
	}
	return out, nil
}

func (s *PlatformNetworkService) DeleteEgressGateway(ctx context.Context, req *catalogv1.DeleteEgressGatewayRequest) (*catalogv1.DeleteEgressGatewayResponse, error) {
	actor, err := s.actor(ctx, "DeleteEgressGateway")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !auth.ValidPlatformNetworkID("gateway_id", req.GetGatewayId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.DeleteEgressGateway(ctx, actor, &networkv1.DeleteEgressGatewayRequest{GatewayId: req.GetGatewayId()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetGatewayId(), "gateway") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.DeleteEgressGatewayResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) CreatePublicAddressPool(ctx context.Context, req *catalogv1.CreatePublicAddressPoolRequest) (*catalogv1.CreatePublicAddressPoolResponse, error) {
	actor, err := s.actor(ctx, "CreatePublicAddressPool")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	mode, err := publicPoolMode(req.Data.GetMode())
	if err != nil {
		return nil, err
	}
	reply, err := s.client.CreatePublicAddressPool(ctx, actor, &networkv1.CreatePublicAddressPoolRequest{Name: req.Data.GetName(), Description: req.Data.GetDescription(), Mode: mode, GatewayId: req.Data.GetGatewayId(), Cidr: req.Data.GetCidr(), OvnGatewayIp: req.Data.GetOvnGatewayIp(), ExcludedIps: req.Data.GetExcludedIps(), VlanNetworkId: req.Data.GetVlanNetworkId(), UpstreamGatewayIp: req.Data.GetUpstreamGatewayIp(), IdempotencyKey: req.Data.GetIdempotencyKey()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, "", "public") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.CreatePublicAddressPoolResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) GetPublicAddressPool(ctx context.Context, req *catalogv1.GetPublicAddressPoolRequest) (*catalogv1.GetPublicAddressPoolResponse, error) {
	actor, err := s.actor(ctx, "GetPublicAddressPool")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !auth.ValidPlatformNetworkID("pool_id", req.GetPoolId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.GetPublicAddressPool(ctx, actor, &networkv1.GetPublicAddressPoolRequest{PoolId: req.GetPoolId()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetPoolId(), "public") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetPublicAddressPoolResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) ListPublicAddressPools(ctx context.Context, req *catalogv1.ListPublicAddressPoolsRequest) (*catalogv1.ListPublicAddressPoolsResponse, error) {
	actor, err := s.actor(ctx, "ListPublicAddressPools")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !validPlatformList(req.GetLimit(), req.GetCursor()) {
		return nil, errors.BadRequest("INVALID_PAGINATION", "invalid limit or cursor")
	}
	state, err := platformListState(req.GetState())
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListPublicAddressPools(ctx, actor, &networkv1.ListPublicAddressPoolsRequest{Name: req.GetName(), State: state, Limit: req.GetLimit(), Cursor: req.GetCursor()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if reply.Total < 0 || reply.Total < int64(len(reply.Items)) {
		return nil, invalidNetworkReply()
	}
	out := &catalogv1.ListPublicAddressPoolsResponse{Total: reply.Total, NextCursor: reply.GetNextCursor()}
	limit := req.GetLimit()
	if limit == 0 {
		limit = 20
	}
	if len(reply.Items) > int(limit) || len(reply.NextCursor) > 4096 || (reply.NextCursor != "" && len(reply.Items) == 0) {
		return nil, invalidNetworkReply()
	}
	for _, v := range reply.Items {
		if !validPlatformResource(v, "", "public") {
			return nil, invalidNetworkReply()
		}
		out.Items = append(out.Items, wirePlatformResource(v))
	}
	return out, nil
}

func (s *PlatformNetworkService) DeletePublicAddressPool(ctx context.Context, req *catalogv1.DeletePublicAddressPoolRequest) (*catalogv1.DeletePublicAddressPoolResponse, error) {
	actor, err := s.actor(ctx, "DeletePublicAddressPool")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !auth.ValidPlatformNetworkID("pool_id", req.GetPoolId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.DeletePublicAddressPool(ctx, actor, &networkv1.DeletePublicAddressPoolRequest{PoolId: req.GetPoolId()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetPoolId(), "public") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.DeletePublicAddressPoolResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) RecordPublicPoolVerification(ctx context.Context, req *catalogv1.RecordPublicPoolVerificationRequest) (*catalogv1.RecordPublicPoolVerificationResponse, error) {
	actor, err := s.actor(ctx, "RecordPublicPoolVerification")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	if !auth.ValidPlatformNetworkID("pool_id", req.GetPoolId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.RecordPublicPoolVerification(ctx, actor, &networkv1.RecordPublicPoolVerificationRequest{PoolId: req.GetPoolId(), ExpectedVersion: req.Data.GetExpectedVersion(), Verification: inputPublicPoolVerification(req.Data.GetVerification()), IdempotencyKey: req.Data.GetIdempotencyKey()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetPoolId(), "public") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.RecordPublicPoolVerificationResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) SetPublicPoolAllocationEnabled(ctx context.Context, req *catalogv1.SetPublicPoolAllocationEnabledRequest) (*catalogv1.SetPublicPoolAllocationEnabledResponse, error) {
	actor, err := s.actor(ctx, "SetPublicPoolAllocationEnabled")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	if !auth.ValidPlatformNetworkID("pool_id", req.GetPoolId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.SetPublicPoolAllocationEnabled(ctx, actor, &networkv1.SetPublicPoolAllocationEnabledRequest{PoolId: req.GetPoolId(), Enabled: req.Data.GetEnabled(), ExpectedVersion: req.Data.GetExpectedVersion(), IdempotencyKey: req.Data.GetIdempotencyKey()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetPoolId(), "public") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.SetPublicPoolAllocationEnabledResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) SetDefaultPublicPool(ctx context.Context, req *catalogv1.SetDefaultPublicPoolRequest) (*catalogv1.SetDefaultPublicPoolResponse, error) {
	actor, err := s.actor(ctx, "SetDefaultPublicPool")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	if !auth.ValidPlatformNetworkID("pool_id", req.GetPoolId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.SetDefaultPublicPool(ctx, actor, &networkv1.SetDefaultPublicPoolRequest{PoolId: req.GetPoolId(), ExpectedVersion: req.Data.GetExpectedVersion(), IdempotencyKey: req.Data.GetIdempotencyKey()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetPoolId(), "public") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.SetDefaultPublicPoolResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) CreateIntranetAddressPool(ctx context.Context, req *catalogv1.CreateIntranetAddressPoolRequest) (*catalogv1.CreateIntranetAddressPoolResponse, error) {
	actor, err := s.actor(ctx, "CreateIntranetAddressPool")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	reply, err := s.client.CreateIntranetAddressPool(ctx, actor, &networkv1.CreateIntranetAddressPoolRequest{Name: req.Data.GetName(), Description: req.Data.GetDescription(), Cidr: req.Data.GetCidr(), OvnGatewayIp: req.Data.GetOvnGatewayIp(), ExcludedIps: req.Data.GetExcludedIps(), DefaultVpcName: req.Data.GetDefaultVpcName(), DefaultVpcUid: req.Data.GetDefaultVpcUid(), IntranetNetworks: req.Data.GetIntranetNetworks(), IdempotencyKey: req.Data.GetIdempotencyKey()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, "", "intranet") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.CreateIntranetAddressPoolResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) GetIntranetAddressPool(ctx context.Context, req *catalogv1.GetIntranetAddressPoolRequest) (*catalogv1.GetIntranetAddressPoolResponse, error) {
	actor, err := s.actor(ctx, "GetIntranetAddressPool")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !auth.ValidPlatformNetworkID("pool_id", req.GetPoolId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.GetIntranetAddressPool(ctx, actor, &networkv1.GetIntranetAddressPoolRequest{PoolId: req.GetPoolId()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetPoolId(), "intranet") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetIntranetAddressPoolResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) ListIntranetAddressPools(ctx context.Context, req *catalogv1.ListIntranetAddressPoolsRequest) (*catalogv1.ListIntranetAddressPoolsResponse, error) {
	actor, err := s.actor(ctx, "ListIntranetAddressPools")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !validPlatformList(req.GetLimit(), req.GetCursor()) {
		return nil, errors.BadRequest("INVALID_PAGINATION", "invalid limit or cursor")
	}
	state, err := platformListState(req.GetState())
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListIntranetAddressPools(ctx, actor, &networkv1.ListIntranetAddressPoolsRequest{Name: req.GetName(), State: state, Limit: req.GetLimit(), Cursor: req.GetCursor()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if reply.Total < 0 || reply.Total < int64(len(reply.Items)) {
		return nil, invalidNetworkReply()
	}
	out := &catalogv1.ListIntranetAddressPoolsResponse{Total: reply.Total, NextCursor: reply.GetNextCursor()}
	limit := req.GetLimit()
	if limit == 0 {
		limit = 20
	}
	if len(reply.Items) > int(limit) || len(reply.NextCursor) > 4096 || (reply.NextCursor != "" && len(reply.Items) == 0) {
		return nil, invalidNetworkReply()
	}
	for _, v := range reply.Items {
		if !validPlatformResource(v, "", "intranet") {
			return nil, invalidNetworkReply()
		}
		out.Items = append(out.Items, wirePlatformResource(v))
	}
	return out, nil
}

func (s *PlatformNetworkService) DeleteIntranetAddressPool(ctx context.Context, req *catalogv1.DeleteIntranetAddressPoolRequest) (*catalogv1.DeleteIntranetAddressPoolResponse, error) {
	actor, err := s.actor(ctx, "DeleteIntranetAddressPool")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !auth.ValidPlatformNetworkID("pool_id", req.GetPoolId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.DeleteIntranetAddressPool(ctx, actor, &networkv1.DeleteIntranetAddressPoolRequest{PoolId: req.GetPoolId()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetPoolId(), "intranet") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.DeleteIntranetAddressPoolResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) RecordIntranetPoolVerification(ctx context.Context, req *catalogv1.RecordIntranetPoolVerificationRequest) (*catalogv1.RecordIntranetPoolVerificationResponse, error) {
	actor, err := s.actor(ctx, "RecordIntranetPoolVerification")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	if !auth.ValidPlatformNetworkID("pool_id", req.GetPoolId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.RecordIntranetPoolVerification(ctx, actor, &networkv1.RecordIntranetPoolVerificationRequest{PoolId: req.GetPoolId(), ExpectedVersion: req.Data.GetExpectedVersion(), Verification: inputIntranetPoolVerification(req.Data.GetVerification()), IdempotencyKey: req.Data.GetIdempotencyKey()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetPoolId(), "intranet") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.RecordIntranetPoolVerificationResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) SetIntranetPoolAllocationEnabled(ctx context.Context, req *catalogv1.SetIntranetPoolAllocationEnabledRequest) (*catalogv1.SetIntranetPoolAllocationEnabledResponse, error) {
	actor, err := s.actor(ctx, "SetIntranetPoolAllocationEnabled")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	if !auth.ValidPlatformNetworkID("pool_id", req.GetPoolId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.SetIntranetPoolAllocationEnabled(ctx, actor, &networkv1.SetIntranetPoolAllocationEnabledRequest{PoolId: req.GetPoolId(), Enabled: req.Data.GetEnabled(), ExpectedVersion: req.Data.GetExpectedVersion(), IdempotencyKey: req.Data.GetIdempotencyKey()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetPoolId(), "intranet") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.SetIntranetPoolAllocationEnabledResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) SetDefaultIntranetPool(ctx context.Context, req *catalogv1.SetDefaultIntranetPoolRequest) (*catalogv1.SetDefaultIntranetPoolResponse, error) {
	actor, err := s.actor(ctx, "SetDefaultIntranetPool")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	if !auth.ValidPlatformNetworkID("pool_id", req.GetPoolId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.SetDefaultIntranetPool(ctx, actor, &networkv1.SetDefaultIntranetPoolRequest{PoolId: req.GetPoolId(), ExpectedVersion: req.Data.GetExpectedVersion(), IdempotencyKey: req.Data.GetIdempotencyKey()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if !validPlatformResource(reply.Resource, req.GetPoolId(), "intranet") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.SetDefaultIntranetPoolResponse{Resource: wirePlatformResource(reply.Resource)}, nil
}

func (s *PlatformNetworkService) GetPlatformNetworkCapabilities(ctx context.Context, req *catalogv1.GetPlatformNetworkCapabilitiesRequest) (*catalogv1.GetPlatformNetworkCapabilitiesResponse, error) {
	actor, err := s.actor(ctx, "GetPlatformNetworkCapabilities")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	reply, err := s.client.GetPlatformNetworkCapabilities(ctx, actor, &networkv1.GetPlatformNetworkCapabilitiesRequest{})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if reply.GetCapabilities() == nil {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetPlatformNetworkCapabilitiesResponse{Capabilities: wirePlatformNetworkCapabilities(reply.GetCapabilities())}, nil
}

func (s *PlatformNetworkService) GetPlatformOperation(ctx context.Context, req *catalogv1.GetPlatformOperationRequest) (*catalogv1.GetPlatformOperationResponse, error) {
	actor, err := s.actor(ctx, "GetPlatformOperation")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	if !auth.ValidPlatformNetworkID("operation_id", req.GetOperationId()) {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_ID", "invalid resource ID")
	}
	reply, err := s.client.GetPlatformOperation(ctx, actor, &networkv1.GetPlatformOperationRequest{OperationId: req.GetOperationId()})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	if reply.GetOperation() == nil {
		return nil, invalidNetworkReply()
	}
	if reply.Operation.Id != req.OperationId {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetPlatformOperationResponse{Operation: wirePlatformOperation(reply.GetOperation())}, nil
}

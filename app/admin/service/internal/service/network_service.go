package service

import (
	"context"
	"github.com/go-kratos/kratos/v2/errors"
	klog "github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/transport"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	adminv1 "go-wind-admin/api/gen/go/admin/service/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"regexp"
	"strings"
)

// ResourceTenantResolver 把治理中心 uint32 租户主键换为下游服务的 resource tenant UUID。
// 原先定义在已删除的 model_service.go 中，现随 Network 接入保留在此；
// 后续其他下游接入复用本接口，勿重复声明。
type ResourceTenantResolver interface {
	ResourceTenantID(context.Context, uint32) (string, error)
}
type VPCGetter interface {
	GetVPC(context.Context, string, string, string) (*networkv1.GetVPCResponse, error)
}

// NetworkTenantClient is the tenant-scoped downstream surface the BFF may call.
// Every method replays the resolved tenant UUID and verified actor; the BFF
// never passes request-derived tenant identity downstream.
type NetworkTenantClient interface {
	ListVPCCIDRPresets(context.Context, string, string) (*networkv1.ListVPCCIDRPresetsResponse, error)
	VPCGetter
	ListVPCs(context.Context, string, string, string, string, int32, string) (*networkv1.ListVPCsResponse, error)
	CreateVPC(context.Context, string, string, string, string, string, string) (*networkv1.CreateVPCResponse, error)
	DeleteVPC(context.Context, string, string, string) (*networkv1.DeleteVPCResponse, error)
	GetOperation(context.Context, string, string, string) (*networkv1.GetOperationResponse, error)
	GetEIP(context.Context, string, string, string) (*networkv1.GetEIPResponse, error)
	ListEIPs(context.Context, string, string, string, string, int32, string) (*networkv1.ListEIPsResponse, error)
	CreateEIP(context.Context, string, string, string, string, string) (*networkv1.CreateEIPResponse, error)
	DeleteEIP(context.Context, string, string, string) (*networkv1.DeleteEIPResponse, error)
	GetVPCSnat(context.Context, string, string, string) (*networkv1.GetVPCSnatResponse, error)
	BindVPCSnat(context.Context, string, string, string, string, string) (*networkv1.BindVPCSnatResponse, error)
	CreateSubnet(context.Context, string, string, string, string, string, string, string, *string) (*networkv1.CreateSubnetResponse, error)
	GetSubnet(context.Context, string, string, string) (*networkv1.GetSubnetResponse, error)
	ListSubnets(context.Context, string, string, string, string, string, int32, string) (*networkv1.ListSubnetsResponse, error)
	DeleteSubnet(context.Context, string, string, string) (*networkv1.DeleteSubnetResponse, error)
	GetVPCSnatBinding(context.Context, string, string, string) (*networkv1.GetVPCSnatBindingResponse, error)
	SetVPCSnatEnabled(context.Context, string, string, string, bool, int64, string) (*networkv1.SetVPCSnatEnabledResponse, error)
	DeleteVPCSnatBinding(context.Context, string, string, string) (*networkv1.DeleteVPCSnatBindingResponse, error)
	CreateLoadBalancer(context.Context, string, string, *networkv1.CreateLoadBalancerRequest) (*networkv1.CreateLoadBalancerResponse, error)
	GetLoadBalancer(context.Context, string, string, string) (*networkv1.GetLoadBalancerResponse, error)
	ListLoadBalancers(context.Context, string, string, *networkv1.ListLoadBalancersRequest) (*networkv1.ListLoadBalancersResponse, error)
	UpdateLoadBalancer(context.Context, string, string, *networkv1.UpdateLoadBalancerRequest) (*networkv1.UpdateLoadBalancerResponse, error)
	DeleteLoadBalancer(context.Context, string, string, string) (*networkv1.DeleteLoadBalancerResponse, error)
	GetLoadBalancerOperation(context.Context, string, string, string) (*networkv1.GetLoadBalancerOperationResponse, error)
}
type NetworkService struct {
	adminv1.UnimplementedNetworkServiceServer
	client  NetworkTenantClient
	tenants ResourceTenantResolver
}

func NewNetworkService(client NetworkTenantClient, tenants ResourceTenantResolver) *NetworkService {
	return &NetworkService{client: client, tenants: tenants}
}

var vpcIDPattern = regexp.MustCompile(`^vpc_[0-9a-f]{32}$`)
var subnetIDPattern = regexp.MustCompile(`^subnet_[0-9a-f]{32}$`)
var snatIDPattern = regexp.MustCompile(`^snat_[0-9a-f]{32}$`)
var loadBalancerIDPattern = regexp.MustCompile(`^lb_[0-9a-f]{32}$`)
var eipIDPattern = regexp.MustCompile(`^eip_[0-9a-f]{32}$`)
var operationIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// trustedOperator re-authenticates the caller identity shared by every Network
// BFF method; the tenant UUID is resolved from the persisted tenant record.
func trustedOperator(ctx context.Context, s *NetworkService) (*auth.Principal, string, error) {
	operator, err := auth.PrincipalFromContext(ctx)
	if err != nil || operator == nil {
		return nil, "", errors.Unauthorized("INVALID_LOGIN", "login required")
	}
	if operator.TenantID == 0 || operator.ID == 0 {
		return nil, "", errors.Forbidden("TENANT_REQUIRED", "tenant identity required")
	}
	if s.client == nil || s.tenants == nil {
		return nil, "", errors.ServiceUnavailable("NETWORK_UNAVAILABLE", "network read access is not configured")
	}
	if _, err := operator.Actor(); err != nil {
		return nil, "", err
	}
	tenant, err := s.tenants.ResourceTenantID(ctx, operator.TenantID)
	if err != nil {
		return nil, "", err
	}
	return operator, tenant, nil
}

func (s *NetworkService) GetVPC(ctx context.Context, req *catalogv1.GetVPCRequest) (*catalogv1.GetVPCResponse, error) {
	if req == nil || !vpcIDPattern.MatchString(req.GetVpcId()) {
		return nil, errors.BadRequest("INVALID_VPC_ID", "invalid VPC ID")
	}
	if tr, ok := transport.FromServerContext(ctx); ok {
		if ht, ok := tr.(*khttp.Transport); ok {
			if err := auth.ValidateVPCReadRequest(ht.Request()); err != nil {
				return nil, err
			}
		}
	}
	_, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := actorOf(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetVPC(ctx, tenant, actor, req.GetVpcId())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Vpc == nil || reply.Vpc.TenantId != tenant || reply.Vpc.Id != req.GetVpcId() || reply.Vpc.State < networkv1.ResourceState_RESOURCE_STATE_PROVISIONING || reply.Vpc.State > networkv1.ResourceState_RESOURCE_STATE_DELETED {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
	}
	v := reply.Vpc
	return &catalogv1.GetVPCResponse{Vpc: wireVPC(v)}, nil
}

// actorOf re-derives the actor after trustedOperator validated the principal.
func actorOf(ctx context.Context) (string, error) {
	operator, err := auth.PrincipalFromContext(ctx)
	if err != nil || operator == nil {
		return "", errors.Unauthorized("INVALID_LOGIN", "login required")
	}
	return operator.Actor()
}

func wireVPC(v *networkv1.VPC) *catalogv1.VPC {
	return &catalogv1.VPC{Id: v.Id, Name: v.Name, Description: v.Description, Cidr: v.Cidr,
		State: strings.ToLower(strings.TrimPrefix(v.State.String(), "RESOURCE_STATE_")), Reason: v.Reason,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, Version: v.Version, ObservedAt: v.ObservedAt,
		ObservationStale: v.ObservationStale, LastOperationId: v.LastOperationId, SubnetCount: v.SubnetCount}
}

func wireEIP(v *networkv1.EIP) *catalogv1.EIP {
	r := &catalogv1.EIP{Id: v.Id, Name: v.Name, Description: v.Description,
		State: strings.ToLower(strings.TrimPrefix(v.State.String(), "RESOURCE_STATE_")), Reason: v.Reason,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, Version: v.Version, ObservedAt: v.ObservedAt,
		ObservationStale: v.ObservationStale, LastOperationId: v.LastOperationId, Address: v.Address,
		BindingId: v.BindingId, BindingState: v.BindingState, Scope: v.Scope, ManagedBy: v.ManagedBy}
	if v.BindingTarget != nil {
		r.BindingTarget = &catalogv1.EIPBindingTarget{Kind: v.BindingTarget.Kind, Id: v.BindingTarget.Id, State: v.BindingTarget.State}
	}
	return r
}

func wireSnat(v *networkv1.VPCSnatBinding) *catalogv1.VPCSnat {
	return &catalogv1.VPCSnat{Id: v.Id, VpcId: v.VpcId, EipId: v.EipId, EipAddress: v.EipAddress,
		State: strings.ToLower(strings.TrimPrefix(v.State.String(), "RESOURCE_STATE_")), Reason: v.Reason,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, Version: v.Version,
		DesiredEnabled: v.DesiredEnabled, AppliedEnabled: v.AppliedEnabled,
		ObservedAt: v.ObservedAt, ObservationStale: v.ObservationStale, LastOperationId: v.LastOperationId, Purpose: v.Purpose, ReasonMessage: v.ReasonMessage}
}

func wireOperation(v *networkv1.Operation) *catalogv1.Operation {
	return &catalogv1.Operation{Id: v.Id, ResourceId: v.ResourceId, ResourceType: strings.ToLower(strings.TrimPrefix(v.ResourceType.String(), "RESOURCE_TYPE_")),
		Kind: strings.ToLower(strings.TrimPrefix(v.Kind.String(), "OPERATION_KIND_")), State: strings.ToLower(strings.TrimPrefix(v.State.String(), "OPERATION_STATE_")),
		Reason: v.Reason, ReasonMessage: v.ReasonMessage, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, CompletedAt: v.CompletedAt, NextAttemptAt: v.NextAttemptAt}
}

func (s *NetworkService) ListVPCs(ctx context.Context, req *catalogv1.ListVPCsRequest) (*catalogv1.ListVPCsResponse, error) {
	_, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := actorOf(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListVPCs(ctx, tenant, actor, req.GetName(), strings.ToUpper(req.GetState()), req.GetLimit(), req.GetCursor())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
	}
	out := &catalogv1.ListVPCsResponse{NextCursor: reply.GetNextCursor(), Total: reply.GetTotal()}
	for _, v := range reply.GetItems() {
		if v == nil || v.TenantId != tenant {
			return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
		}
		out.Items = append(out.Items, wireVPC(v))
	}
	return out, nil
}

func (s *NetworkService) CreateVPC(ctx context.Context, req *catalogv1.CreateVPCRequest) (*catalogv1.CreateVPCResponse, error) {
	_, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := actorOf(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.CreateVPC(ctx, tenant, actor, req.GetName(), req.GetCidr(), req.GetDescription(), req.GetIdempotencyKey())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Vpc == nil || reply.Vpc.TenantId != tenant {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
	}
	return &catalogv1.CreateVPCResponse{Vpc: wireVPC(reply.Vpc)}, nil
}

func (s *NetworkService) DeleteVPC(ctx context.Context, req *catalogv1.DeleteVPCRequest) (*catalogv1.DeleteVPCResponse, error) {
	if req == nil || !vpcIDPattern.MatchString(req.GetVpcId()) {
		return nil, errors.BadRequest("INVALID_VPC_ID", "invalid VPC ID")
	}
	_, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := actorOf(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.DeleteVPC(ctx, tenant, actor, req.GetVpcId())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Vpc == nil || reply.Vpc.TenantId != tenant || reply.Vpc.Id != req.GetVpcId() {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
	}
	return &catalogv1.DeleteVPCResponse{Vpc: wireVPC(reply.Vpc)}, nil
}

func (s *NetworkService) GetOperation(ctx context.Context, req *catalogv1.GetOperationRequest) (*catalogv1.GetOperationResponse, error) {
	if req == nil || !operationIDPattern.MatchString(req.GetOperationId()) {
		return nil, errors.BadRequest("INVALID_OPERATION_ID", "invalid operation ID")
	}
	_, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := actorOf(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetOperation(ctx, tenant, actor, req.GetOperationId())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Operation == nil || reply.Operation.TenantId != tenant || reply.Operation.Id != req.GetOperationId() {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
	}
	return &catalogv1.GetOperationResponse{Operation: wireOperation(reply.Operation)}, nil
}

func (s *NetworkService) GetEIP(ctx context.Context, req *catalogv1.GetEIPRequest) (*catalogv1.GetEIPResponse, error) {
	if req == nil || !eipIDPattern.MatchString(req.GetEipId()) {
		return nil, errors.BadRequest("INVALID_EIP_ID", "invalid EIP ID")
	}
	_, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := actorOf(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetEIP(ctx, tenant, actor, req.GetEipId())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Eip == nil || reply.Eip.TenantId != tenant || reply.Eip.Id != req.GetEipId() {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
	}
	return &catalogv1.GetEIPResponse{Eip: wireEIP(reply.Eip)}, nil
}

func (s *NetworkService) ListEIPs(ctx context.Context, req *catalogv1.ListEIPsRequest) (*catalogv1.ListEIPsResponse, error) {
	_, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := actorOf(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListEIPs(ctx, tenant, actor, req.GetName(), strings.ToUpper(req.GetState()), req.GetLimit(), req.GetCursor())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
	}
	out := &catalogv1.ListEIPsResponse{NextCursor: reply.GetNextCursor(), Total: reply.GetTotal()}
	for _, v := range reply.GetItems() {
		if v == nil || v.TenantId != tenant {
			return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
		}
		out.Items = append(out.Items, wireEIP(v))
	}
	return out, nil
}

func (s *NetworkService) CreateEIP(ctx context.Context, req *catalogv1.CreateEIPRequest) (*catalogv1.CreateEIPResponse, error) {
	_, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := actorOf(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.CreateEIP(ctx, tenant, actor, req.GetName(), req.GetDescription(), req.GetIdempotencyKey())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Eip == nil || reply.Eip.TenantId != tenant {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
	}
	return &catalogv1.CreateEIPResponse{Eip: wireEIP(reply.Eip)}, nil
}

func (s *NetworkService) DeleteEIP(ctx context.Context, req *catalogv1.DeleteEIPRequest) (*catalogv1.DeleteEIPResponse, error) {
	if req == nil || !eipIDPattern.MatchString(req.GetEipId()) {
		return nil, errors.BadRequest("INVALID_EIP_ID", "invalid EIP ID")
	}
	_, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := actorOf(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.DeleteEIP(ctx, tenant, actor, req.GetEipId())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Eip == nil || reply.Eip.TenantId != tenant || reply.Eip.Id != req.GetEipId() {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
	}
	return &catalogv1.DeleteEIPResponse{Eip: wireEIP(reply.Eip)}, nil
}

func (s *NetworkService) GetVPCSnat(ctx context.Context, req *catalogv1.GetVPCSnatRequest) (*catalogv1.GetVPCSnatResponse, error) {
	if req == nil || !vpcIDPattern.MatchString(req.GetVpcId()) {
		return nil, errors.BadRequest("INVALID_VPC_ID", "invalid VPC ID")
	}
	_, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := actorOf(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetVPCSnat(ctx, tenant, actor, req.GetVpcId())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Binding == nil || reply.Binding.TenantId != tenant || reply.Binding.VpcId != req.GetVpcId() {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
	}
	return &catalogv1.GetVPCSnatResponse{Snat: wireSnat(reply.Binding)}, nil
}

func (s *NetworkService) BindVPCSnat(ctx context.Context, req *catalogv1.BindVPCSnatRequest) (*catalogv1.BindVPCSnatResponse, error) {
	if req == nil || !vpcIDPattern.MatchString(req.GetVpcId()) || !eipIDPattern.MatchString(req.GetEipId()) {
		return nil, errors.BadRequest("INVALID_SNAT_REQUEST", "invalid VPC or EIP ID")
	}
	_, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := actorOf(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.BindVPCSnat(ctx, tenant, actor, req.GetVpcId(), req.GetEipId(), req.GetIdempotencyKey())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Binding == nil || reply.Binding.TenantId != tenant || reply.Binding.VpcId != req.GetVpcId() || reply.Binding.EipId != req.GetEipId() {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
	}
	return &catalogv1.BindVPCSnatResponse{Snat: wireSnat(reply.Binding)}, nil
}

// Error reasons are carried by Resource's typed ErrorInfo; public responses
// retain the reason without forwarding private dependency messages.
func mapNetworkError(err error) error {
	code, reason, message := 503, "NETWORK_UNAVAILABLE", "network request unavailable"
	switch status.Code(err) {
	case codes.NotFound:
		code, reason, message = 404, "VPC_NOT_FOUND", "network resource not found"
	case codes.InvalidArgument:
		code, reason, message = 400, "INVALID_VPC_ID", "invalid network request"
	case codes.AlreadyExists, codes.Aborted:
		code, reason, message = 409, "NETWORK_CONFLICT", "network resource conflict"
	case codes.FailedPrecondition:
		code, reason, message = 412, "NETWORK_PRECONDITION_FAILED", "network resource is not in the required state"
	case codes.DeadlineExceeded:
		code, reason, message = 504, "NETWORK_TIMEOUT", "network request timed out"
	case codes.Canceled:
		code, reason, message = 499, "NETWORK_CANCELED", "network request canceled"
	}
	if code != 503 || status.Code(err) == codes.Unavailable {
		for _, detail := range status.Convert(err).Details() {
			if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Domain == "network.ani.io" && regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`).MatchString(info.Reason) {
				reason = info.Reason
				break
			}
		}
	}
	klog.Errorf("network RPC failed: code=%s reason=%s", status.Code(err), reason)
	return errors.New(code, reason, message)
}

func wireSubnet(v *networkv1.Subnet) *catalogv1.Subnet {
	return &catalogv1.Subnet{Id: v.Id, VpcId: v.VpcId, Name: v.Name, Description: v.Description, Cidr: v.Cidr, Gateway: v.Gateway,
		State: strings.ToLower(strings.TrimPrefix(v.State.String(), "RESOURCE_STATE_")), Reason: v.Reason, ReasonMessage: v.ReasonMessage,
		CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, Version: v.Version, ObservedAt: v.ObservedAt,
		ObservationStale: v.ObservationStale, LastOperationId: v.LastOperationId, AvailableIpRange: v.AvailableIpRange, UsedIpRange: v.UsedIpRange}
}
func validSubnetReply(v *networkv1.Subnet, tenant, id, vpcID string) bool {
	return v != nil && v.TenantId == tenant && subnetIDPattern.MatchString(v.Id) && (id == "" || v.Id == id) &&
		vpcIDPattern.MatchString(v.VpcId) && (vpcID == "" || v.VpcId == vpcID) && v.State >= networkv1.ResourceState_RESOURCE_STATE_PROVISIONING && v.State <= networkv1.ResourceState_RESOURCE_STATE_DELETED
}
func invalidNetworkReply() error {
	return errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
}

func (s *NetworkService) CreateSubnet(ctx context.Context, req *catalogv1.CreateSubnetRequest) (*catalogv1.CreateSubnetResponse, error) {
	if req == nil || !vpcIDPattern.MatchString(req.GetVpcId()) {
		return nil, errors.BadRequest("INVALID_VPC_ID", "invalid VPC ID")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	reply, err := s.client.CreateSubnet(ctx, tenant, actor, req.GetVpcId(), req.GetName(), req.GetCidr(), req.GetDescription(), req.GetIdempotencyKey(), req.Gateway)
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || !validSubnetReply(reply.Subnet, tenant, "", req.GetVpcId()) {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.CreateSubnetResponse{Subnet: wireSubnet(reply.Subnet)}, nil
}
func (s *NetworkService) ListSubnets(ctx context.Context, req *catalogv1.ListSubnetsRequest) (*catalogv1.ListSubnetsResponse, error) {
	if req == nil || (req.GetVpcId() != "" && !vpcIDPattern.MatchString(req.GetVpcId())) {
		return nil, errors.BadRequest("INVALID_VPC_ID", "invalid VPC ID")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListSubnets(ctx, tenant, actor, req.GetVpcId(), req.GetName(), req.GetState(), req.GetLimit(), req.GetCursor())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Total < 0 {
		return nil, invalidNetworkReply()
	}
	out := &catalogv1.ListSubnetsResponse{NextCursor: reply.NextCursor, Total: reply.Total, Items: make([]*catalogv1.Subnet, 0, len(reply.Items))}
	for _, v := range reply.Items {
		if !validSubnetReply(v, tenant, "", req.GetVpcId()) {
			return nil, invalidNetworkReply()
		}
		out.Items = append(out.Items, wireSubnet(v))
	}
	return out, nil
}
func (s *NetworkService) GetSubnet(ctx context.Context, req *catalogv1.GetSubnetRequest) (*catalogv1.GetSubnetResponse, error) {
	if req == nil || !subnetIDPattern.MatchString(req.GetSubnetId()) {
		return nil, errors.BadRequest("INVALID_SUBNET_ID", "invalid subnet ID")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetSubnet(ctx, tenant, actor, req.GetSubnetId())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || !validSubnetReply(reply.Subnet, tenant, req.GetSubnetId(), "") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetSubnetResponse{Subnet: wireSubnet(reply.Subnet)}, nil
}
func (s *NetworkService) DeleteSubnet(ctx context.Context, req *catalogv1.DeleteSubnetRequest) (*catalogv1.DeleteSubnetResponse, error) {
	if req == nil || !subnetIDPattern.MatchString(req.GetSubnetId()) {
		return nil, errors.BadRequest("INVALID_SUBNET_ID", "invalid subnet ID")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	reply, err := s.client.DeleteSubnet(ctx, tenant, actor, req.GetSubnetId())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || !validSubnetReply(reply.Subnet, tenant, req.GetSubnetId(), "") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.DeleteSubnetResponse{Subnet: wireSubnet(reply.Subnet)}, nil
}
func (s *NetworkService) GetVPCSnatBinding(ctx context.Context, req *catalogv1.GetVPCSnatBindingRequest) (*catalogv1.GetVPCSnatBindingResponse, error) {
	if req == nil || !snatIDPattern.MatchString(req.GetBindingId()) {
		return nil, errors.BadRequest("INVALID_SNAT_ID", "invalid SNAT binding ID")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetVPCSnatBinding(ctx, tenant, actor, req.GetBindingId())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Binding == nil || reply.Binding.TenantId != tenant || reply.Binding.Id != req.GetBindingId() {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetVPCSnatBindingResponse{Snat: wireSnat(reply.Binding)}, nil
}
func (s *NetworkService) SetVPCSnatEnabled(ctx context.Context, req *catalogv1.SetVPCSnatEnabledRequest) (*catalogv1.SetVPCSnatEnabledResponse, error) {
	if req == nil || !snatIDPattern.MatchString(req.GetBindingId()) {
		return nil, errors.BadRequest("INVALID_SNAT_ID", "invalid SNAT binding ID")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	reply, err := s.client.SetVPCSnatEnabled(ctx, tenant, actor, req.GetBindingId(), req.GetEnabled(), req.GetExpectedVersion(), req.GetIdempotencyKey())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Binding == nil || reply.Binding.TenantId != tenant || reply.Binding.Id != req.GetBindingId() {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.SetVPCSnatEnabledResponse{Snat: wireSnat(reply.Binding)}, nil
}
func (s *NetworkService) DeleteVPCSnatBinding(ctx context.Context, req *catalogv1.DeleteVPCSnatBindingRequest) (*catalogv1.DeleteVPCSnatBindingResponse, error) {
	if req == nil || !snatIDPattern.MatchString(req.GetBindingId()) {
		return nil, errors.BadRequest("INVALID_SNAT_ID", "invalid SNAT binding ID")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	reply, err := s.client.DeleteVPCSnatBinding(ctx, tenant, actor, req.GetBindingId())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Binding == nil || reply.Binding.TenantId != tenant || reply.Binding.Id != req.GetBindingId() {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.DeleteVPCSnatBindingResponse{Snat: wireSnat(reply.Binding)}, nil
}

func networkLBBackends(inputs []*catalogv1.LoadBalancerBackendInput) ([]*networkv1.LoadBalancerBackendInput, error) {
	out := make([]*networkv1.LoadBalancerBackendInput, 0, len(inputs))
	for _, v := range inputs {
		if v == nil {
			return nil, errors.BadRequest("INVALID_ARGUMENT", "missing load balancer backend")
		}
		out = append(out, &networkv1.LoadBalancerBackendInput{Id: v.Id, SubnetId: v.SubnetId, Address: v.Address, Port: v.Port, Weight: v.Weight})
	}
	return out, nil
}
func networkLBHealth(v *catalogv1.LoadBalancerHealthCheck) *networkv1.LoadBalancerHealthCheck {
	if v == nil {
		return nil
	}
	return &networkv1.LoadBalancerHealthCheck{Protocol: networkv1.LoadBalancerHealthCheckProtocol(v.Protocol), IntervalSeconds: v.IntervalSeconds, TimeoutSeconds: v.TimeoutSeconds, UnhealthyThreshold: v.UnhealthyThreshold, HealthyThreshold: v.HealthyThreshold, Port: v.Port, Path: v.Path, Hostname: v.Hostname}
}
func networkLBListeners(v *catalogv1.LoadBalancerListenerSet) (*networkv1.LoadBalancerListenerSet, error) {
	if v == nil {
		return nil, nil
	}
	out := &networkv1.LoadBalancerListenerSet{}
	for _, l := range v.Items {
		if l == nil {
			return nil, errors.BadRequest("INVALID_ARGUMENT", "missing listener")
		}
		backends, err := networkLBBackends(l.Backends)
		if err != nil {
			return nil, err
		}
		listener := &networkv1.LoadBalancerListenerInput{Id: l.Id, Name: l.Name, Protocol: networkv1.LoadBalancerListenerProtocol(l.Protocol), Port: l.Port, Backends: backends, HealthCheck: networkLBHealth(l.HealthCheck), Algorithm: networkv1.LoadBalancerAlgorithm(l.Algorithm), MaxConnections: l.MaxConnections, GzipEnabled: l.GzipEnabled, HttpsRedirectStatusCode: l.HttpsRedirectStatusCode}
		if l.CertificateRef != nil {
			listener.CertificateRef = &networkv1.LoadBalancerCertificateRef{Name: l.CertificateRef.Name, Namespace: l.CertificateRef.Namespace}
		}
		if l.SessionPersistence != nil {
			listener.SessionPersistence = &networkv1.LoadBalancerSessionPersistence{Mode: networkv1.LoadBalancerSessionMode(l.SessionPersistence.Mode), CookieExpirySeconds: l.SessionPersistence.CookieExpirySeconds}
		}
		out.Items = append(out.Items, listener)
	}
	return out, nil
}
func wireLBHealth(v *networkv1.LoadBalancerHealthCheck) *catalogv1.LoadBalancerHealthCheck {
	if v == nil {
		return nil
	}
	return &catalogv1.LoadBalancerHealthCheck{Protocol: catalogv1.LoadBalancerHealthCheckProtocol(v.Protocol), IntervalSeconds: v.IntervalSeconds, TimeoutSeconds: v.TimeoutSeconds, UnhealthyThreshold: v.UnhealthyThreshold, HealthyThreshold: v.HealthyThreshold, Port: v.Port, Path: v.Path, Hostname: v.Hostname}
}
func wireLoadBalancer(v *networkv1.LoadBalancer) *catalogv1.LoadBalancer {
	out := &catalogv1.LoadBalancer{Id: v.Id, VpcId: v.VpcId, SubnetId: v.SubnetId, Name: v.Name, Description: v.Description,
		Exposure: catalogv1.LoadBalancerExposure(v.Exposure), Flavor: v.Flavor, PublicEipId: v.PublicEipId, PrivateIp: v.PrivateIp,
		HealthCheck: wireLBHealth(v.HealthCheck), Algorithm: catalogv1.LoadBalancerAlgorithm(v.Algorithm),
		State: strings.ToLower(strings.TrimPrefix(v.State.String(), "RESOURCE_STATE_")), Reason: v.Reason, ReasonMessage: v.ReasonMessage,
		Version: v.Version, DesiredVersion: v.DesiredVersion, AppliedVersion: v.AppliedVersion, ProviderTargetVersion: v.ProviderTargetVersion,
		ConfigurationState: catalogv1.LoadBalancerConfigurationState(v.ConfigurationState), DataPlaneState: catalogv1.LoadBalancerDataPlaneState(v.DataPlaneState),
		ObservedAt: v.ObservedAt, ObservationStale: v.ObservationStale, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		LastOperationId: v.LastOperationId, PublicAddress: v.PublicAddress, DataPlaneObservedAt: v.DataPlaneObservedAt,
		Backends: make([]*catalogv1.LoadBalancerBackendMember, 0, len(v.Backends))}
	if v.Listener != nil {
		out.Listener = &catalogv1.LoadBalancerListener{Id: v.Listener.Id, Protocol: catalogv1.LoadBalancerListenerProtocol(v.Listener.Protocol), Port: v.Listener.Port, DeploymentState: v.Listener.DeploymentState, Reason: v.Listener.Reason}
	}
	for _, b := range v.Backends {
		out.Backends = append(out.Backends, &catalogv1.LoadBalancerBackendMember{Id: b.Id, SubnetId: b.SubnetId, Address: b.Address, Port: b.Port, Weight: b.Weight, AttachmentId: b.AttachmentId, State: b.State, Reason: b.Reason, ObservedAt: b.ObservedAt, ObservationStale: b.ObservationStale})
	}
	for _, l := range v.Listeners {
		listener := &catalogv1.LoadBalancerListener{Id: l.Id, Name: l.Name, Protocol: catalogv1.LoadBalancerListenerProtocol(l.Protocol), Port: l.Port, HealthCheck: wireLBHealth(l.HealthCheck), Algorithm: catalogv1.LoadBalancerAlgorithm(l.Algorithm), MaxConnections: l.MaxConnections, GzipEnabled: l.GzipEnabled, HttpsRedirectStatusCode: l.HttpsRedirectStatusCode, DeploymentState: l.DeploymentState, Reason: l.Reason}
		if l.CertificateRef != nil {
			listener.CertificateRef = &catalogv1.LoadBalancerCertificateRef{Name: l.CertificateRef.Name, Namespace: l.CertificateRef.Namespace}
		}
		if l.SessionPersistence != nil {
			listener.SessionPersistence = &catalogv1.LoadBalancerSessionPersistence{Mode: catalogv1.LoadBalancerSessionMode(l.SessionPersistence.Mode), CookieExpirySeconds: l.SessionPersistence.CookieExpirySeconds}
		}
		for _, b := range l.Backends {
			listener.Backends = append(listener.Backends, &catalogv1.LoadBalancerBackendMember{Id: b.Id, SubnetId: b.SubnetId, Address: b.Address, Port: b.Port, Weight: b.Weight, AttachmentId: b.AttachmentId, State: b.State, Reason: b.Reason, ObservedAt: b.ObservedAt, ObservationStale: b.ObservationStale})
		}
		out.Listeners = append(out.Listeners, listener)
	}
	if len(out.Listeners) > 0 {
		out.Listener = out.Listeners[0]
	}
	return out
}
func validLoadBalancerReply(v *networkv1.LoadBalancer, tenant, id, vpcID, subnetID string) bool {
	if v == nil || v.TenantId != tenant || !loadBalancerIDPattern.MatchString(v.Id) || (id != "" && v.Id != id) ||
		!vpcIDPattern.MatchString(v.VpcId) || !subnetIDPattern.MatchString(v.SubnetId) || (vpcID != "" && v.VpcId != vpcID) || (subnetID != "" && v.SubnetId != subnetID) ||
		v.State < networkv1.ResourceState_RESOURCE_STATE_PROVISIONING || v.State > networkv1.ResourceState_RESOURCE_STATE_DELETED {
		return false
	}
	for _, b := range v.Backends {
		if b == nil {
			return false
		}
	}
	for _, l := range v.Listeners {
		if l == nil {
			return false
		}
		for _, b := range l.Backends {
			if b == nil {
				return false
			}
		}
	}
	return true
}
func validLoadBalancerOperation(v *networkv1.Operation, tenant, id, resourceID string, kind networkv1.OperationKind) bool {
	return v != nil && v.TenantId == tenant && operationIDPattern.MatchString(v.Id) && v.Id != "00000000-0000-0000-0000-000000000000" &&
		(id == "" || v.Id == id) && v.ResourceType == networkv1.ResourceType_RESOURCE_TYPE_LOAD_BALANCER &&
		loadBalancerIDPattern.MatchString(v.ResourceId) && (resourceID == "" || v.ResourceId == resourceID) &&
		(kind == networkv1.OperationKind_OPERATION_KIND_UNSPECIFIED || v.Kind == kind)
}
func (s *NetworkService) CreateLoadBalancer(ctx context.Context, req *catalogv1.CreateLoadBalancerRequest) (*catalogv1.CreateLoadBalancerResponse, error) {
	if req == nil || !vpcIDPattern.MatchString(req.GetVpcId()) || !subnetIDPattern.MatchString(req.GetSubnetId()) {
		return nil, errors.BadRequest("INVALID_ARGUMENT", "invalid VPC or subnet ID")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	backends, err := networkLBBackends(req.Backends)
	if err != nil {
		return nil, err
	}
	in := &networkv1.CreateLoadBalancerRequest{Name: req.Name, Description: req.Description, VpcId: req.VpcId, SubnetId: req.SubnetId,
		Exposure: networkv1.LoadBalancerExposure(req.Exposure), Flavor: req.Flavor, PublicEipId: req.PublicEipId, PrivateIp: req.PrivateIp,
		Backends: backends, HealthCheck: networkLBHealth(req.HealthCheck), IdempotencyKey: req.IdempotencyKey}
	if req.Listener != nil {
		legacy, err := networkLBListeners(&catalogv1.LoadBalancerListenerSet{Items: []*catalogv1.LoadBalancerListenerInput{req.Listener}})
		if err != nil {
			return nil, err
		}
		in.Listener = legacy.Items[0]
	}
	in.Listeners, err = networkLBListeners(req.Listeners)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.CreateLoadBalancer(ctx, tenant, actor, in)
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || !validLoadBalancerReply(reply.LoadBalancer, tenant, "", req.VpcId, req.SubnetId) ||
		!validLoadBalancerOperation(reply.Operation, tenant, reply.LoadBalancer.LastOperationId, reply.LoadBalancer.Id, networkv1.OperationKind_OPERATION_KIND_CREATE_LOAD_BALANCER) {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.CreateLoadBalancerResponse{LoadBalancer: wireLoadBalancer(reply.LoadBalancer), Operation: wireOperation(reply.Operation)}, nil
}
func (s *NetworkService) GetLoadBalancer(ctx context.Context, req *catalogv1.GetLoadBalancerRequest) (*catalogv1.GetLoadBalancerResponse, error) {
	if req == nil || !loadBalancerIDPattern.MatchString(req.GetLoadBalancerId()) {
		return nil, errors.BadRequest("INVALID_LOAD_BALANCER_ID", "invalid load balancer ID")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetLoadBalancer(ctx, tenant, actor, req.GetLoadBalancerId())
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || !validLoadBalancerReply(reply.LoadBalancer, tenant, req.GetLoadBalancerId(), "", "") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetLoadBalancerResponse{LoadBalancer: wireLoadBalancer(reply.LoadBalancer)}, nil
}
func (s *NetworkService) ListLoadBalancers(ctx context.Context, req *catalogv1.ListLoadBalancersRequest) (*catalogv1.ListLoadBalancersResponse, error) {
	if req == nil || (req.GetVpcId() != "" && !vpcIDPattern.MatchString(req.GetVpcId())) || (req.GetSubnetId() != "" && !subnetIDPattern.MatchString(req.GetSubnetId())) {
		return nil, errors.BadRequest("INVALID_ARGUMENT", "invalid VPC or subnet filter")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	state := networkv1.ResourceState_RESOURCE_STATE_UNSPECIFIED
	if req.GetState() != "" {
		value, ok := networkv1.ResourceState_value[req.GetState()]
		if !ok {
			value, ok = networkv1.ResourceState_value["RESOURCE_STATE_"+strings.ToUpper(req.GetState())]
		}
		if !ok {
			return nil, errors.BadRequest("INVALID_ARGUMENT", "invalid resource state filter")
		}
		state = networkv1.ResourceState(value)
	}
	reply, err := s.client.ListLoadBalancers(ctx, tenant, actor, &networkv1.ListLoadBalancersRequest{Name: req.Name, VpcId: req.VpcId, SubnetId: req.SubnetId, Exposure: networkv1.LoadBalancerExposure(req.Exposure), State: state, Limit: req.Limit, Cursor: req.Cursor})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Total < 0 {
		return nil, invalidNetworkReply()
	}
	out := &catalogv1.ListLoadBalancersResponse{NextCursor: reply.NextCursor, Total: reply.Total, Items: make([]*catalogv1.LoadBalancer, 0, len(reply.Items))}
	for _, v := range reply.Items {
		if !validLoadBalancerReply(v, tenant, "", req.VpcId, req.SubnetId) {
			return nil, invalidNetworkReply()
		}
		out.Items = append(out.Items, wireLoadBalancer(v))
	}
	return out, nil
}
func (s *NetworkService) UpdateLoadBalancer(ctx context.Context, req *catalogv1.UpdateLoadBalancerRequest) (*catalogv1.UpdateLoadBalancerResponse, error) {
	if req == nil || !loadBalancerIDPattern.MatchString(req.GetLoadBalancerId()) {
		return nil, errors.BadRequest("INVALID_LOAD_BALANCER_ID", "invalid load balancer ID")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	backends, err := networkLBBackends(req.Backends)
	if err != nil {
		return nil, err
	}
	in := &networkv1.UpdateLoadBalancerRequest{LoadBalancerId: req.LoadBalancerId, ExpectedVersion: req.ExpectedVersion, IdempotencyKey: req.IdempotencyKey, Name: req.Name, Description: req.Description, Backends: backends, HealthCheck: networkLBHealth(req.HealthCheck), UpdateMask: req.UpdateMask}
	in.Listeners, err = networkLBListeners(req.Listeners)
	if err != nil {
		return nil, err
	}
	if req.Data != nil {
		listeners, err := networkLBListeners(req.Data.Listeners)
		if err != nil {
			return nil, err
		}
		in.Data = &networkv1.LoadBalancerMutableData{Name: req.Data.Name, Description: req.Data.Description, Listeners: listeners}
	}
	reply, err := s.client.UpdateLoadBalancer(ctx, tenant, actor, in)
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || !validLoadBalancerReply(reply.LoadBalancer, tenant, req.LoadBalancerId, "", "") ||
		!validLoadBalancerOperation(reply.Operation, tenant, reply.LoadBalancer.LastOperationId, reply.LoadBalancer.Id, networkv1.OperationKind_OPERATION_KIND_UPDATE_LOAD_BALANCER) {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.UpdateLoadBalancerResponse{LoadBalancer: wireLoadBalancer(reply.LoadBalancer), Operation: wireOperation(reply.Operation)}, nil
}
func (s *NetworkService) DeleteLoadBalancer(ctx context.Context, req *catalogv1.DeleteLoadBalancerRequest) (*catalogv1.DeleteLoadBalancerResponse, error) {
	if req == nil || !loadBalancerIDPattern.MatchString(req.GetLoadBalancerId()) {
		return nil, errors.BadRequest("INVALID_LOAD_BALANCER_ID", "invalid load balancer ID")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	reply, err := s.client.DeleteLoadBalancer(ctx, tenant, actor, req.LoadBalancerId)
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || !validLoadBalancerReply(reply.LoadBalancer, tenant, req.LoadBalancerId, "", "") ||
		!validLoadBalancerOperation(reply.Operation, tenant, reply.LoadBalancer.LastOperationId, reply.LoadBalancer.Id, networkv1.OperationKind_OPERATION_KIND_DELETE_LOAD_BALANCER) {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.DeleteLoadBalancerResponse{LoadBalancer: wireLoadBalancer(reply.LoadBalancer), Operation: wireOperation(reply.Operation)}, nil
}
func (s *NetworkService) GetLoadBalancerOperation(ctx context.Context, req *catalogv1.GetLoadBalancerOperationRequest) (*catalogv1.GetLoadBalancerOperationResponse, error) {
	if req == nil || !operationIDPattern.MatchString(req.GetOperationId()) || req.GetOperationId() == "00000000-0000-0000-0000-000000000000" {
		return nil, errors.BadRequest("INVALID_OPERATION_ID", "invalid operation ID")
	}
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, err
	}
	reply, err := s.client.GetLoadBalancerOperation(ctx, tenant, actor, req.OperationId)
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || !validLoadBalancerOperation(reply.Operation, tenant, req.OperationId, "", networkv1.OperationKind_OPERATION_KIND_UNSPECIFIED) {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetLoadBalancerOperationResponse{Operation: wireOperation(reply.Operation)}, nil
}

func (s *NetworkService) ListVPCCIDRPresets(ctx context.Context, req *catalogv1.ListVPCCIDRPresetsRequest) (*catalogv1.ListVPCCIDRPresetsResponse, error) {
	if req == nil {
		return nil, errors.BadRequest("NETWORK_INVALID_REQUEST", "request required")
	}
	_, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, err
	}
	actor, err := actorOf(ctx)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListVPCCIDRPresets(ctx, tenant, actor)
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid network response")
	}
	return &catalogv1.ListVPCCIDRPresetsResponse{Cidrs: reply.Cidrs}, nil
}

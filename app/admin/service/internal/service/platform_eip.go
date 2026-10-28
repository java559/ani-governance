package service

import (
	"context"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	identityv1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/pkg/middleware/auth"
)

type PlatformEIPClient interface {
	ListPlatformEIPs(context.Context, string, *networkv1.ListPlatformEIPsRequest) (*networkv1.ListPlatformEIPsResponse, error)
	GetPlatformEIP(context.Context, string, *networkv1.GetPlatformEIPRequest) (*networkv1.GetPlatformEIPResponse, error)
	ReleasePlatformEIP(context.Context, string, *networkv1.ReleasePlatformEIPRequest) (*networkv1.ReleasePlatformEIPResponse, error)
}
type PlatformEIPTenantResolver interface {
	ResourceTenantResolver
	PlatformEIPTenants(context.Context, []string) (map[string]*identityv1.Tenant, error)
}

func (s *PlatformNetworkService) platformEIPClient(ctx context.Context, op string) (PlatformEIPClient, string, error) {
	actor, err := s.actor(ctx, op)
	if err != nil {
		return nil, "", err
	}
	c, ok := s.client.(PlatformEIPClient)
	if !ok || s.eipTenants == nil {
		return nil, "", errors.ServiceUnavailable("NETWORK_UNAVAILABLE", "platform EIP access is unavailable")
	}
	return c, actor, nil
}
func (s *PlatformNetworkService) wirePlatformEIPs(ctx context.Context, values []*networkv1.PlatformEIP) ([]*catalogv1.PlatformEIP, error) {
	ids := make([]string, 0, len(values))
	for _, v := range values {
		if v == nil || v.Eip == nil || !eipIDPattern.MatchString(v.Eip.Id) || v.ClusterId == "" || !auth.ValidPlatformNetworkID("pool_id", v.PoolId) || v.Eip.State == networkv1.ResourceState_RESOURCE_STATE_UNSPECIFIED {
			return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid platform EIP response")
		}
		if v.Eip.ManagedBy == "system" && v.Eip.TenantId == "" && v.Eip.Scope == "public" && v.Eip.BindingTarget.GetKind() == "platform_default_egress" && auth.ValidPlatformNetworkID("egress_id", v.Eip.BindingTarget.GetId()) {
			continue
		}
		id, err := uuid.Parse(v.Eip.TenantId)
		if err != nil || id == uuid.Nil || id.String() != v.Eip.TenantId {
			return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid platform EIP owner")
		}
		ids = append(ids, v.Eip.TenantId)
	}
	tenants, err := s.eipTenants.PlatformEIPTenants(ctx, ids)
	if err != nil {
		return nil, errors.ServiceUnavailable("TENANT_MAPPING_UNAVAILABLE", "EIP tenant metadata unavailable")
	}
	out := make([]*catalogv1.PlatformEIP, 0, len(values))
	for _, v := range values {
		e := wireEIP(v.Eip)
		r := &catalogv1.PlatformEIP{Id: e.Id, Name: e.Name, Description: e.Description, State: e.State, Reason: e.Reason, ReasonMessage: v.Eip.ReasonMessage, Version: e.Version, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, ObservedAt: e.ObservedAt, ObservationStale: e.ObservationStale, LastOperationId: e.LastOperationId, Address: e.Address, BindingState: e.BindingState, BindingTarget: e.BindingTarget, Scope: e.Scope, ManagedBy: e.ManagedBy, ResourceTenantId: v.Eip.TenantId, PoolId: v.PoolId, PoolName: v.PoolName, ClusterId: v.ClusterId}
		if t := tenants[v.Eip.TenantId]; t != nil {
			r.TenantId, r.TenantName = t.GetId(), t.GetName()
		}
		out = append(out, r)
	}
	return out, nil
}
func (s *PlatformNetworkService) ListPlatformEIPs(ctx context.Context, r *catalogv1.ListPlatformEIPsRequest) (*catalogv1.ListPlatformEIPsResponse, error) {
	c, actor, err := s.platformEIPClient(ctx, "ListPlatformEIPs")
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.BadRequest("INVALID_NETWORK_REQUEST", "request is required")
	}
	page, size, err := resolveVPCPage(r.Page, r.PageSize)
	if err != nil {
		return nil, err
	}
	state, err := platformListState(r.State)
	if err != nil {
		return nil, err
	}
	target := ""
	if r.TenantId != nil {
		if r.GetTenantId() == 0 {
			return nil, errors.BadRequest("INVALID_NETWORK_REQUEST", "tenant_id must be positive")
		}
		target, err = s.eipTenants.ResourceTenantID(ctx, r.GetTenantId())
		if err != nil {
			return nil, err
		}
	}
	reply, err := c.ListPlatformEIPs(ctx, actor, &networkv1.ListPlatformEIPsRequest{Page: &page, PageSize: &size, TenantId: target, Name: r.Name, Address: r.Address, PoolId: r.PoolId, State: state, Scope: r.Scope})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Total < 0 {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "invalid platform EIP response")
	}
	if target != "" {
		for _, v := range reply.Items {
			if v == nil || v.Eip == nil || v.Eip.TenantId != target {
				return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "EIP tenant filter mismatch")
			}
		}
	}
	items, err := s.wirePlatformEIPs(ctx, reply.Items)
	if err != nil {
		return nil, err
	}
	return &catalogv1.ListPlatformEIPsResponse{Items: items, Total: reply.Total}, nil
}
func (s *PlatformNetworkService) GetPlatformEIP(ctx context.Context, r *catalogv1.GetPlatformEIPRequest) (*catalogv1.GetPlatformEIPResponse, error) {
	c, actor, err := s.platformEIPClient(ctx, "GetPlatformEIP")
	if err != nil {
		return nil, err
	}
	if r == nil || !eipIDPattern.MatchString(r.GetEipId()) {
		return nil, errors.BadRequest("INVALID_EIP_ID", "invalid EIP ID")
	}
	reply, err := c.GetPlatformEIP(ctx, actor, &networkv1.GetPlatformEIPRequest{EipId: r.EipId})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Resource == nil || reply.Resource.GetEip().GetId() != r.EipId {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "EIP identity mismatch")
	}
	items, err := s.wirePlatformEIPs(ctx, []*networkv1.PlatformEIP{reply.Resource})
	if err != nil {
		return nil, err
	}
	return &catalogv1.GetPlatformEIPResponse{Resource: items[0]}, nil
}
func (s *PlatformNetworkService) ReleasePlatformEIP(ctx context.Context, r *catalogv1.ReleasePlatformEIPRequest) (*catalogv1.ReleasePlatformEIPResponse, error) {
	c, actor, err := s.platformEIPClient(ctx, "ReleasePlatformEIP")
	if err != nil {
		return nil, err
	}
	if r == nil || !eipIDPattern.MatchString(r.GetEipId()) {
		return nil, errors.BadRequest("INVALID_EIP_ID", "invalid EIP ID")
	}
	reply, err := c.ReleasePlatformEIP(ctx, actor, &networkv1.ReleasePlatformEIPRequest{EipId: r.EipId})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Resource == nil || reply.Resource.GetEip().GetId() != r.EipId {
		return nil, errors.ServiceUnavailable("NETWORK_INVALID_RESPONSE", "EIP identity mismatch")
	}
	items, err := s.wirePlatformEIPs(ctx, []*networkv1.PlatformEIP{reply.Resource})
	if err != nil {
		return nil, err
	}
	return &catalogv1.ReleasePlatformEIPResponse{Resource: items[0]}, nil
}

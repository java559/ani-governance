package service

import (
	"context"
	"net/netip"

	"github.com/go-kratos/kratos/v2/errors"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
)

func wireDefaultVPCEgress(v *networkv1.DefaultVPCEgress) *catalogv1.DefaultVPCEgress {
	if v == nil {
		return nil
	}
	return &catalogv1.DefaultVPCEgress{Id: v.Id, Name: v.Name, PoolId: v.PoolId, State: v.State, Reason: v.Reason, Version: v.Version, LastOperationId: v.LastOperationId, OperationState: v.OperationState, Phase: v.Phase, PendingUnknown: v.PendingUnknown, Address: v.Address, EipProviderName: v.EipProviderName, EipProviderUid: v.EipProviderUid, SnatProviderName: v.SnatProviderName, SnatProviderUid: v.SnatProviderUid, DesiredEnabled: v.DesiredEnabled, AppliedEnabled: v.AppliedEnabled, ClusterId: v.ClusterId, ClusterUid: v.ClusterUid, DefaultVpcNamespace: v.DefaultVpcNamespace, DefaultVpcName: v.DefaultVpcName, DefaultVpcUid: v.DefaultVpcUid, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, ObservedAt: v.ObservedAt, TargetGeneration: v.TargetGeneration, PendingAction: v.PendingAction, ObservationStale: v.ObservationStale}
}

func (s *PlatformNetworkService) CreateDefaultVPCEgress(ctx context.Context, req *catalogv1.CreateDefaultVPCEgressRequest) (*catalogv1.CreateDefaultVPCEgressResponse, error) {
	actor, err := s.actor(ctx, "CreateDefaultVPCEgress")
	if err != nil {
		return nil, err
	}
	if req == nil || req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	if !auth.ValidPlatformNetworkID("pool_id", req.Data.PoolId) {
		return nil, errors.BadRequest("INVALID_POOL_ID", "invalid public pool ID")
	}
	reply, err := s.client.CreateDefaultVPCEgress(ctx, actor, &networkv1.CreateDefaultVPCEgressRequest{Name: req.Data.Name, PoolId: req.Data.PoolId, IdempotencyKey: req.Data.IdempotencyKey})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	out := wireDefaultVPCEgress(reply.Egress)
	if !validDefaultVPCEgress(out, "") || out.PoolId != req.Data.PoolId {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.CreateDefaultVPCEgressResponse{Egress: out}, nil
}

func (s *PlatformNetworkService) GetDefaultVPCEgress(ctx context.Context, req *catalogv1.GetDefaultVPCEgressRequest) (*catalogv1.GetDefaultVPCEgressResponse, error) {
	actor, err := s.actor(ctx, "GetDefaultVPCEgress")
	if err != nil {
		return nil, err
	}
	if req == nil || !auth.ValidPlatformNetworkID("egress_id", req.GetEgressId()) {
		return nil, errors.BadRequest("INVALID_DEFAULT_EGRESS_ID", "invalid default VPC egress ID")
	}
	reply, err := s.client.GetDefaultVPCEgress(ctx, actor, &networkv1.GetDefaultVPCEgressRequest{EgressId: req.EgressId})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	out := wireDefaultVPCEgress(reply.Egress)
	if !validDefaultVPCEgress(out, req.EgressId) {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetDefaultVPCEgressResponse{Egress: out}, nil
}

func (s *PlatformNetworkService) DeleteDefaultVPCEgress(ctx context.Context, req *catalogv1.DeleteDefaultVPCEgressRequest) (*catalogv1.DeleteDefaultVPCEgressResponse, error) {
	actor, err := s.actor(ctx, "DeleteDefaultVPCEgress")
	if err != nil {
		return nil, err
	}
	if req == nil || !auth.ValidPlatformNetworkID("egress_id", req.GetEgressId()) {
		return nil, errors.BadRequest("INVALID_DEFAULT_EGRESS_ID", "invalid default VPC egress ID")
	}
	reply, err := s.client.DeleteDefaultVPCEgress(ctx, actor, &networkv1.DeleteDefaultVPCEgressRequest{EgressId: req.EgressId})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	out := wireDefaultVPCEgress(reply.Egress)
	if !validDefaultVPCEgress(out, req.EgressId) {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.DeleteDefaultVPCEgressResponse{Egress: out}, nil
}

func validDefaultVPCEgress(v *catalogv1.DefaultVPCEgress, id string) bool {
	if v == nil || !auth.ValidPlatformNetworkID("egress_id", v.Id) || (id != "" && v.Id != id) || !auth.ValidPlatformNetworkID("pool_id", v.PoolId) || v.Version < 1 || !auth.ValidPlatformNetworkID("operation_id", v.LastOperationId) || v.ClusterId == "" || v.ClusterUid == "" || v.DefaultVpcNamespace == "" || v.DefaultVpcName == "" || v.DefaultVpcUid == "" {
		return false
	}
	switch v.State {
	case "provisioning", "degraded", "failed", "deleting", "deleted":
		return true
	case "available":
		_, err := netip.ParseAddr(v.Address)
		return err == nil && !v.ObservationStale && !v.PendingUnknown && v.PendingAction == "" && v.DesiredEnabled && v.AppliedEnabled != nil && v.GetAppliedEnabled() && v.EipProviderName != "" && v.EipProviderUid != "" && v.SnatProviderName != "" && v.SnatProviderUid != "" && v.TargetGeneration > 0 && v.ObservedAt != nil
	default:
		return false
	}
}

package service

import (
	"context"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type instanceSecurityGroupClient interface {
	SetSecurityGroups(context.Context, string, string, *res.SetSecurityGroupsRequest) (*res.OperationResponse, error)
}

func (s *InstanceService) SetInstanceSecurityGroups(ctx context.Context, r *view.SetInstanceSecurityGroupsRequest) (*view.Acceptance, error) {
	if r == nil || r.Data == nil {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "instance data required")
	}
	d := r.Data
	tenant, actor, err := s.scope(ctx, data.InstanceSecurityGroupsPath, "PUT")
	if err != nil {
		return nil, err
	}
	if !instanceUUID(d.InstanceId) || !instanceUUID(d.IdempotencyKey) || d.ExpectedVersion < 1 {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid security group command")
	}
	canonical, err := res.CanonicalSecurityGroupIntent(d.InstanceId, d.ExpectedVersion, d.SecurityGroupIds)
	if err != nil {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid security group intent")
	}
	client, ok := s.binding.client.(instanceSecurityGroupClient)
	if !ok {
		return nil, kratoserrors.ServiceUnavailable("INSTANCE_DEPENDENCY_UNAVAILABLE", "instance security group owner unavailable")
	}
	reply, err := s.binding.client.Lookup(ctx, tenant, actor, &res.LookupCommandReplayRequest{TenantId: tenant, InstanceId: d.InstanceId, Action: "set-security-groups", IdempotencyKey: d.IdempotencyKey, CanonicalIntent: canonical})
	if err == nil {
		return instanceCommandAcceptance(reply, d.InstanceId, true)
	}
	if status.Code(err) != codes.NotFound {
		return nil, err
	}
	if err = s.canOperate(ctx, d.InstanceId); err != nil {
		return nil, err
	}
	if !s.binding.newIntentsEnabled {
		return nil, kratoserrors.ServiceUnavailable("INSTANCE_ACTION_UNAVAILABLE", "instance action unavailable")
	}
	reply, err = client.SetSecurityGroups(ctx, tenant, actor, &res.SetSecurityGroupsRequest{TenantId: tenant, InstanceId: d.InstanceId, ExpectedVersion: d.ExpectedVersion, IdempotencyKey: d.IdempotencyKey, SecurityGroupIds: append([]string(nil), d.SecurityGroupIds...)})
	if err != nil {
		return nil, err
	}
	return instanceCommandAcceptance(reply, d.InstanceId, false)
}

package data

import (
	"context"
	network "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (c *NetworkClient) CreateSecurityGroup(ctx context.Context, tenant, actor string, request *network.CreateSecurityGroupRequest) (*network.CreateSecurityGroupResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.CreateSecurityGroupRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.CreateSecurityGroupResponse, error) {
		return c.securityGroups.CreateSecurityGroup(ctx, in)
	})
}
func (c *NetworkClient) GetSecurityGroup(ctx context.Context, tenant, actor string, request *network.GetSecurityGroupRequest) (*network.GetSecurityGroupResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.GetSecurityGroupRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.GetSecurityGroupResponse, error) {
		return c.securityGroups.GetSecurityGroup(ctx, in)
	})
}
func (c *NetworkClient) ListSecurityGroups(ctx context.Context, tenant, actor string, request *network.ListSecurityGroupsRequest) (*network.ListSecurityGroupsResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.ListSecurityGroupsRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.ListSecurityGroupsResponse, error) {
		return c.securityGroups.ListSecurityGroups(ctx, in)
	})
}
func (c *NetworkClient) UpdateSecurityGroup(ctx context.Context, tenant, actor string, request *network.UpdateSecurityGroupRequest) (*network.UpdateSecurityGroupResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.UpdateSecurityGroupRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.UpdateSecurityGroupResponse, error) {
		return c.securityGroups.UpdateSecurityGroup(ctx, in)
	})
}
func (c *NetworkClient) DeleteSecurityGroup(ctx context.Context, tenant, actor string, request *network.DeleteSecurityGroupRequest) (*network.DeleteSecurityGroupResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.DeleteSecurityGroupRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.DeleteSecurityGroupResponse, error) {
		return c.securityGroups.DeleteSecurityGroup(ctx, in)
	})
}
func (c *NetworkClient) ListSecurityGroupRules(ctx context.Context, tenant, actor string, request *network.ListSecurityGroupRulesRequest) (*network.ListSecurityGroupRulesResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.ListSecurityGroupRulesRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.ListSecurityGroupRulesResponse, error) {
		return c.securityGroups.ListSecurityGroupRules(ctx, in)
	})
}
func (c *NetworkClient) CreateSecurityGroupRule(ctx context.Context, tenant, actor string, request *network.CreateSecurityGroupRuleRequest) (*network.CreateSecurityGroupRuleResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.CreateSecurityGroupRuleRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.CreateSecurityGroupRuleResponse, error) {
		return c.securityGroups.CreateSecurityGroupRule(ctx, in)
	})
}
func (c *NetworkClient) UpdateSecurityGroupRule(ctx context.Context, tenant, actor string, request *network.UpdateSecurityGroupRuleRequest) (*network.UpdateSecurityGroupRuleResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.UpdateSecurityGroupRuleRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.UpdateSecurityGroupRuleResponse, error) {
		return c.securityGroups.UpdateSecurityGroupRule(ctx, in)
	})
}
func (c *NetworkClient) DeleteSecurityGroupRule(ctx context.Context, tenant, actor string, request *network.DeleteSecurityGroupRuleRequest) (*network.DeleteSecurityGroupRuleResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.DeleteSecurityGroupRuleRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.DeleteSecurityGroupRuleResponse, error) {
		return c.securityGroups.DeleteSecurityGroupRule(ctx, in)
	})
}
func (c *NetworkClient) ListSecurityGroupBindings(ctx context.Context, tenant, actor string, request *network.ListSecurityGroupBindingsRequest) (*network.ListSecurityGroupBindingsResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.ListSecurityGroupBindingsRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.ListSecurityGroupBindingsResponse, error) {
		return c.securityGroups.ListSecurityGroupBindings(ctx, in)
	})
}
func (c *NetworkClient) SetEIPSecurityGroups(ctx context.Context, tenant, actor string, request *network.SetEIPSecurityGroupsRequest) (*network.SetEIPSecurityGroupsResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.SetEIPSecurityGroupsRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.SetEIPSecurityGroupsResponse, error) {
		return c.securityGroups.SetEIPSecurityGroups(ctx, in)
	})
}
func (c *NetworkClient) GetEIPSecurityGroups(ctx context.Context, tenant, actor string, request *network.GetEIPSecurityGroupsRequest) (*network.GetEIPSecurityGroupsResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.GetEIPSecurityGroupsRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.GetEIPSecurityGroupsResponse, error) {
		return c.securityGroups.GetEIPSecurityGroups(ctx, in)
	})
}
func (c *NetworkClient) GetSecurityGroupOperation(ctx context.Context, tenant, actor string, request *network.GetSecurityGroupOperationRequest) (*network.GetSecurityGroupOperationResponse, error) {
	tc, err := c.trusted(tenant, actor)
	if err != nil {
		return nil, err
	}
	if request == nil || c.securityGroups == nil {
		return nil, status.Error(codes.Unavailable, "security group client unavailable")
	}
	in := proto.Clone(request).(*network.GetSecurityGroupOperationRequest)
	in.TargetTenantId = tc.tenant
	return outCall(c, ctx, tc, func(ctx context.Context) (*network.GetSecurityGroupOperationResponse, error) {
		return c.securityGroups.GetSecurityGroupOperation(ctx, in)
	})
}

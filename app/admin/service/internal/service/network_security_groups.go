package service

import (
	"context"
	"github.com/go-kratos/kratos/v2/errors"
	network "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"regexp"
	"strings"
)

type NetworkSecurityGroupClient interface {
	CreateSecurityGroup(context.Context, string, string, *network.CreateSecurityGroupRequest) (*network.CreateSecurityGroupResponse, error)
	GetSecurityGroup(context.Context, string, string, *network.GetSecurityGroupRequest) (*network.GetSecurityGroupResponse, error)
	ListSecurityGroups(context.Context, string, string, *network.ListSecurityGroupsRequest) (*network.ListSecurityGroupsResponse, error)
	UpdateSecurityGroup(context.Context, string, string, *network.UpdateSecurityGroupRequest) (*network.UpdateSecurityGroupResponse, error)
	DeleteSecurityGroup(context.Context, string, string, *network.DeleteSecurityGroupRequest) (*network.DeleteSecurityGroupResponse, error)
	ListSecurityGroupRules(context.Context, string, string, *network.ListSecurityGroupRulesRequest) (*network.ListSecurityGroupRulesResponse, error)
	CreateSecurityGroupRule(context.Context, string, string, *network.CreateSecurityGroupRuleRequest) (*network.CreateSecurityGroupRuleResponse, error)
	UpdateSecurityGroupRule(context.Context, string, string, *network.UpdateSecurityGroupRuleRequest) (*network.UpdateSecurityGroupRuleResponse, error)
	DeleteSecurityGroupRule(context.Context, string, string, *network.DeleteSecurityGroupRuleRequest) (*network.DeleteSecurityGroupRuleResponse, error)
	ListSecurityGroupBindings(context.Context, string, string, *network.ListSecurityGroupBindingsRequest) (*network.ListSecurityGroupBindingsResponse, error)
	SetEIPSecurityGroups(context.Context, string, string, *network.SetEIPSecurityGroupsRequest) (*network.SetEIPSecurityGroupsResponse, error)
	GetEIPSecurityGroups(context.Context, string, string, *network.GetEIPSecurityGroupsRequest) (*network.GetEIPSecurityGroupsResponse, error)
	GetSecurityGroupOperation(context.Context, string, string, *network.GetSecurityGroupOperationRequest) (*network.GetSecurityGroupOperationResponse, error)
}

var securityGroupIDPattern = regexp.MustCompile(`^sg_[0-9a-f]{32}$`)
var securityGroupRuleIDPattern = regexp.MustCompile(`^(sgr_[0-9a-f]{32}|sgr_default_ingress_drop)$`)

func invalidSecurityGroupRequest() error {
	return errors.BadRequest("INVALID_NETWORK_REQUEST", "invalid security group request")
}
func (s *NetworkService) securityGroupScope(ctx context.Context) (NetworkSecurityGroupClient, string, string, error) {
	operator, tenant, err := trustedOperator(ctx, s)
	if err != nil {
		return nil, "", "", err
	}
	actor, err := operator.Actor()
	if err != nil {
		return nil, "", "", err
	}
	client, ok := s.client.(NetworkSecurityGroupClient)
	if !ok {
		return nil, "", "", errors.ServiceUnavailable("NETWORK_UNAVAILABLE", "security group access unavailable")
	}
	return client, tenant, actor, nil
}
func securityGroupWire(in, out proto.Message) error {
	if in == nil || !in.ProtoReflect().IsValid() {
		return invalidNetworkReply()
	}
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(in)
	if err != nil {
		return invalidNetworkReply()
	}
	if err = (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, out); err != nil {
		return invalidNetworkReply()
	}
	return nil
}
func validSecurityGroup(group *network.SecurityGroup, tenant, id string) bool {
	return group != nil && group.TenantId == tenant && securityGroupIDPattern.MatchString(group.Id) && (id == "" || group.Id == id) && group.Version > 0
}

type securityGroupMutationReply interface {
	GetSecurityGroup() *network.SecurityGroup
	GetRule() *network.SecurityGroupRule
	GetOperation() *network.Operation
}

func securityGroupMutation(reply securityGroupMutationReply, tenant, id string) (*view.SecurityGroupResponse, error) {
	if reply == nil || !validSecurityGroup(reply.GetSecurityGroup(), tenant, id) || reply.GetOperation() == nil || reply.GetOperation().TenantId != tenant || reply.GetOperation().ResourceId != reply.GetSecurityGroup().Id || !operationIDPattern.MatchString(reply.GetOperation().Id) {
		return nil, invalidNetworkReply()
	}
	out := &view.SecurityGroupResponse{SecurityGroup: new(view.SecurityGroup), Operation: wireOperation(reply.GetOperation())}
	if err := securityGroupWire(reply.GetSecurityGroup(), out.SecurityGroup); err != nil {
		return nil, err
	}
	return out, nil
}
func securityGroupRuleMutation(reply securityGroupMutationReply, tenant, id, ruleID string) (*view.SecurityGroupRuleResponse, error) {
	result, err := securityGroupMutation(reply, tenant, id)
	if err != nil {
		return nil, err
	}
	out := &view.SecurityGroupRuleResponse{SecurityGroup: result.SecurityGroup, Operation: result.Operation}
	if reply.GetRule() != nil {
		if !securityGroupRuleIDPattern.MatchString(reply.GetRule().Id) || (ruleID != "" && reply.GetRule().Id != ruleID) {
			return nil, invalidNetworkReply()
		}
		out.Rule = new(view.SecurityGroupRule)
		if err = securityGroupWire(reply.GetRule(), out.Rule); err != nil {
			return nil, err
		}
		out.Rule.SecurityGroupId = id
	}
	return out, nil
}
func securityGroupRule(in *view.SecurityGroupRuleInput) (*network.SecurityGroupRule, error) {
	if in == nil {
		return nil, invalidSecurityGroupRequest()
	}
	out := new(network.SecurityGroupRule)
	if err := securityGroupWire(in, out); err != nil {
		return nil, invalidSecurityGroupRequest()
	}
	return out, nil
}
func (s *NetworkService) CreateSecurityGroup(ctx context.Context, req *view.CreateSecurityGroupRequest) (*view.SecurityGroupResponse, error) {
	if req == nil || req.Data == nil {
		return nil, invalidSecurityGroupRequest()
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	d := req.Data
	in := &network.CreateSecurityGroupRequest{Name: d.Name, Description: d.Description, IdempotencyKey: d.IdempotencyKey}
	for _, rule := range d.Rules {
		item, err := securityGroupRule(rule)
		if err != nil {
			return nil, err
		}
		in.Rules = append(in.Rules, item)
	}
	reply, e := c.CreateSecurityGroup(ctx, t, a, in)
	if e != nil {
		return nil, mapNetworkError(e)
	}
	return securityGroupMutation(reply, t, "")
}
func (s *NetworkService) GetSecurityGroup(ctx context.Context, req *view.GetSecurityGroupRequest) (*view.SecurityGroupResponse, error) {
	if req == nil || !securityGroupIDPattern.MatchString(req.SecurityGroupId) {
		return nil, invalidSecurityGroupRequest()
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	reply, e := c.GetSecurityGroup(ctx, t, a, &network.GetSecurityGroupRequest{SecurityGroupId: req.SecurityGroupId})
	if e != nil {
		return nil, mapNetworkError(e)
	}
	if reply == nil || !validSecurityGroup(reply.SecurityGroup, t, req.SecurityGroupId) {
		return nil, invalidNetworkReply()
	}
	out := &view.SecurityGroupResponse{SecurityGroup: new(view.SecurityGroup)}
	e = securityGroupWire(reply.SecurityGroup, out.SecurityGroup)
	return out, e
}
func (s *NetworkService) ListSecurityGroups(ctx context.Context, req *view.ListSecurityGroupsRequest) (*view.ListSecurityGroupsResponse, error) {
	if req == nil {
		return nil, invalidSecurityGroupRequest()
	}
	page, size, e := resolveVPCPage(req.Page, req.PageSize)
	if e != nil {
		return nil, e
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	reply, e := c.ListSecurityGroups(ctx, t, a, &network.ListSecurityGroupsRequest{Page: page, PageSize: size, Name: req.Name, State: req.State})
	if e != nil {
		return nil, mapNetworkError(e)
	}
	if reply == nil || reply.Total < 0 {
		return nil, invalidNetworkReply()
	}
	out := &view.ListSecurityGroupsResponse{Items: make([]*view.SecurityGroup, 0, len(reply.Items)), Total: reply.Total}
	for _, item := range reply.Items {
		if !validSecurityGroup(item, t, "") {
			return nil, invalidNetworkReply()
		}
		v := new(view.SecurityGroup)
		if e = securityGroupWire(item, v); e != nil {
			return nil, e
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}
func (s *NetworkService) UpdateSecurityGroup(ctx context.Context, req *view.UpdateSecurityGroupRequest) (*view.SecurityGroupResponse, error) {
	if req == nil || req.Data == nil || !securityGroupIDPattern.MatchString(req.SecurityGroupId) || req.UpdateMask == nil {
		return nil, invalidSecurityGroupRequest()
	}
	// Metadata updates replace both fields atomically; a mask names both explicitly.
	fields := map[string]bool{}
	for _, path := range req.UpdateMask.Paths {
		if path != "name" && path != "description" || fields[path] {
			return nil, invalidSecurityGroupRequest()
		}
		fields[path] = true
	}
	if !fields["name"] || !fields["description"] || strings.TrimSpace(req.Data.Name) == "" {
		return nil, invalidSecurityGroupRequest()
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	d := req.Data
	reply, e := c.UpdateSecurityGroup(ctx, t, a, &network.UpdateSecurityGroupRequest{SecurityGroupId: req.SecurityGroupId, Name: d.Name, Description: d.Description, ExpectedVersion: d.ExpectedVersion, IdempotencyKey: d.IdempotencyKey})
	if e != nil {
		return nil, mapNetworkError(e)
	}
	return securityGroupMutation(reply, t, req.SecurityGroupId)
}
func (s *NetworkService) DeleteSecurityGroup(ctx context.Context, req *view.DeleteSecurityGroupRequest) (*view.SecurityGroupResponse, error) {
	if req == nil || !securityGroupIDPattern.MatchString(req.SecurityGroupId) {
		return nil, invalidSecurityGroupRequest()
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	reply, e := c.DeleteSecurityGroup(ctx, t, a, &network.DeleteSecurityGroupRequest{SecurityGroupId: req.SecurityGroupId})
	if e != nil {
		return nil, mapNetworkError(e)
	}
	return securityGroupMutation(reply, t, req.SecurityGroupId)
}
func (s *NetworkService) ListSecurityGroupRules(ctx context.Context, req *view.ListSecurityGroupRulesRequest) (*view.ListSecurityGroupRulesResponse, error) {
	if req == nil || !securityGroupIDPattern.MatchString(req.SecurityGroupId) || (req.Direction != "" && req.Direction != "ingress" && req.Direction != "egress") {
		return nil, invalidSecurityGroupRequest()
	}
	page, size, e := resolveVPCPage(req.Page, req.PageSize)
	if e != nil {
		return nil, e
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	reply, e := c.ListSecurityGroupRules(ctx, t, a, &network.ListSecurityGroupRulesRequest{SecurityGroupId: req.SecurityGroupId, Direction: req.Direction})
	if e != nil {
		return nil, mapNetworkError(e)
	}
	if reply == nil || reply.Version < 1 || reply.Total != int64(len(reply.Items)) || len(reply.Items) > 200 {
		return nil, invalidNetworkReply()
	}
	// Resource returns one bounded rules snapshot. Slice that snapshot after its direction filter.
	start := (uint64(page) - 1) * uint64(size)
	end := start + uint64(size)
	if end > uint64(len(reply.Items)) {
		end = uint64(len(reply.Items))
	}
	out := &view.ListSecurityGroupRulesResponse{Items: make([]*view.SecurityGroupRule, 0), Total: reply.Total, Version: reply.Version}
	for i, item := range reply.Items {
		if item == nil || !securityGroupRuleIDPattern.MatchString(item.Id) || (req.Direction != "" && item.Direction != req.Direction) {
			return nil, invalidNetworkReply()
		}
		if uint64(i) < start || uint64(i) >= end {
			continue
		}
		v := new(view.SecurityGroupRule)
		if e = securityGroupWire(item, v); e != nil {
			return nil, e
		}
		v.SecurityGroupId = req.SecurityGroupId
		out.Items = append(out.Items, v)
	}
	return out, nil
}
func (s *NetworkService) CreateSecurityGroupRule(ctx context.Context, req *view.CreateSecurityGroupRuleRequest) (*view.SecurityGroupRuleResponse, error) {
	if req == nil || req.Data == nil || !securityGroupIDPattern.MatchString(req.SecurityGroupId) {
		return nil, invalidSecurityGroupRequest()
	}
	rule, e := securityGroupRule(req.Data.Rule)
	if e != nil {
		return nil, e
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	reply, e := c.CreateSecurityGroupRule(ctx, t, a, &network.CreateSecurityGroupRuleRequest{SecurityGroupId: req.SecurityGroupId, Rule: rule, ExpectedVersion: req.Data.ExpectedVersion, IdempotencyKey: req.Data.IdempotencyKey})
	if e != nil {
		return nil, mapNetworkError(e)
	}
	return securityGroupRuleMutation(reply, t, req.SecurityGroupId, "")
}
func (s *NetworkService) UpdateSecurityGroupRule(ctx context.Context, req *view.UpdateSecurityGroupRuleRequest) (*view.SecurityGroupRuleResponse, error) {
	if req == nil || req.Data == nil || !securityGroupIDPattern.MatchString(req.SecurityGroupId) || !securityGroupRuleIDPattern.MatchString(req.RuleId) {
		return nil, invalidSecurityGroupRequest()
	}
	rule, e := securityGroupRule(req.Data.Rule)
	if e != nil {
		return nil, e
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	reply, e := c.UpdateSecurityGroupRule(ctx, t, a, &network.UpdateSecurityGroupRuleRequest{SecurityGroupId: req.SecurityGroupId, RuleId: req.RuleId, Rule: rule, ExpectedVersion: req.Data.ExpectedVersion, IdempotencyKey: req.Data.IdempotencyKey})
	if e != nil {
		return nil, mapNetworkError(e)
	}
	return securityGroupRuleMutation(reply, t, req.SecurityGroupId, req.RuleId)
}
func (s *NetworkService) DeleteSecurityGroupRule(ctx context.Context, req *view.DeleteSecurityGroupRuleRequest) (*view.SecurityGroupRuleResponse, error) {
	if req == nil || !securityGroupIDPattern.MatchString(req.SecurityGroupId) || !securityGroupRuleIDPattern.MatchString(req.RuleId) {
		return nil, invalidSecurityGroupRequest()
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	reply, e := c.DeleteSecurityGroupRule(ctx, t, a, &network.DeleteSecurityGroupRuleRequest{SecurityGroupId: req.SecurityGroupId, RuleId: req.RuleId})
	if e != nil {
		return nil, mapNetworkError(e)
	}
	return securityGroupRuleMutation(reply, t, req.SecurityGroupId, req.RuleId)
}
func (s *NetworkService) ListSecurityGroupBindings(ctx context.Context, req *view.ListSecurityGroupBindingsRequest) (*view.ListSecurityGroupBindingsResponse, error) {
	if req == nil || !securityGroupIDPattern.MatchString(req.SecurityGroupId) || (req.TargetType != "" && req.TargetType != "container" && req.TargetType != "vm" && req.TargetType != "eip") {
		return nil, invalidSecurityGroupRequest()
	}
	page, size, e := resolveVPCPage(req.Page, req.PageSize)
	if e != nil {
		return nil, e
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	reply, e := c.ListSecurityGroupBindings(ctx, t, a, &network.ListSecurityGroupBindingsRequest{SecurityGroupId: req.SecurityGroupId, Page: page, PageSize: size, TargetType: req.TargetType})
	if e != nil {
		return nil, mapNetworkError(e)
	}
	if reply == nil || reply.Total < 0 {
		return nil, invalidNetworkReply()
	}
	out := &view.ListSecurityGroupBindingsResponse{Items: make([]*view.SecurityGroupBinding, 0), Total: reply.Total}
	for _, item := range reply.Items {
		if item == nil || item.TargetId == "" || item.TargetType == "" || (req.TargetType != "" && item.TargetType != req.TargetType) {
			return nil, invalidNetworkReply()
		}
		found := false
		for _, id := range item.SecurityGroupIds {
			found = found || id == req.SecurityGroupId
		}
		for _, id := range item.AppliedSecurityGroupIds {
			found = found || id == req.SecurityGroupId
		}
		if !found {
			return nil, invalidNetworkReply()
		}
		v := new(view.SecurityGroupBinding)
		if e = securityGroupWire(item, v); e != nil {
			return nil, e
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

type eipSecurityGroupReplyValue interface {
	GetBinding() *network.SecurityGroupBinding
	GetOperation() *network.Operation
}

func eipSecurityGroupReply(reply eipSecurityGroupReplyValue, id string) (*view.SetEIPSecurityGroupsResponse, error) {
	if reply == nil || reply.GetBinding() == nil || reply.GetBinding().TargetId != id || reply.GetBinding().TargetType != "eip" {
		return nil, invalidNetworkReply()
	}
	out := &view.SetEIPSecurityGroupsResponse{Binding: new(view.SecurityGroupBinding)}
	if e := securityGroupWire(reply.GetBinding(), out.Binding); e != nil {
		return nil, e
	}
	if reply.GetOperation() != nil {
		out.Operation = wireOperation(reply.GetOperation())
	}
	return out, nil
}
func (s *NetworkService) SetEIPSecurityGroups(ctx context.Context, req *view.SetEIPSecurityGroupsRequest) (*view.SetEIPSecurityGroupsResponse, error) {
	if req == nil || req.Data == nil || !eipIDPattern.MatchString(req.EipId) {
		return nil, invalidSecurityGroupRequest()
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	d := req.Data
	reply, e := c.SetEIPSecurityGroups(ctx, t, a, &network.SetEIPSecurityGroupsRequest{EipId: req.EipId, SecurityGroupIds: append([]string(nil), d.SecurityGroupIds...), ExpectedVersion: d.ExpectedVersion, IdempotencyKey: d.IdempotencyKey})
	if e != nil {
		return nil, mapNetworkError(e)
	}
	if reply == nil || reply.Operation == nil || reply.Operation.TenantId != t || reply.Operation.ResourceId != req.EipId {
		return nil, invalidNetworkReply()
	}
	return eipSecurityGroupReply(reply, req.EipId)
}
func (s *NetworkService) GetEIPSecurityGroups(ctx context.Context, req *view.GetEIPSecurityGroupsRequest) (*view.SetEIPSecurityGroupsResponse, error) {
	if req == nil || !eipIDPattern.MatchString(req.EipId) {
		return nil, invalidSecurityGroupRequest()
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	reply, e := c.GetEIPSecurityGroups(ctx, t, a, &network.GetEIPSecurityGroupsRequest{EipId: req.EipId})
	if e != nil {
		return nil, mapNetworkError(e)
	}
	return eipSecurityGroupReply(reply, req.EipId)
}
func (s *NetworkService) GetSecurityGroupOperation(ctx context.Context, req *view.GetSecurityGroupOperationRequest) (*view.GetOperationResponse, error) {
	if req == nil || !operationIDPattern.MatchString(req.OperationId) {
		return nil, invalidSecurityGroupRequest()
	}
	c, t, a, e := s.securityGroupScope(ctx)
	if e != nil {
		return nil, e
	}
	reply, e := c.GetSecurityGroupOperation(ctx, t, a, &network.GetSecurityGroupOperationRequest{OperationId: req.OperationId})
	if e != nil {
		return nil, mapNetworkError(e)
	}
	if reply == nil || reply.Operation == nil || reply.Operation.TenantId != t || reply.Operation.Id != req.OperationId {
		return nil, invalidNetworkReply()
	}
	return &view.GetOperationResponse{Operation: wireOperation(reply.Operation)}, nil
}

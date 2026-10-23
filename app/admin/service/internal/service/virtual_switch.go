package service

import (
	"context"
	"strings"

	"github.com/go-kratos/kratos/v2/errors"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"regexp"
)

func wireVirtualSwitchNode(v *networkv1.VirtualSwitchNode) *catalogv1.VirtualSwitchNode {
	if v == nil {
		return nil
	}
	return &catalogv1.VirtualSwitchNode{NodeName: v.NodeName, NodeUid: v.NodeUid, Mac: v.Mac, Priority: v.Priority, State: v.State, Reason: v.Reason}
}

func wireVirtualSwitch(v *networkv1.VirtualSwitch) *catalogv1.VirtualSwitch {
	if v == nil {
		return nil
	}
	out := &catalogv1.VirtualSwitch{Id: v.Id, Name: v.Name, State: strings.ToLower(strings.TrimPrefix(v.State.String(), "RESOURCE_STATE_")), Reason: v.Reason, ReasonMessage: v.ReasonMessage, Version: v.Version, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, LastOperationId: v.LastOperationId, ClusterId: v.ClusterId, GroupName: v.GroupName, BindingState: v.BindingState, BindingVersion: v.BindingVersion, MembershipFingerprint: v.MembershipFingerprint}
	for _, node := range v.Nodes {
		out.Nodes = append(out.Nodes, wireVirtualSwitchNode(node))
	}
	return out
}

func (s *PlatformNetworkService) CreateVirtualSwitch(ctx context.Context, req *catalogv1.CreateVirtualSwitchRequest) (*catalogv1.CreateVirtualSwitchResponse, error) {
	actor, err := s.actor(ctx, "CreateVirtualSwitch")
	if err != nil {
		return nil, err
	}
	if req == nil || req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	reply, err := s.client.CreateVirtualSwitch(ctx, actor, &networkv1.CreateVirtualSwitchRequest{Name: req.Data.Name, IdempotencyKey: req.Data.IdempotencyKey})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	out := wireVirtualSwitch(reply.Resource)
	if !validVirtualSwitch(out, "") {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.CreateVirtualSwitchResponse{Resource: out}, nil
}

func (s *PlatformNetworkService) GetVirtualSwitch(ctx context.Context, req *catalogv1.GetVirtualSwitchRequest) (*catalogv1.GetVirtualSwitchResponse, error) {
	actor, err := s.actor(ctx, "GetVirtualSwitch")
	if err != nil {
		return nil, err
	}
	if req == nil || !auth.ValidPlatformNetworkID("virtual_switch_id", req.GetVirtualSwitchId()) {
		return nil, errors.BadRequest("INVALID_VIRTUAL_SWITCH_ID", "invalid virtual switch ID")
	}
	reply, err := s.client.GetVirtualSwitch(ctx, actor, &networkv1.GetVirtualSwitchRequest{VirtualSwitchId: req.VirtualSwitchId})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	out := wireVirtualSwitch(reply.Resource)
	if !validVirtualSwitch(out, req.VirtualSwitchId) {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetVirtualSwitchResponse{Resource: out}, nil
}

func (s *PlatformNetworkService) ListVirtualSwitches(ctx context.Context, req *catalogv1.ListVirtualSwitchesRequest) (*catalogv1.ListVirtualSwitchesResponse, error) {
	actor, err := s.actor(ctx, "ListVirtualSwitches")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "request required")
	}
	page, size := int32(1), int32(20)
	if req.Page != nil {
		page = *req.Page
	}
	if req.PageSize != nil {
		size = *req.PageSize
	}
	if page < 1 || size < 1 || size > 100 {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "invalid virtual switch page")
	}
	state, err := platformListState(req.State)
	if err != nil {
		return nil, err
	}
	reply, err := s.client.ListVirtualSwitches(ctx, actor, &networkv1.ListVirtualSwitchesRequest{Name: req.Name, State: state, Page: page, PageSize: size})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil || reply.Total < 0 || reply.Total < int64(len(reply.Items)) || len(reply.Items) > int(size) {
		return nil, invalidNetworkReply()
	}
	out := &catalogv1.ListVirtualSwitchesResponse{Items: make([]*catalogv1.VirtualSwitch, 0, len(reply.Items)), Total: reply.Total}
	seen := make(map[string]bool, len(reply.Items))
	for _, v := range reply.Items {
		item := wireVirtualSwitch(v)
		if !validVirtualSwitch(item, "") || seen[item.Id] {
			return nil, invalidNetworkReply()
		}
		seen[item.Id] = true
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func (s *PlatformNetworkService) DeleteVirtualSwitch(ctx context.Context, req *catalogv1.DeleteVirtualSwitchRequest) (*catalogv1.DeleteVirtualSwitchResponse, error) {
	actor, err := s.actor(ctx, "DeleteVirtualSwitch")
	if err != nil {
		return nil, err
	}
	if req == nil || !auth.ValidPlatformNetworkID("virtual_switch_id", req.GetVirtualSwitchId()) {
		return nil, errors.BadRequest("INVALID_VIRTUAL_SWITCH_ID", "invalid virtual switch ID")
	}
	reply, err := s.client.DeleteVirtualSwitch(ctx, actor, &networkv1.DeleteVirtualSwitchRequest{VirtualSwitchId: req.VirtualSwitchId})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	out := wireVirtualSwitch(reply.Resource)
	if !validVirtualSwitch(out, req.VirtualSwitchId) {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.DeleteVirtualSwitchResponse{Resource: out}, nil
}

var virtualSwitchName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_-]{0,61}[A-Za-z0-9])?$`)

// Virtual switches have their own contract. A persisted unbound switch does not
// have a Provider object, cluster identity, or the PlatformResource configuration.
func validVirtualSwitch(v *catalogv1.VirtualSwitch, id string) bool {
	if v == nil || !auth.ValidPlatformNetworkID("virtual_switch_id", v.Id) || (id != "" && v.Id != id) || !virtualSwitchName.MatchString(v.Name) || v.Version < 1 {
		return false
	}
	switch v.State {
	case "provisioning", "available", "degraded", "failed", "deleting", "deleted":
	default:
		return false
	}
	if v.LastOperationId != "" && !auth.ValidPlatformNetworkID("operation_id", v.LastOperationId) {
		return false
	}
	if v.GroupName != "" && v.GroupName != v.Name {
		return false
	}
	if v.ClusterId == "" {
		return v.BindingVersion == 0 && v.MembershipFingerprint == "" && len(v.Nodes) == 0 && (v.BindingState == "" || v.BindingState == "unbound" || (v.State == "deleted" && v.BindingState == "cleaned"))
	}
	if v.GroupName == "" || v.BindingVersion < 1 || v.MembershipFingerprint == "" || len(v.Nodes) == 0 || v.BindingState == "" || v.BindingState == "unbound" {
		return false
	}
	seen := make(map[string]bool, len(v.Nodes))
	for _, node := range v.Nodes {
		if node == nil || node.NodeName == "" || node.NodeUid == "" || node.Mac == "" || node.Priority != 100 || node.State == "" || seen[node.NodeUid] {
			return false
		}
		seen[node.NodeUid] = true
	}
	return true
}

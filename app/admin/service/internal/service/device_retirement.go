package service

import (
	"context"

	"github.com/go-kratos/kratos/v2/errors"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
)

func wireDeviceRetirement(v *networkv1.DeviceRetirement) *catalogv1.DeviceRetirement {
	if v == nil {
		return nil
	}
	return &catalogv1.DeviceRetirement{DeviceId: v.DeviceId, OperationId: v.OperationId, State: v.State, Reason: v.Reason, Version: v.Version, ExpectedVersion: v.ExpectedVersion, ConfigUid: v.ConfigUid, FenceVersion: v.FenceVersion, PendingUnknown: v.PendingUnknown, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, CompletedAt: v.CompletedAt}
}

func (s *PlatformNetworkService) RetireNetworkDevice(ctx context.Context, req *catalogv1.RetireNetworkDeviceRequest) (*catalogv1.RetireNetworkDeviceResponse, error) {
	actor, err := s.actor(ctx, "RetireNetworkDevice")
	if err != nil {
		return nil, err
	}
	if req == nil || req.Data == nil {
		return nil, errors.BadRequest("INVALID_PLATFORM_NETWORK_REQUEST", "data required")
	}
	if !auth.ValidPlatformNetworkID("device_id", req.DeviceId) {
		return nil, errors.BadRequest("INVALID_NETWORK_DEVICE_ID", "invalid network device ID")
	}
	reply, err := s.client.RetireNetworkDevice(ctx, actor, &networkv1.RetireNetworkDeviceRequest{DeviceId: req.DeviceId, ExpectedVersion: req.Data.ExpectedVersion, IdempotencyKey: req.Data.IdempotencyKey})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	out := wireDeviceRetirement(reply.Retirement)
	if !validDeviceRetirement(out, req.DeviceId) {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.RetireNetworkDeviceResponse{Retirement: out}, nil
}

func (s *PlatformNetworkService) GetNetworkDeviceRetirement(ctx context.Context, req *catalogv1.GetNetworkDeviceRetirementRequest) (*catalogv1.GetNetworkDeviceRetirementResponse, error) {
	actor, err := s.actor(ctx, "GetNetworkDeviceRetirement")
	if err != nil {
		return nil, err
	}
	if req == nil || !auth.ValidPlatformNetworkID("device_id", req.GetDeviceId()) {
		return nil, errors.BadRequest("INVALID_NETWORK_DEVICE_ID", "invalid network device ID")
	}
	reply, err := s.client.GetNetworkDeviceRetirement(ctx, actor, &networkv1.GetNetworkDeviceRetirementRequest{DeviceId: req.DeviceId})
	if err != nil {
		return nil, mapNetworkError(err)
	}
	if reply == nil {
		return nil, invalidNetworkReply()
	}
	out := wireDeviceRetirement(reply.Retirement)
	if !validDeviceRetirement(out, req.DeviceId) {
		return nil, invalidNetworkReply()
	}
	return &catalogv1.GetNetworkDeviceRetirementResponse{Retirement: out}, nil
}

func validDeviceRetirement(v *catalogv1.DeviceRetirement, deviceID string) bool {
	if v == nil || !auth.ValidPlatformNetworkID("device_id", v.DeviceId) || (deviceID != "" && deviceID != v.DeviceId) || !auth.ValidPlatformNetworkID("operation_id", v.OperationId) || v.Version < 1 || v.ExpectedVersion < 1 {
		return false
	}
	switch v.State {
	case "closing", "blocked":
		return v.CompletedAt == nil
	case "retired":
		return !v.PendingUnknown && v.FenceVersion > v.ExpectedVersion && v.ConfigUid != "" && v.CompletedAt != nil
	default:
		return false
	}
}

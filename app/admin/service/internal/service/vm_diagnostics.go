package service

import (
	"context"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	instance "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"google.golang.org/grpc"
)

// VMLogSession contains only the authenticated scope and the protected
// Resource stream. The HTTP handler owns its bounded lifetime and cancellation.
type VMLogSession struct {
	TenantID string
	VMID     string
	Stream   grpc.ServerStreamingClient[instance.InstanceLogFrame]
}

func (s *VMService) OpenVMLogs(ctx context.Context, r *view.VMLogsRequest) (*VMLogSession, error) {
	if r == nil {
		return nil, kratoserrors.BadRequest("VM_INVALID_REQUEST", "request required")
	}
	if parsed, err := uuid.Parse(r.VmId); err != nil || parsed.String() != r.VmId {
		return nil, kratoserrors.BadRequest("VM_INVALID_REQUEST", "invalid VM identifier")
	}
	tail, follow := uint32(100), true
	if r.TailLines != nil {
		tail = *r.TailLines
	}
	if r.Follow != nil {
		follow = *r.Follow
	}
	if tail < 1 || tail > 1000 {
		return nil, kratoserrors.BadRequest("VM_INVALID_REQUEST", "invalid log options")
	}
	tenant, actor, err := s.scope(ctx, "logs")
	if err != nil {
		return nil, err
	}
	if s.binding == nil || s.binding.client == nil {
		return nil, kratoserrors.ServiceUnavailable("VM_ADAPTER_UNAVAILABLE", "VM owner unavailable")
	}
	stream, err := s.binding.client.Logs(ctx, tenant, actor, r.VmId, tail, follow)
	if err != nil {
		return nil, err
	}
	if stream == nil {
		return nil, kratoserrors.ServiceUnavailable("VM_LOG_TARGET_UNAVAILABLE", "VM log target unavailable")
	}
	return &VMLogSession{TenantID: tenant, VMID: r.VmId, Stream: stream}, nil
}

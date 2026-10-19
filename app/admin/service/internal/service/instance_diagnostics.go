package service

import (
	"context"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type instanceDiagnosticsClient interface {
	Runtime(context.Context, string, string, string) (*res.InstanceRuntime, error)
	Events(context.Context, string, string, string) (*res.InstanceEvents, error)
	Logs(context.Context, string, string, string, uint32, bool) (grpc.ServerStreamingClient[res.InstanceLogFrame], error)
}

func (s *InstanceService) diagnosticScope(ctx context.Context, path, id string) (instanceDiagnosticsClient, string, string, error) {
	if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
		return nil, "", "", kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid instance identifier")
	}
	tenant, actor, err := s.scope(ctx, path, "GET")
	if err != nil {
		return nil, "", "", err
	}
	client, ok := s.binding.client.(instanceDiagnosticsClient)
	if !ok || client == nil {
		return nil, "", "", kratoserrors.ServiceUnavailable("INSTANCE_ADAPTER_UNAVAILABLE", "instance diagnostics unavailable")
	}
	return client, tenant, actor, nil
}

// Project only the dedicated safe read contract. Optional Resource scalars map
// to nullable public wrappers; absent observations remain unknown, not zero.
func projectInstanceDiagnostic(from, to proto.Message) error {
	if from == nil || !from.ProtoReflect().IsValid() {
		return kratoserrors.ServiceUnavailable("INSTANCE_DIAGNOSTIC_TARGET_UNAVAILABLE", "instance diagnostics unavailable")
	}
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(from)
	if err == nil {
		err = protojson.Unmarshal(raw, to)
	}
	if err != nil {
		return kratoserrors.ServiceUnavailable("INSTANCE_DIAGNOSTIC_TARGET_UNAVAILABLE", "instance diagnostics unavailable")
	}
	return nil
}

func (s *InstanceService) GetInstanceRuntime(ctx context.Context, r *view.GetInstanceRequest) (*view.InstanceRuntime, error) {
	if r == nil {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "request required")
	}
	client, tenant, actor, err := s.diagnosticScope(ctx, data.InstanceRuntimePath, r.InstanceId)
	if err != nil {
		return nil, err
	}
	observed, err := client.Runtime(ctx, tenant, actor, r.InstanceId)
	if err != nil {
		return nil, err
	}
	out := new(view.InstanceRuntime)
	if err = projectInstanceDiagnostic(observed, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *InstanceService) ListInstanceEvents(ctx context.Context, r *view.GetInstanceRequest) (*view.InstanceEvents, error) {
	if r == nil {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "request required")
	}
	client, tenant, actor, err := s.diagnosticScope(ctx, data.InstanceEventsPath, r.InstanceId)
	if err != nil {
		return nil, err
	}
	observed, err := client.Events(ctx, tenant, actor, r.InstanceId)
	if err != nil {
		return nil, err
	}
	out := new(view.InstanceEvents)
	if err = projectInstanceDiagnostic(observed, out); err != nil {
		return nil, err
	}
	if out.Events == nil {
		out.Events = []*view.InstanceEvent{}
	}
	return out, nil
}

// InstanceLogSession contains only the authenticated scope and the protected
// Resource stream. The HTTP handler owns its bounded lifetime and cancellation.
type InstanceLogSession struct {
	TenantID   string
	InstanceID string
	Stream     grpc.ServerStreamingClient[res.InstanceLogFrame]
}

func (s *InstanceService) OpenInstanceLogs(ctx context.Context, r *view.InstanceLogsRequest) (*InstanceLogSession, error) {
	if r == nil {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "request required")
	}
	tail, follow := uint32(100), true
	if r.TailLines != nil {
		tail = *r.TailLines
	}
	if r.Follow != nil {
		follow = *r.Follow
	}
	if tail < 1 || tail > 1000 {
		return nil, kratoserrors.BadRequest("INVALID_INSTANCE_REQUEST", "invalid log options")
	}
	client, tenant, actor, err := s.diagnosticScope(ctx, data.InstanceLogsPath, r.InstanceId)
	if err != nil {
		return nil, err
	}
	stream, err := client.Logs(ctx, tenant, actor, r.InstanceId, tail, follow)
	if err != nil {
		return nil, err
	}
	if stream == nil {
		return nil, kratoserrors.ServiceUnavailable("INSTANCE_DIAGNOSTIC_TARGET_UNAVAILABLE", "instance diagnostics unavailable")
	}
	return &InstanceLogSession{TenantID: tenant, InstanceID: r.InstanceId, Stream: stream}, nil
}

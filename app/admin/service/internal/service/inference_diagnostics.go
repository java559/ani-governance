package service

import (
	"context"
	"strings"

	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type inferenceDiagnosticsOwner interface {
	RuntimeDiagnosticsTrusted(context.Context, string, string, *runtimev1.GetRuntimeDiagnosticsRequest) (*runtimev1.RuntimeDiagnostics, error)
	RuntimeLogsTrusted(context.Context, string, string, *runtimev1.RuntimeLogsRequest) (*runtimev1.RuntimeLogsResponse, error)
	RuntimeEventsTrusted(context.Context, string, string, *runtimev1.ListRuntimeEventsRequest) (*runtimev1.ListRuntimeEventsResponse, error)
	StreamRuntimeLogsTrusted(context.Context, string, string, *runtimev1.RuntimeLogsRequest) (grpc.ServerStreamingClient[runtimev1.RuntimeLogLine], error)
}

func (s *InferenceService) diagnosticsScope(ctx context.Context, id string) (inferenceDiagnosticsOwner, string, string, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id {
		return nil, "", "", status.Error(codes.InvalidArgument, "resource_id required")
	}
	tenant, actor, err := s.trustedInferenceOperator(ctx)
	if err != nil {
		return nil, "", "", err
	}
	owner, ok := s.owner.(inferenceDiagnosticsOwner)
	if !ok {
		return nil, "", "", status.Error(codes.Unavailable, "INFERENCE_DIAGNOSTICS_UNAVAILABLE")
	}
	return owner, tenant, actor, nil
}
func projectInferenceDiagnostic(from, to proto.Message) error {
	if from == nil || !from.ProtoReflect().IsValid() {
		return status.Error(codes.Unavailable, "INVALID_INFERENCE_DIAGNOSTIC")
	}
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(from)
	if err == nil {
		err = protojson.Unmarshal(raw, to)
	}
	if err != nil {
		return status.Error(codes.Unavailable, "INVALID_INFERENCE_DIAGNOSTIC")
	}
	return nil
}
func (s *InferenceService) GetRuntimeDiagnostics(ctx context.Context, in *view.GetRuntimeDiagnosticsRequest) (*view.RuntimeDiagnostics, error) {
	owner, tenant, actor, err := s.diagnosticsScope(ctx, in.GetResourceId())
	if err != nil {
		return nil, err
	}
	observed, err := owner.RuntimeDiagnosticsTrusted(ctx, tenant, actor, &runtimev1.GetRuntimeDiagnosticsRequest{ResourceId: in.ResourceId})
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	out := new(view.RuntimeDiagnostics)
	if err = projectInferenceDiagnostic(observed, out); err != nil {
		return nil, err
	}
	return out, nil
}
func inferenceLogsRequest(in *view.RuntimeLogsRequest) *runtimev1.RuntimeLogsRequest {
	return &runtimev1.RuntimeLogsRequest{ResourceId: in.ResourceId, Instance: in.Instance, Container: in.Container, Since: in.Since, Until: in.Until, TailLines: in.TailLines}
}
func (s *InferenceService) GetRuntimeLogs(ctx context.Context, in *view.RuntimeLogsRequest) (*view.RuntimeLogsResponse, error) {
	owner, tenant, actor, err := s.diagnosticsScope(ctx, in.GetResourceId())
	if err != nil {
		return nil, err
	}
	observed, err := owner.RuntimeLogsTrusted(ctx, tenant, actor, inferenceLogsRequest(in))
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	out := new(view.RuntimeLogsResponse)
	if err = projectInferenceDiagnostic(observed, out); err != nil {
		return nil, err
	}
	return out, nil
}
func (s *InferenceService) ListRuntimeEvents(ctx context.Context, in *view.ListRuntimeEventsRequest) (*view.ListRuntimeEventsResponse, error) {
	owner, tenant, actor, err := s.diagnosticsScope(ctx, in.GetResourceId())
	if err != nil {
		return nil, err
	}
	observed, err := owner.RuntimeEventsTrusted(ctx, tenant, actor, &runtimev1.ListRuntimeEventsRequest{ResourceId: in.ResourceId, Page: in.Page, PageSize: in.PageSize, Since: in.Since, Until: in.Until, Type: in.Type})
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	out := new(view.ListRuntimeEventsResponse)
	if err = projectInferenceDiagnostic(observed, out); err != nil {
		return nil, err
	}
	return out, nil
}

type InferenceLogSession struct {
	TenantID string
	Stream   grpc.ServerStreamingClient[runtimev1.RuntimeLogLine]
}

func (s *InferenceService) OpenRuntimeLogs(ctx context.Context, in *view.RuntimeLogsRequest) (*InferenceLogSession, error) {
	owner, tenant, actor, err := s.diagnosticsScope(ctx, in.GetResourceId())
	if err != nil {
		return nil, err
	}
	stream, err := owner.StreamRuntimeLogsTrusted(ctx, tenant, actor, inferenceLogsRequest(in))
	if err != nil {
		return nil, mapInferenceOwnerError(err)
	}
	if stream == nil {
		return nil, status.Error(codes.Unavailable, "INFERENCE_DIAGNOSTICS_UNAVAILABLE")
	}
	return &InferenceLogSession{TenantID: tenant, Stream: stream}, nil
}

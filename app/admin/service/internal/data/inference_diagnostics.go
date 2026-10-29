package data

import (
	"context"
	"time"

	"github.com/google/uuid"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Runtime diagnostics can include an invocation probe. Ordinary owner RPCs
// retain their shorter budget, while this call remains bounded by its caller.
const inferenceRuntimeDiagnosticsTimeout = 15 * time.Second
const inferenceRuntimeLogsTimeout = 15 * time.Second

func (c *InferenceClient) RuntimeDiagnosticsTrusted(ctx context.Context, tenant, actor string, request *runtimev1.GetRuntimeDiagnosticsRequest) (*runtimev1.RuntimeDiagnostics, error) {
	if c == nil || c.diagnostics == nil {
		return nil, status.Error(codes.Unavailable, "INFERENCE_DIAGNOSTICS_UNAVAILABLE")
	}
	call, cancel, err := c.trustedContextWithTimeout(ctx, tenant, actor, inferenceRuntimeDiagnosticsTimeout)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid trusted inference identity")
	}
	defer cancel()
	return c.diagnostics.GetRuntimeDiagnostics(call, request)
}
func (c *InferenceClient) RuntimeLogsTrusted(ctx context.Context, tenant, actor string, request *runtimev1.RuntimeLogsRequest) (*runtimev1.RuntimeLogsResponse, error) {
	if c == nil || c.diagnostics == nil {
		return nil, status.Error(codes.Unavailable, "INFERENCE_DIAGNOSTICS_UNAVAILABLE")
	}
	call, cancel, err := c.trustedContextWithTimeout(ctx, tenant, actor, inferenceRuntimeLogsTimeout)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid trusted inference identity")
	}
	defer cancel()
	return c.diagnostics.GetRuntimeLogs(call, request)
}
func (c *InferenceClient) RuntimeEventsTrusted(ctx context.Context, tenant, actor string, request *runtimev1.ListRuntimeEventsRequest) (*runtimev1.ListRuntimeEventsResponse, error) {
	if c == nil || c.diagnostics == nil {
		return nil, status.Error(codes.Unavailable, "INFERENCE_DIAGNOSTICS_UNAVAILABLE")
	}
	call, cancel, err := c.trustedContext(ctx, tenant, actor)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid trusted inference identity")
	}
	defer cancel()
	return c.diagnostics.ListRuntimeEvents(call, request)
}

// The HTTP handler owns cancellation and the five-minute stream lifetime.
// Rebuild metadata here too; do not use the unary client's three-second timeout.
func (c *InferenceClient) StreamRuntimeLogsTrusted(ctx context.Context, tenant, actor string, request *runtimev1.RuntimeLogsRequest) (grpc.ServerStreamingClient[runtimev1.RuntimeLogLine], error) {
	if c == nil || c.diagnostics == nil {
		return nil, status.Error(codes.Unavailable, "INFERENCE_DIAGNOSTICS_UNAVAILABLE")
	}
	if !validInferenceIdentity(tenant, actor) {
		return nil, status.Error(codes.Unauthenticated, "invalid trusted inference identity")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Minute {
		return nil, status.Error(codes.InvalidArgument, "bounded stream deadline required")
	}
	call := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return c.diagnostics.StreamRuntimeLogs(call, request)
}

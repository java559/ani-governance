package service

import (
	"context"
	"testing"

	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type metricsDiagnosticsOwnerProbe struct {
	directInferenceOwnerProbe
	calls         int
	tenant, actor string
}

func (p *metricsDiagnosticsOwnerProbe) RuntimeDiagnosticsTrusted(_ context.Context, tenant, actor string, _ *runtimev1.GetRuntimeDiagnosticsRequest) (*runtimev1.RuntimeDiagnostics, error) {
	p.calls++
	p.tenant, p.actor = tenant, actor
	return &runtimev1.RuntimeDiagnostics{Instances: []*runtimev1.RuntimeInstance{{Name: "owner-pod", Uid: "33333333-3333-4333-8333-333333333333"}}}, nil
}
func (*metricsDiagnosticsOwnerProbe) RuntimeLogsTrusted(context.Context, string, string, *runtimev1.RuntimeLogsRequest) (*runtimev1.RuntimeLogsResponse, error) {
	panic("unused")
}
func (*metricsDiagnosticsOwnerProbe) RuntimeEventsTrusted(context.Context, string, string, *runtimev1.ListRuntimeEventsRequest) (*runtimev1.ListRuntimeEventsResponse, error) {
	panic("unused")
}
func (*metricsDiagnosticsOwnerProbe) StreamRuntimeLogsTrusted(context.Context, string, string, *runtimev1.RuntimeLogsRequest) (grpc.ServerStreamingClient[runtimev1.RuntimeLogLine], error) {
	panic("unused")
}

type metricsTenantProbe struct{ calls int }

func (p *metricsTenantProbe) ResourceTenantID(context.Context, uint32) (string, error) {
	p.calls++
	return "11111111-1111-4111-8111-111111111111", nil
}

type inferenceMetricsReaderProbe struct {
	calls     int
	instances []*view.RuntimeInstance
}

func (p *inferenceMetricsReaderProbe) Query(_ context.Context, _ *view.InferenceMetricsRequest, instances []*view.RuntimeInstance) (*view.InferenceMetricsResponse, error) {
	p.calls++
	p.instances = instances
	return &view.InferenceMetricsResponse{}, nil
}

func TestInferenceMetricsUnconfiguredFailsBeforeOwnerProbeButKeepsIdentity(t *testing.T) {
	owner, tenants := &metricsDiagnosticsOwnerProbe{}, &metricsTenantProbe{}
	s := NewInferenceServiceWithOwner(nil, owner, tenants)
	id := "22222222-2222-4222-8222-222222222222"
	_, err := s.GetInferenceMetrics(context.Background(), &view.InferenceMetricsRequest{ResourceId: id})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Zero(t, owner.calls)
	require.Zero(t, tenants.calls)
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, TenantID: 26, ID: 33})
	_, err = s.GetInferenceMetrics(ctx, &view.InferenceMetricsRequest{ResourceId: id})
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Equal(t, "metrics source is not configured", status.Convert(err).Message())
	require.Equal(t, 1, tenants.calls, "the persisted tenant mapping must still be checked")
	require.Zero(t, owner.calls, "no invocation probe can help an unconfigured metrics source")
	_, err = s.GetInferenceMetrics(ctx, &view.InferenceMetricsRequest{ResourceId: " "})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Zero(t, owner.calls)
}

func TestInferenceMetricsUsesOneTrustedOwnerReadAndOriginalPodUID(t *testing.T) {
	owner, tenants, reader := &metricsDiagnosticsOwnerProbe{}, &metricsTenantProbe{}, &inferenceMetricsReaderProbe{}
	s := NewInferenceServiceWithOwner(nil, owner, tenants)
	require.NoError(t, s.ConfigureDiagnostics("", reader))
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, TenantID: 26, ID: 33})
	_, err := s.GetInferenceMetrics(ctx, &view.InferenceMetricsRequest{ResourceId: "22222222-2222-4222-8222-222222222222"})
	require.NoError(t, err)
	require.Equal(t, 1, tenants.calls)
	require.Equal(t, 1, owner.calls)
	require.Equal(t, "11111111-1111-4111-8111-111111111111", owner.tenant)
	require.Equal(t, "governance:user:33", owner.actor)
	require.Equal(t, 1, reader.calls)
	require.Len(t, reader.instances, 1)
	require.Equal(t, "33333333-3333-4333-8333-333333333333", reader.instances[0].Uid)
}

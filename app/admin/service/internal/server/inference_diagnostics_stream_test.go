package server

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

type streamTestOwner struct{}

func (streamTestOwner) Create(context.Context, *runtimev1.CreateInferenceServiceRequest) (*runtimev1.OperationResponse, error) {
	return nil, nil
}

func (streamTestOwner) Delete(context.Context, *runtimev1.DeleteInferenceServiceRequest) (*runtimev1.OperationResponse, error) {
	return nil, nil
}

func (streamTestOwner) RuntimeDiagnosticsTrusted(context.Context, string, string, *runtimev1.GetRuntimeDiagnosticsRequest) (*runtimev1.RuntimeDiagnostics, error) {
	return nil, nil
}

func (streamTestOwner) RuntimeLogsTrusted(context.Context, string, string, *runtimev1.RuntimeLogsRequest) (*runtimev1.RuntimeLogsResponse, error) {
	return nil, nil
}

func (streamTestOwner) RuntimeEventsTrusted(context.Context, string, string, *runtimev1.ListRuntimeEventsRequest) (*runtimev1.ListRuntimeEventsResponse, error) {
	return nil, nil
}

func (streamTestOwner) StreamRuntimeLogsTrusted(ctx context.Context, _, _ string, _ *runtimev1.RuntimeLogsRequest) (grpc.ServerStreamingClient[runtimev1.RuntimeLogLine], error) {
	return &infoThenBlockStream{ctx: ctx, line: &runtimev1.RuntimeLogLine{Message: "INFO upstream ready"}}, nil
}

type streamTestTenantResolver struct{}

func (streamTestTenantResolver) ResourceTenantID(context.Context, uint32) (string, error) {
	return "11111111-1111-4111-8111-111111111111", nil
}

type infoThenBlockStream struct {
	ctx  context.Context
	line *runtimev1.RuntimeLogLine
	sent bool
}

func (s *infoThenBlockStream) Recv() (*runtimev1.RuntimeLogLine, error) {
	if !s.sent {
		s.sent = true
		return s.line, nil
	}
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func (s *infoThenBlockStream) Header() (metadata.MD, error) { return nil, nil }
func (s *infoThenBlockStream) Trailer() metadata.MD         { return nil }
func (s *infoThenBlockStream) CloseSend() error             { return nil }
func (s *infoThenBlockStream) Context() context.Context     { return s.ctx }
func (s *infoThenBlockStream) SendMsg(any) error            { return nil }
func (s *infoThenBlockStream) RecvMsg(any) error            { return io.EOF }

var _ proto.Message = (*runtimev1.RuntimeLogLine)(nil)

func TestInferenceLogStreamStopsOpeningTimeoutOnFilteredFrame(t *testing.T) {
	s := service.NewInferenceServiceWithOwner(nil, streamTestOwner{}, streamTestTenantResolver{})
	h := &inferenceLogHTTP{service: s, tenants: make(map[string]int)}
	ctx, cancel := context.WithTimeout(auth.NewPrincipalContext(context.Background(), &auth.Principal{
		Type: auth.SubjectUser, TenantID: 7, ID: 9,
	}), 100*time.Millisecond)
	defer cancel()

	w := httptest.NewRecorder()
	err := h.stream(ctx, w, &view.RuntimeLogsRequest{ResourceId: "service-1", Level: "error"})
	require.NoError(t, err, "an upstream INFO frame proves readiness even when the requested level filters it")
}

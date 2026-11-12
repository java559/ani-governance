package server

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	quotapb "github.com/zhangzhe-ctrl/ani-governance/api/quota/gen/go/quota/service/v1"

	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
)

// waitForCompletions polls until the server's deferred stats End has flushed the
// expected number of completion records; the client can observe the status
// before that goroutine finishes.
func waitForCompletions(t *testing.T, buf *bytes.Buffer, count int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		recs := records(t, buf)
		found := 0
		for _, rec := range recs {
			if rec["event"] == "rpc.server.completed" {
				found++
			}
		}
		if found >= count || time.Now().After(deadline) {
			return recs
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type noopStatsHandler struct{}

func (noopStatsHandler) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}
func (noopStatsHandler) HandleRPC(context.Context, stats.RPCStats) {}
func (noopStatsHandler) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}
func (noopStatsHandler) HandleConn(context.Context, stats.ConnStats) {}

func TestQuotaRPCCompletionLevels(t *testing.T) {
	for _, test := range []struct {
		code   codes.Code
		level  bLogger.Level
		result string
	}{
		{codes.OK, bLogger.LevelInfo, "succeeded"},
		{codes.InvalidArgument, bLogger.LevelWarn, "rejected"},
		{codes.Unauthenticated, bLogger.LevelWarn, "rejected"},
		{codes.DeadlineExceeded, bLogger.LevelError, "failed"},
		{codes.Internal, bLogger.LevelError, "failed"},
		{codes.Unimplemented, bLogger.LevelError, "failed"},
		{codes.Unavailable, bLogger.LevelError, "failed"},
	} {
		level, result := quotaRPCCompletion(test.code)
		require.Equal(t, test.level, level, test.code)
		require.Equal(t, test.result, result, test.code)
	}
}

func TestQuotaStatsHandlerRecordsDeadlineAsError(t *testing.T) {
	logger, buf := newTestLogger(t)
	handler := &quotaStatsHandler{delegate: noopStatsHandler{}, logger: logger}
	ctx := traceCtx(t, 0x41)
	ctx = context.WithValue(ctx, quotaRPCKey{}, &quotaRPCState{method: quotapb.QuotaReleaseService_ReportResourceReleased_FullMethodName})
	begin := time.Now()
	handler.HandleRPC(ctx, &stats.End{
		BeginTime: begin,
		EndTime:   begin.Add(3 * time.Millisecond),
		Error:     status.Error(codes.DeadlineExceeded, "deadline exceeded"),
	})

	rec := findEvent(records(t, buf), "rpc.server.completed")
	require.NotNil(t, rec)
	require.Equal(t, "ERROR", rec["level"])
	require.Equal(t, "failed", rec["result"])
	require.Equal(t, "DeadlineExceeded", rec["rpc.code"])
	require.Equal(t, quotapb.QuotaReleaseService_ReportResourceReleased_FullMethodName, rec["rpc.method"])
	require.Equal(t, "41414141414141414141414141414141", rec["trace_id"])
	require.Greater(t, rec["duration_ms"].(float64), 0.0)
	require.Contains(t, rec["error"], "deadline exceeded")
}

type corruptProtoCodec struct{}

func (corruptProtoCodec) Marshal(any) ([]byte, error) { return []byte{0xff, 0xff, 0xff, 0xff}, nil }
func (corruptProtoCodec) Unmarshal([]byte, any) error { return nil }
func (corruptProtoCodec) Name() string                { return "proto" }

func TestQuotaListenerRecordsUnknownMethodAndDecodeFailure(t *testing.T) {
	logger, buf := newTestLogger(t)
	previous := bLogger.GetLogger()
	bLogger.SetLogger(logger)
	t.Cleanup(func() { bLogger.SetLogger(previous) })

	srv, dir, pool := quotaLifecycleServer(t)
	start := quotaStartAsync(srv)
	t.Cleanup(func() {
		_ = srv.Stop(context.Background())
		quotaStartResult(t, start)
	})
	conn := quotaLifecycleConn(t, srv, dir, pool)

	err := conn.Invoke(context.Background(), "/quota.service.v1.QuotaReleaseService/NoSuchMethod", &emptypb.Empty{}, &emptypb.Empty{})
	require.Equal(t, codes.Unimplemented, status.Code(err))

	err = conn.Invoke(context.Background(), quotapb.QuotaReleaseService_ReportQuotaRelease_FullMethodName,
		&quotapb.ReportQuotaReleaseRequest{}, &quotapb.ReportQuotaReleaseResponse{}, grpc.ForceCodec(corruptProtoCodec{}))
	require.Equal(t, codes.Internal, status.Code(err))

	recs := waitForCompletions(t, buf, 2)
	var unknown, decode *map[string]any
	for i := range recs {
		rec := &recs[i]
		if (*rec)["event"] != "rpc.server.completed" {
			continue
		}
		switch (*rec)["rpc.code"] {
		case "Unimplemented":
			unknown = rec
		case "Internal":
			decode = rec
		}
	}
	require.NotNil(t, unknown, "unknown method produced no completion event: %s", buf.String())
	require.Equal(t, "ERROR", (*unknown)["level"])
	require.Equal(t, "failed", (*unknown)["result"])
	require.Equal(t, "/quota.service.v1.QuotaReleaseService/NoSuchMethod", (*unknown)["rpc.method"])
	require.NotEmpty(t, (*unknown)["trace_id"])

	require.NotNil(t, decode, "decode failure produced no completion event: %s", buf.String())
	require.Equal(t, "ERROR", (*decode)["level"])
	require.Equal(t, "failed", (*decode)["result"])
	require.Equal(t, quotapb.QuotaReleaseService_ReportQuotaRelease_FullMethodName, (*decode)["rpc.method"])
	require.Contains(t, (*decode)["error"], "unmarshalling request")
}

type slowQuotaRelease struct {
	quotapb.UnimplementedQuotaReleaseServiceServer
}

func (slowQuotaRelease) ReportResourceReleased(ctx context.Context, _ *quotapb.ReportResourceReleasedRequest) (*quotapb.ReportResourceReleasedResponse, error) {
	<-ctx.Done()
	return nil, status.Error(codes.DeadlineExceeded, "deadline exceeded")
}

func TestQuotaStatsHandlerRecordsServerDeadline(t *testing.T) {
	logger, buf := newTestLogger(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer(grpc.StatsHandler(newQuotaStatsHandler(otelgrpc.NewServerHandler(), logger)))
	quotapb.RegisterQuotaReleaseServiceServer(server, slowQuotaRelease{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = quotapb.NewQuotaReleaseServiceClient(conn).ReportResourceReleased(ctx, &quotapb.ReportResourceReleasedRequest{}, grpc.WaitForReady(true))
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))

	rec := findEvent(waitForCompletions(t, buf, 1), "rpc.server.completed")
	require.NotNil(t, rec, "server deadline produced no completion event: %s", buf.String())
	require.Equal(t, "ERROR", rec["level"])
	require.Equal(t, "failed", rec["result"])
	require.Equal(t, "DeadlineExceeded", rec["rpc.code"])
	require.Equal(t, quotapb.QuotaReleaseService_ReportResourceReleased_FullMethodName, rec["rpc.method"])
	require.NotEmpty(t, rec["trace_id"])
}

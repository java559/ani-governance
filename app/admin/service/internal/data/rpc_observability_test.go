package data

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
)

// LG10: every outbound connection shares one instrumentation option set. This
// test drives the real dial option through a real gRPC server and asserts the
// two observable outcomes: the server receives a valid W3C traceparent, and the
// client emits exactly one rpc.client.completed carrying the real method/code.

func TestOutboundDialInjectsTraceparentAndLogsCompletion(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	traced := newTracedHealthServer(t)
	var got metadata.MD
	traced.onCall = func(md metadata.MD) { got = md }

	var buf bytes.Buffer
	installTestLogger(t, &buf)

	dialOpts := outboundDial(peerResourceService, grpc.WithTransportCredentials(insecure.NewCredentials()))
	conn, err := grpc.NewClient(traced.addr, dialOpts...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Call inside a request span; the handler must inject its traceparent.
	traceID, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	spanID, _ := trace.SpanIDFromHex("b7ad6b7169203331")
	ctx, cancel := context.WithCancel(trace.ContextWithSpanContext(context.Background(),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled})))
	defer cancel()

	client := healthpb.NewHealthClient(conn)
	if _, err := client.Check(ctx, &healthpb.HealthCheckRequest{}); err != nil {
		t.Fatalf("Check: %v", err)
	}

	// 1) The peer received a W3C traceparent for the outbound call.
	tp := headerValue(got, "traceparent")
	if tp == "" {
		t.Fatalf("server received no traceparent; metadata=%v", got)
	}
	if !strings.Contains(tp, traceID.String()) {
		t.Errorf("traceparent %q does not carry the caller trace %s", tp, traceID)
	}

	// 2) Exactly one completion record, with the real method/code/duration.
	recs := decodeLogLines(t, &buf)
	var completions []map[string]any
	for _, r := range recs {
		if r["event"] == "rpc.client.completed" {
			completions = append(completions, r)
		}
	}
	if len(completions) != 1 {
		t.Fatalf("expected exactly 1 rpc.client.completed, got %d: %v", len(completions), completions)
	}
	c := completions[0]
	if c["peer.service"] != string(peerResourceService) {
		t.Errorf("peer.service = %v", c["peer.service"])
	}
	if c["rpc.method"] != "/grpc.health.v1.Health/Check" {
		t.Errorf("rpc.method = %v", c["rpc.method"])
	}
	if c["rpc.code"] != "OK" || c["result"] != "succeeded" {
		t.Errorf("rpc.code=%v result=%v, want OK/succeeded", c["rpc.code"], c["result"])
	}
	if _, ok := c["duration_ms"]; !ok {
		t.Error("completion is missing duration_ms")
	}
}

func TestOutboundDialFailureKeepsRealCodeAndError(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	traced := newTracedHealthServer(t)
	traced.failWith = status.Error(codes.Unavailable, "connection refused by peer")

	var buf bytes.Buffer
	installTestLogger(t, &buf)

	conn, err := grpc.NewClient(traced.addr,
		outboundDial(peerAcceleratorService, grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_, _ = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})

	recs := decodeLogLines(t, &buf)
	var completion, depFailed map[string]any
	for _, r := range recs {
		switch r["event"] {
		case "rpc.client.completed":
			completion = r
		case "dependency.failed":
			depFailed = r
		}
	}
	if completion == nil {
		t.Fatalf("no completion for failing RPC: %v", recs)
	}
	if completion["rpc.code"] != "Unavailable" || completion["result"] != "failed" {
		t.Errorf("code=%v result=%v, want Unavailable/failed", completion["rpc.code"], completion["result"])
	}
	if completion["level"] != "ERROR" {
		t.Errorf("Unavailable must be ERROR, got %v", completion["level"])
	}
	if completion["error"] == nil {
		t.Error("failing completion must retain the real error")
	}
	if depFailed == nil {
		t.Fatalf("Unavailable must also emit dependency.failed: %v", recs)
	}
	if depFailed["dependency"] != string(peerAcceleratorService) {
		t.Errorf("dependency.failed dependency = %v", depFailed["dependency"])
	}
}

func TestOutboundDialRejectionIsWarnNotError(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	traced := newTracedHealthServer(t)
	traced.failWith = status.Error(codes.NotFound, "not found")

	var buf bytes.Buffer
	installTestLogger(t, &buf)
	conn, err := grpc.NewClient(traced.addr, outboundDial(peerModelService, grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_, _ = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})

	recs := decodeLogLines(t, &buf)
	var completion, depFailed map[string]any
	for _, r := range recs {
		switch r["event"] {
		case "rpc.client.completed":
			completion = r
		case "dependency.failed":
			depFailed = r
		}
	}
	if completion == nil || completion["level"] != "WARN" || completion["result"] != "rejected" {
		t.Fatalf("NotFound must be WARN/rejected: %v", completion)
	}
	// A business rejection is not a dependency failure.
	if depFailed != nil {
		t.Errorf("NotFound must not be reported as dependency.failed: %v", depFailed)
	}
}

// LG11: an unreachable peer must surface the caller-side error on the current
// trace, without inventing a server-side request or completion event.
func TestOutboundDialUnreachableKeepsCallerError(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	var buf bytes.Buffer
	installTestLogger(t, &buf)

	// A closed port: nothing is listening, so the dial target cannot serve.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	conn, err := grpc.NewClient(addr, outboundDial(peerObservabilityService, grpc.WithTransportCredentials(insecure.NewCredentials()))...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})

	recs := decodeLogLines(t, &buf)
	var completion, depFailed map[string]any
	for _, r := range recs {
		switch r["event"] {
		case "rpc.client.completed":
			completion = r
		case "dependency.failed":
			depFailed = r
		case "rpc.server.completed":
			t.Errorf("no server-side completion may be fabricated for an unreachable peer")
		}
	}
	if completion == nil {
		t.Fatalf("unreachable peer produced no completion record: %v", recs)
	}
	if completion["level"] != "ERROR" {
		t.Errorf("unreachable peer must be ERROR, got %v (code=%v)", completion["level"], completion["rpc.code"])
	}
	if completion["error"] == nil {
		t.Error("unreachable peer must retain the real transport error")
	}
	if depFailed == nil {
		t.Error("unreachable peer must also emit dependency.failed")
	}
}

// --- helpers ---

type tracedHealthServer struct {
	addr     string
	onCall   func(metadata.MD)
	failWith error
}

func newTracedHealthServer(t *testing.T) *tracedHealthServer {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer()
	ts := &tracedHealthServer{addr: lis.Addr().String()}
	h := health.NewServer()
	h.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s, &interceptHealth{inner: h, ts: ts})
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return ts
}

// interceptHealth captures inbound metadata and can force a status.
type interceptHealth struct {
	healthpb.UnimplementedHealthServer
	inner healthpb.HealthServer
	ts    *tracedHealthServer
}

func (h *interceptHealth) Check(ctx context.Context, req *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if h.ts.onCall != nil {
		h.ts.onCall(md)
	}
	if h.ts.failWith != nil {
		return nil, h.ts.failWith
	}
	return h.inner.Check(ctx, req)
}

// installTestLogger routes the package-global logger to a buffer for this test.
func installTestLogger(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	prev := bLogger.GetLogger()
	bLogger.SetLogger(bLogger.NewSlogLogger(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	t.Cleanup(func() { bLogger.SetLogger(prev) })
}

func headerValue(md metadata.MD, key string) string {
	if md == nil {
		return ""
	}
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

func decodeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad JSON: %v\n%s", err, line)
		}
		out = append(out, m)
	}
	return out
}

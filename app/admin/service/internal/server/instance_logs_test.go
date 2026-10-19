package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const diagnosticInstanceID = "10000000-0000-4000-8000-000000000001"
const diagnosticTenantID = "20000000-0000-4000-8000-000000000002"

type diagnosticAuthorization struct {
	deny  bool
	calls atomic.Int32
}

func (a *diagnosticAuthorization) Authorize(_ context.Context, tenant, actor uint32, path, method string) error {
	a.calls.Add(1)
	if a.deny || tenant != 7 || actor != 9 || method != "GET" || (path != data.InstanceLogsPath && path != data.InstanceRuntimePath && path != data.InstanceEventsPath) {
		return data.ErrActionAuthorizationDenied
	}
	return nil
}

type diagnosticTenants struct{}

func (diagnosticTenants) ResourceTenantID(_ context.Context, id uint32) (string, error) {
	if id != 7 {
		return "", errors.New("tenant mismatch")
	}
	return diagnosticTenantID, nil
}

// Only Resource is substituted here. Service scope checks, projection, public
// query decoding, the Kratos HTTP handler and the socket remain real code.
type diagnosticResource struct {
	service.InstanceOwnerClient
	open   func(context.Context) (grpc.ServerStreamingClient[res.InstanceLogFrame], error)
	calls  atomic.Int32
	tail   uint32
	follow bool
}

func (p *diagnosticResource) identity(t, a, id string) error {
	if t != diagnosticTenantID || a != "governance:user:9" || id != diagnosticInstanceID {
		return status.Error(codes.PermissionDenied, "INSTANCE_SCOPE_DENIED")
	}
	return nil
}
func (p *diagnosticResource) Runtime(_ context.Context, t, a, id string) (*res.InstanceRuntime, error) {
	if err := p.identity(t, a, id); err != nil {
		return nil, err
	}
	p.calls.Add(1)
	ready := int32(0)
	return &res.InstanceRuntime{InstanceId: id, Version: 2, ObservedAt: timestamppb.Now(), StatefulSet: &res.RuntimeStatefulSet{ReadyReplicas: &ready}}, nil
}
func (p *diagnosticResource) Events(_ context.Context, t, a, id string) (*res.InstanceEvents, error) {
	if err := p.identity(t, a, id); err != nil {
		return nil, err
	}
	p.calls.Add(1)
	return &res.InstanceEvents{ObservedAt: timestamppb.Now(), Events: []*res.InstanceEvent{{Type: "Normal", Reason: "Created"}}}, nil
}
func (p *diagnosticResource) Logs(ctx context.Context, t, a, id string, tail uint32, follow bool) (grpc.ServerStreamingClient[res.InstanceLogFrame], error) {
	if err := p.identity(t, a, id); err != nil {
		return nil, err
	}
	p.calls.Add(1)
	p.tail, p.follow = tail, follow
	return p.open(ctx)
}

type diagnosticStream struct {
	grpc.ClientStream
	ctx    context.Context
	frames <-chan instanceLogRead
	onRecv func()
}

func (s *diagnosticStream) Recv() (*res.InstanceLogFrame, error) {
	if s.onRecv != nil {
		s.onRecv()
	}
	select {
	case value, ok := <-s.frames:
		if !ok {
			return nil, io.EOF
		}
		return value.frame, value.err
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}
func diagnosticService(peer *diagnosticResource, authorization *diagnosticAuthorization) *service.InstanceService {
	return service.NewInstanceService(nil, service.NewInstanceGpuBinding(peer, authorization, "cluster"), diagnosticTenants{}, nil, nil)
}
func diagnosticHTTP(t *testing.T, peer *diagnosticResource, a *diagnosticAuthorization, limits InstanceLogLimits) *httptest.Server {
	t.Helper()
	server := khttp.NewServer(khttp.Timeout(0), khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, r any) (any, error) {
			return next(auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7}), r)
		}
	}))
	RegisterInstanceDiagnosticsHTTP(server, diagnosticService(peer, a), limits)
	actual := httptest.NewServer(server)
	t.Cleanup(actual.Close)
	return actual
}
func frames(values ...*res.InstanceLogFrame) <-chan instanceLogRead {
	ch := make(chan instanceLogRead, len(values))
	for _, value := range values {
		ch <- instanceLogRead{frame: value}
	}
	close(ch)
	return ch
}
func readyFrame() *res.InstanceLogFrame {
	return &res.InstanceLogFrame{Kind: res.InstanceLogFrame_READY}
}

func TestInstanceLogQueryStrict(t *testing.T) {
	for _, query := range []string{"tailLines=0", "tailLines=01", "tailLines=-1", "tailLines=1001", "tailLines=1&tailLines=2", "follow=TRUE", "follow=", "follow=true&follow=false", "tail_lines=2", "token=secret", "previous=true", "container=other", "bad=%zz"} {
		if _, err := decodeInstanceLogQuery(httptest.NewRequest("GET", "/logs?"+query, nil)); err == nil {
			t.Errorf("accepted %s", query)
		}
	}
	empty, err := decodeInstanceLogQuery(httptest.NewRequest("GET", "/logs", nil))
	if err != nil || empty.TailLines != nil || empty.Follow != nil {
		t.Fatalf("default presence: %v %v", empty, err)
	}
	parsed, err := decodeInstanceLogQuery(httptest.NewRequest("GET", "/logs?tailLines=1000&follow=false", nil))
	if err != nil || parsed.GetTailLines() != 1000 || parsed.GetFollow() {
		t.Fatalf("options: %v %v", parsed, err)
	}
}

func TestInstanceDiagnosticsSafeProjectionAndHTTP(t *testing.T) {
	peer := &diagnosticResource{}
	actual := diagnosticHTTP(t, peer, &diagnosticAuthorization{}, DefaultInstanceLogLimits())
	for _, tc := range []struct {
		path  string
		check func(map[string]any)
	}{
		{"runtime", func(v map[string]any) {
			if v["instance_id"] != diagnosticInstanceID || v["pod"] != nil {
				t.Fatalf("runtime: %#v", v)
			}
			sts := v["statefulSet"].(map[string]any)
			if sts["desiredReplicas"] != nil || sts["readyReplicas"] != float64(0) {
				t.Fatalf("zero/unknown lost: %#v", sts)
			}
		}},
		{"events", func(v map[string]any) {
			if v["podUid"] != nil {
				t.Fatalf("unknown Pod: %#v", v)
			}
			event := v["events"].([]any)[0].(map[string]any)
			if event["count"] != nil || event["lastOccurredAt"] != nil {
				t.Fatalf("invented observation: %#v", event)
			}
		}},
	} {
		response, err := http.Get(actual.URL + "/api/v1/instances/" + diagnosticInstanceID + "/" + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		err = json.NewDecoder(response.Body).Decode(&value)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("%s %d %v %#v", tc.path, response.StatusCode, err, value)
		}
		tc.check(value)
	}
	response, err := http.Get(actual.URL + "/api/v1/instances/" + diagnosticInstanceID + "/runtime?namespace=other")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatal(response.StatusCode)
	}
}

func TestInstanceLogsHTTPReadyUTF8AndTerminalEvents(t *testing.T) {
	peer := &diagnosticResource{open: func(ctx context.Context) (grpc.ServerStreamingClient[res.InstanceLogFrame], error) {
		return &diagnosticStream{ctx: ctx, frames: frames(readyFrame(), &res.InstanceLogFrame{Kind: res.InstanceLogFrame_DATA, Data: []byte{0xe4, 0xbd}}, &res.InstanceLogFrame{Kind: res.InstanceLogFrame_DATA, Data: []byte{0xa0, '\n', '"', 0xff}}, &res.InstanceLogFrame{Kind: res.InstanceLogFrame_END, Reason: "EOF"})}, nil
	}}
	actual := diagnosticHTTP(t, peer, &diagnosticAuthorization{}, DefaultInstanceLogLimits())
	response, err := http.Get(actual.URL + "/api/v1/instances/" + diagnosticInstanceID + "/logs?follow=false")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers: %d %#v", response.StatusCode, response.Header)
	}
	text := ""
	end := false
	for _, block := range strings.Split(string(raw), "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[1], "data: ") {
			continue
		}
		var value map[string]string
		if err = json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &value); err != nil {
			t.Fatal(err)
		}
		if lines[0] == "event: log" {
			text += value["text"]
		}
		if lines[0] == "event: end" {
			end = value["reason"] == "EOF"
		}
	}
	if text != "你\n\"�" || !end || strings.Contains(string(raw), "\nid:") {
		t.Fatalf("SSE: %s text=%q", raw, text)
	}
	if peer.tail != 100 || peer.follow {
		t.Fatalf("independent defaults: %d %v", peer.tail, peer.follow)
	}
}

func TestInstanceLogsHTTPBeforeAndAfterHeaders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  *res.InstanceLogFrame
		err    error
		denied bool
		code   int
		sse    bool
	}{
		{name: "denied", denied: true, code: 403},
		{name: "notfound", err: status.Error(codes.NotFound, "INSTANCE_NOT_FOUND"), code: 404},
		{name: "target unavailable", err: status.Error(codes.Unavailable, "INSTANCE_DIAGNOSTIC_TARGET_UNAVAILABLE"), code: 503},
		{name: "data before ready", first: &res.InstanceLogFrame{Kind: res.InstanceLogFrame_DATA, Data: []byte("private")}, code: 503},
		{name: "stream error sanitized", first: readyFrame(), code: 200, sse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := &diagnosticResource{open: func(ctx context.Context) (grpc.ServerStreamingClient[res.InstanceLogFrame], error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &diagnosticStream{ctx: ctx, frames: frames(tc.first, &res.InstanceLogFrame{Kind: res.InstanceLogFrame_ERROR, Reason: "secret address credential"})}, nil
			}}
			actual := diagnosticHTTP(t, peer, &diagnosticAuthorization{deny: tc.denied}, DefaultInstanceLogLimits())
			response, err := http.Get(actual.URL + "/api/v1/instances/" + diagnosticInstanceID + "/logs")
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != tc.code || strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") != tc.sse || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "secret address") {
				t.Fatalf("%d %s %s", response.StatusCode, response.Header.Get("Content-Type"), raw)
			}
			if tc.denied && peer.calls.Load() != 0 {
				t.Fatal("authorization bypassed")
			}
			if tc.sse && !strings.Contains(string(raw), "event: error\ndata: {\"reason\":\"INSTANCE_LOG_UPSTREAM_UNAVAILABLE\"}") {
				t.Fatalf("bad terminal error %s", raw)
			}
		})
	}
}

func TestInstanceLogsHTTPHeartbeatLifetimeAndDisconnect(t *testing.T) {
	limits := DefaultInstanceLogLimits()
	limits.Heartbeat = 10 * time.Millisecond
	limits.Lifetime = 100 * time.Millisecond
	captured := make(chan context.Context, 2)
	peer := &diagnosticResource{open: func(ctx context.Context) (grpc.ServerStreamingClient[res.InstanceLogFrame], error) {
		captured <- ctx
		return &diagnosticStream{ctx: ctx, frames: frames(readyFrame())}, nil
	}}
	// Keep the upstream idle after READY instead of signaling EOF.
	peer.open = func(ctx context.Context) (grpc.ServerStreamingClient[res.InstanceLogFrame], error) {
		ch := make(chan instanceLogRead, 1)
		ch <- instanceLogRead{frame: readyFrame()}
		captured <- ctx
		return &diagnosticStream{ctx: ctx, frames: ch}, nil
	}
	actual := diagnosticHTTP(t, peer, &diagnosticAuthorization{}, limits)
	response, err := http.Get(actual.URL + "/api/v1/instances/" + diagnosticInstanceID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), ": heartbeat\n\n") || !strings.Contains(string(raw), "event: end\ndata: {\"reason\":\"TIME_LIMIT\"}") {
		t.Fatalf("lifetime: %s", raw)
	}
	first := <-captured
	select {
	case <-first.Done():
	case <-time.After(time.Second):
		t.Fatal("lifetime leaked upstream")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", actual.URL+"/api/v1/instances/"+diagnosticInstanceID+"/logs", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	second := <-captured
	cancel()
	response.Body.Close()
	select {
	case <-second.Done():
	case <-time.After(time.Second):
		t.Fatal("disconnect leaked upstream")
	}
}

func TestInstanceLogsHTTPOpenDeadlineAndConnectionLimits(t *testing.T) {
	limits := DefaultInstanceLogLimits()
	limits.OpenTimeout = 15 * time.Millisecond
	limits.InstanceConnections = 1
	peer := &diagnosticResource{open: func(ctx context.Context) (grpc.ServerStreamingClient[res.InstanceLogFrame], error) {
		return &diagnosticStream{ctx: ctx, frames: make(chan instanceLogRead)}, nil
	}}
	actual := diagnosticHTTP(t, peer, &diagnosticAuthorization{}, limits)
	response, err := http.Get(actual.URL + "/api/v1/instances/" + diagnosticInstanceID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 503 || strings.Contains(response.Header.Get("Content-Type"), "event-stream") || !strings.Contains(string(raw), "INSTANCE_LOG_OPEN_TIMEOUT") {
		t.Fatalf("open timeout %d %s", response.StatusCode, raw)
	}
	h := &instanceLogHTTP{limits: limits, tenants: map[string]int{}, instances: map[string]int{}}
	release, err := h.acquire(diagnosticTenantID, diagnosticInstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.acquire(diagnosticTenantID, diagnosticInstanceID); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("instance cap: %v", err)
	}
	release()
	if h.active != 0 || len(h.tenants) != 0 || len(h.instances) != 0 {
		t.Fatal("connection accounting leaked")
	}
}

type stalledLogWriter struct {
	header   http.Header
	deadline time.Time
	mu       sync.Mutex
}

func (w *stalledLogWriter) Header() http.Header { return w.header }
func (*stalledLogWriter) WriteHeader(int)       {}
func (w *stalledLogWriter) SetWriteDeadline(at time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.deadline = at
	return nil
}
func (*stalledLogWriter) FlushError() error { return nil }
func (w *stalledLogWriter) Write(raw []byte) (int, error) {
	if !strings.HasPrefix(string(raw), "event: log") {
		return len(raw), nil
	}
	w.mu.Lock()
	deadline := w.deadline
	w.mu.Unlock()
	time.Sleep(time.Until(deadline))
	return 0, os.ErrDeadlineExceeded
}
func TestInstanceLogsSlowWriterCancelsBoundedReader(t *testing.T) {
	limits := DefaultInstanceLogLimits()
	limits.WriteTimeout = 15 * time.Millisecond
	upstream := make(chan context.Context, 1)
	var recvs atomic.Int32
	peer := &diagnosticResource{open: func(ctx context.Context) (grpc.ServerStreamingClient[res.InstanceLogFrame], error) {
		ch := make(chan instanceLogRead, 100)
		ch <- instanceLogRead{frame: readyFrame()}
		for i := 0; i < 99; i++ {
			ch <- instanceLogRead{frame: &res.InstanceLogFrame{Kind: res.InstanceLogFrame_DATA, Data: []byte(strings.Repeat("x", limits.MaxFrameBytes))}}
		}
		upstream <- ctx
		return &diagnosticStream{ctx: ctx, frames: ch, onRecv: func() { recvs.Add(1) }}, nil
	}}
	h := &instanceLogHTTP{service: diagnosticService(peer, &diagnosticAuthorization{}), limits: limits, tenants: map[string]int{}, instances: map[string]int{}}
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7})
	if err := h.stream(ctx, &stalledLogWriter{header: http.Header{}}, &view.InstanceLogsRequest{InstanceId: diagnosticInstanceID}); err != nil {
		t.Fatal(err)
	}
	streamContext := <-upstream
	select {
	case <-streamContext.Done():
	case <-time.After(time.Second):
		t.Fatal("slow client leaked upstream")
	}
	if recvs.Load() > 12 {
		t.Fatalf("unbounded upstream consumption: %d", recvs.Load())
	}
	if h.active != 0 {
		t.Fatal("slow client leaked connection slot")
	}
}

func TestInstanceLogsEscapedFrameByteLimit(t *testing.T) {
	limits := DefaultInstanceLogLimits()
	content := strings.Repeat("\x00", limits.MaxFrameBytes)
	peer := &diagnosticResource{open: func(ctx context.Context) (grpc.ServerStreamingClient[res.InstanceLogFrame], error) {
		return &diagnosticStream{ctx: ctx, frames: frames(readyFrame(), &res.InstanceLogFrame{Kind: res.InstanceLogFrame_DATA, Data: []byte(content)}, &res.InstanceLogFrame{Kind: res.InstanceLogFrame_END, Reason: "EOF"})}, nil
	}}
	actual := diagnosticHTTP(t, peer, &diagnosticAuthorization{}, limits)
	response, err := http.Get(actual.URL + "/api/v1/instances/" + diagnosticInstanceID + "/logs?follow=false")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("HTTP %d: %v", response.StatusCode, err)
	}
	var restored strings.Builder
	for _, block := range strings.Split(string(raw), "\n\n") {
		if block == "" {
			continue
		}
		if len(block)+2 > limits.MaxFrameBytes {
			t.Fatalf("escaped frame exceeds byte limit: %d", len(block)+2)
		}
		if strings.HasPrefix(block, "event: log\ndata: ") {
			var value struct {
				Text string `json:"text"`
			}
			if err = json.Unmarshal([]byte(strings.TrimPrefix(block, "event: log\ndata: ")), &value); err != nil {
				t.Fatal(err)
			}
			restored.WriteString(value.Text)
		}
	}
	if restored.String() != content {
		t.Fatal("bounded frames changed log content")
	}
}

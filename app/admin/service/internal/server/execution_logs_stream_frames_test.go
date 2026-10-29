package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	modeldevcontractv1 "github.com/zhangzhe-ctrl/ani-modeldev-service/api/ani/modeldev/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// executionLogPeer substitutes the owner stream only. The HTTP decoder, frame
// projection, SSE writer and gate are the production code.
type executionLogPeer struct {
	tenant string
	frames []*modeldevcontractv1.GetExecutionLogStreamResponse
	err    error
	seen   *modeldevcontractv1.GetExecutionLogStreamRequest
}

func (p *executionLogPeer) OpenExecutionLogStream(_ context.Context, in *modeldevcontractv1.GetExecutionLogStreamRequest) (*service.ExecutionLogSession, error) {
	p.seen = in
	if p.err != nil {
		return nil, p.err
	}
	return &service.ExecutionLogSession{TenantID: p.tenant, Stream: &executionLogFrames{frames: p.frames}}, nil
}

type executionLogFrames struct {
	grpc.ClientStream
	frames []*modeldevcontractv1.GetExecutionLogStreamResponse
}

func (f *executionLogFrames) Recv() (*modeldevcontractv1.GetExecutionLogStreamResponse, error) {
	if len(f.frames) == 0 {
		return nil, io.EOF
	}
	frame := f.frames[0]
	f.frames = f.frames[1:]
	return frame, nil
}

func executionLogServer(t *testing.T, peer *executionLogPeer) *httptest.Server {
	t.Helper()
	limits := DefaultInstanceLogLimits()
	h := &executionLogHTTP{opener: peer, limits: limits, tenants: map[string]int{}, targets: map[string]int{}}
	server := khttp.NewServer(khttp.Timeout(0))
	server.Route("/").GET(data.ModelDevExecutionLogStreamPath, h.handle)
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	return web
}

func readExecutionLogSSE(t *testing.T, web *httptest.Server, query string) (*http.Response, string) {
	t.Helper()
	response, err := web.Client().Get(web.URL + "/api/v1/modeldev/executions/" + executionLogID + "/logs/stream" + query)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, string(raw)
}

// The client sees READY first, then one offset-bearing event per DATA frame,
// then a terminal control event. No frame is silently dropped.
func TestExecutionLogStreamFramesReadyDataThenEnd(t *testing.T) {
	peer := &executionLogPeer{tenant: executionLogID, frames: []*modeldevcontractv1.GetExecutionLogStreamResponse{
		{Kind: modeldevcontractv1.FrameKind_FRAME_KIND_READY, Offset: 0},
		{Kind: modeldevcontractv1.FrameKind_FRAME_KIND_DATA, Data: []byte("step one\n"), Offset: 9},
		{Kind: modeldevcontractv1.FrameKind_FRAME_KIND_DATA, Data: []byte("step two\n"), Offset: 18},
		{Kind: modeldevcontractv1.FrameKind_FRAME_KIND_CLOSED, Reason: "LOG_ENDED", Offset: 18},
	}}
	web := executionLogServer(t, peer)
	response, raw := readExecutionLogSSE(t, web, "?pod_uid="+executionLogID+"&sinceBytes=0")
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") ||
		response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers: %d %#v", response.StatusCode, response.Header)
	}
	if strings.Contains(raw, "\nid:") {
		t.Fatalf("SSE ids must not be invented: %s", raw)
	}
	var events []string
	for _, block := range strings.Split(raw, "\n\n") {
		if strings.HasPrefix(block, "event: ") {
			events = append(events, block)
		}
	}
	if len(events) != 3 || !strings.Contains(events[0], `"text":"step one`) || !strings.Contains(events[0], `"offset":9`) ||
		!strings.Contains(events[1], `"offset":18`) || !strings.Contains(events[2], "event: end") || !strings.Contains(events[2], "LOG_ENDED") {
		t.Fatalf("frames: %s", raw)
	}
	if peer.seen.GetSinceBytes() != 0 || peer.seen.GetPodUid() != executionLogID {
		t.Fatalf("resume offset not forwarded: %+v", peer.seen)
	}
}

// The resume offset reaches the owner unchanged; a rejected offset becomes a
// terminal error event carrying the reviewed reason, not a silent close.
func TestExecutionLogStreamResumeOffsetAndRejection(t *testing.T) {
	peer := &executionLogPeer{tenant: executionLogID, frames: []*modeldevcontractv1.GetExecutionLogStreamResponse{
		{Kind: modeldevcontractv1.FrameKind_FRAME_KIND_READY, Offset: 120},
		{Kind: modeldevcontractv1.FrameKind_FRAME_KIND_DATA, Data: []byte("tail\n"), Offset: 125},
		{Kind: modeldevcontractv1.FrameKind_FRAME_KIND_CLOSED, Reason: "POD_GONE", Offset: 125},
	}}
	web := executionLogServer(t, peer)
	_, raw := readExecutionLogSSE(t, web, "?pod_uid="+executionLogID+"&sinceBytes=120")
	if peer.seen.GetSinceBytes() != 120 {
		t.Fatalf("resume offset: %+v", peer.seen)
	}
	if !strings.Contains(raw, `"offset":125`) || !strings.Contains(raw, "POD_GONE") {
		t.Fatalf("resumed frames: %s", raw)
	}

	stale := &executionLogPeer{tenant: executionLogID, err: status.Error(codes.FailedPrecondition, "OFFSET_INVALID")}
	response, body := readExecutionLogSSE(t, executionLogServer(t, stale), "?pod_uid="+executionLogID+"&sinceBytes=4096")
	if response.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale offset status=%d body=%s, want 412 before any SSE frame", response.StatusCode, body)
	}
}

// A stream whose owner rejects after READY closes with a control event instead
// of an abrupt disconnect.
func TestExecutionLogStreamLateRejectionClosesWithAControlEvent(t *testing.T) {
	peer := &executionLogPeer{tenant: executionLogID, frames: []*modeldevcontractv1.GetExecutionLogStreamResponse{
		{Kind: modeldevcontractv1.FrameKind_FRAME_KIND_READY},
		{Kind: modeldevcontractv1.FrameKind_FRAME_KIND_REJECTED, Reason: "STEP_LOGS_UNAVAILABLE"},
	}}
	_, raw := readExecutionLogSSE(t, executionLogServer(t, peer), "?pod_uid="+executionLogID)
	if !strings.Contains(raw, "event: error") || !strings.Contains(raw, "STEP_LOGS_UNAVAILABLE") {
		t.Fatalf("late rejection: %s", raw)
	}
}

// The bounded lifetime of one connection ends with TIME_LIMIT so the client
// knows to resume with since_bytes rather than treat the log as finished.
func TestExecutionLogStreamLifetimeEndsWithTimeLimit(t *testing.T) {
	limits := DefaultInstanceLogLimits()
	limits.Lifetime = 20 * time.Millisecond
	h := &executionLogHTTP{opener: stalledExecutionLogPeer{}, limits: limits, tenants: map[string]int{}, targets: map[string]int{}}
	server := khttp.NewServer(khttp.Timeout(0))
	server.Route("/").GET(data.ModelDevExecutionLogStreamPath, h.handle)
	web := httptest.NewServer(server)
	t.Cleanup(web.Close)
	_, raw := readExecutionLogSSE(t, web, "?pod_uid="+executionLogID)
	if !strings.Contains(raw, "TIME_LIMIT") {
		t.Fatalf("lifetime close: %s", raw)
	}
}

// A follower that never ends keeps the SSE connection open until the channel's
// own lifetime expires; it is the lifetime, not an upstream error, that closes.
type stalledExecutionLogPeer struct{}

func (stalledExecutionLogPeer) OpenExecutionLogStream(ctx context.Context, _ *modeldevcontractv1.GetExecutionLogStreamRequest) (*service.ExecutionLogSession, error) {
	return &service.ExecutionLogSession{TenantID: executionLogID, Stream: &stalledExecutionLogFrames{ctx: ctx}}, nil
}

type stalledExecutionLogFrames struct {
	grpc.ClientStream
	ctx  context.Context
	read bool
}

func (f *stalledExecutionLogFrames) Recv() (*modeldevcontractv1.GetExecutionLogStreamResponse, error) {
	if !f.read {
		f.read = true
		return &modeldevcontractv1.GetExecutionLogStreamResponse{Kind: modeldevcontractv1.FrameKind_FRAME_KIND_READY}, nil
	}
	<-f.ctx.Done()
	return nil, f.ctx.Err()
}

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2/middleware"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/middleware/auth"
	"go-wind-admin/pkg/middleware/requestid"
)

// componentLogReaderStub 是 OpenSearch 的边界替身：真实鉴权、严格解码、
// SSE 帧构造、8 秒生命周期与连接额度都走真实代码。
type componentLogReaderStub struct {
	mu        sync.Mutex
	replay    data.PlatformComponentLogBatch
	replayErr error
	increment data.PlatformComponentLogBatch
	incErr    error
	limits    data.PlatformComponentLogStreamLimits
	replays   int
}

func (s *componentLogReaderStub) Replay(_ context.Context, _ data.ReplayQuery) (data.PlatformComponentLogBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replays++
	return s.replay, s.replayErr
}

func (s *componentLogReaderStub) Increment(_ context.Context, _ data.IncrementQuery) (data.PlatformComponentLogBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.increment, s.incErr
}

func (s *componentLogReaderStub) Limits() data.PlatformComponentLogStreamLimits {
	if s.limits.ProcessConnections == 0 {
		return data.DefaultPlatformComponentLogStreamLimits()
	}
	return s.limits
}

func (s *componentLogReaderStub) Close() {}

func componentLogReaderWith(entries ...data.PlatformComponentLogEntry) *componentLogReaderStub {
	return &componentLogReaderStub{
		replay: data.PlatformComponentLogBatch{Entries: entries, RequestedSize: 100},
	}
}

// componentLogFixture 用一个真实 Kratos HTTP server 承载手写 SSE handler。
type componentLogFixture struct {
	server  *httptest.Server
	handler *platformComponentLogHTTP
}

// newComponentLogFixture 装配真实路由与中间件；principal 为 nil 表示未认证。
func newComponentLogFixture(t *testing.T, principal *auth.Principal, reader service.ComponentLogReader) *componentLogFixture {
	t.Helper()
	svc := service.NewPlatformComponentService(nil, nil, nil, reader)
	server := khttp.NewServer(khttp.Timeout(0), khttp.Middleware(
		requestid.Server(),
		func(next middleware.Handler) middleware.Handler {
			return func(ctx context.Context, req any) (any, error) {
				if principal != nil {
					ctx = auth.NewPrincipalContext(ctx, principal)
				}
				return next(ctx, req)
			}
		},
	))
	RegisterPlatformComponentLogHTTP(server, svc)
	actual := httptest.NewServer(server)
	t.Cleanup(actual.Close)
	return &componentLogFixture{server: actual, handler: &platformComponentLogHTTP{
		service:    svc,
		limits:     svc.LogStreamLimits(),
		users:      map[uint32]int{},
		components: map[string]int{},
	}}
}

// componentLogPlatformPrincipal 构造持有 component:log:read 的平台身份。
func componentLogPlatformPrincipal(permissions ...string) *auth.Principal {
	claims := &authv1.UserTokenPayload{
		UserId:      9,
		Roles:       []string{"platform:admin"},
		Permissions: permissions,
	}
	return &auth.Principal{Type: auth.SubjectUser, ID: 9, Claims: claims, Roles: claims.Roles}
}

func componentLogStreamURL(base, component, query string) string {
	target := base + "/api/v1/platform/components/" + component + "/logs/stream"
	if query != "" {
		target += "?" + query
	}
	return target
}

// componentLogErrorBody 是旧格式错误体：字符串 code + message + request_id。
type componentLogErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func decodeComponentLogError(t *testing.T, response *http.Response) componentLogErrorBody {
	t.Helper()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read error body: %v", err)
	}
	var body componentLogErrorBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("error body is not the legacy shape: %s", raw)
	}
	return body
}

// TestPlatformComponentLogHTTPWritesSSEFrames 验证首帧 connected、六字段 log 帧、
// 响应头与 8 秒契约常量。
func TestPlatformComponentLogHTTPWritesSSEFrames(t *testing.T) {
	at := time.Date(2026, 10, 28, 10, 0, 3, 500*int(time.Millisecond), time.UTC)
	entry := data.PlatformComponentLogEntry{
		Container: "governance", Level: "error", Message: "boom\nsecond line",
		Pod: "ani-governance-88dd5cb45-g4j4m", Stream: "stderr", Timestamp: at,
		Index: "ani-logs-2026.10.28", ID: "a1",
	}
	fixture := newComponentLogFixture(t, componentLogPlatformPrincipal(data.PlatformComponentLogPermission), componentLogReaderWith(entry))
	response, err := http.Get(componentLogStreamURL(fixture.server.URL, "ani-governance", "limit=1&interval_seconds=5"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("status %d: %s", response.StatusCode, raw)
	}
	if got := response.Header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("cache headers = %#v", response.Header)
	}
	if response.Header.Get(requestid.HeaderRequestID) == "" {
		t.Fatal("stream must echo X-Request-ID")
	}

	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read first line: %v", err)
	}
	if first != ": connected\n" {
		t.Fatalf("first frame = %q", first)
	}
	if blank, err := reader.ReadString('\n'); err != nil || blank != "\n" {
		t.Fatalf("connected frame must be a comment block: %q %v", blank, err)
	}
	eventLine, err := reader.ReadString('\n')
	if err != nil || eventLine != "event: log\n" {
		t.Fatalf("event line = %q %v", eventLine, err)
	}
	dataLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("data line: %v", err)
	}
	if !strings.HasPrefix(dataLine, "data: {") {
		t.Fatalf("data line = %q", dataLine)
	}
	var frame map[string]string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSuffix(dataLine, "\n"), "data: ")), &frame); err != nil {
		t.Fatalf("frame is not single-line JSON: %v", err)
	}
	if len(frame) != 6 {
		t.Fatalf("frame must carry exactly six fields: %#v", frame)
	}
	if frame["container"] != "governance" || frame["level"] != "error" || frame["stream"] != "stderr" ||
		frame["pod"] != "ani-governance-88dd5cb45-g4j4m" || frame["message"] != "boom\nsecond line" {
		t.Fatalf("frame = %#v", frame)
	}
	if frame["timestamp"] != "2026-10-28T10:00:03.500Z" {
		t.Fatalf("timestamp = %q, want millisecond RFC3339", frame["timestamp"])
	}
	// 8 秒是契约常量：前端重连节奏依赖它，不能被配置改写。
	if data.PlatformComponentLogLifetime != 8*time.Second {
		t.Fatalf("contract lifetime = %v", data.PlatformComponentLogLifetime)
	}
}

// TestPlatformComponentLogHTTPErrorsBeforeHeaders 验证发头前失败返回真实状态码
// 与旧格式错误体，且不含 connected 帧、不回退到全局数字 code 编码器。
func TestPlatformComponentLogHTTPErrorsBeforeHeaders(t *testing.T) {
	platform := componentLogPlatformPrincipal(data.PlatformComponentLogPermission)
	for _, tc := range []struct {
		name      string
		principal *auth.Principal
		component string
		reader    service.ComponentLogReader
		status    int
		code      string
	}{
		{"anonymous", nil, "ani-governance", componentLogReaderWith(), 401, "UNAUTHORIZED"},
		{"no_capability", componentLogPlatformPrincipal("component:status:read"), "ani-governance", componentLogReaderWith(), 403, service.PlatformComponentLogDeniedCode},
		{"unregistered", platform, "not-a-component", componentLogReaderWith(), 404, service.PlatformComponentLogNotFoundCode},
		{"not_configured", platform, "ani-governance", nil, 503, service.PlatformComponentLogUnavailableCode},
		{"backend_down", platform, "ani-governance", &componentLogReaderStub{replayErr: data.ErrPlatformComponentLogUnavailable}, 503, service.PlatformComponentLogUnavailableCode},
		{"backend_timeout", platform, "ani-governance", &componentLogReaderStub{replayErr: data.ErrPlatformComponentLogTimeout}, 504, service.PlatformComponentLogTimeoutCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newComponentLogFixture(t, tc.principal, tc.reader)
			response, err := http.Get(componentLogStreamURL(fixture.server.URL, tc.component, "limit=5"))
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.status {
				raw, _ := io.ReadAll(response.Body)
				t.Fatalf("status = %d want %d: %s", response.StatusCode, tc.status, raw)
			}
			if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatal("failed open must not be an SSE response")
			}
			body := decodeComponentLogError(t, response)
			if body.Code != tc.code || body.Message == "" {
				t.Fatalf("error body = %#v", body)
			}
			if body.RequestID == "" {
				t.Fatal("legacy error body must carry request_id")
			}
			if body.RequestID != response.Header.Get(requestid.HeaderRequestID) {
				t.Fatalf("request_id mismatch: %q vs %q", body.RequestID, response.Header.Get(requestid.HeaderRequestID))
			}
		})
	}
}

// TestPlatformComponentLogHTTPStrictQuery 验证严格参数解析：忽略名单放行，
// 其余未知参数、重复参数、越界、前导零、小数一律 400。
func TestPlatformComponentLogHTTPStrictQuery(t *testing.T) {
	platform := componentLogPlatformPrincipal(data.PlatformComponentLogPermission)
	fixture := newComponentLogFixture(t, platform, componentLogReaderWith())

	for _, query := range []string{
		"limit=0", "limit=-1", "limit=201", "limit=1.5", "limit=", "limit=01",
		"limit=5&limit=6", "interval_seconds=0", "interval_seconds=6",
		"interval_seconds=", "interval_seconds=2&interval_seconds=3",
		"index=ani-logs-2026.10.28", "namespace=ani-platform", "tenant_id=0",
		"pod=ani-governance-88dd5cb45-g4j4m", "container=governance", "limit=+5",
		"limit=%205", "interval_seconds=01",
	} {
		t.Run(query, func(t *testing.T) {
			response, err := http.Get(componentLogStreamURL(fixture.server.URL, "ani-governance", query))
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				raw, _ := io.ReadAll(response.Body)
				t.Fatalf("status = %d: %s", response.StatusCode, raw)
			}
			body := decodeComponentLogError(t, response)
			if body.Code != "INVALID_COMPONENT_LOG_QUERY" || body.RequestID == "" {
				t.Fatalf("error body = %#v", body)
			}
		})
	}

	// 忽略名单：不校验取值、允许重复，且与合法参数混用不影响取值。
	for _, query := range []string{"_=1730000000000", "_t=1730000000000&_t=1730000000001", "t=abc", "limit=3&_t=1&_=2&t="} {
		t.Run("ignored_"+query, func(t *testing.T) {
			response, err := http.Get(componentLogStreamURL(fixture.server.URL, "ani-governance", query))
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				raw, _ := io.ReadAll(response.Body)
				t.Fatalf("status = %d: %s", response.StatusCode, raw)
			}
			_, _ = io.CopyN(io.Discard, response.Body, 1)
		})
	}
}

// TestPlatformComponentLogHTTPRejectsLastEventID 验证一期不支持断点续传：
// 非空 Last-Event-ID 必须 400，不能悄悄宣称续读成功。
func TestPlatformComponentLogHTTPRejectsLastEventID(t *testing.T) {
	fixture := newComponentLogFixture(t, componentLogPlatformPrincipal(data.PlatformComponentLogPermission), componentLogReaderWith())
	request, err := http.NewRequest(http.MethodGet, componentLogStreamURL(fixture.server.URL, "ani-governance", "limit=5"), nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Last-Event-ID", "12345")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if body := decodeComponentLogError(t, response); body.Code != "INVALID_COMPONENT_LOG_QUERY" {
		t.Fatalf("error body = %#v", body)
	}
}

// TestPlatformComponentLogHTTPConnectionQuota 验证每用户连接上限 429 + Retry-After: 8，
// 关闭一条后额度归还（QUOTA-01/02 的进程内等价验证）。
func TestPlatformComponentLogHTTPConnectionQuota(t *testing.T) {
	reader := componentLogReaderWith()
	reader.limits = data.PlatformComponentLogStreamLimits{
		OpenTimeout: 3 * time.Second, WriteTimeout: 2 * time.Second, Heartbeat: 2 * time.Second,
		MaxFrameBytes: 256 << 10, ProcessConnections: 16, UserConnections: 2, ComponentConnections: 4,
	}
	fixture := newComponentLogFixture(t, componentLogPlatformPrincipal(data.PlatformComponentLogPermission), reader)

	var held []*http.Response
	closeHeld := func() {
		for _, response := range held {
			response.Body.Close()
		}
		held = nil
	}
	defer closeHeld()

	for index := 0; index < 2; index++ {
		response, err := http.Get(componentLogStreamURL(fixture.server.URL, "ani-governance", "interval_seconds=5"))
		if err != nil {
			t.Fatalf("open %d: %v", index, err)
		}
		if response.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(response.Body)
			t.Fatalf("open %d status = %d: %s", index, response.StatusCode, raw)
		}
		held = append(held, response)
	}

	response, err := http.Get(componentLogStreamURL(fixture.server.URL, "ani-governance", "interval_seconds=5"))
	if err != nil {
		t.Fatalf("third open: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTooManyRequests {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 429: %s", response.StatusCode, raw)
	}
	if response.Header.Get("Retry-After") != "8" {
		t.Fatalf("Retry-After = %q", response.Header.Get("Retry-After"))
	}
	if body := decodeComponentLogError(t, response); body.Code != "COMPONENT_LOG_CONNECTION_LIMIT" {
		t.Fatalf("error body = %#v", body)
	}

	// 关闭一条后额度必须归还；计数最终归零。
	held[0].Body.Close()
	held = held[1:]
	deadline := time.Now().Add(2 * time.Second)
	for {
		fixture.handler.mu.Lock()
		active := fixture.handler.active
		fixture.handler.mu.Unlock()
		if active <= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connection slot not released, active=%d", active)
		}
		time.Sleep(10 * time.Millisecond)
	}
	response2, err := http.Get(componentLogStreamURL(fixture.server.URL, "ani-governance", "interval_seconds=5"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if response2.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response2.Body)
		response2.Body.Close()
		t.Fatalf("reopen status = %d: %s", response2.StatusCode, raw)
	}
	response2.Body.Close()
}

// TestPlatformComponentLogHTTPReleasesOnClientCancel 验证客户端中断后服务端
// 及时释放额度与查询上下文（CANCEL-01 的进程内等价验证）。
func TestPlatformComponentLogHTTPReleasesOnClientCancel(t *testing.T) {
	fixture := newComponentLogFixture(t, componentLogPlatformPrincipal(data.PlatformComponentLogPermission), componentLogReaderWith())
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, componentLogStreamURL(fixture.server.URL, "ani-governance", "interval_seconds=5"), nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d: %s", response.StatusCode, raw)
	}
	reader := bufio.NewReader(response.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != ": connected\n" {
		t.Fatalf("first frame = %q %v", line, err)
	}
	cancel()
	response.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		fixture.handler.mu.Lock()
		active, opening := fixture.handler.active, fixture.handler.opening
		fixture.handler.mu.Unlock()
		if active == 0 && opening == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancelled client leaked slots: active=%d opening=%d", active, opening)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPlatformComponentLogHTTPIncrementSendsNewEntries 验证增量轮次在
// 8 秒窗口内推送新条目，且重复命中被丢弃。
func TestPlatformComponentLogHTTPIncrementSendsNewEntries(t *testing.T) {
	at := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	replayed := data.PlatformComponentLogEntry{
		Container: "governance", Level: "info", Message: "replayed", Pod: "ani-governance-88dd5cb45-g4j4m",
		Stream: "stdout", Timestamp: at, Index: "ani-logs-2026.10.28", ID: "a1",
	}
	fresh := data.PlatformComponentLogEntry{
		Container: "governance", Level: "info", Message: "fresh", Pod: "ani-governance-88dd5cb45-g4j4m",
		Stream: "stdout", Timestamp: at.Add(time.Second), Index: "ani-logs-2026.10.28", ID: "a2",
	}
	reader := &componentLogReaderStub{
		replay:    data.PlatformComponentLogBatch{Entries: []data.PlatformComponentLogEntry{replayed}, RequestedSize: 100},
		increment: data.PlatformComponentLogBatch{Entries: []data.PlatformComponentLogEntry{replayed, fresh}, RequestedSize: 500},
	}
	fixture := newComponentLogFixture(t, componentLogPlatformPrincipal(data.PlatformComponentLogPermission), reader)
	response, err := http.Get(componentLogStreamURL(fixture.server.URL, "ani-governance", "interval_seconds=1"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer response.Body.Close()

	messages := make([]string, 0, 2)
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var frame map[string]string
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatalf("bad frame %q: %v", line, err)
		}
		messages = append(messages, frame["message"])
		if len(messages) >= 2 {
			break
		}
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0] != "replayed" || messages[1] != "fresh" {
		t.Fatalf("replay must precede increment and duplicates must be dropped: %#v", messages)
	}
}

// TestPlatformComponentLogHTTPIncompleteWindowEmitsErrorEvent 验证取满上限时
// 发送 WINDOW_INCOMPLETE 错误事件后结束，不宣称成功。
func TestPlatformComponentLogHTTPIncompleteWindowEmitsErrorEvent(t *testing.T) {
	entries := []data.PlatformComponentLogEntry{
		{Container: "governance", Level: "info", Message: "a", Pod: "p", Stream: "stdout", Timestamp: time.Now().UTC(), Index: "i", ID: "1"},
		{Container: "governance", Level: "info", Message: "b", Pod: "p", Stream: "stdout", Timestamp: time.Now().UTC(), Index: "i", ID: "2"},
	}
	reader := &componentLogReaderStub{
		replay:    data.PlatformComponentLogBatch{RequestedSize: 100},
		increment: data.PlatformComponentLogBatch{Entries: entries, RequestedSize: 2},
	}
	fixture := newComponentLogFixture(t, componentLogPlatformPrincipal(data.PlatformComponentLogPermission), reader)
	response, err := http.Get(componentLogStreamURL(fixture.server.URL, "ani-governance", "interval_seconds=1"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "event: error\n") {
		t.Fatalf("stream must surface an error event: %s", text)
	}
	var payload componentLogErrorBody
	for _, block := range strings.Split(text, "\n\n") {
		if !strings.HasPrefix(block, "event: error\ndata: ") {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(block, "event: error\ndata: ")), &payload); err != nil {
			t.Fatalf("error payload: %v", err)
		}
	}
	if payload.Code != service.PlatformComponentLogIncompleteCode {
		t.Fatalf("error code = %q", payload.Code)
	}
}

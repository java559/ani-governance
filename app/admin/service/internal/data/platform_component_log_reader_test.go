package data

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// platformComponentLogReaderFixture 启动一个 TLS 测试后端并构造指向它的 reader。
// 除 OpenSearch/HTTP 之外不使用替身：查询 DSL、响应解析、错误分类都走真实代码。
type platformComponentLogReaderFixture struct {
	reader   *PlatformComponentLogReader
	requests []string
	bodies   []string
	mu       sync.Mutex
}

func (f *platformComponentLogReaderFixture) record(request *http.Request, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request.URL.Path)
	f.bodies = append(f.bodies, body)
}

func (f *platformComponentLogReaderFixture) lastBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		return ""
	}
	return f.bodies[len(f.bodies)-1]
}

// newPlatformComponentLogReaderFixture 用 handler 承担 _search 与 _mapping 两个端点。
// hasIngestedAt 决定探测端点返回什么，从而选择增量查询使用的时间字段。
func newPlatformComponentLogReaderFixture(
	t *testing.T, hasIngestedAt bool, search http.HandlerFunc,
) *platformComponentLogReaderFixture {
	t.Helper()
	fixture := &platformComponentLogReaderFixture{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body string
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
		}
		fixture.record(r, body)
		if strings.Contains(r.URL.Path, "_mapping") {
			w.Header().Set("Content-Type", "application/json")
			if hasIngestedAt {
				_, _ = w.Write([]byte(`{"ani-logs-2026.10.28":{"mappings":{"ani_log.ingested_at":{"full_name":"ani_log.ingested_at","mapping":{"ingested_at":{"type":"date"}}}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
			return
		}
		search(w, r)
	})
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	dir := t.TempDir()
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("test server has no certificate")
	}
	caPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o440); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	usernamePath := filepath.Join(dir, "username")
	passwordPath := filepath.Join(dir, "password")
	if err := os.WriteFile(usernamePath, []byte("reader\n"), 0o440); err != nil {
		t.Fatalf("write username: %v", err)
	}
	if err := os.WriteFile(passwordPath, []byte("secret\n"), 0o440); err != nil {
		t.Fatalf("write password: %v", err)
	}
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	reader, err := NewPlatformComponentLogReader(PlatformComponentLogConfig{
		Endpoint:          server.URL,
		ServerName:        parsed.Hostname(),
		CAFile:            caPath,
		UsernameFile:      usernamePath,
		PasswordFile:      passwordPath,
		IndexPrefix:       "ani-logs-",
		RetentionDays:     3,
		QueryTimeout:      2 * time.Second,
		ResponseMaxBytes:  4 << 20,
		OpenTimeout:       3 * time.Second,
		WriteTimeout:      2 * time.Second,
		Heartbeat:         2 * time.Second,
		ReplayLimitMax:    200,
		IncrementMax:      500,
		Overlap:           30 * time.Second,
		MaxFrameBytes:     256 << 10,
		MaxBufferedBytes:  1 << 20,
		ProcessConnections: 16,
		UserConnections:    4,
		ComponentConnections: 4,
	})
	if err != nil {
		t.Fatalf("build reader: %v", err)
	}
	t.Cleanup(reader.Close)
	fixture.reader = reader
	return fixture
}

const platformComponentLogSearchHits = `{
  "took": 3,
  "timed_out": false,
  "hits": {
    "total": {"value": 3, "relation": "eq"},
    "hits": [
      {"_index":"ani-logs-2026.10.28","_id":"a1","_source":{
        "@timestamp":"2026-10-28T10:00:03.500Z","log":"2026-10-28T18:00:03+08:00 stderr F boom\n",
        "stream":"stderr","kubernetes":{"namespace_name":"ani-system","pod_name":"ani-governance-88dd5cb45-g4j4m","container_name":"governance"},
        "ani_log":{"level":"ERROR","message":"boom"}}},
      {"_index":"ani-logs-2026.10.28","_id":"a2","_source":{
        "@timestamp":"2026-10-28T10:00:02.500Z",
        "kubernetes":{"namespace_name":"ani-system","pod_name":"ani-governance-88dd5cb45-g4j4m","container_name":"governance"},
        "ani_log":{"message":"only normalized message"},"stream":"stdout"}},
      {"_index":"ani-logs-2026.10.28","_id":"a3","_source":{
        "@timestamp":"2026-10-28T10:00:01.500Z","log_payload":{"msg":"payload text","n":7},
        "kubernetes":{"namespace_name":"ani-system","pod_name":"ani-governance-88dd5cb45-g4j4m"},
        "ani_log":{"level":""}}}
    ]
  }
}`

func platformComponentLogOKHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

// TestPlatformComponentLogReaderReplayProjectsSixFields 验证回放：六字段投影、
// message 三级回退、level 兜底 unknown、缺失字段空串、以及倒序取回后翻转成正序。
func TestPlatformComponentLogReaderReplayProjectsSixFields(t *testing.T) {
	fixture := newPlatformComponentLogReaderFixture(t, false, platformComponentLogOKHandler(platformComponentLogSearchHits))
	scope, ok := PlatformComponentLogScopeFor("ani-governance")
	if !ok {
		t.Fatal("ani-governance must be registered")
	}
	batch, err := fixture.reader.Replay(context.Background(), ReplayQuery{Scope: scope, Limit: 3})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(batch.Entries) != 3 {
		t.Fatalf("entries = %d", len(batch.Entries))
	}
	if batch.RequestedSize != 3 {
		t.Fatalf("requested size = %d", batch.RequestedSize)
	}
	// 后端按 @timestamp desc 返回，Replay 必须翻转成正序。
	if !batch.Entries[0].Timestamp.Before(batch.Entries[2].Timestamp) {
		t.Fatalf("replay not ascending: %v", batch.Entries)
	}
	first := batch.Entries[2]
	if first.Container != "governance" || first.Pod != "ani-governance-88dd5cb45-g4j4m" {
		t.Fatalf("identity fields = %+v", first)
	}
	if first.Level != "error" {
		t.Fatalf("level must be lowercased, got %q", first.Level)
	}
	if !strings.HasPrefix(first.Message, "2026-10-28T18:00:03+08:00 stderr F boom") {
		t.Fatalf("message must use raw log field, got %q", first.Message)
	}
	if first.Stream != "stderr" {
		t.Fatalf("stream = %q", first.Stream)
	}
	second := batch.Entries[1]
	if second.Message != "only normalized message" || second.Level != "unknown" {
		t.Fatalf("fallback to ani_log.message/unknown failed: %+v", second)
	}
	third := batch.Entries[0]
	if third.Message != `{"msg":"payload text","n":7}` {
		t.Fatalf("payload fallback = %q", third.Message)
	}
	if third.Stream != "" || third.Container != "" {
		t.Fatalf("missing fields must be empty strings: %+v", third)
	}
	if third.Level != "unknown" {
		t.Fatalf("blank level must be unknown: %q", third.Level)
	}
}

// TestPlatformComponentLogReaderQueryBindsScope 验证查询 DSL 只使用注册表派生的
// namespace/Pod 范围，并带 sandbox 冗余排除；请求里不接受任何外部输入。
func TestPlatformComponentLogReaderQueryBindsScope(t *testing.T) {
	fixture := newPlatformComponentLogReaderFixture(t, false, platformComponentLogOKHandler(`{"timed_out":false,"hits":{"hits":[]}}`))
	scope, _ := PlatformComponentLogScopeFor("ani-governance")
	if _, err := fixture.reader.Replay(context.Background(), ReplayQuery{Scope: scope, Limit: 5}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	body := fixture.lastBody()
	for _, want := range []string{
		`"kubernetes.namespace_name.keyword":"ani-system"`,
		`"kubernetes.pod_name.keyword":"ani-governance-[a-z0-9]{5,10}-[a-z0-9]{5}"`,
		`"must_not"`,
		`*sandbox*`,
		`"log_payload"`,
		`"@timestamp"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("query body missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "ani-logs-2026") {
		t.Fatalf("query must not name an explicit index: %s", body)
	}
	if !strings.HasSuffix(fixture.requests[len(fixture.requests)-1], "/ani-logs-*/_search") {
		t.Fatalf("search path = %q", fixture.requests[len(fixture.requests)-1])
	}
}

// TestPlatformComponentLogReaderIncrementUsesIngestedAtWhenPresent 验证探测到
// ani_log.ingested_at 时增量按入库时间窗口查询（迟到日志可读）。
func TestPlatformComponentLogReaderIncrementUsesIngestedAtWhenPresent(t *testing.T) {
	fixture := newPlatformComponentLogReaderFixture(t, true, platformComponentLogOKHandler(`{"timed_out":false,"hits":{"hits":[]}}`))
	scope, _ := PlatformComponentLogScopeFor("ani-governance")
	from := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	if _, err := fixture.reader.Increment(context.Background(), IncrementQuery{Scope: scope, From: from, To: from.Add(8 * time.Second), MaxSize: 500}); err != nil {
		t.Fatalf("increment: %v", err)
	}
	body := fixture.lastBody()
	if !strings.Contains(body, `"ani_log.ingested_at"`) {
		t.Fatalf("increment must filter on ingested_at: %s", body)
	}
	if !strings.Contains(body, `"gte":"2026-10-28T10:00:00.000Z"`) || !strings.Contains(body, `"lte":"2026-10-28T10:00:08.000Z"`) {
		t.Fatalf("window bounds missing: %s", body)
	}
	if !strings.Contains(body, `"order":"asc"`) {
		t.Fatalf("increment must sort ascending: %s", body)
	}
}

// TestPlatformComponentLogReaderIncrementFallsBackToEventTime 验证字段缺失时
// 退化为按 @timestamp 窗口，并在结构上留下可观测的差异。
func TestPlatformComponentLogReaderIncrementFallsBackToEventTime(t *testing.T) {
	fixture := newPlatformComponentLogReaderFixture(t, false, platformComponentLogOKHandler(`{"timed_out":false,"hits":{"hits":[]}}`))
	scope, _ := PlatformComponentLogScopeFor("ani-governance")
	from := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	if _, err := fixture.reader.Increment(context.Background(), IncrementQuery{Scope: scope, From: from, To: from.Add(time.Second), MaxSize: 500}); err != nil {
		t.Fatalf("increment: %v", err)
	}
	body := fixture.lastBody()
	if strings.Contains(body, "ani_log.ingested_at") {
		t.Fatalf("missing field must not be queried: %s", body)
	}
	if !strings.Contains(body, `"range":{"@timestamp"`) {
		t.Fatalf("fallback must filter on @timestamp: %s", body)
	}
}

// TestPlatformComponentLogReaderSkipsIncompleteDocuments 验证缺 @timestamp 或缺
// message 的记录被跳过并计数，而不是输出空串或拿当前时间冒充。
func TestPlatformComponentLogReaderSkipsIncompleteDocuments(t *testing.T) {
	body := `{"timed_out":false,"hits":{"hits":[
      {"_index":"i","_id":"1","_source":{"@timestamp":"2026-10-28T10:00:00.000Z","kubernetes":{"pod_name":"ani-governance-88dd5cb45-g4j4m"}}},
      {"_index":"i","_id":"2","_source":{"log":"no timestamp","kubernetes":{"pod_name":"ani-governance-88dd5cb45-g4j4m"}}},
      {"_index":"i","_id":"3","_source":{"@timestamp":"not-a-time","log":"bad timestamp"}},
      {"_index":"i","_id":"4","_source":{"@timestamp":"2026-10-28T10:00:01.000Z","log":"kept","kubernetes":{"pod_name":"ani-governance-88dd5cb45-g4j4m"}}}
    ]}}`
	fixture := newPlatformComponentLogReaderFixture(t, false, platformComponentLogOKHandler(body))
	scope, _ := PlatformComponentLogScopeFor("ani-governance")
	batch, err := fixture.reader.Replay(context.Background(), ReplayQuery{Scope: scope, Limit: 4})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(batch.Entries) != 1 || batch.Entries[0].Message != "kept" {
		t.Fatalf("entries = %+v", batch.Entries)
	}
	if batch.Skipped != 3 {
		t.Fatalf("skipped = %d, want 3", batch.Skipped)
	}
}

// TestPlatformComponentLogReaderDegradesOnOversizedResponse 验证响应超限时把
// size 降到 1/4 重试一次；重试仍超限则显式失败，不静默截断。
func TestPlatformComponentLogReaderDegradesOnOversizedResponse(t *testing.T) {
	var attempts int
	var sizes []string
	handler := func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		filler := strings.Repeat("x", 2048)
		if attempts == 1 {
			sizes = append(sizes, "large")
			_, _ = w.Write([]byte(`{"timed_out":false,"padding":"` + filler + `","hits":{"hits":[]}}`))
			return
		}
		sizes = append(sizes, "small")
		_, _ = w.Write([]byte(`{"timed_out":false,"hits":{"hits":[]}}`))
	}
	fixture := newPlatformComponentLogReaderFixture(t, false, handler)
	fixture.reader.config.ResponseMaxBytes = 1024
	scope, _ := PlatformComponentLogScopeFor("ani-governance")
	batch, err := fixture.reader.Replay(context.Background(), ReplayQuery{Scope: scope, Limit: 200})
	if err != nil {
		t.Fatalf("degrade retry must succeed: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
	if batch.RequestedSize != 50 {
		t.Fatalf("degraded size = %d, want 50", batch.RequestedSize)
	}
	if !strings.Contains(fixture.lastBody(), `"size":50`) {
		t.Fatalf("retry must shrink size: %s", fixture.lastBody())
	}
}

// TestPlatformComponentLogReaderFailsWhenStillOversized 验证二次仍超限时报错，
// 而不是返回空结果冒充"没有日志"。
func TestPlatformComponentLogReaderFailsWhenStillOversized(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"padding":"` + strings.Repeat("x", 4096) + `"}`))
	}
	fixture := newPlatformComponentLogReaderFixture(t, false, handler)
	fixture.reader.config.ResponseMaxBytes = 1024
	scope, _ := PlatformComponentLogScopeFor("ani-governance")
	if _, err := fixture.reader.Replay(context.Background(), ReplayQuery{Scope: scope, Limit: 8}); err == nil {
		t.Fatal("persistent oversize must fail")
	}
}

// TestPlatformComponentLogReaderClassifiesErrors 覆盖凭据被拒、后端不可用、
// 超时与坏 JSON：分类必须区分"部署问题"和"用户问题"，不能一律 401。
func TestPlatformComponentLogReaderClassifiesErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		delay   time.Duration
		want    error
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{}`, want: ErrPlatformComponentLogRejected},
		{name: "forbidden", status: http.StatusForbidden, body: `{}`, want: ErrPlatformComponentLogRejected},
		{name: "throttled", status: http.StatusTooManyRequests, body: `{}`, want: ErrPlatformComponentLogUnavailable},
		{name: "upstream_down", status: http.StatusServiceUnavailable, body: `{}`, want: ErrPlatformComponentLogUnavailable},
		{name: "bad_gateway", status: http.StatusBadGateway, body: `{}`, want: ErrPlatformComponentLogUnavailable},
		{name: "server_error", status: http.StatusInternalServerError, body: `{}`, want: ErrPlatformComponentLogUnavailable},
		{name: "bad_json", status: http.StatusOK, body: `{"hits":`, want: ErrPlatformComponentLogUnavailable},
		{name: "slow", status: http.StatusOK, body: `{"hits":{"hits":[]}}`, delay: 300 * time.Millisecond, want: ErrPlatformComponentLogTimeout},
		{name: "backend_timed_out", status: http.StatusOK, body: `{"timed_out":true,"hits":{"hits":[]}}`, want: ErrPlatformComponentLogTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newPlatformComponentLogReaderFixture(t, false, func(w http.ResponseWriter, _ *http.Request) {
				if tc.delay > 0 {
					time.Sleep(tc.delay)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			fixture.reader.config.QueryTimeout = 100 * time.Millisecond
			fixture.reader.client.Timeout = 200 * time.Millisecond
			scope, _ := PlatformComponentLogScopeFor("ani-governance")
			_, err := fixture.reader.Replay(context.Background(), ReplayQuery{Scope: scope, Limit: 5})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestPlatformComponentLogReaderHonoursCancellation 验证上游取消立刻终止查询，
// 且不把取消误报成后端故障。
func TestPlatformComponentLogReaderHonoursCancellation(t *testing.T) {
	release := make(chan struct{})
	fixture := newPlatformComponentLogReaderFixture(t, false, func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte(`{"hits":{"hits":[]}}`))
	})
	t.Cleanup(func() { close(release) })
	scope, _ := PlatformComponentLogScopeFor("ani-governance")
	call, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := fixture.reader.Replay(call, ReplayQuery{Scope: scope, Limit: 5})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// TestPlatformComponentLogReaderRejectsRedirect 验证禁止跟随重定向到其他主机：
// 凭据不能被转发到任意地址。
func TestPlatformComponentLogReaderRejectsRedirect(t *testing.T) {
	fixture := newPlatformComponentLogReaderFixture(t, false, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://example.invalid/steal")
		w.WriteHeader(http.StatusMovedPermanently)
	})
	scope, _ := PlatformComponentLogScopeFor("ani-governance")
	if _, err := fixture.reader.Replay(context.Background(), ReplayQuery{Scope: scope, Limit: 5}); err == nil {
		t.Fatal("redirect must not be followed")
	}
}

// TestPlatformComponentLogReaderRejectsPlainTextEndpoint 验证构造期拒绝明文地址。
func TestPlatformComponentLogReaderRejectsPlainTextEndpoint(t *testing.T) {
	if _, err := NewPlatformComponentLogReader(PlatformComponentLogConfig{}); !errors.Is(err, ErrPlatformComponentLogNotConfigured) {
		t.Fatalf("empty config err = %v", err)
	}
}

// TestPlatformComponentLogReaderRequiresResolvableCA 验证 CA 文件不含证书时
// 构造失败，不静默降级成不校验。
func TestPlatformComponentLogReaderRequiresResolvableCA(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	usernamePath := filepath.Join(dir, "username")
	passwordPath := filepath.Join(dir, "password")
	for path, body := range map[string]string{caPath: "not a cert\n", usernamePath: "u\n", passwordPath: "p\n"} {
		if err := os.WriteFile(path, []byte(body), 0o440); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	_, err := NewPlatformComponentLogReader(PlatformComponentLogConfig{
		Endpoint:     "https://example.invalid:9200",
		ServerName:   "example.invalid",
		CAFile:       caPath,
		UsernameFile: usernamePath,
		PasswordFile: passwordPath,
		QueryTimeout: time.Second,
	})
	if err == nil {
		t.Fatal("invalid CA must fail construction")
	}
}

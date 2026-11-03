package requestid

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// probeServer 构造一个挂载了 requestid 中间件的进程内 HTTP 服务。
//
// 关键：Kratos 的 HTTP 中间件链**不是由路由自动套用的**。生成代码
// （protoc-gen-go-http 产出的 *_http.pb.go，见 i_authentication_http.pb.go:80）
// 在处理器内部显式调用 ctx.Middleware(...) 包裹业务调用，中间件才会执行。
// 手写路由若不调用它，即使 NewServer(khttp.Middleware(...)) 传了中间件也不会生效。
// 本挂具沿用与本仓 pkg/middleware/logging 测试相同的做法。
//
// biz 拿到的是中间件链之后、业务侧的上下文；返回的 error 会经 Kratos 默认
// 错误编码器写出响应体，便于端到端断言错误 body。
func probeServer(t *testing.T, biz func(ctx context.Context) error) *khttp.Server {
	t.Helper()
	srv := khttp.NewServer(khttp.Middleware(Server()))
	srv.Route("/").GET("/probe", func(ctx khttp.Context) error {
		h := ctx.Middleware(func(c context.Context, req interface{}) (interface{}, error) {
			return nil, biz(c)
		})
		_, err := h(ctx, nil)
		return err
	})
	return srv
}

// fire 向进程内 server 直投 GET /probe，headers 原样设到请求头。
func fire(t *testing.T, srv *khttp.Server, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/probe", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestServerGeneratesWhenAbsent 验证：客户端不带任何请求 ID 头时，
// 中间件必须生成一个合法 UUID，写回响应头，并让业务侧能从上下文读到同一个值。
//
// 这正是本轮补上的缺口：审计中间件一直会读 X-Request-ID 并把缺失记为
// NO_REQUEST_ID 风险因子，却没有任何组件负责生成。
func TestServerGeneratesWhenAbsent(t *testing.T) {
	var fromCtx string
	srv := probeServer(t, func(ctx context.Context) error {
		fromCtx, _ = FromContext(ctx)
		return nil
	})

	rec := fire(t, srv, nil)
	require.Equal(t, 200, rec.Code)

	generated := rec.Header().Get(HeaderRequestID)
	require.NotEmpty(t, generated, "缺失时必须在响应头回写生成的请求 ID")
	_, err := uuid.Parse(generated)
	require.NoError(t, err, "生成值应为合法 UUID，实际 %q", generated)

	require.Equal(t, generated, fromCtx, "业务侧上下文中的请求 ID 应与响应头一致")
}

// TestServerBackfillsRequestHeader 验证生成值被回填到入站请求头。
//
// 既有审计中间件是直接读 request.Header（logging 包的 getRequestId），
// 而非读上下文。若不回填，中间件以为有 ID、审计仍记为缺失并打上风险因子。
func TestServerBackfillsRequestHeader(t *testing.T) {
	var headerSeenByBiz string
	var headerSeenByTransport string

	srv := khttp.NewServer(khttp.Middleware(Server()))
	srv.Route("/").GET("/probe", func(ctx khttp.Context) error {
		h := ctx.Middleware(func(c context.Context, req interface{}) (interface{}, error) {
			// 业务侧与"审计视角"读的是同一个 request header。
			headerSeenByBiz = ctx.Request().Header.Get(HeaderRequestID)
			headerSeenByTransport = ctx.Request().Header.Get("X-Request-ID")
			return nil, nil
		})
		_, err := h(ctx, nil)
		return err
	})

	rec := fire(t, srv, nil)
	generated := rec.Header().Get(HeaderRequestID)
	require.NotEmpty(t, generated)
	require.Equal(t, generated, headerSeenByBiz, "入站请求头必须被回填为同一个 ID")
	require.Equal(t, generated, headerSeenByTransport)
}

// TestServerPassesThroughValidInbound 验证合法入站值原样透传，不重新生成。
func TestServerPassesThroughValidInbound(t *testing.T) {
	const inbound = "req-from-client-0001"
	var fromCtx string
	srv := probeServer(t, func(ctx context.Context) error {
		fromCtx, _ = FromContext(ctx)
		return nil
	})

	rec := fire(t, srv, map[string]string{HeaderRequestID: inbound})
	require.Equal(t, inbound, rec.Header().Get(HeaderRequestID))
	require.Equal(t, inbound, fromCtx)
}

// TestServerRejectsUnsafeInbound 验证非法入站值被丢弃并重新生成。
//
// 该值会被回写响应头、写进日志与审计记录，原样透传等于开放经请求头
// 注入任意内容的通道（换行可伪造日志行、超长可撑爆记录、非 ASCII 可污染终端输出）。
func TestServerRejectsUnsafeInbound(t *testing.T) {
	cases := []struct {
		name string
		val  string
	}{
		{"含换行（日志行注入）", "abc\r\nX-Injected: evil"},
		{"含空格", "abc def"},
		{"含中文", "请求一号"},
		{"超长", strings.Repeat("a", maxRequestIDLen+1)},
		{"含分号", "abc;rm -rf"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := probeServer(t, func(ctx context.Context) error { return nil })
			rec := fire(t, srv, map[string]string{HeaderRequestID: c.val})

			got := rec.Header().Get(HeaderRequestID)
			require.NotEqual(t, c.val, got, "非法入站值不得原样透传")
			_, err := uuid.Parse(got)
			require.NoError(t, err, "非法值应被替换为重新生成的 UUID，实际 %q", got)
		})
	}
}

// TestServerFallsBackToAlternateHeaders 验证兜底顺序与 logging.getRequestId 一致：
// X-Request-ID 缺失时依次尝试 X-Correlation-ID、x-fc-request-id。
// 顺序不一致会导致"中间件认为有 ID、审计认为没有"的错位。
func TestServerFallsBackToAlternateHeaders(t *testing.T) {
	srv := probeServer(t, func(ctx context.Context) error { return nil })

	rec := fire(t, srv, map[string]string{HeaderCorrelationID: "corr-0001"})
	require.Equal(t, "corr-0001", rec.Header().Get(HeaderRequestID))

	rec = fire(t, srv, map[string]string{HeaderFcRequestID: "fc-0001"})
	require.Equal(t, "fc-0001", rec.Header().Get(HeaderRequestID))

	// X-Request-ID 优先级最高。
	rec = fire(t, srv, map[string]string{
		HeaderRequestID:     "primary-0001",
		HeaderCorrelationID: "corr-should-lose",
	})
	require.Equal(t, "primary-0001", rec.Header().Get(HeaderRequestID))
}

// TestServerAttachesRequestIDToErrorMetadata 端到端验证错误响应体：
// request_id 落在 metadata 里，且 HTTP 状态码/reason/message 形状不受影响。
//
// 这是对齐 ANI 契约 { code, message, request_id } 的落地方式——
// 不换 ErrorEncoder，因此前端已做的错误码切面无需调整。
func TestServerAttachesRequestIDToErrorMetadata(t *testing.T) {
	srv := probeServer(t, func(ctx context.Context) error {
		return errors.BadRequest("PROBE_REASON", "probe message")
	})

	rec := fire(t, srv, nil)
	require.Equal(t, 400, rec.Code, "中间件不得改变 HTTP 状态码")

	body := rec.Body.String()
	require.Contains(t, body, "PROBE_REASON", "reason 应保持原样")
	require.Contains(t, body, "probe message", "message 应保持原样")
	require.Contains(t, body, "request_id", "错误 metadata 应携带 request_id")

	// 错误体里的 request_id 必须与响应头一致，否则排障时对不上。
	generated := rec.Header().Get(HeaderRequestID)
	require.Contains(t, body, generated, "错误体中的 request_id 应与响应头一致")
}

func TestServerDoesNotMutateSharedErrors(t *testing.T) {
	shared := errors.Unauthorized("UNAUTHORIZED", "missing bearer token").WithMetadata(map[string]string{"other": "value"})
	srv := probeServer(t, func(context.Context) error { return shared })
	for _, id := range []string{"request-one", "request-two"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			rec := fire(t, srv, map[string]string{HeaderRequestID: id})
			require.Equal(t, 401, rec.Code)
			var body struct {
				Metadata map[string]string `json:"metadata"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, id, rec.Header().Get(HeaderRequestID))
			require.Equal(t, id, body.Metadata[MetadataKey])
			require.Equal(t, "value", body.Metadata["other"])
		})
	}
	t.Cleanup(func() { require.NotContains(t, shared.Metadata, MetadataKey) })
}

// TestContextHelpers 验证上下文存取辅助函数。
func TestContextHelpers(t *testing.T) {
	_, ok := FromContext(context.Background())
	require.False(t, ok, "未注入时应返回 false")

	ctx := NewContext(context.Background(), "rid-0001")
	got, ok := FromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "rid-0001", got)

	// 显式注入空串视同未注入。
	_, ok = FromContext(NewContext(context.Background(), ""))
	require.False(t, ok)

	// nil ctx 不得 panic。
	_, ok = FromContext(nil)
	require.False(t, ok)
}

// TestIsValidRequestID 钉死入站值校验规则。
func TestIsValidRequestID(t *testing.T) {
	valid := []string{"abc", "ABC-123", "a.b_c-d", strings.Repeat("a", maxRequestIDLen)}
	for _, v := range valid {
		require.True(t, isValidRequestID(v), "%q 应判为合法", v)
	}

	invalid := []string{
		"", " ", "a b", "a\tb", "a\nb", "a\rb", "中文",
		strings.Repeat("a", maxRequestIDLen+1),
		"a;b", "a/b", "a?b", "a#b", "a%b", "a=b", "a:b",
	}
	for _, v := range invalid {
		require.False(t, isValidRequestID(v), "%q 应判为非法", v)
	}
}

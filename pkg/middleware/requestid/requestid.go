// Package requestid 为每个请求建立可追踪的请求 ID。
//
// 背景：本仓的审计中间件（pkg/middleware/logging）一直在读取 X-Request-ID 并落库到
// 各类审计日志，缺失时还会记 NO_REQUEST_ID 风险因子（login_audit_log.go:371）。
// 但此前**没有任何组件负责生成它**：客户端不带该头时，全链路 request_id 恒为空，
// 审计记录无法串联，且每条登录都被打上"无请求 ID"风险标记。本中间件补齐这一环。
//
// 同时对齐 ANI Core 契约对错误响应的要求 { code, message, request_id, details? }：
// request_id 写入 Kratos 错误的 metadata（metadata.request_id），
// **不改变默认错误体形状** —— 前端已按 {code, reason, message, metadata} 做的
// 错误码切面不受影响，也无需自定义 ErrorEncoder。
package requestid

import (
	"context"
	"strings"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	ktransport "github.com/go-kratos/kratos/v2/transport"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/uuid"
)

const (
	// HeaderRequestID 对外响应头，也是首选的入站请求头。
	// 值与 pkg/middleware/logging/constants.go 的约定保持一致，两处不可漂移。
	HeaderRequestID = "X-Request-ID"

	// HeaderCorrelationID 与 HeaderFcRequestID 是既有审计逻辑的兜底读取顺序：
	// X-Request-ID → X-Correlation-ID → x-fc-request-id
	// （见 pkg/middleware/logging/utils.go 的 getRequestId）。此处沿用同一顺序，
	// 避免"中间件认为有 ID、审计认为没有"的错位。
	HeaderCorrelationID = "X-Correlation-ID"
	HeaderFcRequestID   = "x-fc-request-id"

	// MetadataKey 错误 metadata 中承载 request id 的键名。
	MetadataKey = "request_id"

	// maxRequestIDLen 入站值长度上限。request id 会被回写响应头并落进审计日志，
	// 不限长等于给攻击者一个撑大日志与响应头的入口。
	maxRequestIDLen = 128
)

type ctxKey struct{}

// NewContext 把 request id 注入上下文。
func NewContext(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, requestID)
}

// FromContext 取出当前请求的 request id；不存在时返回 ("", false)。
func FromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(ctxKey{}).(string)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// isValidRequestID 校验入站 request id 是否可用。
//
// 只接受 [A-Za-z0-9._-]，且长度不超过 maxRequestIDLen。该值会被回写响应头、
// 写进日志与审计记录，若原样透传则等于开了一个经请求头注入任意内容的洞
// （换行可伪造日志行、超长可撑爆记录）。非法值一律丢弃并重新生成。
func isValidRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

// resolveFromHeader 按既有的三级兜底顺序读取入站 request id，非法值返回空串。
func resolveFromHeader(header interface{ Get(string) string }) string {
	if header == nil {
		return ""
	}
	for _, key := range []string{HeaderRequestID, HeaderCorrelationID, HeaderFcRequestID} {
		if v := strings.TrimSpace(header.Get(key)); isValidRequestID(v) {
			return v
		}
	}
	return ""
}

// Server 返回请求 ID 中间件。
//
// 挂载位置建议：recovery 之后、logging 之前。这样日志与审计中间件都能从上下文
// 读到同一个 ID，异常路径也已被 recovery 收敛。
func Server() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			var httpTransport *khttp.Transport
			if tr, ok := ktransport.FromServerContext(ctx); ok {
				httpTransport, _ = tr.(*khttp.Transport)
			}

			requestID := ""
			if httpTransport != nil {
				if r := httpTransport.Request(); r != nil {
					requestID = resolveFromHeader(r.Header)
				}
			}
			if requestID == "" {
				requestID = uuid.NewString()
			}

			ctx = NewContext(ctx, requestID)

			if httpTransport != nil {
				// 回写响应头，便于调用方与前端在报错时上报该 ID。
				httpTransport.ReplyHeader().Set(HeaderRequestID, requestID)

				// 把生成结果回填到入站请求头：既有审计中间件是直接读
				// request.Header（见 logging 包的 getRequestId），而非读上下文。
				// 在此处回填可让审计拿到同一个 ID，且无需改动 logging 包。
				// 本中间件位于链上游、handler 之前，同 goroutine 内无并发问题。
				if r := httpTransport.Request(); r != nil {
					r.Header.Set(HeaderRequestID, requestID)
				}
			}

			reply, err := handler(ctx, req)
			if err != nil {
				err = attachToError(err, requestID)
			}
			return reply, err
		}
	}
}

// attachToError 把 request id 挂到 Kratos 错误的 metadata 上。
//
// 每次复制错误，避免修改认证中间件复用的包级错误和并发请求的 metadata。
func attachToError(err error, requestID string) error {
	if err == nil || requestID == "" {
		return err
	}
	se := errors.FromError(err)
	if se == nil {
		return err
	}
	copy := errors.Clone(se).WithCause(err)
	copy.Metadata[MetadataKey] = requestID
	return copy
}

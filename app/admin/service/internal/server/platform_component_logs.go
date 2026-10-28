package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/middleware/auth"
	"go-wind-admin/pkg/middleware/requestid"
)

// platformComponentLogHTTP 承载组件日志流的手写 SSE handler。
//
// 计数刻意留在进程内：一期是 8 秒短连接、单副本部署，没有跨实例配额的
// 需求；多副本时上限按副本数线性放大，属于已知边界。
type platformComponentLogHTTP struct {
	service *service.PlatformComponentService
	limits  data.PlatformComponentLogStreamLimits

	mu         sync.Mutex
	active     int
	opening    int
	users      map[uint32]int
	components map[string]int
}

// RegisterPlatformComponentLogHTTP 注册组件日志流（GET SSE）。
//
// 与组件状态接口并列，复用同一个 PlatformComponentService 形参：日志与状态
// 共用一份受控组件清单，不重复维护。
func RegisterPlatformComponentLogHTTP(server *khttp.Server, s *service.PlatformComponentService) {
	if s == nil {
		return
	}
	h := &platformComponentLogHTTP{
		service:    s,
		limits:     s.LogStreamLimits(),
		users:      map[uint32]int{},
		components: map[string]int{},
	}
	server.Route("/").GET(data.PlatformComponentLogStreamPath, h.handle)
}

// platformComponentLogErrorBody 是旧版错误体：{code, message, request_id}。
// 该入口的局部兼容不进全局编码器，其他接口的错误格式不变。
type platformComponentLogErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func (h *platformComponentLogHTTP) handle(ctx khttp.Context) error {
	khttp.SetOperation(ctx, data.PlatformComponentLogStreamOperation)
	// 认证/授权/审计与请求 ID 都在 ctx.Middleware 内，参数 400 也必须带同一个
	// request_id，因此严格解码放在中间件调用里执行。
	_, err := ctx.Middleware(func(call context.Context, _ any) (any, error) {
		return nil, h.stream(call, ctx)
	})(ctx, nil)
	if err != nil {
		h.writeErrorBody(ctx, err)
	}
	// 任何错误都由本 handler 自己写出，不能再交回全局编码器，否则会在旧格式
	// 错误体后追加一段数字 code 的 JSON。
	return nil
}

func (h *platformComponentLogHTTP) stream(call context.Context, ctx khttp.Context) error {
	request := ctx.Request()
	component := ctx.Vars().Get("component")
	limit, interval, err := decodePlatformComponentLogQuery(request)
	if err != nil {
		return err
	}
	if header := request.Header.Get("Last-Event-ID"); header != "" {
		return kratoserrors.New(http.StatusBadRequest, "INVALID_COMPONENT_LOG_QUERY", "this version does not support resuming from Last-Event-ID")
	}

	releaseOpening, err := h.reserveOpen()
	if err != nil {
		return err
	}
	defer releaseOpening()

	// 首次回放必须在写响应头之前完成：否则打不开时会先发出 200 再断开，
	// 前端无法区分"没有日志"和"接口故障"。
	openCall, cancelOpen := context.WithTimeout(call, h.limits.OpenTimeout)
	session, err := h.service.OpenPlatformComponentLogs(openCall, component, limit, interval)
	cancelOpen()
	if err != nil {
		return err
	}

	userID := platformComponentLogUserID(call)
	release, err := h.acquire(userID, component)
	if err != nil {
		return err
	}
	defer release()

	return h.writeStream(call, ctx, session)
}

// writeStream 在已完成首次回放之后写 SSE。该函数返回后连接结束，
// 不再有任何 HTTP 错误可写。
func (h *platformComponentLogHTTP) writeStream(call context.Context, ctx khttp.Context, session *service.PlatformComponentLogSession) error {
	response := ctx.Response()
	controller := http.NewResponseController(response)
	if err := controller.SetWriteDeadline(time.Now().Add(h.limits.WriteTimeout)); err != nil {
		return kratoserrors.New(http.StatusServiceUnavailable, service.PlatformComponentLogUnavailableCode, "stream transport unavailable")
	}
	defer controller.SetWriteDeadline(time.Time{})

	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Accel-Buffering", "no")
	if id, ok := requestid.FromContext(call); ok {
		response.Header().Set(requestid.HeaderRequestID, id)
	}
	response.WriteHeader(http.StatusOK)

	// 8 秒从 connected 起算，与旧契约一致；普通 HTTP 超时已被 F11 的豁免分支跳过。
	lifetime, cancelLifetime := context.WithTimeout(call, data.PlatformComponentLogLifetime)
	defer cancelLifetime()

	writer := platformComponentLogSSEWriter{
		w:          response,
		controller: controller,
		timeout:    h.limits.WriteTimeout,
		maxFrame:   h.limits.MaxFrameBytes,
	}
	if err := writer.comment("connected"); err != nil {
		return nil
	}
	for _, entry := range session.Replay {
		if err := writer.log(entry); err != nil {
			return nil
		}
	}

	interval := time.Duration(session.Query.IntervalSeconds) * time.Second
	poll := time.NewTicker(interval)
	defer poll.Stop()
	heartbeat := time.NewTicker(h.limits.Heartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-lifetime.Done():
			// 正常到期关闭：不发 error 事件，前端按旧逻辑重连。
			return nil
		case <-call.Done():
			return nil
		case <-heartbeat.C:
			if err := writer.comment("heartbeat"); err != nil {
				return nil
			}
		case <-poll.C:
			entries, _, complete, err := h.service.PollPlatformComponentLogs(call, session)
			if err != nil {
				_ = writer.event("error", platformComponentLogErrorPayload(call, err))
				return nil
			}
			for _, entry := range entries {
				if err := writer.log(entry); err != nil {
					return nil
				}
			}
			if !complete {
				_ = writer.event("error", platformComponentLogErrorPayload(call,
					kratoserrors.New(http.StatusServiceUnavailable, service.PlatformComponentLogIncompleteCode, "increment window not fully consumed")))
				return nil
			}
		}
	}
}

// platformComponentLogErrorPayload 生成流内错误事件的数据体。
func platformComponentLogErrorPayload(call context.Context, err error) platformComponentLogErrorBody {
	body := platformComponentLogErrorBody{Code: service.PlatformComponentLogUnavailableCode, Message: "component log unavailable"}
	var known *kratoserrors.Error
	if errors.As(err, &known) && known.Reason != "" {
		body.Code = known.Reason
		body.Message = known.Message
	}
	body.RequestID = platformComponentLogRequestID(call, err)
	return body
}

// writeErrorBody 在写出 SSE 之前失败时使用：返回真实 HTTP 状态与旧格式错误体。
func (h *platformComponentLogHTTP) writeErrorBody(ctx khttp.Context, err error) {
	status := http.StatusInternalServerError
	code := service.PlatformComponentLogNotFoundCode
	message := "component log request failed"
	var known *kratoserrors.Error
	if errors.As(err, &known) {
		if known.Code >= 400 && known.Code <= 599 {
			status = int(known.Code)
		}
		code = known.Reason
		if known.Message != "" {
			message = known.Message
		}
	}
	switch status {
	case http.StatusUnauthorized:
		// 旧契约的 401 code 恒为 UNAUTHORIZED，直接复用 Kratos reason 常量。
		code = "UNAUTHORIZED"
	case http.StatusForbidden:
		if code == "" {
			code = service.PlatformComponentLogDeniedCode
		}
	}
	if code == "" {
		code = service.PlatformComponentLogNotFoundCode
	}
	if status == http.StatusTooManyRequests {
		ctx.Response().Header().Set("Retry-After", "8")
	}
	body := platformComponentLogErrorBody{
		Code:      code,
		Message:   message,
		RequestID: platformComponentLogRequestID(ctx.Request().Context(), err),
	}
	raw, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		return
	}
	response := ctx.Response()
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	if id, ok := requestid.FromContext(ctx.Request().Context()); ok {
		response.Header().Set(requestid.HeaderRequestID, id)
	}
	response.WriteHeader(status)
	_, _ = response.Write(raw)
}

// platformComponentLogRequestID 先取错误 metadata 上的 request_id，
// 缺失时回落到 context；两者都没有则输出空串而不是省略字段。
func platformComponentLogRequestID(call context.Context, err error) string {
	var known *kratoserrors.Error
	if errors.As(err, &known) && known.Metadata != nil {
		if id := known.Metadata[requestid.MetadataKey]; id != "" {
			return id
		}
	}
	if call != nil {
		if id, ok := requestid.FromContext(call); ok {
			return id
		}
	}
	return ""
}

// decodePlatformComponentLogQuery 严格解析两个查询参数。
//
// 规则（实施方案 §3）：
//   - `_`、`_t`、`t` 整体忽略：不校验取值、允许重复（缓存击穿参数）；
//   - 其余未知参数 400；
//   - limit / interval_seconds 重复 400，越界、前导零、非整数 400。
func decodePlatformComponentLogQuery(request *http.Request) (int, int, error) {
	invalid := func(message string) error {
		return kratoserrors.New(http.StatusBadRequest, "INVALID_COMPONENT_LOG_QUERY", message)
	}
	if request.Method != http.MethodGet {
		return 0, 0, invalid("only GET is supported")
	}
	if request.Body != nil {
		if raw, err := io.ReadAll(io.LimitReader(request.Body, 1)); err != nil || len(raw) != 0 {
			return 0, 0, invalid("request body is not allowed")
		}
	}
	values, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return 0, 0, invalid("invalid query encoding")
	}
	limit := data.PlatformComponentLogDefaultLimit
	interval := data.PlatformComponentLogDefaultInterval
	for key, list := range values {
		switch key {
		case "_", "_t", "t":
			continue
		case "limit":
			if len(list) != 1 {
				return 0, 0, invalid("limit must appear at most once")
			}
			parsed, ok := platformComponentLogStrictInt(list[0], data.PlatformComponentLogMinLimit, data.PlatformComponentLogMaxLimit)
			if !ok {
				return 0, 0, invalid("limit must be an integer within 1..200")
			}
			limit = parsed
		case "interval_seconds":
			if len(list) != 1 {
				return 0, 0, invalid("interval_seconds must appear at most once")
			}
			parsed, ok := platformComponentLogStrictInt(list[0], data.PlatformComponentLogMinInterval, data.PlatformComponentLogMaxInterval)
			if !ok {
				return 0, 0, invalid("interval_seconds must be an integer within 1..5")
			}
			interval = parsed
		default:
			return 0, 0, invalid("unsupported query parameter: " + key)
		}
	}
	return limit, interval, nil
}

// platformComponentLogStrictInt 拒绝前导零、正负号、空白与小数。
func platformComponentLogStrictInt(raw string, minimum, maximum int) (int, bool) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	if strconv.Itoa(value) != raw {
		return 0, false
	}
	if value < minimum || value > maximum {
		return 0, false
	}
	return value, true
}

func platformComponentLogUserID(call context.Context) uint32 {
	if principal, err := auth.PrincipalFromContext(call); err == nil && principal != nil {
		return principal.ID
	}
	return 0
}

// reserveOpen 计入"正在打开"的连接，防止并发打开绕过上限。
func (h *platformComponentLogHTTP) reserveOpen() (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active+h.opening >= h.limits.ProcessConnections {
		return nil, platformComponentLogConnectionLimit()
	}
	h.opening++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.opening--
		})
	}, nil
}

// acquire 计入已建立的连接：进程、用户、组件三个维度。
func (h *platformComponentLogHTTP) acquire(userID uint32, component string) (func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active >= h.limits.ProcessConnections ||
		h.users[userID] >= h.limits.UserConnections ||
		h.components[component] >= h.limits.ComponentConnections {
		return nil, platformComponentLogConnectionLimit()
	}
	h.active++
	h.users[userID]++
	h.components[component]++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.active--
			h.users[userID]--
			h.components[component]--
			if h.users[userID] == 0 {
				delete(h.users, userID)
			}
			if h.components[component] == 0 {
				delete(h.components, component)
			}
		})
	}, nil
}

func platformComponentLogConnectionLimit() error {
	return kratoserrors.New(http.StatusTooManyRequests, "COMPONENT_LOG_CONNECTION_LIMIT", "component log connection limit reached")
}

// platformComponentLogSSEWriter 写 SSE 帧。每帧都重设写超时，
// 单个慢消费者只影响自己这条连接。
type platformComponentLogSSEWriter struct {
	w          http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
	maxFrame   int
}

func (w *platformComponentLogSSEWriter) write(raw []byte) error {
	if len(raw) > w.maxFrame {
		return errPlatformComponentLogFrameTooLarge
	}
	if err := w.controller.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil {
		return err
	}
	if _, err := w.w.Write(raw); err != nil {
		return err
	}
	return w.controller.Flush()
}

// errPlatformComponentLogFrameTooLarge 表示单条日志超过配置上限：
// 显式失败而不是截断（截断后的日志会误导排障）。
var errPlatformComponentLogFrameTooLarge = errors.New("component log frame exceeds limit")

func (w *platformComponentLogSSEWriter) comment(text string) error {
	return w.write([]byte(": " + text + "\n\n"))
}

func (w *platformComponentLogSSEWriter) event(name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	// data 必须是单行：JSON 编码已把换行转义成 \n，这里再拼行长会破坏帧边界。
	frame := append([]byte("event: "+name+"\ndata: "), raw...)
	frame = append(frame, '\n', '\n')
	return w.write(frame)
}

func (w *platformComponentLogSSEWriter) log(entry data.PlatformComponentLogEntry) error {
	return w.event("log", platformComponentLogFrame{
		Container: entry.Container,
		Level:     entry.Level,
		Message:   entry.Message,
		Pod:       entry.Pod,
		Stream:    entry.Stream,
		Timestamp: entry.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

// platformComponentLogFrame 是 SSE 的 data 体：严格六字段，与旧契约同名同序。
type platformComponentLogFrame struct {
	Container string `json:"container"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Pod       string `json:"pod"`
	Stream    string `json:"stream"`
	Timestamp string `json:"timestamp"`
}

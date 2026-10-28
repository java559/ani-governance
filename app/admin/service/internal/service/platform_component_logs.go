package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
)

// PlatformComponentLogQuery 是一次日志流请求的已校验参数。
type PlatformComponentLogQuery struct {
	Scope           data.PlatformComponentLogScope
	Limit           int
	IntervalSeconds int
}

// PlatformComponentLogSession 是一条日志连接的会话状态。
//
// 它只持有"本次连接"需要的只读快照：检索范围、回放批次与增量窗口起点。
// 没有跨连接游标、没有 Redis 状态（实施方案 §6.2）。
type PlatformComponentLogSession struct {
	Query   PlatformComponentLogQuery
	Replay  []data.PlatformComponentLogEntry
	Skipped int
	// NextFrom 是第一轮增量的窗口上界（= 打开时刻）。
	NextFrom time.Time
	// Window 是本连接的去重集合。
	Window *data.PlatformComponentLogWindow
}

// 日志接口的业务错误。HTTP 层按 code 映射状态码与旧格式错误体。
const (
	PlatformComponentLogNotFoundCode    = "COMPONENT_DIAGNOSTICS_FAILED"
	PlatformComponentLogDeniedCode      = "PLATFORM_COMPONENT_DENIED"
	PlatformComponentLogUnavailableCode = "COMPONENT_LOG_UNAVAILABLE"
	PlatformComponentLogTimeoutCode     = "COMPONENT_LOG_QUERY_TIMEOUT"
	PlatformComponentLogIncompleteCode  = "COMPONENT_LOG_WINDOW_INCOMPLETE"
	PlatformComponentLogLimitCode       = "COMPONENT_LOG_LIMIT_EXCEEDED"
)

// ComponentLogReader 是 service 层依赖的日志读取能力；nil 表示未配置。
type ComponentLogReader interface {
	Replay(ctx context.Context, query data.ReplayQuery) (data.PlatformComponentLogBatch, error)
	Increment(ctx context.Context, query data.IncrementQuery) (data.PlatformComponentLogBatch, error)
	// Limits 暴露流保护参数，供 HTTP 层在写响应前就知道超时与配额。
	Limits() data.PlatformComponentLogStreamLimits
	Close()
}

// LogStreamLimits 返回 HTTP 层的流保护参数。日志依赖未配置时使用默认上限，
// 以便接口回 503 之前仍能正确计数并释放连接额度。
func (s *PlatformComponentService) LogStreamLimits() data.PlatformComponentLogStreamLimits {
	if s == nil || s.logReader == nil {
		return data.DefaultPlatformComponentLogStreamLimits()
	}
	return s.logReader.Limits()
}

// OpenPlatformComponentLogs 校验平台身份与日志能力，锁定组件范围并完成首次回放。
//
// 首次回放不产生任何 SSE 字节：调用方只有拿到 session 才写响应头，
// 因此这里的任何失败都还能返回真实的 HTTP 错误码（实施方案 §7）。
func (s *PlatformComponentService) OpenPlatformComponentLogs(
	ctx context.Context, component string, limit, intervalSeconds int,
) (*PlatformComponentLogSession, error) {
	if _, err := s.actorWithPermission(ctx, data.PlatformComponentLogPermission); err != nil {
		return nil, err
	}
	scope, ok := data.PlatformComponentLogScopeFor(component)
	if !ok {
		return nil, kratoserrors.New(http.StatusNotFound, PlatformComponentLogNotFoundCode, "component is not registered")
	}
	if s.logReader == nil {
		return nil, kratoserrors.New(http.StatusServiceUnavailable, PlatformComponentLogUnavailableCode, "component log reader is not configured")
	}
	// roundStart 在回放前记录：第一轮增量用它作上界，保证"回放期间入库的记录"也被覆盖。
	roundStart := time.Now().UTC()
	replay, err := s.logReader.Replay(ctx, data.ReplayQuery{Scope: scope, Limit: limit})
	if err != nil {
		return nil, platformComponentLogServiceError(err)
	}
	window := data.NewPlatformComponentLogWindow(platformComponentLogWindowCap)
	// 回放命中必须预置进去重集合，否则第一轮增量会把回放的最后一批重发。
	for _, entry := range replay.Entries {
		window.Mark(entry.Index, entry.ID)
	}
	return &PlatformComponentLogSession{
		Query:    PlatformComponentLogQuery{Scope: scope, Limit: limit, IntervalSeconds: intervalSeconds},
		Replay:   replay.Entries,
		Skipped:  replay.Skipped,
		NextFrom: roundStart,
		Window:   window,
	}, nil
}

// platformComponentLogWindowCap 是单连接去重集合上限。8 秒短连接里实际远达不到，
// 它只用于防止异常大窗口把内存吃满。
const platformComponentLogWindowCap = 20000

// PollPlatformComponentLogs 执行一轮增量查询并返回**尚未发送过**的条目。
//
// 返回值 complete=false 表示本轮已取满上限（可能还有更多），调用方应发
// WINDOW_INCOMPLETE 并结束连接，不能宣称成功（实施方案 §6.2.3）。
func (s *PlatformComponentService) PollPlatformComponentLogs(
	ctx context.Context, session *PlatformComponentLogSession,
) (entries []data.PlatformComponentLogEntry, skipped int, complete bool, err error) {
	if session == nil || s.logReader == nil {
		return nil, 0, false, kratoserrors.New(http.StatusServiceUnavailable, PlatformComponentLogUnavailableCode, "component log reader is not configured")
	}
	now := time.Now().UTC()
	batch, err := s.logReader.Increment(ctx, data.IncrementQuery{
		Scope:   session.Query.Scope,
		From:    now.Add(-platformComponentLogOverlap),
		To:      now,
		MaxSize: platformComponentLogIncrementMax,
	})
	if err != nil {
		return nil, 0, false, platformComponentLogServiceError(err)
	}
	session.NextFrom = now
	out := make([]data.PlatformComponentLogEntry, 0, len(batch.Entries))
	for _, entry := range batch.Entries {
		if session.Window.Seen(entry.Index, entry.ID) {
			continue
		}
		session.Window.Mark(entry.Index, entry.ID)
		out = append(out, entry)
	}
	// 判定阈值是"本轮实际下发的 size"（降级后可能是 125），不是固定常量。
	complete = len(batch.Entries) < batch.RequestedSize
	if !complete {
		// 本窗口未读完时必须显式结束，不做"截断后冒充完整"。
		return out, batch.Skipped, false, nil
	}
	if session.Window.Full() {
		return out, batch.Skipped, false, nil
	}
	return out, batch.Skipped, true, nil
}

// platformComponentLogOverlap / platformComponentLogIncrementMax 与配置默认值保持一致；
// 这里的常量只用于 service 层不引入额外配置分支。
const (
	platformComponentLogOverlap      = 30 * time.Second
	platformComponentLogIncrementMax = 500
)

// platformComponentLogServiceError 把 data 层错误映射成对外错误码。
func platformComponentLogServiceError(err error) error {
	switch {
	case errors.Is(err, data.ErrPlatformComponentLogTimeout):
		return kratoserrors.New(http.StatusGatewayTimeout, PlatformComponentLogTimeoutCode, "component log query timed out")
	case errors.Is(err, data.ErrPlatformComponentLogUnavailable):
		return kratoserrors.New(http.StatusServiceUnavailable, PlatformComponentLogUnavailableCode, "component log backend unavailable")
	case errors.Is(err, data.ErrPlatformComponentLogRejected):
		// 后端拒绝凭据是部署问题，不能回 401（会让前端以为用户掉线）。
		return kratoserrors.New(http.StatusServiceUnavailable, PlatformComponentLogUnavailableCode, "component log backend rejected the reader credential")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return kratoserrors.New(http.StatusInternalServerError, PlatformComponentLogNotFoundCode, "component log query failed")
	}
}

// actorWithPermission 校验平台身份与指定能力码。
//
// 与 actor 的区别只有能力码：状态查询走 component:status:read，日志走
// component:log:read，两条能力不得互相替代。
func (s *PlatformComponentService) actorWithPermission(ctx context.Context, code string) (string, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return "", kratoserrors.Unauthorized("INVALID_LOGIN", "login required")
	}
	if p.Type != auth.SubjectUser || p.TenantID != 0 || p.Claims == nil ||
		p.Claims.GetTenantId() != 0 || p.ID != p.Claims.GetUserId() ||
		!hasPlatformRole(p.Roles) || !hasPermission(p.Claims, code) {
		return "", kratoserrors.Forbidden(PlatformComponentLogDeniedCode, "platform component permission required")
	}
	return p.Actor()
}

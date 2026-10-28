package service

import (
	"context"
	"errors"
	"testing"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
)

// componentLogReaderStub 只实现 service 层依赖的读取接口。
// 这是外部系统（OpenSearch）边界替身：身份校验、范围锁定、去重与投影仍走真实代码。
type componentLogReaderStub struct {
	replayBatch    data.PlatformComponentLogBatch
	replayErr      error
	incrementBatch data.PlatformComponentLogBatch
	incrementErr   error
	replayQueries  []data.ReplayQuery
	incrementCalls []data.IncrementQuery
	closed         bool
}

func (s *componentLogReaderStub) Replay(_ context.Context, query data.ReplayQuery) (data.PlatformComponentLogBatch, error) {
	s.replayQueries = append(s.replayQueries, query)
	return s.replayBatch, s.replayErr
}

func (s *componentLogReaderStub) Increment(_ context.Context, query data.IncrementQuery) (data.PlatformComponentLogBatch, error) {
	s.incrementCalls = append(s.incrementCalls, query)
	return s.incrementBatch, s.incrementErr
}

func (s *componentLogReaderStub) Limits() data.PlatformComponentLogStreamLimits {
	return data.PlatformComponentLogStreamLimits{
		OpenTimeout: 3 * time.Second, WriteTimeout: 2 * time.Second, Heartbeat: 2 * time.Second,
		MaxFrameBytes: 256 << 10, ProcessConnections: 16, UserConnections: 4, ComponentConnections: 4,
	}
}

func (s *componentLogReaderStub) Close() { s.closed = true }

// platformComponentLogPrincipal 构造一个合法平台身份，权限码由调用方指定。
func platformComponentLogPrincipal(id uint32, permissions ...string) *auth.Principal {
	claims := &authv1.UserTokenPayload{
		UserId:      id,
		Roles:       []string{"platform:admin"},
		Permissions: permissions,
	}
	return &auth.Principal{Type: auth.SubjectUser, ID: id, TenantID: 0, Claims: claims, Roles: claims.Roles}
}

func platformComponentLogEntry(pod, message, level string, at time.Time) data.PlatformComponentLogEntry {
	return data.PlatformComponentLogEntry{
		Container: "governance", Level: level, Message: message, Pod: pod, Stream: "stdout",
		Timestamp: at, Index: "ani-logs-2026.10.28", ID: message,
	}
}

// TestPlatformComponentLogServiceIdentityGate 覆盖平台身份闸门：
// 只有"用户态主体 + tenant 0 + claims 一致 + platform 角色 + component:log:read"才放行。
func TestPlatformComponentLogServiceIdentityGate(t *testing.T) {
	reader := &componentLogReaderStub{}
	s := NewPlatformComponentService(nil, nil, nil, reader)

	// 合法身份必须能进入业务：回放成功即视为通过闸门。
	valid := platformComponentLogPrincipal(9, data.PlatformComponentLogPermission)
	if _, err := s.OpenPlatformComponentLogs(auth.NewPrincipalContext(context.Background(), valid), "ani-governance", 5, 2); err != nil {
		t.Fatalf("valid platform identity rejected: %v", err)
	}

	for _, tc := range []struct {
		name      string
		principal *auth.Principal
	}{
		{"api_key", &auth.Principal{Type: auth.SubjectAPIKey, ID: 9, Claims: valid.Claims, Roles: valid.Roles}},
		{"tenant", &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 1, Claims: valid.Claims, Roles: valid.Roles}},
		{"claims_tenant", func() *auth.Principal {
			tenant := uint32(1)
			claims := &authv1.UserTokenPayload{UserId: 9, TenantId: &tenant, Roles: valid.Roles, Permissions: valid.Claims.Permissions}
			return &auth.Principal{Type: auth.SubjectUser, ID: 9, Claims: claims, Roles: claims.Roles}
		}()},
		{"id_mismatch", &auth.Principal{Type: auth.SubjectUser, ID: 10, Claims: valid.Claims, Roles: valid.Roles}},
		{"missing_platform_role", func() *auth.Principal {
			claims := &authv1.UserTokenPayload{UserId: 9, Roles: []string{"tenant:owner"}, Permissions: valid.Claims.Permissions}
			return &auth.Principal{Type: auth.SubjectUser, ID: 9, Claims: claims, Roles: claims.Roles}
		}()},
		{"status_permission_only", platformComponentLogPrincipal(9, "component:status:read")},
		{"no_permission", platformComponentLogPrincipal(9)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.OpenPlatformComponentLogs(auth.NewPrincipalContext(context.Background(), tc.principal), "ani-governance", 5, 2)
			known := kratoserrors.FromError(err)
			if known.Code != 403 || known.Reason != PlatformComponentLogDeniedCode {
				t.Fatalf("untrusted identity admitted: %v", err)
			}
		})
	}

	// 完全未认证：401，不能 403（前端据此决定是否重新登录）。
	if _, err := s.OpenPlatformComponentLogs(context.Background(), "ani-governance", 5, 2); kratoserrors.FromError(err).Code != 401 {
		t.Fatalf("anonymous must get 401, got %v", err)
	}
}

// TestPlatformComponentLogServiceUnregisteredComponent 验证未注册组件 404，
// 且不会触发任何 OpenSearch 查询（鉴权通过后仍要拦住范围扩大）。
func TestPlatformComponentLogServiceUnregisteredComponent(t *testing.T) {
	reader := &componentLogReaderStub{}
	s := NewPlatformComponentService(nil, nil, nil, reader)
	principal := platformComponentLogPrincipal(9, data.PlatformComponentLogPermission)
	_, err := s.OpenPlatformComponentLogs(auth.NewPrincipalContext(context.Background(), principal), "not-a-component", 5, 2)
	known := kratoserrors.FromError(err)
	if known.Code != 404 || known.Reason != PlatformComponentLogNotFoundCode {
		t.Fatalf("err = %v", err)
	}
	if len(reader.replayQueries) != 0 {
		t.Fatal("unregistered component must not reach the log backend")
	}
}

// TestPlatformComponentLogServiceNotConfigured 验证 reader 未配置时 503，
// 组件状态接口不受影响（状态走另一个方法，此处只断言日志侧）。
func TestPlatformComponentLogServiceNotConfigured(t *testing.T) {
	s := NewPlatformComponentService(nil, nil, nil, nil)
	principal := platformComponentLogPrincipal(9, data.PlatformComponentLogPermission)
	_, err := s.OpenPlatformComponentLogs(auth.NewPrincipalContext(context.Background(), principal), "ani-governance", 5, 2)
	known := kratoserrors.FromError(err)
	if known.Code != 503 || known.Reason != PlatformComponentLogUnavailableCode {
		t.Fatalf("err = %v", err)
	}
	if limits := s.LogStreamLimits(); limits.ProcessConnections != 16 || limits.UserConnections != 4 {
		t.Fatalf("unconfigured limits must fall back to defaults: %+v", limits)
	}
}

// TestPlatformComponentLogServiceReplaySeedsWindow 验证回放命中被预置进去重集合：
// 第一轮增量不能把回放的最后一批原样重发（方案 §6.2 最容易漏的一条）。
func TestPlatformComponentLogServiceReplaySeedsWindow(t *testing.T) {
	at := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	replayed := platformComponentLogEntry("ani-governance-88dd5cb45-g4j4m", "already sent", "info", at)
	reader := &componentLogReaderStub{
		replayBatch: data.PlatformComponentLogBatch{Entries: []data.PlatformComponentLogEntry{replayed}, RequestedSize: 5},
		incrementBatch: data.PlatformComponentLogBatch{
			Entries:       []data.PlatformComponentLogEntry{replayed, platformComponentLogEntry("ani-governance-88dd5cb45-g4j4m", "fresh", "info", at.Add(time.Second))},
			RequestedSize: 500,
		},
	}
	s := NewPlatformComponentService(nil, nil, nil, reader)
	principal := platformComponentLogPrincipal(9, data.PlatformComponentLogPermission)
	session, err := s.OpenPlatformComponentLogs(auth.NewPrincipalContext(context.Background(), principal), "ani-governance", 5, 2)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(session.Replay) != 1 {
		t.Fatalf("replay = %d", len(session.Replay))
	}
	entries, _, complete, err := s.PollPlatformComponentLogs(context.Background(), session)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if !complete {
		t.Fatal("partial increment must still be complete when below size")
	}
	if len(entries) != 1 || entries[0].Message != "fresh" {
		t.Fatalf("increment must drop already replayed documents: %+v", entries)
	}
}

// TestPlatformComponentLogServiceIncompleteWindow 验证取满上限时 complete=false，
// 调用方必须发 WINDOW_INCOMPLETE 而不是宣称成功；阈值取实际下发的 size。
func TestPlatformComponentLogServiceIncompleteWindow(t *testing.T) {
	at := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	entries := []data.PlatformComponentLogEntry{
		platformComponentLogEntry("ani-governance-88dd5cb45-g4j4m", "a", "info", at),
		platformComponentLogEntry("ani-governance-88dd5cb45-g4j4m", "b", "info", at),
	}
	reader := &componentLogReaderStub{
		replayBatch: data.PlatformComponentLogBatch{RequestedSize: 5},
		// 降级后实际 size 就是 2，命中 2 条同样算"未读完"。
		incrementBatch: data.PlatformComponentLogBatch{Entries: entries, RequestedSize: 2},
	}
	s := NewPlatformComponentService(nil, nil, nil, reader)
	principal := platformComponentLogPrincipal(9, data.PlatformComponentLogPermission)
	session, err := s.OpenPlatformComponentLogs(auth.NewPrincipalContext(context.Background(), principal), "ani-governance", 5, 2)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, _, complete, err := s.PollPlatformComponentLogs(context.Background(), session)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if complete {
		t.Fatal("full window must report incomplete")
	}
	if len(got) != 2 {
		t.Fatalf("entries = %d", len(got))
	}
}

// TestPlatformComponentLogServiceIncrementWindowBounds 验证每轮增量用
// [now-overlap, now] 的固定 overlap 窗口，且不推进任何持久水位。
func TestPlatformComponentLogServiceIncrementWindowBounds(t *testing.T) {
	reader := &componentLogReaderStub{replayBatch: data.PlatformComponentLogBatch{RequestedSize: 5}}
	s := NewPlatformComponentService(nil, nil, nil, reader)
	principal := platformComponentLogPrincipal(9, data.PlatformComponentLogPermission)
	session, err := s.OpenPlatformComponentLogs(auth.NewPrincipalContext(context.Background(), principal), "ani-governance", 5, 3)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if session.Query.IntervalSeconds != 3 {
		t.Fatalf("interval = %d", session.Query.IntervalSeconds)
	}
	if _, _, _, err := s.PollPlatformComponentLogs(context.Background(), session); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(reader.incrementCalls) != 1 {
		t.Fatalf("increment calls = %d", len(reader.incrementCalls))
	}
	call := reader.incrementCalls[0]
	if gap := call.To.Sub(call.From); gap != 30*time.Second {
		t.Fatalf("window = %v, want 30s overlap", gap)
	}
	if call.MaxSize != 500 {
		t.Fatalf("max size = %d", call.MaxSize)
	}
	if call.Scope.Component != "ani-governance" || call.Scope.Namespace != "ani-system" {
		t.Fatalf("scope = %+v", call.Scope)
	}
}

// TestPlatformComponentLogServiceErrorMapping 验证 data 层错误映射到对外码：
// 后端拒绝凭据必须是 503 而不是 401（否则前端会误判用户掉线）。
func TestPlatformComponentLogServiceErrorMapping(t *testing.T) {
	principal := platformComponentLogPrincipal(9, data.PlatformComponentLogPermission)
	for _, tc := range []struct {
		name   string
		err    error
		status int32
		reason string
	}{
		{"timeout", data.ErrPlatformComponentLogTimeout, 504, PlatformComponentLogTimeoutCode},
		{"unavailable", data.ErrPlatformComponentLogUnavailable, 503, PlatformComponentLogUnavailableCode},
		{"rejected", data.ErrPlatformComponentLogRejected, 503, PlatformComponentLogUnavailableCode},
		{"unknown", errors.New("boom"), 500, PlatformComponentLogNotFoundCode},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewPlatformComponentService(nil, nil, nil, &componentLogReaderStub{replayErr: tc.err})
			_, err := s.OpenPlatformComponentLogs(auth.NewPrincipalContext(context.Background(), principal), "ani-governance", 5, 2)
			known := kratoserrors.FromError(err)
			if known.Code != tc.status || known.Reason != tc.reason {
				t.Fatalf("err = %v (%d/%s)", err, known.Code, known.Reason)
			}
		})
	}
}

// TestPlatformComponentLogServiceCapabilitiesAreSeparate 验证两条能力互不替代：
// 只有日志权限不能读状态，只有状态权限不能读日志。
func TestPlatformComponentLogServiceCapabilitiesAreSeparate(t *testing.T) {
	logOnly := platformComponentLogPrincipal(9, data.PlatformComponentLogPermission)
	s := NewPlatformComponentService(nil, nil, nil, &componentLogReaderStub{})
	if _, err := s.ListPlatformComponents(auth.NewPrincipalContext(context.Background(), logOnly)); kratoserrors.FromError(err).Code != 403 {
		t.Fatalf("log capability must not admit status: %v", err)
	}
	statusOnly := platformComponentLogPrincipal(9, "component:status:read")
	if _, err := s.OpenPlatformComponentLogs(auth.NewPrincipalContext(context.Background(), statusOnly), "ani-governance", 5, 2); kratoserrors.FromError(err).Code != 403 {
		t.Fatalf("status capability must not admit logs: %v", err)
	}
}

// TestPlatformComponentLogServiceReaderLimits 验证 HTTP 层取到的保护参数
// 来自已加载配置，而不是写死常量。
func TestPlatformComponentLogServiceReaderLimits(t *testing.T) {
	s := NewPlatformComponentService(nil, nil, nil, &componentLogReaderStub{})
	limits := s.LogStreamLimits()
	if limits.MaxFrameBytes != 256<<10 || limits.ComponentConnections != 4 {
		t.Fatalf("limits = %+v", limits)
	}
}

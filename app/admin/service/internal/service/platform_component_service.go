package service

import (
	"context"
	"errors"
	"net/http"
	"sync"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"go-wind-admin/app/admin/service/internal/data"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"go-wind-admin/pkg/middleware/auth"
)

// PlatformComponentService 提供平台组件健康快照（BOSS「组件状态」页数据源）。
// 只读聚合：Kubernetes 工作负载状态 + Prometheus 抓取可达性。
type PlatformComponentService struct {
	log    *bLogger.Helper
	cluster *data.PlatformComponentCluster
	scrape *data.PlatformComponentScrapeReader
}

// NewPlatformComponentService 构造服务。cluster 必须非 nil（未配置 kubeconfig
// 时其内部 client 为 nil，Collect 返回 ErrPlatformComponentUnsupported）；
// scrape 可为 nil，表示 Prometheus 未配置，service 组 scrape_status 全 unknown。
func NewPlatformComponentService(
	log *bLogger.Helper,
	cluster *data.PlatformComponentCluster,
	scrape *data.PlatformComponentScrapeReader,
) *PlatformComponentService {
	return &PlatformComponentService{log: log, cluster: cluster, scrape: scrape}
}

func (s *PlatformComponentService) warnf(ctx context.Context, format string, args ...any) {
	if s == nil || s.log == nil {
		return
	}
	s.log.Warnf(ctx, format, args...)
}

// actor 校验平台身份与权限码。与 PlatformNetworkService 同一套闸门：
// 用户态主体、tenant_id 为 0、令牌 tenant 为 0、角色码带 platform: 前缀、
// 持有 component:status:read。
func (s *PlatformComponentService) actor(ctx context.Context) (string, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return "", kratoserrors.Unauthorized("INVALID_LOGIN", "login required")
	}
	if p.Type != auth.SubjectUser || p.TenantID != 0 || p.Claims == nil ||
		p.Claims.GetTenantId() != 0 || p.ID != p.Claims.GetUserId() ||
		!hasPlatformRole(p.Roles) || !hasPermission(p.Claims, "component:status:read") {
		return "", kratoserrors.Forbidden("PLATFORM_COMPONENT_DENIED", "platform component permission required")
	}
	return p.Actor()
}

// ListPlatformComponents 返回组件状态快照。
//
// 降级口径（对齐旧版）：单个工作负载读不到 → 该行 unknown + reason，整体
// 仍返回快照；Prometheus 不可用 → service 组 scrape_status 全 unknown 并追加
// reason，但不翻转 real_provider；仅 Kubernetes 客户端未配置时返回 500。
func (s *PlatformComponentService) ListPlatformComponents(ctx context.Context) (*data.PlatformComponentSnapshot, error) {
	if _, err := s.actor(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.cluster == nil {
		return nil, kratoserrors.New(http.StatusInternalServerError, "COMPONENT_STATUS_FAILED", "component status unavailable")
	}

	// 两个数据源并行：K8s 三 namespace List 与 Prometheus up 查询各自有
	// 独立超时预算，串行相加会顶到 HTTP 层的普通请求超时。
	var (
		snapshot   data.PlatformComponentSnapshot
		statuses   map[string]string
		collectErr error
		scrapeErr  error
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		snapshot, collectErr = s.cluster.Collect(ctx)
	}()
	go func() {
		defer wg.Done()
		statuses, scrapeErr = s.scrape.ReadScrapeStatuses(ctx)
	}()
	wg.Wait()

	if collectErr != nil {
		if errors.Is(collectErr, data.ErrPlatformComponentUnsupported) {
			s.warnf(ctx, "platform component status: %v", collectErr)
		}
		return nil, kratoserrors.New(http.StatusInternalServerError, "COMPONENT_STATUS_FAILED", "component status unavailable")
	}
	if scrapeErr != nil {
		// scrape 失败只影响 service 组的 scrape_status，不改变 real_provider。
		s.warnf(ctx, "platform component scrape status: %v", scrapeErr)
	}
	for index := range snapshot.Components {
		component := &snapshot.Components[index]
		if component.Group != data.PlatformComponentGroupService {
			continue
		}
		if scrapeErr != nil {
			component.ScrapeStatus = platformComponentStringPointer(data.PlatformComponentScrapeUnknown)
			component.Reason = joinPlatformComponentReason(component.Reason, "scrape reader unavailable: "+scrapeErr.Error())
			continue
		}
		status := statuses[data.PlatformComponentMetricsServiceName(component.Name)]
		if status == "" {
			status = data.PlatformComponentScrapeUnknown
		}
		component.ScrapeStatus = platformComponentStringPointer(status)
	}
	return &snapshot, nil
}
func platformComponentStringPointer(value string) *string { return &value }

// joinPlatformComponentReason 合并已有原因与新增原因，保持旧版 "; " 分隔。
func joinPlatformComponentReason(existing *string, extra string) *string {
	if existing == nil || *existing == "" {
		return platformComponentStringPointer(extra)
	}
	return platformComponentStringPointer(*existing + "; " + extra)
}

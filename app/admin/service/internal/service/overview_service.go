package service

import (
	"context"
	"sync"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	inferencev1 "go-wind-admin/api/gen/go/inference/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/types/known/emptypb"
)

// 首页资源卡片预置状态键：旧版契约的全部键都返回，未出现的记 0，不省略字段。
var (
	overviewInstanceStateKeys   = []string{"pending", "running", "stopped", "failed"}
	overviewInferenceStatusKeys = []string{"pending", "deploying", "running", "stopping", "stopped", "failed"}
	overviewModelStatusKeys     = []string{"pending", "importing", "ready", "error"}
	overviewKnowledgeBaseKeys   = []string{"active", "rebuilding"}
	overviewModelStatusFilter   = []string{"pending", "importing", "ready", "error"}
)

const (
	// 推理列表不支持按状态过滤，只能全量翻页；单页 100、最多 50 页（5000 条）。
	overviewInferencePageSize = int32(100)
	overviewInferenceMaxPages = 50
	// 传输层预算 10s，内部聚合预算 8s，留 2s 余量写回 200。
	overviewAggregateBudget = 8 * time.Second
)

// OverviewAuthorizer 是首页聚合的入口与数据源二次鉴权。入口权限码
// overview:resource-count:read 挂 INSTANCE 模块；数据源复用各自列表路由。
type OverviewAuthorizer interface {
	Authorize(context.Context, uint32, uint32, string, string) error
	AuthorizeSource(context.Context, uint32, uint32, string, string) error
}

// 首页聚合只依赖三个既有服务的只读列表方法，便于按数据源注入替身做降级测试。
type overviewInstanceLister interface {
	ListInstances(context.Context, *view.ListInstancesRequest) (*view.ListInstancesResponse, error)
}

type overviewInferenceLister interface {
	ListInference(context.Context, *inferencev1.ListInferenceRequest) (*inferencev1.ListInferenceResponse, error)
}

type overviewModelLister interface {
	ListModels(context.Context, *catalogv1.ListModelsRequest) (*catalogv1.ListModelsResponse, error)
}

// OverviewService 为首页四张资源卡片提供只读聚合快照。
// 任一数据源失败（含 403 降级、下游不可用、超时）时该卡片返回空计数，
// 整体仍 200；这与旧版 console_overview 的部分成功语义一致。
type OverviewService struct {
	log           *bLogger.Helper
	instances     overviewInstanceLister
	inference     overviewInferenceLister
	models        overviewModelLister
	authorization OverviewAuthorizer
}

func NewOverviewService(
	log *bLogger.Helper,
	instances overviewInstanceLister,
	inference overviewInferenceLister,
	models overviewModelLister,
	authorization OverviewAuthorizer,
) *OverviewService {
	return &OverviewService{log: log, instances: instances, inference: inference, models: models, authorization: authorization}
}

func (s *OverviewService) warnf(ctx context.Context, format string, args ...any) {
	if s == nil || s.log == nil {
		return
	}
	s.log.Warnf(ctx, format, args...)
}

func zeroInstanceOverview() *adminV1.ConsoleInstanceOverview {
	byState := make(map[string]int64, len(overviewInstanceStateKeys))
	for _, key := range overviewInstanceStateKeys {
		byState[key] = 0
	}
	return &adminV1.ConsoleInstanceOverview{ByState: byState}
}

func zeroStatusOverview(keys []string) *adminV1.ConsoleStatusOverview {
	byStatus := make(map[string]int64, len(keys))
	for _, key := range keys {
		byStatus[key] = 0
	}
	return &adminV1.ConsoleStatusOverview{ByStatus: byStatus}
}

// GetOverview 组装首页四张卡片。入口鉴权失败整体拒绝（403/503），
// 数据源失败只降级对应卡片。
func (s *OverviewService) GetOverview(ctx context.Context, _ *emptypb.Empty) (*adminV1.ConsoleOverviewResponse, error) {
	p, err := auth.PrincipalFromContext(ctx)
	if err != nil || p == nil || p.ID == 0 {
		return nil, kratoserrors.Unauthorized("INVALID_LOGIN", "login required")
	}
	if p.TenantID == 0 {
		return nil, kratoserrors.Forbidden("FORBIDDEN", "overview denied")
	}
	if s.authorization == nil {
		return nil, kratoserrors.ServiceUnavailable("OVERVIEW_AUTHORIZATION_UNAVAILABLE", "overview authorization unavailable")
	}
	if err := instanceAuthorizationError(s.authorization.Authorize(ctx, p.TenantID, p.ID, data.OverviewPath, "GET")); err != nil {
		return nil, err
	}

	out := &adminV1.ConsoleOverviewResponse{
		Instances:         zeroInstanceOverview(),
		InferenceServices: zeroStatusOverview(overviewInferenceStatusKeys),
		Models:            zeroStatusOverview(overviewModelStatusKeys),
		KnowledgeBases:    zeroStatusOverview(overviewKnowledgeBaseKeys),
	}
	// 知识库没有任何数据源，恒为空计数（不调用任何下游）。
	call, cancel := context.WithTimeout(ctx, overviewAggregateBudget)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		out.Instances = s.instanceOverview(call, p)
	}()
	go func() {
		defer wg.Done()
		out.InferenceServices = s.inferenceOverview(call, p)
	}()
	go func() {
		defer wg.Done()
		out.Models = s.modelsOverview(call, p)
	}()
	wg.Wait()
	return out, nil
}

// instanceOverview 按 running/stopped/pending 三次计数（pageSize=1），不使用
// 无过滤 total（含 deleted）。pending 计入异常：by_state.failed = pending 数，
// by_state.pending 恒为 0。
func (s *OverviewService) instanceOverview(ctx context.Context, p *auth.Principal) *adminV1.ConsoleInstanceOverview {
	card := zeroInstanceOverview()
	if s.instances == nil {
		s.warnf(ctx, "console overview data source degraded: source=instances tenant=%d err=missing", p.TenantID)
		return card
	}
	page, size := uint32(1), uint32(1)
	counts := make(map[string]int64, 3)
	for _, state := range []string{"running", "stopped", "pending"} {
		reply, err := s.instances.ListInstances(ctx, &view.ListInstancesRequest{Page: &page, PageSize: &size, ObservedState: state})
		if err != nil || reply == nil {
			s.warnf(ctx, "console overview data source degraded: source=instances state=%s tenant=%d err=%v", state, p.TenantID, err)
			return card
		}
		counts[state] = reply.GetTotal()
	}
	card.Total = counts["running"] + counts["stopped"] + counts["pending"]
	card.ByState["running"] = counts["running"]
	card.ByState["stopped"] = counts["stopped"]
	card.ByState["failed"] = counts["pending"]
	card.Other = counts["stopped"]
	return card
}

// inferenceOverview 全量翻页并按 desiredState 剔除软删记录，再按 phase 聚合：
// ready→running、stopped→stopped，其余值（含过渡态）→failed 并记 WARN。
func (s *OverviewService) inferenceOverview(ctx context.Context, p *auth.Principal) *adminV1.ConsoleStatusOverview {
	card := zeroStatusOverview(overviewInferenceStatusKeys)
	if s.inference == nil {
		s.warnf(ctx, "console overview data source degraded: source=inference_services tenant=%d err=missing", p.TenantID)
		return card
	}
	if err := s.authorization.AuthorizeSource(ctx, p.TenantID, p.ID, data.InferenceCreatePath, "INFERENCE"); err != nil {
		s.warnf(ctx, "console overview data source denied: source=inference_services tenant=%d err=%v", p.TenantID, err)
		return card
	}
	page, size := int32(1), overviewInferencePageSize
	first, err := s.inference.ListInference(ctx, &inferencev1.ListInferenceRequest{Page: &page, PageSize: &size})
	if err != nil || first == nil {
		s.warnf(ctx, "console overview data source degraded: source=inference_services tenant=%d err=%v", p.TenantID, err)
		return card
	}
	pages := int((first.GetTotal() + int64(size) - 1) / int64(size))
	if pages > overviewInferenceMaxPages {
		pages = overviewInferenceMaxPages
		s.warnf(ctx, "console overview inference pages capped: tenant=%d total=%d", p.TenantID, first.GetTotal())
	}
	var running, stopped, failed int64
	collect := func(items []*inferencev1.InferenceView) {
		for _, item := range items {
			if item == nil || item.GetDesiredState() == "deleted" {
				continue
			}
			switch item.GetPhase() {
			case "ready":
				running++
			case "stopped":
				stopped++
			default:
				failed++
				s.warnf(ctx, "console overview inference phase unrecognized: phase=%s tenant=%d", item.GetPhase(), p.TenantID)
			}
		}
	}
	collect(first.GetItems())
	for n := int32(2); n <= int32(pages); n++ {
		requestPage := n
		reply, err := s.inference.ListInference(ctx, &inferencev1.ListInferenceRequest{Page: &requestPage, PageSize: &size})
		if err != nil || reply == nil {
			s.warnf(ctx, "console overview data source degraded: source=inference_services page=%d tenant=%d err=%v", n, p.TenantID, err)
			return card
		}
		collect(reply.GetItems())
	}
	card.Total = running + stopped + failed
	card.ByStatus["running"] = running
	card.ByStatus["stopped"] = stopped
	card.ByStatus["failed"] = failed
	card.Other = stopped
	return card
}

// modelsOverview 按 pending/importing/ready/error 四次计数（pageSize=1），
// 并额外取一次无过滤 total：若大于四者之和只记 WARN 不计数，保证
// total = ready + error + other 恒等。
func (s *OverviewService) modelsOverview(ctx context.Context, p *auth.Principal) *adminV1.ConsoleStatusOverview {
	card := zeroStatusOverview(overviewModelStatusKeys)
	if s.models == nil {
		s.warnf(ctx, "console overview data source degraded: source=models tenant=%d err=missing", p.TenantID)
		return card
	}
	if err := s.authorization.AuthorizeSource(ctx, p.TenantID, p.ID, data.OverviewModelsPath, "MODEL"); err != nil {
		s.warnf(ctx, "console overview data source denied: source=models tenant=%d err=%v", p.TenantID, err)
		return card
	}
	page, size := int32(1), int32(1)
	counts := make(map[string]int64, len(overviewModelStatusFilter))
	for _, status := range overviewModelStatusFilter {
		reply, err := s.models.ListModels(ctx, &catalogv1.ListModelsRequest{Status: status, Page: &page, PageSize: &size})
		if err != nil || reply == nil {
			s.warnf(ctx, "console overview data source degraded: source=models status=%s tenant=%d err=%v", status, p.TenantID, err)
			return card
		}
		counts[status] = reply.GetTotal()
	}
	sum := counts["pending"] + counts["importing"] + counts["ready"] + counts["error"]
	if all, err := s.models.ListModels(ctx, &catalogv1.ListModelsRequest{Page: &page, PageSize: &size}); err == nil && all != nil && all.GetTotal() > sum {
		s.warnf(ctx, "console overview model status unrecognized: total=%d counted=%d tenant=%d", all.GetTotal(), sum, p.TenantID)
	}
	card.Total = sum
	for status, count := range counts {
		card.ByStatus[status] = count
	}
	card.Other = counts["pending"] + counts["importing"]
	return card
}

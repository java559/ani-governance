package service

import (
	"context"
	"math"
	"strconv"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	monitoring "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/monitoring/v1"
	target "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/target/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// trendMetric 是首页资源趋势专用目录：前端 metric 枚举 → obs 租户级编码与单位。
// 与实例接口的 metricCatalog 相互独立，避免两类接口互相污染（实施手册 3.2）。
type trendMetric struct{ Code, Unit string }

var trendMetricCodes = map[string]trendMetric{
	"cpu":    {"tenant.cpu.usage_cores", "cores"},
	"memory": {"tenant.memory.working_set_bytes", "bytes"},
	"gpu":    {"tenant.gpu.allocated_cores", "cores"},
}

// trendTrendTiers 按 range 档位分级校验（技术方案 2.6 三档：1d/7d/30d）。
type trendTier struct {
	span, minStep, maxStep, defaultStep time.Duration
}

var trendTiers = map[string]trendTier{
	"1d":  {24 * time.Hour, 30 * time.Second, time.Hour, 5 * time.Minute},
	"7d":  {7 * 24 * time.Hour, 5 * time.Minute, time.Hour, 30 * time.Minute},
	"30d": {30 * 24 * time.Hour, 15 * time.Minute, 4 * time.Hour, 2 * time.Hour},
}

// trendTargetKind 是 obs 的租户级 TargetKind（TENANT=3）。方案 A 不改 obs api
// 子模块 proto：proto3 未知枚举值原样透传，obs 侧按数值识别租户级聚合。
const trendTargetKind = target.TargetKind(3)

func trendInvalid() error {
	return kratoserrors.BadRequest("MONITORING_INVALID_QUERY", "invalid monitoring query")
}

// trendScope 取可信租户身份：租户只来自已认证主体，经 INSTANCE 模块租户闸门
// （路径 /api/v1/observability/resource-trend，权限码 observability:resource-trend:read），
// 再映射为 Res 侧租户 UUID；actor 形态与 obs 的受信校验一致。
func (s *InstanceService) trendScope(ctx context.Context) (string, string, error) {
	p, e := gpuPrincipal(ctx)
	if e != nil {
		return "", "", e
	}
	if s.binding == nil || s.binding.authorization == nil {
		return "", "", kratoserrors.Forbidden("FORBIDDEN", "instance action denied")
	}
	if e = instanceAuthorizationError(s.binding.authorization.Authorize(ctx, p.TenantID, p.ID, data.ObservabilityResourceTrendPath, "GET")); e != nil {
		return "", "", e
	}
	tenant, e := s.resolver.ResourceTenantID(ctx, p.TenantID)
	return tenant, "governance:user:" + strconv.FormatUint(uint64(p.ID), 10), e
}

// QueryResourceTrend 首页资源趋势（租户级聚合，GPU/CPU/内存三条曲线）。
// 前端只传 metric/range（可选 start/end/step），租户身份只取自 JWT，
// obs 编码与 PromQL 表达式全部由服务端固定，上游无法注入任何查询片段。
func (s *InstanceService) QueryResourceTrend(ctx context.Context, r *view.QueryResourceTrendRequest) (*view.QueryResourceTrendResponse, error) {
	if r == nil {
		return nil, trendInvalid()
	}
	tier, ok := trendTiers[r.Range]
	if !ok {
		return nil, trendInvalid()
	}
	// metric 省略时一次返回三条曲线，顺序固定 cpu/memory/gpu。
	names := []string{"cpu", "memory", "gpu"}
	if r.Metric != "" {
		if _, ok := trendMetricCodes[r.Metric]; !ok {
			return nil, trendInvalid()
		}
		names = []string{r.Metric}
	}
	codes := make([]string, 0, len(names))
	for _, name := range names {
		codes = append(codes, trendMetricCodes[name].Code)
	}

	step := tier.defaultStep
	if r.StepSeconds != nil {
		step = time.Duration(*r.StepSeconds) * time.Second
	}
	var start, end time.Time
	if r.Start != nil || r.End != nil {
		// 显式窗口必须成对出现、整秒精度且落在档位跨度内。
		if r.Start == nil || r.End == nil || !metricTimestamp(r.Start) || !metricTimestamp(r.End) {
			return nil, trendInvalid()
		}
		start, end = r.Start.AsTime(), r.End.AsTime()
		// 不再拒绝 end 晚于当前时间：客户端时钟漂移会导致稳定越界。
		if end.Before(start) || end.Sub(start) > tier.span {
			return nil, trendInvalid()
		}
	} else {
		// 后端统一计算窗口，避免客户端时间漂移在 24h 边界被拒（方案 9.9）。
		end = time.Now().Truncate(time.Second)
		start = end.Add(-tier.span)
	}
	if step < tier.minStep || step > tier.maxStep || step%(30*time.Second) != 0 {
		return nil, trendInvalid()
	}
	count := int(end.Sub(start)/step) + 1
	if count > 3000 {
		return nil, trendInvalid()
	}

	tenant, actor, err := s.trendScope(ctx)
	if err != nil {
		return nil, err
	}
	if s.monitoring == nil {
		return nil, kratoserrors.ServiceUnavailable("MONITORING_SOURCE_UNAVAILABLE", "monitoring unavailable")
	}
	// obs 租户级批次预算 15s，客户端同步放大到 20s（实施手册 3.1/4.3）。
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	reply, err := s.monitoring.QueryRange(call, tenant, actor, &monitoring.QueryRangeRequest{
		Target:      &target.TargetScope{Kind: trendTargetKind, TenantId: tenant},
		MetricCodes: codes,
		Start:       timestamppb.New(start),
		End:         timestamppb.New(end),
		StepSeconds: uint32(step / time.Second),
	})
	if err != nil {
		return nil, err
	}
	if reply == nil || reply.Start == nil || reply.End == nil ||
		reply.Start.AsTime().Unix() != start.Unix() || reply.End.AsTime().Unix() != end.Unix() ||
		reply.StepSeconds != uint32(step/time.Second) {
		return nil, metricResponseInvalid()
	}
	series, err := trendSeries(reply.Series, names, codes, start, step, count)
	if err != nil {
		return nil, err
	}
	return &view.QueryResourceTrendResponse{Data: &view.ResourceTrendData{Start: reply.Start, End: reply.End, StepSeconds: reply.StepSeconds, Series: series}}, nil
}

// trendSeries 严格校验 obs 回包并映射为前端曲线：编码/单位/点数/网格/状态逐一核对，
// 任何不一致都以 502 拒绝，绝不透出半可信数据。
func trendSeries(in []*monitoring.MetricSeries, names, codes []string, start time.Time, step time.Duration, count int) ([]*view.ResourceTrendSeries, error) {
	if len(in) != len(codes) {
		return nil, metricResponseInvalid()
	}
	out := make([]*view.ResourceTrendSeries, 0, len(in))
	for i, s := range in {
		tm, ok := trendMetricCodes[names[i]]
		if !ok || s == nil || s.MetricCode != codes[i] || s.Unit != tm.Unit || len(s.Points) != count {
			return nil, metricResponseInvalid()
		}
		item := &view.ResourceTrendSeries{Metric: names[i], MetricCode: s.MetricCode, Unit: s.Unit, Points: make([]*view.ResourceTrendPoint, 0, count)}
		valid := 0
		for n, p := range s.Points {
			if p == nil || !metricTimestamp(p.Timestamp) || !p.Timestamp.AsTime().Equal(start.Add(time.Duration(n)*step)) {
				return nil, metricResponseInvalid()
			}
			point := &view.ResourceTrendPoint{Timestamp: p.Timestamp}
			if p.Value != nil {
				v := *p.Value
				if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || p.GapReason != monitoring.GapReason_GAP_REASON_UNSPECIFIED {
					return nil, metricResponseInvalid()
				}
				point.Value = wrapperspb.Double(v)
				valid++
			} else {
				var reason string
				switch p.GapReason {
				case monitoring.GapReason_GAP_REASON_NO_DATA:
					reason = "NO_DATA"
				case monitoring.GapReason_GAP_REASON_AMBIGUOUS_SOURCE:
					reason = "AMBIGUOUS_SOURCE"
				case monitoring.GapReason_GAP_REASON_INVALID_VALUE:
					reason = "INVALID_VALUE"
				default:
					return nil, metricResponseInvalid()
				}
				point.GapReason = &reason
			}
			item.Points = append(item.Points, point)
		}
		status := monitoring.SeriesStatus_SERIES_STATUS_NO_DATA
		item.Status = "NO_DATA"
		if valid == count {
			status = monitoring.SeriesStatus_SERIES_STATUS_OK
			item.Status = "OK"
		} else if valid > 0 {
			status = monitoring.SeriesStatus_SERIES_STATUS_PARTIAL
			item.Status = "PARTIAL"
		}
		if s.Status != status {
			return nil, metricResponseInvalid()
		}
		out = append(out, item)
	}
	return out, nil
}

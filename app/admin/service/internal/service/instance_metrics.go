package service

import (
	"context"
	"math"
	"regexp"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	monitoring "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/monitoring/v1"
	target "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/target/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type InstanceMonitoringClient interface {
	QueryRange(context.Context, string, string, *monitoring.QueryRangeRequest) (*monitoring.QueryRangeResponse, error)
	QueryLatest(context.Context, string, string, *monitoring.QueryLatestRequest) (*monitoring.QueryLatestResponse, error)
}

var metricNamespace = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// metricCatalog 是实例指标的完整目录：code → 单位。白名单与单位映射共用。
var metricCatalog = map[string]string{
	"cpu.usage_cores":                   "cores",
	"memory.working_set_bytes":          "bytes",
	"network.receive_bytes_per_second":  "bytes_per_second",
	"network.transmit_bytes_per_second": "bytes_per_second",
	"gpu.utilization_percent":           "percent",
	"gpu.memory_usage_bytes":            "bytes",
}

// targetKindVM 与 observability 内部 VM kind 对齐；该枚举尚未进入 obs API 模块，
// 因此这里以数值发送，避免为一次枚举扩展引入跨仓依赖升级。
const targetKindVM = target.TargetKind(4)

func metricUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
func metricInvalid() error {
	return kratoserrors.BadRequest("MONITORING_INVALID_QUERY", "invalid monitoring query")
}
func metricResponseInvalid() error {
	return kratoserrors.New(502, "MONITORING_SOURCE_RESPONSE_INVALID", "invalid monitoring response")
}
func metricCodesValid(codes []string) bool {
	if len(codes) < 1 || len(codes) > 6 {
		return false
	}
	seen := map[string]bool{}
	for _, code := range codes {
		if seen[code] {
			return false
		}
		if _, ok := metricCatalog[code]; !ok {
			return false
		}
		seen[code] = true
	}
	return true
}
func metricTimestamp(ts *timestamppb.Timestamp) bool {
	return ts != nil && ts.CheckValid() == nil && ts.Nanos == 0
}

func (s *InstanceService) metricTarget(ctx context.Context, id, path, method string) (*target.TargetScope, string, error) {
	tenant, actor, err := s.scope(ctx, path, method)
	if err != nil {
		return nil, "", err
	}
	if !metricUUID(id) {
		return nil, "", metricInvalid()
	}
	if s.monitoring == nil {
		return nil, "", kratoserrors.ServiceUnavailable("MONITORING_SOURCE_UNAVAILABLE", "monitoring unavailable")
	}
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	scope, err := s.binding.client.ResolveInstanceMetricScope(call, tenant, actor, id)
	if err != nil {
		return nil, "", err
	}
	if scope == nil || scope.TenantId != tenant || scope.InstanceId != id || !metricUUID(scope.TenantId) || !metricUUID(scope.InstanceId) || !metricUUID(scope.ClusterId) || len(scope.Namespace) > 63 || !metricNamespace.MatchString(scope.Namespace) {
		return nil, "", metricResponseInvalid()
	}
	kind := target.TargetKind_TARGET_KIND_CONTAINER_INSTANCE
	switch scope.ContainerName {
	case "workspace":
	case "compute":
		// VM（KubeVirt）实例由 resource-service 统一解析，容器名固定 compute。
		kind = targetKindVM
	default:
		return nil, "", metricResponseInvalid()
	}
	return &target.TargetScope{Kind: kind, TenantId: scope.TenantId, InstanceId: scope.InstanceId, ClusterId: scope.ClusterId, Namespace: scope.Namespace, ContainerName: scope.ContainerName}, actor, nil
}

func (s *InstanceService) QueryInstanceMetrics(ctx context.Context, r *view.QueryInstanceMetricsRequest) (*view.QueryInstanceMetricsResponse, error) {
	if r == nil || r.Data == nil {
		return nil, metricInvalid()
	}
	d := r.Data
	step := uint32(60)
	if d.StepSeconds != nil {
		step = *d.StepSeconds
	}
	if !metricCodesValid(d.MetricCodes) || !metricTimestamp(d.Start) || !metricTimestamp(d.End) || step < 30 || step > 3600 || step%30 != 0 {
		return nil, metricInvalid()
	}
	start, end := d.Start.AsTime(), d.End.AsTime()
	span := end.Sub(start)
	// 不再拒绝 end 晚于服务器当前时间：前端按客户端时钟计算窗口并向上取整，
	// 秒级时钟漂移会稳定触发该校验。跨度与点数上限仍然生效。
	if end.Before(start) || span > 24*time.Hour || int64(span/time.Second)/int64(step)+1 > 3000 {
		return nil, metricInvalid()
	}
	t, actor, err := s.metricTarget(ctx, r.InstanceId, data.InstanceMetricsQueryPath, "POST")
	if err != nil {
		return nil, err
	}
	call, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	reply, err := s.monitoring.QueryRange(call, t.TenantId, actor, &monitoring.QueryRangeRequest{Target: t, MetricCodes: d.MetricCodes, Start: d.Start, End: d.End, StepSeconds: step})
	if err != nil {
		return nil, err
	}
	if reply == nil || !proto.Equal(reply.Start, d.Start) || !proto.Equal(reply.End, d.End) || reply.StepSeconds != step {
		return nil, metricResponseInvalid()
	}
	series, err := metricSeries(reply.Series, d.MetricCodes, start, step, int(span/time.Second)/int(step)+1)
	if err != nil {
		return nil, err
	}
	return &view.QueryInstanceMetricsResponse{Data: &view.InstanceMetricsRangeData{InstanceId: r.InstanceId, Start: reply.Start, End: reply.End, StepSeconds: step, Series: series}}, nil
}
func (s *InstanceService) GetInstanceLatestMetrics(ctx context.Context, r *view.GetInstanceRequest) (*view.GetInstanceLatestMetricsResponse, error) {
	if r == nil {
		return nil, metricInvalid()
	}
	t, actor, err := s.metricTarget(ctx, r.InstanceId, data.InstanceMetricsLatestPath, "GET")
	if err != nil {
		return nil, err
	}
	codes := []string{"cpu.usage_cores", "memory.working_set_bytes", "network.receive_bytes_per_second", "network.transmit_bytes_per_second", "gpu.utilization_percent", "gpu.memory_usage_bytes"}
	call, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	reply, err := s.monitoring.QueryLatest(call, t.TenantId, actor, &monitoring.QueryLatestRequest{Target: t, MetricCodes: codes})
	if err != nil {
		return nil, err
	}
	if reply == nil || !metricTimestamp(reply.EvaluatedAt) {
		return nil, metricResponseInvalid()
	}
	series, err := metricSeries(reply.Series, codes, reply.EvaluatedAt.AsTime(), 0, 1)
	if err != nil {
		return nil, err
	}
	return &view.GetInstanceLatestMetricsResponse{Data: &view.InstanceMetricsLatestData{InstanceId: r.InstanceId, EvaluatedAt: reply.EvaluatedAt, Series: series}}, nil
}

func metricSeries(in []*monitoring.MetricSeries, codes []string, start time.Time, step uint32, count int) ([]*view.InstanceMetricSeries, error) {
	if len(in) != len(codes) {
		return nil, metricResponseInvalid()
	}
	out := make([]*view.InstanceMetricSeries, 0, len(in))
	for i, s := range in {
		unit, ok := metricCatalog[codes[i]]
		if !ok {
			return nil, metricResponseInvalid()
		}
		if s == nil || s.MetricCode != codes[i] || s.Unit != unit || len(s.Points) != count {
			return nil, metricResponseInvalid()
		}
		item := &view.InstanceMetricSeries{MetricCode: s.MetricCode, Unit: s.Unit, Points: make([]*view.InstanceMetricPoint, 0, count)}
		valid := 0
		for n, p := range s.Points {
			if p == nil || !metricTimestamp(p.Timestamp) || !p.Timestamp.AsTime().Equal(start.Add(time.Duration(n)*time.Duration(step)*time.Second)) {
				return nil, metricResponseInvalid()
			}
			point := &view.InstanceMetricPoint{Timestamp: p.Timestamp}
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

package server

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RegisterObservabilityTrendHTTP 注册首页资源趋势 GET handler（薄封装）：
// 查询参数在此严格校验（未声明参数、重复参数、空值、坏格式一律 400），
// 业务校验（档位、步长、跨度）在 service 层，错误码沿用实例指标映射。
func RegisterObservabilityTrendHTTP(server *khttp.Server, s *service.InstanceService) {
	server.Route("/").GET(data.ObservabilityResourceTrendPath, func(ctx khttp.Context) error {
		khttp.SetOperation(ctx, admin.OperationInstanceServiceQueryResourceTrend)
		request, err := decodeResourceTrendHTTP(ctx.Request())
		if err != nil {
			return err
		}
		out, err := ctx.Middleware(func(call context.Context, in any) (any, error) {
			return s.QueryResourceTrend(call, in.(*view.QueryResourceTrendRequest))
		})(ctx, request)
		if err != nil {
			return instanceMetricsHTTPError(err)
		}
		return instanceMetricsResult(ctx, out.(proto.Message))
	})
}

func trendInvalidHTTP() error {
	return kratoserrors.BadRequest("MONITORING_INVALID_QUERY", "invalid monitoring query")
}

func decodeResourceTrendHTTP(r *http.Request) (*view.QueryResourceTrendRequest, error) {
	if r == nil || r.Method != http.MethodGet || r.Body == nil {
		return nil, trendInvalidHTTP()
	}
	// GET 只接受查询串：不接受请求体、非规范路径、强制查询与超长查询串。
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.ForceQuery ||
		(r.URL.RawPath != "" && r.URL.RawPath != r.URL.Path) || len(r.URL.RawQuery) > 2048 {
		return nil, trendInvalidHTTP()
	}
	if raw, err := io.ReadAll(io.LimitReader(r.Body, 1)); err != nil || len(raw) != 0 {
		return nil, trendInvalidHTTP()
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, trendInvalidHTTP()
	}
	request := &view.QueryResourceTrendRequest{}
	fields := map[string]func(string){
		"metric": func(v string) { request.Metric = v },
		"range":  func(v string) { request.Range = v },
		"start":  func(v string) { request.Start = trendTimestamp(v) },
		"end":    func(v string) { request.End = trendTimestamp(v) },
		"step":   func(v string) { request.StepSeconds = trendStepSeconds(v) },
	}
	converters := map[string]func(string) bool{
		"metric": func(v string) bool { return v == "cpu" || v == "memory" || v == "gpu" },
		"range":  func(v string) bool { return v == "1d" || v == "7d" || v == "30d" },
		"start":  func(v string) bool { return trendTimestamp(v) != nil },
		"end":    func(v string) bool { return trendTimestamp(v) != nil },
		"step":   func(v string) bool { return trendStepSeconds(v) != nil },
	}
	for key, values := range query {
		convert, declared := converters[key]
		if !declared || len(values) != 1 || values[0] == "" || !convert(values[0]) {
			return nil, trendInvalidHTTP()
		}
		fields[key](values[0])
	}
	return request, nil
}

// trendTimestamp 解析 RFC3339 整秒时间戳；带小数秒的输入由 service 层拒绝。
func trendTimestamp(v string) *timestamppb.Timestamp {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil
	}
	ts := timestamppb.New(t)
	if ts.CheckValid() != nil {
		return nil
	}
	return ts
}

// trendStepSeconds 解析 Go duration 字符串（如 30m / 2h）为整秒；0、负值
// 与非整秒在 handler 拒绝，档位上下限由 service 层按 range 校验。
func trendStepSeconds(v string) *uint32 {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 || d%time.Second != 0 || d/time.Second > math.MaxUint32 {
		return nil
	}
	seconds := uint32(d / time.Second)
	return &seconds
}

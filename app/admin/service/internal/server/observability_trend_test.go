package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	monitoring "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/monitoring/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// trendTestUnits 与 service.trendMetricCodes 的单位映射保持一致（测试侧独立维护）。
var trendTestUnits = map[string]string{"cpu": "cores", "memory": "bytes", "gpu": "cores"}

var trendTestCodes = map[string]string{
	"cpu":    "tenant.cpu.usage_cores",
	"memory": "tenant.memory.working_set_bytes",
	"gpu":    "tenant.gpu.allocated_cores",
}

type trendPeer struct {
	calls     int
	kind      int32
	metric    string
	start     string
	end       string
	step      uint32
	clusterID string
	failure   error
}

func (p *trendPeer) QueryRange(ctx context.Context, tenant, actor string, r *monitoring.QueryRangeRequest) (*monitoring.QueryRangeResponse, error) {
	p.calls++
	p.kind = int32(r.Target.Kind)
	p.metric = strings.Join(r.MetricCodes, ",")
	p.start = r.Start.AsTime().Format(time.RFC3339)
	p.end = r.End.AsTime().Format(time.RFC3339)
	p.step = r.StepSeconds
	p.clusterID = r.Target.ClusterId
	if tenant != diagnosticTenantID || actor != "governance:user:9" || r.Target.TenantId != tenant ||
		r.Target.InstanceId != "" || r.Target.Namespace != "" || r.Target.ContainerName != "" {
		panic("untrusted tenant scope")
	}
	if d, ok := ctx.Deadline(); !ok || time.Until(d) > 20*time.Second {
		panic("obs deadline missing")
	}
	if p.failure != nil {
		return nil, p.failure
	}
	zero, v := 0.0, 1.5
	count := int(r.End.AsTime().Sub(r.Start.AsTime())/(time.Duration(r.StepSeconds)*time.Second)) + 1
	series := make([]*monitoring.MetricSeries, 0, len(r.MetricCodes))
	for _, code := range r.MetricCodes {
		unit := ""
		for name, c := range trendTestCodes {
			if c == code {
				unit = trendTestUnits[name]
			}
		}
		points := make([]*monitoring.MetricPoint, 0, count)
		for n := 0; n < count; n++ {
			ts := timestamppb.New(r.Start.AsTime().Add(time.Duration(n) * time.Duration(r.StepSeconds) * time.Second))
			if n == 0 {
				points = append(points, &monitoring.MetricPoint{Timestamp: ts, Value: &zero})
			} else if n == count-1 {
				points = append(points, &monitoring.MetricPoint{Timestamp: ts, Value: &v})
			} else {
				points = append(points, &monitoring.MetricPoint{Timestamp: ts, GapReason: monitoring.GapReason_GAP_REASON_NO_DATA})
			}
		}
		series = append(series, &monitoring.MetricSeries{MetricCode: code, Unit: unit, Status: monitoring.SeriesStatus_SERIES_STATUS_PARTIAL, Points: points})
	}
	return &monitoring.QueryRangeResponse{Start: r.Start, End: r.End, StepSeconds: r.StepSeconds, Series: series}, nil
}

func (p *trendPeer) QueryLatest(context.Context, string, string, *monitoring.QueryLatestRequest) (*monitoring.QueryLatestResponse, error) {
	panic("trend must not call QueryLatest")
}

// trendAuthorization 只放行趋势路由（独立于实例指标路径的租户闸门）。
type trendAuthorization struct {
	deny  bool
	calls int
}

func (a *trendAuthorization) Authorize(_ context.Context, tenant, actor uint32, path, method string) error {
	a.calls++
	if a.deny || tenant != 7 || actor != 9 || path != data.ObservabilityResourceTrendPath || method != "GET" {
		return data.ErrActionAuthorizationDenied
	}
	return nil
}

func trendHTTP(t *testing.T, peer *trendPeer, authorization *trendAuthorization, authenticated bool) *httptest.Server {
	t.Helper()
	s := service.NewInstanceService(nil, service.NewInstanceGpuBinding(&metricsResource{}, authorization, ""), diagnosticTenants{}, nil, nil, service.InstanceCatalogConfig{Monitoring: peer})
	srv := khttp.NewServer(khttp.Timeout(0), khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, r any) (any, error) {
			op, ok := transport.FromServerContext(ctx)
			if !ok || op.Operation() != "InstanceService_QueryResourceTrend" && op.Operation() != "/admin.v1.InstanceService/QueryResourceTrend" && !strings.Contains(op.Operation(), "QueryResourceTrend") {
				t.Errorf("missing operation: %v", op.Operation())
			}
			if authenticated {
				ctx = auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7})
			}
			return next(ctx, r)
		}
	}))
	RegisterObservabilityTrendHTTP(srv, s)
	actual := httptest.NewServer(srv)
	t.Cleanup(actual.Close)
	return actual
}

func trendRequest(t *testing.T, srv *httptest.Server, query string) (int, map[string]any) {
	t.Helper()
	response, err := srv.Client().Get(srv.URL + data.ObservabilityResourceTrendPath + query) //nolint:gosec // loopback httptest server
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	return response.StatusCode, object
}

func TestResourceTrendHTTPDefaultWindowAndSeries(t *testing.T) {
	p, a := &trendPeer{}, &trendAuthorization{}
	srv := trendHTTP(t, p, a, true)
	code, object := trendRequest(t, srv, "?range=7d")
	if code != 200 {
		t.Fatalf("%d %+v", code, object)
	}
	d := object["data"].(map[string]any)
	series := d["series"].([]any)
	if len(series) != 3 || p.calls != 1 || a.calls != 1 || p.kind != 3 || p.clusterID != "" {
		t.Fatalf("three series expected: %d %+v kind=%d", len(series), d, p.kind)
	}
	if p.metric != "tenant.cpu.usage_cores,tenant.memory.working_set_bytes,tenant.gpu.allocated_cores" {
		t.Fatalf("code order %s", p.metric)
	}
	// 默认窗口：end=当前整秒、start=end-7d、step=30m。
	end, err := time.Parse(time.RFC3339, p.end)
	if err != nil {
		t.Fatal(err)
	}
	if end.Sub(end.Truncate(time.Second)) != 0 || time.Until(end) > time.Second {
		t.Fatalf("end not truncated to now: %s", p.end)
	}
	if want := end.Add(-7 * 24 * time.Hour).Format(time.RFC3339); p.start != want || p.step != 1800 {
		t.Fatalf("default 7d window %s..%s step=%d", p.start, p.end, p.step)
	}
	names := []string{"cpu", "memory", "gpu"}
	for i, item := range series {
		s := item.(map[string]any)
		if s["metric"] != names[i] || s["metric_code"] != trendTestCodes[names[i]] || s["unit"] != trendTestUnits[names[i]] {
			t.Fatalf("series[%d]=%v", i, s)
		}
		points := s["points"].([]any)
		if len(points) != 337 {
			t.Fatalf("7d/30m points %d", len(points))
		}
		zero := points[0].(map[string]any)
		if zero["value"] != float64(0) {
			t.Fatalf("valid point %v", zero)
		}
		if _, exists := zero["gap_reason"]; exists {
			t.Fatalf("valid point must omit gap_reason: %v", zero)
		}
	}
	// 单指标请求只返回一条。
	code, object = trendRequest(t, srv, "?metric=gpu&range=30d")
	if code != 200 || len(object["data"].(map[string]any)["series"].([]any)) != 1 || p.step != 7200 {
		t.Fatalf("gpu 30d: %d %+v step=%d", code, object, p.step)
	}
}

func TestResourceTrendHTTPRejectsInput(t *testing.T) {
	cases := []string{
		"", "?range=2d", "?range=7D", "?metric=cpu", "?metric=all&range=1d", "?range=7d&metric=",
		"?range=7d&foo=1", "?range=7d&metric=cpu&metric=memory", "?range=7d&tenant_id=other",
		"?range=7d&start=not-a-time", "?range=7d&start=2026-10-20T00:00:00Z",
		"?range=7d&start=2026-10-20T00:00:00Z&end=2026-10-21T00:00:00.5Z",
		"?range=7d&step=0s", "?range=7d&step=-30m", "?range=7d&step=90.5s",
		"?range=1d&step=4h", "?range=7d&step=1m", "?range=30d&step=16h",
	}
	for _, query := range cases {
		t.Run(query, func(t *testing.T) {
			p := &trendPeer{}
			srv := trendHTTP(t, p, &trendAuthorization{}, true)
			code, _ := trendRequest(t, srv, query)
			if code != 400 || p.calls != 0 {
				t.Fatalf("query %q: %d calls=%d", query, code, p.calls)
			}
		})
	}
}

func TestResourceTrendHTTPWindowBounds(t *testing.T) {
	// 显式窗口必须成对、跨度不超档位；end 允许晚于当前时间（客户端时钟漂移）。
	p := &trendPeer{}
	srv := trendHTTP(t, p, &trendAuthorization{}, true)
	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-24 * time.Hour)
	code, _ := trendRequest(t, srv, fmt.Sprintf("?range=1d&start=%s&end=%s&step=1h", start.Format(time.RFC3339), end.Format(time.RFC3339)))
	if code != 200 || p.step != 3600 {
		t.Fatalf("explicit 1d window: %d step=%d", code, p.step)
	}
	future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	code, _ = trendRequest(t, srv, fmt.Sprintf("?range=1d&start=%s&end=%s", future.Add(-24*time.Hour).Format(time.RFC3339), future.Format(time.RFC3339)))
	if code != 200 || p.step != 300 {
		t.Fatalf("future end rejected: %d step=%d", code, p.step)
	}
	old := end.Add(-25 * time.Hour)
	code, _ = trendRequest(t, srv, fmt.Sprintf("?range=1d&start=%s&end=%s", old.Format(time.RFC3339), end.Format(time.RFC3339)))
	if code != 400 {
		t.Fatalf("25h span on 1d tier accepted: %d", code)
	}
	span31d := end.Add(-31 * 24 * time.Hour)
	code, _ = trendRequest(t, srv, fmt.Sprintf("?range=30d&start=%s&end=%s", span31d.Format(time.RFC3339), end.Format(time.RFC3339)))
	if code != 400 {
		t.Fatalf("31d span accepted: %d", code)
	}
}

func TestResourceTrendHTTPAuthorizationAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name                string
		authenticated, deny bool
		failure             error
		want                int
	}{{"unauthenticated", false, false, nil, 401}, {"permission", true, true, nil, 403},
		{"timeout", true, false, status.Error(codes.DeadlineExceeded, "private"), 504},
		{"unavailable", true, false, status.Error(codes.Unavailable, "down"), 503}} {
		t.Run(tc.name, func(t *testing.T) {
			p := &trendPeer{failure: tc.failure}
			srv := trendHTTP(t, p, &trendAuthorization{deny: tc.deny}, tc.authenticated)
			code, _ := trendRequest(t, srv, "?range=1d")
			if code != tc.want {
				t.Fatalf("%d", code)
			}
			if !tc.authenticated || tc.deny {
				if p.calls != 0 {
					t.Fatal("unauthorized dependency call")
				}
			}
		})
	}
}

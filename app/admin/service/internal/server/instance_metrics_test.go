package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	monitoring "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/monitoring/v1"
	target "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/target/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type metricsAuthorization struct {
	deny  bool
	calls int
}

func (a *metricsAuthorization) Authorize(_ context.Context, tenant, actor uint32, path, method string) error {
	a.calls++
	if a.deny || tenant != 7 || actor != 9 || !(path == data.InstanceMetricsQueryPath && method == "POST" || path == data.InstanceMetricsLatestPath && method == "GET") {
		return data.ErrActionAuthorizationDenied
	}
	return nil
}

type metricsResource struct {
	service.InstanceOwnerClient
	calls     int
	tenant    string
	container string
	failure   error
}

func (p *metricsResource) ResolveInstanceMetricScope(ctx context.Context, tenant, actor, id string) (*res.InstanceMetricScope, error) {
	p.calls++
	if d, ok := ctx.Deadline(); !ok || time.Until(d) > 2*time.Second {
		panic("Res deadline missing")
	}
	if p.failure != nil {
		return nil, p.failure
	}
	if actor != "governance:user:9" {
		panic("untrusted actor")
	}
	if p.tenant != "" {
		tenant = p.tenant
	}
	container := p.container
	if container == "" {
		container = "workspace"
	}
	return &res.InstanceMetricScope{TenantId: tenant, InstanceId: id, ClusterId: "30000000-0000-4000-8000-000000000003", Namespace: "workspace", ContainerName: container}, nil
}

type metricsPeer struct {
	calls     int
	step      uint32
	kind      target.TargetKind
	container string
	failure   error
}

// metricTestUnits 与 service.metricCatalog 的单位映射保持一致（测试侧独立维护）。
var metricTestUnits = map[string]string{
	"cpu.usage_cores":                   "cores",
	"memory.working_set_bytes":          "bytes",
	"network.receive_bytes_per_second":  "bytes_per_second",
	"network.transmit_bytes_per_second": "bytes_per_second",
	"gpu.utilization_percent":           "percent",
	"gpu.memory_usage_bytes":            "bytes",
}

func (p *metricsPeer) QueryRange(ctx context.Context, tenant, actor string, r *monitoring.QueryRangeRequest) (*monitoring.QueryRangeResponse, error) {
	p.calls++
	p.step = r.StepSeconds
	p.kind = r.Target.Kind
	p.container = r.Target.ContainerName
	if tenant != diagnosticTenantID || actor != "governance:user:9" || r.Target.TenantId != tenant {
		panic("untrusted scope")
	}
	if d, ok := ctx.Deadline(); !ok || time.Until(d) > 6*time.Second {
		panic("Obs deadline missing")
	}
	if p.failure != nil {
		return nil, p.failure
	}
	zero, v := 0.0, 0.75
	series := make([]*monitoring.MetricSeries, 0, len(r.MetricCodes))
	for _, code := range r.MetricCodes {
		series = append(series, &monitoring.MetricSeries{MetricCode: code, Unit: metricTestUnits[code], Status: monitoring.SeriesStatus_SERIES_STATUS_PARTIAL, Points: []*monitoring.MetricPoint{{Timestamp: r.Start, Value: &zero}, {Timestamp: timestamppb.New(r.Start.AsTime().Add(time.Duration(r.StepSeconds) * time.Second)), GapReason: monitoring.GapReason_GAP_REASON_NO_DATA}, {Timestamp: r.End, Value: &v}}})
	}
	return &monitoring.QueryRangeResponse{Start: r.Start, End: r.End, StepSeconds: r.StepSeconds, Series: series}, nil
}
func (p *metricsPeer) QueryLatest(ctx context.Context, tenant, actor string, r *monitoring.QueryLatestRequest) (*monitoring.QueryLatestResponse, error) {
	p.calls++
	p.kind = r.Target.Kind
	p.container = r.Target.ContainerName
	if strings.Join(r.MetricCodes, ",") != "cpu.usage_cores,memory.working_set_bytes,network.receive_bytes_per_second,network.transmit_bytes_per_second,gpu.utilization_percent,gpu.memory_usage_bytes" {
		panic("latest order")
	}
	if p.failure != nil {
		return nil, p.failure
	}
	now := timestamppb.New(time.Now().Truncate(time.Second))
	zero := 0.0
	series := make([]*monitoring.MetricSeries, 0, len(r.MetricCodes))
	for i, code := range r.MetricCodes {
		points := []*monitoring.MetricPoint{{Timestamp: now, GapReason: monitoring.GapReason_GAP_REASON_NO_DATA}}
		status := monitoring.SeriesStatus_SERIES_STATUS_NO_DATA
		if i == 0 {
			points = []*monitoring.MetricPoint{{Timestamp: now, Value: &zero}}
			status = monitoring.SeriesStatus_SERIES_STATUS_OK
		}
		series = append(series, &monitoring.MetricSeries{MetricCode: code, Unit: metricTestUnits[code], Status: status, Points: points})
	}
	return &monitoring.QueryLatestResponse{EvaluatedAt: now, Series: series}, nil
}
func metricsHTTP(t *testing.T, resource *metricsResource, peer *metricsPeer, authorization *metricsAuthorization, authenticated bool) *httptest.Server {
	t.Helper()
	s := service.NewInstanceService(nil, service.NewInstanceGpuBinding(resource, authorization, ""), diagnosticTenants{}, nil, nil, service.InstanceCatalogConfig{Monitoring: peer})
	srv := khttp.NewServer(khttp.Timeout(0), khttp.Middleware(func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, r any) (any, error) {
			op, ok := transport.FromServerContext(ctx)
			if !ok || !strings.Contains(op.Operation(), "InstanceService/") {
				t.Error("missing operation")
			}
			if authenticated {
				ctx = auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 7})
			}
			return next(ctx, r)
		}
	}))
	RegisterInstanceMetricsHTTP(srv, s)
	actual := httptest.NewServer(srv)
	t.Cleanup(actual.Close)
	return actual
}
func metricsRequest(t *testing.T, srv *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	r, _ := http.NewRequest(method, srv.URL+"/api/v1/instances/"+diagnosticInstanceID+"/metrics/"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	response, err := srv.Client().Do(r)
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

const validMetricsBody = `{"data":{"metric_codes":["cpu.usage_cores"],"start":"2026-10-01T08:00:00+08:00","end":"2026-10-01T08:02:00+08:00"}}`

func TestInstanceMetricsHTTPEncodingAndTrustedScope(t *testing.T) {
	r, p, a := &metricsResource{}, &metricsPeer{}, &metricsAuthorization{}
	srv := metricsHTTP(t, r, p, a, true)
	code, object := metricsRequest(t, srv, "POST", "query", validMetricsBody)
	if code != 200 {
		t.Fatalf("%d %+v", code, object)
	}
	d := object["data"].(map[string]any)
	if d["instance_id"] != diagnosticInstanceID || d["start"] != "2026-10-01T00:00:00Z" || p.step != 60 || r.calls != 1 || a.calls != 1 {
		t.Fatalf("scope/default/time %+v", d)
	}
	points := d["series"].([]any)[0].(map[string]any)["points"].([]any)
	zero := points[0].(map[string]any)
	missing := points[1].(map[string]any)
	if zero["value"] != float64(0) || zero["gap_reason"] != nil || missing["value"] != nil || missing["gap_reason"] != "NO_DATA" || points[2].(map[string]any)["value"] != 0.75 {
		t.Fatalf("presence %+v", points)
	}
	if _, exists := missing["value"]; !exists {
		t.Fatal("missing value must explicitly be null")
	}
	if _, exists := zero["gap_reason"]; exists {
		t.Fatal("valid points must omit gap_reason")
	}
	code, object = metricsRequest(t, srv, "GET", "latest", "")
	if code != 200 || object["data"].(map[string]any)["evaluated_at"] == nil {
		t.Fatalf("latest %d %+v", code, object)
	}
}
func TestInstanceMetricsSixCodesAcceptedAndSevenRejected(t *testing.T) {
	r, p, a := &metricsResource{}, &metricsPeer{}, &metricsAuthorization{}
	srv := metricsHTTP(t, r, p, a, true)
	six := `{"data":{"metric_codes":["cpu.usage_cores","memory.working_set_bytes","network.receive_bytes_per_second","network.transmit_bytes_per_second","gpu.utilization_percent","gpu.memory_usage_bytes"],"start":"2026-10-01T08:00:00+08:00","end":"2026-10-01T08:02:00+08:00"}}`
	code, object := metricsRequest(t, srv, "POST", "query", six)
	if code != 200 {
		t.Fatalf("%d %+v", code, object)
	}
	series := object["data"].(map[string]any)["series"].([]any)
	if len(series) != 6 {
		t.Fatalf("series count %d", len(series))
	}
	wantUnits := []string{"cores", "bytes", "bytes_per_second", "bytes_per_second", "percent", "bytes"}
	wantCodes := []string{"cpu.usage_cores", "memory.working_set_bytes", "network.receive_bytes_per_second", "network.transmit_bytes_per_second", "gpu.utilization_percent", "gpu.memory_usage_bytes"}
	for i, s := range series {
		item := s.(map[string]any)
		if item["metric_code"] != wantCodes[i] || item["unit"] != wantUnits[i] {
			t.Fatalf("series[%d]=%v want %s/%s", i, item, wantCodes[i], wantUnits[i])
		}
	}
	seven := `{"data":{"metric_codes":["cpu.usage_cores","memory.working_set_bytes","network.receive_bytes_per_second","network.transmit_bytes_per_second","gpu.utilization_percent","gpu.memory_usage_bytes","cpu.usage_cores"],"start":"2026-10-01T08:00:00+08:00","end":"2026-10-01T08:02:00+08:00"}}`
	r2, p2 := &metricsResource{}, &metricsPeer{}
	srv2 := metricsHTTP(t, r2, p2, &metricsAuthorization{}, true)
	code, _ = metricsRequest(t, srv2, "POST", "query", seven)
	if code != 400 || r2.calls != 0 || p2.calls != 0 {
		t.Fatalf("seven codes: %d calls=%d/%d", code, r2.calls, p2.calls)
	}
}

func TestInstanceMetricsVMScopeUsesComputeTarget(t *testing.T) {
	r, p := &metricsResource{container: "compute"}, &metricsPeer{}
	srv := metricsHTTP(t, r, p, &metricsAuthorization{}, true)
	code, object := metricsRequest(t, srv, "GET", "latest", "")
	if code != 200 {
		t.Fatalf("vm latest %d %+v", code, object)
	}
	if int32(p.kind) != 4 || p.container != "compute" {
		t.Fatalf("vm target kind=%d container=%s", p.kind, p.container)
	}
}

func TestInstanceMetricsHTTPRejectsInputBeforeDependencies(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null}`, strings.Replace(validMetricsBody, `"metric_codes"`, `"metricCodes"`, 1), strings.Replace(validMetricsBody, `"data":`, `"instance_id":"`+diagnosticInstanceID+`","data":`, 1), strings.Replace(validMetricsBody, `"start":`, `"cluster_id":"x","start":`, 1), strings.Replace(validMetricsBody, `"start":`, `"step_seconds":null,"start":`, 1), strings.Replace(validMetricsBody, `"start":`, `"step_seconds":"60","start":`, 1), strings.Replace(validMetricsBody, `"start":`, `"step_seconds":0,"start":`, 1), strings.Replace(validMetricsBody, `"start":`, `"step_seconds":60,"step_seconds":60,"start":`, 1), strings.Replace(validMetricsBody, `["cpu.usage_cores"]`, `["cpu.usage_cores","cpu.usage_cores"]`, 1)} {
		r, p := &metricsResource{}, &metricsPeer{}
		srv := metricsHTTP(t, r, p, &metricsAuthorization{}, true)
		code, _ := metricsRequest(t, srv, "POST", "query", body)
		if code != 400 || r.calls != 0 || p.calls != 0 {
			t.Fatalf("body %s: %d calls=%d/%d", body, code, r.calls, p.calls)
		}
	}
	r, p := &metricsResource{}, &metricsPeer{}
	srv := metricsHTTP(t, r, p, &metricsAuthorization{}, true)
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{{"GET", "latest?x=1", "", 400}, {"GET", "latest?", "", 400}, {"GET", "latest", "{}", 400}, {"POST", "query?x=1", validMetricsBody, 400}, {"POST", "query", strings.Repeat(" ", 65537), 413}} {
		code, _ := metricsRequest(t, srv, tc.method, tc.path, tc.body)
		if code != tc.want {
			t.Fatalf("%+v status=%d", tc, code)
		}
	}
}
func TestInstanceMetricsHTTPAcceptsFutureEnd(t *testing.T) {
	r, p, a := &metricsResource{}, &metricsPeer{}, &metricsAuthorization{}
	srv := metricsHTTP(t, r, p, a, true)
	start := time.Now().UTC().Truncate(time.Second)
	end := start.Add(2 * time.Minute)
	body := `{"data":{"metric_codes":["cpu.usage_cores"],"start":"` +
		start.Format(time.RFC3339) + `","end":"` + end.Format(time.RFC3339) + `"}}`
	code, object := metricsRequest(t, srv, "POST", "query", body)
	if code != 200 {
		t.Fatalf("%d %+v", code, object)
	}
	raw := object["data"].(map[string]any)["end"].(string)
	got, err := time.Parse(time.RFC3339, raw)
	if err != nil || !got.Equal(end) {
		t.Fatalf("end=%s err=%v want %v", raw, err, end)
	}
	if r.calls != 1 || p.calls != 1 {
		t.Fatalf("calls=%d/%d", r.calls, p.calls)
	}
}

func TestInstanceMetricsHTTPStillRejectsOversizedWindow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for name, window := range map[string][2]time.Time{
		"past":   {now.Add(-24 * time.Hour), now.Add(time.Hour)},
		"future": {now.Add(-23 * time.Hour), now.Add(2 * time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			r, p := &metricsResource{}, &metricsPeer{}
			srv := metricsHTTP(t, r, p, &metricsAuthorization{}, true)
			body := `{"data":{"metric_codes":["cpu.usage_cores"],"start":"` +
				window[0].Format(time.RFC3339) + `","end":"` + window[1].Format(time.RFC3339) + `"}}`
			code, _ := metricsRequest(t, srv, "POST", "query", body)
			if code != 400 || r.calls != 0 || p.calls != 0 {
				t.Fatalf("oversized window: %d calls=%d/%d", code, r.calls, p.calls)
			}
		})
	}
}

func TestInstanceMetricsHTTPAuthorizationScopeAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name                string
		authenticated, deny bool
		tenant              string
		failure             error
		want                int
		reason              string
	}{{"unauthenticated", false, false, "", nil, 401, ""}, {"permission", true, true, "", nil, 403, ""}, {"cross tenant", true, false, "40000000-0000-4000-8000-000000000004", nil, 502, "MONITORING_SOURCE_RESPONSE_INVALID"}, {"timeout", true, false, "", status.Error(codes.DeadlineExceeded, "private address"), 504, "MONITORING_QUERY_TIMEOUT"}} {
		t.Run(tc.name, func(t *testing.T) {
			r, p := &metricsResource{tenant: tc.tenant}, &metricsPeer{failure: tc.failure}
			srv := metricsHTTP(t, r, p, &metricsAuthorization{deny: tc.deny}, tc.authenticated)
			code, object := metricsRequest(t, srv, "GET", "latest", "")
			if code != tc.want {
				t.Fatalf("%d %+v", code, object)
			}
			if tc.reason != "" && object["reason"] != tc.reason {
				t.Fatalf("reason %+v", object)
			}
			if !tc.authenticated || tc.deny || tc.tenant != "" {
				if p.calls != 0 {
					t.Fatal("unauthorized dependency call")
				}
			}
		})
	}
	for reason, want := range map[string]int{"MONITORING_RESULT_TOO_LARGE": 422, "MONITORING_CONCURRENCY_LIMIT_EXCEEDED": 429} {
		s, _ := status.New(codes.ResourceExhausted, "private expression").WithDetails(&errdetails.ErrorInfo{Reason: reason})
		r, p := &metricsResource{}, &metricsPeer{failure: s.Err()}
		srv := metricsHTTP(t, r, p, &metricsAuthorization{}, true)
		code, object := metricsRequest(t, srv, "GET", "latest", "")
		if code != want || object["reason"] != reason {
			t.Fatalf("%d %+v", code, object)
		}
	}
	s, _ := status.New(codes.FailedPrecondition, "private scope").WithDetails(&errdetails.ErrorInfo{Reason: "INSTANCE_METRIC_SCOPE_UNAVAILABLE"})
	srv := metricsHTTP(t, &metricsResource{failure: s.Err()}, &metricsPeer{}, &metricsAuthorization{}, true)
	code, object := metricsRequest(t, srv, "GET", "latest", "")
	if code != 409 || object["reason"] != "INSTANCE_METRIC_SCOPE_UNAVAILABLE" {
		t.Fatalf("%d %+v", code, object)
	}
}

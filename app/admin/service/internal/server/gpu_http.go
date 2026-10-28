package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	errors "github.com/go-kratos/kratos/v2/errors"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	admin "go-wind-admin/api/gen/go/admin/service/v1"
	view "go-wind-admin/api/gen/go/gpu/service/v1"
	"go-wind-admin/app/admin/service/internal/service"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var gpuHTTPPath = regexp.MustCompile(`^/api/v1/(admin/gpu|gpu)(/|$)`)
var gpuQuotaCode = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

func RegisterGpuHTTPServer(s *khttp.Server, g *service.GpuMonitoringService) {
	type route struct {
		method, path, operation string
		request                 func() proto.Message
		invoke                  func(context.Context, proto.Message) (proto.Message, error)
	}
	routes := []route{
		{"GET", "/api/v1/admin/gpu/devices", admin.OperationGpuAdminServiceListGpuDevices, func() proto.Message { return new(view.ListGpuDevicesRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return g.ListGpuDevices(c, r.(*view.ListGpuDevicesRequest))
		}},
		{"GET", "/api/v1/admin/gpu/devices/{device_id}/metrics/latest", admin.OperationGpuAdminServiceGetPhysicalGpuLatest, func() proto.Message { return new(view.GpuPhysicalLatestRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return g.GetPhysicalGpuLatest(c, r.(*view.GpuPhysicalLatestRequest))
		}},
		{"POST", "/api/v1/admin/gpu/devices/{device_id}/metrics/query", admin.OperationGpuAdminServiceQueryPhysicalGpu, func() proto.Message { return new(view.GpuPhysicalQueryRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return g.QueryPhysicalGpu(c, r.(*view.GpuPhysicalQueryRequest))
		}},
		{"GET", "/api/v1/admin/gpu/metrics/latest", admin.OperationGpuAdminServiceGetPlatformGpuLatest, func() proto.Message { return new(view.GpuPhysicalLatestRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return g.GetPlatformGpuLatest(c, r.(*view.GpuPhysicalLatestRequest))
		}},
		{"POST", "/api/v1/admin/gpu/metrics/query", admin.OperationGpuAdminServiceQueryPlatformGpu, func() proto.Message { return new(view.GpuPhysicalQueryRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return g.QueryPlatformGpu(c, r.(*view.GpuPhysicalQueryRequest))
		}},
		{"GET", "/api/v1/admin/gpu/tenants/{tenant_id}/metrics/latest", admin.OperationGpuAdminServiceGetTenantGpuLatest, func() proto.Message { return new(view.GpuTenantLatestRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return g.GetTenantGpuLatest(c, r.(*view.GpuTenantLatestRequest))
		}},
		{"POST", "/api/v1/admin/gpu/tenants/{tenant_id}/metrics/query", admin.OperationGpuAdminServiceQueryTenantGpu, func() proto.Message { return new(view.GpuTenantQueryRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return g.QueryTenantGpu(c, r.(*view.GpuTenantQueryRequest))
		}},
		{"GET", "/api/v1/gpu/metrics/latest", admin.OperationGpuServiceGetMyGpuLatest, func() proto.Message { return new(view.GpuTenantLatestRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return g.GetMyGpuLatest(c, r.(*view.GpuTenantLatestRequest))
		}},
		{"POST", "/api/v1/gpu/metrics/query", admin.OperationGpuServiceQueryMyGpu, func() proto.Message { return new(view.GpuTenantQueryRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return g.QueryMyGpu(c, r.(*view.GpuTenantQueryRequest))
		}},
		{"GET", "/api/v1/gpu/resources/{resource_kind}/{resource_id}/metrics/latest", admin.OperationGpuServiceGetResourceGpuLatest, func() proto.Message { return new(view.GpuResourceLatestRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return g.GetResourceGpuLatest(c, r.(*view.GpuResourceLatestRequest))
		}},
		{"POST", "/api/v1/gpu/resources/{resource_kind}/{resource_id}/metrics/query", admin.OperationGpuServiceQueryResourceGpu, func() proto.Message { return new(view.GpuResourceQueryRequest) }, func(c context.Context, r proto.Message) (proto.Message, error) {
			return g.QueryResourceGpu(c, r.(*view.GpuResourceQueryRequest))
		}},
	}
	router := s.Route("/")
	for _, r := range routes {
		r := r
		handler := func(ctx khttp.Context) error {
			khttp.SetOperation(ctx, r.operation)
			request := r.request()
			var err error
			if r.method == "GET" {
				err = ctx.BindQuery(request)
			} else {
				err = ctx.Bind(request)
			}
			if err != nil {
				return errors.BadRequest("GPU_INVALID_QUERY", "invalid GPU query")
			}
			if err = ctx.BindVars(request); err != nil {
				return errors.BadRequest("GPU_INVALID_QUERY", "invalid GPU target")
			}
			out, err := ctx.Middleware(func(call context.Context, in any) (any, error) { return r.invoke(call, in.(proto.Message)) })(ctx, request)
			if err != nil {
				return err
			}
			if reply, ok := out.(*view.GpuQueryResponse); ok {
				return gpuHTTPResult(ctx, reply)
			}
			return ctx.Result(http.StatusOK, out)
		}
		if r.method == "GET" {
			router.GET(r.path, handler)
		} else {
			router.POST(r.path, handler)
		}
	}
}

func gpuHTTPJSON(message *view.GpuQueryResponse) ([]byte, error) {
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(message)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err = json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	nullable := func(obj map[string]any, keys ...string) {
		for _, key := range keys {
			if _, ok := obj[key]; !ok {
				obj[key] = nil
			}
		}
	}
	quality := func(obj any) {
		if q, ok := obj.(map[string]any); ok {
			nullable(q, "coverage", "expectedSources")
		}
	}
	if series, ok := body["series"].([]any); ok {
		for _, item := range series {
			row, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New(502, "GPU_INVALID_RESPONSE", "invalid GPU series")
			}
			nullable(row, "mean", "peak")
			quality(row["quality"])
			if points, ok := row["points"].([]any); ok {
				for _, item := range points {
					point, ok := item.(map[string]any)
					if !ok {
						return nil, errors.New(502, "GPU_INVALID_RESPONSE", "invalid GPU point")
					}
					nullable(point, "value", "peak", "knownValue", "knownPeak")
					quality(point["quality"])
				}
			}
		}
	}
	raw, err = json.Marshal(body)
	if err == nil && len(raw) > 8<<20 {
		return nil, errors.New(429, "GPU_QUERY_BUDGET_EXCEEDED", "GPU response exceeds8MiB")
	}
	return raw, err
}
func gpuHTTPResult(ctx khttp.Context, message *view.GpuQueryResponse) error {
	raw, err := gpuHTTPJSON(message)
	if err != nil {
		return err
	}
	ctx.Response().Header().Set("Content-Type", "application/json")
	ctx.Response().WriteHeader(http.StatusOK)
	_, err = ctx.Response().Write(raw)
	return err
}

func gpuHTTPFilter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		summary := r.URL.Path == "/api/v1/admin/tenants/quota-account-summaries"
		if summary || gpuHTTPPath.MatchString(r.URL.Path) {
			if err := validateGpuHTTP(r, summary); err != nil {
				khttp.DefaultErrorEncoder(w, r, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func validateGpuHTTP(r *http.Request, summary bool) error {
	if r.URL.Path == gpuTenantListPath {
		return validateGpuTenantHTTP(r)
	}
	invalid := errors.BadRequest("GPU_INVALID_QUERY", "invalid or unsupported GPU query field")
	if len(r.URL.RawQuery) > 4096 {
		return invalid
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return invalid
	}
	if r.Method == http.MethodGet {
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			return invalid
		}
		for k, vs := range q {
			if !summary && k == "metricCodes" && strings.HasSuffix(r.URL.Path, "/metrics/latest") {
				if len(vs) > 4 {
					return invalid
				}
				seen := map[string]bool{}
				for _, v := range vs {
					if v == "" || seen[v] {
						return invalid
					}
					seen[v] = true
				}
				continue
			}
			if len(vs) != 1 || vs[0] == "" {
				return invalid
			}
			v := vs[0]
			if summary {
				switch k {
				case "page", "pageSize", "tenantId":
					n, e := strconv.ParseUint(v, 10, 32)
					if e != nil || n == 0 || strconv.FormatUint(n, 10) != v || k == "pageSize" && n > 100 {
						return invalid
					}
				case "quotaCode":
					if !gpuQuotaCode.MatchString(v) {
						return invalid
					}
				default:
					return invalid
				}
				continue
			}
			switch k {
			case "clusterId", "modelKey":
				if !strings.Contains(r.URL.Path, "/admin/gpu/") {
					return invalid
				}
			case "metricCodes":
				if !strings.HasSuffix(r.URL.Path, "/metrics/latest") {
					return invalid
				}
			case "page", "pageSize":
				if r.URL.Path != "/api/v1/admin/gpu/devices" {
					return invalid
				}
				n, e := strconv.ParseUint(v, 10, 32)
				if e != nil || n == 0 || strconv.FormatUint(n, 10) != v || k == "pageSize" && n > 100 {
					return invalid
				}
			default:
				return invalid
			}
		}
		return nil
	}
	if summary || r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/metrics/query") || len(q) != 0 || r.Body == nil {
		return invalid
	}
	raw, e := io.ReadAll(io.LimitReader(r.Body, 16385))
	if e != nil || len(raw) == 0 || len(raw) > 16384 {
		return invalid
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	// A streaming token check rejects duplicate keys at every object level.
	dec := json.NewDecoder(bytes.NewReader(raw))
	if e = uniqueGpuJSON(dec); e != nil {
		return invalid
	}
	if _, e = dec.Token(); e != io.EOF {
		return invalid
	}
	var body struct {
		Data *struct {
			MetricCodes []string `json:"metricCodes"`
			Start       *string  `json:"start"`
			End         *string  `json:"end"`
			Window      string   `json:"window"`
			ClusterID   string   `json:"clusterId"`
			ModelKey    string   `json:"modelKey"`
		} `json:"data"`
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil || body.Data == nil {
		return invalid
	}
	if !strings.Contains(r.URL.Path, "/admin/gpu/") && (body.Data.ClusterID != "" || body.Data.ModelKey != "") {
		return invalid
	}
	return nil
}
func uniqueGpuJSON(d *json.Decoder) error {
	t, e := d.Token()
	if e != nil {
		return e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return e
			}
			k, ok := key.(string)
			if !ok || seen[k] {
				return errors.BadRequest("GPU_INVALID_QUERY", "duplicate JSON field")
			}
			seen[k] = true
			if e = uniqueGpuJSON(d); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e = uniqueGpuJSON(d); e != nil {
				return e
			}
		}
	default:
		return errors.BadRequest("GPU_INVALID_QUERY", "invalid JSON")
	}
	_, e = d.Token()
	return e
}

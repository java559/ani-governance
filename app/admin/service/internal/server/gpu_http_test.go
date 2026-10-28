package server

import (
	"encoding/json"
	view "go-wind-admin/api/gen/go/gpu/service/v1"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGpuHTTPStrictSelectors(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		ok                       bool
	}{
		{"latest", "GET", "/api/v1/gpu/metrics/latest", "", true},
		{"metric-list", "GET", "/api/v1/gpu/metrics/latest?metricCodes=gpu.resource.memory_used_bytes&metricCodes=gpu.resource.memory_limit_bytes", "", true},
		{"metric-repeat", "GET", "/api/v1/gpu/metrics/latest?metricCodes=gpu.resource.memory_used_bytes&metricCodes=gpu.resource.memory_used_bytes", "", false},
		{"source-forgery", "GET", "/api/v1/gpu/metrics/latest?pod=other", "", false},
		{"tenant-forgery", "GET", "/api/v1/gpu/metrics/latest?tenantId=2", "", false},
		{"duplicate-cluster", "GET", "/api/v1/admin/gpu/devices?clusterId=a&clusterId=b", "", false},
		{"unknown-pagination", "GET", "/api/v1/admin/gpu/devices?page_size=2", "", false},
		{"zero-page", "GET", "/api/v1/admin/gpu/devices?page=0", "", false},
		{"window", "POST", "/api/v1/gpu/metrics/query", `{"data":{"window":"30d"}}`, true},
		{"unknown-data", "POST", "/api/v1/gpu/metrics/query", `{"data":{"owner":"other"}}`, false},
		{"internal-scope", "POST", "/api/v1/gpu/metrics/query", `{"data":{"scopes":[]}}`, false},
		{"duplicate-data", "POST", "/api/v1/gpu/metrics/query", `{"data":{"window":"1h","window":"30d"}}`, false},
		{"null-data", "POST", "/api/v1/gpu/metrics/query", `{"data":null}`, false},
		{"quota-page", "GET", "/api/v1/admin/tenants/quota-account-summaries?page=2&pageSize=100", "", true},
		{"quota-leading-zero", "GET", "/api/v1/admin/tenants/quota-account-summaries?page=01", "", false},
		{"quota-filter", "GET", "/api/v1/admin/tenants/quota-account-summaries?tenantId=1&quotaCode=gpu.physical", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			e := validateGpuHTTP(r, strings.Contains(tc.path, "quota-account-summaries"))
			if (e == nil) != tc.ok {
				t.Fatalf("valid=%v want=%v err=%v", e == nil, tc.ok, e)
			}
		})
	}
}
func TestGpuHTTPBudgetDoesNotChangeLegacy(t *testing.T) {
	for _, tc := range []struct {
		path     string
		min, max time.Duration
	}{
		{"/api/v1/admin/gpu/devices/id/metrics/query", 34 * time.Second, 36 * time.Second},
		{"/api/v1/gpu/metrics/latest", 34 * time.Second, 36 * time.Second},
		{"/api/v1/admin/me", time.Second, 3 * time.Second},
	} {
		t.Run(tc.path, func(t *testing.T) {
			h := instanceHTTPTimeoutFilter(2 * time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				d, ok := r.Context().Deadline()
				if !ok {
					t.Fatal("deadline missing")
				}
				left := time.Until(d)
				if left < tc.min || left > tc.max {
					t.Fatalf("budget=%v", left)
				}
			}))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", tc.path, nil))
		})
	}
}

func TestGpuHTTPMissingValuesAreNull(t *testing.T) {
	value := 0.0
	raw, err := gpuHTTPJSON(&view.GpuQueryResponse{Series: []*view.GpuSeries{{Quality: &view.GpuQuality{Status: "PARTIAL"}, Points: []*view.GpuPoint{{KnownValue: &value, Quality: &view.GpuQuality{Status: "PARTIAL"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	row := body["series"].([]any)[0].(map[string]any)
	point := row["points"].([]any)[0].(map[string]any)
	for _, key := range []string{"value", "peak", "knownPeak"} {
		v, ok := point[key]
		if !ok || v != nil {
			t.Fatalf("%s must be explicit null", key)
		}
	}
	if point["knownValue"] != float64(0) {
		t.Fatal("valid zero lost")
	}
	quality := point["quality"].(map[string]any)
	if v, ok := quality["coverage"]; !ok || v != nil {
		t.Fatal("unknown coverage must be null")
	}
}

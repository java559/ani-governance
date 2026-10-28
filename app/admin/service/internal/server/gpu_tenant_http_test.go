package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	view "go-wind-admin/api/gen/go/gpu/service/v1"
	"google.golang.org/protobuf/proto"
)

func TestGpuTenantHTTPStrictPage(t *testing.T) {
	for _, tc := range []struct {
		query string
		valid bool
	}{
		{"", true}, {"?page=2&pageSize=6&tenantId=9", true},
		{"?pageSize=7", false}, {"?page=0", false}, {"?page=01", false},
		{"?tenantId=0", false}, {"?tenantId=1&tenantId=2", false},
		{"?page_size=2", false}, {"?metricCodes=gpu.resource.memory_used_bytes", false},
		{"?start=2026-10-01", false}, {"?pod=secret", false},
	} {
		if err := validateGpuTenantHTTP(httptest.NewRequest("GET", gpuTenantListPath+tc.query, nil)); (err == nil) != tc.valid {
			t.Errorf("query=%q valid=%v err=%v", tc.query, tc.valid, err)
		}
	}
	if err := validateGpuTenantHTTP(httptest.NewRequest("GET", gpuTenantListPath, strings.NewReader(`{}`))); err == nil {
		t.Fatal("GET body accepted")
	}
}

func TestGpuTenantHTTPNestedMissingValuesAreNull(t *testing.T) {
	known := 512.0
	raw, err := gpuTenantHTTPJSON(&view.ListGpuTenantsResponse{Items: []*view.GpuTenantCurrentSummary{
		{Usage: &view.GpuQueryResponse{Series: []*view.GpuSeries{{Quality: &view.GpuQuality{Status: "PARTIAL"}, Points: []*view.GpuPoint{{KnownValue: &known, Quality: &view.GpuQuality{Status: "PARTIAL"}}}}}}, UsageStatus: "PARTIAL"},
		{UsageStatus: "UNAVAILABLE", UsageReason: "GPU_SOURCE_UNAVAILABLE", Allocation: &view.GpuTenantCurrentAllocation{Status: "PARTIAL", KnownMemoryMib: proto.String("8192"), KnownBindingCount: proto.Uint64(1)}},
		{Allocation: &view.GpuTenantCurrentAllocation{Status: "UNAVAILABLE"}},
	}, Total: 3})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	items := body["items"].([]any)
	usage := items[0].(map[string]any)["usage"].(map[string]any)
	point := usage["series"].([]any)[0].(map[string]any)["points"].([]any)[0].(map[string]any)
	for _, key := range []string{"value", "peak", "knownPeak"} {
		value, present := point[key]
		if !present || value != nil {
			t.Fatalf("%s must be explicit null: %s", key, raw)
		}
	}
	if point["knownValue"] != known || items[1].(map[string]any)["usage"] != nil || body["total"] != "3" {
		t.Fatalf("unexpected current page: %s", raw)
	}
	allocation := items[1].(map[string]any)["allocation"].(map[string]any)
	for _, key := range []string{"memoryMiB", "bindingCount"} {
		value, present := allocation[key]
		if !present || value != nil {
			t.Fatalf("allocation %s must be explicit null: %s", key, raw)
		}
	}
	if allocation["knownMemoryMiB"] != "8192" || allocation["knownBindingCount"] != "1" {
		t.Fatalf("known allocation was lost: %s", raw)
	}
	unknown := items[2].(map[string]any)["allocation"].(map[string]any)
	for _, key := range []string{"memoryMiB", "bindingCount", "knownMemoryMiB", "knownBindingCount"} {
		value, present := unknown[key]
		if !present || value != nil {
			t.Fatalf("unavailable allocation %s must be explicit null: %s", key, raw)
		}
	}
	raw, err = gpuTenantHTTPJSON(&view.ListGpuTenantsResponse{})
	if err != nil || !strings.Contains(string(raw), `"items":[]`) || !strings.Contains(string(raw), `"total":"0"`) {
		t.Fatalf("empty page: %s %v", raw, err)
	}
}

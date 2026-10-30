package auth

import (
	adminv1 "go-wind-admin/api/gen/go/admin/service/v1"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlatformNetworkHTTPContract(t *testing.T) {
	const base = "/api/v1/admin/networks"
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"eip_page_filter", "GET", base + "/eips?page=3&pageSize=2&tenant_id=17&address=10.10.20.202&scope=public", "", 204},
		{"eip_detail", "GET", base + "/eips/eip_11111111111111111111111111111111", "", 204},
		{"eip_release", "DELETE", base + "/eips/eip_11111111111111111111111111111111", "", 204},
		{"eip_wrong_id", "DELETE", base + "/eips/pool_11111111111111111111111111111111", "", 400},
		{"eip_duplicate_tenant", "GET", base + "/eips?tenant_id=17&tenant_id=18", "", 400},
		{"eip_legacy_cursor", "GET", base + "/eips?cursor=a", "", 400},
		{"eip_page_zero", "GET", base + "/eips?page=0", "", 400},
		{"eip_page_size_overflow", "GET", base + "/eips?pageSize=101", "", 400},
		{"list", "GET", base + "/public-address-pools?limit=1&state=available", "", 204},
		{"default_egress_create", "POST", base + "/default-vpc-egresses", `{"data":{"name":"run-default-egress","pool_id":"pool_0123456789abcdef0123456789abcdef","idempotency_key":"default-egress"}}`, 204},
		{"default_egress_get", "GET", base + "/default-vpc-egresses/dve_0123456789abcdef0123456789abcdef", "", 204},
		{"default_egress_current", "GET", base + "/default-vpc-egress", "", 204},
		{"default_egress_current_no_identity", "GET", base + "/default-vpc-egress?cluster_id=forged", "", 400},
		{"default_egress_delete", "DELETE", base + "/default-vpc-egresses/dve_0123456789abcdef0123456789abcdef", "", 204},
		{"default_egress_no_tenant", "POST", base + "/default-vpc-egresses", `{"data":{"name":"run-default-egress","pool_id":"pool_0123456789abcdef0123456789abcdef","tenant_id":"arbitrary"}}`, 400},
		{"default_egress_no_arbitrary_vpc", "POST", base + "/default-vpc-egresses", `{"data":{"name":"run-default-egress","pool_id":"pool_0123456789abcdef0123456789abcdef","default_vpc_uid":"arbitrary"}}`, 400},
		{"default_egress_wrong_id", "GET", base + "/default-vpc-egresses/vpc_0123456789abcdef0123456789abcdef", "", 400},
		{"switch_name_only", "POST", base + "/virtual-switches", `{"data":{"name":"test-switch","idempotency_key":"create-switch"}}`, 204},
		{"switch_unbound_get", "GET", base + "/virtual-switches/vsw_0123456789abcdef0123456789abcdef", "", 204},
		{"switch_delete", "DELETE", base + "/virtual-switches/vsw_0123456789abcdef0123456789abcdef", "", 204},
		{"switch_direct_page", "GET", base + "/virtual-switches?page=3&pageSize=2", "", 204},
		{"switch_default_page", "GET", base + "/virtual-switches?pageSize=2", "", 204},
		{"switch_default_size", "GET", base + "/virtual-switches?page=3", "", 204},
		{"switch_zero_page", "GET", base + "/virtual-switches?page=0", "", 400},
		{"switch_large_page", "GET", base + "/virtual-switches?page=2147483648", "", 400},
		{"switch_zero_size", "GET", base + "/virtual-switches?pageSize=0", "", 400},
		{"switch_large_size", "GET", base + "/virtual-switches?pageSize=101", "", 400},
		{"switch_noncanonical", "GET", base + "/virtual-switches?page=01", "", 400},
		{"switch_duplicate_page", "GET", base + "/virtual-switches?page=1&page=2", "", 400},
		{"switch_cursor_rejected", "GET", base + "/virtual-switches?cursor=a", "", 400},
		{"switch_region_rejected", "POST", base + "/virtual-switches", `{"data":{"name":"test-switch","region":"r1"}}`, 400},
		{"switch_description_rejected", "POST", base + "/virtual-switches", `{"data":{"name":"test-switch","description":"desc"}}`, 400},
		{"vlan_v2", "POST", base + "/vlan-networks", `{"data":{"name":"vlan","cluster_id":"cluster","virtual_switch_id":"vsw_0123456789abcdef0123456789abcdef","device_name":"ens35","vlan_id":0,"idempotency_key":"vlan-v2"}}`, 204},
		{"vlan_mixed_version", "POST", base + "/vlan-networks", `{"data":{"name":"vlan","device_id":"device_0123456789abcdef0123456789abcdef","cluster_id":"cluster","virtual_switch_id":"vsw_0123456789abcdef0123456789abcdef","device_name":"ens35"}}`, 400},
		{"vlan_partial_v2", "POST", base + "/vlan-networks", `{"data":{"name":"vlan","cluster_id":"cluster","device_name":"ens35"}}`, 400},
		{"default_limit", "GET", base + "/intranet-address-pools", "", 204},
		{"create_data", "POST", base + "/egress-gateways", `{"data":{"name":"gw","idempotency_key":"create"}}`, 204},
		{"legacy_window", "GET", base + "/vlan-networks?page=1", "", 400},
		{"zero", "GET", base + "/vlan-networks?limit=0", "", 400},
		{"overflow", "GET", base + "/vlan-networks?limit=101", "", 400},
		{"noncanonical", "GET", base + "/vlan-networks?limit=01", "", 400},
		{"duplicate", "GET", base + "/vlan-networks?limit=1&limit=2", "", 400},
		{"unknown_filter", "GET", base + "/public-address-pools?tenant_id=0", "", 400},
		{"identity_in_body", "POST", base + "/egress-gateways", `{"data":{"name":"gw","actor":"governance:user:1"}}`, 400},
		{"bad_reference", "POST", base + "/vlan-networks", `{"data":{"name":"vlan","device_id":"vpc_0123456789abcdef0123456789abcdef"}}`, 400},
		{"duplicate_body_field", "POST", base + "/egress-gateways", `{"data":{"name":"a","name":"b"}}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			w := httptest.NewRecorder()
			NetworkHTTPFilter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				op, _, _, _ := networkHTTPRoute(r)
				if !IsPlatformNetworkOperation(op) || ValidateNetworkRequest(r, op) != nil {
					t.Fatal("validated platform operation missing")
				}
				if allowsAPIKey(op) {
					t.Fatal("tenant key gained platform authority")
				}
				w.WriteHeader(204)
			})).ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	for _, header := range []string{"X-Ani-Scope", "X-Ani-Tenant-Id", "X-Ani-Actor", "X-Platform-Admin"} {
		t.Run(header, func(t *testing.T) {
			r := httptest.NewRequest("GET", base+"/capabilities", nil)
			r.Header.Set(header, "platform")
			w := httptest.NewRecorder()
			NetworkHTTPFilter(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("spoofed identity reached decoding") })).ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("status=%d", w.Code)
			}
		})
	}
}

func TestRetiredPlatformNetworkRoutesAreRejected(t *testing.T) {
	const base = "/api/v1/admin/networks"
	const device = "/devices/device_0123456789abcdef0123456789abcdef"
	for _, route := range []struct{ operation, method, path string }{
		{"ListNodeInterfaces", "GET", "/node-interfaces"},
		{"AdoptNetworkDevice", "POST", "/devices"},
		{"GetNetworkDevice", "GET", device},
		{"RetireNetworkDevice", "POST", device + "/retirement"},
		{"GetNetworkDeviceRetirement", "GET", device + "/retirement"},
	} {
		t.Run(route.operation, func(t *testing.T) {
			for _, method := range adminv1.PlatformNetworkService_ServiceDesc.Methods {
				if method.MethodName == route.operation {
					t.Fatal("retired RPC remains generated")
				}
			}
			operation := "/admin.service.v1.PlatformNetworkService/" + route.operation
			if IsPlatformNetworkOperation(operation) || platformNetworkMessage(operation) != nil {
				t.Fatal("retired operation remains registered")
			}
			r := httptest.NewRequest(route.method, base+route.path, nil)
			if operation, _, _, _ := networkHTTPRoute(r); operation != "" {
				t.Fatal("retired route remains registered")
			}
			w := httptest.NewRecorder()
			NetworkHTTPFilter(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("retired route reached decoding")
			})).ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

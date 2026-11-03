package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityGroupHTTPRejectsNestedPathIdentityAndImplicitClear(t *testing.T) {
	group := "sg_0123456789abcdef0123456789abcdef"
	rule := "sgr_0123456789abcdef0123456789abcdef"
	for _, test := range []struct{ name, method, path, body string }{
		{"forged default rule", "POST", "/api/v1/networks/security-groups/" + group + "/rules", `{"data":{"rule":{"direction":"ingress","protocol":"all","action":"drop","priority":1,"is_default":true}}}`},
		{"wrong rule in body", "PATCH", "/api/v1/networks/security-groups/" + group + "/rules/" + rule, `{"rule_id":"sgr_ffffffffffffffffffffffffffffffff","data":{"rule":{"action":"drop"}}}`},
		{"wrong group in body", "PATCH", "/api/v1/networks/security-groups/" + group + "/rules/" + rule, `{"security_group_id":"sg_ffffffffffffffffffffffffffffffff","data":{"rule":{"action":"drop"}}}`},
		{"foreign nested path", "GET", "/api/v1/networks/security-groups/" + group + "/rules/../../x", ""},
		{"malformed nested rule", "GET", "/api/v1/networks/security-groups/" + group + "/rules/sg_0123456789abcdef0123456789abcdef", ""},
		{"implicit EIP clear", "PUT", "/api/v1/networks/eips/" + policyEIPID + "/security-groups", `{"data":{"expected_version":"1","idempotency_key":"clear"}}`},
		{"null EIP clear", "PUT", "/api/v1/networks/eips/" + policyEIPID + "/security-groups", `{"data":{"security_group_ids":null}}`},
		{"unknown direction", "GET", "/api/v1/networks/security-groups/" + group + "/rules?direction=sideways", ""},
		{"duplicate page", "GET", "/api/v1/networks/security-groups?page=1&page=2", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			NetworkHTTPFilter(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(w, r)
			if called || w.Code != 400 {
				t.Fatalf("unsafe request reached handler: %d", w.Code)
			}
		})
	}
}

func TestSecurityGroupDefaultRuleRouteIsRegisteredForOwnerRejection(t *testing.T) {
	called := false
	path := "/api/v1/networks/security-groups/sg_0123456789abcdef0123456789abcdef/rules/sgr_default_ingress_drop"
	request := httptest.NewRequest("DELETE", path, nil)
	response := httptest.NewRecorder()
	NetworkHTTPFilter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(204) })).ServeHTTP(response, request)
	if !called || response.Code != 204 {
		t.Fatal("managed default path cannot reach its owner guard", response.Code)
	}
}

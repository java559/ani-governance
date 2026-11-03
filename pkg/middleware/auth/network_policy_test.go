package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v2/errors"
)

const policySubnetID = "subnet_0123456789abcdef0123456789abcdef"
const policyEIPID = "eip_0123456789abcdef0123456789abcdef"
const policySNATID = "snat_0123456789abcdef0123456789abcdef"
const policyLBID = "lb_0123456789abcdef0123456789abcdef"
const policyOperationID = "01234567-89ab-cdef-0123-456789abcdef"

func TestNetworkAllTenantOperationsSignAndReplayBody(t *testing.T) {
	// Independent HTTP fixtures cover every public operation, including methods
	// and request shapes that the former empty-GET signer could not admit.
	fixtures := []struct{ operation, method, path, query, body string }{
		{"CreateVPC", "POST", "/api/v1/networks/vpcs", "", `{"name":"vpc","cidr":"10.0.0.0/16","idempotency_key":"create-vpc"}`},
		{"GetVPC", "GET", vectorPath, "", ""},
		{"ListVPCs", "GET", "/api/v1/networks/vpcs", "name=demo&page=3&pageSize=2&state=available", ""},
		{"ListVPCCIDRPresets", "GET", "/api/v1/networks/vpc-cidr-presets", "", ""},
		{"DeleteVPC", "DELETE", vectorPath, "", ""},
		{"GetOperation", "GET", "/api/v1/networks/operations/" + policyOperationID, "", ""},
		{"CreateSubnet", "POST", "/api/v1/networks/subnets", "", `{"vpc_id":"vpc_0123456789abcdef0123456789abcdef","name":"subnet","cidr":"10.0.1.0/24","idempotency_key":"create-subnet"}`},
		{"GetSubnet", "GET", "/api/v1/networks/subnets/" + policySubnetID, "", ""},
		{"ListSubnets", "GET", "/api/v1/networks/subnets", "limit=1&vpc_id=vpc_0123456789abcdef0123456789abcdef", ""},
		{"DeleteSubnet", "DELETE", "/api/v1/networks/subnets/" + policySubnetID, "", ""},
		{"CreateEIP", "POST", "/api/v1/networks/eips", "", `{"name":"eip","idempotency_key":"create-eip"}`},
		{"GetEIP", "GET", "/api/v1/networks/eips/" + policyEIPID, "", ""},
		{"ListEIPs", "GET", "/api/v1/networks/eips", "cursor=opaque-cursor&limit=2", ""},
		{"DeleteEIP", "DELETE", "/api/v1/networks/eips/" + policyEIPID, "", ""},
		{"GetVPCSnat", "GET", vectorPath + "/snat", "", ""},
		{"BindVPCSnat", "POST", vectorPath + "/snat/bindings", "", `{"eip_id":"eip_0123456789abcdef0123456789abcdef","idempotency_key":"bind-snat"}`},
		{"GetVPCSnatBinding", "GET", "/api/v1/networks/snat/bindings/" + policySNATID, "", ""},
		{"SetVPCSnatEnabled", "PATCH", "/api/v1/networks/snat/bindings/" + policySNATID, "", `{"enabled":false,"expected_version":"1","idempotency_key":"disable-snat"}`},
		{"DeleteVPCSnatBinding", "DELETE", "/api/v1/networks/snat/bindings/" + policySNATID, "", ""},
		{"CreateLoadBalancer", "POST", "/api/v1/networks/load-balancers", "", `{"name":"lb","vpc_id":"vpc_0123456789abcdef0123456789abcdef","subnet_id":"subnet_0123456789abcdef0123456789abcdef","exposure":"LOAD_BALANCER_EXPOSURE_PRIVATE","listener":{"protocol":"LOAD_BALANCER_LISTENER_PROTOCOL_HTTP","port":80},"idempotency_key":"create-lb"}`},
		{"GetLoadBalancer", "GET", "/api/v1/networks/load-balancers/" + policyLBID, "", ""},
		{"ListLoadBalancers", "GET", "/api/v1/networks/load-balancers", "exposure=LOAD_BALANCER_EXPOSURE_PRIVATE&limit=1&state=RESOURCE_STATE_AVAILABLE", ""},
		{"UpdateLoadBalancer", "PATCH", "/api/v1/networks/load-balancers/" + policyLBID, "", `{"expected_version":"1","idempotency_key":"update-lb","name":"updated","backends":[]}`},
		{"DeleteLoadBalancer", "DELETE", "/api/v1/networks/load-balancers/" + policyLBID, "", ""},
		{"GetLoadBalancerOperation", "GET", "/api/v1/networks/load-balancers/operations/" + policyOperationID, "", ""},
		{"CreateSecurityGroup", "POST", "/api/v1/networks/security-groups", "", `{"data":{"name":"sg","rules":[{"direction":"ingress","protocol":"tcp","remote_addresses":["10.0.0.0/8"],"port_ranges":[{"min":22,"max":22}],"action":"allow","priority":3000}],"idempotency_key":"create-sg"}}`},
		{"GetSecurityGroup", "GET", "/api/v1/networks/security-groups/sg_0123456789abcdef0123456789abcdef", "", ""},
		{"ListSecurityGroups", "GET", "/api/v1/networks/security-groups", "name=demo&page=3&pageSize=2&state=available", ""},
		{"UpdateSecurityGroup", "PATCH", "/api/v1/networks/security-groups/sg_0123456789abcdef0123456789abcdef", "", `{"data":{"name":"updated","description":"","expected_version":"1","idempotency_key":"update-sg"},"update_mask":"name,description"}`},
		{"DeleteSecurityGroup", "DELETE", "/api/v1/networks/security-groups/sg_0123456789abcdef0123456789abcdef", "", ""},
		{"ListSecurityGroupRules", "GET", "/api/v1/networks/security-groups/sg_0123456789abcdef0123456789abcdef/rules", "direction=ingress&page=2&pageSize=5", ""},
		{"CreateSecurityGroupRule", "POST", "/api/v1/networks/security-groups/sg_0123456789abcdef0123456789abcdef/rules", "", `{"data":{"rule":{"direction":"ingress","protocol":"all","action":"drop","priority":1},"expected_version":"1","idempotency_key":"create-rule"}}`},
		{"UpdateSecurityGroupRule", "PATCH", "/api/v1/networks/security-groups/sg_0123456789abcdef0123456789abcdef/rules/sgr_0123456789abcdef0123456789abcdef", "", `{"data":{"rule":{"direction":"egress","protocol":"all","action":"allow","priority":2},"expected_version":"2","idempotency_key":"update-rule"}}`},
		{"DeleteSecurityGroupRule", "DELETE", "/api/v1/networks/security-groups/sg_0123456789abcdef0123456789abcdef/rules/sgr_0123456789abcdef0123456789abcdef", "", ""},
		{"ListSecurityGroupBindings", "GET", "/api/v1/networks/security-groups/sg_0123456789abcdef0123456789abcdef/bindings", "page=1&pageSize=20", ""},
		{"SetEIPSecurityGroups", "PUT", "/api/v1/networks/eips/" + policyEIPID + "/security-groups", "", `{"data":{"security_group_ids":[],"expected_version":"1","idempotency_key":"clear-eip-sg"}}`},
		{"GetEIPSecurityGroups", "GET", "/api/v1/networks/eips/" + policyEIPID + "/security-groups", "", ""},
		{"GetSecurityGroupOperation", "GET", "/api/v1/networks/security-groups/operations/" + policyOperationID, "", ""},
	}
	if len(NetworkOperations) != len(fixtures) {
		t.Fatal("unexpected public Network surface")
	}
	now := time.Unix(1700000000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	seen := map[string]bool{}
	for _, fixture := range fixtures {
		t.Run(fixture.operation, func(t *testing.T) {
			operation := "/admin.service.v1.NetworkService/" + fixture.operation
			if seen[operation] || !allowsAPIKey(operation) {
				t.Fatal("missing or duplicate allowlist operation")
			}
			seen[operation] = true
			policy := NetworkOperations[operation]
			if code, ok := NetworkPermission(policy.Method, policy.Path); !ok || code != policy.Permission || policy.Method != fixture.method {
				t.Fatal("catalog permission mismatch")
			}
			r := httptest.NewRequest(fixture.method, fixture.path, strings.NewReader(fixture.body))
			r.URL.RawQuery = fixture.query
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Access-Key", vectorAK)
			r.Header.Set("X-Timestamp", ts)
			r.Header.Set("X-Signature", NetworkSignature(vectorSK, fixture.method, fixture.path, fixture.query, vectorAK, ts, []byte(fixture.body)))
			store := &signingStoreStub{key: &SigningKey{ID: 9, TenantID: 5, Role: "tenant:network-admin", RoleAllowed: true, Secret: vectorSK}}
			called := false
			w := httptest.NewRecorder()
			NetworkHTTPFilter(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				p, err := verifySignature(context.Background(), r, operation, store, now)
				if err != nil || p == nil || p.Type != SubjectAPIKey || p.TenantID != 5 || p.ID != 9 {
					t.Fatal("signed Network request denied", err)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != fixture.body {
					t.Fatal("signed bytes were changed")
				}
				w.WriteHeader(204)
			})).ServeHTTP(w, r)
			if !called || w.Code != 204 || store.touched != 1 {
				t.Fatal("operation was not authenticated", w.Code)
			}
		})
	}
	for _, operation := range []string{"/admin.service.v1.NetworkService/Prepare", "/network.v1.PlatformNetworkService/CreateVLAN", "/admin.service.v1.NetworkService/GetVPCExtra", "/admin.service.v1.NetworkService/GetSubmission"} {
		if allowsAPIKey(operation) {
			t.Fatal("internal/unknown operation admitted", operation)
		}
	}
}

func TestNetworkHTTPRejectsAmbiguityBeforeHandler(t *testing.T) {
	for _, test := range []struct{ name, method, path, body, header string }{
		{"identity body", "POST", "/api/v1/networks/subnets", `{"tenant_id":"foreign"}`, ""},
		{"duplicate field", "POST", "/api/v1/networks/vpcs", `{"name":"one","name":"two"}`, ""},
		{"unknown nested field", "POST", "/api/v1/networks/load-balancers", `{"listener":{"tenant_id":"foreign"}}`, ""},
		{"duplicate nested field", "POST", "/api/v1/networks/load-balancers", `{"listener":{"port":80,"port":81}}`, ""},
		{"unknown numeric enum", "POST", "/api/v1/networks/load-balancers", `{"exposure":99}`, ""},
		{"invalid nested subnet", "POST", "/api/v1/networks/load-balancers", `{"backends":[{"subnet_id":"bad"}]}`, ""},
		{"path/body mismatch", "PATCH", "/api/v1/networks/snat/bindings/" + policySNATID, `{"binding_id":"snat_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, ""},
		{"unknown query", "GET", "/api/v1/networks/subnets?tenant_id=foreign", "", ""},
		{"duplicate query", "GET", "/api/v1/networks/vpcs?limit=1&limit=2", "", ""},
		{"query aliases", "GET", "/api/v1/networks/subnets?vpcId=vpc_0123456789abcdef0123456789abcdef", "", ""},
		{"invalid limit", "GET", "/api/v1/networks/vpcs?limit=101", "", ""},
		{"invalid state", "GET", "/api/v1/networks/vpcs?state=unknown", "", ""},
		{"invalid exposure", "GET", "/api/v1/networks/load-balancers?exposure=99", "", ""},
		{"invalid query ID", "GET", "/api/v1/networks/subnets?vpc_id=bad", "", ""},
		{"body on read", "GET", vectorPath, `{}`, ""},
		{"body on delete", "DELETE", vectorPath, `{}`, ""},
		{"query on delete", "DELETE", vectorPath + "?vpc_id=vpc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", ""},
		{"empty query marker", "GET", vectorPath + "?", "", ""},
		{"encoded path", "GET", "/api/v1/networks/vpcs/%76pc_0123456789abcdef0123456789abcdef", "", ""},
		{"trailing slash", "GET", vectorPath + "/", "", ""},
		{"platform route", "POST", "/api/v1/networks/vlans", `{}`, ""},
		{"public actor", "GET", vectorPath, "", "X-Ani-Actor"},
		{"public tenant", "GET", vectorPath, "", "X-Tenant-Id"},
		{"public administrator", "GET", vectorPath, "", "X-Platform-Admin"},
		{"oversized body", "POST", "/api/v1/networks/vpcs", strings.Repeat("x", 65537), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			r.Header.Set("Content-Type", "application/json")
			if test.header != "" {
				r.Header.Set(test.header, "forged")
			}
			called := false
			w := httptest.NewRecorder()
			NetworkHTTPFilter(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(w, r)
			if w.Code != 400 || called {
				t.Fatal("invalid request reached handler", w.Code)
			}
		})
	}
}

func TestNetworkSignatureBindsMethodPathQueryAndBody(t *testing.T) {
	now := time.Unix(1700000000, 0)
	ts := "1700000000"
	for _, test := range []struct{ name, operation, method, path, query, originalBody, body, signedPath, signedQuery string }{
		{"body", "CreateVPC", "POST", "/api/v1/networks/vpcs", "", `{"name":"original"}`, `{"name":"changed"}`, "/api/v1/networks/vpcs", ""},
		{"raw bytes", "CreateVPC", "POST", "/api/v1/networks/vpcs", "", `{"name":"original"}`, `{ "name":"original"}`, "/api/v1/networks/vpcs", ""},
		{"path", "GetSubnet", "GET", "/api/v1/networks/subnets/subnet_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", "", "", "/api/v1/networks/subnets/" + policySubnetID, ""},
		{"query", "ListSubnets", "GET", "/api/v1/networks/subnets", "limit=2", "", "", "/api/v1/networks/subnets", "limit=1"},
		{"query order", "ListSubnets", "GET", "/api/v1/networks/subnets", "name=demo&limit=1", "", "", "/api/v1/networks/subnets", "name=demo&limit=1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			r.URL.RawQuery = test.query
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Access-Key", vectorAK)
			r.Header.Set("X-Timestamp", ts)
			r.Header.Set("X-Signature", NetworkSignature(vectorSK, test.method, test.signedPath, test.signedQuery, vectorAK, ts, []byte(test.originalBody)))
			store := &signingStoreStub{key: &SigningKey{ID: 9, TenantID: 5, Role: "tenant:network-admin", RoleAllowed: true, Secret: vectorSK}}
			called := false
			NetworkHTTPFilter(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				called = true
				p, err := verifySignature(context.Background(), r, "/admin.service.v1.NetworkService/"+test.operation, store, now)
				if kerrors.Code(err) != 401 || p != nil || store.touched != 0 {
					t.Fatal("altered signature accepted", err)
				}
			})).ServeHTTP(httptest.NewRecorder(), r)
			if !called {
				t.Fatal("signature check did not run")
			}
		})
	}
	if _, err := verifySignature(context.Background(), signedRequest(now.Unix()), "/admin.service.v1.NetworkService/DeleteVPC", nil, now); kerrors.Code(err) != 400 {
		t.Fatal("operation mismatch accepted")
	}
}

func TestNetworkSignatureRejectsMethodTampering(t *testing.T) {
	now := time.Unix(1700000000, 0)
	const path = "/api/v1/networks/vpcs"
	const body = `{"name":"original"}`
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Access-Key", vectorAK)
	r.Header.Set("X-Timestamp", "1700000000")
	r.Header.Set("X-Signature", NetworkSignature(vectorSK, "GET", path, "", vectorAK, "1700000000", []byte(body)))
	store := &signingStoreStub{key: &SigningKey{ID: 9, TenantID: 5, Role: "tenant:network-admin", RoleAllowed: true, Secret: vectorSK}}
	called := false
	NetworkHTTPFilter(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		called = true
		p, err := verifySignature(context.Background(), r, "/admin.service.v1.NetworkService/CreateVPC", store, now)
		if kerrors.Code(err) != 401 || p != nil || store.touched != 0 {
			t.Fatal("signature did not bind HTTP method", err)
		}
	})).ServeHTTP(httptest.NewRecorder(), r)
	if !called {
		t.Fatal("signature check did not run")
	}
}

func TestVPCPageHTTPParameters(t *testing.T) {
	op := "/admin.service.v1.NetworkService/ListVPCs"
	for _, query := range []string{"", "page=3", "pageSize=2", "page=3&pageSize=2", "page=4294967295&pageSize=100"} {
		values, err := url.ParseQuery(query)
		if err != nil || !validateNetworkQuery(op, values) {
			t.Fatalf("valid query rejected: %s", query)
		}
	}
	for _, query := range []string{"page=0", "pageSize=0", "page=-1", "pageSize=101", "page=01", "page=+1", "page=1.0", "page=", "pageSize=", "page=4294967296", "page=18446744073709551616", "page=1&page=2", "pageSize=1&pageSize=2", "limit=1", "cursor=x", "page=1&cursor=x", "pageSize=2&limit=1", "offset=0", "page_size=2", "page_token=x", "token=x", "noPaging=true"} {
		values, err := url.ParseQuery(query)
		if err == nil && validateNetworkQuery(op, values) {
			t.Fatalf("invalid query accepted: %s", query)
		}
	}
}

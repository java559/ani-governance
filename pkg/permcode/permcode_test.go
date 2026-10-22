package permcode

import (
	"errors"
	"testing"
)

// TestResolveAuthoritativeCodes 核对两张权威表的代表性命中与取码来源。
func TestResolveAuthoritativeCodes(t *testing.T) {
	cases := []struct {
		method, path string
		wantCode     string
		wantSource   string
	}{
		{"GET", "/api/v1/models/{model_id}/operations", "model:service:read", SourceCapability},
		{"GET", "/api/v1/admin/users", "user:view", SourceManagement},
		{"POST", "/api/v1/admin/users/{user_id}/password", "user:reset-password", SourceManagement},
		{"GET", "/api/v1/admin/dict/entries/by-type-code", "dict:entry:view", SourceManagement},
		{"GET", "/api/v1/admin/networks/vlan-networks", "network:platform:vlan-network:list", SourceCapability},
		{"GET", "/api/v1/networks/vpcs/{vpc_id}", "network:vpc:get", SourceCapability},
		{"POST", "/api/v1/instances/{data.instance_id}:start", "instance:start", SourceCapability},
		{"POST", "/api/v1/instances/{data.instance_id}:stop", "instance:stop", SourceCapability},
		{"GET", "/api/v1/me/quota-accounts", "quota:account:self-read", SourceCapability},
		{"GET", "/api/v1/admin/accelerator/profiles:preview", "accelerator:profile:preview", SourceCapability},
		{"POST", "/api/v1/admin/accelerator/profiles/{profile_id}:set-state", "accelerator:profile:set-state", SourceCapability},
		{"POST", "/api/v1/admin/accelerator/profiles/{profile_id}:delete", "accelerator:profile:delete", SourceCapability},
		{"GET", "/api/v1/admin/accelerator/initialization", "accelerator:initialization:read", SourceManagement},
		{"GET", "/api/v1/admin/accelerator/discovered-nodes", "accelerator:discovery:list", SourceManagement},
		{"POST", "/api/v1/admin/accelerator/initializations", "accelerator:initialization:create", SourceManagement},
		{"GET", "/api/v1/admin/accelerator/initializations/{operation_id}", "accelerator:initialization:read", SourceManagement},
		{"POST", "/api/v1/admin/accelerator/initializations/{operation_id}:retry", "accelerator:initialization:retry", SourceManagement},
	}
	for _, tc := range cases {
		exp, err := Resolve(tc.method, tc.path)
		if err != nil {
			t.Fatalf("Resolve(%s %s) unexpected error: %v", tc.method, tc.path, err)
		}
		if exp.Code != tc.wantCode {
			t.Errorf("Resolve(%s %s) code = %q, want %q", tc.method, tc.path, exp.Code, tc.wantCode)
		}
		if exp.Source != tc.wantSource {
			t.Errorf("Resolve(%s %s) source = %q, want %q", tc.method, tc.path, exp.Source, tc.wantSource)
		}
	}
}

func TestInstanceConfigurationRoutePermissions(t *testing.T) {
	for _, route := range []struct {
		path string
		code string
	}{
		{"/api/v1/instances/mountable-pvcs", "instance:create"},
		{"/api/v1/instances/{instance_id}/configuration", "instance:read"},
	} {
		t.Run(route.path, func(t *testing.T) {
			exp, err := Resolve("GET", route.path)
			if err != nil {
				t.Fatalf("registered instance route must resolve for permissions-align: %v", err)
			}
			if exp.Code != route.code || exp.Source != SourceCapability || exp.BusinessModule != "INSTANCE" || exp.ScopeKind != "tenant" {
				t.Fatalf("instance configuration route must reuse its existing tenant grant: %+v", exp)
			}
		})
	}
}

func TestInstanceMetricsRoutePermissions(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/v1/instances/{instance_id}/metrics/query"},
		{"GET", "/api/v1/instances/{instance_id}/metrics/latest"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			exp, err := Resolve(route.method, route.path)
			if err != nil {
				t.Fatalf("registered metrics route must resolve for permissions-align: %v", err)
			}
			if exp.Code != "instance:read" || exp.Source != SourceCapability || exp.BusinessModule != "INSTANCE" || exp.ScopeKind != "tenant" {
				t.Fatalf("metrics route must reuse the existing tenant read grant: %+v", exp)
			}
		})
	}
}

func TestComputePresetPlatformPermissions(t *testing.T) {
	for _, route := range []struct{ method, path, action string }{
		{"GET", "/api/v1/admin/compute-presets", "list"},
		{"GET", "/api/v1/admin/compute-presets/{id}", "read"},
		{"POST", "/api/v1/admin/compute-presets", "create"},
		{"PATCH", "/api/v1/admin/compute-presets/{id}", "update"},
		{"POST", "/api/v1/admin/compute-presets/{id}:set-enabled", "update"},
		{"DELETE", "/api/v1/admin/compute-presets/{id}", "delete"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			exp, err := Resolve(route.method, route.path)
			if err != nil || exp.Code != "compute:preset:admin:"+route.action || exp.BusinessModule != "SYSTEM" || exp.ScopeKind != "platform" {
				t.Fatalf("compute platform capability: %+v %v", exp, err)
			}
		})
	}
	if _, err := Resolve("GET", "/api/v1/instance-presets"); err == nil {
		t.Fatal("removed route remains in permission table")
	}
	for _, usage := range []string{"CONTAINER_INSTANCE", "TRAINING", "INFERENCE"} {
		module, code, ok := ComputeCatalogGrant(usage)
		if !ok || module == "" || code == "" {
			t.Fatal("missing usage grant", usage)
		}
	}
	if _, _, ok := ComputeCatalogGrant(""); ok {
		t.Fatal("implicit usage grant")
	}

}

// TestResolveMissing 未命中且非豁免时返回 MissingError（对齐入口据此整批拒绝）。
func TestResolveMissing(t *testing.T) {
	_, err := Resolve("GET", "/api/v1/admin/not-registered")
	var me *MissingError
	if !errors.As(err, &me) {
		t.Fatalf("expected MissingError, got %v", err)
	}
}

// TestExempt 独立认证/会话、特殊传输与业务域接口应被豁免，不产生业务码。
func TestExempt(t *testing.T) {
	cases := []struct{ method, path string }{
		{"POST", "/api/v1/auth/password/login"},
		{"GET", "/api/v1/events"},
		{"GET", "/api/v1/modeldev/executions"},
		{"GET", "/api/v1/auth/api-keys"},
	}
	for _, tc := range cases {
		if reason, ok := IsExempt(tc.method, tc.path); !ok || reason == "" {
			t.Errorf("IsExempt(%s %s) = (%q,%v), want exempt", tc.method, tc.path, reason, ok)
		}
		if _, err := Resolve(tc.method, tc.path); err == nil {
			t.Errorf("Resolve(%s %s) should miss (exempt, not a business code)", tc.method, tc.path)
		}
	}
}

// TestNoConflicts 两张权威表对同一键的码必须一致（生成期已校验，此处为运行时自检）。
func TestNoConflicts(t *testing.T) {
	if got := Conflicts(); len(got) != 0 {
		t.Fatalf("unexpected cross-table conflicts: %v", got)
	}
}

// TestCountsAndKeys 表体量与键集合自检，防止生成物与实现脱节。
func TestCountsAndKeys(t *testing.T) {
	if ManagementCount() != 199 {
		t.Errorf("ManagementCount() = %d, want 199", ManagementCount())
	}
	if CapabilityCount() != 140 {
		t.Errorf("CapabilityCount() = %d, want 140", CapabilityCount())
	}
	keys := AllKeys()
	if len(keys) < 262 {
		t.Errorf("AllKeys() = %d, want >= 262", len(keys))
	}
	method, path := SplitKey("GET /api/v1/admin/users")
	if method != "GET" || path != "/api/v1/admin/users" {
		t.Errorf("SplitKey() = (%q,%q)", method, path)
	}
}

func TestModelImportLogsReadPermission(t *testing.T) {
	exp, err := Resolve("GET", "/api/v1/models/import-tasks/{task_id}/logs")
	if err != nil {
		t.Fatal(err)
	}
	if exp.Code != "model:service:read" || exp.Source != SourceCapability || exp.BusinessModule != "MODEL" || exp.ScopeKind != "tenant" {
		t.Fatalf("import logs must reuse the tenant Model read grant: %+v", exp)
	}
}

// TestLookup 命中返回码，未命中 ok=false（聚合层批量取码用）。
func TestLookup(t *testing.T) {
	if code, ok := Lookup("GET", "/api/v1/admin/roles"); !ok || code != "role:view" {
		t.Errorf("Lookup(roles) = (%q,%v), want (role:view,true)", code, ok)
	}
	for _, route := range []struct{ method, path, code string }{
		{"GET", "/api/v1/inference/services", "inference:service:read"},
		{"POST", "/api/v1/inference/services/{resource_id}:delete", "inference:service:delete"},
	} {
		if code, ok := Lookup(route.method, route.path); !ok || code != route.code {
			t.Errorf("Lookup(%s %s) = (%q,%v), want %q", route.method, route.path, code, ok, route.code)
		}
	}
	if _, ok := Lookup("GET", "/api/v1/nope"); ok {
		t.Error("Lookup unregistered route should miss")
	}
}

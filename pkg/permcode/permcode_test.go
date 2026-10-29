package permcode

import (
	"errors"
	"reflect"
	"testing"
)

// TestNormalizePath 路径参数段名不同但结构相同的模板必须归一为同一字符串。
func TestNormalizePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/api/v1/admin/users/{user_id}/password", "/api/v1/admin/users/{}/password"},
		{"/api/v1/admin/users/{userId}/password", "/api/v1/admin/users/{}/password"},
		{"/api/v1/admin/mfa/{credentialId}", "/api/v1/admin/mfa/{}"},
		{"/api/v1/admin/mfa/{credential_id}", "/api/v1/admin/mfa/{}"},
		{"/api/v1/inference/services/{data.resource_id}:start", "/api/v1/inference/services/{}:start"},
		{"/api/v1/inference/services/{resource_id}:start", "/api/v1/inference/services/{}:start"},
		{"/api/v1/admin/users", "/api/v1/admin/users"},
		{"  /api/v1/admin/roles/{id}  ", "/api/v1/admin/roles/{}"},
	}
	for _, tc := range cases {
		if got := NormalizePath(tc.in); got != tc.want {
			t.Errorf("NormalizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestResolveAuthoritativeCodes 核对两张权威表的代表性命中与取码来源。
func TestResolveAuthoritativeCodes(t *testing.T) {
	cases := []struct {
		method, path string
		wantCode     string
		wantSource   string
	}{
		{"GET", "/api/v1/models/{model_id}/operations", "model:service:read", SourceCapability},
		{"GET", "/api/v1/virtual-machine-dns-defaults", "vm:create", SourceCapability},
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
	for _, usage := range []string{"CONTAINER_INSTANCE", "TRAINING", "INFERENCE", "VIRTUAL_MACHINE"} {
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
		{"POST", "/api/v1/modeldev/finetuning/readiness/check"},
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
	// GPU v1.1 adds these 13 reads to the management mapping. Keep their full
	// contracts explicit so an unrelated route cannot satisfy the count change.
	const gpuSource = "GPU monitoring v1.1 read capabilities"
	gpuReads := map[string]expectation{
		"GET /api/v1/admin/gpu/devices":                                          {Operation: "GpuAdminService_ListGpuDevices", Code: "accelerator:device:list", Source: gpuSource, BusinessModule: "ACCELERATOR", ScopeKind: "platform"},
		"GET /api/v1/admin/gpu/devices/{device_id}/metrics/latest":               {Operation: "GpuAdminService_GetPhysicalGpuLatest", Code: "accelerator:device:list", Source: gpuSource, BusinessModule: "ACCELERATOR", ScopeKind: "platform"},
		"GET /api/v1/admin/gpu/metrics/latest":                                   {Operation: "GpuAdminService_GetPlatformGpuLatest", Code: "accelerator:device:list", Source: gpuSource, BusinessModule: "ACCELERATOR", ScopeKind: "platform"},
		"GET /api/v1/admin/gpu/tenants":                                          {Operation: "GpuTenantAdminService_ListGpuTenants", Code: "tenant:quota-read", Source: gpuSource, BusinessModule: "ACCELERATOR", ScopeKind: "platform"},
		"GET /api/v1/admin/gpu/tenants/{tenant_id}/metrics/latest":               {Operation: "GpuAdminService_GetTenantGpuLatest", Code: "tenant:quota-read", Source: gpuSource, BusinessModule: "ACCELERATOR", ScopeKind: "platform"},
		"GET /api/v1/admin/tenants/quota-account-summaries":                      {Operation: "QuotaAdminService_ListTenantQuotaAccountSummaries", Code: "tenant:quota-read", Source: gpuSource, BusinessModule: "TENANT", ScopeKind: "platform"},
		"GET /api/v1/gpu/metrics/latest":                                         {Operation: "GpuService_GetMyGpuLatest", Code: "accelerator:usage:list", Source: gpuSource, BusinessModule: "ACCELERATOR", ScopeKind: "tenant"},
		"GET /api/v1/gpu/resources/{resource_kind}/{resource_id}/metrics/latest": {Operation: "GpuService_GetResourceGpuLatest", Code: "accelerator:usage:get", Source: gpuSource, BusinessModule: "ACCELERATOR", ScopeKind: "tenant"},
		"POST /api/v1/admin/gpu/devices/{device_id}/metrics/query":               {Operation: "GpuAdminService_QueryPhysicalGpu", Code: "accelerator:device:list", Source: gpuSource, BusinessModule: "ACCELERATOR", ScopeKind: "platform"},
		"POST /api/v1/admin/gpu/metrics/query":                                   {Operation: "GpuAdminService_QueryPlatformGpu", Code: "accelerator:device:list", Source: gpuSource, BusinessModule: "ACCELERATOR", ScopeKind: "platform"},
		"POST /api/v1/admin/gpu/tenants/{tenant_id}/metrics/query":               {Operation: "GpuAdminService_QueryTenantGpu", Code: "tenant:quota-read", Source: gpuSource, BusinessModule: "ACCELERATOR", ScopeKind: "platform"},
		"POST /api/v1/gpu/metrics/query":                                         {Operation: "GpuService_QueryMyGpu", Code: "accelerator:usage:list", Source: gpuSource, BusinessModule: "ACCELERATOR", ScopeKind: "tenant"},
		"POST /api/v1/gpu/resources/{resource_kind}/{resource_id}/metrics/query": {Operation: "GpuService_QueryResourceGpu", Code: "accelerator:usage:get", Source: gpuSource, BusinessModule: "ACCELERATOR", ScopeKind: "tenant"},
	}
	// These three capabilities arrived with the retained platform EIP mainline.
	const eipSource = "sql/data/20261027_platform_eip_permissions.sql ; pkg/middleware/auth/platform_network_policy.go"
	platformEIPReadsAndRelease := map[string]expectation{
		"GET /api/v1/admin/networks/eips":             {Code: "network:platform:eip:list", Source: eipSource, BusinessModule: "NETWORK", ScopeKind: "platform"},
		"GET /api/v1/admin/networks/eips/{eip_id}":    {Code: "network:platform:eip:get", Source: eipSource, BusinessModule: "NETWORK", ScopeKind: "platform"},
		"DELETE /api/v1/admin/networks/eips/{eip_id}": {Code: "network:platform:eip:release", Source: eipSource, BusinessModule: "NETWORK", ScopeKind: "platform"},
	}
	// 首页资源趋势与首页资源统计卡片（租户级只读，挂 INSTANCE 模块）。
	const homepageSource = "scripts/ops/sql/register-observability-permissions.sql ; scripts/ops/sql/register-overview-permissions.sql"
	homepageReads := map[string]expectation{
		"GET /api/v1/observability/resource-trend": {Code: "observability:resource-trend:read", Source: homepageSource, BusinessModule: "INSTANCE", ScopeKind: "tenant"},
		"GET /api/v1/overview":                     {Code: "overview:resource-count:read", Source: homepageSource, BusinessModule: "INSTANCE", ScopeKind: "tenant"},
	}
	// 平台组件健康快照（BOSS 组件状态页）：平台级只读，挂 SYSTEM 模块。
	const platformComponentSource = "scripts/ops/sql/register-platform-component-permissions.sql"
	platformComponentReads := map[string]expectation{
		"GET /api/v1/platform/components": {Code: "component:status:read", Source: platformComponentSource, BusinessModule: "SYSTEM", ScopeKind: "platform"},
	}
	// 平台组件日志流（BOSS 组件日志窗口）：独立能力码，与状态快照互不替代。
	const platformComponentLogSource = "scripts/ops/sql/register-platform-component-log-permissions.sql"
	platformComponentLogStreams := map[string]expectation{
		"GET /api/v1/platform/components/{component}/logs/stream": {Code: "component:log:read", Source: platformComponentLogSource, BusinessModule: "SYSTEM", ScopeKind: "platform"},
	}
	// 容器实例重启与 PVC 挂载/卸载（dev 侧随 70807c81 引入 4 条能力）。
	// 与 instance 其余能力同源，无法按 source 整表对拍，故逐键校验。
	const instanceRestartPVCSource = "scripts/ops/sql/register-instance-permissions.sql"
	instanceRestartPVCReads := map[string]expectation{
		"POST /api/v1/instances/{data.instance_id}:restart":    {Code: "instance:restart", Source: instanceRestartPVCSource, BusinessModule: "INSTANCE", ScopeKind: "tenant"},
		"POST /api/v1/instances/{data.instance_id}:attach-pvc": {Code: "instance:pvc:attach", Source: instanceRestartPVCSource, BusinessModule: "INSTANCE", ScopeKind: "tenant"},
		"POST /api/v1/instances/{data.instance_id}:detach-pvc": {Code: "instance:pvc:detach", Source: instanceRestartPVCSource, BusinessModule: "INSTANCE", ScopeKind: "tenant"},
		"GET /api/v1/instances/{instance_id}/mountable-pvcs":   {Code: "instance:pvc:attach", Source: instanceRestartPVCSource, BusinessModule: "INSTANCE", ScopeKind: "tenant"},
	}
	const storageWriteSource = "sql/data/20261029_storage_write_permissions.sql"
	storageWrites := map[string]expectation{
		"POST /api/v1/storage/buckets":                                     {Code: "storage:bucket:create", Source: storageWriteSource, BusinessModule: "STORAGE", ScopeKind: "tenant"},
		"POST /api/v1/storage/buckets/{data.bucket_id}/objects:upload-url": {Code: "storage:object:upload", Source: storageWriteSource, BusinessModule: "STORAGE", ScopeKind: "tenant"},
		"POST /api/v1/storage/buckets/{data.bucket_id}/objects:delete":     {Code: "storage:object:delete", Source: storageWriteSource, BusinessModule: "STORAGE", ScopeKind: "tenant"},
	}
	if want := 227; ManagementCount() != want {
		t.Errorf("ManagementCount() = %d, want %d", ManagementCount(), want)
	}
	if want := 204 + len(platformComponentReads) + len(platformComponentLogStreams) + len(storageWrites); CapabilityCount() != want {
		t.Errorf("CapabilityCount() = %d, want %d", CapabilityCount(), want)
	}
	keys := AllKeys()
	if want := 390 + len(storageWrites); len(keys) != want {
		t.Errorf("AllKeys() = %d, want %d", len(keys), want)
	}
	keySet := make(map[string]bool, len(keys))
	for _, k := range keys {
		keySet[k] = true
	}
	for _, table := range []struct {
		name, source string
		actual, want map[string]expectation
	}{
		{"gpu-read-management", gpuSource, managementOperations, gpuReads},
		{"platform-eip-capability", eipSource, capabilityOperations, platformEIPReadsAndRelease},
		{"storage-write-capability", storageWriteSource, capabilityOperations, storageWrites},
	} {
		t.Run(table.name, func(t *testing.T) {
			actual := make(map[string]expectation)
			for k, v := range table.actual {
				if v.Source == table.source {
					actual[k] = v
				}
			}
			if !reflect.DeepEqual(actual, table.want) {
				t.Errorf("route contracts differ: got %+v, want %+v", actual, table.want)
			}
			for k := range table.want {
				if !keySet[k] {
					t.Errorf("AllKeys() missing %q", k)
				}
			}
		})
	}
	for key, want := range homepageReads {
		got, ok := capabilityOperations[key]
		if !ok || got.Code != want.Code || got.BusinessModule != want.BusinessModule || got.ScopeKind != want.ScopeKind {
			t.Errorf("homepage capability %q = %+v (ok=%v), want %+v", key, got, ok, want)
		}
		if !keySet[key] {
			t.Errorf("AllKeys() missing %q", key)
		}
	}
	for key, want := range platformComponentReads {
		got, ok := capabilityOperations[key]
		if !ok || got.Code != want.Code || got.BusinessModule != want.BusinessModule || got.ScopeKind != want.ScopeKind {
			t.Errorf("platform component capability %q = %+v (ok=%v), want %+v", key, got, ok, want)
		}
		if !keySet[key] {
			t.Errorf("AllKeys() missing %q", key)
		}
	}
	for key, want := range platformComponentLogStreams {
		got, ok := capabilityOperations[key]
		if !ok || got.Code != want.Code || got.BusinessModule != want.BusinessModule || got.ScopeKind != want.ScopeKind || got.Source != want.Source {
			t.Errorf("platform component log capability %q = %+v (ok=%v), want %+v", key, got, ok, want)
		}
		if !keySet[key] {
			t.Errorf("AllKeys() missing %q", key)
		}
	}
	// 容器实例重启与 PVC 能力逐键对拍：码、模块、作用域与来源都必须一致。
	for key, want := range instanceRestartPVCReads {
		got, ok := capabilityOperations[key]
		if !ok || got != want {
			t.Errorf("instance restart/pvc capability %q = %+v (ok=%v), want %+v", key, got, ok, want)
		}
		if !keySet[key] {
			t.Errorf("AllKeys() missing %q", key)
		}
	}
	// 两条组件能力互不替代：能力码不同，且日志流不能被放进 stream 豁免表。
	if platformComponentLogStreams["GET /api/v1/platform/components/{component}/logs/stream"].Code == platformComponentReads["GET /api/v1/platform/components"].Code {
		t.Fatal("component log and status must not share one capability code")
	}
	if _, exempt := exemptReasons["GET /api/v1/platform/components/{component}/logs/stream"]; exempt {
		t.Fatal("component log stream must carry its capability code, not fall into the exempt table")
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
		{"POST", "/api/v1/inference-keys", "inference:key:create"},
		{"GET", "/api/v1/inference-keys", "inference:key:read"},
		{"GET", "/api/v1/inference-keys/{key_id}", "inference:key:read"},
		{"DELETE", "/api/v1/inference-keys/{key_id}", "inference:key:revoke"},
		{"POST", "/api/v1/inference-keys/{key_id}:rotate", "inference:key:rotate"},
	} {
		if code, ok := Lookup(route.method, route.path); !ok || code != route.code {
			t.Errorf("Lookup(%s %s) = (%q,%v), want %q", route.method, route.path, code, ok, route.code)
		}
	}
	if _, ok := Lookup("GET", "/api/v1/nope"); ok {
		t.Error("Lookup unregistered route should miss")
	}
}

func TestResourceUsageReadHasIndependentPlatformPermission(t *testing.T) {
	for _, path := range []string{"/api/v1/admin/resource-usage/overview", "/api/v1/admin/resource-usage/tenants", "/api/v1/admin/resource-usage/tenants/{tenant_id}"} {
		exp, err := Resolve("GET", path)
		if err != nil {
			t.Fatal(err)
		}
		if exp.Code != "resource-usage:read" || exp.BusinessModule != "DASHBOARD" || exp.ScopeKind != "platform" {
			t.Fatalf("unexpected resource usage grant: %+v", exp)
		}
	}
}

func TestSandboxCapabilities(t *testing.T) {
	for _, r := range []struct{ method, path, action string }{
		{"POST", "/api/v1/sandbox", "create"},
		{"GET", "/api/v1/sandbox", "list"},
		{"GET", "/api/v1/sandbox/{sandbox_id}", "read"},
		{"GET", "/api/v1/sandbox/operations/{operation_id}", "operation:read"},
		{"POST", "/api/v1/sandbox/{data.sandbox_id}:pause", "pause"},
		{"POST", "/api/v1/sandbox/{data.sandbox_id}:resume", "resume"},
		{"DELETE", "/api/v1/sandbox/{data.sandbox_id}", "delete"},
	} {
		exp, err := Resolve(r.method, r.path)
		if err != nil || exp.Code != "sandbox:"+r.action || exp.BusinessModule != "INSTANCE" || exp.ScopeKind != "tenant" {
			t.Fatalf("Sandbox capability %s %s: %+v %v", r.method, r.path, exp, err)
		}
	}
	module, code, ok := ComputeCatalogGrant("SANDBOX")
	if !ok || module != "INSTANCE" || code != "sandbox:preset:read" {
		t.Fatalf("Sandbox catalog grant: %s %s %v", module, code, ok)
	}
}

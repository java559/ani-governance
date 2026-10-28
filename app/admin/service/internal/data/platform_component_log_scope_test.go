package data

import (
	"regexp"
	"testing"
)

// TestPlatformComponentLogScopeRegistry 验证 26 条注册项都能构造出检索范围，
// 且范围只来自注册表：namespace、Kind 与 Pod 模式逐条对齐。
func TestPlatformComponentLogScopeRegistry(t *testing.T) {
	if len(platformComponentRegistry) != 26 {
		t.Fatalf("registry size = %d, want 26", len(platformComponentRegistry))
	}
	for _, entry := range platformComponentRegistry {
		scope, ok := PlatformComponentLogScopeFor(entry.Name)
		if !ok {
			t.Fatalf("%s must resolve to a log scope", entry.Name)
		}
		if scope.Namespace != entry.Namespace {
			t.Fatalf("%s namespace = %q, want %q", entry.Name, scope.Namespace, entry.Namespace)
		}
		if scope.Kind != entry.Kind || scope.Component != entry.Name {
			t.Fatalf("%s scope = %+v", entry.Name, scope)
		}
		if scope.PodPattern == "" {
			t.Fatalf("%s pod pattern is empty", entry.Name)
		}
	}
}

// TestPlatformComponentLogPodPatternByKind 覆盖三种工作负载的命名规则。
func TestPlatformComponentLogPodPatternByKind(t *testing.T) {
	for _, tc := range []struct {
		name, kind, want string
	}{
		{"ani-governance", PlatformComponentKindDeployment, `ani-governance-[a-z0-9]{5,10}-[a-z0-9]{5}`},
		{"postgresql", PlatformComponentKindStatefulSet, `postgresql-[0-9]+`},
		{"ani-fluent-bit", PlatformComponentKindDaemonSet, `ani-fluent-bit-[a-z0-9]{5}`},
	} {
		got, ok := platformComponentPodPattern(tc.name, tc.kind)
		if !ok || got != tc.want {
			t.Fatalf("%s/%s = %q,%v want %q", tc.name, tc.kind, got, ok, tc.want)
		}
	}
	if _, ok := platformComponentPodPattern("ani-governance", "Job"); ok {
		t.Fatal("unknown kind must not produce a pod pattern")
	}
}

// TestPlatformComponentLogScopeRejectsUnregistered 验证请求输入不能扩大范围：
// 未注册名字、跨 namespace 写法、Pod 名与索引名都不能变成检索目标。
func TestPlatformComponentLogScopeRejectsUnregistered(t *testing.T) {
	for _, name := range []string{
		"",
		"ani-governance-sandbox-20261027-abcde",
		"ani-governance-64bbb9fdf4-qmk2n",
		"ani-auth-service",
		"ani-logs-2026.10.28",
		"ani-system/ani-governance",
		"ANI-GOVERNANCE",
		"ani_governance",
		"ani-governance*",
		"-ani-governance",
		"ani governance",
		"ani-governance/logs",
	} {
		if _, ok := PlatformComponentLogScopeFor(name); ok {
			t.Fatalf("%q must not resolve to a log scope", name)
		}
	}
}

// TestPlatformComponentLogScopePatternExcludesSandbox 用真实 Pod 名验证命名规则的边界：
// 正式 Pod 命中，sandbox/临时 Deployment 与邻近组件不命中。
//
// OpenSearch 的 Lucene regexp 对 term 是整串匹配（隐含锚点），而 Go 的
// regexp.MatchString 允许子串匹配，因此这里显式加 ^(?:...)$ 才能等价复现线上语义。
func TestPlatformComponentLogScopePatternExcludesSandbox(t *testing.T) {
	for _, tc := range []struct {
		name string
		pod  string
		want bool
	}{
		{"ani-governance", "ani-governance-88dd5cb45-g4j4m", true},
		{"ani-governance", "ani-governance-64bbb9fdf4-qmk2n", true},
		{"ani-governance", "ani-governance-6798c9b7d9-2xk7p", true},
		{"ani-governance", "ani-governance-sandbox-20261027-abcde", false},
		{"ani-governance", "ani-governance-sandbox", false},
		{"ani-governance", "ani-governance", false},
		{"ani-governance", "ani-governance-88dd5cb45", false},
		{"ani-governance", "ani-boss-console-88dd5cb45-g4j4m", false},
		{"ani-governance", "ani-governance2-88dd5cb45-g4j4m", false},
		{"postgresql", "postgresql-0", true},
		{"postgresql", "postgresql-0-1", false},
		{"ani-fluent-bit", "ani-fluent-bit-abcde", true},
		{"ani-fluent-bit", "ani-fluent-bit-abcdef", false},
	} {
		scope, ok := PlatformComponentLogScopeFor(tc.name)
		if !ok {
			t.Fatalf("%s must be registered", tc.name)
		}
		compiled, err := regexp.Compile("^(?:" + scope.PodPattern + ")$")
		if err != nil {
			t.Fatalf("pattern %q: %v", scope.PodPattern, err)
		}
		if matched := compiled.MatchString(tc.pod); matched != tc.want {
			t.Fatalf("%s against %s matched=%v want %v", tc.pod, tc.name, matched, tc.want)
		}
	}
}

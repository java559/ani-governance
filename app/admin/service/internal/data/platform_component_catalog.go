package data

import (
	"regexp"
	"time"
)

// PlatformComponentsPath 是 BOSS「组件状态」页的组件清单路由；入口权限码
// component:status:read，仅平台身份可访问（tenant_id=0 + 平台角色）。
const PlatformComponentsPath = "/api/v1/platform/components"

// PlatformComponentLogStreamPath 是组件日志流的对外路由模板（GET SSE），
// 入口权限码 component:log:read。与状态接口共用同一份受控组件清单。
const PlatformComponentLogStreamPath = "/api/v1/platform/components/{component}/logs/stream"

// PlatformComponentLogStreamOperation 是日志流的 HTTP operation 名，供鉴权
// 中间件按 operation 取权限码，也供手写 handler 写审计痕迹。
const PlatformComponentLogStreamOperation = "/admin.service.v1.PlatformComponentService/StreamComponentLogs"

// 日志接口的固定契约常量：8 秒正常关闭是旧前端重连节奏依赖的常量，
// 刻意不进配置（改动必须与前端同版发布）。
const (
	PlatformComponentLogPermission      = "component:log:read"
	PlatformComponentLogLifetime        = 8 * time.Second
	PlatformComponentLogDefaultLimit    = 100
	PlatformComponentLogMinLimit        = 1
	PlatformComponentLogMaxLimit        = 200
	PlatformComponentLogDefaultInterval = 2
	PlatformComponentLogMinInterval     = 1
	PlatformComponentLogMaxInterval     = 5
	// PlatformComponentLogIgnoredParams 是浏览器/axios 常见的缓存击穿参数，
	// 整体忽略（不校验取值、允许重复），否则前端日志窗口直接打不开。
	PlatformComponentLogIgnoredParams = "_"
)

// platformComponentNamePattern 限定组件名取值域。名字只来自本文件注册表，
// 但仍显式校验：组件名会被拼进 Lucene regexp，必须确保没有元字符。
var platformComponentNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// PlatformComponentLogScope 是一个组件对应的日志检索范围。
// 只由注册表派生，不接受任何请求输入。
type PlatformComponentLogScope struct {
	Component string
	Namespace string
	Kind      string
	// PodPattern 是 Lucene regexp（整词匹配，无 ^/$ 锚点），
	// 按工作负载类型匹配 Pod 名，用于把 sandbox/临时 Deployment 排除在外。
	PodPattern string
}

// PlatformComponentLogScopeFor 按组件名构造检索范围。
// 未注册的组件返回 false，调用方据此返回 404。
func PlatformComponentLogScopeFor(name string) (PlatformComponentLogScope, bool) {
	if !platformComponentNamePattern.MatchString(name) {
		return PlatformComponentLogScope{}, false
	}
	for _, entry := range platformComponentRegistry {
		if entry.Name != name {
			continue
		}
		pattern, ok := platformComponentPodPattern(entry.Name, entry.Kind)
		if !ok {
			return PlatformComponentLogScope{}, false
		}
		return PlatformComponentLogScope{
			Component:  entry.Name,
			Namespace:  entry.Namespace,
			Kind:       entry.Kind,
			PodPattern: pattern,
		}, true
	}
	return PlatformComponentLogScope{}, false
}

// platformComponentPodPattern 按 Kind 生成 Pod 名匹配规则。
//   - Deployment：<name>-<ReplicaSet 后缀 5~10 位>-<Pod 后缀 5 位>
//   - StatefulSet：<name>-<序号>
//   - DaemonSet：<name>-<Pod 后缀 5 位>
//
// 不用 `prefix=name-` 这类宽松匹配：`ani-governance-sandbox-...` 同样满足前缀。
func platformComponentPodPattern(name, kind string) (string, bool) {
	switch kind {
	case PlatformComponentKindDeployment:
		return name + `-[a-z0-9]{5,10}-[a-z0-9]{5}`, true
	case PlatformComponentKindStatefulSet:
		return name + `-[0-9]+`, true
	case PlatformComponentKindDaemonSet:
		return name + `-[a-z0-9]{5}`, true
	default:
		return "", false
	}
}

// 组件分组名，顺序即响应中 groups[] 的顺序（契约固定三组、固定顺序）。
const (
	PlatformComponentGroupService    = "service"
	PlatformComponentGroupDependency = "dependency"
	PlatformComponentGroupPlatform   = "platform"
)

// 工作负载类型。
const (
	PlatformComponentKindDeployment  = "Deployment"
	PlatformComponentKindStatefulSet = "StatefulSet"
	PlatformComponentKindDaemonSet   = "DaemonSet"
)

// 组件状态四值（对齐旧版 ports/component_status.go，无 pending、无 missing）。
const (
	PlatformComponentStatusRunning  = "running"
	PlatformComponentStatusDegraded = "degraded"
	PlatformComponentStatusStopped  = "stopped"
	PlatformComponentStatusUnknown  = "unknown"
)

// 抓取可达性三值。
const (
	PlatformComponentScrapeReachable   = "reachable"
	PlatformComponentScrapeUnreachable = "unreachable"
	PlatformComponentScrapeUnknown     = "unknown"
)

// PlatformComponentGroups 固定三组的顺序，不可增删。
var PlatformComponentGroups = []string{
	PlatformComponentGroupService,
	PlatformComponentGroupDependency,
	PlatformComponentGroupPlatform,
}

// platformComponentRegistration 是静态清单的一行。
// metricsServiceName 只对 service 组有意义：它是 Prometheus `up` 序列上
// service label 的值（= 提供 /metrics 的那个 Service 名），不一定等于组件名
// （例如 ani-inference-service 的指标在 ani-inference-admin 上）。
type platformComponentRegistration struct {
	Name               string
	Kind               string
	Namespace          string
	Group              string
	MetricsServiceName string
}

// platformComponentRegistry 是 2026-10-28 在 kcn 新环境实测得到的平台组件清单。
//
// 与旧版 22 条完全不同（旧清单里的服务在新环境基本不存在）。刻意排除：
//   - 沙箱/临时 Deployment（ani-governance-sandbox-*、ani-resource-sandbox-*）
//   - 租户推理实例（image-registry-*-predictor-*、instance-*）
//   - K8s/CNI/存储基础设施（kcn-cni-ds、kube-proxy、volcano-*、rook-ceph 等）
var platformComponentRegistry = []platformComponentRegistration{
	// service：业务微服务（7）
	{Name: "ani-accelerator-service", Kind: PlatformComponentKindDeployment, Namespace: "ani-system", Group: PlatformComponentGroupService, MetricsServiceName: "ani-accelerator-service"},
	{Name: "ani-inference-service", Kind: PlatformComponentKindDeployment, Namespace: "ani-system", Group: PlatformComponentGroupService, MetricsServiceName: "ani-inference-admin"},
	{Name: "ani-model-service", Kind: PlatformComponentKindDeployment, Namespace: "ani-system", Group: PlatformComponentGroupService, MetricsServiceName: "ani-model-admin"},
	{Name: "ani-modeldev", Kind: PlatformComponentKindDeployment, Namespace: "ani-system", Group: PlatformComponentGroupService, MetricsServiceName: "ani-modeldev"},
	{Name: "ani-observability-service", Kind: PlatformComponentKindDeployment, Namespace: "ani-system", Group: PlatformComponentGroupService, MetricsServiceName: "ani-observability-service"},
	{Name: "ani-resource-service", Kind: PlatformComponentKindDeployment, Namespace: "ani-system", Group: PlatformComponentGroupService, MetricsServiceName: "ani-resource-service"},
	{Name: "ani-session-gateway", Kind: PlatformComponentKindDeployment, Namespace: "ani-system", Group: PlatformComponentGroupService, MetricsServiceName: "ani-session-gateway-websocket"},

	// dependency：中间件与存储（6）
	{Name: "postgresql", Kind: PlatformComponentKindStatefulSet, Namespace: "ani-platform", Group: PlatformComponentGroupDependency},
	{Name: "valkey", Kind: PlatformComponentKindStatefulSet, Namespace: "ani-platform", Group: PlatformComponentGroupDependency},
	{Name: "nats", Kind: PlatformComponentKindStatefulSet, Namespace: "ani-platform", Group: PlatformComponentGroupDependency},
	{Name: "ani-milvus-etcd", Kind: PlatformComponentKindStatefulSet, Namespace: "ani-platform", Group: PlatformComponentGroupDependency},
	{Name: "ani-milvus-standalone", Kind: PlatformComponentKindDeployment, Namespace: "ani-platform", Group: PlatformComponentGroupDependency},
	{Name: "ani-rustfs", Kind: PlatformComponentKindDeployment, Namespace: "ani-platform", Group: PlatformComponentGroupDependency},

	// platform：控制面 / 前端 / 观测（13，含 4 个 DaemonSet）
	{Name: "ani-governance", Kind: PlatformComponentKindDeployment, Namespace: "ani-system", Group: PlatformComponentGroupPlatform},
	{Name: "ani-console", Kind: PlatformComponentKindDeployment, Namespace: "ani-system", Group: PlatformComponentGroupPlatform},
	{Name: "ani-boss-console", Kind: PlatformComponentKindDeployment, Namespace: "ani-system", Group: PlatformComponentGroupPlatform},
	{Name: "ani-modeldev-webhook", Kind: PlatformComponentKindDeployment, Namespace: "ani-system", Group: PlatformComponentGroupPlatform},
	{Name: "ani-metrics-kube-state-metrics", Kind: PlatformComponentKindDeployment, Namespace: "ani-observability", Group: PlatformComponentGroupPlatform},
	{Name: "ani-metrics-operator", Kind: PlatformComponentKindDeployment, Namespace: "ani-observability", Group: PlatformComponentGroupPlatform},
	{Name: "prometheus-ani-metrics-prometheus", Kind: PlatformComponentKindStatefulSet, Namespace: "ani-observability", Group: PlatformComponentGroupPlatform},
	{Name: "alertmanager-ani-metrics-alertmanager", Kind: PlatformComponentKindStatefulSet, Namespace: "ani-observability", Group: PlatformComponentGroupPlatform},
	{Name: "ani-opensearch-master", Kind: PlatformComponentKindStatefulSet, Namespace: "ani-observability", Group: PlatformComponentGroupPlatform},
	{Name: "ani-node-agent", Kind: PlatformComponentKindDaemonSet, Namespace: "ani-system", Group: PlatformComponentGroupPlatform},
	{Name: "ani-fluent-bit", Kind: PlatformComponentKindDaemonSet, Namespace: "ani-observability", Group: PlatformComponentGroupPlatform},
	{Name: "ani-gpu-nvml-v11", Kind: PlatformComponentKindDaemonSet, Namespace: "ani-observability", Group: PlatformComponentGroupPlatform},
	{Name: "ani-metrics-prometheus-node-exporter", Kind: PlatformComponentKindDaemonSet, Namespace: "ani-observability", Group: PlatformComponentGroupPlatform},
}

// PlatformComponentMetricsServiceName 返回组件对应的 metrics Service 名
// （= Prometheus `up` 序列上的 service label 值）。非 service 组返回空串。
func PlatformComponentMetricsServiceName(name string) string {
	for _, entry := range platformComponentRegistry {
		if entry.Name == name {
			return entry.MetricsServiceName
		}
	}
	return ""
}

// platformComponentNamespaces 从清单派生读取范围，避免硬编码第二份 namespace 列表。
func platformComponentNamespaces() []string {
	seen := make(map[string]bool, 3)
	namespaces := make([]string, 0, 3)
	for _, entry := range platformComponentRegistry {
		if seen[entry.Namespace] {
			continue
		}
		seen[entry.Namespace] = true
		namespaces = append(namespaces, entry.Namespace)
	}
	return namespaces
}

// PlatformComponentState 是单个组件的归一化状态；指针字段为 nil 表示
// 该值不可得（HTTP 层输出 null，不是 0 或空串）。
type PlatformComponentState struct {
	Name            string
	Kind            string
	Namespace       string
	Group           string
	Status          string
	DesiredReplicas *int32
	ReadyReplicas   *int32
	Version         *string
	ScrapeStatus    *string
	Reason          *string
}

// PlatformComponentSnapshot 是一次完整观测结果。
type PlatformComponentSnapshot struct {
	ObservedAt   time.Time
	Components   []PlatformComponentState
	RealProvider bool
	Reason       string
}

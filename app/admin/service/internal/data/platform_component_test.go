package data

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func derefString(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

// newPlatformComponentClusterForTest 供测试注入 fake clientset；生产装配
// 只用 NewPlatformComponentClusterFromEnv，该构造函数不进入生产源码。
func newPlatformComponentClusterForTest(client kubernetes.Interface, ttl time.Duration) *PlatformComponentCluster {
	if ttl <= 0 {
		ttl = platformComponentDefaultTTL
	}
	return &PlatformComponentCluster{client: client, ttl: ttl, now: time.Now}
}

func deployment(namespace, name string, replicas int32, ready int32, image string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: image}}}},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: ready, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue}}},
	}
}

func statefulSet(namespace, name string, replicas int32, ready int32, image string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: image}}}},
		},
		Status: appsv1.StatefulSetStatus{ReadyReplicas: ready},
	}
}

func daemonSet(namespace, name string, desired int32, ready int32, image string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: image}}}}},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: desired, NumberReady: ready},
	}
}

// TestCollectRegistryMatchesCluster 全清单对拍：26 条注册项都能在
// fake 集群里找到并得到预期 status/replicas/version。
func TestCollectRegistryMatchesCluster(t *testing.T) {
	cluster := newPlatformComponentClusterForTest(k8sfake.NewSimpleClientset(
		// service 组 7 个（inference/model 的 metrics 在另一个 Service，与状态无关）
		deployment("ani-system", "ani-accelerator-service", 1, 1, "10.10.1.66:30003/a@sha256:4f45136cc6b4d4625dc8476526228a4fec41cdd81a54344434560fc5cd30c1de"),
		deployment("ani-system", "ani-inference-service", 1, 1, "10.10.1.66:30003/a:scope-20261028"),
		deployment("ani-system", "ani-model-service", 2, 2, "10.10.1.66:30003/a:tag1"),
		deployment("ani-system", "ani-modeldev", 1, 1, "10.10.1.66:30003/a:tag2"),
		deployment("ani-system", "ani-observability-service", 1, 1, "10.10.1.66:30003/a:tag3"),
		deployment("ani-system", "ani-resource-service", 1, 1, "10.10.1.66:30003/a:tag4"),
		deployment("ani-system", "ani-session-gateway", 1, 1, "10.10.1.66:30003/a:tag5"),
		// dependency 组 6 个
		statefulSet("ani-platform", "postgresql", 1, 1, "postgres:17.11-bookworm"),
		statefulSet("ani-platform", "valkey", 1, 1, "valkey:8"),
		statefulSet("ani-platform", "nats", 1, 1, "nats:2"),
		statefulSet("ani-platform", "ani-milvus-etcd", 1, 1, "etcd:3"),
		deployment("ani-platform", "ani-milvus-standalone", 1, 1, "milvus:2"),
		deployment("ani-platform", "ani-rustfs", 1, 1, "rustfs:1"),
		// platform 组
		deployment("ani-system", "ani-governance", 1, 1, "10.10.1.66:30003/g@sha256:e5f60a8f3a99e0d1c2b3a4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f70819"),
		deployment("ani-system", "ani-console", 1, 1, "console:202610271606"),
		deployment("ani-system", "ani-boss-console", 1, 1, "console:202610271606"),
		deployment("ani-system", "ani-modeldev-webhook", 1, 1, "webhook:1"),
		deployment("ani-observability", "ani-metrics-kube-state-metrics", 1, 1, "ksm:2"),
		deployment("ani-observability", "ani-metrics-operator", 1, 1, "operator:1"),
		statefulSet("ani-observability", "prometheus-ani-metrics-prometheus", 1, 1, "prom:3"),
		statefulSet("ani-observability", "alertmanager-ani-metrics-alertmanager", 1, 1, "am:1"),
		statefulSet("ani-observability", "ani-opensearch-master", 1, 1, "os:2"),
		daemonSet("ani-system", "ani-node-agent", 3, 3, "agent@sha256:"+strings.Repeat("a0", 32)),
		daemonSet("ani-observability", "ani-fluent-bit", 3, 3, "fluent/fluent-bit:5.1.2"),
		daemonSet("ani-observability", "ani-gpu-nvml-v11", 3, 3, "nvidia@sha256:"+strings.Repeat("b8", 32)),
		daemonSet("ani-observability", "ani-metrics-prometheus-node-exporter", 3, 3, "node-exporter:v1.11.1-distroless"),
	), time.Second)

	snapshot, err := cluster.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Components) != len(platformComponentRegistry) {
		t.Fatalf("components = %d, want %d", len(snapshot.Components), len(platformComponentRegistry))
	}
	if !snapshot.RealProvider || snapshot.Reason != "" {
		t.Fatalf("all healthy but dev_profile wrong: %+v", snapshot)
	}
	byName := make(map[string]PlatformComponentState, len(snapshot.Components))
	for _, component := range snapshot.Components {
		byName[component.Name] = component
	}
	cases := []struct {
		name    string
		status  string
		desired int32
		ready   int32
		version string
	}{
		{"ani-accelerator-service", PlatformComponentStatusRunning, 1, 1, "4f45136cc6b4"}, // digest 截 12 位
		{"ani-inference-service", PlatformComponentStatusRunning, 1, 1, "scope-20261028"},
		{"ani-session-gateway", PlatformComponentStatusRunning, 1, 1, "tag5"},
		{"postgresql", PlatformComponentStatusRunning, 1, 1, "17.11-bookworm"},
		{"ani-governance", PlatformComponentStatusRunning, 1, 1, "e5f60a8f3a99"}, // 另一个 digest 截断
		{"ani-console", PlatformComponentStatusRunning, 1, 1, "202610271606"},
		{"ani-fluent-bit", PlatformComponentStatusRunning, 3, 3, "5.1.2"}, // DaemonSet
		{"ani-node-agent", PlatformComponentStatusRunning, 3, 3, "a0a0a0a0a0a0"},
	}
	for _, tc := range cases {
		component, ok := byName[tc.name]
		if !ok {
			t.Fatalf("component %q missing", tc.name)
		}
		if component.Status != tc.status {
			t.Errorf("%s status = %q, want %q", tc.name, component.Status, tc.status)
		}
		if component.DesiredReplicas == nil || *component.DesiredReplicas != tc.desired || component.ReadyReplicas == nil || *component.ReadyReplicas != tc.ready {
			t.Errorf("%s replicas = %v/%v, want %d/%d", tc.name, component.DesiredReplicas, component.ReadyReplicas, tc.desired, tc.ready)
		}
		if component.Version == nil || *component.Version != tc.version {
			t.Errorf("%s version = %s, want %q", tc.name, derefString(component.Version), tc.version)
		}
		if component.ScrapeStatus != nil {
			t.Errorf("%s scrape_status must be nil from data layer, got %v", tc.name, component.ScrapeStatus)
		}
	}
}

// TestCollectDegradedAndMissing 降级三态：0 副本 → stopped；副本不足 →
// degraded；对象缺失 → unknown + reason 且 replicas/version 为 nil。
func TestCollectDegradedAndMissing(t *testing.T) {
	cluster := newPlatformComponentClusterForTest(k8sfake.NewSimpleClientset(
		deployment("ani-system", "ani-accelerator-service", 0, 0, "a:1"),
		deployment("ani-system", "ani-inference-service", 2, 1, "a:2"),
		// ani-model-service 整个缺失
		statefulSet("ani-platform", "postgresql", 1, 0, "postgres:17"), // degraded
	), time.Second)

	snapshot, err := cluster.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RealProvider {
		t.Fatal("degraded snapshot must set real_provider=false")
	}
	byName := make(map[string]PlatformComponentState)
	for _, component := range snapshot.Components {
		byName[component.Name] = component
	}
	if got := byName["ani-accelerator-service"]; got.Status != PlatformComponentStatusStopped {
		t.Errorf("zero replicas status = %q, want stopped", got.Status)
	}
	if got := byName["ani-inference-service"]; got.Status != PlatformComponentStatusDegraded {
		t.Errorf("partial ready status = %q, want degraded", got.Status)
	}
	got := byName["ani-model-service"]
	if got.Status != PlatformComponentStatusUnknown || got.Reason == nil || *got.Reason != "workload not found" {
		t.Fatalf("missing object = %+v, want unknown/workload not found", got)
	}
	if got.DesiredReplicas != nil || got.ReadyReplicas != nil || got.Version != nil {
		t.Fatalf("unknown component must omit replicas/version: %+v", got)
	}
	if got.ScrapeStatus != nil {
		t.Fatalf("data layer must not fill scrape_status: %+v", got)
	}
}

// TestCollectNamespaceFailure 只降级该 namespace，整体仍 200 快照。
func TestCollectNamespaceFailure(t *testing.T) {
	client := k8sfake.NewSimpleClientset(
		deployment("ani-system", "ani-accelerator-service", 1, 1, "a:1"),
	)
	// 仅让 ani-observability 的 List 失败（reactor 按 namespace 拦截）。
	client.PrependReactor("list", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "ani-observability" {
			return true, nil, errors.New("boom")
		}
		return false, nil, nil
	})
	cluster := newPlatformComponentClusterForTest(client, time.Second)

	snapshot, err := cluster.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]PlatformComponentState)
	for _, component := range snapshot.Components {
		byName[component.Name] = component
	}
	if got := byName["ani-accelerator-service"]; got.Status != PlatformComponentStatusRunning {
		t.Errorf("healthy namespace must stay running, got %q", got.Status)
	}
	if got := byName["ani-metrics-operator"]; got.Status != PlatformComponentStatusUnknown || got.Reason == nil {
		t.Fatalf("failed namespace must degrade to unknown+reason, got %+v", got)
	}
	if snapshot.RealProvider {
		t.Error("failed namespace must set real_provider=false")
	}
}

// TestComponentImageVersion digest 截断规则的四个对照样（实施方案 §4.3）。
func TestComponentImageVersion(t *testing.T) {
	cases := []struct {
		image, want string
	}{
		{"10.10.1.66:30003/ani-gpu-platform/ani-governance@sha256:4f45136cc6b4d4625dc8476526228a4fec41cdd81a54344434560fc5cd30c1de", "4f45136cc6b4"},
		{"docker.changqingyun.cn/ani/ani-console:202610271606", "202610271606"},
		{"10.10.1.66:5001/library/postgres:17.11-bookworm", "17.11-bookworm"},
		{"10.10.1.66:5001/fluent/fluent-bit:5.1.2", "5.1.2"},
		{"bare-image", ""}, // 无 ':' → 空
	}
	for _, tc := range cases {
		if got := componentImageVersion([]corev1.Container{{Image: tc.image}}); got != tc.want {
			t.Errorf("version(%q) = %q, want %q", tc.image, got, tc.want)
		}
	}
}

// TestScrapeStatuses 三值判定：无序列 → unknown；有序列全挂 → unreachable；
// 有 up=1 → reachable。Prometheus 响应形状按 /api/v1/query 实际返回构造。
func TestScrapeStatuses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.Context().Err(); err != nil {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"service":"ani-accelerator-service","namespace":"ani-system"},"value":[1769000000,"1"]},
			{"metric":{"service":"ani-inference-admin","namespace":"ani-system"},"value":[1769000000,"0"]}
		]}}`))
	}))
	defer server.Close()

	reader := NewPlatformComponentScrapeReader(PlatformComponentScrapeConfig{Endpoint: server.URL})
	statuses, err := reader.ReadScrapeStatuses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if statuses["ani-accelerator-service"] != PlatformComponentScrapeReachable {
		t.Errorf("up=1 → reachable, got %q", statuses["ani-accelerator-service"])
	}
	if statuses["ani-inference-admin"] != PlatformComponentScrapeUnreachable {
		t.Errorf("up=0 → unreachable, got %q", statuses["ani-inference-admin"])
	}
	if statuses["ani-resource-service"] != PlatformComponentScrapeUnknown {
		t.Errorf("no series → unknown, got %q", statuses["ani-resource-service"])
	}
}

// TestScrapeReaderNotConfigured 未配置 URL 时 reader 为 nil，调用返回错误。
func TestScrapeReaderNotConfigured(t *testing.T) {
	config, err := PlatformComponentScrapeConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Endpoint != "" {
		t.Fatalf("empty env must yield empty endpoint, got %q", config.Endpoint)
	}
	if NewPlatformComponentScrapeReader(config) != nil {
		t.Fatal("empty config must produce nil reader")
	}
	if _, err := (*PlatformComponentScrapeReader)(nil).ReadScrapeStatuses(context.Background()); err == nil {
		t.Fatal("nil reader must error")
	}
}

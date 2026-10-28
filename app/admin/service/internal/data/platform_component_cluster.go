package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// platformComponentDefaultTTL 对齐旧版 componentStatusDefaultTTL：组件页 5s 轮询
	// 不应每次打到 apiserver。
	platformComponentDefaultTTL = 15 * time.Second
	// platformComponentListTimeout 是一次快照的总预算（3 namespace × 3 资源并行）。
	platformComponentListTimeout    = 5 * time.Second
	platformComponentProviderName   = "kubernetes-component-status"
	platformComponentDegradedPrefix = "degraded components: "
)

// ErrPlatformComponentUnsupported 表示 Kubernetes 客户端未配置；
// 接口层据此返回 500（对齐旧版，不是 200 全 unknown）。
var ErrPlatformComponentUnsupported = errors.New("platform component status unsupported")

// PlatformComponentCluster 只读平台工作负载（Deployment / StatefulSet / DaemonSet）。
// 与 InstanceSchedulingCluster 分开：后者带 writeReady 语义，读写混用会让职责不清。
type PlatformComponentCluster struct {
	client kubernetes.Interface
	ttl    time.Duration
	now    func() time.Time

	mu       sync.Mutex
	cached   *PlatformComponentSnapshot
	cachedAt time.Time
}

// NewPlatformComponentClusterFromEnv 从环境变量构造只读客户端。
//   - ANI_INSTANCE_KUBECONFIG 存在：用它构造；
//   - 否则 KUBERNETES_SERVICE_HOST 存在：用 InClusterConfig；
//   - 都没有：client 为 nil，接口返回 500（显式的"未部署"语义，不是错误配置）；
//   - 显式配置但无效：返回 error，启动失败。
func NewPlatformComponentClusterFromEnv() (*PlatformComponentCluster, error) {
	c := &PlatformComponentCluster{ttl: platformComponentDefaultTTL, now: time.Now}
	var config *rest.Config
	var err error
	if path := strings.TrimSpace(os.Getenv("ANI_INSTANCE_KUBECONFIG")); path != "" {
		config, err = clientcmd.BuildConfigFromFlags("", path)
	} else if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		config, err = rest.InClusterConfig()
	} else {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("platform component cluster configuration: %w", err)
	}
	config.Timeout = platformComponentListTimeout
	c.client, err = kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("platform component cluster client: %w", err)
	}
	return c, nil
}

// newPlatformComponentClusterForTest 供测试注入 fake clientset；生产装配
// 只用 NewPlatformComponentClusterFromEnv。
func newPlatformComponentClusterForTest(client kubernetes.Interface, ttl time.Duration) *PlatformComponentCluster {
	if ttl <= 0 {
		ttl = platformComponentDefaultTTL
	}
	return &PlatformComponentCluster{client: client, ttl: ttl, now: time.Now}
}

// Collect 返回组件状态快照（TTL 内复用上次结果）。
// client 为 nil 时返回 ErrPlatformComponentUnsupported，调用方应映射为 500。
func (c *PlatformComponentCluster) Collect(ctx context.Context) (PlatformComponentSnapshot, error) {
	if c == nil || c.client == nil {
		return PlatformComponentSnapshot{}, fmt.Errorf("%w: kubernetes client not configured", ErrPlatformComponentUnsupported)
	}
	c.mu.Lock()
	if c.cached != nil && c.now().Sub(c.cachedAt) < c.ttl {
		snapshot := *c.cached
		c.mu.Unlock()
		return snapshot, nil
	}
	c.mu.Unlock()

	snapshot := c.collect(ctx)

	c.mu.Lock()
	c.cached = &snapshot
	c.cachedAt = c.now()
	c.mu.Unlock()
	return snapshot, nil
}

// workloadIndex 是一次 List 的结果索引：key = namespace/name。
type workloadIndex struct {
	deployments  map[string]*appsv1.Deployment
	statefulSets map[string]*appsv1.StatefulSet
	daemonSets   map[string]*appsv1.DaemonSet
	failures     map[string]string
}

func workloadKey(namespace, name string) string { return namespace + "/" + name }

// workloadShape 是三种工作负载归一后的共同形态，供状态判定复用。
type workloadShape struct {
	desired    int32
	ready      int32
	available  bool
	containers []corev1.Container
}

// collect 执行实际的 List 与归一化。任一 namespace 读取失败只影响该
// namespace 下的组件（降级为 unknown + reason），整体仍返回快照。
func (c *PlatformComponentCluster) collect(ctx context.Context) PlatformComponentSnapshot {
	call, cancel := context.WithTimeout(ctx, platformComponentListTimeout)
	defer cancel()

	index := &workloadIndex{
		deployments:  make(map[string]*appsv1.Deployment),
		statefulSets: make(map[string]*appsv1.StatefulSet),
		daemonSets:   make(map[string]*appsv1.DaemonSet),
		failures:     make(map[string]string),
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, namespace := range platformComponentNamespaces() {
		namespace := namespace
		wg.Add(3)
		go func() {
			defer wg.Done()
			list, err := c.client.AppsV1().Deployments(namespace).List(call, metav1.ListOptions{})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				index.failures[namespace+"/"+PlatformComponentKindDeployment] = err.Error()
				return
			}
			for i := range list.Items {
				item := list.Items[i]
				index.deployments[workloadKey(namespace, item.Name)] = &item
			}
		}()
		go func() {
			defer wg.Done()
			list, err := c.client.AppsV1().StatefulSets(namespace).List(call, metav1.ListOptions{})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				index.failures[namespace+"/"+PlatformComponentKindStatefulSet] = err.Error()
				return
			}
			for i := range list.Items {
				item := list.Items[i]
				index.statefulSets[workloadKey(namespace, item.Name)] = &item
			}
		}()
		go func() {
			defer wg.Done()
			list, err := c.client.AppsV1().DaemonSets(namespace).List(call, metav1.ListOptions{})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				index.failures[namespace+"/"+PlatformComponentKindDaemonSet] = err.Error()
				return
			}
			for i := range list.Items {
				item := list.Items[i]
				index.daemonSets[workloadKey(namespace, item.Name)] = &item
			}
		}()
	}
	wg.Wait()

	components := make([]PlatformComponentState, 0, len(platformComponentRegistry))
	var degraded []string
	for _, entry := range platformComponentRegistry {
		component := index.component(entry)
		if component.Status == PlatformComponentStatusUnknown && component.Reason != nil {
			degraded = append(degraded, fmt.Sprintf("%s/%s: %s", entry.Namespace, entry.Name, *component.Reason))
		}
		components = append(components, component)
	}

	snapshot := PlatformComponentSnapshot{
		ObservedAt:   c.now().UTC(),
		Components:   components,
		RealProvider: true,
	}
	if len(degraded) > 0 {
		snapshot.RealProvider = false
		snapshot.Reason = platformComponentDegradedPrefix + strings.Join(degraded, "; ")
	}
	return snapshot
}

// component 按注册项取出对应对象并归一化；对象缺失或该 namespace/kind
// 读取失败时返回 status=unknown + reason（对齐旧版"404 → unknown"语义）。
func (index *workloadIndex) component(entry platformComponentRegistration) PlatformComponentState {
	component := PlatformComponentState{
		Name:      entry.Name,
		Kind:      entry.Kind,
		Namespace: entry.Namespace,
		Group:     entry.Group,
		Status:    PlatformComponentStatusUnknown,
	}
	if reason, failed := index.failures[entry.Namespace+"/"+entry.Kind]; failed {
		component.Reason = stringPointer("workload list failed: " + reason)
		return component
	}
	key := workloadKey(entry.Namespace, entry.Name)
	switch entry.Kind {
	case PlatformComponentKindDeployment:
		if object, ok := index.deployments[key]; ok {
			return componentFromShape(component, deploymentShape(object))
		}
	case PlatformComponentKindStatefulSet:
		if object, ok := index.statefulSets[key]; ok {
			return componentFromShape(component, statefulSetShape(object))
		}
	case PlatformComponentKindDaemonSet:
		if object, ok := index.daemonSets[key]; ok {
			return componentFromShape(component, daemonSetShape(object))
		}
	}
	component.Reason = stringPointer("workload not found")
	return component
}

func deploymentShape(object *appsv1.Deployment) workloadShape {
	desired := int32(1)
	if object.Spec.Replicas != nil {
		desired = *object.Spec.Replicas
	}
	available := true
	for _, condition := range object.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable {
			available = condition.Status == corev1.ConditionTrue
			break
		}
	}
	return workloadShape{
		desired:    desired,
		ready:      object.Status.ReadyReplicas,
		available:  available,
		containers: object.Spec.Template.Spec.Containers,
	}
}

func statefulSetShape(object *appsv1.StatefulSet) workloadShape {
	desired := int32(1)
	if object.Spec.Replicas != nil {
		desired = *object.Spec.Replicas
	}
	// StatefulSet 的 condition 类型是 DeploymentCondition 之外的一套
	// （ReplicaFailure 等），没有 Available 语义；旧版对非 DaemonSet 的
	// 判定只取 Deployment 的 Available，StatefulSet 缺省按 available=true。
	available := true
	for _, condition := range object.Status.Conditions {
		if condition.Type == "Available" {
			available = condition.Status == corev1.ConditionTrue
			break
		}
	}
	return workloadShape{
		desired:    desired,
		ready:      object.Status.ReadyReplicas,
		available:  available,
		containers: object.Spec.Template.Spec.Containers,
	}
}

func daemonSetShape(object *appsv1.DaemonSet) workloadShape {
	available := true
	for _, condition := range object.Status.Conditions {
		if condition.Type == "Available" {
			available = condition.Status == corev1.ConditionTrue
			break
		}
	}
	return workloadShape{
		desired:    object.Status.DesiredNumberScheduled,
		ready:      object.Status.NumberReady,
		available:  available,
		containers: object.Spec.Template.Spec.Containers,
	}
}

// componentFromShape 把工作负载归一为组件状态，判定顺序与旧版
// componentStatusFromObject 一致：desired==0 → stopped；ready>=desired 且
// available → running；其余（含 ready=0）→ degraded。
func componentFromShape(component PlatformComponentState, shape workloadShape) PlatformComponentState {
	desired, ready := shape.desired, shape.ready
	component.DesiredReplicas = int32Pointer(desired)
	component.ReadyReplicas = int32Pointer(ready)
	if version := componentImageVersion(shape.containers); version != "" {
		component.Version = stringPointer(version)
	}
	if desired == 0 {
		component.Status = PlatformComponentStatusStopped
		return component
	}
	if ready >= desired && shape.available {
		component.Status = PlatformComponentStatusRunning
		return component
	}
	component.Status = PlatformComponentStatusDegraded
	return component
}

// componentImageVersion 取首个容器镜像的 tag 段：先切掉 registry host
// （最后一个 '/' 之前），再取最后一个 ':' 之后段；无 ':' 返回空。
//
// 在此基础上新增一条规则（对本接口是有意偏离旧版的改动）：若结果长度
// 恰为 64 且全为十六进制字符，说明它是 @sha256: 摘要而不是 tag，截取前
// 12 位。判定必须严格，避免误伤真实的长 tag。
func componentImageVersion(containers []corev1.Container) string {
	if len(containers) == 0 {
		return ""
	}
	image := containers[0].Image
	short := image
	if index := strings.LastIndex(image, "/"); index >= 0 {
		short = image[index+1:]
	}
	index := strings.LastIndex(short, ":")
	if index < 0 {
		return ""
	}
	version := short[index+1:]
	if len(version) == 64 && strings.Trim(version, "0123456789abcdefABCDEF") == "" {
		return version[:12]
	}
	return version
}

func stringPointer(value string) *string { return &value }

func int32Pointer(value int32) *int32 { return &value }

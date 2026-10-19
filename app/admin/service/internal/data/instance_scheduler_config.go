package data

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/zhangzhe-ctrl/ani-accelerator-service/pkg/schedulerconfig"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const InstanceSchedulerConfigPath = "/api/v1/admin/scheduler-config"

// These objects are registered by this deployment, never selected by a request.
const instanceSchedulerNamespace = "volcano-system"
const instanceSchedulerConfigMap = "ani-volcano-scheduler-configmap"
const instanceSchedulerConfigKey = "volcano-scheduler.conf"
const instanceSchedulerVersion = "v1.15.2"

type InstanceSchedulingCluster struct {
	client     kubernetes.Interface
	groups     dynamic.Interface
	writeReady bool
}

// A missing cluster credential leaves unrelated CPU requests available. An
// explicitly configured but invalid credential is a startup error.
func NewInstanceSchedulingClusterFromEnv() (*InstanceSchedulingCluster, error) {
	c := &InstanceSchedulingCluster{writeReady: os.Getenv("ANI_INSTANCE_SCHEDULER_BASELINES_ALIGNED") == "true"}
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
		return nil, fmt.Errorf("instance scheduling cluster configuration: %w", err)
	}
	config.Timeout = 15 * time.Second
	c.groups, err = dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("instance scheduling dynamic client: %w", err)
	}
	c.client, err = kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("instance scheduling cluster client: %w", err)
	}
	return c, nil
}
func (c *InstanceSchedulingCluster) ReadSchedulerConfig(ctx context.Context) (string, string, error) {
	if c == nil || c.client == nil {
		return "", "", errors.ServiceUnavailable("SCHEDULER_CONFIG_UNAVAILABLE", "scheduler configuration unavailable")
	}
	cm, err := c.client.CoreV1().ConfigMaps(instanceSchedulerNamespace).Get(ctx, instanceSchedulerConfigMap, metav1.GetOptions{})
	if err != nil {
		return "", "", errors.ServiceUnavailable("SCHEDULER_CONFIG_UNAVAILABLE", "scheduler configuration unavailable").WithCause(err)
	}
	document, ok := cm.Data[instanceSchedulerConfigKey]
	if !ok || cm.ResourceVersion == "" {
		return "", "", errors.ServiceUnavailable("SCHEDULER_CONFIG_UNAVAILABLE", "registered configuration is unavailable")
	}
	return document, cm.ResourceVersion, nil
}
func (c *InstanceSchedulingCluster) WriteSchedulerConfig(ctx context.Context, document, expected string) (string, string, error) {
	if expected == "" || strings.TrimSpace(expected) != expected || len(document) > 1024*1024 {
		return "", "", errors.BadRequest("INVALID_SCHEDULER_CONFIG", "configuration and expected_resource_version required")
	}
	if c == nil || c.client == nil {
		return "", "", errors.ServiceUnavailable("SCHEDULER_CONFIG_UNAVAILABLE", "scheduler configuration unavailable")
	}
	if !c.writeReady {
		return "", "", errors.ServiceUnavailable("SCHEDULER_BASELINE_NOT_ALIGNED", "scheduler baseline migration is required")
	}
	next, err := schedulerconfig.ProtectedDigest(instanceSchedulerVersion, document)
	if err != nil {
		return "", "", errors.BadRequest("INVALID_SCHEDULER_CONFIG", "invalid scheduler configuration").WithCause(err)
	}
	// Read once to preserve metadata and other data, then keep the editor's
	// resourceVersion in the actual Update. Never retry a conflict with latest.
	cm, err := c.client.CoreV1().ConfigMaps(instanceSchedulerNamespace).Get(ctx, instanceSchedulerConfigMap, metav1.GetOptions{})
	if err != nil {
		return "", "", errors.ServiceUnavailable("SCHEDULER_CONFIG_UNAVAILABLE", "scheduler configuration unavailable").WithCause(err)
	}
	if cm.ResourceVersion != expected {
		return "", "", errors.Conflict("SCHEDULER_CONFIG_CONFLICT", "scheduler configuration changed")
	}
	current, ok := cm.Data[instanceSchedulerConfigKey]
	if !ok {
		return "", "", errors.ServiceUnavailable("SCHEDULER_CONFIG_UNAVAILABLE", "registered configuration is unavailable")
	}
	protected, err := schedulerconfig.ProtectedDigest(instanceSchedulerVersion, current)
	if err != nil {
		return "", "", errors.ServiceUnavailable("SCHEDULER_CONFIG_UNAVAILABLE", "current scheduler configuration is invalid").WithCause(err)
	}
	if protected != next {
		return "", "", errors.BadRequest("SCHEDULER_GPU_CONFIG_PROTECTED", "protected scheduler configuration cannot change here")
	}
	copy := cm.DeepCopy()
	copy.ResourceVersion = expected
	copy.Data[instanceSchedulerConfigKey] = document
	updated, err := c.client.CoreV1().ConfigMaps(instanceSchedulerNamespace).Update(ctx, copy, metav1.UpdateOptions{})
	if apierrors.IsConflict(err) {
		return "", "", errors.Conflict("SCHEDULER_CONFIG_CONFLICT", "scheduler configuration changed").WithCause(err)
	}
	if err != nil {
		return "", "", errors.ServiceUnavailable("SCHEDULER_CONFIG_UNAVAILABLE", "scheduler configuration save failed").WithCause(err)
	}
	return updated.Data[instanceSchedulerConfigKey], updated.ResourceVersion, nil
}
func (c *InstanceSchedulingCluster) PriorityClassExists(ctx context.Context, name string) error {
	if c == nil || c.client == nil {
		return errors.ServiceUnavailable("SCHEDULING_OPTION_UNAVAILABLE", "priority class lookup unavailable")
	}
	_, err := c.client.SchedulingV1().PriorityClasses().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return errors.BadRequest("SCHEDULING_OPTION_UNSUPPORTED", "priority class does not exist in the configured cluster").WithCause(err)
	}
	if err != nil {
		return errors.ServiceUnavailable("SCHEDULING_OPTION_UNAVAILABLE", "priority class lookup unavailable").WithCause(err)
	}
	return nil
}

// PriorityClassRuntimeCompatible only reads a fixed Runtime's PodGroup, when
// one is already named. It never changes or creates a group to hide a conflict.
func (c *InstanceSchedulingCluster) PriorityClassRuntimeCompatible(ctx context.Context, namespace, group, name string) error {
	if c == nil || c.groups == nil {
		return errors.ServiceUnavailable("SCHEDULING_OPTION_UNAVAILABLE", "PodGroup lookup unavailable")
	}
	obj, err := c.groups.Resource(schema.GroupVersionResource{Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "podgroups"}).Namespace(namespace).Get(ctx, group, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return errors.BadRequest("SCHEDULING_OPTION_UNSUPPORTED", "fixed PodGroup is unavailable").WithCause(err)
	}
	if err != nil {
		return errors.ServiceUnavailable("SCHEDULING_OPTION_UNAVAILABLE", "PodGroup lookup unavailable").WithCause(err)
	}
	actual, exists, err := unstructured.NestedString(obj.Object, "spec", "priorityClassName")
	if err != nil || !exists || actual != name {
		return errors.BadRequest("SCHEDULING_FIELD_CONFLICT", "fixed PodGroup priority differs from requested priority")
	}
	return nil
}

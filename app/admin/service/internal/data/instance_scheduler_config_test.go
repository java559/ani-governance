package data

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const schedulerTestYAML = "actions: enqueue, allocate, backfill\ntiers:\n- plugins:\n  - name: deviceshare\n    arguments:\n      deviceshare.VGPUEnable: true\n  - name: nodeorder\n    arguments:\n      nodeaffinity.weight: 2\n"

func TestInstanceSchedulerConditionalUpdate(t *testing.T) {
	ctx := context.Background()
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: instanceSchedulerConfigMap, Namespace: instanceSchedulerNamespace, ResourceVersion: "42", Labels: map[string]string{"preserve": "label"}, Annotations: map[string]string{"preserve": "annotation"}}, Data: map[string]string{instanceSchedulerConfigKey: schedulerTestYAML, "other-key": "preserve"}, BinaryData: map[string][]byte{"other-binary": []byte("preserve")}}
	client := fake.NewClientset(cm)
	owner := &InstanceSchedulingCluster{client: client, writeReady: true}
	document, version, err := owner.ReadSchedulerConfig(ctx)
	if err != nil || document != schedulerTestYAML || version != "42" {
		t.Fatalf("read: %q %q %v", document, version, err)
	}
	// A stale editor fails before any Update, without rewriting with current RV.
	if _, _, err = owner.WriteSchedulerConfig(ctx, schedulerTestYAML, "41"); errors.Code(err) != 409 {
		t.Fatalf("stale edit: %v", err)
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "update" {
			t.Fatal("stale request wrote configuration")
		}
	}
	next := "actions: enqueue, allocate, backfill\ntiers:\n- plugins:\n  - name: deviceshare\n    arguments:\n      deviceshare.VGPUEnable: true\n  - name: nodeorder\n    arguments:\n      nodeaffinity.weight: 0\n"
	if _, _, err = owner.WriteSchedulerConfig(ctx, next, "42"); err != nil {
		t.Fatal(err)
	}
	saved, err := client.CoreV1().ConfigMaps(instanceSchedulerNamespace).Get(ctx, instanceSchedulerConfigMap, metav1.GetOptions{})
	if err != nil || saved.ResourceVersion != "42" || saved.Data[instanceSchedulerConfigKey] != next || saved.Data["other-key"] != "preserve" || string(saved.BinaryData["other-binary"]) != "preserve" || saved.Labels["preserve"] != "label" || saved.Annotations["preserve"] != "annotation" {
		t.Fatalf("preservation: %+v %v", saved, err)
	}
	protected := "actions: enqueue, allocate, backfill\ntiers:\n- plugins:\n  - name: deviceshare\n    arguments:\n      deviceshare.VGPUEnable: false\n"
	if _, _, err = owner.WriteSchedulerConfig(ctx, protected, "42"); errors.Reason(err) != "SCHEDULER_GPU_CONFIG_PROTECTED" {
		t.Fatalf("protected mode accepted: %v", err)
	}
	if _, _, err = owner.WriteSchedulerConfig(ctx, "tiers: [", "42"); errors.Code(err) != 400 {
		t.Fatalf("invalid YAML: %v", err)
	}
	if _, _, err = owner.WriteSchedulerConfig(ctx, next, ""); errors.Code(err) != 400 {
		t.Fatalf("missing version: %v", err)
	}
	owner.writeReady = false
	if _, _, err = owner.WriteSchedulerConfig(ctx, next, "42"); errors.Reason(err) != "SCHEDULER_BASELINE_NOT_ALIGNED" {
		t.Fatalf("unaligned write: %v", err)
	}
	if _, _, err = owner.ReadSchedulerConfig(ctx); err != nil {
		t.Fatalf("baseline gate blocked read: %v", err)
	}
}

package data

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestInstanceSchedulerFinalWriteConflictIsNotRetried(t *testing.T) {
	client := fake.NewClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: instanceSchedulerConfigMap, Namespace: instanceSchedulerNamespace, ResourceVersion: "editor-version"}, Data: map[string]string{instanceSchedulerConfigKey: schedulerTestYAML}})
	updates := 0
	client.PrependReactor("update", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		updates++
		cm := action.(ktesting.UpdateAction).GetObject().(*corev1.ConfigMap)
		if cm.ResourceVersion != "editor-version" {
			t.Errorf("substituted version %q", cm.ResourceVersion)
		}
		// Another administrator changed the object between our GET and Update.
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, cm.Name, nil)
	})
	owner := &InstanceSchedulingCluster{client: client, writeReady: true}
	_, _, err := owner.WriteSchedulerConfig(context.Background(), schedulerTestYAML, "editor-version")
	if errors.Code(err) != 409 || updates != 1 {
		t.Fatalf("conditional write retried or concealed conflict: %d %v", updates, err)
	}
	gets := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "get" {
			gets++
		}
	}
	if gets != 1 {
		t.Fatalf("conflict fetched latest to overwrite it: %d GETs", gets)
	}
}

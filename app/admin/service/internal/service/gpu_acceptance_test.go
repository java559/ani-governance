package service

import (
	"context"
	"testing"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/protobuf/proto"
)

type capabilityTestBinding struct {
	owner   string
	actions []string
}

func (b capabilityTestBinding) OwnerService() string { return b.owner }
func (b capabilityTestBinding) Actions() []string    { return b.actions }
func (b capabilityTestBinding) CreateAction() string {
	if b.owner == "ani-inference" {
		return InferenceCreateAction
	}
	return InstanceCreateAction
}
func (b capabilityTestBinding) DeleteAction() string {
	if b.owner == "ani-inference" {
		return InferenceDeleteAction
	}
	return InstanceDeleteAction
}
func (capabilityTestBinding) GpuQuotaCodes() []string { return []string{GpuSharedQuotaCode} }
func (capabilityTestBinding) AuthorizeGpu(context.Context, *auth.Principal, string, string) error {
	return nil
}
func (capabilityTestBinding) ValidateGpuBusiness(context.Context, proto.Message) ([]data.QuotaOccupyItem, error) {
	return nil, nil
}
func (capabilityTestBinding) Dispatch(context.Context, *QuotaDispatchCommand) ([]byte, error) {
	panic("capability inspection must not dispatch")
}

func TestGpuCapabilityRequiresSameOwnerActions(t *testing.T) {
	for _, tc := range []struct {
		name           string
		owned, foreign []string
		enabled        bool
	}{
		{"complete", []string{InstanceCreateAction, InstanceDeleteAction}, nil, true},
		{"missing-delete", []string{InstanceCreateAction}, nil, false},
		{"foreign-delete", []string{InstanceCreateAction}, []string{InstanceDeleteAction}, false},
		{"foreign-create", []string{InstanceDeleteAction}, []string{InstanceCreateAction}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewQuotaAdapterRegistry()
			if err := r.Register(capabilityTestBinding{owner: InstanceOwner, actions: tc.owned}); err != nil {
				t.Fatal(err)
			}
			if len(tc.foreign) > 0 {
				if err := r.Register(capabilityTestBinding{owner: "other-test-owner", actions: tc.foreign}); err != nil {
					t.Fatal(err)
				}
			}
			if got := r.GPUExecutionEnabled(InstanceOwner); got != tc.enabled {
				t.Fatalf("execution enabled=%t, want %t", got, tc.enabled)
			}
			if got := r.EnforcesQuota(GpuSharedQuotaCode); got != tc.enabled {
				t.Fatalf("shared enforcement=%t, want %t", got, tc.enabled)
			}
			if r.EnforcesQuota(GpuPhysicalQuotaCode) || r.EnforcesQuota("unknown") || r.GPUExecutionEnabled("other-test-owner") {
				t.Fatal("unsupported capability enabled")
			}
		})
	}
}

func TestGpuCapabilityEnablesInferenceOwner(t *testing.T) {
	r := NewQuotaAdapterRegistry()
	if err := r.Register(capabilityTestBinding{owner: "ani-inference", actions: []string{InferenceCreateAction, InferenceDeleteAction}}); err != nil {
		t.Fatal(err)
	}
	if !r.GPUExecutionEnabled("ani-inference") || !r.EnforcesQuota(GpuSharedQuotaCode) || r.EnforcesQuota(GpuPhysicalQuotaCode) {
		t.Fatal("inference owner GPU admission was not enabled")
	}
}

type unregisteredCapabilityBinding struct{ capabilityTestBinding }

func (unregisteredCapabilityBinding) CreateAction() string { return "CAPABILITY_TEST_CREATE" }
func (unregisteredCapabilityBinding) DeleteAction() string { return "CAPABILITY_TEST_DELETE" }
func TestGpuCapabilityRejectsUnregisteredActionPair(t *testing.T) {
	r := NewQuotaAdapterRegistry()
	b := unregisteredCapabilityBinding{capabilityTestBinding{owner: InstanceOwner, actions: []string{"CAPABILITY_TEST_CREATE", "CAPABILITY_TEST_DELETE"}}}
	if err := r.Register(b); err != nil {
		t.Fatal(err)
	}
	if r.GPUExecutionEnabled(InstanceOwner) || r.EnforcesQuota(GpuSharedQuotaCode) {
		t.Fatal("an unregistered action pair must not activate managed capability")
	}
}

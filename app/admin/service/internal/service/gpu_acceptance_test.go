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

func (b capabilityTestBinding) OwnerService() string  { return b.owner }
func (b capabilityTestBinding) Actions() []string     { return b.actions }
func (capabilityTestBinding) CreateAction() string    { return "CAPABILITY_TEST_CREATE" }
func (capabilityTestBinding) DeleteAction() string    { return "CAPABILITY_TEST_DELETE" }
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
		{"complete", []string{"CAPABILITY_TEST_CREATE", "CAPABILITY_TEST_DELETE"}, nil, true},
		{"missing-delete", []string{"CAPABILITY_TEST_CREATE"}, nil, false},
		{"foreign-delete", []string{"CAPABILITY_TEST_CREATE"}, []string{"CAPABILITY_TEST_DELETE"}, false},
		{"foreign-create", []string{"CAPABILITY_TEST_DELETE"}, []string{"CAPABILITY_TEST_CREATE"}, false},
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
	if err := r.Register(capabilityTestBinding{owner: "ani-inference", actions: []string{"CAPABILITY_TEST_CREATE", "CAPABILITY_TEST_DELETE"}}); err != nil {
		t.Fatal(err)
	}
	if !r.GPUExecutionEnabled("ani-inference") || !r.EnforcesQuota(GpuSharedQuotaCode) || r.EnforcesQuota(GpuPhysicalQuotaCode) {
		t.Fatal("inference owner GPU admission was not enabled")
	}
}

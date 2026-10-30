package service

import (
	"strings"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
)

func listFilterPtr(v string) *string { return &v }

func TestVMListFilterNormalizesAndRejects(t *testing.T) {
	if state, keyword, err := vmListFilter(nil, nil); err != nil || state != "" || keyword != "" {
		t.Fatalf("absent filters: %q %q %v", state, keyword, err)
	}
	if state, keyword, err := vmListFilter(listFilterPtr(" all "), listFilterPtr("  vm-linux  ")); err != nil || state != "" || keyword != "vm-linux" {
		t.Fatalf("normalization: %q %q %v", state, keyword, err)
	}
	for _, state := range []string{"pending", "provisioning", "starting", "stopping", "running", "stopped", "failed", "deleting", "deleted"} {
		if got, _, err := vmListFilter(listFilterPtr(state), nil); err != nil || got != state {
			t.Fatalf("state %s must be accepted: %q %v", state, got, err)
		}
	}
	if _, _, err := vmListFilter(listFilterPtr("Running"), nil); err == nil || kratoserrors.Reason(err) != "VM_INVALID_REQUEST" {
		t.Fatalf("case-sensitive state must be rejected: %v", err)
	}
	if _, _, err := vmListFilter(listFilterPtr("bogus"), nil); err == nil || kratoserrors.Reason(err) != "VM_INVALID_REQUEST" {
		t.Fatalf("unknown state must be rejected: %v", err)
	}
	if _, _, err := vmListFilter(nil, listFilterPtr(strings.Repeat("字", 81))); err == nil || kratoserrors.Reason(err) != "VM_INVALID_REQUEST" {
		t.Fatalf("oversized keyword must be rejected: %v", err)
	}
	if _, _, err := vmListFilter(nil, listFilterPtr(strings.Repeat("字", 80))); err != nil {
		t.Fatalf("80-rune keyword must be accepted: %v", err)
	}
}

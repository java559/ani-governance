package service

import (
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"testing"
)

func TestVMSecurityGroupCommandPreservesExplicitSelection(t *testing.T) {
	d := &view.ChangeVMData{VmId: "11111111-1111-4111-8111-111111111111", IdempotencyKey: "22222222-2222-4222-8222-222222222222", ExpectedVersion: 3, SecurityGroupIds: []string{"sg_0123456789abcdef0123456789abcdef"}}
	if !validVMChange(d, "set-security-groups") {
		t.Fatal("valid selection rejected")
	}
	if validVMChange(d, "start") {
		t.Fatal("security groups accepted on unrelated command")
	}
	intent, ok := vmChangeIntent("33333333-3333-4333-8333-333333333333", "set-security-groups", d).(*vm.SetSecurityGroupsRequest)
	if !ok || intent.ExpectedVersion != 3 || len(intent.SecurityGroupIds) != 1 || intent.SecurityGroupIds[0] != d.SecurityGroupIds[0] {
		t.Fatal("selection lost", intent)
	}
	before, err := vm.CanonicalCommandIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	intent.SecurityGroupIds = nil
	after, err := vm.CanonicalCommandIntent(intent)
	if err != nil || string(before) == string(after) {
		t.Fatal("clear not bound to command intent", err)
	}
}

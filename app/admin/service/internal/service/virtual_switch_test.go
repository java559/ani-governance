package service

import (
	"testing"

	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"google.golang.org/protobuf/proto"
)

func TestVirtualSwitchUnboundContract(t *testing.T) {
	unbound := &catalogv1.VirtualSwitch{Id: "vsw_0123456789abcdef0123456789abcdef", Name: "test-switch", State: "available", Version: 1, BindingState: "unbound"}
	if !validVirtualSwitch(unbound, unbound.Id) {
		t.Fatal("persisted unbound switch must not require fake cluster or Provider identity")
	}
	for _, tc := range []struct {
		name string
		edit func(*catalogv1.VirtualSwitch)
	}{
		{"wrong_resource", func(v *catalogv1.VirtualSwitch) { v.Id = "vlan_0123456789abcdef0123456789abcdef" }},
		{"invalid_name", func(v *catalogv1.VirtualSwitch) { v.Name = "external.group" }},
		{"fake_binding", func(v *catalogv1.VirtualSwitch) { v.ClusterId = "cluster" }},
		{"phantom_members", func(v *catalogv1.VirtualSwitch) { v.Nodes = []*catalogv1.VirtualSwitchNode{{NodeName: "node"}} }},
		{"wrong_group", func(v *catalogv1.VirtualSwitch) { v.GroupName = "external-group" }},
		{"unknown_state", func(v *catalogv1.VirtualSwitch) { v.State = "ready" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := proto.Clone(unbound).(*catalogv1.VirtualSwitch)
			tc.edit(v)
			if validVirtualSwitch(v, "") {
				t.Fatal("invalid downstream switch accepted")
			}
		})
	}
	deleted := proto.Clone(unbound).(*catalogv1.VirtualSwitch)
	deleted.State, deleted.BindingState = "deleted", "cleaned"
	if !validVirtualSwitch(deleted, deleted.Id) {
		t.Fatal("unbound deletion tombstone must not acquire a fake binding")
	}
	if validVirtualSwitch(unbound, "vsw_1123456789abcdef0123456789abcdef") {
		t.Fatal("downstream returned a different switch")
	}
}

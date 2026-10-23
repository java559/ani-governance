package service

import (
	"testing"
	"time"

	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDefaultVPCEgressContract(t *testing.T) {
	applied := true
	available := &catalogv1.DefaultVPCEgress{Id: "dve_0123456789abcdef0123456789abcdef", Name: "test-egress", PoolId: "pool_0123456789abcdef0123456789abcdef", State: "available", Version: 2, LastOperationId: "01234567-89ab-4cde-8123-0123456789ab", Address: "172.16.102.192", EipProviderName: "test-eip", EipProviderUid: "actual-eip-uid", SnatProviderName: "test-snat", SnatProviderUid: "actual-snat-uid", DesiredEnabled: true, AppliedEnabled: &applied, ClusterId: "configured-cluster", ClusterUid: "actual-cluster-uid", DefaultVpcNamespace: "configured-system-namespace", DefaultVpcName: "configured-system-vpc", DefaultVpcUid: "actual-default-vpc-uid", TargetGeneration: 2, ObservedAt: timestamppb.New(time.Unix(1, 0))}
	if !validDefaultVPCEgress(available, available.Id) {
		t.Fatal("jointly observed platform default egress rejected")
	}
	for _, tc := range []struct {
		name string
		edit func(*catalogv1.DefaultVPCEgress)
	}{
		{"stale_observation", func(v *catalogv1.DefaultVPCEgress) { v.ObservationStale = true }},
		{"unknown_mutation", func(v *catalogv1.DefaultVPCEgress) { v.PendingUnknown = true }},
		{"pending_action", func(v *catalogv1.DefaultVPCEgress) { v.PendingAction = "create_snat" }},
		{"unknown_applied", func(v *catalogv1.DefaultVPCEgress) { v.AppliedEnabled = nil }},
		{"missing_eip_uid", func(v *catalogv1.DefaultVPCEgress) { v.EipProviderUid = "" }},
		{"missing_snat_uid", func(v *catalogv1.DefaultVPCEgress) { v.SnatProviderUid = "" }},
		{"missing_generation", func(v *catalogv1.DefaultVPCEgress) { v.TargetGeneration = 0 }},
		{"missing_observation", func(v *catalogv1.DefaultVPCEgress) { v.ObservedAt = nil }},
		{"missing_default_vpc_identity", func(v *catalogv1.DefaultVPCEgress) { v.DefaultVpcUid = "" }},
		{"missing_cluster_identity", func(v *catalogv1.DefaultVPCEgress) { v.ClusterUid = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := proto.Clone(available).(*catalogv1.DefaultVPCEgress)
			tc.edit(v)
			if validDefaultVPCEgress(v, v.Id) {
				t.Fatal("unproven default egress accepted as available")
			}
		})
	}
	pending := proto.Clone(available).(*catalogv1.DefaultVPCEgress)
	pending.State, pending.PendingUnknown, pending.PendingAction, pending.AppliedEnabled = "provisioning", true, "create_snat", nil
	if !validDefaultVPCEgress(pending, pending.Id) {
		t.Fatal("unknown accepted outcome must remain observable without fabricated applied state")
	}
}

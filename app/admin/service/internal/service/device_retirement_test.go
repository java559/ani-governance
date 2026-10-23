package service

import (
	"testing"
	"time"

	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDeviceRetirementContract(t *testing.T) {
	retired := &catalogv1.DeviceRetirement{DeviceId: "device_0123456789abcdef0123456789abcdef", OperationId: "01234567-89ab-4cde-8123-0123456789ab", State: "retired", Version: 2, ExpectedVersion: 3, ConfigUid: "actual-config-uid", FenceVersion: 4, CompletedAt: timestamppb.New(time.Unix(1, 0))}
	if !validDeviceRetirement(retired, retired.DeviceId) {
		t.Fatal("proven retirement rejected")
	}
	for _, tc := range []struct {
		name string
		edit func(*catalogv1.DeviceRetirement)
	}{
		{"pending_unknown", func(v *catalogv1.DeviceRetirement) { v.PendingUnknown = true }},
		{"missing_fence", func(v *catalogv1.DeviceRetirement) { v.FenceVersion = v.ExpectedVersion }},
		{"missing_uid", func(v *catalogv1.DeviceRetirement) { v.ConfigUid = "" }},
		{"missing_completion", func(v *catalogv1.DeviceRetirement) { v.CompletedAt = nil }},
		{"invalid_operation", func(v *catalogv1.DeviceRetirement) { v.OperationId = "request-id" }},
		{"unknown_state", func(v *catalogv1.DeviceRetirement) { v.State = "available" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := proto.Clone(retired).(*catalogv1.DeviceRetirement)
			tc.edit(v)
			if validDeviceRetirement(v, retired.DeviceId) {
				t.Fatal("unproven retirement accepted as complete")
			}
		})
	}
	blocked := proto.Clone(retired).(*catalogv1.DeviceRetirement)
	blocked.State, blocked.CompletedAt, blocked.PendingUnknown, blocked.ConfigUid = "blocked", nil, true, ""
	if !validDeviceRetirement(blocked, blocked.DeviceId) {
		t.Fatal("unknown blocked outcome must remain observable")
	}
}

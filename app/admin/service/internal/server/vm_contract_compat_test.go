package server

import (
	"testing"

	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/vm/service/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestVMListResponseKeepsSecurityGroupsAndCreationTime(t *testing.T) {
	vm := &view.VirtualMachine{SecurityGroupIds: []string{"sg_123"}, CreatedAt: "2026-10-30T00:00:00Z"}
	fields := vm.ProtoReflect().Descriptor().Fields()
	require.EqualValues(t, 23, fields.ByName("security_group_ids").Number())
	require.EqualValues(t, 24, fields.ByName("created_at").Number())
	encoded, err := protojson.Marshal(vm)
	require.NoError(t, err)
	require.JSONEq(t, `{"security_group_ids":["sg_123"],"createdAt":"2026-10-30T00:00:00Z"}`, string(encoded))
}

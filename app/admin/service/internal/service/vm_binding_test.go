package service

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	"go-wind-admin/app/admin/service/internal/commandcontract"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"strings"
	"testing"
)

func vmFixtureSpec(t *testing.T) *vm.CreateSpec {
	t.Helper()
	cluster, preset := uuid.NewString(), uuid.NewString()
	start := false
	selection := &compute.ComputeSelection{SchemaVersion: 1, PresetId: preset, Code: "vm-small", Name: "VM small", ComputeType: "cpu", CpuMillis: 1500, MemoryMib: 1024}
	var err error
	selection.SelectionDigest, err = compute.SelectionDigest(selection)
	require.NoError(t, err)
	intent := &vm.CreateVMData{Name: "vm-test", PresetId: preset, SourceKind: "DISK_IMAGE", MediaKey: "ubuntu-24", SystemDiskSizeGb: 20, StartAfterCreate: &start, Network: &vm.NetworkSelection{VpcId: "vpc_" + strings.ReplaceAll(uuid.NewString(), "-", ""), SubnetId: "subnet_" + strings.ReplaceAll(uuid.NewString(), "-", "")}}
	return &vm.CreateSpec{Intent: intent, Compute: selection, ClusterId: cluster, Namespace: "tenant-vm", NamespaceUid: uuid.NewString(), ServiceAccount: "vm", Storage: &vm.StoragePolicy{StorageClass: "vm-block", StorageClassUid: uuid.NewString(), VolumeMode: "Block", AccessModes: []string{"ReadWriteOnce"}}, Media: &vm.MediaReference{MediaKey: intent.MediaKey, Name: "Ubuntu", Kind: "DISK_IMAGE", ClusterId: cluster, Namespace: "vm-source", PvcName: "ubuntu-24", PvcUid: uuid.NewString(), Checksum: strings.Repeat("a", 64), VolumeMode: "Block", ContentType: "kubevirt", VirtualCapacityBytes: 1000000000, RequestedBytes: 1000000000, OsFamily: "linux", OsVersion: "24.04", Architecture: "amd64", Firmware: "bios", RootBus: "virtio", DataBus: "scsi", NicModel: "virtio"}, Network: &vm.FrozenNetwork{VpcId: intent.Network.VpcId, SubnetId: intent.Network.SubnetId, NadName: "tenant-default", NadUid: uuid.NewString(), NadConfigDigest: strings.Repeat("b", 64)}, Sockets: 1, Cores: 2, Threads: 1, BootDevice: "SYSTEM_DISK"}
}

func TestVMDeleteOnlyExplicitVersionRejectionTerminatesFence(t *testing.T) {
	for _, test := range []struct {
		err  error
		want bool
	}{
		{status.Error(codes.Aborted, "VM_VERSION_CONFLICT"), true},
		{status.Error(codes.FailedPrecondition, "VM_VERSION_CONFLICT"), true},
		{status.Error(codes.DeadlineExceeded, "VM_VERSION_CONFLICT"), false},
		{status.Error(codes.Unavailable, "VM_VERSION_CONFLICT"), false},
		{status.Error(codes.Aborted, "unknown result"), false},
		{status.Error(codes.PermissionDenied, "VM_SCOPE_DENIED"), false},
	} {
		require.Equal(t, test.want, vmDeleteVersionRejected(test.err), test.err.Error())
	}
}
func vmFixtureCanonical(t *testing.T, spec *vm.CreateSpec) []byte {
	t.Helper()
	raw, err := vm.CanonicalCreateSpec(spec)
	require.NoError(t, err)
	raw, err = commandcontract.MarshalVMCanonical(&commandcontract.VMCanonical{VMContractVersion: 1, CreateSpec: raw})
	require.NoError(t, err)
	return raw
}
func TestVMActionIsolationAndCanonical(t *testing.T) {
	raw := vmFixtureCanonical(t, vmFixtureSpec(t))
	input := commandcontract.StoredCommand{Owner: InstanceOwner, Action: commandcontract.VMCreateAction, Canonical: raw, InstanceContractVersion: 1}
	facts, err := commandcontract.ValidateStoredCommand(input)
	require.NoError(t, err)
	require.NotNil(t, facts.VM)
	require.False(t, facts.CPU)
	input.InstanceContractVersion = 91
	_, err = commandcontract.ValidateStoredCommand(input)
	require.NoError(t, err, "legacy Instance column cannot version VM")
	for _, action := range []string{InstanceCreateAction, InstanceDeleteAction, "/vm.v1.VMService/Unknown"} {
		input.Action = action
		_, err = commandcontract.ValidateStoredCommand(input)
		require.Error(t, err)
	}
	input.Action = commandcontract.VMCreateAction
	input.Charges = []commandcontract.ChargeRef{{QuotaCode: "cpu", ChargedUnits: 1}}
	_, err = commandcontract.ValidateStoredCommand(input)
	require.Error(t, err)
	for _, field := range []string{`"schema":"vm-v1",`, `"usage":"VIRTUAL_MACHINE",`, `"vm_contract_version":2,`} {
		mutated := append([]byte("{"+field), raw[1:]...)
		_, err = commandcontract.DecodeVMCanonical(mutated)
		require.Error(t, err)
	}
	deletion, err := commandcontract.DecodeVMCanonical(raw)
	require.NoError(t, err)
	deletion.DeleteExpectedVersion = 7
	deleted, err := commandcontract.MarshalVMCanonical(deletion)
	require.NoError(t, err)
	_, err = commandcontract.DeleteSnapshot(InstanceOwner, string(raw), string(deleted))
	require.NoError(t, err)
	deletion.CreateSpec = json.RawMessage(`{"intent":null}`)
	changed, err := commandcontract.MarshalVMCanonical(deletion)
	require.NoError(t, err)
	_, err = commandcontract.DeleteSnapshot(InstanceOwner, string(raw), string(changed))
	require.Error(t, err)
	require.Equal(t, "VIRTUAL_MACHINE", commandcontract.Usage(InstanceOwner, commandcontract.VMCreateAction))
	require.Equal(t, "CONTAINER_INSTANCE", commandcontract.Usage(InstanceOwner, InstanceCreateAction))
	require.Empty(t, commandcontract.Usage(InstanceOwner, "unknown"))
}

type vmResolvePeer struct {
	VMOwnerClient
	spec  *vm.CreateSpec
	calls int
}

func (p *vmResolvePeer) Resolve(_ context.Context, _, _ string, in *vm.CreateVMData) (*vm.ResolveCreateSpecResponse, error) {
	p.calls++
	requireSpec := proto.Clone(p.spec).(*vm.CreateSpec)
	requireSpec.Intent = proto.Clone(in).(*vm.CreateVMData)
	digest, err := vm.CreateSpecDigest(requireSpec)
	if err != nil {
		return nil, err
	}
	return &vm.ResolveCreateSpecResponse{VmContractVersion: 1, CreateSpec: requireSpec, SelectionDigest: requireSpec.Compute.SelectionDigest, VmCreateDigest: digest}, nil
}
func TestVMBindingFreezesSpecWithoutQuotaPlan(t *testing.T) {
	spec := vmFixtureSpec(t)
	peer := &vmResolvePeer{spec: spec}
	binding := NewVMBinding(peer, nil, spec.ClusterId)
	prepared, err := binding.PrepareManagedBusiness(context.Background(), &auth.Principal{ID: 7}, uuid.NewString(), spec.Intent)
	require.NoError(t, err)
	require.Nil(t, prepared.gpu)
	require.Zero(t, prepared.instanceContractVersion)
	first, err := prepared.freeze("tenant", "user", "7", "op", "resource", nil)
	require.NoError(t, err)
	peer.spec.Compute.CpuMillis = 10000
	second, err := prepared.freeze("tenant", "user", "7", "op", "resource", nil)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, peer.calls)
	registry := NewQuotaAdapterRegistry()
	require.NoError(t, registry.Register(NewResourceDispatchAdapter(NewInstanceGpuBinding(nil, nil, ""), binding)))
	require.True(t, registry.HasAdapter(InstanceOwner, InstanceCreateAction))
	require.True(t, registry.HasAdapter(InstanceOwner, commandcontract.VMCreateAction))
	require.Error(t, registry.Register(binding), "Resource owner must be unique")
}

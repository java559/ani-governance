package data

import (
	"context"
	"github.com/google/uuid"
	instance "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	console "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/console/v1"
	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"time"
)

type VMClient struct {
	client vm.VMServiceClient
	conn   grpc.ClientConnInterface
}

const vmLogsMethod = "/vm.v1.VMService/StreamVMLogs"

// vmLogClient carries the guest-serial stream until the Resource release that
// publishes VMService/StreamVMLogs is pinned in go.mod. The instance log
// messages share field numbers with the VM contract (tenant, workload, tail,
// follow and kind/data/reason), so the same generated types stay on the wire.
type vmLogClient struct{ grpc.ClientStream }

func (s *vmLogClient) Recv() (*instance.InstanceLogFrame, error) {
	out := new(instance.InstanceLogFrame)
	if e := s.ClientStream.RecvMsg(out); e != nil {
		return nil, e
	}
	return out, nil
}

// Logs deliberately inherits the caller's stream lifetime; the unary 30 second
// deadline would cut idle follow streams.
func (c *VMClient) Logs(ctx context.Context, tenant, actor, id string, tail uint32, follow bool) (grpc.ServerStreamingClient[instance.InstanceLogFrame], error) {
	if c == nil || c.client == nil || c.conn == nil {
		return nil, status.Error(codes.Unavailable, "VM_ADAPTER_UNAVAILABLE")
	}
	if !validResourceIdentity(tenant, actor) {
		return nil, status.Error(codes.Unauthenticated, "VM_SCOPE_DENIED")
	}
	call := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	stream, err := c.conn.NewStream(call, &grpc.StreamDesc{StreamName: "StreamVMLogs", ServerStreams: true}, vmLogsMethod, grpc.MaxCallRecvMsgSize(20*1024))
	if err != nil {
		return nil, err
	}
	if err = stream.SendMsg(&instance.StreamInstanceLogsRequest{TenantId: tenant, InstanceId: id, TailLines: tail, Follow: follow}); err != nil {
		return nil, err
	}
	if err = stream.CloseSend(); err != nil {
		return nil, err
	}
	return &vmLogClient{ClientStream: stream}, nil
}

func vmCall[T any](c *VMClient, ctx context.Context, tenant, actor string, invoke func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil || c.client == nil {
		return zero, status.Error(codes.Unavailable, "VM_ADAPTER_UNAVAILABLE")
	}
	if !validResourceIdentity(tenant, actor) {
		return zero, status.Error(codes.Unauthenticated, "VM_SCOPE_DENIED")
	}
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call = metadata.NewOutgoingContext(call, metadata.Pairs("x-ani-tenant-id", tenant, "x-ani-actor", actor, "x-ani-request-id", uuid.NewString()))
	return invoke(call)
}
func (c *VMClient) Resolve(ctx context.Context, t, a string, in *vm.CreateVMData) (*vm.ResolveCreateSpecResponse, error) {
	return vmCall(c, ctx, t, a, func(ctx context.Context) (*vm.ResolveCreateSpecResponse, error) {
		return c.client.ResolveCreateSpec(ctx, &vm.ResolveCreateSpecRequest{TenantId: t, Data: in, VmContractVersion: vm.ContractVersion})
	})
}
func (c *VMClient) Create(ctx context.Context, t, a string, in *vm.AcceptCreateRequest) (*vm.Acceptance, error) {
	if in == nil || in.Context == nil || in.Context.TenantId != t {
		return nil, status.Error(codes.InvalidArgument, "VM_INVALID_REQUEST")
	}
	return vmCall(c, ctx, t, a, func(ctx context.Context) (*vm.Acceptance, error) { return c.client.AcceptCreate(ctx, in) })
}
func (c *VMClient) Delete(ctx context.Context, t, a string, in *vm.AcceptDeleteRequest) (*vm.Acceptance, error) {
	if in == nil || in.Context == nil || in.Context.TenantId != t {
		return nil, status.Error(codes.InvalidArgument, "VM_INVALID_REQUEST")
	}
	return vmCall(c, ctx, t, a, func(ctx context.Context) (*vm.Acceptance, error) { return c.client.AcceptDelete(ctx, in) })
}
func (c *VMClient) Get(ctx context.Context, t, a, id string) (*vm.VirtualMachine, error) {
	return vmCall(c, ctx, t, a, func(ctx context.Context) (*vm.VirtualMachine, error) {
		return c.client.GetVM(ctx, &vm.GetVMRequest{TenantId: t, VmId: id})
	})
}
func (c *VMClient) List(ctx context.Context, t, a string, page, size uint32) (*vm.ListVMsResponse, error) {
	return vmCall(c, ctx, t, a, func(ctx context.Context) (*vm.ListVMsResponse, error) {
		return c.client.ListVMs(ctx, &vm.ListVMsRequest{TenantId: t, Page: page, PageSize: size})
	})
}
func (c *VMClient) Operation(ctx context.Context, t, a, id, operation string) (*vm.Operation, error) {
	return vmCall(c, ctx, t, a, func(ctx context.Context) (*vm.Operation, error) {
		return c.client.GetOperation(ctx, &vm.GetOperationRequest{TenantId: t, VmId: id, OperationId: operation})
	})
}
func (c *VMClient) Lookup(ctx context.Context, t, a string, in *vm.LookupCommandReplayRequest) (*vm.Operation, error) {
	if in == nil || in.TenantId != t {
		return nil, status.Error(codes.InvalidArgument, "VM_INVALID_REQUEST")
	}
	return vmCall(c, ctx, t, a, func(ctx context.Context) (*vm.Operation, error) { return c.client.LookupCommandReplay(ctx, in) })
}
func (c *VMClient) Target(ctx context.Context, t, a, id string, version int64) (*console.VMConsoleBinding, error) {
	return vmCall(c, ctx, t, a, func(ctx context.Context) (*console.VMConsoleBinding, error) {
		return c.client.ResolveConsoleTarget(ctx, &vm.ResolveConsoleTargetRequest{TenantId: t, VmId: id, ExpectedVersion: version})
	})
}
func (c *VMClient) Change(ctx context.Context, t, a, action string, in proto.Message) (*vm.Operation, error) {
	return vmCall(c, ctx, t, a, func(ctx context.Context) (*vm.Operation, error) {
		switch action {
		case "start":
			return c.client.Start(ctx, in.(*vm.ChangeVMRequest))
		case "stop":
			return c.client.Stop(ctx, in.(*vm.ChangeVMRequest))
		case "restart":
			return c.client.Restart(ctx, in.(*vm.ChangeVMRequest))
		case "force-stop":
			return c.client.ForceStop(ctx, in.(*vm.ForceStopRequest))
		case "create-disk":
			return c.client.CreateDisk(ctx, in.(*vm.CreateDiskRequest))
		case "attach-disk":
			return c.client.AttachDisk(ctx, in.(*vm.DiskChangeRequest))
		case "detach-disk":
			return c.client.DetachDisk(ctx, in.(*vm.DiskChangeRequest))
		case "delete-disk":
			return c.client.DeleteDisk(ctx, in.(*vm.DiskChangeRequest))
		case "set-media":
			return c.client.SetMedia(ctx, in.(*vm.SetMediaRequest))
		case "eject-media":
			return c.client.EjectMedia(ctx, in.(*vm.EjectMediaRequest))
		case "set-boot":
			return c.client.SetBootDevice(ctx, in.(*vm.SetBootDeviceRequest))
		case "create-snapshot":
			return c.client.CreateSnapshot(ctx, in.(*vm.SnapshotCreateRequest))
		case "delete-snapshot":
			return c.client.DeleteSnapshot(ctx, in.(*vm.SnapshotChangeRequest))
		case "restore-snapshot":
			return c.client.RestoreSnapshot(ctx, in.(*vm.RestoreSnapshotRequest))
		default:
			return nil, status.Error(codes.InvalidArgument, "VM_INVALID_REQUEST")
		}
	})
}
func (c *VMClient) Read(ctx context.Context, t, a, action, id, snapshot string) (proto.Message, error) {
	return vmCall(c, ctx, t, a, func(ctx context.Context) (proto.Message, error) {
		switch action {
		case "disks":
			return c.client.ListDisks(ctx, &vm.GetVMRequest{TenantId: t, VmId: id})
		case "media":
			return c.client.ListMedia(ctx, &vm.ListMediaRequest{TenantId: t})
		case "dns-defaults":
			return c.client.GetDNSDefaults(ctx, &vm.GetDNSDefaultsRequest{TenantId: t})
		case "snapshots":
			return c.client.ListSnapshots(ctx, &vm.GetVMRequest{TenantId: t, VmId: id})
		case "snapshot":
			return c.client.GetSnapshot(ctx, &vm.GetSnapshotRequest{TenantId: t, VmId: id, SnapshotId: snapshot})
		case "preview-restore":
			return c.client.PreviewRestore(ctx, &vm.PreviewRestoreRequest{TenantId: t, VmId: id, SnapshotId: snapshot})
		default:
			return nil, status.Error(codes.InvalidArgument, "VM_INVALID_REQUEST")
		}
	})
}

package data

import (
	"context"
	"net"
	"testing"

	vm "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const (
	vmLogsTestTenant = "11111111-1111-4111-8111-111111111111"
	vmLogsTestVM     = "22222222-2222-4222-8222-222222222222"
	vmLogsTestActor  = "governance:user:42"
)

type vmLogsServer struct {
	vm.UnimplementedVMServiceServer
	received *vm.StreamVMLogsRequest
	md       metadata.MD
}

func (s *vmLogsServer) StreamVMLogs(r *vm.StreamVMLogsRequest, stream grpc.ServerStreamingServer[vm.VMLogFrame]) error {
	s.received = r
	if incoming, ok := metadata.FromIncomingContext(stream.Context()); ok {
		s.md = incoming
	}
	if err := stream.Send(&vm.VMLogFrame{Kind: vm.VMLogFrame_READY}); err != nil {
		return err
	}
	return stream.Send(&vm.VMLogFrame{Kind: vm.VMLogFrame_END, Reason: "EOF"})
}

func vmLogsTestConnection(t *testing.T) (*grpc.ClientConn, *vmLogsServer) {
	t.Helper()
	peer := new(vmLogsServer)
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	vm.RegisterVMServiceServer(server, peer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///vm-logs", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, peer
}

func TestVMClientLogsUseStreamVMLogs(t *testing.T) {
	conn, peer := vmLogsTestConnection(t)
	stream, err := (&VMClient{client: vm.NewVMServiceClient(conn)}).Logs(context.Background(), vmLogsTestTenant, vmLogsTestActor, vmLogsTestVM, 42, false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || first.Kind != vm.VMLogFrame_READY {
		t.Fatalf("ready: %v %v", first, err)
	}
	end, err := stream.Recv()
	if err != nil || end.Kind != vm.VMLogFrame_END || end.Reason != "EOF" {
		t.Fatalf("end: %v %v", end, err)
	}
	if _, err = stream.Recv(); err == nil {
		t.Fatal("stream did not finish")
	}
	if peer.received.GetTenantId() != vmLogsTestTenant || peer.received.GetVmId() != vmLogsTestVM || peer.received.GetTailLines() != 42 || peer.received.GetFollow() {
		t.Fatalf("request: %+v", peer.received)
	}
	if got := peer.md.Get("x-ani-actor"); len(got) != 1 || got[0] != vmLogsTestActor {
		t.Fatalf("metadata: %v", peer.md)
	}
}

func TestVMClientLogsRejectsUnscopedCallers(t *testing.T) {
	conn, _ := vmLogsTestConnection(t)
	client := &VMClient{client: vm.NewVMServiceClient(conn)}
	if _, err := client.Logs(context.Background(), vmLogsTestTenant, "", vmLogsTestVM, 10, false); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing actor: %v", err)
	}
	if _, err := (&VMClient{}).Logs(context.Background(), vmLogsTestTenant, vmLogsTestActor, vmLogsTestVM, 10, false); status.Code(err) != codes.Unavailable {
		t.Fatalf("missing connection: %v", err)
	}
}

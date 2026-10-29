package data

import (
	"context"
	"net"
	"testing"

	instance "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
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

// The VM stream is served here with instance log messages: they occupy the
// same field numbers as the VM contract, which is the compatibility property
// the client relies on until the Resource release is pinned.
func vmLogsTestServer(t *testing.T) (*grpc.ClientConn, *instance.StreamInstanceLogsRequest, *metadata.MD) {
	t.Helper()
	received := new(instance.StreamInstanceLogsRequest)
	md := new(metadata.MD)
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "vm.v1.VMService",
		HandlerType: (*any)(nil),
		Streams: []grpc.StreamDesc{{
			StreamName:    "StreamVMLogs",
			ServerStreams: true,
			Handler: func(_ any, stream grpc.ServerStream) error {
				if incoming, ok := metadata.FromIncomingContext(stream.Context()); ok {
					*md = incoming
				}
				if err := stream.RecvMsg(received); err != nil {
					return err
				}
				if err := stream.SendMsg(&instance.InstanceLogFrame{Kind: instance.InstanceLogFrame_READY}); err != nil {
					return err
				}
				return stream.SendMsg(&instance.InstanceLogFrame{Kind: instance.InstanceLogFrame_END, Reason: "EOF"})
			},
		}},
	}, struct{}{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///vm-logs", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, received, md
}

func TestVMClientLogsReachStreamVMLogs(t *testing.T) {
	conn, received, md := vmLogsTestServer(t)
	client := &VMClient{client: vm.NewVMServiceClient(conn), conn: conn}
	stream, err := client.Logs(context.Background(), vmLogsTestTenant, vmLogsTestActor, vmLogsTestVM, 42, false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := stream.Recv()
	if err != nil || first.Kind != instance.InstanceLogFrame_READY {
		t.Fatalf("ready: %v %v", first, err)
	}
	end, err := stream.Recv()
	if err != nil || end.Kind != instance.InstanceLogFrame_END || end.Reason != "EOF" {
		t.Fatalf("end: %v %v", end, err)
	}
	if _, err = stream.Recv(); err == nil {
		t.Fatal("stream did not finish")
	}
	if received.GetTenantId() != vmLogsTestTenant || received.GetInstanceId() != vmLogsTestVM || received.GetTailLines() != 42 || received.GetFollow() {
		t.Fatalf("wire request: %+v", received)
	}
	if got := md.Get("x-ani-actor"); len(got) != 1 || got[0] != vmLogsTestActor {
		t.Fatalf("metadata: %v", md)
	}
}

func TestVMClientLogsRejectsUnscopedCallers(t *testing.T) {
	conn, _, _ := vmLogsTestServer(t)
	client := &VMClient{client: vm.NewVMServiceClient(conn), conn: conn}
	if _, err := client.Logs(context.Background(), vmLogsTestTenant, "", vmLogsTestVM, 10, false); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing actor: %v", err)
	}
	if _, err := (&VMClient{}).Logs(context.Background(), vmLogsTestTenant, vmLogsTestActor, vmLogsTestVM, 10, false); status.Code(err) != codes.Unavailable {
		t.Fatalf("missing connection: %v", err)
	}
}

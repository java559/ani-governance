package data

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	pb "github.com/zhangzhe-ctrl/ani-resource-service/api/sandbox/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type tenantSpaceClientTestServer struct {
	pb.UnimplementedSandboxServiceServer
	t *testing.T
}

func (s *tenantSpaceClientTestServer) check(ctx context.Context, tenant string) {
	s.t.Helper()
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || len(md.Get("x-ani-actor")) != 1 || md.Get("x-ani-actor")[0] != "governance:tenant-space" || len(md.Get("x-ani-tenant-id")) != 1 || md.Get("x-ani-tenant-id")[0] != tenant || len(md.Get("x-ani-request-id")) != 1 {
		s.t.Errorf("unexpected tenant space metadata")
	}
}
func (s *tenantSpaceClientTestServer) OpenTenantSpace(ctx context.Context, r *pb.OpenTenantSpaceRequest) (*pb.OpenTenantSpaceResponse, error) {
	s.check(ctx, r.GetTenantId())
	return &pb.OpenTenantSpaceResponse{Space: &pb.TenantSpace{TenantId: r.GetTenantId(), OperationId: r.GetOperationId(), Phase: "open"}}, nil
}
func (s *tenantSpaceClientTestServer) CloseTenantSpace(ctx context.Context, r *pb.CloseTenantSpaceRequest) (*pb.CloseTenantSpaceResponse, error) {
	s.check(ctx, r.GetTenantId())
	return &pb.CloseTenantSpaceResponse{Space: &pb.TenantSpace{TenantId: r.GetTenantId(), OperationId: r.GetOperationId(), Phase: "closed"}}, nil
}
func TestTenantSpaceClientUsesDedicatedIdentity(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	pb.RegisterSandboxServiceServer(server, &tenantSpaceClientTestServer{t: t})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///space-test", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := &InstanceClient{sandbox: pb.NewSandboxServiceClient(conn)}
	tenant, operation := uuid.NewString(), uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opened, err := client.OpenTenantSpace(ctx, tenant, operation)
	if err != nil || opened.TenantID != tenant || opened.OperationID != operation || opened.Phase != "open" {
		t.Fatalf("open=%+v err=%v", opened, err)
	}
	closed, err := client.CloseTenantSpace(ctx, tenant, operation)
	if err != nil || closed.TenantID != tenant || closed.OperationID != operation || closed.Phase != "closed" {
		t.Fatalf("close=%+v err=%v", closed, err)
	}
	if _, err = client.OpenTenantSpace(ctx, "bad", operation); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid tenant: %v", err)
	}
}

package data

import (
	"context"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"testing"
	"time"
)

type platformEIPRPCProbe struct {
	networkv1.PlatformEIPServiceClient
	t     *testing.T
	calls int
}

func (p *platformEIPRPCProbe) ListPlatformEIPs(ctx context.Context, r *networkv1.ListPlatformEIPsRequest, _ ...grpc.CallOption) (*networkv1.ListPlatformEIPsResponse, error) {
	p.calls++
	md, _ := metadata.FromOutgoingContext(ctx)
	if len(md) != 3 || len(md.Get("x-ani-tenant-id")) != 0 || len(md.Get("authorization")) != 0 || md.Get("x-ani-scope")[0] != "platform" || md.Get("x-ani-actor")[0] != "governance:user:9" {
		p.t.Fatalf("unexpected platform metadata: %v", md)
	}
	if _, ok := ctx.Deadline(); !ok {
		p.t.Fatal("deadline missing")
	}
	if r.TenantId != "11111111-1111-4111-8111-111111111111" || r.GetPage() != 3 {
		p.t.Fatal("server filters lost")
	}
	return &networkv1.ListPlatformEIPsResponse{}, nil
}
func TestPlatformEIPClientDelegation(t *testing.T) {
	probe := &platformEIPRPCProbe{t: t}
	c := &NetworkClient{platform: struct {
		networkv1.PlatformNetworkServiceClient
	}{}, platformEIPs: probe, timeout: time.Second}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-ani-tenant-id", "forged", "authorization", "secret"))
	page := uint32(3)
	r := &networkv1.ListPlatformEIPsRequest{Page: &page, TenantId: "11111111-1111-4111-8111-111111111111"}
	if _, err := c.ListPlatformEIPs(ctx, "governance:user:9", r); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListPlatformEIPs(ctx, "governance:access-key:9", r); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("API key actor admitted: %v", err)
	}
	if probe.calls != 1 {
		t.Fatal("denied actor reached RPC")
	}
}

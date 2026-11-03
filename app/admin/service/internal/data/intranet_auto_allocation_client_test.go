package data

import (
	"context"
	"testing"
	"time"

	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type intranetAutoAllocationRPCProbe struct {
	networkv1.PlatformNetworkServiceClient
	t     *testing.T
	calls int
}

func (p *intranetAutoAllocationRPCProbe) SetIntranetPoolAutoAllocationEnabled(ctx context.Context, req *networkv1.SetIntranetPoolAutoAllocationEnabledRequest, _ ...grpc.CallOption) (*networkv1.SetIntranetPoolAutoAllocationEnabledResponse, error) {
	p.calls++
	md, _ := metadata.FromOutgoingContext(ctx)
	if len(md) != 3 || len(md.Get("x-ani-tenant-id")) != 0 || len(md.Get("authorization")) != 0 || md.Get("x-ani-scope")[0] != "platform" || md.Get("x-ani-actor")[0] != "governance:user:9" || len(md.Get("x-ani-request-id")) != 1 {
		p.t.Fatalf("platform metadata was not rebuilt: %v", md)
	}
	if _, ok := ctx.Deadline(); !ok {
		p.t.Fatal("platform deadline missing")
	}
	if req.PoolId != "pool_0123456789abcdef0123456789abcdef" || !req.Enabled || req.ExpectedVersion != 3 || req.IdempotencyKey != "auto-on" {
		p.t.Fatal("command content lost")
	}
	return &networkv1.SetIntranetPoolAutoAllocationEnabledResponse{}, nil
}

func TestIntranetAutoAllocationClientDelegation(t *testing.T) {
	p := &intranetAutoAllocationRPCProbe{t: t}
	c := &NetworkClient{platform: p, timeout: time.Second}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("x-ani-tenant-id", "forged", "x-ani-scope", "tenant", "authorization", "secret"))
	req := &networkv1.SetIntranetPoolAutoAllocationEnabledRequest{PoolId: "pool_0123456789abcdef0123456789abcdef", Enabled: true, ExpectedVersion: 3, IdempotencyKey: "auto-on"}
	if _, err := c.SetIntranetPoolAutoAllocationEnabled(ctx, "governance:user:9", req); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetIntranetPoolAutoAllocationEnabled(ctx, "governance:access-key:9", req); status.Code(err) != codes.Unauthenticated || p.calls != 1 {
		t.Fatalf("key actor reached platform command: %v", err)
	}
}

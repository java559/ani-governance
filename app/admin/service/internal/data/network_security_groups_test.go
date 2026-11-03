package data

import (
	"context"
	"github.com/google/uuid"
	network "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"testing"
	"time"
)

type securityGroupRPCProbe struct {
	network.NetworkSecurityGroupServiceClient
	t     *testing.T
	actor string
	calls int
}

func (p *securityGroupRPCProbe) SetEIPSecurityGroups(ctx context.Context, in *network.SetEIPSecurityGroupsRequest, _ ...grpc.CallOption) (*network.SetEIPSecurityGroupsResponse, error) {
	p.calls++
	md, _ := metadata.FromOutgoingContext(ctx)
	if in.TargetTenantId != "11111111-1111-4111-8111-111111111111" || len(md.Get("x-ani-tenant-id")) != 1 || md.Get("x-ani-tenant-id")[0] != in.TargetTenantId || len(md.Get("x-ani-actor")) != 1 || md.Get("x-ani-actor")[0] != p.actor {
		p.t.Fatal("untrusted metadata", md, in)
	}
	if len(md.Get("authorization")) != 0 || len(md.Get("evil")) != 0 {
		p.t.Fatal("public headers forwarded", md)
	}
	if len(md.Get("x-ani-request-id")) != 1 {
		p.t.Fatal("request ID not single-valued")
	}
	if _, err := uuid.Parse(md.Get("x-ani-request-id")[0]); err != nil {
		p.t.Fatal(err)
	}
	if _, ok := ctx.Deadline(); !ok {
		p.t.Fatal("missing bounded timeout")
	}
	return &network.SetEIPSecurityGroupsResponse{}, nil
}
func TestSecurityGroupClientRebuildsIdentityAndPreservesCallerInput(t *testing.T) {
	for _, actor := range []string{"governance:user:7", "governance:access-key:42"} {
		probe := &securityGroupRPCProbe{t: t, actor: actor}
		client := &NetworkClient{client: failedNetworkRPC{}, securityGroups: probe, timeout: time.Second}
		request := &network.SetEIPSecurityGroupsRequest{TargetTenantId: "forged", EipId: "eip_0123456789abcdef0123456789abcdef", SecurityGroupIds: []string{"sg_0123456789abcdef0123456789abcdef"}}
		ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "public-token", "evil", "forwarded", "x-ani-tenant-id", "forged", "x-ani-tenant-id", "duplicate"))
		if _, err := client.SetEIPSecurityGroups(ctx, "11111111-1111-4111-8111-111111111111", actor, request); err != nil {
			t.Fatal(err)
		}
		if probe.calls != 1 || request.TargetTenantId != "forged" {
			t.Fatal("request scope mutated")
		}
	}
}

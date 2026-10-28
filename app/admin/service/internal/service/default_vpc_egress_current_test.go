package service

import (
	"context"
	"github.com/go-kratos/kratos/v2/errors"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

type currentDefaultEgressProbe struct {
	NetworkPlatformClient
	calls int
	err   error
}

func (p *currentDefaultEgressProbe) GetCurrentDefaultVPCEgress(_ context.Context, actor string, _ *networkv1.GetCurrentDefaultVPCEgressRequest) (*networkv1.GetCurrentDefaultVPCEgressResponse, error) {
	if actor != "governance:user:9" {
		panic("untrusted actor")
	}
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return &networkv1.GetCurrentDefaultVPCEgressResponse{Egress: &networkv1.DefaultVPCEgress{Id: "dve_0123456789abcdef0123456789abcdef", PoolId: "pool_0123456789abcdef0123456789abcdef", Version: 1, State: "provisioning", LastOperationId: "01234567-89ab-4cde-8123-0123456789ab", ClusterId: "fixed", ClusterUid: "fixed-uid", DefaultVpcNamespace: "kcn-system", DefaultVpcName: "kcn-cluster", DefaultVpcUid: "vpc-uid"}}, nil
}
func TestCurrentDefaultVPCEgressContract(t *testing.T) {
	p := &currentDefaultEgressProbe{}
	s := NewPlatformNetworkService(p)
	claims := &authv1.UserTokenPayload{UserId: 9, Roles: []string{"platform:reader"}, Permissions: []string{"network:platform:default-egress:get"}}
	ctx := auth.NewContext(context.Background(), claims)
	result, err := s.GetCurrentDefaultVPCEgress(ctx, &catalogv1.GetCurrentDefaultVPCEgressRequest{})
	if err != nil || result.Egress.DefaultVpcName != "kcn-cluster" || p.calls != 1 {
		t.Fatalf("current outlet not returned: %v %v", result, err)
	}
	tenant := auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: 9, TenantID: 1, Claims: claims, Roles: claims.Roles})
	if _, err = s.GetCurrentDefaultVPCEgress(tenant, &catalogv1.GetCurrentDefaultVPCEgressRequest{}); errors.FromError(err).Code != 403 || p.calls != 1 {
		t.Fatalf("tenant reached platform current outlet: %v", err)
	}
	p.err = status.Error(codes.NotFound, "outlet absent")
	if _, err = s.GetCurrentDefaultVPCEgress(ctx, &catalogv1.GetCurrentDefaultVPCEgressRequest{}); errors.FromError(err).Code != 404 {
		t.Fatalf("absence hidden as dependency failure: %v", err)
	}
}

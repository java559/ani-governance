package service

import (
	"context"
	"testing"

	"github.com/go-kratos/kratos/v2/errors"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type intranetAutoAllocationProbe struct {
	NetworkPlatformClient
	t       *testing.T
	calls   int
	request *networkv1.SetIntranetPoolAutoAllocationEnabledRequest
	reply   *networkv1.SetIntranetPoolAutoAllocationEnabledResponse
	err     error
}

func (p *intranetAutoAllocationProbe) GetIntranetPoolDefaults(_ context.Context, actor string, _ *networkv1.GetIntranetPoolDefaultsRequest) (*networkv1.GetIntranetPoolDefaultsResponse, error) {
	if actor != "governance:user:9" {
		p.t.Fatalf("untrusted actor %q", actor)
	}
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return &networkv1.GetIntranetPoolDefaultsResponse{DefaultVpcName: "kcn-cluster", DefaultVpcUid: "fixed-provider-uid", IntranetNetworks: []string{"172.16.101.0/24"}}, nil
}

func (p *intranetAutoAllocationProbe) SetIntranetPoolAutoAllocationEnabled(_ context.Context, actor string, req *networkv1.SetIntranetPoolAutoAllocationEnabledRequest) (*networkv1.SetIntranetPoolAutoAllocationEnabledResponse, error) {
	if actor != "governance:user:9" {
		p.t.Fatalf("untrusted actor %q", actor)
	}
	p.calls++
	p.request = req
	return p.reply, p.err
}

func TestIntranetAutoAllocationDelegation(t *testing.T) {
	const poolID = "pool_0123456789abcdef0123456789abcdef"
	p := &intranetAutoAllocationProbe{t: t, reply: &networkv1.SetIntranetPoolAutoAllocationEnabledResponse{Resource: &networkv1.PlatformResource{
		Id: poolID, ClusterId: "fixed", Version: 4, State: networkv1.ResourceState_RESOURCE_STATE_AVAILABLE,
		Configuration: &networkv1.PlatformResource_IntranetPool{IntranetPool: &networkv1.IntranetAddressPool{Scope: "intranet", IsDefault: true, AllocationEnabled: true, NewVpcsEnabled: true}},
	}}}
	s := NewPlatformNetworkService(p)
	claims := &authv1.UserTokenPayload{UserId: 9, Roles: []string{"platform:network-admin"}, Permissions: []string{"network:platform:intranet-pool:auto-allocate"}}
	ctx := auth.NewContext(context.Background(), claims)
	req := &catalogv1.SetIntranetPoolAutoAllocationEnabledRequest{PoolId: poolID, Data: &catalogv1.SetIntranetPoolAutoAllocationEnabledData{Enabled: true, ExpectedVersion: 3, IdempotencyKey: "auto-on"}}
	result, err := s.SetIntranetPoolAutoAllocationEnabled(ctx, req)
	if err != nil || result.GetResource().GetVersion() != 4 || !result.GetResource().GetIntranetPool().GetNewVpcsEnabled() {
		t.Fatalf("auto-allocation summary lost: %v %v", result, err)
	}
	if p.calls != 1 || !proto.Equal(p.request, &networkv1.SetIntranetPoolAutoAllocationEnabledRequest{PoolId: poolID, Enabled: true, ExpectedVersion: 3, IdempotencyKey: "auto-on"}) {
		t.Fatalf("command was not forwarded once intact: %v", p.request)
	}
	for _, principal := range []*auth.Principal{
		{Type: auth.SubjectUser, ID: 9, TenantID: 1, Claims: claims, Roles: claims.Roles},
		{Type: auth.SubjectAPIKey, ID: 9, Claims: claims, Roles: claims.Roles},
		{Type: auth.SubjectUser, ID: 9, Claims: &authv1.UserTokenPayload{UserId: 9, Roles: claims.Roles, Permissions: []string{"network:platform:intranet-pool:default"}}, Roles: claims.Roles},
	} {
		if _, err := s.SetIntranetPoolAutoAllocationEnabled(auth.NewPrincipalContext(ctx, principal), req); errors.Code(err) != 403 || p.calls != 1 {
			t.Fatalf("subject/action boundary bypassed: %v", err)
		}
	}
	for _, bad := range []*catalogv1.SetIntranetPoolAutoAllocationEnabledRequest{nil, {PoolId: poolID}, {PoolId: "invalid", Data: req.Data}} {
		if _, err := s.SetIntranetPoolAutoAllocationEnabled(ctx, bad); errors.Code(err) != 400 || p.calls != 1 {
			t.Fatalf("invalid input reached downstream: %v", err)
		}
	}
	p.err = status.Error(codes.FailedPrecondition, "default pool changed")
	if _, err := s.SetIntranetPoolAutoAllocationEnabled(ctx, req); errors.Code(err) != 412 {
		t.Fatalf("precondition failure not retained: %v", err)
	}
	p.err = nil
	p.reply.Resource.Id = "pool_11111111111111111111111111111111"
	if _, err := s.SetIntranetPoolAutoAllocationEnabled(ctx, req); errors.Code(err) != 503 {
		t.Fatalf("wrong target response accepted: %v", err)
	}
}

func TestIntranetPoolDefaultsPlatformRead(t *testing.T) {
	p := &intranetAutoAllocationProbe{t: t}
	s := NewPlatformNetworkService(p)
	claims := &authv1.UserTokenPayload{UserId: 9, Roles: []string{"platform:network-reader"}, Permissions: []string{"network:platform:intranet-pool:get"}}
	ctx := auth.NewContext(context.Background(), claims)
	reply, err := s.GetIntranetPoolDefaults(ctx, &catalogv1.GetIntranetPoolDefaultsRequest{})
	if err != nil || reply.GetDefaultVpcName() != "kcn-cluster" || reply.GetDefaultVpcUid() != "fixed-provider-uid" || len(reply.GetIntranetNetworks()) != 1 {
		t.Fatalf("Provider defaults not preserved: %v %v", reply, err)
	}
	for _, principal := range []*auth.Principal{
		{Type: auth.SubjectUser, ID: 9, TenantID: 1, Claims: claims, Roles: claims.Roles},
		{Type: auth.SubjectAPIKey, ID: 9, Claims: claims, Roles: claims.Roles},
	} {
		if _, err := s.GetIntranetPoolDefaults(auth.NewPrincipalContext(ctx, principal), &catalogv1.GetIntranetPoolDefaultsRequest{}); errors.Code(err) != 403 || p.calls != 1 {
			t.Fatalf("tenant/key read Provider defaults: %v", err)
		}
	}
	if _, err := s.GetIntranetPoolDefaults(ctx, nil); errors.Code(err) != 400 || p.calls != 1 {
		t.Fatalf("nil request reached downstream: %v", err)
	}
}

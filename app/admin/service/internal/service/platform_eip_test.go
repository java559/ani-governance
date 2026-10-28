package service

import (
	"context"
	"github.com/go-kratos/kratos/v2/errors"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	identityv1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
)

const platformEIPOwner = "11111111-1111-4111-8111-111111111111"
const platformEIPID = "eip_11111111111111111111111111111111"

type platformEIPProbe struct {
	NetworkPlatformClient
	list       *networkv1.ListPlatformEIPsRequest
	calls      int
	releaseErr error
	resource   *networkv1.PlatformEIP
}

func (p *platformEIPProbe) ListPlatformEIPs(_ context.Context, actor string, r *networkv1.ListPlatformEIPsRequest) (*networkv1.ListPlatformEIPsResponse, error) {
	if actor != "governance:user:9" {
		panic("untrusted actor")
	}
	p.calls++
	p.list = r
	return &networkv1.ListPlatformEIPsResponse{Items: []*networkv1.PlatformEIP{p.resource}, Total: 41}, nil
}
func (p *platformEIPProbe) GetPlatformEIP(_ context.Context, _ string, _ *networkv1.GetPlatformEIPRequest) (*networkv1.GetPlatformEIPResponse, error) {
	p.calls++
	return &networkv1.GetPlatformEIPResponse{Resource: p.resource}, nil
}
func (p *platformEIPProbe) ReleasePlatformEIP(_ context.Context, _ string, _ *networkv1.ReleasePlatformEIPRequest) (*networkv1.ReleasePlatformEIPResponse, error) {
	p.calls++
	return &networkv1.ReleasePlatformEIPResponse{Resource: p.resource}, p.releaseErr
}

type platformEIPTenants struct{}

func (platformEIPTenants) ResourceTenantID(context.Context, uint32) (string, error) {
	return platformEIPOwner, nil
}
func (platformEIPTenants) PlatformEIPTenants(context.Context, []string) (map[string]*identityv1.Tenant, error) {
	return map[string]*identityv1.Tenant{platformEIPOwner: {Id: proto.Uint32(17), Name: proto.String("tenant-A")}}, nil
}
func platformEIPFixture() *platformEIPProbe {
	return &platformEIPProbe{resource: &networkv1.PlatformEIP{Eip: &networkv1.EIP{Id: platformEIPID, TenantId: platformEIPOwner, Name: "office", State: networkv1.ResourceState_RESOURCE_STATE_AVAILABLE, Scope: "public", ManagedBy: "tenant", Address: "10.10.20.202", BindingState: "unbound"}, PoolId: "pool_11111111111111111111111111111111", PoolName: "egress-test", ClusterId: "cluster"}}
}
func platformEIPContext() context.Context {
	return auth.NewContext(context.Background(), &authv1.UserTokenPayload{UserId: 9, Roles: []string{"platform:admin"}, Permissions: []string{"network:platform:eip:list", "network:platform:eip:get", "network:platform:eip:release"}})
}
func TestPlatformSystemDefaultEgressEIP(t *testing.T) {
	p := platformEIPFixture()
	p.resource.Eip.TenantId, p.resource.Eip.ManagedBy = "", "system"
	p.resource.Eip.BindingTarget = &networkv1.EIPBindingTarget{Kind: "platform_default_egress", Id: "dve_11111111111111111111111111111111", State: "available"}
	s := NewPlatformNetworkService(p, platformEIPTenants{})
	got, err := s.GetPlatformEIP(platformEIPContext(), &catalogv1.GetPlatformEIPRequest{EipId: platformEIPID})
	if err != nil || got.Resource.ManagedBy != "system" || got.Resource.TenantId != 0 || got.Resource.ResourceTenantId != "" {
		t.Fatalf("system outlet forced into tenant ownership: %v %v", got, err)
	}
	p.resource.Eip.BindingTarget = nil
	if _, err = s.GetPlatformEIP(platformEIPContext(), &catalogv1.GetPlatformEIPRequest{EipId: platformEIPID}); err == nil {
		t.Fatal("anonymous owner accepted without platform outlet binding")
	}
}
func TestPlatformEIPMappingAndFilters(t *testing.T) {
	p := platformEIPFixture()
	s := NewPlatformNetworkService(p, platformEIPTenants{})
	ctx := platformEIPContext()
	page, size, tenant := uint32(3), uint32(2), uint32(17)
	got, err := s.ListPlatformEIPs(ctx, &catalogv1.ListPlatformEIPsRequest{Page: &page, PageSize: &size, TenantId: &tenant, Name: "office", Address: "10.10.20.202", PoolId: p.resource.PoolId, State: "available", Scope: "public"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 41 || len(got.Items) != 1 || got.Items[0].TenantId != 17 || got.Items[0].TenantName != "tenant-A" || got.Items[0].ResourceTenantId != platformEIPOwner || got.Items[0].PoolName != "egress-test" {
		t.Fatalf("owner or inventory lost: %v", got)
	}
	r := p.list
	if r.GetPage() != 3 || r.GetPageSize() != 2 || r.TenantId != platformEIPOwner || r.Name != "office" || r.Address != "10.10.20.202" || r.PoolId != p.resource.PoolId || r.Scope != "public" || r.State != networkv1.ResourceState_RESOURCE_STATE_AVAILABLE {
		t.Fatalf("filters changed: %v", r)
	}
	if _, err = s.ListPlatformEIPs(ctx, &catalogv1.ListPlatformEIPsRequest{}); err != nil || p.list.TenantId != "" {
		t.Fatalf("global scope was replaced with a tenant: %v", err)
	}
	if _, err = s.GetPlatformEIP(ctx, &catalogv1.GetPlatformEIPRequest{EipId: platformEIPID}); err != nil {
		t.Fatal(err)
	}
	p.resource.Eip.TenantId = "22222222-2222-4222-8222-222222222222"
	if _, err = s.ListPlatformEIPs(ctx, &catalogv1.ListPlatformEIPsRequest{TenantId: &tenant}); errors.Reason(err) != "NETWORK_INVALID_RESPONSE" {
		t.Fatalf("filtered cross-tenant reply accepted: %v", err)
	}
}
func TestPlatformEIPSubjectAndReleaseBoundaries(t *testing.T) {
	p := platformEIPFixture()
	s := NewPlatformNetworkService(p, platformEIPTenants{})
	for _, principal := range []*auth.Principal{
		{Type: auth.SubjectAPIKey, ID: 9, Roles: []string{"platform:admin"}, Claims: &authv1.UserTokenPayload{UserId: 9, Permissions: []string{"network:platform:eip:list"}}},
		{Type: auth.SubjectUser, ID: 9, TenantID: 17, Roles: []string{"platform:admin"}, Claims: &authv1.UserTokenPayload{UserId: 9, Permissions: []string{"network:platform:eip:list"}}},
		{Type: auth.SubjectUser, ID: 9, Roles: []string{"platform:admin"}, Claims: &authv1.UserTokenPayload{UserId: 9, Permissions: []string{"network:eip:list"}}},
	} {
		if _, err := s.ListPlatformEIPs(auth.NewPrincipalContext(context.Background(), principal), &catalogv1.ListPlatformEIPsRequest{}); errors.FromError(err).Code != 403 {
			t.Fatalf("subject admitted: %v", err)
		}
	}
	if p.calls != 0 {
		t.Fatal("unauthorized request reached Resource")
	}
	reader := auth.NewContext(context.Background(), &authv1.UserTokenPayload{UserId: 9, Roles: []string{"platform:admin"}, Permissions: []string{"network:platform:eip:list"}})
	if _, err := s.ReleasePlatformEIP(reader, &catalogv1.ReleasePlatformEIPRequest{EipId: platformEIPID}); errors.FromError(err).Code != 403 {
		t.Fatalf("list granted release: %v", err)
	}
	p.releaseErr = status.Error(codes.FailedPrecondition, "active binding")
	if _, err := s.ReleasePlatformEIP(platformEIPContext(), &catalogv1.ReleasePlatformEIPRequest{EipId: platformEIPID}); err == nil {
		t.Fatal("downstream release protection lost")
	}
	p.releaseErr = nil
	p.resource.Eip.State = networkv1.ResourceState_RESOURCE_STATE_DELETING
	got, err := s.ReleasePlatformEIP(platformEIPContext(), &catalogv1.ReleasePlatformEIPRequest{EipId: platformEIPID})
	if err != nil || got.GetResource().GetState() != "deleting" {
		t.Fatalf("release acceptance not mapped: %v %v", got, err)
	}
}

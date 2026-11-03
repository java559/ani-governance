package service

import (
	"context"
	"fmt"
	"github.com/go-kratos/kratos/v2/errors"
	network "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	view "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/middleware/auth"
	"testing"
)

// The probe replaces only the external Resource RPC boundary.
type securityGroupProbe struct {
	NetworkTenantClient
	NetworkSecurityGroupClient
	tenant, actor string
	wrong         bool
	withDefault   bool
	calls         int
}

func (p *securityGroupProbe) GetSecurityGroup(_ context.Context, t, a string, in *network.GetSecurityGroupRequest) (*network.GetSecurityGroupResponse, error) {
	p.calls++
	p.tenant, p.actor = t, a
	if p.wrong {
		t = "22222222-2222-4222-8222-222222222222"
	}
	return &network.GetSecurityGroupResponse{SecurityGroup: &network.SecurityGroup{Id: in.SecurityGroupId, TenantId: t, Version: 1, State: "available"}}, nil
}
func (p *securityGroupProbe) ListSecurityGroupRules(_ context.Context, t, a string, in *network.ListSecurityGroupRulesRequest) (*network.ListSecurityGroupRulesResponse, error) {
	p.calls++
	p.tenant, p.actor = t, a
	out := &network.ListSecurityGroupRulesResponse{Total: 5, Version: 7}
	if p.withDefault {
		out.Total++
		out.Items = append(out.Items, &network.SecurityGroupRule{Id: "sgr_default_ingress_drop", Direction: "ingress", Protocol: "all", Action: "drop", Priority: 1, IsDefault: true})
	}
	for i := 1; i <= 5; i++ {
		out.Items = append(out.Items, &network.SecurityGroupRule{Id: fmt.Sprintf("sgr_%032x", i), Direction: in.Direction, Protocol: "tcp", Action: "allow", Priority: int32(i)})
	}
	return out, nil
}
func TestSecurityGroupTrustedScopeAndReplyTenant(t *testing.T) {
	const id = "sg_0123456789abcdef0123456789abcdef"
	for _, subject := range []struct {
		typ   auth.SubjectType
		id    uint32
		actor string
	}{{auth.SubjectUser, 7, "governance:user:7"}, {auth.SubjectAPIKey, 42, "governance:access-key:42"}} {
		p := &securityGroupProbe{}
		s := NewNetworkService(p, &tenantProbe{})
		request := &view.GetSecurityGroupRequest{SecurityGroupId: id}
		if _, err := s.GetSecurityGroup(context.Background(), request); errors.Code(err) != 401 || p.calls != 0 {
			t.Fatal("anonymous scope passed", err)
		}
		ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: subject.typ, ID: subject.id, TenantID: 5})
		reply, err := s.GetSecurityGroup(ctx, request)
		if err != nil || reply.SecurityGroup.Id != id || p.tenant != "11111111-1111-4111-8111-111111111111" || p.actor != subject.actor {
			t.Fatal("trusted scope not preserved", err)
		}
		p.wrong = true
		if _, err = s.GetSecurityGroup(ctx, request); errors.Code(err) != 503 {
			t.Fatal("cross tenant response accepted", err)
		}
	}
}
func TestSecurityGroupRulesPagePreservesSnapshotTotalAndVersion(t *testing.T) {
	p := &securityGroupProbe{}
	s := NewNetworkService(p, &tenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 5})
	page, size := uint32(2), uint32(2)
	request := &view.ListSecurityGroupRulesRequest{SecurityGroupId: "sg_0123456789abcdef0123456789abcdef", Page: &page, PageSize: &size, Direction: "ingress"}
	reply, err := s.ListSecurityGroupRules(ctx, request)
	if err != nil || reply.Total != 5 || reply.Version != 7 || len(reply.Items) != 2 || reply.Items[0].Id != "sgr_00000000000000000000000000000003" {
		t.Fatal("wrong rule page", reply, err)
	}
	page = 4
	reply, err = s.ListSecurityGroupRules(ctx, request)
	if err != nil || reply.Total != 5 || len(reply.Items) != 0 {
		t.Fatal("wrong empty page", reply, err)
	}
}

func TestSecurityGroupWirePreservesExplicitSnakeCaseFields(t *testing.T) {
	in := &network.SecurityGroupRule{Id: "sgr_0123456789abcdef0123456789abcdef", RemoteAddresses: []string{"10.0.0.0/8"}, PortRanges: []*network.SecurityGroupPortRange{{Min: 22, Max: 22}}, IsDefault: true}
	out := new(view.SecurityGroupRule)
	if err := securityGroupWire(in, out); err != nil || len(out.RemoteAddresses) != 1 || len(out.PortRanges) != 1 || out.PortRanges[0].Min != 22 || !out.IsDefault {
		t.Fatal("rule fields lost", out, err)
	}
	binding := &network.SecurityGroupBinding{TargetType: "eip", TargetId: "eip_0123456789abcdef0123456789abcdef", SecurityGroupIds: []string{"sg_0123456789abcdef0123456789abcdef"}, AppliedSecurityGroupIds: []string{"sg_0123456789abcdef0123456789abcdef"}}
	public := new(view.SecurityGroupBinding)
	if err := securityGroupWire(binding, public); err != nil || len(public.SecurityGroupIds) != 1 || len(public.AppliedSecurityGroupIds) != 1 || public.TargetType != "eip" {
		t.Fatal("binding fields lost", public, err)
	}
}

func (p *securityGroupProbe) ListSecurityGroupBindings(_ context.Context, t, a string, in *network.ListSecurityGroupBindingsRequest) (*network.ListSecurityGroupBindingsResponse, error) {
	p.calls++
	p.tenant, p.actor = t, a
	return &network.ListSecurityGroupBindingsResponse{Total: 1, Items: []*network.SecurityGroupBinding{{TargetType: "eip", TargetId: "eip_0123456789abcdef0123456789abcdef", AppliedSecurityGroupIds: []string{in.SecurityGroupId}, State: "applying", Version: 2}}}, nil
}
func TestSecurityGroupBindingsRetainsAppliedReferenceDuringUnbind(t *testing.T) {
	p := &securityGroupProbe{}
	s := NewNetworkService(p, &tenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 5})
	reply, err := s.ListSecurityGroupBindings(ctx, &view.ListSecurityGroupBindingsRequest{SecurityGroupId: "sg_0123456789abcdef0123456789abcdef", TargetType: "eip"})
	if err != nil || reply.Total != 1 || len(reply.Items) != 1 || len(reply.Items[0].SecurityGroupIds) != 0 || len(reply.Items[0].AppliedSecurityGroupIds) != 1 {
		t.Fatal("pending applied reference lost", reply, err)
	}
}

func TestSecurityGroupDefaultIngressDropRemainsVisible(t *testing.T) {
	p := &securityGroupProbe{withDefault: true}
	s := NewNetworkService(p, &tenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7, TenantID: 5})
	reply, err := s.ListSecurityGroupRules(ctx, &view.ListSecurityGroupRulesRequest{SecurityGroupId: "sg_0123456789abcdef0123456789abcdef", Direction: "ingress"})
	if err != nil || reply.Total != 6 || len(reply.Items) != 6 || reply.Items[0].Id != "sgr_default_ingress_drop" || !reply.Items[0].IsDefault || reply.Items[0].Priority != 1 {
		t.Fatal("managed default missing", reply, err)
	}
}

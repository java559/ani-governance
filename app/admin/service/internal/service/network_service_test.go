package service

import (
	"context"
	"github.com/go-kratos/kratos/v2/errors"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	authv1 "go-wind-admin/api/gen/go/authentication/service/v1"
	catalogv1 "go-wind-admin/api/gen/go/catalog/service/v1"
	"go-wind-admin/pkg/localdeps/go-utils/trans"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"testing"
)

type vpcProbe struct {
	actor         string
	calls         int
	wrong         bool
	err           error
	subnetGateway *string
	lbCreate      *networkv1.CreateLoadBalancerRequest
	lbUpdate      *networkv1.UpdateLoadBalancerRequest
	lbList        *networkv1.ListLoadBalancersRequest
	snatEnabled   bool
	snatVersion   int64
	snatKey       string
}

func (p *vpcProbe) GetVPC(_ context.Context, tenant string, actor string, id string) (*networkv1.GetVPCResponse, error) {
	p.calls++
	expectedActor := p.actor
	if expectedActor == "" {
		expectedActor = "governance:user:7"
	}
	if actor != expectedActor || tenant != "11111111-1111-4111-8111-111111111111" {
		panic("untrusted scope")
	}
	if p.wrong {
		tenant = "other"
	}
	return &networkv1.GetVPCResponse{Vpc: &networkv1.VPC{Id: id, TenantId: tenant, Name: "vpc", State: networkv1.ResourceState_RESOURCE_STATE_AVAILABLE}}, p.err
}

// panicTenant panics on any unexpected replayed tenant/actor; it keeps the
// untrusted-scope assertion shared across every additional probe method.
func panicTenant(tenant, actor string) {
	if actor != "governance:user:7" && actor != "governance:access-key:42" {
		panic("untrusted scope")
	}
	if tenant != "11111111-1111-4111-8111-111111111111" {
		panic("untrusted scope")
	}
}

func (p *vpcProbe) ListVPCs(_ context.Context, tenant, actor string, _ string, _ string, _ int32, _ string) (*networkv1.ListVPCsResponse, error) {
	p.calls++
	panicTenant(tenant, actor)
	return &networkv1.ListVPCsResponse{Total: 37, Items: []*networkv1.VPC{{Id: "vpc_11111111111111111111111111111111", TenantId: tenant, State: networkv1.ResourceState_RESOURCE_STATE_AVAILABLE}}}, p.err
}

func (p *vpcProbe) CreateVPC(_ context.Context, tenant, actor string, _, _, _, _ string) (*networkv1.CreateVPCResponse, error) {
	p.calls++
	panicTenant(tenant, actor)
	return &networkv1.CreateVPCResponse{Vpc: &networkv1.VPC{Id: "vpc_11111111111111111111111111111111", TenantId: tenant, State: networkv1.ResourceState_RESOURCE_STATE_PROVISIONING}}, p.err
}

func (p *vpcProbe) DeleteVPC(_ context.Context, tenant, actor, id string) (*networkv1.DeleteVPCResponse, error) {
	p.calls++
	panicTenant(tenant, actor)
	return &networkv1.DeleteVPCResponse{Vpc: &networkv1.VPC{Id: id, TenantId: tenant, State: networkv1.ResourceState_RESOURCE_STATE_DELETED}}, p.err
}

func (p *vpcProbe) GetOperation(_ context.Context, tenant, actor, id string) (*networkv1.GetOperationResponse, error) {
	p.calls++
	panicTenant(tenant, actor)
	return &networkv1.GetOperationResponse{Operation: &networkv1.Operation{Id: id, TenantId: tenant, State: networkv1.OperationState_OPERATION_STATE_SUCCEEDED}}, p.err
}

func (p *vpcProbe) GetEIP(_ context.Context, tenant, actor, id string) (*networkv1.GetEIPResponse, error) {
	p.calls++
	panicTenant(tenant, actor)
	return &networkv1.GetEIPResponse{Eip: &networkv1.EIP{Id: id, TenantId: tenant, State: networkv1.ResourceState_RESOURCE_STATE_AVAILABLE}}, p.err
}

func (p *vpcProbe) ListEIPs(_ context.Context, tenant, actor string, _ string, _ string, _ int32, _ string) (*networkv1.ListEIPsResponse, error) {
	p.calls++
	panicTenant(tenant, actor)
	return &networkv1.ListEIPsResponse{Total: 37, Items: []*networkv1.EIP{{Id: "eip_11111111111111111111111111111111", TenantId: tenant, State: networkv1.ResourceState_RESOURCE_STATE_AVAILABLE}}}, p.err
}

func (p *vpcProbe) CreateEIP(_ context.Context, tenant, actor string, _, _, _ string) (*networkv1.CreateEIPResponse, error) {
	p.calls++
	panicTenant(tenant, actor)
	return &networkv1.CreateEIPResponse{Eip: &networkv1.EIP{Id: "eip_11111111111111111111111111111111", TenantId: tenant, State: networkv1.ResourceState_RESOURCE_STATE_PROVISIONING}}, p.err
}

func (p *vpcProbe) DeleteEIP(_ context.Context, tenant, actor, id string) (*networkv1.DeleteEIPResponse, error) {
	p.calls++
	panicTenant(tenant, actor)
	return &networkv1.DeleteEIPResponse{Eip: &networkv1.EIP{Id: id, TenantId: tenant, State: networkv1.ResourceState_RESOURCE_STATE_DELETED}}, p.err
}

func (p *vpcProbe) GetVPCSnat(_ context.Context, tenant, actor, vpcID string) (*networkv1.GetVPCSnatResponse, error) {
	p.calls++
	panicTenant(tenant, actor)
	return &networkv1.GetVPCSnatResponse{Binding: &networkv1.VPCSnatBinding{Id: "snat_1", TenantId: tenant, VpcId: vpcID, State: networkv1.ResourceState_RESOURCE_STATE_AVAILABLE}}, p.err
}

func (p *vpcProbe) BindVPCSnat(_ context.Context, tenant, actor, vpcID, eipID, _ string) (*networkv1.BindVPCSnatResponse, error) {
	p.calls++
	panicTenant(tenant, actor)
	return &networkv1.BindVPCSnatResponse{Binding: &networkv1.VPCSnatBinding{Id: "snat_1", TenantId: tenant, VpcId: vpcID, EipId: eipID, State: networkv1.ResourceState_RESOURCE_STATE_PROVISIONING}}, p.err
}

// tenantProbe 原先定义在已删除的 model_service_test.go 中，是 package service
// 共用的 ResourceTenantResolver 测试替身。
type tenantProbe struct{ calls int }

func (p *tenantProbe) ResourceTenantID(_ context.Context, id uint32) (string, error) {
	p.calls++
	if id != 5 {
		return "", errors.Forbidden("BAD_TENANT", "unexpected tenant")
	}
	return "11111111-1111-4111-8111-111111111111", nil
}
func TestNetworkTrustedScope(t *testing.T) {
	p, resolver := &vpcProbe{}, &tenantProbe{}
	s := NewNetworkService(p, resolver)
	req := &catalogv1.GetVPCRequest{VpcId: "vpc_11111111111111111111111111111111"}
	for _, ctx := range []context.Context{context.Background(), auth.NewContext(context.Background(), &authv1.UserTokenPayload{UserId: 7})} {
		if _, err := s.GetVPC(ctx, req); err == nil {
			t.Fatal("unauthenticated/platform accepted")
		}
	}
	if p.calls != 0 || resolver.calls != 0 {
		t.Fatal("rejected scope called dependencies")
	}
	ctx := auth.NewContext(context.Background(), &authv1.UserTokenPayload{TenantId: trans.Ptr(uint32(5)), UserId: 7})
	for _, id := range []string{"", "vpc_x", "vpc_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "../x"} {
		if _, err := s.GetVPC(ctx, &catalogv1.GetVPCRequest{VpcId: id}); errors.Code(err) != 400 {
			t.Fatalf("id %q: %v", id, err)
		}
	}
	if p.calls != 0 {
		t.Fatal("invalid ID called backend")
	}
	out, err := s.GetVPC(ctx, req)
	if err != nil || out.Vpc.State != "available" || p.calls != 1 {
		t.Fatalf("result %v %v", out, err)
	}
	p.wrong = true
	if _, err := s.GetVPC(ctx, req); errors.Code(err) != 503 {
		t.Fatalf("foreign response accepted: %v", err)
	}
	p.wrong = false
	for code, want := range map[codes.Code]int{codes.NotFound: 404, codes.InvalidArgument: 400, codes.PermissionDenied: 503, codes.Unauthenticated: 503, codes.Unavailable: 503, codes.DeadlineExceeded: 504} {
		p.err = status.Error(code, "private dependency detail")
		if _, err := s.GetVPC(ctx, req); errors.Code(err) != want {
			t.Fatalf("%v: %v", code, err)
		}
	}
	if _, err := NewNetworkService(nil, resolver).GetVPC(ctx, req); errors.Code(err) != 503 {
		t.Fatal("disabled dependency accepted")
	}
}

func TestNetworkTrustedKeyPrincipal(t *testing.T) {
	probe := &vpcProbe{actor: "governance:access-key:42"}
	service := NewNetworkService(probe, &tenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectAPIKey, ID: 42, TenantID: 5, Roles: []string{"reader"}})
	reply, err := service.GetVPC(ctx, &catalogv1.GetVPCRequest{VpcId: "vpc_11111111111111111111111111111111"})
	if err != nil || reply.GetVpc().GetId() != "vpc_11111111111111111111111111111111" || probe.calls != 1 {
		t.Fatalf("key query: %v", err)
	}
}

func TestNetworkListTotalsPassThroughTrustedScope(t *testing.T) {
	ctx := auth.NewContext(context.Background(), &authv1.UserTokenPayload{TenantId: trans.Ptr(uint32(5)), UserId: 7})
	s := NewNetworkService(&vpcProbe{}, &tenantProbe{})
	vpcs, err := s.ListVPCs(ctx, &catalogv1.ListVPCsRequest{Limit: 1})
	if err != nil || vpcs.GetTotal() != 37 || len(vpcs.GetItems()) != 1 {
		t.Fatalf("VPC total lost: %v %v", vpcs, err)
	}
	eips, err := s.ListEIPs(ctx, &catalogv1.ListEIPsRequest{Limit: 1})
	if err != nil || eips.GetTotal() != 37 || len(eips.GetItems()) != 1 {
		t.Fatalf("EIP total lost: %v %v", eips, err)
	}
}

const testSubnetID = "subnet_22222222222222222222222222222222"
const testVPCID = "vpc_11111111111111111111111111111111"
const testBindingID = "snat_33333333333333333333333333333333"
const testLBID = "lb_44444444444444444444444444444444"
const testOperationID = "55555555-5555-4555-8555-555555555555"

func (p *vpcProbe) scope(tenant, actor string) string {
	p.calls++
	panicTenant(tenant, actor)
	if p.wrong {
		return "foreign"
	}
	return tenant
}
func testSubnet(tenant, id, vpcID string) *networkv1.Subnet {
	return &networkv1.Subnet{Id: id, TenantId: tenant, VpcId: vpcID, Name: "subnet", Cidr: "10.0.1.0/24", Gateway: "10.0.1.1", State: networkv1.ResourceState_RESOURCE_STATE_AVAILABLE}
}
func (p *vpcProbe) CreateSubnet(_ context.Context, tenant, actor, vpcID, _, _, _, _ string, gateway *string) (*networkv1.CreateSubnetResponse, error) {
	p.subnetGateway = gateway
	return &networkv1.CreateSubnetResponse{Subnet: testSubnet(p.scope(tenant, actor), testSubnetID, vpcID)}, p.err
}
func (p *vpcProbe) GetSubnet(_ context.Context, tenant, actor, id string) (*networkv1.GetSubnetResponse, error) {
	return &networkv1.GetSubnetResponse{Subnet: testSubnet(p.scope(tenant, actor), id, testVPCID)}, p.err
}
func (p *vpcProbe) ListSubnets(_ context.Context, tenant, actor, vpcID, _, _ string, _ int32, _ string) (*networkv1.ListSubnetsResponse, error) {
	return &networkv1.ListSubnetsResponse{Items: []*networkv1.Subnet{testSubnet(p.scope(tenant, actor), testSubnetID, vpcID)}, Total: 37, NextCursor: "subnet-cursor"}, p.err
}
func (p *vpcProbe) DeleteSubnet(_ context.Context, tenant, actor, id string) (*networkv1.DeleteSubnetResponse, error) {
	return &networkv1.DeleteSubnetResponse{Subnet: testSubnet(p.scope(tenant, actor), id, testVPCID)}, p.err
}
func testSnat(tenant, id string) *networkv1.VPCSnatBinding {
	return &networkv1.VPCSnatBinding{Id: id, TenantId: tenant, VpcId: testVPCID, EipId: "eip_66666666666666666666666666666666", State: networkv1.ResourceState_RESOURCE_STATE_AVAILABLE, DesiredEnabled: true, ObservationStale: true, LastOperationId: testOperationID}
}
func (p *vpcProbe) GetVPCSnatBinding(_ context.Context, tenant, actor, id string) (*networkv1.GetVPCSnatBindingResponse, error) {
	return &networkv1.GetVPCSnatBindingResponse{Binding: testSnat(p.scope(tenant, actor), id)}, p.err
}
func (p *vpcProbe) SetVPCSnatEnabled(_ context.Context, tenant, actor, id string, enabled bool, version int64, key string) (*networkv1.SetVPCSnatEnabledResponse, error) {
	p.snatEnabled, p.snatVersion, p.snatKey = enabled, version, key
	return &networkv1.SetVPCSnatEnabledResponse{Binding: testSnat(p.scope(tenant, actor), id)}, p.err
}
func (p *vpcProbe) DeleteVPCSnatBinding(_ context.Context, tenant, actor, id string) (*networkv1.DeleteVPCSnatBindingResponse, error) {
	return &networkv1.DeleteVPCSnatBindingResponse{Binding: testSnat(p.scope(tenant, actor), id)}, p.err
}
func testLB(tenant, id string) *networkv1.LoadBalancer {
	return &networkv1.LoadBalancer{Id: id, TenantId: tenant, VpcId: testVPCID, SubnetId: testSubnetID, State: networkv1.ResourceState_RESOURCE_STATE_AVAILABLE, DesiredVersion: 8, AppliedVersion: 7, LastOperationId: testOperationID, DataPlaneState: networkv1.LoadBalancerDataPlaneState_LOAD_BALANCER_DATA_PLANE_STATE_UNKNOWN}
}
func testLBOperation(tenant string, kind networkv1.OperationKind) *networkv1.Operation {
	return &networkv1.Operation{Id: testOperationID, TenantId: tenant, ResourceId: testLBID, ResourceType: networkv1.ResourceType_RESOURCE_TYPE_LOAD_BALANCER, ReasonMessage: "Configuration accepted", Kind: kind, State: networkv1.OperationState_OPERATION_STATE_QUEUED}
}
func (p *vpcProbe) CreateLoadBalancer(_ context.Context, tenant, actor string, in *networkv1.CreateLoadBalancerRequest) (*networkv1.CreateLoadBalancerResponse, error) {
	p.lbCreate = in
	tenant = p.scope(tenant, actor)
	return &networkv1.CreateLoadBalancerResponse{LoadBalancer: testLB(tenant, testLBID), Operation: testLBOperation(tenant, networkv1.OperationKind_OPERATION_KIND_CREATE_LOAD_BALANCER)}, p.err
}
func (p *vpcProbe) GetLoadBalancer(_ context.Context, tenant, actor, id string) (*networkv1.GetLoadBalancerResponse, error) {
	return &networkv1.GetLoadBalancerResponse{LoadBalancer: testLB(p.scope(tenant, actor), id)}, p.err
}
func (p *vpcProbe) ListLoadBalancers(_ context.Context, tenant, actor string, in *networkv1.ListLoadBalancersRequest) (*networkv1.ListLoadBalancersResponse, error) {
	p.lbList = in
	return &networkv1.ListLoadBalancersResponse{Items: []*networkv1.LoadBalancer{testLB(p.scope(tenant, actor), testLBID)}, Total: 43, NextCursor: "lb-cursor"}, p.err
}
func (p *vpcProbe) UpdateLoadBalancer(_ context.Context, tenant, actor string, in *networkv1.UpdateLoadBalancerRequest) (*networkv1.UpdateLoadBalancerResponse, error) {
	p.lbUpdate = in
	tenant = p.scope(tenant, actor)
	return &networkv1.UpdateLoadBalancerResponse{LoadBalancer: testLB(tenant, testLBID), Operation: testLBOperation(tenant, networkv1.OperationKind_OPERATION_KIND_UPDATE_LOAD_BALANCER)}, p.err
}
func (p *vpcProbe) DeleteLoadBalancer(_ context.Context, tenant, actor, id string) (*networkv1.DeleteLoadBalancerResponse, error) {
	tenant = p.scope(tenant, actor)
	return &networkv1.DeleteLoadBalancerResponse{LoadBalancer: testLB(tenant, id), Operation: testLBOperation(tenant, networkv1.OperationKind_OPERATION_KIND_DELETE_LOAD_BALANCER)}, p.err
}
func (p *vpcProbe) GetLoadBalancerOperation(_ context.Context, tenant, actor, _ string) (*networkv1.GetLoadBalancerOperationResponse, error) {
	tenant = p.scope(tenant, actor)
	return &networkv1.GetLoadBalancerOperationResponse{Operation: testLBOperation(tenant, networkv1.OperationKind_OPERATION_KIND_CREATE_LOAD_BALANCER)}, p.err
}

func TestNetworkNewTenantMethodsRequireTrustedScope(t *testing.T) {
	probe := &vpcProbe{}
	s := NewNetworkService(probe, &tenantProbe{})
	calls := []struct {
		name string
		call func(context.Context) (proto.Message, error)
	}{
		{"CreateSubnet", func(ctx context.Context) (proto.Message, error) {
			return s.CreateSubnet(ctx, &catalogv1.CreateSubnetRequest{VpcId: testVPCID})
		}},
		{"GetSubnet", func(ctx context.Context) (proto.Message, error) {
			return s.GetSubnet(ctx, &catalogv1.GetSubnetRequest{SubnetId: testSubnetID})
		}},
		{"ListSubnets", func(ctx context.Context) (proto.Message, error) {
			return s.ListSubnets(ctx, &catalogv1.ListSubnetsRequest{VpcId: testVPCID})
		}},
		{"DeleteSubnet", func(ctx context.Context) (proto.Message, error) {
			return s.DeleteSubnet(ctx, &catalogv1.DeleteSubnetRequest{SubnetId: testSubnetID})
		}},
		{"GetVPCSnatBinding", func(ctx context.Context) (proto.Message, error) {
			return s.GetVPCSnatBinding(ctx, &catalogv1.GetVPCSnatBindingRequest{BindingId: testBindingID})
		}},
		{"SetVPCSnatEnabled", func(ctx context.Context) (proto.Message, error) {
			return s.SetVPCSnatEnabled(ctx, &catalogv1.SetVPCSnatEnabledRequest{BindingId: testBindingID})
		}},
		{"DeleteVPCSnatBinding", func(ctx context.Context) (proto.Message, error) {
			return s.DeleteVPCSnatBinding(ctx, &catalogv1.DeleteVPCSnatBindingRequest{BindingId: testBindingID})
		}},
		{"CreateLoadBalancer", func(ctx context.Context) (proto.Message, error) {
			return s.CreateLoadBalancer(ctx, &catalogv1.CreateLoadBalancerRequest{VpcId: testVPCID, SubnetId: testSubnetID})
		}},
		{"GetLoadBalancer", func(ctx context.Context) (proto.Message, error) {
			return s.GetLoadBalancer(ctx, &catalogv1.GetLoadBalancerRequest{LoadBalancerId: testLBID})
		}},
		{"ListLoadBalancers", func(ctx context.Context) (proto.Message, error) {
			return s.ListLoadBalancers(ctx, &catalogv1.ListLoadBalancersRequest{VpcId: testVPCID, SubnetId: testSubnetID})
		}},
		{"UpdateLoadBalancer", func(ctx context.Context) (proto.Message, error) {
			return s.UpdateLoadBalancer(ctx, &catalogv1.UpdateLoadBalancerRequest{LoadBalancerId: testLBID})
		}},
		{"DeleteLoadBalancer", func(ctx context.Context) (proto.Message, error) {
			return s.DeleteLoadBalancer(ctx, &catalogv1.DeleteLoadBalancerRequest{LoadBalancerId: testLBID})
		}},
		{"GetLoadBalancerOperation", func(ctx context.Context) (proto.Message, error) {
			return s.GetLoadBalancerOperation(ctx, &catalogv1.GetLoadBalancerOperationRequest{OperationId: testOperationID})
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			before := probe.calls
			if _, err := tc.call(context.Background()); errors.Code(err) != 401 || probe.calls != before {
				t.Fatalf("anonymous reached RPC: %v", err)
			}
			for _, principal := range []*auth.Principal{{Type: auth.SubjectUser, ID: 7, TenantID: 5}, {Type: auth.SubjectAPIKey, ID: 42, TenantID: 5}} {
				ctx := auth.NewPrincipalContext(context.Background(), principal)
				if _, err := tc.call(ctx); err != nil {
					t.Fatalf("trusted principal rejected: %v", err)
				}
				probe.wrong = true
				if _, err := tc.call(ctx); errors.Code(err) != 503 {
					t.Fatalf("foreign response accepted: %v", err)
				}
				probe.wrong = false
			}
		})
	}
}
func TestNetworkNewFieldsRetainIntentAndObservations(t *testing.T) {
	p := &vpcProbe{}
	s := NewNetworkService(p, &tenantProbe{})
	ctx := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectAPIKey, ID: 42, TenantID: 5})
	gateway := "10.0.1.254"
	if _, err := s.CreateSubnet(ctx, &catalogv1.CreateSubnetRequest{VpcId: testVPCID, Gateway: &gateway}); err != nil || p.subnetGateway == nil || *p.subnetGateway != gateway {
		t.Fatalf("gateway lost: %v", err)
	}
	subnets, err := s.ListSubnets(ctx, &catalogv1.ListSubnetsRequest{VpcId: testVPCID, Limit: 1})
	if err != nil || subnets.GetTotal() != 37 || subnets.GetNextCursor() != "subnet-cursor" {
		t.Fatalf("subnet page lost: %v", err)
	}
	snat, err := s.SetVPCSnatEnabled(ctx, &catalogv1.SetVPCSnatEnabledRequest{BindingId: testBindingID, Enabled: false, ExpectedVersion: 19, IdempotencyKey: "toggle-key"})
	if err != nil || p.snatEnabled || p.snatVersion != 19 || p.snatKey != "toggle-key" || snat.GetSnat().AppliedEnabled != nil || !snat.GetSnat().GetDesiredEnabled() || !snat.GetSnat().GetObservationStale() {
		t.Fatalf("SNAT intent/unknown observation lost: %v", err)
	}
	zero, port := uint32(0), uint32(80)
	backends := []*catalogv1.LoadBalancerBackendInput{{SubnetId: testSubnetID, Address: "10.0.1.2", Port: 80, Weight: &zero}}
	health := &catalogv1.LoadBalancerHealthCheck{Port: &port}
	lb, err := s.CreateLoadBalancer(ctx, &catalogv1.CreateLoadBalancerRequest{VpcId: testVPCID, SubnetId: testSubnetID, Backends: backends, HealthCheck: health})
	if err != nil || p.lbCreate.Backends[0].Weight == nil || *p.lbCreate.Backends[0].Weight != 0 || p.lbCreate.HealthCheck.Port == nil || lb.GetOperation() == nil || lb.GetOperation().GetReasonMessage() != "Configuration accepted" || lb.GetLoadBalancer().GetDesiredVersion() != 8 || lb.GetLoadBalancer().GetAppliedVersion() != 7 {
		t.Fatalf("LB accepted intent/snapshot lost: %v", err)
	}
	if _, err := s.UpdateLoadBalancer(ctx, &catalogv1.UpdateLoadBalancerRequest{LoadBalancerId: testLBID, ExpectedVersion: 8, IdempotencyKey: "update-key", Backends: backends, HealthCheck: health}); err != nil || p.lbUpdate.ExpectedVersion != 8 || p.lbUpdate.IdempotencyKey != "update-key" {
		t.Fatalf("LB mutation contract lost: %v", err)
	}
	for _, protocol := range []catalogv1.LoadBalancerListenerProtocol{catalogv1.LoadBalancerListenerProtocol_LOAD_BALANCER_LISTENER_PROTOCOL_HTTP, catalogv1.LoadBalancerListenerProtocol_LOAD_BALANCER_LISTENER_PROTOCOL_HTTPS, catalogv1.LoadBalancerListenerProtocol_LOAD_BALANCER_LISTENER_PROTOCOL_TCP, catalogv1.LoadBalancerListenerProtocol_LOAD_BALANCER_LISTENER_PROTOCOL_UDP, catalogv1.LoadBalancerListenerProtocol_LOAD_BALANCER_LISTENER_PROTOCOL_SSL} {
		input := &catalogv1.LoadBalancerListenerInput{Name: "parameters", Protocol: protocol, Port: &port, Backends: backends, Algorithm: catalogv1.LoadBalancerAlgorithm_LOAD_BALANCER_ALGORITHM_SOURCE_IP, CertificateRef: &catalogv1.LoadBalancerCertificateRef{Name: "tls", Namespace: "bound"}, SessionPersistence: &catalogv1.LoadBalancerSessionPersistence{Mode: catalogv1.LoadBalancerSessionMode_LOAD_BALANCER_SESSION_MODE_COOKIE_TIMEOUT, CookieExpirySeconds: 600}, MaxConnections: &zero, GzipEnabled: true, HttpsRedirectStatusCode: 302, HealthCheck: &catalogv1.LoadBalancerHealthCheck{Protocol: catalogv1.LoadBalancerHealthCheckProtocol_LOAD_BALANCER_HEALTH_CHECK_PROTOCOL_HTTP, Port: &zero, Path: "/health", Hostname: "health.example.com"}}
		if _, err := s.CreateLoadBalancer(ctx, &catalogv1.CreateLoadBalancerRequest{VpcId: testVPCID, SubnetId: testSubnetID, Listeners: &catalogv1.LoadBalancerListenerSet{Items: []*catalogv1.LoadBalancerListenerInput{input}}}); err != nil {
			t.Fatal(err)
		}
		got := p.lbCreate.Listeners.Items[0]
		if got.Protocol != networkv1.LoadBalancerListenerProtocol(protocol) || got.Algorithm != networkv1.LoadBalancerAlgorithm_LOAD_BALANCER_ALGORITHM_SOURCE_IP || got.CertificateRef.Name != "tls" || got.SessionPersistence.CookieExpirySeconds != 600 || got.MaxConnections == nil || *got.MaxConnections != 0 || !got.GzipEnabled || got.HttpsRedirectStatusCode != 302 || got.HealthCheck.Path != "/health" || got.HealthCheck.Hostname != "health.example.com" {
			t.Fatalf("listener intent lost: %v", got)
		}
		wire := wireLoadBalancer(&networkv1.LoadBalancer{Listeners: []*networkv1.LoadBalancerListener{{Id: "listener", Protocol: got.Protocol, Algorithm: got.Algorithm, CertificateRef: got.CertificateRef, SessionPersistence: got.SessionPersistence, MaxConnections: got.MaxConnections, GzipEnabled: got.GzipEnabled, HttpsRedirectStatusCode: got.HttpsRedirectStatusCode, HealthCheck: got.HealthCheck}}})
		if wire.Listener.Algorithm != input.Algorithm || wire.Listeners[0].CertificateRef.Namespace != "bound" || wire.Listener.HealthCheck.Path != "/health" || wire.Listener.MaxConnections == nil {
			t.Fatalf("listener readback lost: %v", wire)
		}
	}
	for _, ranges := range []*string{nil, func() *string { v := ""; return &v }(), func() *string { v := "10.42.1.2,10.42.1.4-10.42.1.9"; return &v }()} {
		got := wireSubnet(&networkv1.Subnet{AvailableIpRange: ranges, UsedIpRange: ranges, ObservationStale: true})
		if (got.AvailableIpRange == nil) != (ranges == nil) || (got.UsedIpRange == nil) != (ranges == nil) || ranges != nil && (*got.AvailableIpRange != *ranges || *got.UsedIpRange != *ranges) || !got.ObservationStale {
			t.Fatalf("subnet range presence lost: %v", got)
		}
	}
	lbs, err := s.ListLoadBalancers(ctx, &catalogv1.ListLoadBalancersRequest{VpcId: testVPCID, SubnetId: testSubnetID, State: "available", Limit: 1, Cursor: "input-cursor"})
	if err != nil || lbs.GetTotal() != 43 || lbs.GetNextCursor() != "lb-cursor" || p.lbList.State != networkv1.ResourceState_RESOURCE_STATE_AVAILABLE || p.lbList.Cursor != "input-cursor" {
		t.Fatalf("LB filtered page lost: %v", err)
	}
}
func TestNetworkLoadBalancerDeferredFields(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		desired, target, applied int64
		state                    networkv1.LoadBalancerConfigurationState
		deployment, reason       string
	}{
		{"saved", 1, 0, 0, networkv1.LoadBalancerConfigurationState_LOAD_BALANCER_CONFIGURATION_STATE_WAITING_FOR_BACKENDS, "waiting_for_backends", ""},
		{"mixed", 3, 2, 2, networkv1.LoadBalancerConfigurationState_LOAD_BALANCER_CONFIGURATION_STATE_CONFIGURED, "waiting_for_backends", "WAITING_FOR_REDIRECT_TARGET"},
		{"removing", 4, 4, 2, networkv1.LoadBalancerConfigurationState_LOAD_BALANCER_CONFIGURATION_STATE_APPLYING, "removing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := testLB("11111111-1111-4111-8111-111111111111", testLBID)
			v.DesiredVersion, v.ProviderTargetVersion, v.AppliedVersion = tc.desired, tc.target, tc.applied
			v.ConfigurationState = tc.state
			v.Listeners = []*networkv1.LoadBalancerListener{{Id: "listener", DeploymentState: tc.deployment, Reason: tc.reason}}
			got := wireLoadBalancer(v)
			if got.DesiredVersion != tc.desired || got.ProviderTargetVersion != tc.target || got.AppliedVersion != tc.applied || int32(got.ConfigurationState) != int32(tc.state) || got.Listener.DeploymentState != tc.deployment || got.Listeners[0].Reason != tc.reason || got.Listener != got.Listeners[0] || got.ObservedAt != nil || got.DataPlaneState != catalogv1.LoadBalancerDataPlaneState_LOAD_BALANCER_DATA_PLANE_STATE_UNKNOWN {
				t.Fatalf("deferred configuration or independent observation lost: %v", got)
			}
		})
	}
	legacy := wireLoadBalancer(&networkv1.LoadBalancer{Listener: &networkv1.LoadBalancerListener{Id: "legacy", DeploymentState: "unknown", Reason: "PROVIDER_NOT_READY"}})
	if legacy.Listener.DeploymentState != "unknown" || legacy.Listener.Reason != "PROVIDER_NOT_READY" {
		t.Fatalf("legacy listener summary lost deployment facts: %v", legacy)
	}
	for _, input := range []proto.Message{&catalogv1.LoadBalancerListenerInput{}, &catalogv1.CreateLoadBalancerRequest{}, &catalogv1.UpdateLoadBalancerRequest{}, &networkv1.LoadBalancerListenerInput{}} {
		fields := input.ProtoReflect().Descriptor().Fields()
		for _, name := range []protoreflect.Name{"deployment_state", "provider_target_version", "reason"} {
			if fields.ByName(name) != nil {
				t.Fatalf("read-only %s entered input contract %s", name, input.ProtoReflect().Descriptor().FullName())
			}
		}
	}
}
func TestNetworkDomainErrorReasons(t *testing.T) {
	for _, tc := range []struct {
		code   codes.Code
		reason string
		http   int
	}{{codes.AlreadyExists, "IDEMPOTENCY_CONFLICT", 409}, {codes.FailedPrecondition, "VERSION_CONFLICT", 412}, {codes.FailedPrecondition, "RESOURCE_IN_USE", 412}, {codes.NotFound, "RESOURCE_NOT_FOUND", 404}, {codes.Unavailable, "BASE_CONNECTIVITY_NOT_READY", 503}} {
		st, err := status.New(tc.code, "private dependency detail").WithDetails(&errdetails.ErrorInfo{Domain: "network.ani.io", Reason: tc.reason})
		if err != nil {
			t.Fatal(err)
		}
		mapped := errors.FromError(mapNetworkError(st.Err()))
		if int(mapped.Code) != tc.http || mapped.Reason != tc.reason || mapped.Message == "private dependency detail" {
			t.Fatalf("mapped error lost or leaked detail: %v", mapped)
		}
	}
}

func (p *vpcProbe) ListVPCCIDRPresets(_ context.Context, tenant, actor string) (*networkv1.ListVPCCIDRPresetsResponse, error) {
	p.calls++
	return &networkv1.ListVPCCIDRPresetsResponse{Cidrs: []string{"10.61.0.0/16"}}, p.err
}

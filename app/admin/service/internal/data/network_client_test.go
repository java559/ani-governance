package data

import (
	"context"
	"github.com/google/uuid"
	networkv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

type failedNetworkRPC struct {
	networkv1.NetworkServiceClient
	err error
}

func (p failedNetworkRPC) GetVPC(context.Context, *networkv1.GetVPCRequest, ...grpc.CallOption) (*networkv1.GetVPCResponse, error) {
	return nil, p.err
}
func TestNetworkClientPreservesRPCStatus(t *testing.T) {
	for _, code := range []codes.Code{codes.DeadlineExceeded, codes.Unavailable, codes.FailedPrecondition, codes.PermissionDenied} {
		downstream, err := status.New(code, "private downstream failure").WithDetails(&errdetails.ErrorInfo{Reason: "RESOURCE_CONFLICT", Domain: "resource.network"})
		if err != nil {
			t.Fatal(err)
		}
		c := &NetworkClient{client: failedNetworkRPC{err: downstream.Err()}, timeout: time.Second}
		_, got := c.GetVPC(context.Background(), "11111111-1111-4111-8111-111111111111", "governance:user:7", "vpc_11111111111111111111111111111111")
		if !proto.Equal(status.Convert(got).Proto(), downstream.Proto()) {
			t.Fatalf("downstream status or domain details changed: %v", got)
		}
	}
}

type networkRPCProbe struct {
	networkv1.NetworkServiceClient
	t    *testing.T
	wait bool
}

func (p networkRPCProbe) GetVPC(ctx context.Context, in *networkv1.GetVPCRequest, _ ...grpc.CallOption) (*networkv1.GetVPCResponse, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	if len(md.Get("evil")) != 0 || len(md.Get("x-ani-tenant-id")) != 1 || md.Get("x-ani-tenant-id")[0] != in.TenantId || md.Get("x-ani-actor")[0] != "governance:user:7" {
		p.t.Fatalf("metadata not rebuilt: %v", md)
	}
	if _, err := uuid.Parse(md.Get("x-ani-request-id")[0]); err != nil {
		p.t.Fatal(err)
	}
	if _, ok := ctx.Deadline(); !ok {
		p.t.Fatal("missing bounded deadline")
	}
	if p.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &networkv1.GetVPCResponse{}, nil
}
func TestNetworkClientIdentityAndCancellation(t *testing.T) {
	c := &NetworkClient{client: networkRPCProbe{t: t}, timeout: 20 * time.Millisecond}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("evil", "forwarded", "x-ani-tenant-id", "forged"))
	if _, err := c.GetVPC(ctx, "11111111-1111-4111-8111-111111111111", "governance:user:7", "vpc_11111111111111111111111111111111"); err != nil {
		t.Fatal(err)
	}
	c.client = networkRPCProbe{t: t, wait: true}
	if _, err := c.GetVPC(ctx, "11111111-1111-4111-8111-111111111111", "governance:user:7", "vpc_11111111111111111111111111111111"); err != context.DeadlineExceeded {
		t.Fatalf("timeout: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.GetVPC(canceled, "11111111-1111-4111-8111-111111111111", "governance:user:7", "vpc_11111111111111111111111111111111"); status.Code(err) != codes.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	if _, _, err := NewNetworkClient(NetworkClientConfig{}); err == nil {
		t.Fatal("missing TLS configuration accepted")
	}
}

type actorNetworkRPCProbe struct {
	networkv1.NetworkServiceClient
	actor string
	t     *testing.T
}

func (p actorNetworkRPCProbe) GetVPC(ctx context.Context, in *networkv1.GetVPCRequest, _ ...grpc.CallOption) (*networkv1.GetVPCResponse, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	if md.Get("x-ani-actor")[0] != p.actor || md.Get("x-ani-tenant-id")[0] != in.TenantId {
		p.t.Fatal("actor or tenant mismatch")
	}
	if len(md.Get("authorization")) != 0 || len(md.Get("x-signature")) != 0 {
		p.t.Fatal("public credentials forwarded")
	}
	return &networkv1.GetVPCResponse{}, nil
}
func TestNetworkClientKeyActor(t *testing.T) {
	c := &NetworkClient{client: actorNetworkRPCProbe{actor: "governance:access-key:42", t: t}, timeout: time.Second}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "secret", "x-signature", "secret"))
	if _, err := c.GetVPC(ctx, "11111111-1111-4111-8111-111111111111", "governance:access-key:42", "vpc_11111111111111111111111111111111"); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"governance:user:0", "governance:access-key:0", "governance:user:01", "governance:evil:1", "user:7", "governance:user:4294967296", "governance:access-key:4294967296"} {
		if _, err := c.GetVPC(ctx, "11111111-1111-4111-8111-111111111111", actor, "vpc_11111111111111111111111111111111"); err == nil {
			t.Fatalf("accepted %s", actor)
		}
	}
}

type networkTenantRPCProbe struct {
	networkv1.NetworkServiceClient
	networkv1.TenantEgressServiceClient
	networkv1.TenantLoadBalancerServiceClient
	t        *testing.T
	requests map[string]proto.Message
}

func (p *networkTenantRPCProbe) capture(ctx context.Context, tenant, method string, request proto.Message) {
	md, _ := metadata.FromOutgoingContext(ctx)
	if len(md) != 3 || len(md.Get("x-ani-tenant-id")) != 1 || md.Get("x-ani-tenant-id")[0] != tenant || tenant != "11111111-1111-4111-8111-111111111111" || len(md.Get("x-ani-actor")) != 1 || md.Get("x-ani-actor")[0] != "governance:access-key:42" || len(md.Get("x-ani-request-id")) != 1 {
		p.t.Fatalf("trusted metadata not rebuilt for %s", method)
	}
	if id, err := uuid.Parse(md.Get("x-ani-request-id")[0]); err != nil || id == uuid.Nil {
		p.t.Fatalf("invalid request identity for %s", method)
	}
	if _, ok := ctx.Deadline(); !ok {
		p.t.Fatal("missing bounded deadline")
	}
	if field := request.ProtoReflect().Descriptor().Fields().ByName("attribution"); field != nil {
		value := request.ProtoReflect().Get(field).Message()
		actor := value.Descriptor().Fields().ByName("actor")
		caller := value.Descriptor().Fields().ByName("direct_caller")
		correlation := value.Descriptor().Fields().ByName("correlation_id")
		if value.Get(actor).String() != md.Get("x-ani-actor")[0] || value.Get(caller).String() != "ani-governance" || value.Get(correlation).String() != md.Get("x-ani-request-id")[0] {
			p.t.Fatalf("attribution conflicts with trusted metadata for %s", method)
		}
	}
	p.requests[method] = request
}
func (p *networkTenantRPCProbe) CreateVPC(ctx context.Context, in *networkv1.CreateVPCRequest, _ ...grpc.CallOption) (*networkv1.CreateVPCResponse, error) {
	p.capture(ctx, in.TenantId, "CreateVPC", in)
	return &networkv1.CreateVPCResponse{}, nil
}
func (p *networkTenantRPCProbe) CreateEIP(ctx context.Context, in *networkv1.CreateEIPRequest, _ ...grpc.CallOption) (*networkv1.CreateEIPResponse, error) {
	p.capture(ctx, in.TargetTenantId, "CreateEIP", in)
	return &networkv1.CreateEIPResponse{}, nil
}

func TestNetworkClientCreateEIPRetainsInitialGroupsAndTrustedScope(t *testing.T) {
	p := &networkTenantRPCProbe{t: t, requests: make(map[string]proto.Message)}
	c := &NetworkClient{client: p, egress: p, timeout: time.Second}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "private", "x-ani-tenant-id", "forged", "x-ani-actor", "governance:user:999"))
	groups := []string{"sg_11111111111111111111111111111111"}
	tenant, actor := "11111111-1111-4111-8111-111111111111", "governance:access-key:42"
	if _, err := c.CreateEIP(ctx, tenant, "governance:user:0", "public", "desc", "eip-key", groups); err == nil || len(p.requests) != 0 {
		t.Fatal("invalid actor reached create")
	}
	if _, err := c.CreateEIP(ctx, tenant, actor, "public", "desc", "eip-key", groups); err != nil {
		t.Fatal(err)
	}
	in := p.requests["CreateEIP"].(*networkv1.CreateEIPRequest)
	if in.Name != "public" || in.Description != "desc" || in.IdempotencyKey != "eip-key" || len(in.SecurityGroupIds) != 1 || in.SecurityGroupIds[0] != groups[0] {
		t.Fatalf("atomic initial groups create intent lost: %v", in)
	}
	in.SecurityGroupIds[0] = "changed"
	if groups[0] == "changed" {
		t.Fatal("request aliases caller's groups")
	}
}
func (p *networkTenantRPCProbe) CreateSubnet(ctx context.Context, in *networkv1.CreateSubnetRequest, _ ...grpc.CallOption) (*networkv1.CreateSubnetResponse, error) {
	p.capture(ctx, in.TenantId, "CreateSubnet", in)
	return &networkv1.CreateSubnetResponse{}, nil
}
func (p *networkTenantRPCProbe) GetSubnet(ctx context.Context, in *networkv1.GetSubnetRequest, _ ...grpc.CallOption) (*networkv1.GetSubnetResponse, error) {
	p.capture(ctx, in.TenantId, "GetSubnet", in)
	return &networkv1.GetSubnetResponse{}, nil
}
func (p *networkTenantRPCProbe) ListSubnets(ctx context.Context, in *networkv1.ListSubnetsRequest, _ ...grpc.CallOption) (*networkv1.ListSubnetsResponse, error) {
	p.capture(ctx, in.TenantId, "ListSubnets", in)
	return &networkv1.ListSubnetsResponse{}, nil
}
func (p *networkTenantRPCProbe) DeleteSubnet(ctx context.Context, in *networkv1.DeleteSubnetRequest, _ ...grpc.CallOption) (*networkv1.DeleteSubnetResponse, error) {
	p.capture(ctx, in.TenantId, "DeleteSubnet", in)
	return &networkv1.DeleteSubnetResponse{}, nil
}
func (p *networkTenantRPCProbe) GetVPCSnatBinding(ctx context.Context, in *networkv1.GetVPCSnatBindingRequest, _ ...grpc.CallOption) (*networkv1.GetVPCSnatBindingResponse, error) {
	p.capture(ctx, in.TargetTenantId, "GetVPCSnatBinding", in)
	return &networkv1.GetVPCSnatBindingResponse{}, nil
}
func (p *networkTenantRPCProbe) SetVPCSnatEnabled(ctx context.Context, in *networkv1.SetVPCSnatEnabledRequest, _ ...grpc.CallOption) (*networkv1.SetVPCSnatEnabledResponse, error) {
	p.capture(ctx, in.TargetTenantId, "SetVPCSnatEnabled", in)
	return &networkv1.SetVPCSnatEnabledResponse{}, nil
}
func (p *networkTenantRPCProbe) DeleteVPCSnatBinding(ctx context.Context, in *networkv1.DeleteVPCSnatBindingRequest, _ ...grpc.CallOption) (*networkv1.DeleteVPCSnatBindingResponse, error) {
	p.capture(ctx, in.TargetTenantId, "DeleteVPCSnatBinding", in)
	return &networkv1.DeleteVPCSnatBindingResponse{}, nil
}
func (p *networkTenantRPCProbe) CreateLoadBalancer(ctx context.Context, in *networkv1.CreateLoadBalancerRequest, _ ...grpc.CallOption) (*networkv1.CreateLoadBalancerResponse, error) {
	p.capture(ctx, in.TargetTenantId, "CreateLoadBalancer", in)
	return &networkv1.CreateLoadBalancerResponse{}, nil
}
func (p *networkTenantRPCProbe) GetLoadBalancer(ctx context.Context, in *networkv1.GetLoadBalancerRequest, _ ...grpc.CallOption) (*networkv1.GetLoadBalancerResponse, error) {
	p.capture(ctx, in.TargetTenantId, "GetLoadBalancer", in)
	return &networkv1.GetLoadBalancerResponse{}, nil
}
func (p *networkTenantRPCProbe) ListLoadBalancers(ctx context.Context, in *networkv1.ListLoadBalancersRequest, _ ...grpc.CallOption) (*networkv1.ListLoadBalancersResponse, error) {
	p.capture(ctx, in.TargetTenantId, "ListLoadBalancers", in)
	return &networkv1.ListLoadBalancersResponse{}, nil
}
func (p *networkTenantRPCProbe) UpdateLoadBalancer(ctx context.Context, in *networkv1.UpdateLoadBalancerRequest, _ ...grpc.CallOption) (*networkv1.UpdateLoadBalancerResponse, error) {
	p.capture(ctx, in.TargetTenantId, "UpdateLoadBalancer", in)
	return &networkv1.UpdateLoadBalancerResponse{}, nil
}
func (p *networkTenantRPCProbe) DeleteLoadBalancer(ctx context.Context, in *networkv1.DeleteLoadBalancerRequest, _ ...grpc.CallOption) (*networkv1.DeleteLoadBalancerResponse, error) {
	p.capture(ctx, in.TargetTenantId, "DeleteLoadBalancer", in)
	return &networkv1.DeleteLoadBalancerResponse{}, nil
}
func (p *networkTenantRPCProbe) GetLoadBalancerOperation(ctx context.Context, in *networkv1.GetLoadBalancerOperationRequest, _ ...grpc.CallOption) (*networkv1.GetLoadBalancerOperationResponse, error) {
	p.capture(ctx, in.TargetTenantId, "GetLoadBalancerOperation", in)
	return &networkv1.GetLoadBalancerOperationResponse{}, nil
}
func TestNetworkClientAllNewMethodsRebuildMetadata(t *testing.T) {
	probe := &networkTenantRPCProbe{t: t, requests: make(map[string]proto.Message)}
	c := &NetworkClient{client: probe, egress: probe, loadBalancers: probe, timeout: time.Second}
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("evil", "public", "x-ani-tenant-id", "forged", "x-ani-actor", "governance:user:999", "authorization", "private", "x-signature", "private"))
	tenant, actor := "11111111-1111-4111-8111-111111111111", "governance:access-key:42"
	gateway := "10.0.1.254"
	zero, port := uint32(0), uint32(80)
	createLB := &networkv1.CreateLoadBalancerRequest{TargetTenantId: "forged", VpcId: "vpc", SubnetId: "subnet", IdempotencyKey: "create-key", Backends: []*networkv1.LoadBalancerBackendInput{{Weight: &zero}}, HealthCheck: &networkv1.LoadBalancerHealthCheck{Port: &port}}
	updateLB := &networkv1.UpdateLoadBalancerRequest{TargetTenantId: "forged", LoadBalancerId: "lb", ExpectedVersion: 19, IdempotencyKey: "update-key"}
	listLB := &networkv1.ListLoadBalancersRequest{TargetTenantId: "forged", VpcId: "vpc", SubnetId: "subnet", Limit: 7, Cursor: "lb-cursor", Exposure: networkv1.LoadBalancerExposure_LOAD_BALANCER_EXPOSURE_PRIVATE, State: networkv1.ResourceState_RESOURCE_STATE_AVAILABLE}
	calls := []struct {
		name string
		call func(string) error
	}{
		{"CreateVPC", func(a string) error {
			_, err := c.CreateVPC(ctx, tenant, a, "vpc", "10.0.0.0/16", "description", "vpc-key")
			return err
		}},
		{"CreateSubnet", func(a string) error {
			_, err := c.CreateSubnet(ctx, tenant, a, "vpc", "subnet", "10.0.1.0/24", "description", "subnet-key", &gateway)
			return err
		}},
		{"GetSubnet", func(a string) error { _, err := c.GetSubnet(ctx, tenant, a, "subnet"); return err }},
		{"ListSubnets", func(a string) error {
			_, err := c.ListSubnets(ctx, tenant, a, "vpc", "name", "AVAILABLE", 7, "subnet-cursor")
			return err
		}},
		{"DeleteSubnet", func(a string) error { _, err := c.DeleteSubnet(ctx, tenant, a, "subnet"); return err }},
		{"GetVPCSnatBinding", func(a string) error { _, err := c.GetVPCSnatBinding(ctx, tenant, a, "binding"); return err }},
		{"SetVPCSnatEnabled", func(a string) error {
			_, err := c.SetVPCSnatEnabled(ctx, tenant, a, "binding", false, 19, "toggle-key")
			return err
		}},
		{"DeleteVPCSnatBinding", func(a string) error { _, err := c.DeleteVPCSnatBinding(ctx, tenant, a, "binding"); return err }},
		{"CreateLoadBalancer", func(a string) error { _, err := c.CreateLoadBalancer(ctx, tenant, a, createLB); return err }},
		{"GetLoadBalancer", func(a string) error { _, err := c.GetLoadBalancer(ctx, tenant, a, "lb"); return err }},
		{"ListLoadBalancers", func(a string) error { _, err := c.ListLoadBalancers(ctx, tenant, a, listLB); return err }},
		{"UpdateLoadBalancer", func(a string) error { _, err := c.UpdateLoadBalancer(ctx, tenant, a, updateLB); return err }},
		{"DeleteLoadBalancer", func(a string) error { _, err := c.DeleteLoadBalancer(ctx, tenant, a, "lb"); return err }},
		{"GetLoadBalancerOperation", func(a string) error { _, err := c.GetLoadBalancerOperation(ctx, tenant, a, "operation"); return err }},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call("governance:user:0"); err == nil {
				t.Fatal("invalid actor accepted")
			}
			if probe.requests[tc.name] != nil {
				t.Fatal("invalid actor reached RPC")
			}
			if err := tc.call(actor); err != nil {
				t.Fatal(err)
			}
		})
	}
	subnet := probe.requests["CreateSubnet"].(*networkv1.CreateSubnetRequest)
	if subnet.Gateway == nil || *subnet.Gateway != gateway || subnet.Attribution.GetActor() != actor || subnet.Attribution.GetDirectCaller() != "ani-governance" || subnet.IdempotencyKey != "subnet-key" {
		t.Fatal("subnet accepted intent changed")
	}
	subnetList := probe.requests["ListSubnets"].(*networkv1.ListSubnetsRequest)
	if subnetList.VpcId != "vpc" || subnetList.Cursor != "subnet-cursor" || subnetList.Limit != 7 || subnetList.State != networkv1.ResourceState_RESOURCE_STATE_AVAILABLE {
		t.Fatal("subnet filter changed")
	}
	toggle := probe.requests["SetVPCSnatEnabled"].(*networkv1.SetVPCSnatEnabledRequest)
	if toggle.Enabled || toggle.ExpectedVersion != 19 || toggle.IdempotencyKey != "toggle-key" {
		t.Fatal("SNAT mutation changed")
	}
	capturedCreate := probe.requests["CreateLoadBalancer"].(*networkv1.CreateLoadBalancerRequest)
	if capturedCreate == createLB || capturedCreate.Backends[0].Weight == nil || *capturedCreate.Backends[0].Weight != 0 || capturedCreate.HealthCheck.Port == nil {
		t.Fatal("LB cloned intent changed")
	}
	if createLB.TargetTenantId != "forged" || updateLB.TargetTenantId != "forged" || listLB.TargetTenantId != "forged" {
		t.Fatal("client mutated caller request")
	}
}

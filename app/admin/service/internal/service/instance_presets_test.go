package service

import (
	"context"
	"strings"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	view "go-wind-admin/api/gen/go/compute/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const presetTenant = "20000000-0000-4000-8000-000000000002"
const presetOtherTenant = "30000000-0000-4000-8000-000000000003"

type instancePresetOwnerProbe struct {
	InstanceOwnerClient
	t        *testing.T
	tenant   string
	requests []*res.ListPresetsRequest
	reply    func(int, *res.ListPresetsRequest) (*res.ListPresetsResponse, error)
}

func (p *instancePresetOwnerProbe) ListPresets(_ context.Context, tenant, actor string, r *res.ListPresetsRequest) (*res.ListPresetsResponse, error) {
	p.t.Helper()
	if tenant != p.tenant || r.TenantId != p.tenant || actor != "governance:user:9" || r.Usage != res.Usage_CONTAINER_INSTANCE {
		p.t.Fatalf("untrusted owner identity: %s %s %#v", tenant, actor, r)
	}
	p.requests = append(p.requests, proto.Clone(r).(*res.ListPresetsRequest))
	return p.reply(len(p.requests), r)
}

type instancePresetAuthorization struct {
	instanceErr, catalogErr     error
	instanceCalls, catalogCalls int
}

func (a *instancePresetAuthorization) Authorize(_ context.Context, tenant, actor uint32, path, method string) error {
	a.instanceCalls++
	if (tenant != 7 && tenant != 8) || actor != 9 || path != data.InstancePresetPath || method != "GET" {
		return data.ErrActionAuthorizationDenied
	}
	return a.instanceErr
}
func (a *instancePresetAuthorization) AuthorizeCatalog(_ context.Context, tenant, actor uint32) error {
	a.catalogCalls++
	if (tenant != 7 && tenant != 8) || actor != 9 {
		return data.ErrActionAuthorizationDenied
	}
	return a.catalogErr
}

type instancePresetTenantResolver struct{}

func (instancePresetTenantResolver) ResourceTenantID(_ context.Context, id uint32) (string, error) {
	switch id {
	case 7:
		return presetTenant, nil
	case 8:
		return presetOtherTenant, nil
	default:
		return "", data.ErrActionAuthorizationDenied
	}
}

type instancePresetCatalogProbe struct {
	t               *testing.T
	cluster, tenant string
	requests        []*acc.ListProfilesRequest
	reply           func(int, *acc.ListProfilesRequest) (*acc.ListProfilesResponse, error)
	fitReply        func(*acc.CheckGpuFitRequest) (*acc.GpuFit, error)
}

func (p *instancePresetCatalogProbe) ListProfiles(_ context.Context, r *acc.ListProfilesRequest, _ ...grpc.CallOption) (*acc.ListProfilesResponse, error) {
	p.t.Helper()
	if r == nil || r.Context == nil || r.Context.Actor == nil || r.Context.TenantId != p.tenant || r.Context.Actor.Type != "user" || r.Context.Actor.Id != "9" || r.ClusterId != p.cluster || r.Page == nil || r.Page.Size != 200 {
		p.t.Fatalf("invalid delegated catalog request %#v", r)
	}
	if id, err := uuid.Parse(r.Context.RequestId); err != nil || id.String() != r.Context.RequestId {
		p.t.Fatal("missing trusted request identity")
	}
	p.requests = append(p.requests, proto.Clone(r).(*acc.ListProfilesRequest))
	return p.reply(len(p.requests), r)
}
func (p *instancePresetCatalogProbe) CheckGpuFit(_ context.Context, r *acc.CheckGpuFitRequest, _ ...grpc.CallOption) (*acc.GpuFit, error) {
	if p.fitReply != nil {
		return p.fitReply(r)
	}
	return &acc.GpuFit{Status: acc.FitStatus_FIT_ESTIMATE, EstimateKnown: true, EstimatedReplicas: 1, Observation: &acc.Observation{Complete: true}}, nil
}
func (p *instancePresetCatalogProbe) GetCapacity(context.Context, *acc.GetCapacityRequest, ...grpc.CallOption) (*acc.CapacityView, error) {
	p.t.Fatal("unexpected multi-device capacity request")
	return nil, nil
}
func instancePresetContext(tenant uint32) context.Context {
	return auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, TenantID: tenant, ID: 9})
}
func instancePresetService(owner *instancePresetOwnerProbe, a InstanceActionAuthorizer, catalog InstanceGPUCatalog, cluster string) *InstanceService {
	return NewInstanceService(nil, NewInstanceGpuBinding(owner, a, cluster), instancePresetTenantResolver{}, nil, nil, InstanceCatalogConfig{ClusterID: cluster, Compute: owner, Catalog: catalog})
}
func instancePresetBuilder(t *testing.T) *gpuPlanFixture {
	t.Helper()
	plan, _, _ := gpuVector(t)
	plan.Profile.Spec.Mode = acc.SupplyMode_WHOLE_EXCLUSIVE
	plan.Profile.Spec.MemoryMibPerDevice = 15360
	plan.Profile.Spec.CoreLimitPercent = 100
	plan.Profile.Spec.MaxDevicesPerReplica = 2
	plan.Profile.Spec.IsolationClass = "WHOLE_DEVICE_EXCLUSIVE"
	plan.Profile.SpecDigest, _ = gpuSpecDigest(plan.Profile)

	return plan
}
func instancePresetCatalogPage(plan *gpuPlanFixture) *acc.ListProfilesResponse {
	return &acc.ListProfilesResponse{Items: []*acc.GpuProfile{proto.Clone(plan.Profile).(*acc.GpuProfile)}, Scopes: []*acc.ProfileScope{{ProfileId: plan.Profile.ProfileId, ProfileVersion: plan.Profile.ProfileVersion, SpecDigest: plan.Profile.SpecDigest, ClusterId: plan.Request.ClusterId, PoolId: plan.Request.PoolId}}}
}
func instancePresetCPU() *res.Preset {
	return &res.Preset{Id: "40000000-0000-4000-8000-000000000004", Code: "cpu-visible", Name: "CPU safe result", ComputeType: "cpu", CpuMillis: 1000, MemoryMib: 1024, Enabled: true}
}

// The boundary returns pre-counted pages; pagination/filtering SQL is exercised
// by Resource's real-PG tests, rather than reimplemented inside this test.
func TestInstancePresetCatalogAllPagesAuthorizeBeforeFinalCount(t *testing.T) {
	plan := instancePresetBuilder(t)
	first := instancePresetCatalogPage(plan)
	second := instancePresetCatalogPage(plan)
	// Another published identity can reuse the same valid Spec/Baseline. Identity
	// and publication are catalog metadata, not inputs to SpecDigest.
	second.Items[0].ProfileId = "50000000-0000-4000-8000-000000000005"
	second.Items[0].ProfileVersion = 7
	second.Scopes[0].ProfileId = second.Items[0].ProfileId
	second.Scopes[0].ProfileVersion = 7
	first.NextToken = "second-page"
	catalog := &instancePresetCatalogProbe{t: t, cluster: plan.Request.ClusterId, tenant: presetTenant, reply: func(n int, r *acc.ListProfilesRequest) (*acc.ListProfilesResponse, error) {
		switch n {
		case 1:
			if r.Page.Token != "" {
				t.Fatal("initial token")
			}
			return first, nil
		case 2:
			if r.Page.Token != "second-page" {
				t.Fatal("pagination token lost")
			}
			return second, nil
		default:
			t.Fatal("unexpected ACC page")
			return nil, nil
		}
	}}
	page, size := uint32(3), uint32(2)
	owner := &instancePresetOwnerProbe{t: t, tenant: presetTenant, reply: func(n int, r *res.ListPresetsRequest) (*res.ListPresetsResponse, error) {
		if r.Page != page || r.PageSize != size || r.Name != "match" || r.Code != "" || r.GpuMode != "" {
			t.Fatalf("filters/page lost %#v", r)
		}
		if n == 1 {
			if len(r.AllowedGpuProfiles) != 0 {
				t.Fatal("initial request exposed GPU")
			}
			return &res.ListPresetsResponse{Items: []*res.Preset{}, Total: 2, HasGpuCandidates: true}, nil
		}
		if n != 2 || len(r.AllowedGpuProfiles) != 2 {
			t.Fatalf("missing authorized final request %#v", r)
		}
		for index, scope := range []*acc.ProfileScope{first.Scopes[0], second.Scopes[0]} {
			wanted := &res.GpuPresetReference{ProfileId: scope.ProfileId, ProfileVersion: scope.ProfileVersion, SpecDigest: scope.SpecDigest, ClusterId: scope.ClusterId, PoolId: scope.PoolId}
			if !proto.Equal(wanted, r.AllowedGpuProfiles[index]) {
				t.Fatalf("authorization tuple lost: got %v want %v", r.AllowedGpuProfiles[index], wanted)
			}
		}
		return &res.ListPresetsResponse{Items: []*res.Preset{{Id: "60000000-0000-4000-8000-000000000006", Code: "gpu-visible", ComputeType: "gpu", Gpu: &res.GpuPresetReference{ProfileId: second.Items[0].ProfileId, ProfileVersion: 7, Mode: acc.SupplyMode_WHOLE_EXCLUSIVE, DevicesPerInstance: 2, CoreLimitPercent: 100}}}, Total: 5, HasGpuCandidates: true}, nil
	}}
	authorization := &instancePresetAuthorization{}
	s := instancePresetService(owner, authorization, catalog, plan.Request.ClusterId)
	result, err := s.ListInstancePresets(instancePresetContext(7), &view.ListPresetsRequest{Page: &page, PageSize: &size, Name: "match"})
	if err != nil || result.Total != 5 || len(result.Items) != 1 || result.Items[0].Code != "gpu-visible" || result.GpuCatalogStatus != "OK" || result.GpuCatalogReason != "" {
		t.Fatalf("final page/count not taken atomically from final owner response: %v %v", result, err)
	}
	if len(owner.requests) != 2 || len(catalog.requests) != 2 || authorization.instanceCalls != 1 || authorization.catalogCalls != 1 {
		t.Fatal("unexpected authorization/RPC counts")
	}
}

func TestInstancePresetCatalogFaultsPreserveSafeCPUPage(t *testing.T) {
	plan := instancePresetBuilder(t)
	for _, tc := range []struct {
		name            string
		mutate          func(*acc.ListProfilesResponse)
		rpcErr, gateErr error
		status          string
		catalogCalls    int
	}{
		{name: "catalog denied", rpcErr: status.Error(codes.PermissionDenied, "private ACC tenant message"), status: "NO_PERMISSION", catalogCalls: 1},
		{name: "catalog unauthenticated", rpcErr: status.Error(codes.Unauthenticated, "private service identity"), status: "NO_PERMISSION", catalogCalls: 1},
		{name: "catalog timeout", rpcErr: status.Error(codes.DeadlineExceeded, "10.0.0.1 credential"), status: "UNAVAILABLE", catalogCalls: 1},
		{name: "gate denied", gateErr: data.ErrActionAuthorizationDenied, status: "NO_PERMISSION"},
		{name: "gate unavailable", gateErr: data.ErrActionAuthorizationUnavailable, status: "UNAVAILABLE"},
		{name: "missing scope", mutate: func(r *acc.ListProfilesResponse) { r.Scopes = nil }, status: "UNAVAILABLE", catalogCalls: 1},
		{name: "mismatched scope digest", mutate: func(r *acc.ListProfilesResponse) { r.Scopes[0].SpecDigest = strings.Repeat("f", 64) }, status: "UNAVAILABLE", catalogCalls: 1},
		{name: "scope wrong cluster", mutate: func(r *acc.ListProfilesResponse) { r.Scopes[0].ClusterId = uuid.NewString() }, status: "UNAVAILABLE", catalogCalls: 1},
		{name: "scope missing pool", mutate: func(r *acc.ListProfilesResponse) { r.Scopes[0].PoolId = "" }, status: "UNAVAILABLE", catalogCalls: 1},
		{name: "scope nil UUID pool", mutate: func(r *acc.ListProfilesResponse) { r.Scopes[0].PoolId = uuid.Nil.String() }, status: "UNAVAILABLE", catalogCalls: 1},
		{name: "coherent malformed digest", mutate: func(r *acc.ListProfilesResponse) {
			r.Scopes[0].SpecDigest = "invalid"
			r.Items[0].SpecDigest = "invalid"
		}, status: "UNAVAILABLE", catalogCalls: 1},
		{name: "invalid profile state", mutate: func(r *acc.ListProfilesResponse) { r.Items[0].State = acc.ProfileState_PROFILE_STATE_UNSPECIFIED }, status: "UNAVAILABLE", catalogCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := instancePresetCatalogPage(plan)
			if tc.mutate != nil {
				tc.mutate(reply)
			}
			catalog := &instancePresetCatalogProbe{t: t, cluster: plan.Request.ClusterId, tenant: presetTenant, reply: func(int, *acc.ListProfilesRequest) (*acc.ListProfilesResponse, error) { return reply, tc.rpcErr }}
			owner := &instancePresetOwnerProbe{t: t, tenant: presetTenant, reply: func(n int, r *res.ListPresetsRequest) (*res.ListPresetsResponse, error) {
				if n != 1 || len(r.AllowedGpuProfiles) != 0 {
					t.Fatal("unsafe final request on ACC failure")
				}
				return &res.ListPresetsResponse{Items: []*res.Preset{instancePresetCPU()}, Total: 9, HasGpuCandidates: true}, nil
			}}
			s := instancePresetService(owner, &instancePresetAuthorization{catalogErr: tc.gateErr}, catalog, plan.Request.ClusterId)
			page, size := uint32(5), uint32(2)
			result, err := s.ListInstancePresets(instancePresetContext(7), &view.ListPresetsRequest{Page: &page, PageSize: &size})
			if err != nil || result.GpuCatalogStatus != tc.status || result.Total != 9 || len(result.Items) != 1 || result.Items[0].ComputeType != "cpu" {
				t.Fatalf("safe degraded result %v %v", result, err)
			}
			expectedReason := "GPU_CATALOG_UNAVAILABLE"
			if tc.status == "NO_PERMISSION" {
				expectedReason = "GPU_CATALOG_NO_PERMISSION"
			}
			if result.GpuCatalogReason != expectedReason {
				t.Fatalf("reason leaked underlying failure: %q", result.GpuCatalogReason)
			}
			if len(owner.requests) != 1 || len(catalog.requests) != tc.catalogCalls {
				t.Fatal("unexpected fault RPC count")
			}
		})
	}
}

func TestInstancePresetCatalogEmptyDirectoryUsesFinalSnapshot(t *testing.T) {
	plan := instancePresetBuilder(t)
	catalog := &instancePresetCatalogProbe{t: t, cluster: plan.Request.ClusterId, tenant: presetTenant, reply: func(int, *acc.ListProfilesRequest) (*acc.ListProfilesResponse, error) {
		return &acc.ListProfilesResponse{}, nil
	}}
	other := instancePresetCPU()
	other.Id = "70000000-0000-4000-8000-000000000007"
	other.Code = "cpu-second"
	owner := &instancePresetOwnerProbe{t: t, tenant: presetTenant, reply: func(n int, r *res.ListPresetsRequest) (*res.ListPresetsResponse, error) {
		if len(r.AllowedGpuProfiles) != 0 {
			t.Fatal("empty authorization became allow all")
		}
		if n == 1 {
			return &res.ListPresetsResponse{Items: []*res.Preset{instancePresetCPU(), other}, Total: 9, HasGpuCandidates: true}, nil
		}
		if n != 2 {
			t.Fatal("extra owner query")
		}
		return &res.ListPresetsResponse{Items: []*res.Preset{instancePresetCPU(), other}, Total: 6, HasGpuCandidates: true}, nil
	}}
	page, size := uint32(3), uint32(2)
	result, err := instancePresetService(owner, &instancePresetAuthorization{}, catalog, plan.Request.ClusterId).ListInstancePresets(instancePresetContext(7), &view.ListPresetsRequest{Page: &page, PageSize: &size})
	if err != nil || result.GpuCatalogStatus != "OK" || result.Total != 6 || len(result.Items) != 2 || len(owner.requests) != 2 || len(catalog.requests) != 1 {
		t.Fatalf("empty directory used stale first snapshot: %v %v", result, err)
	}
}

func TestInstancePresetCatalogRejectsRepeatedTokensAndIdentities(t *testing.T) {
	plan := instancePresetBuilder(t)
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "repeated token", true: "repeated profile"}[duplicate], func(t *testing.T) {
			catalog := &instancePresetCatalogProbe{t: t, cluster: plan.Request.ClusterId, tenant: presetTenant, reply: func(n int, _ *acc.ListProfilesRequest) (*acc.ListProfilesResponse, error) {
				reply := instancePresetCatalogPage(plan)
				reply.NextToken = "cycle"
				if n == 2 && !duplicate {
					reply.Items[0].ProfileVersion = 2
					reply.Scopes[0].ProfileVersion = 2
				}
				if n > 2 {
					t.Fatal("unbounded pagination")
				}
				return reply, nil
			}}
			owner := &instancePresetOwnerProbe{t: t, tenant: presetTenant, reply: func(n int, r *res.ListPresetsRequest) (*res.ListPresetsResponse, error) {
				if n != 1 {
					t.Fatal("partial GPU authorization escaped")
				}
				return &res.ListPresetsResponse{Items: []*res.Preset{instancePresetCPU()}, Total: 1, HasGpuCandidates: true}, nil
			}}
			result, err := instancePresetService(owner, &instancePresetAuthorization{}, catalog, plan.Request.ClusterId).ListInstancePresets(instancePresetContext(7), &view.ListPresetsRequest{})
			if err != nil || result.GpuCatalogStatus != "UNAVAILABLE" || result.Total != 1 || len(catalog.requests) != 2 {
				t.Fatalf("pagination corruption accepted: %v %v", result, err)
			}
		})
	}
}

func TestInstancePresetCatalogSkipsACCAndKeepsTrustedTenant(t *testing.T) {
	for _, tc := range []struct {
		name, compute string
		hasCandidates bool
		tenant        uint32
	}{
		{name: "pure CPU", compute: "cpu", hasCandidates: true, tenant: 7},
		{name: "no GPU candidate", tenant: 7},
		{name: "different trusted tenant", tenant: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenant := presetTenant
			if tc.tenant == 8 {
				tenant = presetOtherTenant
			}
			owner := &instancePresetOwnerProbe{t: t, tenant: tenant, reply: func(n int, r *res.ListPresetsRequest) (*res.ListPresetsResponse, error) {
				if n != 1 || r.Page != 1 || r.PageSize != 20 || r.ComputeType != tc.compute {
					t.Fatal("safe request defaults/filter")
				}
				return &res.ListPresetsResponse{Items: []*res.Preset{}, Total: 0, HasGpuCandidates: tc.hasCandidates}, nil
			}}
			catalog := &instancePresetCatalogProbe{t: t, reply: func(int, *acc.ListProfilesRequest) (*acc.ListProfilesResponse, error) {
				t.Fatal("unnecessary ACC call")
				return nil, nil
			}}
			authorization := &instancePresetAuthorization{}
			result, err := instancePresetService(owner, authorization, catalog, "cluster").ListInstancePresets(instancePresetContext(tc.tenant), &view.ListPresetsRequest{ComputeType: tc.compute})
			if err != nil || result.GpuCatalogStatus != "OK" || result.Items == nil || result.Total != 0 || authorization.catalogCalls != 0 || len(catalog.requests) != 0 {
				t.Fatalf("CPU/no candidate depends on ACC: %v %v", result, err)
			}
		})
	}
}

func TestInstancePresetCatalogRequiresInstancePermission(t *testing.T) {
	owner := &instancePresetOwnerProbe{t: t, tenant: presetTenant, reply: func(int, *res.ListPresetsRequest) (*res.ListPresetsResponse, error) {
		t.Fatal("permission bypass called Resource")
		return nil, nil
	}}
	authorization := &instancePresetAuthorization{instanceErr: data.ErrActionAuthorizationDenied}
	s := instancePresetService(owner, authorization, nil, "cluster")
	if _, err := s.ListInstancePresets(instancePresetContext(7), &view.ListPresetsRequest{}); kratoserrors.Code(err) != 403 {
		t.Fatalf("instance permission: %v", err)
	}
	if len(owner.requests) != 0 || authorization.catalogCalls != 0 {
		t.Fatal("denied request reached dependencies")
	}
	if _, err := s.ListInstancePresets(context.Background(), &view.ListPresetsRequest{}); err == nil {
		t.Fatal("unauthenticated caller accepted")
	}
}

func TestInstancePresetCatalogKeepsAuthorizedInactiveProfilesVisible(t *testing.T) {
	for state, reason := range map[acc.ProfileState]string{acc.ProfileState_PROFILE_DISABLED: "GPU_PROFILE_DISABLED", acc.ProfileState_PROFILE_DELETED: "GPU_PROFILE_DELETED"} {
		t.Run(state.String(), func(t *testing.T) {
			plan := instancePresetBuilder(t)
			plan.Profile.State = state
			catalog := &instancePresetCatalogProbe{t: t, cluster: plan.Request.ClusterId, tenant: presetTenant, reply: func(int, *acc.ListProfilesRequest) (*acc.ListProfilesResponse, error) {
				return instancePresetCatalogPage(plan), nil
			}}
			owner := &instancePresetOwnerProbe{t: t, tenant: presetTenant, reply: func(n int, r *res.ListPresetsRequest) (*res.ListPresetsResponse, error) {
				if n == 1 {
					return &res.ListPresetsResponse{Items: []*res.Preset{instancePresetCPU()}, Total: 1, HasGpuCandidates: true}, nil
				}
				if len(r.AllowedGpuProfiles) != 1 {
					t.Fatal("authorized scope lost because profile is inactive")
				}
				return &res.ListPresetsResponse{Items: []*res.Preset{{Id: "60000000-0000-4000-8000-000000000006", Code: "gpu-visible", ComputeType: "gpu", Enabled: true, Selectable: true, Gpu: &res.GpuPresetReference{ProfileId: plan.Profile.ProfileId, ProfileVersion: plan.Profile.ProfileVersion}}}, Total: 1, HasGpuCandidates: true}, nil
			}}
			result, err := instancePresetService(owner, &instancePresetAuthorization{}, catalog, plan.Request.ClusterId).ListInstancePresets(instancePresetContext(7), &view.ListPresetsRequest{})
			if err != nil || len(result.Items) != 1 || result.Items[0].Selectable || result.Items[0].UnavailableReason != reason {
				t.Fatalf("inactive profile reason: %v %v", result, err)
			}
		})
	}
}

func TestInstancePresetAvailabilityReasons(t *testing.T) {
	accError := func(reason string) error {
		st, e := status.New(codes.FailedPrecondition, "managed source precondition").WithDetails(&errdetails.ErrorInfo{Reason: reason})
		if e != nil {
			t.Fatal(e)
		}
		return st.Err()
	}
	for _, test := range []struct {
		name, want string
		fit        *acc.GpuFit
		err        error
	}{
		{name: "unknown", want: "GPU_CAPACITY_UNKNOWN", fit: &acc.GpuFit{Status: acc.FitStatus_FIT_UNKNOWN, Observation: &acc.Observation{}}},
		{name: "full", want: "GPU_TEMPORARILY_FULL", fit: &acc.GpuFit{Status: acc.FitStatus_WAIT_FOR_CAPACITY, EstimateKnown: true, Observation: &acc.Observation{Complete: true}}},
		{name: "catalog", want: "GPU_CATALOG_UNAVAILABLE", err: status.Error(codes.Unavailable, "offline")},
		{name: "paused", want: "GPU_SUPPLY_PAUSED", err: accError("ADMISSION_CLOSED")},
		{name: "source unknown", want: "GPU_CAPACITY_UNKNOWN", err: accError("DEVICE_CAPACITY_UNKNOWN")},
		{name: "configuration drift", want: "GPU_SUPPLY_CONFIG_MISMATCH", err: accError("CONFIG_DRIFT")},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := instancePresetBuilder(t)
			catalog := &instancePresetCatalogProbe{t: t, cluster: plan.Request.ClusterId, tenant: presetTenant, fitReply: func(r *acc.CheckGpuFitRequest) (*acc.GpuFit, error) {
				if r.Context.TenantId != presetTenant || r.Context.Actor.Id != "9" || r.Gpu.ProfileId != plan.Profile.ProfileId || r.Gpu.DevicesPerReplica != 1 {
					t.Fatal("availability lost authorized identity")
				}
				return test.fit, test.err
			}}
			svc := instancePresetService(nil, &instancePresetAuthorization{}, catalog, plan.Request.ClusterId)
			got := svc.presetGPUAvailability(instancePresetContext(7), presetTenant, &res.GpuPresetReference{ProfileId: plan.Profile.ProfileId, ProfileVersion: 1, ClusterId: plan.Request.ClusterId, PoolId: plan.Request.PoolId, DevicesPerInstance: 1})
			if got != test.want {
				t.Fatalf("reason %s want %s", got, test.want)
			}
		})
	}
}

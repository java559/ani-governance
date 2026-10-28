//go:build quota_pg

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	obs "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/gpu/v1"
	view "go-wind-admin/api/gen/go/gpu/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Probes replace only ACC and Obs RPCs; the list, snapshot, identity mapping,
// platform guard and GPU target construction all use production constructors.
type gpuTenantListACCProbe struct {
	acc.AcceleratorAdminServiceClient
	mu                                     sync.Mutex
	cluster                                string
	scopes                                 map[string][]*acc.GpuMetricScope
	bindings                               []*acc.GpuBinding
	clusterCalls, bindingCalls, scopeCalls int
	cycle                                  bool
}

func (p *gpuTenantListACCProbe) ListClusters(_ context.Context, req *acc.ListClustersRequest, _ ...grpc.CallOption) (*acc.ListClustersResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clusterCalls++
	if req.Context == nil || req.Context.Actor == nil {
		return nil, status.Error(codes.PermissionDenied, "missing platform actor")
	}
	r := &acc.ListClustersResponse{Items: []*acc.Cluster{{ClusterId: p.cluster}}}
	if p.cycle {
		r.Items = nil
		r.NextToken = "repeat"
	}
	return r, nil
}
func (p *gpuTenantListACCProbe) AdminListBindings(_ context.Context, req *acc.AdminListBindingsRequest, _ ...grpc.CallOption) (*acc.ListBindingsResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bindingCalls++
	if req.ClusterId != p.cluster || req.PhysicalDeviceId != "" {
		return nil, status.Error(codes.InvalidArgument, "unexpected inventory filter")
	}
	return &acc.ListBindingsResponse{Items: p.bindings, Observation: &acc.Observation{Complete: true, SnapshotRef: "allocation-snapshot"}}, nil
}
func (p *gpuTenantListACCProbe) ListAdminGpuMetricScopes(_ context.Context, req *acc.AdminGpuMetricScopesRequest, _ ...grpc.CallOption) (*acc.GpuMetricScopesResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scopeCalls++
	if req.Context == nil || req.Context.Actor == nil || req.Ref != nil {
		return nil, status.Error(codes.PermissionDenied, "invalid platform scope")
	}
	return &acc.GpuMetricScopesResponse{Items: p.scopes[req.TenantId], SnapshotId: "usage-snapshot", IndexComplete: true}, nil
}

type gpuTenantListObsProbe struct {
	mu                      sync.Mutex
	active, maximum, calls  int
	failTenant              string
	delay                   time.Duration
	err                     error
	extraSeries, extraPoint bool
}

func (p *gpuTenantListObsProbe) QueryGpuLatest(ctx context.Context, platform bool, tenant, actor string, req *obs.QueryGpuLatestRequest) (*obs.QueryGpuResponse, error) {
	if !platform || tenant != "" || actor == "" || req.Target.TenantId == "" {
		return nil, status.Error(codes.PermissionDenied, "wrong trusted transport identity")
	}
	p.mu.Lock()
	p.calls++
	p.active++
	p.maximum = max(p.maximum, p.active)
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.active--; p.mu.Unlock() }()
	select {
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case <-time.After(p.delay):
	}
	if p.err != nil {
		return nil, p.err
	}
	if req.Target.TenantId == p.failTenant {
		return nil, status.Error(codes.Unavailable, "secret-pod secret-token secret-node")
	}
	r := &obs.QueryGpuResponse{QueryTime: timestamppb.Now(), IndexComplete: true}
	for _, code := range req.MetricCodes {
		r.Series = append(r.Series, &obs.GpuSeries{MetricCode: code, Unit: "bytes", Quality: &obs.GpuQuality{Status: "PARTIAL"}, Points: []*obs.GpuPoint{{Timestamp: timestamppb.Now(), KnownValue: proto.Float64(512), Quality: &obs.GpuQuality{Status: "PARTIAL"}}}})
	}
	if p.extraSeries {
		r.Series = append(r.Series, proto.Clone(r.Series[0]).(*obs.GpuSeries))
	}
	if p.extraPoint {
		r.Series[0].Points = append(r.Series[0].Points, proto.Clone(r.Series[0].Points[0]).(*obs.GpuPoint))
	}
	return r, nil
}
func (p *gpuTenantListObsProbe) QueryGpuRange(context.Context, bool, string, string, *obs.QueryGpuRangeRequest) (*obs.QueryGpuResponse, error) {
	return nil, status.Error(codes.Internal, "tenant current list must never expand historical curves")
}

func newGpuTenantListFixture(t *testing.T, count int) (*GpuTenantAdminService, *gpuMonitoringFixture, *gpuTenantListACCProbe, *gpuTenantListObsProbe, []*ent.Tenant, context.Context) {
	t.Helper()
	f := newGpuMonitoringFixture(t)
	sys := appViewer.NewSystemViewerContext(context.Background())
	tenants := []*ent.Tenant{f.tenant}
	for i := 1; i < count; i++ {
		name := "gpu-page-" + uuid.NewString()
		tenant, err := f.client.Tenant.Create().SetName(name).SetCode(name).SetNillablePlanID(f.tenant.PlanID).Save(sys)
		require.NoError(t, err)
		tenants = append(tenants, tenant)
	}
	peer := &gpuTenantListACCProbe{cluster: f.scope.ClusterId, scopes: map[string][]*acc.GpuMetricScope{}}
	for _, tenant := range tenants {
		scope := proto.Clone(f.scope).(*acc.GpuMetricScope)
		scope.Ref = proto.Clone(f.ref).(*acc.GpuUsageRef)
		scope.Ref.TenantId = tenant.ResourceTenantID
		scope.PodName, scope.PodUid = uuid.NewString(), uuid.NewString()
		peer.scopes[tenant.ResourceTenantID] = []*acc.GpuMetricScope{scope}
		peer.bindings = append(peer.bindings, &acc.GpuBinding{BindingId: uuid.NewString(), VerifiedUsageRef: scope.Ref, Pod: &acc.PodRef{ClusterId: peer.cluster, Namespace: scope.Namespace, Name: scope.PodName, Uid: scope.PodUid, ContainerName: scope.ContainerName}, PhysicalDeviceId: scope.DeviceId, State: acc.AllocationState_ACTIVE, MemoryMib: 8192, Observation: &acc.Observation{Complete: true}})
	}
	monitor := &gpuTenantListObsProbe{delay: 20 * time.Millisecond}
	f.svc.accelerator.client.Admin = peer
	f.svc.monitoring = monitor
	c := testutil.NewQuotaPGClient(t)
	quota := data.NewQuotaAdminRepo(testutil.NewBootstrapContext(nil), c)
	platform := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7})
	return NewGpuTenantAdminService(quota, f.svc, f.svc.accelerator), f, peer, monitor, tenants, platform
}

func TestGpuTenantListCurrentQuotaUsageAllocation(t *testing.T) {
	svc, f, accPeer, monitor, tenants, platform := newGpuTenantListFixture(t, 3)
	monitor.failTenant = tenants[1].ResourceTenantID
	out, err := svc.ListGpuTenants(platform, &view.ListGpuTenantsRequest{})
	require.NoError(t, err)
	require.EqualValues(t, 3, out.Total)
	require.Len(t, out.Items, 3)
	require.Equal(t, 2, monitor.maximum, "only two tenant observations run concurrently")
	require.Equal(t, 1, accPeer.clusterCalls)
	require.Equal(t, 1, accPeer.bindingCalls, "allocation inventory is not queried per tenant")
	require.Equal(t, 3, accPeer.scopeCalls)
	for i, row := range out.Items {
		require.Equal(t, tenants[i].ID, row.Quota.TenantId)
		require.Equal(t, "AVAILABLE", row.Allocation.Status)
		require.Equal(t, "8192", row.Allocation.GetMemoryMib(), "ACC commitment is independent of actual 512-byte known usage and quota")
		require.EqualValues(t, 1, row.Allocation.GetBindingCount())
		if i == 1 {
			require.Nil(t, row.Usage)
			require.Equal(t, "UNAVAILABLE", row.UsageStatus)
			require.Equal(t, "GPU_SOURCE_UNAVAILABLE", row.UsageReason, "internal errors never become row reasons")
		} else {
			require.Equal(t, "PARTIAL", row.UsageStatus)
			require.Len(t, row.Usage.Series, 2)
			require.Nil(t, row.Usage.Series[0].Points[0].Value)
			require.Equal(t, 512.0, row.Usage.Series[0].Points[0].GetKnownValue())
		}
	}
	account := out.Items[0].Quota.Accounts
	found := false
	for _, a := range account {
		if a.QuotaCode == GpuSharedQuotaCode {
			found = true
			require.Equal(t, "8192", a.Occupied)
			require.Equal(t, "100000", a.Limit)
		}
	}
	require.True(t, found)
	require.Equal(t, 0, f.acc.tenantCalls, "platform observations never invent a tenant transport context")
	_, err = svc.ListGpuTenants(f.ctx, &view.ListGpuTenantsRequest{})
	require.Error(t, err)
	_, err = svc.ListGpuTenants(auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectAPIKey, ID: 7}), &view.ListGpuTenantsRequest{})
	require.Error(t, err)
	_, err = svc.ListGpuTenants(platform, &view.ListGpuTenantsRequest{PageSize: proto.Uint32(7)})
	require.Error(t, err)
	out, err = svc.ListGpuTenants(platform, &view.ListGpuTenantsRequest{Page: proto.Uint32(2)})
	require.NoError(t, err)
	require.Empty(t, out.Items)
	require.EqualValues(t, 3, out.Total)
}

func TestGpuTenantListUnknownAllocationNeverBecomesZero(t *testing.T) {
	svc, _, peer, monitor, _, platform := newGpuTenantListFixture(t, 1)
	unknown := proto.Clone(peer.bindings[0]).(*acc.GpuBinding)
	unknown.BindingId, unknown.Pod.Uid = uuid.NewString(), uuid.NewString()
	unknown.VerifiedUsageRef = nil
	unknown.State = acc.AllocationState_ALLOCATION_UNKNOWN
	peer.bindings = append(peer.bindings, unknown)
	out, err := svc.ListGpuTenants(platform, &view.ListGpuTenantsRequest{})
	require.NoError(t, err)
	allocation := out.Items[0].Allocation
	require.Equal(t, "PARTIAL", allocation.Status)
	require.Nil(t, allocation.MemoryMib)
	require.Nil(t, allocation.BindingCount)
	require.Equal(t, "8192", allocation.GetKnownMemoryMib())
	require.EqualValues(t, 1, allocation.GetKnownBindingCount())
	peer.bindings = []*acc.GpuBinding{unknown}
	out, err = svc.ListGpuTenants(platform, &view.ListGpuTenantsRequest{})
	require.NoError(t, err)
	require.Nil(t, out.Items[0].Allocation.KnownMemoryMib, "no observed trusted allocation is unknown, not known zero")
	require.Nil(t, out.Items[0].Allocation.KnownBindingCount)
	monitor.extraSeries = true
	out, err = svc.ListGpuTenants(platform, &view.ListGpuTenantsRequest{})
	require.NoError(t, err)
	require.Equal(t, "GPU_INVALID_RESPONSE", out.Items[0].UsageReason)
	require.Nil(t, out.Items[0].Usage, "unexpected series must not evade the fixed page curve budget")
	monitor.extraSeries, monitor.extraPoint = false, true
	out, err = svc.ListGpuTenants(platform, &view.ListGpuTenantsRequest{})
	require.NoError(t, err)
	require.Equal(t, "GPU_INVALID_RESPONSE", out.Items[0].UsageReason)
	require.Nil(t, out.Items[0].Usage, "current list never expands historical points")
}

func TestGpuTenantListBudgetAndDeadlineFailWholePage(t *testing.T) {
	svc, _, peer, monitor, tenants, platform := newGpuTenantListFixture(t, 2)
	for _, tenant := range tenants {
		first := peer.scopes[tenant.ResourceTenantID][0]
		for i := 1; i < 65; i++ {
			scope := proto.Clone(first).(*acc.GpuMetricScope)
			scope.PodName = uuid.NewString()
			peer.scopes[tenant.ResourceTenantID] = append(peer.scopes[tenant.ResourceTenantID], scope)
		}
	}
	out, err := svc.ListGpuTenants(platform, &view.ListGpuTenantsRequest{})
	require.Nil(t, out)
	require.EqualValues(t, 429, kerrors.Code(err), "two individually valid tenant scopes cannot bypass the page source budget")
	require.LessOrEqual(t, monitor.calls, 1)
	for _, tenant := range tenants {
		peer.scopes[tenant.ResourceTenantID] = peer.scopes[tenant.ResourceTenantID][:1]
	}
	monitor.delay = time.Second
	short, cancel := context.WithTimeout(platform, 10*time.Millisecond)
	defer cancel()
	out, err = svc.ListGpuTenants(short, &view.ListGpuTenantsRequest{})
	require.Nil(t, out)
	require.EqualValues(t, 504, kerrors.Code(err))
	peer.cycle = true
	out, err = svc.ListGpuTenants(platform, &view.ListGpuTenantsRequest{})
	require.NoError(t, err, "ordinary allocation dependency failure stays explicit on each row")
	require.Equal(t, "UNAVAILABLE", out.Items[0].Allocation.Status)
}

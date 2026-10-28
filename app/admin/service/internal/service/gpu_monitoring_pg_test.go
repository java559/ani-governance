//go:build quota_pg

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	runtimev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	obs "github.com/zhangzhe-ctrl/ani-observability-service/api/observability/gpu/v1"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/gpu/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	crudviewer "go-wind-admin/pkg/localdeps/go-crud/viewer"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Only generated ACC/Obs and business-owner RPC boundaries are substituted.
// Mapping, original ledger reads, resource authorization and DTOs are real Gov.
type gpuMonitoringACCProbe struct {
	acc.AcceleratorAdminServiceClient
	acc.AcceleratorUsageServiceClient
	tenantCalls, adminCalls, deviceCalls int
	tenant                               func(*acc.TenantGpuMetricScopesRequest) (*acc.GpuMetricScopesResponse, error)
	admin                                func(*acc.AdminGpuMetricScopesRequest) (*acc.GpuMetricScopesResponse, error)
	devices                              func(*acc.ListDevicesRequest) (*acc.ListDevicesResponse, error)
}

func (p *gpuMonitoringACCProbe) ListTenantGpuMetricScopes(_ context.Context, r *acc.TenantGpuMetricScopesRequest, _ ...grpc.CallOption) (*acc.GpuMetricScopesResponse, error) {
	p.tenantCalls++
	return p.tenant(r)
}
func (p *gpuMonitoringACCProbe) ListAdminGpuMetricScopes(_ context.Context, r *acc.AdminGpuMetricScopesRequest, _ ...grpc.CallOption) (*acc.GpuMetricScopesResponse, error) {
	p.adminCalls++
	return p.admin(r)
}
func (p *gpuMonitoringACCProbe) ListDevices(_ context.Context, r *acc.ListDevicesRequest, _ ...grpc.CallOption) (*acc.ListDevicesResponse, error) {
	p.deviceCalls++
	return p.devices(r)
}

type gpuMonitoringObsProbe struct {
	calls          int
	platform       bool
	tenant, actor  string
	target         *obs.GpuTarget
	start, end     *timestamppb.Timestamp
	err            error
	physicalEvents bool
}

func (p *gpuMonitoringObsProbe) result(platform bool, tenant, actor string, target *obs.GpuTarget, codes []string) (*obs.QueryGpuResponse, error) {
	p.calls++
	p.platform, p.tenant, p.actor, p.target = platform, tenant, actor, proto.Clone(target).(*obs.GpuTarget)
	if p.err != nil {
		return nil, p.err
	}
	r := &obs.QueryGpuResponse{QueryTime: timestamppb.Now(), IndexComplete: target.IndexComplete, ScopeWatermark: "secret-watermark", IncludedResourceKinds: target.IncludedResourceKinds, UncoveredResourceKinds: target.UncoveredResourceKinds}
	for _, code := range codes {
		series := &obs.GpuSeries{MetricCode: code, Unit: "bytes", BaseResolutionSeconds: 30, Quality: &obs.GpuQuality{Status: "PARTIAL"}, Points: []*obs.GpuPoint{{Timestamp: timestamppb.Now(), KnownValue: proto.Float64(512), Quality: &obs.GpuQuality{Status: "PARTIAL"}}}}
		if p.physicalEvents {
			series.EventCode, series.Semantics = "31", "exporter-observed-event"
			series.ExporterProcessStarts = []*timestamppb.Timestamp{timestamppb.New(time.Now().Add(-time.Hour))}
		}
		r.Series = append(r.Series, series)
		if p.physicalEvents {
			other := proto.Clone(series).(*obs.GpuSeries)
			other.EventCode = "79"
			r.Series = append(r.Series, other)
		}
	}
	return r, nil
}
func (p *gpuMonitoringObsProbe) QueryGpuLatest(_ context.Context, platform bool, tenant, actor string, r *obs.QueryGpuLatestRequest) (*obs.QueryGpuResponse, error) {
	return p.result(platform, tenant, actor, r.Target, r.MetricCodes)
}
func (p *gpuMonitoringObsProbe) QueryGpuRange(_ context.Context, platform bool, tenant, actor string, r *obs.QueryGpuRangeRequest) (*obs.QueryGpuResponse, error) {
	p.start, p.end = r.Start, r.End
	return p.result(platform, tenant, actor, r.Target, r.MetricCodes)
}

type gpuMonitoringOwnerProbe struct {
	InstanceOwnerClient
	calls             int
	tenant, actor, id string
	err               error
}

func (p *gpuMonitoringOwnerProbe) Get(_ context.Context, tenant, actor, id string) (*res.Instance, error) {
	p.calls++
	p.tenant, p.actor, p.id = tenant, actor, id
	if p.err != nil {
		return nil, p.err
	}
	return &res.Instance{Id: id}, nil
}

type gpuMonitoringFixture struct {
	svc        *GpuMonitoringService
	client     *ent.Client
	ctx        context.Context
	tenant     *ent.Tenant
	operator   *ent.User
	membership *ent.UserRole
	acc        *gpuMonitoringACCProbe
	obs        *gpuMonitoringObsProbe
	owner      *gpuMonitoringOwnerProbe
	ref        *acc.GpuUsageRef
	scope      *acc.GpuMetricScope
}

func newGpuMonitoringFixture(t *testing.T) *gpuMonitoringFixture {
	t.Helper()
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	sys := appViewer.NewSystemViewerContext(context.Background())
	bctx := testutil.NewBootstrapContext(nil)
	name := "gpu-monitor-read-" + uuid.NewString()
	p, err := c.Client().Plan.Create().SetName(name).Save(sys)
	require.NoError(t, err)
	tenant, err := c.Client().Tenant.Create().SetName(name).SetCode(name).SetPlanID(p.ID).Save(sys)
	require.NoError(t, err)
	for _, module := range []planmodule.Module{planmodule.Module("INSTANCE"), planmodule.Module("INFERENCE")} {
		require.NoError(t, c.Client().PlanModule.Create().SetPlanID(p.ID).SetModule(module).Exec(sys))
	}
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(p.ID).SetQuotaCode(GpuSharedQuotaCode).SetQuotaValue(100000).Exec(sys))
	operator, err := c.Client().User.Create().SetTenantID(tenant.ID).SetUsername(name).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	memberRole, err := c.Client().Role.Create().SetTenantID(tenant.ID).SetName(name).SetCode("tenant:" + name).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	membership, err := c.Client().UserRole.Create().SetTenantID(tenant.ID).SetUserID(operator.ID).SetRoleID(memberRole.ID).SetStatus(userrole.StatusActive).Save(sys)
	require.NoError(t, err)
	perm, err := c.Client().Permission.Create().SetName(name).SetCode(name).SetStatus(permission.StatusOn).Save(sys)
	require.NoError(t, err)
	for path, module := range map[string]string{data.InstanceGetPath: "INSTANCE", "/api/v1/inference/services/{resource_id}": "INFERENCE"} {
		route, err := c.Client().Api.Query().Where(api.PathEQ(path), api.MethodEQ("GET"), api.ScopeEQ(api.ScopeAdmin)).Only(sys)
		if ent.IsNotFound(err) {
			route, err = c.Client().Api.Create().SetModule(module).SetPath(path).SetMethod("GET").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModule(module)).SetStatus(api.StatusOn).Save(sys)
		}
		require.NoError(t, err)
		require.NoError(t, c.Client().PermissionApi.Create().SetPermissionID(perm.ID).SetAPIID(route.ID).Exec(sys))
	}
	require.NoError(t, c.Client().RolePermission.Create().SetTenantID(tenant.ID).SetRoleID(memberRole.ID).SetPermissionID(perm.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Exec(sys))
	ctx := crudviewer.WithContext(context.Background(), appViewer.NewUserViewer(uint64(operator.ID), uint64(tenant.ID), 0, "", []crudviewer.DataScope{{ScopeType: crudviewer.ScopeTypeAll}}))
	ctx = auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: operator.ID, TenantID: tenant.ID})
	plan, _, _ := gpuVector(t)
	business := instanceSettingsBusiness(t)
	business.ComputeType, business.GpuExecution, business.GpuDisplay = "gpu", plan.Resolved.Execution, plan.Resolved.Selection
	payload, err := res.CanonicalBusinessPayloadVersion(business, 2)
	require.NoError(t, err)
	sum := sha256.Sum256(payload)
	canonical, err := gpuCanonicalJSON(GpuCanonical{SchemaVersion: 3, InstanceContractVersion: 2, GpuRequest: plan.Request, BusinessPayload: payload, BusinessPayloadDigest: hex.EncodeToString(sum[:]), MeteringVersion: GpuMeteringVersion, QuotaItems: []GpuQuotaItem{{QuotaCode: GpuSharedQuotaCode, Units: 8192}}})
	require.NoError(t, err)
	ledger := data.NewQuotaLedgerRepo(bctx, c)
	resourceID, createID := uuid.NewString(), uuid.NewString()
	_, err = ledger.Occupy(ctx, &data.QuotaOccupyInput{TenantID: tenant.ID, ResourceTenantID: tenant.ResourceTenantID, ResourceID: resourceID, OperationID: createID, OwnerService: InstanceOwner, Action: InstanceCreateAction, ActorType: "user", ActorID: strconv.FormatUint(uint64(operator.ID), 10), IdempotencyKey: uuid.NewString(), RequestHash: "monitoring-original", CanonicalRequest: string(canonical), InstanceContractVersion: 2, Items: []data.QuotaOccupyItem{{QuotaCode: GpuSharedQuotaCode, Units: 8192}}})
	require.NoError(t, err)
	ref := &acc.GpuUsageRef{TenantId: tenant.ResourceTenantID, OwnerService: InstanceOwner, ResourceId: resourceID, CreateOperationId: createID}
	now := time.Now().UTC()
	scope := &acc.GpuMetricScope{ScopeId: "secret-scope", Ref: ref, ResourceKind: "container_instance", ClusterId: plan.Request.ClusterId, Node: &acc.NodeRef{Name: "secret-node", Uid: uuid.NewString()}, Namespace: "secret-namespace", PodName: "secret-pod", PodUid: uuid.NewString(), ContainerName: "workspace", GpuUuid: "GPU-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", VdeviceId: "0", DeviceId: uuid.NewString(), BindingId: uuid.NewString(), ValidFrom: now.Add(-time.Hour).Format(time.RFC3339Nano), EvidenceAt: now.Format(time.RFC3339Nano), OwnershipState: "VERIFIED", CandidateComplete: true}
	accPeer := &gpuMonitoringACCProbe{}
	accPeer.tenant = func(r *acc.TenantGpuMetricScopesRequest) (*acc.GpuMetricScopesResponse, error) {
		require.Equal(t, tenant.ResourceTenantID, r.Context.TenantId)
		require.Equal(t, strconv.FormatUint(uint64(operator.ID), 10), r.Context.Actor.Id)
		if r.Ref != nil {
			require.True(t, proto.Equal(ref, r.Ref), "the original create identity comes from the real ledger")
		}
		return &acc.GpuMetricScopesResponse{Items: []*acc.GpuMetricScope{proto.Clone(scope).(*acc.GpuMetricScope)}, SnapshotId: "secret-snapshot", IndexComplete: true, IndexStartedAt: scope.ValidFrom}, nil
	}
	accPeer.admin = func(r *acc.AdminGpuMetricScopesRequest) (*acc.GpuMetricScopesResponse, error) {
		require.Equal(t, tenant.ResourceTenantID, r.TenantId)
		require.Nil(t, r.Ref)
		require.NotNil(t, r.Context.Actor)
		return &acc.GpuMetricScopesResponse{Items: []*acc.GpuMetricScope{proto.Clone(scope).(*acc.GpuMetricScope)}, SnapshotId: "secret-snapshot", IndexComplete: true}, nil
	}
	obsPeer, owner := &gpuMonitoringObsProbe{}, &gpuMonitoringOwnerProbe{}
	tenants := data.NewTenantRepo(bctx, c)
	accelerator := NewAcceleratorService(&data.AcceleratorClient{Admin: accPeer, Usage: accPeer}, tenants, NewGpuBffLedgerBridge(ledger, nil), nil, nil, NewQuotaAdapterRegistry(), nil)
	instanceSvc := NewInstanceService(nil, NewInstanceGpuBinding(owner, data.NewInstanceAuthorizationRepo(c), scope.ClusterId), tenants, nil, nil)
	svc := NewGpuMonitoringService(accelerator, obsPeer, instanceSvc, nil, data.NewGpuResourceReadAuthorizationRepo(c))
	return &gpuMonitoringFixture{svc: svc, client: c.Client(), ctx: ctx, tenant: tenant, operator: operator, membership: membership, acc: accPeer, obs: obsPeer, owner: owner, ref: ref, scope: scope}
}

func TestGpuMonitoringTenantAndContainerUseTrustedScopes(t *testing.T) {
	f := newGpuMonitoringFixture(t)
	before, err := f.client.QuotaAccount.Query().Only(appViewer.NewSystemViewerContext(context.Background()))
	require.NoError(t, err)
	r, err := f.svc.GetResourceGpuLatest(f.ctx, &view.GpuResourceLatestRequest{ResourceKind: "container", ResourceId: f.ref.ResourceId})
	require.NoError(t, err)
	require.Equal(t, 1, f.owner.calls)
	require.Equal(t, f.tenant.ResourceTenantID, f.owner.tenant)
	require.Equal(t, "governance:user:"+strconv.FormatUint(uint64(f.operator.ID), 10), f.owner.actor)
	require.Equal(t, "RESOURCE", f.obs.target.Kind)
	require.Equal(t, "container", f.obs.target.Scopes[0].ResourceKind)
	require.Equal(t, "container_instance", f.scope.ResourceKind, "provider identity is adapted without mutating its fact")
	require.Equal(t, f.tenant.ResourceTenantID, f.obs.tenant)
	require.False(t, f.obs.platform)
	require.Equal(t, f.ref.CreateOperationId, f.obs.target.Scopes[0].UsageRef.CreateOperationId)
	require.Equal(t, f.scope.Node.Uid, f.obs.target.Scopes[0].NodeUid)
	require.True(t, f.obs.target.Scopes[0].CandidateComplete)
	require.Equal(t, "container", r.ResourceKind)
	require.Nil(t, r.Series[0].Points[0].Value, "unknown complete totals must remain absent")
	require.Equal(t, 512.0, r.Series[0].Points[0].GetKnownValue())
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(r)
	require.NoError(t, err)
	for _, secret := range []string{"secret-", f.scope.PodUid, f.scope.BindingId, f.scope.DeviceId, f.scope.Node.Uid, f.ref.CreateOperationId} {
		require.NotContains(t, string(raw), secret)
	}
	f.scope.ValidTo = time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339Nano)
	f.owner.err = status.Error(codes.NotFound, "deleted owner resource")
	_, err = f.svc.QueryMyGpu(f.ctx, &view.GpuTenantQueryRequest{Data: &view.GpuQueryData{Window: "30d"}})
	require.NoError(t, err)
	require.Equal(t, "TENANT", f.obs.target.Kind)
	require.NotNil(t, f.obs.target.Scopes[0].ValidTo, "trusted retired scope survives tenant history aggregation")
	require.Equal(t, 1, f.owner.calls, "tenant history does not enumerate only currently readable owner objects")
	require.InDelta(t, 30*24*time.Hour.Seconds(), f.obs.end.AsTime().Sub(f.obs.start.AsTime()).Seconds(), 1)
	require.Equal(t, 2, f.acc.tenantCalls)
	require.Zero(t, f.acc.adminCalls, "tenant reads never call the administrator scope method")
	after, err := f.client.QuotaAccount.Query().Only(appViewer.NewSystemViewerContext(context.Background()))
	require.NoError(t, err)
	require.Equal(t, before.OccupiedUnits, after.OccupiedUnits, "monitoring must not occupy or release quota")
	platform := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7})
	_, err = f.svc.GetTenantGpuLatest(platform, &view.GpuTenantLatestRequest{TenantId: f.tenant.ID})
	require.NoError(t, err)
	require.True(t, f.obs.platform)
	require.Empty(t, f.obs.tenant, "platform transport never invents a tenant context")
	require.Equal(t, f.tenant.ResourceTenantID, f.obs.target.TenantId)
	require.Equal(t, 1, f.acc.adminCalls)
}

func TestGpuMonitoringInferenceAdapterPreservesOriginalAndReplicas(t *testing.T) {
	f := newGpuMonitoringFixture(t)
	c := testutil.NewQuotaPGClient(t) // same exclusive DB, no fixture reset
	bctx := testutil.NewBootstrapContext(nil)
	principal, err := auth.PrincipalFromContext(f.ctx)
	require.NoError(t, err)
	plan, _, _ := gpuVector(t)
	plan.Request.ContainerName = "kserve-container"
	selection := inferenceCPUSelection(t)
	selection.ComputeType = "gpu"
	selection.Gpu = &compute.GpuSelection{ProfileId: plan.Request.ProfileId, ProfileVersion: plan.Request.ProfileVersion, SpecDigest: plan.Profile.SpecDigest, DevicesPerUnit: 1, ClusterId: plan.Request.ClusterId, PoolId: plan.Request.PoolId, Mode: plan.Resolved.Selection.Mode, ModelKey: plan.Resolved.Selection.ModelKey, MemoryMibPerDevice: 8192, CoreLimitPercent: 25}
	selection.SelectionDigest, err = compute.SelectionDigest(selection)
	require.NoError(t, err)
	intent := inferencePublicIntent()
	model := &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Model: &modelv1.Model{TenantId: f.tenant.ResourceTenantID}, Version: &modelv1.ModelVersion{Id: intent.ModelVersionId, Status: "ready"}}}
	owner := &inferenceMissingOwner{}
	binding := NewInferenceComputeBinding(owner, data.NewInferenceAuthorizationRepo(c), model, &inferenceComputeProbe{selection: selection})
	prepared, err := binding.PrepareManagedBusiness(f.ctx, principal, f.tenant.ResourceTenantID, intent)
	require.NoError(t, err)
	createID, resourceID := uuid.NewString(), uuid.NewString()
	actorID := strconv.FormatUint(uint64(principal.ID), 10)
	canonical, err := prepared.freeze(f.tenant.ResourceTenantID, "user", actorID, createID, resourceID, plan.Resolved)
	require.NoError(t, err)
	ledger := data.NewQuotaLedgerRepo(bctx, c)
	_, err = ledger.Occupy(f.ctx, &data.QuotaOccupyInput{TenantID: f.tenant.ID, ResourceTenantID: f.tenant.ResourceTenantID, ResourceID: resourceID, OperationID: createID, ActorType: "user", ActorID: actorID, OwnerService: "ani-inference", Action: InferenceCreateAction, IdempotencyKey: uuid.NewString(), RequestHash: "inference-monitor-original", CanonicalRequest: string(canonical), Items: []data.QuotaOccupyItem{{QuotaCode: GpuSharedQuotaCode, Units: 8192}}})
	require.NoError(t, err)
	owner.service = &runtimev1.InferenceService{Id: resourceID, Replicas: 2, Generation: 1}
	f.svc.inference = NewInferenceServiceWithOwner(nil, owner, data.NewTenantRepo(bctx, c))
	f.acc.tenant = func(r *acc.TenantGpuMetricScopesRequest) (*acc.GpuMetricScopesResponse, error) {
		require.Equal(t, "ani-inference", r.Ref.OwnerService)
		require.Equal(t, resourceID, r.Ref.ResourceId)
		require.Equal(t, createID, r.Ref.CreateOperationId)
		require.Equal(t, f.tenant.ResourceTenantID, r.Context.TenantId)
		var scopes []*acc.GpuMetricScope
		for _, pod := range []string{"replica-0", "replica-1"} {
			scope := proto.Clone(f.scope).(*acc.GpuMetricScope)
			scope.Ref, scope.ResourceKind, scope.PodName, scope.PodUid = proto.Clone(r.Ref).(*acc.GpuUsageRef), "inference", pod, uuid.NewString()
			scopes = append(scopes, scope)
		}
		return &acc.GpuMetricScopesResponse{Items: scopes, SnapshotId: "inference-snapshot", IndexComplete: true}, nil
	}
	r, err := f.svc.GetResourceGpuLatest(f.ctx, &view.GpuResourceLatestRequest{ResourceKind: "inference", ResourceId: resourceID})
	require.NoError(t, err)
	require.Equal(t, "inference", r.ResourceKind)
	require.Equal(t, resourceID, r.ResourceId)
	require.Len(t, f.obs.target.Scopes, 2, "distinct replicas must reach Obs separately")
	for _, scope := range f.obs.target.Scopes {
		require.Equal(t, createID, scope.UsageRef.CreateOperationId)
	}
	require.Equal(t, 1, owner.gets)
	require.Equal(t, f.tenant.ResourceTenantID, owner.getTenant)
	require.Equal(t, "governance:user:"+actorID, owner.getActor)
	require.Zero(t, f.owner.calls, "inference does not route through the container owner")
}

func TestGpuMonitoringResourceAuthorizationAndScopeIsolation(t *testing.T) {
	f := newGpuMonitoringFixture(t)
	for _, r := range []*view.GpuTenantLatestRequest{{TenantId: f.tenant.ID}} {
		_, err := f.svc.GetMyGpuLatest(f.ctx, r)
		require.Equal(t, "GPU_INVALID_QUERY", kerrors.FromError(err).Reason)
	}
	_, err := f.svc.GetResourceGpuLatest(f.ctx, &view.GpuResourceLatestRequest{ResourceKind: "modeldev", ResourceId: f.ref.ResourceId})
	require.Equal(t, "GPU_RESOURCE_KIND_UNSUPPORTED", kerrors.FromError(err).Reason)
	require.Zero(t, f.acc.tenantCalls)
	withoutPermissionReader := NewGpuMonitoringService(f.svc.accelerator, f.obs, f.svc.instances, nil)
	_, err = withoutPermissionReader.GetResourceGpuLatest(f.ctx, &view.GpuResourceLatestRequest{ResourceKind: "container", ResourceId: f.ref.ResourceId})
	require.Equal(t, "GPU_RESOURCE_READ_DENIED", kerrors.FromError(err).Reason)
	_, err = f.svc.GetResourceGpuLatest(f.ctx, &view.GpuResourceLatestRequest{ResourceKind: "container", ResourceId: uuid.NewString()})
	require.Equal(t, "QUOTA_NOT_FOUND", kerrors.FromError(err).Reason, "owner-readable resources still require a trusted original GPU ledger")
	unknownTenant := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: f.operator.ID, TenantID: ^uint32(0)})
	_, err = f.svc.GetMyGpuLatest(unknownTenant, &view.GpuTenantLatestRequest{})
	require.Equal(t, "TENANT_MAPPING_UNAVAILABLE", kerrors.FromError(err).Reason)
	require.Zero(t, f.acc.tenantCalls)
	f.owner.err = status.Error(codes.NotFound, "owner resource hidden")
	_, err = f.svc.GetResourceGpuLatest(f.ctx, &view.GpuResourceLatestRequest{ResourceKind: "container", ResourceId: f.ref.ResourceId})
	require.Error(t, err)
	require.Zero(t, f.acc.tenantCalls, "owner denial precedes ACC/Obs")
	f.owner.err = nil
	for _, change := range []func(*acc.GpuMetricScope){func(s *acc.GpuMetricScope) { s.Ref.TenantId = uuid.NewString() }, func(s *acc.GpuMetricScope) { s.Ref.CreateOperationId = uuid.NewString() }, func(s *acc.GpuMetricScope) { s.Ref.OwnerService = "ani-inference" }} {
		f.acc.tenant = func(*acc.TenantGpuMetricScopesRequest) (*acc.GpuMetricScopesResponse, error) {
			s := proto.Clone(f.scope).(*acc.GpuMetricScope)
			change(s)
			return &acc.GpuMetricScopesResponse{Items: []*acc.GpuMetricScope{s}, SnapshotId: "snapshot", IndexComplete: true}, nil
		}
		_, err = f.svc.GetResourceGpuLatest(f.ctx, &view.GpuResourceLatestRequest{ResourceKind: "container", ResourceId: f.ref.ResourceId})
		require.Equal(t, "ACCELERATOR_INVALID_RESPONSE", kerrors.FromError(err).Reason)
	}
	require.Zero(t, f.obs.calls, "cross-tenant and different-create scope data never reaches Obs")
	require.NoError(t, f.client.UserRole.UpdateOneID(f.membership.ID).SetStatus(userrole.StatusDisabled).Exec(appViewer.NewSystemViewerContext(context.Background())))
	ownerCalls := f.owner.calls
	for _, kind := range []string{"container", "inference"} {
		_, err = f.svc.GetResourceGpuLatest(f.ctx, &view.GpuResourceLatestRequest{ResourceKind: kind, ResourceId: f.ref.ResourceId})
		require.Equal(t, "GPU_RESOURCE_READ_DENIED", kerrors.FromError(err).Reason)
	}
	require.Equal(t, ownerCalls, f.owner.calls, "current role denial occurs before owner reads")
}

func TestGpuMonitoringScopePaginationAndQuality(t *testing.T) {
	f := newGpuMonitoringFixture(t)
	f.acc.tenant = func(r *acc.TenantGpuMetricScopesRequest) (*acc.GpuMetricScopesResponse, error) {
		s := proto.Clone(f.scope).(*acc.GpuMetricScope)
		s.CandidateComplete = false
		s.Reasons = []string{"SOURCE_HISTORY_INCOMPLETE"}
		if r.Page.Token == "" {
			return &acc.GpuMetricScopesResponse{Items: []*acc.GpuMetricScope{s}, NextToken: "next", SnapshotId: "stable", IndexComplete: true}, nil
		}
		require.Equal(t, "next", r.Page.Token)
		require.Equal(t, "stable", r.SnapshotId)
		return &acc.GpuMetricScopesResponse{SnapshotId: "stable", IndexComplete: true}, nil
	}
	r, err := f.svc.GetMyGpuLatest(f.ctx, &view.GpuTenantLatestRequest{})
	require.NoError(t, err)
	require.Equal(t, 2, f.acc.tenantCalls)
	require.False(t, f.obs.target.IndexComplete)
	require.Equal(t, "OWNERSHIP_UNKNOWN", f.obs.target.Scopes[0].OwnershipState)
	require.Equal(t, []string{"SOURCE_HISTORY_INCOMPLETE"}, f.obs.target.Scopes[0].Reasons)
	require.Equal(t, []string{"container"}, r.UncoveredResourceKinds)
	for name, reply := range map[string]func(*acc.TenantGpuMetricScopesRequest) (*acc.GpuMetricScopesResponse, error){
		"changed-snapshot": func(r *acc.TenantGpuMetricScopesRequest) (*acc.GpuMetricScopesResponse, error) {
			if r.Page.Token == "" {
				return &acc.GpuMetricScopesResponse{SnapshotId: "first", NextToken: "next"}, nil
			}
			return &acc.GpuMetricScopesResponse{SnapshotId: "changed"}, nil
		},
		"token-cycle": func(r *acc.TenantGpuMetricScopesRequest) (*acc.GpuMetricScopesResponse, error) {
			next := "a"
			if r.Page.Token == "a" {
				next = "b"
			}
			return &acc.GpuMetricScopesResponse{SnapshotId: "stable", NextToken: next}, nil
		},
		"empty-page-budget": func(*acc.TenantGpuMetricScopesRequest) (*acc.GpuMetricScopesResponse, error) {
			return &acc.GpuMetricScopesResponse{SnapshotId: "stable", NextToken: strconv.Itoa(f.acc.tenantCalls)}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			f.acc.tenant = reply
			calls := f.obs.calls
			_, err := f.svc.GetMyGpuLatest(f.ctx, &view.GpuTenantLatestRequest{})
			require.Error(t, err)
			require.Equal(t, calls, f.obs.calls)
		})
	}
	f.acc.tenant = func(*acc.TenantGpuMetricScopesRequest) (*acc.GpuMetricScopesResponse, error) {
		var scopes []*acc.GpuMetricScope
		for i := 0; i < 129; i++ {
			scope := proto.Clone(f.scope).(*acc.GpuMetricScope)
			scope.PodName = "source-" + strconv.Itoa(i)
			scopes = append(scopes, scope)
		}
		return &acc.GpuMetricScopesResponse{Items: scopes, SnapshotId: "stable", IndexComplete: true}, nil
	}
	calls := f.obs.calls
	_, err = f.svc.GetMyGpuLatest(f.ctx, &view.GpuTenantLatestRequest{})
	require.Equal(t, "GPU_QUERY_BUDGET_EXCEEDED", kerrors.FromError(err).Reason)
	require.Equal(t, calls, f.obs.calls, "budget overflow must not silently truncate the target")
}

func TestGpuMonitoringPhysicalAdminAndEventMetadata(t *testing.T) {
	f := newGpuMonitoringFixture(t)
	device := &acc.PhysicalDevice{DeviceId: f.scope.DeviceId, ClusterId: f.scope.ClusterId, Node: f.scope.Node, VendorDeviceId: f.scope.GpuUuid, ModelKey: "test-model"}
	f.acc.devices = func(r *acc.ListDevicesRequest) (*acc.ListDevicesResponse, error) {
		require.NotNil(t, r.Context.Actor)
		require.Equal(t, device.ClusterId, r.ClusterId)
		return &acc.ListDevicesResponse{Items: []*acc.PhysicalDevice{device}}, nil
	}
	f.obs.physicalEvents = true
	platform := auth.NewPrincipalContext(context.Background(), &auth.Principal{Type: auth.SubjectUser, ID: 7})
	r, err := f.svc.GetPhysicalGpuLatest(platform, &view.GpuPhysicalLatestRequest{ClusterId: device.ClusterId, DeviceId: device.DeviceId, MetricCodes: []string{"gpu.physical.xid_errors_total"}})
	require.NoError(t, err)
	require.True(t, f.obs.platform)
	require.Empty(t, f.obs.tenant)
	require.Equal(t, device.Node.Uid, f.obs.target.Scopes[0].NodeUid)
	require.True(t, f.obs.target.Scopes[0].CandidateComplete)
	require.Len(t, r.Series, 2, "one event metric can return multiple bounded Xid dimensions")
	require.Equal(t, "31", r.Series[0].EventCode)
	require.Equal(t, "79", r.Series[1].EventCode)
	require.Equal(t, "exporter-observed-event", r.Series[0].Semantics)
	require.Len(t, r.Series[0].ExporterProcessStarts, 1)
	_, err = f.svc.GetPhysicalGpuLatest(f.ctx, &view.GpuPhysicalLatestRequest{ClusterId: device.ClusterId, DeviceId: device.DeviceId})
	require.Equal(t, "PLATFORM_REQUIRED", kerrors.FromError(err).Reason)
	for _, principal := range []*auth.Principal{{Type: auth.SubjectAPIKey, ID: 7, TenantID: f.tenant.ID}, {Type: auth.SubjectUser, ID: 7}} {
		_, err = f.svc.GetMyGpuLatest(auth.NewPrincipalContext(context.Background(), principal), &view.GpuTenantLatestRequest{})
		require.Error(t, err)
	}
	f.obs.err = status.Error(codes.ResourceExhausted, "secret upstream body")
	_, err = f.svc.GetPhysicalGpuLatest(platform, &view.GpuPhysicalLatestRequest{ClusterId: device.ClusterId, DeviceId: device.DeviceId})
	require.Equal(t, "GPU_QUERY_BUDGET_EXCEEDED", kerrors.FromError(err).Reason)
	require.NotContains(t, err.Error(), "secret")
}

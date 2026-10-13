//go:build quota_pg

package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	"go-wind-admin/app/admin/service/internal/data"
	api "go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// These two doubles are only the Resource resolution and Acc RPC boundaries.
// Current authorization and original-request admission execute real PG code.
type instanceResolutionPeer struct {
	InstanceOwnerClient
	plan        *acc.ResolvedGpuPlan
	calls       int
	unavailable bool
}

func (p *instanceResolutionPeer) Resolve(ctx context.Context, tenant, actor string, d *res.CreateInstanceData) (*res.CreateBusiness, error) {
	p.calls++
	if p.unavailable {
		return nil, errors.New("external image/preset unavailable")
	}
	if p.InstanceOwnerClient != nil {
		return p.InstanceOwnerClient.Resolve(ctx, tenant, actor, d)
	}
	return &res.CreateBusiness{Data: proto.Clone(d).(*res.CreateInstanceData), Gpu: proto.Clone(p.plan.Request).(*acc.GpuRequest), Spec: &res.FrozenSpec{ImageReference: "registry.invalid/work@sha256:" + strings.Repeat("a", 64), ImageDigest: "sha256:" + strings.Repeat("a", 64), Cpu: "1", Memory: "2Gi", ClusterId: p.plan.Request.ClusterId, Namespace: "tenant-" + tenant, ServiceAccount: "workload", StorageClass: "ani-block", WorkspaceSize: "1Gi", Shell: "/bin/sh", Uid: 1000, Gid: 1000}}, nil
}

type instancePlanPeer struct {
	plan        *acc.ResolvedGpuPlan
	calls       int
	unavailable bool
}

func (p *instancePlanPeer) ResolveGpuRequest(_ context.Context, r *acc.ResolveGpuRequestRequest, _ ...grpc.CallOption) (*acc.ResolvedGpuPlan, error) {
	p.calls++
	if p.unavailable {
		return nil, errors.New("external Acc unavailable")
	}
	if !proto.Equal(r.Gpu, p.plan.Request) {
		return nil, errors.New("request mismatch")
	}
	return proto.Clone(p.plan).(*acc.ResolvedGpuPlan), nil
}

func TestInstanceCurrentAuthorizationOriginalReplayAndOwnerRelease(t *testing.T) {
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	sys := appViewer.NewSystemViewerContext(context.Background())
	bctx := testutil.NewBootstrapContext(nil)
	fixture := uuid.NewString()
	p, e := c.Client().Plan.Create().SetName("instance-" + fixture).Save(sys)
	require.NoError(t, e)
	tenant, e := c.Client().Tenant.Create().SetName("instance-" + fixture).SetCode("instance-" + fixture).SetPlanID(p.ID).Save(sys)
	require.NoError(t, e)
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(p.ID).SetQuotaCode(GpuSharedQuotaCode).SetQuotaValue(20000).Exec(sys))
	require.NoError(t, c.Client().PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.Module("INSTANCE")).Exec(sys))
	operator, e := c.Client().User.Create().SetTenantID(tenant.ID).SetUsername("instance-" + fixture).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, e)
	r, e := c.Client().Role.Create().SetTenantID(tenant.ID).SetName("instance-" + fixture).SetCode("tenant:instance:" + fixture).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, e)
	membership, e := c.Client().UserRole.Create().SetTenantID(tenant.ID).SetUserID(operator.ID).SetRoleID(r.ID).SetStatus(userrole.StatusActive).Save(sys)
	require.NoError(t, e)
	perm, e := c.Client().Permission.Create().SetName("instance create").SetCode("instance:create:" + fixture).SetStatus(permission.StatusOn).Save(sys)
	require.NoError(t, e)
	route, e := c.Client().Api.Create().SetModule("InstanceService").SetPath(data.InstanceCreatePath).SetMethod("POST").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModule("INSTANCE")).SetStatus(api.StatusOn).Save(sys)
	require.NoError(t, e)
	require.NoError(t, c.Client().PermissionApi.Create().SetPermissionID(perm.ID).SetAPIID(route.ID).Exec(sys))
	require.NoError(t, c.Client().RolePermission.Create().SetTenantID(tenant.ID).SetRoleID(r.ID).SetPermissionID(perm.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Exec(sys))
	requestCtx := viewer.WithContext(context.Background(), appViewer.NewUserViewer(uint64(operator.ID), uint64(tenant.ID), 0, "", []viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}}))
	requestCtx = auth.NewPrincipalContext(requestCtx, &auth.Principal{Type: auth.SubjectUser, ID: operator.ID, TenantID: tenant.ID})
	plan, _, _ := gpuVector(t)
	plan.Request.Replicas = 1
	plan.Request.ContainerName = "workspace"
	plan.Totals.LogicalDeviceCount = 1
	plan.Totals.SharedMemoryMib = plan.Profile.Spec.SharedMemoryMib
	plan.ResolutionDigest, e = GpuPlanDigest(plan)
	require.NoError(t, e)
	owner := instanceResourceProcess(t, plan.Request)
	resource := &instanceResolutionPeer{InstanceOwnerClient: owner, plan: plan}
	plans := &instancePlanPeer{plan: plan}
	binding := NewInstanceGpuBinding(resource, data.NewInstanceAuthorizationRepo(c))
	registry := NewQuotaAdapterRegistry()
	require.NoError(t, registry.Register(binding))
	ledger := data.NewQuotaLedgerRepo(bctx, c)
	acceptance, e := NewGpuAcceptance(ledger, registry, data.NewTenantRepo(bctx, c), plans, binding, nil)
	require.NoError(t, e)
	key := uuid.NewString()
	public := &res.CreateInstanceData{Name: "workspace", ImageId: "img_" + strings.ReplaceAll(uuid.NewString(), "-", ""), ImageScope: "platform", PresetId: "shared", PresetVersion: 1}
	first, e := acceptance.AcceptGpuCreate(requestCtx, key, nil, public)
	require.NoError(t, e)
	require.False(t, first.Replayed)
	require.Equal(t, 1, resource.calls)
	require.Equal(t, 1, plans.calls)
	claimed, e := ledger.ClaimDispatchable(context.Background(), "instance-wire-worker", time.Minute, 1)
	require.NoError(t, e)
	require.Len(t, claimed, 1)
	worker := NewQuotaDispatchWorker(bctx, ledger, registry)
	worker.dispatchOne(context.Background(), &claimed[0])
	operation, e := ledger.GetOperationForUser(requestCtx, tenant.ID, first.OperationID)
	require.NoError(t, e)
	require.Equal(t, quotaoperation.DispatchStateAcked, operation.DispatchState, "real Resource durable ACK required")
	instance, e := owner.Get(requestCtx, tenant.ResourceTenantID, "governance:user:"+strconv.FormatUint(uint64(operator.ID), 10), first.ResourceID)
	require.NoError(t, e)
	require.Equal(t, first.OperationID, instance.CreateOperationId)
	require.EqualValues(t, 1, instance.Version)
	resource.unavailable = true
	plans.unavailable = true
	replay, e := acceptance.AcceptGpuCreate(requestCtx, key, nil, public)
	require.NoError(t, e)
	require.True(t, replay.Replayed)
	require.Equal(t, first.OperationID, replay.OperationID)
	require.Equal(t, first.ChargeIDs, replay.ChargeIDs)
	require.Equal(t, 1, resource.calls)
	require.Equal(t, 1, plans.calls)
	changed := proto.Clone(public).(*res.CreateInstanceData)
	changed.Name = "different"
	_, e = acceptance.AcceptGpuCreate(requestCtx, key, nil, changed)
	require.Error(t, e)
	require.Equal(t, 1, resource.calls)
	require.NoError(t, c.Client().UserRole.UpdateOneID(membership.ID).SetStatus(userrole.StatusDisabled).Exec(sys))
	_, e = acceptance.AcceptGpuCreate(requestCtx, key, nil, public)
	require.Error(t, e, "revoked current role must deny even frozen replay")
	require.Equal(t, 1, resource.calls)
	charges, e := ledger.GetChargesForOperation(requestCtx, tenant.ID, first.OperationID)
	require.NoError(t, e)
	require.Len(t, charges, 1)
	release := &data.QuotaReleaseInput{OwnerService: InstanceOwner, ReleaseEventID: uuid.NewString(), OperationID: first.OperationID, Reason: "RESOURCE_RELEASED", PayloadHash: "original-event", PayloadJSON: `{"scope":"owner-release"}`, Items: []data.QuotaReleaseItemInput{{ChargeID: charges[0].ChargeID, QuotaCode: charges[0].QuotaCode, ReleasedTotal: charges[0].OriginalUnits}}}
	wrong := *release
	wrong.OwnerService = "ani-inference"
	_, e = ledger.Release(context.Background(), &wrong)
	require.Error(t, e)
	receipt, e := ledger.Release(context.Background(), release)
	require.NoError(t, e, "owner release must not require a DELETE operation or K8s callback")
	require.Equal(t, charges[0].OriginalUnits, receipt[0].AppliedDelta)
	again, e := ledger.Release(context.Background(), release)
	require.NoError(t, e)
	require.Equal(t, receipt[0].ChargeID, again[0].ChargeID)
	require.Equal(t, receipt[0].ReleasedTotal, again[0].ReleasedTotal)
	require.Zero(t, again[0].AppliedDelta, "replayed event must not apply balance twice")
	second := *release
	second.ReleaseEventID = uuid.NewString()
	second.PayloadHash = "same-cumulative-total"
	receipt, e = ledger.Release(context.Background(), &second)
	require.NoError(t, e)
	require.Zero(t, receipt[0].AppliedDelta)
	op, all, hasDelete, e := ledger.LoadGpuProjectionSource(requestCtx, tenant.ID, first.OperationID)
	require.NoError(t, e)
	require.False(t, hasDelete)
	projection, e := deriveGpuProjection(op, all, hasDelete)
	require.NoError(t, e)
	require.Equal(t, acc.UsageState_ENDED, projection.State)
	require.Equal(t, InstanceOwner, projection.Ref.OwnerService)
	balances, e := ledger.RecomputeInvariants(context.Background(), tenant.ID)
	require.NoError(t, e)
	for _, balance := range balances {
		require.True(t, balance.Balanced)
		require.Zero(t, balance.OccupiedUnits)
	}
}

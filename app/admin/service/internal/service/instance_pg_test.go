//go:build quota_pg

package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	acc "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	res "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/v1"
	view "go-wind-admin/api/gen/go/instance/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	api "go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	entplan "go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/quotaoperation"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/sysconfig"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// Resource calls delegate to the real mTLS/PG provider. Only dependency failure
// injection and the external Acc plan response are controlled here.
type instanceResolutionPeer struct {
	InstanceOwnerClient
	mu                  sync.Mutex
	plan                *gpuPlanFixture
	calls               int
	unavailable         bool
	rejectDeleteVersion bool
	deleteCalls         int
	operationCalls      int
	deleteRequest       *res.AcceptDeleteRequest
}

func (p *instanceResolutionPeer) Resolve(ctx context.Context, tenant, actor string, d *res.CreateInstanceData, defaults *res.SchedulingOptions) (*res.CreateSpec, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.unavailable {
		return nil, errors.New("external image/preset unavailable")
	}
	if p.InstanceOwnerClient != nil {
		return p.InstanceOwnerClient.Resolve(ctx, tenant, actor, d, defaults)
	}
	return nil, errors.New("Resource resolution peer required")
}

func (p *instanceResolutionPeer) Delete(ctx context.Context, request *res.AcceptDeleteRequest) (*res.OperationResponse, error) {
	p.mu.Lock()
	p.deleteCalls++
	p.deleteRequest = proto.Clone(request).(*res.AcceptDeleteRequest)
	rejected := p.rejectDeleteVersion
	p.mu.Unlock()
	if rejected {
		// Only the owner response boundary is injected. Gov's validation,
		// durable claim, adapter dispatch and rejection writeback stay real.
		return nil, status.Error(codes.FailedPrecondition, "INSTANCE_VERSION_CONFLICT")
	}
	return p.InstanceOwnerClient.Delete(ctx, request)
}

func (p *instanceResolutionPeer) Operation(ctx context.Context, tenant, actor, id string) (*res.Operation, error) {
	p.mu.Lock()
	p.operationCalls++
	p.mu.Unlock()
	return p.InstanceOwnerClient.Operation(ctx, tenant, actor, id)
}

type instancePlanPeer struct {
	mu          sync.Mutex
	plan        *gpuPlanFixture
	calls       int
	unavailable bool
}

func (p *instancePlanPeer) ResolveGpuRequest(_ context.Context, r *acc.ResolveGpuRequestRequest, _ ...grpc.CallOption) (*acc.ResolvedGpuRequest, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.unavailable {
		return nil, errors.New("external Acc unavailable")
	}
	if !proto.Equal(r.Gpu, p.plan.Request) {
		return nil, errors.New("request mismatch")
	}
	return proto.Clone(p.plan.Resolved).(*acc.ResolvedGpuRequest), nil
}

func TestInstanceCurrentAuthorizationOriginalReplayAndOwnerRelease(t *testing.T) {
	t.Run("gpu", func(t *testing.T) { instanceCurrentAuthorizationOriginalReplayAndOwnerRelease(t, false) })
	t.Run("cpu", func(t *testing.T) { instanceCurrentAuthorizationOriginalReplayAndOwnerRelease(t, true) })
}

func TestInstanceDeleteVersionRejectedReplayAndLocalOperation(t *testing.T) {
	// Reuse the original current-authorization/real-PG/mTLS fixture and stop
	// after this rejection slice, before its unrelated release scenarios.
	t.Run("cpu", func(t *testing.T) { instanceCurrentAuthorizationOriginalReplayAndOwnerRelease(t, true, false, true) })
	t.Run("gpu", func(t *testing.T) { instanceCurrentAuthorizationOriginalReplayAndOwnerRelease(t, false, false, true) })
}

func instanceCurrentAuthorizationOriginalReplayAndOwnerRelease(t *testing.T, cpu bool, schedulingScenario ...bool) {
	t.Setenv("ANI_INSTANCE_CONFIG_V2_WRITES", "true")
	t.Setenv("ANI_INSTANCE_NEW_INTENTS_ENABLED", "true")
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	sys := appViewer.NewSystemViewerContext(context.Background())
	bctx := testutil.NewBootstrapContext(nil)
	fixture := uuid.NewString()
	p, e := c.Client().Plan.Create().SetName("instance-" + fixture).Save(sys)
	require.NoError(t, e)
	tenant, e := c.Client().Tenant.Create().SetName("instance-" + fixture).SetCode("instance-" + fixture).SetPlanID(p.ID).Save(sys)
	require.NoError(t, e)
	if !cpu {
		require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(p.ID).SetQuotaCode(GpuSharedQuotaCode).SetQuotaValue(20000).Exec(sys))
		require.NoError(t, c.Client().PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleAccelerator).Exec(sys))
	}
	require.NoError(t, c.Client().PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.Module("INSTANCE")).Exec(sys))
	operator, e := c.Client().User.Create().SetTenantID(tenant.ID).SetUsername("instance-" + fixture).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, e)
	r, e := c.Client().Role.Create().SetTenantID(tenant.ID).SetName("instance-" + fixture).SetCode("tenant:instance:" + fixture).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, e)
	membership, e := c.Client().UserRole.Create().SetTenantID(tenant.ID).SetUserID(operator.ID).SetRoleID(r.ID).SetStatus(userrole.StatusActive).Save(sys)
	require.NoError(t, e)
	perm, e := c.Client().Permission.Create().SetName("instance create").SetCode("instance:create:" + fixture).SetStatus(permission.StatusOn).Save(sys)
	require.NoError(t, e)
	catalogAPI := func(path, method string) *ent.Api {
		row, err := c.Client().Api.Query().Where(api.ModuleEQ("InstanceService"), api.PathEQ(path), api.MethodEQ(method), api.ScopeEQ(api.ScopeAdmin)).Only(sys)
		if ent.IsNotFound(err) {
			row, err = c.Client().Api.Create().SetModule("InstanceService").SetPath(path).SetMethod(method).SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModule("INSTANCE")).SetStatus(api.StatusOn).Save(sys)
		}
		require.NoError(t, err)
		require.NotNil(t, row.BusinessModule)
		require.Equal(t, api.BusinessModule("INSTANCE"), *row.BusinessModule)
		require.NotNil(t, row.Status)
		require.Equal(t, api.StatusOn, *row.Status)
		return row
	}
	route := catalogAPI(data.InstanceCreatePath, "POST")
	require.NoError(t, c.Client().PermissionApi.Create().SetPermissionID(perm.ID).SetAPIID(route.ID).Exec(sys))
	require.NoError(t, c.Client().RolePermission.Create().SetTenantID(tenant.ID).SetRoleID(r.ID).SetPermissionID(perm.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Exec(sys))
	deleteRoute := catalogAPI(data.InstanceDeletePath, "DELETE")
	require.NoError(t, c.Client().PermissionApi.Create().SetPermissionID(perm.ID).SetAPIID(deleteRoute.ID).Exec(sys))
	operationRoute := catalogAPI(data.InstanceOperationPath, "GET")
	require.NoError(t, c.Client().PermissionApi.Create().SetPermissionID(perm.ID).SetAPIID(operationRoute.ID).Exec(sys))
	requestCtx := viewer.WithContext(context.Background(), appViewer.NewUserViewer(uint64(operator.ID), uint64(tenant.ID), 0, "", []viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}}))
	requestCtx = auth.NewPrincipalContext(requestCtx, &auth.Principal{Type: auth.SubjectUser, ID: operator.ID, TenantID: tenant.ID})
	plan, _, _ := gpuVector(t)
	plan.Request.Replicas = 1
	plan.Request.ContainerName = "workspace"
	fixturePlan, expectedPlanCalls := plan, 1
	if cpu {
		fixturePlan, expectedPlanCalls = nil, 0
	}
	owner, computeOwner, peer := instanceResourceProcess(t, fixturePlan)
	resource := &instanceResolutionPeer{InstanceOwnerClient: owner, plan: plan}
	plans := &instancePlanPeer{plan: plan}
	var schedulingStore *instanceSchedulingReplayStore
	var settingsRepo *data.ConfigRepo
	var schedulingPolicy *InstanceSchedulingPolicy
	if len(schedulingScenario) > 0 && schedulingScenario[0] {
		require.True(t, cpu)
		settingsRepo = data.NewConfigRepo(bctx, c, nil)
		t.Cleanup(settingsRepo.Close)
		t.Cleanup(func() {
			_, err := c.Client().SysConfig.Delete().Where(sysconfig.KeyEQ("instance.scheduling." + peer.ClusterID)).Exec(sys)
			require.NoError(t, err)
		})
		require.NoError(t, settingsRepo.SaveInstanceSchedulingSettings(sys, peer.ClusterID, operator.ID, []byte(`{"defaults":{"pod_placement":"SPREAD_PREFERRED"},"priority_class_allowlists":[]}`)))
		schedulingStore = &instanceSchedulingReplayStore{InstanceSchedulingStore: settingsRepo}
		schedulingPolicy = NewInstanceSchedulingPolicy(schedulingStore, data.NewTenantRepo(bctx, c), &instanceSettingsClusterProbe{}, peer.ClusterID)
	}
	authorization := data.NewInstanceAuthorizationRepo(c)
	if !cpu {
		require.ErrorIs(t, authorization.AuthorizeCatalog(requestCtx, tenant.ID, operator.ID), data.ErrActionAuthorizationDenied, "Profile catalog permission is absent, while GPU use remains eligible")
	}
	binding := NewInstanceGpuBinding(resource, authorization, peer.ClusterID, schedulingPolicy)
	registry := NewQuotaAdapterRegistry()
	require.NoError(t, registry.Register(binding))
	ledger := data.NewQuotaLedgerRepo(bctx, c)
	var planResolver GpuPlanResolver = plans
	if cpu {
		planResolver = nil
	}
	acceptance, e := NewGpuAcceptance(ledger, registry, data.NewTenantRepo(bctx, c), planResolver, binding, nil)
	require.NoError(t, e)
	key := uuid.NewString()
	public := &res.CreateInstanceData{Name: "workspace", ImageId: "img_" + strings.ReplaceAll(uuid.NewString(), "-", ""), ImageScope: "platform", PresetId: peer.PresetID}
	first, e := acceptance.AcceptGpuCreate(requestCtx, key, nil, public)
	require.NoError(t, e)
	require.False(t, first.Replayed)
	require.Equal(t, 1, resource.calls)
	require.Equal(t, expectedPlanCalls, plans.calls)
	service := NewInstanceService(acceptance, binding, data.NewTenantRepo(bctx, c), ledger, nil)
	if cpu {
		pending, e := service.GetInstanceOperation(requestCtx, &view.GetOperationRequest{OperationId: first.OperationID})
		require.NoError(t, e, "frontend can poll immediately after Gov durable admission")
		require.Equal(t, "QUEUED", pending.State)
		require.Equal(t, first.ResourceID, pending.InstanceId)
		require.Zero(t, pending.IntentVersion)
	}
	claimed, e := ledger.ClaimDispatchable(context.Background(), "instance-wire-worker", time.Minute, 1)
	require.NoError(t, e)
	require.Len(t, claimed, 1)
	worker := NewQuotaDispatchWorker(bctx, ledger, registry)
	worker.dispatchOne(context.Background(), &claimed[0])
	operation, e := ledger.GetOperationForUser(requestCtx, tenant.ID, first.OperationID)
	require.NoError(t, e)
	require.Equal(t, quotaoperation.DispatchStateAcked, operation.DispatchState, "real Resource durable ACK required")
	require.EqualValues(t, 2, operation.InstanceContractVersion)
	instance, e := owner.Get(requestCtx, tenant.ResourceTenantID, "governance:user:"+strconv.FormatUint(uint64(operator.ID), 10), first.ResourceID)
	require.NoError(t, e)
	require.Equal(t, first.OperationID, instance.CreateOperationId)
	configuration, err := owner.Configuration(requestCtx, tenant.ResourceTenantID, "governance:user:"+strconv.FormatUint(uint64(operator.ID), 10), first.ResourceID)
	require.NoError(t, err)
	require.EqualValues(t, 2, configuration.InstanceContractVersion)
	require.Equal(t, "/workspace", configuration.WorkspaceMountPath, "omitted optional configuration resolves the normal current defaults")
	require.Empty(t, configuration.VolumeMounts)
	require.Empty(t, configuration.Env)
	require.EqualValues(t, 1, instance.Version)
	require.Equal(t, "container", instance.InstanceKind)
	require.Equal(t, peer.PresetID, instance.PresetId)
	require.Equal(t, "1000m", instance.Cpu)
	require.Equal(t, "2048Mi", instance.Memory)
	require.NotNil(t, instance.Scheduling)
	expectedPlacement := res.SchedulingOptions_NONE
	if schedulingStore != nil {
		expectedPlacement = res.SchedulingOptions_SPREAD_PREFERRED
		require.Equal(t, 1, schedulingStore.reads)
	}
	require.Equal(t, expectedPlacement, instance.Scheduling.GetPodPlacement())
	if cpu {
		require.Equal(t, "cpu", instance.ComputeType)
		require.Nil(t, instance.GpuSummary)
	} else {
		require.Equal(t, "gpu", instance.ComputeType)
		require.NotNil(t, instance.GpuSummary)
		require.Equal(t, plan.Request.ProfileId, instance.GpuSummary.ProfileId)
		require.Equal(t, plan.Request.DevicesPerReplica, instance.GpuSummary.DevicesPerInstance)
	}
	if len(schedulingScenario) > 1 && schedulingScenario[1] {
		original := operation
		originalCharges, err := ledger.GetChargesForOperation(requestCtx, tenant.ID, first.OperationID)
		require.NoError(t, err)
		deleteKey := uuid.NewString()
		request := &view.ChangeInstanceRequest{Data: &view.ChangeInstanceData{InstanceId: first.ResourceID, IdempotencyKey: deleteKey, ExpectedVersion: instance.Version}}
		accepted, err := service.DeleteInstance(requestCtx, request)
		require.NoError(t, err)
		require.Equal(t, "QUEUED_FOR_OWNER", accepted.Result)
		require.False(t, accepted.Replayed)
		require.Equal(t, first.ResourceID, accepted.ResourceId)
		require.Error(t, service.canOperate(requestCtx, first.ResourceID))
		claimed, err := ledger.ClaimDispatchable(context.Background(), "instance-delete-rejection", time.Minute, 1)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		require.Equal(t, accepted.OperationId, claimed[0].OperationID)
		require.Equal(t, first.OperationID, claimed[0].CreateOperationID)
		resource.rejectDeleteVersion = true
		worker.dispatchOne(context.Background(), &claimed[0])
		require.Equal(t, 1, resource.deleteCalls)
		require.NotNil(t, resource.deleteRequest)
		require.EqualValues(t, instance.Version, resource.deleteRequest.ExpectedVersion)
		require.Equal(t, accepted.OperationId, resource.deleteRequest.Context.OperationId)
		rejected, err := ledger.GetOperationForUser(requestCtx, tenant.ID, accepted.OperationId)
		require.NoError(t, err)
		require.Equal(t, quotaoperation.DispatchStateRejected, rejected.DispatchState)
		require.NotNil(t, rejected.LastErrorCode)
		require.Equal(t, "INSTANCE_VERSION_CONFLICT", *rejected.LastErrorCode)
		require.Nil(t, rejected.AckJSON)
		require.Equal(t, claimed[0].CanonicalRequest, rejected.CanonicalRequest)
		require.NoError(t, service.canOperate(requestCtx, first.ResourceID), "definite owner rejection reopens the Instance intent gate")

		replay, err := service.DeleteInstance(requestCtx, request)
		require.NoError(t, err)
		require.True(t, replay.Replayed)
		require.Equal(t, "REJECTED_VERSION_CONFLICT", replay.Result)
		require.Equal(t, accepted.OperationId, replay.OperationId)
		require.Equal(t, accepted.ResourceId, replay.ResourceId)
		projection, err := acceptance.AcceptGpuDeleteVersion(requestCtx, deleteKey, first.ResourceID, instance.Version)
		require.NoError(t, err)
		require.Equal(t, "REJECTED_VERSION_CONFLICT", projection.Result)
		require.Equal(t, accepted.OperationId, projection.DeleteOperationId)
		require.Empty(t, projection.DispatchOperationId, "a rejected replay cannot ask the dispatcher to resend")
		ownerQueries := resource.operationCalls
		local, err := service.GetInstanceOperation(requestCtx, &view.GetOperationRequest{OperationId: accepted.OperationId})
		require.NoError(t, err)
		require.Equal(t, accepted.OperationId, local.Id)
		require.Equal(t, first.ResourceID, local.InstanceId)
		require.Equal(t, "delete", local.Action)
		require.Equal(t, "failed", local.State)
		require.Zero(t, local.IntentVersion, "Resource did not admit an operation version")
		require.Equal(t, "INSTANCE_VERSION_CONFLICT", local.Reason)
		require.Equal(t, ownerQueries, resource.operationCalls, "the rejected operation only exists in Gov")
		resends, err := ledger.ClaimDispatchable(context.Background(), "instance-delete-no-resend", time.Minute, 8)
		require.NoError(t, err)
		require.Empty(t, resends)
		require.Equal(t, 1, resource.deleteCalls)
		changed := proto.Clone(request).(*view.ChangeInstanceRequest)
		changed.Data.ExpectedVersion++
		_, err = service.DeleteInstance(requestCtx, changed)
		require.Error(t, err, "the same key cannot be rewritten with today's version")
		after, err := ledger.GetOperationForUser(requestCtx, tenant.ID, accepted.OperationId)
		require.NoError(t, err)
		require.Equal(t, rejected.CanonicalRequest, after.CanonicalRequest)
		require.Equal(t, rejected.RequestHash, after.RequestHash)
		require.Equal(t, rejected.AttemptCount, after.AttemptCount)
		require.Equal(t, rejected.LeaseGeneration, after.LeaseGeneration)
		createAfter, err := ledger.GetOperationForUser(requestCtx, tenant.ID, first.OperationID)
		require.NoError(t, err)
		require.Equal(t, original.CanonicalRequest, createAfter.CanonicalRequest)
		require.Equal(t, original.AckJSON, createAfter.AckJSON)
		chargesAfter, err := ledger.GetChargesForOperation(requestCtx, tenant.ID, first.OperationID)
		require.NoError(t, err)
		require.Len(t, chargesAfter, len(originalCharges))
		for i, charge := range chargesAfter {
			require.Equal(t, originalCharges[i].ChargeID, charge.ChargeID)
			require.Equal(t, originalCharges[i].OriginalUnits, charge.OriginalUnits)
			require.Zero(t, charge.ReleasedUnits)
		}
		require.Zero(t, c.Client().QuotaReleaseReceipt.Query().CountX(sys), "a definite version rejection is not owner cleanup")
		balances, err := ledger.RecomputeInvariants(context.Background(), tenant.ID)
		require.NoError(t, err)
		for _, balance := range balances {
			require.True(t, balance.Balanced)
			require.Equal(t, balance.ChargeRemainder, balance.OccupiedUnits)
		}
		return
	}
	if schedulingStore != nil {
		require.NoError(t, settingsRepo.SaveInstanceSchedulingSettings(sys, peer.ClusterID, operator.ID, []byte(`{"defaults":{"pod_placement":"COLOCATE_PREFERRED"},"priority_class_allowlists":[]}`)))
		_, err := computeOwner.AdminUpdatePreset(sys, "governance:user:1", &compute.AdminUpdatePresetRequest{Id: peer.PresetID, Data: &compute.PresetData{CpuMillis: 2000, MemoryMib: 4096}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"cpu_millis", "memory_mib"}}})
		require.NoError(t, err)
		schedulingStore.fail = true
	}
	if !cpu {
		_, err := c.Client().PlanModule.Delete().Where(planmodule.HasPlanWith(entplan.IDEQ(p.ID)), planmodule.ModuleEQ(planmodule.ModuleAccelerator)).Exec(sys)
		require.NoError(t, err)
	}
	t.Setenv("ANI_INSTANCE_CONFIG_V2_WRITES", "false")
	closedBinding := NewInstanceGpuBinding(resource, data.NewInstanceAuthorizationRepo(c), peer.ClusterID, schedulingPolicy)
	closedAcceptance, err := NewGpuAcceptance(ledger, registry, data.NewTenantRepo(bctx, c), planResolver, closedBinding, nil)
	require.NoError(t, err)
	_, err = closedAcceptance.AcceptGpuCreate(requestCtx, uuid.NewString(), nil, public)
	require.Error(t, err)
	require.EqualValues(t, 503, kratoserrors.FromError(err).Code)
	require.Equal(t, "INSTANCE_CREATE_DISABLED", kratoserrors.FromError(err).Reason)
	frozenReplay, err := closedAcceptance.AcceptGpuCreate(requestCtx, key, nil, public)
	require.NoError(t, err, "maintenance gate retains current accepted replay")
	require.Equal(t, first.OperationID, frozenReplay.OperationID)
	require.True(t, frozenReplay.Replayed)
	require.Equal(t, 1, resource.calls, "closed admission must not call current resolution")
	resource.unavailable = true
	plans.unavailable = true
	replay, e := acceptance.AcceptGpuCreate(requestCtx, key, nil, public)
	require.NoError(t, e)
	require.True(t, replay.Replayed)
	require.Equal(t, first.OperationID, replay.OperationID)
	require.Equal(t, first.ChargeIDs, replay.ChargeIDs)
	require.Equal(t, 1, resource.calls)
	require.Equal(t, expectedPlanCalls, plans.calls)
	if schedulingStore != nil {
		require.Equal(t, 1, schedulingStore.reads, "original replay must not query today's changed or unavailable settings")
		frozen, err := owner.Get(requestCtx, tenant.ResourceTenantID, "governance:user:"+strconv.FormatUint(uint64(operator.ID), 10), first.ResourceID)
		require.NoError(t, err)
		require.Equal(t, "1000m", frozen.Cpu)
		require.Equal(t, "2048Mi", frozen.Memory)
		require.Equal(t, res.SchedulingOptions_SPREAD_PREFERRED, frozen.Scheduling.GetPodPlacement())
	}
	explicitNone := proto.Clone(public).(*res.CreateInstanceData)
	explicitNone.Scheduling = &res.SchedulingOptions{PodPlacement: res.SchedulingOptions_NONE.Enum()}
	_, e = acceptance.AcceptGpuCreate(requestCtx, key, nil, explicitNone)
	require.Error(t, e, "omitted scheduling and explicit NONE are different original requests")
	require.Equal(t, 1, resource.calls, "idempotency conflict must not resolve current defaults")
	require.Equal(t, expectedPlanCalls, plans.calls, "idempotency conflict must not resolve another GPU plan")
	changed := proto.Clone(public).(*res.CreateInstanceData)
	changed.Name = "different"
	_, e = acceptance.AcceptGpuCreate(requestCtx, key, nil, changed)
	require.Error(t, e)
	require.Equal(t, 1, resource.calls)
	changed.Name = public.Name
	changed.PresetId = uuid.NewString()
	_, e = acceptance.AcceptGpuCreate(requestCtx, key, nil, changed)
	require.Error(t, e)
	require.Equal(t, 1, resource.calls)
	require.NoError(t, c.Client().UserRole.UpdateOneID(membership.ID).SetStatus(userrole.StatusDisabled).Exec(sys))
	_, e = acceptance.AcceptGpuCreate(requestCtx, key, nil, public)
	require.Error(t, e, "revoked current role must deny even frozen replay")
	require.Equal(t, 1, resource.calls)
	charges, e := ledger.GetChargesForOperation(requestCtx, tenant.ID, first.OperationID)
	require.NoError(t, e)
	if cpu {
		require.Empty(t, charges, "CPU creates no GPU ledger charge")
		candidates, e := ledger.ScanGpuCreatePage(context.Background(), 0, 100)
		require.NoError(t, e)
		require.Empty(t, candidates, "CPU must never enter Acc usage projection")
		require.NoError(t, c.Client().UserRole.UpdateOneID(membership.ID).SetStatus(userrole.StatusActive).Exec(sys))
		deleteKey := uuid.NewString()
		accepted, e := closedAcceptance.AcceptGpuDeleteVersion(requestCtx, deleteKey, first.ResourceID, instance.Version)
		require.NoError(t, e)
		require.Equal(t, "QUEUED_FOR_OWNER", accepted.Result)
		repeated, e := acceptance.AcceptGpuDeleteVersion(requestCtx, deleteKey, first.ResourceID, instance.Version)
		require.NoError(t, e)
		require.Equal(t, accepted.DeleteOperationId, repeated.DeleteOperationId)
		pending, e := service.GetInstanceOperation(requestCtx, &view.GetOperationRequest{OperationId: accepted.DeleteOperationId})
		require.NoError(t, e)
		require.Equal(t, "QUEUED", pending.State)
		require.Equal(t, "delete", pending.Action)
		require.Error(t, service.canOperate(requestCtx, first.ResourceID), "durable delete blocks new start/stop/exec")
		claimed, e = ledger.ClaimDispatchable(context.Background(), "cpu-delete-worker", time.Minute, 1)
		require.NoError(t, e)
		require.Len(t, claimed, 1)
		require.Empty(t, claimed[0].Charges)
		require.Equal(t, first.OperationID, claimed[0].CreateOperationID)
		worker.dispatchOne(context.Background(), &claimed[0])
		deletion, e := ledger.GetOperationForUser(requestCtx, tenant.ID, accepted.DeleteOperationId)
		require.NoError(t, e)
		require.Equal(t, quotaoperation.DispatchStateAcked, deletion.DispatchState, "real Resource CPU delete durable ACK required")
		owned, e := service.GetInstanceOperation(requestCtx, &view.GetOperationRequest{OperationId: accepted.DeleteOperationId})
		require.NoError(t, e)
		require.Equal(t, "delete", owned.Action)
		require.NotEqual(t, "QUEUED", owned.State, "ACKED reads Resource operation progress")
		resource.unavailable = false
		if schedulingStore != nil {
			schedulingStore.fail = false
		}
		unsent, e := acceptance.AcceptGpuCreate(requestCtx, uuid.NewString(), nil, public)
		require.NoError(t, e)
		if schedulingStore != nil {
			require.Equal(t, 2, schedulingStore.reads)
			operation, err := ledger.GetOperationForUser(requestCtx, tenant.ID, unsent.OperationID)
			require.NoError(t, err)
			command, err := data.DecodeInstanceCPUCanonical([]byte(operation.CanonicalRequest))
			require.NoError(t, err)
			business, err := res.DecodeBusinessPayload(command.BusinessPayload)
			require.NoError(t, err)
			require.Equal(t, "2000m", business.Cpu)
			require.Equal(t, "4096Mi", business.Memory)
			require.Equal(t, res.SchedulingOptions_COLOCATE_PREFERRED, business.Scheduling.GetPodPlacement())
		}
		canceled, e := acceptance.AcceptGpuDeleteVersion(requestCtx, uuid.NewString(), unsent.ResourceID, 1)
		require.NoError(t, e)
		require.Equal(t, "CANCELED_BEFORE_DISPATCH", canceled.Result)
		for _, id := range []string{unsent.OperationID, canceled.DeleteOperationId} {
			local, e := service.GetInstanceOperation(requestCtx, &view.GetOperationRequest{OperationId: id})
			require.NoError(t, e, "locally canceled commands never existed in Resource")
			require.Equal(t, "CANCELED_UNSENT", local.State)
			require.Equal(t, "CANCELED_BEFORE_DISPATCH", local.Reason)
		}
		return
	}
	require.Len(t, charges, 1)
	// Current business permission remains valid, but a new GPU key must check
	// today's eligibility. The original key above never re-ran that gate.
	require.NoError(t, c.Client().UserRole.UpdateOneID(membership.ID).SetStatus(userrole.StatusActive).Exec(sys))
	resource.unavailable = false
	_, e = acceptance.AcceptGpuCreate(requestCtx, uuid.NewString(), nil, public)
	require.EqualValues(t, 403, kratoserrors.FromError(e).Code)
	require.Equal(t, "GPU_USE_DENIED", kratoserrors.FromError(e).Reason)
	require.Equal(t, expectedPlanCalls, plans.calls, "denied new GPU use cannot call ACC")
	operations, e := c.Client().QuotaOperation.Query().Where(quotaoperation.TenantIDEQ(tenant.ID)).Count(sys)
	require.NoError(t, e)
	require.Equal(t, 1, operations, "denied new GPU use cannot persist admission or charges")
	require.NoError(t, c.Client().PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleAccelerator).Exec(sys))
	plans.unavailable = false
	concurrentKey := uuid.NewString()
	results := make(chan *data.QuotaOccupyResult, 8)
	failures := make(chan error, 8)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := acceptance.AcceptGpuCreate(requestCtx, concurrentKey, nil, public)
			results <- result
			failures <- err
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	var unique *data.QuotaOccupyResult
	for result := range results {
		require.NotNil(t, result)
		if unique == nil {
			unique = result
		}
		require.Equal(t, unique.OperationID, result.OperationID)
		require.Equal(t, unique.ResourceID, result.ResourceID)
		require.Equal(t, unique.ChargeIDs, result.ChargeIDs)
	}
	concurrentCharges, e := ledger.GetChargesForOperation(requestCtx, tenant.ID, unique.OperationID)
	require.NoError(t, e)
	require.Len(t, concurrentCharges, 1)
	concurrentDelete, e := acceptance.AcceptGpuDeleteVersion(requestCtx, uuid.NewString(), unique.ResourceID, 1)
	require.NoError(t, e)
	require.Equal(t, "CANCELED_BEFORE_DISPATCH", concurrentDelete.Result)
	release := &data.QuotaReleaseInput{OwnerService: InstanceOwner, ReleaseEventID: uuid.NewString(), OperationID: first.OperationID, Reason: "RESOURCE_RELEASED", PayloadHash: "original-event", PayloadJSON: `{"scope":"owner-release"}`, ResourceID: first.ResourceID, Complete: true}
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
	// Terminal replay keeps the original identity/charges without current
	// resolution, while every stored command must still satisfy its codec.
	terminalResourceCalls, terminalPlanCalls := resource.calls, plans.calls
	require.NoError(t, c.Client().UserRole.UpdateOneID(membership.ID).SetStatus(userrole.StatusActive).Exec(sys))
	terminal, e := acceptance.AcceptGpuCreate(requestCtx, key, nil, public)
	require.NoError(t, e)
	require.True(t, terminal.Replayed)
	require.Equal(t, first.OperationID, terminal.OperationID)
	require.Equal(t, first.ResourceID, terminal.ResourceID)
	require.Equal(t, first.ChargeIDs, terminal.ChargeIDs)
	originalCanonical := operation.CanonicalRequest
	updated, e := c.DB().ExecContext(sys, "UPDATE sys_quota_operations SET canonical_request=$1 WHERE tenant_id=$2 AND operation_id=$3", `{"offline_audit":true}`, tenant.ID, first.OperationID)
	require.NoError(t, e)
	auditRows, e := updated.RowsAffected()
	require.NoError(t, e)
	require.EqualValues(t, 1, auditRows)
	_, e = acceptance.AcceptGpuCreate(requestCtx, key, nil, public)
	require.Error(t, e, "a terminal charge must not bypass original command validation")
	_, e = c.DB().ExecContext(sys, "UPDATE sys_quota_operations SET canonical_request=$1 WHERE tenant_id=$2 AND operation_id=$3", originalCanonical, tenant.ID, first.OperationID)
	require.NoError(t, e)
	terminal, e = acceptance.AcceptGpuCreate(requestCtx, key, nil, public)
	require.NoError(t, e)
	require.True(t, terminal.Replayed)
	require.Equal(t, first.OperationID, terminal.OperationID)
	require.Equal(t, first.ResourceID, terminal.ResourceID)
	require.Equal(t, first.ChargeIDs, terminal.ChargeIDs)
	require.Equal(t, terminalResourceCalls, resource.calls, "terminal replay must not resolve current resources")
	require.Equal(t, terminalPlanCalls, plans.calls, "terminal replay must not resolve another GPU plan")

}

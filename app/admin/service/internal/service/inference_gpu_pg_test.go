//go:build quota_pg

package service

import (
	"context"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/google/uuid"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	modelv1 "github.com/liangzai006/ani-model-service/api/model/v1"
	"github.com/stretchr/testify/require"
	view "go-wind-admin/api/gen/go/inference/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/quotadefinition"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	"go-wind-admin/pkg/middleware/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Only Model, Accelerator and Inference RPCs are boundary substitutes. The BFF,
// current route authorization, acceptance and PostgreSQL ledger are real.
type inferenceMissingOwner struct {
	directInferenceOwnerProbe
	gets      int
	deletes   int
	service   *inferencev1.InferenceService
	delete    *inferencev1.DeleteInferenceServiceRequest
	getTenant string
	getActor  string
}

func (p *inferenceMissingOwner) GetTrusted(_ context.Context, tenant, actor string, _ *inferencev1.GetInferenceServiceRequest) (*inferencev1.InferenceService, error) {
	p.gets++
	p.getTenant, p.getActor = tenant, actor
	if p.service != nil {
		return p.service, nil
	}
	return nil, status.Error(codes.NotFound, "owner has not received CREATE")
}

func (p *inferenceMissingOwner) DeleteTrusted(_ context.Context, tenant, actor string, request *inferencev1.DeleteInferenceServiceRequest) (*inferencev1.OperationResponse, error) {
	p.deletes++
	p.tenant, p.actor, p.delete = tenant, actor, request
	return &inferencev1.OperationResponse{Operation: &inferencev1.Operation{Id: uuid.NewString()}}, nil
}

type inferenceGPUFixture struct {
	service     *InferenceService
	ledger      *data.QuotaLedgerRepo
	client      *ent.Client
	model       *inferenceModelProbe
	plans       *instancePlanPeer
	owner       *inferenceMissingOwner
	ctx         context.Context
	tenantID    uint32
	membership  *ent.UserRole
	request     *view.CreateInferenceRequest
	planID      uint32
	permissions []uint32
}

func newInferenceGPUFixture(t *testing.T) *inferenceGPUFixture {
	t.Helper()
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	sys := appViewer.NewSystemViewerContext(context.Background())
	bctx := testutil.NewBootstrapContext(nil)
	id := uuid.NewString()
	exists, err := c.Client().QuotaDefinition.Query().Where(quotadefinition.CodeEQ(GpuSharedQuotaCode)).Exist(sys)
	require.NoError(t, err)
	if !exists {
		require.NoError(t, c.Client().QuotaDefinition.Create().SetCode(GpuSharedQuotaCode).SetDisplayName("GPU shared memory").SetUnit("MiB").SetAccountingKind(quotadefinition.AccountingKindConcurrent).Exec(sys))
	}
	planRow, err := c.Client().Plan.Create().SetName("inference-" + id).Save(sys)
	require.NoError(t, err)
	tenant, err := c.Client().Tenant.Create().SetName("inference-" + id).SetCode("inference-" + id).SetPlanID(planRow.ID).Save(sys)
	require.NoError(t, err)
	require.NoError(t, c.Client().PlanQuota.Create().SetPlanID(planRow.ID).SetQuotaCode(GpuSharedQuotaCode).SetQuotaValue(20000).Exec(sys))
	require.NoError(t, c.Client().PlanModule.Create().SetPlanID(planRow.ID).SetModule(planmodule.Module("INFERENCE")).Exec(sys))
	operator, err := c.Client().User.Create().SetTenantID(tenant.ID).SetUsername("inference-" + id).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	r, err := c.Client().Role.Create().SetTenantID(tenant.ID).SetName("inference-" + id).SetCode("tenant:inference:" + id).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	membership, err := c.Client().UserRole.Create().SetTenantID(tenant.ID).SetUserID(operator.ID).SetRoleID(r.ID).SetStatus(userrole.StatusActive).Save(sys)
	require.NoError(t, err)
	var permissions []uint32
	for _, path := range []string{data.InferenceCreatePath, data.InferenceDeletePath} {
		perm, e := c.Client().Permission.Create().SetName("inference " + path).SetCode("inference:" + uuid.NewString()).SetStatus(permission.StatusOn).Save(sys)
		require.NoError(t, e)
		route, e := c.Client().Api.Query().Where(api.PathEQ(path), api.MethodEQ("POST")).Only(sys)
		if ent.IsNotFound(e) {
			route, e = c.Client().Api.Create().SetModule("InferenceService").SetPath(path).SetMethod("POST").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModule("INFERENCE")).SetStatus(api.StatusOn).Save(sys)
		}
		require.NoError(t, e)
		require.NoError(t, c.Client().PermissionApi.Create().SetPermissionID(perm.ID).SetAPIID(route.ID).Exec(sys))
		require.NoError(t, c.Client().RolePermission.Create().SetTenantID(tenant.ID).SetRoleID(r.ID).SetPermissionID(perm.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Exec(sys))
		permissions = append(permissions, perm.ID)
	}
	requestCtx := viewer.WithContext(context.Background(), appViewer.NewUserViewer(uint64(operator.ID), uint64(tenant.ID), 0, "", []viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}}))
	requestCtx = auth.NewPrincipalContext(requestCtx, &auth.Principal{Type: auth.SubjectUser, ID: operator.ID, TenantID: tenant.ID})
	plan, _, _ := gpuVector(t)
	plan.Request.ContainerName = "kserve-container"
	plan.ResolutionDigest, err = GpuPlanDigest(plan)
	require.NoError(t, err)
	modelVersionID := uuid.NewString()
	model := &inferenceModelProbe{response: &modelv1.GetModelVersionResponse{Model: &modelv1.Model{TenantId: tenant.ResourceTenantID}, Version: &modelv1.ModelVersion{Id: modelVersionID, Status: "ready"}}}
	owner := &inferenceMissingOwner{}
	plans := &instancePlanPeer{plan: plan}
	binding := NewInferenceGpuBindingWithModel(owner, data.NewInferenceAuthorizationRepo(c), model)
	registry := NewQuotaAdapterRegistry()
	require.NoError(t, registry.Register(binding))
	ledger := data.NewQuotaLedgerRepo(bctx, c)
	acceptance, err := NewGpuAcceptance(ledger, registry, data.NewTenantRepo(bctx, c), plans, binding, nil)
	require.NoError(t, err)
	service := NewInferenceServiceWithOwnerAndModel(acceptance, owner, data.NewTenantRepo(bctx, c), model)
	request := &view.CreateInferenceRequest{Data: &view.CreateInferenceData{
		IdempotencyKey: uuid.NewString(), Name: "qwen", ModelVersionId: modelVersionID, ServedModelName: "qwen", EngineType: "vllm", EngineImage: "registry.invalid/vllm:fixture", Command: "exec vllm serve /models", Cpu: "1", Memory: "2Gi", Replicas: 2,
		Gpu: &view.GpuSelection{ClusterId: plan.Request.ClusterId, PoolId: plan.Request.PoolId, ProfileId: plan.Request.ProfileId, ProfileVersion: plan.Request.ProfileVersion},
	}}
	return &inferenceGPUFixture{service: service, ledger: ledger, client: c.Client(), model: model, plans: plans, owner: owner, ctx: requestCtx, tenantID: tenant.ID, membership: membership, request: request, planID: planRow.ID, permissions: permissions}
}

func (f *inferenceGPUFixture) otherAuthorizedTenant(t *testing.T) (context.Context, string) {
	t.Helper()
	sys := appViewer.NewSystemViewerContext(context.Background())
	id := "other-inference-" + uuid.NewString()
	tenant, err := f.client.Tenant.Create().SetName(id).SetCode(id).SetPlanID(f.planID).Save(sys)
	require.NoError(t, err)
	operator, err := f.client.User.Create().SetTenantID(tenant.ID).SetUsername(id).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	r, err := f.client.Role.Create().SetTenantID(tenant.ID).SetName(id).SetCode("tenant:" + id).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	require.NoError(t, f.client.UserRole.Create().SetTenantID(tenant.ID).SetUserID(operator.ID).SetRoleID(r.ID).SetStatus(userrole.StatusActive).Exec(sys))
	for _, permissionID := range f.permissions {
		require.NoError(t, f.client.RolePermission.Create().SetTenantID(tenant.ID).SetRoleID(r.ID).SetPermissionID(permissionID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Exec(sys))
	}
	ctx := viewer.WithContext(context.Background(), appViewer.NewUserViewer(uint64(operator.ID), uint64(tenant.ID), 0, "", []viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}}))
	return auth.NewPrincipalContext(ctx, &auth.Principal{Type: auth.SubjectUser, ID: operator.ID, TenantID: tenant.ID}), tenant.ResourceTenantID
}

func TestInferenceGPUReplayWhileModelUnavailable(t *testing.T) {
	f := newInferenceGPUFixture(t)
	first, err := f.service.CreateInference(f.ctx, f.request)
	require.NoError(t, err)
	require.False(t, first.Replayed)
	f.model.err = status.Error(codes.Unavailable, "Model unavailable")
	f.plans.unavailable = true
	replay, err := f.service.CreateInference(f.ctx, f.request)
	require.NoError(t, err, "original durable acceptance remains replayable without Model or Accelerator")
	require.True(t, replay.Replayed)
	require.Equal(t, first.OperationId, replay.OperationId)
	require.Equal(t, first.ResourceId, replay.ResourceId)
	require.Equal(t, 1, f.model.calls)
	newRequest := proto.Clone(f.request).(*view.CreateInferenceRequest)
	newRequest.Data.IdempotencyKey = uuid.NewString()
	_, err = f.service.CreateInference(f.ctx, newRequest)
	require.Equal(t, codes.Unavailable, status.Code(err), "new admission still requires Model readiness")
	require.Equal(t, 2, f.model.calls)
	sys := appViewer.NewSystemViewerContext(context.Background())
	require.NoError(t, f.client.UserRole.UpdateOneID(f.membership.ID).SetStatus(userrole.StatusDisabled).Exec(sys))
	_, err = f.service.CreateInference(f.ctx, f.request)
	require.Equal(t, 403, kratoserrors.Code(err), "current authorization precedes original-key replay")
	require.Equal(t, 2, f.model.calls)
	rows, err := f.ledger.RecomputeInvariants(context.Background(), f.tenantID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Balanced)
	require.EqualValues(t, 12288, rows[0].OccupiedUnits)
}

func TestInferenceGPUCancelBeforeOwnerReceivesCreate(t *testing.T) {
	f := newInferenceGPUFixture(t)
	created, err := f.service.CreateInference(f.ctx, f.request)
	require.NoError(t, err)
	request := &view.DeleteInferenceRequest{Data: &view.DeleteInferenceData{IdempotencyKey: uuid.NewString(), ResourceId: created.ResourceId}}
	otherCtx, otherTenant := f.otherAuthorizedTenant(t)
	_, err = f.service.DeleteInference(otherCtx, request)
	require.Equal(t, codes.NotFound, status.Code(err), "another authorized tenant cannot cancel this CREATE")
	require.Equal(t, otherTenant, f.owner.getTenant)
	before, err := f.ledger.RecomputeInvariants(context.Background(), f.tenantID)
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.EqualValues(t, 12288, before[0].OccupiedUnits)
	sys := appViewer.NewSystemViewerContext(context.Background())
	require.NoError(t, f.client.UserRole.UpdateOneID(f.membership.ID).SetStatus(userrole.StatusDisabled).Exec(sys))
	_, err = f.service.DeleteInference(f.ctx, request)
	require.Equal(t, 403, kratoserrors.Code(err), "a revoked actor cannot cancel the original CREATE")
	require.Equal(t, 1, f.owner.gets, "revoked authorization cannot reach the owner")
	require.NoError(t, f.client.UserRole.UpdateOneID(f.membership.ID).SetStatus(userrole.StatusActive).Exec(sys))
	canceled, err := f.service.DeleteInference(f.ctx, request)
	require.NoError(t, err, "a queued GPU CREATE can be canceled before the owner receives it")
	require.Equal(t, "CANCELED_BEFORE_DISPATCH", canceled.Result)
	require.Empty(t, canceled.DispatchOperationId)
	require.Equal(t, 1, f.owner.gets, "local cancellation does not require an owner query")
	replay, err := f.service.DeleteInference(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, canceled, replay)
	charges, err := f.ledger.GetChargesForOperation(f.ctx, f.tenantID, created.OperationId)
	require.NoError(t, err)
	require.Len(t, charges, 1)
	for _, charge := range charges {
		require.EqualValues(t, 12288, charge.OriginalUnits)
		require.Equal(t, charge.OriginalUnits, charge.ReleasedUnits, "the complete original charge is refunded once")
	}
	rows, err := f.ledger.RecomputeInvariants(context.Background(), f.tenantID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Balanced)
	require.Zero(t, rows[0].OccupiedUnits)
	// Configured GPU acceptance must preserve both existing CPU actor types.
	principal, err := auth.PrincipalFromContext(f.ctx)
	require.NoError(t, err)
	for _, subject := range []auth.SubjectType{auth.SubjectUser, auth.SubjectAPIKey} {
		ctx := auth.NewPrincipalContext(f.ctx, &auth.Principal{Type: subject, ID: principal.ID, TenantID: principal.TenantID})
		f.owner.service = &inferencev1.InferenceService{Id: "cpu-" + uuid.NewString(), Generation: 7}
		cpuRequest := &view.DeleteInferenceRequest{Data: &view.DeleteInferenceData{IdempotencyKey: uuid.NewString(), ResourceId: f.owner.service.Id}}
		_, err := f.service.DeleteInference(ctx, cpuRequest)
		require.NoError(t, err, "CPU deletion retains trusted JWT and API Key support")
		actor, err := (&auth.Principal{Type: subject, ID: principal.ID, TenantID: principal.TenantID}).Actor()
		require.NoError(t, err)
		require.Equal(t, actor, f.owner.actor)
		require.EqualValues(t, 7, f.owner.delete.ExpectedGeneration)
	}
	apiKeyCtx := auth.NewPrincipalContext(f.ctx, &auth.Principal{Type: auth.SubjectAPIKey, ID: principal.ID, TenantID: principal.TenantID})
	f.owner.service = &inferencev1.InferenceService{Id: created.ResourceId, Generation: 1, Resource: &inferencev1.ResourceSpec{Gpu: &inferencev1.GpuRequest{}}}
	deletes := f.owner.deletes
	_, err = f.service.DeleteInference(apiKeyCtx, request)
	require.Error(t, err, "API Keys cannot bypass GPU tenant-user admission")
	require.Equal(t, deletes, f.owner.deletes)
}

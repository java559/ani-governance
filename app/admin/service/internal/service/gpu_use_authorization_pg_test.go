//go:build quota_pg

package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
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
	"go-wind-admin/pkg/middleware/auth"
)

// This exercises the shared authorization capability only. It does not add a
// training compute consumer, acceptance record, execution or ModelDev wiring.
func TestGpuTrainingUseCombinesRealBusinessAuthorizationWithoutInstance(t *testing.T) {
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	sys := appViewer.NewSystemViewerContext(context.Background())
	name := "training-use-" + uuid.NewString()
	plan, err := c.Client().Plan.Create().SetName(name).Save(sys)
	require.NoError(t, err)
	for _, module := range []planmodule.Module{planmodule.ModuleModel, planmodule.ModuleAccelerator} {
		require.NoError(t, c.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(module).Exec(sys))
	}
	tenant, err := c.Client().Tenant.Create().SetName(name).SetCode(name).SetPlanID(plan.ID).Save(sys)
	require.NoError(t, err)
	operator, err := c.Client().User.Create().SetTenantID(tenant.ID).SetUsername(name).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	memberRole, err := c.Client().Role.Create().SetTenantID(tenant.ID).SetName(name).SetCode("tenant:" + name).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	membership, err := c.Client().UserRole.Create().SetTenantID(tenant.ID).SetUserID(operator.ID).SetRoleID(memberRole.ID).SetStatus(userrole.StatusActive).Save(sys)
	require.NoError(t, err)
	permissionRow, err := c.Client().Permission.Create().SetName(name).SetCode("modeldev:create:" + name).SetStatus(permission.StatusOn).Save(sys)
	require.NoError(t, err)
	route, err := c.Client().Api.Query().Where(api.ModuleEQ("ModelDevService"), api.PathEQ("/api/v1/modeldev/executions"), api.MethodEQ("POST"), api.ScopeEQ(api.ScopeAdmin)).Only(sys)
	if ent.IsNotFound(err) {
		route, err = c.Client().Api.Create().SetModule("ModelDevService").SetPath("/api/v1/modeldev/executions").SetMethod("POST").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleModel).SetStatus(api.StatusOn).Save(sys)
	}
	require.NoError(t, err)
	require.NoError(t, c.Client().PermissionApi.Create().SetPermissionID(permissionRow.ID).SetAPIID(route.ID).Exec(sys))
	require.NoError(t, c.Client().RolePermission.Create().SetTenantID(tenant.ID).SetRoleID(memberRole.ID).SetPermissionID(permissionRow.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Exec(sys))
	principal := &auth.Principal{Type: auth.SubjectUser, ID: operator.ID, TenantID: tenant.ID}
	ctx := auth.NewPrincipalContext(sys, principal)
	business := data.NewModelDevAuthorizationRepo(c)
	eligibility := data.NewGpuUseAuthorizationRepo(c)
	for _, purpose := range []string{"ordinary-training", "fine-tuning"} {
		t.Run(purpose, func(t *testing.T) {
			grant, err := authorizeGpuBusiness(ctx, principal, compute.Usage_TRAINING, "/modeldev.v1.ModelDevService/CreateExecution", func() error {
				return business.AuthorizeCreate(ctx, tenant.ID, operator.ID)
			})
			require.NoError(t, err)
			require.NoError(t, authorizeNewGpuUse(ctx, grant, eligibility), "MODEL business grant plus ACCELERATOR, without INSTANCE or Profile GET")
		})
	}
	require.ErrorIs(t, data.NewInstanceAuthorizationRepo(c).AuthorizeCatalog(ctx, tenant.ID, operator.ID), data.ErrActionAuthorizationDenied)
	require.NoError(t, c.Client().UserRole.UpdateOneID(membership.ID).SetStatus(userrole.StatusDisabled).Exec(sys))
	grant, err := authorizeGpuBusiness(ctx, principal, compute.Usage_TRAINING, "/modeldev.v1.ModelDevService/CreateExecution", func() error {
		return business.AuthorizeCreate(ctx, tenant.ID, operator.ID)
	})
	require.ErrorIs(t, err, data.ErrModelDevAuthorizationDenied)
	require.Error(t, authorizeNewGpuUse(ctx, grant, eligibility), "eligibility alone cannot authorize training")
}

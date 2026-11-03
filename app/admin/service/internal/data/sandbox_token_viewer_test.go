package data

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
)

func TestSandboxTokenTenantViewerChecksCurrentIssuerGrant(t *testing.T) {
	c := testutil.NewEntClientForTest(t)
	sys := testutil.NewSystemViewerCtx(context.Background())
	db := c.Client()
	p := db.Plan.Create().SetExpiryPolicy(plan.ExpiryPolicyReadonly).SaveX(sys)
	tenantRow := db.Tenant.Create().SetName("token-viewer").SetStatus(tenant.StatusOn).SetPlanID(p.ID).SaveX(sys)
	db.PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleInstance).SaveX(sys)
	issuer := db.User.Create().SetTenantID(tenantRow.ID).SetUsername("token-issuer").SetStatus(user.StatusNormal).SaveX(sys)
	roleRow := db.Role.Create().SetTenantID(tenantRow.ID).SetName("files").SetCode("tenant:token-test").SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).SaveX(sys)
	db.UserRole.Create().SetTenantID(tenantRow.ID).SetUserID(issuer.ID).SetRoleID(roleRow.ID).SetStatus(userrole.StatusActive).SaveX(sys)
	perm := db.Permission.Create().SetName("sandbox-files-read").SetCode("sandbox:files:read").SetStatus(permission.StatusOn).SaveX(sys)
	route := db.Api.Create().SetModule("SandboxService").SetPath(SandboxFilesContentPath).SetMethod("GET").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleInstance).SetStatus(api.StatusOn).SaveX(sys)
	db.PermissionApi.Create().SetPermissionID(perm.ID).SetAPIID(route.ID).SaveX(sys)
	db.RolePermission.Create().SetTenantID(tenantRow.ID).SetRoleID(roleRow.ID).SetPermissionID(perm.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).SaveX(sys)
	tokenContext := viewer.WithContext(context.Background(), appViewer.NewUserViewer(0, uint64(tenantRow.ID), 0, "", nil))
	checker := &TenantAccessCheckerImpl{entClient: db, log: bLogger.NewHelper(bLogger.NopLogger())}
	require.NoError(t, checker.CheckTenantAccess(tokenContext, tenantRow.ID, SandboxFilesContentPath, "GET"))
	authz := NewInstanceAuthorizationRepo(c)
	require.NoError(t, authz.Authorize(tokenContext, tenantRow.ID, issuer.ID, SandboxFilesContentPath, "GET"))
	db.Role.UpdateOneID(roleRow.ID).SetStatus(role.StatusOff).SaveX(sys)
	require.ErrorIs(t, authz.Authorize(tokenContext, tenantRow.ID, issuer.ID, SandboxFilesContentPath, "GET"), ErrActionAuthorizationDenied)
}

//go:build quota_pg

package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	compute "github.com/zhangzhe-ctrl/ani-resource-service/api/compute/v1"
	view "go-wind-admin/api/gen/go/compute/service/v1"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/permissionapi"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/app/admin/service/tests/testutil"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/middleware/auth"
	"go-wind-admin/pkg/permcode"
)

// Real Service + current RR authorization + persistent Ent state; only Resource
// is an external boundary double. Token strings cannot grant another usage.
func TestComputeCatalogCurrentUsageAuthorizationPostgres(t *testing.T) {
	c := testutil.NewQuotaPGClient(t)
	testutil.ResetQuotaFixture(t, c)
	sys := appViewer.NewSystemViewerContext(context.Background())
	name := "compute-catalog-" + uuid.NewString()
	plan, err := c.Client().Plan.Create().SetName(name).Save(sys)
	require.NoError(t, err)
	tenant, err := c.Client().Tenant.Create().SetName(name).SetCode(name).SetPlanID(plan.ID).Save(sys)
	require.NoError(t, err)
	member, err := c.Client().User.Create().SetTenantID(tenant.ID).SetUsername(name).SetStatus(user.StatusNormal).Save(sys)
	require.NoError(t, err)
	r, err := c.Client().Role.Create().SetTenantID(tenant.ID).SetName(name).SetCode("tenant:" + name).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(sys)
	require.NoError(t, err)
	membership, err := c.Client().UserRole.Create().SetTenantID(tenant.ID).SetUserID(member.ID).SetRoleID(r.ID).SetStatus(userrole.StatusActive).Save(sys)
	require.NoError(t, err)
	// API/permission catalogs are global and survive tenant fixture resets.
	route, err := c.Client().Api.Query().Where(api.ModuleEQ("ComputePresetService"), api.PathEQ(data.ComputeCatalogPath), api.MethodEQ("GET"), api.ScopeEQ(api.ScopeAdmin)).Only(sys)
	if ent.IsNotFound(err) {
		route, err = c.Client().Api.Create().SetModule("ComputePresetService").SetOperation("ComputePresetService_ListPresets").SetPath(data.ComputeCatalogPath).SetMethod("GET").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleSystem).SetStatus(api.StatusOn).Save(sys)
	}
	require.NoError(t, err)
	require.NotNil(t, route.BusinessModule)
	require.Equal(t, api.BusinessModuleSystem, *route.BusinessModule)
	require.NotNil(t, route.Status)
	require.Equal(t, api.StatusOn, *route.Status)
	gate := data.NewComputeCatalogAuthorizationRepo(c)
	checker := data.NewTenantAccessCheckerImpl(testutil.NewBootstrapContext(nil), c).(auth.TenantRequestAccessChecker)
	usages := []string{"CONTAINER_INSTANCE", "TRAINING", "INFERENCE"}
	entitlements := map[string]uint32{}
	for _, usage := range usages {
		module, _, _ := permcode.ComputeCatalogGrant(usage)
		row, e := c.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.Module(module)).Save(sys)
		require.NoError(t, e)
		entitlements[usage] = row.ID
	}

	// No INSTANCE dependency for training/inference, no cross-usage role leakage.
	for _, usage := range usages {
		t.Run(usage, func(t *testing.T) {
			module, code, _ := permcode.ComputeCatalogGrant(usage)
			entitlementID := entitlements[usage]
			grant, err := c.Client().Permission.Query().Where(permission.CodeEQ(code)).Only(sys)
			if ent.IsNotFound(err) {
				grant, err = c.Client().Permission.Create().SetName(code).SetCode(code).SetStatus(permission.StatusOn).Save(sys)
			}
			require.NoError(t, err)
			require.NotNil(t, grant.Status)
			require.Equal(t, permission.StatusOn, *grant.Status)
			linked, err := c.Client().PermissionApi.Query().Where(permissionapi.PermissionIDEQ(grant.ID), permissionapi.APIIDEQ(route.ID)).Exist(sys)
			require.NoError(t, err)
			if !linked {
				require.NoError(t, c.Client().PermissionApi.Create().SetPermissionID(grant.ID).SetAPIID(route.ID).Exec(sys))
			}
			relation, err := c.Client().RolePermission.Create().SetTenantID(tenant.ID).SetRoleID(r.ID).SetPermissionID(grant.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(sys)
			require.NoError(t, err)
			require.NoError(t, gate.AuthorizeComputeCatalog(sys, tenant.ID, member.ID, usage))
			require.NoError(t, checker.CheckTenantRequestAccess(sys, tenant.ID, data.ComputeCatalogPath, "GET", &view.ListPresetsRequest{Usage: usage}))
			for _, other := range usages {
				if other != usage {
					require.ErrorIs(t, gate.AuthorizeComputeCatalog(sys, tenant.ID, member.ID, other), data.ErrActionAuthorizationDenied)
				}
			}
			require.ErrorIs(t, gate.AuthorizeComputeCatalog(sys, tenant.ID+1, member.ID, usage), data.ErrActionAuthorizationDenied)
			require.NoError(t, c.Client().Permission.UpdateOneID(grant.ID).SetStatus(permission.StatusOff).Exec(sys))
			require.ErrorIs(t, gate.AuthorizeComputeCatalog(sys, tenant.ID, member.ID, usage), data.ErrActionAuthorizationDenied)
			require.NoError(t, c.Client().Permission.UpdateOneID(grant.ID).SetStatus(permission.StatusOn).Exec(sys))
			require.NoError(t, c.Client().UserRole.UpdateOneID(membership.ID).SetStatus(userrole.StatusDisabled).Exec(sys))
			require.ErrorIs(t, gate.AuthorizeComputeCatalog(sys, tenant.ID, member.ID, usage), data.ErrActionAuthorizationDenied)
			require.NoError(t, c.Client().UserRole.UpdateOneID(membership.ID).SetStatus(userrole.StatusActive).Exec(sys))
			require.NoError(t, c.Client().PlanModule.DeleteOneID(entitlementID).Exec(sys))
			require.ErrorIs(t, gate.AuthorizeComputeCatalog(sys, tenant.ID, member.ID, usage), data.ErrActionAuthorizationDenied)
			require.Error(t, checker.CheckTenantRequestAccess(sys, tenant.ID, data.ComputeCatalogPath, "GET", &view.ListPresetsRequest{Usage: usage}))
			restored, e := c.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.Module(module)).Save(sys)
			require.NoError(t, e)
			entitlements[usage] = restored.ID
			if usage == "TRAINING" {
				require.NoError(t, c.Client().PlanModule.DeleteOneID(entitlements["CONTAINER_INSTANCE"]).Exec(sys))
				require.NoError(t, gate.AuthorizeComputeCatalog(sys, tenant.ID, member.ID, usage), "training must not require INSTANCE")
				restored, e = c.Client().PlanModule.Create().SetPlanID(plan.ID).SetModule(planmodule.ModuleInstance).Save(sys)
				require.NoError(t, e)
				entitlements["CONTAINER_INSTANCE"] = restored.ID
			}
			require.NoError(t, c.Client().RolePermission.DeleteOneID(relation.ID).Exec(sys))
		})
	}
	require.ErrorIs(t, gate.AuthorizeComputeCatalog(sys, tenant.ID, member.ID, ""), data.ErrActionAuthorizationDenied)
	// A permission string in an identity is not sufficient for current authorization.
	ctx := auth.NewPrincipalContext(sys, &auth.Principal{Type: auth.SubjectUser, TenantID: tenant.ID, ID: member.ID})
	owner := &computeCatalogDenyOwner{t: t}
	svc := NewComputePresetService(nil, ComputeCatalogConfig{Compute: owner, Resolver: instancePresetTenantResolver{}, Authorization: gate})
	_, err = svc.ListPresets(ctx, &view.ListPresetsRequest{Usage: "INFERENCE"})
	require.Error(t, err)
}

type computeCatalogDenyOwner struct{ t *testing.T }

func (o *computeCatalogDenyOwner) ListPresets(context.Context, string, string, *compute.ListPresetsRequest) (*compute.ListPresetsResponse, error) {
	o.t.Fatal("unauthorized request reached Resource")
	return nil, nil
}

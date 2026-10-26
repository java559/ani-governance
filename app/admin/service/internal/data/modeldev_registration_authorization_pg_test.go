//go:build modeldev_pg

package data

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

func TestModelDevRegistrationResumeRequiresCurrentManageCodeAndExactAPI(t *testing.T) {
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 20*time.Second)
	t.Cleanup(cancel)
	client := newModelDevPGClient(t)
	cleanup := func(remove func(context.Context) error) {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
			defer cancel()
			require.NoError(t, remove(ctx))
		})
	}
	id := uuid.NewString()
	owner, err := client.Client().Tenant.Create().SetCode("md-resume-" + id).SetName("ModelDev resume " + id).SetStatus(tenant.StatusOn).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().Tenant.DeleteOneID(owner.ID).Exec(c) })
	operator, err := client.Client().User.Create().SetTenantID(owner.ID).SetUsername("md-resume-" + id).SetStatus(user.StatusNormal).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().User.DeleteOneID(operator.ID).Exec(c) })
	currentRole, err := client.Client().Role.Create().SetTenantID(owner.ID).SetName("Resume manager").SetCode("tenant:md-resume:" + id).SetType(role.TypeTenant).SetDataScope(role.DataScopeAll).SetStatus(role.StatusOn).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().Role.DeleteOneID(currentRole.ID).Exec(c) })
	member, err := client.Client().UserRole.Create().SetTenantID(owner.ID).SetUserID(operator.ID).SetRoleID(currentRole.ID).SetStatus(userrole.StatusActive).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().UserRole.DeleteOneID(member.ID).Exec(c) })
	manage, err := client.Client().Permission.Create().SetCode(ModelDevManageExecutionPermissionCode).SetName("Original execution management").SetStatus(permission.StatusOn).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().Permission.DeleteOneID(manage.ID).Exec(c) })
	grant, err := client.Client().RolePermission.Create().SetTenantID(owner.ID).SetRoleID(currentRole.ID).SetPermissionID(manage.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().RolePermission.DeleteOneID(grant.ID).Exec(c) })
	repo := NewModelDevAuthorizationRepo(newModelDevPGClient(t))
	require.NoError(t, repo.AuthorizeManageExecution(ctx, owner.ID, operator.ID))
	require.ErrorIs(t, repo.AuthorizeResume(ctx, owner.ID, operator.ID), ErrModelDevAuthorizationDenied, "CLI management code alone cannot authorize a new HTTP action")
	route, err := client.Client().Api.Create().SetModule("modeldev-resume-" + id).SetOperation("resume-" + id).SetPath("/api/v1/modeldev/executions/{execution_id}:resume-model-registration").SetMethod("POST").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleModel).SetStatus(api.StatusOn).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().Api.DeleteOneID(route.ID).Exec(c) })
	link, err := client.Client().PermissionApi.Create().SetPermissionID(manage.ID).SetAPIID(route.ID).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().PermissionApi.DeleteOneID(link.ID).Exec(c) })
	require.ErrorIs(t, repo.AuthorizeResume(ctx, owner.ID, operator.ID), ErrModelDevAuthorizationDenied, "resume also requires current execution read authority")
	readAPI, err := client.Client().Api.Create().SetModule("modeldev-resume-read-" + id).SetOperation("resume-read-" + id).SetPath(ModelDevGetExecutionPath).SetMethod("GET").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleModel).SetStatus(api.StatusOn).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().Api.DeleteOneID(readAPI.ID).Exec(c) })
	readLink, err := client.Client().PermissionApi.Create().SetPermissionID(manage.ID).SetAPIID(readAPI.ID).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().PermissionApi.DeleteOneID(readLink.ID).Exec(c) })
	require.NoError(t, repo.AuthorizeResume(ctx, owner.ID, operator.ID))
	for _, change := range []struct{ disable, restore func() error }{
		{func() error {
			return client.Client().UserRole.UpdateOneID(member.ID).SetStatus(userrole.StatusDisabled).Exec(ctx)
		}, func() error {
			return client.Client().UserRole.UpdateOneID(member.ID).SetStatus(userrole.StatusActive).Exec(ctx)
		}},
		{func() error {
			return client.Client().RolePermission.UpdateOneID(grant.ID).SetStatus(rolepermission.StatusOff).Exec(ctx)
		}, func() error {
			return client.Client().RolePermission.UpdateOneID(grant.ID).SetStatus(rolepermission.StatusOn).Exec(ctx)
		}},
		{func() error { return client.Client().Api.UpdateOneID(route.ID).SetStatus(api.StatusOff).Exec(ctx) }, func() error { return client.Client().Api.UpdateOneID(route.ID).SetStatus(api.StatusOn).Exec(ctx) }},
		{func() error { return client.Client().Api.UpdateOneID(readAPI.ID).SetStatus(api.StatusOff).Exec(ctx) }, func() error { return client.Client().Api.UpdateOneID(readAPI.ID).SetStatus(api.StatusOn).Exec(ctx) }},
		{func() error {
			return client.Client().Role.UpdateOneID(currentRole.ID).SetDataScope(role.DataScopeSelf).Exec(ctx)
		}, func() error {
			return client.Client().Role.UpdateOneID(currentRole.ID).SetDataScope(role.DataScopeAll).Exec(ctx)
		}},
	} {
		require.NoError(t, change.disable())
		require.ErrorIs(t, repo.AuthorizeResume(ctx, owner.ID, operator.ID), ErrModelDevAuthorizationDenied)
		require.NoError(t, change.restore())
	}
}

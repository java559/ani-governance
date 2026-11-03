//go:build modeldev_pg

package data

import (
	"context"
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
	"testing"
	"time"
)

func TestModelDevEnvironmentCurrentIndependentGrantsAndRevocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 20*time.Second)
	defer cancel()
	client := newModelDevPGClient(t)
	id := uuid.NewString()
	remove := func(fn func(context.Context) error) {
		t.Cleanup(func() {
			c, stop := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
			defer stop()
			require.NoError(t, fn(c))
		})
	}
	owner, err := client.Client().Tenant.Create().SetName("environment auth " + id).SetCode("env-" + id).SetStatus(tenant.StatusOn).Save(ctx)
	require.NoError(t, err)
	remove(func(c context.Context) error { return client.Client().Tenant.DeleteOneID(owner.ID).Exec(c) })
	operator, err := client.Client().User.Create().SetTenantID(owner.ID).SetUsername("env-" + id).SetStatus(user.StatusNormal).Save(ctx)
	require.NoError(t, err)
	remove(func(c context.Context) error { return client.Client().User.DeleteOneID(operator.ID).Exec(c) })
	currentRole, err := client.Client().Role.Create().SetTenantID(owner.ID).SetName("environment auth").SetCode("tenant:env-" + id).SetType(role.TypeTenant).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(ctx)
	require.NoError(t, err)
	remove(func(c context.Context) error { return client.Client().Role.DeleteOneID(currentRole.ID).Exec(c) })
	member, err := client.Client().UserRole.Create().SetTenantID(owner.ID).SetUserID(operator.ID).SetRoleID(currentRole.ID).SetStatus(userrole.StatusActive).Save(ctx)
	require.NoError(t, err)
	remove(func(c context.Context) error { return client.Client().UserRole.DeleteOneID(member.ID).Exec(c) })
	repo := NewModelDevAuthorizationRepo(client)
	require.ErrorIs(t, repo.AuthorizeEnvironment(ctx, owner.ID, operator.ID, true), ErrModelDevAuthorizationDenied)
	require.ErrorIs(t, repo.AuthorizeEnvironment(ctx, 0, operator.ID, false), ErrModelDevAuthorizationDenied)
	for _, ensure := range []bool{false, true} {
		code, path, method := ModelDevViewEnvironmentPermissionCode, ModelDevGetEnvironmentPath, "GET"
		if ensure {
			code, path, method = ModelDevManageEnvironmentPermissionCode, ModelDevEnsureEnvironmentPath, "POST"
		}
		p, err := client.Client().Permission.Create().SetCode(code).SetName(code).SetStatus(permission.StatusOn).Save(ctx)
		require.NoError(t, err)
		permissionID := p.ID
		remove(func(c context.Context) error { return client.Client().Permission.DeleteOneID(permissionID).Exec(c) })
		a, err := client.Client().Api.Create().SetPath(path).SetMethod(method).SetModule("modeldev-env").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleModel).SetStatus(api.StatusOn).Save(ctx)
		require.NoError(t, err)
		apiID := a.ID
		remove(func(c context.Context) error { return client.Client().Api.DeleteOneID(apiID).Exec(c) })
		link, err := client.Client().PermissionApi.Create().SetPermissionID(p.ID).SetAPIID(a.ID).Save(ctx)
		require.NoError(t, err)
		linkID := link.ID
		remove(func(c context.Context) error { return client.Client().PermissionApi.DeleteOneID(linkID).Exec(c) })
		grant, err := client.Client().RolePermission.Create().SetTenantID(owner.ID).SetRoleID(currentRole.ID).SetPermissionID(p.ID).SetEffect(rolepermission.EffectAllow).SetStatus(rolepermission.StatusOn).Save(ctx)
		require.NoError(t, err)
		grantID := grant.ID
		remove(func(c context.Context) error { return client.Client().RolePermission.DeleteOneID(grantID).Exec(c) })
		require.NoError(t, repo.AuthorizeEnvironment(ctx, owner.ID, operator.ID, ensure))
		if !ensure {
			require.ErrorIs(t, repo.AuthorizeEnvironment(ctx, owner.ID, operator.ID, true), ErrModelDevAuthorizationDenied)
		}
		require.NoError(t, client.Client().RolePermission.UpdateOneID(grant.ID).SetStatus(rolepermission.StatusOff).Exec(ctx))
		require.ErrorIs(t, repo.AuthorizeEnvironment(ctx, owner.ID, operator.ID, ensure), ErrModelDevAuthorizationDenied, "same request/retry must see revocation")
		require.NoError(t, client.Client().RolePermission.UpdateOneID(grant.ID).SetStatus(rolepermission.StatusOn).Exec(ctx))
		require.NoError(t, client.Client().Api.UpdateOneID(a.ID).SetPath(path+"/foreign").Exec(ctx))
		require.ErrorIs(t, repo.AuthorizeEnvironment(ctx, owner.ID, operator.ID, ensure), ErrModelDevAuthorizationDenied)
		require.NoError(t, client.Client().Api.UpdateOneID(a.ID).SetPath(path).Exec(ctx))
	}
	require.NoError(t, client.Client().Role.UpdateOneID(currentRole.ID).SetDataScope(role.DataScopeSelf).Exec(ctx))
	require.ErrorIs(t, repo.AuthorizeEnvironment(ctx, owner.ID, operator.ID, true), ErrModelDevAuthorizationDenied)
	require.NoError(t, client.Client().Role.UpdateOneID(currentRole.ID).SetDataScope(role.DataScopeAll).Exec(ctx))
	var acceptances, charges int
	acceptances, err = client.Client().ModelDevAcceptance.Query().Count(ctx)
	require.NoError(t, err)
	charges, err = client.Client().QuotaCharge.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, acceptances)
	require.Zero(t, charges)
}

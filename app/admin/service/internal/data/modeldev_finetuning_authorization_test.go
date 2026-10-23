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

func TestModelDevFinetuningRequiresCurrentModelAPIRights(t *testing.T) {
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 20*time.Second)
	t.Cleanup(cancel)
	client := newModelDevPGClient(t)
	cleanup := func(remove func(context.Context) error) {
		t.Cleanup(func() {
			c, stop := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
			defer stop()
			require.NoError(t, remove(c))
		})
	}
	id := uuid.NewString()
	owner, err := client.Client().Tenant.Create().SetCode("md-model-auth-" + id).SetName("ModelDev model rights " + id).SetStatus(tenant.StatusOn).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().Tenant.DeleteOneID(owner.ID).Exec(c) })
	operator, err := client.Client().User.Create().SetTenantID(owner.ID).SetUsername("md-model-auth-" + id).SetStatus(user.StatusNormal).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().User.DeleteOneID(operator.ID).Exec(c) })
	currentRole, err := client.Client().Role.Create().SetTenantID(owner.ID).SetName("ModelDev model rights " + id).SetCode("tenant:md-model:" + id).SetType(role.TypeTenant).SetDataScope(role.DataScopeAll).SetStatus(role.StatusOn).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().Role.DeleteOneID(currentRole.ID).Exec(c) })
	member, err := client.Client().UserRole.Create().SetTenantID(owner.ID).SetUserID(operator.ID).SetRoleID(currentRole.ID).SetStatus(userrole.StatusActive).Save(ctx)
	require.NoError(t, err)
	cleanup(func(c context.Context) error { return client.Client().UserRole.DeleteOneID(member.ID).Exec(c) })

	type requiredAPI struct{ path, method string }
	paths := []requiredAPI{
		{"/api/v1/model-versions/{model_version_id}", "GET"},
		{"/api/v1/model-versions/{model_version_id}/download-url", "GET"},
		{"/api/v1/model-versions", "POST"},
		{"/api/v1/models/{model_id}", "GET"},
		{"/api/v1/models", "POST"},
		{"/api/v1/modeldev/executions", "POST"},
	}
	var grants []uint32
	for i, route := range paths {
		p, err := client.Client().Permission.Create().SetCode("md-model-right:" + uuid.NewString()).SetName("Model API fixture").SetStatus(permission.StatusOn).Save(ctx)
		require.NoError(t, err)
		cleanup(func(c context.Context) error { return client.Client().Permission.DeleteOneID(p.ID).Exec(c) })
		a, err := client.Client().Api.Create().SetModule("md-model-" + id).SetOperation("md-model-" + uuid.NewString()).SetPath(route.path).SetMethod(route.method).SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleModel).SetStatus(api.StatusOn).Save(ctx)
		require.NoError(t, err)
		cleanup(func(c context.Context) error { return client.Client().Api.DeleteOneID(a.ID).Exec(c) })
		link, err := client.Client().PermissionApi.Create().SetPermissionID(p.ID).SetAPIID(a.ID).Save(ctx)
		require.NoError(t, err)
		cleanup(func(c context.Context) error { return client.Client().PermissionApi.DeleteOneID(link.ID).Exec(c) })
		grant, err := client.Client().RolePermission.Create().SetTenantID(owner.ID).SetRoleID(currentRole.ID).SetPermissionID(p.ID).SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(ctx)
		require.NoError(t, err)
		cleanup(func(c context.Context) error { return client.Client().RolePermission.DeleteOneID(grant.ID).Exec(c) })
		require.Equal(t, i, len(grants))
		grants = append(grants, grant.ID)
	}
	repo := NewModelDevAuthorizationRepo(client)
	require.NoError(t, repo.AuthorizeFinetuning(ctx, owner.ID, operator.ID, false))
	require.NoError(t, repo.AuthorizeFinetuning(ctx, owner.ID, operator.ID, true))
	for i, route := range paths[:5] {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			require.NoError(t, client.Client().RolePermission.UpdateOneID(grants[i]).SetStatus(rolepermission.StatusOff).Exec(ctx))
			for _, newModel := range []bool{false, true} {
				err := repo.AuthorizeFinetuning(ctx, owner.ID, operator.ID, newModel)
				if i < 3 || i == 3 && !newModel || i == 4 && newModel {
					require.ErrorIs(t, err, ErrModelDevAuthorizationDenied)
				} else {
					require.NoError(t, err)
				}
			}
			// Ordinary training retains its independent create right and never
			// requires Model read/write access.
			require.NoError(t, repo.AuthorizeCreate(ctx, owner.ID, operator.ID))
			require.NoError(t, client.Client().RolePermission.UpdateOneID(grants[i]).SetStatus(rolepermission.StatusOn).Exec(ctx))
		})
	}
}

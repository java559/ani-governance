//go:build modeldev_pg

package data

import (
	"context"
	"database/sql"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"os"
	"testing"
	"time"
)

func TestModelDevEnvironmentPermissionDataStepGrantsOnlyTenantManagers(t *testing.T) {
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 20*time.Second)
	defer cancel()
	client := newModelDevPGClient(t)
	id := uuid.NewString()
	owner, err := client.Client().Tenant.Create().SetName("environment permission data " + id).SetCode("env-perm-" + id).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, client.Client().Tenant.DeleteOneID(owner.ID).Exec(appViewer.NewSystemViewerContext(context.Background())))
	})
	var roles []uint32
	for _, c := range []struct {
		tenant uint32
		code   string
		kind   role.Type
	}{{0, "template:tenant:manager", role.TypeTemplate}, {owner.ID, "tenant:manager", role.TypeTenant}, {owner.ID, "tenant:viewer", role.TypeTenant}} {
		r, err := client.Client().Role.Create().SetTenantID(c.tenant).SetName(c.code).SetCode(c.code).SetType(c.kind).SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(ctx)
		require.NoError(t, err)
		roles = append(roles, r.ID)
		roleID := r.ID
		t.Cleanup(func() {
			require.NoError(t, client.Client().Role.DeleteOneID(roleID).Exec(appViewer.NewSystemViewerContext(context.Background())))
		})
	}
	for _, c := range []struct{ path, method string }{{ModelDevEnsureEnvironmentPath, "POST"}, {ModelDevGetEnvironmentPath, "GET"}} {
		a, err := client.Client().Api.Create().SetPath(c.path).SetMethod(c.method).SetModule("modeldev-env").SetScope(api.ScopeAdmin).SetBusinessModule(api.BusinessModuleModel).SetStatus(api.StatusOn).Save(ctx)
		require.NoError(t, err)
		apiID := a.ID
		t.Cleanup(func() {
			require.NoError(t, client.Client().Api.DeleteOneID(apiID).Exec(appViewer.NewSystemViewerContext(context.Background())))
		})
	}
	count, err := client.Client().Permission.Query().Where(permission.CodeIn(ModelDevManageEnvironmentPermissionCode, ModelDevViewEnvironmentPermissionCode)).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count, "exclusive input has no environment grant before the data step")
	db, err := sql.Open("pgx", os.Getenv("ANI_TEST_DATABASE_DSN"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	raw, err := os.ReadFile("../../../../../sql/data/20261101010000_modeldev_environment_permissions.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(raw))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := appViewer.NewSystemViewerContext(context.Background())
		ps, err := client.Client().Permission.Query().Where(permission.CodeIn(ModelDevManageEnvironmentPermissionCode, ModelDevViewEnvironmentPermissionCode)).All(cleanup)
		require.NoError(t, err)
		for _, p := range ps {
			_, err = client.Client().RolePermission.Delete().Where(rolepermission.PermissionIDEQ(p.ID)).Exec(cleanup)
			require.NoError(t, err)
			_, err = db.ExecContext(cleanup, "DELETE FROM sys_permission_apis WHERE permission_id=$1", p.ID)
			require.NoError(t, err)
			require.NoError(t, client.Client().Permission.DeleteOneID(p.ID).Exec(cleanup))
		}
	})
	for i, roleID := range roles {
		count, err = client.Client().RolePermission.Query().Where(rolepermission.RoleIDEQ(roleID), rolepermission.StatusEQ(rolepermission.StatusOn), rolepermission.EffectEQ(rolepermission.EffectAllow)).Count(ctx)
		require.NoError(t, err)
		want := 2
		if i == 2 {
			want = 0
		}
		require.Equal(t, want, count)
	}
	// Reapplying the same reviewed step does not widen the read-only role.
	_, err = db.ExecContext(ctx, string(raw))
	require.NoError(t, err)
	count, err = client.Client().RolePermission.Query().Where(rolepermission.RoleIDEQ(roles[2])).Count(ctx)
	require.NoError(t, err)
	require.Zero(t, count)
}

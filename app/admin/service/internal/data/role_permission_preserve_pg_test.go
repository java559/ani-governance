//go:build quota_pg

package data

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	conf "go-wind-admin/pkg/localdeps/kratos-bootstrap/api/gen/go/conf/v1"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
)

func TestRolePermissionPostgresAddPreservesExistingGrants(t *testing.T) {
	c := newLedgerPGClient(t)
	ctx := appViewer.NewSystemViewerContext(context.Background())
	suffix := uuid.NewString()
	tenantRow, err := c.Client().Tenant.Create().SetName("catalog-grants-" + suffix).SetCode("catalog-grants-" + suffix).SetResourceTenantID(uuid.NewString()).Save(ctx)
	require.NoError(t, err)
	roleRow, err := c.Client().Role.Create().SetTenantID(tenantRow.ID).SetName("catalog-grants-" + suffix).SetCode("catalog-grants-" + suffix).SetType(role.TypeTenant).Save(ctx)
	require.NoError(t, err)
	var ids []uint32
	for i := 0; i < 5; i++ {
		p, e := c.Client().Permission.Create().SetCode(uuid.NewString()).SetName("catalog grant").SetStatus(permission.StatusOn).Save(ctx)
		require.NoError(t, e)
		ids = append(ids, p.ID)
	}
	t.Cleanup(func() {
		_, _ = c.Client().RolePermission.Delete().Where(rolepermission.RoleIDEQ(roleRow.ID)).Exec(ctx)
		_ = c.Client().Role.DeleteOneID(roleRow.ID).Exec(ctx)
		_, _ = c.Client().Permission.Delete().Where(permission.IDIn(ids...)).Exec(ctx)
		_ = c.Client().Tenant.DeleteOneID(tenantRow.ID).Exec(ctx)
	})
	stamp := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	retained, err := c.Client().RolePermission.Create().SetTenantID(tenantRow.ID).SetRoleID(roleRow.ID).SetPermissionID(ids[0]).SetStatus(rolepermission.StatusOff).SetEffect(rolepermission.EffectDeny).SetPriority(7).SetCreatedAt(stamp).SetCreatedBy(42).Save(ctx)
	require.NoError(t, err)
	retained, err = c.Client().RolePermission.Get(ctx, retained.ID)
	require.NoError(t, err)
	oldJSON, err := json.Marshal(retained)
	require.NoError(t, err)
	remove, err := c.Client().RolePermission.Create().SetTenantID(tenantRow.ID).SetRoleID(roleRow.ID).SetPermissionID(ids[1]).Save(ctx)
	require.NoError(t, err)
	bctx := bootstrap.NewContextWithParam(ctx, &conf.AppInfo{}, &conf.Bootstrap{}, bLogger.NopLogger())
	repo := NewRolePermissionRepo(bctx, c)
	apply := func(selected []uint32) {
		tx, e := c.Client().Tx(ctx)
		require.NoError(t, e)
		defer tx.Rollback()
		// This is the same serialized transaction boundary as RoleRepo.Update.
		_, e = tx.Role.Query().Where(role.IDEQ(roleRow.ID)).ForUpdate().Only(ctx)
		require.NoError(t, e)
		require.NoError(t, repo.ReplacePermissions(ctx, tx, tenantRow.ID, 1, roleRow.ID, selected))
		require.NoError(t, tx.Commit())
	}
	apply(ids)
	after, err := c.Client().RolePermission.Get(ctx, retained.ID)
	require.NoError(t, err)
	newJSON, err := json.Marshal(after)
	require.NoError(t, err)
	require.JSONEq(t, string(oldJSON), string(newJSON), "an additive role update must retain every old grant field")
	first, err := c.Client().RolePermission.Query().Where(rolepermission.RoleIDEQ(roleRow.ID)).Order(rolepermission.ByID()).All(ctx)
	require.NoError(t, err)
	require.Len(t, first, 5)
	firstJSON, err := json.Marshal(first)
	require.NoError(t, err)
	apply(append(ids, ids[2]))
	repeated, err := c.Client().RolePermission.Query().Where(rolepermission.RoleIDEQ(roleRow.ID)).Order(rolepermission.ByID()).All(ctx)
	require.NoError(t, err)
	repeatedJSON, err := json.Marshal(repeated)
	require.NoError(t, err)
	require.JSONEq(t, string(firstJSON), string(repeatedJSON), "repeating the same role set must not rewrite any grant")
	apply([]uint32{ids[0], ids[2], ids[3], ids[4]})
	_, err = c.Client().RolePermission.Get(ctx, remove.ID)
	require.Error(t, err)
	after, err = c.Client().RolePermission.Get(ctx, retained.ID)
	require.NoError(t, err)
	newJSON, err = json.Marshal(after)
	require.NoError(t, err)
	require.JSONEq(t, string(oldJSON), string(newJSON))
}

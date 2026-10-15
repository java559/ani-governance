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
	"go-wind-admin/pkg/localdeps/go-crud/viewer"
)

func TestModelDevAuthorizationUsesCurrentRoleMembership(t *testing.T) {
	ctx, cancel := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 15*time.Second)
	t.Cleanup(cancel)
	client := newModelDevPGClient(t)
	// Every created row gets its own exact cleanup before the next operation.
	// No shared permissions, API records, schema or bootstrap rows are changed.
	removeAfterTest := func(remove func(context.Context) error) {
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
			defer stop()
			require.NoError(t, remove(cleanup))
		})
	}
	fixtureID := uuid.NewString()
	owner, err := client.Client().Tenant.Create().
		SetName("modeldev current authorization").SetCode("cpu-auth-" + fixtureID).
		SetStatus(tenant.StatusOn).Save(ctx)
	require.NoError(t, err, "PG fixture preparation is not authorization RED")
	removeAfterTest(func(cleanup context.Context) error { return client.Client().Tenant.DeleteOneID(owner.ID).Exec(cleanup) })

	operator, err := client.Client().User.Create().
		SetTenantID(owner.ID).SetUsername("cpu-auth-" + fixtureID).
		SetStatus(user.StatusNormal).Save(ctx)
	require.NoError(t, err, "PG fixture preparation is not authorization RED")
	removeAfterTest(func(cleanup context.Context) error {
		return client.Client().User.DeleteOneID(operator.ID).Exec(cleanup)
	})

	currentRole, err := client.Client().Role.Create().
		SetTenantID(owner.ID).SetName("modeldev create fixture").
		SetCode("tenant:modeldev-auth:" + fixtureID).SetType(role.TypeTenant).
		SetStatus(role.StatusOn).SetDataScope(role.DataScopeAll).Save(ctx)
	require.NoError(t, err, "PG fixture preparation is not authorization RED")
	removeAfterTest(func(cleanup context.Context) error {
		return client.Client().Role.DeleteOneID(currentRole.ID).Exec(cleanup)
	})

	now := time.Now().UTC().Truncate(time.Microsecond)
	starts, ends := now.Add(-time.Hour), now.Add(time.Hour)
	membership, err := client.Client().UserRole.Create().
		SetTenantID(owner.ID).SetUserID(operator.ID).SetRoleID(currentRole.ID).
		SetStatus(userrole.StatusActive).SetStartAt(starts).SetEndAt(ends).Save(ctx)
	require.NoError(t, err, "PG fixture preparation is not authorization RED")
	removeAfterTest(func(cleanup context.Context) error {
		_, cleanupErr := client.Client().UserRole.Delete().Where(userrole.IDEQ(membership.ID)).Exec(cleanup)
		return cleanupErr
	})

	createPermission, err := client.Client().Permission.Create().
		SetName("modeldev create fixture").SetCode("modeldev.execution.create." + fixtureID).
		SetStatus(permission.StatusOn).Save(ctx)
	require.NoError(t, err, "PG fixture preparation is not authorization RED")
	removeAfterTest(func(cleanup context.Context) error {
		return client.Client().Permission.DeleteOneID(createPermission.ID).Exec(cleanup)
	})

	createAPI, err := client.Client().Api.Create().
		SetModule("modeldev-auth-" + fixtureID).SetPath("/api/v1/modeldev/executions").
		SetMethod("POST").SetScope(api.ScopeAdmin).
		SetBusinessModule(api.BusinessModuleModel).SetStatus(api.StatusOn).Save(ctx)
	require.NoError(t, err, "PG fixture preparation is not authorization RED")
	removeAfterTest(func(cleanup context.Context) error {
		return client.Client().Api.DeleteOneID(createAPI.ID).Exec(cleanup)
	})

	permissionAPI, err := client.Client().PermissionApi.Create().
		SetPermissionID(createPermission.ID).SetAPIID(createAPI.ID).Save(ctx)
	require.NoError(t, err, "PG fixture preparation is not authorization RED")
	removeAfterTest(func(cleanup context.Context) error {
		return client.Client().PermissionApi.DeleteOneID(permissionAPI.ID).Exec(cleanup)
	})

	roleGrant, err := client.Client().RolePermission.Create().
		SetTenantID(owner.ID).SetRoleID(currentRole.ID).SetPermissionID(createPermission.ID).
		SetStatus(rolepermission.StatusOn).SetEffect(rolepermission.EffectAllow).Save(ctx)
	require.NoError(t, err, "PG fixture preparation is not authorization RED")
	removeAfterTest(func(cleanup context.Context) error {
		return client.Client().RolePermission.DeleteOneID(roleGrant.ID).Exec(cleanup)
	})

	// A separately opened connection proves the complete committed authorization
	// fixture. Failures above or here are preparation failures, not product RED.
	reader := newModelDevPGClient(t)
	storedTenant, err := reader.Client().Tenant.Get(ctx, owner.ID)
	require.NoError(t, err)
	tenantOn := tenant.StatusOn
	require.Equal(t, &tenantOn, storedTenant.Status)
	require.NotEmpty(t, storedTenant.ResourceTenantID)
	storedUser, err := reader.Client().User.Get(ctx, operator.ID)
	require.NoError(t, err)
	normal := user.StatusNormal
	require.Equal(t, &owner.ID, storedUser.TenantID)
	require.Equal(t, &normal, storedUser.Status)
	storedRole, err := reader.Client().Role.Get(ctx, currentRole.ID)
	require.NoError(t, err)
	roleOn, tenantType, all := role.StatusOn, role.TypeTenant, role.DataScopeAll
	roleCode := "tenant:modeldev-auth:" + fixtureID
	require.Equal(t, &owner.ID, storedRole.TenantID)
	require.Equal(t, &roleOn, storedRole.Status)
	require.Equal(t, &tenantType, storedRole.Type)
	require.Equal(t, &all, storedRole.DataScope)
	require.Equal(t, &roleCode, storedRole.Code)
	storedMembership, err := reader.Client().UserRole.Get(ctx, membership.ID)
	require.NoError(t, err)
	require.Equal(t, &owner.ID, storedMembership.TenantID)
	require.Equal(t, &operator.ID, storedMembership.UserID)
	require.Equal(t, &currentRole.ID, storedMembership.RoleID)
	require.Equal(t, userrole.StatusActive, storedMembership.Status)
	require.NotNil(t, storedMembership.StartAt)
	require.NotNil(t, storedMembership.EndAt)
	require.True(t, starts.Equal(*storedMembership.StartAt))
	require.True(t, ends.Equal(*storedMembership.EndAt))
	storedPermission, err := reader.Client().Permission.Get(ctx, createPermission.ID)
	require.NoError(t, err)
	permissionOn := permission.StatusOn
	require.Equal(t, &permissionOn, storedPermission.Status)
	storedAPI, err := reader.Client().Api.Get(ctx, createAPI.ID)
	require.NoError(t, err)
	apiOn, model, admin := api.StatusOn, api.BusinessModuleModel, api.ScopeAdmin
	path, method := "/api/v1/modeldev/executions", "POST"
	require.Equal(t, &apiOn, storedAPI.Status)
	require.Equal(t, &model, storedAPI.BusinessModule)
	require.Equal(t, &admin, storedAPI.Scope)
	require.Equal(t, &path, storedAPI.Path)
	require.Equal(t, &method, storedAPI.Method)
	storedPermissionAPI, err := reader.Client().PermissionApi.Get(ctx, permissionAPI.ID)
	require.NoError(t, err)
	require.Equal(t, &createPermission.ID, storedPermissionAPI.PermissionID)
	require.Equal(t, &createAPI.ID, storedPermissionAPI.APIID)
	storedGrant, err := reader.Client().RolePermission.Get(ctx, roleGrant.ID)
	require.NoError(t, err)
	grantOn, allow := rolepermission.StatusOn, rolepermission.EffectAllow
	require.Equal(t, &owner.ID, storedGrant.TenantID)
	require.Equal(t, &currentRole.ID, storedGrant.RoleID)
	require.Equal(t, &createPermission.ID, storedGrant.PermissionID)
	require.Equal(t, &grantOn, storedGrant.Status)
	require.Equal(t, &allow, storedGrant.Effect)
	t.Log("PG authorization fixture independently verified before product behavior")

	// Retain the same tenant/user and old ALL viewer across calls. A revoked
	// database membership must not inherit authority from that stale context.
	requestCtx := viewer.WithContext(ctx, appViewer.NewUserViewer(
		uint64(operator.ID), uint64(owner.ID), 0, "",
		[]viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}},
	))
	repo := NewModelDevAuthorizationRepo(client)
	require.NoError(t, repo.AuthorizeCreate(requestCtx, owner.ID, operator.ID),
		"current valid MODEL/create grant and ALL scope must authorize")

	// Change one committed fact through the independent connection, retaining
	// the original caller context. Restore even if a denial assertion fails.
	for _, scenario := range []struct {
		name    string
		change  func(context.Context) error
		restore func(context.Context) error
	}{
		{
			name: "user disabled",
			change: func(writeCtx context.Context) error {
				return reader.Client().User.UpdateOneID(operator.ID).SetStatus(user.StatusDisabled).Exec(writeCtx)
			},
			restore: func(writeCtx context.Context) error {
				return reader.Client().User.UpdateOneID(operator.ID).SetStatus(user.StatusNormal).Exec(writeCtx)
			},
		},
		{
			name: "role off",
			change: func(writeCtx context.Context) error {
				return reader.Client().Role.UpdateOneID(currentRole.ID).SetStatus(role.StatusOff).Exec(writeCtx)
			},
			restore: func(writeCtx context.Context) error {
				return reader.Client().Role.UpdateOneID(currentRole.ID).SetStatus(role.StatusOn).Exec(writeCtx)
			},
		},
		{
			name: "membership disabled",
			change: func(writeCtx context.Context) error {
				return reader.Client().UserRole.UpdateOneID(membership.ID).SetStatus(userrole.StatusDisabled).Exec(writeCtx)
			},
			restore: func(writeCtx context.Context) error {
				return reader.Client().UserRole.UpdateOneID(membership.ID).SetStatus(userrole.StatusActive).Exec(writeCtx)
			},
		},
		{
			name: "membership not started",
			change: func(writeCtx context.Context) error {
				return reader.Client().UserRole.UpdateOneID(membership.ID).SetStartAt(now.Add(time.Minute)).Exec(writeCtx)
			},
			restore: func(writeCtx context.Context) error {
				return reader.Client().UserRole.UpdateOneID(membership.ID).SetStartAt(starts).Exec(writeCtx)
			},
		},
		{
			name: "membership expired",
			change: func(writeCtx context.Context) error {
				return reader.Client().UserRole.UpdateOneID(membership.ID).SetEndAt(now.Add(-time.Minute)).Exec(writeCtx)
			},
			restore: func(writeCtx context.Context) error {
				return reader.Client().UserRole.UpdateOneID(membership.ID).SetEndAt(ends).Exec(writeCtx)
			},
		},
		{
			name: "scope narrowed to self",
			change: func(writeCtx context.Context) error {
				return reader.Client().Role.UpdateOneID(currentRole.ID).SetDataScope(role.DataScopeSelf).Exec(writeCtx)
			},
			restore: func(writeCtx context.Context) error {
				return reader.Client().Role.UpdateOneID(currentRole.ID).SetDataScope(role.DataScopeAll).Exec(writeCtx)
			},
		},
		{
			name: "permission off",
			change: func(writeCtx context.Context) error {
				return reader.Client().Permission.UpdateOneID(createPermission.ID).SetStatus(permission.StatusOff).Exec(writeCtx)
			},
			restore: func(writeCtx context.Context) error {
				return reader.Client().Permission.UpdateOneID(createPermission.ID).SetStatus(permission.StatusOn).Exec(writeCtx)
			},
		},
		{
			name: "API off",
			change: func(writeCtx context.Context) error {
				return reader.Client().Api.UpdateOneID(createAPI.ID).SetStatus(api.StatusOff).Exec(writeCtx)
			},
			restore: func(writeCtx context.Context) error {
				return reader.Client().Api.UpdateOneID(createAPI.ID).SetStatus(api.StatusOn).Exec(writeCtx)
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
				defer stop()
				require.NoError(t, scenario.restore(cleanup), "restore the owned authorization fact after failures")
			})
			require.NoError(t, scenario.change(ctx), "commit the changed authorization fact")
			require.ErrorIs(t, repo.AuthorizeCreate(requestCtx, owner.ID, operator.ID), ErrModelDevAuthorizationDenied)
			require.NoError(t, scenario.restore(ctx))
			require.NoError(t, repo.AuthorizeCreate(requestCtx, owner.ID, operator.ID), "restoring the current grant permits the same caller")
		})
	}

	t.Run("different trusted tenant", func(t *testing.T) {
		other, createErr := reader.Client().Tenant.Create().
			SetName("modeldev other tenant").SetCode("cpu-auth-other-" + fixtureID).
			SetStatus(tenant.StatusOn).Save(ctx)
		require.NoError(t, createErr)
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(appViewer.NewSystemViewerContext(context.Background()), 5*time.Second)
			defer stop()
			require.NoError(t, reader.Client().Tenant.DeleteOneID(other.ID).Exec(cleanup))
		})
		otherCtx := viewer.WithContext(ctx, appViewer.NewUserViewer(
			uint64(operator.ID), uint64(other.ID), 0, "",
			[]viewer.DataScope{{ScopeType: viewer.ScopeTypeAll}},
		))
		require.ErrorIs(t, repo.AuthorizeCreate(otherCtx, other.ID, operator.ID), ErrModelDevAuthorizationDenied,
			"an active tenant cannot borrow a different tenant's user or grant")
	})
	t.Run("caller already canceled", func(t *testing.T) {
		canceledCtx, stop := context.WithCancel(requestCtx)
		stop()
		require.ErrorIs(t, repo.AuthorizeCreate(canceledCtx, owner.ID, operator.ID), context.Canceled)
		require.NoError(t, repo.AuthorizeCreate(requestCtx, owner.ID, operator.ID))
	})
	t.Run("authorization database closed", func(t *testing.T) {
		closed := newModelDevPGClient(t)
		require.NoError(t, closed.Close(), "close this separately opened real PG client")
		require.ErrorIs(t, NewModelDevAuthorizationRepo(closed).AuthorizeCreate(requestCtx, owner.ID, operator.ID),
			ErrModelDevAuthorizationUnavailable, "storage failure is not an authorization denial")
		require.NoError(t, repo.AuthorizeCreate(requestCtx, owner.ID, operator.ID), "the owned live client remains usable")
	})

	require.NoError(t, reader.Client().UserRole.DeleteOneID(membership.ID).Exec(ctx))
	present, err := client.Client().UserRole.Query().Where(userrole.IDEQ(membership.ID)).Exist(ctx)
	require.NoError(t, err)
	require.False(t, present, "revocation must be committed before checking the original caller again")
	require.ErrorIs(t, repo.AuthorizeCreate(requestCtx, owner.ID, operator.ID), ErrModelDevAuthorizationDenied,
		"the same caller must lose create authorization immediately after membership revocation")
}

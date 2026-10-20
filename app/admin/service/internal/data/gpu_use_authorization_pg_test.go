//go:build quota_pg

package data

import (
	"context"
	"testing"
	"time"

	entgo "entgo.io/ent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	appViewer "go-wind-admin/pkg/entgo/viewer"
)

func TestGpuUseAuthorizationPostgres(t *testing.T) {
	for _, scenario := range []string{
		"model-accelerator-without-instance-or-profile-read",
		"accelerator-revoked", "accelerator-soft-deleted", "missing-plan", "plan-deleted",
		"tenant-expired", "tenant-disabled", "tenant-deleted",
		"user-disabled", "user-deleted", "cross-tenant-user", "zero-tenant", "zero-user",
	} {
		t.Run(scenario, func(t *testing.T) {
			c := newLedgerPGClient(t)
			ctx := appViewer.NewSystemViewerContext(context.Background())
			name := "gpu-use-" + uuid.NewString()
			currentPlan := c.Client().Plan.Create().SetName(name).SaveX(ctx)
			current := c.Client().Tenant.Create().SetCode(name).SetName(name).SetStatus(tenant.StatusOn).SetPlanID(currentPlan.ID).SaveX(ctx)
			member := c.Client().User.Create().SetTenantID(current.ID).SetUsername(name).SetStatus(user.StatusNormal).SaveX(ctx)
			c.Client().PlanModule.Create().SetPlanID(currentPlan.ID).SetModule(planmodule.ModuleModel).SaveX(ctx)
			accelerator := c.Client().PlanModule.Create().SetPlanID(currentPlan.ID).SetModule(planmodule.ModuleAccelerator).SaveX(ctx)
			tenantID, userID := current.ID, member.ID
			past := time.Now().UTC().Add(-time.Hour)
			switch scenario {
			case "accelerator-revoked":
				require.NoError(t, c.Client().PlanModule.DeleteOne(accelerator).Exec(ctx))
			case "accelerator-soft-deleted":
				c.Client().PlanModule.UpdateOne(accelerator).SetDeletedAt(past).SaveX(ctx)
			case "missing-plan":
				c.Client().Tenant.UpdateOne(current).ClearPlanID().SaveX(ctx)
			case "plan-deleted":
				c.Client().Plan.UpdateOne(currentPlan).SetDeletedAt(past).SaveX(ctx)
			case "tenant-expired":
				c.Client().Tenant.UpdateOne(current).SetExpiredAt(past).SaveX(ctx)
			case "tenant-disabled":
				c.Client().Tenant.UpdateOne(current).SetStatus(tenant.StatusOff).SaveX(ctx)
			case "tenant-deleted":
				c.Client().Tenant.UpdateOne(current).SetDeletedAt(past).SaveX(ctx)
			case "user-disabled":
				c.Client().User.UpdateOne(member).SetStatus(user.StatusDisabled).SaveX(ctx)
			case "user-deleted":
				c.Client().User.UpdateOne(member).SetDeletedAt(past).SaveX(ctx)
			case "cross-tenant-user":
				other := c.Client().Tenant.Create().SetCode(name + "-other").SetStatus(tenant.StatusOn).SetPlanID(currentPlan.ID).SaveX(ctx)
				tenantID = other.ID
			case "zero-tenant":
				tenantID = 0
			case "zero-user":
				userID = 0
			}
			repo := NewGpuUseAuthorizationRepo(c)
			if scenario != "model-accelerator-without-instance-or-profile-read" {
				require.ErrorIs(t, repo.AuthorizeGpuUse(ctx, tenantID, userID), ErrActionAuthorizationDenied)
				return
			}
			require.NoError(t, repo.AuthorizeGpuUse(ctx, tenantID, userID))
			instanceAuthorization := NewInstanceAuthorizationRepo(c)
			require.NoError(t, instanceAuthorization.AuthorizeGpuUse(ctx, tenantID, userID))
			// Eligibility does not imply any business action. This user has no
			// role grants, and its plan intentionally has MODEL + ACCELERATOR only.
			require.False(t, c.Client().PlanModule.Query().Where(planmodule.HasPlanWith(plan.IDEQ(currentPlan.ID)), planmodule.ModuleEQ(planmodule.ModuleInstance)).ExistX(ctx))
			require.ErrorIs(t, instanceAuthorization.AuthorizeCatalog(ctx, tenantID, userID), ErrActionAuthorizationDenied)
			require.ErrorIs(t, NewActionAuthorizationRepo(c).authorizeRoute(ctx, tenantID, userID, "/api/v1/models", "MODEL", "POST"), ErrActionAuthorizationDenied)
			require.ErrorIs(t, instanceAuthorization.Authorize(ctx, tenantID, userID, InstanceCreatePath, "POST"), ErrActionAuthorizationDenied)
		})
	}
}

func TestGpuUseAuthorizationPostgresCancellationAndUnavailable(t *testing.T) {
	ctx := appViewer.NewSystemViewerContext(context.Background())
	var missing *GpuUseAuthorizationRepo
	require.ErrorIs(t, missing.AuthorizeGpuUse(ctx, 1, 1), ErrActionAuthorizationUnavailable)
	require.ErrorIs(t, NewGpuUseAuthorizationRepo(nil).AuthorizeGpuUse(ctx, 1, 1), ErrActionAuthorizationUnavailable)
	var missingInstance *InstanceAuthorizationRepo
	require.ErrorIs(t, missingInstance.AuthorizeGpuUse(ctx, 1, 1), ErrActionAuthorizationUnavailable)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, missing.AuthorizeGpuUse(canceled, 1, 1), context.Canceled)
	require.ErrorIs(t, missingInstance.AuthorizeGpuUse(canceled, 1, 1), context.Canceled)
	deadline, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer stop()
	require.ErrorIs(t, missing.AuthorizeGpuUse(deadline, 1, 1), context.DeadlineExceeded)

	t.Run("database-unavailable", func(t *testing.T) {
		c := newLedgerPGClient(t)
		require.NoError(t, c.Client().Close())
		require.ErrorIs(t, NewGpuUseAuthorizationRepo(c).AuthorizeGpuUse(ctx, 1, 1), ErrActionAuthorizationUnavailable)
	})
	t.Run("cancel-during-query", func(t *testing.T) {
		c := newLedgerPGClient(t)
		name := "gpu-use-cancel-" + uuid.NewString()
		currentPlan := c.Client().Plan.Create().SetName(name).SaveX(ctx)
		current := c.Client().Tenant.Create().SetCode(name).SetStatus(tenant.StatusOn).SetPlanID(currentPlan.ID).SaveX(ctx)
		member := c.Client().User.Create().SetTenantID(current.ID).SetUsername(name).SetStatus(user.StatusNormal).SaveX(ctx)
		c.Client().PlanModule.Create().SetPlanID(currentPlan.ID).SetModule(planmodule.ModuleAccelerator).SaveX(ctx)
		queryContext, cancelQuery := context.WithCancel(ctx)
		defer cancelQuery()
		// Cancellation occurs after the tenant read, at the real User query.
		// The repository must retain context cancellation through SQL rollback.
		c.Client().User.Intercept(entgo.InterceptFunc(func(next entgo.Querier) entgo.Querier {
			return entgo.QuerierFunc(func(call context.Context, query entgo.Query) (entgo.Value, error) {
				cancelQuery()
				return next.Query(call, query)
			})
		}))
		require.ErrorIs(t, NewGpuUseAuthorizationRepo(c).AuthorizeGpuUse(queryContext, current.ID, member.ID), context.Canceled)
	})
}

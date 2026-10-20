package data

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

// GpuUseAuthorizationRepo checks current GPU entitlement after the caller has
// authorized its business action. It grants no action or Profile catalog access.
type GpuUseAuthorizationRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
}

func NewGpuUseAuthorizationRepo(c *entCrud.EntClient[*ent.Client]) *GpuUseAuthorizationRepo {
	return &GpuUseAuthorizationRepo{entClient: c}
}

func (r *GpuUseAuthorizationRepo) AuthorizeGpuUse(ctx context.Context, tenantID, userID uint32) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == 0 || userID == 0 {
		return ErrActionAuthorizationDenied
	}
	if r == nil || r.entClient == nil || r.entClient.Client() == nil {
		return ErrActionAuthorizationUnavailable
	}
	tx, err := r.entClient.Client().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return ErrActionAuthorizationUnavailable
	}
	defer func() {
		if rollback := tx.Rollback(); rollback != nil && !errors.Is(rollback, sql.ErrTxDone) {
			result = ErrActionAuthorizationUnavailable
		}
		if contextErr := ctx.Err(); contextErr != nil {
			result = contextErr
		}
	}()
	current, err := tx.Tenant.Query().Where(tenant.IDEQ(tenantID), tenant.StatusEQ(tenant.StatusOn), tenant.DeletedAtIsNil(), tenant.Or(tenant.ExpiredAtIsNil(), tenant.ExpiredAtGT(time.Now().UTC()))).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrActionAuthorizationDenied
	}
	if err != nil {
		return ErrActionAuthorizationUnavailable
	}
	if current.PlanID == nil || *current.PlanID == 0 {
		return ErrActionAuthorizationDenied
	}
	active, err := tx.User.Query().Where(user.IDEQ(userID), user.TenantIDEQ(tenantID), user.StatusEQ(user.StatusNormal), user.DeletedAtIsNil()).Exist(ctx)
	if err != nil {
		return ErrActionAuthorizationUnavailable
	}
	if !active {
		return ErrActionAuthorizationDenied
	}
	eligible, err := tx.PlanModule.Query().Where(planmodule.HasPlanWith(plan.IDEQ(*current.PlanID), plan.DeletedAtIsNil()), planmodule.ModuleEQ(planmodule.ModuleAccelerator), planmodule.DeletedAtIsNil()).Exist(ctx)
	if err != nil {
		return ErrActionAuthorizationUnavailable
	}
	if !eligible {
		return ErrActionAuthorizationDenied
	}
	if err := tx.Commit(); err != nil {
		return ErrActionAuthorizationUnavailable
	}
	return nil
}

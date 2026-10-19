package data

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/permission"
	"go-wind-admin/app/admin/service/internal/data/ent/permissionapi"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/pkg/constants"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

var ErrActionAuthorizationDenied = errors.New("action authorization denied")
var ErrActionAuthorizationUnavailable = errors.New("action authorization unavailable")

type ActionAuthorizationRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
}

func NewActionAuthorizationRepo(c *entCrud.EntClient[*ent.Client]) *ActionAuthorizationRepo {
	return &ActionAuthorizationRepo{entClient: c}
}

// Check current tenant, membership, role, plan and the exact route grant in one
// repeatable-read snapshot before a business command can read its replay key.
func (r *ActionAuthorizationRepo) authorizeRoute(ctx context.Context, tenantID, userID uint32, path, module, method string) (result error) {
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
	now := time.Now().UTC()
	current, err := tx.Tenant.Query().Where(tenant.IDEQ(tenantID), tenant.StatusEQ(tenant.StatusOn), tenant.DeletedAtIsNil(), tenant.Or(tenant.ExpiredAtIsNil(), tenant.ExpiredAtGT(now))).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrActionAuthorizationDenied
	}
	if err != nil {
		return ErrActionAuthorizationUnavailable
	}
	if current.PlanID == nil || *current.PlanID == 0 {
		return ErrActionAuthorizationDenied
	}
	allowed, err := tx.PlanModule.Query().Where(planmodule.HasPlanWith(plan.IDEQ(*current.PlanID)), planmodule.ModuleEQ(planmodule.Module(module)), planmodule.DeletedAtIsNil()).Exist(ctx)
	if err != nil {
		return ErrActionAuthorizationUnavailable
	}
	if !allowed {
		return ErrActionAuthorizationDenied
	}
	allowed, err = tx.User.Query().Where(user.IDEQ(userID), user.TenantIDEQ(tenantID), user.StatusEQ(user.StatusNormal), user.DeletedAtIsNil()).Exist(ctx)
	if err != nil {
		return ErrActionAuthorizationUnavailable
	}
	if !allowed {
		return ErrActionAuthorizationDenied
	}
	var memberRoleIDs []uint32
	err = tx.UserRole.Query().Where(userrole.TenantIDEQ(tenantID), userrole.UserIDEQ(userID), userrole.StatusEQ(userrole.StatusActive), userrole.DeletedAtIsNil(), userrole.Or(userrole.StartAtIsNil(), userrole.StartAtLTE(now)), userrole.Or(userrole.EndAtIsNil(), userrole.EndAtGT(now))).Select(userrole.FieldRoleID).Scan(ctx, &memberRoleIDs)
	if err != nil {
		return ErrActionAuthorizationUnavailable
	}
	if len(memberRoleIDs) == 0 {
		return ErrActionAuthorizationDenied
	}
	roles, err := tx.Role.Query().Where(role.IDIn(memberRoleIDs...), role.TenantIDEQ(tenantID), role.StatusEQ(role.StatusOn), role.TypeEQ(role.TypeTenant), role.CodeNEQ(""), role.DeletedAtIsNil(), role.Not(role.CodeHasPrefix(constants.TemplateRoleCodePrefix))).All(ctx)
	if err != nil {
		return ErrActionAuthorizationUnavailable
	}
	var roleIDs []uint32
	hasAll := false
	for _, grant := range roles {
		roleIDs = append(roleIDs, grant.ID)
		if grant.DataScope != nil && *grant.DataScope == role.DataScopeAll {
			hasAll = true
		}
	}
	if !hasAll {
		return ErrActionAuthorizationDenied
	}
	var permissionIDs []uint32
	err = tx.RolePermission.Query().Where(rolepermission.TenantIDEQ(tenantID), rolepermission.RoleIDIn(roleIDs...), rolepermission.StatusEQ(rolepermission.StatusOn), rolepermission.EffectEQ(rolepermission.EffectAllow), rolepermission.DeletedAtIsNil()).Select(rolepermission.FieldPermissionID).Scan(ctx, &permissionIDs)
	if err != nil {
		return ErrActionAuthorizationUnavailable
	}
	if len(permissionIDs) == 0 {
		return ErrActionAuthorizationDenied
	}
	permissionIDs, err = tx.Permission.Query().Where(permission.IDIn(permissionIDs...), permission.StatusEQ(permission.StatusOn), permission.DeletedAtIsNil()).IDs(ctx)
	if err != nil {
		return ErrActionAuthorizationUnavailable
	}
	if len(permissionIDs) == 0 {
		return ErrActionAuthorizationDenied
	}
	var apiIDs []uint32
	err = tx.PermissionApi.Query().Where(permissionapi.PermissionIDIn(permissionIDs...), permissionapi.DeletedAtIsNil()).Select(permissionapi.FieldAPIID).Scan(ctx, &apiIDs)
	if err != nil {
		return ErrActionAuthorizationUnavailable
	}
	if len(apiIDs) == 0 {
		return ErrActionAuthorizationDenied
	}
	allowed, err = tx.Api.Query().Where(api.IDIn(apiIDs...), api.StatusEQ(api.StatusOn), api.DeletedAtIsNil(), api.ScopeEQ(api.ScopeAdmin), api.BusinessModuleEQ(api.BusinessModule(module)), api.PathEQ(path), api.MethodEQ(method)).Exist(ctx)
	if err != nil {
		return ErrActionAuthorizationUnavailable
	}
	if !allowed {
		return ErrActionAuthorizationDenied
	}
	if err = tx.Commit(); err != nil {
		return ErrActionAuthorizationUnavailable
	}
	return nil
}

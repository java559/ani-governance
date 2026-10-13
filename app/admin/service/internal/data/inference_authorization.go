package data

import (
	"context"
	"database/sql"
	"errors"
	"time"

	identityv1 "go-wind-admin/api/gen/go/identity/service/v1"
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

const (
	InferenceCreatePath = "/api/v1/inference/services"
	InferenceDeletePath = "/api/v1/inference/services/{data.resource_id}:delete"
)

var ErrInferenceAuthorizationDenied = errors.New("inference action authorization denied")
var ErrInferenceAuthorizationUnavailable = errors.New("inference authorization unavailable")

type InferenceAuthorizationRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
}

func NewInferenceAuthorizationRepo(c *entCrud.EntClient[*ent.Client]) *InferenceAuthorizationRepo {
	return &InferenceAuthorizationRepo{entClient: c}
}

// InferenceModuleRegistered is a fail-closed check against the authoritative
// generated registry. No provisional numeric module or Accelerator grant is
// allowed to enable this integration.
func InferenceModuleRegistered() bool {
	id, ok := identityv1.Module_value["INFERENCE"]
	return ok && id > 0
}

// Authorize requires the current tenant user's route grant AND the registered
// Inference plan module in one RR snapshot, before any idempotency read.
func (r *InferenceAuthorizationRepo) Authorize(ctx context.Context, tenantID, userID uint32, path string) (result error) {
	if !InferenceModuleRegistered() || (path != InferenceCreatePath && path != InferenceDeletePath) {
		return ErrInferenceAuthorizationDenied
	}
	return r.authorizeRoute(ctx, tenantID, userID, path, "INFERENCE", "POST")
}

// The two GPU owners share the current tenant/role/plan snapshot checks while
// keeping their module and exact API actions distinct.
func (r *InferenceAuthorizationRepo) authorizeRoute(ctx context.Context, tenantID, userID uint32, path, module, method string) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == 0 || userID == 0 {
		return ErrInferenceAuthorizationDenied
	}
	if r == nil || r.entClient == nil || r.entClient.Client() == nil {
		return ErrInferenceAuthorizationUnavailable
	}
	tx, err := r.entClient.Client().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ErrInferenceAuthorizationUnavailable
	}
	defer func() {
		if rollback := tx.Rollback(); rollback != nil && !errors.Is(rollback, sql.ErrTxDone) {
			result = ErrInferenceAuthorizationUnavailable
		}
		if contextErr := ctx.Err(); contextErr != nil {
			result = contextErr
		}
	}()
	now := time.Now().UTC()
	current, err := tx.Tenant.Query().Where(tenant.IDEQ(tenantID), tenant.StatusEQ(tenant.StatusOn), tenant.DeletedAtIsNil(), tenant.Or(tenant.ExpiredAtIsNil(), tenant.ExpiredAtGT(now))).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrInferenceAuthorizationDenied
	}
	if err != nil {
		return ErrInferenceAuthorizationUnavailable
	}
	if current.PlanID == nil || *current.PlanID == 0 {
		return ErrInferenceAuthorizationDenied
	}
	allowed, err := tx.PlanModule.Query().Where(planmodule.HasPlanWith(plan.IDEQ(*current.PlanID)), planmodule.ModuleEQ(planmodule.Module(module)), planmodule.DeletedAtIsNil()).Exist(ctx)
	if err != nil {
		return ErrInferenceAuthorizationUnavailable
	}
	if !allowed {
		return ErrInferenceAuthorizationDenied
	}
	allowed, err = tx.User.Query().Where(user.IDEQ(userID), user.TenantIDEQ(tenantID), user.StatusEQ(user.StatusNormal), user.DeletedAtIsNil()).Exist(ctx)
	if err != nil {
		return ErrInferenceAuthorizationUnavailable
	}
	if !allowed {
		return ErrInferenceAuthorizationDenied
	}
	var memberRoleIDs []uint32
	err = tx.UserRole.Query().Where(userrole.TenantIDEQ(tenantID), userrole.UserIDEQ(userID), userrole.StatusEQ(userrole.StatusActive), userrole.DeletedAtIsNil(), userrole.Or(userrole.StartAtIsNil(), userrole.StartAtLTE(now)), userrole.Or(userrole.EndAtIsNil(), userrole.EndAtGT(now))).Select(userrole.FieldRoleID).Scan(ctx, &memberRoleIDs)
	if err != nil {
		return ErrInferenceAuthorizationUnavailable
	}
	if len(memberRoleIDs) == 0 {
		return ErrInferenceAuthorizationDenied
	}
	roles, err := tx.Role.Query().Where(role.IDIn(memberRoleIDs...), role.TenantIDEQ(tenantID), role.StatusEQ(role.StatusOn), role.TypeEQ(role.TypeTenant), role.CodeNEQ(""), role.DeletedAtIsNil(), role.Not(role.CodeHasPrefix(constants.TemplateRoleCodePrefix))).All(ctx)
	if err != nil {
		return ErrInferenceAuthorizationUnavailable
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
		return ErrInferenceAuthorizationDenied
	}
	var permissionIDs []uint32
	err = tx.RolePermission.Query().Where(rolepermission.TenantIDEQ(tenantID), rolepermission.RoleIDIn(roleIDs...), rolepermission.StatusEQ(rolepermission.StatusOn), rolepermission.EffectEQ(rolepermission.EffectAllow), rolepermission.DeletedAtIsNil()).Select(rolepermission.FieldPermissionID).Scan(ctx, &permissionIDs)
	if err != nil {
		return ErrInferenceAuthorizationUnavailable
	}
	if len(permissionIDs) == 0 {
		return ErrInferenceAuthorizationDenied
	}
	permissionIDs, err = tx.Permission.Query().Where(permission.IDIn(permissionIDs...), permission.StatusEQ(permission.StatusOn), permission.DeletedAtIsNil()).IDs(ctx)
	if err != nil {
		return ErrInferenceAuthorizationUnavailable
	}
	if len(permissionIDs) == 0 {
		return ErrInferenceAuthorizationDenied
	}
	var apiIDs []uint32
	err = tx.PermissionApi.Query().Where(permissionapi.PermissionIDIn(permissionIDs...), permissionapi.DeletedAtIsNil()).Select(permissionapi.FieldAPIID).Scan(ctx, &apiIDs)
	if err != nil {
		return ErrInferenceAuthorizationUnavailable
	}
	if len(apiIDs) == 0 {
		return ErrInferenceAuthorizationDenied
	}
	allowed, err = tx.Api.Query().Where(api.IDIn(apiIDs...), api.StatusEQ(api.StatusOn), api.DeletedAtIsNil(), api.ScopeEQ(api.ScopeAdmin), api.BusinessModuleEQ(api.BusinessModule(module)), api.PathEQ(path), api.MethodEQ(method)).Exist(ctx)
	if err != nil {
		return ErrInferenceAuthorizationUnavailable
	}
	if !allowed {
		return ErrInferenceAuthorizationDenied
	}
	if err = tx.Commit(); err != nil {
		return ErrInferenceAuthorizationUnavailable
	}
	return nil
}

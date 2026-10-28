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
	"go-wind-admin/app/admin/service/internal/data/ent/role"
	"go-wind-admin/app/admin/service/internal/data/ent/rolepermission"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/internal/data/ent/user"
	"go-wind-admin/app/admin/service/internal/data/ent/userrole"
	"go-wind-admin/pkg/constants"
	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"
)

var (
	ErrModelDevAuthorizationDenied      = errors.New("modeldev create authorization denied")
	ErrModelDevAuthorizationUnavailable = errors.New("modeldev create authorization unavailable")
)

const ModelDevManageInputPermissionCode = "modeldev:manage_input"
const ModelDevManageExecutionPermissionCode = "modeldev:manage_execution"

type modelDevAuthorizationAction uint8

const (
	modelDevCreateAction modelDevAuthorizationAction = iota + 1
	modelDevManageInputAction
	modelDevGetExecutionAction
	modelDevListArtifactsAction
	modelDevDownloadArtifactAction
	modelDevStopAction
	modelDevListExecutionsAction
	modelDevGetExecutionLogsAction
	modelDevListExecutionPodsAction
	modelDevListExecutionEventsAction
	modelDevExecutionLogStreamAction
	modelDevExecutionMetricsAction
	modelDevListImplementationsAction
	modelDevGetInputVersionAction
	modelDevListInputVersionsAction
	modelDevManageExecutionAction
	modelDevFinetuningExistingModelAction
	modelDevFinetuningNewModelAction
	modelDevResumeRegistrationAction
	modelDevFinetuningArtifactsAction
)

const (
	ModelDevGetExecutionPath        = "/api/v1/modeldev/executions/{execution_id}"
	ModelDevListArtifactsPath       = "/api/v1/modeldev/executions/{execution_id}/artifacts"
	ModelDevDownloadArtifactPath    = "/api/v1/modeldev/artifacts/{artifact_id}/content"
	ModelDevListExecutionsPath      = "/api/v1/modeldev/executions"
	ModelDevGetExecutionLogsPath    = "/api/v1/modeldev/executions/{execution_id}/logs"
	ModelDevListExecutionPodsPath   = "/api/v1/modeldev/executions/{execution_id}/pods"
	ModelDevListExecutionEventsPath = "/api/v1/modeldev/executions/{execution_id}/events"
	ModelDevExecutionLogStreamPath  = "/api/v1/modeldev/executions/{execution_id}/logs/stream"
	ModelDevExecutionMetricsPath    = "/api/v1/modeldev/executions/{execution_id}/resource-metrics"
	ModelDevListImplementationsPath = "/api/v1/modeldev/implementations"
	ModelDevGetInputVersionPath     = "/api/v1/modeldev/input-versions/{input_version_id}"
	ModelDevListInputVersionsPath   = "/api/v1/modeldev/input-versions"
)

// AuthorizeQuery checks the current independent GET grant and ALL data scope.
// Only these fixed route templates can produce downstream query delegation.
func (r *ModelDevAuthorizationRepo) AuthorizeQuery(ctx context.Context, tenantID, userID uint32, path string) error {
	var action modelDevAuthorizationAction
	switch path {
	case ModelDevGetExecutionPath:
		action = modelDevGetExecutionAction
	case ModelDevListArtifactsPath:
		action = modelDevListArtifactsAction
	case ModelDevDownloadArtifactPath:
		action = modelDevDownloadArtifactAction
	case ModelDevListExecutionsPath:
		action = modelDevListExecutionsAction
	case ModelDevGetExecutionLogsPath:
		action = modelDevGetExecutionLogsAction
	case ModelDevListExecutionPodsPath:
		action = modelDevListExecutionPodsAction
	case ModelDevListExecutionEventsPath:
		action = modelDevListExecutionEventsAction
	case ModelDevExecutionLogStreamPath:
		action = modelDevExecutionLogStreamAction
	case ModelDevExecutionMetricsPath:
		action = modelDevExecutionMetricsAction
	case ModelDevListImplementationsPath:
		action = modelDevListImplementationsAction
	case ModelDevGetInputVersionPath:
		action = modelDevGetInputVersionAction
	case ModelDevListInputVersionsPath:
		action = modelDevListInputVersionsAction
	default:
		return ErrModelDevAuthorizationDenied
	}
	return r.authorizeCurrent(ctx, tenantID, userID, action)
}

// ModelDevAuthorizationRepo checks current database grants for the fixed
// CPU-P01 create and input management actions. It does not
// authenticate a user or enable assets. Create's HTTP authentication,
// TenantAccess and Casbin checks remain required before AuthorizeCreate.
type ModelDevAuthorizationRepo struct {
	entClient *entCrud.EntClient[*ent.Client]
}

func NewModelDevAuthorizationRepo(client *entCrud.EntClient[*ent.Client]) *ModelDevAuthorizationRepo {
	return &ModelDevAuthorizationRepo{entClient: client}
}

// AuthorizeCreate requires trusted tenant/user IDs from the verified Principal.
// It must run before FindAccepted for both new requests and original-key replay.
func (r *ModelDevAuthorizationRepo) AuthorizeCreate(ctx context.Context, tenantID, userID uint32) error {
	return r.authorizeCurrent(ctx, tenantID, userID, modelDevCreateAction)
}

const ModelDevStopExecutionPath = "/api/v1/modeldev/executions/{execution_id}:stop"

func (r *ModelDevAuthorizationRepo) AuthorizeStop(ctx context.Context, tenantID, userID uint32) error {
	return r.authorizeCurrent(ctx, tenantID, userID, modelDevStopAction)
}

// AuthorizeManageInput requires trusted tenant/user IDs from the
// verified Principal and the current, separate management permission. A create
// API grant or a JWT's cached roles cannot authorize this action.
func (r *ModelDevAuthorizationRepo) AuthorizeManageInput(ctx context.Context, tenantID, userID uint32) error {
	return r.authorizeCurrent(ctx, tenantID, userID, modelDevManageInputAction)
}

func (r *ModelDevAuthorizationRepo) AuthorizeManageExecution(ctx context.Context, tenantID, userID uint32) error {
	return r.authorizeCurrent(ctx, tenantID, userID, modelDevManageExecutionAction)
}

func (r *ModelDevAuthorizationRepo) AuthorizeResume(ctx context.Context, tenantID, userID uint32) error {
	if err := r.authorizeCurrent(ctx, tenantID, userID, modelDevResumeRegistrationAction); err != nil {
		return err
	}
	return r.AuthorizeQuery(ctx, tenantID, userID, ModelDevGetExecutionPath)
}

// AuthorizeFinetuning complements the current ModelDev create grant with the
// existing Model API grants. A trusted ModelDev service identity cannot grant
// its submitting user permission to read model bytes or register a version.
func (r *ModelDevAuthorizationRepo) AuthorizeFinetuning(ctx context.Context, tenantID, userID uint32, newModel bool) error {
	action := modelDevFinetuningExistingModelAction
	if newModel {
		action = modelDevFinetuningNewModelAction
	}
	return r.authorizeCurrent(ctx, tenantID, userID, action)
}

// AuthorizeFinetuningArtifacts requires current access to the real base model
// bytes without granting any target-model write authority.
func (r *ModelDevAuthorizationRepo) AuthorizeFinetuningArtifacts(ctx context.Context, tenantID, userID uint32) error {
	return r.authorizeCurrent(ctx, tenantID, userID, modelDevFinetuningArtifactsAction)
}

func (r *ModelDevAuthorizationRepo) authorizeCurrent(ctx context.Context, tenantID, userID uint32, action modelDevAuthorizationAction) (result error) {
	// Cancellation remains distinguishable from a denied grant or failed store,
	// including cancellation during transaction completion or cleanup.
	defer func() {
		if err := ctx.Err(); err != nil {
			result = err
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if tenantID == 0 || userID == 0 {
		return ErrModelDevAuthorizationDenied
	}
	if r == nil || r.entClient == nil || r.entClient.Client() == nil {
		return ErrModelDevAuthorizationUnavailable
	}
	tx, err := r.entClient.Client().BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return ErrModelDevAuthorizationUnavailable
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			result = ErrModelDevAuthorizationUnavailable
		}
	}()

	active, err := tx.Tenant.Query().Where(
		tenant.IDEQ(tenantID), tenant.StatusEQ(tenant.StatusOn), tenant.DeletedAtIsNil(),
	).Exist(ctx)
	if err != nil {
		return ErrModelDevAuthorizationUnavailable
	}
	if !active {
		return ErrModelDevAuthorizationDenied
	}
	active, err = tx.User.Query().Where(
		user.IDEQ(userID), user.TenantIDEQ(tenantID), user.StatusEQ(user.StatusNormal), user.DeletedAtIsNil(),
	).Exist(ctx)
	if err != nil {
		return ErrModelDevAuthorizationUnavailable
	}
	if !active {
		return ErrModelDevAuthorizationDenied
	}

	// The current OneToOne tenant model uses UserRole, not Membership. All
	// membership times and subsequent grants are evaluated in this RR snapshot.
	now := time.Now().UTC()
	var memberRoleIDs []uint32
	err = tx.UserRole.Query().Where(
		userrole.TenantIDEQ(tenantID), userrole.UserIDEQ(userID), userrole.RoleIDGT(0),
		userrole.StatusEQ(userrole.StatusActive), userrole.DeletedAtIsNil(),
		userrole.Or(userrole.StartAtIsNil(), userrole.StartAtLTE(now)),
		userrole.Or(userrole.EndAtIsNil(), userrole.EndAtGT(now)),
	).Select(userrole.FieldRoleID).Scan(ctx, &memberRoleIDs)
	if err != nil {
		return ErrModelDevAuthorizationUnavailable
	}
	if len(memberRoleIDs) == 0 {
		return ErrModelDevAuthorizationDenied
	}
	roles, err := tx.Role.Query().Where(
		role.IDIn(memberRoleIDs...), role.TenantIDEQ(tenantID), role.StatusEQ(role.StatusOn),
		role.TypeEQ(role.TypeTenant), role.CodeNEQ(""), role.DeletedAtIsNil(),
		role.Not(role.CodeHasPrefix(constants.TemplateRoleCodePrefix)),
	).All(ctx)
	if err != nil {
		return ErrModelDevAuthorizationUnavailable
	}
	roleIDs := make([]uint32, 0, len(roles))
	hasAll := false
	for _, current := range roles {
		roleIDs = append(roleIDs, current.ID)
		if current.DataScope != nil && *current.DataScope == role.DataScopeAll {
			hasAll = true
		}
	}
	// Existing data scopes form a union: one effective ALL grants the tenant
	// range. A separate current action grant is still mandatory below.
	if !hasAll {
		return ErrModelDevAuthorizationDenied
	}

	var permissionIDs []uint32
	err = tx.RolePermission.Query().Where(
		rolepermission.TenantIDEQ(tenantID), rolepermission.RoleIDIn(roleIDs...),
		rolepermission.PermissionIDGT(0), rolepermission.StatusEQ(rolepermission.StatusOn),
		rolepermission.EffectEQ(rolepermission.EffectAllow), rolepermission.DeletedAtIsNil(),
	).Select(rolepermission.FieldPermissionID).Scan(ctx, &permissionIDs)
	if err != nil {
		return ErrModelDevAuthorizationUnavailable
	}
	if len(permissionIDs) == 0 {
		return ErrModelDevAuthorizationDenied
	}
	permissionQuery := tx.Permission.Query().Where(
		permission.IDIn(permissionIDs...), permission.StatusEQ(permission.StatusOn), permission.DeletedAtIsNil(),
	)
	switch action {
	case modelDevCreateAction, modelDevGetExecutionAction, modelDevListArtifactsAction, modelDevDownloadArtifactAction, modelDevStopAction, modelDevListExecutionsAction, modelDevGetExecutionLogsAction, modelDevListExecutionPodsAction, modelDevListExecutionEventsAction, modelDevExecutionLogStreamAction, modelDevExecutionMetricsAction, modelDevListImplementationsAction, modelDevGetInputVersionAction, modelDevListInputVersionsAction, modelDevFinetuningExistingModelAction, modelDevFinetuningNewModelAction, modelDevFinetuningArtifactsAction:
		// Create retains its permission-to-API check below.
	case modelDevManageInputAction:
		permissionQuery.Where(permission.CodeEQ(ModelDevManageInputPermissionCode))
	case modelDevManageExecutionAction, modelDevResumeRegistrationAction:
		permissionQuery.Where(permission.CodeEQ(ModelDevManageExecutionPermissionCode))
	default:
		return ErrModelDevAuthorizationDenied
	}
	permissionIDs, err = permissionQuery.IDs(ctx)
	if err != nil {
		return ErrModelDevAuthorizationUnavailable
	}
	if len(permissionIDs) == 0 {
		return ErrModelDevAuthorizationDenied
	}
	if action != modelDevManageInputAction && action != modelDevManageExecutionAction {
		path, method := "/api/v1/modeldev/executions", "POST"
		switch action {
		case modelDevResumeRegistrationAction:
			path = ModelDevResumeModelRegistrationPath
		case modelDevStopAction:
			path = ModelDevStopExecutionPath
		case modelDevGetExecutionAction:
			path, method = ModelDevGetExecutionPath, "GET"
		case modelDevListArtifactsAction:
			path, method = ModelDevListArtifactsPath, "GET"
		case modelDevDownloadArtifactAction:
			path, method = ModelDevDownloadArtifactPath, "GET"
		case modelDevListExecutionsAction:
			path, method = ModelDevListExecutionsPath, "GET"
		case modelDevGetExecutionLogsAction:
			path, method = ModelDevGetExecutionLogsPath, "GET"
		case modelDevListExecutionPodsAction:
			path, method = ModelDevListExecutionPodsPath, "GET"
		case modelDevListExecutionEventsAction:
			path, method = ModelDevListExecutionEventsPath, "GET"
		case modelDevExecutionLogStreamAction:
			path, method = ModelDevExecutionLogStreamPath, "GET"
		case modelDevExecutionMetricsAction:
			path, method = ModelDevExecutionMetricsPath, "GET"
		case modelDevListImplementationsAction:
			path, method = ModelDevListImplementationsPath, "GET"
		case modelDevGetInputVersionAction:
			path, method = ModelDevGetInputVersionPath, "GET"
		case modelDevListInputVersionsAction:
			path, method = ModelDevListInputVersionsPath, "GET"
		case modelDevFinetuningArtifactsAction:
			if err := authorizeModelDevAPI(ctx, tx, permissionIDs, "/api/v1/model-versions/{model_version_id}", "GET"); err != nil {
				return err
			}
			path, method = "/api/v1/model-versions/{model_version_id}/download-url", "GET"
		case modelDevFinetuningExistingModelAction, modelDevFinetuningNewModelAction:
			for _, required := range []struct{ path, method string }{
				{"/api/v1/model-versions/{model_version_id}", "GET"},
				{"/api/v1/model-versions/{model_version_id}/download-url", "GET"},
				{"/api/v1/model-versions", "POST"},
			} {
				if err := authorizeModelDevAPI(ctx, tx, permissionIDs, required.path, required.method); err != nil {
					return err
				}
			}
			if action == modelDevFinetuningNewModelAction {
				path, method = "/api/v1/models", "POST"
			} else {
				path, method = "/api/v1/models/{model_id}", "GET"
			}
		}
		if err := authorizeModelDevAPI(ctx, tx, permissionIDs, path, method); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return ErrModelDevAuthorizationUnavailable
	}
	return nil
}

func authorizeModelDevAPI(ctx context.Context, tx *ent.Tx, permissionIDs []uint32, path, method string) error {
	var apiIDs []uint32
	err := tx.PermissionApi.Query().Where(
		permissionapi.PermissionIDIn(permissionIDs...), permissionapi.APIIDGT(0), permissionapi.DeletedAtIsNil(),
	).Select(permissionapi.FieldAPIID).Scan(ctx, &apiIDs)
	if err != nil {
		return ErrModelDevAuthorizationUnavailable
	}
	if len(apiIDs) == 0 {
		return ErrModelDevAuthorizationDenied
	}
	allowed, err := tx.Api.Query().Where(
		api.IDIn(apiIDs...), api.StatusEQ(api.StatusOn), api.DeletedAtIsNil(),
		api.ScopeEQ(api.ScopeAdmin), api.BusinessModuleEQ(api.BusinessModuleModel),
		api.PathEQ(path), api.MethodEQ(method),
	).Exist(ctx)
	if err != nil {
		return ErrModelDevAuthorizationUnavailable
	}
	if !allowed {
		return ErrModelDevAuthorizationDenied
	}
	return nil
}

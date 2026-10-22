package data

import (
	"context"
	"time"

	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	entCrud "go-wind-admin/pkg/localdeps/go-crud/entgo"

	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"

	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/middleware/auth"
	"go-wind-admin/pkg/permcode"
)

// TenantAccessCheckerImpl 是 TenantAccessChecker 接口的 data 层实现。
// 仅对租户用户（tenantId>0）调用（中间件已保证），平台管理员（tid=0）不会进入此检查。
type TenantAccessCheckerImpl struct {
	entClient *ent.Client
	log       *bLogger.Helper
}

func NewTenantAccessCheckerImpl(
	ctx *bootstrap.Context,
	entClient *entCrud.EntClient[*ent.Client],
) auth.TenantAccessChecker {
	return &TenantAccessCheckerImpl{
		entClient: entClient.Client(),
		log:       ctx.NewLoggerHelper("tenant-access-checker/admin-service"),
	}
}

// CheckTenantAccess 实现 auth.TenantAccessChecker。
// tenantId 由中间件保证 > 0。
func (c *TenantAccessCheckerImpl) CheckTenantAccess(ctx context.Context, tenantId uint32, path string, method string) error {
	return c.checkTenantAccess(ctx, tenantId, path, method, "")
}

func (c *TenantAccessCheckerImpl) CheckTenantRequestAccess(ctx context.Context, tenantId uint32, path, method string, request any) error {
	module := ""
	if path == ComputeCatalogPath && method == "GET" {
		r, ok := request.(interface{ GetUsage() string })
		if !ok {
			return adminV1.ErrorForbidden("access denied")
		}
		var valid bool
		module, _, valid = permcode.ComputeCatalogGrant(r.GetUsage())
		if !valid {
			return adminV1.ErrorForbidden("access denied")
		}
	}
	return c.checkTenantAccess(ctx, tenantId, path, method, module)
}

func (c *TenantAccessCheckerImpl) checkTenantAccess(ctx context.Context, tenantId uint32, path, method, usageModule string) error {
	sysCtx := appViewer.NewSystemViewerContext(ctx)

	// 1. 查租户状态与到期时间（WithPlan 预载套餐以读取 expiry_policy）
	t, err := c.entClient.Tenant.Query().
		Where(tenant.IDEQ(tenantId)).
		WithPlan().
		Only(sysCtx)
	if err != nil || t == nil {
		c.log.Errorf(ctx, "tenant access check: tenant %d not found: %v", tenantId, err)
		return adminV1.ErrorForbidden("access denied")
	}

	if t.Status != nil && *t.Status != tenant.StatusOn {
		return adminV1.ErrorForbidden("tenant is not active")
	}

	// 2. 到期只读判定（READONLY 策略 + 已到期）
	planId := uint32(0)
	var planExpiryPolicy plan.ExpiryPolicy
	if t.Edges.Plan != nil {
		planId = t.Edges.Plan.ID
		if t.Edges.Plan.ExpiryPolicy != nil {
			planExpiryPolicy = *t.Edges.Plan.ExpiryPolicy
		}
	}
	planExpired := t.ExpiredAt != nil && !t.ExpiredAt.IsZero() && t.ExpiredAt.Before(time.Now())

	if planExpired && planExpiryPolicy == plan.ExpiryPolicyReadonly {
		switch method {
		case "GET", "HEAD", "OPTIONS":
			// 只读放行，但仍需过白名单（见第 3 步）
		default:
			return adminV1.ErrorForbidden("tenant is read-only due to expiry")
		}
	}

	// 3. 套餐模块白名单
	bizModule := identityV1.Module_MODULE_UNSPECIFIED
	a, aerr := c.findAPIByTemplate(sysCtx, method, path)
	if aerr != nil {
		c.log.Errorf(ctx, "tenant access check: api lookup failed for %s %s: %v", method, path, aerr)
		return adminV1.ErrorForbidden("access denied")
	}
	if a == nil {
		// 找不到对应 API 记录，无法判定模块。fail-closed 拒绝。
		c.log.Errorf(ctx, "tenant access check: api not found for %s %s", method, path)
		return adminV1.ErrorForbidden("access denied")
	}
	if a.BusinessModule != nil {
		bizModule = mapApiBusinessModuleToProto(*a.BusinessModule)
	}

	if path == ComputeCatalogPath {
		if method != "GET" || usageModule == "" || a == nil || a.Status == nil || *a.Status != api.StatusOn || a.DeletedAt != nil || planId == 0 {
			return adminV1.ErrorForbidden("access denied")
		}
		allowed, err := c.entClient.PlanModule.Query().Where(planmodule.HasPlanWith(plan.IDEQ(planId)), planmodule.ModuleEQ(planmodule.Module(usageModule)), planmodule.DeletedAtIsNil()).Exist(sysCtx)
		if err != nil || !allowed {
			return adminV1.ErrorForbidden("module not allowed")
		}
		return nil
	}
	if bizModule == identityV1.Module_MODULE_UNSPECIFIED {
		// 未归类模块：不在任何白名单内，拒绝。
		return adminV1.ErrorForbidden("module not allowed")
	}

	// 自助类路由（scope_kind = self / tenant-self）豁免套餐模块白名单。
	// 这类路由是"读/写操作者自己（或个人所属租户）的资源"，不属于可售业务能力，
	// 不应因套餐未勾选对应模块而拒绝。
	// 位置：状态（OFF/EXPIRED/FREEZE）与到期只读判定已在第 1、2 步完成，此处只豁免
	// "买没买这个模块"；授权仍由下游 casbin 强制（调用方须持有对应 self 权限码）。
	if isSelfScopedRoute(method, path) {
		return nil
	}

	if planId == 0 {
		// 租户未关联套餐，无白名单，拒绝所有业务模块。
		return adminV1.ErrorForbidden("no subscription plan")
	}

	allowed, lerr := c.isModuleAllowed(sysCtx, planId, bizModule)
	if lerr != nil {
		c.log.Errorf(ctx, "tenant access check: whitelist query failed: %v", lerr)
		return adminV1.ErrorForbidden("access denied")
	}
	if !allowed {
		return adminV1.ErrorForbidden("module not allowed")
	}

	return nil
}

// findAPIByTemplate 按 (method, path) 查 API 目录，路径参数名不一致时按归一化回退匹配。
//
// 目录路径由 OpenAPI 生成（驼峰参数名，如 {userId}），请求 path 是运行时路由模板
// （proto 蛇形，如 {user_id}）。精确查询失败后，在同方法候选里做参数段归一化比较；
// 归一化命中多条时无法判定业务模块，按未命中处理（fail-closed）。
func (c *TenantAccessCheckerImpl) findAPIByTemplate(ctx context.Context, method, path string) (*ent.Api, error) {
	a, err := c.entClient.Api.Query().
		Where(api.PathEQ(path), api.MethodEQ(method)).
		Only(ctx)
	if err == nil && a != nil {
		return a, nil
	}
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}

	target := permcode.NormalizePath(path)
	candidates, lerr := c.entClient.Api.Query().
		Where(api.MethodEQ(method)).
		All(ctx)
	if lerr != nil {
		return nil, lerr
	}

	var match *ent.Api
	for _, candidate := range candidates {
		if candidate == nil || candidate.Path == nil || permcode.NormalizePath(*candidate.Path) != target {
			continue
		}
		if match != nil {
			return nil, nil
		}
		match = candidate
	}
	return match, nil
}

// isSelfScopedRoute 报告 (method, path) 是否为自助类路由（scope_kind = self / tenant-self）。
// 判据取自权限码权威表（pkg/permcode），该表由生成期固化、覆盖全部已登记路由；
// 未命中权威表时返回 false（fail-closed，保持原有模块白名单判定）。
// 权威表不含 scope_kind 的数据库列，故只能在代码侧查表，不能读 sys_apis。
func isSelfScopedRoute(method, path string) bool {
	exp, err := permcode.Resolve(method, path)
	if err != nil || exp == nil {
		return false
	}
	return exp.ScopeKind == permcode.ScopeKindSelf || exp.ScopeKind == permcode.ScopeKindTenantSelf
}

// isModuleAllowed 查询套餐白名单是否包含指定模块。
func (c *TenantAccessCheckerImpl) isModuleAllowed(ctx context.Context, planId uint32, module identityV1.Module) (bool, error) {
	entModule := mapProtoModuleToEnt(module)
	if entModule == "" {
		return false, nil
	}
	cnt, err := c.entClient.PlanModule.Query().
		Where(
			planmodule.HasPlanWith(plan.IDEQ(planId)),
			planmodule.ModuleEQ(entModule),
		).
		Count(ctx)
	if err != nil {
		return false, err
	}
	return cnt > 0, nil
}

// mapProtoModuleToEnt 将 proto Module 值映射到 ent planmodule.Module 字符串。
// 返回空串表示 UNSPECIFIED。
func mapProtoModuleToEnt(m identityV1.Module) planmodule.Module {
	switch m {
	case identityV1.Module_DASHBOARD:
		return planmodule.ModuleDashboard
	case identityV1.Module_OPM:
		return planmodule.ModuleOpm
	case identityV1.Module_SYSTEM:
		return planmodule.ModuleSystem
	case identityV1.Module_DICT:
		return planmodule.ModuleDict
	case identityV1.Module_TENANT:
		return planmodule.ModuleTenant
	case identityV1.Module_PERMISSION:
		return planmodule.ModulePermission
	case identityV1.Module_LOG:
		return planmodule.ModuleLog
	case identityV1.Module_INTERNAL_MESSAGE:
		return planmodule.ModuleInternalMessage
	case identityV1.Module_TASK:
		return planmodule.ModuleTask
	case identityV1.Module_NETWORK:
		return planmodule.ModuleNetwork
	case identityV1.Module_ACCELERATOR:
		return planmodule.ModuleAccelerator
	case identityV1.Module_IMAGE:
		return planmodule.ModuleImage
	case identityV1.Module_INFERENCE:
		return planmodule.ModuleInference
	case identityV1.Module_INSTANCE:
		return planmodule.ModuleInstance
	case identityV1.Module_MODEL:
		return planmodule.ModuleModel
	default:
		return ""
	}
}

// mapApiBusinessModuleToProto 将 ent api.BusinessModule 字符串枚举映射到 proto identityV1.Module。
// 返回 MODULE_UNSPECIFIED 表示未归类。
func mapApiBusinessModuleToProto(m api.BusinessModule) identityV1.Module {
	switch m {
	case api.BusinessModuleDashboard:
		return identityV1.Module_DASHBOARD
	case api.BusinessModuleOpm:
		return identityV1.Module_OPM
	case api.BusinessModuleSystem:
		return identityV1.Module_SYSTEM
	case api.BusinessModuleDict:
		return identityV1.Module_DICT
	case api.BusinessModuleTenant:
		return identityV1.Module_TENANT
	case api.BusinessModulePermission:
		return identityV1.Module_PERMISSION
	case api.BusinessModuleLog:
		return identityV1.Module_LOG
	case api.BusinessModuleInternalMessage:
		return identityV1.Module_INTERNAL_MESSAGE
	case api.BusinessModuleTask:
		return identityV1.Module_TASK
	case api.BusinessModuleNetwork:
		return identityV1.Module_NETWORK
	case api.BusinessModuleAccelerator:
		return identityV1.Module_ACCELERATOR
	case api.BusinessModuleImage:
		return identityV1.Module_IMAGE
	case api.BusinessModuleInference:
		return identityV1.Module_INFERENCE
	case api.BusinessModuleInstance:
		return identityV1.Module_INSTANCE
	case api.BusinessModuleModel:
		return identityV1.Module_MODEL
	default:
		return identityV1.Module_MODULE_UNSPECIFIED
	}
}

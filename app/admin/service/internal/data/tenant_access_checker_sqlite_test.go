// 本文件对 TenantAccessCheckerImpl 做决策矩阵测试（SQLite 内存库）：
//
//	租户不存在 → fail-closed 拒绝；
//	租户状态 OFF/EXPIRED/FREEZE → 拒绝；
//	API 表无 (path,method) 记录 → fail-closed 拒绝；
//	API 有记录但未归类业务模块（UNSPECIFIED）→ 拒绝；
//	租户未挂套餐 → 拒绝；套餐白名单不含该模块 → 拒绝；白名单命中 → 放行；
//	已到期 + READONLY 策略：GET/HEAD/OPTIONS 放行、写方法拒绝；
//	未到期或非 READONLY 策略：不做只读门控（按白名单判定）。
//
// 另附 mapProtoModuleToEnt / mapApiBusinessModuleToProto 纯映射函数的全量枚举测试。
// 该闸门是租户隔离的 HTTP 层强制点，语义见 docs/tenant_isolation.md。
package data

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/go-kratos/kratos/v2/errors"

	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"

	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
	"go-wind-admin/app/admin/service/internal/data/ent"
	"go-wind-admin/app/admin/service/internal/data/ent/api"
	"go-wind-admin/app/admin/service/internal/data/ent/plan"
	"go-wind-admin/app/admin/service/internal/data/ent/planmodule"
	"go-wind-admin/app/admin/service/internal/data/ent/tenant"
	"go-wind-admin/app/admin/service/tests/testutil"
)

// newCheckerSqlite 白盒构造被测闸门（字段与生产构造器一致，仅 log 换 Nop）。
func newCheckerSqlite(t *testing.T) (*TenantAccessCheckerImpl, *ent.Client, context.Context) {
	t.Helper()
	client := testutil.NewEntClientForTest(t).Client()
	sysCtx := testutil.NewSystemViewerCtx(context.Background())
	return &TenantAccessCheckerImpl{
		entClient: client,
		log:       bLogger.NewHelper(bLogger.NopLogger()),
	}, client, sysCtx
}

// forbiddenMessage 取 kratos 错误的 message 文案（本闸门全部以 *errors.Error 返回）。
func forbiddenMessage(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	e := errors.FromError(err)
	require.NotNil(t, e)
	return e.GetMessage()
}

// TestTenantAccessCheckerPathTemplateNormalization 目录路径来自 OpenAPI（驼峰参数名），
// 请求 path 是运行时路由模板（proto 蛇形）；参数名不同时按归一化命中，
// 归一化命中多条时无法判定业务模块，保持 fail-closed。
func TestTenantAccessCheckerPathTemplateNormalization(t *testing.T) {
	t.Run("驼峰目录_蛇形请求_放行", func(t *testing.T) {
		checker, client, sysCtx := newCheckerSqlite(t)
		p := client.Plan.Create().SetExpiryPolicy(plan.ExpiryPolicyReadonly).SaveX(sysCtx)
		tid := client.Tenant.Create().SetName("t-camel").SetStatus(tenant.StatusOn).SetPlanID(p.ID).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/api/v1/admin/users/{userId}/password").SetMethod("POST").SetBusinessModule(api.BusinessModuleOpm).SaveX(sysCtx)
		_ = client.PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleOpm).SaveX(sysCtx)
		require.NoError(t, checker.CheckTenantAccess(context.Background(), tid, "/api/v1/admin/users/{user_id}/password", "POST"))
	})

	t.Run("归一化命中多条_拒绝", func(t *testing.T) {
		checker, client, sysCtx := newCheckerSqlite(t)
		p := client.Plan.Create().SetExpiryPolicy(plan.ExpiryPolicyReadonly).SaveX(sysCtx)
		tid := client.Tenant.Create().SetName("t-ambiguous").SetStatus(tenant.StatusOn).SetPlanID(p.ID).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/t/{a}/x").SetMethod("POST").SetBusinessModule(api.BusinessModuleOpm).SaveX(sysCtx)
		_ = client.Api.Create().SetPath("/t/{b}/x").SetMethod("POST").SetBusinessModule(api.BusinessModuleOpm).SaveX(sysCtx)
		_ = client.PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleOpm).SaveX(sysCtx)
		err := checker.CheckTenantAccess(context.Background(), tid, "/t/{c}/x", "POST")
		require.Contains(t, forbiddenMessage(t, err), "access denied")
	})
}

// TestTenantAccessCheckerDecisionMatrix 决策矩阵逐分支断言。
func TestTenantAccessCheckerDecisionMatrix(t *testing.T) {
	pastTime := time.Now().Add(-24 * time.Hour)

	t.Run("租户不存在_failclosed", func(t *testing.T) {
		checker, _, _ := newCheckerSqlite(t)
		err := checker.CheckTenantAccess(context.Background(), 999, "/x", "GET")
		require.Contains(t, forbiddenMessage(t, err), "access denied")
	})

	t.Run("租户状态OFF_拒绝", func(t *testing.T) {
		checker, client, sysCtx := newCheckerSqlite(t)
		tid := client.Tenant.Create().SetName("t-off").SetStatus(tenant.StatusOff).SaveX(sysCtx).ID
		err := checker.CheckTenantAccess(context.Background(), tid, "/x", "GET")
		require.Contains(t, forbiddenMessage(t, err), "tenant is not active")
	})

	t.Run("租户状态FREEZE_拒绝", func(t *testing.T) {
		checker, client, sysCtx := newCheckerSqlite(t)
		tid := client.Tenant.Create().SetName("t-freeze").SetStatus(tenant.StatusFreeze).SaveX(sysCtx).ID
		err := checker.CheckTenantAccess(context.Background(), tid, "/x", "GET")
		require.Contains(t, forbiddenMessage(t, err), "tenant is not active")
	})

	t.Run("租户状态EXPIRED_拒绝", func(t *testing.T) {
		checker, client, sysCtx := newCheckerSqlite(t)
		tid := client.Tenant.Create().SetName("t-exp").SetStatus(tenant.StatusExpired).SaveX(sysCtx).ID
		err := checker.CheckTenantAccess(context.Background(), tid, "/x", "GET")
		require.Contains(t, forbiddenMessage(t, err), "tenant is not active")
	})

	t.Run("API表无记录_failclosed", func(t *testing.T) {
		checker, client, sysCtx := newCheckerSqlite(t)
		tid := client.Tenant.Create().SetName("t-noapi").SetStatus(tenant.StatusOn).SaveX(sysCtx).ID
		err := checker.CheckTenantAccess(context.Background(), tid, "/not/registered", "GET")
		require.Contains(t, forbiddenMessage(t, err), "access denied")
	})

	t.Run("API未归类模块_拒绝", func(t *testing.T) {
		checker, client, sysCtx := newCheckerSqlite(t)
		tid := client.Tenant.Create().SetName("t-unclassified").SetStatus(tenant.StatusOn).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/t/unclassified").SetMethod("GET").SaveX(sysCtx) // business_module 留空 → UNSPECIFIED
		err := checker.CheckTenantAccess(context.Background(), tid, "/t/unclassified", "GET")
		require.Contains(t, forbiddenMessage(t, err), "module not allowed")
	})

	t.Run("租户未挂套餐_拒绝", func(t *testing.T) {
		checker, client, sysCtx := newCheckerSqlite(t)
		tid := client.Tenant.Create().SetName("t-noplan").SetStatus(tenant.StatusOn).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/t/noplan").SetMethod("GET").SetBusinessModule(api.BusinessModuleDict).SaveX(sysCtx)
		err := checker.CheckTenantAccess(context.Background(), tid, "/t/noplan", "GET")
		require.Contains(t, forbiddenMessage(t, err), "no subscription plan")
	})

	t.Run("套餐白名单不含模块_拒绝", func(t *testing.T) {
		checker, client, sysCtx := newCheckerSqlite(t)
		p := client.Plan.Create().SetExpiryPolicy(plan.ExpiryPolicyReadonly).SaveX(sysCtx)
		tid := client.Tenant.Create().SetName("t-nowhitelist").SetStatus(tenant.StatusOn).SetPlanID(p.ID).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/t/nowhitelist").SetMethod("GET").SetBusinessModule(api.BusinessModuleDict).SaveX(sysCtx)
		// 套餐白名单只挂 TASK，请求 DICT 模块 → 拒绝
		_ = client.PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleTask).SaveX(sysCtx)
		err := checker.CheckTenantAccess(context.Background(), tid, "/t/nowhitelist", "GET")
		require.Contains(t, forbiddenMessage(t, err), "module not allowed")
	})

	t.Run("白名单命中_放行", func(t *testing.T) {
		checker, client, sysCtx := newCheckerSqlite(t)
		p := client.Plan.Create().SetExpiryPolicy(plan.ExpiryPolicyReadonly).SaveX(sysCtx)
		tid := client.Tenant.Create().SetName("t-allow").SetStatus(tenant.StatusOn).SetPlanID(p.ID).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/t/allow").SetMethod("GET").SetBusinessModule(api.BusinessModuleDict).SaveX(sysCtx)
		_ = client.PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleDict).SaveX(sysCtx)
		require.NoError(t, checker.CheckTenantAccess(context.Background(), tid, "/t/allow", "GET"))
	})

	t.Run("未到期READONLY_写方法放行", func(t *testing.T) {
		// 未到期：只读门控不生效（须同时满足已到期 + READONLY）
		checker, client, sysCtx := newCheckerSqlite(t)
		p := client.Plan.Create().SetExpiryPolicy(plan.ExpiryPolicyReadonly).SaveX(sysCtx)
		tid := client.Tenant.Create().SetName("t-notexpired").SetStatus(tenant.StatusOn).SetPlanID(p.ID).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/t/notexpired").SetMethod("POST").SetBusinessModule(api.BusinessModuleDict).SaveX(sysCtx)
		_ = client.PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleDict).SaveX(sysCtx)
		require.NoError(t, checker.CheckTenantAccess(context.Background(), tid, "/t/notexpired", "POST"))
	})

	t.Run("到期READONLY_GET放行", func(t *testing.T) {
		checker, client, sysCtx := newCheckerSqlite(t)
		p := client.Plan.Create().SetExpiryPolicy(plan.ExpiryPolicyReadonly).SaveX(sysCtx)
		tid := client.Tenant.Create().SetName("t-readonly-get").SetStatus(tenant.StatusOn).
			SetNillableExpiredAt(&pastTime).SetPlanID(p.ID).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/t/readonlyget").SetMethod("GET").SetBusinessModule(api.BusinessModuleDict).SaveX(sysCtx)
		_ = client.PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleDict).SaveX(sysCtx)
		// 只读放行但仍过白名单：白名单未挂 TASK → TASK 模块即便 GET 也拒绝
		_ = client.Api.Create().SetPath("/t/readonlyget2").SetMethod("GET").SetBusinessModule(api.BusinessModuleTask).SaveX(sysCtx)
		require.NoError(t, checker.CheckTenantAccess(context.Background(), tid, "/t/readonlyget", "GET"))
		err := checker.CheckTenantAccess(context.Background(), tid, "/t/readonlyget2", "GET")
		require.Contains(t, forbiddenMessage(t, err), "module not allowed")
	})

	t.Run("到期READONLY_写方法拒绝", func(t *testing.T) {
		checker, client, sysCtx := newCheckerSqlite(t)
		p := client.Plan.Create().SetExpiryPolicy(plan.ExpiryPolicyReadonly).SaveX(sysCtx)
		tid := client.Tenant.Create().SetName("t-readonly-post").SetStatus(tenant.StatusOn).
			SetNillableExpiredAt(&pastTime).SetPlanID(p.ID).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/t/readonlypost").SetMethod("POST").SetBusinessModule(api.BusinessModuleDict).SaveX(sysCtx)
		_ = client.PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleDict).SaveX(sysCtx)
		err := checker.CheckTenantAccess(context.Background(), tid, "/t/readonlypost", "POST")
		require.Contains(t, forbiddenMessage(t, err), "read-only")
	})

	t.Run("到期BLOCKLOGIN_无只读门控", func(t *testing.T) {
		// 到期但策略为 BLOCK_LOGIN（≠READONLY）：只读门控不生效，按白名单判定
		checker, client, sysCtx := newCheckerSqlite(t)
		p := client.Plan.Create().SetExpiryPolicy(plan.ExpiryPolicyBlockLogin).SaveX(sysCtx)
		tid := client.Tenant.Create().SetName("t-blocklogin").SetStatus(tenant.StatusOn).
			SetNillableExpiredAt(&pastTime).SetPlanID(p.ID).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/t/blocklogin").SetMethod("POST").SetBusinessModule(api.BusinessModuleDict).SaveX(sysCtx)
		_ = client.PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleDict).SaveX(sysCtx)
		require.NoError(t, checker.CheckTenantAccess(context.Background(), tid, "/t/blocklogin", "POST"))
	})

	// 以下三例覆盖"自助类路由豁免模块白名单"（scope_kind = self / tenant-self）。
	// 判据来自权限码权威表，故使用其登记的真实路由，而非虚构路径。
	t.Run("自助路由_白名单不含OPM_仍放行", func(t *testing.T) {
		// GET /api/v1/admin/me 归类 OPM、scope_kind=self；套餐只挂 TASK → 应豁免放行
		checker, client, sysCtx := newCheckerSqlite(t)
		p := client.Plan.Create().SetExpiryPolicy(plan.ExpiryPolicyReadonly).SaveX(sysCtx)
		tid := client.Tenant.Create().SetName("t-self-opm").SetStatus(tenant.StatusOn).SetPlanID(p.ID).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/api/v1/admin/me").SetMethod("GET").SetBusinessModule(api.BusinessModuleOpm).SaveX(sysCtx)
		_ = client.PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleTask).SaveX(sysCtx)
		require.NoError(t, checker.CheckTenantAccess(context.Background(), tid, "/api/v1/admin/me", "GET"))
	})

	t.Run("自助路由_未挂套餐_仍放行", func(t *testing.T) {
		// tenant-self 路由豁免在 planId==0 判定之前生效：无套餐也放行（授权交 casbin）
		checker, client, sysCtx := newCheckerSqlite(t)
		tid := client.Tenant.Create().SetName("t-self-noplan").SetStatus(tenant.StatusOn).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/api/v1/me/quota-accounts").SetMethod("GET").SetBusinessModule(api.BusinessModuleTenant).SaveX(sysCtx)
		require.NoError(t, checker.CheckTenantAccess(context.Background(), tid, "/api/v1/me/quota-accounts", "GET"))
	})

	t.Run("非自助路由_同条件下仍拒绝", func(t *testing.T) {
		// 对照：同为 TENANT 模块但 scope_kind=platform 的路由（/api/v1/admin/tenants GET）
		// 不豁免，白名单未命中仍 module not allowed。
		checker, client, sysCtx := newCheckerSqlite(t)
		p := client.Plan.Create().SetExpiryPolicy(plan.ExpiryPolicyReadonly).SaveX(sysCtx)
		tid := client.Tenant.Create().SetName("t-notself").SetStatus(tenant.StatusOn).SetPlanID(p.ID).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/api/v1/admin/tenants").SetMethod("GET").SetBusinessModule(api.BusinessModuleTenant).SaveX(sysCtx)
		_ = client.PlanModule.Create().SetPlanID(p.ID).SetModule(planmodule.ModuleTask).SaveX(sysCtx)
		err := checker.CheckTenantAccess(context.Background(), tid, "/api/v1/admin/tenants", "GET")
		require.Contains(t, forbiddenMessage(t, err), "module not allowed")
	})

	t.Run("自助路由_租户停用仍拒绝", func(t *testing.T) {
		// 边界：豁免只作用于模块白名单，状态检查保持前置，OFF 租户仍被拒。
		checker, client, sysCtx := newCheckerSqlite(t)
		tid := client.Tenant.Create().SetName("t-self-off").SetStatus(tenant.StatusOff).SaveX(sysCtx).ID
		_ = client.Api.Create().SetPath("/api/v1/admin/me").SetMethod("GET").SetBusinessModule(api.BusinessModuleOpm).SaveX(sysCtx)
		err := checker.CheckTenantAccess(context.Background(), tid, "/api/v1/admin/me", "GET")
		require.Contains(t, forbiddenMessage(t, err), "tenant is not active")
	})
}

// TestIsSelfScopedRoute 断言自助路由判据取自权威表：self / tenant-self 命中，
// 其余 scope_kind 与未登记路由均不豁免（fail-closed）。
func TestIsSelfScopedRoute(t *testing.T) {
	exempt := []struct{ method, path string }{
		{"GET", "/api/v1/admin/me"},                         // self
		{"PUT", "/api/v1/admin/me"},                         // self
		{"POST", "/api/v1/admin/me/password"},               // self
		{"GET", "/api/v1/admin/online-session/my-sessions"}, // self
		{"GET", "/api/v1/me/quota-accounts"},                // tenant-self
		{"get", "/api/v1/admin/me"},                         // 方法大小写不敏感
	}
	for _, c := range exempt {
		require.True(t, isSelfScopedRoute(c.method, c.path), "%s %s 应豁免", c.method, c.path)
	}

	notExempt := []struct{ method, path string }{
		{"GET", "/api/v1/admin/tenants"},               // platform
		{"GET", "/api/v1/admin/plans"},                 // platform
		{"GET", "/api/v1/admin/quota-definitions"},     // 可售/共享
		{"GET", "/api/v1/admin/not-registered-at-all"}, // 未登记 → fail-closed
		{"GET", "/api/v1/me"},                          // 未登记 → fail-closed
	}
	for _, c := range notExempt {
		require.False(t, isSelfScopedRoute(c.method, c.path), "%s %s 不应豁免", c.method, c.path)
	}
}

// TestModuleMapping 全量枚举 proto↔ent 模块映射：各业务模块双向一致，
// UNSPECIFIED / 未知值 / 空串各自落到默认分支。
func TestModuleMapping(t *testing.T) {
	pairs := map[identityV1.Module]struct {
		entModule planmodule.Module
		apiModule api.BusinessModule
	}{
		identityV1.Module_DASHBOARD:        {planmodule.ModuleDashboard, api.BusinessModuleDashboard},
		identityV1.Module_OPM:              {planmodule.ModuleOpm, api.BusinessModuleOpm},
		identityV1.Module_SYSTEM:           {planmodule.ModuleSystem, api.BusinessModuleSystem},
		identityV1.Module_DICT:             {planmodule.ModuleDict, api.BusinessModuleDict},
		identityV1.Module_TENANT:           {planmodule.ModuleTenant, api.BusinessModuleTenant},
		identityV1.Module_PERMISSION:       {planmodule.ModulePermission, api.BusinessModulePermission},
		identityV1.Module_LOG:              {planmodule.ModuleLog, api.BusinessModuleLog},
		identityV1.Module_INTERNAL_MESSAGE: {planmodule.ModuleInternalMessage, api.BusinessModuleInternalMessage},
		identityV1.Module_TASK:             {planmodule.ModuleTask, api.BusinessModuleTask},
		identityV1.Module_ACCELERATOR:      {planmodule.ModuleAccelerator, api.BusinessModuleAccelerator},
		identityV1.Module_IMAGE:            {planmodule.ModuleImage, api.BusinessModuleImage},
	}

	for protoMod, entMods := range pairs {
		require.Equal(t, entMods.entModule, mapProtoModuleToEnt(protoMod), "proto→ent(planmodule) 映射漂移: %v", protoMod)
		require.Equal(t, protoMod, mapApiBusinessModuleToProto(entMods.apiModule), "ent(api)→proto 映射漂移: %v", entMods.apiModule)
	}

	// 默认分支：UNSPECIFIED/未知 proto 值 → 空串；空 ent 串 → UNSPECIFIED。
	require.Empty(t, mapProtoModuleToEnt(identityV1.Module_MODULE_UNSPECIFIED))
	require.Empty(t, mapProtoModuleToEnt(identityV1.Module(9999)))
	require.Equal(t, identityV1.Module_MODULE_UNSPECIFIED, mapApiBusinessModuleToProto(""))
}

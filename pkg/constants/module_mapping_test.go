package constants

import (
	"testing"

	"github.com/stretchr/testify/assert"

	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
)

// TestServiceTagToBusinessModuleExactMapping 钉死服务 tag → 业务模块映射表的
// 全量内容。该表驱动 sys_apis.business_module 回填，是租户套餐模块白名单
// （模块白名单过滤）的判定来源：任何一个条目漂移都会导致对应服务的 API
// 在租户侧被误放行或误拒绝，因此任何改动都必须是有意识的、显式更新本测试的改动。
func TestServiceTagToBusinessModuleExactMapping(t *testing.T) {
	expected := map[string]identityV1.Module{
		"ImageService":            identityV1.Module_IMAGE,
		"InferenceService":        identityV1.Module_INFERENCE,
		"InstanceService":         identityV1.Module_INSTANCE,
		"AcceleratorAdminService": identityV1.Module_ACCELERATOR,
		"AcceleratorService":      identityV1.Module_ACCELERATOR,
		"QuotaSelfService":        identityV1.Module_TENANT,
		"AdminPortalService":      identityV1.Module_DASHBOARD,
		"DashboardService":        identityV1.Module_DASHBOARD,
		"MfaService":              identityV1.Module_SYSTEM,
		"PlanModuleService":       identityV1.Module_TENANT,
		"AuthenticationService":   identityV1.Module_DASHBOARD,

		"UserService":        identityV1.Module_OPM,
		"OrgUnitService":     identityV1.Module_OPM,
		"PositionService":    identityV1.Module_OPM,
		"UserProfileService": identityV1.Module_OPM,
		"RoleService":        identityV1.Module_OPM,

		"MenuService":            identityV1.Module_PERMISSION,
		"ApiService":             identityV1.Module_PERMISSION,
		"PermissionService":      identityV1.Module_PERMISSION,
		"PermissionGroupService": identityV1.Module_PERMISSION,

		"DictTypeService":    identityV1.Module_DICT,
		"DictEntryService":   identityV1.Module_DICT,
		"LanguageService":    identityV1.Module_SYSTEM,
		"TaskService":        identityV1.Module_TASK,
		"LoginPolicyService": identityV1.Module_SYSTEM,
		"ConfigService":      identityV1.Module_SYSTEM,
		"AccessKeyService":   identityV1.Module_SYSTEM,

		"TenantService":     identityV1.Module_TENANT,
		"QuotaAdminService": identityV1.Module_TENANT,
		"QuotaLabService":   identityV1.Module_TENANT,
		"PlanService":       identityV1.Module_TENANT,
		"PlanQuotaService":  identityV1.Module_TENANT,

		"ApiAuditLogService":         identityV1.Module_LOG,
		"LoginAuditLogService":       identityV1.Module_LOG,
		"OperationAuditLogService":   identityV1.Module_LOG,
		"DataAccessAuditLogService":  identityV1.Module_LOG,
		"PermissionAuditLogService":  identityV1.Module_LOG,
		"PolicyEvaluationLogService": identityV1.Module_LOG,
		"RedisCacheMonitorService":   identityV1.Module_LOG,
		"ServerMonitorService":       identityV1.Module_SYSTEM,
		"NotificationChannelService": identityV1.Module_SYSTEM,
		"OnlineSessionService":       identityV1.Module_SYSTEM,

		"InternalMessageService":          identityV1.Module_INTERNAL_MESSAGE,
		"InternalMessageCategoryService":  identityV1.Module_INTERNAL_MESSAGE,
		"InternalMessageRecipientService": identityV1.Module_INTERNAL_MESSAGE,

		"NetworkService":         identityV1.Module_NETWORK,
		"PlatformNetworkService": identityV1.Module_NETWORK,
		"ModelService":           identityV1.Module_MODEL,
		"ModelDevService":        identityV1.Module_MODEL,
	}
	assert.Equal(t, expected, ServiceTagToBusinessModule,
		"ServiceTagToBusinessModule 内容漂移；如为有意变更请同步更新本测试")
}

// TestServiceTagToBusinessModuleReverseMapping 双向一致性：由正向映射推导
// 反向映射（模块 → 服务 tag 集合），必须与预期完全一致。防止只看正向表时
// 某模块的登记被悄悄增删。
func TestServiceTagToBusinessModuleReverseMapping(t *testing.T) {
	expected := map[identityV1.Module][]string{
		identityV1.Module_IMAGE:            {"ImageService"},
		identityV1.Module_INFERENCE:        {"InferenceService"},
		identityV1.Module_INSTANCE:         {"InstanceService"},
		identityV1.Module_DASHBOARD:        {"AdminPortalService", "DashboardService", "AuthenticationService"},
		identityV1.Module_OPM:              {"UserService", "OrgUnitService", "PositionService", "UserProfileService", "RoleService"},
		identityV1.Module_PERMISSION:       {"MenuService", "ApiService", "PermissionService", "PermissionGroupService"},
		identityV1.Module_DICT:             {"DictTypeService", "DictEntryService"},
		identityV1.Module_SYSTEM:           {"MfaService", "LanguageService", "LoginPolicyService", "ConfigService", "AccessKeyService", "ServerMonitorService", "NotificationChannelService", "OnlineSessionService"},
		identityV1.Module_TASK:             {"TaskService"},
		identityV1.Module_TENANT:           {"TenantService", "PlanService", "PlanQuotaService", "PlanModuleService", "QuotaAdminService", "QuotaLabService", "QuotaSelfService"},
		identityV1.Module_LOG:              {"ApiAuditLogService", "LoginAuditLogService", "OperationAuditLogService", "DataAccessAuditLogService", "PermissionAuditLogService", "PolicyEvaluationLogService", "RedisCacheMonitorService"},
		identityV1.Module_INTERNAL_MESSAGE: {"InternalMessageService", "InternalMessageCategoryService", "InternalMessageRecipientService"},
		identityV1.Module_NETWORK:          {"NetworkService", "PlatformNetworkService"},
		identityV1.Module_ACCELERATOR:      {"AcceleratorAdminService", "AcceleratorService"},
		identityV1.Module_MODEL:            {"ModelDevService", "ModelService"},
	}

	actual := make(map[identityV1.Module][]string)
	for tag, module := range ServiceTagToBusinessModule {
		actual[module] = append(actual[module], tag)
	}
	assert.Len(t, actual, len(expected), "反向映射的模块数量与预期不符")
	for module, expectedTags := range expected {
		assert.Contains(t, actual, module, "模块 %v 应有登记的服务", module)
		assert.ElementsMatch(t, expectedTags, actual[module],
			"模块 %v 登记的服务集合与预期不符", module)
	}
}

// TestServiceTagToBusinessModuleValuesValid 映射值合法性：每个 tag 必须映射到
// 一个有定义且非 UNSPECIFIED 的模块。UNSPECIFIED 会让该服务的全部 API 被
// 租户白名单当作"不在任何白名单内"而拒绝（fail-closed），等于整块业务
// 对租户不可用——必须显式登记。
func TestServiceTagToBusinessModuleValuesValid(t *testing.T) {
	for tag, module := range ServiceTagToBusinessModule {
		assert.NotEmpty(t, tag, "映射键不允许为空串")
		name, defined := identityV1.Module_name[int32(module)]
		assert.True(t, defined, "tag %q 映射到未定义模块值 %d", tag, module)
		assert.NotEqual(t, "MODULE_UNSPECIFIED", name,
			"tag %q 映射到 UNSPECIFIED（等于未登记，租户侧将被 fail-closed 拒绝）", tag)
	}
}

// modulesWithoutService 是"已在枚举中定义、但当前确实没有服务登记"的模块白名单。
// 加入本表等于显式声明：该模块此刻没有任何服务承接，是有意为之而非漏登记。
//
// ModelDev and the Model BFF share the MODEL module. This is intentional:
// model import/version lifecycle and ModelDev are both tenant model workflows.
var modulesWithoutService = map[identityV1.Module]string{}

// TestServiceTagToBusinessModuleCoversAllDefinedModules 覆盖完备性：每个已定义的
// 非 UNSPECIFIED 模块都必须有至少一个服务 tag 登记。模块枚举新增值而映射表
// 未同步登记，是租户白名单误伤整模块的典型来源，此测试让它当场暴露。
func TestServiceTagToBusinessModuleCoversAllDefinedModules(t *testing.T) {
	registered := make(map[identityV1.Module]bool)
	for _, module := range ServiceTagToBusinessModule {
		registered[module] = true
	}
	for value, name := range identityV1.Module_name {
		if name == "MODULE_UNSPECIFIED" {
			continue
		}
		module := identityV1.Module(value)
		if _, exempt := modulesWithoutService[module]; exempt {
			continue
		}
		assert.True(t, registered[module],
			"模块 %s(%d) 已在枚举中定义但映射表无任何服务登记；若为新增模块请在 ServiceTagToBusinessModule 显式登记各服务，若确无服务承接请加入 modulesWithoutService", name, value)
	}
}

// TestModulesWithoutServiceAreStillUndefinedOrUnregistered 防止豁免表腐烂：
// 豁免项必须仍然"没有服务登记"，否则说明服务已经接回来了，该把它从豁免表移除。
func TestModulesWithoutServiceAreStillUnregistered(t *testing.T) {
	registered := make(map[identityV1.Module]bool)
	for _, module := range ServiceTagToBusinessModule {
		registered[module] = true
	}
	for module, reason := range modulesWithoutService {
		name, defined := identityV1.Module_name[int32(module)]
		assert.True(t, defined, "豁免表含未定义模块值 %d", module)
		assert.False(t, registered[module],
			"模块 %s 已被豁免为无服务（%s），但映射表里已有登记；请从 modulesWithoutService 移除", name, reason)
	}
}

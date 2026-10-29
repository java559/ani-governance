package constants

import (
	identityV1 "go-wind-admin/api/gen/go/identity/service/v1"
)

// ServiceTagToBusinessModule 将 OpenAPI tag 名（gRPC 服务名）映射到业务功能模块。
// 用于 sys_apis.business_module 的回填，供套餐模块白名单过滤使用。
// 新增服务时必须在此登记，否则其 API 的 business_module 将为 UNSPECIFIED，
// 租户白名单过滤会把 UNSPECIFIED 视为"不在任何白名单内"而拒绝访问。
var ServiceTagToBusinessModule = map[string]identityV1.Module{
	"ImageService":    identityV1.Module_IMAGE,
	"InstanceService": identityV1.Module_INSTANCE,
	// 首页资源统计卡片为租户级只读聚合，挂 INSTANCE 模块（现有套餐均含 INSTANCE）。
	"OverviewService":             identityV1.Module_INSTANCE,
	"StorageService":              identityV1.Module_STORAGE,
	"VMService":                   identityV1.Module_VIRTUAL_MACHINE,
	"ComputePresetService":        identityV1.Module_SYSTEM,
	"AcceleratorAdminService":     identityV1.Module_ACCELERATOR,
	"AcceleratorService":          identityV1.Module_ACCELERATOR,
	"GpuAdminService":             identityV1.Module_ACCELERATOR,
	"GpuService":                  identityV1.Module_ACCELERATOR,
	"ResourceUsageAdminService":   identityV1.Module_DASHBOARD,
	"GpuTenantAdminService":       identityV1.Module_ACCELERATOR,
	"QuotaSelfService":            identityV1.Module_TENANT,
	"PlatformNetworkService":      identityV1.Module_NETWORK,
	"NetworkService":              identityV1.Module_NETWORK,
	"ModelService":                identityV1.Module_MODEL,
	"InferenceService":            identityV1.Module_INFERENCE,
	"InferenceKeyService":         identityV1.Module_INFERENCE,
	"InferenceDiagnosticsService": identityV1.Module_INFERENCE,
	"ModelDevService":             identityV1.Module_MODEL,
	"AdminPortalService":          identityV1.Module_DASHBOARD,
	"DashboardService":            identityV1.Module_DASHBOARD,

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

	"TenantService":    identityV1.Module_TENANT,
	"PlanService":      identityV1.Module_TENANT,
	"PlanQuotaService": identityV1.Module_TENANT,

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

	"MfaService":            identityV1.Module_SYSTEM,
	"PlanModuleService":     identityV1.Module_TENANT,
	"AuthenticationService": identityV1.Module_DASHBOARD,

	// QUOTA-GPU-LOCAL-01：配额管理读取与实验入口均登记为 TENANT 模块
	//（§10.1/§10.2；实验路由仅 quota_lab 构建的 OpenAPI 之外手动登记）。
	"QuotaAdminService": identityV1.Module_TENANT,
	"QuotaLabService":   identityV1.Module_TENANT,

	// 平台组件健康快照（BOSS 组件状态页）：平台级只读接口，仅 platform:admin 可达。
	"PlatformComponentService": identityV1.Module_SYSTEM,
}

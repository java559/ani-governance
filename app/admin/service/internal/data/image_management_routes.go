package data

// Platform image management surface. The 22 routes are a fixed table because
// each path and method carries exactly one permission code and one gRPC
// operation; a generated binding would let a route and its permission drift
// apart. Paths use the proto snake_case parameter names on purpose: the runtime
// router matches what the HTTP handlers register, and the API catalog stores the
// same value.
const (
	PlatformImageSpacePath             = "/api/v1/platform/images/space"
	PlatformImageSpaceEnablePath       = "/api/v1/platform/images/space:enable"
	PlatformImageCredentialPath        = "/api/v1/platform/images/publisher-credential"
	PlatformImageCredentialIssuePath   = "/api/v1/platform/images/publisher-credential:issue"
	PlatformImageCredentialResetPath   = "/api/v1/platform/images/publisher-credential:reset"
	PlatformImageCredentialDisablePath = "/api/v1/platform/images/publisher-credential:disable"
	PlatformImageCollectionPath        = "/api/v1/platform/images/registrations"
	PlatformImageGetPath               = "/api/v1/platform/images/registrations/{image_id}"
	PlatformImageUnregisterPath        = "/api/v1/platform/images/registrations/{image_id}:unregister"

	ManagedTenantImageSpacePath             = "/api/v1/platform/tenants/{target_tenant_id}/images/space"
	ManagedTenantImageSpaceEnablePath       = "/api/v1/platform/tenants/{target_tenant_id}/images/space:enable"
	ManagedTenantImageCredentialPath        = "/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential"
	ManagedTenantImageCredentialIssuePath   = "/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential:issue"
	ManagedTenantImageCredentialResetPath   = "/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential:reset"
	ManagedTenantImageCredentialDisablePath = "/api/v1/platform/tenants/{target_tenant_id}/images/publisher-credential:disable"
	ManagedTenantImageCollectionPath        = "/api/v1/platform/tenants/{target_tenant_id}/images/registrations"
	ManagedTenantImageGetPath               = "/api/v1/platform/tenants/{target_tenant_id}/images/registrations/{image_id}"
	ManagedTenantImageUnregisterPath        = "/api/v1/platform/tenants/{target_tenant_id}/images/registrations/{image_id}:unregister"
)

// ImageManagementRoute 是一条平台镜像管理路由的唯一定义。
type ImageManagementRoute struct {
	Path, Method, Operation, Service, Permission string
}

// imageManagementRoutes 是 22 条路由的权威表，顺序即平台 11 条、代管 11 条。
var imageManagementRoutes = map[string]ImageManagementRoute{
	"platform-space-get":        {PlatformImageSpacePath, "GET", "GetPlatformSpace", "PlatformImageService", "image:platform:space:get"},
	"platform-space-enable":     {PlatformImageSpaceEnablePath, "POST", "InitializePlatform", "PlatformImageService", "image:platform:space:enable"},
	"platform-credential-get":   {PlatformImageCredentialPath, "GET", "GetPlatformPublisherCredential", "PlatformImageService", "image:platform:credential:get"},
	"platform-credential-issue": {PlatformImageCredentialIssuePath, "POST", "IssuePlatformPublisherCredential", "PlatformImageService", "image:platform:credential:issue"},
	"platform-credential-reset": {PlatformImageCredentialResetPath, "POST", "ResetPlatformPublisherCredential", "PlatformImageService", "image:platform:credential:reset"},
	"platform-credential-disable": {
		PlatformImageCredentialDisablePath, "POST", "DisablePlatformPublisherCredential", "PlatformImageService", "image:platform:credential:disable"},
	"platform-registration-list":       {PlatformImageCollectionPath, "GET", "ListPlatformImages", "PlatformImageService", "image:platform:registration:list"},
	"platform-registration-get":        {PlatformImageGetPath, "GET", "GetPlatformImage", "PlatformImageService", "image:platform:registration:get"},
	"platform-registration-create":     {PlatformImageCollectionPath, "POST", "RegisterPlatformImage", "PlatformImageService", "image:platform:registration:create"},
	"platform-registration-update":     {PlatformImageGetPath, "PATCH", "UpdatePlatformImage", "PlatformImageService", "image:platform:registration:update"},
	"platform-registration-unregister": {PlatformImageUnregisterPath, "POST", "UnregisterPlatformImage", "PlatformImageService", "image:platform:registration:unregister"},

	"tenant-space-get":        {ManagedTenantImageSpacePath, "GET", "GetTenantImageSpace", "AdminTenantImageService", "image:platform:tenant-space:get"},
	"tenant-space-enable":     {ManagedTenantImageSpaceEnablePath, "POST", "EnsureTenantImageSpace", "AdminTenantImageService", "image:platform:tenant-space:enable"},
	"tenant-credential-get":   {ManagedTenantImageCredentialPath, "GET", "GetTenantPublisherCredential", "AdminTenantImageService", "image:platform:tenant-credential:get"},
	"tenant-credential-issue": {ManagedTenantImageCredentialIssuePath, "POST", "IssueTenantPublisherCredential", "AdminTenantImageService", "image:platform:tenant-credential:issue"},
	"tenant-credential-reset": {ManagedTenantImageCredentialResetPath, "POST", "ResetTenantPublisherCredential", "AdminTenantImageService", "image:platform:tenant-credential:reset"},
	"tenant-credential-disable": {
		ManagedTenantImageCredentialDisablePath, "POST", "DisableTenantPublisherCredential", "AdminTenantImageService", "image:platform:tenant-credential:disable"},
	"tenant-registration-list":       {ManagedTenantImageCollectionPath, "GET", "ListTenantImages", "AdminTenantImageService", "image:platform:tenant-registration:list"},
	"tenant-registration-get":        {ManagedTenantImageGetPath, "GET", "GetTenantImage", "AdminTenantImageService", "image:platform:tenant-registration:get"},
	"tenant-registration-create":     {ManagedTenantImageCollectionPath, "POST", "RegisterTenantImage", "AdminTenantImageService", "image:platform:tenant-registration:create"},
	"tenant-registration-update":     {ManagedTenantImageGetPath, "PATCH", "UpdateTenantImage", "AdminTenantImageService", "image:platform:tenant-registration:update"},
	"tenant-registration-unregister": {ManagedTenantImageUnregisterPath, "POST", "UnregisterTenantImage", "AdminTenantImageService", "image:platform:tenant-registration:unregister"},
}

// ImageManagementActionRoute 返回一条已登记路由；未登记的 action 一律视为不存在。
func ImageManagementActionRoute(action string) (ImageManagementRoute, bool) {
	r, ok := imageManagementRoutes[action]
	return r, ok
}

// ImageManagementRoutes 返回路由表副本，供 HTTP 注册与契约测试逐条枚举。
func ImageManagementRoutes() []ImageManagementRoute {
	out := make([]ImageManagementRoute, 0, len(imageManagementRoutes))
	for _, r := range imageManagementRoutes {
		out = append(out, r)
	}
	return out
}

// ImageManagementWriteActions 是 14 条写路由的 action 集合，超时预算与
// Cache-Control 判定都以此为准；读路由不得出现在这里。
var ImageManagementWriteActions = map[string]bool{
	"platform-space-enable":            true,
	"platform-credential-issue":        true,
	"platform-credential-reset":        true,
	"platform-credential-disable":      true,
	"platform-registration-create":     true,
	"platform-registration-update":     true,
	"platform-registration-unregister": true,
	"tenant-space-enable":              true,
	"tenant-credential-issue":          true,
	"tenant-credential-reset":          true,
	"tenant-credential-disable":        true,
	"tenant-registration-create":       true,
	"tenant-registration-update":       true,
	"tenant-registration-unregister":   true,
}

// SecretBearingActions 是仅有的 4 条返回 secret 的路由。其它任何路由返回
// secret 都属于破坏契约，服务层必须拒绝整个回包。
var SecretBearingActions = map[string]bool{
	"platform-credential-issue": true,
	"platform-credential-reset": true,
	"tenant-credential-issue":   true,
	"tenant-credential-reset":   true,
}

package server

import (
	"context"
	"fmt"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/selector"
	"github.com/go-kratos/kratos/v2/middleware/validate"
	"github.com/go-kratos/kratos/v2/transport/http"

	authz "go-wind-admin/pkg/localdeps/kratos-authz/middleware"

	swaggerUI "go-wind-admin/pkg/localdeps/kratos-swagger-ui"

	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"
	bLogger "go-wind-admin/pkg/localdeps/kratos-bootstrap/logger"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/rpc"

	"go-wind-admin/app/admin/service/cmd/server/assets"
	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/service"

	adminV1 "go-wind-admin/api/gen/go/admin/service/v1"
	auditV1 "go-wind-admin/api/gen/go/audit/service/v1"

	"go-wind-admin/pkg/authorizer"
	appViewer "go-wind-admin/pkg/entgo/viewer"
	"go-wind-admin/pkg/middleware/auth"
	applogging "go-wind-admin/pkg/middleware/logging"
	"go-wind-admin/pkg/middleware/requestid"
)

// NewRestMiddleware 创建中间件
func NewRestMiddleware(
	ctx *bootstrap.Context,
	accessTokenChecker auth.AccessTokenChecker,
	signingKeyStore auth.SigningKeyStore,
	tenantAccessChecker auth.TenantAccessChecker,
	authorizer *authorizer.Authorizer,
	apiAuditLogRepo *data.ApiAuditLogRepo,
	loginLogRepo *data.LoginAuditLogRepo,
	operationAuditLogRepo *data.OperationAuditLogRepo,
	permissionAuditLogRepo *data.PermissionAuditLogRepo,
	dataAccessAuditLogRepo *data.DataAccessAuditLogRepo,
	policyEvaluationLogRepo *data.PolicyEvaluationLogRepo,
) []middleware.Middleware {
	var ms []middleware.Middleware
	// recovery 必须置于链首：任何中间件/handler 的 panic（如审计日志解析畸形 JWT）
	// 兜底为 500，避免崩溃请求 goroutine、被用作未认证 DoS。
	ms = append(ms, recovery.Recovery())
	// request id 必须早于 logging 与审计中间件：两者都要读这个 ID
	// （审计直接读 request header，日志从上下文取），挂晚了只能拿到空值。
	// 同时它会把 ID 写进错误 metadata，对齐 ANI 契约要求的 request_id 字段。
	ms = append(ms, requestid.Server())
	// Login request String() may include the submitted password.
	// Keep operation/status/error evidence, but never log request bodies here.
	ms = append(ms, logging.Server(log.NewFilter(bLogger.AsKratosLogger(ctx.GetLogger()), log.FilterKey("args"))))

	ms = append(ms, applogging.Server(
		applogging.WithWriteApiLogFunc(func(ctx context.Context, data *auditV1.ApiAuditLog) error {
			// TODO 如果系统的负载比较小，可以同步写入数据库，否则，建议使用异步方式，即投递进队列。
			return apiAuditLogRepo.Create(ctx, &auditV1.CreateApiAuditLogRequest{Data: data})
		}),
		applogging.WithWriteLoginLogFunc(func(ctx context.Context, data *auditV1.LoginAuditLog) error {
			// TODO 如果系统的负载比较小，可以同步写入数据库，否则，建议使用异步方式，即投递进队列。
			return loginLogRepo.Create(ctx, &auditV1.CreateLoginAuditLogRequest{Data: data})
		}),
		applogging.WithWriteOperationAuditLogFunc(func(ctx context.Context, data *auditV1.OperationAuditLog) error {
			return operationAuditLogRepo.Create(ctx, &auditV1.CreateOperationAuditLogRequest{Data: data})
		}),
		applogging.WithWritePermissionAuditLogFunc(func(ctx context.Context, data *auditV1.PermissionAuditLog) error {
			return permissionAuditLogRepo.Create(ctx, &auditV1.CreatePermissionAuditLogRequest{Data: data})
		}),
		applogging.WithWriteDataAccessAuditLogFunc(func(ctx context.Context, data *auditV1.DataAccessAuditLog) error {
			return dataAccessAuditLogRepo.Create(ctx, &auditV1.CreateDataAccessAuditLogRequest{Data: data})
		}),
	))

	// 输入校验：对所有 RPC（含白名单内的 login/register/refresh/MFA）调用生成代码的
	// Validate()。放 selector 外，否则白名单路由会被跳过——而它们恰是最需要校验入参的。
	// 当前业务 proto 尚未补 (validate.rules)，多数 Validate() 返回 nil；补规则后再生效。
	ms = append(ms, auth.CredentialHeaders(), validate.Validator())

	// add white list for authentication.
	rpc.AddWhiteList(
		adminV1.OperationAuthenticationServicePasswordLogin,
		// 平台账密登录免鉴权：与租户登录同属未认证入口，
		// 平台身份的准入校验在授权阶段完成（平台角色闸门），不靠 access token 拦。
		adminV1.OperationAuthenticationServicePlatformPasswordLogin,
		adminV1.OperationAuthenticationServiceGenerateCaptcha,
		adminV1.OperationAuthenticationServiceVerifyCaptcha,
		// 刷新令牌接口免鉴权：refresh token 现以 HttpOnly Cookie 传输且为自描述 JWT，
		// 可脱离 access token 独立鉴权。页面刷新后 access token 丢失时，前端凭
		// refresh cookie 静默恢复会话，不再强制重新登录。
		adminV1.OperationAuthenticationServiceRefreshAccessToken,
		// CSRF 证明签发免鉴权：身份来自 HttpOnly RT cookie（浏览器自动携带），
		// 证明按会话 sid 绑定签发，供刷新时经 X-CSRF-Token 回传（双提交 Cookie 模式）。
		// 端点内部做 Origin/Referer 精确校验，跨站页面拿不到证明。
		adminV1.OperationAuthenticationServiceCsrf,
		// 登出免鉴权：身份与会话定位全部来自 HttpOnly RT cookie 中经验签的
		// application/sid claim，按 sid 精确吊销当前应用的当前逻辑会话。
		// 旧实现依赖 access token 取操作人，AT 过期后无法登出——白名单化后消除该缺陷。
		adminV1.OperationAuthenticationServiceRevokeJti,
		// MFA 登录挑战验证免鉴权：operation_id 由登录流程签发，见 doGrantTypePassword 的 MFA 闸门。
		// 仅此一个 MFA RPC 免鉴权；管理侧 RPC（GetMFAStatus 等）走正常 auth+authz。
		adminV1.OperationMfaServiceVerifyMFAChallenge,
		// 找回密码两个端点免鉴权：验证码发送与凭码重置，
		// 重置成功后会吊销该用户全部会话。
		adminV1.OperationAuthenticationServiceForgotPassword,
		adminV1.OperationAuthenticationServiceResetPasswordByCode,
		adminV1.OperationAuthenticationServiceAcceptInvitation,
	)

	ms = append(ms, selector.Server(
		auth.Server(
			auth.WithAccessTokenChecker(accessTokenChecker),
			auth.WithSigningKeyStore(signingKeyStore),
			auth.WithTenantAccessChecker(tenantAccessChecker),
			auth.WithInjectMetadata(false),
			auth.WithInjectEnt(true),
		),
		authz.Server(newEvalLoggingEngine(authorizer.Engine(), policyEvaluationLogRepo)),
	).
		Match(rpc.NewRestWhiteListMatcher()).
		Build(),
	)

	return ms
}

// NewRestServer new an REST server.
func NewRestServer(
	ctx *bootstrap.Context,

	middlewares []middleware.Middleware,
	authorizer *authorizer.Authorizer,

	authenticationService *service.AuthenticationService,
	mfaService *service.MfaService,
	loginPolicyService *service.LoginPolicyService,

	portalService *service.AdminPortalService,
	taskService *service.TaskService,

	dictTypeService *service.DictTypeService,
	dictEntryService *service.DictEntryService,
	languageService *service.LanguageService,

	tenantService *service.TenantService,
	planService *service.PlanService,
	planQuotaService *service.PlanQuotaService,
	quotaAdminService *service.QuotaAdminService,
	planModuleService *service.PlanModuleService,
	userService *service.UserService,
	userProfileService *service.UserProfileService,
	roleService *service.RoleService,
	positionService *service.PositionService,
	orgUnitService *service.OrgUnitService,

	menuService *service.MenuService,
	apiService *service.ApiService,
	permissionService *service.PermissionService,
	permissionGroupService *service.PermissionGroupService,
	permissionAuditLogService *service.PermissionAuditLogService,
	policyEvaluationLogService *service.PolicyEvaluationLogService,

	loginAuditLogService *service.LoginAuditLogService,
	apiAuditLogService *service.ApiAuditLogService,
	operationAuditLogService *service.OperationAuditLogService,
	dataAccessAuditLogService *service.DataAccessAuditLogService,
	redisCacheMonitorService *service.RedisCacheMonitorService,
	serverMonitorService *service.ServerMonitorService,
	notificationChannelService *service.NotificationChannelService,
	onlineSessionService *service.OnlineSessionService,
	dashboardService *service.DashboardService,

	internalMessageService *service.InternalMessageService,
	internalMessageCategoryService *service.InternalMessageCategoryService,
	internalMessageRecipientService *service.InternalMessageRecipientService,

	// register:param ── 新模块服务形参在此行后注册(make register 工具锚点,勿删)
	accessKeyService *service.AccessKeyService,
	configService *service.ConfigService,
	networkService *service.NetworkService,
	platformNetworkService *service.PlatformNetworkService,
	acceleratorService *service.AcceleratorService,
	imageService *service.ImageService,
	modeldevService *service.ModelDevService,
	inferenceService *service.InferenceService,
	instanceServices ...*service.InstanceService,
) (*http.Server, error) {
	cfg := ctx.GetConfig()

	if cfg == nil || cfg.Server == nil || cfg.Server.Rest == nil {
		return nil, nil
	}

	srv, err := rpc.CreateRestServerWithFilters(cfg, []http.FilterFunc{auth.ImageHTTPFilter, auth.NetworkHTTPFilter},
		middlewares...,
	)
	if err != nil {
		return nil, err
	}

	apiService.RegisterRouteWalker(srv)

	adminV1.RegisterAuthenticationServiceHTTPServer(srv, authenticationService)
	registerInvitationPage(srv)

	adminV1.RegisterMfaServiceHTTPServer(srv, mfaService)

	adminV1.RegisterUserProfileServiceHTTPServer(srv, userProfileService)

	adminV1.RegisterAdminPortalServiceHTTPServer(srv, portalService)
	adminV1.RegisterTaskServiceHTTPServer(srv, taskService)
	adminV1.RegisterLoginPolicyServiceHTTPServer(srv, loginPolicyService)

	adminV1.RegisterDictTypeServiceHTTPServer(srv, dictTypeService)
	adminV1.RegisterDictEntryServiceHTTPServer(srv, dictEntryService)
	adminV1.RegisterLanguageServiceHTTPServer(srv, languageService)

	adminV1.RegisterApiServiceHTTPServer(srv, apiService)
	adminV1.RegisterMenuServiceHTTPServer(srv, menuService)
	adminV1.RegisterPermissionServiceHTTPServer(srv, permissionService)
	adminV1.RegisterPermissionGroupServiceHTTPServer(srv, permissionGroupService)
	adminV1.RegisterPolicyEvaluationLogServiceHTTPServer(srv, policyEvaluationLogService)
	adminV1.RegisterPermissionAuditLogServiceHTTPServer(srv, permissionAuditLogService)

	// 字段权限装饰器在最内层（紧贴业务实现），静态脱敏 redact 在外层兜底；
	// 两者都按"清值"裁剪，protojson 默认不输出未填充字段，效果等价于移除字段。
	adminV1.RegisterUserServiceHTTPServer(srv, adminV1.RedactedUserServiceServer(
		service.NewFieldPermissionUserServiceServer(&userServiceServerAdapter{UserServiceHTTPServer: userService}),
		nil))
	adminV1.RegisterOrgUnitServiceHTTPServer(srv, orgUnitService)
	adminV1.RegisterRoleServiceHTTPServer(srv, roleService)
	adminV1.RegisterPositionServiceHTTPServer(srv, positionService)
	adminV1.RegisterTenantServiceHTTPServer(srv, tenantService)
	adminV1.RegisterPlanServiceHTTPServer(srv, planService)
	adminV1.RegisterPlanQuotaServiceHTTPServer(srv, planQuotaService)
	adminV1.RegisterPlanModuleServiceHTTPServer(srv, planModuleService)
	adminV1.RegisterQuotaAdminServiceHTTPServer(srv, quotaAdminService)

	adminV1.RegisterLoginAuditLogServiceHTTPServer(srv, loginAuditLogService)
	adminV1.RegisterApiAuditLogServiceHTTPServer(srv, apiAuditLogService)
	adminV1.RegisterOperationAuditLogServiceHTTPServer(srv, operationAuditLogService)
	adminV1.RegisterDataAccessAuditLogServiceHTTPServer(srv, dataAccessAuditLogService)
	adminV1.RegisterRedisCacheMonitorServiceHTTPServer(srv, redisCacheMonitorService)
	adminV1.RegisterServerMonitorServiceHTTPServer(srv, serverMonitorService)
	adminV1.RegisterNotificationChannelServiceHTTPServer(srv, notificationChannelService)
	adminV1.RegisterOnlineSessionServiceHTTPServer(srv, onlineSessionService)
	adminV1.RegisterDashboardServiceHTTPServer(srv, dashboardService)

	adminV1.RegisterInternalMessageServiceHTTPServer(srv, internalMessageService)
	adminV1.RegisterInternalMessageCategoryServiceHTTPServer(srv, internalMessageCategoryService)
	adminV1.RegisterInternalMessageRecipientServiceHTTPServer(srv, internalMessageRecipientService)

	// register:route ── 新模块路由在此行后注册(make register 工具锚点,勿删)
	registerAccessKeyHTTP(srv, accessKeyService)
	adminV1.RegisterConfigServiceHTTPServer(srv, configService)
	adminV1.RegisterNetworkServiceHTTPServer(srv, networkService)
	adminV1.RegisterPlatformNetworkServiceHTTPServer(srv, platformNetworkService)
	adminV1.RegisterImageServiceHTTPServer(srv, imageService)
	adminV1.RegisterAcceleratorAdminServiceHTTPServer(srv, acceleratorService)
	adminV1.RegisterAcceleratorServiceHTTPServer(srv, acceleratorService)
	adminV1.RegisterQuotaSelfServiceHTTPServer(srv, acceleratorService)
	registerModelDevHTTP(srv, modeldevService)
	if inferenceService != nil {
		RegisterInferenceHTTPServer(srv, inferenceService)
	}
	if len(instanceServices) > 0 && instanceServices[0] != nil {
		RegisterInstanceHTTPServer(srv, instanceServices[0])
	}

	if cfg.GetServer().GetRest().GetEnableSwagger() {
		swaggerUI.RegisterSwaggerUIServerWithOption(
			srv,
			swaggerUI.WithTitle("GoWind Admin"),
			swaggerUI.WithMemoryData(assets.OpenApiData, "yaml"),
		)
	}

	if authorizer != nil {
		if err = authorizer.ResetPolicies(appViewer.NewSystemViewerContext(ctx.Context())); err != nil {
			return nil, fmt.Errorf("load authorization policies: %w", err)
		}
	}

	return srv, nil
}

// userServiceServerAdapter 将 UserServiceHTTPServer 桥接为 UserServiceServer，
// 使其可以被 RedactedUserServiceServer 包装以实现 HTTP 路径的脱敏。
type userServiceServerAdapter struct {
	adminV1.UnsafeUserServiceServer
	adminV1.UserServiceHTTPServer
}

package main

import (
	dbbootstrap "go-wind-admin/sql/bootstrap"

	"fmt"
	"os"

	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/transport"
	"go-wind-admin/pkg/localdeps/kratos-bootstrap/bootstrap"

	"go-wind-admin/app/admin/service/internal/data"
	"go-wind-admin/app/admin/service/internal/server"
	"go-wind-admin/app/admin/service/internal/service"
	"go-wind-admin/pkg/authorizer"
	"go-wind-admin/pkg/crypto"
	pagination "go-wind-admin/pkg/localdeps/go-crud/pagination"
)

// initApp 手写装配整个应用,是唯一后端构建的依赖注入点。
//
// 装配严格单向分层,自上而下的阅读顺序即依赖方向:
// The wiring is strictly layered; reading top-down follows the dependency direction:
//
//	基础设施 → 仓储层(data) → 认证与鉴权 → 服务层(service) → 传输层(server)
//
// 约定 / Conventions:
//   - 新增仓储/服务:在对应分层小节追加一行构造,并传给下游消费者;漏接由编译器在调用处报错。
//     To add a repo/service: append one constructor line in its layer section, then pass it to
//     downstream consumers; a missing connection is a compile error at the call site.
//   - 持有 cleanup 的资源创建成功后立即注册进 cleanups;任何一步失败,rollback 逆序执行已注册
//     的清理(shutdown 与中途失败共用同一条 LIFO 路径)。
//     Resources owning a cleanup register it immediately; rollback runs them LIFO both on
//     mid-way failure and on shutdown.
//   - 本文件只做构造与传参,不写业务逻辑。
//     Construction and parameter passing only; no business logic in this file.
func initApp(ctx *bootstrap.Context) (*kratos.App, func(), error) {
	// Downstream catalog queries must never run with the upstream development noop authorizer.
	if ctx.GetConfig() == nil || ctx.GetConfig().GetAuthz() == nil || ctx.GetConfig().GetAuthz().GetType() != "casbin" {
		return nil, nil, fmt.Errorf("ani-governance downstream catalog access requires tenant-domain casbin authorization")
	}
	// cleanup 注册表:rollback 时逆序执行。
	// Cleanup registry; rollback runs entries in reverse order.
	var cleanups []func()
	rollback := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	// ═══════════════════════ 一、基础设施 ═══════════════════════

	accessKeyCipher, err := crypto.LoadAccessKeyCipherFromEnv()
	if err != nil {
		return nil, nil, err
	}

	// 游标分页（/api/v1/admin/users 的 limit+cursor）签名密钥：由同一份 AK/SK 主密钥
	// 派生，因此所有加载同一密钥文件的副本派生结果一致，A 副本签发的游标可在
	// B 副本校验。刻意不复用 token 游标密钥（SetTokenSecret）：后者未配置时保留
	// "接受未签名 token"的兼容语义，不能把新游标的签名要求绑在它上面。
	pagination.SetCursorSecret(accessKeyCipher.DeriveSubkey("ani-governance/pagination-cursor/v1"))

	redisClient, cleanupRedis, err := data.NewRedisClient(ctx)
	if err != nil {
		return nil, nil, err
	}
	cleanups = append(cleanups, cleanupRedis)

	entClient, cleanupEnt, err := data.NewEntClient(ctx)
	if err != nil {
		rollback()
		return nil, nil, err
	}
	cleanups = append(cleanups, cleanupEnt)
	if err = dbbootstrap.Check(ctx.Context(), entClient.DB()); err != nil {
		rollback()
		return nil, nil, err
	}
	modeldevConfig, err := data.ModelDevConfigFromEnv()
	if err != nil {
		rollback()
		return nil, nil, err
	}
	var modeldevClient *data.ModelDevClient
	if modeldevConfig.Address != "" {
		client, closeModelDev, err := data.NewModelDevClient(modeldevConfig)
		if err != nil {
			rollback()
			return nil, nil, err
		}
		cleanups = append(cleanups, closeModelDev)
		modeldevClient = client
	}
	modelConfig, err := data.ModelConfigFromEnv()
	if err != nil {
		rollback()
		return nil, nil, err
	}
	var modelClient *data.ModelClient
	if modelConfig.Address != "" {
		client, closeModel, err := data.NewModelClient(modelConfig)
		if err != nil {
			rollback()
			return nil, nil, err
		}
		cleanups = append(cleanups, closeModel)
		modelClient = client
	}

	// 认证基建:令牌缓存 → 认证器 → 访问令牌校验器。
	clientType := data.NewClientType()
	passwordCrypto := data.NewPasswordCrypto()
	captcha := data.NewCaptcha(redisClient)
	userTokenCache := data.NewUserTokenCache(ctx, redisClient)
	authenticator := data.NewAuthenticator(ctx, userTokenCache)
	accessTokenChecker := data.NewTokenChecker(ctx, authenticator, clientType)
	loginRateLimiter := data.NewLoginRateLimiter(ctx, redisClient)
	mfaChallengeCache := data.NewMfaChallengeCache(ctx, redisClient)
	vcodeCache := data.NewVCodeCache(ctx, redisClient)

	// ═══════════════════════ 二、仓储层(internal/data) ═══════════════════════

	// 身份与账号
	// RBAC:权限 → 角色(聚合权限),Api/Menu 为权限挂载点
	// 需先于 userRepo 构造：userRepo 按 role_code 解析角色 ID 时依赖 roleRepo。
	permissionApiRepo := data.NewPermissionApiRepo(ctx, entClient)
	permissionMenuRepo := data.NewPermissionMenuRepo(ctx, entClient)
	rolePermissionRepo := data.NewRolePermissionRepo(ctx, entClient)
	roleOrgUnitRepo := data.NewRoleOrgUnitRepo(ctx, entClient)
	roleFieldPermissionRepo := data.NewRoleFieldPermissionRepo(ctx, entClient)
	roleMetadataRepo := data.NewRoleMetadataRepo(ctx, entClient)
	permissionRepo := data.NewPermissionRepo(ctx, entClient, permissionApiRepo, permissionMenuRepo)
	roleRepo := data.NewRoleRepo(ctx, entClient, rolePermissionRepo, roleOrgUnitRepo, permissionRepo, roleMetadataRepo, roleFieldPermissionRepo)

	userRoleRepo := data.NewUserRoleRepo(ctx, entClient)
	userOrgUnitRepo := data.NewUserOrgUnitRepo(ctx, entClient)
	userPositionRepo := data.NewUserPositionRepo(ctx, entClient)
	membershipRoleRepo := data.NewMembershipRoleRepo(ctx, entClient)
	membershipPositionRepo := data.NewMembershipPositionRepo(ctx, entClient)
	membershipOrgUnitRepo := data.NewMembershipOrgUnitRepo(ctx, entClient)
	membershipRepo := data.NewMembershipRepo(ctx, entClient, membershipRoleRepo, membershipPositionRepo, membershipOrgUnitRepo)
	userRepo := data.NewUserRepo(ctx, entClient, userRoleRepo, userOrgUnitRepo, userPositionRepo, membershipRepo, roleRepo)
	configRepo := data.NewConfigRepo(ctx, entClient, redisClient)
	cleanups = append(cleanups, configRepo.Close)
	userCredentialRepo := data.NewUserCredentialRepo(ctx, entClient, passwordCrypto, configRepo)
	userMfaFactorRepo := data.NewUserMfaFactorRepo(ctx, entClient)
	loginPolicyRepo := data.NewLoginPolicyRepo(ctx, entClient)

	// 组织架构与租户
	orgUnitRepo := data.NewOrgUnitRepo(ctx, entClient, userOrgUnitRepo)
	positionRepo := data.NewPositionRepo(ctx, entClient)
	tenantRepo := data.NewTenantRepo(ctx, entClient)
	tenantUsageRepo := data.NewTenantUsageRepo(ctx, entClient, authenticator)
	modeldevAuthorizationRepo := data.NewModelDevAuthorizationRepo(entClient)
	modeldevAcceptanceRepo := data.NewModelDevAcceptanceRepo(entClient)

	// RBAC：permission/role 相关仓储已提前到「身份与账号」段构造
	// （userRepo 按 role_code 解析角色 ID 依赖 roleRepo）。此处仅补充其余挂载点。
	permissionGroupRepo := data.NewPermissionGroupRepo(ctx, entClient)
	apiRepo := data.NewApiRepo(ctx, entClient)
	menuRepo := data.NewMenuRepo(ctx, entClient)

	// 审计日志
	apiAuditLogRepo := data.NewApiAuditLogRepo(ctx, entClient)
	loginAuditLogRepo := data.NewLoginAuditLogRepo(ctx, entClient)
	operationAuditLogRepo := data.NewOperationAuditLogRepo(ctx, entClient)
	permissionAuditLogRepo := data.NewPermissionAuditLogRepo(ctx, entClient)
	dataAccessAuditLogRepo := data.NewDataAccessAuditLogRepo(ctx, entClient)
	policyEvaluationLogRepo := data.NewPolicyEvaluationLogRepo(ctx, entClient)
	auditLogArchiveRepo := data.NewAuditLogArchiveRepo(ctx, entClient)

	// 字典与多语言
	dictTypeRepo := data.NewDictTypeRepo(ctx, entClient)
	dictEntryI18nRepo := data.NewDictEntryI18nRepo(ctx, entClient)
	dictEntryRepo := data.NewDictEntryRepo(ctx, entClient, dictEntryI18nRepo)
	languageRepo := data.NewLanguageRepo(ctx, entClient)

	// 套餐与计费
	planRepo := data.NewPlanRepo(ctx, entClient)
	planQuotaRepo := data.NewPlanQuotaRepo(ctx, entClient)
	planModuleRepo := data.NewPlanModuleRepo(ctx, entClient)
	quotaAdminRepo := data.NewQuotaAdminRepo(ctx, entClient)

	// 任务 / 运维观测
	taskRepo := data.NewTaskRepo(ctx, entClient)
	redisCacheMonitorRepo := data.NewRedisCacheMonitorRepo(ctx, redisClient)
	serverMonitorRepo := data.NewServerMonitorRepo(ctx, entClient)
	notificationChannelRepo := data.NewNotificationChannelRepo(ctx, entClient)
	dashboardRepo := data.NewDashboardRepo(ctx, entClient)

	// 站内信
	internalMessageRepo := data.NewInternalMessageRepo(ctx, entClient)
	internalMessageCategoryRepo := data.NewInternalMessageCategoryRepo(ctx, entClient)
	internalMessageRecipientRepo := data.NewInternalMessageRecipientRepo(ctx, entClient)

	// ── register:repo ── 新模块仓储在此行后注册(make register 工具锚点,勿删)
	accessKeyRepo := data.NewAccessKeyRepo(ctx, entClient, accessKeyCipher)

	// 配额账本与持久化转发（QUOTA-GPU-LOCAL-01）
	quotaLedgerRepo := data.NewQuotaLedgerRepo(ctx, entClient)
	quotaRegistry := service.NewQuotaAdapterRegistry()
	inferenceConfig, err := data.InferenceConfigFromEnv()
	if err != nil {
		rollback()
		return nil, nil, err
	}
	var inferenceClient service.InferenceOwnerClient
	if inferenceConfig.Address != "" {
		client, closeInference, err := data.NewInferenceClient(inferenceConfig)
		if err != nil {
			rollback()
			return nil, nil, err
		}
		cleanups = append(cleanups, closeInference)
		inferenceClient = client
	}

	instanceOwner, computeOwner, closeResource, err := data.NewResourceClientsFromEnv()
	if err != nil {
		rollback()
		return nil, nil, err
	}
	cleanups = append(cleanups, closeResource)
	inferenceBinding := service.NewInferenceComputeBinding(inferenceClient, data.NewInferenceAuthorizationRepo(entClient), modelClient, computeOwner)
	observabilityClient, closeObservability, err := data.NewObservabilityClientFromEnv()
	if err != nil {
		rollback()
		return nil, nil, err
	}
	cleanups = append(cleanups, closeObservability)
	var instanceClient service.InstanceOwnerClient
	if instanceOwner != nil {
		instanceClient = instanceOwner
	}
	instanceCluster, err := data.NewInstanceSchedulingClusterFromEnv()
	if err != nil {
		rollback()
		return nil, nil, err
	}
	instanceScheduling := service.NewInstanceSchedulingPolicy(configRepo, tenantRepo, instanceCluster, os.Getenv("ANI_INSTANCE_CLUSTER_ID"))
	instanceBinding := service.NewInstanceGpuBinding(instanceClient, data.NewInstanceAuthorizationRepo(entClient), os.Getenv("ANI_INSTANCE_CLUSTER_ID"), instanceScheduling)
	var vmClient service.VMOwnerClient
	if instanceOwner != nil {
		vmClient = instanceOwner.VM()
	}
	vmGuestCipher, err := crypto.NewVMGuestCipher(accessKeyCipher)
	if err != nil {
		rollback()
		return nil, nil, err
	}
	vmBinding := service.NewVMBinding(vmClient, data.NewVMAuthorizationRepo(entClient), os.Getenv("ANI_INSTANCE_CLUSTER_ID"), vmGuestCipher)
	var dispatchInstance *service.InstanceGpuBinding
	var dispatchVM service.QuotaDispatchAdapter
	if instanceClient != nil && data.InstanceModuleRegistered() {
		dispatchInstance = instanceBinding
	}
	if vmClient != nil && data.VMModuleRegistered() {
		dispatchVM = vmBinding
	}
	if dispatchInstance != nil || dispatchVM != nil {
		if err = quotaRegistry.Register(service.NewResourceDispatchAdapter(dispatchInstance, dispatchVM)); err != nil {
			rollback()
			return nil, nil, err
		}
	}
	if inferenceClient != nil && data.InferenceModuleRegistered() {
		if err = quotaRegistry.Register(inferenceBinding); err != nil {
			rollback()
			return nil, nil, err
		}
	}
	quotaAdminRepo.SetExecutionCapabilities(quotaRegistry)
	quotaWorker := service.NewQuotaDispatchWorker(ctx, quotaLedgerRepo, quotaRegistry)

	// ═══════════════════════ 三、认证与鉴权 ═══════════════════════

	tenantAccessChecker := data.NewTenantAccessCheckerImpl(ctx, entClient)
	authorizerProvider := data.NewAuthorizerProvider(ctx, roleRepo, apiRepo)
	authz := authorizer.NewAuthorizer(ctx, authorizerProvider)

	// ═══════════════════════ 四、服务层(internal/service) ═══════════════════════

	// 认证与登录策略
	authenticationService := service.NewAuthenticationService(ctx, userRepo, userCredentialRepo, roleRepo, tenantRepo, membershipRepo, orgUnitRepo, roleOrgUnitRepo, roleFieldPermissionRepo, permissionRepo, authenticator, clientType, captcha, loginRateLimiter, loginPolicyRepo, userMfaFactorRepo, mfaChallengeCache, vcodeCache, notificationChannelRepo)
	mfaService := service.NewMfaService(ctx, userMfaFactorRepo, mfaChallengeCache, authenticator, loginRateLimiter, userRepo)
	loginPolicyService := service.NewLoginPolicyService(ctx, loginPolicyRepo)

	// 身份与组织
	userService := service.NewUserService(ctx, userRepo, roleRepo, userCredentialRepo, positionRepo, orgUnitRepo, tenantRepo, membershipRepo, authenticator, notificationChannelRepo)
	userProfileService := service.NewUserProfileService(ctx, userRepo, roleRepo, userCredentialRepo, authenticator, notificationChannelRepo, vcodeCache)
	positionService := service.NewPositionService(ctx, positionRepo, orgUnitRepo)
	orgUnitService := service.NewOrgUnitService(ctx, orgUnitRepo, userRepo)

	// RBAC
	roleService := service.NewRoleService(ctx, authz, roleRepo, tenantRepo)
	menuService := service.NewMenuService(ctx, menuRepo)
	apiService := service.NewApiService(ctx, apiRepo, authz)
	permissionService := service.NewPermissionService(ctx, permissionRepo, permissionGroupRepo, menuRepo, apiRepo, roleRepo, authz)
	permissionGroupService := service.NewPermissionGroupService(ctx, permissionGroupRepo, permissionRepo)

	// 租户与套餐
	tenantService := service.NewTenantService(ctx, tenantRepo, tenantUsageRepo, userRepo, userCredentialRepo, roleRepo, authz, notificationChannelRepo)
	planService := service.NewPlanService(ctx, planRepo)
	planQuotaService := service.NewPlanQuotaService(ctx, planQuotaRepo)
	planModuleService := service.NewPlanModuleService(ctx, planModuleRepo)
	quotaAdminService := service.NewQuotaAdminService(ctx, quotaAdminRepo)

	// 字典与多语言
	dictTypeService := service.NewDictTypeService(ctx, dictTypeRepo)
	dictEntryService := service.NewDictEntryService(ctx, dictEntryRepo)
	languageService := service.NewLanguageService(ctx, languageRepo)

	// 任务
	taskService := service.NewTaskService(ctx, taskRepo, userRepo, tenantUsageRepo, auditLogArchiveRepo)

	// 审计日志
	loginAuditLogService := service.NewLoginAuditLogService(ctx, loginAuditLogRepo)
	apiAuditLogService := service.NewApiAuditLogService(ctx, apiAuditLogRepo, apiRepo)
	operationAuditLogService := service.NewOperationAuditLogService(ctx, operationAuditLogRepo)
	dataAccessAuditLogService := service.NewDataAccessAuditLogService(ctx, dataAccessAuditLogRepo)
	permissionAuditLogService := service.NewPermissionAuditLogService(ctx, permissionAuditLogRepo)
	policyEvaluationLogService := service.NewPolicyEvaluationLogService(ctx, policyEvaluationLogRepo)

	// 运维观测与门户
	redisCacheMonitorService := service.NewRedisCacheMonitorService(ctx, redisCacheMonitorRepo)
	serverMonitorService := service.NewServerMonitorService(ctx, serverMonitorRepo)
	notificationChannelService := service.NewNotificationChannelService(ctx, notificationChannelRepo)
	onlineSessionService := service.NewOnlineSessionService(ctx, authenticator)
	dashboardService := service.NewDashboardService(ctx, dashboardRepo)
	adminPortalService := service.NewAdminPortalService(ctx, menuRepo, roleRepo, userRepo, permissionRepo, planModuleRepo, tenantRepo)

	// 站内信
	internalMessageService := service.NewInternalMessageService(ctx, internalMessageRepo, internalMessageCategoryRepo, internalMessageRecipientRepo, userRepo, authenticator, clientType)
	internalMessageCategoryService := service.NewInternalMessageCategoryService(ctx, internalMessageCategoryRepo)
	internalMessageRecipientService := service.NewInternalMessageRecipientService(ctx, internalMessageRepo, internalMessageRecipientRepo)

	// ── register:service ── 新模块服务在此行后注册(make register 工具锚点,勿删)
	accessKeyService := service.NewAccessKeyService(ctx, accessKeyRepo)
	configService := service.NewConfigService(ctx, configRepo)
	networkConfig, err := data.NetworkConfigFromEnv()
	if err != nil {
		rollback()
		return nil, nil, err
	}
	var networkClient service.NetworkTenantClient
	var platformNetworkClient service.NetworkPlatformClient
	if networkConfig.Address != "" {
		client, closeNetwork, err := data.NewNetworkClient(networkConfig)
		if err != nil {
			rollback()
			return nil, nil, err
		}
		cleanups = append(cleanups, closeNetwork)
		networkClient = client
		platformNetworkClient = client
	}
	networkService := service.NewNetworkService(networkClient, tenantRepo)
	platformNetworkService := service.NewPlatformNetworkService(platformNetworkClient)
	imageConfig, err := data.ImageConfigFromEnv()
	if err != nil {
		rollback()
		return nil, nil, err
	}
	var imageClient *data.ImageClient
	if imageConfig.Address != "" {
		client, closeImage, err := data.NewImageClient(imageConfig)
		if err != nil {
			rollback()
			return nil, nil, err
		}
		cleanups = append(cleanups, closeImage)
		imageClient = client
	}
	imageService := service.NewImageService(imageClient, tenantRepo)
	storageConfig, err := data.StorageConfigFromEnv()
	if err != nil {
		rollback()
		return nil, nil, err
	}
	var storageClient *data.StorageClient
	if storageConfig.Address != "" {
		client, closeStorage, err := data.NewStorageClient(storageConfig)
		if err != nil {
			rollback()
			return nil, nil, err
		}
		cleanups = append(cleanups, closeStorage)
		storageClient = client
	}
	storageService := service.NewStorageService(storageClient, tenantRepo)
	acceleratorConfig, err := data.AcceleratorConfigFromEnv()
	if err != nil {
		rollback()
		return nil, nil, err
	}
	var acceleratorClient *data.AcceleratorClient
	if acceleratorConfig.Address != "" {
		client, closeAccelerator, err := data.NewAcceleratorClient(acceleratorConfig)
		if err != nil {
			rollback()
			return nil, nil, err
		}
		cleanups = append(cleanups, closeAccelerator)
		acceleratorClient = client
	}
	gpuBffLedger := service.NewGpuBffLedgerBridge(quotaLedgerRepo, quotaAdminRepo)
	acceleratorService := service.NewAcceleratorService(acceleratorClient, tenantRepo, gpuBffLedger, gpuBffLedger, quotaAdminRepo, quotaRegistry, authz.Engine())
	var gpuPlans service.GpuPlanResolver
	if acceleratorClient != nil {
		gpuPlans = acceleratorClient.Catalog
	}
	inferenceAcceptance, err := service.NewGpuAcceptance(quotaLedgerRepo, quotaRegistry, tenantRepo, gpuPlans, inferenceBinding, quotaWorker)
	if err != nil {
		rollback()
		return nil, nil, err
	}
	inferenceService := service.NewInferenceServiceWithOwnerAndModel(inferenceAcceptance, inferenceClient, tenantRepo, modelClient)
	metricsClient, err := data.NewInferenceMetricsClient(os.Getenv("ANI_INFERENCE_PROMETHEUS_URL"), os.Getenv("ANI_INFERENCE_RUNTIME_NAMESPACE"))
	if err != nil {
		return nil, nil, err
	}
	var metricsReader service.InferenceMetricsReader
	if metricsClient != nil {
		metricsReader = metricsClient
	}
	if err := inferenceService.ConfigureDiagnostics(os.Getenv("ANI_INFERENCE_INVOCATION_ENDPOINT"), metricsReader); err != nil {
		return nil, nil, err
	}

	modelService := service.NewModelService(modelClient, tenantRepo, imageClient)
	instanceAcceptance, err := service.NewGpuAcceptance(quotaLedgerRepo, quotaRegistry, tenantRepo, gpuPlans, instanceBinding, quotaWorker)
	if err != nil {
		rollback()
		return nil, nil, err
	}
	instanceSession, closeInstanceSession, err := data.NewInstanceSessionClientFromEnv()
	if err != nil {
		rollback()
		return nil, nil, err
	}
	cleanups = append(cleanups, closeInstanceSession)
	var instanceSessions service.InstanceSessionClient
	if instanceSession != nil {
		instanceSessions = instanceSession
	}
	instanceCatalog := service.InstanceCatalogConfig{ClusterID: os.Getenv("ANI_INSTANCE_CLUSTER_ID"), Cluster: instanceCluster, Settings: instanceScheduling}
	computeCatalog := service.ComputeCatalogConfig{ClusterID: os.Getenv("ANI_INSTANCE_CLUSTER_ID"), Resolver: tenantRepo, Authorization: data.NewComputeCatalogAuthorizationRepo(entClient)}
	var computeClient service.ComputePresetOwner
	if computeOwner != nil {
		computeClient = computeOwner
		computeCatalog.Compute = computeOwner
	}
	if observabilityClient != nil {
		instanceCatalog.Monitoring = observabilityClient
	}
	if acceleratorClient != nil {
		computeCatalog.Admin = acceleratorClient.Admin
		computeCatalog.Catalog = acceleratorClient.Catalog
	}
	instanceService := service.NewInstanceService(instanceAcceptance, instanceBinding, tenantRepo, quotaLedgerRepo, instanceSessions, instanceCatalog)
	overviewService := service.NewOverviewService(
		ctx.NewLoggerHelper("overview/service/admin-service"),
		instanceService,
		inferenceService,
		modelService,
		data.NewInstanceAuthorizationRepo(entClient),
	)
	vmAcceptance, err := service.NewGpuAcceptance(quotaLedgerRepo, quotaRegistry, tenantRepo, nil, vmBinding, quotaWorker)
	if err != nil {
		rollback()
		return nil, nil, err
	}
	var vmSessions service.VMSessionClient
	if instanceSession != nil {
		vmSessions = instanceSession
	}
	vmService := service.NewVMService(vmAcceptance, vmBinding, tenantRepo, quotaLedgerRepo, vmSessions)
	computeService := service.NewComputePresetService(computeClient, computeCatalog)
	modeldevGPU := service.ModelDevGPUAdmission{Eligibility: data.NewGpuUseAuthorizationRepo(entClient)}
	if acceleratorClient != nil {
		modeldevGPU.Plans = acceleratorClient.Catalog
	}
	modeldevService := service.NewModelDevService(modeldevAuthorizationRepo, tenantRepo, modeldevAcceptanceRepo, modeldevClient, computeOwner, modeldevGPU)
	if observabilityClient != nil {
		modeldevService.SetMonitoring(observabilityClient)
	}

	// ═══════════════════════ 五、传输层(internal/server) ═══════════════════════

	// 内部退额 mTLS listener（QUOTA-03）：默认 disabled；enabled 缺凭据启动失败。
	quotaInternalCfg := server.QuotaInternalConfigFromEnv()
	quotaInternalServer, err := server.NewQuotaInternalServer(quotaInternalCfg, quotaLedgerRepo)
	if err != nil {
		rollback()
		return nil, nil, err
	}
	if quotaInternalServer != nil {
		cleanups = append(cleanups, func() { _ = quotaInternalServer.Stop(ctx.Context()) })
	}

	restMiddlewares := server.NewRestMiddleware(ctx, accessTokenChecker, accessKeyRepo, tenantAccessChecker, authz,
		apiAuditLogRepo, loginAuditLogRepo, operationAuditLogRepo, permissionAuditLogRepo, dataAccessAuditLogRepo, policyEvaluationLogRepo)

	restServer, err := server.NewRestServer(ctx, restMiddlewares, authz,
		authenticationService, mfaService, loginPolicyService,
		adminPortalService, taskService,
		dictTypeService, dictEntryService, languageService,
		tenantService, planService, planQuotaService, quotaAdminService, planModuleService,
		userService, userProfileService, roleService, positionService, orgUnitService,
		menuService, apiService, permissionService, permissionGroupService,
		permissionAuditLogService, policyEvaluationLogService,
		loginAuditLogService, apiAuditLogService, operationAuditLogService, dataAccessAuditLogService,
		redisCacheMonitorService, serverMonitorService, notificationChannelService,
		onlineSessionService, dashboardService,
		internalMessageService, internalMessageCategoryService, internalMessageRecipientService,
		// register:rest-arg ── 新模块服务实参在此行后追加(make register 工具锚点,勿删)
		accessKeyService,
		configService,
		networkService,
		platformNetworkService,
		acceleratorService,
		imageService,
		storageService,
		modeldevService,
		inferenceService,
		modelService,
		computeService,
		overviewService,
		instanceService,
	)
	if err != nil {
		rollback()
		return nil, nil, err
	}
	if restServer != nil {
		server.RegisterVMHTTPServer(restServer, vmService)
	}
	asynqServer, err := server.NewAsynqServer(ctx, taskService, internalMessageService)
	if err != nil {
		rollback()
		return nil, nil, err
	}

	sseServer := server.NewSseServer(ctx, internalMessageService)

	// disabled 时 quotaInternalServer 为 nil：typed-nil 不能进入 transport.Server
	// 接口切片，否则 Start 空指针崩溃。
	extraServers := []transport.Server{quotaWorker}
	if modeldevClient != nil {
		extraServers = append(extraServers, service.NewModelDevDispatchWorker(ctx, modeldevAcceptanceRepo, modeldevClient))
	}
	if acceleratorClient != nil {
		gpuSyncLog := ctx.NewLoggerHelper("gpu-usage-sync")
		gpuSync := service.NewGpuUsageSyncWorker(quotaLedgerRepo, acceleratorClient, func(err error) {
			gpuSyncLog.Errorf(ctx.Context(), "GPU usage projection recovery failed: %v", err)
		})
		extraServers = append(extraServers, gpuSync)
	}
	if quotaInternalServer != nil {
		extraServers = append(extraServers, quotaInternalServer)
	}
	return newApp(ctx, restServer, asynqServer, sseServer, extraServers...), rollback, nil
}

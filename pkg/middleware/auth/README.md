# 认证与授权上下文中间件

[Server](auth.go) 校验请求凭据，建立可信 `Principal`、Ent viewer 和授权上下文，并可检查租户状态与套餐模块。实际 HTTP 链由 [rest_server.go](../../../app/admin/service/internal/server/rest_server.go) 装配；公开登录、刷新、邀请等接口通过该文件的 selector 白名单另行处理。

## Bearer / JWT

HTTP Bearer 分支要求恰好一个 `Authorization` header，并从 Kratos metadata 提取 Bearer token。必须通过 `WithAccessTokenChecker` 或 `WithAccessTokenCheckerFromFuncs` 配置有效性检查器；缺失检查器、校验失败、空 payload 或零用户 ID 都拒绝请求。

当前主服务的 [TokenChecker](../../../app/admin/service/internal/data/token_checker.go) 委托 [Authenticator](../../../app/admin/service/internal/data/authenticator.go) 校验 JWT 签名、过期时间和 payload，再核对 Redis 中的访问令牌及 JTI 黑名单。`Server` 调用检查器时固定传 `skipRedis=false`。JWT 签名用于验证令牌真实性；Redis 记录与黑名单用于会话撤销和失效判断，二者分别承担职责。

当前 API 定义于 [options.go](options.go)：`WithAccessTokenCheckerFromFuncs(valid, blocker)` 使用 `NewAccessTokenCheckerFromFuncs` 组合接口。`Server` 实际调用的是 `IsValidAccessToken`，不会单独调用组合对象的 `IsBlockedAccessToken`；自定义 `valid` 函数须包含所需的撤销检查。没有独立的 `WithAccessTokenBlocker` 开关。

## AK/SK

HTTP 请求带任一签名 header 时进入 AK 分支，和 `Authorization` 混用会被拒绝。该分支需要 `WithSigningKeyStore`，验证固定 header、时间窗口、请求规范化及 HMAC 签名；当前仅允许明确登记的 Network/Image 操作。存储缺失或不可用不会放行。

AK 通过后主体类型为 `api_key`，主体 ID 是 Access Key ID，不能当作用户 ID；租户、角色和 `Actor()` 均从校验后的主体获得。详细报文、允许范围与错误合同见 [AK/SK 签名合同](../../../docs/contracts/aksk-signature.md)，不在本页另建一套协议。

## 可信身份与授权

业务代码通过 [PrincipalFromContext](principal.go) 读取用户或机器主体；[FromContext](context.go) 保留旧的用户 claims 接口。不能从公网 tenant、actor 或角色字段建立可信身份。AK 的兼容 claims 没有用户 ID，需使用 `Principal` 区分主体。

`WithTenantAccessChecker` 配置租户闸门；当前主服务在认证后、业务处理前检查非零租户的状态、到期策略和套餐模块。自助类路由（权限码权威表 `scope_kind` 为 `self` / `tenant-self`，如 `GET /api/v1/admin/me`、`GET /api/v1/me/quota-accounts`）豁免套餐模块白名单——这类路由读写的是操作者自己或个人所属租户的资源，不属于可售模块；状态与到期检查仍前置生效，业务授权仍由下游 Casbin 强制。判定依据见 [tenant_access_checker.go](../../../app/admin/service/internal/data/tenant_access_checker.go)。`WithInjectEnt`、`WithInjectMetadata`、`WithInjectOperatorId` 和 `WithInjectTenantId` 控制上下文或请求注入；默认注入 Ent viewer 与 metadata，默认不改请求操作人/租户字段。主服务显式关闭 metadata 注入，出站身份另按调用合同重建。

`WithEnableAuthority` 控制是否准备授权 claims，不等于执行策略判定。[utils.go](utils.go) 对 HTTP 使用路由模板与方法，对其他传输使用 operation 与 `ANY`，租户 domain 来自可信 claims。主服务随后通过 `authz.Server` 的 Casbin 引擎执行授权；认证成功不等于已获业务权限。

## 验证与来源

正常和拒绝路径见 [auth_test.go](auth_test.go)、[signature_test.go](signature_test.go)、[authorization_domain_test.go](authorization_domain_test.go) 及本目录请求策略测试。包测试入口为仓库根目录的 `go test ./pkg/middleware/auth`；它不代替真实 JWT/Redis、Casbin 或下游联调验收。

项目来源、原许可和本地接管说明见 [THIRD_PARTY_NOTICES.md](../../../THIRD_PARTY_NOTICES.md)。

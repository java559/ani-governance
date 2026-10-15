# 管理员开通：立即激活与邮件邀请

Governance 当前提供两种开通方式，复用现有用户、角色、租户、凭证和 SMTP。以下描述实现行为；目标环境的邮件与开通链路仍须单独验收。

## 实现行为与验证范围

1. `POST /api/v1/admin/users` 与 `POST /api/v1/admin/tenants:with-admin` 增加 `activationMode`：默认 `IMMEDIATE`，可选 `EMAIL_INVITATION`。
2. 邀请使用现有凭证表 `activate_token_*` 字段：仅保存随机令牌 SHA-256，24 小时有效；账号为 PENDING，用户名凭证禁用，不设置默认密码。
3. Governance 提供简洁设密页与 `POST /api/v1/auth/invitations/accept`；令牌校验、设密、启用凭证、确认邮箱和激活账号在同一事务完成。
4. 定向验证默认模式无 SMTP、平台/租户邀请、错误及过期令牌、单次消费、密码策略、租户边界和失败回滚。

## 配置和使用

- 立即激活不读取邀请配置，也不要求 SMTP；密码沿用现有接口约定。
- 邮件邀请要求 `data.email`（建租户接口为 `user.email`），不接受预设密码。
- `ANI_INVITATION_BASE_URL` 是 Governance 对外 origin，例如 `https://admin.example.com`。仅邀请模式读取；本机验收允许 `http://localhost:<port>` 或回环 IP。
- 配置并启用现有 EMAIL 通知渠道。邀请邮件沿用第一个启用 EMAIL 渠道。
- 邮件链接指向 `/api/v1/auth/invitations/accept#<token>`。片段不会随页面 GET 请求发送给服务器；页面以 POST 提交令牌和新密码，不自动登录。

`activationMode` 是每次创建请求的顶层参数。省略或传 `IMMEDIATE` 就不发邮件；需要邀请时传 `EMAIL_INVITATION`。两个现有创建入口都支持，不需要另调签发接口。

例如，创建一个受邀用户（`123` 须替换为目标范围内已有的角色 ID）：

```json
{
  "activationMode": "EMAIL_INVITATION",
  "data": {
    "username": "alice",
    "email": "alice@example.com",
    "roleIds": [123]
  }
}
```

建租户时在原 `tenant` / `user` 请求外层加同一参数，管理员角色仍由服务端从现有租户模板生成。受邀账号在设密前不能用默认密码登录；设密遵守现有密码策略。页面不会替用户选择平台或租户登录入口，成功后提示返回原入口登录。

## 最小同步发送边界

邀请账号、角色关联和禁用凭证在一个数据库事务中创建，SMTP 发送成功后提交。发送失败返回错误并回滚，可以修好渠道后重试原创建请求。SMTP 与数据库不能原子提交：若邮件已受理但提交失败，已发出的链接无效，调用方重试创建；本实现不声称可靠异步投递。邮件发送有超时，不新增队列或后台重试。

## 数据与接口升级

复用现有激活字段，无新增表或 schema 变更。已有数据库必须已具备仓库当前 schema 的 `activate_token_hash`、`activate_token_expires_at`、`activate_token_used_at` 字段。

创建接口路径、权限和套餐关系不变。新增接受接口为令牌认证的公开入口，不授予用户管理权限；设密页不消费令牌。已有数据库升级时使用 `admin sync-apis --dry-run` 预览，再运行 `admin sync-apis`。同步保留已有 ID 和权限关联，不自动授权。首次部署及套餐绑定见 [部署与初始化流程](deployment.md)。

上线时部署本版本，针对目标数据库执行上述增量登记。需要邮件时配置 `ANI_INVITATION_BASE_URL` 和现有 EMAIL 渠道；只用立即激活时无需这两项配置。

套餐仍按现有开通请求填写；账号激活不代表套餐或业务资源已经配置。

## 验证记录

2026-09-21，Ubuntu 独立目录 `/home/ubuntu/Workspace/.codex-runs/onboarding-20260921`；Go 1.26.7、buf 1.50.0、protoc-gen-go 1.36.12。未修改远端已有工作区。

- pass：SQLite 与隔离 PostgreSQL 18 上，两类创建入口的立即激活和邀请设密；接受后可通过原用户名凭证校验，用户和角色归属正确。
- pass：SMTP 测试接收端实际收到链接；SMTP 拒绝时用户、凭证、角色关联和新租户全部回滚；SMTP 问候阶段阻塞可被超时中断。
- pass：错误/过期令牌、弱密码、用户停用、邮箱变更、身份租户不匹配、凭证写入冲突均不能完成激活；失败不消费有效令牌。
- pass：PostgreSQL 上 8 个并发接受请求仅 1 个成功；重复使用被拒绝。
- pass：公开接受接口可免登录到达参数校验；用户创建仍要求认证；接受请求的密码、令牌和 Referer 不进入 API 请求体审计。
- pass：PostgreSQL 增量 API 登记连续执行两次，旧接口 ID 不变，新接口不重复；原平台/租户登录适配与创建入口定向回归通过。
- pass：服务入口 `go build ./app/admin/service/cmd/server`。
- not_verified：真实邮件供应商投递及收件箱送达、生产环境部署。测试使用回环 SMTP 接收端，未向真实邮箱发送。

本次没有数据库结构迁移。回退服务二进制不会删除已创建账号；旧版本不提供接受入口，因此回退前发出的未使用邀请在旧版本上不可接受。

定向复验：

```sh
go test ./app/admin/service/internal/service ./app/admin/service/internal/data ./app/admin/service/internal/server ./pkg/mailer ./pkg/middleware/logging -run 'Test(Onboarding|Invitation|SendMail)' -count=1
# ANI_ONBOARDING_TEST_DSN 指向可丢弃的 PostgreSQL 测试库；每个用例单独建 schema 并清理。
ANI_ONBOARDING_TEST_DSN="$TEST_DATABASE_URL" go test ./app/admin/service/internal/service -run TestOnboarding -count=1 -v
```

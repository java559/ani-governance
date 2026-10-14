# 列表分页规范

本文件是 Governance 分页要求的唯一正文，承接已接受的 `LIST-CURSOR-01` 与 `LIST-CURSOR-02`，并区分要求、当前实现和待迁移范围。[接口集成登记](../interface-integration-register.md#list-cursor-01) 保留批次、验收与遗留记录；修改列表、筛选或排序前，先核对本文和该接口的领域合同。

## 适用范围与强制要求

已采用 `limit/cursor` 的 Governance 自管列表，以及明确纳入同类分页改造批次的列表，遵守本节。新增列表先登记采用的分页协议、默认值、上限、排序、过滤及授权范围；复用下面的游标基线时完整落实其要求。本文不决定将全部历史列表统一迁移，也不将已冻结的 Network、ModelDev、Accelerator 或 Quota 协议改名。

仅修改既有列表的业务字段、筛选或鉴权时，保持已接受的窗口协议；若需要改变协议，先确定具体接口、兼容与迁移批次。共享 `pagination.PagingRequest` 出现某个字段，不代表每个 RPC 都实现该字段的语义。

### 请求与响应

- 游标列表请求使用 `limit` 和不透明 `cursor`。首次不传 cursor，后续原样回传上一页 `next_cursor`，并保留相同筛选与排序；客户端不解析或改写游标。
- Governance 自管游标基线的 limit 默认 20，合法范围 1～100。非法、零或越界值返回 400，不能静默截断。领域专用协议沿用其已接受的默认值与上限。
- cursor 与 `page/pageSize`、`offset`、`token` 互斥，冲突返回 400；显式旧页码兼容属于独立分支。`noPaging` 的支持及冲突处理必须在该接口登记，不能用它绕过游标参数校验。
- 游标响应为 `{items, next_cursor, total}`。只在确实存在下一页时给出非空 next_cursor；末页为空。HTTP 编码器可能省略空字段或输出空串，调用者以非空值判断是否继续。
- total 是当前可信租户、当前权限及相同筛选条件下的匹配总数，不受 cursor、limit、page 或 offset 窗口影响，不能用本页 items 长度代替。空结果 total 为零；Proto int64/uint64 的 HTTP JSON 值为十进制字符串。
- 未承诺 total 的领域列表保持原响应，不伪造零或页内计数。新增 total 时同时实现同租户、同权限、同筛选的计数路径并验收。

### 过滤、身份与授权

每次翻页重新经过该接口当前的认证、权限、租户状态和套餐检查，分页凭证不代替授权。租户与身份来自认证上下文和服务端可信 viewer/Principal；入站租户字段只在接口明确允许时作为受约束的筛选条件，不能切换调用者身份。

列表查询和 total 查询使用同一组租户、权限与业务过滤条件。关系筛选也必须在可信租户内解析；ID 使用明确的精确条件，文本搜索使用 contains，不能将 ID 放入模糊搜索。筛选值通过参数化查询进入数据库，HTTP 参数白名单与未知、重复或歧义参数的处理按该接口明确合同落实。

### 排序与游标签名

- 使用稳定的键集排序：确定主排序列及方向，追加唯一 ID 兜底，排序与续页谓词保持一致。允许排序列、NULL 位置及值类型由资源声明，不直接采用任意客户端列名。
- 可空列区分 SQL NULL、空串和零值。游标取值来自数据库实体；时间、数字还原为数据库可比较的类型。`fieldMask` 只裁剪输出，生成游标需要的排序列与 ID 可额外读取，输出前按原 mask 清理。
- 签名游标绑定资源、可信上下文类型、可信租户及原始筛选与有效排序的指纹。指纹在业务扩展关系筛选之前计算，防止内部展开的 ID 集合改变客户端筛选的含义。
- 使用受控密钥签名。签名失败、格式/版本不支持、超长或绑定不匹配返回 400，不降级为首页；缺可信 viewer 或签名配置时 fail-closed。所有副本使用兼容的密钥配置，轮换后的旧游标失效与客户端重启首页一起处理。
- 本仓 `c1` 游标与历史 token 格式独立，不能互换，也不能继承旧 token 在缺密钥时的未签名兼容行为。

稳定键集排序保证固定结果集中的续页顺序，不自动提供跨请求快照。并发写入、删除、排序值或授权变化可能改变后续页与 total；需要快照语义的接口必须另行明确一致性合同并验证，不能从游标签名推导出该能力。

## 当前实现

以下按本仓源码核对；历史运行证据仍以登记中的具名批次为准，不代表本次重新执行验收。

<a id="users-current"></a>

### users：已接入游标，保留显式旧窗口

`GET /api/v1/admin/users` 使用 [PagingRequest](../../api/localdeps/pagination/pagination/v1/pagination.proto)，返回 [ListUserResponse](../../api/protos/identity/service/v1/user.proto)。[UserService.List](../../app/admin/service/internal/service/user_service.go) 委托 [userRepo.List](../../app/admin/service/internal/data/user_repo.go)，游标细节由 [user_cursor.go](../../app/admin/service/internal/data/user_cursor.go) 和本地 [ListWithCursor](../../pkg/localdeps/go-crud/entgo/cursor.go) 实现。

普通分页请求未显式提供 `page`、`pageSize`、`offset` 或 `token` 时进入游标分支，limit 默认 20、范围 1～100；存在非空 cursor 且同时提供上述窗口字段或 `noPaging` 时报错。显式提供旧窗口字段时仍走 `ListWithPaging`，例如 `?page=1&pageSize=20`；该分支返回 items/total，不具备新游标续页语义。

users 的默认排序为 ID 降序，等价于 `orderBy=["-id"]`。客户端 `sorting/orderBy` 只接受一个有效主排序字段并追加同方向唯一 ID。允许列为 `id/tenant_id/username/nickname/realname/email/mobile/status/gender/created_at/updated_at/last_login_at/locked_until`；降序 NULL 在前、升序 NULL 在后，键集谓词分别处理 NULL 区段。具体字段类型和取值以 `userCursorSortColumns` 为准。

[cursor codec](../../pkg/localdeps/go-crud/pagination/cursor_codec.go) 使用 HMAC-SHA256，格式为 `c1.<base64url(payload)>.<hex(hmac)>`；base64 载荷长度上限 2048 字节。绑定资源为 `identity.user`，上下文为 tenant/platform/system，平台及系统上下文租户绑定值为 0，租户上下文取服务端 viewer。当前绑定不包含具体用户 ID 或角色版本；每次请求仍须重新授权。

[启动装配](../../app/admin/service/cmd/server/wiring_ent.go) 从 `ANI_ACCESS_KEY_ENCRYPTION_KEY_FILE` 加载的 AK/SK 主密钥，以 `ani-governance/pagination-cursor/v1` 域分离派生游标签名子密钥，并设置 `SetCursorSecret`。所有副本需使用同一主密钥；轮换会使旧游标失效。它不复用 `SetTokenSecret`。

total 为 uint64，HTTP JSON 为字符串。查询侧 Ent TenantPrivacy 继续施加租户边界；未新增路由或权限点。当前页查询与 Count 分别执行，没有显式绑定同一个数据库快照，也没有跨页快照令牌。

<a id="existing-protocols"></a>

### 其他列表：保留各自协议

| 范围 | 当前协议与边界 | 依据 |
| --- | --- | --- |
| roles 与 api-keys | 已接入与 users 相同的 limit/cursor 基线（`{items, next_cursor, total}`）。roles 默认 `sort_order` 升序、api-keys 默认 `id` 降序，均追加唯一 ID 兜底；可排序列各自白名单，api-keys 显式排除 `secret_ciphertext` | LIST-CURSOR-02、[role_cursor.go](../../app/admin/service/internal/data/role_cursor.go)、[access_key_cursor.go](../../app/admin/service/internal/data/access_key_cursor.go) |
| 其他使用 PagingRequest 的 admin 列表 | 各接口现有 page/pageSize、offset 或 token 分支保留。共享 cursor 字段可能出现在 OpenAPI，但本批未接入游标语义；不能据此宣称已迁移 | [接口集成登记](../interface-integration-register.md#list-cursor-01)、各 Service/Repo |
| Network 的 VPC、EIP、Subnet、LB 列表 | 保留 limit/cursor 与 items/next_cursor/total。total 为 int64 字符串，遵守同可信租户、权限和筛选的计数要求；HTTP 对显式 limit 接受规范十进制 1～100 | [Network 策略](../../pkg/middleware/auth/network_policy.go)、[Network BFF](../../app/admin/service/internal/service/network_service.go) |
| 平台网络 VLAN、网关、Public/Intranet 池 | limit/cursor 与 items/next_cursor/total，沿用 VPC 默认 20、1～100 和下游签名游标；节点网卡清单不分页 | [平台网络合同](platform-network.md) |
| Image registrations | 保留领域已接受的 limit/cursor 与 items/next_cursor/total，按 Image 策略限制参数并透传已验证下游结果；不强制改为 users 的 c1 编码 | [Image 策略](../../pkg/middleware/auth/image_policy.go)、[Image BFF](../../app/admin/service/internal/service/image_service.go) |
| ModelDev execution 列表 | 保留 page_size/page_token 与 next_page_token；execution 列表默认 20、上限 100，token 上限 2048 字节，不附加虚构 total。产物、preset 和 input-version 列表按各自专用合同 | [ModelDev 查询 HTTP](../../app/admin/service/internal/server/modeldev_query_http.go)、[目录 HTTP](../../app/admin/service/internal/server/modeldev_catalogue_http.go)、登记中的 MODELDEV-03/06/07 |
| Accelerator 领域列表 | 保留 page_size/page_token 与 next_page_token，page_size 为 0 时默认 50、最大 200；不伪造 total。下游 token 不替换成 Governance c1 | [Accelerator BFF](../../app/admin/service/internal/service/accelerator_service.go)、登记中的 GOV-ACC-V12-01 |
| Quota 管理目录及套餐配额列表 | 核对对应 Repo 的现有 PagingRequest 实现，不据共享 cursor 自动改造，也不改写领域专项 token 合同 | [quota_admin_repo.go](../../app/admin/service/internal/data/quota_admin_repo.go)、[plan_quota_list.go](../../app/admin/service/internal/data/plan_quota_list.go) |

旧页码依赖默认每页上限为 100，超限可能截断；支持的 `noPaging=true` 路径默认最多 10,000 行，并非无限全量导出。以上是 [本地分页器](../../pkg/localdeps/go-crud/pagination/paginator/limits.go) 的默认值，各资源仍需核对实际实现，不能把旧分支的截断行为移植到新 cursor 分支。

<a id="current-differences"></a>

## 待迁移范围与已知差异

1. **其他历史列表未迁移。** LIST-CURSOR-01 仅改变 users；LIST-CURSOR-02 新增 roles 与 api-keys 两个接口。逐接口明确改造范围、业务排序和兼容策略后再实施。Network、ModelDev、Accelerator 与 Quota 的字段和 token 格式按其专项合同维护，本文不授权统一改名。
2. **同快照计数未由游标实现保证。** 当前 list/count 的谓词一致，但分别执行；本文只记录实际一致性边界，不据此引入全仓事务迁移。若具体业务要求同一次响应的强快照或跨页冻结结果，先确定合同与相应实现。
3. **多字段筛选的指纹稳定性依赖规范化。** `QueryStringConverter` 按 Go map 随机顺序追加 `Conditions`，因此指纹计算前须经 `NormalizeFilterExpr` 重排（`entgo.CursorFilterKey` 已接入）。新增或重写筛选解析路径时必须保持该规范化，否则语义相同的 query 会产生不同指纹，使原样回传的 `next_cursor` 被误判为篡改。

> 原先记录的两条 users 差异——**noPaging 分支绕过游标参数校验**与**空关系筛选提前返回绕过 limit/游标校验**——已由 LIST-CURSOR-02 修复：互斥检查覆盖 `noPaging`，且参数与游标校验前移到空集返回之前（见 [LIST-CURSOR-02](../interface-integration-register.md#list-cursor-02)）。已不在差异列表内。

这些差异属于后续实现任务；本次文档更新没有修改业务代码、参数解析、签名配置或数据库行为。

## 迁移与验收

按已授权的具体接口小步实施：

1. 登记路由/RPC、当前协议、目标协议、租户与数据权限、可用过滤和稳定排序、total 语义，以及客户端兼容范围。复用原编号，领域专项 token 保持不变，除非改造范围明确包含它。
2. 用真实 Repo → Service → HTTP/RPC 接通最小翻页主链，同时落实签名、空结果入口校验、过滤与排序绑定、同条件计数、旧窗口兼容和租户负向行为；不使用模拟业务查询制造通过。
3. 对 Proto 输入和生成模板走仓库完整生成入口，保留工作树保护。API 路由未改变时不凭分页字段新增触发权限扩张；新增或修改路由按 API 目录/权限/套餐合同另行处理。
4. 定向验证默认与边界 limit、空列表及末页、同值排序与 NULL 区段、所有受支持类型、原样续页、total 不受窗口影响、fieldMask、旧窗口兼容，以及篡改、跨租户、换资源/筛选/排序、未知版本、超长、缺密钥和缺可信身份。另测 noPaging 冲突与空关系筛选，不能只验证有数据路径。
5. 在明确授权环境验证真实数据库的时间排序、多副本游标互认、当前权限和撤权拒绝；需要特定快照保证时加入该保证的并发验证。分别报告源码测试、外部边界替身、真实 HTTP/数据库链路与部署验收，未执行保留 `not_verified`。

2026-10-10 的 users 历史验收包含两页读取、total 稳定、参数/签名/绑定拒绝、旧页码回归及 PostgreSQL created_at 翻页，详见 [LIST-CURSOR-01](../interface-integration-register.md#list-cursor-01)；roles 与 api-keys 的对应验收见 [LIST-CURSOR-02](../interface-integration-register.md#list-cursor-02)。SQLite 夹具的时间值存储不能证明 PostgreSQL 时间列键集行为。上述两条提前分支差异已修复并由部署实测覆盖（空关系筛选 + 非法 limit/坏游标、`noPaging` + cursor 均 400）；本次入仓的文档更新本身未重跑这些测试或运行验收。

# 推理调用 Key 管理

前端页面迁移、接口示例及推理调用流程见[推理 AK 与策略前端对接](../frontend-inference-ak-policy.md)。

Governance 提供以下 HTTP 管理入口，使用租户用户 Bearer JWT、明确的
`inference:key:*` 权限和有效的 `INFERENCE` 套餐模块。平台租户 0、平台 AK/SK
机器主体和推理 Key 都不能管理这些凭证。接口与 `/api/v1/auth/api-keys` 独立，
Key 由 Inference 签发并保存在 `inference_access_keys`，Governance 不存储 secret。
这些 `/api/v1/...` 路由直接访问 Governance，不接入 Higress。

| 方法和路径 | 权限 | 请求与结果 |
|---|---|---|
| `POST /api/v1/inference-keys` | `inference:key:create` | `{data:{...}}` → `{key,secret,replayed}` |
| `GET /api/v1/inference-keys` | `inference:key:read` | `page/pageSize` → `{items,total}`，各项含完整 `secret` |
| `GET /api/v1/inference-keys/{key_id}` | `inference:key:read` | 直接返回 Key 信息和完整 `secret` |
| `DELETE /api/v1/inference-keys/{key_id}` | `inference:key:revoke` | 不带请求体；撤销后直接返回元数据，保留历史 |
| `POST /api/v1/inference-keys/{key_id}:rotate` | `inference:key:rotate` | `{data:{idempotency_key}}` → `{key,secret,replayed}` |
| `POST /api/v1/inference-keys/{key_id}:bind-policy` | `inference:key:bind-policy` | `{data:{policy_id}}` → Key 元数据 |

成功状态均为 200。`key_id` 是规范的小写 UUID，不是平台 AK 的数字 ID。
元数据包含 `id/name/key_prefix/policy_id`、`expires_at/revoked_at`、
`created_by/created_at/last_used_at/version`；列表和详情另含 `secret`。`version`
和 `total` 在 JSON 中为十进制字符串。

创建请求示例：`{"data":{"name":"应用推理调用","policy_id":"<同租户策略 UUID>","idempotency_key":"key-001"}}`。
`name` 非空且最多 256 字节；必须提供同租户的 `policy_id`，可选 `expires_at`。
模型及限额属于策略，旧 Key 范围和限额字段不再接受。重新绑定同样要求同租户；
绑定响应不返回 secret。

## 策略接口

| 方法和路径 | 权限 | 请求与结果 |
|---|---|---|
| `POST /api/v1/inference-policies` | `inference:policy:create` | `{data:{name,allowed_model_names,requests_per_second,requests_per_minute,requests_per_hour}}` → 策略 |
| `GET /api/v1/inference-policies` | `inference:policy:read` | `page/pageSize` → `{items,total}` |
| `GET /api/v1/inference-policies/{policy_id}` | `inference:policy:read` | 策略详情 |
| `PUT /api/v1/inference-policies/{policy_id}` | `inference:policy:update` | `{data:{...}}` 完整替换 |
| `DELETE /api/v1/inference-policies/{policy_id}` | `inference:policy:delete` | 未被绑定时删除，返回 `{}` |

模型列表按推理请求 `model` 精确匹配；空列表拒绝全部模型。限额为非负 int32，
0 表示该窗口不限。同一策略的多把 AK 分别计算额度；更改策略不清空已有计数。
策略、AK 和模型发布服务目前必须同租户。已绑定策略不可删除，返回 409。
写接口拒绝重复和未知字段、query、公网身份 header 和路径 ID 冲突；列表分页
`page/pageSize` 各为 1..100，默认 1/20。删除和绑定行为由 Inference 审计。
旧数据不自动迁移：升级后旧 AK 未绑定策略时无法调用，需显式重新绑定。

创建与轮换必须带非空、无首尾空白、最多 128 字节的 `idempotency_key`。
创建幂等作用域沿用 owner 的「租户 + 创建幂等键」，轮换沿用「租户 + Key + 当前
轮换幂等键」。重放返回原元数据和 `replayed=true`，不再交付明文 secret。
创建同键异意图返回 409 `INFERENCE_KEY_CONFLICT`，Gov 对 owner 返回的持久元数据
比较名称、策略 ID 与到期时间；不增加本地幂等账本。不要把一个幂等键复用于不同
请求。首次响应丢失时，可从列表取得原 Key ID，再从详情读取当前 secret。

同一租户的相同创建幂等键并发投递时，Inference 仅对该唯一键冲突执行
`ON CONFLICT ... DO NOTHING`，再从同一 READ COMMITTED 事务读取已提交的原 Key；
只有首次创建交付 secret 并写入创建审计。不同租户的幂等键互不占用，其他约束错误
保持失败并回滚，不转为重放。Gov 对重放结果继续执行上述意图比较。

列表缺省独立使用 `page=1/pageSize=20`。当前 owner 将两参数均限制为 `1..100`，
HTTP 保留该实际限制；支持直接跳页，不接受游标、筛选或排序参数。空列表返回
`items:[]`；`total` 保留 owner 的总数。重复参数、前导零、空值、未知参数或查询
请求体返回 400。非列表接口不接受 query。写入严格拒绝未知 JSON 字段、重复字段
和伪造身份；轮换请求中的 `key_id` 如提供，必须与路径一致。

Inference `internal/data/postgres/access_key.go` 的列表与计数使用同一只读
REPEATABLE READ 事务。并发写入不会使一次响应中的 `items` 与 `total` 使用不同
快照；Gov 直接保留 owner 的总数。独立 PostgreSQL 回归在读取列表后、计数前
提交另一条 Key，验证本次列表与总数均维持原快照。不同分页请求仍各自取得快照。

按用户最终要求，列表各项和详情通过 `secret` 返回完整的当前 `iak_...`，继续使用
租户用户 JWT、`inference:key:read`、INFERENCE 套餐和同租户隔离。创建/轮换的首次
响应仍在顶层返回 `secret`，写操作重放仍只返回元数据，不重复交付；撤销响应只含
元数据。所有这些 HTTP 响应带 `Cache-Control: no-store`，Proto 日志脱敏会清除
secret，审计不保留响应、请求体、query 或 Referer。使用时将 secret 放入 Higress 请求头 `x-api-key`，调用
`/v1/completions` 等推理接口；它不能用于 Governance 登录或平台 AK/SK 验签。

Inference 显式迁移 `000025_inference_access_key_secrets.sql` 增加可空密文字段；
创建和轮换在原事务内持久化 AES-256-GCM 密文，Argon2id 哈希继续用于鉴权。
外部文件 `ANI_INFERENCE_ACCESS_KEY_ENCRYPTION_KEY_FILE` 必须包含恰好 32 字节的
随机二进制主密钥；密钥不入库、不入镜像、不入 Git，部署时独立挂载并可靠保管。
密文绑定租户、Key ID、前缀和鉴权哈希；无法解密时整次列表/详情返回不可用，
不返回部分结果、不降级为空值。更换或丢失主密钥会使已有密文无法读取，不能在
重启时重新生成主密钥；主密钥轮换不在本批范围。

迁移前的 Key 仅有不可逆哈希，不会被自动轮换。旧记录尚未补存时不含 `secret`；
该 Key 下一次成功鉴权、通过策略/限流检查后，用本次已验证的原值补存
密文，之后列表和详情即可读到完整原 AK。补存按租户、ID、版本和哈希条件更新，
不覆盖并发轮换/撤销，也不改变版本或限流配置。无法再成功使用的历史凭证无法
还原；可用的新凭证通过显式轮换产生。列表/详情均只读本次快照，不因查询签发新值。

错误通过 HTTP 状态及 Proto reason 表达：400 `INVALID_INFERENCE_KEY_REQUEST`、
403 `INFERENCE_KEY_USER_REQUIRED`、404 `INFERENCE_KEY_NOT_FOUND`、
409 `INFERENCE_KEY_CONFLICT`、503 `INFERENCE_KEY_UNAVAILABLE`。跨租户对象不存在
返回 404；依赖错误保留在内部 cause，公网不回显下游原始报文、密钥或地址。

内部调用使用已登记的 `ani-governance` → Inference mTLS 连接，精确接收方 DNS SAN
取自 `ANI_INFERENCE_SERVER_NAME`。Key 与策略 RPC 属于
`/inference.v1.InferenceAccessKeyService/`，依次为 `CreateInferenceAccessKey`、
`ListInferenceAccessKeys`、`GetInferenceAccessKey`、`RevokeInferenceAccessKey`、
`RotateInferenceAccessKey`；另有策略 CRUD 和绑定 RPC，均为用户委托。Gov 从已认证 Principal 取得用户和租户，
通过持久映射转换租户 UUID，重新构造 `x-ani-tenant-id/x-ani-actor/x-ani-request-id`，
不透传公网认证或身份 metadata。每次 RPC 最长 3 秒，继承请求取消，不自动重试。
Gov 不调用网关专用的 `CheckInferenceAccess`，不新开下游 listener。

部署时先按部署规范执行 API 目录 dry-run 和显式同步，再运行
`scripts/ops/sql/register-inference-key-permissions.sql` 和
`scripts/ops/sql/register-inference-policy-permissions.sql` 注册权限关系，按目标角色
和租户套餐显式授权，并刷新策略。脚本不自动给角色授权，也不自动启用套餐。
不需要 Governance 表结构迁移；Inference 必须已启用自己的 Key 持久化迁移和 RPC。
源码及外部协议替身测试不代表目标环境已完成部署或真实推理验收。

跨仓验证可使用以下命令显式运行：

```sh
go test -tags=policybridge ./app/admin/service/internal/server -run '^TestInferencePolicyCrossBackend$' -count=1
```

运行前设置
`ANI_POLICY_BRIDGE_ENABLE=1`、`ANI_POLICY_BRIDGE_DEDICATED_DB=1`、
`ANI_POLICY_BRIDGE_DSN`（已经执行完整 Inference migration 的独占测试库）和
`ANI_POLICY_BRIDGE_SOURCE`（Inference 源码目录）。测试使用真实 Governance HTTP
认证和权限、真实 mTLS、Inference 策略/AK 持久化与鉴权；模型发布状态作为外部
运行环境的测试事实注入，不启动 Higress 或模型引擎。不得把该测试当作集群验收。

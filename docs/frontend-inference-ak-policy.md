# 推理 AK 与访问策略：前端对接

本文用于将旧版“AK 直接配置模型和限额”的页面迁移到“先建策略，再绑定 AK”。接口字段和安全边界以[推理调用 Key 合同](contracts/inference-keys.md)为准；推理服务诊断接口见[推理管理对接说明](contracts/inference-governance-integration.md)。上线状态以目标环境实际部署与验收为准。

## 1. 页面和接口如何变化

| 页面 | 旧版 | 新版 |
| --- | --- | --- |
| AK 新建 | 在 AK 上填写 `allowed_model_names`、`allowed_service_ids` 和限额 | 选择一个已有策略，提交 `policy_id` |
| 策略管理 | 服务上的访问策略 | 独立的同租户策略，配置模型名称、QPS、RPM、RPH |
| AK 列表和详情 | 模型范围、限额直接显示在 AK 中 | 返回 `policy_id`；通过策略列表或详情显示名称和配置 |
| 更换策略 | 修改 AK 的范围 | 调用 AK 的 `:bind-policy` 接口 |

策略允许的模型是推理调用 JSON 中的 `model`，应选择推理服务实际发布的 `served_model_name`，不要用服务 UUID 或展示名称替代。模型名称精确匹配；策略的空模型列表拒绝所有模型。三个限额均为非负整数，`0` 表示该时间窗口不限。同一策略绑定多个 AK 时，限额按 AK 分别计数。

管理接口请求发送到 **Governance**，使用租户用户登录后的 `Authorization: Bearer <JWT>`，由当前登录身份确定租户。推理调用发送到 **Higress**，使用 `x-api-key: <推理AK>`。推理 AK 和平台 `/api/v1/auth/api-keys` 的 AK 不是同一类凭证。

## 2. 策略接口

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| POST | `/api/v1/inference-policies` | 创建策略 |
| GET | `/api/v1/inference-policies?page=1&pageSize=20` | 列表 |
| GET | `/api/v1/inference-policies/{policy_id}` | 详情 |
| PUT | `/api/v1/inference-policies/{policy_id}` | 完整替换策略配置 |
| DELETE | `/api/v1/inference-policies/{policy_id}` | 删除未被绑定的策略 |

创建请求：

```json
{
  "data": {
    "name": "应用推理策略",
    "allowed_model_names": ["image-registry-20261026-whole-gpu"],
    "requests_per_second": 2,
    "requests_per_minute": 60,
    "requests_per_hour": 1000
  }
}
```

成功直接返回策略对象，示例：

```json
{
  "id": "11111111-1111-4111-8111-111111111111",
  "name": "应用推理策略",
  "allowed_model_names": ["image-registry-20261026-whole-gpu"],
  "requests_per_second": 2,
  "requests_per_minute": 60,
  "requests_per_hour": 1000,
  "version": "1"
}
```

更新使用与创建相同的 `data`，是完整替换；页面应提交全部期望保留的模型和限额，不能只提交变更字段。已绑定 AK 的策略不能删除，会返回 409。

列表响应为 `{ "items": [...], "total": "1" }`。`page` 与 `pageSize` 默认分别为 1、20，均支持 1～100；不支持其他筛选参数。策略 ID 是小写标准 UUID，`version` 和 `total` 是十进制字符串。

## 3. AK 接口

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| POST | `/api/v1/inference-keys` | 创建并绑定策略 |
| GET | `/api/v1/inference-keys?page=1&pageSize=20` | 列表 |
| GET | `/api/v1/inference-keys/{key_id}` | 详情 |
| POST | `/api/v1/inference-keys/{key_id}:bind-policy` | 绑定或更换策略 |
| POST | `/api/v1/inference-keys/{key_id}:rotate` | 轮换 AK |
| DELETE | `/api/v1/inference-keys/{key_id}` | 撤销 AK |

创建请求：

```json
{
  "data": {
    "name": "业务应用",
    "policy_id": "11111111-1111-4111-8111-111111111111",
    "idempotency_key": "create-business-app-001"
  }
}
```

`policy_id` 必须是同租户策略，可选 `expires_at` 为 RFC 3339 时间。`idempotency_key` 非空、无首尾空白且最多 128 字节；一次操作重试应复用，创建另一个 AK 应使用新值。旧模型和限额字段不得继续放在 AK 创建请求中。

创建或轮换首次成功返回组合结果：

```json
{
  "key": {
    "id": "22222222-2222-4222-8222-222222222222",
    "name": "业务应用",
    "key_prefix": "iak_example",
    "policy_id": "11111111-1111-4111-8111-111111111111",
    "version": "1"
  },
  "secret": "iak_<完整凭证示例>",
  "replayed": false
}
```

列表各项和详情中的 `secret` 也是当前完整 AK；历史记录若没有可恢复的密文，该字段可能缺失。撤销和绑定策略的响应不包含 secret。创建或轮换幂等重放返回 `replayed: true`，不重复交付顶层 secret；可在有权限时通过列表或详情读取当前值。轮换会使旧值失效。

更换策略：

```http
POST /api/v1/inference-keys/22222222-2222-4222-8222-222222222222:bind-policy
Content-Type: application/json
```

```json
{"data":{"policy_id":"11111111-1111-4111-8111-111111111111"}}
```

轮换请求体为 `{"data":{"idempotency_key":"rotate-business-app-001"}}`。列表响应同样是 `items/total`，分页范围和策略列表一致。列表或详情展示 secret 时应由用户主动查看或复制，避免写入日志、URL、浏览器持久缓存或分析事件；后端响应带 `Cache-Control: no-store`。

## 4. 推理调用与错误

```http
POST /v1/completions
x-api-key: iak_<完整推理AK>
Content-Type: application/json
```

```json
{"model":"image-registry-20261026-whole-gpu","prompt":"Hello","max_tokens":16}
```

管理接口按 HTTP 状态及响应 `reason` 处理，常见状态为 400 参数错误、403 权限不足、404 资源不存在或不属于当前租户、409 冲突、503 下游不可用。推理调用通常以 401 表示凭证无效、403 表示模型或策略拒绝、429 表示限流；前端不能把 429 当作凭证失效。

## 5. 切换检查

1. 增加策略管理页面和 AK 的策略选择，先获取策略列表再创建 AK。
2. 删除旧 AK 请求和列表中的模型、限额字段；展示 `policy_id` 并关联策略详情。
3. 调整新建、轮换、查看和复制完整 secret 的交互，处理无 secret 的历史记录。
4. 后端升级、数据库迁移和权限授权完成后再启用新页面；旧 AK 未绑定策略时不能调用，按现场切换方案重新签发或绑定。
5. 分别验证允许模型、未允许模型、限流和跨租户拒绝；不能以接口 200 代替真实推理验证。

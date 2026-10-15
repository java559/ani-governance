# CPU 训练前端对接说明（Governance BFF / ModelDev CPU-P01）

> 面向前端同学。本文描述普通租户在 **Governance BFF** 上完成 CPU 训练「选择数据 → 发起训练 → 观察状态 → 停止 → 下载模型」的完整 HTTP 合同。
>
> 权威来源：`api/protos/admin/service/v1/i_modeldev.proto`、`api/protos/modeldev/service/v1/execution.proto`，以及运行服务自带的 OpenAPI（`/swagger` 或 `assets/openapi.yaml` 的 `ModelDevService` 段）。状态与产物枚举值（`compute_state`/`delivery_state`/`close_state`/`resource_state`/`current_step`/`role`）实际来自 ModelDev 服务 `ani-modeldev-service` 的 `api/ani/modeldev/v1/modeldev.proto` 与 `api/ani/training/v1/training.proto`（Governance 仅去掉枚举前缀透传）。
> 更新日期：2026-10-12。字段以本文与 OpenAPI 为准。

***

## 1. 对接总原则（务必先读）

| 项        | 约定                                                                                                                                              |
| -------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| **入口**   | 一律走 Governance BFF：`https://<governance-host>/admin/v1/modeldev/*`。**禁止**直连 ModelDev 的 `:9000`（命令口）、`:9001`（受管 Step 口），那是内部 mTLS 端口，前端不可达也不被允许。 |
| **认证**   | 所有请求携带 `Authorization: Bearer <access-token>`，复用已有登录 / 刷新链路。                                                                                    |
| **协议**   | REST + JSON。请求体字段名统一 **`lower_snake_case`**（不是 camelCase）。                                                                                      |
| **状态更新** | **没有 SSE / WebSocket 推送**，状态变化必须靠**轮询** `GET .../executions/{id}`。                                                                              |
| **身份来源** | 租户 / 操作人 / 集群 / 命名空间 / 对象存储 key 全部由后端从可信上下文推导；**请求体与 query 中不得出现这些字段**，出现即被拒绝。                                                                  |
| **缓存**   | 查询、详情、产物、下载类响应的响应头为 `Cache-Control: no-store`，前端不得缓存。                                                                                           |

***

## 2. 接口清单

| 用途           | 方法   | 路径                                                       |
| ------------ | ---- | -------------------------------------------------------- |
| 训练预设（下拉框）    | GET  | `/admin/v1/modeldev/presets`                             |
| 输入数据集列表（下拉框） | GET  | `/admin/v1/modeldev/input-versions`                      |
| 输入数据集详情      | GET  | `/admin/v1/modeldev/input-versions/{input_version_id}`   |
| 提交训练         | POST | `/admin/v1/modeldev/executions`                          |
| 执行列表         | GET  | `/admin/v1/modeldev/executions`                          |
| 执行详情（轮询）     | GET  | `/admin/v1/modeldev/executions/{execution_id}`           |
| 训练日志         | GET  | `/admin/v1/modeldev/executions/{execution_id}/logs`      |
| 停止训练         | POST | `/admin/v1/modeldev/executions/{execution_id}:stop`      |
| 产物列表         | GET  | `/admin/v1/modeldev/executions/{execution_id}/artifacts` |
| 取产物短期下载地址    | GET  | `/admin/v1/modeldev/artifacts/{artifact_id}/content`     |

ID 规则：`execution_id`、`preset_id`、`input_version_id`、`artifact_id` 均为**标准 36 字符小写 UUID**（不接受大写、compact、URN 等别名）。

### 分页约定

- 列表类接口统一使用 `limit` + `cursor`（游标分页，与 users/VPC 一致）。
- `limit` 上限 **100**；`cursor` 上限 **2048 字节**。
- 返回 `next_cursor`；为空表示没有下一页。首次不传 `cursor`，后续原样回传上一页的 `next_cursor`。
- 列表一起返回 `total`（uint64，**JSON 中是十进制字符串**）：同租户、同权限、同筛选条件下的匹配总数，不受窗口影响（不是本页条数）。
- `limit` 要么**不传**（用后端默认值），要么传 **1\~100 的规范十进制正整数**；显式传 `0`、超上限或非规范写法（如前导零）会返回 `400`。
- `cursor` 可直接透传，无需解析；换筛选或换排序时请从首页重新取数。

***

## 3. 认证与前置

### 3.1 登录

沿用现有登录接口获得 access token；本模块不提供独立登录。access token 过期用既有刷新接口续期。

### 3.2 请求会被 401/403 拒绝的情况

| HTTP | 含义                                    | 前端处理       |
| ---- | ------------------------------------- | ---------- |
| 401  | 未登录 / token 无效 / 会话失效                 | 触发登录或刷新    |
| 403  | 无权限 / 套餐未含 MODEL 模块 / 路由未登记 / 使用不支持字段 | 提示无权限，不要重试 |

ModelDev 的查询、创建与停止都**要求租户具备** **`ALL`** **数据范围**，更窄的数据范围会返回 403（当前实现选择，非 bug）。

***

## 4. 接口详解

### 4.1 训练预设 `GET /admin/v1/modeldev/presets`

Query：`limit`、`cursor`。

响应：

```json
{
  "items": [
    {
      "preset_id": "uuid",
      "name": "general-cpu",
      "kind": "GENERAL_TRAINING",
      "active_release_id": "uuid",
      "image_version_ids": ["uuid"],
      "parameter_rules": [
        {
          "name": "learning_rate",
          "type": "DECIMAL",
          "required": false,
          "default_value": {"name": "learning_rate", "type": "DECIMAL", "value": "0.01"},
          "allowed_values": [],
          "minimum": null,
          "maximum": {"name": "learning_rate", "type": "DECIMAL", "value": "0.1"}
        }
      ],
      "binding_generation": "1",
      "new_submissions_enabled": true
    }
  ],
  "next_cursor": "",
  "total": "1"
}
```

前端要点：

- `binding_generation` 是 uint64，**JSON 中是十进制字符串**。
- `parameter_rules` 可用于动态渲染参数表单（`type` / `required` / `default_value` / `minimum` / `maximum` / `allowed_values`）。
- **`minimum`** **/** **`maximum`** **/** **`default_value`** **可能为** **`null`**（实测环境里 `minimum` 常为 `null`，仅部分参数有 `maximum`）；渲染表单前必须判空，不能假定一定有值。
- **`new_submissions_enabled=false`** **时该预设不可发起新训练**；`active_release_id` 为空表示当前没有启用绑定，不能提交。

### 4.2 输入数据集 `GET /admin/v1/modeldev/input-versions`

Query：`limit`、`cursor`、`state`（可选）。

- `state` 只接受：`FIXING` | `VALIDATING` | `READY` | `FAILED`。
- **发起训练只能选** **`state=READY`** **的数据集**，建议前端直接传 `?state=READY`。

响应：

```json
{
  "items": [
    {
      "input_version_id": "uuid",
      "state": "READY",
      "format": "CSV",
      "size_bytes": "1048576",
      "sha256": "hex...",
      "row_count": 1024,
      "feature_count": 16,
      "created_at": "2026-10-10T08:00:00Z"
    }
  ],
  "next_cursor": "",
  "total": "1"
}
```

`size_bytes` 是 int64，JSON 中是字符串。详情接口 `GET /admin/v1/modeldev/input-versions/{id}` 直接返回单个 `InputVersionView` 对象（无外层包装）。

### 4.3 提交训练 `POST /admin/v1/modeldev/executions`

**请求头**：

```
Authorization: Bearer <access-token>
Content-Type: application/json
```

请求体（严格 JSON，字段名 `lower_snake_case`）：

```json
{
  "name": "cpu-mlp-demo",
  "kind": "GENERAL_TRAINING",
  "preset_id": "<preset-uuid>",
  "dataset_version_id": "<ready-input-version-uuid>",
  "idempotency_key": "cpu-demo-001",
  "general_parameters": [
    {"name": "learning_rate", "type": "DECIMAL", "value": "0.01"}
  ]
}
```

字段说明：

| 字段                    | 必填 | 说明                                                  |
| --------------------- | -- | --------------------------------------------------- |
| `name`                | 是  | 展示名                                                 |
| `kind`                | 是  | **当前只支持** **`GENERAL_TRAINING`**；取值应取自 `preset.kind`，勿硬编码（见 §10） |
| `preset_id`           | 是  | 来自 presets                                          |
| `dataset_version_id`  | 是  | 必须是 READY 的 input version                           |
| `idempotency_key`     | 是  | 幂等键，见下方规则                                           |
| `image_version_id`    | 否  | 只能填所选 Release 登记的镜像版本                               |
| `general_parameters`  | 否  | 参数数组，值一律是**字符串**                                    |
| `source_execution_id` | —  | **当前必须省略**，填写会返回 `403 SOURCE_EXECUTION_UNAVAILABLE` |

**幂等键规则（重点）**：

- 请求超时或响应丢失时，**必须用同一** **`idempotency_key`** **+ 完全相同的意图重试**，系统会返回原执行（`replayed=true`）。
- **同一 key 改参数会返回** **`409`**；只有确实要再训一次时才换新 key。
- `general_parameters` 的**省略 ≠ 显式** **`[]`**，二者是不同意图；`null` 被拒绝。
- `learning_rate` 取值：十进制字符串，范围 `(0, 0.1]`，默认 `0.01`。epochs/batch\_size 固定，不可传。

响应 `202 Accepted`：

```json
{
  "operation_id": "uuid",
  "execution_id": "uuid",
  "resolved_release_id": "uuid",
  "replayed": false,
  "compute_state": "ACCEPTED",
  "delivery_state": "PENDING",
  "close_state": "OPEN",
  "resource_state": "NOT_APPLICABLE"
}
```

> **`202`** **只代表「已持久受理」，不代表开始计算或训练成功。** 前端不能据此显示“训练中”。请用返回的 `execution_id` 轮询详情。
>
> **`202`** **响应体中的四轴状态仅为受理时刻的快照，不可当作当前进展**：当受理前已存在持久投递确认时，响应体可能已是 `TRAINING` 等中间态；否则为 `compute_state=ACCEPTED`。前端一律以轮询 `GET .../executions/{id}` 的结果为准，不要直接渲染 `202` 体的状态。

### 提交训练的 412 拒绝（与产物 412 含义不同）

创建接口的 `412` 表示**当前无法受理新训练**，至少三种原因，前端应按 reason 区分提示：

| reason                  | 含义                                           |
| ----------------------- | -------------------------------------------- |
| `INPUT_NOT_READY`       | 所选数据集尚未就绪（不是 READY）                          |
| `NO_COMPATIBLE_RELEASE` | 所选预设当前没有启用的 Release 绑定                       |
| `SUBMISSIONS_PAUSED`    | 该预设已暂停接受新提交（`new_submissions_enabled=false`） |

### 4.4 执行详情 `GET /admin/v1/modeldev/executions/{execution_id}`

响应**直接返回** **`ExecutionView`** **对象**（无外层包装）：

```json
{
  "operation_id": "uuid",
  "execution_id": "uuid",
  "name": "cpu-mlp-demo",
  "kind": "GENERAL_TRAINING",
  "preset_id": "uuid",
  "release_id": "uuid",
  "input_version_id": "uuid",
  "image_version_id": "",
  "compute_state": "TRAINING",
  "delivery_state": "PENDING",
  "close_state": "OPEN",
  "resource_state": "NOT_APPLICABLE",
  "current_step": "TRAIN_WAIT",
  "stop_requested": false,
  "close_generation": "0",
  "accepted_at": "2026-10-10T08:00:00Z",
  "deadline_at": "2026-10-10T09:00:00Z",
  "observed_at": "2026-10-10T08:05:00Z"
}
```

时间均为 RFC3339；`close_generation` 是 uint64 字符串。

`current_step` 取值为大写枚举名（不是连字符小写），可能为 `UNSPECIFIED`（尚无运行事实）、`PREPARE`、`TRAIN_WAIT`、`PUBLISH`、`CLOSE`。

### 4.5 训练日志 `GET /admin/v1/modeldev/executions/{execution_id}/logs`

Query（都可选）：`tail_lines`（默认 200，上限 **1000**）、`max_bytes`（默认 16384，上限 **65536**）。

响应：

```json
{
  "log_id": "...",
  "lines": [{"timestamp": "2026-10-10T08:05:01Z", "text": "epoch 1/3 ..."}],
  "truncated": false,
  "observed_at": "2026-10-10T08:05:02Z"
}
```

- 后端读取的是**实际训练 Pod** 的日志（**不返回** Pod UID / container / object key）。
- 日志不可读、资源身份变化时返回不可用，不会伪造空日志。
- **训练 Pod 回收后（执行已** **`CLOSED`、Pod 不再存在）本接口返回** **`503 MODELDEV_QUERY_UNAVAILABLE`**（实测已验证）。前端须对 503 容错：仅在执行进行中（`close_state != CLOSED`）展示“实时日志”，已关闭的历史执行应显示“日志已不可用”，不要当作接口故障永久重试。
- 写入 query 的 `Pod`、`namespace`、`log_id` 等选择器会被拒绝（400）。

### 4.6 停止训练 `POST /admin/v1/modeldev/executions/{execution_id}:stop`

**Body 必须为空**（不接受任何请求体或 query）。

```http
POST /admin/v1/modeldev/executions/{id}:stop
Authorization: Bearer <access-token>
```

响应 `202`：

```json
{
  "operation_id": "uuid",
  "execution_id": "uuid",
  "stop_requested": true,
  "intent_generation": "1",
  "replayed": false
}
```

> **`202`** **只代表停止意图已受理，不代表已经停止。** 前端需继续轮询详情，直到 `close_state` 变为 `CLOSED`。重复点击停止是幂等的（`replayed=true`，代次不变）。

### 4.7 产物列表 `GET /admin/v1/modeldev/executions/{execution_id}/artifacts`

Query：`limit`、`cursor`。先等 `delivery_state=PUBLISHED` 再拉取才有内容。

响应：

```json
{
  "items": [
    {
      "artifact_id": "uuid",
      "execution_id": "uuid",
      "filename": "model.pt",
      "role": "CHECKPOINT",
      "size_bytes": "1234567",
      "sha256": "hex...",
      "delivery_state": "PUBLISHED",
      "verified_at": "2026-10-10T08:20:00Z"
    }
  ],
  "next_cursor": "",
  "total": "4"
}
```

正常训练产出四个文件，`role` 与文件的对应关系如下（`role` 是大写枚举名）：

| 文件                  | role           |
| ------------------- | -------------- |
| `model.pt`          | `CHECKPOINT`   |
| `model_config.json` | `MODEL_CONFIG` |
| `metrics.jsonl`     | `METRICS`      |
| `summary.json`      | `SUMMARY`      |

### 4.8 取下载地址 `GET /admin/v1/modeldev/artifacts/{artifact_id}/content`

响应：

```json
{
  "artifact": { "artifact_id": "uuid", "filename": "model.pt", "role": "CHECKPOINT", "size_bytes": "1234567", "sha256": "hex...", "delivery_state": "PUBLISHED", "verified_at": "..." },
  "download_url": "https://.../signed?versionId=...",
  "expires_at": "2026-10-10T08:21:00Z"
}
```

**下载地址是短期凭据（ModelDev 侧固定约 60 秒有效；前端一律以返回的** **`expires_at`** **为准）**：

- 不要缓存、不要写日志、不要落库、不要展示给用户复制；
- 过期后重新调用本接口获取新地址；
- 前端拿到 `download_url` 后直接发起 HTTPS 下载（可能跨域到对象存储）。

***

## 5. 状态机（前端渲染依据）

详情返回四组**相互独立**的状态轴，不要混为一谈：

| 轴                | 取值                                                                                                     | 说明         |
| ---------------- | ------------------------------------------------------------------------------------------------------ | ---------- |
| `compute_state`  | `ACCEPTED` → `SUBMITTING` → `SUBMISSION_CONFIRMED` → `PREPARING` → `TRAINING` → `SUCCEEDED` / `FAILED` | 计算进展       |
| `delivery_state` | `PENDING` / `PUBLISHED`                                                                                | 结果交付（只有两态） |
| `close_state`    | `OPEN` / `CLOSING` / `CLOSED` / `NEEDS_REVIEW`                                                         | 关闭         |
| `resource_state` | `NOT_APPLICABLE`                                                                                       | CPU 场景恒为此值 |

另有中间态需前端容错（**不要当成失败**）：

- `SUBMISSION_NOT_SENT`、`SUBMISSION_UNCERTAIN`：投递结果不明确，系统保留围栏等待对账。

**判定建议**：

- “训练成功”看 `compute_state=SUCCEEDED`；
- “可下载”看 `delivery_state=PUBLISHED`；
- “已彻底停止”看 `close_state=CLOSED`；
- `NEEDS_REVIEW` 是需人工核查的中间态，前端应显示“需人工处理”，不要显示成功或失败。

***

## 6. 错误码与前端处理

| HTTP | 场景                                                                                                | 前端建议                           |
| ---- | ------------------------------------------------------------------------------------------------- | ------------------------------ |
| 400  | 参数非法 / 未知或重复 query / 显式传 `limit=0`                                                            | 表单校验提示                         |
| 401  | 未登录 / 凭证无效                                                                                        | 登录或刷新 token                    |
| 403  | 无权限 / 套餐未含 MODEL / 路由未登记 / 数据范围不是 `ALL` / 传了 `source_execution_id`                                | 提示无权限，勿重试                      |
| 404  | 资源不可用（含跨租户不可见）                                                                                    | 提示不存在                          |
| 409  | 幂等键冲突（同 key 不同意图）                                                                                 | 提示“该幂等键已用于其他请求”，换 key 或改为查询原执行 |
| 412  | 创建：`INPUT_NOT_READY` / `NO_COMPATIBLE_RELEASE` / `SUBMISSIONS_PAUSED`；查询：`ARTIFACT_NOT_PUBLISHED` | 按 reason 区分提示（见 §4.3、§4.8）     |
| 503  | 依赖不可用 `MODELDEV_*_UNAVAILABLE`（含创建侧 `ENVIRONMENT_NOT_READY`）                                      | 稍后重试                           |
| 504  | 超时 `MODELDEV_QUERY_TIMEOUT`                                                                       | 稍后重试；创建类用**原幂等键**重试            |

> **注意**：创建接口会把领域错误**显式映射**为细分公开错误：`INPUT_NOT_READY`→412、`ENVIRONMENT_NOT_READY`→503、`NO_COMPATIBLE_RELEASE`→412（见 §4.3），前端可直接按 HTTP + reason 展示。**查询接口**才会把下游其余领域错误折叠：`NOT_FOUND`/`PERMISSION_DENIED`/`UNAUTHENTICATED` 统一为 404 `RESOURCE_NOT_FOUND`，其他 `FAILED_PRECONDITION` 统一为 412 `ARTIFACT_NOT_PUBLISHED`。若前端需要查询侧的更细原因，请与后端确认（见第 8 节）。

***

## 7. 交互流程建议

```
登录
  └─ GET presets              → 渲染预设下拉（过滤 new_submissions_enabled=true）
  └─ GET input-versions?state=READY → 渲染数据集下拉
  └─ POST executions          → 202，取 execution_id
        （超时/断网：保留同一 idempotency_key + 同意图重试）
  └─ 轮询 GET executions/{id} → 3~5s 一次，进入 TRAINING 后放缓到 10~15s
        判终态：compute_state ∈ {SUCCEEDED, FAILED}
  └─ 用户点停止 → POST {id}:stop（空体）→ 继续轮询到 close_state=CLOSED
  └─ delivery_state=PUBLISHED
        └─ GET executions/{id}/artifacts
        └─ 对每个 artifact GET artifacts/{id}/content → 拿 download_url
        └─ 用 download_url 下载（60s 内，勿缓存）
```

轮询建议：不要对 `GET .../logs` 高频轮询（日志读取会命中实际训练 Pod，有 1000 行 / 64 KiB 上限）。

***

## 8. 前端联调前，后端必须准备好的内容

前端能联通的**前提**是后端/运维已完成下列配置（否则会一直 403 或创建失败）：

1. **权限登记**：`(path, method)` 已在 Api 表登记，且用户角色 → 权限 → API 关系已建立（`admin sync-apis` 显式同步 + 授权）。
2. **数据范围**：租户具备 `ALL` 数据范围。
3. **套餐模块**：租户 Plan 含 `MODEL` 模块。
4. **训练材料**：管理员已通过 CLI 导入 Release、核验 CSV 为 READY、并 CAS 启用 preset→release 绑定（`admin modeldev-import-release` / `import-csv` / `enable`）。没有启用绑定，预设列表不会声明可用。
5. **环境就绪**：Release / facts / dispatch binding / Runtime / 存储与身份材料齐备。

***

## 9. 当前不支持 / 暂缓（前端请勿设计依赖）

| 能力                        | 现状                                                 |
| ------------------------- | -------------------------------------------------- |
| **浏览器上传 CSV**             | **不支持**。输入数据集只能由管理员经 CLI 导入审核后的对象，普通用户无上传 HTTP 接口。 |
| **实时推送（SSE/WebSocket）**   | **无**。只能轮询。                                        |
| **训练进度百分比 / 预估剩余时间**      | 无。只有 `current_step` 和四轴状态。                         |
| **提交前查询剩余配额**             | 无对外接口（配额在治理内部）。                                    |
| **一键重跑 / 复制历史任务**         | 无。`source_execution_id` 当前被拒绝。                     |
| **删除 / 归档历史执行**           | 普通用户无此接口（清理是管理员受管操作）。                              |
| **训练完成站内消息通知**            | 当前无面向用户的完成通知闭环。                                    |
| **列表总数** **`total`**      | 列表只返回游标，无总数。                                       |
| **GPU / 分布式 / 微调 / 推理服务** | 均不在当前范围，仅 CPU 固定训练方案。                              |

***

## 10. 面向后续能力（微调等）的扩展约定

> 本节不是当前能力，而是**给前端的实现约束**：后续将新增「模型微调（LoRA SFT）」等能力，其设计已明确**受理入口、查询视图、步骤协议、状态机均不变**，只通过预设（preset）切换能力与参数。若前端按下述约定实现，微调上线时**几乎零改动**；若把当前值写死，则需返工。

**必须遵守的两条**

1. **`kind` 不要写死**：不要在前端硬编码 `"GENERAL_TRAINING"`。提交训练与渲染预设时，`kind` 一律**取自 `GET /presets` 返回的 `preset.kind`**，原样回填到 `POST /executions` 的 `kind`。后续会新增 `MODEL_FINETUNING` 等取值。
2. **参数表单按 `parameter_rules` 动态渲染**：不要写死 `epochs`/`batch_size`/`learning_rate` 三个输入框。表单字段应由 §4.1 的 `parameter_rules` 生成（`type`/`required`/`default_value`/`minimum`/`maximum`/`allowed_values`，均可能为 `null`）。后续微调会换成另一组参数（如 `lora_rank`/`lora_alpha`/`max_steps`/`cutoff_len`），集合由预设声明，前端无需改代码。

**建议**

- **数据集格式只展示、不写死选择逻辑**：当前为 CSV，后续可能为 JSONL（`format` 字段会体现）；下拉仍来自 `GET /input-versions?state=READY`。
- **产物按 `filename`/`role` 展示，不写死四个文件名**：当前为 `model.pt` 等 4 个（见 §4.7），后续微调会产出 LoRA adapter 类文件，前端按后端返回渲染即可。
- **过滤/降级以字段驱动**：预设能否提交看 `new_submissions_enabled` 与 `active_release_id`（§4.1），不要按 `kind` 硬编码可用性。

**保持不变（前端可按现状实现）**：全部接口路径与请求/响应结构、四轴状态机（§5）、幂等与错误码（§4.3、§6）、轮询交互（§7）。

***

## 11. 常见误区

- ❌ 直连 `:9000` / `:9001` 内部端口。
- ❌ 把 `202` 当训练开始；把 `CLOSING` 当已停止。
- ❌ 缓存或记录 `download_url`。
- ❌ 在 body/query 传 `tenant_id`、`namespace`、对象存储 key、Pod 名。
- ❌ 期望推送，或高频轮询日志接口。
- ❌ 用 camelCase 字段名（本模块统一 `lower_snake_case`）。

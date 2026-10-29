# 训练 / 微调任务详情 · 容器组、日志、事件、监控 接口对接

Governance BFF 对外接口合同。覆盖任务详情相关的四类读取能力：**容器组（Pod）、日志（快照 + SSE 流）、事件、资源曲线**。普通训练与微调共用同一套接口。

字段以 `api/protos/modeldev/service/v1/execution.proto`、`api/protos/admin/service/v1/i_modeldev.proto` 为准。整体任务生命周期（目录与输入 / 创建 / 列表 / 详情 / 产物 / 停止 / 恢复登记）见配套 handout `frontend-training-handoff.md`；本文与它互不重复，本文负责详情页四类读取能力，它负责其余接口。

联调环境：`http://10.10.1.66:30085`（NodePort），租户 `tenant-a`。

本文只描述请求、响应、字段语义与错误码，不含页面布局与交互建议。

---

## 1. 接口与实测状态

| 能力 | 接口 | 实测 |
| --- | --- | --- |
| 容器组 | `GET /api/v1/modeldev/executions/{id}/pods` | **200** |
| 事件 | `GET /api/v1/modeldev/executions/{id}/events` | **200**（保留窗口 `1h`） |
| 日志快照 | `GET /api/v1/modeldev/executions/{id}/logs` | **200** |
| 日志流 | `GET /api/v1/modeldev/executions/{id}/logs/stream` | **200**（SSE）；帧序列已实测，`TIME_LIMIT`/续传未实测，见 §10 |
| 资源曲线 | `GET /api/v1/modeldev/executions/{id}/resource-metrics` | **200**（CPU/内存；GPU 未实测） |

"200" 只表示接口可达，不代表每个字段的内容都已验收。逐项的验证状态见 §10。

四类能力都按"执行冻结的运行时计划"取数：调用方不传、也不被接受集群、命名空间、Pod 名。

---

## 2. 通用约定

### 2.1 认证与权限

- 全部要求**登录用户**（JWT），请求头 `Authorization: Bearer <access_token>`。API Key 主体一律 `403`。
- 请求与 URL **不得**携带 tenant / actor / owner；服务端忽略并删除这类入站头。
- 权限码：

| 接口 | 权限码 |
| --- | --- |
| `/pods` | `modeldev:execution:get` |
| `/events` | `modeldev:execution:get` |
| `/resource-metrics` | `modeldev:execution:get` |
| `/logs` | `modeldev:execution:logs` |
| `/logs/stream` | `modeldev:execution:logs` |

### 2.2 响应头

所有 modeldev 读接口返回 `Cache-Control: no-store`、`Pragma: no-cache`、`Referrer-Policy: no-referrer`，响应不可缓存。

### 2.3 错误响应

```json
{"code":412,"reason":"ARTIFACT_NOT_PUBLISHED","message":"artifact unavailable","metadata":{"request_id":"7abb6015-539b-44d5-9447-e2d9b05921aa"}}
```

`code` + `reason` 是稳定的分支依据；`message` 是英文说明文案，不保证措辞稳定。`metadata.request_id` 是排查用的关联 ID，响应中可能出现也可能缺省。

### 2.4 query 参数命名不统一

五个接口的参数命名风格不一致，且**服务端不做大小写或写法纠正**：

| 接口 | 参数 | 风格 |
| --- | --- | --- |
| `/pods` | `layer`、`includeRecycled` | 驼峰 |
| `/events` | `scope` | 单词 |
| `/logs` | `tail_lines`、`max_bytes`、`logId`、`pod_uid`/`podUid`、`container`、`previous` | snake + 驼峰混用 |
| `/logs/stream` | `pod_uid`/`podUid`、`container`、`previous`、`sinceBytes` | 驼峰为主 |
| `/resource-metrics` | `pod_uid`、`container`、`start`、`end`、`step_seconds`、`metric_codes` | 全 snake |

Pod 定位参数在 `/logs` 与 `/logs/stream` 上同时接受 `pod_uid`（推荐）和 `podUid`（兼容别名），两者不能同时出现；`/resource-metrics` 只认 `pod_uid`；`/logs/stream` 上是 `sinceBytes`，写成 `since_bytes` 会被拒。

对所有接口统一适用：

- **未知参数返回 `400 INVALID_MODELDEV_QUERY`**，不静默忽略。
- **重复参数返回 `400`**（`?scope=key&scope=all` 也是 400）。
- 请求体必须为空；URL 末尾的裸 `?` 也返回 `400`。

### 2.5 字段输出规则

这五个接口的响应有以下实测规则：

- **非 `optional` 字段总是输出**，空值是：字符串 `""`、数值 `0`、布尔 `false`、数组 `[]`、消息型 `null`（如容器的 `last_state`）。
- **`optional` 字段未赋值时不输出该键**，例如 `points[].value`、`points[].gap_reason`、容器与 Pod 的 `exit_code`。所以"这个键不存在"本身就是一种信息。

字段名是 snake_case，以本文各节的示例为准。

### 2.6 时间

时间字段统一 `RFC3339Nano` UTC 字符串，如 `2026-10-27T04:25:00.123456Z`。

本组接口没有 `int64`/`uint64` 字段；唯一的数值字符串是 `points[].value`，按设计就是十进制文本（见 §6.3）。

---

## 3. 容器组 `GET .../pods`

```
GET /api/v1/modeldev/executions/{execution_id}/pods?layer=all&includeRecycled=true
```

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `layer` | 否 | `all`（缺省值）/ `trainer`（训练容器）/ `pipeline`（流水线步骤）。其它值 `400` |
| `includeRecycled` | 否 | `true` / `false`，缺省 `false`。**默认不返回已回收的 Pod**；`true` 才会把已回收的 Pod 并入结果 |

### 3.1 响应

```json
{
  "pods": [
    {
      "pod_name": "md-rtttx-2081272040",
      "pod_uid": "8d0e4f4c-5a5f-4a1f-9c3d-2b1a0e6f7c11",
      "pod_ip": "10.244.3.51",
      "phase": "Succeeded",
      "ready": false,
      "node_name": "node3",
      "restarts": 0,
      "created_at": "2026-10-23T09:55:20Z",
      "start_time": "2026-10-23T09:55:22Z",
      "deletion_timestamp": "",
      "layer": "TRAINER",
      "availability": "LIVE",
      "step_name": "",
      "attempt": 0,
      "job_index": 0,
      "log_id": "d2889863-4286-59e7-ae26-fc3ed5359a48",
      "log_available": true,
      "containers": [
        {"name": "node", "type": "container", "state": "terminated", "reason": "Completed",
         "message": "", "restart_count": 0, "exit_code": 0,
         "started_at": "2026-10-23T09:55:22Z", "finished_at": "2026-10-23T10:12:01Z",
         "last_state": null}
      ],
      "init_containers": [],
      "ephemeral_containers": [],
      "api_present": true,
      "exit_code": 0,
      "metric_instance_id": "23da0705-7fd7-44f0-89e4-e40fc2400115"
    }
  ],
  "observed_at": "2026-10-27T04:25:00Z",
  "availability": "LIVE"
}
```

已回收的 Pod 会保留行但字段大面积为空（`pod_ip`/`node_name`/`phase` 为 `""`、时间字段为 `""`、`availability` 为 `RECYCLED`），因为 Kubernetes 对象已不存在，只剩持久化观测。这是 §2.5 规则的实际体现。

### 3.2 Pod 字段

| 字段 | 说明 |
| --- | --- |
| `layer` | `TRAINER` / `PIPELINE` |
| `availability` | 单个 Pod 的存在状态，取值见 §3.3 |
| `phase` | **仅** Kubernetes 原始 phase；它不是状态机，已回收或从未创建的 Pod 由 `availability` 表达，`phase` 可能是 `""` 或历史值 |
| `restarts` | Pod 的重启次数 |
| `exit_code` | **`node`（worker）容器的退出码**；Pod 里没有 `node` 容器时（`PIPELINE` 层）该键不输出，容器未终止时也不输出。它只描述 worker 进程，不是流水线步骤的退出码 |
| `api_present` | 该 Pod 的 Kubernetes API 对象当前是否仍存在；`false` 表示只有持久化记录可看 |
| `step_name` | 仅 `PIPELINE` 层有值：该 Pod 对应的 **Argo 节点名**，见 §3.4 |
| `job_index` / `attempt` | 多机训练的 rank 与重启次数（来自 JobSet 标签） |
| `log_id` | 该 Pod 的不透明日志凭证，规范 UUID；可作为 `/logs` 的 `logId` 回传 |
| `log_available` | 该 Pod 当前是否有可读的日志来源。为 `false` 时 `/logs` 返回 `412` |
| `containers` / `init_containers` / `ephemeral_containers` | 三类容器分列：普通容器、init 容器、临时调试容器 |
| `metric_instance_id` | 该 Pod 在可观测侧上报的实例 ID，服务端内部使用，调用方不传；早于指标标签的历史 Pod 为 `""` |

容器对象字段：

| 字段 | 说明 |
| --- | --- |
| `name` | 容器名 |
| `type` | `container` / `init_container` / `ephemeral` |
| `state` | `running` / `waiting` / `terminated` / `unknown`（Kubernetes 原始词） |
| `reason` | 如 `Completed`、`OOMKilled`、`CrashLoopBackOff`、`ImagePullBackOff`；可为 `""` |
| `message` | 容器状态附带信息，可为 `""` |
| `restart_count` | 单容器重启次数 |
| `exit_code` | 未终止时该键不输出 |
| `started_at` / `finished_at` | 未开始或未结束时为 `""` |
| `last_state` | 上一次终止实例的容器状态，结构同本表；`null` 表示没有上一次实例 |

> `PIPELINE` 层的 Pod 通常有两个容器：`wait`（流程控制）和 `main`（实际步骤）。`/logs` 与 `/logs/stream` 两个都可读（见 §5）；`/resource-metrics` 只接受 `main`（见 §6.2）。

### 3.3 `availability` 取值

| 值 | 含义 |
| --- | --- |
| `LIVE` | Pod 仍存在 |
| `RECYCLED` | 曾存在，已被回收 |
| `NEVER_CREATED` | 从未创建（任务未走到该阶段） |
| `REJECTED` | 创建被拒绝 |
| `ORPHANED` | 已不属于当前执行但仍可观测 |
| `CREATION_PENDING` | 创建中 |
| `UNSPECIFIED` | 无法判定 |

顶层 `availability` 描述**本次查询整体能证明什么**，与单个 Pod 的 `availability` 是两个不同层级的字段。当所有 worker Pod 都只来自持久化观测时，顶层为 `RECYCLED`，即使请求未传 `includeRecycled`。

### 3.4 `step_name` 与受管步骤角色

`step_name` 是流水线控制器写在该 Pod 上的 **Argo 节点名**（`workflows.argoproj.io/node-name`），不是受管步骤的 role。实测取值形如：

```
md-rtttx.root-driver
md-rtttx.root.exit-handler-1.prepare.executor(0)
md-rtttx.root.exit-handler-1.train-wait-driver
md-rtttx.root.exit-handler-1.createpvc
md-rtttx.root.exit-handler-1.workspace-name.executor(0)
md-rtttx.root.exit-handler-1.onExit.finalize-close.executor(0)
```

所以 `layer=pipeline` 的行里既有受管步骤对应的节点，也有主流程之外的系统节点（`*-driver`、`createpvc`、`workspace-name`、`exit-handler` 等）。前端若要把它与详情 `steps[]` 对齐，只能按 `step_name` 中的角色片段（`prepare`、`train-wait`、`collect`、`publish`、`close`、`export`、`verify`、`register`）做子串匹配，接口不保证节点与受管步骤一一对应。

受管步骤的角色序列由执行 `kind` 决定，与 `step_name` 无关：

| kind | `steps[].role` 序列（即执行顺序） |
| --- | --- |
| `GENERAL_TRAINING` | `prepare` → `train-wait` → `collect` → `publish` → `close` |
| `FINETUNING` | `prepare` → `train-wait` → `export` → `verify` → `collect` → `publish` → `register` → `close` |

微调未填模型登记目标时，`register` 只确认产物、不登记模型，运行结束后状态同样是 `SUCCEEDED`（2026-10-27 起）。

---

## 4. 事件 `GET .../events`

```
GET /api/v1/modeldev/executions/{execution_id}/events?scope=key
```

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `scope` | 否 | `key`（缺省值）/ `all` / `warning`。其它值 `400` |

| 值 | 返回内容 |
| --- | --- |
| `key` | 所有 `Warning` 事件，加上非 Pod 对象（Job / JobSet / TrainJob / Workflow）的 `Normal` 事件 |
| `warning` | 只返回 `Warning` |
| `all` | 不过滤 |

### 4.1 响应

```json
{
  "events": [
    {
      "type": "Warning",
      "reason": "FailedScheduling",
      "message": "0/3 nodes are available: 3 Insufficient nvidia.com/gpu.",
      "first_timestamp": "2026-10-23T09:56:30Z",
      "last_timestamp": "2026-10-23T09:58:10Z",
      "count": 5,
      "source": "default-scheduler",
      "involved_kind": "Pod",
      "involved_name": "md-rtttx-2081272040",
      "involved_uid": "8d0e4f4c-…",
      "field_path": "spec.containers{node}",
      "reporting_instance": "node3"
    }
  ],
  "observed_at": "2026-10-27T04:25:00Z",
  "retention_hint": "1h",
  "truncated": false
}
```

| 字段 | 说明 |
| --- | --- |
| `type` | `Normal` / `Warning` |
| `reason` | 事件原因，如 `FailedScheduling`、`BackOff` |
| `message` | 事件文本，取自 Kubernetes Event 的 `message` 字段 |
| `first_timestamp` / `last_timestamp` | 同一事件首次与最近一次发生时间 |
| `count` | 该事件被聚合的次数 |
| `source` | 上报组件，如 `default-scheduler`、`volcano-scheduler`、`kubelet` |
| `involved_kind` / `involved_name` / `involved_uid` | 事件关联对象 |
| `field_path` | 关联对象内的字段路径，可为 `""` |
| `reporting_instance` | 上报组件所在节点，可为 `""` |
| `retention_hint` | 当前 `1h` |
| `truncated` | 本次结果是否被截断 |

### 4.2 边界

- 事件来自 Kubernetes Event 对象，**默认保留约 1 小时**。历史执行返回 `events: []` 是正常结果，不是错误。
- 空结果**不能区分**"从未产生"与"已过期"，本期不提供该区分。
- 更长的历史需要 event-exporter 或落库方案，本期不提供。
- 事件与执行日志是两个独立数据源。
- PodGroup / Volcano 排队事件按执行冻结的运行时计划条件化：计划里配了 PodGroup 才会出现，没配不会报错。

---

## 5. 日志

### 5.1 快照 `GET .../logs`

```
GET /api/v1/modeldev/executions/{execution_id}/logs?tail_lines=200&max_bytes=16384
```

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `tail_lines` | 否 | 1–1000，缺省 200 |
| `max_bytes` | 否 | 1–65536，缺省 16384 |
| `pod_uid` | 否 | 规范 UUID，取自 `/pods` 的 `pod_uid`。不给时服务端解析该执行的 TRAINER Pod（优先归档、取最后一次尝试），见下方兼容路径；兼容别名 `podUid`，两者不可同时给 |
| `container` | 否 | 小写 Kubernetes 容器名，须是该 Pod 的 `containers[].name` 之一。worker 为 `node`；`PIPELINE` 层的 Pod 通常有 `wait` 与 `main` 两个容器，**两者都可读**（实测均 200）。**不能单独给**，必须配 `pod_uid` |
| `logId` | 否 | 规范 UUID，取自 `/pods` 的 `log_id`；给了就必须与该 Pod 匹配，否则 `404` |
| `previous` | 否 | `true` 读**上一次**终止实例的日志；没有上次实例时 `404` |

选择规则：

| `pod_uid` | `container` | 结果 |
| --- | --- | --- |
| 不给 | 不给 | 兼容路径：服务端解析该执行的 TRAINER Pod（归档日志优先），读取其 `node` 容器日志 |
| 给 | 不给 | 读取该 Pod 的缺省容器（worker = `node`，step = `main`）；实测 **200** |
| 给 | 给 | 读取该 Pod 的该容器 |
| 不给 | 给 | **`412 ARTIFACT_NOT_PUBLISHED`**（实测） |

`container` 给了一个该 Pod 上不存在的名字时返回 `503`，不是 `400`/`404`（见下方错误表）。

响应：

```json
{
  "log_id": "d2889863-4286-59e7-ae26-fc3ed5359a48",
  "lines": [
    {"timestamp": "2026-10-23T08:40:01.123456Z", "text": "epoch 1/5 loss=0.42"}
  ],
  "truncated": true,
  "observed_at": "2026-10-23T09:10:12.345678Z",
  "source_kind": "TRAINER",
  "container_name": "node",
  "previous": false,
  "available": true
}
```

| 字段 | 说明 |
| --- | --- |
| `lines` | 日志行，`timestamp` + `text`；`text` 不含换行符 |
| `source_kind` | `TRAINER`（训练日志）/ `KFP_STEP`（流水线步骤日志） |
| `container_name` | 实际读取的容器名 |
| `previous` | 回显请求 |
| `available` | 200 响应里恒为 `true`；"不可用"由 `412` 表达，不是靠这个字段 |
| `truncated` | 结果是否被 `tail_lines` / `max_bytes` 截断 |
| `log_id` | 本次读取的来源凭证 |
| `observed_at` | 服务端读取时刻 |

错误：

| HTTP | `reason` | 场景 |
| --- | --- | --- |
| `400` | `INVALID_MODELDEV_QUERY` | 参数越界、未知参数、重复参数、非规范 UUID、请求体非空 |
| `401` | `INVALID_LOGIN` | 未认证 |
| `403` | `FORBIDDEN` | 无权限 / 数据范围不足 |
| `404` | `RESOURCE_NOT_FOUND` | 执行不存在；Pod 不属于该执行；`previous=true` 但没有上次实例；`logId` 不匹配 |
| `412` | `ARTIFACT_NOT_PUBLISHED` | **没有可读的日志来源**：日志尚未产出、容器已回收、只给了 `container`、或 step Pod 已消失且无归档 |
| `499` | `MODELDEV_QUERY_CANCELED` | 请求被取消 |
| `503` | `MODELDEV_QUERY_UNAVAILABLE` | 下游不可用；**Pod 上不存在该 `container` 时也返回此码**（实测传一个不存在的容器名得到 503，不是 400/404） |
| `504` | `MODELDEV_QUERY_TIMEOUT` | 查询超时 |

> 返回 `412` 是**设计行为**：训练容器日志在容器回收后不再可读。它表示"这个来源没有日志"，不表示服务故障。

### 5.2 日志流 `GET .../logs/stream`（SSE）

```
GET /api/v1/modeldev/executions/{execution_id}/logs/stream?pod_uid=…&container=main&previous=false&sinceBytes=115
```

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `pod_uid` | 否 | 规范 UUID；兼容别名 `podUid`，两者不可同时给。省略时服务端解析该执行**存活**的 TRAINER Pod |
| `container` | 否 | 小写 Kubernetes 容器名，语义同 `/logs`（见 §5.1）；省略时按来源取缺省 |
| `previous` | 否 | `true` / `false` |
| `sinceBytes` | 否 | 续传偏移。整数 `0`–`2^30`，必须是规范十进制（`01`、`+1`、负数、超界均 `400`）。`0` 等价于不传 |

`Content-Type: text/event-stream`。

#### 帧格式

```
: ready

: heartbeat

event: log
data: {"text":"epoch 1/5 loss=0.42","offset":115}

event: log
data: {"text":"epoch 2/5 loss=0.31","offset":268}

event: end
data: {"reason":"LOG_ENDED"}
```

| 帧 | 说明 |
| --- | --- |
| `: ready` | 注释帧，表示连接已建立、后续是日志数据。总是首帧 |
| `: heartbeat` | 注释帧，约每 15 秒一次 |
| `event: log` | 一行日志。`text` 是一行文本，`offset` 是该行在日志流中的累计字节偏移，也是 `sinceBytes` 的取值来源 |
| `event: end` | 流正常结束 |
| `event: error` | 连接建立后发生的终止性错误 |

`text` 可能为空字符串，此时 `offset` 仍然有效（多字节字符被帧边界切开的情况）。

#### `end` / `error` 的 `reason`

两个帧共用同一套 reason 白名单，另有几个只出现在特定帧：

| `reason` | 可出现在 | 含义 |
| --- | --- | --- |
| `LOG_ENDED` | `end` | 日志正常结束（容器退出） |
| `TIME_LIMIT` | `end` | 单条连接的生命周期（10 分钟）到期 |
| `LIFETIME_EXCEEDED` | `end` | 同上，上游侧的生命周期 |
| `POD_GONE` | `end` / `error` | Pod 已消失 |
| `OFFSET_INVALID` | `end` / `error` | 传入的 `sinceBytes` 已失效（日志轮转或 Pod 重建） |
| `PREVIOUS_UNAVAILABLE` | `end` / `error` | 请求了上次实例但不可读 |
| `STEP_LOGS_UNAVAILABLE` | `end` / `error` | step 日志不可用（Pod 已消失且本期无归档读取） |
| `LOG_UNAVAILABLE` | `end` / `error` | 兜底不可读 |
| `EXECUTION_LOG_UPSTREAM_PROTOCOL` | `error` | 上游帧不符合协议，连接不可继续 |

#### 连接前的失败（HTTP 状态码，不是 SSE 帧）

失败语义分两段：**首帧 `: ready` 之前**的错误以 HTTP 状态码返回；**之后**的错误以 `event: error` 返回。已建立的流不会回传 HTTP 错误，异常断开即终止。

| HTTP | `reason` | 场景 |
| --- | --- | --- |
| `400` | `INVALID_MODELDEV_QUERY` | 参数不合法（含非规范 UUID、非规范 `sinceBytes`） |
| `401` | `INVALID_LOGIN` | 未认证 |
| `404` | `RESOURCE_NOT_FOUND` | 执行不存在或不可见（不区分无权限，避免泄露资源是否存在） |
| `412` | 见上表 | 打开失败，`reason` 取自同一白名单 |
| `429` | `EXECUTION_LOG_CONNECTION_LIMIT` | 并发连接超限 |
| `503` | `EXECUTION_LOG_*` | 上游不可用、打开超时或协议不符；`reason` 是若干内部常量之一（如 `EXECUTION_LOG_UPSTREAM_UNAVAILABLE`、`EXECUTION_LOG_OPEN_TIMEOUT`） |

打开阶段的超时也归入 `503`；服务端对打开有独立超时，超时不会返回部分帧。

> 本小节的 `reason` 常量取自实现，未在真实链路逐一复核；分支请按 HTTP 状态码为主、`reason` 为辅。

#### 并发上限

| 维度 | 上限 |
| --- | --- |
| 单执行（execution） | 3 条流 |
| 单租户 | 10 条流 |
| 进程全局 | 100 条流 |

超限返回 `429 EXECUTION_LOG_CONNECTION_LIMIT`。计数按执行维度，一个执行可能跨多个 worker Pod，所以上限不在 Pod 上。

#### 续传语义

- 连接的生命周期上限是 **10 分钟**，到期以 `event: end` + `reason: TIME_LIMIT` 正常收口，属于设计行为。
- `sinceBytes` 给的是"从哪个字节偏移之后继续"，取值就是最后一个 `event: log` 的 `offset`。
- `offset` 失效（日志轮转、Pod 重建）时，服务端在**首帧之前**返回 `412 OFFSET_INVALID`，不会先发出一部分再报错。
- `end` 的 `LOG_ENDED` / `POD_GONE` 表示日志源已结束，不再有后续数据。

---

## 6. 资源曲线 `GET .../resource-metrics`

```
GET /api/v1/modeldev/executions/{execution_id}/resource-metrics
    ?pod_uid=…&container=main&start=2026-10-27T00:00:00Z&end=2026-10-27T01:00:00Z
    &step_seconds=60&metric_codes=memory.working_set_bytes,cpu.usage_cores
```

### 6.1 一次请求只返回一个 Pod 的一个容器

上游可观测合同一次只解析一个物理工作负载，**并行 step 的曲线不能合并成一次调用**。执行级聚合不是本接口的能力。

### 6.2 参数

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `pod_uid` | **是** | 规范 UUID，取自 `/pods` 的 `pod_uid`。用 UID 而非名字：Pod 重建后不继承旧曲线 |
| `container` | **是** | 必须与层匹配：`TRAINER` 层只能 `node`，`PIPELINE` 层只能 `main`。传错返回 `400`，不会静默返回空曲线 |
| `start` / `end` | **是** | RFC3339 但必须是**整秒**（带小数秒如 `00:00:00.5Z` 返回 `400`）；`end` 不早于 `start`，跨度 ≤ 24h，`end` 不晚于当前时间 |
| `step_seconds` | 否 | 缺省 `30`；允许 30–3600 且必须是 30 的整数倍 |
| `metric_codes` | **是** | 逗号分隔，1–2 个，只能取 `cpu.usage_cores` / `memory.working_set_bytes`，不得重复 |

点数上限：`(end-start)/step_seconds + 1 ≤ 3000`。

参数不合法一律 `400 INVALID_MODELDEV_QUERY`。

### 6.3 响应

```json
{
  "execution_id": "23da0705-7fd7-44f0-89e4-e40fc2400115",
  "pod_uid": "8d0e4f4c-5a5f-4a1f-9c3d-2b1a0e6f7c11",
  "pod_name": "md-rtttx-2081272040",
  "container": "node",
  "start": "2026-10-27T00:00:00Z",
  "end": "2026-10-27T01:00:00Z",
  "step_seconds": 60,
  "observed_at": "2026-10-27T01:00:05Z",
  "source": "PROMETHEUS_CADVISOR_PROJECTION",
  "retention_hint": "24h",
  "device_mode": "cpu",
  "gpu_requested": false,
  "gpu_available": false,
  "series": [
    {
      "metric_code": "memory.working_set_bytes",
      "unit": "bytes",
      "status": "PARTIAL",
      "points": [
        {"timestamp": "2026-10-27T00:00:00Z", "value": "65331200"},
        {"timestamp": "2026-10-27T00:01:00Z", "gap_reason": "NO_DATA"}
      ]
    },
    {
      "metric_code": "cpu.usage_cores",
      "unit": "cores",
      "status": "NO_DATA",
      "points": [
        {"timestamp": "2026-10-27T00:00:00Z", "gap_reason": "NO_DATA"}
      ]
    }
  ]
}
```

| 字段 | 说明 |
| --- | --- |
| `execution_id` / `pod_uid` / `pod_name` / `container` | 回显本次查询定位到的对象 |
| `start` / `end` / `step_seconds` | 回显并规范化后的窗口与步长 |
| `observed_at` | BFF 组装响应的时刻 |
| `source` | `PROMETHEUS_CADVISOR_PROJECTION`：数据来自 recording rule 对 cAdvisor 的投影，不是直读 cAdvisor |
| `retention_hint` | 当前 `24h` |
| `device_mode` | `cpu` / `gpu`，来自执行冻结的算力选择 |
| `gpu_requested` | 该执行是否申请了 GPU |
| `gpu_available` | 是否真有 GPU 指标序列。**当前环境没有 GPU exporter，恒为 `false`** |
| `series[].metric_code` / `unit` | 与请求的 `metric_codes` 一一对应、同序 |
| `series[].status` | `OK`（全部点有值）/ `PARTIAL`（部分点有值）/ `NO_DATA`（无点有值），由有效点个数决定 |
| `points[].timestamp` | 从 `start` 起按 `step_seconds` 递增，个数恒为 `(end-start)/step_seconds + 1` |
| `points[].value` | 十进制字符串（如 `"65331200"`、`"1.72"`），使得大字节数不失真 |
| `points[].gap_reason` | 缺口原因：`NO_DATA` / `AMBIGUOUS_SOURCE` / `INVALID_VALUE` / `EXPORTER_UNAVAILABLE` |

**`value` 与 `gap_reason` 互斥**：有 `value` 就没有 `gap_reason`，有 `gap_reason` 就没有 `value` 键（两个都是 `optional` 字段，见 §2.5）。缺口点表示该时刻**没有测得数值**，不代表 0。

### 6.4 已知限制

- **`cpu.usage_cores` 对 step Pod 通常是整条 `NO_DATA`**：该指标用 `rate(...[5m])` 求值，要求同一 Pod 连续存活 ≥5 分钟，而 step Pod 一般只活 10–30 秒。这是采集规则的物理限制，不是接口缺陷。worker Pod 长时间运行可正常出点。
- 每租户并发上限 4；超出的请求在 BFF 排队等待，等待期间若请求上下文超时则返回 `504`。上游仍可能直接返回 `429 MODELDEV_METRIC_CONCURRENCY_LIMIT`。
- `retention_hint` 之外的窗口数据点标 `NO_DATA`，接口不报错。
- GPU 曲线形状尚未实测（环境里唯一的 GPU 执行 Pod 已被回收）。

### 6.5 错误

| HTTP | `reason` | 场景 |
| --- | --- | --- |
| `400` | `INVALID_MODELDEV_QUERY` | 参数不合法（含 layer 与容器名不匹配、Pod 不含该容器） |
| `401` | `INVALID_LOGIN` | 未认证 |
| `403` | `FORBIDDEN` | 无权限 / 数据范围不足 |
| `404` | `RESOURCE_NOT_FOUND` | 执行不存在或 Pod 不属于该执行 |
| `412` | `MODELDEV_METRIC_SCOPE_UNAVAILABLE` | 执行缺少冻结的集群 / 命名空间，或无法确定该 Pod 的上报实例 |
| `429` | `MODELDEV_METRIC_CONCURRENCY_LIMIT` | 并发超限（正常路径由 BFF 先排队） |
| `499` | `MODELDEV_METRIC_CANCELED` | 请求被取消 |
| `502` | `MODELDEV_METRIC_RESPONSE_INVALID` | 上游响应形状不符 |
| `503` | `MODELDEV_METRIC_UNAVAILABLE` | 指标服务不可用 |
| `504` | `MODELDEV_METRIC_TIMEOUT` | 查询超时（该接口服务端预算 10s） |

---

## 7. 普通训练 vs 微调

五个接口完全共用，不需要按 kind 分叉。差异只有：

| 差异点 | 普通训练 | 微调 |
| --- | --- | --- |
| 受管步骤 | 5 步（见 §3.4） | 8 步（见 §3.4） |
| 详情 `steps[]` | 5 条 | 8 条 |
| `layer=pipeline` 的节点 | 受管步骤节点 + 系统节点 | 同上，另有 `export` / `verify` / `register` 对应的节点 |

不变的部分：

- worker 容器名恒为 `node`；`/resource-metrics` 只接受 `main` 作为 step 曲线容器，而 `/logs` 与 `/logs/stream` 接受该 Pod 的任一容器（`wait` / `main` 均可）。
- 指标码只有 `cpu.usage_cores` 与 `memory.working_set_bytes`。
- 事件、容器组、日志的字段与错误语义完全一致。

`export` / `verify` / `register` 阶段的失败体现在 `layer=pipeline` 中 `step_name` 含该角色片段的节点上，日志在其容器里；worker 日志在那时通常已正常结束。

---

## 8. 接口间的输入依赖

纯数据依赖，不含调用顺序建议：

| 接口 | 依赖 |
| --- | --- |
| `/logs` | `pod_uid`（或别名 `podUid`）取自 `/pods` 的 `pod_uid`；省略时服务端解析 TRAINER Pod。`container` 取自该 Pod 的 `containers[].name`（worker 通常 `node`，step 通常 `wait` / `main`）；`logId` 取自该 Pod 的 `log_id` |
| `/logs/stream` | 同 `/logs`，但缺省只解析**存活**的 TRAINER Pod；`sinceBytes` 取自上一次流中最后一个 `event: log` 的 `offset` |
| `/resource-metrics` | `pod_uid` 取自 `/pods` 的 `pod_uid`；`container` 由 `layer` 决定且被服务端钉死（`TRAINER`=`node`，`PIPELINE`=`main`） |
| `/pods`、`/events` | 只依赖路径上的 `execution_id` |

`/pods` 是唯一能提供 `pod_uid` / `container` / `log_id` / `step_name` 的接口，另外三个接口的定位参数都从它来。

---

## 9. 已知限制汇总

| 项 | 限制 |
| --- | --- |
| 事件保留窗口 | `1h`（`retention_hint`）；更早的事件不返回，且无法区分"从未产生"与"已过期" |
| 指标保留窗口 | `24h`（`retention_hint`）；更早的窗口数据点标 `NO_DATA` |
| 容器回收后无日志 | `/logs` 返回 `412 ARTIFACT_NOT_PUBLISHED` |
| step Pod 消失且无归档 | `/logs` 同样返回 `412 ARTIFACT_NOT_PUBLISHED`；日志流则用 `STEP_LOGS_UNAVAILABLE` 作 reason（`412` 或 `end`/`error` 帧） |
| step Pod 的 `cpu.usage_cores` | 通常整条 `NO_DATA`（`rate(...[5m])` 与 step 存活时长不匹配） |
| GPU 指标 | `gpu_requested=true` 时 `gpu_available` 仍为 `false`（无 exporter）；曲线形状未实测 |
| 日志流生命周期 | 单连接 10 分钟，到期以 `end/TIME_LIMIT` 收口 |
| 日志流并发 | 单执行 3 / 单租户 10 / 进程 100，超限 `429` |
| 指标并发 | 每租户 4，超出的请求排队 |
| 指标粒度 | 采集间隔 30s；`step_seconds` 最小 30，窗口 ≤ 24h，点数 ≤ 3000 |
| 一次一个 Pod | `/resource-metrics` 不支持执行级聚合 |
| `pipeline` 节点与受管步骤 | 不一一对应：`layer=pipeline` 还包含 `*-driver`、`createpvc`、`workspace-name`、`exit-handler` 等系统节点 |
| Pod 的 `exit_code` | 只描述 `node`（worker）容器；`PIPELINE` 层没有 `node` 容器，该键不输出 |

---

## 10. 验证状态（2026-10-27，66 集群）

已实测：

- `/pods`、`/events`、`/logs`、`/resource-metrics` 正例均 **200**；不存在的执行返回 **404 `RESOURCE_NOT_FOUND`**。
- `/resource-metrics` 正例返回真实内存值（`65331200`，约 62.3 MiB，取自前次验收），`status=PARTIAL`，未测到点的位置标 `NO_DATA`；缺口点确实不含 `value` 键。
- 参数类负例全部返回预期状态码：缺 `pod_uid`、非规范 UUID、未登记指标码、layer 与容器名不匹配、容器名大写、`step_seconds` 取 15 / 45、窗口 >24h、`end` 晚于当前、重复指标码、未知 query 参数、Pod 不属于该执行、执行不存在、无 token。
- `/pods` 的 `includeRecycled` 行为已核实：缺省不返回已回收 Pod，`true` 时返回且字段大面积为空（`pod_ip`/`node_name`/`phase` 为 `""`）。
- `/pods` 的 `step_name` 已核实为 Argo 节点名，且 `layer=pipeline` 含系统节点（实测一条执行 11 个节点，多于其 5 个受管步骤）。
- `/pods` 的分层 `exit_code` 已核实：`TRAINER` 层 Pod 带该键，`PIPELINE` 层不带；容器的 `exit_code` 在未赋值时不输出。
- `/logs` 的兼容路径（不给 `podUid`/`container`）已核实可用，返回训练日志。
- `/logs` 的四种选择组合已核实：`podUid` 单独给 **200**；`podUid`+`container` **200**；只给 `container` **412 `ARTIFACT_NOT_PUBLISHED`**；`podUid` 指向不属于该执行的 Pod 返回 **404 `RESOURCE_NOT_FOUND`**。
- `/logs/stream` 的帧序列已在真实链路实测：`: ready` → `event: log`（`data` 含 `text` 与 `offset`）→ `event: end` + `{"reason":"LOG_ENDED"}`。执行没有可读日志源时，首帧之前直接返回 **`412` + `{"reason":"LOG_UNAVAILABLE"}`**（不是 SSE 帧）；指定 `container` 但 Pod 已消失时返回 **`412 POD_GONE`**。
- 保留 5 个请求头行为已核实：列表、详情、`/pods`、`/events`、`/logs` 均返回 `Cache-Control: no-store`、`Pragma: no-cache`、`Referrer-Policy: no-referrer` 三条。
- 列表 `total` 在四个列表接口（实现、输入版本、算力预设、执行）上均实测为**十进制字符串**而非数字。

未验证：

- **`/logs/stream` 的 `TIME_LIMIT` 收口与 `sinceBytes` 续传未在真实链路实测**（帧格式与 `LOG_ENDED` 已实测，见上）；`OFFSET_INVALID`、`PREVIOUS_UNAVAILABLE`、`STEP_LOGS_UNAVAILABLE` 等 reason 的依据仍是实现与包内测试。
- `/logs/stream` 的并发上限（`429`）未压测。
- GPU 曲线的响应形状（环境中唯一的 GPU 执行 Pod 已被回收）。
- 500 Pod 规模的容量回归。

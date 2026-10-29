# Governance Inference 对接说明

本文对应 `codex/gov-main-sync-20261023` 分支和 PR #40，面向前端、Inference owner、平台运维和权限运维。文档描述源码合同与接线步骤；接口目录、授权、证书和下游运行环境仍需在目标环境单独验收。

## 1. 接入边界

Inference 的管理面统一由 Governance BFF 提供，入口是 `/api/v1/inference/...`。创建、查询、更新、启停、删除、运行诊断、日志和访问策略都不能经 Higress 转发。Higress 只承载 OpenAI 兼容的数据面，例如 `/v1/completions`。

浏览器或业务前端只调用 Governance，使用租户用户 Bearer JWT。Governance 从已认证 Principal 重建租户、actor 和 request ID，再通过 TLS 1.3 双向 mTLS 调用 Inference owner；公网请求中的 tenant、actor、owner、plan、charges 或身份 header 不作为可信输入。API Key 不用于这组管理接口。

本批还包含两项兼容调整：推理和模型写入请求使用顶层扁平 JSON，不再使用 `{data:{...}}`；`GET /api/v1/models/{model_id}` 直接返回 Model 资源，不再增加 `model` 外层。Model 的 `page` 只要求为正数，`pageSize` 仍为 `1..100`。

## 2. 公网接口和权限

所有接口都要求有效租户用户、当前租户处于有效状态以及 `INFERENCE` 套餐模块。资源归属由 Governance 和下游 owner 校验，前端不能提交租户或操作人字段。

| 方法和路径 | 用途 | 权限 |
| --- | --- | --- |
| `GET /api/v1/inference/services` | 推理服务列表 | `inference:service:read` |
| `GET /api/v1/inference/services/{resource_id}` | 服务详情 | `inference:service:read` |
| `GET /api/v1/inference/operations/{operation_id}` | 操作详情 | `inference:service:read` |
| `GET /api/v1/inference/services/{resource_id}/operations` | 服务操作列表 | `inference:service:read` |
| `POST /api/v1/inference/services` | 创建 | `inference:service:create` |
| `POST /api/v1/inference/services/{resource_id}:delete` | 删除 | `inference:service:delete` |
| `POST /api/v1/inference/services/{resource_id}:update` | 更新 | `inference:service:update` |
| `POST /api/v1/inference/services/{resource_id}:start` | 启动 | `inference:service:start` |
| `POST /api/v1/inference/services/{resource_id}:stop` | 停止 | `inference:service:stop` |
| `POST /api/v1/inference/services/{resource_id}:restart` | 重启 | `inference:service:restart` |
| `GET /api/v1/inference/services/{resource_id}/runtime` | 运行时和调用健康 | `inference:service:read` |
| `GET /api/v1/inference/services/{resource_id}/metrics` | CPU、内存时间序列 | `inference:service:read` |
| `GET /api/v1/inference/services/{resource_id}/logs` | 一次性日志查询 | `inference:service:read` |
| `GET /api/v1/inference/services/{resource_id}/logs:stream` | SSE 日志流 | `inference:service:read` |
| `GET /api/v1/inference/services/{resource_id}/events` | 运行事件列表 | `inference:service:read` |
| `/api/v1/inference-policies` | AK 策略 CRUD | `inference:policy:*` |
| `POST /api/v1/inference-keys/{key_id}:bind-policy` | 绑定 AK 策略 | `inference:key:bind-policy` |

接口目录登记和权限关系不是自动授权。新增诊断路由使用 `scripts/ops/sql/register-inference-diagnostics-permissions.sql`；访问策略使用 `scripts/ops/sql/register-inference-policy-permissions.sql`。登记后仍需按目标租户、角色和套餐明确启用。

## 3. 运行诊断接口

### 3.1 运行时状态

`GET /api/v1/inference/services/{resource_id}/runtime` 返回：

```json
{
  "invocation_health": "healthy",
  "health_reason": "",
  "health_observed_at": "2026-10-23T08:00:00Z",
  "runtime_observed_at": "2026-10-23T08:00:02Z",
  "instances": [{"name":"inference-0","uid":"<pod-uid>","phase":"Running","containers":["engine"]}],
  "observed_at": "2026-10-23T08:00:02Z",
  "invocation_endpoint": "https://inference.example/v1/completions"
}
```

`invocation_endpoint` 只来自 Governance 受管配置，不能由请求传入。没有独立调用探针观测时，`health_observed_at` 可以为空；不能把运行时 Ready 当成推理调用成功。

运行诊断可能包含真实推理健康探测，owner RPC 使用独立的 15 秒预算，`/runtime` HTTP 请求预算为 20 秒；一次性日志查询也使用 15 秒 RPC 预算和 20 秒 HTTP 预算，其他普通 Inference RPC 仍为 3 秒。调用方更早的 deadline 和取消始终生效。

### 3.2 Prometheus 指标

请求示例：

```bash
curl -G "$GOV/api/v1/inference/services/$RESOURCE_ID/metrics" \
  -H "Authorization: Bearer $TOKEN" \
  --data-urlencode 'start=2026-10-23T07:00:00Z' \
  --data-urlencode 'end=2026-10-23T08:00:00Z' \
  --data-urlencode 'step=60'
```

查询参数只有 `start`、`end` 和 `step`。时间使用 RFC3339；省略 `end` 时取当前时间，省略 `start` 时取结束时间前一小时，省略 `step` 时为 60 秒。`step` 范围为 15～3600 秒，时间范围最多 24 小时且最多 1440 个区间，结束时间不能超过当前时间一分钟。响应是 `series` 数组，当前 series 为 `cpu`（单位 `cores`）和 `memory`（单位 `bytes`），每项包含 timestamp/value 点。

Prometheus 查询按 owner 返回的 Pod UID 和配置的 namespace 限定，不按 Pod 名称猜测历史实例。未配置指标源时返回依赖不可用；Prometheus 地址只在 Governance 服务端使用，不能暴露给浏览器。

`/metrics` HTTP 请求预算为 30 秒，覆盖一次 owner 运行诊断和两个各自有界的 Prometheus 查询；指标源未配置时，完成可信身份和租户映射校验后直接返回不可用，不触发健康探测。

### 3.3 日志查询和 SSE

一次性日志：

```text
GET /api/v1/inference/services/{resource_id}/logs
  ?instance=inference-0&container=engine
  &since=2026-10-23T07:55:00Z&until=2026-10-23T08:00:00Z
  &tail_lines=200&level=error
```

允许的参数为 `instance`、`container`、`since`、`until`、`tail_lines` 和 `level`。`instance` 必须使用 `/runtime` 返回的真实实例名称；多容器实例须同时选择返回的 `container`，不能使用推理服务 ID 代替 Pod 名称。`tail_lines` 为 1～1000；`level` 为 `trace/debug/info/warn/error/fatal/unknown`。响应为 `{items:[{timestamp,message,level}],truncated}`。日志等级由结构化 `level/severity` 或文本首字段归一化，无法判断时为 `unknown`。

SSE 使用相同的过滤参数：

```bash
curl -N -G "$GOV/api/v1/inference/services/$RESOURCE_ID/logs:stream" \
  --data-urlencode "instance=$INSTANCE" \
  --data-urlencode "container=$CONTAINER" \
  --data-urlencode 'level=error' \
  -H "Authorization: Bearer $TOKEN"
```

响应 `Content-Type` 为 `text/event-stream`。事件类型为 `log`、`heartbeat`、`done` 和 `error`；`log` 的 data 是 ProtoJSON 日志行，heartbeat 和 done 的 data 为空对象。单个连接最长 5 分钟，建立上游流最多等待 10 秒；进程最多 32 条并发流、每个租户最多 4 条。客户端应处理 heartbeat、断线和重复查询，不要把 SSE 断开解释为 owner 已完成。

上游返回首个原始日志帧即视为连接就绪，即使该帧因 `level` 过滤不会产生 `log` 事件；就绪确认后才开始应用等级过滤，后续仍持续发送匹配日志和 heartbeat。因此，`level=error` 不会因为上游持续发送 INFO 而在 10 秒后误报 503。

### 3.4 运行事件

`GET /api/v1/inference/services/{resource_id}/events` 支持 `since`、`until`、`type`、`page` 和 `pageSize`。`page` 必须为正数，`pageSize` 为 1～100；重复参数、未知参数、空值、非规范数字和与路径不一致的资源 ID 均拒绝。响应为 `{items:[...],total:"..."}`，事件项包含 `id/type/reason/message/timestamp/instance/count`。

## 4. AK 访问策略

策略是独立的同租户资源，控制允许调用的模型及 QPS、RPM、RPH。每个 AK 绑定
一个策略，多把 AK 绑定同一策略时分别计数。策略目录的创建、列表、详情、完整
更新、删除，以及 AK 绑定接口的报文和权限见 [推理调用 Key 和策略](inference-keys.md)。
旧的服务绑定策略入口已退役。

## 5. Inference owner 对接要求

Gov 使用当前 `go.mod` 锁定的 Inference API 模块版本，owner 必须同时发布兼容 RPC：

| 能力 | Inference API 接收 RPC/数据源 |
| --- | --- |
| 访问策略 | `GetServicePolicy`、`UpdateServicePolicy` |
| 运行时 | `GetRuntimeDiagnostics` |
| 日志 | `GetRuntimeLogs`、`StreamRuntimeLogs` |
| 事件 | `ListRuntimeEvents` |
| 指标 | `GetRuntimeDiagnostics` 返回真实 Pod UID；Gov 再按 UID 查询 Prometheus |

Gov → Inference 连接要求 TLS 1.3、双向证书和精确 DNS SAN。客户端身份为 `ani-governance`，服务端名称按受管配置固定为 `ani-inference`；禁止明文回退、通配符 SAN、透传公网 Authorization/身份 header 或使用用户提供的下游地址。下游必须继续校验租户、actor、资源归属和 RPC 方法授权。

## 6. Helm 和运行配置

默认诊断源关闭。启用后至少配置：

```yaml
downstream:
  inference:
    enabled: true
    address: ani-inference.ani-system.svc.cluster.local:7788
    serverName: ani-inference
    caSecret: ani-inference-ca
    clientCertificateSecret: governance-client-tls
    prometheusURL: http://prometheus.ani-infra.svc.cluster.local:9090
    runtimeNamespace: ani-system
    invocationEndpoint: https://inference.example/v1/completions
```

对应环境变量为 `ANI_INFERENCE_ADDR`、`ANI_INFERENCE_SERVER_NAME`、`ANI_INFERENCE_CA_FILE`、`ANI_INFERENCE_CERT_FILE`、`ANI_INFERENCE_KEY_FILE`、`ANI_INFERENCE_PROMETHEUS_URL`、`ANI_INFERENCE_RUNTIME_NAMESPACE` 和 `ANI_INFERENCE_INVOCATION_ENDPOINT`。`invocationEndpoint` 只能是无用户信息、无 query/fragment 且路径以 `/v1/completions` 结尾的 HTTP(S) 地址。

部署前确认 CA、客户端证书和 key 已放入 Chart 引用的 Secret；证书轮换后按当前部署策略重启 Pod，使启动时加载新证书。Prometheus 只供 Governance 后端访问，不能通过浏览器跨域直连。

## 7. 发布和验收顺序

1. 发布带上述 owner RPC 和消息的 Inference API，确认模块版本和服务端 mTLS listener 已就绪。策略接口要求 Inference 自有数据库已应用 `000023_inference_service_policies.sql`；仍处于第 21 版的环境须先补第 22 版，再应用第 23 版。以 owner 的迁移目录、实际表结构和迁移记录为准，更新 Gov 不会替 Inference 建表。
2. 部署 Governance，并设置 Inference 地址、server name、CA、客户端证书和可选诊断配置。
3. 执行 `admin sync-apis --dry-run`，审核 17 条 Inference 管理/诊断/策略路由后再执行同步。
4. 执行 `register-inference-permissions.sql`、`register-inference-diagnostics-permissions.sql` 和 `register-inference-policy-permissions.sql`；目录同步不会替角色授权。
5. 对明确的租户、`tenant:manager` 角色和已启用 `INFERENCE` 套餐执行 `enable-inference-access.sql`；策略权限按目标租户显式执行 `enable-inference-policy-access.sql`。
6. 用租户 JWT 分别验收服务详情、runtime、metrics、logs、SSE、events 和 policy；验证另一租户、API Key、伪造身份 header、未知 query、重复 key 和资源越权均被拒绝。
7. 分别验证 Governance 管理入口和 Higress `/v1/completions` 数据面，不能用一个入口的 200 响应代替另一个入口的验收。

本分支已验证 `make api` 和 `make check-generated`；常规编译、格式、布局和包测试通过。安全扫描曾因执行环境资源不足被 `govulncheck` 以 137 终止，数据库集成验收需要目标环境提供独占 PostgreSQL DSN。

## 8. 参考合同

- [Inference 扁平 HTTP 合同](inference-flat-api.md)
- [前端 HTTP 接口规范](frontend-http-api.md)
- [服务间调用规范](service-to-service-calls.md)
- [接口集成登记](../interface-integration-register.md)
- [API 权限运维](../api-role-permission-ops.md)

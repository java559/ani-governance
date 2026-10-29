# Inference 管理接口状态

Governance 当前通过 BFF 提供 Inference 管理接口。普通和 GPU 推理的创建、列表、详情、操作查询、更新、启停/重启及删除均使用 `/api/v1/inference/...`；BFF 对外遵循 `page`/`pageSize`、`items`/`total` 的 HTTP 合同，按租户与权限过滤后分页。Inference Go API 仅作为 Gov 的受信服务间依赖，不改变 Model 目录/版本/导入或 ACC、Resource 的职责。Higress 仍只承载 `/v1/completions` 等推理数据面，不承载这些管理路由。

## 算力规格创建和冻结更新

`GET /api/v1/compute-presets?usage=INFERENCE&page=1&pageSize=20` 复用统一目录；必须有
`inference:compute-preset:read` 和当前 `INFERENCE` 套餐。目录权限不替代创建的
`inference:service:create`；GPU 新建另外检查当前 `ACCELERATOR` 资格。
当前公网管理接口只接受租户用户 JWT，签名 API Key 仍返回 `USER_REQUIRED`。

`POST /api/v1/inference/services` 接受扁平 JSON 请求体，字段直接位于顶层，不接受 `data` 包装。必须带 UUID
`idempotency_key`、UUID `preset_id`、`name`、`model_version_id`、
`served_model_name`、`engine_image`、`engine_type`、完整 `command` 和正数
`replicas`。`runtime_mode` 默认 Deployment；`worker_replicas` 由正式 Inference
拓扑合同校验。旧 `cpu`、`memory`、`gpu` 输入即使为空也拒绝，可信身份、
Execution、charges 和未知字段同样拒绝。GPU LWS 在占额前拒绝。

新请求只解析一次 Res 单执行容器选择，冻结精确 `cpu_millis/memory_mib` 及完整来源；
CPU/GPU 统一返回 202、固定 resource/CREATE operation ID 和持久投递状态。
CPU 无 charges、GPU Usage 或退款；GPU 仅按 ACC 返回的唯一计量向量占额一次。
同键同意图复用原身份与快照，不重新访问 Res、Model 或新 GPU 资格。
202 和 owner 的 durable ACK 均不表示 Ready。

`POST /api/v1/inference/services/{resource_id}:update` 的顶层字段必填
`idempotency_key/resource_id/expected_generation/update_mask`。mask 只允许
`model_version_id`、`served_model_name`、`engine`；engine 是
`engine_image/engine_type/command` 三项整体替换。选中的值不可为空，mask 不可为空、
重复或包含未知路径；掩码外业务字段不写入。CPU/RAM/GPU、preset、副本、拓扑、
provider/endpoint 均冻结，原 CREATE 字节与 charges 不改。UPDATE/Start/Stop/Restart
沿 owner 既有幂等和当前 generation，无新的 Gov 账本，不重新解析规格或占退额。

DELETE 分配独立 operation ID，关联原 CREATE。只有锁内证明原 CREATE 从未发送
才本地取消；发送过或结果不明均投递受管 DELETE。Gov 从原快照派生内部关闭范围，
公网不能提供 scope。owner 尚无 service 的关闭仍可查询，并在重启后恢复；
被阻断的 CREATE 与 DELETE 各自保留操作事实。

详情额外返回原 `preset_id/preset_code/cpu_millis/memory_mib/selection_digest`；
原目录变更后仍来自受理快照。模型、引擎、命令和 generation 使用 owner 当前值。
操作查询增加 `gov_dispatch_state` 和 `refund_state`：CPU 为 `NONE`；GPU 为
`PENDING/APPLIED`，与 owner 的计算释放、最终清理分别显示。
原始 operation/resource ID 不因 ACK 丢失或重试改变。

## 升级顺序

先发布匹配的独立 Inference API 并确认正式下载，再升级 owner 根模块、显式运行
owner 新迁移、部署匹配 runtime，最后升级锁定同一 API 的 Gov。Gov 复用现有
QuotaOperation/charges/dispatch 表，不增加 CPU 账本或本次 Gov 数据库迁移；
非实例行的 `instance_contract_version=1` 默认值不表示 Inference 合同版本，
Inference 仅按 `inference-managed-v2` schema 解释。历史普通资源没有 Gov 原 CREATE
时保留 owner 原查询/删除路径，不回填受管身份或配额。权限目录先 dry-run 后显式同步，
角色/套餐同步与真实库迁移按部署授权执行。

关闭操作保留 owner 的真实 `phase/step`：`observe_release` 等待释放观察，
`recover_execution` 表示历史执行仍未知，`report_resource_released` 表示稳定释放事实
与报告已持久化、通知仍待本地收敛，`complete` 表示 owner 最终完成。
Gov 不用这些字符串计算退款；例如通知 ACK 丢失时允许 owner 尚在
`report_resource_released` 而 Gov `refund_state=APPLIED`，原事件重试不会二次退款。

## 推理 AK 策略

策略为同租户独立资源，绑定 AK 而非推理服务。完整管理合同、请求与权限见
[推理调用 Key 和策略](inference-keys.md)。旧 `/api/v1/inference/services/{resource_id}/policy`
接口已退役。管理请求走 Governance；Higress 只承载 `/v1/...` 推理。

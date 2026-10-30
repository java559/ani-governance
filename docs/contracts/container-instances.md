# 容器实例 HTTP 合同

本合同对应 ANI 容器实例联合批次。消息与 RPC 以 `api/protos/instance/service/v1/instance.proto` 和 admin BFF Proto 为准；源码能力与现场验收分别确认。前端只调用 Governance，不直接调用 Resource、ACC 或 Kubernetes。

## 身份与受理

租户实例、PVC 候选和配置查询入口要求已认证的 user 主体、非零租户及当前 INSTANCE 套餐与动作权限；身份和 Resource 租户由服务器映射。API Key 或平台主体不能冒充租户用户调用这一组接口。平台管理入口要求平台身份（tenant=0）及对应 permission，并经过现有 Casbin。租户管理员不能以平台权限码调用平台入口。请求不接受 tenant、namespace、actor、owner、GPU 执行参数、charges 或 FrozenSpec。

创建 `POST /api/v1/instances` 返回 202：

```json
{"data":{"idempotencyKey":"10000000-0000-4000-8000-000000000001","name":"工作区","imageId":"实际登记镜像ID","imageScope":"platform","presetId":"20000000-0000-4000-8000-000000000001","scheduling":{"pod_placement":"SPREAD_PREFERRED","priority_class_name":"batch-low"}}}
```

`scheduling` 可以省略；出现时至少含一项。仅允许 `pod_placement` 为 `NONE`、`COLOCATE_PREFERRED`、`SPREAD_PREFERRED`，以及非空 `priority_class_name`。拒绝 null、空对象、字段 null、数值枚举、UNSPECIFIED、未知字段。优先级引用须获本租户授权、在唯一目标集群存在且受运行路径支持。显式值覆盖默认值；没有任何来源的 placement 为 NONE。软亲和仅作用本实例，不增加副本。

可信规格决定 `instance_kind=container` 与 `compute_type=cpu|gpu`。CPU 不调用 ACC，不产生 GPU 执行参数或 GPU charges。GPU 创建由 Resource 解析受管业务规格，ACC 返回 `GpuExecution`、配额向量和 `GpuDisplay`。Gov 将执行参数和展示信息冻结到同一份 `CreateSpec`；`GpuExecution` 只有 `scheduler_name/queue_name/runtime_class_name/node_selector/pod_annotations/resources/execution_digest`，`GpuDisplay` 只有 `profile_id/profile_version/profile_name/model_key/mode`。`resources` 的 GPU 键固定为 `volcano.sh/vgpu-number`、`volcano.sh/vgpu-memory`、`volcano.sh/vgpu-cores`，其中 memory 直接使用 MiB 整数值。Gov→Resource 创建合同只有命令上下文和这份扁平规格，不发送完整 GPU 计划、重复请求或 charges；原 GPU 请求、配额向量和 charge 原账由 Gov 保存。

受理顺序为当前业务动作授权→原幂等记录→新键才调用一次 `ResolveCreateSpec`。新 GPU 再组合当前业务授权与当前租户的 ACCELERATOR 套餐资格，然后调用 ACC；资格检查不要求 Profile 目录 GET 权限。缺资格返回 403 `GPU_USE_DENIED`，资格存储不可用返回 503 `GPU_USE_AUTHORIZATION_UNAVAILABLE`。已受理原键仍先检查当前业务授权，随后返回原冻结记录，不重查规格、不重做新 GPU 资格。CPU 跳过资格与 ACC。共享资格能力支持 TRAINING（普通训练和微调共用），不依赖 INSTANCE；本次未增加训练或推理创建消费链。

原公开请求哈希不含动态解析的规格、默认值、执行参数或配额向量；当前授权通过后先找原幂等记录。已受理重试返回原 operation/resource，规格编辑、禁用或删除不改变原冻结参数，也不重新解析当前目录。共享按原账的 MiB 占用 `gpu.shared_memory_mib`，整卡按设备数占用 `gpu.physical.count`。最终删除须由 Resource 持久确认资源释放及不会迟到创建，再以 `release_event_id/create_operation_id/resource_id/reason` 上报；Gov 校验可信 owner 和原创建关系，在同一事务中按原账一次性全退。重复事件不重复退额，同事件内容冲突拒绝，同资源换事件也不能产生第二次退款。stop、重启及单独的 Usage ENDED 不触发退额。

响应为 `{operationId,resourceId,replayed,result:"ACCEPTED"}`。ACCEPTED 表示持久受理，运行情况通过 operation 和实例状态查询。启停与删除沿用 `{data:{idempotencyKey,expectedVersion}}` 及路径实例 ID；终端沿用既有独立接口。实例没有变配、CPU/GPU 互转或历史规格版本入口。

JSON 字段名逐字段遵守 Proto 的 `json_name`，不能对整个对象统一转换命名风格：

| 报文 | 实际 JSON 名称 |
|---|---|
| 创建基础字段 | `idempotencyKey/imageId/imageScope/presetId`；也接受其 Proto 原名 `idempotency_key/image_id/image_scope/preset_id`，同一字段不能重复提交两种写法 |
| 新增创建配置 | 固定使用 `workspace_mount_path/volume_mounts`；挂载项为 `pvc_name/mount_path/read_only`，网络项为 `vpc_id/subnet_id`，其余为 `labels/annotations/env` |
| 受理、操作和普通实例状态 | 例如 `operationId/resourceId/intentVersion/desiredState/observedState/computeCleaned/workspaceCleaned/quotaReturned`；显式指定的 `instance_kind/compute_type/gpu_summary` 保持 snake_case |
| PVC 候选和配置查询响应 | 下文列出的 snake_case 名称原样返回；配置版本 `instance_contract_version` 是 JSON 数字，`total` 及其他 int64/uint64 是十进制字符串 |

## 工作目录、已有卷与业务配置

创建的 `data` 支持 `workspace_mount_path`、`volume_mounts`、`network`、`labels`、`annotations` 和 `env`。服务器始终解析并保存当前配置合同 2；未提供可选配置时正常解析默认值，工作目录默认为 `/workspace`。公网不能提交 `instance_contract_version`。Gov `ANI_INSTANCE_CONFIG_V2_WRITES` 未设或不为 `true` 时只关闭新的 CREATE，返回 `503 INSTANCE_CREATE_DISABLED`，不会生成旧合同；当前合同的查询、同键重放、启停和删除不受此维护闸门影响。本批按 Resource 的显式迁移与旧执行者退出流程升级，保留现有实例、原 CREATE 和原账；保持新意图关闭，验证正式模块、当前配置初始化证明及恢复门禁后开放对应能力。

```json
{"data":{"idempotencyKey":"10000000-0000-4000-8000-000000000001","name":"工作区","imageId":"实际镜像ID","imageScope":"platform","presetId":"实际规格ID","workspace_mount_path":"/work","volume_mounts":[{"pvc_name":"research-data","mount_path":"/datasets","read_only":false},{"pvc_name":"reference-data","mount_path":"/reference","read_only":true}],"labels":{"business.example.com/team":"research"},"annotations":{"business.example.com/purpose":"experiment"},"env":[{"name":"RUN_MODE","value":"  literal value  "},{"name":"OUTPUT_DIR","value":"$(RUN_MODE)"}]}}
```

`volume_mounts` 最多 8 项，只有已有 PVC 名、容器内绝对规范路径及只读方式；不接受 PVC UID、Namespace、任意 volume source 或专属盘身份。Res 在可信租户 Namespace 内解析并冻结原 UID，用户卷以 borrowed 引用保存，停止保留、删除只解除引用。工作盘继续独立创建与回收。挂载路径彼此及与工作目录不能相同或互为父子目录，也不能覆盖系统目录；合法 Pending/WaitForFirstConsumer 由正常调度继续完成。

`env` 最多 64 项、值的 UTF-8 字节总计 16 KiB，名称不能重复或覆盖平台保留变量；数组保序、值不 trim 或展开。用户 labels/annotations 各最多 32 项，两类键和值的 UTF-8 字节合计 16 KiB，受管 ANI、网络、Notebook、调度器控制键均拒绝。HTTP 请求正文最多 64 KiB；未知字段、重复 JSON key、任意新增配置显式 null、空工作路径或 `network:{}` 均拒绝。未提供与空集合表示相同意图；同键改变配置值、env 顺序或挂载方式产生 409 幂等冲突。同一当前请求返回原 operation/resource/原账且不重新 Resolve；缺省或旧版本的持久命令不再执行。

| 方法与路径 | 输入、结果与权限 |
|---|---|
| GET `/api/v1/instances/mountable-pvcs` | `page/pageSize/name`，`{items,total}`；当前 `instance:create` 与 INSTANCE 套餐权限 |
| GET `/api/v1/instances/{instance_id}/configuration` | 当前权威配置及应用版本、待变更状态；当前实例 `instance:read` 与 INSTANCE 套餐权限；响应 `Cache-Control: no-store` |

PVC 候选项含 `pvc_name/phase/requested_capacity/access_modes/volume_mode/selectable/unavailable_reason`，容量为 quantity 字符串，分页默认 page=1/pageSize=20、pageSize 最多 100。只接受 `page/pageSize/name`，不接受 `page_size`、cursor、排序或任意 label selector；未知、重复及非规范正整数查询参数返回 400。`name` 是区分大小写的名称子串，最长 253 字节；先过滤，再按 PVC 名升序计算 total 和分页。未选中的候选也可能以 `selectable=false` 返回，界面以该值控制可选性并展示安全的 `unavailable_reason`。合法 Pending/WaitForFirstConsumer 不等于不可选择。完整快照最多扫描租户 Namespace 的 2000 个 PVC，超过上限返回 422；name 过滤不绕过该扫描上限。目录不能替代创建时重查。

候选响应形状示例（资源名须替换为真实目录项）：

```json
{"items":[{"pvc_name":"research-data","phase":"Bound","requested_capacity":"1Gi","access_modes":["ReadWriteOnce"],"volume_mode":"Filesystem","selectable":true,"unavailable_reason":""}],"total":"1"}
```

配置查询直接返回 `instance_id/instance_contract_version/workspace_mount_path/volume_mounts/network/labels/annotations/env`，没有外层 data。响应的合同版本固定为 2。同租户具有当前实例 read 权限的用户可读取冻结的业务字面 env；这不是系统环境、Secret 或控制面凭据导出接口。列表、详情安全投影、诊断、操作和审计不回显业务配置值；审计对 Instance 请求正文脱敏。响应为 `Cache-Control: no-store`，前端不应将配置值放入 URL、遥测、错误报告或持久缓存。

```json
{"instance_id":"30000000-0000-4000-8000-000000000001","instance_contract_version":2,"workspace_mount_path":"/work","volume_mounts":[{"pvc_name":"research-data","mount_path":"/datasets","read_only":false},{"pvc_name":"reference-data","mount_path":"/reference","read_only":true}],"network":null,"labels":{"business.example.com/team":"research"},"annotations":{},"env":[{"name":"RUN_MODE","value":"  synthetic literal  "},{"name":"OUTPUT_DIR","value":"$(RUN_MODE)"}]}
```

上述 env 均为合成示例；空格和 `$(RUN_MODE)` 在容器环境中保留字面内容。响应不含 PVC UID、Namespace、Attachment、NIC 或控制面引用。

`network` 使用 `{vpc_id,subnet_id}`，服务端按可信租户及目标集群校验；只有 Network 生命周期实现及对应门禁完成后开放。网络依赖尚未启用时返回 `503 INSTANCE_NETWORK_NOT_ENABLED`，不会静默丢弃选择。以下 reason 可用于前端分支；身份、当前权限及租户不可见仍沿用 401/403/404。错误不回显配置值。

| HTTP / reason | 触发与处理 |
|---|---|
| 400 `INVALID_INSTANCE_REQUEST` | 非法字段、值、正文或查询 |
| 400 `INSTANCE_RESERVED_CONFIG` | 业务 metadata/env 覆盖系统控制键；修改输入后用新幂等键创建 |
| 404 `INSTANCE_PVC_NOT_FOUND` / `INSTANCE_NETWORK_NOT_FOUND` | 引用不存在或当前租户不可见；重新加载授权目录 |
| 409 `IDEMPOTENCY_CONFLICT` | 同键不同原始业务意图；网络重试保持原键与原正文 |
| 409 `INSTANCE_PVC_REFERENCE_CHANGED` / `INSTANCE_NETWORK_CONFLICT` | 冻结引用或网络关系冲突；异步阶段也可能由 operation/实例 reason 报告，不能覆盖旧受理快照 |
| 422 `INSTANCE_PVC_CATALOG_LIMIT` | PVC 快照扫描超过上限；不能把截断目录展示成完整可选集合 |
| 503 `INSTANCE_CREATE_DISABLED` / `INSTANCE_NETWORK_NOT_ENABLED` / `INSTANCE_DEPENDENCY_UNAVAILABLE` | 新建维护闸门关闭、网络受理未开放或依赖不可用；保留原键与正文，恢复后查询原 operation 或重试 |

Gov 显式结构迁移 `20261019090000_instance_contract_version.sql` 为操作增加不可变版本；共享配额表中其它动作的默认值不构成实例合同支持。实例新操作只接受显式 2，版本、请求意图摘要、冻结载荷及占额在同一事务保存，同键并发比较当前完整意图。CPU 使用当前规范投影及原 business_payload 字节摘要，不接受旧 envelope 或旧摘要域；GPU 保留唯一冻结 Execution、原始 charges、稳定退额事件与物理释放 fence。迁移由独立身份显式执行，启动迁移保持关闭。

这些是源码接口合同；正式依赖、Fedora 验证及 CSI/终端/网络/GPU 实测结论见 Resource 本任务唯一执行状态，不能据字段或路由存在认定业务验收通过。

引用、保留键、当前合同 2 摘要及网络生命周期的完整规则以 [Resource 权威设计](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/3cd79a303d710617e513937ce5fc6f93ace8ccd5/docs/specs/instance-configuration-network.md) 为准；本页只维护前端 HTTP 边界，不另建实施状态台账。

## 重启与已有 PVC 挂卸

| 方法与路径 | 权限与输入 |
|---|---|
| POST `/api/v1/instances/{instance_id}:restart` | `instance:restart`；`{data:{idempotencyKey,expectedVersion}}` |
| POST `/api/v1/instances/{instance_id}:attach-pvc` | `instance:pvc:attach`；另有 `pvc_name/mount_path/read_only/restart_if_running` |
| POST `/api/v1/instances/{instance_id}:detach-pvc` | `instance:pvc:detach`；另有 `mount_path/restart_if_running` |
| GET `/api/v1/instances/{instance_id}/mountable-pvcs` | `instance:pvc:attach`；只接受 `page/pageSize/name`，按实例持久 placement 查询 |

三个新动作的基础 JSON 名固定为 `instanceId/idempotencyKey/expectedVersion`，版本必须是正整数十进制字符串；路径 ID 可省略，提供时必须与 URL 一致。卷字段与 `restart_if_running` 只接受表列 snake_case，bool 缺省 false。拒绝 null、别名、重复/未知键、额外 query、数值或科学计数版本及客户端内部身份字段。旧 Start/Stop 保留原兼容解码。运行态挂卸只有显式 `restart_if_running:true` 才授权同一操作内的停止与恢复，无需额外 Start/Stop 权限；false 返回 `INSTANCE_REQUIRES_STOP`。独立 Restart 只接受稳定 running。

新动作只有 Resource durable ACK 后才返回 202；响应直接为 `{operationId,resourceId,replayed,result:"ACCEPTED"}`。调用顺序固定为当前身份/动作/INSTANCE 套餐授权、公共 Resource API helper 规范意图、只读 `LookupCommandReplay`，未命中才检查 DELETE 和能力门禁。原键重放保留原版本和正文，不因当前版本变化、PVC 消失或门禁关闭而失效；同键异意图返回 409 `INSTANCE_IDEMPOTENCY_CONFLICT`。Start/Stop 同样采用 Lookup 先行。

Gov 与 Resource 的 `ANI_INSTANCE_NEW_INTENTS_ENABLED` 为维护受理门禁；`ANI_INSTANCE_RESTART_ENABLED` 和 `ANI_INSTANCE_PVC_MUTATIONS_ENABLED` 分别控制新能力，均缺省关闭。CREATE 还要求既有 `ANI_INSTANCE_CONFIG_V2_WRITES=true`。开关不阻止授权的 Lookup/查询/候选、已受理恢复及最终 DELETE。挂卸开放须先完成引用 guard、迁移和初始化证明。

本批权限登记使用[窄增量脚本](../../scripts/ops/sql/register-instance-restart-pvc-permissions.sql)。先用确切合入 dev 版本的 `admin sync-apis --dry-run` 核对目录并备份，再显式给出脚本要求的已审核租户、套餐和角色身份；脚本缺省只预览，`apply=true` 才补本批缺失关联。既有 ID、启停状态及其他权限关联保留，身份或目录漂移会中止。它不修改套餐额度，也不运行 `SyncPermissions`；应用后刷新全部 Gov 实例的策略再验权。

实例新增 `activeOperationId/executionRunState/appliedMountVersion`。configuration 的 `version/applied_mount_version/pending_change` 展示当前权威挂载，原 CREATE 不变。`pending_change` 内固定 `operation_id/intent_version/phase/target_configuration`；operation 新增 `phase/target_configuration/final_run_state/applied_mount_version/completion_scope/failure_reason`。所有 int64 为十进制字符串。无目标变更时 `target_configuration:null`；卸空时为 `{"volume_mounts":[]}`，CurrentMounts 空集合不会回退 CREATE。配置应用和运行完成分别由 `template_applied/runtime_verified` 表示。

只读候选沿用 `{items,total}`，分页缺省 1/20、pageSize 上限 100，显式 0、重复或未知参数拒绝。实例范围候选使用 attach 权，不要求 create 权；创建范围候选仍使用 create 权。

CPU/GPU DELETE 均持久关闭新意图闸门。只有当前 dispatcher lease、DISPATCHING、从未 ACK 且 owner 明确 FailedPrecondition/`INSTANCE_VERSION_CONFLICT` 时标 REJECTED 并解闸，不退额。超时/未知继续保闸。同键重放拒绝返回原 operation、`replayed:true/result:"REJECTED_VERSION_CONFLICT"`；该本地 operation 查询为 `action:"delete",intentVersion:"0",state:"failed",reason:"INSTANCE_VERSION_CONFLICT"`，避免查询不存在的 Resource 操作。已 ACK DELETE 仍查询 Resource。其他 owner/VM 语义保持原合同。

错误沿用 code/reason/message/metadata 封装；新格式错误 400、缺对象 404、业务状态/版本/互斥/DELETE/PVC guard 冲突 409、依赖或能力未开放 503。已受理后通过 operation 查询执行失败，HTTP 200 只表示查询成功。

本批正式 HTTP 验收入口为 [container-restart-pvc-acceptance.py](../../scripts/ops/container-restart-pvc-acceptance.py)，在授权驱动主机显式给出 `--base-url`、`--credentials-file`、`--evidence-dir`。凭据文件必须归调用者所有且权限为 `0600`，格式和正常租户/平台登录合同见脚本说明；没有默认账号或目标。证据目录为 `0700`，只保存原键、版本、请求正文摘要和白名单状态，不保存配置 env、密码、token 或 cookie；结束注销自己的会话。

`restart-cpu --create-input <0600请求文件>` 或 `restart-cpu --instance-id <本批实例UUID> --expected-version <当前版本>` 执行 CP-01。`--observation-hook <绝对可执行文件>` 通过 stdin 接收合成标记、挂载路径及旧 Pod 身份，必须返回真实 exec 读写摘要、Notebook/PVC/新 Pod UID 和旧 Pod 的直接 GET 证明；钩子不接收登录凭据。没有该证明只记 `NOT_VERIFIED`（退出码 3），不能据 operation completed 记 LIVE PASS。单步 `command` 支持 Restart/Stop/Attach/Start/Detach；每个新意图用独立证据目录，同一目录重试必须保持原动作、版本、正文和键。超时或 ACK 丢失保留原意图，不自动换键重做。

## 规格与列表

| 方法与路径 | 输入与结果 |
|---|---|
| GET `/api/v1/compute-presets?usage=CONTAINER_INSTANCE` | page/pageSize，name/code/compute_type/gpu_mode；`{items,total,gpu_catalog_status,gpu_catalog_reason}` |
| GET `/api/v1/instances` | page/pageSize，name/id/instance_kind/compute_type/observedState；`{items,total}`；不传 observedState 时不返回 deleted |
| GET `/api/v1/instances/{instance_id}` | 冻结静态详情，与列表使用同一安全投影 |
| GET `/api/v1/instance-operations/{operation_id}` | 受理响应的 operationId；返回 id/instanceId/action/intentVersion/state/reason |
| POST `/api/v1/instances/{instance_id}:start`、`:stop` | `{data:{idempotencyKey,expectedVersion}}`，202 + 受理响应 |
| DELETE `/api/v1/instances/{instance_id}` | 同启停请求，202 + 受理响应；继续观察清理标志 |
| GET `/api/v1/admin/compute-presets` | 同规格过滤，另有 enabled；`{items,total}` |
| GET `/api/v1/admin/compute-presets/{id}` | 单个完整规格 |
| POST `/api/v1/admin/compute-presets` | `{data:{...}}`，201 + 规格 |
| PATCH `/api/v1/admin/compute-presets/{id}` | `{data:{...},update_mask:"name,cpuMillis"}`，200 + 规格 |
| POST `/api/v1/admin/compute-presets/{id}:set-enabled` | `{data:{enabled:false}}`，200 + 规格 |
| DELETE `/api/v1/admin/compute-presets/{id}` | 无正文，204 |

规格目录 DTO 归 [compute](../../api/protos/compute/service/v1/compute.proto)，平台六条管理 HTTP
由 [ComputePresetService](../../api/protos/admin/service/v1/i_compute.proto) 单独接管，使用既有 SYSTEM 管理分类和
`compute:preset:admin:list/read/create/update/delete` 精确权限；set-enabled 复用 update。
原 InstanceService 管理方法与旧管理权限判断直接删除。容器目录路径与
`instance:preset:read`/INSTANCE 授权保持，调用新的 `compute.v1.ComputePresetService/ListPresets`，
服务端固定用途 CONTAINER_INSTANCE；可信 GPU 五元组交集先于最终计数和分页。
目录与实例创建分别调用各自接口；容器创建仍只调用一次活跃 ResolveCreateSpec，
不先调用公共 ResolveComputePreset，不改变已冻结的实例 v2 字节与摘要。

分页独立默认 page=1/pageSize=20，pageSize 1..100；未知、重复、零和非规范正整数参数拒绝。授权、可见性和过滤先于最终计数及分页。HTTP 的 int64/uint64 按 ProtoJSON 为十进制字符串。

列表的 `name` 为区分大小写子串（去首尾空白，最长 80）；`id` 为不区分大小写子串（去首尾空白，最长 36 字节，空值等同未传），用于按实例 ID 片段检索。`observedState` 不传时排除 `deleted` 墓碑，显式传 `observedState=deleted` 才能查看已删除实例；两者与其它过滤取交集。

创建示例中的镜像、规格与 PriorityClass 必须替换为真实可见资源；不需要调度意图时省略整个 scheduling。幂等键为 UUID，每个新动作使用新键，网络重试沿用同一键和原始正文。expectedVersion 使用最近静态详情的字符串版本。operation 的 QUEUED/执行中/完成或失败与 Pod 运行状态分别观察。

列表、详情还返回 `networkState/networkReason/networkCleaned/volumeReferencesReleased`，采用实际 ProtoJSON camelCase。四项是安全观察状态，不包含 Pod UID、地址控制凭据或内部端口引用，也不参与 CREATE 业务摘要。未选网络时 `networkState="not_requested"`、`networkCleaned=true`；选择网络后按 Resource 当前状态展示，不能只用 Pod Ready 推断已联网。详情需要 `instance:read`，列表需要 `instance:list`，均受当前 INSTANCE 套餐与租户范围限制。

DELETE 的 202 只表示持久受理，继续轮询原 operation 和详情，分别展示以下完成标志：

| 字段 | 完成范围 |
|---|---|
| `computeCleaned` | 实例计算已释放 |
| `workspaceCleaned` | 系统专属工作盘完成回收 |
| `networkCleaned` | 关联网络及历史关系完成释放；未选网络为 true |
| `volumeReferencesReleased` | 借用的用户卷引用已解除，用户 PVC/PV 和文件保留 |
| `quotaReturned` | 原计算账本退额已处理；GPU 退额仍以原 CREATE 和物理释放事实为准 |

这些标志可以分阶段完成：计算已释放时，网络或磁盘仍可能等待清理。只有计算、workspace、全部保护态借用引用、网络和未知写均已清理后，Resource 才按原 CREATE 账触发一次最终退额。Stop/Start/Restart 与挂卸及其失败不产生占额或退额；borrowed PVC/PV 和文件保留。退款完成或 404 不能代替全部清理证明。

规格字段为 id/code/name/description/compute_type/cpu_millis/memory_mib/enabled/gpu/created_at/updated_at/selectable/unavailable_reason。CPU/内存为正整数，无浮点规格。code 创建后不可改。GPU 公开输入只有 profile_id/profile_version/devices_per_instance；服务器从 ACC 可信目录核对完整引用。整卡数量为正整数，并满足 `1 ≤ devices_per_instance ≤ Profile.max_devices_per_replica`，同一个 Profile 可选择其上限内的数量；不硬编码 2 卡或逐数量分流。共享保持单卡。修改不更新已有实例，不级联删除 Profile 或实例。`selectable/unavailable_reason` 是已授权候选的当前可选性提示，不能替代创建时的正式解析，也不能改变已受理请求的重放结果。

PATCH 使用 FieldMask 的 JSON 字符串形式，路径按 ProtoJSON lowerCamel 转换；HTTP DTO 字段仍以 Proto 的 json_name 为准。只修改 mask 中的字段；清空 GPU 必须同时给 `gpu:null` 和 `gpu` mask。enabled 必须显式存在。CPU 转换须清掉 GPU，GPU 转换须提供合法 GPU 引用；行锁下合并并原子校验，不先在 Gov 读整行后覆盖。

静态详情新增 instance_kind、compute_type、cpu、memory、createdAt、workspaceSize、preset_name、preset_code、gpu_summary 和实际 scheduling。`gpu_summary` 仅含 `profile_id/profile_version/mode/model_key/devices_per_instance/memory_mib_per_device/memory_mib_total/core_limit_percent`。共享和整卡都返回具体 MiB 值，整卡采用冻结的单卡完整显存值；两个显存字段均为 int64，HTTP 按 ProtoJSON 输出十进制字符串。旧 `shared_memory_mib_per_device/shared_memory_mib_total` 字段已删除且保留编号，不接受旧字段。CPU 的 gpu_summary 为 null。规格候选中的 GPU 目录不可用或无权限时保留 CPU 候选并明确状态，不把不完整 GPU 集合当完整目录。

## 平台调度管理

| 方法与路径 | 合同与权限 |
|---|---|
| GET `/api/v1/admin/scheduler-config` | `{config,resource_version}`；`instance:scheduler:read` |
| PUT `/api/v1/admin/scheduler-config` | `{data:{config,expected_resource_version}}`；`instance:scheduler:update` |
| GET `/api/v1/admin/instance-scheduling-settings` | `{defaults,priority_class_allowlists}`；`instance:scheduling:read` |
| PUT `/api/v1/admin/instance-scheduling-settings` | `{data:{defaults,priority_class_allowlists}}`；同时要求 `instance:scheduling:defaults:update` 和 `instance:scheduling:allowlists:update` |

config 为登记 ConfigMap 固定 key 的原生 YAML 字符串。最终更新使用编辑时的不透明 resourceVersion；冲突返回 409，客户端重新读取并由管理员处理冲突。其他键、metadata、actions 顺序、tiers 和未知项保留。ACC 同一版本归一化算法只豁免已审核的评分参数，GPU 模式、编码、单位和未知内容受保护。环境基线尚未对齐时写入口返回 503，读取保持可用。保存只确认 ConfigMap 更新；不表示调度器已经加载。

settings 是当前唯一集群的一条完整配置，PUT 原子替换 defaults 和名单；不接受租户覆盖层。两个字段均须出现，`defaults:{}` 和 `priority_class_allowlists:[]` 合法。defaults 只含可选 `pod_placement`、`priority_class_name`；名单形如 `[{"tenant_id":1,"priority_class_names":["batch-low"]}]`，tenant_id 是已存在的 Gov 租户 ID。重复租户或名称、空或非法 PriorityClass 名称、未知字段均拒绝。空配置读取为 `{defaults:{},priority_class_allowlists:[]}`。

创建在查原始幂等记录后读取一次配置；显式字段覆盖该快照的默认字段，placement 无来源时使用 NONE。实际采用的 PriorityClass 必须在本租户名单内，并通过可信 Resource 租户映射、目标集群存在性和 GPU 路径兼容检查；没有引用时不启动 PriorityClass 检查。平台设置只授权引用，不创建 PriorityClass，不修改已冻结的 GPU 执行参数或现有 PodGroup。既有实例和受理重试保持冻结值；配置读取失败返回 503，不静默采用空默认。存储复用受保护的全局配置记录，通用配置 CRUD 不公开或修改此记录。

## 诊断与 SSE

`GET /api/v1/instances/{instance_id}/runtime` 返回 instance_id/version/observedAt/pod/statefulSet；对象缺失为 null。pod 只给安全状态、环境变量名称及来源类别、挂载种类与容器内路径，不给变量值、Secret 内容或宿主机路径。

`GET /api/v1/instances/{instance_id}/events` 返回 podUid/observedAt/events/truncated；事件只属于当前核对过 owner 链的 Pod UID。未知时间和次数为 null。最多读取 1000 条（每页 200），截断集合不宣称全局最新。

`GET /api/v1/instances/{instance_id}/logs?tailLines=100&follow=true` 只读当前 workspace stdout/stderr。tailLines=1..1000、follow=true|false，均单值且可独立省略；默认 100/true。近期日志 follow=false 仍返回 SSE。目标及上游 READY 通过前返回普通 HTTP 错误；头后只发 SSE error/end，不再写 HTTP JSON 错误。

```javascript
const abort = new AbortController();
const response = await fetch(`/api/v1/instances/${instanceId}/logs?tailLines=100&follow=true`, {
  headers: { Authorization: `Bearer ${accessToken}` },
  signal: abort.signal,
});
if (!response.ok) throw await response.json();
const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
let pending = "";
try {
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    pending += value;
    for (;;) {
      const boundary = pending.indexOf("\n\n");
      if (boundary < 0) break;
      const frame = pending.slice(0, boundary);
      pending = pending.slice(boundary + 2);
      if (frame.startsWith(":")) continue; // heartbeat
      let event = "message";
      const data = [];
      for (const line of frame.split("\n")) {
        if (line.startsWith("event:")) event = line.slice(6).trim();
        if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
      }
      if (data.length) onInstanceLogEvent(event, JSON.parse(data.join("\n")));
    }
  }
} finally {
  abort.abort();
  reader.releaseLock();
}
// 页面关闭/切换实例时也调用 abort.abort()。
```

`log` 数据为 `{text:"..."}`，`end` 为 `{reason:"EOF|TIME_LIMIT|POD_CHANGED"}`，`error` 为稳定脱敏 reason。没有事件 ID 或断点恢复。Bearer 不放 URL。连接有 no-store、flush、关闭代理缓冲和 15 秒注释心跳。默认每帧 16KiB、连接缓冲最多 256KiB、写超时 10 秒、寿命 10 分钟，进程/租户/实例上限 100/10/3。慢客户端终止，断连取消传递到上游，不切换到新 Pod。

非法参数 400，权限 403，不存在 404，幂等/版本冲突 409，PVC 候选目录上限 422，日志连接上限 429，依赖不可用 503；错误体沿现有 code/reason/message/metadata。运行、GPU 硬件、浏览器及现场基线验收以本批执行证据为准。

统一算力目录的必传 usage、三业务权限与旧路由删除约定见[算力目录 HTTP 合同](compute-presets.md)。

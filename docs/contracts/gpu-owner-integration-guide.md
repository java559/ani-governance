# GPU owner 接入、关闭与释放指南

本文面向 Inference 和后续 GPU 业务服务，描述 Governance 的创建接线与累计退额合同。`GOV-ACC-INF-V12-WIRING-01` 新增正式 Inference BFF、编译注册 adapter、公开退额 API 和固定 owner 身份装配；具体软件验收与发布版本以本批结果索引为准。未配置 Inference 客户端或缺少当前套餐/动作授权时继续拒绝新创建。持久 ACK 与受控注入退额不证明模型运行、物理释放或生产验收。

独立持久化测试 owner 只存在于 `app/admin/service/tests/contracts/gpu/*_test.go`。它运行真实 PostgreSQL 命令、墓碑和通知事务，但不创建 Kubernetes 工作负载，不实现 Inference，不证明物理 GPU 清理。软件、真实 owner、硬件和生产部署的结论必须分别记录。

## 1. 版本与可复用范围

| 层 | 本文所需能力及版本门槛 | 当前边界 |
|---|---|---|
| Governance | 最低实现版本为 **`bd9ad1a33bbe8ce28f4faeb19bfc9ee494ae8a84`**：`GpuAcceptance`、schema 2 canonical、`gpu-metering-v1`、完整配额账本（当前已恢复 Ent；下列 SHA 为历史交付版本）、DELETE acceptance、独立 usage sync、严格 ACK/GPU release 校验 | 源码 manifest、版本对和后续证据提交写入[交付记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/release/closeout.md)。任务起点 `0fbe1a69e49cc23dc7a1696b62f68c34a7c6a48a` 不包含这些能力 |
| Accelerator | 最低固定交付 SHA `1d32dd9a9173b8869fa0ef2ae64e20b88f2ca0a3`；`accelerator.v1`、`accelerator.integration.v1`、22 RPC、公开附件及本批运行时整改 | 已发布的任务分支提交；这不表示生产部署、真实 owner 或硬件已经通过 |
| 跨仓依赖 | 本仓 [go.mod](../../go.mod) / [go.sum](../../go.sum) 固定 `v0.0.0-20260924030150-1d32dd9a9173`，对应上行 Acc SHA | 上述 Gov 实现与该模块已完成 Fedora 软件验收；精确CI状态和用户接受不等待Gov CI的边界见交付记录。旧API基线联调不能认证新模块 |
| 当前 owner | `ani-inference`，固定单 owner 的 ref、身份、公钥和查询约束 | 正式 Inference adapter 源码已接入；实际环境启用仍需受管连接、当前授权、额度和身份；第二 owner 未实现 |
| 未开放能力 | 任意 owner、自报 URL/RPC 转发、在线 GPU 扩缩、额外 GPU 容器/滚动副本、多设备容器 | 均不能仅改配置或更换名称启用 |

接入评审必须先填写最低 Gov SHA、最低 Acc SHA、实际 Go 模块版本与 sum、数据库迁移版本和两仓受测版本对；未锁定前只可做接口开发，不能据本文宣布接入验收完成。使用 `GOWORK=off` 正式消费模块，不使用兄弟目录 `replace`、复制 Proto 或跨仓 `internal` 导入。

字段权威来源是上述固定版本 Acc 的 [Proto](https://github.com/zhangzhe-ctrl/ani-accelerator-service/tree/1d32dd9a9173b8869fa0ef2ae64e20b88f2ca0a3/api/protos/accelerator)、[消费合同](https://github.com/zhangzhe-ctrl/ani-accelerator-service/blob/1d32dd9a9173b8869fa0ef2ae64e20b88f2ca0a3/docs/contracts/accelerator-consumer-contract.md)和[固定向量](https://github.com/zhangzhe-ctrl/ani-accelerator-service/blob/1d32dd9a9173b8869fa0ef2ae64e20b88f2ca0a3/docs/contracts/accelerator-vectors.json)。Governance 保留了用于逐字回归的[固定向量](../../app/admin/service/internal/service/testdata/accelerator-vectors.json)；它是测试输入，不是另一个 API 定义。本文的实现索引如下：

| 接缝 | Governance 权威实现 |
|---|---|
| 受控业务绑定、受理、历史重放 | [gpu_acceptance.go](../../app/admin/service/internal/service/gpu_acceptance.go) |
| canonical、plan/charge 校验、公开附件装配 | [gpu_contract.go](../../app/admin/service/internal/service/gpu_contract.go) |
| adapter、结构化命令、严格 ACK | [quota_adapter.go](../../app/admin/service/internal/service/quota_adapter.go)、[quota_dispatch_worker.go](../../app/admin/service/internal/service/quota_dispatch_worker.go) |
| 原子占额、取消、DELETE、累计释放 | [quota_ledger_repo.go](../../app/admin/service/internal/data/quota_ledger_repo.go) |
| 独立投影扫描、租约、重试与 CAS | [gpu_usage_sync_worker.go](../../app/admin/service/internal/service/gpu_usage_sync_worker.go)、[quota_gpu_sync_repo.go](../../app/admin/service/internal/data/quota_gpu_sync_repo.go) |
| owner 退款身份及稳定协议 | [quota_internal_server.go](../../app/admin/service/internal/server/quota_internal_server.go)、[quota_release.proto](../../api/protos/quota/service/v1/quota_release.proto) |
| 正式业务 BFF、严格 adapter | [inference_service.go](../../app/admin/service/internal/service/inference_service.go)、[inference_client.go](../../app/admin/service/internal/data/inference_client.go) |
| 当前用户/角色/套餐与路由授权 | [inference_authorization.go](../../app/admin/service/internal/data/inference_authorization.go) |
| 正式装配与受管连接 | [wiring_ent.go](../../app/admin/service/cmd/server/wiring_ent.go) |
| 已登记的公网接口和权限 | [接口集成登记](../interface-integration-register.md)、[BFF Proto](../../api/protos/admin/service/v1/i_accelerator.proto) |

## 2. 权威和职责

| 事实 | 权威服务 | 不可替代它的信号 |
|---|---|---|
| 当前用户权限、套餐、占额、原操作/完整 charges、退款 receipt | Governance | 客户端自报 tenant/actor/额度、Acc 容量、owner 的普通状态字符串 |
| 供给、profile、冻结 GPU plan、设备/分配观察、binding、容量完整度 | Accelerator | Governance 余额、Pod 名称、未验证标签、使用投影 ENDED |
| 模型/镜像/网络等业务合法性、持久命令、业务 readiness、创建封闭、完整资源历史和清理完成 | 实际业务 owner | 持久 ACK、HTTP 200、API NotFound、单次 ObserveRelease |
| DECLARED/ENDED 使用关联投影 | Governance 从原账本派生，Acc 持久接收 | 这不是占额账本、库存预留或业务 Ready 状态 |

业务状态与配额状态分开显示。缺少投影表示关联尚未就绪，不表示资源不存在；ENDED 不清除仍被观察到的 live binding。容量 `complete=false`/UNKNOWN 不显示成“确定无空闲”，也不显示成可立即调度。租户 BFF 隐藏完整 plan/runtime、source fact、物理 UUID、Pod 身份和证据；不能从脱敏 DTO 重建投递或同步报文。

## 3. 受控装配与新 owner 准入

正式业务适配器在 Governance 内实现 `GpuBusinessBinding`，其中包含既有 `QuotaDispatchAdapter`，以及 `CreateAction()`、`DeleteAction()`、`GpuQuotaCodes()`、`AuthorizeGpu(...)`、`ValidateGpuBusiness(...)`。这些是 Governance 内部装配接口，不是公开 RPC，也不是供其他仓库导入的 SDK。

1. 先读取真实 owner 的业务 API，固定业务消息、操作名称、幂等语义、readiness 和删除边界。在该业务消息中嵌入公开 `GpuOwnerCreateAttachment` / `GpuOwnerDeleteAttachment`；本文不定义 `AcceptCreate` 等可调用服务。
2. `AuthorizeGpu` 负责当前 Principal 的动作与资源权限，必须在幂等读取前执行，且不能依赖当前 dispatch adapter 是否启用。`ValidateGpuBusiness` 校验全部业务输入、模板和其余配额维度，返回非 GPU quota items；真实渲染端还必须再次校验。
3. 在 composition root 编译注册固定 owner、不同的 CREATE/DELETE action 和支持的 GPU code；拒绝重复 owner/action。构造 `GpuAcceptance` 并由真实业务 BFF 调用。不得绕过它直接将用户 plan/charge 传入账本。
4. adapter 使用受管目的地址、mTLS 和固定超时，将冻结命令映射到真实业务 RPC；验证真实业务响应，再返回严格 ACK。用户不能指定目的 URL、方法、owner 或启用开关。
5. 正式注册 owner 退款 DNS 映射、worker 生命周期和明确运维恢复入口；增量登记真实 API、模块/套餐/角色权限并刷新策略。仅增加目录或套餐行不会改变 `NOT_ENABLED`。

当前 `NewGpuAcceptance`、registry 能力判断、usage ref 定位和 Acc 的 owner 校验均限于 `ani-inference`。新服务准入还要审查 Acc URI 身份、owner allowlist、公钥选择/轮换、签名验证、ref 查询和唯一性/跨 owner 隔离，以及 Gov binding、代码支持、退款 DNS 映射和后台扫描范围。不能让另一服务持有 Inference 证书或把 `owner_service` 改名就认为支持多 owner。

私钥只由对应服务持有；Acc 只配置签名公钥。当前合同不提供动态多 key 轮换 RPC。轮换必须验证旧 Pod 断言仍可核验的策略、重签/历史范围和回退过程，不能直接替换公钥后把旧占用视为不存在。

## 4. 身份与不可变字段

各连接均验证 CA、对端服务名和 TLS 1.3，不允许明文、CN 回退、Bearer 穿透或 `InsecureSkipVerify`。

客户端校验目的服务端证书 DNS；本批受控部署分别使用 `ani-accelerator` 和退款 listener 的 `ani-governance`。这个服务端名称校验与客户端 URI/DNS 身份映射是两道独立检查；部署时固定其实际受管值，不能仅因同 CA 就跳过 server name。

| 方向 | 当前鉴权合同 |
|---|---|
| Gov → Acc，20 个委托 RPC | 客户端证书唯一 URI SAN 精确为 `spiffe://ani.internal/service/ani-governance`；单值 `x-ani-action` 为 RPC 短名，`x-ani-actor-type`/`x-ani-actor-id` 与 typed context 一致；租户调用再传资源租户 UUID `x-ani-tenant-id`，命中 actor/action/tenant/cluster 静态 grant |
| Gov → Acc，SyncGpuUsage | 同一精确 Gov URI，按原持久 ref/plan/账本派生；不套用当前用户 grant。权限撤回和套餐到期不能阻断原事实同步 |
| owner → Acc，ObserveRelease | 唯一 URI SAN 精确为 `spiffe://ani.internal/service/ani-inference`；`x-ani-tenant-id` 等于原 ref，实际 Pod 断言也必须可信 |
| owner → Gov，ReportQuotaRelease | 内部独立 mTLS listener，客户端**精确 DNS SAN** 命中编译装配的 owner map；至少命中一个 owner，命中不同 owner 拒绝，同 owner 多个 SAN 可接受。不是 Acc 的唯一 URI 规则 |
| Gov → Inference 受管 GPU RPC | TLS 1.3；客户端证书唯一 URI SAN 精确为 `spiffe://ani.internal/service/ani-governance`。接收器验证链及该身份，从可信附件 ref 取得原租户；普通 metadata 自报不能提供授权 |

平台 Acc grant 不包含伪造 tenant，可以配置明确平台 cluster `*`；租户 grant 只允许具体 cluster UUID。不能用 tenant `0` 或空值绕过租户边界。Gov 内部退款服务默认关闭；编译装配仅允许固定 owner `ani-inference`，精确客户端 DNS SAN 由 `ANI_QUOTA_INFERENCE_DNS_SAN` 配置。没有提供生产 SAN 时不能启用 listener；通配符、URI、IP 和非规范 DNS 名均拒绝。配置不能选择另一个 owner，同 CA 的其他客户端也不会获得退额权限。

### 4.1 正式创建/删除入口与受管配置

本批用户确认登记 `INFERENCE=15`，独立于 `ACCELERATOR`。`PlanModule(INFERENCE)` 表示当前套餐允许推理功能；`PlanQuota(gpu.shared_memory_mib/gpu.physical.count)` 表示额度上限；当前角色对两个独立动作的授权仍是另一道必需闸门。来源为活跃 `identity/service/v1/module.proto` 和接口集成登记。

| 接口 | 公共报文与结果 |
|---|---|
| `POST /api/v1/inference/services` | `{"data":{"idempotency_key":"UUID","name":"...","model_version_id":"UUID","served_model_name":"qwen2.5-1.5b-instruct","engine_image":"registry.example/vllm:latest","engine_type":"vllm","command":"exec vllm serve /models --served-model-name qwen2.5-1.5b-instruct --max-model-len 4096","cpu":"8","memory":"32Gi","replicas":1,"gpu":{"cluster_id":"UUID","pool_id":"UUID","profile_id":"UUID","profile_version":"1"}}}`；业务调用既有 `AcceptGpuCreate`，返回 operation/resource、replayed、`ACCEPTED` 和 dispatch ID |
| `POST /api/v1/inference/services/{data.resource_id}:delete` | `{"data":{"idempotency_key":"UUID"}}`，资源从路径绑定；若正文同时带 resource_id 必须相同。调用既有 `AcceptGpuDelete`，返回 `QUEUED_FOR_OWNER` 或 `CANCELED_BEFORE_DISPATCH`；不接受本地 generation，也不返回释放完成 |

公共 DTO 没有附件、charges、可信 tenant/actor/owner、plan 或目的 URL；严格 HTTP 解码拒绝未知字段、重复字段、额外 query 和身份注入头。正式 adapter 调用 `InferenceServiceManager/CreateInferenceService` 与专用 `DeleteInferenceServiceRequest`；create 附件为 tag 10、原全量 charges 为 tag 11，delete 保留 tags 1..3、附件为 tag 4、原全量 charges 为 tag 5，响应 `durable_owner_ack` 为 tag 3。

原业务规范和 digest 由 Inference 公开 API 包 `CanonicalBusinessPayload` / `DecodeBusinessPayload` / `BusinessPayloadDigest` 唯一提供。全部业务 snake_case 字段参与，map 为字典序对象，整数与枚举为十进制字符串，空 list/map 为 `[]/{}`、缺 message 为 null，SHA-256 无前缀；完全排除 request_id、gpu_owner_attachment 和 original_charges。计划摘要继续使用 Acc 自己的算法；不能混用。

Gov 在 Occupy 前调用同包 `ValidateBusinessPayload`。当前锁定 KServe 的 LWS 路径无法可靠承载所需 Pod GPU annotations 与 Queue，受管 GPU LWS 输入明确拒绝；不能先占额后靠异步补丁补参数，C02 的实际 LWS 参数投影保持 `not_verified`。固定副本 Deployment 路径和 CPU LWS 分别按其实际支持能力处理。

Gov→Inference 配置为 `ANI_INFERENCE_ADDR`、`ANI_INFERENCE_CA_FILE`、`ANI_INFERENCE_CERT_FILE`、`ANI_INFERENCE_KEY_FILE`、`ANI_INFERENCE_SERVER_NAME`。最后一项是部署方提供的精确服务端 DNS SAN，不从服务名猜测；固定三秒超时、TLS 1.3，无明文或 skipVerify。退款 receiver 配置为 `ANI_QUOTA_ENABLED=true`、`ANI_QUOTA_INTERNAL_ADDR`、`ANI_QUOTA_CA_FILE`、`ANI_QUOTA_CERT_FILE`、`ANI_QUOTA_KEY_FILE`、`ANI_QUOTA_INFERENCE_DNS_SAN`；最后一项只映射到固定 `ani-inference`。

权限登记使用 [register-inference-permissions.sql](../../scripts/ops/sql/register-inference-permissions.sql)，先执行当前 API dry-run 与显式同步。选定租户的现有 `tenant:manager` 角色和选定套餐启用使用 [enable-inference-access.sql](../../scripts/ops/sql/enable-inference-access.sql)，明确传 tenant_id/role_id/plan_id；不更改基础 FREE、不向模板/所有角色自动授予，也不配置额度。共享套餐的 INFERENCE entitlement 影响全部订阅该套餐的租户，须按预期选择套餐。脚本仅供显式运维执行，源码落盘不表示已操作生产数据库。

| 字段 | 来源和语义 |
|---|---|
| Governance `tenant_id` | 当前可信用户 Principal 的持久数字租户；普通请求不能自报覆盖 |
| `GpuUsageRef.tenant_id` | 由可信数字租户查持久 resource UUID 映射；不是将数字 tenant 转字符串 |
| `actor.type/id` | 从 Principal 分列保存。当前公共受理限 `user` 且 user/tenant ID 非零；删除保存本次授权删除主体，不冒充原创建者 |
| `ref.owner_service/resource_id/create_operation_id` | 注册 binding 和原 CREATE 事务生成并持久的稳定身份；不能换 CREATE ID 复活同一资源 |
| `delete_operation_id` | 本次持久 DELETE 意图 UUID；发往 owner 时为真实 DELETE operation ID。安全本地取消无 dispatch ID |
| `request_hash` | Gov 原规范业务输入摘要；CREATE/DELETE 使用不同 schema 和语义，不含实时容量、解析时间、新 resource UUID |
| `gpu_plan` / `original_gpu_plan` | Resolve 后被冻结的完整 plan，删除仍带原快照，不能按当前目录重算 |
| `gpu_charges` / `original_gpu_charges` | 从原 CREATE **全部** charges 校验后提取的完整 GPU 子集，含 `charge_id/quota_code/original_units` |
| `business_payload_digest` | CREATE 全部规范业务消息字节的 SHA-256；不限 GPU 参数。Gov 当前实现这里不加 `acc-c14n-v1\n` 摘要前缀，不能混用 plan hash 算法 |
| `metering_version` | 当前严格为 `gpu-metering-v1`；canonical schema 为 2，ResolvedGpuPlan schema 为 1 |

公开附件只承载 GPU 子集。`QuotaDispatchCommand.Charges` 承载原操作所有维度，DELETE 的 `CreateOperationID` 指向原 CREATE。业务 adapter 必须保留非 GPU 业务协议并核对完整集合，不能因为 GPU 附件只有一项就丢掉存储等 charge。

## 5. 请求、规范摘要与计量

首期固定 `replicas=1..16`、`devices_per_replica=1`，每 Pod 一个指定 GPU 容器。profile ID 和 version 必须具体；零枚举、负数量、浮点、溢出、未知字段和不支持的形态必须拒绝。

`GpuRequest` 的字段/tag 是 `cluster_id=1`、`pool_id=2`、`profile_id=3`、`profile_version=4`、`replicas=5`、`devices_per_replica=6`、`container_name=7`。三个目录 ID 必须一致归属，容器名须是合法实际 GPU 容器名。固定版本的公开 Proto、当前运行时和 owner 合同均按首期 1..16；真实 owner 仍须验收其实际支持的副本上限。

| 模式 | 每 Pod 渲染 | Governance 原占额 |
|---|---|---|
| SHARED_FIXED | `m>0` MiB，`F>0` MiB/block，`m%F=0`，写 `vgpu-number=1`、`vgpu-memory=q=m/F` 和冻结 cores（1..99） | 仅 `gpu.shared_memory_mib = replicas × m` |
| WHOLE_EXCLUSIVE | `vgpu-number=1`、`vgpu-cores=100`、`vgpu-memory-percentage=100`，不再写绝对 memory | 仅 `gpu.physical.count = replicas` |

F 不是超配倍率；memory/core scale 为 100%。m、q、F 要满足 provider int32 边界，乘加满足 int64。总 MiB 不能写到每个 Pod。当前一个合法单模式 plan 恰好产生一个 GPU code；附件的 repeated 字段不表示当前允许整卡和共享双计。`gpu.count` 保持历史 LAB 语义。

`acc-c14n-v1` 使用 UTF-8、无 Unicode 归一化、对象键字典排序、无空白/末尾换行、无 HTML 转义；非负整数以十进制 JSON 字符串表示，bool 保持 bool，缺 message 为 null，全部领域字段都参与。数组保留语义顺序，受管 KeyValue 集合按 key 排序且拒绝重复。摘要为 `SHA256("acc-c14n-v1\n" + canonical_json)`，不直接 hash protojson 或 protobuf wire bytes。

baseline 排除自身摘要/验证/观察证据引用；spec 是 `{spec,baseline_id}`；plan 清空 `resolution_digest`、令 `profile.published=false` 后对完整静态内容求摘要。projection 对 ref、revision、state、plan_digest、end_reason、source_fact_ref、原 CREATE 时间求摘要。不要把 canonical JSON 的整数约定与语言结构体的存储 JSON 混为一谈；对外 Proto JSON 的 int64 使用十进制字符串。

冻结向量与[可执行测试](../../app/admin/service/internal/service/gpu_contract_test.go)是接入验收输入：

| 向量 | 固定预期 |
|---|---|
| 共享 m=6144，replicas=2，F=1/256/1024 | 每 Pod q=6144/24/6；原额度恒为 12288 MiB |
| 同请求 F=10 | 拒绝非整除粒度，不能向下取整 |
| baseline digest | `52245607efe2656462e8aba6b9660b2e5eb9abb55bc381892952fd77ff71a70a` |
| spec digest | `a10a605b164c85aec29cfa13c5e870759da9833825c8a9af089540fbc708add9` |
| plan digest | `ba778f500e33dfac97665cac8c359d33d69aa0192bd4b23d3330f8852c5e6389` |
| projection digest | `07498c0f5e4e4ddbb609bdf04f03e3792b015945f54dabd89b910ddb73bf8251` |
| `{"a":"<&中文>","b":true,"z":"9223372036854775807"}` 的规范 hash | `c8151fcfc0f56c9a06c7a35c22ad29a17501bceacabde76c420bff9ffc4a43cb` |
| replicas=17、devices=2、重复受管 key、额外 GPU 总量、错误 scheduler、溢出 | 即使重新计算得到匹配摘要也必须拒绝语义错误 |

## 6. 创建：先授权重放，再受理新请求

`AcceptGpuCreate` 的实际顺序是当前 Principal/业务授权 → 规范化请求和可信租户映射 → 原幂等读取 → 仅新请求检查 registry/业务/Resolve → plan 和完整向量校验 → 单 Ent 事务 Occupy → 唤醒 dispatch。

已授权同 key 同内容重放直接返回原 operation/resource/charge IDs；从原 canonical 核验原 plan，不再 Resolve。目录 CLOSED、F 改变、解析服务不可用、adapter 临时移除或新建关闭都不能触发第二次占额。当前权限仍先校验；同 key 异内容冲突，不能用原记录泄漏绕过授权。

新请求 Resolve 在事务外，持久 canonical 冻结业务内容及摘要、原 GPU 请求、完整 plan、计量版本和全部 quota items。并发请求即使各自 Resolve 到不同快照，最终由 Occupy 同一事务裁定幂等胜者；原 operation、全部 charges 和账户更新一次提交，任一维度不足全部回滚。Governance 内部统一使用一个 Ent Tx，不得拆成两次提交。

容量是参考值。合法 plan、有额度且执行能力启用时，容量已知为零或 UNKNOWN 可以接受等待；配置漂移、未验证、CLOSED 导致的 Resolve 错误则拒绝新受理。不存在第二次 reserved→allocated 扣额。无需在 owner 创建工作负载前注册 Consumer，也不等待 usage projection 建立才接受业务命令。

worker 领取提交后，在数据库事务外调用 adapter。投递前重新读取原全部 charges，按 code/id 校验唯一性、单位、原数量及完整性，再组装公开附件；读取失败、缺项或 plan/canonical 不合法时不调用 owner，保留原账本和错误。

owner 必须在返回 ACK 前，以同一持久事务保存命令身份、原请求/摘要、plan/charges、可恢复执行意图和 ACK。同 operation 同内容重试返回原 ACK；同 ID 异内容拒绝。ACK 丢失后不得再次创建不同资源。

adapter 返回给 Gov worker 的 JSON 必须**恰好**包含三个小写 snake_case 字段，每个一次：

```json
{"operation_id":"11111111-1111-4111-8111-111111111111","resource_id":"22222222-2222-4222-8222-222222222222","accepted":true}
```

这里 UUID 仅示意，实际必须等于收到的命令。未知字段、大小写别名、重复 key、尾随第二 JSON、错误 ID 或 false 均不能记 ACKED。真实 RPC 可以用公开 `DurableOwnerAck` 消息，adapter 仍需显式映射上述 JSON；不要依赖 protojson 默认 camelCase。ACKED 只证明持久接受，不证明创建成功、Ready 或删除完成。

## 7. owner 渲染与 Pod 关联

真正的 renderer 必须验证实际业务输入摘要与冻结 plan，固定 scheduler `volcano`、recipe `volcano-hami-v1`、已核对 Queue、`accelerator.ani.io/{supply-group,model-key,baseline-id}` 和 `volcano.sh/vgpu-mode=hami-core`。禁止业务模板覆盖、删除或扩大选择器、额外 GPU init/sidecar/容器、额外滚动副本和在线扩缩；受理校验不能替代最终渲染校验。

owner 应持久记录每次创建尝试及 API 结果未知的在途写，使用业务资源与原 CREATE 身份对账。重启、超时或 API 返回 NotFound 不能推断“从未创建”。记录全部实际 cluster、Pod UID、容器和替换历史；Pod 名称复用不能覆盖旧 UID。

在获得真实 Pod UID 后，owner 对 Pod 加四个标签 `accelerator.ani.io/{resource-tenant-id,owner-service,resource-id,create-operation-id}`，加 `accelerator.ani.io/plan-digest` 和 `accelerator.ani.io/owner-signature` 注解。Ed25519 签名输入是 `"acc-owner-pod-v1\n" + canonical_json({pod:完整PodRef,ref:完整GpuUsageRef,plan_digest:原摘要})`，这里不再加 canonical 哈希前缀；签名使用标准 Base64。

Acc 只有公钥，不持有 owner 私钥。未签名或不匹配的实际分配仍计入容量，但保持未关联；通过断言并与投影一致才可出现在租户绑定列表。签名证明来源与关联，不豁免额外容器或数量约束，也不证明真实消费证据已经通过。

## 8. DELETE、关闭墓碑与 ObserveRelease

公开删除由正式 Inference BFF 做当前权限校验后调用 `AcceptGpuDelete`。Gov 从本库读取原 CREATE、plan 和完整 charges，在一个事务内锁住原操作并与 worker 领取串行化：

| 原 CREATE 状态 | 持久结果 |
|---|---|
| QUEUED 且 attempt_count=0 | 全部 charge 原子退还并保存 DELETE acceptance，返回 `CANCELED_BEFORE_DISPATCH`，dispatch ID 为空，无 owner ACK |
| 已 CANCELED_UNSENT，新的 DELETE key | 核验原取消/完整退款，再保存同类本地结果；不重退，不新建 owner DELETE |
| 有任何发送尝试、UNKNOWN 或 ACKED | 持久真实 DELETE operation，返回 `QUEUED_FOR_OWNER`；不新占额，不立即退 GPU |

同 DELETE key 同内容重放原结果，异内容冲突。不同 key 仍指向同一原 CREATE；owner 必须把关闭约束绑定到原 tenant/resource/create operation，而不能只按单次 DELETE ID 去重。

DELETE 可以先于 CREATE 到达。owner 应先核验原 plan/charges/ref，在持久存储保存关闭墓碑，封闭所有创建执行点及重试队列，再协调在途副作用。迟到 CREATE 可以持久接受并返回原语义 ACK，但不得越过墓碑重新创建。墓碑不能按普通重试 TTL 清除；历史重放与备份恢复也要遵守它。

真实 owner 的关闭过程必须覆盖所有控制器、当前和替换历史 Pod、未确认的 API 写入、重启恢复执行和未来重建来源，直到确认不再创建且所有实际分配解除。正常退出、失败、重启和删除 ACK 都不等于这个状态。

owner 直接调用 Acc `ObserveRelease`，传原 ref、明确 DELETE UUID 和**非空**完整实际 Pod UID/container 范围。该 RPC 不要求 usage projection 已存在，也没有 Gov 公网代理。

请求字段/tag 为 `request_id=1`、`ref=2`、`delete_operation_id=3`、`pods=4`。每个 `PodRef` 必须包含 `cluster_id=1`、`namespace=2`、`name=3`、`uid=4`、`container_name=5`；cluster 是平台 UUID，uid 保留实际 Kubernetes UID，name 只作定位。`GpuUsageRef` 的字段/tag 为 `tenant_id=1`、`owner_service=2`、`resource_id=3`、`create_operation_id=4`。

| 观察结果 | owner 可以得出的结论 |
|---|---|
| ALLOCATION_STILL_PRESENT | 指定范围仍有分配，不能因已退配额或 ENDED 忽略它 |
| RELEASE_UNKNOWN | 范围缺失/陈旧/冲突/失联等使结果未知；继续保留关闭任务和占额，查明来源后重试 |
| ALLOCATION_RELEASED | 只证明所提交实际范围的受控解除事实；仍须 owner 独立证明范围完整、创建已封闭、无未知在途写 |

若实际从未创建 Pod，不能用空 scope 调用制造“释放成功”。owner 需要自己的持久证据证明从未执行、所有在途路径已核清且未来不可执行，再报告已关闭事实。Gov 仍要求明确持久 DELETE 关联。API 强删/NotFound、节点失联或单次采样缺项不能作为物理释放依据。

## 9. ReportQuotaRelease：原 CREATE、累计 GPU 子集与可靠通知

复用 `quota.service.v1.QuotaReleaseService/ReportQuotaRelease`，不改 tag、不另造 GPU 退款服务。报文没有可信 tenant/owner、DELETE ID 或清理证明字段；Gov 用证书 owner 和原账本定位租户、资源及 DELETE 关系。

| 请求字段 | owner 必须保存并重用的值 |
|---|---|
| release_event_id (tag 1) | 一个逻辑通知的稳定 UUID；重试绝不能重新生成 |
| operation_id (tag 2) | **原 CREATE** UUID，不是 DELETE operation |
| items (tag 3) | 1..16，原 charge_id + quota_code + 自创建以来累计 released_total |
| reason (tag 4) | 只允许 ABORTED_CLEANED 或 RESOURCE_RELEASED；这两个值都不替代真实完成事实 |
| resource_refs (tag 5) | 最多 64 项，只审计，不提供授权或清理证明 |

Gov 先校验原完整 charge 集合。通知只要含任一 GPU item，就必须包含原 GPU 子集全部 code/id，且每项 `released_total == original_units`，并能关联本库已持久的 owner DELETE 意图。无需 DELETE 已 ACKED，因此完成通知可以先于 ACK 回写。漏项、重复、错原 CREATE、错 owner/tenant、超量、部分 GPU 和无 DELETE 均拒绝，事务不改余额。

混合业务仍允许先仅对非 GPU charge 报告合法累计部分释放。GPU 完全释放后可以 ENDED，不必等待无关存储等 charge 归零；仅非 GPU 释放不能结束 GPU。完整原向量验证和 GPU 子集终态判断不能合并为“第一笔 charge 是否已退”。

累计语义为 `new_total=max(saved_total, reported_total)`，本次 delta 为差值；同事件同内容重试和新事件重复累计值不会双退，乱序不会负退。相同事件异内容冲突。Gov 在同一 Ent 事务保存 receipt、charge 累计值和账户更新，整个通知原子提交。

owner 在关闭完成事务中持久写入通知 outbox，保存原事件和完整报文；Gov 不可用或响应丢失时重发相同内容，收到有效响应后再持久确认 outbox。owner 重启必须扫描未确认通知。不能在内存中“发一次即可”，不能先删除关闭记录再等待通知成功。

可信旧操作退款不依赖用户 grant、套餐仍有效或新创建仍启用。Gov 不会因 ObserveRelease、TTL、错误字符串或 owner RPC NotFound 自动退款；真实清理与未来不再创建的真实性仍由可信 owner 负责。

## 10. 使用同步、错误与恢复

Gov usage worker 周期分页重扫原 CREATE 和完整 charges，派生同一 ref/plan/原 CREATE 时间：revision 1 为 DECLARED；安全未发取消且原全集合已退，或有 DELETE 关联且原 GPU 子集已全退，才派生 revision 2 ENDED。source fact 分别为 `quota-operation:<原CREATE>:accepted`、`:canceled-unsent`、`:gpu-fully-released`。空/损坏集合不产生终态。

`sys_gpu_usage_sync` 是可重建进度；它不参与占额或退款关键事务。占额提交而同步行尚未创建即崩溃，可从原事实补齐；旧操作后来退款也会被重扫。同 revision 的 payload/hash 可确定性重建。领取后网络调用在事务外，ACK 按原 tenant、revision 和 lease generation CAS，迟到 ACK 不覆盖新目标。Acc 接受 2 先到，晚到 1 不复活；同步失败不增加业务投递次数、不发 owner 命令、不改余额。

| 故障/边界 | 权威与恢复责任 |
|---|---|
| Occupy 提交前失败 | Gov PG 回滚，无 owner RPC；调用者同 key/内容重试 |
| Occupy 已提交、受理响应丢失 | Gov 原幂等记录；有权限同 key 重放原 IDs/plan/charges，不重 Resolve |
| owner 接受已提交、ACK 丢失 | owner 命令日志和原 ACK；Gov 同 operation/content 重投，owner 不重建资源 |
| owner/Acc Unavailable、DeadlineExceeded 或传输结果不明 | 保留占额和原命令，按 1/2/4/8/16/30 秒上限退避重试；不得猜测无副作用 |
| 错 ACK、InvalidArgument、FailedPrecondition、NotFound、PermissionDenied、Unauthenticated | 当前 worker 归为永久合同错误，置 retry_blocked，保留原账本；修复接口/身份后受控恢复，不能直接退额 |
| 同步 USAGE_PROJECTION_CONFLICT | usage worker 按实际稳定 reason 单独永久阻断并保留原 payload；不能改 hash/ID 覆盖。普通 Aborted 仍按传输重试，业务 adapter 应将明确永久拒绝映射到既定永久分类 |
| adapter 暂缺 | 历史数据保留，worker 记录 ADAPTER_MISSING 并等待；有权限的受理重放不依赖 adapter，新创建仍关闭 |
| 原全部 charge 读取/校验失败 | 不调用 owner；保存 ORIGINAL_CHARGES_UNAVAILABLE / ORIGINAL_CHARGES_INVALID / ORIGINAL_GPU_COMMAND_INVALID 等实际原因后再查。当前此类记录定时重查，不自动修复或退款 |
| DELETE 已提交、owner 响应未知 | 重发原关闭命令，owner 墓碑约束晚 CREATE；不以超时取消原占额 |
| Gov release 提交、owner outbox ACK 前崩溃 | 原通知重发，由 Gov receipt/累计值幂等；owner 恢复后再确认本地 outbox |
| 同步 payload/ACK 损坏或永久错误 | 保留该条并阻断重试；从原 CREATE/charges 对账，不改业务账本。未知/瞬态错误重试原 payload |
| 数据库/进程重启、dump 恢复 | 显式迁移并核对原行/摘要；原 dispatch 租约接管、owner outbox 和周期 sync 各自恢复。只恢复一侧旧快照前必须评估命令与墓碑错位 |

外部稳定错误还包括 `MEMORY_GRANULARITY_MISMATCH`、`PROVIDER_INTEGER_OVERFLOW`、`PLAN_DIGEST_MISMATCH`、`CONFIG_DRIFT`、`ADMISSION_CLOSED`、`CONSUMPTION_NOT_VERIFIED`、`USAGE_PROJECTION_CONFLICT/NOT_READY`、`PAGE_SCOPE_MISMATCH`。按 gRPC status 和 ErrorInfo reason 判断；不能靠文本猜测退款。BFF 映射为参数 400、认证/授权 401/403、不可见或不存在 404、冲突 409、前置 412、依赖不可用 503、调用超时 504。

当前 `QuotaLedgerRepo.ResumeDispatch` 是保留原命令的受控恢复原语；实验控制路由不进入正式构建，不能在生产调用测试 `/control/*`。本文不声称存在正式通用 unblock RPC 或 sync 解除阻断 CLI。接入正式 owner 前须落实授权运维入口，记录原 tenant/operation/revision/generation、错误、原 payload/hash、修复证据及操作者；修复后重投原内容，禁止绕过摘要/FK、手工清 charge 或新造 CREATE ID。

数据恢复先关闭新受理和相关 worker，保留备份及脱敏证据；旧版本不能读 schema 2 canonical/新增模块时不可直接回滚二进制。不得自动 DROP/DOWN 带业务的新表。运行账号使用非 owner、非 superuser、无 BYPASSRLS/DDL/TEMP 权限；Governance 业务查询通过显式 tenant 条件的 Ent，Acc 使用 pgx/sqlc，RLS/policy 不作为隔离后门。

## 11. 接入验收模板

复用现有用例，新增真实业务部分；不能把另一版本或另一层的 PASS 拼成当前链路。每条记录精确 Gov/Acc/owner SHA、正式模块版本、schema/查询/生成摘要、命令、退出码、时间、原始日志和前后数据库断言，保留失败原记录。测试凭据与硬件证明私钥不得进入生产镜像或证据仓库。

| 验收范围 | 最少场景与现有入口 |
|---|---|
| 摘要、计量、附件、ACK | `TestGpuFrozenCanonicalVectors`、`TestGpuCanonicalRejectsSemanticMutations`、`TestGpuCanonicalUnicodeNullInteger`、`TestGpuChargesCompleteAndOrderIndependent`、`TestQuotaDurableAckFields`；需拒绝重新算对 hash 的非法 shape 和不完整非 GPU 向量 |
| 公共软件闭环 | [tests/contracts/gpu](../../app/admin/service/tests/contracts/gpu)：真实 PG/独立进程/mTLS，同键 12 并发、持久 ACK 丢失、DELETE 先到、outbox 响应丢失、旧操作同步重建、混合 charge 恢复、非空 live binding；测试业务控制入口不能进入正式 API |
| 受限数据库与进程故障 | [quota_process_pg_test.go](../../app/admin/service/internal/data/quota_process_pg_test.go)、[quota_gpu_pg_test.go](../../app/admin/service/internal/data/quota_gpu_pg_test.go)：各原子边界提交前 kill/提交后丢响应、双领取/取消、跨租户、迁移/还原、无双扣双退 |
| 身份/BFF | 20 委托 RPC 缺 grant、错 URI/CA、租户/actor/scope 游标负向；退款同 owner 多 SAN 与不同 owner 多 SAN；真实 JWT/权限/套餐模块及非空 DTO 脱敏 |
| A 正式边界 | 普通 Gov/Acc 二进制和合法受控 provider 来源；无消费证明时 Publish 必须真实失败且不落 profile/不变 VERIFIED；正式目录 NOT_ENABLED，新占额在 Resolve 前关闭 |
| B 软件装配 | 隔离 PG/CA，只有外部硬件证据端口和测试 owner 是 fixture；经过实际 SaveObservation 的 binding 与真实业务代码/Sync/BFF。记录 `hardware_observed=false` |
| C provider 回放 | 生产 TLS HTTP/Unix gRPC reader，单位/去重/局部冲突/stale/失联、可信范围解除；不能据构造来源宣称硬件实采 |
| 真实 owner 专属 | 真实业务消息/权限、模板防绕过、实际 renderer、API 写入未知对账、关闭执行点、全部历史/替换范围、签名/密钥轮换、可靠通知、持久恢复。当前 OWNER-01/02 未验证 |
| 多 owner | 新身份/公钥/adapter、查询/退款隔离和并发负向；当前 OWNER-03 未实现/未验证 |
| 真实 GPU 后续门禁 | F>1 的 plan→Pod→实际分配→运行显存限制；同父卡两个真实模型加载推理；K 满后 Pending 且不越预算；整卡排他/尾差/真实解除后容量恢复；真实 owner 账本与完整清理一致。LIVE-01..05 未验证 |

Governance 当前统一入口为 `make verify-ci`，Acc 入口仍为 `make verify`，在授权 Fedora 隔离环境执行；集成脚本见 [run.sh](../../scripts/experiments/gpu-contract/run.sh)，独占数据库、证书、端点和私有缓存等运行前提见 [GPU 软件合同实验说明](../../scripts/experiments/gpu-contract/README.md)。摘要表直接引用已有受测固定向量，不创造新“示例正确 hash”。完整 Goal 矩阵仍是 Acc `docs/plans/governance-accelerator-v1.2-acceptance.md`，本文不替代逐 ID 结果。

[历史 GOV-ACC-V12-01 软件联调证据](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/joint-software.md)明确区分 fixture 和真实事实，不能作为新 Inference 接线的验收结果。新的 `TestActualInferenceOwnerWiring` 使用实际 Inference RPC/PG 和实际退款客户端，暂停外部模型/Kubernetes 执行，在 Gov 已有持久 DELETE 后显式注入完成通知；其运行结果单独记录。真实模型/调度/硬件、Pod 签名与完整范围、创建封闭/在途写/完整清理、ObserveRelease、关闭事实/可靠 outbox、业务退款触发和真实恢复仍由推理负责人验收。ENDED 不清仍在观察到的 live binding；CPU 推理、sleep 容器、YAML 或 HTTP 存活不能替代这些门槛。

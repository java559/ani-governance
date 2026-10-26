# 模型研发训练与微调 BFF 合同

当前只有一套活动请求合同，普通训练与微调都经 Governance 当前用户认证、租户映射、ALL 数据范围和独立 API 权限。内部 ModelDev mTLS 地址不向前端开放。字段以 `api/protos/modeldev/service/v1/execution.proto`、admin HTTP声明与严格共享 Intent parser 为准。

| 用途 | 方法与路径 |
| --- | --- |
| 实现及实际参数规则 | `GET /api/v1/modeldev/implementations?page=1&pageSize=20` |
| 公共算力目录 | `GET /api/v1/compute-presets?usage=TRAINING&page=1&pageSize=20` |
| READY 输入 | `GET /api/v1/modeldev/input-versions`、`/{input_version_id}` |
| 创建/列表/详情 | `POST /api/v1/modeldev/executions`、`GET /api/v1/modeldev/executions`、`/{execution_id}` |
| 日志 | `GET /api/v1/modeldev/executions/{execution_id}/logs` |
| 停止 | `POST /api/v1/modeldev/executions/{execution_id}:stop` |
| 恢复原模型登记 | `POST /api/v1/modeldev/executions/{execution_id}:resume-model-registration` |
| 产物/下载 | `GET /api/v1/modeldev/executions/{execution_id}/artifacts`、`GET /api/v1/modeldev/artifacts/{artifact_id}/content` |

创建提交JSON对象：`name`、`kind`、`implementation_id`、`compute_preset_id`、`dataset_version_id`、必填对象 `parameters`、`idempotency_key`；`timeout_seconds`可省略，省略才采用冻结材料默认。普通训练唯一组合为 `GENERAL_TRAINING / builtin.mlp`，参数支持 `learning_rate`（非指数十进制字符串）、`epochs`、`batch_size`（正整数）和 `seed`（uint32）。参数对象允许 `{}`，重复键/未知键/null/错误类型拒绝；字段省略与显式默认保留为不同原意图。

微调唯一组合为 `FINETUNING / builtin.sft_lora`，另需 `base_model_version_id`。同时省略 `target_model_id` 与 `new_model_name` 时仅发布训练产物；需要向 Model 登记时二选一，同时提交仍拒绝。无目标仍校验真实 READY 基模并要求当前版本读取和下载权限，不要求模型创建、版本写入或目标模型读取权限，也没有可 Resume 的登记。参数支持 `learning_rate`、`max_steps`、`per_device_train_batch_size`、`gradient_accumulation_steps`、`cutoff_len`、`lora_rank`、`lora_alpha`、`lora_dropout`、`seed`；具体默认、数值表示和模型边界来自规则响应。用户不提交镜像/command/env、worker、重试、步骤时限或租户/actor身份。

实现目录返回实现版本、参数合同版本、规则、支持设备、当前可用性及默认/最大总时长、统一步骤上限。公共算力目录的可见性不代替创建资格。历史详情显示受理时固定的 implementation/compute 和原数值，不查询当前目录重建历史。训练 Release/preset/binding、组合审批和旧解码请求已退出当前产品。

创建202只说明原键受理已持久化，仍需轮询详情区分 compute/delivery/close/resource 四个状态。原键同意图返回原记录，异意图409；重放同样重查当前权限，已撤权不可读取历史。停止202只说明持久停止意图，不能显示尚未观察到的 CLOSED。GPU接线未完成时明确拒绝新 GPU 受理；不能先启动后补扣。

列表采用 `page/pageSize`、`items/total`，详情直接资源对象。查询/日志/下载禁止缓存；日志只返回有界真实内容，不接受Pod/namespace选择器。下载仅向当前授权调用者返回短期URL；URL不得进入持久日志或快照。普通训练产物无需强制模型登记，微调成功另要求真实 export/verify、publication确认与Model登记；独立使用与计算清理/退款分别观察。

当前代码改动、Fedora检查、目标集群真实执行与页面产品验收分别记录。本次合同文档不表示这些验收已经完成。

## 原微调登记恢复与历史投影

恢复请求仅有路径中的原 `execution_id`，body 必须为空且不得带 query（包括空 `?`）；
`{}` 也不接受。调用者须为当前正常租户用户：当前 `modeldev:manage_execution` 对
Resume POST 的 ALL 数据范围、原执行 GET 权限，以及原意图所需的 Model 权限均重新检查。
Model 权限包括版本 GET、版本 download-url GET、版本 POST；原目标为已有模型时另需
该模型 GET，原目标为新模型时另需模型 POST。目录可见、历史操作者的权限或内部服务证书
均不能补授当前用户权限。同租户当前获权操作者可以与最初受理者不同；跨租户原记录不调用 owner。

Governance 从本租户原持久受理读取 FINETUNING source，原目标、新模型名称、基模、参数、
registration key 和冻结 Timing 不接受本次覆盖。owner 仅对已 CLOSED 执行恢复原登记或
核实原未知结果，不重训、不重新 export、不新受理执行；显式恢复使用 owner 持久的有限恢复窗口，
不延长原训练/步骤截止时间。未知响应后先读原 execution 的事实，不由客户端自动重试写入。

成功 HTTP 200 直接返回同详情的执行投影，不增加包装或第二套状态：

| 只读字段 | 当前含义 |
| --- | --- |
| `model_registration.state=PENDING` | 尚无已验证登记 IDs，三个 ID 和 failure_code 为空 |
| `model_registration.state=DEPENDENCY_UNAVAILABLE` | 无 IDs，唯一公开 failure_code 为 `MODEL_DEPENDENCY_UNAVAILABLE` |
| `model_registration.state=READY` | 原 provider 已验证 receipt 的 `registration_id`、`model_id`、`model_version_id` 均为规范非零 UUID，failure_code 为空；已有目标的 model_id 必须仍为原目标 |
| `history` | 原持久关闭证据的 run_id、workflow_uid、observed_at、run_state/run_finished_at、workflow_phase/workflow_finished_at、workflow_resource_version、workflow_nodes_sha256；缺失终态事实保留未知 |

`history` 的已知 Run 终态为 SUCCEEDED/FAILED/CANCELED，Workflow 为 SUCCEEDED/FAILED/ERROR；
时间、成组身份和 hash 由严格 client 验证。恢复 READY 可以与原 FAILED Run 同时存在；
BFF 不实时拼 Model/KFP 查询来改写原失败历史，不从 publication、CLOSED 或 HTTP 200 推断 READY。
返回执行仍须匹配原 operation/execution/spec hash、implementation/compute、accepted_at 和原 Timing。

内部唯一调用为 `/ani.modeldev.v1.ModelDevOperationsService/ResumeModelRegistration`，沿用
TLS1.3、精确单一 Gov DNS SAN、ModelDev 字面 ServerName、当前 tenant/actor 和精确方法委派。
每次仅一次 RPC，继承更短父 deadline 与取消，关闭自动 retry。

有限 owner 预算只用于精确训练创建、产物 content grant、Resume，以及 Model 上传地址和版本创建入口；
不把全部列表/日志/管理 API 延长。Model client 默认 30s，ModelDev client 默认 3s；HTTP owner
预算最大 24h，Model client 同样拒绝超过 24h。实际配置、consumer/receiver 预算与更短父期限须一致。

Fedora 的真实 JWT/Redis/Casbin、受限 PostgreSQL 原受理和 mTLS 外部 MD 边界测试已 GREEN，
含逐项撤权、跨租户、原 target/hash 不一致、未知响应仅一次调用和原行/hash 不变。
具名收据为唯一 PLAN 的 `records/workloads-governance-registration.json`；Model client 有限预算
RED/GREEN 为任务目录 `contracts/governance-model-budget/receipt.json`。这些是候选源码与边界验证；
真实 provider Resume、完整微调到独立模型使用仍 **NOT_RUN**。用户本轮明确排除前端源码/部署修改，
本文件只交接既有 BFF 合同，不宣称页面或整项产品验收通过。

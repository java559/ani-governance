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
| 产物/下载 | `GET /api/v1/modeldev/executions/{execution_id}/artifacts`、`GET /api/v1/modeldev/artifacts/{artifact_id}/content` |

创建提交JSON对象：`name`、`kind`、`implementation_id`、`compute_preset_id`、`dataset_version_id`、必填对象 `parameters`、`idempotency_key`；`timeout_seconds`可省略，省略才采用冻结材料默认。普通训练唯一组合为 `GENERAL_TRAINING / builtin.mlp`，参数支持 `learning_rate`（非指数十进制字符串）、`epochs`、`batch_size`（正整数）和 `seed`（uint32）。参数对象允许 `{}`，重复键/未知键/null/错误类型拒绝；字段省略与显式默认保留为不同原意图。

微调唯一组合为 `FINETUNING / builtin.sft_lora`，另需 `base_model_version_id`，以及 `target_model_id` / `new_model_name` 二选一。参数支持 `learning_rate`、`max_steps`、`per_device_train_batch_size`、`gradient_accumulation_steps`、`cutoff_len`、`lora_rank`、`lora_alpha`、`lora_dropout`、`seed`；具体默认、数值表示和模型边界来自规则响应。用户不提交镜像/command/env、worker、重试、步骤时限或租户/actor身份。

实现目录返回实现版本、参数合同版本、规则、支持设备、当前可用性及默认/最大总时长、统一步骤上限。公共算力目录的可见性不代替创建资格。历史详情显示受理时固定的 implementation/compute 和原数值，不查询当前目录重建历史。训练 Release/preset/binding、组合审批和旧解码请求已退出当前产品。

创建202只说明原键受理已持久化，仍需轮询详情区分 compute/delivery/close/resource 四个状态。原键同意图返回原记录，异意图409；重放同样重查当前权限，已撤权不可读取历史。停止202只说明持久停止意图，不能显示尚未观察到的 CLOSED。GPU接线未完成时明确拒绝新 GPU 受理；不能先启动后补扣。

列表采用 `page/pageSize`、`items/total`，详情直接资源对象。查询/日志/下载禁止缓存；日志只返回有界真实内容，不接受Pod/namespace选择器。下载仅向当前授权调用者返回短期URL；URL不得进入持久日志或快照。普通训练产物无需强制模型登记，微调成功另要求真实 export/verify、publication确认与Model登记；独立使用与计算清理/退款分别观察。

当前代码改动、Fedora检查、目标集群真实执行与页面产品验收分别记录。本次合同文档不表示这些验收已经完成。

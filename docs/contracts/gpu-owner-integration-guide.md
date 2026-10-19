# GPU owner 集成合同

当前合同使用每卡具体显存 MiB，供给 F=1。管理员目录与执行快照分开：停用或软删除规格阻止新解析，已受理的新合同仍按原快照恢复和删除。没有测试报告、人工资格或有效期准入。

本期实际接入 owner 为 Resource 容器实例。Governance 已移除 Inference 管理接口、API 模块依赖及出站装配，普通和 GPU 推理管理均不提供；后续重新对接另行发布合同。真实 CUDA、物理释放与退额的结果以任务验收回执为准，软件测试不能代替硬件验证。

## 1. 主链与写入职责

管理员创建 ACC Profile → Resource Preset 组合 CPU/主机内存 → 用户选择 Preset → Gov 当前用户权限与套餐检查 → Res 受管镜像/规格解析 → ACC 最终执行参数与计量 → Gov 原子占额及持久命令 → Res 持久受理 → worker 写 Kubernetes → Volcano 分配 → CUDA → 完整删除和创建封闭 → Res 释放 outbox → Gov 按原账全退 → ACC 观测容量恢复。

ACC 只维护供给、目录、最终 GPU 执行参数、Usage 投影与物理绑定；Gov 拥有唯一额度账本与可靠投递；Res 拥有实例、工作区、Kubernetes 副作用及完整释放事实。任何一方不得直接写其他业务服务的库。

## 2. 公共协议与发布顺序

- GPU 公共类型来自正式 ACC 模块的 `accelerator.v1`；公共 durable ACK 位于 `accelerator.command.v1`。
- 释放协议位于独立 `github.com/zhangzhe-ctrl/ani-governance/api/quota`，权威源为 `api/protos/quota/service/v1/quota_release.proto`。固定入口 `make api-quota-release`；生成产物在 `api/quota/gen/go`。
- ACC 与 Gov quota 提供方先发布，Res 消费精确版本后发布，Gov 最后消费正式可下载版本。Inference API 不在当前依赖链中。使用 `GOWORK=off`，记录 `Origin.Hash` 与校验和，不使用本地 `replace` 或复制生成代码。
- Gov BFF 使用本仓 `make api` 的固定插件与暂存保护流程；生成代码不得手改。

## 3. 创建合同

`AcceptCreate` 仅包含 `context` 和扁平的 `spec`。Context 是可信租户、资源 ID、操作 ID 和整个命令摘要；actor 审计信息从 Gov 已认证并持久保存的上下文重建到内部 metadata。Res 验证调用证书、方法、租户和资源归属。

Spec 保存真实实例业务参数、展示信息及一份 GPU 执行快照。镜像使用不可变 digest reference；CPU、主机内存、受管集群、namespace、ServiceAccount、UID/GID、shell、存储和 scheduling 继续属于真实业务参数。临时 `gpu_selection` 只用于 Res 解析响应，Gov 获得 ACC 结果后清除，Res 拒绝把它持久化为第二份 GPU 权威。

`GpuExecution` 的固定字段仅为 scheduler、queue、RuntimeClass、node selector、annotations、resources、execution digest。资源白名单仅为 `volcano.sh/vgpu-number`、`volcano.sh/vgpu-memory`、`volcano.sh/vgpu-cores`；显存数值就是 MiB。共享以具体显存与 1..99 核心申请，整卡以该组单卡全部可用显存及 100 核心申请。RuntimeClass 只来自 ACC 受管配置。

Res 不接收或持久保存原账、charge ID/code/units、完整 Plan、Encoding/Totals 或第二份执行参数。Gov 的原始公共请求、原计量与原账留在 Gov。执行摘要由 ACC 公开 `pkg/gpuprotocol` 固定六字段规范化计算；未知执行字段和额外资源键均拒绝。

## 4. 删除与完整释放

`AcceptDelete` 只包含可信 context、原 create operation、expected version、instance kind 和 compute type。没有 GPU Plan 或 charges。持久 DELETE 封闭原 CREATE；DELETE 先到、迟到 CREATE、ACK 丢失和未知写确认仍由 Res 真实持久化和副作用逻辑保证。

Res 只有确认计算资源已清理、没有残留 GPU 访问、没有在途或待确认创建，才持久化释放 outbox 并调用：

```json
{
  "release_event_id": "<stable UUID>",
  "create_operation_id": "<original CREATE UUID>",
  "resource_id": "<resource UUID>",
  "reason": "RESOURCE_RELEASED"
}
```

失败清理使用相同四字段及 `ABORTED_CLEANED`。普通退出、Stop、重启、NotFound、超时、durable ACK、Usage ENDED 和单次 binding 缺失都不能触发完整退额。从未分配 GPU 的 Pending 实例可以在可靠封闭创建并确认无残留后走同一合同。

`QuotaReleaseService/ReportResourceReleased` 仅在内部 TLS 1.3 双向认证 listener 注册。客户端证书必须含唯一精确 DNS SAN，映射到已登记 owner，且只允许登记方法。请求没有可信 tenant/owner 字段；Gov 用认证 owner 和原 CREATE 查租户，锁定租户、原操作及 charges，再核对 resource。退款数量只取原账剩余量，不按现有目录重算。

同 event 不同内容拒绝；同资源换 event 重报不重复退款。新完整事件使用现有全局唯一 receipt ID 约束封闭跨租户或跨 owner 重用。响应回同 event/create/resource，status 为 `APPLIED` 或 `ALREADY_APPLIED`；Res 仅接受引用全部匹配的确认。

旧 `ReportQuotaRelease` 仅保留非 GPU 的独立累计释放语义，新的 GPU code 不能走该入口。本期不新增 CPU、主机内存或 GPU 核心 quota。

## 5. Usage 与容量

Gov→ACC 异步 Usage 只有 ref、revision/state/end reason/source fact/time、Profile ID/version、group/baseline/cluster/container 及 execution digest。它不带完整 Plan，不重新加载当前目录重算历史实例；Res 无需等待 Usage 才创建。

用户 Preset 先由 Res 在同一租户授权快照内分页过滤，再显示目录状态及当前容量提示：`GPU_PROFILE_DISABLED`、`GPU_PROFILE_DELETED`、`GPU_SUPPLY_PAUSED`、`GPU_CAPACITY_UNKNOWN`、`GPU_TEMPORARILY_FULL` 或 `GPU_CATALOG_UNAVAILABLE`。CPU 查询不依赖 ACC。容量提示不预留设备，也不成为受理快照的重放或生命周期条件。

绑定身份签名绑定真实 cluster/namespace/Pod UID/container、tenant/owner/resource/create operation 和新 execution digest。Usage ENDED 不清除活绑定；ACC 只按真实物理释放恢复容量。多规格共用逐卡物理总账，视图不能相加；未知占用保留，证据缺失不能宣告空闲。

## 6. 切换与验证边界

旧活动实例、在途投递、未知写、释放 outbox 和原账必须在切换前实际盘点。终态资料只读操作引用、请求摘要和回执；服务不把旧 raw Plan 送入新执行解码器。协议和 F1 切换后不回退旧合同、旧百分比或 F256。

回归入口为 `internal/service/instance_pg_test.go`、`internal/server/instance_joint_test.go` 与现有账本/内部 mTLS 测试。前者验证真实 Gov/Resource 业务和 PostgreSQL，ACC 在该模块回归中是显式协议边界替身，不能算完整主链；后者连接真实 ACC 与 Resource subprocess，并明确区分外部 Kubernetes 替身和实际集群。旧独立 fake owner 实验已退役，历史在 Git 中保留。

提交前执行 Fedora 上 `make verify-ci`（本仓无 `make verify` 目标）。真实验收另需首条 4 CPU/8192 MiB 主机内存/8192 MiB GPU、共享混用、整卡、真实 CUDA、删除封闭、物理释放、ACC 容量及 Gov 原额归零。官方正式版 Volcano 通过之前保留当前修复版；相关 PR 暂不合并。

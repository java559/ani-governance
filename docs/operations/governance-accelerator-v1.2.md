# GOV-ACC-V12-01 运行、迁移与恢复手册

本文保留 GOV-ACC-V12-01 历史批次的操作与结果。当前 GPU 实例及 Inference 接线以 [GPU owner 合同](../contracts/gpu-owner-integration-guide.md) 和 [部署说明](../deployment.md) 为准：保留 Resource 实例链，并由 Governance 通过 mTLS 调用 Inference 管理 API。下文旧 Inference owner 配置与启用步骤仅作历史记录，当前参数以部署说明为准。

本手册描述本批交付实现的操作边界，配套[逐项验收结果](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/acceptance-results.md)、[数据层证据](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/data-layer.md)、[owner 接入指南](../contracts/gpu-owner-integration-guide.md)及[接口登记](../interface-integration-register.md)。操作步骤不等于已经执行；实际版本、CI例外和已执行的清理见[收尾记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/release/closeout.md)。本批没有生产部署或真实 GPU 验收。

## 1. 版本与当前运行边界

本批历史记录中，Acc 固定为已发布提交 `1d32dd9a9173b8869fa0ef2ae64e20b88f2ca0a3`，Gov 当时依赖为 `v0.0.0-20260924030150-1d32dd9a9173`；当前依赖以本仓 [go.mod](../../go.mod)/[go.sum](../../go.sum)为准，不能用历史版本锁替换。该批 Gov 最低实现版本为 `bd9ad1a33bbe8ce28f4faeb19bfc9ee494ae8a84`；后续纯证据提交不改变该批能力。历史 `9f9712198488` API 模块测试只能证明当时版本。

GOV-ACC-V12-01 当时的正式构建没有 GPU owner adapter、业务创建 BFF 或退款 owner map；该批两个正式 code `gpu.physical.count`、`gpu.shared_memory_mib` 均为 `NOT_ENABLED`，只读/管理 Accelerator BFF 可以独立运行。当时新创建必须在 Resolve/占额之前失败关闭；旧操作仍保留，不能因 adapter 缺失清账或退款，`gpu.count` 保持原 LAB 语义。配置下游地址、添加套餐额度或打开环境变量本身不会注册 owner。

后续固定 Inference owner 的源码接线、BFF、退款映射和开放条件以 [当前 GPU owner 合同](../contracts/gpu-owner-integration-guide.md)及 [GOV-ACC-INF-V12-WIRING-01 登记](../interface-integration-register.md#gov-acc-inf-v12-wiring-01推理创建与删除接线2026-10-09) 为准；实际部署和真实 GPU 验收另行确认，本节历史批次结论不代替当前能力核对。

生产监听不加载测试 owner、故障 HTTP 控制入口、消费证明私钥或 skipVerification。`SyncGpuUsage` 是内部恢复 worker，`ObserveRelease` 由可信 owner 直接调用；二者没有公网代理。当前 Acc 只支持 `ani-inference` 的合同标识，多 owner 需要另批身份、公钥、查询和 adapter 改造。

本批所有运行类工作只在 `ssh fedora` 的任务目录 `/home/chabking/gov-acc-v12-01-20260923` 执行，使用私有源码副本、cache、lock、回环端口及受限库；`GOWORK=off GOMAXPROCS=2 GOFLAGS=-p=2`。本手册中的命令均须在授权的隔离副本中执行，不能拿联调业务库运行会清理账本的测试。

## 2. 唯一结构与查询来源

2026-09-24 纠正：pgx/sqlc 要求仅适用于 Acc。此前把 Gov 配额账本一起迁移属于范围扩大，现已恢复 Ent。既有版本迁移与数据约束保留，本次不改 schema、不要求重跑迁移。旧验收记录只证明其对应历史实现，当前 Ent 回归证据另行记录。

| 范围 | 权威链及执行职责 |
|---|---|
| Gov 结构 | `app/admin/service/internal/data/ent/schema/` → 固定 Ent v0.14.6 + `cmd/schema/constraints.sql` → `app/admin/service/schema.sql` → 审查后的 Atlas 版本迁移 |
| Gov 查询 | Ent schema → Ent 生成的 Query/Create/Update/Delete；完整通用 quota、plan quota、dispatch、DELETE、release 和 sync 直接使用 Ent |
| Gov 原子事务 | `quotaTransaction` 通过 `ent.Client.Tx` 创建单个 `ent.Tx`，全部查询和写入由该事务执行并提交/回滚；没有 Conn.Raw、pgx 事务或 sqlc 桥接 |
| Acc 结构/查询 | 固定上游正式 migrations → sqlc；不将设计 SQL 或 Gov schema 复制为 Acc 结构来源 |

Gov 版本迁移按 [migrations](../../migrations/) 现有链执行：初始 `20260921134442_initial`、既有 quota expand/constraints `20260922190000`/`20260922190100`，再到本批：

| 迁移 | 本批作用 |
|---|---|
| [20260923130000_gpu_sqlc.sql](../../migrations/20260923130000_gpu_sqlc.sql) | 补必需 tenant/plan、tenant-preserving 关系、独立 DELETE acceptance 与 usage sync、完整事务约束 |
| [20260923130100_gpu_no_rls_catalog.sql](../../migrations/20260923130100_gpu_no_rls_catalog.sql) | owned 表显式取消 RLS/FORCE RLS/残留 policy，增量插入两正式 GPU code；不授予额度或启用执行 |
| [20260923130200_gpu_sync_ref.sql](../../migrations/20260923130200_gpu_sync_ref.sql) | sync JSON/ref 与关系列一致、NULL/缺字段负向、revision/state 与独立 retry blocking |

`migrations/atlas.sum` 随链交付。**不能重复裸执行目录 INSERT SQL**；重复部署依赖 Atlas 版本记录跳过已应用迁移，不靠重跑 bootstrap 或手工忽略唯一键错误。已有数据库不能套初始建表 SQL，先确认其真实迁移版本；无可追溯基线或结构不匹配时先在副本比对并制定明确升级方案。

本批实际迁移工具为 Atlas community **v0.36.0**，sqlc **v1.30.0**、Ent **v0.14.6**；具体二进制 checksum 见数据层/最终生成证据。通用部署文档中的其他 Atlas 镜像示例不是本批已验证工具版本，不使用 `@latest` 或现场生成新迁移。结构生成与部署执行分开，服务运行镜像不承担 Atlas/Go 生成。

## 3. 显式迁移与运行角色

迁移前暂停新受理及相关 worker，确认目标库、owner、备份和恢复副本。迁移 owner 与应用 runtime 是不同账号，应用不继承 owner 角色。由受控迁移会话注入 `ANI_DATABASE_DSN`，不要把密码写在命令行或日志；不要开启 `set -x`。

以下是已存在的仓库入口，变量必须由授权环境提供，不是可直接复制的真实凭据：

```sh
# Fedora 已固定 PATH 中的 Atlas v0.36.0；当前目录为受审查的 Gov 副本。
: "${GOV_MIGRATION_DSN_FILE:?受保护的目标迁移连接文件}"
export ANI_DATABASE_DSN="$(cat "$GOV_MIGRATION_DSN_FILE")"
./scripts/atlas.sh migrate status
./scripts/atlas.sh migrate apply --dry-run
# 核对 dry-run 仅为批准链后，再显式执行：
./scripts/atlas.sh migrate apply
./scripts/atlas.sh migrate status
unset ANI_DATABASE_DSN
```

[scripts/atlas.sh](../../scripts/atlas.sh) 进入 `app/admin/service` 并使用 [atlas.hcl](../../app/admin/service/atlas.hcl) 的 `governance` 环境。`ANI_ATLAS_DEV_DSN` 只用于开发期 diff 的独立可清空库，不能指向目标业务库；本次部署只 apply 已审查迁移。

升级已有库不执行 `admin init`。首次空库初始化另按[通用部署说明](../deployment.md)独立完成；服务启动始终 `data.database.migrate: false`，true 会报错，不能作为自动升级开关。升级后明确执行 API 目录 dry-run、增量同步及 [Accelerator 权限补丁](../../scripts/ops/sql/register-accelerator-permissions.sql)，不自动给租户角色授权：

```sh
# 同版本 admin 制品；由受控运维身份连接已迁移的目标库。
./bin/admin sync-apis --dry-run
./bin/admin sync-apis
```

权限补丁显式登记 `(method,path)` 关联；套餐 ACCELERATOR 模块、精确角色权限、运行实例策略刷新和 Acc 下游 grant 是独立步骤。目录存在或 HTTP 200 健康检查不证明业务授权完整。

runtime 必须是非表 owner、非 superuser、无 BYPASSRLS、无 schema/database CREATE、无 TEMP，且无可继承的 owner/DDL 权限。运行账号只拥有所需表 DML、序列及 schema USAGE。参考[运行授权 SQL](../../scripts/ops/sql/grant-runtime-role.sql)、[数据库权限收紧 SQL](../../scripts/ops/sql/restrict-database.sql)；它们含 psql 变量且会收紧 PUBLIC 权限，执行前审查实际数据库和所有合法使用者，不能对共享未知库照搬。

用**实际 runtime 连接**执行[角色/RLS 审计 SQL](../../scripts/ops/sql/audit-runtime-privileges.sql)，确认 flags、owner、policy、tenant nullable 和权限；再通过受限角色测试确认 DDL/TEMP 被拒绝。所有租户读取/写入/幂等/分页/后台 CAS 显式保留 tenant；禁用 tenant GUC、SET ROLE、SET row_security 或提升成 BYPASSRLS 来绕过错误。全局目录不是 tenant=0 业务数据。

## 4. 受控配置与启动/停机顺序

启动前按制品版本核对配置、数据库迁移、证书有效期和服务端 DNS。Gov→Acc 配置见 [accelerator.env.example](../../app/admin/service/configs/accelerator.env.example)：未配置地址时禁用下游；配置后 CA/cert/key/server name 必须完整。示例 DNS 是样例，必须匹配部署实际服务端证书；客户端叶证书只有精确 URI `spiffe://ani.internal/service/ani-governance`。

20 个委托 RPC 还要求 Acc 配置的 actor/action/tenant/cluster grant；用户 JWT 权限不能代替它。Sync 使用 Gov 直接服务身份，不依赖用户当前 grant。owner→Gov 退款使用独立 DNS SAN→owner map，不能误套 Acc 唯一 URI 规则。2026-10-09 后源码已编译接入固定 `ani-inference` owner，由 `ANI_QUOTA_INFERENCE_DNS_SAN` 配置其精确部署证书身份；listener 默认关闭，缺必要配置会启动失败。完整当前接线和配置以 [GPU owner 指南](../contracts/gpu-owner-integration-guide.md)为准；源码注册不证明目标部署或真实 GPU 验收已完成。

建议的依赖与放流顺序：

1. 确认目标 Gov/Acc/owner 各自数据库、显式迁移和 runtime 权限，Redis 可用；备份/版本锁可定位。
2. 启动受控 Acc 普通服务及其合法 provider 读来源；无硬件消费证明时保持 CLOSED/NOT_VERIFIED。不要向普通服务挂入 B 层测试密钥。
3. 用冻结制品启动 Gov 普通服务；核对普通配置 `migrate=false`，证书/DNS/委托条件、管理只读和目录 NOT_ENABLED。没有正式 owner 时不开放业务创建入口。
4. 当前 composition 在 Gov 进程内注册 dispatch worker；配置有效 Acc client 时注册独立 usage sync worker。两者由应用生命周期管理，不是两个可随意启停的公开管理 API。开始恢复前确认下游服务身份和历史数据兼容。
5. 当前固定 Inference owner 仍须核对实际装配、受支持的 quota code、权限/套餐和目标环境验收，再开放新创建。新增 owner 必须另行通过接入门禁并显式注册 action 和退款映射；B 层持久测试 owner 的启动不能代替正式接入。

应用框架的 Server 列表不承诺严格串行 Start/Stop；不要依靠数组位置解决外部依赖。现有公网接口也没有生产 pause/unblock 管理 RPC。维护时先在既有入口/部署流程停止新受理，再优雅停止所有相关 Gov 副本，以停止领取及等待在途操作；记录 SIGTERM、退出码、仍未完成命令和租约。不能把撤掉套餐当成禁止可信退款或抹掉历史业务的措施。

dispatch Stop 会取消数据库/RPC上下文并有限等待；失败退出应留证据，不强行写 ACKED。进程重启创建新 worker 对象，原租约由持久 generation/CAS 接管。停机后仍要保留 owner 未确认 outbox、关闭墓碑及原 CREATE/charges。需要停止 owner/Acc 时，先确认相关 Gov/owner 调用已排空或可按原 ID 重试；最后才停依赖库和 Redis。

## 5. 日常对账与独立投影恢复

排障分别读取三类事实：Gov 原操作/账户/charges/receipt；owner 命令/墓碑/outbox；Acc 使用投影/实际 binding/容量。不要把 ACKED 当 Ready、DELETE ACK 当清理完成、ENDED 当设备空闲、容量 UNKNOWN 当零或把 projection 缺失当资源不存在。

Gov 权威持久对象为 `sys_quota_operations`、`sys_quota_charges`、`sys_quota_accounts`、`sys_quota_release_receipts` 和 `sys_gpu_delete_acceptances`。`sys_gpu_usage_sync` 仅保存派生进度，不能成为退款或受理的必写前置。对账以可信 tenant + 原 CREATE 为作用域，核对原 canonical、完整 quota_items/charges、原 resource UUID/owner、DELETE 意图与累计 receipt；严禁只看第一笔 charge。

usage worker 每秒触发有界扫描，按原 CREATE 分页，每轮结束回绕。它读取原 operation、完整 charges 和 DELETE 事实派生 revision：

- revision 1 DECLARED：已受理且没有合法终结依据。
- revision 2 ENDED：原 CREATE 未发送而安全取消且原全集合退完；或者已有明确 DELETE 且原 GPU 子集全量累计退完。后一种不等待无关非 GPU charge 归零。
- 空集合、损坏 canonical、摘要/计量/ref 不一致产生错误，不用空集合 all 条件生成 ENDED。

扫描是持续恢复来源，Notify 只是优化。CREATE 提交后尚无 sync 行就崩溃、旧行后来退款或派生行丢失，健康 worker 能从原事实补齐。相同 revision 的 ref、完整 plan、原 CREATE 时间、source fact、payload/hash 必须相同；不使用当前 F、目录、容量或重试时间重新生成业务计划。

sync 每次在 RPC 前单独领取，15 秒 lease、3 秒调用超时；网络在事务外。回写核对原 tenant/operation/revision/generation/实际领取状态，旧 ACK 不确认新 revision。传输故障按 1/2/4/8/16/30 秒上限退避重发原 payload；同步不得修改业务 dispatch attempt、charges 或余额。

发现缺行时先确认原事实完整并恢复正常 worker，**不要先删全表或重置所有 ACK**。旧 `restore_pending_projection.sql`、`failure_fixture`、`formal_fixture_clear_ledger`、`old_version_fixture` 故障夹具已退出 HEAD，不能从历史提交取出后在运行库执行。跨仓合同的 `governance-seed.sql` 只用于独占测试库的显式准备，见 [实验入口](../../scripts/experiments/gpu-contract/README.md)。

## 6. 错误、永久阻断与原 charge 损坏

| 情况 | 已实现行为 | 运维处理 |
|---|---|---|
| Unavailable/deadline/未知传输结果 | 原命令/投影和占额保留，退避重试 | 修复依赖后观察原 ID 收敛，不新造幂等键规避未知副作用 |
| 无 adapter | 历史账本保留，dispatch记录 ADAPTER_MISSING 后重试 | 恢复正确编译装配；不是自动退款或重新占额理由 |
| 业务非法 ACK 或永久 gRPC 合同错误 | UNKNOWN + retry_blocked，保留 charge/原命令 | 查清真实 owner 是否持久接受，修复映射/身份；只重投原命令 |
| 同步 USAGE_PROJECTION_CONFLICT | 按稳定 reason 永久 block，保留原 payload/hash | 对账两端原 CREATE/ref/plan/time/revision；不能更换 create ID、改 hash 或覆盖接收端让错误消失 |
| 同步 payload/ACK 损坏 | 该条保留并阻断；其他合法记录可继续 | 区分原事实损坏与派生内容损坏，保存原副本和摘要后制订定向恢复 |
| ORIGINAL_CHARGES_UNAVAILABLE/INVALID、ORIGINAL_GPU_COMMAND_INVALID | 不调用 owner，记录错误并定时重查，保留账本 | 修复读取依赖或从可信原记录恢复完整集合；不能删掉坏行、退首项或跳过验证 |

当前 `QuotaLedgerRepo.ResumeDispatch` 是带 tenant 的内部恢复原语；仅实验装配有控制路由。**没有正式通用 unblock API，也没有生产 sync unblock CLI。** 正式 owner 接入前必须明确受控运维入口；不要调用测试 `/control/*` 或凭文档杜撰 RPC。

永久阻断的处置必须记录作用域、错误、原内容/hash、revision/generation、修复理由、操作者和数据库前后状态。若需要修改派生状态，先停止会竞争的 worker，在独立恢复副本验证，再审查定向运维脚本；脚本应显式匹配 tenant/operation/预期 revision/hash/generation，限定影响行数，并保留审计。不能用无 WHERE 的 UPDATE 清除全局 block，也不能把失配的同 revision 当新事实覆盖。真实原账本出现合法新终态时，新的 revision 可按实现使派生工作重新调度；伪造终态不属于恢复。

原 charge 损坏时，先冻结该原操作的人工变更并保留完整快照。比对原不可变 canonical 的所有 quota_items、历史备份、receipt、owner 已持久命令，确认原 charge ID/code/units/tenant/operation/released_total。只有可信证据足够才在审查后的定向事务恢复原记录；恢复后重算账户不变量，验证原集合与 GPU 子集完整，再由正常 worker 重投原 DELETE/CREATE。证据不足时保持阻断，不按当前套餐/当前 plan 猜数量、生成替代 charge 或直接把账户减到零。

退款只由持有效身份的原 owner 回调，使用原 CREATE 和完整 GPU 子集全量累计，且 Gov 已持久 DELETE 意图；不要求 DELETE 已 ACKED。普通退出、失败、restart、API NotFound、超时、部分清理和 ObserveRelease 单独结果均不授权退款。已知纯非 GPU 部分累计退款保留原语义，不应被故障处置误改成 GPU 终态。

## 7. 备份、另库恢复与回退

备份清单至少包含：两仓/owner 制品及配置摘要、模块 sum、迁移版本/atlas.sum、完整数据库 dump、表计数/规范行摘要、原命令与 receipt/DELETE/sync、owner 墓碑/outbox，以及访问这些恢复输入所需的受保护证书/key/配置位置。密钥与 DSN 不复制到 Git；脱敏证据和私有恢复材料分开保留。

数据库备份可用受控连接服务配置执行，下面的 service 名、文件名和目标库必须由操作环境先核实；代码块只是明确入口，不表示已执行：

```sh
: "${GOV_BACKUP_SERVICE:?pg_service.conf中已核对的源库只读备份连接}"
: "${GOV_BACKUP_FILE:?任务保护目录中的新dump绝对路径}"
umask 077
test ! -e "$GOV_BACKUP_FILE"
PGSERVICE="$GOV_BACKUP_SERVICE" pg_dump --format=custom --file="$GOV_BACKUP_FILE"
pg_restore --list "$GOV_BACKUP_FILE" > "$GOV_BACKUP_FILE.contents"
sha256sum "$GOV_BACKUP_FILE" > "$GOV_BACKUP_FILE.sha256"
```

恢复前由迁移 owner **显式建立不同目标库**，确认目标为空且不是原业务库。使用受管目标连接恢复，例如 `PGSERVICE="$GOV_RESTORE_SERVICE" pg_restore --exit-on-error --no-owner --no-privileges --dbname="$GOV_RESTORE_DB" "$GOV_BACKUP_FILE"`；执行前要求这两个变量已核对为独立目标，恢复后按目标 owner/runtime 重新授予受审查的权限。不要使用 `--clean` 覆盖运行库，也不要用 bootstrap 代替恢复。

恢复顺序：

1. 保持新受理与所有相关 worker 停止；记录原库仍保留、备份 hash 和目标库唯一身份。
2. 恢复完整 dump（包括序列/版本记录），比较逐表行数与规范摘要，参照 [compare-table-content.sql](../../scripts/ops/sql/compare-table-content.sql)。先确认恢复版本，再显式迁移缺少的批准版本。
3. 用 runtime 检查 no-RLS/policy、非 owner/noDDL/TEMP、tenant/ref/FK/JSON约束；核对原 charge/released/account 不变量和 DELETE/receipt 唯一性。
4. 分别恢复 owner 原命令、墓碑和通知 outbox；查清 Gov 与 owner 备份时间差可能导致的在途命令/退款。不得仅恢复较旧 Gov 账本就认为 owner 不存在晚执行。
5. 在独立恢复环境先恢复原 dispatch、outbox 和 sync，验证原 ID 幂等、重复累计 delta=0、晚 ACK/CAS 拒绝和投影重建；不重seed、不清恢复数据后重新跑一条“干净闭环”。
6. 记录差异和恢复收敛回执后，再决定部署侧切换；本 Goal 未授权真实生产切换或恢复覆盖。

旧二进制若不理解 ACCELERATOR 枚举、schema 2 GPU canonical、新增表/约束，不可直接降级。回退选择是已证明兼容的制品，或保留新版本恢复工具、在另库还原受审查备份后制订切换方案。新业务数据存在后不自动 DROP/DOWN/清库，也不因删除 sync 表看似能重建就删除原权威账本。

## 8. 验证入口和历史暂停

当前 Gov `make verify-ci` 串行运行 `check`、`test-integration`、`check-generated`；它覆盖格式、配额 Ent 执行边界扫描、受影响测试与真实 PG/race、完整生成和固定漏洞/secret 审计。旧 `verify-gpu` 的运行记录见[数据层历史复跑说明](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/data-layer.md)，只对应旧 SHA。普通 data suite 会清理它自己的测试账本，**只能使用其独立 data 库**；不能把 joint-B、owner 或恢复业务库 DSN 传给它。

当前跨仓软件合同入口为 [GPU 软件合同实验](../../scripts/experiments/gpu-contract/README.md)；旧 `scripts/accelerator-acceptance/run-joint-contract.sh`、BFF 和正式 A 边界脚本只作为固定历史版本的来源阅读。复跑使用明确不同 A/B DSN/CA/config，Acc 使用其固定版本的 `make verify`。本批原结果保留在[95项验收结果表](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/acceptance-results.md)；新结果记录自己的精确版本、source manifest、命令/退出码、原始失败与复跑，不用 component PASS 代替整项。

历史暂停记录保留原样：

- [数据安全暂停](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/data-pause.md)：当时未完成的列表兼容/门禁失败、数据库和备份位置。
- [BFF 安全暂停](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/bff/paused-handoff.md)：当时尚无A→A/FULL/UNKNOWN全链、Redis与证书恢复输入。
- [协调暂停快照](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/pause-check.txt)：该时刻事务与资源状态，不当作当前PID/状态。
- [恢复后的BFF证据](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/bff/resume/README.md)、[data-layer](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/data-layer.md)、[进程故障](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/process/README.md)：追加的新事实，不倒改旧暂停结论。

任务恢复前先检查实际容器/进程/端口、备份 hash、DSN权限与版本，不按旧 PID 盲目 kill/start，不重建或重播种已有 joint-B。

Acc 已发布版本中的 `docs/runbooks/accelerator-runtime.md` 仍保留旧批次的“手写参数化 SQL、不需要 sqlc”、只列0001迁移及“外部Gov接入本批不执行”描述；`docs/operations/governance-accelerator-v1.2.md` 也仍是 R0 约束与当时 not_verified 的快照。这两个历史入口都不代表本批整改后的数据层现状。新实现依据为固定提交中的 `migrations/0002_explicit_isolation.sql`、`internal/data/queries/accelerator.sql`、`internal/data/accdb/`，以及 `docs/evidence/gov-acc-v12-01/acc/resume-results.md` 和 candidate-v7/replicas-comment 的真实验证结果。保留旧文件不代表过去已经使用sqlc；本Gov手册及验收表只追加当前事实，不倒改历史。

Acc运维也必须按新结构显式执行完整链：空库先 `bash scripts/migrate 0001` 再 `bash scripts/migrate 0002`；已确认v1的旧库先备份，再仅执行 `0002`。这两个入口都只使用受保护libpq环境，由迁移owner运行，不是服务启动行为；版本或结构不确定时先在恢复副本核对，不猜测重跑。应用角色仍须非owner、非superuser、无BYPASSRLS/DDL/TEMP，以新迁移与真实catalog审计为准；不能沿用旧手册中未完整列出的权限要求来豁免新门禁。

本次最终模块 `1d32dd9a9173` 下，Gov [verify-gpu-05](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/data/resume/verify-gpu-05.meta) 已exit0；独立联合[普通六项](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/joint-resume/20260924T031542Z/test.log)140.884s及[race六项](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/joint-resume/20260924T031813Z/test.log)161.297s均PASS，包含FULL/UNKNOWN真实BFF、持久测试owner、退款和投影恢复。[最终模块普通A→A](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/bff/final-1d32dd9/README.md)也已复验。它们是软件运行证据；最终完整提交树、Gov发布/精确SHA CI和任务清理仍依验收表逐项登记，不由这些命令通过推定。

## 9. 清理与后续门禁

本批任务资源已按用户接受不等待CI的明确指示完成清理，见[实际检查回执](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/release/cleanup-result.json)及[日志](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/release/cleanup.log)。以下要求同样适用于未来隔离复跑的收尾；每次 cleanup manifest 应记录：精确进程PID/命令/退出码、任务容器ID/标签、库/角色、临时凭据/cache/锁、实际动作与时间，以及保留备份/证据/恢复材料的路径和hash。先核对归属，确认无消费者后停止任务进程；只清任务资源，不清共享服务、共享数据库或他人cache。

历史 task 数据库/CA/helper是否保留，应逐项记录目的和恢复入口，不能笼统写“全部完成清理”。在备份恢复所需密钥/证书尚未有替代保存方式前，不删除唯一恢复输入。复测用例自己的临时库 cleanup 不代表整个 Goal 已清理。

后续正式 owner 仍需真实业务API/权限、持久接受、受管renderer防绕过、全部历史Pod和在途副作用关闭、签名/轮换与可靠通知；真实GPU仍需F>1单位链、双模型同父卡推理、K满Pending、安全预算、整卡排他和真实解除后容量恢复。第二owner和生产部署另批验收。软件fixture、生成YAML、CPU推理或HTTP存活不能升级为这些通过结论。

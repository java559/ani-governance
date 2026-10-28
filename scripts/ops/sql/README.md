# 显式运维 SQL

这些文件是可审阅的操作输入，不由服务启动或 CI 自动执行。写入型脚本仅在核对目标数据库、角色、参数和备份后按对应部署指南显式运行；不能直接用于共享未知库。

| 文件 | 用途与前置 |
| --- | --- |
| `inspect-connections.sql` | 只读，查看当前连接和事务；以有 `pg_stat_activity` 可见权限的角色运行。 |
| `audit-runtime-privileges.sql` | 只读，以实际受限 runtime 连接核查角色、表 owner、RLS 和权限。 |
| `compare-table-content.sql` | 只读，逐表计数和内容摘要；需能读目标表，大表有开销，摘要不构成密码学完整性证明。 |
| `restrict-database.sql` | 写权限，需 `database_name`、`runtime_role` psql 变量；会收紧 PUBLIC，先审查所有合法使用者。 |
| `grant-runtime-role.sql` | 写权限，需 `migrate_role`、`runtime_role` 变量；在结构迁移后由迁移 owner 运行，不授 owner/DDL。 |
| `retire-legacy-file-access.sql` | 数据修复，旧安装文件/API/menu 清理；先按文件退役计划完成结构迁移并审查备份。 |
| `repair-tenant-logout.sql` | 数据修复，旧安装登出权限缺失时使用；原子且可重跑，需确认现有权限/API 唯一。 |
| `register-accelerator-permissions.sql` | 数据修复，API 目录 dry-run 与显式同步后登记权限；不授角色、不开套餐或租户额度。 |
| `retire-inference-management.sql` | 部署不含推理管理路由的版本后，停用旧 API 与未被其它活动 API 使用的推理权限；保留 ID、授权关系和历史账目，执行前审查并备份匹配行。 |
| `register-model-permissions.sql` | 数据修复，Model BFF API 目录同步后登记精确权限；不授角色、不开套餐或租户额度。 |
| `register-compute-permissions.sql` | 空库 API 目录同步后的平台算力规格权限登记；新权限为 OFF，只登记五个 compute 管理权限及六条 API 关系，不授角色或套餐。已有旧权限必须先走显式升级。 |
| `migrate-compute-preset-permissions.sql` | 受控切换中原地改名五个管理权限及六条 API owner；保留 ID、状态、审计和授权关系，冲突原子失败，可重跑。 |
| `restore-instance-preset-permissions.sql` | 受控恢复的逆向元数据迁移；须结合 Resource 实际提交状态和匹配版本恢复，不能只回退旧二进制。 |
| `enable-model-access.sql` | 只给显式指定的租户 `tenant:manager` 角色和套餐开放 MODEL；不会修改其他租户、模板角色或配额。 |
| `grant-quota-platform-read.sql` | 数据修复，给受保护平台管理员角色（`platform:admin`）补授 `quota-definition:view`、`tenant:quota-read`，修复配额目录/租户配额账户对平台管理员 403；不新建/放宽其它角色，不改权限-API 关联、不开套餐。 |
| `grant-quota-tenant-self-read.sql` | 数据修复，给租户管理员模板 `template:tenant:manager` 与既有 `tenant:manager` 角色补授 `quota:account:self-read`（自助读本人配额账户）；不触碰平台角色与其它角色码，不代表已开通 TENANT 模块。 |
| `grant-storage-tenant-permissions.sql` | 数据修复，给租户管理员模板 `template:tenant:manager` 与既有 `tenant:manager` 角色补授全部 9 个 `storage:*` 权限码（卷创建/列表/详情/扩容/删除，桶与对象只读浏览/下载），修复存储接口对租户管理员 403（权限与 API 关联本已存在，缺的是角色授予）；逐条校验权限点已绑定对应在线路由，不触碰平台角色与其它角色码，不代表已开通 STORAGE 模块。 |
| `register-modeldev-execution-detail-permissions.sql` | 数据修复，把执行详情的 `/pods`、`/events`、`/resource-metrics` 关联到 `modeldev:execution:get`、`/logs/stream` 关联到 `modeldev:execution:logs`；不新建权限、不授角色或套餐。须先跑 `admin sync-apis --dry-run` 确认四条路由已入目录，再由迁移 owner 执行；整套前置见 `scripts/ops/apply-modeldev-execution-detail.sh`。 |
| `register-modeldev-resume-registration-permission.sql` | 数据修复，把 `POST /api/v1/modeldev/executions/{execution_id}:resume-model-registration` 关联到既有的 `modeldev:manage_execution`；不新建权限码、不授角色或套餐。该路由由 `admin sync-apis` 写入目录但不会自动绑定权限，未执行前恢复登记恒返回 403。 |

SQL 中保留旧路径注释以保持迁址前后字节一致；当前调用路径以本表和 [部署指南](../../../docs/deployment.md) 为准。

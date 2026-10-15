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
| `register-model-inference-permissions.sql` | 数据修复，Model/Inference BFF API 目录同步后登记精确权限；不授角色、不开套餐或租户额度。 |
| `enable-model-inference-access.sql` | 只给显式指定的租户 `tenant:manager` 角色和套餐开放 MODEL、INFERENCE；不会修改其他租户、模板角色或配额。 |
| `grant-quota-platform-read.sql` | 数据修复，给受保护平台管理员角色（`platform:admin`）补授 `quota-definition:view`、`tenant:quota-read`，修复配额目录/租户配额账户对平台管理员 403；不新建/放宽其它角色，不改权限-API 关联、不开套餐。 |
| `grant-quota-tenant-self-read.sql` | 数据修复，给租户管理员模板 `template:tenant:manager` 与既有 `tenant:manager` 角色补授 `quota:account:self-read`（自助读本人配额账户）；不触碰平台角色与其它角色码，不代表已开通 TENANT 模块。 |

SQL 中保留旧路径注释以保持迁址前后字节一致；当前调用路径以本表和 [部署指南](../../../docs/deployment.md) 为准。

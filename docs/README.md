# 文档导航

| 当前入口 | 用途 |
|---|---|
| [开发指南](development.md) | 工具、生成、测试与证据边界 |
| [仓库卫生规则](contributing/repository-hygiene.md) | 测试、脚本、CI、归档和例外处理 |
| [执行规则](../AGENTS.md) | 授权、改动与安全边界 |
| [部署与初始化](deployment.md) | Atlas、初始化、运行配置 |
| [平台网络接入](contracts/platform-network.md) | 平台用户的地址池、二层网络与依赖接口、委托和响应合同 |
| [分页规范](contracts/pagination.md) | 强制要求、users 游标合同、其他列表现状与迁移边界 |
| [服务间调用规范](contracts/service-to-service-calls.md) | 三种方向的统一调用要求、存量差异与迁移边界 |
| [权限码规范](contracts/permission-code.md) | 权限码业务语义、命名、接口映射与目录校验；附[业务能力映射表](contracts/permission-code-capability-map.tsv)（六域+Quota 94 条）与[管理面权限映射表](contracts/permission-code-management-map.tsv)（管理接口 180 条） |
| [业务服务接入](service-integration.md) | 当前客户端配置、手工装配与 API 登记 |
| [接口集成登记](interface-integration-register.md) | 现行接口与问题，不因归档自动结项 |
| [API 权限运维](api-role-permission-ops.md) | 目录同步、权限、套餐和策略 |
| [管理员开通](onboarding.md) | 立即激活、邮件邀请与目标环境验收 |
| [Network / Image AK/SK 合同](contracts/aksk-signature.md) | 签名、当前机器访问范围及客户端限制 |
| [GPU owner 合同](contracts/gpu-owner-integration-guide.md) | 账本、释放、测试 owner 与真实接入界限 |
| [Image publisher 停用合同](contracts/image-publisher-disable.md) | 未签发错误、版本/重放及真实跨仓 HTTP 回归入口 |
| [Governance/Accelerator 运维](operations/governance-accelerator-v1.2.md) | 运行操作和恢复边界 |
| [脚本导航](../scripts/README.md) | CI、运维和可选隔离实验 |
| [版本数据步骤](../sql/data/README.md) | quota expand、数据回填、constraints 的显式顺序 |
| [运维 SQL](../scripts/ops/sql/README.md) | 只读诊断与显式写入脚本的权限和目标边界 |

接口修改先读对应合同与源码，再查看登记中的当前说明和待修问题；带日期的验收结果只覆盖所列源码、范围与环境，不能自动升级为当前验证。[历史索引](history/README.md) 给出退出 HEAD 的迁移回执、原始证据、第三方来源和旧实验的固定提交；[本机联调历史](local-integration.md) 仅供排查当时故障。当前工具配置在 `tools/config/`；数据库结构迁移在 `migrations/`。

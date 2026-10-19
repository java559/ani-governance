# ANI Governance

ANI Governance 是 ANI 平台独立维护的治理后端。本仓包含后端服务、API 定义、开发工具和必要的验证脚本，不包含产品前端。当前代码覆盖身份认证与授权、租户与套餐、配额、审计、任务及 SSE 等；代码存在不等于全部业务域已通过部署和跨仓验收。

**开始开发：[本仓开发指南](docs/development.md)。** 文档导航见 [docs/README.md](docs/README.md)，AI 执行规则见 [AGENTS.md](AGENTS.md)。

## 仓库身份

- 展示名称为 **ANI Governance**，既有服务身份为 `ani-governance`。
- Go 模块路径暂为 **`go-wind-admin`**。全部技术导入、Proto Go 映射和工具身份保持一致；最终托管域名确定后另开模块重命名 PR。
- 当前运行库与开发工具的本地接管源码分别位于 `pkg/localdeps/`、`tools/localdeps/`。不要用上游二进制覆盖本仓工具。
- 项目来源、版权与许可见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)；来源归档与历史记录不是当前使用教程。

## 开发入口

下面的命令都从仓库根目录执行；工具、配置和资源前置先按开发指南准备。

```bash
make gow                         # 从当前源码构建 tools/bin/gow
make build_only                  # 只编译已有源码，不生成、不连接业务库
make build_admin                 # 构建显式运维 CLI：bin/admin
make api                         # 完整活跃 API 链，含暂存 OpenAPI 后处理
make check-repo-entrypoints       # 安全命令桩检查，不代替真实构建
```

`tools/bin/gow api` 与根 `make api` 使用同一生成链。`make build` 为 API 生成后编译；`make gen` 为 Ent 生成/SQL 导出后 API 生成；`make all` 再加编译。只改普通 Go 实现时不自动运行生成器或依赖整理。

旧主机安装和 PM2 Make 目标已移出 HEAD。安装固定工具的方法见开发指南；容器部署与 Atlas 迁移各按其现行说明执行。

## 运行、部署与业务接入

[部署与初始化流程](docs/deployment.md) 规定：**Atlas 结构迁移 → 显式 admin init → check → 启动 → 登录/API 验证**。启动不迁移、不播种、不同步 API、不恢复默认密码；主装配要求 Casbin。配置目录为 `app/admin/service/configs/`，样例不能直接当作生产参数。

业务服务接入见 [service-integration.md](docs/service-integration.md)，接口排查与已接受的调整持续记入 [接口登记](docs/interface-integration-register.md)。本期不提供 Inference 推理服务管理接口，普通及 GPU 推理的创建、查询、更新、启停、删除和操作查询均已下线；Go API 依赖与出站配置一并移除，后续另行重新对接。Model / ModelDev、ACC 目录与 Resource CPU/GPU 实例链保持各自当前范围。

镜像构建使用根 `Dockerfile` 的服务与 admin CLI target；Atlas 迁移单独执行。备份、部署和实验脚本先查 [脚本指南](scripts/README.md)，不可默认操作共享或业务数据库。

## 维护与验证范围

[CHANGELOG.md](CHANGELOG.md) 记录有依据的本仓变化；旧混合日志原样保存在 [历史目录](docs/history/README.md)。

源码接管已合入主线，历史 R6 样本恢复经所有者调整范围后延期、仍未验证。既有测试缺陷和运行/部署/跨仓未验证边界继续保留，不以仓库整理改成通过。当前配额 CI 并非完整生产验收，具体入口与覆盖见开发指南。

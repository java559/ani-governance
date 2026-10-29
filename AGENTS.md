# AGENTS.md — ANI Governance 执行规则

本仓独立维护 ANI 治理后端。读取顺序：当前任务 → 本文件 → 对应合同与真实源码 → [开发指南](docs/development.md) 的相关章节。当前任务的明确授权决定修改和执行范围；来源与许可见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

## 工作顺序与完成标准

- 实现前简述完整业务主链、可观察结果和当前断点，优先消除主链阻塞。
- 先交付真实业务代码组成的最小纵向可运行用例。环境未就绪时，仅在外部系统边界使用明确替身，不模拟待实现的业务逻辑或绕过权限、持久化；身份、租户隔离、事务、幂等和副作用约束随主流程落实。
- 每轮实现带来可运行行为、解除具体阻塞，或获得足以决定下一步的真实证据。主链未接通时，暂停非阻塞旁支、通用框架与额外抽象；复用已有合同、测试和证据，委派限定职责与文件所有权。
- 交付说明首先回答主流程跑到哪一步、哪里仍未实现；分别报告替身闭环、真实环境验收和未验证范围。模块 PASS、代码量、文件存在或 HTTP 200 不代表业务完成。

## 仓库身份与执行边界

Higress 只承载 OpenAI 兼容的推理调用（例如 `/v1/completions`）；模型、
导入任务、容器实例及推理管理等 `/api/v1/...` 业务接口由本 Governance BFF
提供，不得接入 Higress 路由或 AI 网关插件。Gov 使用固定版本的 Inference API
模块，通过受信服务间调用对接推理服务、AK 和策略管理；遵守服务间调用规范及
对应合同。修改路由、代理、HTTPRoute 或部署配置时保持这条边界；不得把
Higress 调用入口的结果当成管理接口验证。

根目录是 Go 模块根，技术模块名为 `go-wind-admin`，服务身份为 `ani-governance`。单独授权改名任务前，保留 import、go_package、Proto 包名、运行标识与本地包目录。

Go 基准来自 `go.mod`。`gow` 是本仓维护副本，用 `make gow` 构建并使用本仓的 api/ent/run/version；不运行未接管的 Wire、project/new、migrate 等脚手架命令。工具身份与版本以 Makefile、活跃模板和 `tools/config/` 为准，不用 latest 补空缺。

主装配为 Kratos + Ent，数据主路径为 PostgreSQL，主服务配置必须是 Casbin；库内 noop 类型不是受支持启动配置。保留无关工作区修改，只修改和提交本次授权文件；生成、构建、运行、数据库及部署遵守本次授权与指定环境。主机安装、全局配置、业务库操作、推送、PR 和合并按明确授权处理。

## 按任务读取

- 修改前端 HTTP 接口、列表、筛选或排序前，先读 [前端 HTTP 接口规范](docs/contracts/frontend-http-api.md)，核对对应 Proto、真实实现与接口登记。新增或修改接口遵守合同中的请求、响应、分页、过滤、排序和租户边界；存量窗口与响应格式按明确迁移范围处理，不把共享消息中的 cursor 字段当成已迁移。
- 新增或修改服务间调用前，先读 [服务间调用规范](docs/contracts/service-to-service-calls.md)，再按 [业务服务接入指南](docs/service-integration.md) 定位具体客户端与专项合同。统一规范是新接入和修改的要求；[存量差异](docs/contracts/service-to-service-calls.md#current-differences) 须独立迁移，不因文档入仓就视为已改运行逻辑。
- 修改 Repository、DTO 映射或关联更新前，先读 [Repository 合同](docs/development.md#repository-contracts)，核对当前 `api_repo.go`、`RoleRepo.Update` 与本地依赖实现。
- 新接入或修改接口合同前，在 [接口集成登记](docs/interface-integration-register.md) 维护报文、鉴权条件和问题，复用已有编号。接口风格统一调整只按用户指定批次与格式实施；即时修复单独记录，整体登记由用户确认结项。
- 新增、移动或删除测试、脚本、实验与过程材料前，先读 [仓库维护规范](docs/contributing/repository-hygiene.md) 的放置表及对应章节；临时材料留在忽略目录或仓库外。
- 使用工程技能时，创建或读取任务/规格前先读 [任务跟踪配置](docs/agents/issue-tracker.md)；普通票据分诊前先读 [标签映射](docs/agents/triage-labels.md)；领域探索前先读 [领域文档配置](docs/agents/domain.md)。技能配置沿用本仓授权、生成、验证和交付边界。
- 历史接管与恢复任务先查 [开发指南的已知范围](docs/development.md#9-已知范围与生产边界) 和 [历史索引](docs/history/README.md)。维护接管源码采用最小补丁和定向回归，保留来源与许可；已延期样本和已清理实验不因新任务自动恢复。

## 不可省略的实现合同

1. 领域 Proto 定义消息与 RPC，HTTP 路由由 admin BFF 声明；沿用已接受路径，不强制给所有 ANI 接口添加 `/admin/v1` 前缀。
2. CRUD 请求保持 `{ data: {...} }`，部分更新使用 `update_mask`；Service 校验 nil 请求及需要的 data。未请求修改与显式清空分别处理，主表与关联更新在同一事务，验证保持、清空、重加、失败回滚与跨租户拒绝。
3. 公网可信身份来自认证上下文。支持 JWT/AK 的接口通过 `auth.PrincipalFromContext` 区分用户与 API Key，主体 ID 不混用；仅用户管理接口可沿用 `auth.FromContext` 的用户 claims。内部服务和后台事实按服务间调用合同验证，不伪造用户主体，不信任入站租户、操作人或 owner 字段。
4. `tenant_id` 不等于隔离。查询、写入、删除、关联、异步、缓存、消息和下游请求均检查租户边界，保留关系约束与负向测试。
5. 不吞错，使用相应 Proto 错误码并保留原始错误上下文；日志与公开响应保护密码、密钥、令牌和跨租户私有信息。
6. `wiring_ent.go` 手写，不使用 Wire；按基础设施 → Repo → 认证授权 → Service → Server 单向装配，只构造和传参。持有 cleanup 的资源成功后立即登记，失败和退出逆序释放；`make register` 不替代额外依赖的手工装配。
7. 测试调用真实构造和业务行为；不在生产包增加 `ForTest` 构造器、测试控制路由或模拟资源系统来绕过业务、鉴权、隔离或事务。清理仅处理本任务自有资源。

## 数据与生成边界

`data.database.migrate` 必须 false。结构迁移通过 `migrations/` 和 Atlas 显式执行；首次初始化用 `bin/admin init` 与 `sql/bootstrap/001_initial.sql`，旧 default_data 不是部署种子，已有业务库不得重复首次播种。

API 目录先 `admin sync-apis --dry-run` 再显式同步，保留 ID、启停状态与权限关系。新增 `(path, method)` 需权限和套餐关系，目录同步不自动授权，租户闸门保持 fail-closed。CLI/SQL 修改后的策略刷新按 [部署说明](docs/deployment.md) 执行。

`make api` / 本仓 `gow api` 是完整 API 入口；保留开始快照、暂存生成、写回前工作树保护与本地插件身份检查，redact 从当前源码重建，不在正式输出根裸跑 Buf 后再删除或回退产物。API 与 Ent 生成实现不手改；Ent schema 输入在 `app/admin/service/internal/data/ent/schema/`，同树的手写守卫与测试按源码保留。

`migration/` 不等于数据库 `migrations/`；前者的活跃 PGV 清单、工具锁与测试清单按源码保留，不整目录搬删。历史记录、来源与许可归档不改写。

## 验证与交付

默认执行改动对应的行为测试及明确门禁；有相关失败、跨模块影响或明确交付要求时扩大范围，并说明理由。无新变更、失败或未解风险时复用已通过检查；文档修改只做相应引用与一致性验证。

工具命令从根目录运行。Make 委托调整先跑 `make check-repo-entrypoints`，命令桩不代替真实生成或构建。`make test` 不准备依赖资源，父包测试不自动覆盖子包 Ent 守卫。

数据库测试只用明确授权的独占库，helper 可能建表和 TRUNCATE。保存真实退出码与具名结果，缓存、skip 和未执行分别报告；保留失败，不重试至绿、不扩 known-defects、不删有效断言、不全局关闭 vet。

结果说明变更行为、实际验证与未完成事项。源码实现、运行、部署和跨仓业务验收分别确认；缺陷和范围外事项保留，不用无关优化拖延已限定任务，不为回填自身 SHA 反复追加台账提交。

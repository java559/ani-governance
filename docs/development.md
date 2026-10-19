# ANI Governance 本仓开发指南

本指南描述当前仓库的开发流程；命令和工具版本以 HEAD 中的 Makefile、工具锁与源码为准。原“仓库入口整理”任务以 `main@9ba13ad933b66e8f675f7eb9519123abcdb12e8f` 为基线，其验证只覆盖当时的源码与环境。后续任务按实际变更记录验证，不因指南列出命令就视为当前环境已通过。

## 1. 仓库身份与工作范围

ANI Governance 是 ANI 的治理后端。当前仓库只包含后端；业务服务、仓储、传输装配与开发工具都在同一个根 Go 模块下维护。

- 仓库显示名称与服务身份使用 ANI Governance／`ani-governance`。
- **Go 模块名暂时仍为 `go-wind-admin`**，所有现行本仓 import、`go_package`、生成映射及工具身份预期保持一致。最终迁出域名确定后，模块路径重命名单独提 PR。
- 源码接管后的日常维护对象是本仓实现，不从原项目更新说明推导当前功能，也不直接安装上游 gow／redact 来替换本仓工具。
- 来源与许可集中见 [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)。源码归档、原许可、历史日志不属于品牌清理目标。
- 代码存在、构建成功、单项测试通过、服务部署成功是不同结论。没有执行的环节不能记为通过。

当前保留 Model / ModelDev、ACC 和 Resource 实例接入。Inference 管理接口、Go API 依赖、生成导入与出站配置已退出本期范围；普通和 GPU 推理管理均不提供，后续重新对接须重新确定上游版本、合同和装配。历史接入记录不作为当前启用说明。

## 2. 从哪里开始阅读

| 位置 | 职责／修改规则 |
|---|---|
| `api/protos/` | 手写业务与 admin BFF Proto；消息、RPC 和 HTTP 合同的输入 |
| `api/localdeps/` | 已接管分页、脱敏、bootstrap 等 Proto 的输入 |
| `api/gen/go/` | API 生成输出，不手改实现 |
| `app/admin/service/cmd/server/` | 服务入口与手写装配，重点读 `wiring_ent.go` |
| `app/admin/service/cmd/admin/` | 显式初始化、检查与 API 目录同步 CLI |
| `app/admin/service/internal/server/` | HTTP、Asynq、SSE 等传输装配 |
| `app/admin/service/internal/service/` | 业务服务／治理侧入站处理 |
| `app/admin/service/internal/data/` | 仓储、下游客户端与数据访问 |
| `app/admin/service/internal/data/ent/schema/` | 可编辑的 Ent schema 输入 |
| `app/admin/service/internal/data/ent/` | 同时含生成代码与手写守卫、测试，不能整目录覆盖或当成纯生成目录删除 |
| `app/admin/service/configs/` | 配置结构样例，不是生产参数与凭据交付物 |
| `pkg/` | 本仓公共实现；`pkg/localdeps/` 是已接管运行库及 redact 源码 |
| `tools/localdeps/gow/` | 本仓支持的开发命令源码 |
| `tools/register/` | 标准 CRUD 装配登记工具 |
| `migrations/` | 数据库结构迁移及 `atlas.sum`；注意有复数 `s` |
| `sql/bootstrap/` | 显式首次初始化的数据来源 |
| `scripts/` | 生成、验证、部署及运维入口；先读脚本范围 |
| `tools/config/` | 活跃 PGV 范围与锁定工具身份；不可用历史目录代替 |
| `tests/manifests/` | 关键测试结果清单，仅列不可漏的安全和数据断言 |
| `docs/contracts/` | 业务合同；具体专题同时查现有部署、接入和接口登记文档 |

阅读业务链时沿用现有单向装配：基础设施 → Repository → 认证授权 → Service → Server。`wiring_ent.go` 负责构造和传参，不放业务规则。资源创建成功后登记 cleanup，失败与退出逆序释放。

## 3. 工具与环境准备

### 3.1 当前基线

Go 版本由根 `go.mod` 定义，当前为 **1.26.9**。API 工具版本由根 Makefile、各活跃 Buf 模板和 `tools/config/tool-lock.json` 共同限定；不同切片存在已批准的不同插件版本，不要自行“统一到最新版”。

`gow` 的来源版本为 `gowind@v1.0.3`，但当前工具包含本仓适配，**这不是安装上游 v1.0.3 二进制的指令**。必须使用当前 checkout 构建的 `tools/bin/gow`。

常规开发需要 Go、Git、Make、Bash、Python 3；API 生成需要锁定的 Buf／插件。`make tools-integration` 另需原生 `protoc`，已记录基准为 29.3。PostgreSQL 与 Redis 按运行或测试场景准备；当前 CI 使用 PostgreSQL 16.10。Atlas 是显式数据库迁移工具，不内置于服务运行镜像。

### 3.2 本仓工具准备

以下命令从仓库根目录执行。它们会下载并构建开发工具，不会初始化业务库；受控环境仍需事先具备网络和安装授权。

```bash
# 以下仅修改当前 shell，不写全局 go env。
export GOWORK=off
export GOBIN="$PWD/tools/bin"
export PATH="$GOBIN:$PATH"
# 已有 GOFLAGS 时，保留需要的参数并将模块写入模式明确改为 -mod=readonly。
export GOFLAGS="${GOFLAGS:-} -mod=readonly"

go version
make plugin cli gow
```

`make plugin` 安装 Makefile 固定版本的外部生成器，并从本仓构建 redact；`make cli` 安装其已明确版本的工具，未钉版的 kratos CLI 和 golangci-lint 仅输出说明，不保证 `make lint` 已可运行。原生 protoc 需要按锁定记录另行准备，不能因为有 Buf 就假定它已安装。

本地 gow 的主要命令为 `api`、`ent`、`run`、`version`。不要照上游教程执行未接管的 `gow wire`、`project/new`、顶层 `generate`、`migrate` 等命令。

**旧主机安装和 PM2 入口已退役：**相关 Make 目标与脚本均已退出 HEAD。固定工具按本节显式准备；旧实现可从固定历史提交读取。不要以主机安装代替容器部署说明。

需要配置依赖代理时只在当前环境设置，保留校验机制。旧文档针对某些领域模块记录过代理不可取的问题；不要把当时某个模块名或网络状态当成当前依赖清单，先读本次 `go.mod` 与实际解析结果。

## 4. 日常修改、生成与构建

### 4.1 只改普通 Go 代码

先构建受影响包，再跑对应测试；不为普通业务改动自动执行全部生成器或 `go mod tidy`。

```bash
make build_only
make build_admin
# 示例：确实修改了 JWT 包时使用。
go test -mod=readonly -count=1 ./pkg/jwt
```

根 `make build_only` 委托服务目录编译已有源码；`make build_admin` 构建根 `bin/admin`。不要假定两类构建产物落在同一个目录。

### 4.2 修改 Proto／API

```bash
make api
# 等效完整 API 入口；先用 make gow 构建当前源码版本。
tools/bin/gow api
```

完整链校验工具身份和输入，在暂存副本生成并执行 OpenAPI 后处理，成功后才写回受管生成文件。工作区在生成期间被编辑时，冲突应保留用户内容并报告，而不是用 Git 恢复掉用户改动。

`make pgv`、单模板 Buf 命令和切片脚本不是整仓完整链的替代品。不要在共享正式输出上随意运行裸 `buf generate`；旧 `scripts/post-generate-clean.sh` 已退役，不能再用“生成后回退或删文件”修正输出。

独立更新 OpenAPI 的入口是 `make openapi`：它先重新生成原始文档，再执行后处理。完整 `make api`／`gow api` 已包含后处理，不再直接对最终 YAML 手动执行 `finalize-aksk-openapi.py` 第二次。

### 4.3 修改 Ent schema

```bash
make gow
tools/bin/gow ent admin
(
  cd app/admin/service
  go run -mod=readonly ./cmd/schema > schema.sql
)
```

当前 `gow ent admin` 生成 Ent；上面的 SQL 导出是独立步骤，不将“已生成 Ent”写成“已生成或应用数据库迁移”。五项 Ent feature 为 `privacy`、`entql`、`sql/modifier`、`sql/upsert`、`sql/lock`，与现有 schema 门禁保持一致。

不得为品牌清理增加 `sql/versioned-migration`、恢复已移除框架，或把 schema 来源改成另一套实现。数据库结构变更仍走已审查的 Atlas 迁移流程。

### 4.4 聚合 Make 入口的明确职责

| 根目录命令 | 顺序与结果 |
|---|---|
| `make api` | 本地工具准备 → `gow api` 完整链，包含 OpenAPI 后处理 |
| `make build_only` | 只委托每个服务的 `build_only`，不生成 |
| `make build` | 完整 API 一次 → 每个服务只编译，不再进入服务旧生成前置 |
| `make ent` | 服务 Ent 生成 → 当前 admin 的 `schema.sql` 导出，不应用数据库迁移 |
| `make gen` | `make ent` 完成后再 `make api`，显式串行 |
| `make all` | `make gen` 完成后再只编译 |
| `make check-repo-entrypoints` | 安全临时命令桩验证委托、次数、错误传播和退役边界；不是实际生成 |

服务目录仍可用 `make -C app/admin/service api`、`build`、`build_only`、`run`、`gen`、`app`。其中 API 和独立 OpenAPI 委托根入口；服务 `gen` 为该服务 Ent 后完整 API，不额外导出 SQL；服务 `app` 在此基础上编译；服务 `run` 生成后按原参数启动。需要数据库迁移源时按 §4.3 显式导出 SQL，或用根 `make ent`。

这些聚合目标的内部顺序通过显式子 Make 调用固定。不要在一个工作树并发启动两条独立生成命令，也不要使用 `make -j api openapi` 让两个顶层目标同时写共享产物。运行命令实际会启动服务，不能拿“验证委托”当作启动授权。

服务与 admin CLI 的镜像可从根使用 Dockerfile 指定 target，或显式运行 `make -C app/admin/service docker_server` / `docker_admin`；不要把服务 Make 目标误称根 Make 已直接暴露的目标。

## 5. 首次本地启动与显式初始化

已有数据库、共享实验环境和业务环境不能默认视为空库。首次运行使用明确归属的环境，并遵循：

**结构迁移 → 显式初始化 → 检查 → 启动 → 登录/API 检查。**

具体命令和角色分工见 [部署与初始化](deployment.md)，本指南不重复复制一套 SQL 顺序或提供默认密码。

必须核对的配置包括 PostgreSQL、Redis 与 Asynq 的 Redis 地址、认证及加密材料，以及启用的下游服务。服务装配要求 `authz.type: casbin`，不能为了启动成功改成 noop；数据库 `migrate` 必须为 false。启动会做必要状态检查，但不得自动建表、播种或恢复默认密码。

```bash
# 已由授权环境注入迁移身份的 ANI_DATABASE_DSN。
# migrate apply 会修改指定数据库；先核对目标和 dry-run。
./scripts/atlas.sh migrate status
./scripts/atlas.sh migrate apply --dry-run
./scripts/atlas.sh migrate apply

make build_admin
# 密码文件由环境提供，不创建可复用的明文默认密码。
./bin/admin init --username admin --password-file /run/secrets/governance-admin-password
./bin/admin check

# 运行前切换到相应受限身份和配置，不将迁移管理员给服务使用。
tools/bin/gow run admin
```

AccessKey 加密主密钥按当前配置要求由 `ANI_ACCESS_KEY_ENCRYPTION_KEY_FILE` 提供，不将真实内容写入文档、Git 或日志。配置默认 HTTP 端口为 7788，可选 SSE 为 7789；以实际配置为准。

API 目录同步也是显式动作：先执行 `admin sync-apis --dry-run`，核对后再执行同步。新增 Proto 不会自动赋予权限；同步目录也不等于自动授权。细节以 [API 权限运维](api-role-permission-ops.md) 和 [部署文档](deployment.md) 为准。

## 6. 新增或修改业务功能

### 6.1 沿用真实目录，不另造一套架构

先定位已有同类 Service、Repository、Proto 和装配。领域 Proto 与 admin BFF 保持现有分工；治理侧不接管下游领域服务的数据写入职责。下游协议、身份映射和错误处理按现有 [业务服务接入指南](service-integration.md) 与具体客户端源码核对。

配额公开错误 reason 定义在 `api/protos/quota/service/v1/error_reason.proto`，由完整 `make api` 生成；内部退额 gRPC 状态使用显式 code/message/details 映射。三个既有 admin 配额消息集中在同包 `quota_types.proto`，完整消息名及有效 JSON 名保持原值；这只是现有 BFF 外壳的兼容归位，不表示所有消息均由 quota 领域包拥有。套餐配额读取复用 mapper、字段白名单、通用分页器及最终 FieldMask；PostgreSQL SEARCH、签名 token 校验和父套餐写锁仍是具名领域适配。

新标准 CRUD 可以按需要使用 `make register ENTITY=<实体名>`，但该工具不能替代带额外依赖、配置和 cleanup 的手工装配；不运行 Wire。

典型修改顺序：

1. 明确请求响应、可信身份、租户边界、权限和套餐条件，按现有要求登记到 `interface-integration-register.md`。
2. 修改必要 Proto／schema，使用正式入口生成；只改展示文案时不触碰这些输入。
3. 在 Repository 和 Service 各自层次实现，更新 `wiring_ent.go` 与传输注册。
4. 通过显式 API 目录同步与授权流程完成登记，不默认放通新路由。
5. 添加与风险对应的正常及拒绝路径测试，保存实际执行结果。

### 6.2 不变的关键合同

- 可信租户和操作人来自已有认证上下文，不直接相信入站请求中的身份字段。
- `tenant_id` 列并不自动等于隔离；读取、更新、删除、关联、缓存、异步和下游元数据都检查租户边界。
- 部分更新遵循 `update_mask`，区分未请求修改与显式清空；主表与关联更新保留事务边界。实现细节见 [Repository 与 DTO 合同](#repository-contracts)。
- 前端 HTTP 请求响应及分页、过滤、排序和搜索先读 [前端 HTTP 接口规范](contracts/frontend-http-api.md)，按对应接口的当前合同实现；不在清理仓库时改变窗口或响应语义。
- 用户字段权限与脱敏保持既有处理顺序，不能为了“统一命名”改 JWT claim、Cookie、请求头、Redis key 或 topic。
- 下游身份元数据按既有客户端重建，不透传未验证的公网身份信息。保持 mTLS 和稳定错误映射，不为接入方便降级。
- 不吞错误；错误日志不泄露密码、令牌、密钥或跨租户私有内容。

规则与具体函数优先查现有 AGENTS、Service／Repository 和专项合同，不从历史上游教程复制行为。

<a id="repository-contracts"></a>

### 6.3 Repository 与 DTO 合同

新增或修改 Repository、DTO 映射和关联 FieldMask 前，核对本节及当前 [ApiRepo](../app/admin/service/internal/data/api_repo.go)、[RoleRepo.Update](../app/admin/service/internal/data/role_repo.go)。这些合同针对本仓维护的 [go-crud/entgo Repository](../pkg/localdeps/go-crud/entgo/repository.go)，不按上游最新版推断。

- Repository 泛型参数依次为 `Query`、`Select`、`Create`、`CreateBulk`、`Update`、`UpdateOne`、`Delete`、`Predicate`、`DTO`、`Entity`，共 10 项；类型声明和构造保持同一次序。
- DTO mapper 注册 `copierutil.NewTimeStringConverterPair()` 与 `copierutil.NewTimeTimestamppbConverterPair()`；每个需要映射的枚举注册相应转换器的 `NewConverterPair()`。
- `ListWithPaging` 同时传查询 builder 和 `builder.Clone()`，参照 `ApiRepo.List`；查询条件与具体接口的分页合同保持一致。
- 关联是否更新由关联字段是否出现在 `update_mask` 中决定：未请求修改则保留原关联，显式空集合则清空，非空集合按请求替换。
- 先识别关联 mask 并保存关联载荷，再把关联路径从通用标量 mask 分离；避免标量 `FilterByFieldMask` 清掉载荷后误清空关联，或把关联字段当作主表列更新。mask 使用 Proto 字段名；绕过 protojson 的直接调用如需兼容 JSON 字段名，以当前源码的明确处理为准。
- 主表与关联更新使用同一事务，任一步失败回滚。关联 ID 必须在可信租户范围内验证；定向回归覆盖未指定、显式清空、重新添加、跨租户拒绝及失败回滚，不能以清空成功代替租户隔离验证。

## 7. 验证层次与实际前置

| 验证 | 入口／边界 |
|---|---|
| 受影响普通包 | `go test -mod=readonly -count=1 <包>`，需要时加 `-race` |
| 本地 gow 工具 | 定向测试 `./tools/localdeps/gow/internal/...`，遵循测试需要的工具与工作区条件 |
| redact 生成集成 | `make tools-integration`；需要固定 protoc 与本仓构建的插件，不因缺工具 skip |
| Ent 生成／配额静态边界 | `make check-generated` 在隔离副本完整运行两遍 `make gen` 并比较；`make check` 执行配额存储边界扫描 |
| CI 三条门禁 | `make check`、`make test-integration`、`make check-generated`；`make verify-ci` 串行调用三者；集成需独占 PG/Redis，生成需锁定工具 |
| 租户／数据范围 Ent 守卫 | 包是 `./app/admin/service/internal/data/ent`，不能以父包 `internal/data` 通过代替；helper 可能建表和 TRUNCATE，仅用独占测试库 |
| 广泛测试 | `make test` 是 `go test ./...`，不负责准备所有外部资源，也不保证所有环境门控用例实际执行 |
| lint | `make lint` 需要使用方明确准备合适版本的 golangci-lint；现有锁记录仍有未解析项，不承诺开箱即用 |

当前 CI 位于 `.github/workflows/governance.yml`：`checks`、`integration`、`generation` 独立执行。PostgreSQL 16.10 经显式迁移和受限角色运行；Redis 每次有独占容器和标签。GitHub CI 与任务 Fedora 验证分别记录，均不代表部署或跨仓联调。

`ANI_TEST_DATABASE_DSN` 与 `ANI_TEST_GUARD_DATABASE_DSN` 必须指向本次授权的独占测试库；设置 `ANI_TEST_DATABASE_EXCLUSIVE=1` 和 `ANI_TEST_GUARD_DATABASE_EXCLUSIVE=1`。race 套件另用 `ANI_TEST_RACE_DATA_DSN` 和 `ANI_TEST_RACE_SERVICE_DSN`。不再接受旧通用测试变量名；Asynq 测试使用进程内隔离 Redis，不连接外部 Redis。

成功需要命令真实退出码和所要求的具名测试结果；包级 `ok`、缓存命中、skip 和环境未启动的 not_verified 分开记录。保存管道命令结果时不使用最后一个 `tee`／`echo` 的退出码冒充测试结果。

## 8. 本地接管依赖的维护

`pkg/localdeps/` 与 `tools/localdeps/` 已是本仓维护的实际源码。因目录名称仍含 go-wind／tx7do 来源标识就移动包，会同时改变 Go 包路径；这不属于本轮品牌整理。

维护接管源码时记录最小修改原因和针对性测试，保留对应来源与许可。不要把历史“逐字节等价”结论套到后续已修改源码；也不要每改一个本仓包都重跑 T00～T15。

活跃生成配置为 `tools/config/pgv-scope.json` 与 `tools/config/tool-lock.json`；缺失或损坏会阻止严格生成。关键结果清单在 `tests/manifests/critical-tests.json`，`scripts/ci/assert-test-results.py` 区分 pass、fail、skip 与未执行。新增 validator 时按真实范围更新配置并审查；旧数量不是永久阈值。历史迁移回执与来源归档可从固定提交 `63849fc4cde38b879184a8fea4a6f539e60063e1` 阅读，见 [历史索引](history/README.md)。

历史第三方源码归档不是完整离线依赖环境；维护副本内的许可证仍在包目录。下载路径、构建闭包和许可来源是不同概念，不能只搜索仓库里的字符串就声称依赖清零或无风险。

## 9. 已知范围与生产边界

本轮源码接管已合入主线；历史 R6 两份样本恢复按所有者裁决移出本轮合并前范围，仍未验证。原测试、迁移 SQL 和来源记录保留，不恢复寻找已清理样本的任务，也不将普通运行成功算作 R6 通过。

既有 captcha／ID 裁决、DST 日差及 copylocks 记录保持原有性质。真正出现新失败时按证据定位，不扩大例外、删除测试或使用全局关闭检查使其消失。

运行时、部署与完整跨仓联调独立验收。服务镜像、admin CLI 镜像由根 Dockerfile 的 `runtime-server`、`runtime-admin` target 构建；结构迁移使用独立 Atlas 工具／镜像，不放入每个服务副本启动动作。

本轮不修改部署环境的服务标识、证书身份、密钥或现有密码，不自动创建发布、推送镜像或操作业务库。

## 10. 提交与文档维护

从当前主线新建任务分支，先检查实际 HEAD 和用户工作区。品牌与文档整理不夹带模块路径重命名、依赖升级、业务合同调整或 GitLab 迁移。

PR 应简要说明范围、实际命令与结果、未执行部分。文档调整只做相应链接／路径／命令核对；改 Make 委托或工具安装入口时增加针对性验证；按现有 CI 获取正常结果，不每写一个说明文件就重复整场迁移。

README 是项目入口，本文是开发步骤，AGENTS 保留代理工作规则；CLAUDE 等入口只引用权威规则，避免多份大段相同指南互相漂移。部署、服务接入、接口登记与业务合同保留独立权威来源。

## 11. 文档依据与适用边界

当前命令应对照根 [Makefile](../Makefile)、服务公共 [app.mk](../app.mk) 和 [服务 Makefile](../app/admin/service/Makefile)。版本对照 [工具锁](../tools/config/tool-lock.json)、Go 模块声明及活跃模板，不复制第二份可编辑版本真相表。

基础部署与业务接入细则分别以 [部署文档](deployment.md)、[接入指南](service-integration.md) 和真实装配代码核对。本页不改写原专题中的历史证据、原机地址或当前计划状态；发现具体偏差应提出定向文档修订，不擅自修改安全合同或复制历史凭据。

旧混合更新日志见 [历史说明](history/README.md)。本仓规则与操作步骤分离，但不是移除来源、测试或安全约束。Linux 命令桩与真实构建、PowerShell 语法检查与真实 Windows 运行应分别记录，不互相冒充。

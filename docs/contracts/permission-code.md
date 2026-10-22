# 权限码规范

日期：2026-10-13 编制，2026-10-14 采纳定稿。状态：**已采纳为本仓唯一正式权限码规范**。本规范代表目标合同，不代表相关代码、数据或部署已完成。核对源码基线为 main `34388cf90d0834da76c9e5701ade5d92a248144f`；其他分支的实现应单独标明提交与验收结果。

本规范定义 ani-governance 权限码的业务语义、命名、接口映射和目录校验。核心原则是：**权限码表达业务能力，HTTP 路径定位接口。业务能力采用显式映射；普通管理 CRUD 可以使用经过审核的推导规则；映射缺失或冲突时停止目录写入，不自动合并不同能力。**

当前用户已确认前端没有接入权限码、没有真实用户。本稿不要求为历史权限字符串增加兼容层。已有合理且被后端使用的能力码优先沿用；需要调整的名称在采纳前列明映射并同步消费者，不能用“无人使用”替代后端一致性验证。

本规范与同目录 [业务能力映射附录](permission-code-capability-map.tsv) 已采纳为本仓正式合同与权威期望值；实现批次、验收结果与未完成项单独记录，不在本文回填。

## 1 范围和两类 code

覆盖 Network 租户、Network 平台、Image、Accelerator、Instance 和 Model 的现行接口，以及前缀迁移涉及的管理接口。Inference 管理接口现由 Governance BFF 提供，相关路由和 `INFERENCE=15` 模块已登记在现行能力映射中；权限仍需显式同步和授权。Quota 使用独立业务域；ModelDev 等其他业务接口遵循本规范的映射要求，不能因为原地址为 `/admin/v1` 就按普通管理 CRUD 处理。

| 名称 | 载体 | 含义 |
| --- | --- | --- |
| 授权码 | `sys_permissions.code` | 经审核的能力标识；角色通过 permission_id 和 API 关联获得接口授权 |
| 候选推导码 | `ConvertCodeByPath` 等函数的输出 | 普通管理 CRUD 生成候选值的工具输出；经过规则和语义校验才能写入权限目录 |
| 规范期望码 | 本规范及已采纳的能力映射 | 校验实际实现、注册 SQL 和数据库目录的独立预期；不能由被校验函数自己生成 |

授权链至少有两层：

- 通用 Casbin 主链通过角色、租户域、已启用的权限/API 关联，按实际方法和路径模板授权。
- 部分服务还有按权限码检查能力的规则。例如平台网络检查 JWT 中的 `network:platform:*`，用户与 MFA 管理使用特定 `sys:*` 能力码。

因此，修补推导函数本身不会自动改变既有数据库授权，但修改数据库 code 后必须同步直接读取 code 的后端检查、SQL 和令牌消费者。不能将“Casbin 策略不直接使用权限码”描述为“整个后端不使用权限码”。

`sys_permissions.code` 当前为全局唯一。租户隔离由可信主体、角色域、关联约束和业务检查保证，不通过给 code 拼租户 ID、用户 ID、资源实例 ID 来实现。

## 2 授权码命名和语义

### 字符规则

码由至少两个非空段组成，以 `:` 分隔。每段以小写英文字母开头，后续允许小写字母、数字、连字符 `-` 和下划线 `_`；不得包含空格、斜杠、路径参数、通配符或空段。语法为：

```text
[a-z][a-z0-9_-]*(?::[a-z][a-z0-9_-]*)+
```

该语法允许现有 `sys:platform_admin` 和 `network:platform:vlan-network:create`。语法合法不等于已登记能力，更不等于获得权限。数据库/Proto 的长度限制执行时按实际输入合同核对，不能截断码值后写入。

### 命名形态

| 类型 | 形态 | 示例 |
| --- | --- | --- |
| 系统保留能力 | `sys:{名称}` | `sys:access_backend`、`sys:reset_others_mfa` |
| 简单管理能力 | `{资源}:{动作}` | `user:view`、`role:edit` |
| 业务能力 | `{域}:{子资源}:{动作}`；已采纳的两段形式可保留 | `network:vpc:list`、`image:registration:create`、`instance:start` |
| 平台业务能力 | `{域}:platform:{子资源}:{动作}` 或既有显式平台动作 | `network:platform:vlan-network:create`、`accelerator:profile:admin-list` |
| Quota 能力 | `quota:{子资源}:{动作}` | `quota:account:self-read` |

域、资源和动作表达稳定业务含义，不由 `/api`、版本号、`admin`、前端名称或部署地址决定。`admin` 是 API 功能分组，不能成为新业务能力的资源名。仅有 `/api/v1/admin` 而没有资源的路径不得据此创建 `admin:view` 权限。

系统保留码维持当前七个明确值：`sys:access_backend`、`sys:manage_tenants`、`sys:audit_logs`、`sys:platform_admin`、`sys:tenant_manager`、`sys:reset_others_credential`、`sys:reset_others_mfa`。`sys:` 是保留命名空间，不允许为逃避重建或清理而随意新增该前缀。

### 动作定义

| 动作 | 含义与使用要求 |
| --- | --- |
| `view` | 已审核的普通管理读取能力，可以有意聚合该管理资源的列表与详情 |
| `list` | 枚举资源或查询资源集合 |
| `get` / `read` | 读取对象或结果；既有拼写可沿用，新增接口在同一子资源内选择一个并说明归属 |
| `create` | 创建资源，不能代替启动、停止、执行、发布或其他命令 |
| `edit` | 已审核的普通管理更新；特殊操作采用具体动作 |
| `delete` | 删除或提交删除意图，即使 HTTP 方法为 POST 也仍是 delete 能力 |
| `start` / `stop` / `exec` | 启动、停止和执行；分别登记，默认不能互相授权 |
| 其他领域动作 | enable、issue、reset、disable、adopt、verify、allocate、bind、register、publish、self-read 等按业务合同登记 |

同一码可以绑定多个 API，但必须是有意合并的同一能力，记录这些 API 和合并理由。不能因为方法或首段相同而自动合并。多个权限点也可以合法绑定同一 API，例如系统管理权限与具体业务权限；规范业务码不等于该 API 只能有一个 permission_id。

码值在正式采纳后保持稳定。API 改地址不触发改码；确需改码时，列出旧码、新码、业务含义、后端消费者、数据步骤和验证结果。当前开发阶段允许一次性整理命名，不强加旧用户兼容要求。

## 3 普通管理 CRUD 的候选推导

### 适用条件

只有被明确登记为普通管理 CRUD 的接口才允许推导。推导结果仍需与规范期望码核对后才能写入目录。以下接口不能走未知项回退：

- 各接口面的业务资源及其命令。
- ModelDev 等涉及真实业务生命周期的接口。
- 管理面的 sync、restart、start、stop、control、reset 等特殊动作。
- 独立认证的公开/会话接口，以及 SSE 等特殊传输入口。

普通管理 CRUD 的目标规则：

1. 只按受支持的 API 前缀和版本段规范化，例如 `/admin/v1`、`/api/v1/admin`；不能把任意中间段 `admin` 删除。
2. 去掉独立路径参数段；确认剩余首段是已登记的管理资源。
3. 对该资源使用确定的单数化规则，保留现有合理资源码，例如 users → user。
4. GET 的普通管理读取使用 view；POST 的资源创建使用 create；PUT/PATCH 的普通更新使用 edit；DELETE 的资源删除使用 delete。
5. 路径后缀、业务 operation 或请求语义表明它是命令时，转到显式映射；不能仅凭 `/list` 后缀、HTTP POST 或默认小写方法创建能力。
6. 路径为空、资源缺失、方法不支持、特殊动作没有映射时，报告具体缺项，停止该批次目录写入。纯字符串工具可以返回候选值，权限聚合层不得把未审核候选值当授权码。

| 方法与路径 | 目标规范码 | 判定 |
| --- | --- | --- |
| GET `/admin/v1/users` 或 `/api/v1/admin/users` | `user:view` | 同一普通管理能力 |
| POST `/api/v1/admin/users` | `user:create` | 经登记的资源创建 |
| GET `/api/v1/admin/settings` | `setting:view` | 仅当 settings 是实际登记的管理资源 |
| GET `/api/v1/admin` | 不生成 | 缺少资源，不登记新 admin:view 能力 |
| POST `/api/v1/instances/{data.instance_id}:start` | `instance:start` | 显式映射，禁止 instance:create 回退 |

`ConvertCodeByOperationID` 不作为自动替代算法。它可能把 list/get 合成 view，并加入 RPC 名称中的资源段，不能保证保持已有能力码。若启用，应独立审核完整映射，不能以“按 operation 推导”代替业务定义。

## 4 业务能力映射和权威来源

### 解析和写入规则

针对每个实际 `(method, path_template)`，按以下顺序解析：

1. 独立认证/会话或特殊传输入口：采用它自己的认证合同，记录不适用业务推导的原因。不得因此放宽其他接口。
2. 显式业务能力映射：使用已采纳的码和适用主体；旧/新地址指向同一能力时保留码值。
3. 已批准的普通管理 CRUD 映射：使用审核后的候选推导规则和规范期望码。
4. 以上均未命中：标记缺项，禁止自动写入权限或赋予角色。

映射按方法和实际注册路径模板精确匹配，不能用具体资源 URL、路径前缀或模糊匹配查能力。两份显式表对同一键返回不同码时，必须报告冲突并停止；不能依靠 else-if 顺序静默选择。

代码中的 operation policy、注册 SQL 和规范附录必须一致。现有表能够承担对应业务域的权威定义时直接复用；其他输入从该定义生成或进行一致性检查，避免再维护多份独立手写清单。附录与本规范已一并采纳为权威期望值；即便已采纳，落地仍须走已审查的对齐步骤，冲突或不一致时停止并报错，不得自动覆盖现存数据。

### 各接口面和 Quota 的完整显式目录

同目录 [业务能力映射附录](permission-code-capability-map.tsv) 逐条记录现行明确注册码，并列出目标路径、业务域、主体范围、业务模块、来源和 SHA。原始来源 SHA 保留各行编制依据；Model 与 Inference 来源分别由各自登记脚本维护。计数为：

| 接口面 | 条目 | 规范码来源 | 目标要求 |
| --- | --- | --- | --- |
| Network 租户 | 25 | NetworkOperations 与注册 SQL | 显式映射，保持 network:{资源}:{动作} |
| Network 平台 | 27 | PlatformNetworkOperations 与注册 SQL | 显式映射，保持 network:platform:{资源}:{动作} |
| Image | 11 | ImageOperations 与注册 SQL | 显式映射，保持已登记领域动作 |
| Accelerator 平台/租户 | 22 | register-accelerator-permissions.sql | 明确平台与租户读取及各个命令，不能合成 accelerator:view/create |
| Quota 自助 | 1 | 同一 Accelerator 注册脚本 | 保持 quota:account:self-read；business_module 为 TENANT |
| Instance | 9 | register-instance-permissions.sql | list/read、operation/preset读取、create/start/stop/delete/exec分别登记 |
| Model | 16 | register-model-permissions.sql | 保持 model:service:read/manage |
| Inference | 14 | register-inference-permissions.sql、register-inference-diagnostics-permissions.sql | 推理管理由 Governance BFF 提供，Higress 不承载 `/api/v1/...` |
| 合计 | 125 | 现行能力映射附录 | 所有条目均须被实际聚合入口接入并通过校验 |

这 125 条对应 103 个不同权限码。`network:snat:get` 有意用于 VPC SNAT 与 SNAT binding 两种读取，Model 与 Inference 的读取和管理分别聚合多个接口，附录已记录这些能力。其他方法/目标路径均唯一。这是接口面的现有显式能力清单，不是数据库现状快照，也不包含全部管理或其他业务接口。未实现的覆盖接入属于本规范落地缺项，不能同时声明“全域收敛完成”并把相应接口面延期。

主体范围字段用于描述已有平台/租户/本人约束，不能通过码字符串或 URL 前缀自动赋予平台身份、跨租户范围或套餐许可。BOSS/Console 可以共用 path，仍按可信主体和现有授权边界检查。

### 分组和套餐模块

permission group 是权限目录组织方式；套餐业务模块来自实际 Service tag 与业务模块映射。二者单独校验，不能把权限码首段的字符串前缀匹配当成套餐或服务所有权的权威依据。

例如 quota 的 code 域为 quota，而自助接口当前模块是 TENANT；这是明确映射，不通过把 code 改成 me:* 或 accelerator:* 消除。instance-operation 的路径也不决定权限域，应按显式能力 instance:operation:read 登记。

## 5 路由迁移表和权限映射

路由迁移与权限命名分别维护对应关系。API 目录的存量地址使用显式 rename 保留 API ID、启停状态和既有关联；`sync-apis` 不会识别旧/新路径的逻辑同一性，不可直接用 ADD 新条目代替 rename。

原有 route-map 的 method、old_path、new_path、operation、Service tag、classification、action、source_file、source_sha 保留。落地时增加或关联以下权限字段：

| 字段 | 含义 |
| --- | --- |
| expected_permission_code | 本规范业务码；若不适用，则为空并注明独立认证原因 |
| permission_source | 显式能力表、已批准管理 CRUD、独立认证合同或特殊传输合同 |
| auth_mode | 实际认证方式，包括主体/令牌用途要求；不能由空 code 推断匿名 |
| business_module | 按实际 Service tag 核验的套餐模块 |
| scope_kind | 适用的平台、租户或本人范围；不是可信入站 selector |
| review_status | 已审核、待映射或冲突；未完成项不得自动写入 |
| expected_api_set | 同码有意聚合的 API 集合，或对对应集合的权威引用 |

同一 API 既有权限 ID 集合可包含系统和业务权限。迁移验收既要检查规范业务码，也要检查全部既有权限/API关联；不能用一个标量 expected_permission_code 代替关系校验。

固定基线的 Proto 中有 288 个唯一方法/路径组合：旧 `/admin/v1` 191 个，已有 `/api/v1` 97 个。再计入规划新增 CSRF 和 SSE 自定义入口才得到 290 行候选清单；290 不表示全部接口已生成、装配或部署。仓库已提交的 route-map 仍是更早基线，执行时必须更新新增路由并冻结实际源码 SHA。

191 个旧前缀条目包含普通管理、Accelerator 平台、ModelDev 业务和 MFA 登录挑战，不能全部归为“无覆盖管理 CRUD”。尤其：

- `/admin/v1/mfa/verify` → `/api/v1/auth/mfa/verify`：按独立登录挑战合同处理，不自动把 mfa:create 换成 auth:create 并分配角色。
- `/admin/v1/modeldev/*` → `/api/v1/modeldev/*`：业务模块与主体检查保留，按已审核业务能力或既有系统授权关联核验。
- `/admin/v1/accelerator/*` → `/api/v1/admin/accelerator/*`：按显式业务能力附录核验，不走管理首段回退。
- `/events` → `/api/v1/events`：独立传输鉴权与 stream 归属另行验收。

冻结后 main 新增路由时，应补录受影响新接口并审核其权限定义；不要只改全表 source_sha 而忽略内容差异。

## 6 目录对齐和一致性判据

### 正常对齐

正常目录对齐通过已审查的注册/更新步骤完成。以稳定 code 定位权限，以实际方法/模板定位 API；保持已有 ID、启停状态和有意授权关系，新增能力则明确新增身份/角色授权。不因注册码存在就自动启用权限、API、套餐模块或用户。

完整映射、冲突和授权计划必须在任何删除/写入前预检。适用数据步骤在同一事务内更新权限及关联，错误时回滚；数据成功后再刷新策略，多实例按部署合同完成策略加载。现有先删除再聚合的 SyncPermissions 不能直接承担这项“失败无写入”合同。

`ON CONFLICT(code) DO NOTHING` 只能避免重复插入，不能证明已有记录的语义、状态、模块和关联正确。冲突项必须读取并校验；不一致时停止，不静默忽略或合并。

现在没有真实用户，可在明确的开发初始化过程中准备新权限目录与测试角色，不需要为旧用户会话兼容。但开发重建不代表日后发布也可以丢弃授权关系。

### 一致性判据

1. 实际聚合结果、注册 SQL、服务端直接能力检查和规范期望码一致。
2. 业务接口使用显式映射；start、stop、exec、delete等独立能力不能被方法推导合并。
3. 旧/新地址表示同一能力时，规范码保持一致；有意改码必须有明确变更映射。
4. permission_id、api_id、状态和权限/API/角色关联按本批定义保持；开发重建另按声明的新授权矩阵验收。
5. 同一码的所有 API 都属于已批准能力集合；额外绑定或遗漏均失败。
6. 缺项、冲突、未知方法/资源、错误主体范围或未分类模块均给出具名错误，阻止本批写入；不报虚假通过。
7. 权限组分类、套餐模块、身份与租户检查分别通过，不能由码值一致替代业务拒绝用例。

迁移验收使用独立的规范期望值和数据库关系快照，不只比较同一推导器的 old_path/new_path 输出；keep 路径同样核验。路径相同导致的“旧等于新”不是权限定义正确的证据。

## 7 硬删重建的操作边界

当前 `SyncPermissions` 会删除非 sys: 权限并重新生成，不能保证原 permission_id、角色绑定和权限组保留。生成的码值正确，也不表示授权准备完成。它不是路径迁移或日常发布的必需步骤。

该入口不能作为本规范正常对齐流程的默认命令。开发环境若明确选择重建，必须在已授权的独占范围内执行，并依次验证能力表、准备角色/权限/API关联、刷新策略和执行主链及拒绝用例；完成之前不能宣称“重建成功即可使用”。

如果后续将同步改造成正常可用的增量对齐，需要单独实现并验收：稳定 ID、保持状态、有意更新关联、冲突拒绝、事务错误传播和幂等重跑。此修订稿不代表该代码改造已获授权或已完成。

修改 code 字面值时，后端常量/策略、注册脚本、查询 code 的逻辑和测试必须配套。JWT 已签发的 Permissions 不会因数据库更新而变化；当前开发阶段可明确重新登录准备新测试会话，不需要旧码兼容层。

## 8 校验和实施顺序

### 必需校验

| 检查 | 覆盖内容 |
| --- | --- |
| 命名 | 字符语法、保留域、业务含义和已有合理码值 |
| 映射覆盖 | 94条显式目录全命中；管理CRUD候选经过批准；未知项和冲突被拒绝 |
| 实际接入 | 验证 appendAPis/实际目录聚合确实使用目标映射；仅测试取码函数不足 |
| 多源一致 | 策略表、注册SQL、规范附录、实际聚合结果和服务端能力检查一致 |
| 聚合粒度 | 每个规范码对应准确API集合；Instance start/stop/exec等不能相互获得授权 |
| 目录关系 | code和ID/状态、角色/权限/API/菜单关联符合对齐或开发重建的明确合同 |
| 运行行为 | 授权主体成功；缺能力、错租户、错平台身份、无套餐模块等拒绝 |
| 路由迁移 | 最新基线清单、特殊路由与独立认证例外、旧/新路径及策略加载 |

`admin sync-apis --dry-run` 只复核 API 目录差异，不校验权限码、覆盖接入或角色绑定。Go定向验证需包含 converter、auth映射函数以及 service层实际聚合；数据库关系和运行授权另在独占资源上验证，不能被两个包的字符串测试替代。

未进行测试、skip、缓存结果和真实pass分别记录。本次仅修改规范与附录，没有执行上述代码或数据库验收。

### 实施顺序

1. 采纳命名/能力定义，核对附录与实际目标基线；确定普通管理 CRUD 与特殊接口的完整映射。
2. 修补前缀推导兼容，并接入所有纳入范围的显式能力映射；缺项和冲突拒绝写入。
3. 完成策略、SQL、聚合层与后端能力检查的一致性验证，再准备权限目录与测试授权。
4. 执行路由rename、生成、前端地址配合和实际业务链验收；保持会话改造与本批范围分别报告。
5. 更新唯一正式规范与接口登记，列出实现SHA、具名结果和未完成项。代码提交或文档存在不自动结项。

## 9 当前实现与目标规范的差异

以下状态只针对本稿固定 main 基线。其他分支可提供相应提交及测试证据更新状态，不把它们预先描述为 main 事实。

| 项目 | 固定基线状态 | 目标 |
| --- | --- | --- |
| 管理前缀跳过 admin | 当前推导器仍取去版本后的首段 | 只对已登记管理CRUD兼容新前缀 |
| Image 显式码 | 已接入 appendAPis | 保持一致并覆盖全部11条 |
| Network 租户 | 取码函数存在，当前聚合入口未接入 | 接入明确能力映射 |
| Network 平台 | 策略码和SQL存在，当前聚合入口未接入 | 接入明确能力映射 |
| Instance/Accelerator | 注册码存在，当前聚合入口无对应显式覆盖 | 完整接入；不能标为完成后再将缺口延期 |
| 普通管理和其他特殊动作 | 当前默认方法推导会聚合部分不同操作 | 批准普通CRUD范围，特殊动作明确映射 |
| 目录正常对齐 | API增量同步与权限硬删重建是不同入口 | 路径rename/权限对齐分别保持明确定义 |

## 附录 源码与规范输入

源码引用固定为核对基线，便于复核。业务能力附录中每行同时列明对应文件和SHA，附录的“已提取”不代表覆盖函数或部署已经实现。

- [权限码候选推导](https://github.com/zhangzhe-ctrl/ani-governance/blob/34388cf90d0834da76c9e5701ade5d92a248144f/pkg/utils/converter/api.go)
- [权限聚合与重建](https://github.com/zhangzhe-ctrl/ani-governance/blob/34388cf90d0834da76c9e5701ade5d92a248144f/app/admin/service/internal/service/permission_service.go)
- [权限仓储与硬删实现](https://github.com/zhangzhe-ctrl/ani-governance/blob/34388cf90d0834da76c9e5701ade5d92a248144f/app/admin/service/internal/data/permission_repo.go)
- [Casbin策略来源](https://github.com/zhangzhe-ctrl/ani-governance/blob/34388cf90d0834da76c9e5701ade5d92a248144f/app/admin/service/internal/data/authorizer_provider.go)
- [平台网络显式能力检查](https://github.com/zhangzhe-ctrl/ani-governance/blob/34388cf90d0834da76c9e5701ade5d92a248144f/app/admin/service/internal/service/platform_network_service.go)
- [业务模块映射](https://github.com/zhangzhe-ctrl/ani-governance/blob/34388cf90d0834da76c9e5701ade5d92a248144f/pkg/constants/module_mapping.go)
- [API目录增量同步](https://github.com/zhangzhe-ctrl/ani-governance/blob/34388cf90d0834da76c9e5701ade5d92a248144f/sql/bootstrap/catalog.go)
- [部署与目录管理合同](https://github.com/zhangzhe-ctrl/ani-governance/blob/34388cf90d0834da76c9e5701ade5d92a248144f/docs/deployment.md)
- [已发布的认证与路径迁移计划](https://github.com/zhangzhe-ctrl/ani-governance/blob/34388cf90d0834da76c9e5701ade5d92a248144f/docs/plans/gov-auth-session-api-unification/README.md)

统一算力目录 `GET /api/v1/compute-presets` 使用[usage 权限表](compute-presets.md#请求与业务授权)，不按路径推导单一码。`pkg/permcode/compute_catalog.go` 固定三项精确映射；通用 Casbin 路径授权之后，Service 仍在当前数据库快照检查该 usage 的权限和套餐，禁止另一用途的授权代替。

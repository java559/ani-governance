# 部署、初始化与 API 管理

固定顺序：**Atlas 迁移结构 → 显式初始化数据 → 检查 → 启动服务 → 登录/API 验收**。
服务启动不建表、不播种、不同步 API、不恢复默认密码。配置 `data.database.migrate` 必须为 `false`；旧配置为 `true` 时明确报错。

初始化复验应使用全新、可丢弃且以 `_bootstrap_test` 结尾的独占库，设置 `GOV_BOOTSTRAP_TEST_DSN` 后运行 `go test -tags=quota_pg -count=1 -run 'Test(BootstrapPostgres|EntClientRejectsStartupMigration)$' ./app/admin/service/internal/data`。该标签使测试包注册 pgx 驱动；只用 `-run` 不会注册它。另可运行 `go test -count=1 -run 'Test(ConstructorsDoNotAccessDatabase|CatalogValidation|ServiceTagToBusinessModuleExactMapping)$' ./sql/bootstrap ./app/admin/service/internal/service ./pkg/constants`。测试的 `Schema.Create` 仅用于临时测试库；实际部署仍按 Atlas 与显式 admin CLI 流程执行。历史部署结果只对应其当时源码与环境，不能代替当前复验。

## 1. 准备配置和制品

构建环境按 `go.mod` 使用 Go 1.26.9，从仓库根目录执行：

```bash
make build_only
make build_admin
```

镜像制品从仓库根目录构建，产出**两个独立镜像**（同一个 Dockerfile，按 target 区分）：

```bash
make docker_server   # 服务运行时镜像 $(PROJECT_NAME)/$(APP_NAME)
make docker_admin    # 一次性运维 CLI 镜像 $(PROJECT_NAME)/$(APP_NAME)-admin

# 或直接用 target 构建（未指定 --target 时默认构建服务镜像）
docker build --target runtime-server -t <registry>/ani-governance:<tag> .
docker build --target runtime-admin  -t <registry>/ani-governance-admin:<tag> .
```

两个镜像的基础镜像都是 `gcr.io/distroless/static-debian12:nonroot`：容器内无 shell、自带 CA 证书、以 UID 65532 运行；二进制以 `-trimpath -ldflags "-s -w"` 构建，符号表与 DWARF 已剥离。服务镜像内路径为 `/app/bin/server` 与 `/app/configs`；运维镜像内只有 `/app/bin/admin`，不含配置文件。

构建期依赖代理默认 `https://goproxy.cn,https://proxy.golang.org,direct`（校验仍由 `go.sum` 保证），可用 `--build-arg GOPROXY=...` 覆盖；构建环境若要求 `proxy.golang.org` 在前（见 `AGENTS.md` 的 GOPROXY 约束），显式传入即可。

服务使用 `app/admin/service/configs/` 的配置结构。部署时提供独立配置目录，至少核对：

| 配置 | 要求 |
|---|---|
| `data.database` | PostgreSQL 地址、库名、账号；`migrate: false` |
| `data.redis`、`server.asynq.uri` | 两处都指向可用 Redis，密码正确 |
| `auth`、加密配置 | 使用部署环境自己的密钥，多副本保持一致；`ANI_ACCESS_KEY_ENCRYPTION_KEY_FILE` 必填，指向独立 64 位十六进制主密钥文件，不能明文降级 |
| `authz.type` | 主服务只支持 `casbin`；主装配在启动期校验，非 `casbin` 直接拒绝启动。库内 noop 类型不构成受支持部署配置；`opa` 已移除（见 [历史 OPA 移除记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/opa-removal-plan.md)） |
| `server.rest.addr` | 默认 7788；按实际监听地址与反向代理配置 |
| CORS / Cookie | 优先同域代理；跨端口开发需正确 Origin、凭证设置；代理 HTTPS 传递正确协议 |
| Network | 未接入时不设置 `ANI_NETWORK_ADDR`；接入时另配 mTLS 和下游数据 |

迁移账号负责结构变更；服务运行账号只需业务表/序列的必要读写权限，不授予建表、改表权限。Atlas、初始化命令和服务必须连接同一个目标数据库。

## 2. 数据库结构由 Atlas 单独执行

`migrations/` 是结构迁移目录，包含本次从 Ent schema 生成的初始基线及 `atlas.sum`。种子数据不放在结构迁移里。Atlas 的标准版本迁移流程见 [Ent 官方说明](https://entgo.io/docs/versioned-migrations/)。

由部署环境注入 `ANI_DATABASE_DSN`，不要把真实连接串提交到仓库。安装 Atlas 后，从根目录运行：

```bash
./scripts/atlas.sh migrate status
./scripts/atlas.sh migrate apply --dry-run
./scripts/atlas.sh migrate apply
```

`scripts/atlas.sh` 固定运行目录，避免 Ent schema 位于 Go `internal` 目录导致加载失败。

**集群内执行时用独立 Atlas 镜像**：服务镜像与运维镜像都不含 Atlas（也不含 shell），结构迁移由 `scripts/deploy/atlas/build-image.sh` 产出的 `ani-atlas:<版本>-<migrations摘要>` 承担，作为一次性 Job 运行。Atlas 二进制不在仓库内，构建脚本只定位不下载，找不到就报错退出。

```bash
# 构建（需要 atlas v1.3.0，与 go.mod 的 arigaio/atlas 一致）
ANI_ATLAS_BIN=/path/to/atlas bash scripts/deploy/atlas/build-image.sh

# 一次性 Job（status → dry-run → apply 串行，只跑 apply 也可）
kubectl create job governance-migrate --from=job/manifest   # 或按下列片段声明
```

Job 片段（镜像内 `/app/migrations` 为迁移目录，`ANI_DATABASE_DSN` 由 Secret 注入）：

```yaml
initContainers:
  - name: status
    image: ani-atlas:v1.3.0-<migrations摘要>
    args: ["migrate", "status", "--dir", "file://migrations", "--url", "$(ANI_DATABASE_DSN)"]
  - name: dry-run
    image: ani-atlas:v1.3.0-<migrations摘要>
    args: ["migrate", "apply", "--dry-run", "--dir", "file://migrations", "--url", "$(ANI_DATABASE_DSN)"]
containers:
  - name: apply
    image: ani-atlas:v1.3.0-<migrations摘要>
    args: ["migrate", "apply", "--dir", "file://migrations", "--url", "$(ANI_DATABASE_DSN)"]
```

当前镜像构建与导入入口见 [独占开发镜像说明](../scripts/dev/images/README.md)。

### 字段清理迁移（FIELD-CLEANUP-01）

`migrations/20260923143455_drop_geo_location.sql` 删除 4 张审计日志表的 `geo_location` 列（`sys_api_audit_logs`、`sys_data_access_audit_logs`、`sys_login_audit_logs`、`sys_operation_audit_logs`）。它随其它迁移一起由 `./scripts/atlas.sh migrate apply` 执行，顺序不变。

**执行前必须备份目标库**：`DROP COLUMN` 会一并丢弃历史审计行的归属地数据，且该列不能由迁移"加回"——回滚只能靠备份恢复，不要试图用加回空列假装没删过。服务侧自 IMAGE-SLIM-01 起已不再写入该列，删列与新旧版本服务的读写不冲突（旧版本停止写入后才执行迁移即可）。

`sys_permission_policies.policy_engine` 的枚举值 `OPA` 只在代码与 Ent 生成码中删除；该列在 PostgreSQL 中是 `character varying NOT NULL DEFAULT 'CASBIN'`（无 CHECK 约束），**数据库侧不需要变更，也没有对应迁移**。既有行若存了 `OPA` 值不受影响，但服务已不再使用该引擎。

历史生成方式：本迁移为手写后经 `atlas migrate hash` 校验并在独立开发库 `migrate apply` 验证；当时 `migrate diff` 的 dev 库规范化被既有的 `sys_quota_operations` 外键问题阻塞，见 [固定版本字段清理计划](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/geo-location-field-removal-plan.md)。该记录不证明当前 diff 仍被阻塞；本次文档修订未运行生成或数据库验证。

开发者修改 schema 后，先完成 Ent 生成并运行 `go run ./cmd/schema > schema.sql`（在 `app/admin/service` 下；`make ent` 和 `scripts/generate-aksk-slice.sh` 已包含此步），再用**独立、可清空的开发数据库**生成下一份迁移：

```bash
# ANI_ATLAS_DEV_DSN 指向独立开发库，不能指向待升级的数据库。
./scripts/atlas.sh migrate diff change_name
```

Atlas 的声明式来源为生成的 `app/admin/service/schema.sql`。其导出器在 Ent 表定义上补充 Key 的同租户角色复合外键；不要将来源改回单独 `ent://`，否则可能丢失该约束。结构生成在构建环境执行，部署 `migrate apply` 不需要 Go。审查生成 SQL 和恢复方案，连同 `atlas.sum` 提交。部署只执行已审查的迁移，不在启动时生成迁移。

**已有数据库不能直接套初始建表 SQL。** 先备份，在副本上对比现有结构和基线；完全匹配后才能按 [Atlas baseline 流程](https://atlasgo.io/versioned/apply#existing-databases) 建立版本记录。存在差异时先编写明确的迁移。不能仅用 `--allow-dirty` 跳过接管判断。

## 3. 首次数据初始化：有种子 SQL，密码由部署传入

种子 SQL 为 `sql/bootstrap/001_initial.sql`，通过 `admin init` 执行：

```bash
# 文件由部署环境或 Secret 提供，只包含密码；限制文件读取权限。
./bin/admin init --username admin --password-file /run/secrets/governance-admin-password
./bin/admin check
```

使用运维镜像时（一次性作业，不随服务副本启动）：

```bash
docker run --rm \
  -e ANI_DATABASE_DSN='postgres://USER@HOST:5432/DB?sslmode=require' \
  -v /run/secrets/governance-admin-password:/run/secrets/governance-admin-password:ro \
  <registry>/ani-governance-admin:<tag> /app/bin/admin init --username admin \
  --password-file /run/secrets/governance-admin-password

docker run --rm -e ANI_DATABASE_DSN='postgres://USER@HOST:5432/DB?sslmode=require' \
  <registry>/ani-governance-admin:<tag> /app/bin/admin check
```

`admin` 只连接 PostgreSQL，不需要 Redis、邮件或其他业务服务。密码须满足当前初始策略：至少 8 位、至少三类字符，并受 bcrypt 长度限制。没有内置默认管理员密码。

首次初始化在一个事务内完成：

- 从随二进制发布的 OpenAPI 登记 API。
- 创建平台管理员、启用的密码凭证、平台角色及其权限关联。
- 创建租户管理员模板、权限和菜单。
- 创建“基础管理”套餐，开放 `DASHBOARD`、`OPM`，覆盖后台入口、个人资料与租户用户管理。
- 创建基础语言、口令策略参数，最后记录初始化完成标记。

关联按角色/权限代码、菜单名称、API `(method,path)` 查找，使用数据库实际生成的 ID。种子没有价格、下游资源配额或自动创建的业务租户。

初始化失败整体回滚；重复执行已完成的初始化不改任何业务行，不覆盖密码、套餐或管理员后续配置。无完成标记但已有用户/角色等数据时会拒绝重新播种，避免把历史数据库当空库覆盖。

`admin check` 和服务启动时的检查都是只读检查：确认存在启用的平台账号、凭证、后台访问权限及必要管理 API 授权。它不能代替实际密码登录、租户隔离或下游服务验收。

## 4. 启动与最小业务闭环

```bash
# 开发环境（默认配置已正确填写）
gow run admin

# 制品或镜像内的服务进程
/app/bin/server -c /run/governance/configs
```

根 Dockerfile 产出两个镜像：服务镜像（默认 target `runtime-server`）只含 `/app/bin/server` 与 `/app/configs`，运维镜像（`runtime-admin`）只含 `/app/bin/admin`。默认命令只启动服务；初始化单独以一次性作业或运维命令执行，不放在每个副本的启动命令中。

镜像注意事项：基础镜像无 shell，容器内不能 `sh -c` 执行探针或脚本（迁移仍用独立 Atlas 镜像）；二进制已剥离符号，panic 栈只有地址，排障需按 build id 对应源码，必要时临时用未剥离构建复现。

先用首管理员登录平台：`POST /api/v1/auth/platform/password/login`。登录不再需要图形验证码或验证码请求头；客户端在 JSON 的 `password` 字段直接提交用户输入的密码，不做 AES/Base64/bcrypt 编码，通过 HTTPS 访问部署入口。接口报文和待调整事项统一见 [功能对接与接口风格改动登记](interface-integration-register.md)。不要把“数据库里是 bcrypt”误解为“客户端发送 bcrypt”。

登录后至少验证 `GET /api/v1/admin/me`、`GET /api/v1/admin/initial-context`、`GET /api/v1/admin/plans`，确认能访问管理接口。

然后读取“基础管理”套餐的实际 `id`，通过 `POST /api/v1/admin/tenants:with-admin` 创建租户及管理员：

```json
{
  "tenant": {"name": "示例租户", "code": "example", "status": "ON", "plan_id": 123},
  "user": {"username": "manager", "status": "NORMAL"},
  "password": "由创建者安全提供的初始密码",
  "activation_mode": "IMMEDIATE"
}
```

`123` 必须换成查询得到的套餐 ID；`subscription_plan` 文本不能替代 `plan_id`。`IMMEDIATE` 为默认模式，直接启用账号，不需要邮件。需要邀请邮件时按 [账号开通说明](onboarding.md) 使用 `EMAIL_INVITATION`。

租户登录使用 `POST /api/v1/auth/password/login`，`tenant_name` 填租户的 **code**（示例为 `example`）。登录后验证个人资料、初始上下文、用户列表和租户内创建用户。创建用户的请求还必须包含实际的 `data.role_ids`（先从 `/api/v1/admin/roles` 查询）及明确提供的密码；缺少角色会返回 400。平台租户 ID `0` 不代表可以直接访问任意下游租户资源。

## 5. API 的来源、登记和授权分开管理

| 内容 | 来源与变更方式 |
|---|---|
| 路由、方法、操作名称 | Proto/BFF 生成的 OpenAPI，与服务二进制一起发布 |
| API 对应业务模块 | `pkg/constants/module_mapping.go`，缺映射时同步报错 |
| 数据库 API 目录 | 显式增量同步，以 `(path,method)` 匹配，保留 ID 和启停状态 |
| 权限 → API、角色 → 权限 | 首次种子中的明确清单，后续通过管理入口或单独审查的数据 SQL 修改 |
| 租户允许使用的模块 | 租户 `plan_id` 对应的 `sys_plan_modules` |

升级新增接口时：

```bash
./bin/admin sync-apis --dry-run
./bin/admin sync-apis
./bin/admin check
```

运维镜像内把 `./bin/admin` 换成 `docker run --rm -e ANI_DATABASE_DSN=... <registry>/ani-governance-admin:<tag> /app/bin/admin`。根 Dockerfile 使用 CMD 而非 ENTRYPOINT；覆盖命令时必须给出镜像内二进制路径。K8s 一次性 Job 使用 `command: ["/app/bin/admin"]`，子命令放在 `args`。

预览只读；应用不清表、不重新编号、不删除旧 API、不改权限关联。代码已删除的 API 会显示 `REVIEW`，保留到人工审查其授权关系后再明确清理。相同 `(path,method)` 已有多行会报冲突，不猜测应该保留哪一行。

新增 API 默认没有任何权限绑定，平台管理员也不因目录同步自动获得新权限。按实际接口用途选择权限代码，并检查目标租户套餐包含相应模块。修改已有权限的 API 列表时保留原关联，使用明确 `update_mask`；不要把一次新增误写成替换整组权限。

服务内原有 `SyncApis` 入口已使用同一增量实现。`SyncPermissions` 是另一个会重建派生业务权限的功能，**不是发布流程的一步**。

通过 CLI/SQL 改数据后重启服务实例，使内存中的 Casbin 策略刷新；启动只读取策略。管理 API 入口按现有逻辑刷新当前实例，不能据此声称其他实例的策略已同步。

### 算力规格管理归属迁移

平台规格管理使用 `ComputePresetService`、既有 `SYSTEM` 管理分类和
`compute:preset:admin:list/read/create/update/delete`；set-enabled 复用 update。
租户目录 `GET /api/v1/compute-presets?usage=CONTAINER_INSTANCE` 仍要求 `instance:preset:read` 和 `INSTANCE`。
`SYSTEM` 不是租户使用算力的套餐授权，平台管理仍检查真实平台用户、tenant=0、平台角色和精确权限。

首次安装只在显式同步新 API 目录后执行
[`register-compute-permissions.sql`](../scripts/ops/sql/register-compute-permissions.sql)。
新权限初始为 OFF，后续启用和角色绑定由明确的权限管理操作完成；它不授角色、不改变已有权限启停或关联限制，不适用于已有旧管理权限的库。
已有库在联合迁移演练及匹配版本门禁通过后，暂停规格写入和实例新受理，处理在途请求并停止所有旧 SQL/RPC 访问进程；
对已核对且备份的 Gov 库显式执行
[`migrate-compute-preset-permissions.sql`](../scripts/ops/sql/migrate-compute-preset-permissions.sql)。
它只原地改名五个管理 permission code，并按 path/method 更新六条 API 的 owner、operation 和 SYSTEM 分类；
ID、启停、角色/API 关系和审计字段保持，源目标冲突、缺项或异常 owner 会整事务失败。
新 Res 就绪后启动新 Gov，刷新所有实例的 Casbin 策略并取得新身份凭据，验证平台权限和租户目录后再受控恢复受理；
不运行 `SyncPermissions`、不重复首次种子、不批量补权。

Gov 权限库与 Res 规格库分别提交，必须分别记录结果。恢复时先停止新进程，根据两库实际提交状态协调执行
[`restore-instance-preset-permissions.sql`](../scripts/ops/sql/restore-instance-preset-permissions.sql)
及 Res 已演练的逆向迁移，恢复匹配二进制、刷新全部策略并验证后才开放受理。
这些脚本存在不表示已经在目标库执行或已完成容器业务验收。

### 补齐旧库的租户登出授权

2026-09-21 已在首次种子中补上 `sys:tenant_manager` → `POST /api/v1/auth/logout`。用旧种子初始化的数据库，需要显式执行一次数据补丁；重跑 `admin init` 不会修复已初始化的库。

使用目标库连接配置执行（例如按环境设置 `PGSERVICE` 或 `PGHOST` / `PGDATABASE` / `PGUSER`，密码使用凭据文件）：

```bash
psql -v ON_ERROR_STOP=1 -f scripts/ops/sql/repair-tenant-logout.sql
```

补丁只补这一条权限关联，按权限 code 和 API 方法/路径查找，保留现有 ID；重复执行不重复插入。缺少或重复的前置权限/API 会报错并回滚，不自动新增 API、不启用被停用的账号/权限/接口，也不放宽套餐。

执行后重启所有服务实例以重载 Casbin 策略，再用启用且套餐包含 DASHBOARD 的租户管理员验证登录、登出及旧令牌失效。这里的重启只读数据库，不会自动执行补丁。

### ModelDev 执行合同切换

本轮删除训练 Release 导入、启用、暂停和绑定审批；`modeldev-pause`、`modeldev-enable`、`modeldev-import-release` 不再注册。输入导入保留 `modeldev-import-csv` / `modeldev-import-finetuning-dataset`，当前租户用户需具有独立 `modeldev:manage_input` 与 ALL 范围，导入按实现和固定对象校验。

切换须先停止新受理、备份并盘点原 acceptance/dispatch、未决创建和适用 GPU 原账，再显式运行 Atlas 迁移，发布匹配 ModelDev/Gov 合同、镜像与材料。历史 raw payload 不能交给新 decoder；历史 binding 表的字段、数据和权限完整保留，新活动合同不再读取该表。原二进制与新合同通过隔离的持久化工作面切换，不能交叉写 codec。旧记录、PVC/publication/模型的保留和回退以本任务实际备份/清理记录为准，普通进程不执行 DDL。

生成 API 后先运行 `admin sync-apis --dry-run`，再按部署授权同步新 implementation 规则查询。新 `(path,method)` 需显式当前权限和套餐关联，目录同步不自动授权。旧 Release 权限和 `/api/v1/modeldev/presets` 退出活动入口。

微调模型登记恢复使用 `POST /api/v1/modeldev/executions/{execution_id}:resume-model-registration`，请求体和查询参数均为空。先同步该精确方法，再通过既有权限管理入口授予当前用户；仍要求原执行读取权限和当前 Model 读取、管理权限。恢复仅读取本租户原冻结受理，向 owner 请求同一 execution 的登记恢复，不创建新训练、受理或注册键。`PENDING`、依赖暂不可用和未知响应保留原状态，后续查询原 execution；只有 owner 的完整模型及版本 READY receipt 可作为登记完成依据。

大模型同步校验需显式配置有限 `ANI_MODEL_TIMEOUT`；默认 30s，客户端拒绝超过 24h。`ANI_MODELDEV_TIMEOUT` 默认 3s，当前客户端校验正值，部署值应在 HTTP owner 预算支持的 24h 以内。HTTP 仅对 Model 的上传 URL、版本创建，以及 ModelDev 的执行创建、产物下载授权、模型登记恢复使用对应 owner 预算，并继承更早的父请求期限和取消；其它 API、日志、管理及关闭预算保持原配置。Model 服务端同步读取整个固定对象后才返回版本 READY，因此 receiver、RPC caller 和这两个 Model HTTP 入口须装配一致的实际预算，不能只延长一层或对未知结果自动重发创建。

## 6. 日常升级与排错

日常升级：备份 → Atlas 迁移 → API 差异预览/登记 → 明确的权限或套餐变更 → `admin check` → 启动/重启 → 实际登录和目标 API 验收。不要重跑首次种子来补新版本数据。

| 现象 | 优先核对 |
|---|---|
| 启动报禁止迁移 | 把旧 `migrate: true` 改成 `false`，先单独完成 Atlas 迁移 |
| 表不存在 | Atlas 是否连接同一库、迁移是否全部成功 |
| 启动前检查不通过 | 新库是否执行 `admin init`；旧库账号/凭证/角色/API 关联是否完整 |
| 登录失败 | 平台/租户入口、租户 code、是否仍误发 AES/Base64 密文、账号/凭证状态 |
| 登录成功但 API 403 | 该方法和模板路径是否登记；角色权限是否绑定；租户状态、plan_id、模块白名单 |
| 重启后权限表现不同 | 是否曾只改过某实例内存策略；CLI/SQL 更新后是否刷新所有实例 |

“基础管理”套餐不包含 Network 等下游模块；需要时明确增加套餐模块和接口权限，并单独验收下游接线。进程启动、只读检查、真实登录、目标 API 成功分别记录，不能互相替代。

2026-09-21 Ubuntu kind 的初始化、账号获取、NodePort 验收与恢复记录见 [固定版本运行记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/deployment-kind-20260921.md)。其中环境与结果只对应当时版本，不是当前部署状态。


## 7. AK/SK 签名与首次 VPC 查询

2026-09-22 首批按无旧 Key、无存量用户实施，直接提供最终初始结构和种子，不提供旧 Key 转换或兼容交换入口，见 [固定版本执行与验收记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/aksk-vpc-execution-plan.md)。管理接口为 `/api/v1/auth/api-keys`，仅接受有权限的非零租户用户 JWT。当前业务签名覆盖登记的 Network 与 Image 操作，范围和鉴权条件见 [接口登记](interface-integration-register.md)；300 秒窗口、规范签名和写入重试见 [AK/SK 合同](contracts/aksk-signature.md)。支持签名不自动授予角色权限或套餐模块。

启动前必须提供 `ANI_ACCESS_KEY_ENCRYPTION_KEY_FILE`。文件内容为 32 随机字节的 64 位十六进制文本，最多一个结尾 LF，所有副本一致，以 Secret 挂载并备份；丢失后不能仅凭数据库恢复 SK。缺失、长度错误或不可读时服务构造失败。示例生成命令在部署主机执行：

```bash
umask 077
openssl rand -hex 32 > /run/secrets/governance-access-key-encryption
export ANI_ACCESS_KEY_ENCRYPTION_KEY_FILE=/run/secrets/governance-access-key-encryption
```

`admin init` 本身不要求 SK 主密钥，因为它只执行显式首次播种。首次种子已包含六条新管理路由与租户管理员权限。基础套餐仍仅 DASHBOARD/OPM；用管理 API 创建本次专用套餐，保留这两项并增加 SYSTEM/NETWORK。角色由已有管理权限的操作人显式准备：租户管理员默认能查角色，默认没有角色创建权限。Key 必须绑定本租户 ON/TENANT 角色。

为目标角色授予 VPC 权限时，使用已有 `scripts/ops/sql/bootstrap-network-access.sql`，明确传入真实 tenant_id/role_id；该脚本检查持久化 resource_tenant_id 与 NETWORK 套餐，随后重载内存策略。管理 API 也可维护权限，但不能把开模块当成授予所有 API 权限。

可复现的空库建表、初始化、平台创建套餐/租户/角色、租户创建 Key、Python NodePort 查询及负向验收命令见 [隔离实验说明](../scripts/experiments/aksk-vpc/README.md)；本次真实结果见 [验收证据](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/aksk-vpc-20260922/README.md)。客户端为 `scripts/ops/aksk_vpc_client.py`，默认验证 HTTPS、不跟随重定向；隔离 HTTP 必须明确设置 `ANI_ALLOW_HTTP_FOR_TEST=1`。

## 8. 配额账本与迁移顺序（QUOTA-GPU-LOCAL-01 增量）

配额功能引入三段式升级，**顺序固定为 expand → data → constraints**，不能用一次 `migrate apply` 推进带存量数据的库：

1. `migrations/20260922190000_quota_expand.sql`：目录/账本五张表 + `sys_plan_quotas.quota_code`（nullable）。
2. `sql/data/20260922_quota_catalog_backfill.sql`：版本数据脚本，插入固定目录（user.count/storage.bytes/api.calls/gpu.count）并精确回填旧行；任何坏数据（NULL/未知类型/重复/越界/悬空 plan）整笔失败，不合并不删除。幂等可重跑。
3. `migrations/20260922190100_quota_constraints.sql`：回填完成后加 NOT NULL、UNIQUE(plan_id,quota_code)、目录 FK、数值 CHECK。

运行账号仍无 DDL；服务配置保持 `migrate: false`。配额内部退额 listener 默认关闭；Resource 实例启用时显式设置 `ANI_QUOTA_ENABLED=true`、`ANI_QUOTA_INTERNAL_ADDR`、`ANI_QUOTA_CA_FILE`、`ANI_QUOTA_CERT_FILE`、`ANI_QUOTA_KEY_FILE` 和 `ANI_QUOTA_RESOURCE_DNS_SAN`。ModelDev GPU owner 另须显式设置 `ANI_QUOTA_MODELDEV_ENABLED=true`，仅注册唯一 DNS SAN `ani-modeldev-service` 到同名 owner，保留原 Resource 映射；不接受别名或复用原创建 UUID 作为关闭 UUID。新 GPU 受理在同一原事务中提交 ModelDev acceptance、唯一投递记录及原 quota operation/charges；通用 quota worker 在扫描和 CAS 时均排除 ModelDev，只由其专属投递队列发送原命令。缺凭据或合法映射即启动失败，不降级明文。Inference 管理接口由 Governance BFF 提供；部署 Inference owner 时配置 `ANI_INFERENCE_*` mTLS 客户端参数及精确服务端 DNS SAN，Model 配置保留。完整接线、GPU code 与验收边界见 [GPU owner 接入指南](contracts/gpu-owner-integration-guide.md)；旧 `quota_lab` 模拟路由与构建标签已退役。新账本表的 CHECK 约束已通过 `entsql.Annotation.Checks` 进入 Ent 导出的 schema.sql，与手写迁移同名同义。

本次 Atlas 检查确认 AK/SK、审计与角色复合约束一致。同时发现实施前初始基线仍保留 `files` 表、`sys_users.avatar` 列，而 Ent 已移除它们；这两项既有偏差不属于本批，未执行删除。今后 `migrate diff` 必须审查这些 DROP 候选，不得直接应用。

### 统一租户算力目录切换

当前租户目录与三用途授权以[合同](contracts/compute-presets.md)为准。
先完成定向测试、真实 PG 权限与路由迁移/恢复演练，准备精确 Gov 镜像。
核对目标集群、Gov 库和部署身份，保存原 API 行、permission/API 映射、角色链接、套餐及原镜像。
停止 Gov，执行 `scripts/ops/sql/migrate-compute-catalog-route.sql`，再用新 admin CLI
`sync-apis --dry-run` 核查后显式 `sync-apis`，避免先同步产生新旧双 API 行。
SQL 原地保留旧 API ID 与原 `instance:preset:read` 授权；新两权限不会补入任何角色或套餐。
部署新 Gov 并按本指南刷新策略，检查新三用途授权、旧 URL 404、平台 CRUD 和容器列表。
Resource 协议、二进制、数据与实例运行对象无需变更。
失败时停止 Gov，执行 `restore-instance-catalog-route.sql`，恢复原镜像并刷新策略。
恢复脚本只删除训练/推理到该路由的映射，保留权限本身及角色、套餐状态；恢复之前检查没有外来路由映射。

### Sandbox 父实例删除保护切换

若环境曾应用只有创建记录表的早期 `20261028010000` 草稿，后续 `20261029101000_sandbox_fence_deployment_repair.sql` 通过 Atlas 补齐两张删除保护表；已具备完整结构的环境保持不变。迁移后仍须核对运行角色对三张表的 DML 权限。

`20261028010000_sandbox_parent_fence.sql` 为创建意图、父删除检查及其凭据增加三张持久表；迁移账号执行 Atlas 后，按本页运行角色授权步骤确认三表的 DML 权限。父实例删除依赖 Resource 的 Sandbox List 可用且 mTLS 权限正确；检查不可用时删除返回 503，已受理删除的同键重放仍可查询。行为以[前端 HTTP 合同](contracts/frontend-http-api.md)为准。

旧 Gov 不登记创建意图，普通滚动升级或仅等待固定时间无法保证旧 RPC 不再到达。切换前必须停止旧版本接单，断开或排空旧连接及代理缓冲，以可核实证据确认旧 Resource 请求处理和相关 PostgreSQL 事务均结束，再启用新版本的写入；无法证明时保持写入关闭，不宣称新屏障覆盖旧流量。历史 Sandbox 由父删除前的 Resource 全量检查覆盖，不通过迁移假设历史子资源为空。回退到旧 Gov 同样不能与新保护并行写入。

切换前还须冻结父实例 DELETE 派发，核对所有既有 `QUEUED`、`DISPATCHING`、`UNKNOWN` 及已受理但尚未完成的删除与其 Sandbox；尤其对曾发送但结果未知的请求，必须证明旧请求已排空，不能仅凭时间或一次查询放行。新受理的 DELETE 事务会记录检查凭据，worker 在派发前核验；历史 DELETE 没有该记录时停止派发，错误码为 `SANDBOX_DELETE_CHECK_MISSING`，不会自动创建检查凭据或宣布历史队列安全。处理这些历史记录需要单独对账和恢复方案。

该顺序保护位于 Gov，Resource 的 `InstanceService.AcceptDelete` 不读取 Gov 的检查凭据。Resource mTLS 白名单中的可信调用身份只能由完成上述协调的新 Gov 正常链路使用；其他进程不得持同一身份直调实例删除或绕过 Gov 创建 Sandbox，否则保护不成立。上线前必须核对证书使用方、代理和直连路径。

未确认创建命令可从 `sys_sandbox_create_intents` 按租户与实例核对，`last_error_code` 是脱敏 RPC 状态码，`retry_at` 与 `lease_until` 仅控制重试调度。创建结果未知时不得删除该记录或仅按超时放行父删除。如果冻结规格被 Resource 新配置拒绝，恢复与原命令一致的可信配置，使 Resource 受理原命令，再通过正常 Sandbox 删除清理；这不是所有永久错误都能自动收敛的保证。运维过程中仍需遵循租户授权与数据库访问边界。

## 9. 平台组件日志流（PLAT-COMP-02 增量）

BOSS 日志窗口 `GET /api/v1/platform/components/{component}/logs/stream` 读 OpenSearch 的
`ani-logs-*`，不经 Higress、不经 Loki。发布顺序固定为：只读身份 → 配置与挂载 → 镜像 →
`sync-apis` → 权限 SQL → 刷新策略。任一步失败即停止后续发布。

**默认关闭。** `charts/ani-governance/values.yaml` 的 `componentLogs.enabled` 默认 `false`；
未注入 `ANI_COMPONENT_LOGS_CONFIG` 时该接口返回 503 `COMPONENT_LOG_UNAVAILABLE`，
组件状态接口与其余接口不受影响。

1. **OpenSearch 只读身份。** 按 `deploy/component-logs/opensearch-reader-role.json` 建角色
   （`ani-logs-*` 的 search/scroll/mappings get/admin get + `cluster_monitor`），再建
   rolesmapping 与独立账号；不复用管理员或 Fluent Bit 写身份。用该账号验证：查询允许索引 200，
   写文档、系统索引、删索引、改 pipeline、security API 全部 403，错误密码 401。
   写权限负测只能指向本次测试索引，不得触碰正式日志。
2. **配置与挂载。** 复制 `deploy/component-logs/config.example.json` 到 ConfigMap，CA 与
   用户名/密码放进专属 Secret，按 `charts/ani-governance/templates/deployment.yaml` 挂到
   `/run/component-logs` 与 `/run/component-logs-credentials`（`defaultMode: 0440`），
   并设置 `ANI_COMPONENT_LOGS_CONFIG=/run/component-logs/config.json`。不得挂到只读
   `/app/configs` 子目录，不得用 `/tmp`（emptyDir、可写、重启即丢），不得按数组下标 patch。
   配置解析使用 `DisallowUnknownFields`：写出 `lifetime_seconds` 之类未知键会**启动失败**，
   这是刻意行为——8 秒是契约常量，不可配置。
3. **发布镜像。** server 与 admin 必须同源同版本，按 §1 构建；只替换 `governance` 容器镜像。
4. **API 目录与权限。** 用同版 admin 镜像 Job 依次执行 `sync-apis --dry-run`、`sync-apis`、
   `check`（服务镜像内没有 admin 二进制，不用 `kubectl exec`）。目录只新增一条
   `GET /api/v1/platform/components/{component}/logs/stream`，默认不绑定任何权限。
   随后对已核对的 Gov 库显式执行
   [`register-platform-component-log-permissions.sql`](../scripts/ops/sql/register-platform-component-log-permissions.sql)：
   单事务、路由必须已同步且唯一、冲突即 `RAISE EXCEPTION`，只授予
   `sys_roles.code='platform:admin' AND tenant_id=0 AND type='SYSTEM'`，不给租户角色与 API Key 补权。
   新安装与既有库都要执行这一步；不重跑首次种子、不运行 `SyncPermissions`、不批量打开 SYSTEM 模块。
5. **刷新策略并复测。** CLI/SQL 变更后滚动重启全部 Gov 实例刷新 Casbin，并重新登录取得新
   Token（旧 JWT 里的权限码是旧的）。`component:status:read` 与 `component:log:read` 互不替代，
   分别验证。

**已知缺陷（必须随发布说明登记）**：`ani_log.ingested_at` 由共享 ingest pipeline
`ani-component-log-normalize-v1` 写入；本环境未修改该 pipeline，reader 在运行时探测该字段，
缺失时增量窗口退化为按 `@timestamp`（事件时间），**事件时间早于窗口下界（30 秒）的迟到日志会漏读**。
补齐方式见 `deploy/component-logs/opensearch-ingest-fields.json` 与方案 §6.2.1，修改前须备份并
`_simulate` 验证，且 processor 必须放在重建 `ctx.ani_log` 之后；模板与现存索引 `_mapping`
必须同时更新，否则字段被静默丢弃。

**回滚**：先停止推广，按容器名/卷名精确恢复镜像与本次新增的 env/volumeMount/volume，
分别删除本次新增的 ConfigMap 与 Secret（它们不随 Deployment 回滚），并删除只读角色/账号。
API 目录行与权限 ID 默认保留；索引侧只移除本次新增的 processor，不删索引、不回退此前已修好的原文规范化。

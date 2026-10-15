# API / 角色 / 权限 操作清单（ani-governance 控制面）

面向集成方与运营：**如何通过 HTTP 接口管理 API 资源、编辑角色、给角色赋访问权限、更换用户角色**。
相关接口已经存在；权限点 PUT 的关联更新仍有实现缺陷，见 [当前风险](#permission-update-mask-risk)。其他掩码语义和生效时延见各节，文中源码引用用于核对现状。

> 初始生成时间：2026-09-21。2026-10-11 按固定基线 `79469c6` 静态核对并修正文档；本次未执行 HTTP、数据库或部署复验。文中旧 `file:line` 可能漂移，以实际源码为准。

---

## 0. 前置与鉴权

- **入口**：REST，默认监听 `7788`（`app/admin/service/configs/`）。管理面路径前缀 `/api/v1/admin/*`，认证路径 `/api/v1/auth/*`。
- **认证**：先登录拿 access token，后续请求带 `Authorization: Bearer <token>`。
  - 平台账密登录：`POST /api/v1/auth/platform/password/login`（免鉴权白名单，`rest_server.go:88-109`）。
- **鉴权链**（`rest_server.go:53-122`）：`recovery → requestid → logging → 审计 → validate → [auth.Server(token 校验 → 租户/模块闸门 → 组装 authz claims) → authz.Server(casbin)]`。
  - 注意顺序：**租户/模块白名单闸门在 token 校验内部、先于 casbin**（`pkg/middleware/auth/auth.go`）。
  - casbin 判定主体 = **令牌里的角色码**，资源 = 路由模板，方法 = HTTP 方法，域 = **令牌里的租户 ID**（不信任任何请求头，`pkg/middleware/auth/utils.go:37-43`）。
- **所需权限点**（bootstrap 种子，`sql/bootstrap/001_initial.sql:35-42`）：

| 操作 | 所需权限点 | 说明 |
|---|---|---|
| 管理 API 资源（增删改/同步） | `sys:platform_admin` | 平台管理员 `platform:admin`（`tenant_id=0`） |
| 创建/编辑/删除角色 | `sys:platform_admin` | 同上 |
| 创建/编辑/删除权限点、权限分组 | `sys:platform_admin` | 同上 |
| 改用户角色（`PUT /api/v1/admin/users/{id}`） | `sys:platform_admin` **或** `sys:tenant_manager` | 租户管理员可在本租户内管理用户 |
| 自改角色（`PUT /api/v1/admin/me`） | `sys:platform_admin` **或** `sys:tenant_manager` | 见 §6 注意事项 |

- **平台 vs 租户**：平台管理员（`tenant_id=0`）不进模块白名单；**租户用户**（`tenant_id>0`）每个请求都要过"套餐模块白名单"，判定失败一律 fail-closed（`internal/data/tenant_access_checker.go:44-116`）。
- **重要耦合**：任何新接口要能被授权与访问，必须①出现在内嵌 OpenAPI（`assets/openapi.yaml`）且②其 tag 能在 `pkg/constants/module_mapping.go` 的 `ServiceTagToBusinessModule` 映射到业务模块。否则 `admin sync-apis` 直接报错，且租户侧 `module not allowed`/`access denied`（`sql/bootstrap/catalog.go:42-44`、`tenant_access_checker.go:90-99`）。

---

## 1. API 资源管理

**数据模型**：`sys_apis`（Ent `Api`），列 `path/method/operation/module/module_description/description/business_module/scope/status`。
**真相源**：手写 proto → `make openapi` 生成内嵌 `openapi.yaml`。
**同步**：`POST /api/v1/admin/apis/sync`（`api_service.go:135`）与 CLI `admin sync-apis` 复用同一 `dbbootstrap.SyncAPIs`（`sql/bootstrap/catalog.go:55`）：按 `(method,path)` 幂等 UPSERT，**保留 ID / status / 权限关联，从不删除、从不授权**；OpenAPI 已下线的路由只报 `REVIEW absent from OpenAPI (retained)`。

| 方法 | 路径 | RPC | 说明 |
|---|---|---|---|
| GET | `/api/v1/admin/apis` | `ApiService.List` | 列表 |
| GET | `/api/v1/admin/apis/{id}` | `ApiService.Get` | 详情 |
| POST | `/api/v1/admin/apis` | `ApiService.Create` | 新建（手工登记路由） |
| PUT | `/api/v1/admin/apis/{id}` | `ApiService.Update` | 更新（带 `updateMask`） |
| DELETE | `/api/v1/admin/apis/{id}` | `ApiService.Delete` | 删除 |
| POST | `/api/v1/admin/apis/sync` | `ApiService.SyncApis` | 从内嵌 OpenAPI 同步 |
| GET | `/api/v1/admin/apis/walk-route` | `ApiService.GetWalkRouteData` | 运行时路由遍历（调试用） |

一次同步（dry-run）：
```bash
ANI_DATABASE_DSN='postgres://...' ./bin/admin sync-apis --dry-run
```
每次 API 写操作后服务端会 `authorizer.ResetPolicies`（`api_service.go:85/115/128/141`）。

> ⚠️ **`status` 不可经 HTTP 修改**：`ApiRepo` 的 Create/Update builder 不写 status（`api_repo.go:235-257,303-326`），请求体里的 `status` 被**静默忽略**；新建行默认 `scope=ADMIN,status=ON`。因此接口层面**不能启停 API**。

---

## 2. 角色管理（编辑角色）

**接口**（`api/protos/admin/service/v1/i_role.proto`）：

| 方法 | 路径 | RPC | 请求体 |
|---|---|---|---|
| GET | `/api/v1/admin/roles` | `List` | 分页参数 |
| GET | `/api/v1/admin/roles/{id}` | `Get` | — |
| POST | `/api/v1/admin/roles` | `Create` | `{ "data": Role }` |
| PUT | `/api/v1/admin/roles/{id}` | `Update` | `{ "data": Role, "updateMask": "..." }` |
| DELETE | `/api/v1/admin/roles/{id}` | `Delete` | — |

**`Role` 关键字段**（`permission/service/v1/role.proto`）：`name`、`code`（角色码，即 casbin 主体）、`status`、`type`（SYSTEM/TEMPLATE/TENANT）、`isProtected`、`dataScope`（行级，`ALL`/`SELECTED_UNITS` 等）、`permissions`（权限点 ID 数组）、`orgUnits`、`fieldPermissions`（列级黑名单：`{resource, hiddenFields}`）、`tenantId`。

**更新语义**（`internal/data/role_repo.go:551-699`，单事务）：
- 标量字段按 `updateMask` 更新；`allowMissing=true` 时不存在则新建。
- `permissions` / `orgUnits` / `fieldPermissions` 是**关联字段**（存 `sys_role_permissions`、`sys_role_org_units`、`sys_role_field_permissions`）——**只要出现在 mask 中即"整体替换"（含传空数组＝清空）；不在 mask 中则完全不动**。
- 实现会先把关联路径从 mask 黑名单移出（避免 `SET permissions=NULL`），并对关联数组先快照再替换（避免被字段掩码过滤静默清空）。
- 跨租户 `orgUnits` 由仓库侧拒绝（`role_org_unit_repo.go:38-59`）。

示例：把角色 12 改名为"运营只读"，并把权限整体替换为 [101,102,103]，清空自定义组织单元，设置字段隐藏：
```http
PUT /api/v1/admin/roles/12
Content-Type: application/json
Authorization: Bearer <platform-admin-token>

{
  "data": {
    "name": "运营只读",
    "permissions": [101, 102, 103],
    "orgUnits": [],
    "fieldPermissions": [
      { "resource": "User", "hiddenFields": ["email", "mobile"] }
    ]
  },
  "updateMask": "name,permissions,orgUnits,fieldPermissions"
}
```
- 仅改名字、**不动权限**：`"updateMask": "name"`，且 body 不必带 `permissions`（带了也不生效）。
- 清空权限：`"permissions": []` 且 `updateMask` 含 `permissions`。
- mask 路径大小写：走 protojson 时字段名会规范化为 snake_case（如 `orgUnits`→`org_units`），后端同时兼容 camelCase（`role_repo.go:597-604`）。

---

## 3. 权限点与"给角色赋访问权限"

**权限点模型**（`permission/service/v1/permission.proto`）：`code`（如 `sys:audit_logs`）、`name`、`groupId`、`status`、**`apiIds`**（关联接口）、**`menuIds`**（关联菜单）。`module` 在 **权限分组**（`PermissionGroup`）上，不在权限点上。权限点 `code` 的业务语义、命名、接口映射和目录校验见 [权限码规范](contracts/permission-code.md)。

**接口**：

| 方法 | 路径 | RPC |
|---|---|---|
| GET | `/api/v1/admin/permissions` | `PermissionService.List` |
| GET | `/api/v1/admin/permissions/{id}` | `Get` |
| POST | `/api/v1/admin/permissions` | `Create`（带 `data.apiIds`/`data.menuIds`） |
| PUT | `/api/v1/admin/permissions/{id}` | `Update` |
| DELETE | `/api/v1/admin/permissions/{id}` | `Delete` |
| POST | `/api/v1/admin/permissions/sync:perms` | `SyncPermissions` |
| GET/POST/PUT/DELETE | `/api/v1/admin/permission-groups[/{id}]` | 权限分组管理 |

**给角色赋访问权限有两条 HTTP 路径**：
1. **创建角色时赋权**：`POST /api/v1/admin/roles`，`data.permissions` 非空即写入（`role_repo.go:516-524`）。
2. **更新角色时赋权**：`PUT /api/v1/admin/roles/{id}`，`data.permissions=[权限点ID]` 且 `updateMask` 含 `permissions`（`role_repo.go:600,669-676` → `role_permission_repo.ReplacePermissions`）。

示例：给角色 12 赋予权限点 101/102：
```http
PUT /api/v1/admin/roles/12
{ "data": { "permissions": [101, 102] }, "updateMask": "permissions" }
```

新建权限点并挂接口：
```http
POST /api/v1/admin/permissions
{
  "data": {
    "name": "查看网络",
    "code": "net:vpc:read",
    "groupId": 1,
    "apiIds": [501, 502],
    "menuIds": [77]
  }
}
```

**生效**：权限点/Role 的写操作后均 `ResetPolicies`，内存 casbin 策略重建（`permission_service.go:224/254/267`、`role_service.go:168/234/248`）。策略形态 `p, roleCode, path, method, domain`（`pkg/authorizer/authorizer.go:117-136`），由 `authorizer_provider.go:55` 全量读"角色→权限→API"生成，跳过模板角色、按租户域隔离。

<a id="permission-update-mask-risk"></a>

> ⚠️ **当前权限点 PUT 的关联更新存在实现缺陷，回传完整数组也不能保证保留绑定。** [PermissionService.Update](../app/admin/service/internal/service/permission_service.go) 给非 nil mask 追加 `updated_by`；[PermissionRepo.Update](../app/admin/service/internal/data/permission_repo.go) 从 mask 剔除 `api_ids/menu_ids`，没有先快照关联载荷；随后 [Repository.UpdateOne](../pkg/localdeps/go-crud/entgo/repository.go) 调用 [FilterByFieldMask](../pkg/localdeps/go-utils/fieldmaskutil/fieldmaskutil.go)，清空 mask 外数组，再无条件调用 `AssignApis/AssignMenus`。因此携带 `updateMask` 时，即使回传完整数组也会进入空数组替换路径；省略 mask 和关联数组也会清空绑定。这是当前静态源码结论，本次未运行复现，尚未修复。它不符合仓库要求的关联 mask 合同，不能作为推荐更新方式；修复和行为验证需另行实施，参照 [RoleRepo](../app/admin/service/internal/data/role_repo.go) 的关联快照与事务处理。
> ⚠️ **分组改名/改 module 不触发 `ResetPolicies`**（`permission_group_service.go` 无该调用）——但策略生成链路**只读"角色→角色权限→API"，从不读 `permission_group.module`**（`authorizer_provider.go:59-119`），因此改分组 module 本就不影响授权、也无需刷新。
> ⚠️ **权限点要真正放行请求**，其 `apiIds` 必须指向 `sys_apis` 中已存在的 `(path, method)`；未登记的接口无法授权，且租户侧 fail-closed。

---

## 4. 更换用户角色

**主路径**（`api/protos/admin/service/v1/i_user.proto`）：

| 方法 | 路径 | RPC |
|---|---|---|
| GET | `/api/v1/admin/users` | `UserService.List` |
| GET | `/api/v1/admin/users/{id}` | `Get` |
| POST | `/api/v1/admin/users` | `Create`（可带 `data.roleIds`） |
| PUT | `/api/v1/admin/users/{id}` | `Update`（**改角色用这个**） |
| DELETE | `/api/v1/admin/users/{id}` | `Delete` |

`User` 有单数 `roleId` 与复数 `roleIds` 两个字段（`identity/service/v1/user.proto:108-115`）。

示例：把用户 42 的角色改为 [7,8]：
```http
PUT /api/v1/admin/users/42
{ "data": { "roleIds": [7, 8] }, "updateMask": "roleIds" }
```

**仓库层语义**（`internal/data/user_repo.go:618-741`，单事务）：mask 感知——无 mask 视为全量、有 mask 按路径匹配；`roleIds`+`roleId` 任一在 mask 内即视为要改角色（收集去重）；`delete-then-insert` 重建关联（按 `DefaultUserTenantRelationType` 走 `sys_user_roles` 或 membership 分支）；关联字段从标量 mask 分离。

> ⚠️ **服务层额外硬约束**（`user_service.go:492-503`）：`UserService.Update` **要求至少 1 个合法角色 ID，且不理会 mask**。因此：
> 1. **不能清空用户全部角色**（传空 → 400 `role_ids is required`）。
> 2. **只想改昵称等字段的部分更新，也必须回传合法角色 ID**，否则 400。
> ⚠️ **自改角色旁路**：`PUT /api/v1/admin/me`（`UserProfileService.UpdateUser` → `user_profile_service.go:81-96`）接收同一 `UpdateUserRequest`（含 `roleIds`），把目标强制盖成操作人自己后**直接调 `userRepo.Update`**，**绕过 `UserService` 的校验与合并逻辑**。即用户可改自己的角色集合——生产上需评估是否收紧。
> ⚠️ **角色变更不吊销令牌**：仅改状态或重置密码才吊销会话（`user_service.go:545-559`）。

**租户管理员初始角色**：`CreateTenantWithAdminUser`（`tenant_service.go:231-327`）在单事务内复制 `template:tenant:manager` 模板 → 建管理员 → 绑角色 → `ResetPolicies`。

---

## 5. 生效时延与多实例（重要）

| 变更 | 何时生效 | 依据 |
|---|---|---|
| 角色→权限 关联（赋权/收权） | **该实例立即**（`ResetPolicies` 重建内存 casbin） | `role_service.go:168` |
| 权限点→API 关联 | **该实例立即** | `permission_service.go:224` |
| API 目录同步 | **该实例立即** | `api_service.go:141` |
| 用户→角色 关联 | **本人当前会话要等令牌刷新/重新登录**（casbin 主体取自令牌角色） | `pkg/middleware/auth/utils.go:38` |

> ⚠️ **多副本**：`ResetPolicies` 只刷新**处理该次写请求的那个进程**的内存策略，无跨副本失效机制。`docs/deployment.md` 明确要求改完角色/权限后**重启所有实例**（或每个实例各自触发一次同步）。
> ⚠️ **ResetPolicies 的失败处理不一致**：**RoleService** 只记日志、不向上返回（`role_service.go:168-170/234-236/248-250`）→ 角色改了但策略可能未更新且调用方拿不到错误；**PermissionService / ApiService 会把错误返回给调用方**（`permission_service.go:224/254/267/506`、`api_service.go:85/115/128/141`）。两种情况 DB 写入均已提交，报错不回滚。
> **authz 引擎**：主服务只支持 `casbin`；[wiring_ent.go](../app/admin/service/cmd/server/wiring_ent.go) 在构造资源前拒绝缺配置、`noop` 或其他类型。库内仍有 noop 类型及其全放行语义，但它不构成主服务在开发或生产环境中的受支持配置。

---

## 6. 已知坑汇总（务必阅读）

1. **API `status` 不可经接口改**（Create/Update builder 不写 status）——不能启停 API。边界：若在 `updateMask` 里显式写 `status` 而请求体不给值，go-crud 会把它置为 NULL（仍无法置 ON/OFF）。
2. **权限点 PUT 的关联更新尚有实现缺陷**；携带 mask 时回传完整数组也不能保留绑定，见 §3 的静态调用链与未验证边界。
3. **改权限分组（module）不会、也不需要刷新策略**：`permission_group_service.go` 的 Update 不调 `ResetPolicies`，且策略生成链路只读"角色→角色权限→API"，**从不读 `permission_group.module`**（`authorizer_provider.go:59-119`）——不要把它当作"授权未生效"的原因。
4. **创建角色即可赋权**（`POST /roles` 带 `data.permissions`），非仅 PUT。
5. **`PUT /api/v1/admin/me` 可自改角色**，绕过 `UserService` 校验。
6. **`UserService.Update` 强制 ≥1 角色**，且不理会 mask → 部分更新也要带角色 ID；无法清空角色。
7. **两处关联仓库校验风格不一致**：`role_org_unit_repo.go` 写入前校验组织单元同租户（`:80-90`），而 `role_permission_repo.go:186-220` 的 `AssignPermissions` 无租户校验。注意这**不是跨租户风险**：`sys_permissions` 是**全局共享资源、无 `tenant_id`**（`ent/schema/permission.go:29-55`），本就不存在"权限点租户归属"；此条仅为一致性提醒。
8. **多副本需重启**才能保证策略一致。
9. **租户用户受套餐模块白名单约束**；查不到 API / 未归类 / 无套餐 → fail-closed（`tenant_access_checker.go:90-113`）。
10. **新下游服务必须先在 `ServiceTagToBusinessModule` 登记**，否则 OpenAPI 同步报错、租户 fail-closed。
11. **主服务强制 Casbin**；库内 noop 类型不能作为开发或生产启动配置。

---

## 7. 端到端最小示例（平台管理员）

```bash
BASE=http://<host>:7788
# 1) 登录拿 token（平台账密）
TOKEN=$(curl -s $BASE/api/v1/auth/platform/password/login \
  -H 'Content-Type: application/json' \
  -d '{"grant_type":"password","username":"admin","password":"<pw>"}' | jq -r '.access_token')
AUTH="Authorization: Bearer $TOKEN"

# 2) 同步 API 目录（从内嵌 OpenAPI）
curl -s -X POST $BASE/api/v1/admin/apis/sync -H "$AUTH"

# 3) 建权限点并挂接口
curl -s -X POST $BASE/api/v1/admin/permissions -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"data":{"name":"查看网络","code":"net:vpc:read","groupId":1,"apiIds":[501],"menuIds":[]}}'

# 4) 建角色并赋权（创建期赋权）
curl -s -X POST $BASE/api/v1/admin/roles -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"data":{"name":"网络只读","code":"tenant:net_reader","type":2,"permissions":[<permId>]}}'

# 5) 编辑角色：换权限（整体替换）
curl -s -X PUT $BASE/api/v1/admin/roles/<roleId> -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"data":{"permissions":[<permId1>,<permId2>]},"updateMask":"permissions"}'

# 6) 换用户角色（≥1 个角色，须回传角色 ID）
curl -s -X PUT $BASE/api/v1/admin/users/42 -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"data":{"roleIds":[<roleId>]},"updateMask":"roleIds"}'
```

> 请求体字段用 camelCase（protojson 标准，如 `updateMask`、`roleIds`、`apiIds`、`orgUnits`、`fieldPermissions`）。

---

## 8. 验证与排错

| 现象 | 排查 |
|---|---|
| `403 module not allowed` / `no subscription plan` | 租户未关联套餐、或该接口 `business_module` 不在套餐白名单；查 `sys_apis.business_module` + `sys_plan_modules` |
| `403 access denied`（租户） | `sys_apis` 无该 `(path,method)` 记录（fail-closed）；先 `apis/sync` |
| `403` 平台侧改角色/权限 | 当前角色无 `sys:platform_admin` |
| 改了权限但未生效 | 是否多副本未重启（策略仅本进程重建）？是否 Role 路径的 `ResetPolicies` 失败被吞（查日志）？ |
| `400 role_ids is required` | `UserService.Update` 要求 ≥1 角色；补上合法角色 ID |
| 权限点改完接口绑定没了 | 核对 §3 的关联更新缺陷；完整数组也可能被 mask 过滤。保留原关联快照并另行修复、验证，不能通过反复 PUT 试错恢复 |
| 改用户角色后本人仍旧权限 | 令牌里角色未刷新；重新登录/刷新令牌 |

---

## 附：关键源码索引

| 主题 | 文件 |
|---|---|
| API 实体/仓库/同步 | `internal/data/ent/schema/api.go`、`internal/data/api_repo.go`、`sql/bootstrap/catalog.go` |
| API 服务 | `internal/service/api_service.go` |
| 角色仓库（关联语义） | `internal/data/role_repo.go`、`role_permission_repo.go`、`role_org_unit_repo.go`、`role_field_permission_repo.go` |
| 权限仓库/服务 | `internal/data/permission_repo.go`、`internal/service/permission_service.go` |
| 用户角色仓库/服务 | `internal/data/user_repo.go`、`internal/service/user_service.go`、`internal/service/user_profile_service.go` |
| 鉴权引擎与策略 | `pkg/authorizer/authorizer.go`、`internal/data/authorizer_provider.go`、`pkg/middleware/auth/{auth.go,utils.go}` |
| 租户闸门 | `internal/data/tenant_access_checker.go` |
| 中间件与路由 | `internal/server/rest_server.go` |
| 种子与权限门控 | `sql/bootstrap/001_initial.sql` |

---

*2026-09-21 的初始静态核对不替代后续实现与运行验收。2026-10-11 仅修正已确认的文档矛盾；权限点关联更新、`/me` 直接调用仓库与 UserService 角色校验等现状未在本次修复或运行复现。真实登录、调用链路及具体套餐下的租户判定仍须按实际任务验收。*

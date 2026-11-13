# Gov 前端 HTTP 接口规范

本规范定义经 Gov 向前端提供的 HTTP JSON 接口的请求、响应和分页规则。新增和修改接口遵循本规范；存量接口按明确范围迁移，状态在[接口集成登记](../interface-integration-register.md)维护。VPC 已实现下述页码协议，可作为其他服务的参考，不代表所有存量接口已经统一。

Governance BFF 当前提供 `/api/v1/inference/...` 推理服务管理接口，包括普通及 GPU 推理的创建、列表/详情、操作查询、更新、启停/重启和删除。业务管理请求直接访问 Governance；Higress 仅承载 OpenAI 兼容推理数据面（例如 `/v1/completions`），不得把这些 `/api/v1/...` 路由加入 Higress。Inference BFF 的列表请求使用 `page`/`pageSize`，响应使用 `items`/`total`；`total` 在 JSON 中是十进制字符串。

Sandbox 管理从 Governance BFF 进入，Resource 不暴露公网 HTTP。接口为 `POST /api/v1/sandbox`、`GET /api/v1/sandbox`、`GET /api/v1/sandbox/{sandbox_id}`、`GET /api/v1/sandbox/operations/{operation_id}`，以及带 `:pause`、`:resume` 的 POST 和 DELETE。创建和生命周期请求使用 `data` 对象，返回异步受理的 `operation_id`/`resource_id`；列表返回 `items`/`total`。Sandbox 是独立资源，不要求其他实例。Gov 完成认证、权限、SANDBOX 套餐检查和可信租户映射；Resource 按租户和 Sandbox ID 检查归属。列表使用 `page/pageSize` 和默认值 `1/20`。

Sandbox 文件接口以工作盘 `/workspace` 为根，路径为相对路径。`GET /api/v1/sandbox/{sandbox_id}/files` 接收 `path`、`limit`（默认 100，最大 500）及 `cursor`，递归返回 `items`、`total`、`next_cursor`；`GET /api/v1/sandbox/{sandbox_id}/files/content?path=...` 返回 `content_base64`。`POST /api/v1/sandbox/{sandbox_id}/files` 使用 `data` 包含 `idempotency_key`、`path`、`content_base64`、`overwrite` 和可选 `expected_version`；`DELETE` 同一路径使用 `data` 的 `idempotency_key`、`path` 和可选版本。文件内容单次上限 1 MiB。变更响应有 `operation_id`、`state` 和 `reason`，可通过 `GET /api/v1/sandbox/{sandbox_id}/file-operations/{operation_id}` 查询。相同幂等键及参数重放返回原结果，不同内容冲突；`unknown` 不证明是否执行，不能自动重复提交新键。文件列表、读取、写入、删除和操作查询分别授权。

Sandbox Token：`POST /api/v1/sandbox/{sandbox_id}/tokens` 的 `data` 包含 `idempotency_key`、可选 `expires_in`（Go duration，默认 `15m`，最长 `1h`）及 `scopes`。当前 `files` 能访问文件接口，`exec` 能访问代码执行接口；需要签发者同时具备相应全部权限，且每次使用重新核对其当前权限。未指定时沿用旧默认 `connect`，但当前无对应可访问入口；`ports` 尚不可签发。凭证内容只在签发及有效重放响应返回，并提供 `token_id`、`expires_at`、`scopes`；同键同参数在有效期内重放原凭证，过期或撤销后重放冲突。`DELETE /api/v1/sandbox/{sandbox_id}/tokens/{token_id}` 撤销；Token 仅用于对应 Sandbox 的允许接口，不能管理生命周期或签发令牌。暂停、恢复或删除造成的版本变化使旧 Token 失效。

代码执行：`POST /api/v1/sandbox/{sandbox_id}/code-runs` 的 `data` 包含 `idempotency_key`、`language`、`code`、可选 `stdin`、`timeout_seconds` 和 `expected_version`；返回 `run_id`、状态和重放标记。通过 `GET /api/v1/sandbox/{sandbox_id}/code-runs/{run_id}` 查询状态、输出、退出码及截断标记。执行和查询分别授权；`unknown` 不表示远端进程已停止，也不能自动使用新键重试。

创建时可以从 Resource Image 目录选择镜像，传入 `image_id` 和 `image_scope`（`tenant` 或 `platform`），也可以填写已登记的租户或平台镜像 `image_reference`；两种方式不能同时提供。Resource 校验登记和租户权限，解析并冻结实际镜像引用和 digest。当前不能直接使用未登记的镜像地址。

Sandbox 文件接口以工作盘 `/workspace` 为根，路径为相对路径。`GET /api/v1/sandbox/{sandbox_id}/files` 接收 `path`、`limit`（默认 100，最大 500）及 `cursor`，递归返回 `items`、`total`、`next_cursor`；`GET /api/v1/sandbox/{sandbox_id}/files/content?path=...` 返回 `content_base64`。`POST /api/v1/sandbox/{sandbox_id}/files` 使用 `data` 包含 `idempotency_key`、`path`、`content_base64`、`overwrite` 和可选 `expected_version`；`DELETE` 同一路径使用 `data` 的 `idempotency_key`、`path` 和可选版本。文件内容单次上限 1 MiB。变更响应有 `operation_id`、`state` 和 `reason`，可通过 `GET /api/v1/sandbox/{sandbox_id}/file-operations/{operation_id}` 查询。相同幂等键及参数重放返回原结果，不同内容冲突；`unknown` 不证明是否执行，不能自动重复提交新键。文件列表、读取、写入、删除和操作查询分别授权。

Sandbox Token：`POST /api/v1/sandbox/{sandbox_id}/tokens` 的 `data` 包含 `idempotency_key`、可选 `expires_in`（Go duration，默认 `15m`，最长 `1h`）及 `scopes`。当前 `files` 能访问文件接口，`exec` 能访问代码执行接口；需要签发者同时具备相应全部权限，且每次使用重新核对其当前权限。未指定时沿用旧默认 `connect`，但当前无对应可访问入口；`ports` 尚不可签发。凭证内容只在签发及有效重放响应返回，并提供 `token_id`、`expires_at`、`scopes`；同键同参数在有效期内重放原凭证，过期或撤销后重放冲突。`DELETE /api/v1/sandbox/{sandbox_id}/tokens/{token_id}` 撤销；Token 仅用于对应 Sandbox 的允许接口，不能管理生命周期或签发令牌。暂停、恢复或删除造成的版本变化使旧 Token 失效。

代码执行：`POST /api/v1/sandbox/{sandbox_id}/code-runs` 的 `data` 包含 `idempotency_key`、`language`、`code`、可选 `stdin`、`timeout_seconds` 和 `expected_version`；返回 `run_id`、状态和重放标记。通过 `GET /api/v1/sandbox/{sandbox_id}/code-runs/{run_id}` 查询状态、输出、退出码及截断标记。执行和查询分别授权；`unknown` 不表示远端进程已停止，也不能自动使用新键重试。

创建时可以从 Resource Image 目录选择镜像，传入 `image_id` 和 `image_scope`（`tenant` 或 `platform`），也可以填写已登记的租户或平台镜像 `image_reference`；两种方式不能同时提供。Resource 校验登记和租户权限，解析并冻结实际镜像引用和 digest。当前不能直接使用未登记的镜像地址。

创建命令在 Gov 持久登记，投递超时保持原命令和幂等身份重试；未知结果不能仅凭超时清除。历史命令保留原摘要及旧引用，不在投递时改写。创建重放依照冻结意图匹配，冲突返回 `409 SANDBOX_IDEMPOTENCY_CONFLICT`；生命周期重放保留 Resource 返回标志。历史验证基于旧父实例模型，不代表独立创建已完成真实验收。

## 推理服务管理接口

当前注册的管理路由如下；创建、更新、删除、启动、停止和重启请求的业务字段直接位于 JSON 顶层，不使用 `data` 包装：

- `POST /api/v1/inference/services`：创建服务。
- `GET /api/v1/inference/services`：列表，可选 `status`、`capability`，分页使用 `page`/`pageSize`。
- `GET /api/v1/inference/services/{resource_id}`：详情。
- `POST /api/v1/inference/services/{resource_id}:update`：部分更新。
- `POST /api/v1/inference/services/{resource_id}:delete`：删除。
- `POST /api/v1/inference/services/{resource_id}:start`、`:stop`、`:restart`：生命周期操作。
- `GET /api/v1/inference/operations/{operation_id}`：操作详情。
- `GET /api/v1/inference/services/{resource_id}/operations`：服务操作列表，分页使用 `page`/`pageSize`。

列表筛选在分页前执行；服务状态、能力和发布状态由 Governance 根据 Inference 的受信读结果映射，不能由前端把旧的 `running` 值直接推断为可调用。详情和列表均由 Governance BFF 鉴权并按租户隔离。

## 1 请求约定

推理调用 Key 管理使用独立 `/api/v1/inference-keys`，其创建、轮换请求保持
`{data:{...}}`，不属于上述推理服务扁平请求迁移；列表各项和详情返回完整
`secret`。完整报文、分页上限及旧 Key 补存边界见[推理调用 Key 合同](inference-keys.md)。

- 查询参数放在 URL 中，创建和修改的业务数据使用 JSON 请求体。
- 业务字段及请求体结构以接口定义为准。模型创建、版本创建、导入、组合导入及推理创建、更新、删除、启动、停止、重启共 10 个接口不使用 `data` 包装；其他接口保持各自已声明结构。
- 本批次只迁移请求体，响应结构不变。推理路径中的 `resource_id` 为资源身份；请求体可省略该字段，若提供则必须与路径一致。`idempotency_key`、`expected_generation`、`update_mask` 的含义保持不变。
- 公共分页参数统一使用 `page`、`pageSize`；筛选和排序参数由各接口声明。

## 2 详情响应

查询单个资源时，直接返回资源对象，不增加资源名称、`data` 或 `code/data` 包装。

例如，查询 VPC 详情：

```http
GET /api/v1/networks/vpcs/vpc_0123456789abcdef0123456789abcdef
```

```json
{
  "id": "vpc_0123456789abcdef0123456789abcdef",
  "name": "研发网络",
  "cidr": "10.20.0.0/16"
}
```

示例仅展示部分业务字段。前端直接读取响应体中的 `id`、`name`，无需按 `vpc` 或 `data` 嵌套取出资源对象。

## 3 列表响应与分页

除[平台集群运维的正式原生游标协议](cluster-operations-http.md)外，列表统一返回 `items` 和 `total`：

- `items`：当前页的数据数组。
- `total`：当前调用者有权查看、且符合筛选条件的总条数，不是当前页条数。JSON 中使用十进制字符串。

例如，每页两条，首次列表请求就直接查询 VPC 列表的第三页：

```http
GET /api/v1/networks/vpcs?page=3&pageSize=2
```

```json
{
  "items": [
    {
      "id": "vpc_1123456789abcdef0123456789abcdef",
      "name": "测试网络",
      "cidr": "10.30.0.0/16"
    }
  ],
  "total": "5"
}
```

| 参数 | 含义 | 默认值 | 合法范围 |
| --- | --- | --- | --- |
| `page` | 页码，从第一页开始 | `1` | VPC 为 `1` 到 `4294967295` 的整数 |
| `pageSize` | 每页条数 | `20` | `1` 到 `100` 的整数 |

没有传入某个分页参数时，独立使用该参数的默认值；例如只传 `page=3` 时，`pageSize` 仍为 `20`。参数使用单个规范十进制正整数；显式传入零、负数、空值、前导零、小数、重复参数或超出范围的值时，返回 `400`，不静默修正。未声明的查询参数同样返回 `400`。

列表支持直接跳到任意合法页，不要求先请求前面的页。分页参数只使用 `page/pageSize`，响应直接返回 `items/total`。

没有数据时，`items` 返回 `[]`，`total` 返回 `"0"`。页码超出最后一页时，`items` 仍为 `[]`，`total` 保留实际匹配总数。

列表必须先按确定的顺序排序，再执行分页；主排序值相同时使用唯一键兜底。列表查询与计数采用相同的可信租户、权限和业务筛选范围，数据库读取使用同一个只读快照。连续请求之间数据可能变化，分页不表示结果已被冻结。

VPC 的具体规则：`name` 去首尾空白后区分大小写精确匹配；`state` 按接口定义筛选，不传时排除 `deleted`。排序固定为 `created_at DESC, vpc_id DESC`，不开放任意排序字段。

## 4 创建修改和删除

写入接口需要返回单个资源时，采用与详情相同的对象结构；没有业务结果需要返回时，返回 `{}`。使用 `204` 的接口不返回响应体。

部分更新按接口声明的更新字段处理：没有指定更新的字段保持原值；清空字段必须显式表达，不能把省略字段当成清空。

异步受理必须保留任务或操作信息。请求受理成功，不代表创建、删除或其他业务操作已经完成。

## 5 错误响应

通过 HTTP 状态码区分请求成功或失败，不把失败统一包装成 `200`。

错误体包含错误码、业务原因和说明。例如：

```http
HTTP/1.1 400 Bad Request
```

```json
{
  "code": 400,
  "reason": "INVALID_NETWORK_REQUEST",
  "message": "分页参数不合法",
  "metadata": {
    "request_id": "req-example"
  }
}
```

前端根据 HTTP 状态和 `reason` 处理错误，`message` 用于展示说明，不依赖其固定文案判断业务分支。

响应提供 `X-Request-ID` 或 `metadata.request_id` 时，前端保留该值用于排查；它们不是每个错误都必定存在的业务字段。

本规范有一处显式例外：平台组件日志流
`GET /api/v1/admin/platform/components/{component}/logs/stream` 为兼容旧版前端，错误体使用旧格式
`{"code": <字符串>, "message": <字符串>, "request_id": <字符串>}`，不使用数字 `code` 与 `reason`。
该例外仅限这一条路由，其他接口仍按本节执行。登记见
[接口集成登记](../interface-integration-register.md) 的 `PLAT-COMP-02`。

## 6 公共字段与特殊结果

- 64 位整数在 JSON 中使用十进制字符串，例如 `"total": "3"`、`"version": "1"`；其他字段类型以接口定义为准。
- 时间使用 RFC 3339 字符串，例如 `"2026-10-15T08:30:00Z"`。
- 字段名和枚举值按接口定义使用，不自行转换大小写或把所有 ID 强制转换成数字。
- 普通详情不增加包装，普通列表不增加外层 `data`。成功响应不统一添加 `code/message/data` 外壳。
- 有业务含义的组合结果保留完整结构，例如资源与操作、凭证与密钥、制品与下载地址。列表需要的 `observation` 等元数据也应保留。
- 导航树、权限快照、容量快照、统计和日志按各自业务结构返回，不因为内部有数组就强行改成普通分页列表。

## 7 VPC 参考实现与其他服务接入

现有 VPC 已实现 Gov HTTP → Resource → PostgreSQL 的页码查询链路，可作为其他服务的参考。各 owner 按相同规则接入，不要求使用相同的 Go 接口，也不以建立跨仓公共分页框架为前提。

以下链接固定到已核对的源码版本：Gov `ba7db4c`、Resource `98a5348`，避免引用到其他分支的实现。

| 参考内容 | 代码位置 | owner 应参考的规则 |
| --- | --- | --- |
| HTTP 路由与请求响应定义 | Gov：[i_network.proto](https://github.com/zhangzhe-ctrl/ani-governance/blob/ba7db4c30b6ee8a2a8046a1adb1591bf6f01e2a4/api/protos/admin/service/v1/i_network.proto)、[vpc.proto](https://github.com/zhangzhe-ctrl/ani-governance/blob/ba7db4c30b6ee8a2a8046a1adb1591bf6f01e2a4/api/protos/catalog/service/v1/vpc.proto)；Resource：[network.proto](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/98a534804231c7842acaa080fe1ebec066d921b2/api/network/v1/network.proto) | 详情直接返回对象；列表使用 `page/pageSize` 和 `items/total`；分页字段保留是否传入的信息 |
| 参数校验与签名 | Gov：[network_policy.go](https://github.com/zhangzhe-ctrl/ani-governance/blob/ba7db4c30b6ee8a2a8046a1adb1591bf6f01e2a4/pkg/middleware/auth/network_policy.go)、[vpc_page.go](https://github.com/zhangzhe-ctrl/ani-governance/blob/ba7db4c30b6ee8a2a8046a1adb1591bf6f01e2a4/app/admin/service/internal/service/vpc_page.go)、[AK 客户端](https://github.com/zhangzhe-ctrl/ani-governance/blob/ba7db4c30b6ee8a2a8046a1adb1591bf6f01e2a4/scripts/ops/aksk_vpc_client.py)；Resource：[page.go](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/98a534804231c7842acaa080fe1ebec066d921b2/internal/biz/network/page.go) | 参数单值与范围校验、独立默认值、API Key 查询参数验签，以及简单的 `PageWindow{Limit, Offset}` 窗口计算 |
| 身份传递与结果映射 | Gov：[network_service.go](https://github.com/zhangzhe-ctrl/ani-governance/blob/ba7db4c30b6ee8a2a8046a1adb1591bf6f01e2a4/app/admin/service/internal/service/network_service.go)、[network_client.go](https://github.com/zhangzhe-ctrl/ani-governance/blob/ba7db4c30b6ee8a2a8046a1adb1591bf6f01e2a4/app/admin/service/internal/data/network_client.go)；Resource：[network.go](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/98a534804231c7842acaa080fe1ebec066d921b2/internal/service/network/network.go)、[list.go](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/98a534804231c7842acaa080fe1ebec066d921b2/internal/biz/network/list.go) | 可信身份和租户贯穿调用链；保留领域筛选与结果字段；空数组和总数按前端合同返回 |
| 数据查询与计数 | Resource：[vpcs.sql](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/98a534804231c7842acaa080fe1ebec066d921b2/internal/data/network/queries/vpcs.sql)、[postgres.go](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/98a534804231c7842acaa080fe1ebec066d921b2/internal/data/network/postgres.go) | 确定排序后直接执行 `LIMIT/OFFSET`；Count/List 使用相同租户与筛选范围，并在同一个 Repeatable Read 只读事务内执行 |
| 定向测试 | Gov：[参数与签名测试](https://github.com/zhangzhe-ctrl/ani-governance/blob/ba7db4c30b6ee8a2a8046a1adb1591bf6f01e2a4/pkg/middleware/auth/network_policy_test.go)、[Service 测试](https://github.com/zhangzhe-ctrl/ani-governance/blob/ba7db4c30b6ee8a2a8046a1adb1591bf6f01e2a4/app/admin/service/internal/service/network_service_test.go)、[HTTP 联调测试](https://github.com/zhangzhe-ctrl/ani-governance/blob/ba7db4c30b6ee8a2a8046a1adb1591bf6f01e2a4/app/admin/service/internal/service/network_joint_http_test.go) 的 `vpc_pages`；Resource：[page_test.go](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/98a534804231c7842acaa080fe1ebec066d921b2/internal/biz/network/page_test.go)、[vpc_page_integration_test.go](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/98a534804231c7842acaa080fe1ebec066d921b2/internal/data/network/vpc_page_integration_test.go) | 直接查询第三页、参数默认值与非法值、空页和超界页、总数、稳定排序、筛选、租户权限及计数快照 |

Gov 通过 [go.mod](https://github.com/zhangzhe-ctrl/ani-governance/blob/ba7db4c30b6ee8a2a8046a1adb1591bf6f01e2a4/go.mod) 固定正式 Resource 模块版本。各 owner 参考以上分层、查询和测试方式，业务筛选、可见性和授权条件由所属领域维护。参数解析和窗口计算保持清晰边界，后续有实际重复再提取公共代码。

## 平台基础网络与当前网络关联实例

VPC/Subnet 的 Get/List 返回当前租户私有网络和已登记的平台网络。公共对象不暴露租户或 Provider 身份，新增 `owner_scope`（`tenant` / `platform`）、`can_manage` 和 `can_use`。平台资源 `can_manage=false`；租户对平台 VPC/Subnet 的管理请求由 Resource 返回 `PermissionDenied`，Gov 映射为 HTTP 403。平台资源的 `can_use` 只表达新建资源的接入策略，不能用原生 Ready、已有 IP 或观察时间替代它。

`GET /api/v1/networks/vpcs` 保持 `page/pageSize`；`GET /api/v1/networks/subnets` 保持 `limit/cursor`。二者增加可选 `can_use`、`can_manage` 查询参数：缺省不筛选，`true` 和 `false` 均为等值筛选，同时传入时使用 AND。仅接受单个规范 `true/false`；空值、数字、其他大小写、重复值和 camelCase 别名均返回 400。筛选、排序和总数由 Resource 在同一目录范围完成，Gov 不拼接分页、不重新判定资源能力。

平台目录关闭发布后不再出现在 List；持有已登记产品 ID 的调用者仍可按原 `network:vpc:get` / `network:subnet:get` 权限 Get。已有实例详情据其保存的 ID 查询，不要求网络仍在 List。平台 VPC 不适用的 `base_connectivity` / `public_egress` 对象保持缺省，前端显示“平台托管”；平台 Subnet 的 `used_ip_range` 保持 optional absent，不能展示为全局占用为零。

`GET /api/v1/networks/instances` 读取当前租户尚未 released 的 Attachment 摘要；它不列出还没有 Attachment 的已受理实例。要求 `network:instance:list` 及 NETWORK 套餐模块，支持现行用户 JWT / AK 鉴权。后续进入 VM/容器详情仍执行各自原有 Get 权限；本列表不额外查询或求交这些权限。

| 请求字段 | 约定 |
| --- | --- |
| `vpc_id`、`subnet_id` | 至少一个必填；同时提供时确认父子关系并缩小到指定 Subnet；私有 ID 不可跨租户读取 |
| `page`、`pageSize` | 独立默认 `1/20`，页码为正 uint32，页大小 `1..100`；沿用规范单值十进制校验 |

响应为 `items/total`，`total` 是十进制字符串。每行包含 `consumer_type`、`consumer_id`、`consumer_name`、`consumer_state`、`subnet_id`、`subnet_name`、`slot`、`attachment_id`、`attachment_state`、`attachment_reason`、`observation_stale`、`observed_at` 和 `addresses`。地址项为 `{nic_name,address}`；消费者状态和 Attachment 状态各自显示。类型按 Resource 返回值使用 `container_instance` / `virtual_machine` / `sandbox` / `unknown`，未知消费者仍保留占用行。

已知网络没有本租户占用时返回 `items=[]`、`total="0"`。无权限返回 403，前端显示“无权查看网络关联实例”，不能当作空表。Gov 在映射之前逐行校验 Resource 返回的内部 `tenant_id`，公开行不重复暴露它。

新路由先执行 `admin sync-apis --dry-run` 和经批准的目录同步，再显式运行[精确权限登记 SQL](../../scripts/ops/sql/register-network-instance-permission.sql)。登记不自动授予已有角色、套餐或 API Key。正式消息与路由分别见[目录 Proto](../../api/protos/catalog/service/v1/network_instances.proto)和 [Network BFF Proto](../../api/protos/admin/service/v1/i_network.proto)。

## 模型版本筛选与操作历史（2026-10-21）

- `GET /api/v1/models/{model_id}/versions?page=1&pageSize=20&status=ready`
  的 `status` 可省略；接受 `pending`、`importing`、`ready`、`error`，兼容界面别名
  `downloading`→`importing`、`failed`→`error`。Model 在数据库分页前筛选，返回
  canonical 状态和相同筛选条件的 `total`；Governance 不进行本地筛选或计数。
- `GET /api/v1/models/{model_id}/operations?page=1&pageSize=20` 返回 `items/total`，
  项字段为 `id`、`model_id`、`model_version_id`、`action`、`actor`、`result`、
  `before_state`、`after_state`、`error_class`、`request_id`、`created_at`、`task_id`。
  时间为 RFC 3339，`total` 为十进制字符串。按创建时间、唯一 ID 倒序。
- 这两个 Model 查询的页码为正 int32，页大小为 `1..100`，缺省 `1/20`，不接受其他查询
  参数、重复参数、空值、前导零或查询请求体。操作历史不支持 `status` 筛选。
- `result=accepted` 表示异步导入已受理，不能显示成导入成功；`succeeded` 和
  `failed` 表示该条已记录动作的结果。任务当前结果仍以任务查询为准。历史记录
  仅覆盖 Model 实际产生并能关联到模型的审计事件，不补造未记录的删除等动作。
- 两个入口均经过 Governance 用户认证、`model:service:read` 和 `MODEL` 套餐闸门。
  模型归属由 Model 验证；前端不能指定租户或操作人。

模型详情 `GET /api/v1/models/{model_id}` 的 HTTP 响应直接返回 Model 资源，
没有 `model` 外层；内部 RPC 的 GetModelResponse 保持原形状。

## ModelDev 当前租户执行环境

`POST /api/v1/modeldev/environment:ensure` 显式开通当前租户环境；请求必须为
`application/json` 空对象 `{}`（最多1024字节并紧接EOF），不允许query，且恰有一个
规范非零UUID `Idempotency-Key`。`GET /api/v1/modeldev/environment` 无body/query，
仅返回持久观察。两接口返回 `{"environment": <view>}`，采用snake_case，state为
`PROVISIONING/READY/BLOCKED/NEEDS_REVIEW`，phase为
`NAMESPACE/RESOURCES/EXPERIMENT/VERIFY/PUBLISH/COMPLETE`。未确认的namespace_uid、
experiment_id、binding_digest与last_verified_at省略。只有PROVISIONING返回202和
`Retry-After: 2`；其他存在状态返回200，无登记Get返回404
`ENVIRONMENT_NOT_REGISTERED`。Get不表示本次live健康探测，BLOCKED不表示开通成功。

两接口要求当前tenant用户、当前有效角色的ALL数据范围、独立
`modeldev:manage_environment`/`modeldev:view_environment`权限及对应精确API grant，
仍服从既有MODEL套餐/租户闸门。相同key重试重新授权并读取最新结果，不缓存首次状态。
浏览器不能指定tenant/namespace/cluster/Experiment或覆盖出站x-ani-*。
Ensure/Get不进入执行acceptance、quota或可靠CREATE链。
Prepare/Finalize的严格环境拒绝只输出有限`metadata.environment_reason`，不转发上游message。

During the one-time ModelDev environment maintenance window, startup setting
`ANI_MODELDEV_CREATE_PAUSED=true` returns HTTP 503 with reason
`MODELDEV_CREATE_MAINTENANCE` only for the ModelDev Create action. Existing
authentication and tenant-user restrictions still run; Ensure/Get, queries,
Stop and quota release do not use that gate.


## 平台集群运维

`/api/v1/admin/cluster-ops` 的十一条接口采用[集群运维 HTTP 合同](cluster-operations-http.md)。
该域资源列表正式使用 Kubernetes 原生游标，不提供 `page/pageSize/total`，也不扫描全集补总数；
其他业务列表继续遵守各自已声明的分页规范。写请求保留 `data` 包装，响应保留类型化变更、
删除及扩缩容结果；YAML、Secret、UID/RV、错误和单 Pod SSE 的具体字段与行为以该合同为准。

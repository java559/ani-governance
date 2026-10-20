# 业务服务接入指南（Service Integration）

本指南说明"把一个业务微服务的 API 接入 governance，让租户用户经治理中心拿到结果"的装配步骤，并索引现有客户端实现。新增或修改调用先遵守 [服务间调用规范](contracts/service-to-service-calls.md)；本文的当前身份差异和 Network 示例不是新规范的例外。存量证书、接收端和调用者按 [迁移边界](contracts/service-to-service-calls.md#current-differences) 独立接线、联调与验收。参照实现：

- **Network（Resource 的网络领域，可选依赖）**：[i_network.proto](../api/protos/admin/service/v1/i_network.proto) + [network_client.go](../app/admin/service/internal/data/network_client.go) + [network_service.go](../app/admin/service/internal/service/network_service.go)。地址为空则跳过客户端装配。当前租户面 25 方法（包括 NET-25 VPC CIDR 预设）及 JWT/AK 合同见 [接口登记](interface-integration-register.md)，部署与运行是否可用以实际验收为准。
- **Model / ModelDev（共享 MODEL 模块）**：[model_client.go](../app/admin/service/internal/data/model_client.go) 接入模型目录、版本和导入生命周期；[modeldev_client.go](../app/admin/service/internal/data/modeldev_client.go) 与 [CPU-P01 接口登记](interface-integration-register.md) 记录训练受理、投递、查询和受管目录操作。Model BFF 的恢复范围以 [GOV-MODEL-INF 接口登记](interface-integration-register.md#gov-model-inf-20261012model--inference-bff-接入) 为准。
- **Accelerator / Resource 实例**：[GPU owner 接入指南](contracts/gpu-owner-integration-guide.md) 记录 typed context、冻结命令及退款 listener 等专项合同。Model 出站客户端使用唯一 DNS SAN `ani-governance`；Accelerator 与内部退款 listener 的身份要求分别按各自合同维护。

Inference 管理接入本期整体下线：普通和 GPU 推理的创建、查询、更新、启停、删除及操作查询均不提供，不保留静态 API 模块或 fork 依赖，也不配置 Inference 出站证书和地址。后续重新对接另行确认正式上游合同与发布版本。

下游在目标环境可能缺席时可参考 Network 的可选装配模式；是否属于启动必需依赖由本次接入合同决定。旧 Model 的强制依赖装配仅是历史先例，重接时须重新确定授权范围和启动条件。

## 执行环境边界

本仓库的开发执行环境约定见 [AGENTS.md](../AGENTS.md)（本地编辑、阅读和 Git 操作；生成、构建、测试、运行与部署按任务要求在指定环境执行）。本指南各步骤只标注操作类型，不绑定具体机器：

| 操作 | 命令 |
|---|---|
| Proto / Go API 生成 | `make api` / `tools/bin/gow api` |
| 仅编译 | `make build_only` |
| 依赖与模块 | `go mod tidy`（固定版本模块按 §0.3 核对来源） |
| 权限登记 SQL | 按 §8 的专项脚本在目标数据库执行 |
| 运行与链路验收 | 按 §10 验收清单在运行环境执行 |

## 0. 前置约束（不满足直接停止）

1. **authz 必须是 casbin**。[wiring_ent.go](../app/admin/service/cmd/server/wiring_ent.go) 启动守卫在 `authz.type != "casbin"` 时拒绝启动（noop 不作为受支持配置）。
2. **租户闸门 fail-closed**。新路由未在 Api 表登记 `(path, method)` 前，所有租户请求都会被拒绝。不存在"先上后补"。
3. **下游 Go API 模块依赖**。出站客户端 import 下游仓库的生成包（如 `github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1`）。上游模块在 GitHub 公开可取时直接 `go get` 固定版本伪版本即可，不需要本地 file-GOPROXY 交付；注意 `goproxy.cn` 对这些私有模块可能返回 `not found`，`GOPROXY` 需把 `proxy.golang.org` 放在前面或直接走 `direct`。不要自建 file-GOPROXY 打包交付——历史上那种交付 zip 丢点文件（`.gitignore` 等），导致校验和与 go.sum 不一致。
4. Module 枚举值必须已存在：确认 `api/gen/go/identity/service/v1/` 里有 `Module_<你的模块>`；没有则先改 identity proto 并重新生成（这本身也是一次 §4 的生成任务）。

## 1. 骨架生成（可选）

```bash
scripts/dev/new-service-scaffold.sh \
  -n Network -d catalog -m NETWORK \
  -r github.com/zhangzhe-ctrl/ani-resource-service/api/network/v1 \
  -p '/api/v1/networks/vpcs/{vpc_id}' -s ani-network-service
```

参数：`-n` 服务名（PascalCase）、`-d` 领域包段（proto package 与 gen 目录）、`-m` identityV1 模块枚举后缀、`-r` 下游 Go API import 路径、`-p` HTTP 路由、`-s` 下游 mTLS ServerName（DNS SAN）。

脚本只写入四个骨架文件（领域 proto、BFF proto、出站 client、入站 service），已存在的文件拒绝覆盖，末尾打印手工装配清单。它不执行任何生成/构建。 骨架沿用现有 Network 模式；新调用仍须按统一规范核对并落实证书身份、完整 RPC 允许清单和可信业务范围，文件生成不代表已满足合同。

## 2. 领域 Proto（`api/protos/<domain>/service/v1/<entity>.proto`）

- `package <domain>.service.v1`；源领域 proto **不带 HTTP 注解**（合同纪律，路由只出现在 admin BFF）。
- 只声明治理侧真正需要转发的消息与边界。Network 当前包括 VPC/Subnet、EIP/SNAT、LB 与操作查询；公开 DTO 排除可信租户/actor 和平台配置，保留 Resource 的必要输入、分页及业务结果。新增接口范围仍以任务授权为准。
- 字段用 `json_name` + gnostic property 描述约束，保证 OpenAPI 语义完整。

## 3. 治理 BFF Proxy Proto（`api/protos/admin/service/v1/i_<entity>.proto`）

- `package admin.service.v1`，import 领域 proto，声明 `service <Name>Service` 与 `google.api.http` 路由。
- 路径遵守已接受合同；`/api/v1` 不是强制前缀，但同族服务保持一致。
- gnostic operation 描述里写明鉴权、订阅、拒绝语义与错误码（`i_network.proto` 是模板）。

## 4. 生成

```bash
make api
```

生成物落在 `api/gen/go/`，不手改。此后 `go.mod` 需要下游 API 依赖（`go mod tidy`；固定版本按 §0.3 核对来源）。

## 5. 出站 mTLS 客户端（`internal/data/<entity>_client.go`）

<a id="current-client-contracts"></a>

以下是当前源码的客户端配置索引，保留独立迁移前的实际行为；新增和修改遵循统一服务间调用规范。各客户端均要求受管目的地址、CA/证书/密钥、TLS 1.3、明确超时及出站 metadata 重建。现有身份与环境变量有所不同：

| 当前链路 | 客户端身份与服务端名称 | 受管环境变量 |
|---|---|---|
| Gov → Network | 客户端唯一 DNS SAN `ani-governance`，不允许其他类型 SAN；服务端固定 `ani-network-service`，复核字面 DNS SAN 并拒绝通配符 | `ANI_NETWORK_ADDR/CA/CERT/KEY/TIMEOUT` |
| Gov → Image | 与 Network 使用相同 Resource 身份校验，服务端固定 `ani-network-service` | `ANI_IMAGE_ADDR/CA/CERT/KEY/TIMEOUT` |
| Gov → ModelDev | 客户端唯一 DNS SAN 为 `ani-governance`，不允许其他类型 SAN；服务端固定 `ani-modeldev-service` 并额外检查字面 DNS SAN | `ANI_MODELDEV_ADDR/CA/CERT/KEY/TIMEOUT` |
| Gov → Observability | 客户端唯一 DNS SAN `ani-governance`，服务端固定 `ani-observability-service` 并拒绝通配符；固定 6s，继承父取消 | `ANI_OBSERVABILITY_GRPC_ADDR`、`ANI_OBSERVABILITY_CA_FILE`、`ANI_OBSERVABILITY_CLIENT_CERT_FILE`、`ANI_OBSERVABILITY_CLIENT_KEY_FILE` |
| Gov → Accelerator | 客户端唯一 URI SAN 为 `spiffe://ani.internal/service/ani-governance`；服务端名称来自受管配置 | `ANI_ACCELERATOR_ADDR/CA/CERT/KEY/TIMEOUT/SERVER_NAME` |
| Gov → Resource Instance / Compute | 两个独立领域客户端共享一条现有受管 Resource mTLS 连接，唯一客户端 DNS SAN `ani-governance`，沿用固定服务器 DNS SAN `ani-network-service`；唯一 cleanup 由装配持有 | 共享既有 `ANI_INSTANCE_ADDR/CA/CERT/KEY`，不增加目的地址回退 |

Compute 管理、目录与公共解析由 `ComputeClient` 调用正式 `compute.v1.ComputePresetService`，
不再通过 InstanceService。平台管理 metadata 只携带 platform scope、真实平台用户 actor 和 request ID；
租户目录/解析整体重建 tenant、actor、request ID，不转发公网认证或身份 header。
公共 `ResolveComputePreset` 支持 CONTAINER_INSTANCE、TRAINING、INFERENCE，默认3s并遵守更短的父期限；
客户端使用正式 `compute.ValidateSelection` 校验版本、资源内容及摘要，并核对返回的 preset ID。
它只提供只读候选能力，不新增训练/微调创建字段，也不装配推理消费服务。
容器目录显式注入 ComputeCatalog 且固定 CONTAINER_INSTANCE；容器新建只调用原 ResolveCreateSpec 一次，
由 Res 内部共享 compute resolver，再完成镜像/运行环境等完整校验，原幂等和冻结投递链保持。

以各客户端源码和专项合同为准：[Network](../app/admin/service/internal/data/network_client.go)、[Image](../app/admin/service/internal/data/image_client.go)、[ModelDev](../app/admin/service/internal/data/modeldev_client.go) / [配置](../app/admin/service/internal/data/modeldev_client_config.go)、[Accelerator](../app/admin/service/internal/data/accelerator_client.go)。此表描述现有实现；统一身份规则或迁移须另有明确合同，不以修改本文代替运行代码迁移。

以下逐条说明 **Network 示例**：

- **fail-closed 构造**：address 与正 timeout 必填；CA 必须能解析（无证书即报错）；客户端证书须有效且允许 clientAuth，唯一 DNS SAN 为 `ani-governance`，不混入 URI、IP、email 或其他 DNS 身份。
- **传输**：`tls.Config{MinVersion: TLS13}`；`ServerName` 固定为 `ani-network-service`，默认 CA/用途/有效期验证后再复核字面 SAN，拒绝通配符。Network/Image 均关闭 gRPC service config 和未审查的自动 retry。
- **环境变量**：`ANI_NETWORK_ADDR/CA/CERT/KEY/TIMEOUT`；timeout 默认 3s，解析失败要返回错误，不吞。
- **身份 header 重建**（见 §9 的 Network/Image 契约表）：用 `metadata.NewOutgoingContext` 整体重建，**绝不 append 入站/公网传来的身份 header**。
- **期限与错误**：继承上游取消并设置本次 deadline；保留原 RPC status 与领域 details，不按连接态把 `DeadlineExceeded` 改为 `Unavailable`。公网错误仍由 Service 做安全映射，超时不证明下游没有执行。
- 构造期一次性读证书是已知限制：cert-manager 续期后需重启 Pod 重载，见 [governance-mtls README](../scripts/deploy/governance-mtls/README.md)。

## 6. 入站代理服务（`internal/service/<entity>_service.go`）

照 [network_service.go](../app/admin/service/internal/service/network_service.go) 的不变量：

- 主体只来自 `auth.PrincipalFromContext`，要求非零租户与主体 ID，并通过 `Principal.Actor()` 校验身份类型。用户与 API Key 分别生成用户或机器 actor，不能把机器的 `user_id=0` 误当未登录，也不能伪造用户 ID 绕过守卫。**不信任请求传来的租户、操作人或平台角色。**
- 租户映射经 `ResourceTenantResolver`（[tenant_resource.go](../app/admin/service/internal/data/tenant_resource.go)）把治理中心 uint32 主键换为下游 resource tenant UUID；id==0 拒绝。该接口已在 package service 定义，复用，勿重复声明。
- 请求**严格按接口白名单校验**：Network 的 [HTTP Filter](../pkg/middleware/auth/network_policy.go) 在路由绑定前检查原始 path/query/body，拒绝未知、重复和歧义输入；`vpc_id` 仅在指定 DTO/列表允许，公网租户/actor 字段始终不作为可信身份。签名绑定原始 body 与规范 method/path/query，详情和写入不接受 query，见 [AK/SK 合同](contracts/aksk-signature.md)。
- Network 上游错误映射：`NotFound→404`、`InvalidArgument→400`、幂等/冲突→409、版本/生命周期/占用前置失败→412、`DeadlineExceeded→504`、依赖不可用→503；受信下游身份拒绝也按依赖失败处理，不混成公网登录失败。保留 Resource `ErrorInfo` 的领域 reason，对外消息和日志不转发下游私有详情；公网 401/403 由 Governance 认证、权限和套餐闸门产生。
- nil 请求、nil 响应、nil 列表元素都要处理，不吞错。

## 7. 装配（四处锚点，手写，勿用 Wire）

`make register` 只覆盖标准构造函数；config/client/cleanup/额外依赖手工补齐。

1. **[wiring_ent.go](../app/admin/service/cmd/server/wiring_ent.go)** `── register:service ──` 锚点后追加四连（Network 先例）：

```go
	<entity>Config, err := data.<Name>ConfigFromEnv()
	if err != nil {
		rollback()
		return nil, nil, err
	}
	// 强制依赖型接入用四连;可选依赖型(如下游可能缺席)参照 networkClient 的
	// "地址为空则跳过装配"写法。
	<entity>Client, cleanup<Name>, err := data.New<Name>Client(<entity>Config)
	if err != nil {
		rollback()
		return nil, nil, err
	}
	cleanups = append(cleanups, cleanup<Name>)
	<entity>Service := service.New<Name>Service(<entity>Client, tenantRepo)
```

   任何一步失败先 `rollback()` 再返回；cleanup 创建成功立即注册。
2. **[rest_server.go](../app/admin/service/internal/server/rest_server.go)** `register:param` 锚点后追加形参 `<entity>Service *service.<Name>Service,`。
3. **rest_server.go** `register:route` 锚点后追加 `adminV1.Register<Name>ServiceHTTPServer(srv, <entity>Service)`。
4. **wiring_ent.go** `register:rest-arg` 锚点后把 `<entity>Service` 加进 `NewRestServer` 实参。

漏接由编译器在调用处报错——所以 §4 生成后先 `make build_only` 一轮。

## 8. 模块与权限登记

1. **[module_mapping.go](../pkg/constants/module_mapping.go)**：`"<Name>Service": identityV1.Module_<MODULE>`。漏登记会导致 `business_module` 为 UNSPECIFIED，租户白名单直接拒绝。当前 `MODEL` 由 `ModelDevService` 承接，`modulesWithoutService` 为空。仅当某模块确实没有任何服务承接且该状态属于明确接入范围时，才在 [module_mapping_test.go](../pkg/constants/module_mapping_test.go) 的 `modulesWithoutService` 记录有理由的豁免；不得为已有服务添加豁免。
2. **Api 表登记**：新增接口明确登记 `(path, method)`、权限和套餐关系，参照 [bootstrap-network-access.sql](../scripts/ops/sql/bootstrap-network-access.sql) 写专项脚本；先 `admin sync-apis --dry-run` 再显式同步。[SyncAPIs](../sql/bootstrap/catalog.go) 按 `(method,path)` 幂等 UPSERT，保留既有 ID、status 和权限关联，不删除、不自动授权；源码 SQL、目录登记与运行租户角色授权分别验证，具体步骤见 [API 权限运维](api-role-permission-ops.md)。
3. 套餐：目标租户的 Plan 必须包含该 Module，否则闸门 403。

## 9. 出站身份与报文（按链路区分）

**Gov → Network/Image** 当前使用以下三个 header：

| Header | 值 | 来源 |
|---|---|---|
| `x-ani-tenant-id` | resource tenant UUID | `ResourceTenantID(ctx, principal.TenantID)` |
| `x-ani-actor` | `governance:user:<uid>` 或 `governance:access-key:<key-id>` | 认证后的 `Principal.Actor()`；用户与机器身份独立 |
| `x-ani-request-id` | 新 UUID | 每次调用重建 |

这两条链路的下游只信任已认证 mTLS 对端（治理中心）传来的上述 header；公网或重复 header 一律不可信。治理中心侧每次出站都整体重建，不从入站请求继承。Network 的 CreateVPC/CreateSubnet Attribution 使用同一次生成的 request ID，以及相同 actor 和固定 `ani-governance` direct caller；接收方拒绝冲突。Network 的旧 `x-ani-operator` 仅从可信 actor 推导，不覆盖 request ID。业务 `idempotency_key` 沿用原意图，与追踪 ID 分开。

Resource 接收方的完整方法、租户范围、超时和幂等统一见 [调用登记](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/82c4b2d6fb7ca6fec15ddcb3cf236e5a0ca9a971/docs/specs/governance-calls.md)，需随接收方源码一起交付；任务分支发布不代表已合入 main 或部署。其他现有链路使用各自合同：

- **Gov → ModelDev**：Resolve/AcceptExecution/ApplyCloseIntent 重建上述三项；查询另加完整 RPC 的 `x-ani-authorized-method` 和 `x-ani-data-scope=tenant-all`，受管目录操作复用该委派并按其幂等合同绑定 request ID。见 [query client](../app/admin/service/internal/data/modeldev_query_client.go)、[management client](../app/admin/service/internal/data/modeldev_management_client.go) 与接口登记。
- **Gov → Observability**：复用实例选租户和 `instance:read` 授权，先调用 Res 的 `ResolveInstanceMetricScope`（2s），核对返回范围后仅调用 `MonitoringService/QueryRange` 或 `QueryLatest`（6s）。三项 metadata 每次重建为单值，actor 沿用 `governance:user:<canonical uint32 ID>`，request ID 为新非零规范 UUID。实例入口当前只接受既有用户身份；不因此扩大 API Key 或跨租户管理员范围。新客户端完全未配置时禁用查询，部分配置或证书不合法时启动失败。见 [client](../app/admin/service/internal/data/observability_client.go)、[TLS](../app/admin/service/internal/data/observability_client_security.go) 与 [OBS-07](interface-integration-register.md#obs-07容器实例-cpu内存查询)。
- **Gov → Accelerator**：委托 RPC 从 typed context 派生 `x-ani-action`（RPC 短名）、`x-ani-actor-type`、`x-ani-actor-id`，租户调用再加 `x-ani-tenant-id`；`SyncGpuUsage` 使用原持久投影并重建空 metadata。不能照搬 Network actor header。见 [acceleratorMetadata](../app/admin/service/internal/data/accelerator_client.go) 和 GPU owner 指南。

[scripts/deploy/governance-mtls/](../scripts/deploy/governance-mtls/) 提供 Network/Image DNS 身份材料的 cert-manager 模板（`clientAuth` EKU、Issuer `ani-internal-ca`、有效期 72 小时/提前 24 小时续期；以 [certificate.yaml](../scripts/deploy/governance-mtls/certificate.yaml) 为准）。新下游的 Certificate、SAN、信任根、部署补丁和环境变量须按其具体合同准备；现有 URI 身份不能直接套用该 DNS 证书。

## 10. 验收清单

编译与生成只是起点；隔离、鉴权、持久化、故障恢复**不能由 HTTP 200 或静态检查代替**。以下以 Network 租户入口为例；其他链路按其平台/租户、用户/机器范围及专项合同选择用例。各批实际执行结果见 [接口登记](interface-integration-register.md)，历史 Network 验收不替代当前新增能力：

- [ ] 未登录 → 401
- [ ] 平台租户（tid=0）→ 403 `TENANT_REQUIRED`
- [ ] 登录但无权限 → 403
- [ ] 套餐未含 Module → 403（闸门 fail-closed）
- [ ] 未登记 `(path, method)` → 403（证明 fail-closed 生效后再登记放行）
- [ ] 未知/重复/空查询参数 → 400
- [ ] 下游进程停止 → 503（传输不可达，不是 504）
- [ ] 下游可达但挂起超过 timeout → 504
- [ ] 错误 CA / 错误 SAN 的客户端证书 → 启动即失败（fail-closed 构造）
- [ ] 跨租户隔离：租户 A 的登录态拿不到租户 B 的数据

未执行的项在交付说明里写 `not_verified`，不得用"构建通过"替代。

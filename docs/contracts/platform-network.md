# 平台网络接入合同

本批接通平台用户经 Governance 查询、配置 Public/Intranet 地址池、VLAN 二层网络及其依赖。领域生命周期和持久化由 Resource 拥有；本服务只承担公网身份、动作权限、DTO 与受信委托。租户 VPC/Subnet/EIP/SNAT/LB 合同保持原值。

## 入口与权限

HTTP 前缀为 `/api/v1/admin/networks`。完整方法、路由和权限以 [平台网络策略](../../pkg/middleware/auth/platform_network_policy.go) 为准，报文以 [领域 DTO](../../api/protos/catalog/service/v1/platform_network.proto) 与 [BFF 路由](../../api/protos/admin/service/v1/i_platform_network.proto) 为准。共 35 个 RPC，包括平台 EIP 查询、详情、受保护释放 3 个，平台默认 VPC 出口 4 个、虚拟交换机 4 个、Public/Intranet 池各 7 个、VLAN 4 个、网关 4 个、能力和平台 operation 各 1 个。

仅已认证的平台用户（可信用户 Principal、平台身份 tenant_id=0）可访问，逐请求检查动作权限与当前 Casbin 策略；令牌中的 IsPlatformAdmin 展示标志不授权。普通租户、租户 API Key、缺失权限或已撤销策略一律拒绝。平台配置接口不借用租户 UUID。平台 EIP 管理使用独立平台 RPC；释放的真实租户由 Resource 从持久 EIP 记录解析，不接受客户端指定所有者。NETWORK 套餐检查继续只约束租户业务入口，平台配置不用虚构租户或套餐。

先执行 `admin sync-apis --dry-run`、审阅并同步目录，再显式执行 [权限目录步骤](../../sql/data/20261012_platform_network_permissions.sql)；新增交换机路由再执行[交换机权限增量](../../sql/data/20261021_virtual_switch_permissions.sql)，平台默认 VPC 出口执行[出口权限增量](../../sql/data/20261021_default_egress_permissions.sql)。该步骤只建立 API 与权限关联，不分配角色、启用租户套餐或改变已有 OFF 权限。平台用户的动作权限通过既有角色授权运维另行赋予，刷新策略按 [权限运维](../api-role-permission-ops.md) 执行；本次源码接入不自动修改业务数据库。

## 请求、分页与响应

新增写入请求使用 `{data: {...}}`；资源 ID 来自路由，body 中冲突 ID 拒绝。verification、allocation、default 是领域命令，沿用幂等键及 expected_version，并非任意字段 PATCH。公网 body/header 不允许断言 tenant、actor、管理员或调用服务。

虚拟交换机只需 `{data:{name,idempotency_key}}` 创建；名称遵守 Node label-key 名称规则并禁止点号，无区域、AZ 或简介。未绑定详情不要求 cluster_id、Provider UID 或通用 PlatformResource.configuration；首次创建二层网络才绑定集群。交换机 create/get/list/delete 各有独立 `network:platform:virtual-switch:<action>` 权限。列表采用 `page/pageSize`，独立默认 1/20，范围分别为 1～2147483647 和 1～100，支持直接跳页；响应直接为 `{items,total}`，int64 total 是十进制字符串。显式零、前导零、重复、超限、cursor/limit 或未知查询参数返回 400。

VLAN、网关、Public/Intranet 池列表沿用存量 limit/cursor 协议：默认 20、范围 1～100；显式 0、超限、非规范十进制、重复、未知或旧窗口参数返回 400。允许 name/state 筛选，保留 Resource 的稳定 `(created_at DESC,id DESC)` 排序和签名游标，换资源/筛选或篡改游标拒绝。每一页重新认证和授权。返回 `{items,next_cursor,total}`，total 为同平台集群、同资源种类、同筛选条件的全部匹配数，忽略窗口；Resource 的 count/page 使用同一只读快照。int64 在 HTTP JSON 中为十进制字符串。空结果 total 为字符串 0；末页没有非空 next_cursor。

NodeFacts 网卡发现、独立设备登记/查询和旧设备退出入口已移除，不再提供节点网卡事实或独立设备 DTO。管理员手工提交网卡名创建二层网络，Resource 受理后由 KCN 接管；交换机节点只读身份和状态不再包含采集的 MAC。

创建二层网络 v2 在一个 data 内提交 name、cluster_id、virtual_switch_id、device_name、vlan_id、idempotency_key 及可选 description。旧 device_id 保留第 3 字段用于兼容重放，与三个 v2 字段互斥；不允许混合版本或不完整 v2 输入，前端不调用 Adopt 再串联创建。旧 key 重放、新请求准入和实际绑定由 Resource 负责。

单资源、平台 operation 和能力摘要使用 HTTP response_body 直接返回对象，沿用 VPC 的响应风格。状态、操作类型和 Public 模式为小写产品值，JSON 字段为 snake_case；异步受理返回资源快照和 last_operation_id，不表示数据面就绪。平台管理员可见平台拓扑，普通租户保持原脱敏范围。下游错误按现有 Network 映射，保留内部诊断并隐藏公开响应中的私有传输细节。

## 内部调用与验证边界

沿用一个 Resource 内部业务 mTLS listener 和现有 Network 客户端连接。DNS 身份仍为 ani-governance → ani-network-service，TLS 1.3、双向证书和精确登记 SAN；不增加明文入口或自动重试。平台客户端重建单值 x-ani-scope=platform、x-ani-actor=governance:user:<id> 和规范非零 x-ani-request-id，完全不发送 x-ani-tenant-id；公网身份 header 不透传。

Resource 逐完整 RPC 检查允许清单，平台方法只接受上述平台委托，并由入站 adapter 安装 PlatformAdministrator。租户委托不能调用平台方法，平台委托不能调用租户/Image 方法；混合 scope、重复或非法 metadata、API Key actor、错误证书均拒绝。vpc-read 仍只开放 GetVPC。

当前联调复用 [Network 真实链路夹具](../../app/admin/service/internal/service/network_joint_http_test.go) 的 platform_network 场景：真实 Gov HTTP/JWT/Casbin → mTLS → Resource 用例/PostgreSQL/worker，仅 KC/Envoy 和实例 owner 外部边界受控。它不证明真实网卡接管、交换机/VLAN 或公网转发；源码生成、受控联调和真实环境验收分别报告。

平台默认 VPC 出口使用独立 `network.v1.PlatformDefaultEgressService`，Gov 提供 `/api/v1/admin/networks/default-vpc-egresses` 的 POST 和 `/{egress_id}` 的 GET/DELETE。创建只接收 `{data:{name,pool_id,idempotency_key}}`，三个动作分别要求 `network:platform:default-egress:create/get/delete`。没有租户、任意 VPC、namespace 或 UID 写入字段；真实 cluster/default VPC 身份由 Resource 配置冻结并只读返回。DefaultVPCEgress DTO 保留独立 ID、operation/phase、pending_unknown、EIP/Snat 真实 Provider 身份、target_generation 和 optional applied_enabled；未知应用状态不从 desired_enabled 补全。该平台出口不借用租户 VPC 行或租户绑定入口，旧原生出口不自动纳管，删除须等待产品引用和真实外部结果核清。

## 平台 EIP 管理

`GET /api/v1/admin/networks/eips`、`GET /api/v1/admin/networks/eips/{eip_id}`、`DELETE /api/v1/admin/networks/eips/{eip_id}` 分别要求 `network:platform:eip:list/get/release`，仅平台用户 JWT 可调用。权限目录增量见 [EIP 权限](../../sql/data/20261027_platform_eip_permissions.sql)。

列表使用 `page/pageSize`（默认 1/20，上限 100），支持 `tenant_id/name/address/pool_id/state/scope` 服务端组合筛选，返回 `{items,total}`。`tenant_id` 是 Gov 的正整数租户主键，由 BFF 换成 Resource UUID；不指定则查询当前配置集群的全租户 EIP。响应保留 `resource_tenant_id`、映射出的 `tenant_id/tenant_name`、地址池、绑定对象、状态与观测时间；映射缺失时仍保留原始所有者 UUID。详情与释放直接返回对象。系统管理的 Intranet EIP 只读，不能由管理员独立释放；绑定中的 EIP 和未完成操作仍由 Resource 原有删除链拒绝。没有强制解绑、越过 claim 或直接删库的入口。

## 当前平台默认出口

`GET /api/v1/admin/networks/default-vpc-egress` 使用 `network:platform:default-egress:get`，不接受查询参数或客户端指定的租户、集群、VPC。返回部署固定 VPC 的当前未退役出口详情；未配置返回 404/RESOURCE_NOT_FOUND，删除进行中的记录继续可见。既有创建与删除路径保持不变。

平台 EIP 列表包括平台默认出口持有的地址。其 `managed_by=system`、`scope=public`、租户字段为空/零，`binding_target.kind=platform_default_egress`，目标 ID 为出口配置 ID。系统地址使用持久 `eip_id`，只通过所属出口的删除流程回收；不能从普通 EIP 释放入口单独释放。租户筛选排除此类无租户地址，分页和 total 包含相同过滤范围。

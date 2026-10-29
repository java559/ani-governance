# 功能对接与接口风格改动登记

总体状态：**进行中，未结项**。最后更新：2026-10-13。

本文件覆盖所有后续对接功能。每次用户对接一个新功能时，AI 先排查真实调用链，将涉及的接口、请求和响应、鉴权条件、现有问题追加到对应功能组。先累计登记，等用户指定某个批次后，再统一修改该批接口的路径、字段和响应风格；不能因排查或登记就自动改接口风格。

后续始终维护本文件，不按功能或对话另起登记文件。复用接口引用已有编号，避免重复登记；新功能使用自己的稳定编号。用户明确要求的即时功能修复可单独实施并记录，不必等待风格批次。尚未提供的旧项目格式标为“待指定”；只有用户确认全部改动完成后，整体登记才结项。

本文件的功能索引和前端交接维护当前合同；带日期的实施与验收段落保留对应批次事实。后续增量与早期说明冲突时，同步当前入口，并保留历史结果的源码、环境和未验证边界。

前端交接入口：[用户、租户及两类管理员页面接入说明](#frontend-management-handoff)。该章节可整段转发，包含第一批接口、请求示例、权限、响应和暂缓项；沿用本文已有接口编号。

## 功能登记索引

| 功能组 | 接口编号 | 排查进度 | 风格改动批次 |
| --- | --- | --- | --- |
| 登录、登出及登录后初始化 | AUTH-01～AUTH-08；相关可选接口 AUTH-09～AUTH-14 | 已排查；已按单独要求去掉登录图形验证码 | 待用户分批，尚未统一改风格 |
| 用户管理 | ACCOUNT-01～ACCOUNT-09；复用 ROLE-01/02、AUTH-01～AUTH-08 | 前端交接见专节；管理侧更新须携带目标租户和有效角色；部分接口暂缓 | 待指定 |
| 租户管理 | TENANT-01～TENANT-09 | 已核对 HTTP、Service、Repository 和首次种子；本轮未做运行验收 | 待指定 |
| 租户管理员管理 | 复用 TENANT-06、ACCOUNT-01～ACCOUNT-09、ROLE-01～ROLE-05、AUTH-01/12；AUTH-15～AUTH-18（自助 /me） | 复用用户和租户角色；用户明确不做主管理员移交 | 待指定 |
| 套餐管理 | PLAN-01～PLAN-14；复用 TENANT-04/08 | 模块限制已接线；配额按 quota_code 核对配置、账本和执行点，GPU 占额见专项合同 | 待指定 |
| 平台运营账号管理 | 复用 ACCOUNT-01～ACCOUNT-09、ROLE-01～ROLE-05、AUTH-02/12；AUTH-15～AUTH-18；SESSION-01～SESSION-04；PERM-01～PERM-06、PERMGROUP-01～PERMGROUP-05 | 支持多个平台账号；运营/只读角色模板待 API 接入后再加；自助会话与权限点/权限组接口本轮补登，缺口见 MGMT-08～MGMT-15 | 待指定 |
| API Key / AK-SK | AK-01～AK-07；AK-IDEM-01（创建幂等，2026-10-10）；AK-ROLE-INFO-01（响应回填角色名称/描述，2026-10-13） | 签名、角色绑定、加密与可信审计已完成，真实 VPC 闭环 PASS；创建幂等已在 ani-system 真实环境实测（HTTP 20/20 PASS + 8 并发 ALL PASS）；AK-ROLE-INFO-01 已在 ani-system 实测 14/14 PASS 且既有三脚本回归不退化 | AKSK-VPC-20260922 已完成本批；幂等与角色信息回填属向后兼容增强，未并入风格批次 |
| VPC 详情查询的机器调用 | NET-01；复用 AK-* | 必要 vpc-read 接收已整合，双 actor 与真实 mTLS 查询 PASS | AKSK-VPC-20260922 已完成本批 |
| Network 租户面读写对接 | NET-01～NET-25；复用 AK-*、AUTH-* | 当前 25 方法支持 JWT/AK；NET-01～24 的历史三条主链验收和 NET-25 增量结果分批记录 | 当前合同见下方增量与 [AK/SK 合同](contracts/aksk-signature.md)；真实环境按批次验收 |
| Gov → Resource 调用规范收敛 | S2S-RES-01；复用 NET-01～NET-25 与既有 Image 11 RPC | Network/Image 调用端与 Resource 接收端已修改；隔离真实业务链路通过，生产验收未执行 | 传输与可信上下文即时修复，见下方结果；不调整 HTTP 风格 |
| Gov → Resource Sandbox 生命周期 | SANDBOX-01～08 | 当前需求为独立 Sandbox：源码调整、跨仓兼容和验收结果见 Resource 唯一状态；旧父实例模型的历史证据不能代表新模型已验收 | Gov 需固定支持独立创建的 Resource 正式版本；升级前执行切换门禁，整体验收待完成 |
| 通用配额、GPU 实例与 Inference 接入 | QUOTA-01～03；INF-01/02；QUOTA-LAB-01～04；复用 PLAN-11～14、TENANT-04/08 | 保留 ACC 目录与 Resource 实例执行/释放合同；Inference 管理由 Governance BFF 接入并通过受信 mTLS 客户端投递；旧模拟 owner 入口退役 | 当前合同见 [GPU owner 指南](contracts/gpu-owner-integration-guide.md)；源码、部署与真实 GPU 验收分别记录 |
| ModelDev 训练、微调及原登记恢复 | MODELDEV-01～MODELDEV-08、MODELDEV-12；服务间调用登记 S2S-MODELDEV-CPUP01-01 | 单一 typed 受理/查询及当前授权恢复已实现；原登记 Resume 的 HTTP/JWT/PG/mTLS 边界 GREEN，真实 provider Resume 与完整微调/GPU分别验收；历史 CPU-P01 结果保留原范围 | v2.1，进行中 |
| 存储接入（独立盘与对象只读） | STORAGE-01～STORAGE-10 | 已实现并在 kcn 新环境完成正/负向验证；对象列表是本登记中唯一分页合同例外 | 待指定；前端合同与交付文档另行提供 |
| 平台组件健康快照（BOSS 组件状态页） | PLAT-COMP-01 | 已实现并在 kcn 新环境实测（快照、鉴权、降级、缓存）；前端对接文档见 `local-remote/平台健康/` | 待指定 |
| 平台组件日志流（BOSS 日志窗口） | PLAT-COMP-02；复用 PLAT-COMP-01 组件清单 | 已实现并在 kcn 新环境直连 30085 实测（SSE 六字段、8 秒生命周期、参数严格校验、鉴权、并发配额、客户端取消）；AUTH-02「平台身份但无日志能力」未取到有效证据 | 待指定；旧格式错误体为本登记唯一例外，见下方小节 |

## 风格改动批次

本批 **AKSK-VPC-20260922** 已由用户明确指定，覆盖 AK-01～07、AK-ISSUE-01～05、NET-01/NET-ISSUE-01：实现、远端定向测试、空库真实链路及必要负向验收均 PASS。证据见 [运行记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/aksk-vpc-20260922/README.md)。单个批次完成不代表整体登记结束，其他功能风格仍待指定。

历史 **GOV-RESOURCE-20260922** 对接了 NET-01～NET-11 共 11 个租户接口；“17/17”是该批场景数，不是 Resource 租户 RPC 全量覆盖。该批 AK 实际只支持 GetVPC，其余接口的机器访问在 2026-10-09 增补。历史计划、证据及部署保持各自时点；当前覆盖与响应合同以下方本轮小节和源码为准。

<a id="vpc-page-r01"></a>

## 2026-10-15：VPC R01 页码参考实现

范围仅 NET-01/NET-02 的 VPC 详情与列表及必要调用链；其他服务/资源未迁移。规范为 [前端 HTTP 接口规范](contracts/frontend-http-api.md)，存量游标规则移至[独立合同](contracts/cursor-pagination-legacy.md)，LIST-CURSOR-01/02 的历史记录保留。

基线为 Resource 远端默认分支 main `c826b2b4d3ce7746b3caa49548e104e07d6e3b8e`、Gov 远端 main `686547543339bff4926505d3c7829415d404786f`；两仓任务分支均为 `codex/vpc-page-r01-20261015`，原 Resource Image dirty 工作区及 Gov 原 main 保留。两份忽略目录草稿显式携带，SHA256 分别为 `38c4437bdc3fbc0cfe2713a80fdf1ffafb058529f4a695e352391a912ce8f67d`（frontend-api-contract）和 `8f1ad640f658bca34ef317b7872413ad512291d6e4dfc8d322345e8bc1141f98`（pagination-page-unification）。

Resource 已先推送任务提交 `98a534804231c7842acaa080fe1ebec066d921b2`；Fedora 使用全新模块缓存从正式源下载 `v0.0.0-20261015073834-98a534804231`，核实 `Origin.Hash` 等于该 SHA，模块 Sum=`h1:3XeKt6k05ix9XYq37yItZDnLW/6ppvMRLuQFDDv5OTI=`、GoModSum=`h1:DXqtxWJ+IEu9gZIgGVHTbFONYQc9hcuaeXOUzmY5Yjw=`。Gov 固定此正式依赖，无 replace；下载内容的 Proto、SQL、业务和测试文件与已测 Resource 快照逐字节相同。

本批 `controlled_complete`：真实 Gov HTTP → JWT/AK + Redis 会话 + Casbin/租户/套餐 → mTLS Resource → PostgreSQL 已贯通。首次列表请求即 page=3/pageSize=2，按相同 created_at 的唯一 ID 兜底取正确两条记录，total="7"；独立租户仅见自己的 1 条。默认/单字段缺省、name/state、空数组/字符串 total、末页/越界、非法/重复/溢出/旧参数、AK 参数篡改、跨租户详情和撤权 403 均有实际断言。7 条目标 VPC 与另一租户 VPC 均经 HTTP 创建、查询、产品删除；未绕过业务查询、权限或持久化。只有 KC/Envoy/实例 owner 等外部 Provider 边界使用既有 fixture。

执行主机仅 SSH Fedora，Go 1.26.9、Buf 1.60.0、sqlc 1.31.1，任务根目录 `/home/chabking/workspace/ani-resource-service-runs/vpc-page-r01-01a13e73`：

| 必要验证 | 结果与具名范围 |
| --- | --- |
| Resource 正式生成、来源/边界与定向 vet | pass/0：`make config sql`、`scripts/verify-source`、`scripts/verify-boundaries`，`go vet ./internal/biz/network ./internal/service/network ./internal/data/network` |
| Resource 窗口与真实 PG | pass/0：`TestResolvePage`，`TestVPCPageRandomAccessAndScope`、`TestVPCPageCountAndListShareSnapshot`、`TestVPCListTotalAcrossPagesAndFilters`；命令详见该模块 docs/execution/status.md |
| Gov 完整生成与 AK 脚本 | pass/0：`make api`（11 模板及工作树保护），`python3 -m unittest scripts/tests/test_aksk_vpc_client.py`（9 项） |
| Gov 参数/签名 | pass/0：`TestVPCPageHTTPParameters`、`TestNetworkAllTenantOperationsSignAndReplayBody`、`TestNetworkHTTPRejectsAmbiguityBeforeHandler`、`TestNetworkSignatureBindsMethodPathQueryAndBody`、`TestNetworkSignatureRejectsMethodTampering` |
| Gov 映射、可信 Client 与详情 | pass/0：`TestVPCPageDefaultsAndValidationBeforeCall`、`TestNetworkListTotalsPassThroughTrustedScope`、`TestNetworkTrustedScope`、`TestNetworkTrustedKeyPrincipal`、`TestNetworkClient*`、`TestDetailHTTPResponsesReturnResourceObjects/VPC` |
| 模块与交付静态检查 | pass/0：`go mod verify`；最终 2269 个非文档输入文件与已测快照逐项相同，变更合同的相对链接/锚点检查通过 |
| 真实跨仓 HTTP | pass/0：`RESOURCE_NETWORK_TEST_BINARY=<固定二进制> NETWORK_JOINT_TEST_RUN='^TestNetworkJointHTTP/vpc_pages$' ./scripts/network-joint-integration`；子用例 35.87s、总用例 49.91s，Resource helper exit 0，产品与基础设施清理 pass |

定向 Go 检查统一 `-mod=readonly -count=1`，具名命令/退出码见任务目录 `resource-s02.log`、`resource-vet.log`、`gov-generate-s03-cacheproxy.log`、`gov-checks-02.sh` / `gov-checks-s03.log`、`gov-joint-s04.sh` / `gov-joint-s04.log`，原始日志不提交。按用户要求未运行全量测试、全仓 race、压测、合并或部署。

Resource 验证源码归档 SHA256 为 `53614b8a7ad143fada142fc98df198311efc2724050d18bde8725b2ae6507c2d`（input/resource-s02.tar.gz）；Gov 定向输入为 `535487614198580fe031c1f186695b207ea8e78b4a40c059d15b124908002173`（input/governance-s03.tar.gz），生成/模块固定后的正式模块及生成物逐项回传核验；最终联调输入为 `e4111f8300c485562936811e661fbb3f854371e9e2266192e871df0c46e9d4fb`（input/governance-s04.tar.gz）。s04 除联调 fixture 和文档外与已通过检查的 s03 逐文件相同，fixture gofmt 回传后检查本地未漂移；最终文档补充不改变已测代码。Resource 二进制 SHA256=`9194e6e454606152756623cb43ad8466689d0d208ab8ccbb8b8661ad30916200`。

保留失败边界：初次 Gov 生成因缺已固定 Inference 模块停止；下载后通过。再次生成遇 Go 代理 EOF，离线模式也不支持生成器的版本查询；改用已下载官方模块缓存的 file proxy 后完整入口通过，未改模板/版本/保护检查。第一次 HTTP 联调在最新认证合同要求 sid/application 的 fixture 初始化处失败（exit 1，未进入分页），修正测试令牌为真实 Authenticator/Redis 会话后只重跑 VPC HTTP；此前通过的单位检查未重复。

Gov VPC 无参数现为 page=1/pageSize=20，旧窗口一律 400；Resource 旧 RPC 单独保留；详情继续直接返回 VPC 对象，操作组合结果未削减。前端、消费者实际升级、生产部署和真实 Provider/数据面验收 `not_verified`，没有自动合并或部署。

<a id="s2s-res-01"></a>

## 2026-10-11：Gov → Resource 调用规范收敛（S2S-RES-01）

范围仅 Governance 与 Resource 的现有 Network/Image 用户业务委托，沿用 NET-01～NET-25 和既有 Image 11 RPC；不新增方法、调用者、配额接口或公开 HTTP 字段。唯一传输规范见 [服务间调用规范](contracts/service-to-service-calls.md)，调用端配置见 [接入指南](service-integration.md#current-client-contracts)，完整接收方方法见 [Resource 调用登记](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/82c4b2d6fb7ca6fec15ddcb3cf236e5a0ca9a971/docs/specs/governance-calls.md)。接收端提交 `82c4b2d` 已推送任务分支并建立 [Resource PR #6](https://github.com/zhangzhe-ctrl/ani-resource-service/pull/6)，尚需仓库要求的批准审查，不能把任务分支发布写成已合入 main。

Governance 的两个客户端统一 TLS 1.3、唯一客户端 DNS SAN `ani-governance`、字面服务器 DNS SAN `ani-network-service`，拒绝混合客户端身份和通配符服务器身份，关闭未经审查的 retry/service config。请求只从可信 Principal 和持久租户映射重建 metadata；CreateVPC/CreateSubnet 的 Attribution 与同次 metadata 一致。追踪 ID 不替代业务幂等键，取消/期限向下游传播，保留原 RPC code、reason 和 details。

Resource 的 full、governance、vpc-read 三种互斥模式共用一个受保护的业务 gRPC 构造。方法按 25 Network + 11 Image 精确允许，vpc-read 再收窄到 GetVPC；无明文 fallback，未登记的方法、health/reflection 和 stream 默认拒绝。兼容的 operator 只能由 actor 推导，报文 Attribution 必须与可信上下文相符；既有业务持久化、幂等、权限、租户边界及 Image 平台只读目录保持原合同。admin `/readyz` 继续提供就绪检查。**部署该候选前必须提供合规证书；此前依赖 full 明文业务端口的调用不再接受。**

源码基线为 Governance `8724a32ab7dda903e1c67def28ad206a06d09c44`、Resource `b258907ac8ac2071f909ae116a0f2839b2d7f55f`，两仓各在 `codex/res-gov-call-rules` 任务工作树修改；验收快照时尚未提交/推送，用户随后授权将两仓本批修改提交并交付 main。两仓规则要求通过 PR，实际发布版本以 Git 历史和远端引用为准。Resource 主工作区的 Image 清理材料未改。API 没有变化，Gov 保持正式 SDK 版本 `v0.0.0-20261011084152-b258907ac8ac`，无本地 replace 或 Proto/生成代码复制，联调接收端为本次 Resource 候选源码编译的真实进程。

验证在 SSH `ubuntu`、Go 1.26.9、任务独占 PostgreSQL/Redis 执行，源码/命令/日志位于 `/home/ubuntu/workspace/ani-network-service-runs/res-gov-call-rules-20261011-01a12a2c`。`checks-01.log`、`checks-related-01.log` 与 `checks-build-04.log` 保存真实结果；源码经校验后解包，远端 gofmt 产物按前后 SHA 校验写回。本地与远端的 19 个改动 Go 文件逐项一致，清单为该目录的 `governance.tested-go.sha256`（清单 SHA256 `2daa697036e22c14ca18f44b048a8ac46a962624e66fa6a33efb2bd2ea19385b`）与 `resource.tested-go.sha256`（`9f13f54d619877c560548d703f352f3c104c58ce968607e78bf7c4673fc2373c`）。本轮通过的定向命令及边界如下：

| 验证 | 命令与结果 |
|---|---|
| Gov 调用端 | `go test -mod=readonly -count=1 -timeout=8m -run 'Test(NetworkClient\|ImageClient\|ResourceClient)' ./app/admin/service/internal/data` 及对应 `-race`，PASS；覆盖身份/TLS 拒绝、metadata/Attribution、取消/期限、错误 details、失败写只调用一次 |
| Resource 接收端 | `go test -mod=readonly -count=1 -timeout=5m ./internal/server ./tests/runtime`，PASS；覆盖严格证书、方法允许清单、明文/未知方法拒绝、vpc-read 和字段冲突 |
| Resource 真实进程 | `scripts/integration -timeout=8m -run 'TestBuildAppRunsProductionComposition\|TestMainProcessHandlesSignalAndClosesListeners\|TestBuildAppRejectsInvalidConfig' -v ./cmd/ani-resource-service`，PASS；实际数据库装配、mTLS、admin readiness、信号退出与 listener 关闭 |
| Network 最小主链 | 编译 Resource `networkintegration` helper 后，`RESOURCE_NETWORK_TEST_BINARY=<候选二进制> scripts/network-joint-integration -run '^TestNetworkJointHTTP/(vpc_presets\|preset_access\|authentication_and_plan)$'`，PASS；真实 JWT/AK、权限/套餐、mTLS、PG/worker，VPC/Subnet 创建、原键重放、查询及删除；跨租户、签名和拒绝无副作用 |
| Image 主链 | 编译 Resource `imageintegration` helper 后，`IMAGE_RESOURCE_CONTRACT_BINARY=<候选二进制> scripts/image-joint-integration`，PASS；含 `AKAll11RealResourceHTTP` 的 11 个真实接口、凭证生命周期、登记/更新/注销及隔离/权限拒绝 |
| 生产入口编译 | Gov `go build -mod=readonly ./app/admin/service/cmd/server`、Resource `go build -mod=readonly ./cmd/ani-resource-service`，PASS，最终命令整体退出 0 |

构建环境中 `GONOPROXY` 将该账号的全部模块导向 Git。两次构建在固定 quota SDK 拉取整个历史时主动中止（退出 143，`checks-related-01.log` 的所有前置测试已通过、`checks-build-02.log`）；按任务覆盖代理后出现下载 EOF（`checks-build-03.log`，退出 1）。随后用 curl 从官方 Go 代理下载**相同已锁定版本**的 info/mod/zip 到任务私有文件代理（`quota-proxy-download.log`），由 Go 按既有 `go.sum` 校验消费，最终仅续跑两仓构建通过；没有重跑已通过的行为测试，没有修改依赖版本、校验值或远程全局环境。上述原始中止与失败保留，不写成初次全部通过。

用户授权发布后，在同一 Ubuntu 任务目录的 `publication/resource` 上补跑 Resource `make verify TOOLS_DIR=/home/ubuntu/.local/share/ani-network-service/bin`。发布输入快照 SHA256 为 `ba79b4642aaec4ad89080b506635422c7fcea6929d498837f3e6d8a27347c804`，解包逐项核验后建立独立临时 Git 仓库供门禁使用；该临时提交不发布。短独占 TMPDIR 避免 Unix socket 路径超限。生成一致性、边界守卫、故障构建、tidy 和其余单元包通过；命令在 `biz/network` 的 5 个既有 LB 断言处失败，退出 2（`publication/verify-01.log`），**完整 make verify 为 FAIL**。从远端 main 固定基线 `b258907` 独立归档的相同业务包复现同样 5 个失败，退出 1（`publication/lb-baseline-01.log`）：健康端口缺失/0/独立端口、多监听器 TCP、timeout 等于 interval。它们与现行 [Resource LB 规格](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/b258907ac8ac2071f909ae116a0f2839b2d7f55f/docs/specs/vpc-connectivity-lb.md) 的合法输入相冲突；本批未改该包、测试或合同。该基线的 [CI](https://github.com/zhangzhe-ctrl/ani-resource-service/actions/runs/38126448937) 也已失败，不能归为本次调用规范回归，亦未据此豁免或删除断言。

仅续跑被 make 中断的 `go vet ./...`、`go build -trimpath ./...`、`go mod verify` 与 `git diff --check`，全部通过，续跑整体退出 0（`publication/remaining-01.log`）；没有重跑已通过的联合业务验收。提交前已执行 Resource AGENTS 要求的 make verify；本批按用户明确的调用范围交付并保留失败。推送后的两仓 CI 需按该提交的实际运行结果判定，既有定向 PASS 不代表全仓 CI 已通过。

Network 的 KC/Envoy/实例 owner 与 Image 的 Harbor 仅在外部边界使用测试替身；两个联合夹具也包含协议边界测试，不能将这些结果扩展为真实 Provider/数据面/生产验收。产品资源通过现有业务入口清理，独占 helper 数据库、PG/Redis 容器与进程清理通过。本轮未执行共享业务库变更或部署，不结项整体接口登记；源码发布不代表生产验收。

<a id="lb-deferred-01"></a>

## 2026-10-13：LB 延迟下发合同（LB-DEFERRED-01）

复用 NET-19～NET-24 的 POST/GET/List/PATCH/DELETE/operation 和原 JWT/AK、NETWORK 套餐、权限及 mTLS 身份链。`listeners.items` 内普通监听器允许省略或清空 `backends`，先保存配置；添加后端才激活。PATCH 沿用 `data`、`update_mask`、`expected_version` 和 `idempotency_key`，监听器集合仍表示整体替换，空集合仍拒绝。

Resource 与 Governance 的 `LoadBalancerConfigurationState` 在原 0～5 后追加 `WAITING_FOR_BACKENDS=6`。LB 响应追加只读 `provider_target_version`（字段31）；`desired_version` 是完整配置版本，`provider_target_version` 是改变下发投影的版本，`applied_version` 是经真实外部证据确认的目标或空投影清理版本。仅编辑待配置监听器可使 desired 前进而 target/applied 保持。首次全部待配置时 target/applied 为0、operation 表示配置已保存；混合 LB 的其他监听器继续使用已确认的投影。配置完成、operation 成功与流量健康仍分别表达。

监听器响应追加只读 `deployment_state`（字段13）及 `reason`（字段14），包括 `waiting_for_backends`、`applying`、`configured`、`removing`、`degraded`、`unknown`；重定向等待既定 HTTPS 目标时返回 `WAITING_FOR_REDIRECT_TARGET`。从 GET 组装 PATCH 时只提取 [ListenerInput](../api/protos/catalog/service/v1/vpc.proto) 可写字段，不回填上述观察字段。新字段不进入客户端输入或旧幂等 fingerprint，已保存的受理回执原样重放。

定向联合用例为现有 [Network HTTP 联调](../app/admin/service/internal/service/network_joint_http_test.go) 的 `deferred_load_balancer`：真实身份、mTLS、PG、worker 和 adapter，外部 K8s/controller/owner 协议边界沿用既有夹具；检查零 Provider mutation、三个版本、混合监听器、清空与新 Provider 身份、回执重放、版本冲突、只读输入拒绝和跨租户拒绝。实际结果与真实集群范围只记入 Resource 的 LB-DEFERRED-01 执行状态，不由本合同宣称通过。

## 2026-10-11：监听参数与子网 IP 观测

沿用 NET-19～NET-24 和既有 Get/ListSubnet，不新增路由、权限码或套餐模块。监听参数通过 `listeners.items[]` 接收，旧 singular listener/backends/health_check 保持原合同；不能混用。协议 HTTP/HTTPS/TCP/UDP/SSL，SSL 为 TLS Terminate；新增 certificate_ref、algorithm（RoundRobin/LeastRequest/SourceIP）、session_persistence、max_connections、gzip_enabled、https_redirect_status_code；health_check 支持 TCP/HTTP、path、可选 hostname，UDP 仅 TCP。检查端口省略/0 使用业务端口，非零独立端口允许；正 timeout/interval 独立校验。证书只引用当前资源绑定 namespace 的本租户 TLS Secret，不新增证书管理。

HTTP/HTTPS 的 Cookie 支持停用、COOKIE_TIMEOUT（cookie_expiry_seconds > 0）、COOKIE_SESSION。Gzip 与 Cookie 不适用于其他协议，UDP 不接受连接限制。HTTP 重定向 301/302 与后端互斥，目标使用本 LB 的 HTTPS 443 listener，否则要求唯一 HTTPS listener，并显式生成其端口。更新继续使用 `data`、`update_mask`、expected_version 和幂等键；提供监听器集合表示整体替换，省略保持。参数进入 Resource 配置版本与持久意图，关闭参数或切换协议清理对应旧字段/自有组件。详情和兼容 singular 摘要返回实际保存参数及算法。

Get/ListSubnet 追加有 presence 的 `available_ip_count`/`used_ip_count`（JSON int64 为十进制字符串），分别来自 KCN `status.v4availableIPs`/`v4usingIPs`，由现有观察 → Worker → PostgreSQL → Resource → BFF 读取。实际零返回 `"0"`；未观测/字段缺失/非法值省略字段。有效新观测清除缺失字段的旧值，读取失败保留历史值且不续鲜，过期沿用 observation_stale；租户、binding、namespace、UID、generation、lease、观测时间围栏不变。GetSubnet 仍读取持久化结果，不同步访问 KCN。

JWT/AK、mTLS 机器身份、可信操作人、租户映射、NETWORK 套餐和既有权限保持原闸门。必要外部条件为选定 Secret 的 namespace 内 get 权限，以及 UDP 所需 EnvoyPatchPolicy CRD/底层扩展开关；本任务不更改共享平台。真实集群验收 `not_verified`，外部 fixture 的控制器状态不表示真实数据面能力。登记范围仅本次字段，不结项整体接口风格登记。

本次 Fedora `go test -count=1 -run '^TestNetworkNewFieldsRetainIntentAndObservations$' ./app/admin/service/internal/service` 通过；`scripts/network-joint-integration -run '^TestNetworkJointHTTP/(multiple_listeners|subnet_ip_counts)$'` 通过，涵盖 JWT 创建/AK 重放、AK 更新/JWT 重放、各协议 CRD、参数关闭与清理、计数三种 presence 与跨租户 404，产品与独占 PG/Redis 清理通过。Resource 计数用例另验证 UID/generation/旧 lease/旧观测与读取失败不续鲜。未执行全量测试或完整 JWT/AK 矩阵。

Resource 依赖固定到真实本地提交 `b258907ac8ac2071f909ae116a0f2839b2d7f55f` 的版本 `v0.0.0-20261011084152-b258907ac8ac`，无个人绝对路径 replace。提交尚未推送；其他环境正常拉取依赖前须先发布该 Resource 提交。当前两仓联调使用任务专用 go.work，单仓编译使用该提交的标准 Go module zip；不将缓存命中视为远端发布完成。Fedora 上 GOWORK=off 的两仓生产入口编译通过；Governance 使用 -mod=readonly，并从该真实版本的 module cache 解析 Resource，未借工作区覆盖旧依赖。

## 2026-10-10：固定三规格、多监听器与 VPC 预设后端

用户明确禁止修改前端，本批限后端。NET-19～NET-24 沿用路径、旧字段编号和 LB 组合响应，flavor 固定 small/medium/large。创建可提供 `listeners: {items: [...]}`，每监听器有稳定 id/name、HTTP port、独立 backends/health_check；旧 listener/backends/health_check 输入继续兼容，混用拒绝。更新沿用 expected_version/idempotency_key，新输入使用 `data` 和 `update_mask`；集合遗漏保持，提供表示整体替换，空集合拒绝。查询追加 `listeners`，desired/applied 和配置状态继续与真实流量证据分开。

| 编号 | RPC | HTTP | 权限码 | 响应 |
| --- | --- | --- | --- | --- |
| NET-25 | ListVPCCIDRPresets | `GET /api/v1/networks/vpc-cidr-presets` | `network:vpc:presets` | `{cidrs:[...]}` |

NET-25 无 body/query，支持 JWT 和 ANI-HMAC-SHA256，要求当前租户、角色/API 权限及 `NETWORK` 套餐模块；可信 Resource tenant 从持久映射获取，不接受公网 tenant/actor。机器 operation allowlist、API 权限 SQL 和 OpenAPI 同步纳入新接口。只在独占验收库执行目录 dry-run、显式同步、权限/套餐关系及 Casbin 刷新，未修改业务库。

预设唯一来源是 Resource 启动 YAML。读取只返回无冲突候选，不公开平台拓扑；CreateVPC 首次受理精确匹配并重新检查平台事实，空配置拒绝新建，旧幂等重放保持。配置改动需重启 Resource，Governance 不维护第二份列表。当前验收结果和恢复入口统一见 [本次计划](plans/network-lb-vpc-presets.md#实施结果与剩余断点)。

## 2026-10-09：租户 Network 补齐与响应合同

本轮源码补齐 NET-12～NET-24 的 Subnet、SNAT 和 LB 入口，并把已有未放行的 10 个入口与新增 13 个入口加入精确机器身份策略。该批租户面为下表 24 个方法；后续 NET-25 见 2026-10-10 增量。**全部支持用户 JWT 或 ANI-HMAC-SHA256，并要求当前租户状态、对应角色/API 权限及套餐 `NETWORK` 模块**。源码接入与实际验收分别记录，不能用路由或 OpenAPI 存在代替业务完成。

### NET-01～NET-24 路由、权限与响应（2026-10-09 批次）

NET-01～NET-11 沿用历史编号；NET-12～NET-24 为本轮新增。下表的单资源类型表示 HTTP 直接返回对象；内部 gRPC 响应仍保留原消息字段。

| 编号 | RPC | HTTP 方法与路径 | 权限码 | HTTP 响应 |
| --- | --- | --- | --- | --- |
| NET-01 | GetVPC | `GET /api/v1/networks/vpcs/{vpc_id}` | `network:vpc:get` | VPC |
| NET-02 | ListVPCs | `GET /api/v1/networks/vpcs` | `network:vpc:list` | items / total；page/pageSize，见 VPC R01 |
| NET-03 | CreateVPC | `POST /api/v1/networks/vpcs` | `network:vpc:create` | VPC |
| NET-04 | DeleteVPC | `DELETE /api/v1/networks/vpcs/{vpc_id}` | `network:vpc:delete` | VPC |
| NET-05 | GetOperation | `GET /api/v1/networks/operations/{operation_id}` | `network:operation:get` | Operation |
| NET-06 | GetEIP | `GET /api/v1/networks/eips/{eip_id}` | `network:eip:get` | EIP |
| NET-07 | ListEIPs | `GET /api/v1/networks/eips` | `network:eip:list` | items / next_cursor / total |
| NET-08 | CreateEIP | `POST /api/v1/networks/eips` | `network:eip:create` | EIP |
| NET-09 | DeleteEIP | `DELETE /api/v1/networks/eips/{eip_id}` | `network:eip:delete` | EIP |
| NET-10 | GetVPCSnat | `GET /api/v1/networks/vpcs/{vpc_id}/snat` | `network:snat:get` | VPCSnat |
| NET-11 | BindVPCSnat | `POST /api/v1/networks/vpcs/{vpc_id}/snat/bindings` | `network:snat:bind` | VPCSnat |
| NET-12 | CreateSubnet | `POST /api/v1/networks/subnets` | `network:subnet:create` | Subnet |
| NET-13 | GetSubnet | `GET /api/v1/networks/subnets/{subnet_id}` | `network:subnet:get` | Subnet |
| NET-14 | ListSubnets | `GET /api/v1/networks/subnets` | `network:subnet:list` | items / next_cursor / total |
| NET-15 | DeleteSubnet | `DELETE /api/v1/networks/subnets/{subnet_id}` | `network:subnet:delete` | Subnet |
| NET-16 | GetVPCSnatBinding | `GET /api/v1/networks/snat/bindings/{binding_id}` | `network:snat:get` | VPCSnat |
| NET-17 | SetVPCSnatEnabled | `PATCH /api/v1/networks/snat/bindings/{binding_id}` | `network:snat:update` | VPCSnat |
| NET-18 | DeleteVPCSnatBinding | `DELETE /api/v1/networks/snat/bindings/{binding_id}` | `network:snat:delete` | VPCSnat |
| NET-19 | CreateLoadBalancer | `POST /api/v1/networks/load-balancers` | `network:load-balancer:create` | load_balancer + operation |
| NET-20 | GetLoadBalancer | `GET /api/v1/networks/load-balancers/{load_balancer_id}` | `network:load-balancer:get` | LoadBalancer |
| NET-21 | ListLoadBalancers | `GET /api/v1/networks/load-balancers` | `network:load-balancer:list` | items / next_cursor / total |
| NET-22 | UpdateLoadBalancer | `PATCH /api/v1/networks/load-balancers/{load_balancer_id}` | `network:load-balancer:update` | load_balancer + operation |
| NET-23 | DeleteLoadBalancer | `DELETE /api/v1/networks/load-balancers/{load_balancer_id}` | `network:load-balancer:delete` | load_balancer + operation |
| NET-24 | GetLoadBalancerOperation | `GET /api/v1/networks/load-balancers/operations/{operation_id}` | `network:load-balancer:operation:get` | Operation |

HTTP 方法、字段、签名与请求限制以 [Network 路由输入](../api/protos/admin/service/v1/i_network.proto)、[公开 DTO](../api/protos/catalog/service/v1/vpc.proto) 和 [AK/SK 合同](contracts/aksk-signature.md) 为准，不接受公网 `tenant`、`actor`、`operator` 或平台管理员身份。Subnet 创建保留 `vpc_id` 与可选 `gateway`；SNAT 切换保留 `binding_id/enabled/expected_version/idempotency_key`；LB 创建保留父资源、入口、后端和健康检查输入，更新只允许现有可变字段并携带 `expected_version/idempotency_key`。GET/DELETE 无请求体，POST/PATCH 使用对应 DTO 的 JSON 对象；详情与写入不接受 query。

四类列表保留 `items/next_cursor/total`，`total` 为相同可信租户、权限和筛选条件下的总数，不受本页 cursor/limit 影响；Proto int64 在 HTTP JSON 中为十进制字符串，空列表返回 `"0"`。VPC/EIP 查询允许 `name/state/limit/cursor`，Subnet 另有 `vpc_id`，LB 另有 `vpc_id/subnet_id/exposure`；实际白名单及未知、重复、歧义参数的拒绝见 [Network 请求策略](../pkg/middleware/auth/network_policy.go)。

### 身份、归属与异步结果

用户与 Key 分别建立可信 `Principal`；API Key 继续是独立机器身份，不伪造 `user_id`。签名校验绑定 method、规范 path/query、AK、时间戳与原始 body SHA256，复查 Key/角色/租户/套餐；重复认证头、JWT/AK 混用、未知 operation 和篡改在业务副作用前拒绝。历史 GetVPC 签名向量和 Image 11 方法的合法客户端保留，调用示例见 [签名客户端](../scripts/ops/aksk_vpc_client.py)。

[Network Service](../app/admin/service/internal/service/network_service.go) 从认证主体取得本地租户，经持久 `resource_tenant_id` 映射后调用 [出站客户端](../app/admin/service/internal/data/network_client.go)。客户端整体重建单值 `x-ani-tenant-id/x-ani-actor/x-ani-request-id`，actor 区分 `governance:user:<id>` 与 `governance:access-key:<id>`；不转发公网身份或认证材料。Resource 继续负责真实租户过滤、父子归属、持久幂等、版本、占用和删除约束，Governance 核对返回对象的租户和资源关联。

HTTP 成功表示 Resource 返回了持久资源或受理快照；资源生命周期与 operation 结果分别查询。SNAT 的 `desired_enabled` 与可选 `applied_enabled` 保留期望、实际观测和未知状态，解绑保留独立 EIP。LB 的 `state/configuration_state/data_plane_state` 与 `desired_version/provider_target_version/applied_version`、监听器只读部署事实、观测时间分别保留，不能把受理、配置完成或 operation 成功解释为流量健康；删除结果仍含资源与 operation，并由 Resource 释放关联、保留独立 EIP 和工作负载。公开错误保留 Resource 的领域 reason 和适当 HTTP 码，不暴露下游私有错误详情。

平台 Network 27 方法、Attachment 四个方法、ImageRuntime 两个方法和实例 owner 的 GetSubmission 不属于此租户 HTTP/AK 接入范围。

### 十个既有单对象响应展开

下表通过 BFF `response_body` 展开；资源内部字段、枚举、时间、int64 序列化和内部 gRPC 字段编号保留。新增单对象接口同样直接返回公开对象，既有已展开详情沿用原行为。

| 方法 | HTTP 方法与路径 | `response_body` / 直接类型 |
| --- | --- | --- |
| CreateVPC | `POST /api/v1/networks/vpcs` | `vpc` / VPC |
| DeleteVPC | `DELETE /api/v1/networks/vpcs/{vpc_id}` | `vpc` / VPC |
| CreateEIP | `POST /api/v1/networks/eips` | `eip` / EIP |
| DeleteEIP | `DELETE /api/v1/networks/eips/{eip_id}` | `eip` / EIP |
| BindVPCSnat | `POST /api/v1/networks/vpcs/{vpc_id}/snat/bindings` | `snat` / VPCSnat |
| EnsureImageSpace | `POST /api/v1/images/space:enable` | `space` / ImageSpace |
| DisablePublisherCredential | `POST /api/v1/images/publisher-credential:disable` | `credential` / PublisherCredential |
| RegisterImage | `POST /api/v1/images/registrations` | `image` / ImageRegistration |
| UpdateImage | `PATCH /api/v1/images/registrations/{image_id}` | `image` / ImageRegistration |
| UnregisterImage | `POST /api/v1/images/registrations/{image_id}:unregister` | `image` / ImageRegistration |

例如原 `{"vpc":{"id":"..."}}` 返回为 `{"id":"..."}`。Image 凭证签发/重置的 `credential + secret + replay_until`、API Key 创建/重置的 `data + secret_key`、制品下载授权的 `artifact + download_url + expires_at`、LB 的 `load_balancer + operation` 及分页、统计、日志等有业务含义的组合字段完整保留；不引入全局 code/data 包装。正式生成同时更新 HTTP 服务端、客户端和 [OpenAPI](../app/admin/service/cmd/server/assets/openapi.yaml)，OpenAPI 的 NetworkOperation 使用独立名称避免与生成器 Operation 同名，仍对应公开 Network Operation 对象。

### 权限材料、API 目录与运行授权

[bootstrap-network-access.sql](../scripts/ops/sql/bootstrap-network-access.sql) 是 24 条路由及权限关系的源码材料，面向显式指定的租户、角色与已有 NETWORK 套餐；它不自动扩大共享套餐，运行前核对目标和前置条件。源码/SQL 存在不是目录已登记或角色已获授权的证据。

运行实例先按 [API 权限运维](api-role-permission-ops.md) 执行 `admin sync-apis --dry-run`，确认后显式同步，保留既有 ID、启停状态与权限关联；目录同步不删除接口、不授予角色。再按授权应用所选权限材料、确认对应租户角色和套餐，并刷新实际策略缓存。以上目录和运行角色写入只在本任务独占验收环境执行；共享业务库、角色策略与生产部署仍需单独授权。

逐接口行为、JWT/AK、跨租户与套餐拒绝、真实持久化/worker 和产品清理由 [Network HTTP 联调用例](../app/admin/service/internal/service/network_joint_http_test.go)、[Resource 子进程接线](../app/admin/service/internal/service/network_joint_fixture_test.go) 与 [隔离执行入口](../scripts/network-joint-integration) 验证；[请求与签名负向用例](../pkg/middleware/auth/network_policy_test.go)、[客户端受信 metadata 用例](../app/admin/service/internal/data/network_client_test.go)、[Service 映射用例](../app/admin/service/internal/service/network_service_test.go) 和 [HTTP/OpenAPI 响应用例](../app/admin/service/internal/server/detail_response_http_test.go) 提供对应回归，不代替三条业务主链。独占数据库中的运行授权与清理必须有实际执行结果。

### 本轮运行证据

本轮 Goal 的源码与隔离业务闭环完成，`pass`。基线 Resource `60702b2bcfe441b8ef86991b95274d0da34773ea`、Governance `315389d274acfc4237a87b9b4c98c23320f91b4c`；两仓位于任务独占 `codex/network-governance-20261009` worktree。验收快照时尚未提交或发布；随后用户已授权交付两仓远端 `main`，最终远端 SHA 以对应 Git 提交及交付回执为准，远端 CI 在实际验证前为 `not_verified`。最终验证源包：Governance `governance-final11/source.tar.gz`，SHA256 `5b1c16ff1e3b68e9c42eb378183c064f688872fbb0110732162150a7708294d2`；Resource `resource-final10/source.tar.gz`，SHA256 `0db2c8e312a5470e85919a1ad9a1712565e4fe9deb3f90939f549af26a309b7e`。源包、原始日志、清单和命令仅保留于本地任务归档 `/home/chabking/.codex/worktrees/network-governance-20261009/runs/`，不随业务源码提交；下文材料名均相对该归档根目录。`source-equivalence12.json` 确认验收后的代码及生成输入一致，最终证据文档更新不改变验证代码及生成输入。

最终 `network-joint11` 中 24 个方法逐一经过真实 HTTP 的 JWT 与 AK 身份，完成 Subnet、SNAT、LB 三条主链、持久化/幂等/CAS/占用/关联释放、权限/套餐/租户隔离和签名负向场景。拒绝发生在 Resource 新业务副作用前。`image-joint11` 覆盖原有 11 个 Image AK 方法、五个写响应的 JWT/AK 展开、凭证组合、实际持久化与产品注销/禁用；`affected11` 核实 Network 客户端/Service、十个直接写响应和生成客户端、既有详情/列表与业务组合，均 `pass`。调用使用真实 Governance 身份、Casbin/套餐、mTLS、Resource 业务代码、独占 PostgreSQL 和 worker；KC、Envoy、实例 owner、registry 仅在外部适配器边界使用明确替身。**真实外部网络/Harbor、流量数据面及生产验收 `not_verified`**。

租户资源先经 HTTP 产品接口释放/删除；Resource 子进程随后走真实 Attachment 协作、Subnet/VPC 和平台用例，关闭池分配、依次删除 Public 池、Intranet 池与网关，等待真实 worker 的 Deleted/Succeeded。最终活动 VPC/Subnet/EIP/SNAT/LB/Attachment/平台资源总数为 0。平台合同没有清默认命令，默认引用保留指向已删除池的历史记录，未手改业务状态或删业务记录。Network helper PID `4122479`、Image helper PID `4127965` 均退出 0。基础设施最后清理；独立核查 `evidence/ubuntu/cleanup-ubuntu11.json` 的 20 个明确记录容器、8 个 helper PID 均不存在，任务私有运行目录无残留；`evidence/fedora/cleanup-fedora11.json` 确认正式检查的 Redis 已移除。源码包、日志与构建哈希作为恢复/复现材料保留。

主机：业务/生成/Resource 门禁为 SSH `ubuntu`（`i-8yg2l7u8`），Governance 正式检查为 SSH `fedora`。普通任务先用 Ubuntu；其早先扫描受内存压力中止，改用 Fedora 官方 Go、任务私有模块缓存、2 CPU/8 GiB 上限。重任务串行。最终运行 Go 1.26.9、protoc 29.3、Buf 1.60.0、SQLC 1.31.1；生成器锁保持。短任务 TMPDIR 分别为 `/home/ubuntu/t82`、`/home/chabking/t1f82`。远端临时 Git 仅供正式格式/生成入口比较，不是源仓提交。执行与退出码：

| 命令/验证 | 主机 | 结果及原始日志 |
| --- | --- | --- |
| `RESOURCE_NETWORK_TEST_BINARY=... ./scripts/network-joint-integration` | Ubuntu | pass / 0：`evidence/ubuntu/network-joint11.log` |
| `IMAGE_RESOURCE_CONTRACT_BINARY=... ./scripts/image-joint-integration` | Ubuntu | pass / 0：`evidence/ubuntu/image-joint11.log` |
| `go test -mod=readonly -count=1 -run '^(TestNetwork\|TestSingleWrite\|TestBusinessCombination\|TestDetail)'`，data/service/server | Ubuntu | pass / 0：`evidence/ubuntu/affected11.log` |
| `make check-generated`，两次完整 `make gen`、gow/redact integration | Ubuntu | pass / 0：`evidence/ubuntu/governance-generated11.log`，861 个生成文件/209 个输入/6 根的清单 `evidence/ubuntu/generated11/generation-source-final.json` |
| `make verify BUF=... SQLC=...` | Ubuntu | pass / 0：`evidence/ubuntu/resource-verify11.log` |
| `make check`，格式/布局/Python/build/pkg/SQL/security | Fedora | pass / 0：`evidence/fedora/governance-check11.log` |
| `validate-cleanup11.py EVIDENCE_DIR OUTPUT --tmpdir ...` | 两主机 | pass / 0，清理 JSON 见上文 |

完整串行命令见 `final-ubuntu11.sh`，Resource 联调二进制另留 `evidence/ubuntu/resource-network-binary11.sha256`、`evidence/ubuntu/resource-image-binary11.sha256`。权限材料在独占库经 API 目录 dry-run 回滚、真实 ApiRepoSync 与 SQL 重复应用校验；24 路由/23 权限码（两种 SNAT 查询共享一权限）保持既有禁用条目且不误授其他角色。**源码材料、独占运行目录/角色授权为 pass；共享 API 目录、共享角色策略未写入，应用镜像发布、部署及生产运行授权 not_verified。**

原 Go 1.26.7 的 `evidence/fedora/governance-check10.log` 保留 `fail`：2026-10-08 新公告命中标准库及 HTTP/2。按 [Go 1.26.9 发布记录](https://go.dev/doc/devel/release#go1.26.9) 和 [GO-2026-6617 修复版本](https://pkg.go.dev/vuln/GO-2026-6617)，仅为解除必需门禁升级 Governance Go/Dockerfile 到 1.26.9、x/net 到 v0.60.0，并保留它要求的传递依赖版本；不改生成器锁或 Resource 生产代码。最终扫描可调用漏洞为 0、未检出密钥泄漏；仅依赖级告警按原日志保留。早期代理 EOF/缓存权限、长 TMPDIR socket 及外部 SNAT 状态发布/驱动版本等待失败保留原日志；最终修正未绕过权限、版本或持久化。

外扩 defaultdata 回归 `evidence/ubuntu/governance-regressions09b.log` 保留 `fail`：ModelDev `deadline_in_flight` 断言失败。失败快照的该实现、测试及 go.mod/go.sum 与起始 `315389d` 逐字节一致，源哈希见 `evidence/ubuntu/modeldev-source-comparison09.json`。安全更新前，相同 Ubuntu Go 1.26.7 环境各对照一次：`evidence/ubuntu/modeldev-baseline-10.log` fail / 1，`evidence/ubuntu/modeldev-candidate-10.log` pass / 0；原失败在基线可复现，表现存在时序不稳定。未改 ModelDev，未以定向通过覆盖全包失败。该投递测试在本轮接入改动范围外；本轮不声称全仓回归全部通过，不重复无关配额/Compute/Storage/IAM/安装器验收。

## 功能组：登录、登出及登录后初始化

### 本次改动

用户已明确要求将平台和租户登录改为直接提交密码。本次作为即时改动实施，不等待统一风格批次；具体调用方式与发布边界见下文。

平台和租户密码登录均已移除图形验证码检查。不再需要先获取/验证图片验证码，也不需要 `X-Captcha-Id`、`X-Captcha-Value`；旧客户端即使发送这两个请求头，登录也不读取。没有新增开关，不需要数据库迁移或 API 目录同步；部署新服务二进制后生效。

独立的两个图形验证码接口暂保留兼容，已退出登录流程。账户启用的 MFA、找回密码邮件验证码是不同功能，此次没有改动。

### 当前接口清单

下表为当前已注册的 HTTP 路由，不是计划中的接口。请求体直接传 JSON，不套 `data`；GET 无请求体。受保护接口使用 `Authorization: Bearer <access_token>`，还受现有角色权限与租户套餐检查约束。

| 编号 | 方法与路径 | 请求 / 认证 | 当前响应或作用 | 目标风格 / 状态 |
| --- | --- | --- | --- | --- |
| AUTH-01 | `POST /api/v1/auth/password/login` | `tenant_name`、`username`、`password`；无需 Bearer、无需图形验证码 | 租户登录，返回下述令牌报文并设置 Cookie | 验证码和登录 AES 编码已移除；路径/字段待指定 |
| AUTH-02 | `POST /api/v1/auth/platform/password/login` | `username`、`password`；无需 Bearer、无需图形验证码 | 平台登录，要求平台角色 | 验证码和登录 AES 编码已移除；路径/字段待指定 |
| AUTH-03 | `POST /api/v1/auth/logout` | Bearer；请求体 `{}` 即可；`jti` 可选 | `{"status":"revoked"}`；吊销该用户的全部后台会话并清 Cookie，非仅当前设备 | 路径/登出范围待指定；租户权限问题见 ISSUE-02 |
| AUTH-04 | `POST /api/v1/auth/refresh` | 携带 `refresh_token` Cookie；请求体 `{}`，可选 `client_id`、`device_id`；无需 Bearer | 返回新访问令牌，轮换 Cookie，旧令牌对失效 | Cookie 或其他风格待指定 |
| AUTH-05 | `GET /api/v1/admin/me` | Bearer | 当前用户 `identity.service.v1.User` 对象 | 路径/用户字段待指定 |
| AUTH-06 | `GET /api/v1/admin/initial-context` | Bearer | `menus`、`permissions`、`hiddenFields` | 登录后聚合初始化；路径/报文待指定 |
| AUTH-07 | `GET /api/v1/admin/routes` | Bearer | `items` 菜单路由树 | 单独获取菜单；与聚合接口择需调用 |
| AUTH-08 | `GET /api/v1/admin/perm-codes` | Bearer | `codes`、`hiddenFields` | 单独获取权限；与聚合接口择需调用 |

以下仅在对应功能使用时才需要接入，不是普通账密登录的前置步骤：

| 编号 | 方法与路径 | 当前用途 | 登记状态 |
| --- | --- | --- | --- |
| AUTH-09 | `POST /api/v1/auth/mfa/verify` | 已启用 MFA 的账号提交登录返回的操作 ID 与二次验证答案；返回旧版 `LoginResponse`，与 `TokenPairResponse` 不是同一报文 | 仅 MFA 登录中间态需要，报文风格待指定 |
| AUTH-10 | `POST /api/v1/auth/forgot-password` | 发找回密码邮件验证码 | 可选功能，未改 |
| AUTH-11 | `POST /api/v1/auth/reset-password-by-code` | 用邮件验证码重置密码 | 可选功能，未改 |
| AUTH-12 | `POST /api/v1/auth/invitations/accept` | 接受邀请、设置密码并激活；直接激活模式无需调用 | 可选功能，未改 |
| AUTH-13 | `GET /api/v1/auth/captcha` | 独立获取图形验证码 | 登录不再使用；接口是否删除待指定 |
| AUTH-14 | `POST /api/v1/auth/captcha/verify` | 独立验证图形验证码 | 登录不再使用；接口是否删除待指定 |
| AUTH-15 | `PUT /api/v1/admin/me` | 本人资料自助更新；直接落 `userRepo.Update`，**绕过 `UserService` 的角色类型/租户校验**，携带 `roleIds`+`role_ids`　掩码可能自改角色（见 MGMT-11） | 已接线但此前未登记，本轮补登；路径/字段待指定 |
| AUTH-16 | `POST /api/v1/admin/me/password` | 本人自助改密；`oldPassword`/`newPassword` 走 AES 密文（与登录直接提交明文密码不同）；成功后吊销本人全部客户端令牌 | 已接线但此前未登记，本轮补登；协议/路径待指定 |
| AUTH-17 | `POST /api/v1/admin/me/contact` | 向本人待绑定邮箱发送验证码；当前只支持 EMAIL，不支持手机号。与 AUTH-18 配合建立邮箱凭证，可用于找回密码；邀请接受流程会自行建立邮箱凭证，不以本接口为前置 | 已接线；2026-09-22 前端交接复核更正用途；待指定 |
| AUTH-18 | `POST /api/v1/admin/me/contact/verify` | 校验本人联系方式验证码，与 AUTH-17 成对 | 已接线但此前未登记，本轮补登；待指定 |

`PUT /api/v1/admin/me`（AUTH-15）与账号组的 `ACCOUNT-04`（管理侧改用户）不是同一入口：后者经 `UserService` 校验角色类型/租户并要求至少一个角色，前者不经该校验。

内部方法 `Login`、`WhoAmI`、`ValidateToken` 不等于存在对应公开 HTTP 地址；不要按方法名拼接口。

### 当前最小调用流程

1. 租户账号（包括租户首管理员）调用 AUTH-01；平台账号调用 AUTH-02。`tenant_name` 实际是 `sys_tenants.code`，不是租户显示名称；租户登录漏传它会拒绝，不会自动改走平台登录。用户名允许 `local:` 前缀。
2. 保存响应中的访问令牌；由浏览器或 HTTP 客户端 Cookie jar 保存登录响应 Cookie。
3. 使用 Bearer 调用 AUTH-05、AUTH-06，取得个人资料、菜单与权限。用了聚合初始化后，不必同时调用 AUTH-07、AUTH-08。
4. 使用 AUTH-04 刷新会话并替换访问令牌。旧 refresh token 单次使用，客户端应避免并发刷新。
5. 使用有效 Bearer 调用 AUTH-03，成功后清理本地令牌和用户上下文。仅清前端缓存不代表服务端已登出。访问令牌已经过期时，当前登出接口会先被鉴权拒绝。

租户登录请求示意（密码占位符不能原样发送）：

```json
{
  "tenant_name": "your-tenant-code",
  "username": "manager",
  "password": "<用户输入的原始密码>"
}
```

平台登录去掉 `tenant_name`，使用平台登录路径。两者都不传 `grant_type`、`captcha_id` 或验证码头。

正常登录/刷新返回 `access_token`、`expires_in`（秒）和 `issued_at`。不要依赖响应体中的 `refresh_token`：当前适配层不填该字段。`expires_in` 在 Proto 中是 int64，客户端应兼容 JSON 字符串数值。若账号启用了 MFA，密码通过后返回 `mfa_required=true`、`mfa_operation_id`，此时没有有效访问令牌，应完成 AUTH-09。

当前密码处理链：用户输入密码 → 客户端直接放入 JSON 的 `password` → 通过 HTTPS 传输 → 服务端直接与数据库 bcrypt 哈希校验。登录不再进行 AES/Base64 解码，也不接收客户端 bcrypt 哈希。用户密码与数据库存储无需修改。

发布时前后端必须同步：客户端删除登录前的 AES/Base64 编码；旧客户端提交的 AES 密文会被当作密码本身校验，通常返回 `INVALID_CREDENTIALS`。没有按内容猜测编码方式的兼容分支。HTTPS 由部署入口/反向代理提供，本次没有自动配置域名或 TLS 证书。传输要求见 [OWASP 认证指南](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html#transmit-passwords-only-over-tls-or-other-strong-transport)。

本次只改平台和租户登录协议。修改密码、管理员重置密码等其他接口按各自现有合同处理，不能据此把客户端所有密码字段的编码一起删除。两个登录入口的审计不记录密码请求体，通用请求日志仍过滤 `args`。

Cookie 现状：`refresh_token` 为 HttpOnly，Path=`/api/v1/auth/refresh`、SameSite=Lax；`refresh_exp` 是前端可读的过期时间提示，Path=`/`。Secure 随 HTTPS / 可信代理转发协议判断。浏览器跨源请求需允许携带凭据，实际 CORS 与 Cookie 是否送达仍需按部署域名联调。推荐在当前联调中经同源代理访问；仅加 `credentials: include` 不会取消 SameSite 限制。

### 已发现的对接问题与待改项

| 编号 | 源码确认的现状 | 后续处理 | 状态 |
| --- | --- | --- | --- |
| ISSUE-01 | 原登录强制 AES 编码，与接口说明不一致 | 按用户要求改成 HTTPS 直接提交原始密码；更新源 Proto、生成代码/OpenAPI、登录用例及审计回归；数据库 bcrypt 不变 | 实现完成；登录/密码管理与审计定向测试、服务编译 PASS；前端与后端需同步发布，用户环境尚未部署 |
| ISSUE-02 | 原首次种子遗漏 `sys:tenant_manager` → `POST /api/v1/auth/logout` | 已补首次种子；旧库显式执行 [登出授权补丁](../scripts/ops/sql/repair-tenant-logout.sql)，操作见 [部署流程](deployment.md)；仍保留租户状态、套餐与角色权限检查 | 实现完成；PostgreSQL 18 定向验证 PASS；用户环境尚未应用，HTTP 联调 not_verified |
| ISSUE-03 | 认证路径为 `/api/v1/auth/*`，个人资料/初始化为 `/api/v1/admin/*`；字段也有 snake_case 与 camelCase 混用 | 累计记录差异，待用户指定批次与旧项目目标格式后统一调整 | 待指定 |
| ISSUE-04 | 当前登出吊销用户全部后台会话，`jti` 仅审计用 | 用户指定保留全部登出还是改为当前会话 | 待指定 |
| ISSUE-05 | kind 实际联调发现租户数据库已有套餐，但读取 DTO 丢失 `plan_id`，初始化菜单被误判为无套餐而清空 | 将现有 nullable 外键显式绑定为 Ent 字段；重新生成代码，租户详情与列表可返回套餐 ID；Atlas 确认与现有结构无差异，无需迁移 | 租户仓储、登录与租户服务回归 PASS；kind NodePort 平台/租户登录、非空菜单、权限、刷新、登出验收 PASS；详见 [运行记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/deployment-kind-20260921.md) |

这些条目是现状与待讨论项，不表示已批准新增需求。既有数据库的数据变更必须显式执行；服务启动不迁移、不播种、不同步 API。

### 验证与变更记录

| 日期 | 改动 | 验证 | 对接验收 |
| --- | --- | --- | --- |
| 2026-09-21 | 按用户要求重新初始化 Ubuntu kind Governance；保留旧库，新库 Atlas + 显式初始化；修复租户套餐 ID 映射导致的空菜单；不调整前端 | 后端 NodePort 登录、菜单权限、刷新、平台/租户登出及失效检查 PASS；重启后 17 张初始化/业务表不变，详见 [运行记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/deployment-kind-20260921.md) | 本轮后端 NodePort 验收通过；前端、公网 HTTPS 与邮件未验收，整体接口风格登记仍未结项 |
| 2026-09-21 | 按用户要求取消两个密码登录入口的 AES/Base64 解码；同步 Proto、生成代码/OpenAPI 与部署说明；原 bcrypt 哈希及其他密码接口协议保留 | Ubuntu / Go 1.26.7：25 个 `TestAuthSvcSqlite_` 用例、2 个密码管理回归用例、`TestPasswordLoginAuditDoesNotPersistPassword` 与服务编译 PASS；含正确原始密码通过、旧 AES 报文拒绝、审计不记录密码；生成使用现有 buf 模板，Go 插件版本 v1.36.12 | 用户环境 HTTPS 配置、部署及前端联调 not_verified；需前后端同步发布；整体登记未结项 |
| 2026-09-21 | 按用户要求补齐租户管理员登出授权：新库种子新增关联，旧库提供独立 SQL 补丁；密码协议未改 | Ubuntu / Go 1.26.7 / PostgreSQL 18，`TestBootstrapPostgres` PASS：新库授权存在、模拟旧库缺项可修复、重复执行数据及 ID 不变、缺 API 前置条件时失败且不改数据 | 未执行用户环境补丁；租户 HTTP 登出联调 not_verified；整体登记未结项 |
| 2026-09-21 | 按用户澄清扩展为全功能登记，新增功能索引与待分批规则；登录组保留原编号 | 文档链接与差异检查通过，无运行代码变更 | 风格批次尚未指定，整体登记未结项 |
| 2026-09-21 | 移除共用密码登录逻辑中的验证码门槛、常量及校验函数；更新用例，使平台/租户成功登录都不提供验证码；建立本登记 | Ubuntu，Go 1.26.7，`go test ./app/admin/service/internal/service -run '^TestAuthSvcSqlite_' -count=1` PASS（25 个测试函数）；服务入口 `go build ./app/admin/service/cmd/server` PASS；覆盖无验证码、旧验证码头、错误凭证、限流、租户/平台角色、刷新轮换、登出吊销及独立验证码接口 | 用户当前环境部署与前端联调：not_verified；整体登记保持进行中 |

后续修改本功能组时更新对应 AUTH/ISSUE 条目与本表，区分“实现完成”“定向验证通过”“实际联调通过”“用户确认关闭”。测试使用 Ent SQLite 与 miniredis；本次不能据此声称 PostgreSQL HTTP 全链路或前端联调已通过。

### 源码入口

- [HTTP 认证路由](../api/protos/admin/service/v1/i_authentication.proto)、[登录与刷新报文](../api/protos/authentication/service/v1/ani_auth.proto)
- [个人资料路由](../api/protos/admin/service/v1/i_user_profile.proto)、[初始化路由和响应](../api/protos/admin/service/v1/i_admin_portal.proto)、[MFA 路由](../api/protos/admin/service/v1/i_mfa.proto)
- [认证业务与 Cookie](../app/admin/service/internal/service/authentication_service.go)、[密码解码与凭证校验](../app/admin/service/internal/data/user_credential_repo.go)
- [HTTP 鉴权装配](../app/admin/service/internal/server/rest_server.go)、[租户套餐检查](../app/admin/service/internal/data/tenant_access_checker.go)、[初始化权限 SQL](../sql/bootstrap/001_initial.sql)
- [定向测试](../app/admin/service/internal/service/authentication_service_sqlite_test.go)、[部署与初始化流程](deployment.md)


## 功能组：租户、管理员、套餐与平台运营账号

排查日期：2026-09-21，首次基于 `39008e4` 排查，随后与最新 `main`（`11fa857`，移除文件管理及对象存储）整合。首次排查只登记功能与接口。后续用户授权小范围修复账号停用/删除的会话吊销；不修改密码重置、接口风格或运行数据。上一轮 kind 验收覆盖租户及首管理员创建、平台/租户登录、菜单、套餐读取与登出；本表其余操作的本轮运行验收为 `not_verified`。

2026-09-21 增量复核（按用户要求重列租户管理、租户管理员管理、套餐管理、平台管理员管理四域接口）：逐一对照源码核对 `TENANT-01～09`、`ACCOUNT-01～09`、`ROLE-01～05`、`PLAN-01～14`，与既有登记一致，无错登（`misregistered` 为空）；补登在线会话自助两条路由 `SESSION-03/04` 及授权缺口 `MGMT-08`；并补登此前仅以"待对接"一句带过的**权限点/权限组**接口 `PERM-01～06`、`PERMGROUP-01～05` 与**自助 /me 面** `AUTH-15～18`（登记于登录功能组）；新增 `MGMT-09～MGMT-15`；并新增「鉴权边界（授权矩阵与租户隔离）」小节与 `MGMT-16～MGMT-18`（授权矩阵、令牌租户信任、SystemViewer 旁路、全局权限点）。本轮只做登记，未改任何接口、鉴权或运行数据。

2026-09-22 前端交接复核：在 `8b80461` 源码基线上补充四类页面交接说明；更正 AUTH-17 的邮箱/邀请依赖、平台 API Key 实际准入、MGMT-13 删除保护及 MGMT-16 的租户 0 描述；补充 ACCOUNT-06 平台用户名查重限制。此前“无错登”不代表本轮未发现描述问题。本轮仅改文档，没有修复后端或执行 HTTP 验收。

### 能力与身份边界

| 功能 | 已有实现 | 当前限制 |
| --- | --- | --- |
| 租户管理 | 列表/详情/查重、创建/编辑/删除；同时创建首管理员；设置套餐、有效期、启停/冻结状态；人数与用量查询；显式清理 Governance 本地数据 | 普通创建不自动建管理员；审核仅为状态字段；删除租户记录与清理数据是不同操作，没有下游资源统一销毁流程 |
| 租户管理员 | 开租户时复制管理员角色模板并绑定首管理员；也可通过用户接口增加本租户管理员、编辑资料/角色/状态、删除账号、重置密码；立即激活或邮件邀请 | `adminUserId` 是租户记录的主管理员指针，用户持有管理员角色是另一件事；用户已明确产品不提供主管理员移交，不再列为待实现项；现有源码未发现最后一个管理员保护 |
| 套餐 | 套餐增删改查；免费/标准/企业版本字段；模块白名单管理；用户数/存储/API 调用量配额配置；到期策略及数据保留天数字段 | 模块白名单已用于 API 和导航控制；没有统一的超配额拒绝逻辑；保留天数未找到自动清理消费方；不是订单/支付系统 |
| 平台运营账号 | 用户增删改查、分配系统角色、自定义角色及权限、状态修改、密码重置、在线会话查询与指定会话下线；默认管理员和本人有删除保护 | 账号 `tenantId=0`，角色类型 `SYSTEM`，平台登录要求至少一个 `platform:` 前缀角色并具有后台访问权限；种子只预置 `platform:admin`，无运营/只读角色模板 |

用户列表为共用接口：平台运营账号列表需明确筛选 `tenant_id=0`；租户管理员列表需筛选目标租户并结合管理员角色，不能把该租户所有用户都当管理员。租户登录上下文的创建操作会强制采用当前租户；平台上下文可指定目标租户。默认租户管理员授权包括用户管理和角色读取，不包括角色增删改及套餐/租户平台管理。

新增自定义平台角色可复用 `ROLE-*` 的 `permissions` 字段关联权限点；权限点里的 `menuIds`、`apiIds` 决定菜单与 API 授权。具体运营权限矩阵尚未指定，不能直接用 `sys:platform_admin` 充当“只读”。

### 鉴权边界（授权矩阵与租户隔离）

**授权矩阵**（首次种子 `sql/bootstrap/001_initial.sql`；行号取自两份**授权块**：平台块 `INSERT … WHERE p.code='sys:platform_admin'`（`313-497`，WHERE 在 `497`）、租户块 `WHERE p.code='sys:tenant_manager'`（`501-530`，WHERE 在 `532`）。该文件 `123-308` 是另一段"API 完整性校验目录"，**不是**授权块）。租户管理员实际有 **30 条**授权（`501-530`），其中 6 条为 2026-09-22 新增的 API Key 管理（`/api/v1/auth/api-keys*`，见 AK 功能组）。

| 接口族 | 平台管理员 | 租户管理员 | 平台块行号 / 租户块行号 |
| --- | --- | --- | --- |
| 租户 `/api/v1/admin/tenants*`（含 `:with-admin`、`:exists`、`/usage`、`/cleanup`） | 全 | **无** | 334,407-410,459-461,494 / 无 |
| 套餐 `/api/v1/admin/plans*`、`/plan-modules*`、`/plan-quotas*` | 全 | **无** | 328-330,389-393,449-451,488-490 / 无 |
| 权限点·权限组 `/api/v1/admin/permissions*`、`/permission-groups*` | 全 | **无**（仅 `GET /api/v1/admin/perm-codes` 只读权限码目录，515） | 326-327,385-388,446-448,486-487 / 无 |
| 角色 `/api/v1/admin/roles*` | 全 CRUD | 仅 `GET /roles`、`GET /roles/{id}`（只读） | 332,399-400,453,492 / 516-517 |
| API 目录 / 菜单 `/apis*`、`/menus*` | 全 | **无** | 314,322,341-343,370-371,418-419,435-436,474,483 / 无 |
| 在线会话 `/api/v1/admin/online-session/*`（含自助 `my-sessions`） | 全 | **无** | 376-377,443-444 / 无（见 MGMT-08） |
| 用户 `/api/v1/admin/users*`（建/读/改/删/重置密码） | 全 | **全**（限本租户） | 335-336,411-414,462-463,495 / 507-508,519-522,526-527,530 |
| 自助 `/api/v1/admin/me*` | 全 | **全** | 369,432-434,482 / 514,523-525,529 |
| API Key `/api/v1/auth/api-keys*`（2026-09-22 新增，见 AK 功能组） | 种子有绑定，但 Service 拒绝 `tenantId=0`，平台身份实际不可调用 | **全**（限本租户） | 313,337-338,417,472-473 / 501-506 |

权限码与绑定（`35-42`、`51-56`）：`platform:admin`（`tenant_id=0`，SYSTEM，受保护）绑定 `sys:access_backend` + `sys:platform_admin` 等 6 项；`template:tenant:manager`（`tenant_id=0`，TEMPLATE）只绑 `sys:access_backend` + `sys:tenant_manager`。`sys:access_backend` 是两角色共有的后台入口权限。

**租户隔离（运行时）**：请求主体 → ent viewer（`pkg/middleware/auth/auth.go:112-123`），user/tenant/orgUnit 均取自**校验后的主体**（JWT 或已验签的 AK/SK 主体，`auth.go:49-66`），不信任请求头；casbin domain 亦取主体租户（`utils.go:37-43`）。数据层由 `mixin.TenantID` + go-crud `TenantPrivacy`（查/建/改/删注入 `tenant_id=viewer`，跨租户改租户被拒）＋ 本仓 `TenantMutationGuardPolicy`（`ent/schema/tenant_mutation_guard.go:27-63`）双重约束。服务层再强制：`user_service.go:352-355`（强制 `TenantId=操作人租户`）、`370-395`（只允许**本租户 TENANT 类型**角色，否则 `some roles not found`）、`567-581`/`691-695`（跨租户改密拒绝，除非持 `sys:reset_others_credential`）、`role_service.go:160-162,194-196,208-210`（强制 `type=TENANT`、拒绝改 SYSTEM 角色）。这些普通管理入口的设计边界是：租户管理员操作本租户账号，不获得平台账号（`tenant_id=0`）或其他租户账号的管理权限。

上述源码边界不是完整授权边界的运行验收。MGMT-11 的自助角色写入仍绕过 Service 校验；关系表自身有 tenant_id 不能代替目标角色的租户一致性校验。2026-09-22 复核未运行跨租户 HTTP 负向验证。

| 编号 | 已核对的事实及影响 | 状态 |
| --- | --- | --- |
| MGMT-16 | viewer 的租户取自可信主体；JWT `tenant_id=0` 会绕过 Ent 租户过滤，但仍受角色和 Casbin domain/API 授权。AK/SK 与 JWT 不能混写：`signature.go`、`access_key_repo.go` 明确拒绝租户 0，AccessKey schema 也有正租户约束；管理接口 `keyManager` 拒绝平台身份。 | JWT 签发必须保证租户语义正确；不能把取消行过滤描述为“所有平台 API 全通”，也不能忽略 AK/SK 已有防线。2026-09-22 源码更正；本轮未做运行验收 |
| MGMT-17 | `appViewer.NewSystemViewerContext` 会**完全绕过**租户过滤（该 viewer `IsPlatformContext()`/`IsSystemContext()` 均为真、`TenantID()==0`，go-crud `rule/tenant.go:36-39,68-71` 对平台/系统上下文直接返回空谓词）。该旁路在**请求与认证路径上是刻意且广泛使用**的：登录（`authentication_service.go:294,874` 的 `resetContextForLogin`，注释说明为绕过 TenantPrivacy 以查到租户凭证）、找回密码（`authentication_forgot_password.go:27,75`）、邀请激活（`user_invitation.go:87`）、租户闸门中间件（`tenant_access_checker.go:45`）、租户用量（`tenant_usage_repo.go:74,154,256`）、站内信（`internal_message_service.go:508`）、策略评估日志（`policy_eval_logging_engine.go:94,113`）、启动重置策略（`rest_server.go:259`）、策略装载（`authorizer_provider.go:57`）、定时任务（`asynq_server.go:63`、`task_service.go:559`）与审计落地（`pkg/middleware/logging/*`、`audit_log_archive_repo.go:48`） | `user_service.go`/`role_service.go` 未使用（已确认）；该旁路**安全性取决于各调用点是否自行正确限定范围**，任何新增/改动若误用即成越权口，必须评审 |
| MGMT-18 | `sys_permissions`/`sys_permission_apis` **无 `tenant_id`**，权限点是全局资源；租户上下文下挂权限点不经租户校验，而组织单元有同租户校验（`role_org_unit_repo.go:38-58`）。 | 当前租户管理员**无角色写授权**，故为**潜在**项；一旦开放角色写，可把任意权限点挂到本租户 TENANT 角色（**租户内**提权，非跨租户） |

### 用户确认的范围（2026-09-21）

- 主管理员更换：产品不提供，不新增入口或移交流程；本轮也不借此删除现有字段或改写通用更新合同。
- 平台账号：允许多个不同用户名的 `tenantId=0` 账号，共享同一平台角色或分配不同系统角色；默认管理员的删除保护不限制创建其他账号。
- 运营/只读角色模板：等待后期 API 都接入后，再确定权限矩阵并补充种子；本轮不新增角色或授权。
- 密码重置：AES 协议与路径登记不一致只记录，暂不修复。
- 套餐扩展：可创建多个套餐并组合现有模块、配额值；当前模块来自固定 `Module` 枚举，数量配额类型只有 `USER_LIMIT`、`STORAGE`、`API_CALL`，不是界面新增名称就能得到新统计能力。
- 跨服务配额：另行讨论。建议先列实际需要的配额项，再逐项确定单位、租户维度、累计/当前占用口径、来源服务、统计更新方式以及是否需要超限拦截。Governance 维护套餐限额，各资源服务提供其实际用量；需要强制限额时再明确创建/释放资源中的额度校验与并发处理。本轮不扩展配额模型，也未自动创建新任务。

### 接口及报文

以下均需 Bearer 和相应 API 授权；租户上下文还受租户状态与套餐模块限制。普通新增传 `{data:{...}}`，更新传 `{data:{...},updateMask:...}`；列表使用 `pagination.PagingRequest` 并返回 `{items,total}`，详情直接返回对象，普通写操作返回 `google.protobuf.Empty`。新增租户连同管理员使用专用报文。所有目标风格均为“待指定”。

| 编号 | 方法与路径 | 请求或用途 |
| --- | --- | --- |
| TENANT-01 | `GET /api/v1/admin/tenants` | 分页、过滤租户；返回人数及主管理员名称 |
| TENANT-02 | `GET /api/v1/admin/tenants/{id}` | 租户详情 |
| TENANT-03 | `POST /api/v1/admin/tenants` | `data`；只创建租户记录 |
| TENANT-04 | `PUT /api/v1/admin/tenants/{id}` | `data`、`updateMask`；含 `planId`、`expiredAt`、`status` 等 |
| TENANT-05 | `DELETE /api/v1/admin/tenants/{id}` | 删除租户记录 |
| TENANT-06 | `POST /api/v1/admin/tenants:with-admin` | `{tenant:{...},user:{...},password,activationMode}`；租户、管理员、角色和凭证在同一事务创建 |
| TENANT-07 | `GET /api/v1/admin/tenants:exists` | `code` / `name` 查重，返回 `exist` |
| TENANT-08 | `GET /api/v1/admin/tenants/{id}/usage` | 用户数、API 审计记录数、套餐及配额；文件管理移除后 `storageUsedBytes` 固定为 0，不代表外部存储真实用量；不是计费周期用量 |
| TENANT-09 | `POST /api/v1/admin/tenants/{id}/cleanup` | 显式删除实现所列 Governance 租户数据，保留租户并置 OFF；不等于清理下游资源或对象存储文件 |
| ACCOUNT-01 | `GET /api/v1/admin/users` | 用户分页；按租户、角色等过滤；`query` 支持顶层 `role_code`（单值）与 `role_codes__in`（集合），详见 [ROLE-CODE-FILTER-01](#role-code-filter-01) |
| ACCOUNT-02 | `GET /api/v1/admin/users/{id}` | 用户详情 |
| ACCOUNT-03 | `POST /api/v1/admin/users` | `{data:{tenantId,username,roleIds,...},password,activationMode}`；角色必须与目标租户/系统类型匹配 |
| ACCOUNT-04 | `PUT /api/v1/admin/users/{id}` | `data`、`updateMask`，可另传 `password`；当前 Service 即使只改资料/状态也要求传有效角色 |
| ACCOUNT-05 | `DELETE /api/v1/admin/users/{id}` | 删除用户；拒绝删除默认平台管理员或操作者本人 |
| ACCOUNT-06 | `GET /api/v1/admin/users:exists` | `id` / `username` 二选一，返回 `exist`；用户名查重仅支持当前租户，平台身份按用户名调用会拒绝，不能额外传 `tenantId` 指定目标；平台页面改用 ACCOUNT-01 按目标租户＋用户名精确过滤 |
| ACCOUNT-07 | `POST /api/v1/admin/users/{user_id}/password` | `newPassword`；当前仍要求 AES 编码，与登录不同；路径目录问题见 MGMT-05 |
| ACCOUNT-08 | `GET /api/v1/admin/users/username/{username}` | 用户名读取别名；同名账号可能跨租户，平台管理优先使用 ID |
| ACCOUNT-09 | `DELETE /api/v1/admin/users/username/{username}` | 已注册的旧别名；Service 删除保护按 ID 查目标，不能据路由存在认定此别名可用，见 MGMT-06 |
| ROLE-01 | `GET /api/v1/admin/roles` | 按租户与 `type` 读取角色 |
| ROLE-02 | `GET /api/v1/admin/roles/{id}` | 角色详情与权限 ID |
| ROLE-03 | `POST /api/v1/admin/roles` | `data`，包含角色代码、类型和 `permissions` |
| ROLE-04 | `PUT /api/v1/admin/roles/{id}` | 修改角色及权限关联；受保护角色的类型、代码、状态等禁止修改 |
| ROLE-05 | `DELETE /api/v1/admin/roles/{id}` | 删除角色；受保护角色禁止删除 |
| PLAN-01 | `GET /api/v1/admin/plans` | 套餐列表 |
| PLAN-02 | `GET /api/v1/admin/plans/{id}` | 套餐详情 |
| PLAN-03 | `POST /api/v1/admin/plans` | `data`，含名称、版本、到期策略、保留天数 |
| PLAN-04 | `PUT /api/v1/admin/plans/{id}` | `data`、`updateMask` |
| PLAN-05 | `DELETE /api/v1/admin/plans?id=...` | ID 在查询参数，未使用详情路径 |
| PLAN-06 | `GET /api/v1/admin/plan-modules` | 按 `plan_id` 查模块白名单 |
| PLAN-07 | `GET /api/v1/admin/plan-modules/{id}` | 单条模块配置 |
| PLAN-08 | `POST /api/v1/admin/plan-modules` | `data`，关联套餐与模块 |
| PLAN-09 | `PUT /api/v1/admin/plan-modules/{id}` | `data`、`updateMask` |
| PLAN-10 | `DELETE /api/v1/admin/plan-modules?id=...` | 删除单条模块配置 |
| PLAN-11 | `GET /api/v1/admin/plan-quotas` | 配额列表；BFF 没有注册单条 GET |
| PLAN-12 | `POST /api/v1/admin/plan-quotas` | `data`，含 `planId`、`quotaType`、`quotaValue` |
| PLAN-13 | `PUT /api/v1/admin/plan-quotas/{id}` | `data`、`updateMask` |
| PLAN-14 | `DELETE /api/v1/admin/plan-quotas?id=...` | 删除配额项 |
| SESSION-01 | `GET /api/v1/admin/online-session/sessions` | `page`、`pageSize`、`keyword`；返回在线会话 |
| SESSION-02 | `POST /api/v1/admin/online-session/force-logout` | `userId`、`jti`、`clientType`；吊销指定会话，返回空对象 |
| SESSION-03 | `GET /api/v1/admin/online-session/my-sessions` | 无请求参数，身份取自认证上下文；返回本人会话列表（含 `current` 标记），供个人中心自助查看 |
| SESSION-04 | `POST /api/v1/admin/online-session/my-sessions/revoke` | `clientType`、`jti`；吊销本人指定会话，返回空对象 |
| PERM-01 | `GET /api/v1/admin/permissions` | 权限点分页；租户调用者仅见自身角色可达的权限点，平台调用者见全部 |
| PERM-02 | `GET /api/v1/admin/permissions/{id}` | 权限点详情；租户调用者对越权权限点返回 403 |
| PERM-03 | `POST /api/v1/admin/permissions` | `data`（code/name/groupId/status）；写后 `ResetPolicies` 失败会向上返回错误 |
| PERM-04 | `PUT /api/v1/admin/permissions/{id}` | `data`、`updateMask`；关联更新缺陷已于 2026-10-21 修复（掩码含关联字段则整体替换、不含则不动、nil 则全量替换），见 [关联更新语义](api-role-permission-ops.md#permission-update-mask-risk) 和 MGMT-09 |
| PERM-05 | `DELETE /api/v1/admin/permissions/{id}` | 删除权限点；`sys:*` 受保护清单未在本路径强制（见 MGMT-10） |
| PERM-06 | `POST /api/v1/admin/permissions/sync:perms` | 无请求体；先截断再重建非 `sys:*` 的权限组/权限点，破坏性同步，执行前需备份 |
| PERMGROUP-01 | `GET /api/v1/admin/permission-groups` | 权限组分页；`query` 支持顶层 `exclude_subtree`（排除某分组自身及全部后代），详见 [PERMGROUP-SUBTREE-01](#permgroup-subtree-01) |
| PERMGROUP-02 | `GET /api/v1/admin/permission-groups/{id}` | 权限组详情 |
| PERMGROUP-03 | `POST /api/v1/admin/permission-groups` | `data`；不触发 `ResetPolicies`（策略生成不读分组 module，非刷新缺口） |
| PERMGROUP-04 | `PUT /api/v1/admin/permission-groups/{id}` | `data`、`updateMask`；不触发 `ResetPolicies` |
| PERMGROUP-05 | `DELETE /api/v1/admin/permission-groups/{id}` | 先删组内权限点再删组，两步无共享事务，属破坏性级联 |

> 权限点与权限组此前只以"待对应功能对接时详细登记"一句带过，本轮按 `PERM-*`/`PERMGROUP-*` 正式登记；菜单与 API 目录的独立管理仍待对应功能对接时登记。

> 在线会话在源域只声明 2 个 RPC（`online_session.service.v1.OnlineSessionService.List/ForceLogout`），BFF `OnlineSessionService` 额外暴露了 `ListMyOnlineSession`/`RevokeMyOnlineSession` 两条自助路由（`api/protos/admin/service/v1/i_online_session.proto:27,34`），两者复用同一批源域消息。

登录/登出复用 AUTH-01～04，邮件接受邀请复用 AUTH-12。`activationMode=IMMEDIATE`（默认）不需要 SMTP；`EMAIL_INVITATION` 需邮件通道及邀请入口配置，不接受预设密码，账号待激活，邀请有效期 24 小时。当前无邀请重发/撤销管理 HTTP 接口。权限点与权限组已按 `PERM-01～06`、`PERMGROUP-01～05` 正式登记；自助 `/api/v1/admin/me` 面（`AUTH-15～18`）登记于登录功能组；菜单与 API 目录的独立管理仍待对应功能对接时详细登记，本轮未操作其同步接口。

### 已发现的限制与对接问题

| 编号 | 已核对的事实及影响 | 状态 |
| --- | --- | --- |
| MGMT-01 | 源领域 Proto 有 `AssignTenantAdmin`，但 admin BFF 无该路由，TenantService 无对应方法；通用更新可写 `adminUserId`，却不会同步角色或校验完整移交条件 | 2026-09-21 用户明确产品不需要；作为不实现项关闭，不新增移交功能 |
| MGMT-02 | 按用户授权补齐：状态实际写成非 NORMAL、或删除用户成功后，调用现有全客户端会话吊销；清理访问令牌、刷新令牌及在线会话；FieldMask 排除状态时不误踢；吊销失败明确报错，共用缓存清理不再覆盖前序错误。角色变更不在本轮修复范围 | 实现及定向回归 PASS；尚未部署，kind 验收 not_verified |
| MGMT-03 | 早期管理批次记录用户数 / API 调用量统计、STORAGE 用量固定 0 及保留天数缺少消费方。后续 GPU 已接入冻结计划与原子占额 / 退额账本，见 [GPU owner 合同](contracts/gpu-owner-integration-guide.md)，早期“未找到超限拦截”不代表当前所有资源路径 | 其他 quota_code 的计量来源和执行点按业务 owner 分别核对；历史统计批次不替代当前 GPU 合同或目标环境验收 |
| MGMT-04 | 到期 READONLY 在请求时判断；BLOCK_LOGIN/FREEZE 由每小时扫描修改状态，依赖 Asynq 调度与执行；延长有效期不会自动把已冻结/过期状态改回 ON | 实现已存在；本轮到期运行验收 not_verified |
| MGMT-05 | 用户密码路由注册为 `{user_id}`，嵌入 OpenAPI/首次种子登记为 `{userId}`；TenantAccessChecker 对路径模板做精确匹配，租户直调该接口可能在业务执行前被拒；密码更新/重置仍需 AES，登录已改原始密码 | 用户明确暂不修复，只登记；HTTP 复现 not_verified |
| MGMT-06 | 用户名删除路由虽已注册，UserService.Delete 总是先按 `req.GetId()` 查目标，用户名路由未提供该 ID；平台管理应使用 ACCOUNT-05 | 源码缺口；别名 HTTP 验收 not_verified |
| MGMT-07 | UserService.Update 无论 updateMask 是否涉及角色都校验非空 roleIds；创建普通用户的立即激活分支先提交用户，再创建密码凭证，后者失败不自动回滚已建用户 | 接入限制已确认；待指定修复，未修改 |
| MGMT-08 | 在线会话 4 条路由中，自助两条 `my-sessions`、`my-sessions/revoke` 在首次种子里只授给 `sys:platform_admin`；`sys:tenant_manager` 没有任何 online-session 授权，菜单 `OnlineSessionManagement` 的 `authority` 也只有 `sys:platform_admin`。因此租户管理员（及一般自服务场景）无法查看/下线自己的会话 | 源码与种子已确认；租户管理员是否放行自助会话待用户指定，未修改 |
| MGMT-09 | 仓储从 mask 移除关联字段后，通用更新原地过滤 DTO，再无条件替换 API / 菜单关联；HTTP PUT 的非 nil mask 经 Service 追加 updated_by，因此即使回传完整数组也会被清空，详见 [静态执行链](api-role-permission-ops.md#permission-update-mask-risk) | **2026-10-21 已修复**：`PermissionRepo.Update` 在剔除关联路径前先快照关联载荷，并按 `hasPath` 条件调用 `AssignApis`/`AssignMenus`（掩码含关联字段才替换、不含则不动）。回归测试 `TestPermissionRepoSqlite_UpdateKeepsRelationGrants` 4 子测试通过，且修复前该测试 FAIL（已复现缺陷）。遗留：主表与关联非同事务、三处 nil mask 语义不统一，未并入本次 |
| MGMT-10 | `constants.ProtectedPermissionCodes`（`sys:*` 共 7 个）在 `PermissionService.Delete` 路径未被强制校验，删除保护只落在角色层（`ROLE-05`） | 受保护权限点可被直接删除，导致鉴权与菜单静默失效；是否补删除保护待指定 |
| MGMT-11 | `PUT /api/v1/admin/me`（`UserProfileService.UpdateUser`，`user_profile_service.go:81-96`）直接调 `userRepo.Update`，跳过 `UserService.Update` 的角色类型/租户校验与"至少一个角色"约束；而 `user_repo.go` 在掩码含 `role_ids` 时会应用 `roleIds` | 源码观察（**未运行验证**）：本人可借自助接口改动自身角色集合，形成自助提权面；是否收紧待用户指定 |
| MGMT-12 | `tenant.admin_user_id` 是自由指针（`TenantRepo.AssignTenantAdmin`），无外键/级联或一致性校验；`UserService.Delete` 删除主管理员用户后**不清空**该指针 | 租户记录留下悬空主管理员引用，`List`/`Get` 的 `adminUserName` 静默为空；与 MGMT-01（移交缺失）是不同问题 |
| MGMT-13 | 平台账号复用 `/api/v1/admin/users`；删除保护覆盖 `id==1`、（`username==admin` 且 `tenantId==0`）以及操作者本人。平台登录还要求 `platform:*` 角色及后台访问权限，不能仅凭 `SYSTEM` 类型判定可登录。 | 删除非默认账号不必然导致失去管理员；缺口是停用、移除角色等更新没有“最后一个有效管理员”保护，默认账号被停用或失去管理角色后亦可能失去管理入口。2026-09-22 更正；是否补保护待指定 |
| MGMT-14 | 租户仅注册 `GET /api/v1/admin/tenants/{id}`（绑定 id），源消息的 `query_by code/name` 无 HTTP 入口；`cleanup` 不删租户行（置 OFF），`code`/`name` 仍被 `TenantExists` 占用 | 按 code/name 读取须列表过滤；清理后无法以同 code/name 重新 `with-admin` 建租户 |
| MGMT-15 | `PlanModuleService`/`PlanQuotaService` 的 Update 回调不写 `planId`（仅 updated_by/updated_at 与 module/quota 字段）；三者均无 HTTP `Count` 路由，源域 `TenantService.Count`/`BatchCreate` 亦无 HTTP 入口；`cleanup` 声明 `body:"*"` 但请求体无业务语义 | 白名单行/配额行的套餐归属不能经 PUT 改挂，只能删后重建；批量建租户须循环调用；登记为契约冗余，未改代码 |

### MGMT-02 修复及验证边界

复用现有 `RevokeUserTokenAllClientTypes`，不新增接口、表、迁移或依赖。状态更新后读取实际持久化状态，避免请求带 `status` 但 FieldMask 排除它时误踢用户；删除仅在仓储成功后吊销目标账号。访问令牌、刷新令牌及会话元数据均清理，其他用户保持原会话。

数据库修改与 Redis 吊销不是同一事务。吊销失败返回明确错误，不谎报成功：状态更新失败提示可重试同一状态更新；已删除账号应通过在线会话管理清理残留会话。本次不新增分布式事务或并发登录/刷新栅栏，相关并发竞态未验收。密码重置协议、用户名删除别名、角色变更后的即时权限更新均不在本次修改范围。

验证使用 Ubuntu 独立源码目录、Go 1.26.7、SQLite 和 miniredis；使用独立源码快照，不覆盖远端工作区，不修改 kind 运行实例。验证记录：

- 在未修复源码上运行新增回归：成功复现停用/锁定/删除后旧令牌仍有效，以及缓存部分清理错误被覆盖。
- 修复后执行 `go test ./app/admin/service/internal/data ./app/admin/service/internal/service -run '^(TestUserTokenCache|TestUserService|TestAuthSvcSqlite_)' -count=1`：PASS。
- 新增用例覆盖平台/租户账号停用与删除、锁定、多会话和两类客户端缓存清理、其他账号不受影响、资料修改不踢人、FieldMask 排除状态、受保护账号删除拒绝、Redis 故障明确报错；共用缓存测试验证前序错误不被后续成功覆盖。后台令牌使用真实签名和校验；app 端仅覆盖现有缓存清理，不声称已有 app 签发实现。
- 服务入口 `go build ./app/admin/service/cmd/server` 编译：PASS。未重新部署，真实 PostgreSQL/Redis HTTP 验收为 `not_verified`。
- 远端证据目录：`/home/ubuntu/Workspace/.codex-runs/governance-account-sessions-20260921/`（`baseline.log`、`regression.log`、`build.log`）。

源码入口：[租户业务](../app/admin/service/internal/service/tenant_service.go)、[用户业务](../app/admin/service/internal/service/user_service.go)、[角色业务](../app/admin/service/internal/service/role_service.go)、[套餐业务](../app/admin/service/internal/service/plan_service.go)、[租户模块检查](../app/admin/service/internal/data/tenant_access_checker.go)、[用量与到期任务](../app/admin/service/internal/data/tenant_usage_repo.go)、[首次种子](../sql/bootstrap/001_initial.sql)、[HTTP 装配](../app/admin/service/internal/server/rest_server.go)。

整合记录：已拉取 `11fa857`，源码改动无文本冲突；原未跟踪的 `docs/onboarding-invite-audit.md` 与远端新增版本内容一致，保留远端跟踪版本。同步修正文件管理移除后存储用量固定返回 0 的登记说明；整合后的相同定向回归与服务入口编译均 PASS（Go 1.26.7）；证据目录为 Ubuntu `/home/ubuntu/Workspace/.codex-runs/governance-account-main-20260921/`，包含 `regression.log` 和 `build.log`。未部署到 kind。

整合记录（2026-09-22）：本轮登记与远端 `0c8e567`（AK-SK 签名接入 / Network VPC 校验）及合并提交 `7c8f574` 整合。该提交把访问密钥路由由 `/api/v1/admin/access-keys*` 改为 `/api/v1/auth/api-keys*`，为租户管理员新增 6 条 AK 授权（租户块由 24 条增至 30 条，现为 `501-530`），重写 `pkg/middleware/auth/auth.go`（viewer 构造移至 `112-123`，新增已验签 AK/SK 主体路径 `49-66`），并移除了 `access_key_repo.go` 中的 SystemViewer 调用。本文件「鉴权边界（授权矩阵与租户隔离）」小节的全部行号引用已据此**重算**（平台块 `313-497`、租户块 `501-530`），并新增「API Key」一行及与 AK 功能组的交叉引用。同步修正登录路径的 SystemViewer 引用行号（`294,874`）。登记为文档改动，未改任何接口或运行代码。

<a id="frontend-management-handoff"></a>

## 前端交接：用户、租户、租户管理员、平台管理员页面

交接日期：2026-09-22。源码基线：`8b80461`。范围为上述四类页面及必要的登录、角色选择、套餐选择、会话配套。可将本节完整转发给前端。

**“可接”表示当前源码存在对应路由与实现，可开始开发和联调；不表示目标环境已部署或端到端验收通过。** 本次只核对源码与报文并编写文档；目标环境 HTTP、前端、隔离验收为 `not_verified`。本节“暂缓”是建议的第一批页面范围，不代表后端已关闭这些路由。

### 1. 通用请求与响应

- `BASE_URL` 使用联调环境提供的 Governance 地址；以下路径原样使用，不统一替换 `/api/v1` 和 `/admin/v1`。
- 受保护请求带 `Authorization: Bearer <access_token>`；JSON 请求带 `Content-Type: application/json`，接受 JSON 响应。浏览器登录、刷新需保存并发送 Cookie，建议经同源代理访问。
- 普通创建是 `{ "data": { ... } }`，普通更新是 `{ "data": { ... }, "updateMask": "字段1,字段2" }`。登录和 `tenants:with-admin` 等专用接口按下文报文发送。
- HTTP JSON 的 `updateMask` 是逗号分隔的 **lowerCamelCase 字符串**，例如 `"nickname,roleIds"`、`"planId,expiredAt,status"`；不是 `{ "paths": [...] }`，也不要发送 `"role_ids"`。后端解析后才转换为 snake_case。更新始终明确传 mask，不使用 `allowMissing`。
- 路径中的 `{id}` 替换为真实数字，GET 不带请求体。普通实体 ID 为 JSON number；`total`、用量等 int64/uint64 字段按 JSON 十进制字符串处理，界面计数可在确认安全整数范围后转 number。
- 枚举使用字符串名；时间使用 RFC 3339，例如 `"2027-09-22T00:00:00Z"`，不能发送毫秒时间戳。可选字段可能缺失；不要假设 `null` 或省略字段能清空数据库值。
- 成功响应直接是业务对象，没有统一 `{code,data}` 外壳。列表为 `{ "items": [...], "total": "2" }`；详情直接返回 User/Tenant/Role/Plan；普通创建、更新、删除返回 `{}`，**不返回新建 ID**。创建成功后刷新对应筛选列表获取结果。
- 错误使用非 2xx HTTP 状态，JSON 为 `{ "code": 403, "reason": "FORBIDDEN", "message": "...", "metadata": { "request_id": "..." } }` 这类结构；`reason` 的实际值按响应处理。401 走刷新/重新登录，403 提示权限或租户套餐限制，不能把所有 403 都当登录过期。保留 request_id 便于联调。
- 不提交 `createdBy/updatedBy`、统计字段等只读数据。用户响应中的邮箱/手机可能脱敏或被 `hiddenFields` 隐藏；编辑时只提交实际修改的字段，不能把整个详情对象原样写回。

### 2. 登录及页面初始化

| 编号 | 方法与路径 | 请求 | 响应 / 页面行为 |
| --- | --- | --- | --- |
| AUTH-02 | `POST /api/v1/auth/platform/password/login` | `{username,password}`，原始密码 | 平台管理员登录 |
| AUTH-01 | `POST /api/v1/auth/password/login` | `{tenant_name,username,password}`，原始密码 | 租户用户及租户首管理员登录；`tenant_name` 填租户 `code`，不是显示名称 |
| AUTH-05 | `GET /api/v1/admin/me` | Bearer，无参数 | 当前 User；用于确认当前 `id/tenantId/roles` |
| AUTH-06 | `GET /api/v1/admin/initial-context` | Bearer，无参数 | `{menus,permissions,hiddenFields}`，用于导航、按钮和字段展示 |
| AUTH-04 | `POST /api/v1/auth/refresh` | `{}`，携带 `refresh_token` Cookie，无需 Bearer | 新访问令牌；客户端串行刷新，旧令牌对失效 |
| AUTH-03 | `POST /api/v1/auth/logout` | Bearer，`{}` | `{status:"revoked"}`；吊销该用户全部后台会话并清 Cookie |
| AUTH-07/08 | `GET /api/v1/admin/routes`、`GET /api/v1/admin/perm-codes` | Bearer | 单独导航 `{items}`、权限 `{codes,hiddenFields}`；用了 AUTH-06 可不重复调用 |

正常登录/刷新响应为 `access_token`、`expires_in`（秒，字符串）和 `issued_at` 等字段。刷新令牌从 HttpOnly Cookie 读取，不依赖响应体的 `refresh_token`。密码登录不再使用 AES、Base64 或图形验证码。

若登录返回 `mfa_required=true`，当前尚未完成登录，不能进入管理页；按既有 AUTH-09（`POST /api/v1/auth/mfa/verify`）处理二次验证，其响应与普通登录不同，见登录功能组。不要因首批未制作 MFA 页面而把该中间态当成功。

### 3. 列表、筛选与分页

`GET /api/v1/admin/users` 默认使用 `limit` + `cursor`，响应为 `{items,next_cursor,total}`；首次不传 cursor，后续原样回传上一页的 next_cursor。参数边界、签名绑定、稳定排序和旧页码兼容见 [存量游标合同](contracts/cursor-pagination-legacy.md)，历史验收见 [LIST-CURSOR-01](#list-cursor-01)。租户、角色、套餐等其余 admin 列表仍使用 `page`、`pageSize`，从第 1 页开始，不具备 users 的游标语义。

各列表的 `query` 是 **JSON 字符串**，通过 URL 参数编码发送；筛选字段采用数据库 snake_case，与 JSON body 的 camelCase 分开。

```javascript
// 示例 ID 须替换为接口查到的值。此例由有权限的平台账号读取租户 12 用户的首页。
const params = new URLSearchParams({
  limit: '20',
  query: JSON.stringify({ tenant_id: '12' }),
  orderBy: JSON.stringify(['-id']),
});
const url = `${BASE_URL}/api/v1/admin/users?${params.toString()}`;
// 使用项目统一请求客户端，并附加 Bearer。
// 下一页设置 cursor=response.next_cursor，保留相同 query/orderBy；末页停止。
```

| 需求 | `query` 的对象内容（发送时 JSON.stringify） |
| --- | --- |
| 平台账号列表 | `{ "tenant_id": "0" }` |
| 指定租户的全部用户 | `{ "tenant_id": "12" }` |
| 指定租户管理员列表 | `{ "tenant_id": "12", "role_id": "34" }`，34 为该租户管理员角色 ID |
| 用户名包含搜索 | `{ "tenant_id": "12", "username__contains": "alice" }` |
| 平台替指定租户检查用户名 | `{ "tenant_id": "12", "username": "alice" }`，查 `items/total` |
| 查租户管理员角色 | 对 `/api/v1/admin/roles`：`{ "tenant_id": "12", "type": "TENANT", "code": "tenant:manager", "status": "ON" }` |
| 查默认平台管理员角色 | 对 `/api/v1/admin/roles`：`{ "tenant_id": "0", "type": "SYSTEM", "code": "platform:admin", "status": "ON" }` |
| 租户名称包含搜索 | 对 `/api/v1/admin/tenants`：`{ "name__contains": "示例" }` |
| 创建租户后定位记录 | 对 `/api/v1/admin/tenants`：`{ "code": "example-team" }` |

裸字段是精确条件，文本模糊搜索显式使用 `__contains`，ID 不做模糊匹配。单角色过滤用顶层 `role_id`；多角色用顶层 `"role_ids__in": ["34","35"]`。不要写成 `role_ids:[34,35]`，也不要把角色条件藏在 `$and/$or` 子树中。管理员列表由后端过滤后分页，不在前端拿一页普通用户再筛管理员。

角色/套餐下拉可在范围受控时使用 `noPaging=true`，仍携带必要的 `query`；当前依赖默认上限为 10,000 行，不能当作无限全量导出。页码列表建议 `pageSize=20`，默认上限 100；users 的 limit 默认 20，范围 1～100，不与旧窗口参数混用。当前租户登录只能读取本租户允许的数据，不能靠修改筛选参数切换身份。

### 4. 用户管理：三个账号页面共用

平台管理员可管理平台及目标租户账号；默认租户管理员仅管理本租户。普通用户是否有管理权限取决于其角色，不能因能登录而显示所有管理按钮。

| 编号 | 方法与路径 | 请求 / 响应 | 第一批接入说明 |
| --- | --- | --- | --- |
| ACCOUNT-01 | `GET /api/v1/admin/users` | 默认 `limit/cursor` → `{items,next_cursor,total}`；显式页码兼容见 [LIST-CURSOR-01](#list-cursor-01) | 列表、查重预检查、创建后定位 |
| ACCOUNT-02 | `GET /api/v1/admin/users/{id}` | 无 body → User | 查看及编辑前读取最新角色/状态 |
| ACCOUNT-03 | `POST /api/v1/admin/users` | `{data,password,activationMode}` → `{}` | 新增普通用户、追加租户管理员、创建平台管理员共用 |
| ACCOUNT-04 | `PUT /api/v1/admin/users/{id}` | `{data,updateMask}` → `{}` | 资料、角色、启用/禁用共用；不在资料更新中夹带 password |
| ACCOUNT-05 | `DELETE /api/v1/admin/users/{id}` | 无 body → `{}` | 按 ID 删除；服务端拒绝删除默认平台管理员及本人 |
| ACCOUNT-06 | `GET /api/v1/admin/users:exists` | `?username=alice` 或 `?id=123` → `{exist}` | username 仅当前租户可用；平台用户名查重改用列表精确查询，不能附加 tenantId 绕过 |
| ROLE-01 | `GET /api/v1/admin/roles` | 共用分页 → `{items,total}` | 获取目标租户可分配角色；平台角色筛 `tenant_id=0,type=SYSTEM` |
| ROLE-02 | `GET /api/v1/admin/roles/{id}` | 无 body → Role | 查看角色 `code/type/tenantId/permissions`；permissions 是权限点 ID 数组 |

用户主要显示字段：`id,tenantId,tenantName,username,nickname,realname,roleIds,roles,roleNames,status,email,mobile,createdAt,updatedAt`。角色显示名称不能作为写入标识，写入使用 `roleIds`。

创建示例：`POST /api/v1/admin/users`。`12/34` 为查询所得租户/角色 ID；密码占位符必须替换为用户输入并通过服务端密码策略。

```json
{
  "data": {
    "tenantId": 12,
    "username": "alice",
    "nickname": "Alice",
    "roleIds": [34],
    "status": "NORMAL"
  },
  "password": "<用户输入的原始密码>",
  "activationMode": "IMMEDIATE"
}
```

立即激活时页面要求明确输入密码，不依赖后端默认密码。创建用户和密码凭证目前不是一个事务；若返回失败，应按租户和用户名重新查列表确认是否已建用户，不能无条件自动重试创建（MGMT-07）。

资料更新示例：`PUT /api/v1/admin/users/123`。

```json
{
  "data": {
    "tenantId": 12,
    "roleIds": [34],
    "nickname": "新的昵称",
    "remark": "新的备注"
  },
  "updateMask": "nickname,remark"
}
```

**每次用户更新都带真实目标 `tenantId` 和完整有效 `roleIds`**，即使 mask 只改昵称或状态；这是当前 Service 的校验要求。普通字段更新的 mask 不加 `roleIds`，防止覆盖并发角色变化。平台更新租户账号漏传 `tenantId`，会按 SYSTEM 角色校验并可能报 `some roles not found`。

- 启停：同一 PUT，`data` 保留 `tenantId/roleIds` 并传 `status:"DISABLED"` 或 `"NORMAL"`；`updateMask:"status"`。
- 分配角色：同一 PUT，传目标完整 `roleIds`，`updateMask:"roleIds"`；不允许传空角色数组。角色保存没有吊销旧 JWT，不能提示“已有会话权限立即失效”；刷新/重登才重载用户角色。
- 第一批资料字段可用 `nickname/realname/email/mobile/telephone/region/address/description/remark/gender`；用户名、租户归属保持只读，不提供迁租、改用户名或头像编辑。组织/岗位关联不在本次四页面接入范围。
- 管理侧修改资料中的 email 不等于完成邮箱验证，也不自动建立找回密码所需的邮箱凭证；邀请码接受与 AUTH-17/18 邮箱验证是独立流程。
- 用户状态全集为 `NORMAL/DISABLED/PENDING/LOCKED/EXPIRED/CLOSED`，状态展示需兼容全部值；手动启停使用前两种，邀请待激活不等于管理员手动启用流程。
- 禁用/删除可能出现数据库成功、会话吊销失败而返回 500；页面重新查询记录/状态，展示服务端提示，不把报错简单解释为“没有发生修改”。

### 5. 租户管理员与平台管理员的页面规则

| 页面 | 列表条件 | 创建 / 编辑角色 | 登录入口 |
| --- | --- | --- | --- |
| 租户用户管理 | 指定 `tenant_id`；默认含管理员，可按角色进一步筛选 | 该租户的 TENANT 角色 | AUTH-01 |
| 租户管理员管理 | 指定 `tenant_id` ＋ 该租户 `tenant:manager` 的角色 ID | 复用 ACCOUNT-03/04，绑定查询到的本租户管理员角色 | AUTH-01，含首管理员 |
| 平台管理员管理 | `tenant_id=0`；若只列管理员，再加 `platform:admin` 对应角色 ID | 第一批使用现有 `platform:admin`；角色类型 SYSTEM | AUTH-02 |

三个页面复用同一用户 CRUD，不新增“管理员账号”接口。`tenant_id=0` 是平台范围，不是“当前租户”的占位值；不要在租户用户请求中随意使用 0。

`adminUserId` 是租户记录中的主管理员指针；它不代表所有具有管理员角色的用户。产品不做主管理员移交。默认租户管理员只有角色读取权限，没有角色增删改权限；“给用户分配已有角色”和“编辑角色的权限”是两项功能。

首次种子只提供平台管理员和租户管理员模板，**没有普通用户、平台运营、平台只读的完整角色模板**。普通用户页面需要后端/平台先准备对应 TENANT 角色，不能默认把管理员角色当作普通角色。第一批不制作自定义权限矩阵。

删除、禁用、撤销管理员角色前需要确认目标；当前没有“最后一个有效管理员”后端保护，删除租户主管理员也不会自动清空 `adminUserId`。建议首批暂不开放主管理员删除、管理员降权等易导致失去管理入口的操作（MGMT-12/13）；页面限制不代替后端保护。

### 6. 租户管理

本组为平台侧页面；默认租户管理员无 TENANT-* 及 PLAN-* 授权。

| 编号 | 方法与路径 | 请求 / 响应 | 第一批接入说明 |
| --- | --- | --- | --- |
| TENANT-01 | `GET /api/v1/admin/tenants` | 共用分页 → `{items,total}` | 列表、租户选择、创建后定位 |
| TENANT-02 | `GET /api/v1/admin/tenants/{id}` | 无 body → Tenant | 详情 |
| TENANT-06 | `POST /api/v1/admin/tenants:with-admin` | `{tenant,user,password,activationMode}` → `{}` | 新开租户推荐入口，原子创建租户、首管理员、角色、凭证 |
| TENANT-03 | `POST /api/v1/admin/tenants` | `{data:{...租户字段}}` → `{}` | 仅创建租户记录，不生成管理员/租户管理员角色；不作为默认开通按钮 |
| TENANT-04 | `PUT /api/v1/admin/tenants/{id}` | `{data,updateMask}` → `{}` | 编辑资料、状态、套餐、有效期 |
| TENANT-07 | `GET /api/v1/admin/tenants:exists` | `?code=example-team` 或 `?name=示例团队` → `{exist}` | 至少传一项；两项同时传时任一重复即 true；不支持排除当前记录 ID |
| TENANT-08 | `GET /api/v1/admin/tenants/{id}/usage` | 无 body → TenantUsage | 只读用量；`storageUsedBytes=0` 为当前占位，不能标记为外部存储真实用量 |
| PLAN-01 | `GET /api/v1/admin/plans` | 共用分页 → `{items,total}` | 套餐选择器，使用返回的真实 id |
| PLAN-02 | `GET /api/v1/admin/plans/{id}` | 无 body → Plan | 展示所选套餐名称、到期策略等 |

主要租户展示字段：`id,name,code,status,type,auditStatus,planId,expiredAt,adminUserId,adminUserName,memberCount,createdAt`。审核状态目前是普通字段，不代表独立审批流程。`TenantUsage` 返回 `tenantId,userCount,storageUsedBytes,apiCallCount,planId,planName,quotas`，API 次数不是计费周期统计，quota 也不代表已实现超额拦截。

开通示例：`POST /api/v1/admin/tenants:with-admin`。套餐 ID 5 须换成实际允许后台管理模块的套餐。

```json
{
  "tenant": {
    "name": "示例团队",
    "code": "example-team",
    "status": "ON",
    "planId": 5
  },
  "user": {
    "username": "tenant_admin",
    "nickname": "租户管理员",
    "status": "NORMAL"
  },
  "password": "<用户输入的原始密码>",
  "activationMode": "IMMEDIATE"
}
```

`user.tenantId/roleIds` 和 `tenant.adminUserId` 不由页面填写，由服务端生成绑定。建号成功后刷新列表，按租户 `code` 精确取得新租户。租户必须启用、绑定包含所需模块的套餐，且实际 API 目录和角色绑定齐全，租户账号才能使用管理页；这些是环境前提，前端不用调用 API 同步或修改种子。

`tenants:with-admin` 返回失败时，也先按 code 查询租户及首管理员。数据库事务提交后还会重载策略，重载失败可导致“已创建但响应报错”；不要无条件自动重试创建，由后端确认持久化结果并重载策略。

更新示例：`PUT /api/v1/admin/tenants/12`。

```json
{
  "data": {
    "planId": 5,
    "expiredAt": "2027-09-22T00:00:00Z",
    "status": "ON"
  },
  "updateMask": "planId,expiredAt,status"
}
```

套餐实际关联用 `planId`，`subscriptionPlan` 文本不能代替它。只改状态时 `data:{status:"OFF"}`、`updateMask:"status"`。租户状态为 `ON/OFF/FREEZE/EXPIRED`；延长有效期不会自动将冻结/过期状态恢复为 ON，页面应根据操作者意图明确提交状态。修改 code 会影响后续租户登录输入，应作明显提示。不开放 `adminUserId` 更新。

### 7. 可选配套：邀请与平台会话

**邮件邀请**复用 ACCOUNT-03 或 TENANT-06：设置顶层 `activationMode:"EMAIL_INVITATION"`，移除 password，普通建号增加 `data.email`，建租户增加 `user.email`。服务端会将账号设为 PENDING；须先具备可用 EMAIL 渠道和邀请 origin。邀请 24 小时有效，目前无重发/撤销管理接口，第一批可以只接立即激活。

自建接受邀请页时调用 AUTH-12：`POST /api/v1/auth/invitations/accept`，body 为 `{ "token": "<邮件链接片段中的令牌>", "password": "<用户输入的原始密码>" }`，无需 Bearer，成功返回 `{}`，随后进入原平台/租户登录页；不自动登录。邀请接受不依赖先调用绑定邮箱接口。

**平台会话管理**使用下表；默认租户管理员四条均未授权，第一批不在租户页面展示可操作入口。

| 编号 | 方法与路径 | 请求 | 响应 |
| --- | --- | --- | --- |
| SESSION-01 | `GET /api/v1/admin/online-session/sessions` | `page=1&pageSize=20&keyword=alice`；keyword 按用户名/IP 匹配 | `{items,total}` |
| SESSION-02 | `POST /api/v1/admin/online-session/force-logout` | `{userId,jti,clientType}`，均取目标会话记录 | `{}`；下线指定会话 |
| SESSION-03 | `GET /api/v1/admin/online-session/my-sessions` | 无参数 | `{items,total}`，本人会话，含 `current` 标记 |
| SESSION-04 | `POST /api/v1/admin/online-session/my-sessions/revoke` | `{jti,clientType}` | `{}`；下线本人指定会话 |

会话项含 `jti,userId,username,tenantId,clientType,ipAddress,userAgent,deviceId,loginAt`；`clientType` 为 `"admin"` 或 `"app"`，原样回传。`current` 仅在本人列表有意义。管理会话列表不支持独立 userId/tenantId 精确筛选参数，不能把前端过滤一页的结果和全局 total 当成某用户的完整会话分页。

### 8. 第一批暂缓项与后端跟进

| 接口 / 操作 | 暂缓原因或接入前置 |
| --- | --- |
| AUTH-15 `PUT /api/v1/admin/me` | 直接进入通用用户更新，可写角色；必须先由后端限定自助字段（MGMT-11）。页面隐藏字段无法消除已暴露接口的风险，正式开放给真实用户前需处理 |
| ACCOUNT-07 `POST /api/v1/admin/users/{user_id}/password` | 仍要求 AES+Base64，且 HTTP 路径模板与 API 目录不一致（MGMT-05）；建议本批先不开放重置密码按钮 |
| ACCOUNT-04 顶层 password、AUTH-16 `POST /api/v1/admin/me/password` | 同样仍是旧 AES 协议；不能复用创建/登录明文提交，也不以普通 PUT 夹带密码绕过前述问题 |
| ACCOUNT-08/09 用户名详情/删除别名 | 使用 ID 入口；用户名删除有实现缺口，平台同名账号存在跨租户歧义（MGMT-06） |
| ROLE-03～05、PERM-*、PERMGROUP-* 自定义角色与权限写入 | 四类页面先选择已有角色。运营/只读权限矩阵待定；删除保护不完整（MGMT-10/18）。权限点 PUT 清绑定一条（MGMT-09）已于 2026-10-21 修复，不再构成暂缓理由 |
| TENANT-05 `DELETE /api/v1/admin/tenants/{id}` | 仅删除租户记录，不等于关联数据及下游资源清理；首批普通管理流程不开放 |
| TENANT-09 `POST /api/v1/admin/tenants/{id}/cleanup` | 删除实现列出的 Governance 本地数据，保留租户并置 OFF；不清下游资源，code/name 仍占用，需单独设计操作流程 |
| 主管理员移交 | 产品明确不提供，不调用通用更新改 adminUserId 代替移交 |
| 平台身份调用 API Key 管理 | 当前 Service 拒绝 tenantId=0；本批平台管理员页面不据种子授权矩阵添加该入口 |

前端可先完成列表/详情、现有角色选择、立即激活创建、管理侧基础编辑与启停；按上述限制处理错误和状态。后端应先收紧 `/me`，再按选定范围修复密码重置、角色变更后的旧会话和管理员保护问题。以上为交接建议，未在本次文档工作中实施修复或改变此前产品范围。

### 9. 联调检查与源码依据

联调至少分别使用平台管理员、租户 A 管理员、租户 B 管理员核对：正确列表范围、管理员角色筛选、创建后登录、更新 mask、启停与会话失效、无权限按钮/403、A 无法读写 B 的账号。邮件/MFA/会话功能在选择接入时另验。当前这些目标环境结果均为 `not_verified`，不得仅用 HTTP 200 或菜单出现判定完成。

接口以已注册 BFF 与 Service 为准，不能只按源领域 RPC 名或 OpenAPI 路径模板推导可用性。

- 路由：[用户](../api/protos/admin/service/v1/i_user.proto)、[租户](../api/protos/admin/service/v1/i_tenant.proto)、[角色](../api/protos/admin/service/v1/i_role.proto)、[套餐](../api/protos/admin/service/v1/i_plan.proto)、[认证](../api/protos/admin/service/v1/i_authentication.proto)、[会话](../api/protos/admin/service/v1/i_online_session.proto)。
- 报文：[User](../api/protos/identity/service/v1/user.proto)、[Tenant / TenantUsage](../api/protos/identity/service/v1/tenant.proto)、[Role](../api/protos/permission/service/v1/role.proto)、[Plan](../api/protos/identity/service/v1/plan.proto)、[登录](../api/protos/authentication/service/v1/ani_auth.proto)、[会话](../api/protos/online_session/service/v1/online_session.proto)。
- 实现：[用户校验与会话吊销](../app/admin/service/internal/service/user_service.go)、[用户筛选及查重](../app/admin/service/internal/data/user_repo.go)、[租户与首管理员开通](../app/admin/service/internal/service/tenant_service.go)、[邀请使用说明](onboarding.md)、[部署和权限目录前提](deployment.md)。

## 功能组：API Key / AK-SK

### 独立推理 Key HTTP 管理（INF-KEY-01～05，2026-10-28）

Governance 提供 `/api/v1/inference-keys` 创建/列表、`/{key_id}` 详情/撤销及
`/{key_id}:rotate` 轮换；完整报文和五个内部 RPC 见
[推理调用 Key 合同](contracts/inference-keys.md)。要求租户用户 JWT、
`inference:key:create/read/revoke/rotate` 和有效的 `INFERENCE` 套餐。
平台 AK/SK、推理 Key 和平台租户 0 不能管理这些凭证。

Gov 通过持久化租户映射与现有受管 mTLS 连接调用 Inference；凭证由 Inference
签发和持久化。按用户最终要求，INF-KEY-02/03 列表各项和详情通过 `secret`
返回完整当前 AK；创建/轮换首次响应继续交付，写重放、撤销响应和审计不包含明文。
Inference 以独立挂载主密钥加密持久化，迁移前仅有哈希的记录在成功调用后补存原值，
不自动轮换；未补存的旧记录不含 `secret`，解密故障返回 503。新行为的源码验证与
目标环境部署分别报告，以下旧验收记录不代表本次读回功能已部署。
Key 与平台 AK/SK 的路由、权限和存储独立；这些管理接口不进入 Higress。
新增 API 目录须先 dry-run 再同步，并显式配置权限、角色及套餐后刷新 Casbin。

配套 Inference 修复使列表和 total 使用同一只读 REPEATABLE READ 快照；创建仅对
租户内创建幂等键执行 ON CONFLICT DO NOTHING，并在 READ COMMITTED 事务读取
并发获胜记录。两个并发请求只产生一个 Key、一次 secret 交付和一条创建审计；
异意图重放为 409，其他约束失败仍回滚，不转换成幂等重放。

`scripts/deploy/service-mtls/` 提供现有续期重载 Job 的受限配置与源码；
`ani-inference-gateway-authz-mtls` 同时进入精确 Secret 读取权限和 Inference
工作负载绑定，避免鉴权 companion 在证书续期后仍使用旧连接身份。

本地完整 API 生成、构建及相关回归通过；Gov 的外部 owner 用明确协议替身，
Inference 并发、快照和事务行为用独占 PostgreSQL 验证。用户指定的
`kubernetes-admin@ani-kcn-full` 已完成真实登录、创建/列表/详情、并发幂等、
真实模型生成、无/错/旧/撤销 Key 401、模型越权及伪造模型头 403、小时限额 429、
轮换/撤销和 Higress 管理路径 404 验收。专用鉴权证书实际续期 revision 1→2 后
自动滚动加载，后续无变化任务不重复重启，真实推理继续通过。
三个测试 Key 已撤销。实际命令和脱敏结果保留于本地任务工件，并在 PR 中说明。
验收限于选定租户、服务及 `/v1/completions`；不代表所有模型、协议、限流维度或
租户策略均已验收。既有环境门控测试仍有 skip，未启用 Sandbox，其他环境及
既有 Helm 现场覆盖项固化仍需各自的发布流程。

### 历史交付：AKSK-VPC-20260922（该批已实施及验收）

该历史批次以 [执行文档](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/aksk-vpc-execution-plan.md) 为其最终合同；下列 PASS 仅属该批，2026-10-09 的 Network/Image 当前签名范围以 [AK/SK 合同](contracts/aksk-signature.md) 和本轮登记为准。下面旧版本排查保留为历史，不再描述当前代码。

| 编号 | 当前方法和路径 | 当前合同与验证 |
| --- | --- | --- |
| AK-01 | `GET /api/v1/auth/api-keys` | 用户 JWT；`items/total`，无 SK；真实 HTTP PASS；响应含只读 `role_name`/`role_description`（见 AK-ROLE-INFO-01） |
| AK-02 | `GET /api/v1/auth/api-keys/{key_id}` | 用户 JWT；snake_case Key 信息、数字 id/role_id；跨租户 404，PASS；响应含只读 `role_name`/`role_description`（见 AK-ROLE-INFO-01） |
| AK-03 | `POST /api/v1/auth/api-keys` | `{data:{name,role_id,expires_at?,idempotency_key?}}` → **201** `{data,secret_key}`；真实 HTTP PASS；`idempotency_key` 语义见 AK-IDEM-01；响应 data 含只读 `role_name`/`role_description`（见 AK-ROLE-INFO-01） |
| AK-04 | `PUT /api/v1/auth/api-keys/{key_id}` | 仅 name/role_id/is_active/expires_at；外层 update_mask，值为 FieldMask lowerCamel；清空到期、启停、改绑 PASS |
| AK-05 | `DELETE /api/v1/auth/api-keys/{key_id}` | 200 `{status:"revoked"}`，后续签名 401，PASS |
| AK-06 | `PUT /api/v1/auth/api-keys/{key_id}/secret` | `{}` → `{data,secret_key}`；AK 不变，旧 SK 立即拒绝、新 SK 成功，PASS；响应 data 含只读 `role_name`/`role_description`（见 AK-ROLE-INFO-01） |
| AK-07 | 旧 `/api/v1/admin/access-keys/token` 已移除 | RPC/签发分支/白名单和 secret_hash 删除；旧路径 404，PASS |

Key 绑定同租户启用 TENANT 角色，复用套餐和 Casbin。主密钥文件必填，SK 使用专用 AES-256-GCM 实例加密；仅创建/重置返回一次，列表、详情、日志和审计无 SK/完整签名。每请求查询 Key 和角色状态，主体为独立 `api_key`，不伪造 user_id。首次种子为租户管理员授予六条管理权限；目标专用套餐经 API 开放 DASHBOARD/OPM/SYSTEM/NETWORK。

### 变更：AK-03 创建幂等（AK-IDEM-01，2026-10-10）

`POST /api/v1/auth/api-keys` 增加可选 `idempotency_key`；**未带该键时行为与改造前完全一致**（201 返回明文 SK），属向后兼容的可选增强，不新增或改路径/权限，不触发 API 目录同步。

- 作用域 `(tenant_id, actor_id, action='access_key.create', idempotency_key)`，取自信任上下文；同键同意图回放**原 Key 元数据**（同一 `id`/`access_key`），**不再返回明文 `secret_key`**（SK 仅交付一次）；同键异意图返回 **409**，reason 含 `IDEMPOTENCY_CONFLICT`。
- 存储为**独立表** `sys_access_key_idempotency`（迁移 `20261010131938_access_key_idempotency.sql`），**故意不对 `sys_access_keys` 建外键**：`sys_access_keys` 是物理删除，记录须活得更久，才能把「创建→删除→同键重放」判定为回放而非再建一把。串行化仅依赖唯一索引 `(tenant_id,actor_id,action,idempotency_key)`，未加 tenant 行锁（创建只写一张新行，无共享聚合被改）。
- 实测（ani-system @ ani-01，PostgreSQL + HTTP，2026-10-10）：先补跑迁移（表原缺失，atlas 由 `20261003152319` 升至 `20261010131938`）→ `verify-idempotency.py` **20/20 PASS**（无键 201 带 SK；同键同意图重放同 `id` 且 SK 空；异意图 409；不同键两把；key>128/name 空/role_id 缺均 400；创建→删除→同键重放 **404 `ACCESS_KEY_NOT_FOUND`** 且库内同名 0 把）→ `verify-idempotency-concurrency.py` 8 并发同键 **ALL PASS**（全部 201、同一 `id`、恰好 1 个带明文 SK、库内仅 1 把）。测试数据已清理。
- 边界：**前端联调未做**（前端尚未带 `idempotency_key` 调用）；重放拿不到明文 SK 属既定取舍（首次响应丢失需走 `ResetSecret`）。

### 变更：AK 响应回填角色名称/描述（AK-ROLE-INFO-01，2026-10-13）

`AccessKey` 读模型新增两个**只读**字段 `role_name`（字段 8）、`role_description`（字段 9），服务端按 `role_id` 在**请求租户内**回填。属**向后兼容的响应增强**：只增字段，不改路径、方法、权限、套餐关系，不触发 API 目录同步（沿用 AK-IDEM-01 结论）。

- 生效范围：`List`（含游标与非游标两条分页路径）/ `Get` / `Create` / `ResetSecret` 四条 RPC 的 `AccessKey` 输出，以及幂等回放路径，全部一致。
- 租户边界：角色名称/描述读取走请求上下文的 `TenantPrivacy` 注入谓词，跨租户不可读；未新建平台/系统级读取旁路，未用 `privacy.Allow`。
- 角色缺失语义：角色被删除/停用导致查不到时**字段缺席**（不下发空串），Key 仍可正常列出与验签判定；前端需容忍可选字段。
- 只读约束：两字段标注 `read_only`；`update_mask` 白名单保持 `name/role_id/is_active/expires_at`，客户端提交该两字段仍 **400**；不加入 `accessKeyCursorSortColumns`，不参与排序/筛选/游标载荷。
- 实测（ani-system @ ani-01，2026-10-13）：`verify-api-key-role-info.py` **14/14 PASS**（创建/详情/列表/重置响应带回填、snake_case 无 `roleName`、无 SK、字段形态）；回归 `verify-cursor.py` 24/24、`verify-list-cursor.py` 38/38、`smoke-regression.py` fail=0 均不退化。probe 数据已清理。

| 问题编号 | 本批结论 |
| --- | --- |
| AK-ISSUE-01 | 已解决：一个 Key 一个角色；无权限/跨租户/停用角色及改绑真实验收 PASS |
| AK-ISSUE-02 | 已解决：移除机器 JWT，逐请求签名与当前状态校验；停用/删除/到期/重置即时生效 PASS；无存量迁移任务 |
| AK-ISSUE-03 | 已解决：移除交换接口，审计使用已验证主体；SK/签名扫描及伪造身份回归 PASS |
| AK-ISSUE-04 | 已解决本批首次部署：空库种子权限 + 显式专用套餐与角色配置；租户管理员创建 Key PASS |
| AK-ISSUE-05 | 已解决 NET-01：共用 Principal 与下游 actor；Key 对用户专用/Key 管理接口仍 403，PASS |

证据及复现命令见 [本批记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/aksk-vpc-20260922/README.md)。该批仅验收持久化 VPC 查询，不证明网络数据面；该批只开放 GetVPC 的限制已由本轮 24 个租户 Network 与既有 Image 11 个方法的精确策略扩展，不开放其他未登记的机器接口。

### 实施前历史排查（以下记录截至源码 5a2a2e8，已由上述交付替代）

排查日期：2026-09-22，源码基线 `5a2a2e8`。本轮只排查与登记，没有修改实现、种子或运行环境。结论：**凭证管理、AK/SK 换 JWT 已有实现，但默认初始化后尚不能完成受控业务 API 调用，不能算完整可用。**

### 已有能力与边界

- 支持创建多个凭证、分页列表、详情、修改名称/启停/有效期、删除、重置 SK；创建和重置响应各返回一次明文 SK，列表/详情不返回 SK。凭证表只存 SK 的 SHA-256 摘要；审计路径另有泄露问题，见 AK-ISSUE-03。
- 凭证归属当前操作者的租户，服务端覆盖请求中的 `tenantId`。平台账号创建的是租户 `0` 的凭证，不能通过传入其他 `tenantId` 代建租户凭证。现有租户过滤和写入守卫适用于凭证表；本轮未做完整跨租户 HTTP 验收。
- AK/SK 先换短期 JWT，再携带 `Authorization: Bearer <accessToken>` 调 API。现有链路没有直接传 `X-API-Key`、把 SK 当 Bearer、或逐请求 HMAC 签名的用法。
- 换令牌时校验摘要、凭证状态和有效期，包含 IP+AK 失败限流；凭证 `expiresAt` 留空表示长期有效。`lastUsedAt` 只表示最近换令牌时间，不是最近业务调用时间。
- 机器令牌无 refresh token、不走用户登录 Cookie。到期重新提交 AK/SK；JWT 有效期共用 `authn.jwt.access_token_expires`，配置样例为 `5400s`，未配置时回退 `900s`，以响应 `expiresIn` 为准。JWT 有效期没有截断到凭证到期时间。

### 接口与当前报文

除 AK-07 外均需已登录管理员的 Bearer 和相应 API 权限；租户上下文还受套餐模块限制。API Key 管理当前归类 `SYSTEM`，默认套餐只有 `DASHBOARD`、`OPM`，默认租户管理员也未授予这些管理 API；平台管理员已有管理授权。

| 编号 | 方法与路径 | 请求 / 响应与用途 |
| --- | --- | --- |
| AK-01 | `GET /api/v1/admin/access-keys` | `pagination.PagingRequest`；返回 `{items,total}`，不含 SK |
| AK-02 | `GET /api/v1/admin/access-keys/{id}` | 返回凭证详情，不含 SK |
| AK-03 | `POST /api/v1/admin/access-keys` | `{data:{name,status,expiresAt}}`；返回 `{data:{id,accessKey,...},secret}`，AK/SK 由服务端生成 |
| AK-04 | `PUT /api/v1/admin/access-keys/{id}` | `{data:{...},updateMask:"status"}` 等；可改名称、状态、有效期，不能改 AK、摘要或租户；返回空对象 |
| AK-05 | `DELETE /api/v1/admin/access-keys/{id}` | 删除凭证；返回空对象 |
| AK-06 | `PUT /api/v1/admin/access-keys/{id}/secret` | 请求体 `{}`；返回 `{data,secret}`，旧 SK 不能再换新令牌 |
| AK-07 | `POST /api/v1/admin/access-keys/token` | `{accessKey,secret}`；无需登录 Bearer；返回 `{accessToken,expiresIn,tokenType}` |

这些接口沿用 camelCase，不能直接套用 AUTH-01/02 的 `access_token` 响应解析。没有独立 Count HTTP 路由。

### 当前调用方式及卡点

以下展示现有协议，第三步仍受下述授权缺口阻断；不是已完成的业务接入验收。真实 AK/SK 只应经 HTTPS 提交并保存在调用方服务端，不能放进浏览器前端。

1. 使用有管理权限的账号登录，再创建凭证：

   ```http
   POST /api/v1/admin/access-keys
   Authorization: Bearer <管理员访问令牌>
   Content-Type: application/json

   {"data":{"name":"业务脚本","status":"ON"}}
   ```

   保存响应 `data.id`、`data.accessKey` 和 `secret`。需要固定到期时间时，增加 RFC3339 格式的 `expiresAt`。不要依赖重新查询取回 SK，丢失后只能重置。

2. 调用方用 AK/SK 换令牌，不需要先登录：

   ```http
   POST /api/v1/admin/access-keys/token
   Content-Type: application/json

   {"accessKey":"ak-<创建时返回的值>","secret":"sk-<创建或重置时返回的值>"}
   ```

   响应字段为 `accessToken`、`expiresIn`（秒）、`tokenType`（`bearer`）。缓存令牌，在到期前后按需重新交换，不必每次业务请求都提交 SK。

3. 业务请求带 `Authorization: Bearer <accessToken>`。当前令牌固定 `roles=["machine"]`、`userId=0`；种子没有这个角色的业务授权，也不会继承创建者权限，因此默认 Casbin 下受保护 API 会被拒绝。仅拿到 JWT 不代表已能调用业务接口。

停用示例：`PUT /api/v1/admin/access-keys/{id}`，请求体 `{"data":{"status":"OFF"},"updateMask":"status"}`。它目前只阻止后续换令牌，**不会立即吊销已经发出的 JWT**；删除和重置同样存在这个限制。

### 已发现的缺口

| 编号 | 已核对的事实及影响 | 状态 / 最小处理方向 |
| --- | --- | --- |
| AK-ISSUE-01 | Key 没有角色/权限绑定字段；所有机器 JWT 固定 `machine` 角色，种子没有相应策略；不继承创建者权限。手动给同一租户的 `machine` 角色授权会使该租户所有 Key 共用权限，仍无法分别控制 | 未修复。最小方向是复用现有租户角色和 API 权限，为 Key 明确绑定授权，不另建 IAM |
| AK-ISSUE-02 | 停用、删除、重置、凭证到期只阻止新交换；后续 JWT 校验只验签/有效期/Redis/黑名单，不复查对应 Key。令牌使用 `userId=0` 入 Redis，无按 Key 的吊销索引，也不登记普通在线会话 | 未修复。不能套用本轮已完成的用户账号会话吊销；需要按 Key 使旧令牌失效，或在认证时复查 Key 当前状态 |
| AK-ISSUE-03 | IssueToken 经过通用 API 审计；既没有归入跳过请求体的登录操作，也没有邀请接口的脱敏分支，提交的 SK 会进入审计请求体 | 未修复。换令牌入口须避免记录 `secret`；凭证表存摘要并不能解决这个泄露路径 |
| AK-ISSUE-04 | 默认租户管理员没有 Key 管理 API 授权，默认套餐也没有其 `SYSTEM` 模块；平台创建的 Key 固定归属租户 0 | 未修复。租户自助使用前需明确并显式补齐所需授权/套餐，不通过启动重刷种子或清空 API 表处理 |
| AK-ISSUE-05 | 机器令牌 `userId=0`，部分业务入口要求真实用户；例如 `NetworkService.GetVPC` 明确拒绝零用户 ID，补角色授权后仍然不能调用 | 未修复。按实际要开放的业务 API 适配机器身份并验收，不能声称所有接口天然支持 API Key |

如后续要求补齐最小闭环，范围限于：选定实际要调用的 API、复用角色授权、处理 Key 失效与审计脱敏，并显式配置所需初始化授权。不自动引入跨服务配额、独立认证服务或全平台机器身份框架。

### 请求签名模式的改造评估（待决定，未实施）

用户询问改成“客户端用 SK 为每次请求签名，直接调用业务 API”的成本。本项只是方案评估，尚未实施。此前覆盖通用 HTTP JSON 签名、迁移和较完整验证的初估为 3～5 个开发人天；后续用户要求先跑通主流程，收窄后的第一批范围和估算见文末，不以此前清单作为全部首批要求。

- 认证入口：现有 `auth.Server` 固定提取 Bearer；需要识别签名凭证、验签后建立可信机器身份，再复用租户/套餐、Casbin 与 Ent 上下文。要明确签名失败不回退到另一种认证方式，避免混用凭证绕过校验。
- 请求协议：确定方法、实际路径、查询参数、签名头及原始请求体摘要的规范化规则，检查时间窗并明确重放处理；客户端与服务端必须对同一字节内容计算签名。AWS 的规范化规则可作参考，但采用相似方式不等于兼容其 SDK，参见 [SigV4 请求签名步骤](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html)。
- 密钥与结构：当前代码只存 `SHA-256(SK)`，无法直接用于验证以原 SK 为密钥的常规 HMAC 签名。用户随后明确没有旧 Key、没有存量用户，因此直接改为加密 SK 并移除未使用的摘要/交换代码，不做旧凭证转换或兼容。主密钥由部署环境单独保管，结构由 Atlas 显式管理。现有加密工具允许缺密钥时明文直通，SK 场景必须拒绝这种降级。
- 授权和审计：仍需补齐 AK-ISSUE-01/04/05；改用签名不会自动给机器身份授权。审计当前从 Bearer 解析身份，需改为可靠记录已认证的 Key 与租户，避免记录密钥或可重放的完整认证材料。
- 代码替换：移除换 JWT 入口、签发分支和对应免鉴权登记；普通用户登录 JWT 继续使用。按无存量前提，不增加历史机器 JWT 回收或切换流程。每次验签读取 Key 当前状态时，停用/删除/重置可在后续请求生效，但不能撤回已通过认证并开始执行的请求。
- 验收：除成功调用外，至少覆盖请求篡改、时间边界、禁用/过期 Key、跨租户及越权拒绝、密钥不进入日志、首次初始化，以及普通登录 JWT 回归。只写出 HMAC 计算函数不算完成。

### 本轮验证

源码链路已核对：HTTP 路由 → Service → Repo/Schema → 机器 JWT 签发及 Redis 校验 → Casbin 策略来源与租户套餐 → API 审计。

- Ubuntu 独立源码快照、Go 1.26.7、SQLite + miniredis：`go test ./app/admin/service/internal/data ./app/admin/service/internal/service -run '^TestAccessKey' -count=1 -v`，既有 AccessKey 定向用例全部 PASS。
- 仅在远端快照增加诊断探针：分别停用、删除、重置和把有效期改至过去后，旧 AK/SK 已不能换新令牌，但原 JWT 仍能通过真实签名与 Redis 缓存校验；四种情形均复现。到期情形通过修改有效期模拟，不是墙钟等待到期。
- 使用生成的 AccessKey HTTP 路由、真实 IssueToken Service 和通用审计中间件，在进程内提交测试 AK/SK，确认明文 SK 到达 API 审计写入回调；没有使用真实凭证，也没有连接用户审计数据库。生产装配将该回调接到 ApiAuditLogRepo，实际已部署环境是否存有历史泄露记录未排查。
- 诊断用例的 PASS 表示成功复现现状，**不是缺口已修复**；探针仅保留在远端证据目录，未加入仓库回归测试。证据：`/home/ubuntu/Workspace/.codex-runs/governance-access-key-audit-20260922/access-key-audit.log`；探针位于其 `src/app/admin/service/internal/service/access_key_audit_probe_test.go`。
- 本轮仓库只修改本登记文件；没有部署或修改运行数据库。kind、真实 PostgreSQL/Redis 与业务 API 全链路为 `not_verified`。

源码入口：[HTTP 路由](../api/protos/admin/service/v1/i_access_key.proto)、[报文](../api/protos/access_key/service/v1/access_key.proto)、[业务实现](../app/admin/service/internal/service/access_key_service.go)、[仓储](../app/admin/service/internal/data/access_key_repo.go)、[表定义](../app/admin/service/internal/data/ent/schema/access_key.go)、[机器令牌签发与校验](../app/admin/service/internal/data/authenticator.go)、[角色策略来源](../app/admin/service/internal/data/authorizer_provider.go)、[API 审计](../pkg/middleware/logging/api_audit_log.go)、[首次初始化 SQL](../sql/bootstrap/001_initial.sql)、[VPC 入口身份检查](../app/admin/service/internal/service/network_service.go)。


## 功能组：VPC 详情查询的机器调用

### 历史交付：NET-01 / NET-ISSUE-01（2026-09-22）

`GET /api/v1/networks/vpcs/{vpc_id}` 已接受用户 JWT 或三个签名头之一整组，禁止混用；只接受规范 VPC 路径和空 query/body。可信主体经公共认证层进入租户/套餐/Casbin，数值租户经持久化 resource_tenant_id 映射，下游统一生成 `governance:user:<id>` 或 `governance:access-key:<id>`。

Network 在 66f787b 上仅整合 9e56e1c 必要 vpc-read 改动，保留主线 BaseConnectivity 映射和租户过滤，加入 Key actor；没有整支合并平台工作或改名。真实 NodePort → Governance → mTLS → Network → PostgreSQL 已 PASS；跨租户/不存在同样 404（仅 request_id 不同），伪造公网身份无效，缺/错误证书与 RPC/header 租户不一致拒绝，用户 JWT 查询与登出回归 PASS。NET-ISSUE-01 在该批已解决，当时 full 模式及其他 RPC 不在其开放范围；当前租户 Network 接入使用 Resource 的 governance 组合入口，范围和本轮验收以上方 2026-10-09 小节为准。

本次运行证据见 [本批记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/aksk-vpc-20260922/README.md)，以下保留实施前的分支核对和范围形成记录。

### 实施前历史评估

2026-09-22 只读评估。Governance 基线 `5a2a2e8`；本地 Network 源码 `66f787bd30134141726c596612501a83cf75bdb7`，与 Governance `go.mod` 固定的 API 模块版本一致。这里核对的是源码，不能据此推断实际部署镜像与实验接收端相同；本轮未部署、未运行跨服务验收，也未修改 Network 仓库。

| 编号 | 方法与路径 | 请求 / 响应 | 当前条件 |
| --- | --- | --- | --- |
| NET-01 | `GET /api/v1/networks/vpcs/{vpc_id}` | 路径 ID 为 `vpc_` + 32 位小写十六进制；无 body、拒绝查询参数；返回 `{vpc:{...}}` | 目前需要用户 JWT、非零租户与用户 ID、角色的 `network:vpc:get` API 授权、套餐 `NETWORK` 模块；不存在或跨租户资源按 404 处理 |

### 认证改造影响

无需替换现有 Casbin、套餐或租户隔离规则。建议由 Governance 入口校验 AK/SK 签名并建立可信的 Key ID、租户、角色上下文，再复用既有 API 权限和套餐检查。客户端仍调用 NET-01，不直接访问 Network 内部 gRPC；原始 SK 不传下游。

Governance 的两处适配必须修改：[NetworkService.GetVPC](../app/admin/service/internal/service/network_service.go) 拒绝 `userId=0`；[NetworkClient.GetVPC](../app/admin/service/internal/data/network_client.go) 也拒绝零用户 ID，并固定生成 `governance:user:<uid>`。需接受经过认证、授权的 Key 身份并保留可区分的审计标识（例如 `governance:access-key:<id>`，仅为建议，尚未冻结合同），不能简单删除守卫或伪造用户 ID。角色绑定缺口仍按 AK-ISSUE-01 处理。

**接收端的分支整合差异（NET-ISSUE-01，历史核对后纠正）**：此前仅检查 Network 当前 main `66f787b`，把缺失描述成“尚未实现、需要新补接收层”，不够准确。mTLS 没有被删除：[提交 `9e56e1c`](https://github.com/zhangzhe-ctrl/ani-network-service/commit/9e56e1c675bb2102c8e84adc5a8dfd2962823ddd) 已实现 Governance VPC 只读接收，保留在 `codex/install-ceph` 及对应远端跟踪分支；后续 `f198a1e` 没有删除它，当前 main 不包含该提交。源码包括 `internal/server/governance.go`、`cmd/ani-network-service/vpc_read.go`，需显式启用 `ANI_NETWORK_MODE=vpc-read`；full 模式仍保留历史认证延期边界。Governance 自己的 Network mTLS 客户端和共享证书配置也仍在。

该分支已有的 [GovernanceResolver/Unary](https://github.com/zhangzhe-ctrl/ani-network-service/blob/9e56e1c675bb2102c8e84adc5a8dfd2962823ddd/internal/server/governance.go) 校验证书链及精确 SAN `ani-governance`，随后信任它声明的单值 `x-ani-tenant-id`、`x-ani-actor`、`x-ani-request-id`，仅允许 GetVPC；校验 RPC 请求 `tenant_id` 与可信 header 一致。**Network 不重新查询用户归属或角色权限**，这些由 Governance 保证；一致性检查避免请求体的重复租户字段绕过 header 范围，并非另一套租户授权。当前 actor 格式只接受 `governance:user:<非零ID>`，接 API Key 时需扩展为可区分的机器 actor，同时复用原证书和租户信任链。

Network 的 [GetVPC SQL](https://github.com/zhangzhe-ctrl/ani-network-service/blob/66f787bd30134141726c596612501a83cf75bdb7/internal/data/queries/vpcs.sql) 已按 `tenant_id + vpc_id` 过滤，同租户关联也有条件；请求签名不要求改 VPC 查询或领域模型。后续应先整合已有接收实现，再适配 Key 身份，不重新建设 mTLS 或引入 IAM。Governance 的 `go.mod` 只固定消费的 API 模块，不决定 Network 实际运行的镜像/源码版本；2026-09-21 `a936623` 将模块 pin 更新为 main `66f787b`，没有删除独立分支的服务端代码。

历史 [2026-09-19 执行记录](https://github.com/zhangzhe-ctrl/ani-network-service/blob/9e56e1c675bb2102c8e84adc5a8dfd2962823ddd/docs/execution/records/governance-vpc-read-20260919.md) 保存 VPC 只读链路与 mTLS/隔离验收，不能把这些说成从未实现；它也不证明当前部署或新增 API Key 链路已通过。当前部署版本本轮未检查；分支整合、机器 actor、签名调用和原 JWT 回归仍为 `not_verified`。原“不含下游合同调整”的 3～5 人天估算不包含分支整合与两仓联调。

### 第一批收窄：先跑通签名查询 VPC

用户要求先跑通主流程，后续按实际问题迭代。第一批只验收“管理员配置租户 Key → 客户端签名 → Governance 验签和既有权限检查 → mTLS 调 Network GetVPC → 返回本租户 VPC”；不把所有业务 API、通用云厂商兼容列为前置。用户已明确没有旧 Key、没有存量用户，按首次部署实施。沿用此前 2～3 个开发人天的粗估，不是交付承诺。执行依据见 [AK/SK 执行文档](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/aksk-vpc-execution-plan.md)，尚未改实现。

- Governance：复用 AK 管理，增加加密 SK 和一个同租户角色绑定；入口增加签名认证，首批仅开放 NET-01，其他业务接口按后续批次接入。租户状态、套餐和 Casbin 继续复用。每次查 Key 当前状态即可，先不做权限/密钥缓存。
- Network：整合现有 `9e56e1c` 的 vpc-read 入口，扩展其 actor 校验支持 Key；不重写 mTLS、可信租户 header 或 VPC 查询。`git merge-tree --write-tree main 9e56e1c` 只预览、未实际合并：唯一文本冲突在 `internal/data/postgres.go`，一侧增加 BaseConnectivity 返回映射，另一侧增加连接失败错误分类，需同时保留。仍需实际编译和回归，合并预览不算验证通过。
- 数据与调用：Atlas 显式建表、首次种子授权；给目标租户配置 VPC 只读权限、SYSTEM/NETWORK 套餐；提供 Python 签名示例。直接移除未使用的交换代码，保留普通用户 JWT；不做旧 Key 迁移、存量增量授权补丁、停机切换或恢复演练。
- 必要验证保留：真实查询成功，错误/过时签名、无权限、跨租户、停用 Key 拒绝，以及原用户 JWT 查询仍成功；SK 加密存储且不进入日志。首批 GET 只读接口允许时间窗内重复查询，不宣称一次性请求或通用防重放；写接口接入时再确定相应重放/幂等语义。
- 后续再做：其他业务 API 和写请求、全功能 SDK、无中断密钥轮换、复杂权限组合、自助权限管理界面、性能优化和全面故障恢复验收。不会因本次收窄关闭 AK-* / NET-* 的未完成登记。

### API Key 风格对照与声明预览（历史方向，现已按执行文档实施）

2026-09-22：用户明确将**共用认证层**纳入范围，参照当时的 ANI OpenAPI（历史对照路径 `ANI/repo/api/openapi/v1.yaml`）查看预览后确认方向，并要求编写含 Python 示例的执行文档。用户进一步明确**没有旧 Key、没有存量用户**。后续以 [执行文档](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/aksk-vpc-execution-plan.md) 的接口、签名规范、首次部署与验收步骤为准；本节保留原样式对照；实现和真实运行结果以上述当前交付及执行文档为准。

参照文件在 `servers.url` 中已有 `/api/v1`，Key 路由写作 `/auth/api-keys`，完整路径为 `/api/v1/auth/api-keys`。其 `ApiKeyAuth` 使用单个 `X-API-Key` 长期凭证；创建字段为 `name/scopes/user_id/rate_limit_rpm/expires_at`，创建响应为 `key_id/key_value/key_prefix`，列表为 `items/total`。**这是静态 API Key 声明，不是 AK/SK 请求签名协议。**

建议沿用路径和 snake_case 外观，具体差异如下：

| 项目 | 本批建议 |
| --- | --- |
| 路径 | `/api/v1/auth/api-keys`，明细及操作使用 `/{key_id}` |
| 字段 | `access_key`、`secret_key`、`role_id`、`expires_at`、`is_active` 等使用 snake_case；API Key ID 继续使用当前数字类型 |
| 创建请求 | 保留本仓 `{data:{...}}` 约定；`name/role_id` 必填，`expires_at` 可选，默认启用 |
| 授权 | 一个同租户 `role_id` 复用现有角色及 API 权限；不复制旧 `scopes` 体系。可绑定角色需经过授权校验，不因为知道角色 ID 就能任意绑定 |
| 身份 | 租户和创建者来自可信认证上下文；不开放旧 `user_id` 代建参数 |
| 限流 | 首批不暴露 `rate_limit_rpm`，避免声明尚未实现的单 Key 限流能力 |
| 列表 | 沿用 `items/total`；保留现有分页机制，页参数暂不在本批统一改名。旧 YAML 的 Key 列表本身没有 cursor 参数，不因此新增游标分页 |
| 秘密 | 旧 `key_value` 拆成公开 AK 与一次返回的 SK；SK 仅创建/重置响应返回，后续读取不返回，服务端加密存储以支持 HMAC 验证 |
| 更新 | 保留 PUT 和 `update_mask` 部分更新语义，`is_active` 映射现有 ON/OFF；实现时须核对 FieldMask 的 JSON 编解码，不能仅改字段名就声称掩码路径兼容 |

以下均为**目标路由**，现有 AK-01～07 的当前路由仍以原登记为准：

| 现有编号 | 建议目标 | 行为与响应 |
| --- | --- | --- |
| AK-01 | `GET /api/v1/auth/api-keys` | 列表，200，`{items,total}`，不含 SK |
| AK-02 | `GET /api/v1/auth/api-keys/{key_id}` | 详情，200，Key 信息，不含 SK |
| AK-03 | `POST /api/v1/auth/api-keys` | 创建，建议 201，`{data,secret_key}` |
| AK-04 | `PUT /api/v1/auth/api-keys/{key_id}` | 修改名称、绑定角色、有效期或启停状态；`{data,update_mask}`，200 |
| AK-05 | `DELETE /api/v1/auth/api-keys/{key_id}` | 吊销并删除，建议 200，`{status:"revoked"}` |
| AK-06 | `PUT /api/v1/auth/api-keys/{key_id}/secret` | 重置 SK，200，`{data,secret_key}`，旧 SK 后续请求失效 |
| AK-07 | 不提供新的换 JWT 路由 | 签名直接调用业务 API；删除未使用的交换路由与签发代码，不做存量兼容 |

Key 管理路由使用用户 Bearer JWT 并要求对应管理权限；不因为新增签名认证就允许 Key 管理其他 Key。首次种子必须引用最终路径、补齐租户管理员授权，空库通过 admin init 显式初始化；不写存量路径迁移脚本。

创建预览（角色 12、ID 42 均为示例，不是初始化约定）：

```http
POST /api/v1/auth/api-keys
Authorization: Bearer <用户 JWT>
Content-Type: application/json

{"data":{"name":"vpc-reader","role_id":12,"expires_at":"2026-12-31T23:59:59Z"}}
```

```json
{
  "data": {
    "id": 42,
    "name": "vpc-reader",
    "access_key": "ak-...",
    "role_id": 12,
    "is_active": true,
    "expires_at": "2026-12-31T23:59:59Z"
  },
  "secret_key": "sk-..."
}
```

业务调用外观如下；头名和准确签名输入已写入执行文档。客户端以 SK 计算 HMAC-SHA256 签名，业务请求不发送 SK，也不预先换 JWT。此历史首批仅有 NET-01 签名闭环；当前精确机器入口以上方 2026-10-09 登记及 AK/SK 合同为准，不开放所有业务 API。

```http
GET /api/v1/networks/vpcs/vpc_0123456789abcdef0123456789abcdef
X-Access-Key: ak-...
X-Timestamp: <当前 Unix 秒时间戳>
X-Signature: <HMAC-SHA256 签名的十六进制值>
```

签名绑定方法、实际路径、查询、AK、时间戳和原始请求体摘要；首批只读 VPC 的 query/body 为空，七行字节规则及 300 秒时间窗详见执行文档第 3 节，Python 示例见第 7 节，现已落为可运行文件并完成真实联调。

OpenAPI 认证声明示意（片段，省略消息和响应定义）：

```yaml
components:
  securitySchemes:
    BearerAuth:
      type: http
      scheme: bearer
      bearerFormat: JWT
    AccessKeyAuth:
      type: apiKey
      in: header
      name: X-Access-Key
      description: 公开 AK，不是 SK
    SignatureAuth:
      type: apiKey
      in: header
      name: X-Signature
      description: 使用 SK 对当前请求计算的 HMAC-SHA256 签名
    SignatureTime:
      type: apiKey
      in: header
      name: X-Timestamp
      description: 签名时间，Unix 秒

# NET-01 的 operation 级配置；不作为所有接口的全局默认值
security:
  - BearerAuth: []
  - AccessKeyAuth: []
    SignatureAuth: []
    SignatureTime: []
x-ani-authz:
  principal_kinds: [user, api_key]
```

按 [OpenAPI Security Requirement](https://spec.openapis.org/oas/v3.1.0.html#security-requirement-object) 语义，数组中的两项为二选一；同一项中的 AK、签名和时间戳要求同时提供。`type: apiKey` 在这里描述头部传输位置，不代表服务端直接信任 AK；扩展字段只是文档说明，实际支持范围仍需代码执行。OpenAPI 声明也不会自动为 Swagger UI 或生成客户端计算 HMAC。

共用认证层的目标：用户 JWT 和 Key 签名各自验证后，统一产出调用主体类型、主体 ID、租户和角色，再走现有套餐/Casbin；业务入口从同一可信上下文取身份，下游调用也共用 actor/header 生成逻辑。首批改好 VPC 入口及通用接入示例，后续新接口只声明支持的主体并登记权限，不逐个重写验签；既有硬编码非零用户 ID 的入口仍需按实际接入修正。用户专用功能继续只接受用户身份。

验证范围：只读对照两份源声明并登记草案；未生成、未构建、未部署，签名协议及新版路由均 `not_verified`。

## 功能组：通用配额与 GPU 本地模拟

2026-09-22 用户要求制定详细、强约束的执行计划；当时只写计划，批次 QUOTA-GPU-LOCAL-01。原计划见 `git show 63849fc4cde38b879184a8fea4a6f539e60063e1:docs/quota-gpu-local-execution-plan.md`。当时使用独立进程、真实 PostgreSQL 和持久 GPU 模拟器验收；不能将模拟结果标成真实 GPU 接入成功。

2026-09-22（同日第二批）：QUOTA-GPU-LOCAL-01 已按计划 P0～P8 本地执行完成：目录/账本/单次占额/累计退额/持久化转发与恢复/内部 mTLS 退额/本地 GPU 模拟闭环实现并通过指定验收（见 [验收证据](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/quota-gpu-local-01/README.md)）；QUOTA-01/02/03、QUOTA-LAB-01～04 标记为已实现（lab 路由仅 quota_lab 构建）。真实 GPU 服务接入与真实硬件分配仍为 not_verified，未标成接入成功。

用户确定：Governance 是统一入口，在转发前一次占额；成功不再实扣。资源服务只上报可退额事实，Governance 配额模块不维护资源运行状态。请求超时/操作失败本身不导致退额；只有确定创建已封闭且没有对应资源，或资源实际释放，才归还。计划采用累计 released_total 防重复/乱序多退。

### 计划接口与鉴权

以下全部为拟新增或拟扩展合同，**不是当前可调用接口**。

| 编号 | 方法/路径 | 报文与职责 | 鉴权、套餐关系、状态 |
| --- | --- | --- | --- |
| QUOTA-01 | GET /api/v1/admin/quota-definitions | 配额目录 code/name/unit/kind/enforcement；沿用分页 | 平台管理读权限，TENANT 模块；已实现（本地验收通过） |
| QUOTA-02 | GET /api/v1/admin/tenants/{id}/quota-accounts | 指定租户 limit/occupied/available/overLimit；占额不等于 Running GPU | 平台管理读权限，TENANT 模块；已实现（本地验收通过） |
| QUOTA-03 | gRPC /quota.service.v1.QuotaReleaseService/ReportQuotaRelease | event ID、原 operation、charge 与累计释放量、原因；没有公共 HTTP 映射 | 独立 listener、精确 owner mTLS 身份、账本归属校验；内部退额不受套餐到期阻断；已实现（本地验收通过） |
| QUOTA-LAB-01 | POST /api/v1/quota-lab/gpu-allocations | data.name/gpuCount，Idempotency-Key；202 与稳定操作/占额/资源 ID | 仅 quota_lab 构建；真实用户 JWT、角色与套餐检查，实验目录归 TENANT；已实现（本地验收通过） |
| QUOTA-LAB-02 | GET /api/v1/quota-lab/gpu-allocations/{resource_id} | 经 Governance 查询模拟资源，不扣额 | 同上，限本租户；已实现（本地验收通过） |
| QUOTA-LAB-03 | DELETE /api/v1/quota-lab/gpu-allocations/{resource_id} | 幂等转发删除，接受删除不立即退额 | 同上，限本租户；已实现（本地验收通过） |
| QUOTA-LAB-04 | GET /api/v1/quota-lab/operations/{operation_id} | 投递状态与账本标识，不维护资源 Running 状态 | 同上，限本租户与操作主体；已实现（本地验收通过） |
| 复用 PLAN-11～14 | 现有套餐配额 CRUD | 拟增加 quotaCode、唯一/必填/数值范围约束；旧枚举本批 deprecated 兼容 | 沿用平台套餐管理权限；未改现有接口 |
| 复用 TENANT-04/08 | 现有租户绑定与 usage | 绑定/到期复用；旧 QuotaUsage 仅补 code，旧统计能力不扩大 | 未改现有接口 |

2026-09-27 H03：QUOTA-LAB-01～04 是历史实验登记，模拟器、专属 Proto、路由和构建标签已退役，当前不可调用。真实配额账本、转发 worker、usage sync 与内部 mTLS 退额继续保留；本登记仍未整体结项。

### 已登记边界与执行约束

- QUOTA-ISSUE-01：当前固定配额类型只有 USER_LIMIT/STORAGE/API_CALL。计划将目录与额度计算规则分开，新增 gpu.count 只具备 LAB_ONLY 执行能力；不通过新增枚举假装完整接入。
- QUOTA-ISSUE-02：当前没有占额账本、稳定幂等转发及内部 mTLS 退额接收端。现有 Network request-id 和 last_operation_id 不代表已实现这些能力。
- QUOTA-ISSUE-03：PlanQuota/QuotaUsage 增加 code 使用新 Proto tag；旧数据要精确回填，NULL/重复/未知类型拒绝迁移，不自动合并。Atlas 升级与恢复只在本任务隔离库验证。
- QUOTA-ISSUE-04：实验 GPU 路由、故障入口和模拟 owner 不进入正式构建/正式 OpenAPI/首次种子。正常构建的真实 GPU 创建释放仍 not_verified。
- QUOTA-ISSUE-05：计划对已有配额历史的租户增加物理删除保护；对绑定租户的套餐保留删除保护，具体升级行为须在执行验收中验证并交接。套餐降额/切换不清空既有占额。
- QUOTA-ISSUE-06：本批不调整全局到期后用户 DELETE 权限；内部可信资源释放通道必须持续可用。旧三个配额项仍保持配置/现有统计，不宣称强制拦截已接入。

本轮验证仅为源码核对、计划交叉审查与文档检查；实现、生成、迁移、本地模拟验收均未执行。整体登记继续进行中。

## 功能组：审计归属地停止采集（IMAGE-SLIM-01）

2026-09-23 镜像瘦身批次移除 GeoIP（GeoLite2）依赖，执行计划见 [镜像瘦身与二进制拆分执行计划](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/image-size-reduction-plan.md)。影响面是**既有审计写入的报文内容**，接口路由与鉴权条件不变。

| 编号 | 涉及接口/报文 | 变更内容 | 鉴权、套餐关系、状态 |
| --- | --- | --- | --- |
| AUDIT-GEO-01 | 登录审计、API 审计、操作审计（写入）及对应审计查询响应中的 `geo_location` 子消息 | 不再采集 IP 归属地，新记录 `geo_location` 为 NULL/空；`ip_address`、设备信息、风险评分与签名不变 | 鉴权与套餐不变；Proto/Ent/DB 字段保留，字段删除另开一批；已实现（本机编译 + 定向测试通过，真实落库与查询展示 `not_verified`） |

### 已登记边界与执行约束

- AUDIT-GEO-ISSUE-01：登录策略 REGION 维度此前就未判定，移除地理库后仍无数据来源，登录闸门行为不变；IP/TIME/DEVICE 维度不受影响。
- AUDIT-GEO-ISSUE-02：历史审计行的 `geo_location` 保持原值，本批不做数据迁移；前端与下游消费方需容忍空值。
- AUDIT-GEO-ISSUE-03：本批不动 Proto/DB 字段；字段删除与对应 Atlas 迁移另开一批，不在本批声称完成。
- AUDIT-GEO-ISSUE-04：镜像形态变化（服务镜像与运维 CLI 镜像拆分、基础镜像换 distroless）不属于接口变更，登记在此仅为可追溯；部署命令变更见 [部署文档](deployment.md)。

本轮验证：本机 `go build ./...`、`go vet ./pkg/middleware/logging/...`、`go test ./pkg/middleware/logging/...` 通过；审计落库、查询响应与前端展示未验证（`not_verified`）。

## 功能组：鉴权引擎去掉 OPA（OPA-REMOVAL-01）

2026-09-23 镜像瘦身后续：移除 OPA 鉴权引擎，执行计划见 [OPA 移除记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/opa-removal-plan.md)。接口路由与报文不变，**鉴权条件**收敛。

| 编号 | 涉及条件 | 变更内容 | 状态 |
| --- | --- | --- | --- |
| AUTHZ-OPA-01 | `authz.type` 取值域、鉴权引擎装配 | 可选值由 `casbin / opa / noop` 收敛为 `casbin / noop`；配置 `opa` 时主装配启动期即拒绝（原 fail-closed 校验继续生效）。`rbac.rego` 与 OPA 策略生成器一并移除 | 已实现（本机 `go build ./...`、`go vet ./...`、`go test ./pkg/authorizer/... ./pkg/middleware/auth/...` 通过；真实部署下 Casbin 策略装载与接口鉴权 `not_verified`） |

### 已登记边界与执行约束

- AUTHZ-OPA-ISSUE-01：`authz` 配置块**保留**：`wiring_ent.go:38` 用 `authz.type != "casbin"` 做启动期 fail-closed 校验，去掉配置即失去防线；`noop` 保留作为"未配置/未知类型"的兜底引擎。
- AUTHZ-OPA-ISSUE-02：原 OPA 自定义模型解析失败的 `denyAllEngine` 兜底随 OPA 一并移除，它没有其它调用方。
- AUTHZ-OPA-ISSUE-03：`permission_policy.policy_engine` 枚举仍含 `OPA`（DB 字段），本批不动：改动需 Ent 重生成 + Atlas 迁移，且业务代码当前不写入该字段。这类"字段值域收敛"与 `geo_location` 字段删除同批另做。
- AUTHZ-OPA-ISSUE-04：对外接口与错误码不变；受影响的只是"引擎可选值"这一部署期契约。

本轮验证：`go build ./...`、`go vet ./...`、`go test ./pkg/authorizer/... ./pkg/middleware/auth/...` 通过；部署环境需复验 `admin check`、服务启动日志中的 casbin 引擎装载，以及登录后 `GET /api/v1/admin/me`、`GET /api/v1/admin/initial-context` 与一个需授权接口的 403/200 表现（`not_verified`）。

## 功能组：审计报文去掉 geo_location、权限策略枚举去掉 OPA（FIELD-CLEANUP-01）

2026-09-23 字段清理批次，执行计划见 [字段清理计划](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/geo-location-field-removal-plan.md)。这是**报文结构变更**，不只是实现细节。

| 编号 | 涉及接口/报文 | 变更内容 | 状态 |
| --- | --- | --- | --- |
| AUDIT-GEO-02 | 审计日志的写入与查询（`sys_api_audit_logs` / `sys_login_audit_logs` / `sys_operation_audit_logs` / `sys_data_access_audit_logs`） | Proto 消息 `GeoLocation` 与 `geo_location` 字段删除；HTTP/gRPC 响应与 OpenAPI 不再含该字段；DB 列由迁移 `20260923143455_drop_geo_location.sql` 删除 | 已实现（本机生成、编译、定向测试通过；真实库执行与前端展示 `not_verified`） |
| PERM-ENGINE-01 | 权限策略 `policy_engine` 取值域 | 枚举去掉 `OPA`，仅剩 `CEL` / `CASBIN` / `SQL`（默认 `CASBIN`）；DB 侧为 varchar 无 CHECK，**不产生迁移** | 已实现（Ent 重新生成；DB 无变更） |

### 已登记边界与执行约束

- FIELD-CLEANUP-ISSUE-01：`DROP COLUMN` 不可回滚到"有数据"状态，执行前必须备份；历史行的归属地数据会被丢弃。
- FIELD-CLEANUP-ISSUE-02：前端/下游消费方若读取 `geo_location`，字段消失后应表现为缺省而非报错；需各自确认容错。
- FIELD-CLEANUP-ISSUE-03：`admin sync-apis` 依赖的 OpenAPI 本批只删消息字段，未增删路由，升级后仍应按 dry-run → 应用顺序执行。
- FIELD-CLEANUP-ISSUE-04：`atlas migrate diff` 的 dev 库规范化被既有 `sys_quota_operations` 外键问题阻塞（与本次改动无关），本批迁移改为手写 + `migrate hash` + 独立开发库 apply 验证；该阻塞项另批处理。
- FIELD-CLEANUP-ISSUE-05：`policy_engine` 枚举变化不产生迁移，既有 `OPA` 值行不受影响，但服务已不再支持该引擎（见 [OPA 移除记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/opa-removal-plan.md)）。

本轮验证：`go build ./...`、`go vet ./...`、`go test ./pkg/middleware/logging/...` 通过；`make api` / `make openapi` / `make ent` 重新生成并复核 diff 只含本次变更；新迁移在独立开发库 `migrate apply` 通过（4 条 DROP COLUMN）后开发库已回收。真实库迁移、回滚演练与前端展示未验证（`not_verified`）。

## 功能组：Accelerator v1.2 管理与租户 BFF（GOV-ACC-V12-01）

### 节点发现与初始化增量（2026-10-21）

| 编号 | 方法/路径 | 下游完整 RPC 后缀 / 语义 | 权限 |
| --- | --- | --- | --- |
| ACC-20 | GET /api/v1/admin/accelerator/initialization | GetInitialization；受管连接、登记状态、带来源检查项、配置完成及最新操作 | accelerator:initialization:read |
| ACC-21 | GET /api/v1/admin/accelerator/discovered-nodes | ListDiscoveredNodes；只接受 page/pageSize，默认 1/20，pageSize 上限 200，按节点 name/UID 排序，items/total/discovery_revision | accelerator:discovery:list |
| ACC-22 | POST /api/v1/admin/accelerator/initializations | CreateInitialization；data.idempotency_key/discovery_revision/pools/groups/profiles，一次持久受理 | accelerator:initialization:create + 实际组件能力 |
| ACC-23 | GET /api/v1/admin/accelerator/initializations/{operation_id} | GetInitializationOperation；路径 ID 权威，持久操作及部分成功结果 | accelerator:initialization:read |
| ACC-24 | POST /api/v1/admin/accelerator/initializations/{operation_id}:retry | RetryInitializationOperation；data.idempotency_key/expected_version，不修改原请求 | accelerator:initialization:retry + 冻结 required_actions |

完整下游前缀固定 `/accelerator.v1.AcceleratorAdminService/`，调用者为唯一 DNS SAN
`ani-governance`，接收者为受管 Accelerator 服务，全部属于平台用户委托。
actor 从已认证平台 user Principal 构造，不发送租户身份或公网 Authorization。
沿用现有 mTLS 客户端正超时（默认 3 秒）及父取消；不自动重试写入。
新方法逐一登记客户端完整 RPC 分类，未知完整方法拒绝。

平台角色/action 由现有 Authorizer.Engine 在 project=0 下检查。Create 按实际请求额外检查
cluster:register（可信概况未登记时）、pool:create、supply-group:adopt、profile:publish，
以及会自动 OPEN 的新组所需 supply-group:set-admission；以上短名均带 `accelerator:` 前缀。
Retry 先读取原 operation 的 required_actions，再按固定 action→已登记 method/path 重新检查，
不能把下游任意字符串解释成路由。权限登记不授予角色、不启用套餐、不创建额度。

五方法成功均为 HTTP 200，直接返回 Proto JSON DTO；Create/Retry 的 200 表示受理，不能当作
初始化完成。写请求保持 `{data:{...}}`，64 位整数为十进制字符串。
仅新发现列表使用 page/pageSize；存量 ACC 列表仍 page_size/page_token。
概况/操作 GET 及两个 POST 不接受 query；发现 GET 仅接受单值规范正整数 page/pageSize。
路径 operation_id 不接受 query 覆盖，额外 body 字段不能进入可信下游上下文。
下游服务拒绝仍映射 503 `ACCELERATOR_SERVICE_ACCESS_DENIED`；Gov 当前用户拒绝为 401/403。

前端接入使用 `api/protos/catalog/service/v1/accelerator.proto` 及生成 OpenAPI 的独立 DTO：
前两步检查/编辑草稿，第三步仅提交一次 CreateInitialization，并以 operation_id 恢复查询；
BLOCKED 且 retryable 时用当前 version 和新幂等键显式 Retry，CONFLICT 不自动 rebase。
operation 的历史配置完成、当前 Group admission 和容量分别展示；算力规格须在既有管理入口
显式关联并启用。新组 2/4/8 等分仅使用相同且已知的逐卡安全预算，以整数计算绝对 MiB/core；
已有组继续 PreviewProfile。本批前端由专人接入，不修改前端代码。

联合验证复用 `TestAcceleratorJointHTTP` 的真实 JWT、会话缓存、数据库策略、Casbin 与生成
HTTP handler；设置 `GOV_ACC_INITIALIZATION_JOINT_SEED_FILE` 时运行本增量，并将平台 fixture
限定为实际所需八项权限。ACC 侧显式门控的 `TestPostgresInitializationJointHelper` 使用真实
领域、受限 PG 运行身份、mTLS/委托和 worker，仅 Kubernetes 发现/标签是外部边界替身。
联合用例观察同键重放、BLOCKED、显式 Retry、ACTIVE/OPEN 与最终概况，不将其算作实机验收。

公共 ACC API 固定正式模块 `v0.0.0-20261021055754-9969638c907a`，Origin.Hash 为
`9969638c907a806e895751ac71740d52f8592954`；使用 `GOWORK=off` 正常下载和校验，
不提交 `replace` 或工作树依赖。Gov 构建基准升至 Go 1.26.10，避免旧标准库已知漏洞。

本节登记合同与源码接点；正式 ACC 依赖版本、软件测试和真实环境部署按本批结果分别记录，
不以本节存在宣称主链验收完成。

2026-09-23 本批新增独立 `ACCELERATOR=13` 模块，保留旧枚举。正式依赖固定为
`github.com/zhangzhe-ctrl/ani-accelerator-service v0.0.0-20260923100416-9f9712198488`；
实际测试/后续升级版本以本批证据锁为准。以下为实现合同登记，运行通过状态须引用本批证据，不能继承历史 QUOTA-LAB 结论。

| 编号 | 方法/路径 | 下游 RPC / 语义 | 权限 |
| --- | --- | --- | --- |
| ACC-01 | GET /api/v1/admin/accelerator/clusters | ListClusters | accelerator:cluster:list |
| ACC-02 | POST /api/v1/admin/accelerator/clusters | RegisterCluster；data.display_name/connection_ref/idempotency_key | accelerator:cluster:register |
| ACC-03 | GET /api/v1/admin/accelerator/pools | ListPools；cluster_id | accelerator:pool:list |
| ACC-04 | POST /api/v1/admin/accelerator/pools | CreatePool；data.cluster_id/display_name/idempotency_key | accelerator:pool:create |
| ACC-05 | GET /api/v1/admin/accelerator/supply-groups | ListSupplyGroups；pool_id | accelerator:supply-group:list |
| ACC-06 | POST /api/v1/admin/accelerator/supply-groups:adopt | AdoptSupplyGroup；data 中固定节点、baseline、mode、queue；无 verification 写字段 | accelerator:supply-group:adopt |
| ACC-07 | POST /api/v1/admin/accelerator/supply-groups/{group_id}:set-admission | SetAdmission；data.expected_version/desired/reason/idempotency_key | accelerator:supply-group:set-admission |
| ACC-08 | GET /api/v1/admin/accelerator/profiles | AdminListProfiles；cluster_id | accelerator:profile:admin-list |
| ACC-09 | POST /api/v1/admin/accelerator/profiles | PublishProfile；data.spec/expected_group_version/verification_ref/idempotency_key | accelerator:profile:publish |
| ACC-10 | GET /api/v1/admin/accelerator/devices | ListDevices；cluster_id、可选 group_id | accelerator:device:list |
| ACC-11 | GET /api/v1/admin/accelerator/bindings | AdminListBindings；cluster_id、可选 physical_device_id；包括未关联事实 | accelerator:binding:admin-list |
| ACC-12 | GET /api/v1/admin/accelerator/capacity | AdminGetCapacity；profile_id/profile_version/include_devices | accelerator:capacity:admin-get |
| ACC-13 | GET /api/v1/accelerator/profiles | ListProfiles；cluster_id、可选 mode | accelerator:profile:list |
| ACC-14 | GET /api/v1/accelerator/profiles/{profile_id} | GetProfile；具体 profile_version | accelerator:profile:get |
| ACC-15 | GET /api/v1/accelerator/capacity | GetCapacity；具体 profile/version；租户合同无 include_devices 字段，永不返回设备明细 | accelerator:capacity:get |
| ACC-16 | POST /api/v1/accelerator/admission-preview | ResolveGpuRequest + CheckGpuFit + 本租户额度；只读，不返回 plan/runtime | accelerator:admission:preview |
| ACC-17 | GET /api/v1/accelerator/usages | ListGpuUsages；可选 owner_service/state | accelerator:usage:list |
| ACC-18 | GET /api/v1/accelerator/usages/{owner_service}/{resource_id} | 本库可信原 CREATE 构造 ref → GetGpuUsage | accelerator:usage:get |
| ACC-19 | GET /api/v1/accelerator/usages/{owner_service}/{resource_id}/bindings | 同上 → ListBindings；返回独立脱敏 DTO | accelerator:binding:list |
| QUOTA-04 | GET /api/v1/me/quota-accounts | 复用 QUOTA-02 账户读模型；tenant 来自 Principal | quota:account:self-read |

ACC-01～12 需要用户 JWT、平台身份及对应当前角色权限；平台身份不伪造下游 tenant=0。
ACC-13～19 需要用户 JWT、有效租户、套餐 `ACCELERATOR` 模块、当前角色权限；QUOTA-04
归既有 `TENANT` 模块，不要求 GPU 模块。AK/SK 白名单不增加上述路由。
上述 Governance 授权不能代替 Accelerator 启动配置中的 actor/action/tenant/cluster grant。

HTTP 不接受 tenant/actor/context 作为身份来源；服务从已验证 Principal 分列主体 type/id，
从持久 ResourceTenantID 映射取得 UUID。出站重建 metadata，清除 Bearer 和外部 identity headers。
客户端叶证书要求唯一精确 URI SAN `spiffe://ani.internal/service/ani-governance`，TLS≥1.3，
服务器由受管 CA 与配置的精确 DNS SAN 验证。配置见 `configs/accelerator.env.example`。

写请求保持 `{data:{...}}`；新增字段固定 snake_case；uint64/int64 按 protobuf JSON 为十进制字符串。
列表用 `page_size`（0 默认50，最大200）、`page_token`，响应 `next_page_token`；不伪造 total。
租户 DTO 不含 physical UUID、Pod 身份、原始 evidence、水位、source_fact_ref、完整 plan/runtime。
缺投影是 `USAGE_PROJECTION_NOT_READY`，不表示业务资源不存在；投影/ACK 不表示 Ready 或删除完成。
错误保留受限机器 reason，400/401/403/404/409/412/503/504 分别对应合同失败，原始下游正文不公开。

先执行 `admin sync-apis --dry-run`、复核后执行 `admin sync-apis`，再显式应用
`scripts/ops/sql/register-accelerator-permissions.sql` 登记精确权限关联。脚本不授予角色、不启用套餐、
不默认分配额度；部署方按明确租户/角色/套餐授权并刷新运行实例策略。接口目录存在不代表已授权。

`SyncGpuUsage` 仅独立恢复 worker 以 Governance 服务身份调用，不受当前用户委托撤销或套餐到期阻断；
`ObserveRelease` 仅 owner 直连，本批没有 Governance 公网代理。正式 registry 无生产 GPU owner，
新建仍失败关闭；预览的 owner_execution_readiness 取受控业务绑定，测试装配不能开启正式目录。

真实 HTTP 权限撤销验收发现历史策略装载未过滤禁用的 role-permission。该批将策略装载限制为
启用角色、启用 ALLOW 关联、启用权限和启用 API；禁用后必须刷新实例策略，旧 JWT 不因此获得
已撤销 API 权限。此修复也作用于共用该授权链的既有路由，需运行 Network/权限回归；初次失败
与修复后复跑分别保留，不修改历史批次结论。

本批隔离软件联调的 20 HTTP BFF、当前权限撤销、真实下游 mTLS、ENDED 后非空绑定脱敏及
非 GPU 回归已通过，详见 [BFF 验收证据](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-acc-v12-01/bff/README.md)。这些结果不表示
真实 GPU 调度或生产 owner 已启用；原始失败日志与修复边界一并保留。


## 2026-09-24 即时修复：Governance 配额恢复 Ent

用户明确纠正：pgx/sqlc 要求仅针对 Acc，Governance 配额迁移不在该要求范围内。
本次复用 QUOTA-01～04 和 ACC-01～19，不新增或变更接口、报文、权限及套餐关系。
完整配额账本、套餐配额、投递、DELETE/释放和 GPU usage sync 改为 Ent 查询及单个 Ent 事务；
删除 Conn.Raw/pgx 事务桥接、Governance sqlc 生成代码与强制门禁，保留显式 tenant 条件、
行锁/租约代次校验、既有数据库约束及版本迁移。历史验收不自动计入本次回归，
当前验证见 [Ent 修复记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-quota-ent-20260924/README.md)。整体接口登记仍未结项。

### 2026-09-24：Governance Ent 审核整改

套餐配额列表恢复已接受的 SEARCH 空白、数值和时间字段语义；字段白名单、参数绑定、原分页/过滤、鉴权入口和报文不变。配额及 usage sync 的持久化时间恢复同一 PostgreSQL 事务时间。补充事务异常回滚与生产 SQL 审计断言，修复完整 checkout 格式门禁、Ent race 选择及 CI 证据 artifact。具体修改与执行证据见 [审核整改记录](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/docs/evidence/gov-quota-ent-review-20260924/README.md)。本批不新增接口、权限、数据库迁移或生产启用事实。

### 配额合同兼容归位（本次工作树）

QUOTA-01～04 与 ACC 既有路由、字段和鉴权保持原合同。`ListQuotaDefinitionsResponse`、`GetTenantQuotaAccountsRequest`、`ListTenantQuotaAccountsResponse` 仍属于 `admin.service.v1`，仅移至同包 `quota_types.proto`；`i_quota.proto` 公开导入，`i_accelerator.proto` 直接导入。该裁决只覆盖这三个旧外壳，不声称全部 admin 消息已经迁入 quota 领域。Repo 返回领域项和分页结果，Service 组装 BFF 外壳。公开 quota reason 由 quota 源 Proto 生成，内部退额状态保留独立 gRPC code/message/details。套餐配额保留 PostgreSQL SEARCH、签名 token 与父套餐写锁适配；目录查询在数据库执行分页和存在性判断。本段是源码合同记录，不代表部署或真实 GPU 验收。

## 2026-09-26 排查：T15 R5 旧客户端 → 新服务 HTTP 合同对照

用冻结基线 d27847b 的 `api/gen/go` 生成物（buildinfo 仍含 `github.com/tx7do/go-crud/api v0.0.7`
与 protoc-gen-go-redact）编出旧客户端，与接管后同一提交编译的客户端跑同一份探测源码，分别调用
隔离环境中已 `admin init` 种子并通过 `admin check` 的当前服务，14 项用例在归一化 request_id、
随机用户名后缀与自增 id 后逐项一致（证据 migration/receipts/T15-r5.json）。

排查中记录三项既有接口行为，本批只登记不改接口：

- `GET /api/v1/admin/users/{id}` 读取不存在的 id 返回 `code=500`、`reason` 为空、message
  `ent: user not found`；旧新两侧解析结果相同，说明是服务侧错误映射而非报文差异。若后续要把
  “对象不存在”归到 404，需单独批次评估对既有前端与审计的影响。
- `GET /api/v1/admin/users:exists` 按 username 查询要求租户范围，平台管理员令牌（tenant_id=0）
  返回 `400 tenant scope required to check user exists by username`；调用方需显式带租户。
- 用户报文 `mobile` 由服务端脱敏为 `138****1234`，`email` 原样返回；旧客户端同样解析出脱敏值，
  因此脱敏合同与接管无关，属既有字段权限行为。

本轮结论仅为“历史客户端对新服务的 HTTP 读写、错误、权限、分页/mask 与脱敏合同一致”，不含 gRPC
（该配置只启 REST、SSE 与 asynq，无 gRPC 监听），也不含真实外部客户端应用联调（仍记 not_verified）。
整体接口登记仍未结项。

## IMAGE-01～11：Image MVP 契约输入（2026-09-30）

本次明确授权增加 `ImageService`，公开 DTO 在 `catalog/service/v1/image.proto`，
11 个 HTTP 绑定在 `admin/service/v1/i_image.proto`。具体路由/字段/权限按
Resource `docs/specs/image-api.md`；IMG-01 仅放入契约，尚未注册服务或授权。
租户由可信 Principal 经现有 ResourceTenantResolver 解析，不允许用户输入 tenant；
内部 pull material 不进入公开 Proto。目录使用本任务明确接受的 limit/cursor、固定元数据替换，
不改现有通用分页/mask。凭证交付仅 issue/reset，no-store 与日志排除在 IMG-06 实现验证。
模块 IMAGE 及精确权限后续单独接入，不借 NETWORK 权限；部署同步与授权仍须实证。

### IMAGE-01～11：IMG-06 安全接入实现输入

本批实现同一11路由的 Resource mTLS 客户端、BFF、IMAGE=14 模块和严格 HTTP 边界；保持 Kratos v2 和既有 Network 客户端不变。客户端重建三项可信 metadata；空间/登记/凭证回复逐项验证 tenant/scope/ID，内部 Runtime RPC 不注册公开路由。JSON 使用现有 Proto 编码（camelCase 字段、int64 字符串），额外身份字段/未知字段/重复字段拒绝。凭证路径所有响应 no-store/no-cache，审计只保留操作及认证归属，不收集 Image 请求体。

Image AK 使用现有 ANI-HMAC-SHA256 七行形式：算法、HTTP method、规范 path、`url.Values.Encode()` query、access key、timestamp、原始 body SHA256；空体为标准空SHA256。时间窗口仍为五分钟，必须同时通过当前租户状态/IMAGE套餐及角色API策略；新增签名支持不授予任何角色。`auth.ImageOperations` 为精确路由—permission映射，现有权限同步为这些路由使用 `image:*` code。部署仍需显式 API 同步 dry-run/应用、权限同步及角色/套餐分配；普通读者只选 ReadOnly 项，发布/凭证写仅租户管理员或指定发布者。现有实例不自动重播 bootstrap、不自动扩大 AK 权限。

环境变量为独立 `ANI_IMAGE_ADDR/CA/CERT/KEY/TIMEOUT`，空ADDR保持未配置；固定 Resource server SAN 仍为 `ani-network-service`。本段是源码合同；Fedora测试结果由 Resource 唯一 Image 账本关联，不代表部署或真Harbor通过。

## 功能组：ModelDev 训练与微调（MODELDEV-01～08、12）

当前活动合同为单一typed Intent，规则查询与请求见[前端合同](frontend-training-handoff.md)。同键原意图重放先于新目录解析；当前授权→固定候选 operation/execution/accepted_at→Res TRAINING→ModelDev Prepare/Finalize→Gov单事务持久受理及唯一ModelDevDispatch。网络不进入受理事务，CPU不写GPU原账。

FINETUNING 同时省略 `new_model_name` 与 `target_model_id` 表示仅发布训练产物，
该省略状态进入原 Intent/Snapshot/hash，不增加跳过字段，也不修改已受理意图。
仍须真实基模版本 READY、当前版本 GET/download-url GET 及训练创建权限；
无目标不要求模型 POST、版本 POST 或目标模型 GET。恰一目标沿用原登记和写权限，
同时提交两个目标仍拒绝。仅产物执行没有可恢复的 Model 登记，Resume 返回 412，
不会发起 ModelDev 登记 RPC；不将 publication 成功表述为 Model READY。

| 编号 | 入口/职责 | 当前合同 |
| --- | --- | --- |
| MODELDEV-01 | 创建执行 | implementation/compute/typed parameters/总时长；拒绝旧Release字段；202为持久受理 |
| MODELDEV-02 | 已退役Release操作 | import-release/enable/pause/binding/generation不再注册；历史证据不作为新受理许可 |
| MODELDEV-03～06 | 详情、产物、下载、停止、列表、日志 | 原当前授权/租户隔离和正交状态保留；下载不缓存、不存有效URL |
| MODELDEV-07 | 实现规则与输入 | `/api/v1/modeldev/implementations`；必填 `device_mode`（`cpu`/`gpu`，2026-10-29 `1d2142d9` 引入、owner `2c946f6` 同步强制），缺失/空值/非法/重复值400，无默认值；能力摘要仅来自该模式材料，按现有Readiness类型公开 `runtime_capabilities`/`runtime_capabilities_sha256`；可选 `kind`（`GENERAL_TRAINING`/`FINETUNING`，分页前筛选，非法值400）；公共compute复用TRAINING目录；输入独立 `modeldev:manage_input`；不要求Release导入批准 |
| MODELDEV-08 | inspect/reconcile/cleanup | 当前独立execution权限，精确原UID清理；不自动删PVC/publication/模型 |
| MODELDEV-12 | 恢复原模型登记 | `POST /api/v1/modeldev/executions/{execution_id}:resume-model-registration`；空 body/no query，仅原 CLOSED FINETUNING，当前 manage+GET+Model 原目标分支权限；同详情返回 `model_registration`/`history` |
| MODELDEV-13 | 资源曲线（一次一个 Pod） | `GET /api/v1/modeldev/executions/{execution_id}/resource-metrics`；必填 `pod_uid`/`container`/`start`/`end`/`metric_codes`，可选 `step_seconds`（30–3600、30 的整数倍）；容器由 layer 钉死（worker=`node`、step=`main`）；namespace 与 cluster 取本租户冻结受理，不接受调用方传值；每租户并发 4，BFF 排队；当前 GPU 无 exporter，`gpu_requested=true` 时 `gpu_available=false` |

源码实现、提供方正式模块、Fedora PG/负载、目标集群身份/执行、BFF/Model独立使用和GPU原账分别验收。用户本轮明确排除前端源码/部署修改；本文件仍交接BFF合同。本轮具体来源与实际结果在唯一活动方案 records 中，不沿用旧CPU-P01/Release实验作为本轮完成证据。

### MODELDEV-12：原登记恢复（2026-10-22）

请求仅原 execution UUID 路径，拒绝任何 body（包括 `{}`）、query/空 `?`、tenant/actor/key/target/参数覆盖。
Governance 每次复核当前正常租户用户的 `modeldev:manage_execution`、精确 Resume POST/ALL 和原执行 GET，
再从本租户原持久受理读取 FINETUNING source。当前 Model API 权限另需版本 GET、download-url GET、版本 POST，
原目标已有模型另需模型 GET，原意图新模型另需模型 POST；当前操作者不必等于历史受理者，但不能借服务身份补权。
owner 只恢复原 CLOSED 登记/核实原未知结果，沿原 registration key、目标和对象字节，使用持久有限恢复窗口，
不新建 execution、不重训/export、不续原 Timing。

HTTP 200 直接返回原执行投影：`model_registration` 仅 PENDING（无 IDs/错误）、
DEPENDENCY_UNAVAILABLE（无 IDs、仅 `MODEL_DEPENDENCY_UNAVAILABLE`）或 READY（三个规范非零 UUID：
registration_id/model_id/model_version_id，错误为空）。已有目标 READY 仍须等于原 model_id；响应 operation/execution/spec hash、
implementation/compute、原 accepted_at/deadline/Timing 均与原 source 绑定。
`history` 只投影 owner 持久的原 Run/Workflow 身份、观察/结束时间、终态、Workflow RV 与 node SHA；缺事实保留未知。
原 FAILED Run 与后续 registration READY 可以共存，BFF 不以实时 Model/KFP 查询补造成功或覆盖原历史。

当前 HTTP/PG 边界 GREEN 使用真实正常 JWT、Redis 会话、Casbin/当前授权和受限 PG 原受理，仅 MD 为外部 mTLS peer。
覆盖逐项撤权、跨租户、原目标/hash 绑定、未知响应一次 RPC 和原行/hash 不变；具名证据为唯一 PLAN 的
`records/workloads-governance-registration.json`。真实 provider Resume 与完整微调独立使用仍 **NOT_RUN**，
集成完整门禁/镜像部署按其实际固定树另记，不能由此推导业务通过。

## 服务间调用登记：Governance → ModelDev（CPU-P01，S2S-MODELDEV-CPUP01-01）

本段按 [服务间调用规则](contracts/service-to-service-calls.md) 第 1 节登记 Governance → ModelDev 的内部业务 RPC。登记源码合同现状、已确认的待迁移项与 2026-10-12 在 `ani-system` 的真实环境验证结果。已有功能条目 MODELDEV-01～08 不变，本段只补传输、身份、授权和上下文规则。

调用关系（§1 必填项）：

| 项目 | 登记值 | 源码位置 |
| --- | --- | --- |
| 调用服务身份 | 客户端唯一精确 DNS SAN `ani-governance`（无 URI/IP/email） | `data/modeldev_client.go` |
| 接收服务身份 | 服务器字面 DNS SAN `ani-modeldev-service`（固定 `ServerName` 校验） | `data/modeldev_client.go` |
| 传输 | TLS 1.3、双向证书、`RequireAndVerifyClientCert`，无明文 fallback | 双方 |
| 允许清单 | 接收方按精确 `FullMethod` 白名单，未知方法默认 `PermissionDenied`；训练日志流仅允许登记的 GetExecutionLogStream | `internal/server/governance_command.go` |
| 用户委托 | `PrepareAdmission`、`FinalizeAdmission`（两次只读计划）；Query/Management/Operations 各方法（新签发 mTLS 委派，附 `x-ani-authorized-method` 与 `x-ani-data-scope=tenant-all`） | 见下 RPC 列表 |
| 后台事实 | `AcceptExecution`、`ApplyCloseIntent`（持久投递事实，不伪造用户 Principal） | `data/modeldev_dispatch_client.go`、`data/modeldev_close_client.go` |
| 允许业务范围 | v2.1 单一 typed 普通训练/微调及原登记恢复；历史 CPU-P01 见下节 | 本组 |
| 租户来源 | Governance 已持久化的内部租户主键 → 业务租户 UUID 映射（ResourceTenantResolver），不使用公网 tenant | MODELDEV-01 |
| 幂等键 | `operation_id`（受理/投递）；关闭意图 `intent_generation` | — |
| 超时 | B=`ANI_MODELDEV_TIMEOUT` 默认3s；R=`ANI_MODELDEV_MODEL_RESOLUTION_TIMEOUT_SECONDS` 正整数秒、默认300。Readiness HTTP=R+B（前置B、RPC R）；Create HTTP=R+4B（前置B、Prepare R、GPU B、Finalize B、持久受理B）。AcceptedAt 在Prepare前冻结，后续各阶段同时受 AcceptedAt+T 和更短父deadline/取消约束；不自动retry | `data/modeldev_client_config.go`、`server/instance_timeout.go` |
| 出站元数据 | 每次重建 `x-ani-tenant-id`/`x-ani-actor`/`x-ani-request-id`；可信浏览器request_id另以 `x-ani-parent-request-id` 关联；查询另加 `x-ani-authorized-method`、`x-ani-data-scope`；不透传 Authorization/AK-SK/身份头 | 各 client |
| 证书轮换 | 客户端与服务器证书仅在启动期读取一次，续期后必须重启 Pod | — |

RPC 方法清单（接收方白名单，精确 FullMethod）：

- `ModelDevAdmissionService/PrepareAdmission`、`ModelDevAdmissionService/FinalizeAdmission`
- `ModelDevCommandService/AcceptExecution`、`ModelDevCommandService/ApplyCloseIntent`
- `ModelDevQueryService/GetExecution`、`ListExecutions`、`GetExecutionLogs`、`ListExecutionArtifacts`、`AuthorizeArtifactDownload`、`ListImplementations`、`GetInputVersion`、`ListInputVersions`、`CheckFinetuningReadiness`、`ListExecutionPods`
- `ModelDevManagementService/ImportCSV`、`ImportFinetuningDataset`
- `ModelDevOperationsService/InspectExecution`、`ReconcileExecution`、`PlanExecutionCleanup`、`ApplyExecutionCleanup`、`ResumeModelRegistration`（完整方法 `/ani.modeldev.v1.ModelDevOperationsService/ResumeModelRegistration`）

Resume 沿 Query/Operations 的当前方法委派重建 tenant/actor/request ID，不发送新的 registration key；
未知结果先按原 execution 查询或由 owner 核实原 key。严格 mTLS/CA/精确 SAN 和接收方法白名单保持，
不能以裸 gRPC、客户端重试或原受理时的权限替代当前授权。

日志 tail 与 SSE 均要求显式 `pod_uid`，来源为当前授权 `ListExecutionPods` 返回的原始 UID；无 UID 默认 worker 分支已删除。Pods 返回字段 `pods`、`layer=all/trainer/pipeline` 和 `includeRecycled` 保持现有合同。

新增 Readiness route-map 记录的 `source_sha` 为所列 BFF Proto 文件的 SHA-256。

Readiness 的公开请求仅 `base_model_version_id`、`compute_preset_id` 与可选 `finetuning_template`。
当前读取授权包括独立Readiness API、源版本详情/下载与TRAINING计算目录权，后者沿用
`modeldev:compute-preset:read`（SYSTEM目录API、MODEL套餐）。检查不要求目标写权，不分配GPU，
不创建受理或执行；200的ELIGIBLE/TEMPLATE_REQUIRED/UNSUPPORTED不是创建/训练成功。
响应仅有限能力、结构限额、模板计划与归一化模型概要；所有int64以十进制字符串编码。

模型读取/内容/结构/模板拒绝采用当前有限ErrorDetail原因：422不支持/内容策略，429验证并发忙，
403权限、404可见源不存在、409幂等冲突、503真实依赖不可用、504期限。下游私有消息、配置正文、
访问URL和凭据不得穿透。浏览器request_id与可信内部correlation_id需可关联。

Model导入仅其精确上传/版本入口扩大；ModelDev同步导入在现有管理CLI与ImportCSV/ImportFinetuningDataset使用 `ANI_MODELDEV_IMPORT_TIMEOUT_SECONDS`（正uint32秒，默认1800），保留更早父期限。其他ModelDev查询/命令保持B。
显式日志请求必须选择原Pod UID；通过 `/api/v1/modeldev/executions/{execution_id}/pods` 获取
TRAINER/PIPELINE和LIVE/RECYCLED的真实owner视图，禁止猜单worker或用同名Pod替代原UID。

配置依赖定性（本轮评审确认）：Governance 对 ModelDev 为**可选依赖**。`ANI_MODELDEV_*` 全空时客户端置 nil，仅禁用远程解析/投递并返回不可用，不启动失败。

与 [服务间调用规则](contracts/service-to-service-calls.md) 的差异收敛（三个差异均已实施并部署，源码改动 + 真实环境验证见下节）：

| 差异 | 收敛实现 |
| --- | --- |
| 客户端未显式拒绝含通配符的服务器证书 | `data/modeldev_client.go` 的 `VerifyConnection` 遍历 `DNSNames`，遇 `*` 即失败，再判定字面 `ani-modeldev-service`；客户端/服务器 DNS 名收敛为包内登记常量 |
| 接收方未在启动期校验自身命令服务器证书 | `internal/server/governance_command.go` 新增 `ValidCommandServerCertificate`，要求证书含字面 `ani-modeldev-service` SAN 且 EKU 含 ServerAuth、拒绝通配符；`cmd/ani-modeldev-service/command.go` 装配时校验，不通过即启动失败 |
| `bc.Command == nil` 走无 TLS grpc | `cmd/ani-modeldev-service/app.go` 保留构造该 server，但不加入 `kratos.Server(...)`，shell 模式不再监听明文业务端口 |

对应测试：Gov 侧 `data/modeldev_client_response_test.go` 增加「literal plus wildcard DNS」负向；ModelDev 侧新增 `internal/server/command_certificate_test.go` 覆盖注册身份、附加 SAN、通配符、未注册身份、缺 ServerAuth、过期六类；`internal/testsupport/commandtls` 夹具 SAN 对齐为生产字面 `ani-modeldev-service`。

身份命名约定：客户端 DNS 名固定 `ani-governance`，服务器 DNS 名固定 `ani-modeldev-service`，与部署侧 `GOVERNANCE_DNS_NAME` 取值登记一致。本链路使用 DNS SAN，不改为 URI SAN；已发布字段（`ResourceTenantId`/`AdmittedActorId`/`RequestedActorId`、`ErrorDetail.CorrelationId`、状态枚举）保持不变。

### 历史验证：2026-10-12 CPU-P01（已退役合同，不代表当前交付）

以含上述三个差异收敛的新镜像部署到 `ani-system` 后验证。两个服务新镜像 tag 均为 `20261012-s2s-modeldev`：

| 服务 | 部署镜像 | 运行标识 |
| --- | --- | --- |
| ani-governance | `172.16.101.10:5000/ani/ani-governance:20261012-s2s-modeldev` | `service.version=1.0.1-s2s-modeldev`，Pod 1/1，HTTP 监听 7788 |
| ani-modeldev | `172.16.101.10:5000/ani/ani-modeldev-service:20261012-s2s-modeldev` | `service.version=s2s-modeldev`，Pod 1/1，readyz/healthz 200 |

**正向（真实业务链路，实走 mTLS）**：以租户 A 重新登录取得当前 token，经 BFF 触发三条真实 Gov→ModelDev 调用，全部 HTTP 200 且返回真实业务数据：

| 调用 | 结果 |
| --- | --- |
| `GET /api/v1/modeldev/presets` | 200，返回真实 preset（`active_release_id`、`binding_generation=1`、参数规则） |
| `GET /api/v1/modeldev/executions?page_size=2` | 200，返回真实执行（`compute_state`/`delivery_state`/`close_state`） |
| `GET /api/v1/modeldev/input-versions?state=READY` | 200，返回真实输入版本（row_count=1024、feature_count=16） |

**负向（新校验确实生效）**：

| 用例 | 手段 | 结果 |
| --- | --- | --- |
| 接收方拒绝非受信 CA 的客户端证书 | 自建 rogue-CA 签发的同名 `ani-governance` 证书（SAN 相同、CA 不同）直连 ModelDev 9000 | 被拒：`tlsv1 alert unknown ca (alert 48)` |
| 正向对照 | 真实 Gov 客户端证书直连 9000 | 通过：握手成功、无 alert |
| 协议版本下限 | 强制 TLS1.2 直连 9000 | 被拒：`tlsv1 alert protocol version (alert 70)` |
| 接收方服务器证书身份 | 检查 `modeldev-runtime-private` 证书 | `SAN=DNS:ani-modeldev-service`、EKU=ServerAuth，字面且无通配符 |
| M-1 启动期服务器证书校验 | ModelDev 新镜像启动 | 通过（SAN/EKU 不符将 fail-fast） |
| G-1 客户端拒绝通配符服务器证书 | `data/modeldev_client_response_test.go` 的 `literal_plus_wildcard_DNS` | PASS（Gov 侧遇 `*.ani.internal` 即拒） |

**边界（如实标注）**：

- 上述正向结论在本次会话当日有效证书（`notAfter=Oct 12 06:55:28 2026 GMT`）有效期内取得；会话结束时该证书**尚未重签**，证书到期或重启后须重做正向验证。证书轮换需重启 Pod，见上表"证书轮换"行。
- 负向验证在节点侧用 `openssl s_client` 直连 ModelDev 9000 完成，未改动任何集群对象；rogue-CA 材料为一次性临时生成，测试后已清理。
- 本次未验证 GPU 退额、Stream 方法授权、多租户 dispatch 等相关但不在本链路范围的行为。

## LIST-TOTAL-01：Resource 列表总数（2026-10-08）

本次增量保留既有路由、鉴权和 cursor/limit，给 `GET /api/v1/networks/vpcs`、`GET /api/v1/networks/eips`、`GET /api/v1/images/registrations` 的响应增加 `total`（Proto int64，HTTP JSON 十进制字符串）。总数是受信租户/scope 和相同筛选条件下的匹配数，不是本页 items 长度，且不应用 cursor/limit；空列表返回 `"0"`。本批不改变其他领域的分页协议。

Network 客户端改用 Resource 拥有的 `network.v1` 生成类型，保持 mTLS SAN 和服务身份。Resource 已先发布到 main `73c41706653479b85c03a30505874acc41855505`；本仓锁定 `v0.0.0-20261008083036-73c417066534`，移除不再引用的旧 Network 模块。未加入 replace/go.work。发布前源码联编及数据库筛选证据见 [Resource LIST-TOTAL-20261008](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/73c41706653479b85c03a30505874acc41855505/docs/execution/records/LIST-TOTAL-20261008/README.md)。

发布依赖的独立验证在 `ssh ubuntu`（`i-8yg2l7u8`）、Go 1.26.7、`GOWORK=off` 下完成，目录为 `/home/ubuntu/workspace/ani-network-service-runs/list-totals-20261008/publication/governance`。以下命令均 pass：

- `go test -mod=readonly -count=1 -run 'Test(Network|Image)' ./app/admin/service/internal/service ./app/admin/service/internal/data ./tests/imagecontract`
- `go build -mod=readonly ./app/admin/service/cmd/...`
- `go mod tidy -diff`、`go mod verify`；模块解析为上述已发布版本，无 Replace。
- `scripts/image-joint-integration`，Resource contract test binary 从上述下载模块编译。HTTP 非零/零 total、权限拒绝和真实 mTLS → Resource Catalog → PG 空列表断言通过，独占 PG/Redis 均已清理。

日志保留在执行任务目录外的本地 `/tmp/list-totals-governance-published-validation.log`；未纳入业务提交。部署和目标集群业务验收仍为 `not_verified`。

## DETAIL-FORMAT-01：公开详情直接返回资源对象（2026-10-09）

基于最新 Resource main `73c4170`、Governance main `134686f`，扫描全部 HTTP GET 绑定，统一以下 9 个只有单资源对象的详情响应：

| 方法 | HTTP 路径 | 直接返回 |
|---|---|---|
| GetVPC | `/api/v1/networks/vpcs/{vpc_id}` | VPC |
| GetEIP | `/api/v1/networks/eips/{eip_id}` | EIP |
| GetOperation | `/api/v1/networks/operations/{operation_id}` | Operation |
| GetVPCSnat | `/api/v1/networks/vpcs/{vpc_id}/snat` | VPCSnat |
| GetImageSpace | `/api/v1/images/space` | ImageSpace |
| GetPublisherCredential | `/api/v1/images/publisher-credential` | PublisherCredential |
| GetImage | `/api/v1/images/registrations/{image_id}` | ImageRegistration |
| GetInputVersion | `/api/v1/modeldev/input-versions/{input_version_id}` | InputVersionView |
| GetExecution | `/api/v1/modeldev/executions/{execution_id}` | ExecutionView |

HTTP 绑定使用 `response_body`，ModelDev 的手写路由同步展开。字段名、权限、租户隔离和内部 gRPC 消息保留；普通 CRUD 详情已直接返回对象。列表、日志、导航、统计、MFA 状态及签发/创建等有多字段业务语义的响应保留原结构。固定生成链同时更新 HTTP 客户端与 OpenAPI；后处理移除 gnostic 在显式 200 前追加的推断 200，避免重复 YAML 键。没有手改生成物。

Resource 仅提供内部 gRPC，本批同步公开 JSON 规格和 SBOM，已发布 main `60702b2bcfe441b8ef86991b95274d0da34773ea`；本仓锁定 `v0.0.0-20261009024544-60702b2bcfe4`，无 replace/go.work。

验证主机为 `ssh ubuntu`（`i-8yg2l7u8`），Go 1.26.7，`GOWORK=off`，任务目录 `/home/ubuntu/workspace/ani-network-service-runs/flat-details-20261009`：

- Resource `make verify BUF=<pinned-buf> SQLC=<pinned-sqlc>`、SBOM 生成及供应链结构检查：pass。
- Governance `make api`：pass；9 个详情的 HTTP JSON、鉴权拒绝和 OpenAPI Schema 检查：pass。
- `go test -mod=readonly -count=1 -run 'Test(DetailHTTP|DetailOpenAPI|ModelDevQueryRoutes|ModelDevListAndLog|Network|Image)' ./app/admin/service/internal/server ./app/admin/service/internal/service ./app/admin/service/internal/data ./tests/imagecontract`：pass。
- `go build -mod=readonly ./app/admin/service/cmd/...`、`go mod tidy -diff`、`go mod verify`：pass。
- `go test -mod=readonly -tags=modeldev_pg,modeldev_contract -run '^$' ./app/admin/service/internal/server`：pass，仅为受影响 ModelDev 集成用例的编译检查。
- `scripts/image-joint-integration`：pass。Resource helper 从上述已发布模块编译；真实鉴权 HTTP → mTLS → Resource → PostgreSQL 的空间与未签发凭证 GET 返回展开字段，读取不隐式签发；镜像详情、列表总数和权限/缓存保护检查保留。独占 PG/Redis 和 helper 数据库均已清理。

执行日志位于本地 `/tmp/flat-details-resource-verify.log`、`/tmp/flat-details-resource-sbom.log`、`/tmp/flat-details-governance-generate-test-3.log`、`/tmp/flat-details-governance-published-validation.log`、`/tmp/flat-details-governance-real-details.log`。首次注解重复和 OpenAPI 重复 200 的失败日志保留，未放宽断言。9 路由测试冻结传输结构；真实数据库链路的上述范围与 ModelDev 仅编译检查分别报告。部署和目标环境业务验收仍为 `not_verified`。

<a id="list-cursor-01"></a>

## LIST-CURSOR-01：`GET /api/v1/admin/users` 改为 limit + cursor 游标分页（2026-10-10）

本批仅将 `GET /api/v1/admin/users` 接入 `limit/cursor`，保留显式页码兼容；其余 admin 列表不因共享请求消息增加 cursor 而完成迁移。参数、响应、签名绑定、排序、过滤、身份与密钥规则见 [存量游标合同](contracts/cursor-pagination-legacy.md)。

以下保留 2026-10-10 的历史执行与验收记录，不表示本次文档修订重新运行了这些检查；当前源码差异和后续迁移范围见该规范。

本机（Windows，Go 1.26.9）已执行：`go build ./...`；`go vet`（data/service/server/pkg/localdeps/go-crud/pkg/crypto）；`go test ./pkg/localdeps/go-crud/... ./pkg/crypto/... ./app/admin/service/internal/data ./app/admin/service/internal/server`，覆盖游标 codec 往返/篡改/绑定不匹配/缺密钥/超长/与 token 格式互不兼容、limit 默认值与边界、筛选指纹对筛选与排序敏感、键集谓词四种 NULL 分支的 SQL 文本、repo 层翻页不重不漏/同值排序兜底/`total` 不受窗口影响/可空列升降序都覆盖全量/`fieldMask` 裁剪后仍能生成游标/400 分支。生成物由暂存副本重放已验收 10 个模板后与工作树逐字节比对再回写（未手改生成物、未在正式输出根裸跑 Buf）；分页契约测试 `TestT08PagingRequestContractIsUnchanged` 按本批授权增列 `cursor = 7`。

未验证与边界：SQLite 夹具驱动把 `time.Time` 列存成 Go 时间字符串并按整串文本比较（含单调时钟后缀），时间列键集在 SQLite 上不可靠，因此时间列排序只做编码/解码与谓词单测，真实断言留给部署环境；其余 26 个列表接口的 `cursor` 仅出现在 OpenAPI 参数表，无语义。

部署实测（ani-system @ ani-01，2026-10-10）：本机交叉编译 linux/amd64 → `craft_images.py` 打成 docker-archive（tag `172.16.101.10:5000/ani/ani-governance:20261010-cursor`，53.3MB）→ 经 ttyd 通道上传（整包 md5 校验通过）→ ani-01 `ctr -n k8s.io images import` 并推到可写 registry 5001 → 重启只读 registry 5000 → 该 tag 可见（`tags: ["20261008","20261010-cursor"]`）→ `kubectl set image deployment/ani-governance server=...:20261010-cursor -n ani-system` → rollout 成功，新 Pod 1/1 Running（调度到 ani-03，从 registry 拉取）。本批无 schema 变更，未跑迁移；回滚目标是换镜像前的 `localhost/ani-governance@sha256:9626e873c009650ebb41a9f783b82aede430cb3b08b3e9f72cb54e57e175d829`。

实测（`verify-cursor.py`，全程只读，21 项断言全通过）：平台登录后 `limit=3` 取首页 → 3 条、`total="5"`、返回 `next_cursor`；带游标取第二页 → 2 条、与首页无重复、`total` 仍为 `"5"`；`limit=0/101/abc` 均 400；篡改游标 400；`cursor`+`page` 互斥 400；换筛选复用游标 400；换排序复用游标 400；按 `created_at` 降序 limit=2 翻页 → 5 行无重复且覆盖 `total`；末页 `next_cursor` 为空串。回归冒烟（`smoke-regression.py`，fail=0）：`GET /api/v1/admin/users?page=1&pageSize=2` 旧路径仍 200、2 条带 `total`；`plans/roles/tenants` 带 `cursor=bogus` 仍 200（这些接口不接入游标语义）；无 token 401、`/api/v1/admin/me` 200。

两条实测修正（以实测为准）：① 首页不传排序时的默认排序就是 id 降序，因此"用 `orderBy=["-id"]` 复用游标"属于同一绑定、返回 200 才正确，验收用例已改用 `orderBy=["-created_at"]`；② 本部署的响应编码会输出未填充字段，末页 `next_cursor` 是空串（键存在）而非键缺席，仍满足"末页为空"。

未执行/遗留：本批镜像内含上一批次（access-key 幂等）的代码，而该批迁移当时未在 ani-system 的库上执行（本批不含 schema 变更、未跑 Atlas）。**该遗留已于 2026-10-10 处置**：补跑迁移 `20261010131938` 并授予 `gov_app` 权限后，`POST /api/v1/auth/api-keys` 带 `idempotency_key` 的幂等行为已实测通过（见 AK-IDEM-01）。SQLite 上的时间列键集仍不可断言（夹具驱动存储形态问题），PostgreSQL 上已由上述 `created_at` 翻页用例覆盖。

<a id="list-cursor-02"></a>

## LIST-CURSOR-02：`GET /api/v1/admin/roles` 与 `GET /api/v1/auth/api-keys` 接入 limit + cursor 游标分页（2026-10-10）

在前一批（LIST-CURSOR-01，users）基础上，把 `GET /api/v1/admin/roles` 与 `GET /api/v1/auth/api-keys` 两接口由 `page/pageSize` 改为 `limit` + `cursor`，对外形状对齐 users/vpc：`{items, next_cursor, total}`。同时修复 users 接口审查发现的 5 项安全边界问题（§3.1~§3.5）。共用逻辑抽到 `data/cursor_shared.go`（`resolveCursorSortColumn`/`checkCursorWindowConflict`/`shouldUseCursorPaging`/`validateCursorRequest`），三个接口共用同一 codec/keyset/HMAC 实现。

报文与游标语义同 LIST-CURSOR-01：`limit` 不传取 20、只接受 1~100，越界或非法直接 400；`cursor` 与 `page/pageSize/offset/token/noPaging` 互斥（同时出现 400）；游标绑定资源、上下文、可信租户与"筛选+排序"指纹，篡改/跨租户/换筛选/换排序复用一律 400，不降级为首页。两接口均**新增响应字段 `next_cursor`**（`ListRoleResponse.next_cursor = 3`、`ListAccessKeyResponse.next_cursor = 3`），未新增 `(path, method)`，不触发 API 目录与权限关系变更。

roles 默认排序 `sort_order` 升序（同值按唯一 ID 升序兜底）；可排序列限 `id/tenant_id/sort_order/created_by/name/code/type/data_scope/status/created_at/updated_at`，未列出的排序列 400。api-keys 默认排序 `id` 降序（新建凭证最新在前）；可排序列限 `id/tenant_id/role_id/name/access_key/status/expires_at/last_used_at/created_at/updated_at`，**白名单显式排除 `secret_ciphertext`**，密文既不入响应也不入游标材料。

roles 取页后需逐条回填权限/组织单元/字段权限，故 keyset 采用延迟裁剪（`CursorSpec.DeferHelperTrim`）：辅助列（唯一 ID、排序列）保留到关联回填完成再清理；api-keys 输出需把内部 ON/OFF 映射为公开布尔 `is_active`，沿用"取一页 ID → 二次查询实体 → 显式构造 DTO"的既有做法，`secret_ciphertext` 不进响应。

§3 五项修复（users 为主，两新接口按同一套约束实现）：① `FilterExpr` 规范化（`pagination/filter/normalize.go`）在指纹计算前递归重排 Conditions，消除 Go map 随机遍历导致的"同语义不同指纹"误判；② 游标参数校验前移到空关系筛选短路之前，空集不再绕过 limit/游标 fail-closed，`noPaging` 与 `cursor` 同时出现也 400；③ `fieldMask` 不含 `id` 时延迟裁剪辅助列，保证关联字段仍被回填（不再因掩码裁掉关联视图字段而返回空）；④ `EncodeCursor`（`cursor_codec.go`）补长度上限校验，与解码侧范围一致，超长排序值明确失败而非产生不可解码游标；⑤ `CreateAccessKeyData` 沿用旧 `CreateAccessKeyRequest.data`（原 `AccessKey`）的字段编号（`name=2`/`expires_at=5`/`role_id=7`）并对旧编号 `1,3,4,6,200` 显式 `reserved`，使旧 Proto 的 gRPC 客户端仍能解码。

本机（Windows，Go 1.26.9）已执行：`go build ./...`、`go vet`（data/service/pkg/localdeps/go-crud/pkg/crypto）；`go test ./pkg/localdeps/go-crud/pagination/... ./app/admin/service/internal/data ./app/admin/service/internal/service`，新增定向用例覆盖：规范化对顺序稳定/递归进 Groups/不增删条件（§3.1）、空关系筛选 fail-closed 与 noPaging 互斥（§3.2）、fieldMask 不含 id 仍回填关联（§3.3）、`EncodeCursor` 超长失败（§3.4）、旧创建消息 wire 字节新解码字段正确（§3.5）、两接口键集翻页不重不漏/同值排序兜底/`limit+1` 边界/末页游标为空/`total` 与筛选一致、api-keys `is_active` 映射与 `secret_ciphertext` 不入响应与游标材料、roles 关联填充完整。

部署实测（ani-system @ ani-01，2026-10-10）：交叉编译 linux/amd64 → `craft_images.py`（tag `172.16.101.10:5000/ani/ani-governance:20261010-list-cursor`，53,732,859 字节 / md5 `8b010072a2a7b2caa100c36a1ba17403`）→ ttyd 通道上传（整包 md5 `VERIFY: OK`）→ ani-01 `ctr -n k8s.io images import` 并推 5001 → 重启只读 registry 5000 → 该 tag 可见（`tags: ["20261008","20261010-cursor","20261010-list-cursor"]`）→ `kubectl set image deployment/ani-governance server=...:20261010-list-cursor -n ani-system` → rollout 成功，新 Pod `ani-governance-59fbffcc9b-h6gsw` 1/1 Running（调度到 ani-03，从 registry 拉取）。本批无 schema 变更，未跑迁移；回滚目标为换镜像前的 `172.16.101.10:5000/ani/ani-governance@sha256:de9c9fb87a55c76b01db96e59a92f2d8af2141ae0aadaff9ccc32abf1fd9f6d5`。

实测（全程只读）：`verify-cursor.py`（users，本次扩至 24 项断言）全通过，含 §3.2 两项（空关系筛选 + `limit=101` / `cursor=bogus` → 400）与 `noPaging` + `cursor` 互斥 → 400；`verify-list-cursor.py`（roles 平台/租户、api-keys 租户，36 项断言）全通过——两接口 `limit=2` 返回 `next_cursor`/`total`（十进制字符串）、`limit=0/101/abc` → 400、`cursor=bogus` → 400、篡改游标 → 400、`cursor`+`page` 互斥 → 400、换排序（`-created_at`）复用游标 → 400、roles 平台上下文连翻 5 行无重复且末页游标为空串、api-keys 平台上下文 → 403（非租户用户拒绝）、roles 平台游标用于租户上下文 → 400；`smoke-regression.py` fail=0（users 旧 `page/pageSize` 路径仍 200、plans/tenants 带 `cursor=bogus` 仍 200、roles 坏游标 400 且合法分页 200 带 `next_cursor`/`total`、无 token 401、`/api/v1/admin/me` 200）。

实测修正：`smoke-regression.py` 原"roles 忽略 cursor 仍 200"断言基于 LIST-CURSOR-01 的旧事实（roles 当时未接入游标），本批 roles 已接入，该行改为"坏游标 → 400 + 合法分页 200 带新字段"，以实测为准。

未验证与边界：api-keys 租户上下文在本环境无存量凭证（`total="0"`），其键集翻页与 `is_active` 映射的**真实翻页**未在部署环境覆盖（同值排序兜底与映射由 SQLite repo 用例覆盖，`secret_ciphertext` 不入响应/游标材料由用例覆盖）；roles 租户上下文仅 2 条数据，下一页游标断言被跳过（平台上下文 5 条已覆盖真实翻页）。

事后核查补充修复（同批，2026-10-10）：核查三接口 fieldMask 行为时发现 api-keys 的既有缺陷——`fieldMask` 被当作 SELECT 字段掩码，若不含 `id`，取页结果的 ID 退化为零值，随后"按 ID 二次查询实体映射 `is_active`"命中不到任何行，整页 `items` 变空（`total` 正常但 `items=0`）。游标路径（`listAccessKeysWithCursor`）与存量非游标路径（`List` 的 `ListWithPaging`）都受影响，非本批引入。修复：新增 `data/cursor_shared.go` 的 `withFieldMaskIncludingID`（fieldMask 非空且不含 id 时克隆请求补入 `id`；空掩码即 SELECT * 原样返回），在两条路径取页前调用。`id` 是主键且与游标分页内部"为排序/回填多读唯一 ID"的既有处理一致。新增用例 `TestAccessKeyRepoSqlite_ListCursorFieldMaskKeepsPageItems` 覆盖两路径。

同批核查同时观测到**同类但表现不同**的现象：users/roles 的**非游标路径**在 `fieldMask` 不含 `id` 时，`items` 完整返回但关联 ID（users 的 `role_ids`、roles 的 `permissions`）为空——因为该路径同样先用 `fieldMask` 裁掉 id，随后 `enrichRelationIDs`/`fillPermissionIDs` 以 `item.GetId()`（已为 0）取关联；两接口的**游标路径**因有 `DeferHelperTrim` 不受影响。该现象非本批引入，是否修复待确认，暂未改动。

补充修复的部署实测（2026-10-12，ani-system @ ani-01）：镜像 `172.16.101.10:5000/ani/ani-governance:20261012-list-cursor-fix`（53,743,519 字节 / md5 `abc31c32c24ba753ae1920e847218d0a`，上传 `VERIFY: OK elapsed=65s`）→ registry tags 追加该 tag → `kubectl set image ... server=...:20261012-list-cursor-fix -n ani-system` → rollout 成功，新 Pod `ani-governance-7dc7f449bd-frdkx` 1/1 Running（ani-03）。实测：`verify-cursor.py` **24/24 PASS**、`verify-list-cursor.py` **38/38 PASS**（新增 2 项：api-keys `fieldMask=name` → 200 且 `total` 与无掩码一致）、`smoke-regression.py` fail=0。本环境 api-keys 租户上下文无存量凭证（`total="0"`），fieldMask 修复的**真实翻页**仍只在 SQLite 用例覆盖。

<a id="role-code-filter-01"></a>

## ROLE-CODE-FILTER-01：`GET /api/v1/admin/users` 支持按角色编码筛选（2026-10-12）

在既有 `role_id` / `role_ids__in`（按 ID 过滤）之外，`query` 新增按**角色编码**过滤，省去前端“先查角色 ID 再拼用户条件”的两段调用。参数为顶层条件：

- `role_code`：单值精确匹配（`{"role_code":"tenant:manager"}`）。
- `role_codes__in`：集合匹配（`{"role_codes__in":["tenant:manager","tenant:member"]}`），**必须是数组**。

语义与边界：

- 二者可同时出现。`role_code(s)` 的解析结果只并入**角色 ID 集合**，与 `role_id(s)`（同为角色维度）**合并取并集后去重**（`sliceutil.Unique`）；随后角色、组织单元、岗位三类关系各自的用户集合按既有口径**取交集**（`queryUserIDsByRelationIDs`）。因此 `role_code` 与 `role_id` 之间是并集，而与 `org_unit_id(s)` / `position_id(s)` 之间是交集——如「管理员 A 不属于组织 X、成员 B 属于 X」时，`role_code=tenant:manager + org_unit_id=X` 返回空（非并集）。这是沿用既有 ID 组合语义，不做改变。
- 编码需在明确的租户范围下解析。**单轴模型**：租户上下文直接使用当前租户；平台上下文的范围由**顶层 `tenant_id` 条件是否存在**表达——
  - 给出顶层、`EQ`、纯数字的 `tenant_id=v`（`0 ≤ v ≤ MaxUint32`）→ 解析范围 = `Tenant(v)`；`v=0` 表示**平台行范围**（只匹配 `tenant_id=0` 的平台侧角色，如 `platform:admin`），用于「平台管理员列表」。
  - **缺省 `tenant_id`** → 解析范围 = **全部租户**（`AllTenants`），用于「跨租户管理员列表」（如一次查各租户的 `tenant:manager`）。**这是本批的行为变更**：原「平台 + `role_code` 缺 `tenant_id`」为 400，现改为 200 跨租户。
  - `tenant_id__ne` 等非 `EQ` 写法、非数字值一律 400。校验扫描**全部**顶层 `tenant_id` 条件（不因先遇到合法 EQ 而短路），出现多个取值不同的合法 `EQ` 视为歧义范围同样 400。
- ⚠️ **同名角色提示**：`role.code` 仅在 `(tenant_id, code)` 唯一，某租户可自建同名 `platform:admin`。要「只查平台行」必须**显式** `tenant_id=0`；若写 `{"role_code":"platform:admin"}`（不带 `tenant_id`），会走 `AllTenants`，把各租户同名角色一并匹配。前端须按语义选择写法。
- **不可退化**：条件出现但编码不存在、`role_codes__in` 为显式空数组、或 `role_codes__in` 传了非数组标量时，结果必须是 `{total:"0", items:[]}`，**不得退化为“等于无条件”而返回全量**。空数组/非法标量按空结果短路，不进入租户解析。
- 角色编码条件**必须出现在顶层 `Conditions`**；写入 `$and/$or` 子树会被 fail-closed 拒绝（400），因为摘出机制只处理顶层条件，子组内的虚拟列会落到白名单外被静默丢弃。
- ⚠️ **`$and/$or` 同层键被丢弃（实测）**：`QueryStringConverter.parseRawQuery` 处理含 `$and`/`$or` 的对象时**只解析该逻辑键，同层其它键静默丢弃**。因此「顶层 `role_code` + 同层 `$and`」写法里 `role_code` 会被丢掉，请求退化为只按其余条件过滤，既不报 400 也不按角色筛选。前端/调用方须把 `role_code(s)` 与 `tenant_id` 都平铺在最外层。
- `tenant_id` 与 `role_code(s)` 同现时，`tenant_id` 也应**位于顶层**：代码对「`role_code` 在顶层而 `tenant_id` 落在 `$and/$or` 子树」有 fail-closed 拒绝（400，防“想限定单租户却静默放大为 `AllTenants`”）。**该形态经 HTTP `query` 字符串实际不可达**（受上一条的丢弃行为影响，`role_code` 会先被丢），该 400 分支仅对直接构造 `FilterExpr` 的调用方（如 gRPC 入参）生效。
- 该行为是“条件存在性”驱动（新增状态位），与 `role_ids__in` 的“有命中用户才短路”不同：`role_code(s)` 条件出现而解析不到任何角色 ID 时必须短路为空。

首个租户用 `role_code` 即可，平台管理页面用 `tenant_id + role_code(s)`（平台行）或 `role_code(s)`（跨租户）。

部署实测（ani-system @ ani-01，2026-10-12，单轴模型；**已合入 main 后重部署**）：合入 `origin/main`（含 platform-network / S2S ModelDev 等）后重新构建镜像 `172.16.101.10:5000/ani/ani-governance:20261012-merge-role-scope`（53,925,987 字节 / md5 `FAAC1F8DDA5D0CEF095F034F46A17ECB`，上传 `VERIFY: OK elapsed=98s`）→ registry tags 追加该 tag → `kubectl set image ... server=...:20261012-merge-role-scope -n ani-system` → rollout 成功，新 Pod `ani-governance-f775c6547-pjrlc` 1/1 Running（ani-03）。合入后实测 `verify-role-code-filter.py` **22/22 PASS**：平台缺 `tenant_id` + `role_code` → **200（AllTenants）** 且命中数 ≤ 全量（all=2 base=5）；`tenant_id__ne` / 非数字 → 400；`tenant_id=0` + `role_code=platform:admin` → 200 命中平台行（total=2）；顶层 `EQ` 数字 `tenant_id` + `role_code` → 200 且命中数 ≤ 全量；不存在编码 → `total="0"`（base=5 不退化）；空数组 → 200 且 `total="0"`；标量 `role_codes__in` → 200 且 `total="0"`；租户上下文 `role_code` → 200、不存在编码 → `total="0"`。同批回归 `verify-cursor.py` **24/24**、`verify-list-cursor.py` **38/38**、`verify-permgroup-subtree.py` **12/12**、`smoke-regression.py` **fail=0** 全绿。本批无 schema 变更，未跑迁移；回滚目标为换镜像前的 `172.16.101.10:5000/ani/ani-governance:20261012-role-code-scope`（旧 Pod 已在 rollout 中替换）。

> 排障记录（2026-10-12 重部署）：新 Pod 首次拉起后 liveness/readiness（tcpSocket 7788）连续失败并触发重启（exitCode 2）。经诊断 pod 内独立运行 `/app/bin/server -c /run/configs` 可正常监听 `:7788`；失败窗口恰为**进程启动期**——casbin 策略加载、asynq 定时任务注册与 DB 连接之间耗时超过 `initialDelaySeconds=15`，探测在端口监听前即判死。该现象**非本批改动引入**（失败点全部在 infrastructure 装配段，早于 users 过滤逻辑），且容器**在重启后即自愈**为 1/1 Running。合入 main 引入的 platform-network 装配未改变该启动时序。最终状态 `availableReplicas=1 / readyReplicas=1`，接口全部 PASS。

本机（Windows，Go 1.26.x）已执行：`go build` / `go vet ./app/admin/service/...` 通过；`go test ./app/admin/service/internal/data -count=1` ok，新增/修正 SQLite 定向用例覆盖集合/单值交集、不存在编码与空数组与非法标量的空结果（不退化）、`$and/$or` 子树 fail-closed（`role_code` 与 `tenant_id` 两处）、与 `role_id` 的并集去重、平台租户范围（缺省 AllTenants / `tenant_id=0` 平台行 / 跨租户命中多租户 / 租户上下文不跨租户 / 同名冒牌角色不串号）、游标路径与页码路径一致。`app/admin/service/internal/service` 包仅 `TestTaskService_RealAsynqSchedulerLifecycle` 因缺 `ANI_TEST_REDIS_URI` 环境性失败，与本批无关。

边界：角色编码解析按“同租户 + `code IN (...)`”或“无租户谓词全表匹配（仅平台/系统上下文）”查询，编码在租户内唯一由既有约束保证；平台上下文的跨租户 `AllTenants` 为缺省行为（无修饰符），属有意设计。

<a id="permgroup-subtree-01"></a>

## PERMGROUP-SUBTREE-01：`GET /api/v1/admin/permission-groups` 支持 `exclude_subtree` 与父子循环校验（2026-10-12）

### 列表排除子树

`query` 新增顶层 `exclude_subtree`（单个分组 ID）：结果**排除该分组自身及其全部后代**，用于“选择上级分组”等需要从候选里去掉当前节点子树的场景。

- 列表 `items` 与 `total` 使用同一排除条件，二者一致（不复用未排除的 Count）。
- 传入不存在的 ID 或空值时按“无排除”处理，不报错。
- 实现按 `parent_id` 链 BFS 收集子树，带 `visited` 防环（预置环不会死循环）；脏数据形态（环）已在 SQLite 用例覆盖。悬空 `parent_id` 在开外键的库上无法注入，未覆盖。

### 创建/更新父子循环校验

- `PERMGROUP-03`（创建）：父节点必须存在；不允许把自身设为父。
- `PERMGROUP-04`（更新）：携带 `parent_id` 变更时，落库前预检 `parent_id` 存在性并拒绝“父为自身或自身后代”（返回 400，`cannot move permission group under its own descendant`）。防环判断只依赖节点当前子树，**不依赖事务**；事务仅用于保证“`parent_id` 落库 + 全部后代物化路径重算（`relocateSubtree`）”的原子性，因此**仅在请求携带 `parent_id` 变更时启用**，未改父的更新保持既有非事务路径。被拒的更新不改动任何数据。
- `updateMask` 路径用 proto JSON 的 camelCase（`parentId`）；写 snake_case `parent_id` 会在解码阶段直接失败（CODEC），测到的是解码失败而非防环校验，写用例/脚本时须避免这种“假通过”。

部署实测（同批镜像 `20261012-role-code-filter`）：`verify-permgroup-subtree.py` **12/12 PASS**——`exclude_subtree` 排除自身与全部后代（该环境仅 1 个分组、无后代，`total="0"` 与 `items` 一致、总数 = 全量 − 子树）、不存在 ID 不改变总数；`PUT` 把父设为自身 → 400 且原因体为 `BAD_REQUEST / cannot move permission group under its own descendant`（非 CODEC 解码失败），被拒后 `parentId` 未变。

本机已执行：`go build` / `go vet ./app/admin/service/...` 通过；`go test ./app/admin/service/internal/data -count=1` ok，新增 SQLite 定向用例覆盖排除自身+后代与 total 一致、不存在 ID/空值不排除、环状脏数据不死循环、更新成环被拒且无残留、合法改父原子重算子树路径、创建非法父被拒。

边界：本环境权限分组仅 1 条且无后代，`exclude_subtree` 的真实“多后代排除”由 SQLite 用例覆盖；并发 move 的残余竞态（两个请求同时改父子链）未加行锁，属已知未覆盖项。

## GOV-ACC-INF-V12-WIRING-01：推理创建与删除接线（2026-10-09）

本节记录当前接线。INF-01/02 为治理 BFF 的创建和删除入口；其余列表、详情、操作和生命周期路由见当前 Proto。旧的“已撤下”描述已废弃，权限登记和模块 `INFERENCE=15` 以当前生成目录及 SQL 登记脚本为准。

用户明确批准本仓登记 `INFERENCE=15`。新增推理模块和两项独立动作；当前源码登记与实际授权/部署分别验收，不据此向基础套餐、所有角色或生产环境自动开放。

| 编号 | 入口 | 报文与可靠受理 | 当前授权条件 |
|---|---|---|---|
| INF-01 | `POST /api/v1/inference/services` | 请求顶层使用扁平推理字段（UUID idempotency_key、model_version_id、engine_image、engine_type、完整单字符串 command、cpu、memory、replicas、四字段 gpu）；不暴露内部附件、Kubernetes 数组或嵌套 resource/runtime/engine。调用现有 AcceptGpuCreate：授权、重放、Resolve、冻结、Ent一次占额、持久 dispatch；返回原 operation/resource、replayed、ACCEPTED | 可信 tenant-user Principal；活跃当前用户/租户/角色；当前套餐 INFERENCE；独立 `inference:service:create` → 当前 API route grant；对应 GPU PlanQuota；实际 Acc delegation 与已装配 Inference adapter |
| INF-02 | `POST /api/v1/inference/services/{resource_id}:delete` | 顶层 `idempotency_key`；resource由路径绑定，正文若有须一致。调用现有 AcceptGpuDelete：未发可本地原子取消，否则持久 DELETE 关联并可靠投递；ACK不是释放完成 | 同一可信租户与独立 `inference:service:delete` 动作；权限先于原创建读取/幂等重放，不能用 Accelerator 读权或历史原记录绕过 |

ledger adapter action固定为权威 Inference RPC full method `CreateInferenceService` / `DeleteInferenceService`；目的地址和服务端精确 DNS SAN 只来自受管配置。真实 Inference RPC保留原业务tags，加入公共Acc附件与原全量charges；唯一业务摘要来自 Inference API公开helper，adapter核对durable ACK并映射既有三个snake_case字段。完整字段/单位/身份/错误/恢复见 [GPU owner接入指南](contracts/gpu-owner-integration-guide.md)。

公共创建报文的 `command` 是调用方提供的完整单字符串；公开合同没有 `command[]` 或 `args[]` 数组，Governance、Model 和 Inference 不解析、拼接或追加启动参数。

当前受管GPU的LWS输入在共享业务校验中、Occupy之前明确拒绝；锁定KServe的LWS模板无法可靠投影GPU annotations与Queue。本批不把typed渲染接口或接受后补丁当C02可运行路径，实际LWS投影仍为not_verified。

模块资格 `PlanModule`、上限 `PlanQuota` 与当前用户动作授权各自独立。API先dry-run/显式sync后执行 旧权限目录登记脚本（已退役），再仅为显式选定tenant:manager角色和选定套餐执行 旧启用脚本（已退役），传tenant_id/role_id/plan_id；不修改基础FREE、模板、所有角色或用户配额。Gov内部退款listener仅编译注册固定ani-inference，生产精确客户端DNS SAN必须明确配置，不猜测。

软件测试使用实际Gov受理/账本/当前权限、真实Acc解析和真实Inference RPC/PG；只有外部provider证据可用明确替身，外部模型/Kubernetes执行暂停。完成通知在持久DELETE后显式注入，通过真实Inference退款客户端调用真实Gov mTLS/PG。源码实现、发布依赖、运行结果、CI及生产/真实GPU验收由本批结果索引分别记录；本文不回填未执行PASS，也不将本批之外的接口风格整体登记结项。


## 2026-10-12：子网详情改为 IP 地址范围

用户纠正所需信息为范围。沿用 Get/ListSubnet 与原鉴权/套餐/租户闸门，将公共字段改为有 presence 的 `available_ip_range`/`used_ip_range`；旧 count 字段名称和编号保留为 Proto reserved，不把旧 int64 编号直接改成 string。JSON 返回 KCN `status.v4availableIPrange`/`v4usingIPrange` 的原字符串，例如 `10.42.1.2,10.42.1.4-10.42.1.9`。明确 `""` 为确定空范围；未观测、字段缺失或非字符串为未知。有效新观测清除旧范围，读失败保留历史值但不续鲜，沿用 observation_stale。GetSubnet 仍经 Resource 读取持久化结果；不根据计数或 CIDR 推算，也不同步访问 KCN。

Resource 通过新迁移 0013 增加可空 text 列，保留历史迁移与原 count 列，后台链更新范围。Fedora 正式 `make api` 通过；`go test -count=1 -run '^TestNetworkNewFieldsRetainIntentAndObservations$' ./app/admin/service/internal/service` 通过（0.048s）；`scripts/network-joint-integration -run '^TestNetworkJointHTTP/subnet_ip_ranges$'` 通过（整体 4.867s，范围子场景 0.31s），同一 HTTP 用例修改前失败（缺失 range），修改后通过。两仓生产入口编译通过，Resource 范围详情与旧行迁移定向用例通过。外部替身仅在 Kubernetes/KCN 边界，真实 JWT/AK、mTLS、业务与独占 PostgreSQL 参与验证，产品及 PG/Redis 清理通过；未运行全量测试或重跑监听器矩阵。源码与共享部署分别交付，真实集群验收仍为 not_verified。

本次 range 合同的 Resource 依赖为 `v0.0.0-20261012021348-7c072b343e6a`，对应已推送的真实提交 `7c072b343e6aedb665ae10d981138b11af476eaa`。此前 b258907 的未发布限制也已在 2026-10-11 推送时解除；本次使用正常公开 Go 模块下载和校验，不保留个人路径 replace。
关闭 `go.work` 后，`go build -mod=readonly -trimpath ./app/admin/service/cmd/server` 在 Fedora 通过，二进制模块信息确认上述 Resource 版本与公开校验和。

<a id="gov-platform-network-01"></a>
## GOV-PLATFORM-NETWORK-01：平台网络 BFF

2026-10-27 追加平台默认 VPC 当前出口查询，复用 `network:platform:default-egress:get`；平台 EIP 清单纳入无租户的系统出口地址并保留所属生命周期释放约束。契约见[当前平台默认出口](contracts/platform-network.md#当前平台默认出口)。

2026-10-27 在本编号追加平台 EIP 全租户列表、详情和受保护释放三个接口；使用 `page/pageSize`、组合筛选、真实租户映射和独立动作权限，合同见 [平台 EIP 管理](contracts/platform-network.md#平台-eip-管理)。生命周期与活动绑定保护仍由 Resource 拥有。

2026-10-21 KCN 网络平台化在该编号下追加虚拟交换机四动作、独立 unbound DTO 和 `page/pageSize`，二层网络 v2 一次提交集群/交换机/网卡，并加入旧设备登记受限退役的两个独立平台动作、默认 VPC 出口的三个独立平台动作；旧 `device_id` 第 3 字段保留并与 v2 互斥。请求与授权的唯一合同仍见[平台网络接入](contracts/platform-network.md)，新增权限只通过[交换机权限增量](../sql/data/20261021_virtual_switch_permissions.sql)及[退役权限增量](../sql/data/20261021_device_retirement_permissions.sql)及[默认出口权限增量](../sql/data/20261021_default_egress_permissions.sql)建立关联，不自动授予角色。Provider 已正式发布为 `267c2d29c6119f03a4ea9c90303a2a9ac7af9a0a`，Gov 固定模块 `v0.0.0-20261021072547-267c2d29c611`；标准 Go 与 sum.golang.org 已验证 Origin.Hash、module sum 及 go.mod sum，见[正式 API 发布回执](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/5c458c2102e4093ba23be24db001171f0d1b13ac/docs/execution/records/KCN-NETWORK-PLATFORM-20261021/formal-api-publication.json)。本次执行与验收证据统一由 Resource 的[执行状态](https://github.com/zhangzhe-ctrl/ani-resource-service/blob/codex/kcn-network-platform-20261021/docs/execution/status.md)索引；源码与受控测试不表示目标部署、真实接管或公网验收。

2026-10-12 用户要求接入 Public/Intranet 地址池、VLAN 网络与配套接口，并参考 VPC 核对分页、响应。本批新增 27 个平台用户 JWT 接口；同轮按用户要求将 HTTP 前缀统一为 `/api/v1/admin/networks`。入口、权限、请求、内部委托及分页的唯一合同见 [平台网络接入](contracts/platform-network.md)；完整路由清单引用平台策略和 Proto，不在登记复制。角色与生产目录授权不自动改变，部署/真实数据面验收独立记录。

实现包含 Gov HTTP/BFF/客户端/权限目录步骤，以及 Resource 平台范围校验和注册。Fedora Go 1.26.9 的完整 make api、受影响包定向测试/vet、生产入口构建及 platform_network 真实 HTTP/JWT/Casbin/mTLS/PostgreSQL/worker 场景通过，覆盖 27 平台方法。KC/Envoy/NodeFacts/owner 仅在外部边界受控；租户 JWT、AK 与 VPC 等定向流程也通过。共享夹具整批 Network 回归仍失败：LB 更新停留 PROVIDER_NOT_READY，后续占用/清理失败保留；原始两仓基线同序列也有子网起始事实和后段 AK DEPENDENCY_UNAVAILABLE 失败，但没有复现相同 LB 更新失败，不能归并成同一原因。子网用例通过外部 status API 建立未知起点，原断言保留。未执行真实物理网络/公网转发、业务库目录授权或部署；受控验收快照未提交或推送，用户随后授权发布两仓 main，最终版本以 Git 历史和远端引用为准。实际命令/日志/快照保留在仓库外任务工件目录 `/home/chabking/workspace/platform-network-gov-artifacts-20261012-01a124d1`，不由本登记称整仓已绿。

同轮前缀调整复验 **pass**：Fedora 完整 `make api`，仅生成平台 HTTP/Proto 路由描述及 OpenAPI 路径变更；27 路由的 Proto、生成 HTTP、鉴权策略与权限 SQL 一致，OpenAPI 语义差异只有前缀。`go test -mod=readonly -count=1 -run "TestPlatformNetwork|TestNetwork" ./pkg/middleware/auth` 与 `scripts/network-joint-integration -run "^TestNetworkJointHTTP$/^platform_network$"` 均退出 0，新前缀的 27 平台方法及现有负向、分页和响应检查通过，任务自有 PG/Redis 已清理。沿用同一 Resource 夹具二进制与外部边界替身，不扩大为共享整批 Network 回归或真实数据面通过；前述 LB 未解失败保留。本轮命令、源码哈希、生成和复验日志在既有工件目录的 `gov/admin-prefix/`；该复验快照未提交、推送或部署，后续 main 发布不代表部署。

main 发布使用 Resource `e7b5a8a3435996181bff62e025a21224c97d2464`，Gov 依赖固定为 `v0.0.0-20261012083359-e7b5a8a34359`。Fedora 新模块缓存已验证下载内容、模块/go.mod 校验和及 `Origin.Hash` 与发布提交一致，完整 `make api` 复验无生成差异。发布时保留最新 Governance main 的 ModelDev 和鉴权计划更新；源码发布不改变共享部署或此前 LB 回归未解结论。提交前 Resource 完整 `make verify` 已通过，前缀和平台方法的证据沿用上述入口；发布补充日志保留在同一工件目录的 `publication/`。
## INSTANCE-MVP-V15（2026-10-13，后端接线实施中）

2026-10-16 联合批次的当前合同见 [容器实例 HTTP 合同](contracts/container-instances.md)。该批次将实例公开/内部类型统一为 `instance_kind=container` 与可信 `compute_type`，删除外层规格版本和旧实例分页；新增 Res 数据库规格的 Gov 平台 CRUD、可选 scheduling、平台调度配置、只读 runtime/events 和受保护 SSE 日志。新增/改造列表统一 page/pageSize 与 items/total，授权/筛选先于计数和分页。以下 10-13～10-16 发布记录说明此前交付，不能作为本批验收证据。

本批身份条件：平台规格及调度管理要求 tenant=0 的平台用户和独立权限；租户创建/读取沿当前 INSTANCE 角色/套餐，GPU 候选另取当前 ACC 目录权限交集。ConfigMap 使用编辑时 resourceVersion 条件写入并保持其他键及 metadata；现场基线未对齐时写闸门关闭。日志通过专用 SSE 等 READY 后发送头，使用原请求取消，不套普通 REST/unary 总期限；fetch 示例及运行安全字段见上述合同。

本批实际验证由任务独占 evidence/EXECUTION-STATUS.md 关联具名原始结果：CPU 公开受理至真实 Pod Running/Ready、规格/defaults 冻结重试、正常启停/删除；Whole1/Shared1/Whole2 的实际控制器 Pod 字段（Pending，仅字段）；ConfigMap 条件更新/冲突恢复、平台互斥权限，以及 runtime/events 和真实 SSE 长连接、期限及故障回收。按 Profile 上限通用化的数量增量使用真实 builder、软件与 PG 边界定向验证，未扩 3/4 卡现场矩阵。正式依赖版本由本仓 go.mod/go.sum 固定，使用正常 Go 模块下载核对 Origin.Hash 与校验和；源码发布不表示合并或共享部署，不授权旧实验数据清理，硬件与浏览器边界保持未验证。

新增固定 `INSTANCE=16`，独立于 INFERENCE/ACCELERATOR 的当前套餐和动作授权。输入、路由及安全查询视图以 `api/protos/instance/service/v1/instance.proto`、`api/protos/admin/service/v1/i_instance.proto` 为准；创建/变更保留 `{data:{...}}`，不允许公共 tenant、namespace、owner、GPU plan 或 charges。`instance_http.go` 严格解码写入，并校验路径和正文 ID 相同；终端响应 `no-store`。

创建先执行当前角色/套餐授权及原公开请求幂等查找，新请求再解析 Image/preset/Namespace。占额、持久投递及全量附件沿用 Quota；启停调用原实例版本，不动原账。删除版本和幂等内容进入持久 DELETE，已接受删除阻止后续新启停/终端。Resource owner 固定 `ani-resource-service`，退额证书映射为 `ANI_QUOTA_RESOURCE_DNS_SAN`；合法 owner 原 GPU 全量累计声明不要求 Gov 持久 DELETE，更不查询 Notebook/Pod。Acc ENDED 保留实际 binding。

2026-10-15 CPU 扩展：创建仍只接收 image/preset 公开字段，可信 preset 决定 `cpu`/`gpu`；列表和详情新增只读 `instance_type`，preset 同时返回类型。创建后类型、CPU/内存和 GPU 规格固定，不提供变配/互转接口。CPU 复用当前 INSTANCE 角色/套餐授权、原公开请求幂等、QuotaOperation 持久投递、Notebook/PVC 及 SG 终端；CPU 不调用 Acc、不创建 GPU plan/charge，不新增 CPU 计费额度。零 charge 仅接受固定 Resource owner/action 与 `instance-cpu-v1` 冻结合同，GPU 原全量占退额不变。CPU DELETE 保存原 CREATE 关联及 expected_version，接受后阻止新启停/终端；SG 依据 Resource 可信实例类型使用 CONTAINER，并保留 InstanceBinding 的 namespace/UID/version 校验。CPU 创建/删除已由 Gov 持久受理但尚未得到 Resource ACK 时，operation 查询返回 Gov 原持久 `QUEUED`/`DISPATCHING`/`UNKNOWN`；本地取消返回终态 `CANCELED_UNSENT` 和原因 `CANCELED_BEFORE_DISPATCH`。这些操作尚无 Resource intent_version，返回 0；ACKED 后读取 Resource 的真实操作状态。本项生成/目标构建、真实 PG 的 GPU/CPU 当前授权与幂等接单回归，以及隔离真实 CPU Notebook→SG→两轮启停保盘→删盘均已完成；具体运行配置、精确源码/结果和前端交接统一见 Resource 当前状态，不据此声明 GPU 实机或浏览器页面验收通过。

2026-10-16 按用户授权发布本次全部实例后端代码到 main。Resource provider 为 `921a22a85758d42986d271406cafdd74ca421d1d`，Gov 固定正式模块 `v0.0.0-20261016021904-921a22a85758`；Fedora `GOWORK=off` 下载已核对 `Origin.Hash`、Sum `h1:SCUfArA8dAHTZXs1N3ZpLjd5Y3n77OWQtYDQ12zha3I=` 和 GoModSum `h1:DXqtxWJ+IEu9gZIgGVHTbFONYQc9hcuaeXOUzmY5Yjw=`。两仓目标独立构建通过，未提交 workspace 或 sibling replace。实现/生成物与已通过定向及 CPU 真实业务验收的代码清单一致，复用原证据；本次发布不重复业务场景、不等待 CI、不更新共享部署，GPU 实机与浏览器验收边界保持。准确 Gov 发布提交以远端 main 为准，命令及模块回执在原 Fedora 任务根 `publication-20261016/`。

此前 `page_size/after_id` 列表协议已由本批替换；当前接口不接旧分页参数，不提供双轨兼容。前端按上方当前合同接入。

早期定向联调已使用真实授权、PG/Redis/mTLS 和生产业务代码，GPU/Kubernetes/exec 边界受控；真实 GPU 产品验收仍未完成。2026-10-13 用户授权先推送 main，剩余门禁由后续任务处理：已固定正式 Resource/Acc/SG API 版本并核对下载 SHA/sums，但新版 main 合入后的最终独立构建、定向组合用例及 CI 尚未完成，不声明通过。权威当前状态统一见 Resource `docs/execution/status.md#gpu-container-instance-mvp-v15`。`scripts/ops/sql/register-instance-permissions.sql` 只准备已同步的 API/permission 目录，未执行共享环境授权；本轮没有部署。

2026-10-28 本编号增加 Restart、AttachPVC、DetachPVC 和实例范围候选，当前输入、权限、重放先行、查询与 CPU/GPU DELETE 明确拒绝合同见[容器实例 HTTP 合同](contracts/container-instances.md#重启与已有-pvc-挂卸)。本批仅 Resource/Gov 后端，无 Console/BROWSER 门禁；实现、正式模块、测试部署及验收进度统一在 Resource `docs/execution/status.md#container-restart-pvc-20261028`，不以本登记表示现场通过。

## MODELDEV-09：产物列表分页参数校验对齐（2026-10-13）

前端联调时提出 `next_page_token` 是否为分页参数、能否与其他 ModelDev 列表对齐。当时四个列表统一使用 `page_size` + `page_token` / `next_page_token`，`presets`、`input-versions`（含 `state` 过滤，`state` 非分页参数）本就一致。

发现并修复一处源码实现偏离：`GET /api/v1/modeldev/executions/{execution_id}/artifacts` 的 HTTP 参数解析原为手写 `url.ParseQuery`，只判 `page_size > 100`，会接受 `page_size=0` 和带请求体的 GET，与前端文档 §2 和 MODELDEV-03 声明的「显式传 `0` 返回 400」矛盾。现改为复用与 `presets`/`input-versions`/`executions` 相同的 `modelDevBoundedQuery` + `modelDevQueryPositiveBound`，对 `page_size=0`、前导零、越界、重复参数、未知参数、超长 token、GET 请求体/`ForceQuery` 一律 400。未改字段名、未改路由、未动 Proto/OpenAPI 与鉴权。

本机（Windows）已执行：`go build ./app/admin/service/internal/server/...`、`go vet ./app/admin/service/internal/server/`、`go test -count=1 ./app/admin/service/internal/server/`，均通过。定向用例 `TestModelDevListAndLogRejectUnboundedSelectors` 增列 5 条 artifacts 负向断言（`page_size=0`、`page_size=101`、超长 `page_token`、未知参数、executions 带请求体），全部 pass。真实环境/部署验收尚未执行，保留 `not_verified`。

> 后续已由 MODELDEV-10 将四个列表由 `page_size/page_token` 迁移为 `limit/cursor`；MODELDEV-09 的边界校验结论（`limit=0`、前导零、越界、重复/未知参数、超长 cursor、GET 体一律 400）随之保留，只是参数名改为 `limit`/`cursor`。

## MODELDEV-10：ModelDev 四个列表迁移为 limit/cursor（2026-10-14）

按用户明确授权，把 `GET /api/v1/modeldev/executions`、`/presets`、`/input-versions`、`/executions/{execution_id}/artifacts` 四个列表由 `page_size`/`page_token`/`next_page_token` 迁移为与 users/VPC 同一对外形状：请求 `limit`（默认 20、1～100）、`cursor`（不透明、上限 2048 字节），响应 `{items, next_cursor, total}`（`total` 为 uint64，HTTP JSON 为十进制字符串）。

对外字段统一为 `items`/`next_cursor`/`total` 与 users 一致。`cursor` 沿用下游 ModelDev 的不透明 ID（同 VPC 的“下游游标”，**不做** Governance c1 HMAC 签名）：换筛选/排序复用旧游标不会 400，客户端应从首页重新取数；这是本次明确接受的行为边界，与 users 的签名绑定语义不同。

`total` 与筛选一致，由下游在可信租户内计算：`input-versions` 走单表 `SELECT count(*)`（含 state 过滤）；`executions` 因 `compute/delivery/close` 状态是多表应用层投影、无法用普通 SQL count 精确表达，改为复用与取页相同的批量扫描在应用层全量遍历计数；`presets` 取去重后 preset 数；`artifacts` 取该执行的已发布产物数。

代码面：下游 `ani-modeldev-service` 改 `PageRequest`（`limit/cursor`）与四个 Response（`next_cursor/total`），新增 `CountQueryRecords`/`CountInputVersions`/`InputsCount`，重跑 `buf generate` 与 `sqlc generate`；Governance 对外 `modeldev/service/v1/execution.proto` 同步改名并加 `total`，改 HTTP 参数白名单与 service/client 映射，重生成 `execution.pb.go`。因下游新字段尚未发布为新模块版本，Governance `go.mod` 临时加入 `replace github.com/zhangzhe-ctrl/ani-modeldev-service => ../ani-modeldev-service`，供本机与 CI 消费同工作树下游源码；下游发布后应改为正式伪版本并移除该 replace。

本机（Windows 1.25.4 主机 + 自动拉取 Go 1.26.9 工具链）已执行：下游 `buf generate`/`sqlc generate` 通过，`GOOS=linux go build ./...` 通过，`go vet ./internal/...` 与 `go vet -tags cpu_mainflow ./internal/data/submittest/...` 通过（下游含 Linux-only 代码，Windows 原生编译不适用）；Governance `go build ./...`、`go test -count=1 ./app/admin/service/internal/server/... ./app/admin/service/internal/data/...` 通过。真实环境/部署验收（真实 PG、mTLS 链路、OpenAPI 目录同步）尚未执行，保留 `not_verified`；Governance 完整 `make api`（含 OpenAPI 后处理，Linux/bash 专用链）未在本机重跑，仅对受影响的单个 proto 做了定向 `buf generate`。

## GOV-MODEL-INF-20261012：Model / Inference BFF 接入

本登记同时覆盖 Model 模型目录、版本、导入 BFF 和 Inference 推理服务管理 BFF。
Inference 的普通及 GPU 推理创建、查询、操作查询、更新、启停/重启和删除均由
Governance HTTP 入口提供；Governance 根据已认证的租户用户建立下游 Model/Inference
mTLS gRPC 请求，不信任客户端提交的租户或 actor。Higress 只承载 OpenAI 兼容推理
数据面（例如 `/v1/completions`），不得承载下列 `/api/v1/...` 管理路由。ModelScope
导入仍由 Model Service 创建一次性 Kubernetes Job 完成，文件写入当前 Model Service
配置的 S3-compatible 存储。

### 2026-10-26 默认镜像目录与拒绝收尾

模型版本创建、整包导入 version 和查询投影增加成对可选的
`recommended_image_id/recommended_image_scope`。选择目录时，Gov 使用当前可信
租户及 actor 调用现有 Image `GetImage`，核验 scope、租户、未注销状态并填写
目录固定 `resolved_reference`；矛盾的 `engine_image` 被拒绝。旧调用仍可不传目录
选择。`IMG-KNOWN-P2-001` 保持 **DEFERRED**：Model 已保存但响应丢失后，当前
目录不可用可能阻断原请求到达 Model 重放；本期不新增 replay preflight。

推理创建和镜像更新增加 `image_id/image_scope`，成对提交且与 raw `engine_image`
互斥；默认值仅用于客户端预填，最终选择显式提交。旧 public intent 的 canonical
字段集及 `gov-inference-intent-v2` hash 保留；目录选择使用 v3 intent，并向受信
Inference 发送 `contract_version=2` 的命令。已有 preset admission、原 charge 和
引擎完整启动命令保持。查询返回选择及实际固定 `engine_image`。

P1 只有受信 Inference CREATE 返回 `FailedPrecondition` 与唯一 ErrorInfo
`domain=ani-inference, reason=IMAGE_ADMISSION_REJECTED`，且 metadata 的
`stage=pre_accept`、tenant/resource/operation/request_digest 与原冻结命令完全一致
时触发。Gov 在同一数据库事务保存 CREATE 的 REJECTED、原业务 reason、稳定错误
消息和 `close_operation_id`，并持久建立关联原 CREATE/charge 的独立 DELETE。
worker 重启继续派发 DELETE；重复拒绝及用户随后关闭复用关联操作。DELETE ACK
只代表接受，GPU 仍须既有 owner 完整释放事实后按原账本结算。超时、断连、普通
NotFound 或没有身份/阶段的错误保持未知，不触发该收尾或直接退款。管理查询继续
显示原创建错误及关闭操作，不被删除成功覆盖。

### HTTP 入口

| 业务 | 方法与路径 | 权限 |
|---|---|---|
| 模型列表/详情 | `GET /api/v1/models`、`GET /api/v1/models/{model_id}` | `model:service:read` |
| 外部模型搜索 | `GET /api/v1/models:search` | `model:service:read` |
| 模型版本 | `GET /api/v1/models/{model_id}/versions`、`GET /api/v1/model-versions/{model_version_id}` | `model:service:read` |
| 导入任务查询 | `GET /api/v1/models/import-tasks/{task_id}` | `model:service:read` |
| 模型下载地址 | `GET /api/v1/model-versions/{model_version_id}/download-url` | `model:service:read`（由 Governance 通过受信任的 Model gRPC 调用） |
| 创建模型/版本、上传地址、发起导入、重试 | `POST /api/v1/models`、`POST /api/v1/model-versions`、`POST /api/v1/models/{model_id}/upload-url`、`POST /api/v1/models:import`、`POST /api/v1/models/import-tasks/{task_id}:retry` | `model:service:manage` |
| 聚合创建模型、版本并发起导入 | `POST /api/v1/models:import-bundle`；请求体使用顶层 `model`、`version`、`source` 和 `idempotency_key`，一次幂等委托 Model Service | `model:service:manage` |
| 删除模型/版本 | `DELETE /api/v1/models/{model_id}`、`DELETE /api/v1/model-versions/{model_version_id}` | `model:service:manage` |

| 推理服务列表/详情 | `GET /api/v1/inference/services`、`GET /api/v1/inference/services/{resource_id}` | `inference:service:read` |
| 推理服务创建/删除 | `POST /api/v1/inference/services`、`POST /api/v1/inference/services/{resource_id}:delete` | `inference:service:create` / `inference:service:delete` |
| 推理操作查询 | `GET /api/v1/inference/operations/{operation_id}`、`GET /api/v1/inference/services/{resource_id}/operations` | `inference:service:read` |
| 推理更新/启停/重启 | `POST /api/v1/inference/services/{resource_id}:update`、`:start`、`:stop`、`:restart` | `inference:service:manage` |

Inference 列表请求使用 `page`/`pageSize`（默认 20，最大 100），筛选条件在分页前
应用；响应为 `{items, total}`，其中 `total` 按 HTTP JSON 合同编码为十进制字符串。
旧的 `page_token`/`next_page_token`/`services` 不是 Inference BFF 合同。推理 DTO
中的 `served_model_name`、模型版本、状态、运行时和能力字段由下游 Inference
返回，Governance 只做租户、权限和分页适配。
### 新环境启用步骤

1. 先对本版本 OpenAPI 执行 `admin sync-apis --dry-run`，人工确认后执行
   `admin sync-apis`。
2. 执行 [Model 权限目录登记](../scripts/ops/sql/register-model-permissions.sql)。
   该文件只登记权限与 API 关联，不自动给任何角色授权。
3. 对明确的租户、当前 `tenant:manager` 角色和套餐执行
   [Model 启用脚本](../scripts/ops/sql/enable-model-access.sql)。
   它只增加所选租户角色的权限和所选套餐的 `MODEL` 模块，不修改其他租户、
   模板角色或配额。
4. 配置 Governance → Model 的证书、精确服务 DNS 和地址；调用者证书只含
   DNS SAN `ani-governance`，不混入其他 DNS、URI、IP 或 email。重启 Governance
   使 Casbin 策略重新加载；配置缺失时 BFF 应返回下游不可用，不退化为直连或明文。
5. 执行 [Inference 权限目录登记](../scripts/ops/sql/register-inference-permissions.sql)，
   再按目标租户执行 [Inference 启用脚本](../scripts/ops/sql/enable-inference-access.sql)。
   两个脚本只登记或启用指定租户的 API、角色和 `INFERENCE` 模块，不修改其他租户、
   套餐或配额。配置 Governance → Inference 的 TLS 1.3 双向证书和精确 DNS SAN
   `ani-inference`，缺少证书或 SAN 校验失败时 BFF 返回下游不可用。

### 业务链路验收

`登录 → POST /api/v1/models:import-bundle
→ 轮询导入任务 → 读取已就绪版本`。聚合导入请求必须同时提供模型能力、版本格式、引擎类型、引擎镜像、完整启动命令以及 provider/repo_id/revision；Governance 原样转发这些运行时字段，不拼接启动命令。兼容的分步链路仍为
`登录 → POST /api/v1/models → POST /api/v1/model-versions → POST /api/v1/models:import
→ 轮询导入任务 → 读取已就绪版本`。导入任务状态可以在版本就绪前为
`importing`；`GET /api/v1/model-versions/{id}` 只返回已就绪版本，不能把导入中的
404 当作导入失败。推理管理链路在模型版本就绪后为
`POST /api/v1/inference/services → GET /api/v1/inference/services/{id} →
GET /api/v1/inference/operations/{operation_id}`；该链路需要单独验证 Inference
owner 的 mTLS 和运行时状态，不能据此宣称推理数据面（Higress `/v1/completions`）已验收。


### GOV-ACC-INF-V12-WIRING-01：2026-10-18 显存值合同替换

本轮移除 GPU 旧大附件、百分比与证明准入，使用单执行快照和 `QuotaReleaseService/ReportResourceReleased` 四字段完整释放。内部 mTLS owner → 原 CREATE → resource → 原账事务全退；非 GPU 的独立累计释放接口保持原范围。当前正式合同见 [GPU owner 指南](contracts/gpu-owner-integration-guide.md)。

管理 Profile 创建仍为 `POST /api/v1/admin/accelerator/profiles`，正式 RPC 改为 `CreateProfile`；新增 `GET .../profiles:preview`、`POST .../profiles/{profile_id}:set-state` 和 `POST .../profiles/{profile_id}:delete`。写请求保持 `{data:{...}}`，平台 JWT、当前权限与 ACC 对应 action/cluster grant 必须齐全。API 目录同步只登记接口，不自动授予权限。

用户 Preset 返回每卡显存值、selectable 与 unavailable_reason；CPU 查询不依赖 ACC，GPU 仍须通过租户目录授权过滤。Inference 普通及 GPU 管理接口现由 Governance BFF 提供，API 依赖和出站 mTLS 配置按当前接线维护。编码、软件回归和真实 GPU 验收分别记证，不能沿用旧结果宣称新合同已验收。

### 模型与推理写请求去除 data 包装

模型创建、版本创建、导入、组合导入，以及推理创建、删除、更新、启动、停止、重启共 10 个接口，业务字段移至请求 JSON 顶层；响应保持原合同。租户与操作人仍仅来自可信认证上下文，推理保留严格字段校验、幂等键、预期版本与部分更新掩码。真实 URL 不变，推理路由模板变量从 `{data.resource_id}` 改为 `{resource_id}`；已有环境部署前应显式运行 [`migrate-inference-flat-request-routes.sql`](../scripts/ops/sql/migrate-inference-flat-request-routes.sql)，保留 API ID、启停状态、权限与套餐关联，再按部署文档刷新策略，禁止通过重新建目录丢失既有授权。前后端需协同发布，本记录不代表已经部署。

### 2026-10-21 Model 版本筛选与操作记录补齐

在已有 Model BFF 接入范围内新增 `GET /api/v1/models/{model_id}/operations`，
沿用 `model:service:read`、`MODEL` 模块以及原 `enable-model-access.sql` 的显式
租户角色授权。目录通过生成的 HTTP 路由发现；部署时先 `admin sync-apis --dry-run`
审查，再同步并执行 `register-model-permissions.sql`，未登记时维持拒绝。
版本列表增加 `status`，透传给 Model 在分页前筛选，不能以当前页本地筛选替代。
报文、页码边界和事件结果语义见 [HTTP 合同](contracts/frontend-http-api.md)。
新增 RPC 与服务端 Model migration 000009、API 版本发布和消费者版本更新需要
同批交付；本地临时模块校验不代表已发布或集群已具备新接口。

### GOV-MODEL-INF-20261012：服务访问策略（2026-10-22）

已实现按服务查询和完整替换访问策略，通过已有 mTLS Inference API 调用，租户、
actor 和 request ID 从可信上下文重建。HTTP 入口执行正常认证、权限和 INFERENCE
模块闸门，并拒绝未知/重复字段、query、公网身份和路径冲突。
合同和启用顺序见 [Inference 管理接口状态](contracts/inference-flat-api.md#服务访问策略)。

运行部署以当前现场基线 `3806ae5` 的独立策略分支构建，保留该基线既有创建合同；
本工作区的算力规格迁移没有随此次策略部署发布。源码与运行版本分别验证。

### GOV-MODEL-INF-20261012：详情与查询修正（2026-10-22）

模型详情 HTTP 直接返回 Model，内部 RPC 包装不变；版本与操作查询页码不再限制
100，每页仍为 1..100，SQL offset 超过 int32 存储边界时拒绝。公开请求顶层字段
不再使用 data 包装。真实环境复验使用模型 UUID：status=error 返回 total=0，
status=ready 返回 total=1/ready，operations 返回 total=1。含斜杠的公开模型名不能
替代 HTTP path 的资源 UUID。源码修改、部署与前端验收分别记录。

<a id="storage-20261023"></a>

### STORAGE-01～STORAGE-10：存储接入（2026-10-23）

范围：独立盘（PVC）创建、列表、详情、扩容、删除；对象存储只读浏览与短期下载。
前端合同与样例、实施过程、测试报告和偏离说明另行交付，不在本仓登记。

| 编号 | 方法与路径 | 权限码 |
| --- | --- | --- |
| STORAGE-01 | `GET /api/v1/storage/classes` | `storage:volume:create` |
| STORAGE-02 | `POST /api/v1/storage/volumes` | `storage:volume:create` |
| STORAGE-03 | `GET /api/v1/storage/volumes` | `storage:volume:list` |
| STORAGE-04 | `GET /api/v1/storage/volumes/{pvc_name}` | `storage:volume:get` |
| STORAGE-05 | `POST /api/v1/storage/volumes/{data.pvc_name}:update` | `storage:volume:update` |
| STORAGE-06 | `POST /api/v1/storage/volumes/{data.pvc_name}:delete` | `storage:volume:delete` |
| STORAGE-07 | `GET /api/v1/storage/buckets` | `storage:bucket:list` |
| STORAGE-08 | `GET /api/v1/storage/buckets/{bucket_id}/objects` | `storage:object:list` |
| STORAGE-09 | `POST /api/v1/storage/buckets/{data.bucket_id}/objects:head` | `storage:object:get` |
| STORAGE-10 | `POST /api/v1/storage/buckets/{data.bucket_id}/objects:download-url` | `storage:object:download` |

约定：创建/变更使用 `data` 包装；单资源查询直接返回对象；除对象列表外使用
`page/pageSize` 与 `items/total`（int64 为十进制字符串）。**对象列表是分页合同的
唯一显式例外**：只接受 `limit`（1–100，缺省 100）与绑定 bucket+prefix 的不透明
`cursor`；出现 `page/pageSize/offset/token` 一律 400；响应不含 `total`；游标跨桶/
跨 prefix 复用 400，不降级首页。该例外只适用于 STORAGE-08。

查询参数严格校验（STORAGE-01/03/07/08 四个列表 GET）：参数白名单按路由声明，
每个参数只允许单值；空值、前导零、重复参数、未声明参数一律 400
`STORAGE_INVALID_REQUEST`，不静默取默认值。其余 storage 路由（含写接口）不接受
任何查询串。该过滤器位于生成代码解码之前，与 `docs/contracts/frontend-http-api.md`
的分页合同对齐。

列表筛选（分页前生效，多条件 AND，`total` 为筛选后总数，排序保持既有唯一键）：

- STORAGE-03：`name` 按 `display_name` 不区分大小写包含；`id` 精确匹配 `pvc_name`
  或 `uid`；`state` 精确匹配 `phase`，仅接受 `Pending`/`Bound`/`Lost`。
- STORAGE-07：`name` 按展示名不区分大小写包含；`id` 精确匹配逻辑桶 ID。

认证：存储路由只接受 Bearer 用户身份；`allowsAPIKey` 仅覆盖 Network/Image，
带 AK/SK 签名头调用返回 403 `USER_REQUIRED`，混用返回 400 `MIXED_AUTHENTICATION`。

幂等与身份：`create_token` 是业务重试键，一次创建意图一个；删除后重放旧 Create
可能创建同名新 UID，前端不得在删除后自动重试旧请求。扩容与删除必须携带用户选中
对象的 `expected_uid`，UID 变化返回 `VOLUME_IDENTITY_CHANGED`。扩容只允许增加，
与 `spec` 当前申请值比较，不允许缩容。

模块与套餐：STORAGE 为新增业务模块；`sys_plan_modules` 是白名单，迁移默认不为
存量套餐添加。上线时由运营显式开启，未开启返回 403。权限目录脚本
`sql/data/20261023_storage_permissions.sql` 只建权限点与 API 关联，不授权、不启模块。

本只读批次未提供上传/删除对象和建桶，后续写入合同见 STORAGE-11～13；删桶、用户 S3 AK/SK、桶公开访问、容量配额、重命名仍未提供。
独立盘不计入 `storage.bytes` 配额。

存量库补授权（2026-10-28）：新环境实测存储全部路由对租户管理员返回 403
`FORBIDDEN`（Casbin `subject=tenant:manager ... denied`）；权限点、API 关联与
STORAGE 套餐模块均已在位，缺的是角色授予——只有历史租户角色持码，模板
`template:tenant:manager` 为空。显式执行
[`grant-storage-tenant-permissions.sql`](../scripts/ops/sql/grant-storage-tenant-permissions.sql)
给模板与既有 `tenant:manager` 角色按各自租户补授 9 个 `storage:*` 码，随后重启
Gov 重载策略。该脚本只补角色授予，不改权限、关联、套餐或平台角色。

### STORAGE-11～STORAGE-13：对象存储写入（2026-10-29）

| 编号 | 方法与路径 | 权限码 |
| --- | --- | --- |
| STORAGE-11 | `POST /api/v1/storage/buckets` | `storage:bucket:create` |
| STORAGE-12 | `POST /api/v1/storage/buckets/{data.bucket_id}/objects:upload-url` | `storage:object:upload` |
| STORAGE-13 | `POST /api/v1/storage/buckets/{data.bucket_id}/objects:delete` | `storage:object:delete` |

沿用 Bearer、STORAGE 模块、租户上下文、data 包装及严格查询参数校验。
建桶 data.name 为 1–32 字符小写 ASCII/数字/连字符，首尾字母数字、禁止连续连字符；同名重放，返回桶对象本身。
物理桶按服务端 cluster/tenant 指纹推导；预置桶归属优先，不接管其他归属桶；无用户删桶接口。
上传 data 含 bucket_id/key/content_type/size/overwrite（默认 false），返回 url/headers/expires_at/key，no-store；浏览器按返回 headers 直传原始文件。
size 为声明值（默认上限 2 GiB），不是实际字节硬配额；预签名 PUT 有效期默认 15 分钟且可重用。
非覆盖票据绑定 If-None-Match:*，上传时对象已存在由 S3 拒绝；浏览器错误为 S3 状态/XML，不是 Gov JSON。
删除 data 含 bucket_id/key/可选 expected_etag；校验桶存在，单 key 删除返回 DELETED/GONE；不递归，非永久删除版本历史。
expected_etag 既做 HEAD 比较又下传 If-Match，竞态不匹配返回 OBJECT_IDENTITY_CHANGED。
依赖后端条件写支持、浏览器可达 TLS 入口和 CORS；SDK 存在字段不代表现场支持，实测单独记录。

权限目录为 `sql/data/20261029_storage_write_permissions.sql`；角色授予使用 `scripts/ops/sql/grant-storage-write-permissions.sql`（显式 tenant_ids，默认预览，apply=true 才写入），不修改模板/其他租户/套餐。
对象名称模糊搜索、删桶、分片上传、重命名/移动、容量配额均不提供，既有 prefix 仍只作前缀定位。
本段为实现合同，不代表部署通过；原始响应及现场验证另行交付。

## VM-MVP-20261023：虚拟机 BFF

正式请求/响应及路由输入为 `api/protos/vm/service/v1/vm.proto`、`api/protos/admin/service/v1/i_vm.proto`。Module `VIRTUAL_MACHINE=17`；权限登记复用 [能力映射](contracts/permission-code-capability-map.tsv)，显式目录准备使用 `scripts/ops/sql/register-vm-permissions.sql`，不自动授予角色、套餐或资源额度。共享 compute 路径按 `usage=VIRTUAL_MACHINE` 检查 `vm:compute-preset:read` 及该模块资格。

创建/删除沿已有 Gov operation/事务/worker，VM 空 items/charges；其他业务变更由当前授权后直接调用 Resource。所有直达新变更及新 console 会话在精确原 VM CREATE→VM DELETE 持久受理栅栏后拒绝；只读 Resource/SG 原键重放先于该栅栏，异意图冲突保持原错误。操作查询先读 Gov 待投递记录，不将 owner 尚未接收误报为资源从未存在。DELETE expected_version 冻结后不得更新为当前版本；明确 owner 冲突保持原 operation/error，不把受理或永久拒绝写成删除完成。

DNS 创建选项由 `CreateVMData.dns` 接收：省略、空对象或 `{"mode":"PLATFORM_DEFAULT"}` 均选择平台默认；该模式不能附带 nameservers。自定义示例：

```json
{"data":{"idempotencyKey":"原创建幂等键","dns":{"mode":"CUSTOM","nameservers":["223.5.5.5","2001:4860:4860::8888"]}}}
```

示例省略其他必填创建字段。`CUSTOM` 接受 1–3 个 IP 字符串（IPv4/IPv6，不接受域名、端口或 zone），按首次出现顺序规范化、去重；有效性校验不承诺该 VPC 实际可达。前端可以通过 `GET /api/v1/virtual-machine-dns-defaults` 展示当前默认值，响应直接为 `{"nameservers":["223.5.5.5"]}`。该只读接口无请求体、分页或查询参数，复用创建页的 `vm:create` 权限并校验 `VIRTUAL_MACHINE` 套餐；不给用户开放平台默认值修改或可信租户、DNS 策略、冻结 spec 字段。

Gov 将用户选择规范化后纳入原 CREATE 意图摘要；默认的有效地址由 Resource 解析并冻结在 CreateSpec，Gov 不在公网上接收有效默认值快照。Gov → Resource 的 `/vm.v1.VMService/GetDNSDefaults` 是用户委托只读调用，沿既有受管 mTLS VM 客户端及 30 秒 deadline，从当前 Principal 和持久租户映射重建 tenant/actor metadata，不透传入站身份。详情/列表返回的 `dns` 为 `{mode,nameservers}`，表示该 VM 冻结的有效值；修改平台默认不改变已有 VM 或原幂等键重放，改变自定义地址或顺序仍是异意图冲突。历史没有 DNS 冻结字段的记录保持原表示，不补写当前默认值。实际 Linux、Windows DHCP/DNS 生效仍须分别验证。

Linux `DISK_IMAGE` 仅在 Resource 媒体目录 `initializationCapability=CLOUD_INIT_NOCLOUD` 且 `initializationVerified=true` 时接受可选 `guestInitialization`（用户名、控制台密码、用户提供的 SSH 公钥）；ISO、未知能力及未验证媒体拒绝。公开 DTO 不提供方法切换、密钥对生成或私钥管理。路径专用只读请求的 JSON 名显式保持 Proto 路径变量，避免生成目录 `{vmId}` 与实际鉴权 `{vm_id}` 分裂。

密码不进入普通 Resource CreateSpec、operation 查询或日志。Gov 使用既有强制副本共享的 AccessKeyCipher 主密钥分别派生凭据意图 HMAC 和 AES-GCM 子密钥；普通 intent 仅含 PasswordSet 与 HMAC，私密 payload 在受理事务内加密进原 outbox canonical，AAD 绑定可信 tenant、actor、原 operation/resource 与冻结 spec digest。进程重开从同一持久记录解密后，仅随 mTLS AcceptCreate 的独立 guest_credentials 参数投递；原键重放保留原 ciphertext 与原结果，改变密码仍是异意图冲突。DELETE 不解密密码。现有主密钥是该持久 outbox 的恢复依赖，不能在仍有待投递记录时丢失或任意替换。

实际 CODE/PG/BOUNDARY/LIVE/GUEST/BROWSER 结果统一见 Resource `docs/execution/status.md#vm-mvp` 及 `docs/execution/records/VM-MVP/`；此登记不是执行通过记录。

组合编译核验发现既有 ModelDev 根 SDK `v0.0.0-20261021184454-44188466290f` 仍引入 Model 根 SDK `v0.0.0-20261013092523-8951c85bc150`，其 `api/model/v1` 与本仓已固定的 Model/api 子模块同路径冲突。Gov 显式将该间接父模块固定到与现有 Model/api 同一提交的 `v0.0.0-20261021113912-a21befaaa5e5`；该提交含 `api/go.mod`，父模块不再导出该子树。不更改 Model/ModelDev 源码，不回退既有 API，也不以本地 replace 作为正式依赖修复。

## Sandbox 规格目录与权限接入（2026-10-27）

**当前合同更新：** Sandbox 是独立资源，接口及安全边界见[前端 HTTP 合同](contracts/frontend-http-api.md)。以下为历史记录，父实例相关规则已被本次需求取代，历史验收不能证明独立创建已验证。

管理入口为 Governance BFF；七条路由以 `api/protos/admin/service/v1/i_sandbox.proto` 为准，写请求保留 `data` 包装。各动作使用独立 `sandbox:create/list/read/operation:read/pause/resume/delete` 权限，均检查当前 INSTANCE 套餐、真实父实例租户归属，再经 mTLS 委托 Resource。新建与重放的父实例删除准入、查询/清理边界及 Pod/PVC/PV 引用响应见[前端 HTTP 合同](contracts/frontend-http-api.md)。映射维护于 `docs/contracts/permission-code-capability-map.tsv`。

`GET /api/v1/compute-presets?usage=SANDBOX` 使用 INSTANCE 套餐及独立 `sandbox:preset:read` 权限；默认 `compute_type=cpu`，显式 GPU 类型或非空 `gpu_mode` 返回 400。保持 SANDBOX 用途传给 Resource，不复用容器实例用途或权限。目录同步不自动授权；`scripts/ops/sql/register-sandbox-permissions.sql` 只准备默认 OFF 的权限及 API 关系，角色/套餐通过正常管理入口单独配置。

**联调前历史检查点（由下节后续结果替代）：** 集群 Provider 的 Kata 生命周期已验证；Governance 独立候选 r7 的 SANDBOX 目录查询已由 400 进入授权检查并返回 403。tenant-a/admin 登录 200，但缺 Sandbox 授权且实例为空；完整租户 HTTP → Governance → Resource DB/worker → Kata 生命周期尚待平台管理身份准备测试授权和合法父实例，不能据此段登记声称验收通过。原仓与候选的完整 API 生成、相关 Go 包/HTTP 用例及独占 PostgreSQL 授权/登记回归已定向验证；完整验收证据统一见 Resource 的 `docs/execution/records/SANDBOX-20261027/cluster-integration.json`。

### Sandbox 接口复核修正（2026-10-27）

原先登记的“待平台管理身份/合法父实例”是联调前检查点；随后已通过正常管理入口准备临时租户，完成完整 HTTP 生命周期、工作盘保留、重放与清理。真实证据和当前验收边界统一见 Resource `docs/execution/status.md` 与 `docs/execution/records/SANDBOX-20261027/http-lifecycle/result.json`，不由下述源码修正扩展原验收结论。

本次复核修正创建重放受当前 preset 变化影响、公共页码偏移与默认值、错误 `reason` 丢失及重放标志恒 false。HTTP 路径和 `data` 结构保留；严格校验 query/body、路径重复身份，错误映射回稳定业务原因。新意图摘要使用 Resource API helper；旧记录从冻结快照匹配。修正后的源码测试、生成和候选部署结果只在上述 Resource 唯一状态登记；原 Kata 实测不自动覆盖本轮新源码。
## OBS-TREND-20261027：首页资源趋势（租户级聚合）

新增 `GET /api/v1/observability/resource-trend`（权限码 `observability:resource-trend:read`，挂 `INSTANCE` 业务模块——2026-10-27 定案，现有 plan2/plan3 均含 INSTANCE、均无 DASHBOARD，不修改套餐数据）。目录通过生成的 HTTP 路由发现；部署时先 `admin sync-apis --dry-run` 审查，再同步并执行 `scripts/ops/sql/register-observability-permissions.sql`（权限点 + API 关联 + 向已持有 `instance:read` 的角色继承授予），最后重启刷新策略。

请求仅接受查询参数 `metric`（`cpu|memory|gpu`，省略时一次返回三条，顺序固定）、`range`（`1d|7d|30d`，必填）；可选 `start/end`（RFC3339 整秒，成对出现且跨度不超过所选档位）、`step`（Go duration 字符串，落在档位 `[min,max]` 内且为 30s 整数倍）。档位约束：1d 档 step 30s–1h（默认 5m）、7d 档 5m–1h（默认 30m）、30d 档 15m–4h（默认 2h）；未声明参数、重复参数、`tenant_id` 等一律 400 `MONITORING_INVALID_QUERY`。租户身份只取自 JWT，前端传 `tenant_id` 不生效。

响应为 `data` 包装（`protojson.EmitUnpopulated`），`series[]` 携带 `metric/metric_code/unit/status/points[]`，缺数点 `value=null` 且 `gap_reason="NO_DATA"`，与实例指标接口同构。obs 侧新增租户级 target（TENANT=3，方案 A 不改 api 子模块 proto）与三个租户级编码（`tenant.cpu.usage_cores` / `tenant.memory.working_set_bytes` / `tenant.gpu.memory_usage_bytes`），三条曲线统一为「租户全部工作负载的实际用量」：CPU/内存直查 cAdvisor（不再走 `container="workspace"` 的实例预聚合），GPU 为 HAMi 显存实际用量（bytes），租户归属由 pod/ns 三路 label 收敛的 pod 视图 join 得出；obs 与 gov 必须同批部署，GPU 编码与单位必须成对变更（否则 gov 侧 502）；30d 档依赖 Prometheus 保留期 ≥30d，历史数据不回补。

## OVERVIEW-20261027：首页资源统计卡片

新增 `GET /api/v1/overview`（权限码 `overview:resource-count:read`，挂 `INSTANCE` 业务模块，与首页资源趋势同批定案）。正式请求/响应为 `api/protos/admin/service/v1/i_overview.proto`，请求为 `google.protobuf.Empty`，`service OverviewService` 经 `pkg/constants/module_mapping.go` 映射为 `business_module=INSTANCE`。部署时先 `admin sync-apis --dry-run` 审查，再同步并执行 `scripts/ops/sql/register-overview-permissions.sql`（权限点 + API 关联 + 向已持有 `instance:read` 的角色继承授予），最后重启刷新策略。

请求无查询参数、无请求体，多余输入一律 400 `INVALID_OVERVIEW_REQUEST`。响应为**顶层四块、无 `data` 包装**的聚合快照，计数一律 JSON number（不适用列表接口的 64 位字符串约定）：`instances{total,by_state{pending,running,stopped,failed},other}`、`inference_services{total,by_status{pending,deploying,running,stopping,stopped,failed},other}`、`models{total,by_status{pending,importing,ready,error},other}`、`knowledge_bases{total,by_status{active,rebuilding},other}`，每块恒满足 `total = 正常 + 异常 + other`。

数据口径：实例按 `observedState=running|stopped|pending` 三次计数，`pending` 计入异常（`by_state.failed` = pending 数，`by_state.pending` 恒 0），`total` 排除 deleted；推理全量翻页（单页 100、最多 50 页）并按 `desiredState=deleted` 剔除软删记录，`phase` 的 `ready→running`、`stopped→stopped`、其余值（含过渡态与未识别值）→`failed` 并记 WARN；模型按 `pending/importing/ready/error` 四次计数，`other=pending+importing`；知识库无任何数据源，恒为 0。任一数据源失败、403 降级、超时只把对应卡片归零并记 WARN（含 `source` / `tenant_id` / `err`），整体仍 200；入口权限失败整体 403/503。内部聚合预算 8s，落在 REST 10s 传输预算内。

2026-10-29 增补 `gpu_quota`（GS 侧租户配额展示，编号沿用本条，不新开）：响应新增第五块 `gpu_quota{shared_memory_mib, physical_count}`，两项均含 `quota_code/unit/limit/occupied/available/over_limit/configured/enforcement`，**数值一律十进制字符串**（与 `/api/v1/me/quota-accounts` 同源同口径，避免 JS 2^53 精度上限），与同响应内四块卡片的 JSON number 类型不同。数据源复用 `QuotaAdminRepo.ListTenantAccounts(ctx, tenantID)`，只挑 `gpu.shared_memory_mib`（`CONCURRENT`，MiB）与 `gpu.physical.count`（`CONCURRENT`，gpu）两项，其余配额不进响应；两项是**二选一语义**（Whole 模式计整卡、Shared 模式计显存，同一实例只按一种计），不可相加。租户身份仍只取自 JWT，不接受租户入参（带查询串依旧 400）。`configured=false` 表示当前套餐未配置该配额（数值补 `"0"`），**不表示无限**；`enforcement` 仅 `ENFORCED` 表示真正拦截创建。鉴权沿用入口权限 `overview:resource-count:read`，不加数据源二次鉴权（配额数据属本租户自身套餐配置，无跨租户读取面）。降级沿用部分成功：配额读取失败、`missing` 装配或目录缺该 code 时只把两项补零并置 `configured=false` 记 WARN，整体仍 200。路由、权限码、`sys_apis` 与数据库均无变更，因此不需要 `sync-apis` 或新迁移。

## GPU-MONITORING-01 GPU monitoring v1.1

Contract: [GPU monitoring](contracts/gpu-monitoring.md); authority: [accepted v1.1 plan](plans/gpu-monitoring-backend-phase1-plan.md). Implements dedicated physical, tenant and authorized-resource reads, retained ACC attribution and readonly quota summaries. Container and inference detail types use original UsageRef; ModelDev execution is a reviewed extension contract. GPU transport/response and budget rules are separate from existing CPU/training reads. Implementation, Fedora checks and real-cluster acceptance are tracked separately in the task's single execution index; registration does not assert live completion. API/permission catalogue synchronization is explicit, with no user/role/policy changes.

`GET /api/v1/admin/tenants/quota-account-summaries` additionally returns `summary` with configured-tenant counts and per-quota limit/occupied/available totals. Aggregation retains platform-only `tenant:quota-read`, existing filters and the current read-only snapshot; it is independent of pagination. Shared-plan limits count per tenant, unconfigured account occupation is retained, and every aggregate number is a decimal string. No route, database migration, new permission or quota mutation is added; complete field semantics are maintained in the contract linked above.

### GOV-MODEL-INF-20261012：AK 策略合同更新（2026-10-29）

策略为独立租户资源，通过 `/api/v1/inference-policies` 创建、列表、查询、
完整更新和删除；AK 创建必带 `policy_id`，`POST /api/v1/inference-keys/{key_id}:bind-policy`
可重新绑定。旧服务策略接口退役。请求与权限及迁移边界见
[推理调用 Key 和策略合同](contracts/inference-keys.md)。所有管理请求由 Governance
认证并重建租户/actor，mTLS 转发 Inference；Higress 仅处理 `/v1/...`。
API 目录、权限和套餐关系须按部署流程显式登记，不能仅凭代码生成推断已部署。

前端页面迁移与请求示例见[推理 AK 与策略前端对接](frontend-inference-ak-policy.md)。

## USAGE-01：平台受管业务工作负载使用统计

平台管理员 JWT（tenant=0）及现行 Casbin `resource-usage:read`，模块 DASHBOARD。
新增 GET `/api/v1/admin/resource-usage/overview`、`/tenants`、`/tenants/{tenant_id}`；
Proto 唯一合同为 `api/protos/usage/service/v1/usage.proto`。
查询使用 RFC3339 整秒 start/end（最近30天）、metric=gpu/cpu/memory；排行另支持
keyword 名称/ID子串、order=asc/desc、page/pageSize（独立默认1/20，上限100），未知、重复、空值和GET body拒绝。
结果无 data 包装，total/rank/activeTenantCount 为十进制字符串；缺失完整均值、峰值、占比不补零。
Gov 从同一次租户目录读取持久 UUID，ACC 读取受管集群及历史 GPU scopes；只向已登记
`/observability.usage.v1.ResourceUsageService/QueryUsage` 发送服务端可信范围，mTLS复用现有Obs连接，
platform metadata不伪造tenant header，超时40秒、继承父取消、不自动重试。
Obs先计算全租户时间对齐统计，Gov再排序/搜索/分页，全平台三卡与占比分母不随筛选分页变化；
数据不完整时不提供精确rank/share；rank始终是全局均值降序名次，asc仅改变展示顺序。最多128租户、16集群，超预算失败不截断。
范围以Gov仍持久的租户目录为准（含停用、过期、冻结及保留的软删标记行）；完全物理删除且无目录映射的历史不作完整覆盖承诺。
客户端scope/观测快照按用户、窗口和租户范围短时复用15秒，不复用用户授权结果。
本条为新增实现合同；部署、权限登记和真实跨仓验收由本任务发布步骤分别确认。

## PLAT-COMP-20261028：平台组件健康快照（PLAT-COMP-01）

BOSS「组件状态」页数据源：`GET /api/v1/platform/components`。Proto 合同
`api/protos/admin/service/v1/i_platform_component.proto`；HTTP 由 server 层手写
snake_case JSON（`platform_component_http.go`），不是 protojson。
响应顶层 `observed_at`（UTC RFC3339 秒级）+ 固定三组 `groups`（service/dependency/platform，
顺序固定、空组也输出）+ `dev_profile`。行字段十个，`required` 仅五个；
`desired_replicas/ready_replicas/version/scrape_status/reason` 不可得时输出 JSON `null`（不是 0/空串）。
`kind` 三值含 DaemonSet（取 desiredNumberScheduled/numberReady）；`status` 四值
running/degraded/stopped/unknown（无 pending、无 missing）；`scrape_status` 三值
reachable/unreachable/unknown，仅 service 组非 null，由 Prometheus `up` 判定（Prometheus 未配置时恒 unknown，不翻转 real_provider）。
静态清单 26 条（2026-10-28 kcn 快照）硬编码于 `platform_component_catalog.go`；K8s 侧
3 namespace × 3 资源并行 List + 15s 进程内 TTL 缓存；单个对象缺失/读取失败降级该行
unknown+reason，仅 K8s client 未配置时 500 `COMPONENT_STATUS_FAILED`。
鉴权：平台身份闸门（用户主体、tenant_id=0、platform 角色前缀）+ 权限码
`component:status:read`（SYSTEM 模块，仅授予 `platform:admin`，租户闸门 fail-closed）。
部署侧：ClusterRole 增 apps 三资源 list；ServiceMonitor 白名单抓 6 个 metrics Service
（ani-resource-service 未暴露 19091，该行恒 unknown，属已知决策）。

## PLAT-COMP-20261028-LOG：平台组件日志流（PLAT-COMP-02）

BOSS 日志窗口数据源：`GET /api/v1/platform/components/{component}/logs/stream`（GET + SSE）。
Proto 合同 `api/protos/admin/service/v1/i_platform_component.proto` 内声明 `StreamComponentLogs`
流 RPC（无 `google.api.http` 注解），HTTP 由 `server/platform_component_logs.go` 手写注册。
组件清单与 `PLAT-COMP-01` 共用 `data/platform_component_catalog.go` 的 26 项注册表，不维护第二份。

请求：两个查询参数 `limit`（默认 100，1～200）与 `interval_seconds`（默认 2，1～5），重复/越界/
前导零/非整数一律 400；`_`、`_t`、`t` 作为缓存击穿参数整体忽略（不校验取值、允许重复）；
其余未知参数 400；请求带非空 `Last-Event-ID` 返回 400（本版不支持跨连接续传）。
响应：`text/event-stream; charset=utf-8` + `Cache-Control: no-store` + `X-Accel-Buffering: no`，
先发 `: connected`，回放最近 `limit` 条（正序），随后按 `interval_seconds` 轮询增量，
无日志时每 2 秒发 `: heartbeat` 注释帧，从 `connected` 起 8 秒（代码常量，不可配）正常关闭。

`event: log` 的 `data` 严格六字段、全部为非空 string（不输出 null）：
`container`（`kubernetes.container_name`）、`level`（`ani_log.level`，缺失为 `unknown`）、
`message`（`log` → `ani_log.message` → 序列化 `log_payload` 三级回退，全无则跳条并计数）、
`pod`（`kubernetes.pod_name`）、`stream`（保留 stdout/stderr，缺失为空串、不编造）、
`timestamp`（UTC RFC3339 **毫秒**精度，取自 `@timestamp`；缺失或不可解析则跳条并计数）。
与旧版四处取值差异：`level` 不再恒为 `info`；`timestamp` 由纳秒降为毫秒（不得用作去重键）；
组件名取值域换成新环境 26 项（前端必须从 `GET /api/v1/platform/components` 取，不得硬编码）；
`message` 来源由 Loki 行内容换成 OpenSearch `log` 字段（存量记录可能只有 `log_payload`）。

错误体为本登记唯一例外：不走全局「数字 code + reason」编码器，改为旧版
`{"code": <字符串>, "message": <字符串>, "request_id": <字符串>}`，且 handler 写完后返回 nil，
不让全局编码器在流后追加第二段 JSON。状态码与 code：401 `UNAUTHORIZED`、403
`PLATFORM_COMPONENT_DENIED`、404 `COMPONENT_DIAGNOSTICS_FAILED`、400
`INVALID_COMPONENT_LOG_QUERY`、429 `COMPONENT_LOG_CONNECTION_LIMIT`（带 `Retry-After: 8`）、
503 `COMPONENT_LOG_UNAVAILABLE`、504 `COMPONENT_LOG_QUERY_TIMEOUT`、发头后失败改发
`event: error`（`COMPONENT_LOG_LIMIT_EXCEEDED` / `COMPONENT_LOG_WINDOW_INCOMPLETE`）。

鉴权：平台身份闸门（用户主体、`tenant_id=0`、claims tenant=0、ID 与 claims 一致、platform 角色前缀）
+ 独立权限码 `component:log:read`（SYSTEM 模块，仅授予 `platform:admin`；`component:status:read`
不能替代，反向亦然）；JWT-only，不支持 API Key 签名访问；租户用户 403。

OpenSearch 侧使用独立只读身份：仅 `ani-logs-*` 的 search/scroll/mappings get/admin get 与
`cluster_monitor`，不复用管理员或 Fluent Bit 写身份，地址/CA/凭据全部来自受管配置文件
（`ANI_COMPONENT_LOGS_CONFIG`），不接受请求指定。查询范围完全由注册表派生
（`kubernetes.namespace_name.keyword` 精确 + `kubernetes.pod_name.keyword` 正则按 Kind 区分
Deployment/StatefulSet/DaemonSet），并额外 `must_not` 排除 Pod 名含 `sandbox` 的文档。

增量：连接内按 `_index`+`_id` 去重（回放命中预置进集合），每轮取 `[now-30s, now]` 窗口；
`ani_log.ingested_at` 存在时按入库时间查询，缺失时运行时探测后退化为 `@timestamp` 事件时间，
**该退化会漏读迟到超过 30 秒的日志，属已登记已知缺陷**。单轮取满 `increment_max`（500）时
发 `COMPONENT_LOG_WINDOW_INCOMPLETE` 并结束连接，不截断后冒充完整。
配额：进程 16 / 每用户 4 / 每组件 4（含打开中），超限 429。

保留期 3 天；一期不含跨连接断点续传、深分页、历史回补、全量导出与日志正文脱敏
（正文可能含凭据，靠权限授予范围控制，见方案 §4.2 / §14.1）。

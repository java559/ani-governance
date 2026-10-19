# Governance → Network 的 mTLS 部署参考

Governance 为已接入的租户业务验证登录、租户、套餐和接口权限，
再用自己的服务身份调用对应领域服务。本目录是现有 Network DNS 客户端身份的
证书申请与部署挂载参考，接入步骤见 [业务服务接入指南](../../../docs/service-integration.md)。
其他当前调用链的身份与证书要求以各自合同为准；GPU 链的 URI/DNS 差异见
[GPU owner 接入指南](../../../docs/contracts/gpu-owner-integration-guide.md#4-删除与完整释放)，
不能把此 DNS 示例直接用于 Gov → Accelerator。Gov 的 Inference 管理对接本期延期，
当前不部署其客户端身份；后续重新对接时单独核对身份合同。

> **Model 已于 2026-09-21 暂摘**：`ANI_MODEL_*` 环境变量与 `model-trust` 卷已从
> `deployment-patch.yaml` 移除，`GET /api/v1/models` 下线。
> 历史来源见 [固定版本 model-lab](https://github.com/zhangzhe-ctrl/ani-governance/blob/63849fc4cde38b879184a8fea4a6f539e60063e1/scripts/model-lab/README.md)。
> 旧夹具已退出 HEAD；重接 Model 需要按当前接口与身份合同重新装配和验收。

本目录保留 `certificate.yaml` 和 `deployment-patch.yaml`。新增调用前核对双方身份、
信任根、方法授权和配置；证书与挂载是否可复用取决于双方合同，不能仅凭同一 CA 判断。

## 部署

在远程 Kubernetes 管理节点执行；本地不编译、不运行集群命令。
前提是 cert-manager 已安装，命名空间内存在下游领域服务所信任 CA 对应的
`Issuer/ani-internal-ca`。公共签发机构由基础设施部署（ani-installer）负责，
本目录不安装 cert-manager、不生成或携带 CA 私钥。
若平台提供 ClusterIssuer，修改 certificate.yaml 的 issuerRef 名称及 kind 即可。

当前 [certificate.yaml](certificate.yaml) 声明 `Issuer/ani-internal-ca`、DNS SAN `ani-governance`、
`client auth` 用途、有效期 `72h` 和提前 `24h` 续期；这是本目录的具体示例，实际部署需按目标信任体系核对。

配置适配现有 Deployment/governance 和容器 governance；其他部署名需对应调整。
下游服务地址（如 `ANI_NETWORK_ADDR`）按各服务的接入说明配置。
进入 `scripts/deploy/governance-mtls/`，把平台提供的**下游信任根公钥 PEM**
保存为 ca.pem 后执行：

```sh
NS=gov-network-20260919-01  # 替换为目标命名空间
kubectl -n "$NS" create configmap governance-downstream-ca \
  --from-file=ca.crt=ca.pem --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "$NS" apply -f certificate.yaml
kubectl -n "$NS" wait --for=condition=Ready certificate/governance-client --timeout=60s
kubectl -n "$NS" patch deployment governance --type=strategic --patch-file deployment-patch.yaml
kubectl -n "$NS" rollout status deployment/governance --timeout=60s
```

> 历史记录（Model 时期）使用的是 `governance-model-ca` ConfigMap 与
> `gov-model-20260919-01` 命名空间；ConfigMap 名称与命名空间按目标下游调整。

这两份声明应随 Network 接线的实际 Governance 部署保留；不要重放已退役的
model-lab 脚本来准备当前部署。证书路径和客户端配置应合入当前受审查的部署清单。
客户端在启动时读证书。cert-manager 更新 Secret 后，执行
`kubectl -n "$NS" rollout restart deployment/governance` 并等待 rollout 完成，
随后运行当前下游接口的鉴权与跨租户验证。自动重载与根 CA 轮换需另行验证。
证书自动续期不延长根 CA 的寿命；短期实验 CA 不作为正式环境长期签发机构。
机制参考：https://cert-manager.io/docs/usage/certificate/

## 一次请求如何走（以 Network 为例）

1. 浏览器/外部客户端调用 Governance 的 `GET /api/v1/networks/vpcs/{vpc_id}`。
2. Governance 完成登录、租户、套餐及接口权限检查，从可信上下文获取用户与租户。
3. NetworkClient 用 `ani-governance` 客户端证书连接 Network，验证下游
   的 CA 和固定身份；重新构造内部用户/租户 metadata，
   不透传外部伪造身份头。
4. Network 验证客户端证书、调用方身份和允许的 RPC，然后按可信租户读取自己的数据库。
5. Governance 将结果转换成已有 HTTP 返回格式交给客户端。

（Model 时期的原文描述 `GET /api/v1/models` 链路，机制完全相同，仅下游不同。）

证书证明调用者是哪个服务，不代替用户权限和租户数据隔离。
外部 HTTPS 使用的服务端证书与这里的内部客户端证书是两件事。

## 如何接入其他服务

- Governance → 新服务：在 Governance 加该服务客户端及 HTTP 入口，登记权限和
  套餐关系；按该链合同配置目标地址、CA、客户端证书和服务端身份。
  本目录的 `governance-client-tls` 是 Network DNS 身份示例，不保证适用于其他当前链路。
- 新服务自己的部署申请服务端证书，挂载自己的私钥和客户端信任 CA；接收端明确允许
  已登记的 Governance 证书身份调用哪些 RPC，从可信调用中获取租户并限定数据库查询。
  不能只因证书由同一个 CA 签发就放行所有服务或所有方法。
- Inference → Model 等内部调用仍直接调用目标服务，使用 Inference 自己的证书，
  不借用 Governance 的证书，也不因为 Governance 是外部入口就绕行它。
- 每接一个接口，只验证真实成功、未授权拒绝、跨租户不泄露；复用现有登录和权限机制。

## 回退

部署前记录 Deployment revision。若切换失败，用 `kubectl rollout undo deployment/governance
--to-revision=<原 revision>` 恢复原挂载和环境变量，并等待 rollout 完成。
旧 Secret 在本次切换中保留；切勿在确认新链路成功前删除。

## 每次部署后的复验

按当前下游实际身份和租户准备独占可审计的请求，记录镜像 SHA、证书有效期、Deployment revision、命令和原始退出码。确认真实登录、两个租户的返回与其数据一致、未登录 401、无权限 403、伪造租户头无效、无证书与错误服务证书均在 TLS 层拒绝，以及授权请求实际读成功。Network 路径还需比对 VPC 返回字段与数据源；HTTP 200 本身不足以证明隔离。自然续期后的客户端重载、根 CA 轮换和其他下游仍需分别验收。

2026-09-19 的两份固定结果 JSON 已退出 HEAD；它们只代表当时源码、实验 CA 和集群，来源见 [历史索引](../../../docs/history/README.md)。不可复用已过期实验 CA 或旧 namespace 作为当前部署参数。

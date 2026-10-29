# 内部服务证书与续期加载

本目录是 `ani-system` 现有服务部署的迁移配置。它复用平台已安装的 cert-manager 和
Ready 的 `ClusterIssuer/ani-ca`，不创建新根 CA。跨环境使用前，调整 namespace、
Issuer、工作负载名称和原配置 Secret/ConfigMap 引用；补丁不包含应用镜像、业务配置值或私钥。
不得将这些环境补丁直接当成通用 Helm values。

证书身份遵守 [服务间调用合同](../../../docs/contracts/service-to-service-calls.md)。
共同 CA 不表示共同证书或共同权限。所有新增叶证书独立生成密钥，90 天有效，提前 15 天续期，
`rotationPolicy: Always`。既有 cert-manager Certificate 继续由原部署入口维护，不重复接管。

| 证书 Secret | 用途 | 精确身份 |
|---|---|---|
| ani-governance-client-tls | Gov 调用业务服务 | DNS `ani-governance`，clientAuth |
| ani-governance-server-tls | Gov quota listener | DNS `ani-governance`，serverAuth |
| ani-accelerator-server-tls | ACC listener | DNS `ani-accelerator-service`，serverAuth |
| ani-resource-client-tls | Res 调用 ACC/Gov | DNS `ani-resource-service`，clientAuth |
| ani-resource-server-tls | Res 业务、镜像、终端 listener | DNS `ani-network-service`，serverAuth |
| ani-node-agent-client-tls | Agent 调用 ACC | DNS `ani-node-agent`，clientAuth |
| ani-governance-session-client-tls | 既有 VM 会话控制 | URI `spiffe://ani.internal/service/ani-governance`，clientAuth |
| ani-session-gateway-client-tls | 既有 Res 终端 listener | URI `spiffe://ani.internal/service/ani-session-gateway`，clientAuth |
| ani-session-gateway-server-tls | 既有 SG 控制 listener | DNS `ani-session-gateway`，serverAuth |
| ani-inference-gateway-authz-mtls | Higress 鉴权 companion 调用 Inference | DNS `ani-inference-gateway.ani.internal`，clientAuth |

VM 会话仍按当前接收端合同检查唯一 URI；该身份专用证书与 DNS 业务客户端分开。
本迁移只统一其签发与信任，不宣称这两条存量协议已完成 DNS 身份改造。
Inference 的 ACC、镜像目录和退额客户端使用既有受管证书 `inference-accelerator-mtls`，
唯一 DNS `ani-inference-service`；Model 客户端保留单独登记的 `ani-inference.ani.internal`。

## 分阶段迁移

需要 Python 3、PyYAML、kubectl 及明确的集群 context。先备份工作负载和原身份材料到受保护目录。
`apply.py` 不操作业务数据库，不替换应用镜像，也不覆盖原配置 Secret 中的业务配置和凭据。
它保留原有 init-copy 的普通文件要求，从受管 Secret 投影密钥并在启动时复制到私有目录。

```sh
python3 scripts/deploy/service-mtls/apply.py --context "$ANI_TARGET_CONTEXT" --phase issue
```

等待本目录 9 个 Certificate Ready，再建立迁移信任并重启消费者：

```sh
python3 scripts/deploy/service-mtls/apply.py --context "$ANI_TARGET_CONTEXT" --phase trust \
  --legacy-ca-secret ani-governance-runtime:ca.crt \
  --legacy-ca-secret ani-vm-gov-mtls-20261025:ca.crt \
  --legacy-ca-secret image-registry-20261026-inference-image-runtime-tls:ca.crt
python3 scripts/deploy/service-mtls/apply.py --context "$ANI_TARGET_CONTEXT" --phase identities
```

确认所有工作负载就绪，并用正确调用者证书逐条验证业务 listener 后，移除旧 CA 信任：

```sh
python3 scripts/deploy/service-mtls/apply.py --context "$ANI_TARGET_CONTEXT" --phase finalize
```

最终内部信任源为 `ConfigMap/ani-internal-service-ca` 的 `ca.crt`，只含平台根 CA。
不可变的旧 ConfigMap 保留原业务内容，通过 projected volume 选择新信任源。
原 Secret 内未选中的旧身份材料不再供进程使用；备份清理须按平台保留政策另行执行。
Harbor、Kubernetes API 等独立用途的信任不由本迁移替换。

## 自动续期后的进程加载

cert-manager 更新 Secret 后，已启动的这些进程不会自动重读证书。
`reload-cronjob.yaml` 每两分钟比较受管叶证书和根信任的 SHA-256；变化时仅更新
对应 Deployment/DaemonSet 的 Pod 模板注解，触发 Kubernetes 滚动重启。
没有变化时不重启。证书只读 public `tls.crt` 参与摘要，日志不输出材料内容。
Role 仅能读取登记的证书 Secret、信任 ConfigMap，以及 get/patch 登记的工作负载。

镜像由 `Reload.Dockerfile`、`reload.sh` 和经校验的固定 kubectl 二进制构建；清单中的镜像固定到
发布摘要。调整服务名称或证书 Secret 时，同时更新 bindings 和 Role 的 resourceNames。
`ani-inference-service` 的绑定也包含鉴权 companion 的客户端证书；该叶证书续期时，
同一 Pod 中的 Inference 和 companion 一起滚动重启，重新建立 mTLS 连接。
该 Job 只负责叶证书续期后的加载，不执行根 CA 轮换。

```sh
kubectl --context "$ANI_TARGET_CONTEXT" apply -f scripts/deploy/service-mtls/reload-cronjob.yaml
cmctl --context "$ANI_TARGET_CONTEXT" -n ani-system renew ani-governance-server-tls
```

验收须同时看到 Certificate revision 和叶证书序列变化、自动 Job 成功、Pod 更新完成、
真实连接呈现新的叶证书，并继续通过业务请求。续期命令见
[cert-manager cmctl 文档](https://cert-manager.io/docs/reference/cmctl/#renew)。
单独看到 Certificate Ready 或 Secret 更新不代表进程加载完成。

## 推理退额身份

Gov 的 `ANI_QUOTA_INFERENCE_ENABLED=true` 显式启用固定映射
`ani-inference-service → ani-inference`。前者是受管客户端 DNS，后者是原账本 owner，
不得改写历史账本 owner、复用 Res/Gov 私钥或开放任意 owner 映射。
Helm 对应 `quotaInternal.enabled=true`、`quotaInternal.inferenceEnabled=true`。
镜像必须包含该注册代码；只改 CA 或设置旧镜像不认识的环境变量不会完成退额。
验收复用原释放事件，确认原 charge 只退一次；不得人工修改账本制造成功。

部署配置改变后分别验收 Governance `/api/v1/...` 管理接口与 Higress `/v1/completions`
真实生成；Higress 不承载 `/api/v1/...` 管理路由。

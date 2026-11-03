# ANI Governance Helm Chart

部署 Gov BFF、Atlas 迁移 Job 和首个管理员初始化 Job。推荐 release
`ani-governance`、命名空间 `ani-system`，Service 默认提供 REST `7788` 和 SSE
`7789`。默认类型为 NodePort、外部端口由 Kubernetes 分配；
[离线示例](values-offline.example.yaml) 使用 ClusterIP，由 Console 同源代理访问 REST。

第一批 Governance + Model + Console 复用 installer 的
`postgresql.ani-platform.svc.cluster.local:5432` 和平台 Valkey
`valkey.ani-platform.svc.cluster.local:6379`。本 Chart 不创建 PostgreSQL、
Valkey、Model、Console 或 IAM，不修改当前认证机制。

## 准备数据库和 Secret

先由数据库管理员创建独立库 `ani_governance`、独立迁移账号和独立运行账号。
不要默认使用基础服务的 `ani_app` 创建数据库，它不是数据库管理员。
迁移账号持有本库 DDL 和首次初始化权限；运行账号只持有业务表/序列的必要 DML 权限，
后续迁移新增表的授权也需明确。不可把超级用户或迁移账号放进 Deployment。
数据库权限和 Atlas 显式迁移规则见 [部署说明](../../docs/deployment.md)。

先创建 `ani-system`，再按
[Secret 模板](secrets.example.yaml) 在仓库外制作本环境文件；模板不能直接部署。
它只使用占位符，不含可用密码或密钥。

| Secret / 键 | 消费者与内容 |
| --- | --- |
| `ani-governance-config / zz-secrets.yaml` | 服务配置：运行 DSN、Valkey 原始密码、Asynq URI、JWT RSA 私钥和公钥 |
| `ani-governance-migration / dsn` | 仅 Atlas 和 admin Job 使用的迁移 DSN |
| `ani-governance-auth / access-key-encryption-key` | 独立 64 位十六进制主密钥，挂载为文件，注入 `ANI_ACCESS_KEY_ENCRYPTION_KEY_FILE` |
| `ani-governance-auth / global-encryption-key` | 建议独立生成 64 位十六进制字符串，注入 `GOWIND_CRYPTO_KEY` |
| `ani-governance-auth / admin-password` | 首次管理员密码；已有初始化不会被重置 |

`zz-secrets.yaml` 的结构：

```yaml
data:
  database:
    source: "postgres://RUNTIME_USER:URLENCODED_PASSWORD@postgresql.ani-platform.svc.cluster.local:5432/ani_governance?sslmode=disable"
  redis:
    password: "RAW_VALKEY_PASSWORD"
server:
  asynq:
    uri: "redis://:URLENCODED_VALKEY_PASSWORD@valkey.ani-platform.svc.cluster.local:6379/1"
authn:
  jwt:
    private_key: |
      REPLACE_WITH_PRIVATE_KEY_PEM
    public_key: |
      REPLACE_WITH_PUBLIC_KEY_PEM
```

Gov 的 `data.redis` 字段和 Asynq 的 `redis://` 使用 Redis 兼容协议连接 Valkey，
不要求另建 Redis。部署方从平台受管凭据来源安全准备同一 Valkey 密码；跨命名空间
不能直接引用 Secret。数据库及 Asynq URI 中的密码需 URL 编码，`data.redis.password`
保留原文。
配置文件不展开 shell 环境变量。基础 ConfigMap 已固定
`data.database.driver=postgres`、`migrate=false`、`authz.type=casbin`；
`zz-` 配置文件在其后覆盖。密钥需新生成并安全保存，多副本使用同一套；
`global-encryption-key` 实现接受非空原始字符串并用 SHA-256 派生 AES-256 密钥；
64 位十六进制是本示例的生成约定。`access-key-encryption-key` 则严格要求 64 位十六进制。
Secret 更新后显式滚动重启服务，不把真实值提交到 Git。

## Model 双向 TLS

开启 `downstream.model.enabled` 时，必须同时设置：

| values 字段 | 第一批接线 |
| --- | --- |
| `address` | `ani-model-grpc.ani-system.svc.cluster.local:19090` |
| `serverName` | `ani-model.ani.internal`，精确匹配受管服务端 DNS SAN |
| `caSecret` / `caKey` | `ani-model-server-tls` / `ca.crt`，用于信任 Model CA |
| `clientCertificateSecret` | `governance-client-tls`，包含 `tls.crt`、`tls.key` |

Gov 客户端证书必须只有唯一 DNS SAN `ani-governance`，无 URI、IP、email 或额外 DNS，
用途为 clientAuth；Model 服务端证书需要 serverAuth。双方使用共同受管 CA 和 TLS 1.3。
Chart 注入 `ANI_MODEL_ADDR/SERVER_NAME/CA/CERT/KEY`，不会退化为明文。
Model 启用该身份和 RPC 的要求见
[服务间调用合同](../../docs/contracts/service-to-service-calls.md)。

安装前先签发双方证书、等待 Certificate Ready 和 Secret 全部存在，再启动 Gov。
离线示例由外部 bootstrap 准备证书，`certificate.enabled=false`，
避免重复管理同一个 Certificate。
也可选择先安装 Model 并等待服务端证书，再装 Gov；不能先让 Gov 等待后装 Chart 的 Secret。

若由 Gov Chart 签发客户端证书，设置 `certificate.enabled=true`，
明确配置已 Ready 的 `certificate.issuerRef`（installer 环境通常为 `ClusterIssuer/ani-ca`），
并保持 `certificate.secretName` 与 `clientCertificateSecret` 一致。
不要把旧环境的 CA、叶证书或私钥作为本次输入。

客户端在启动时读取证书。cert-manager 续期后须对 Gov 显式滚动重启并验证真实 RPC；
本 Chart 没有自动续期重载 Job。现有环境专用的
[证书迁移脚本](../../scripts/deploy/service-mtls/README.md) 不能直接当成新集群安装步骤。

## 镜像与离线材料

| 镜像 | 本仓真实构建入口 |
| --- | --- |
| Gov server | 根 `Dockerfile` 的 `runtime-server` target；`make -C app/admin/service docker_server` |
| Gov admin CLI | 根 `Dockerfile` 的 `runtime-admin` target；`make -C app/admin/service docker_admin` |
| Gov Atlas（含本次 migrations） | `scripts/deploy/atlas/build-image.sh`，需要已准备的 Atlas v1.3.0 二进制及 busybox:1.37 基底 |

server/admin 的编译版本以 `go.mod` 和 Dockerfile 为准；服务及 admin runtime 使用
distroless，无 shell。Atlas 独立镜像内必须含 `/bin/atlas` 和
`/app/migrations`，不能仅用一个没有迁移文件的上游 Atlas 镜像替代。

离线环境先收集这三个 Gov 镜像，并配置节点到离线仓库的信任；平台 Valkey 镜像
由 installer 单独准备。
覆盖三个业务镜像的 repository 与 tag/digest；digest 优先。
私有仓库的 `imagePullSecrets` 在各工作负载命名空间分别创建。
本 Chart 无外部 Chart 依赖；镜像与 Chart 归档分开交付。

## 安装和验收

在仓库外编辑离线 values，填写本次构建的三个镜像版本、实际 Console origin 和 Secret 名。
不使用历史验证摘要。Secret、平台 Valkey、业务库和证书须在 Helm 前就绪。

```bash
helm lint charts/ani-governance --strict -f /absolute/path/governance.values.yaml
helm template ani-governance charts/ani-governance \
  --namespace ani-system -f /absolute/path/governance.values.yaml
helm upgrade --install ani-governance charts/ani-governance \
  --namespace ani-system -f /absolute/path/governance.values.yaml \
  --wait --timeout 15m
```

Helm 顺序：Atlas `pre-install/pre-upgrade`（权重 -10）→ admin init（权重 -5）
→ 创建/更新 Gov Deployment。迁移失败会中止；admin init 保留已初始化的数据和原管理员。
API 增量同步仍按 [部署说明](../../docs/deployment.md) 显式预览和执行，不在升级时自动授权。

```bash
kubectl -n ani-system get deploy,pod,svc,job -l app.kubernetes.io/instance=ani-governance
kubectl -n ani-system logs job/ani-governance-migrate
kubectl -n ani-system logs job/ani-governance-admin-init
```

探针仅检查 REST TCP 端口，不能代替登录和模型业务验收。正式验收需检查迁移、首次初始化、
浏览器登录/CSRF/刷新、授权拒绝和 Gov → Model 的真实业务 RPC。
Console 与 Gov 同源代理时不需要跨域 CORS，但认证仍检查精确浏览器 origin
（scheme、host、非默认端口，无路径或末尾斜杠）。
配置 `auth.consoleOrigins`；BOSS 未交付时也不借用 Console 会话。
仅明文 HTTP 环境使用 `auth.insecureRefreshCookie=true`，HTTPS 保持 false。

## 调度权限与本地检查

`rbac.create=true` 默认创建只读 PriorityClass/PodGroup 权限，以及供平台组件健康
快照使用的 Deployment、StatefulSet 和 DaemonSet 跨命名空间 list 权限；仅当
`volcano-system` 已存在时创建对 `ani-volcano-scheduler-configmap` 的 get/update 权限。
Chart 不创建该 namespace。离线 `helm template` 不输出需要 lookup 的 Role/Binding，
实际安装须重新检查；服务账号 token 仅挂 Gov Deployment，两个 Job 不挂。

```bash
ANI_CHART_HELM=/absolute/path/to/helm python3 scripts/tests/test_governance_chart.py -v
```

检查使用本地只读 discovery fixture，不连接现有 kubeconfig，不部署或访问数据库。

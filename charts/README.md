# ANI Governance Charts

这组 Chart 面向新集群，不使用旧环境的 namespace、数据库或 Secret。

| Chart | 命名空间 | 作用 |
| --- | --- | --- |
| `ani-governance-redis` | `ani-infra` | Gov 专用 Redis、Service 和持久卷 |
| `ani-governance` | `ani-system` | Gov BFF、数据库迁移 Job、管理员初始化 Job 和 NodePort Service |

公共 PostgreSQL、MinIO、KServe、LWS、Higress、Model Service、Inference Service、GPU 服务和 IAM
由平台或各自项目负责，本组 Chart 不重复创建。Gov Chart 也不把迁移账号放进服务 Deployment。

## 安装顺序

先确认 kubeconfig 当前 context 是新集群；Helm 默认使用当前 context：

```bash
kubectl config current-context
kubectl get nodes
```

1. 在 `ani-infra/postgres` 创建独立数据库 `ani_governance`。
2. 按 [`ani-governance-redis/README.md`](ani-governance-redis/README.md) 创建 Redis 密码 Secret，并安装 Redis Chart。
3. 按 [`ani-governance/README.md`](ani-governance/README.md) 创建迁移、认证和配置 Secret，以及受限的
   `ani_governance_runtime` 数据库账号。
4. 使用 Helm 安装 Gov Chart。迁移和管理员初始化是 pre-install/pre-upgrade hooks，完成后才会创建/更新
   Gov Deployment。

实际安装只使用 `helm upgrade --install`；仓库里的脚本不属于生产安装路径。

## 当前新集群验证结果

- `ani-governance-redis`：`ani-infra` 中 StatefulSet `redis` 运行正常，2 GiB `local-path` PVC 已绑定。
- `ani-governance`：`ani-system` 中 Deployment 1/1，NodePort Service 暴露 7788（REST）和 7789（SSE）。
- 数据库迁移 Job 和管理员初始化 Job 均为 `Complete`。
- 平台登录和 `GET /admin/v1/me` 已通过；未认证访问返回 401，套餐列表返回 200。

Governance 业务 API 通过 NodePort 对外提供，不经过 Higress。Higress 只承载 AI OpenAPI
（例如 `/v1/completions`）；Chart 不会把 `/api/v1/...` 业务路由加入 Higress。

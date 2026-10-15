# ANI Governance Redis

这是 Gov 的独立 Redis 基础设施 Chart，安装在 `ani-infra`，不由
`ani-governance` 业务 Chart 隐式创建。它只提供 `redis.ani-infra.svc.cluster.local:6379`，
不承担 PostgreSQL、MinIO 或 IAM。

部署前在 `ani-infra` 创建只包含 `password` 键的 Secret：

```bash
kubectl -n ani-infra create secret generic ani-governance-redis \
  --from-file=password=redis-password \
  --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install ani-governance-redis charts/ani-governance-redis \
  --namespace ani-infra --create-namespace --wait --timeout 10m
```

Gov Chart 的配置 Secret 仍需写入同一个 Redis 密码；跨命名空间不会自动读取 Secret。

# ANI Governance Helm Chart

这个 Chart 部署 `/root/kubercon/ani-governance` 的 Gov BFF 入口服务，目标命名空间是
`ani-system`。Gov 是业务入口，负责登录、租户/套餐/权限、模型 BFF 和推理 BFF；数据库迁移
与首个管理员初始化也由本 Chart 管理。

公共 PostgreSQL 和独立 Redis 不在这个 Chart 内创建，分别使用新集群的
`ani-infra/postgres` 与 `ani-infra/redis`。KServe、LWS、Higress、Model Service、Inference Service
和 GPU 服务由各自的平台/服务 Chart 管理。本 Chart 通过 TLS 客户端证书调用 Model 与 Inference，
不会把它们的 Deployment 复制进 Gov Chart。

## 部署前准备

先安装独立 Redis Chart，再在新集群的 PostgreSQL 中创建独立数据库 `ani_governance`。
不要把 Gov 表写入
`ani_model` 或 `ani_inference`：

```bash
kubectl -n ani-infra exec postgres-0 -- psql -U ani -d postgres \
  -c 'CREATE DATABASE ani_governance'
```

如果数据库已经存在，上面的命令返回已存在错误即可，不要删除原数据库。

Redis 先单独安装，命名空间是 `ani-infra`：

```bash
helm upgrade --install ani-governance-redis charts/ani-governance-redis \
  --namespace ani-infra --create-namespace --wait --timeout 10m
```

在部署机上生成本环境自己的密钥。真实值只通过 `kubectl create secret` 写入新集群，
不要提交到 Git：

```bash
umask 077
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out jwt-private-key.pem
openssl pkey -in jwt-private-key.pem -pubout -out jwt-public-key.pem
openssl rand -hex 32 > access-key-encryption-key
openssl rand -hex 32 > global-encryption-key
openssl rand -hex 32 > redis-password
openssl rand -base64 32 > admin-password
```

创建迁移和认证 Secret。运行时 DSN 会放在单独的配置 Secret 中。`postgres-auth` 是公共 PostgreSQL
已有的 Secret；下面只读取它，不会修改旧环境：

```bash
PG_PASSWORD="$(kubectl -n ani-infra get secret postgres-auth -o jsonpath='{.data.password}' | base64 -d)"
MIGRATION_DSN="postgres://ani:${PG_PASSWORD}@postgres.ani-infra.svc.cluster.local:5432/ani_governance?sslmode=disable"

kubectl -n ani-system create secret generic ani-governance-migration \
  --from-literal=dsn="${MIGRATION_DSN}" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n ani-infra create secret generic ani-governance-redis \
  --from-file=password=redis-password --dry-run=client -o yaml | kubectl apply -f -
kubectl -n ani-system create secret generic ani-governance-auth \
  --from-file=access-key-encryption-key \
  --from-file=global-encryption-key \
  --from-file=admin-password \
  --dry-run=client -o yaml | kubectl apply -f -
```

为服务创建只允许业务表读写的 PostgreSQL 运行账号。迁移和管理员初始化继续使用上面的
`MIGRATION_DSN`，不要把超级用户或迁移账号放进 Deployment：

```bash
RUNTIME_PASSWORD="$(openssl rand -hex 32)"
if kubectl -n ani-infra exec postgres-0 -- \
  psql -U ani -d ani_governance -Atc \
  "SELECT 1 FROM pg_roles WHERE rolname='ani_governance_runtime'" | grep -qx 1; then
  kubectl -n ani-infra exec postgres-0 -- \
    psql -U ani -d ani_governance -v ON_ERROR_STOP=1 \
    -c "ALTER ROLE ani_governance_runtime NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD '${RUNTIME_PASSWORD}'"
else
  kubectl -n ani-infra exec postgres-0 -- \
    psql -U ani -d ani_governance -v ON_ERROR_STOP=1 \
    -c "CREATE ROLE ani_governance_runtime LOGIN PASSWORD '${RUNTIME_PASSWORD}' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS"
fi
kubectl -n ani-infra exec -i postgres-0 -- psql -U ani -d ani_governance -v ON_ERROR_STOP=1 <<'SQL'
GRANT CONNECT ON DATABASE ani_governance TO ani_governance_runtime;
GRANT USAGE ON SCHEMA public TO ani_governance_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ani_governance_runtime;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public TO ani_governance_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE ani IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO ani_governance_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE ani IN SCHEMA public
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO ani_governance_runtime;
SQL
```

把本次生成的 `RUNTIME_PASSWORD` 安全保存到部署机临时文件；它要填入下面的
`RUNTIME_DSN`，不能提交到 Git。

Gov 使用 Kratos 文件配置。由于配置文件不会自动展开环境变量，需要再创建一个只在
集群中的配置 Secret，把数据库、Redis、Asynq 和 JWT 密钥放进去：

```bash
export REDIS_PASSWORD="$(cat redis-password)"
export PG_PASSWORD
REDIS_URI_PASSWORD="$(python3 -c 'import os,urllib.parse; print(urllib.parse.quote(os.environ["REDIS_PASSWORD"], safe=""))')"
# RUNTIME_DSN 应使用仅有业务表 DML 权限的 ani_governance_runtime 账号。
# 迁移和 admin init 使用上面的 MIGRATION_DSN，不能让服务容器承担 DDL。
RUNTIME_DSN="postgres://ani_governance_runtime:<runtime-password>@postgres.ani-infra.svc.cluster.local:5432/ani_governance?sslmode=disable"
cat > zz-secrets.yaml <<EOF
data:
  database:
    source: ${RUNTIME_DSN}
  redis:
    password: ${REDIS_URI_PASSWORD}
server:
  asynq:
    uri: redis://:${REDIS_URI_PASSWORD}@redis.ani-infra.svc.cluster.local:6379/1
authn:
  jwt:
    private_key: |
$(sed 's/^/      /' jwt-private-key.pem)
    public_key: |
$(sed 's/^/      /' jwt-public-key.pem)
EOF
kubectl -n ani-system create secret generic ani-governance-config \
  --from-file=zz-secrets.yaml \
  --dry-run=client -o yaml | kubectl apply -f -
```

这里的 `zz-secrets.yaml` 以 `zz-` 开头，是为了让 Kratos 文件配置在基础配置之后
覆盖开发环境示例密钥和默认连接信息。这个文件不应加入 Git。

## 只使用 Helm 安装

确认当前 kubeconfig 指向新集群。Helm 默认使用当前 kubeconfig context，不要切换到旧环境：

```bash
kubectl config current-context
kubectl get nodes
```

先确认三个业务镜像已经推送到当前集群可访问的镜像仓库，并使用不可变摘要或明确标签：

```bash
helm lint charts/ani-governance \
  --set image.tag=<server-tag> \
  --set adminImage.tag=<admin-tag> \
  --set atlasImage.tag=<atlas-tag>

helm upgrade --install ani-governance charts/ani-governance \
  --namespace ani-system --create-namespace \
  --set-string image.digest=sha256:6153868f5e3f16e79026f7050a13aaebd6a265c3fe5bde4eb6a6cc1d11d83133 \
  --set-string adminImage.digest=sha256:2bc561fbb3d7eb5d857492c3b42d80aa9aa08f0b22a8649292b321d47591dcc8 \
  --set-string atlasImage.digest=sha256:8a53d310007316dd33a6a3382887af5b3be5a18651bf1bcb5a43152c4ed0ced2 \
  --wait --timeout 15m
```

上面的摘要是本次新集群验证使用的镜像；换版本时必须同时替换三个摘要。也可以使用明确的
镜像 tag，但不能省略 `image`、`adminImage` 和 `atlasImage` 的版本。

Helm 会按以下顺序执行：

1. pre-install/pre-upgrade 的 Atlas Job 应用 `migrations/`，服务配置始终保持
   `data.database.migrate=false`。
2. Gov BFF Deployment 启动 REST `7788` 和 SSE `7789`，连接独立 Redis。
3. pre-install/pre-upgrade 的 admin Job 执行 `admin init`，再创建/更新服务 Deployment；已初始化的数据库保持
   原管理员和原数据不变。

检查结果：

```bash
kubectl -n ani-system get deploy,pod,svc,statefulset,job -l app.kubernetes.io/instance=ani-governance
kubectl -n ani-system logs job/ani-governance-migrate
kubectl -n ani-system logs job/ani-governance-admin-init
kubectl -n ani-system port-forward svc/ani-governance 7788:7788
```

启用两个下游客户端时，三份证书必须使用同一个 `ani-internal` CA：

```bash
helm upgrade --install ani-governance charts/ani-governance \
  --namespace ani-system --create-namespace \
  --set downstream.model.enabled=true \
  --set downstream.model.address=ani-model-grpc.ani-system.svc.cluster.local:19090 \
  --set downstream.model.serverName=ani-model.ani.internal \
  --set downstream.model.caSecret=ani-model-server-tls \
  --set downstream.inference.enabled=true \
  --set downstream.inference.address=ani-inference-grpc.ani-system.svc.cluster.local:19090 \
  --set downstream.inference.serverName=ani-inference.ani.internal \
  --set downstream.inference.caSecret=ani-inference-server-tls \
  --set certificate.enabled=true \
  --wait --timeout 15m
```

`certificate.enabled=true` 会在 `ani-system` 请求 DNS SAN 为
`ani-governance` 的 Gov 客户端证书，不混入其他 DNS、URI、IP 或 email SAN；Model 和 Inference
接收端必须登记该客户端身份和允许的 RPC，双方身份兼容需要独立联调验收。服务端证书的字面 DNS SAN
必须匹配各自受管的 `serverName`。
服务端与客户端都要求 TLS 1.3 和 mTLS。没有完整证书配置时，客户端保持关闭或服务启动失败，不能
退化成明文连接。

Accelerator 的存量客户端仍使用独立 URI 身份，启用时应通过
`downstream.accelerator.clientCertificateSecret` 指定其证书 Secret，不能复用上面的 DNS 证书。

Gov 的 Model BFF 负责模型、模型版本、ModelScope 导入任务和任务查询；Inference BFF 负责
CPU/KServe 服务的创建、查询、列表、操作查询和删除。请求中的 tenant/actor 不会透传为权限
依据，而是从 Gov 登录主体解析后由 mTLS 服务间调用重新注入。

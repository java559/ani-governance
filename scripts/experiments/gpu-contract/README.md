# GPU 软件合同实验已退役

旧入口以独立测试 owner 和完整 Plan/charges 模拟 Inference GPU 业务，不支持新的单执行快照和完整资源释放合同，源码可从提交 `370543b0a484b6139fe6bf9394ca4b9a9b0d12f3` 查阅。本期 Inference GPU 业务没有接入。

当前纵向回归使用真实 Resource 接收器、PostgreSQL 和 mTLS：`app/admin/service/internal/service/instance_pg_test.go`、`app/admin/service/internal/server/instance_joint_test.go`。仅 Kubernetes 等外部依赖可替换；相关跨仓二进制与独占数据库须显式提供。它们不代替真实 GPU、CUDA、物理释放与占退额的集群验证。

账本并发、租户边界、释放事件幂等和 ACK 丢失回归继续由 `internal/data`、`internal/server` 和 `internal/service` 的正式测试维护，入口为仓库 `make verify-ci`。

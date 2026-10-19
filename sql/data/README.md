# 版本数据脚本

`20260922_quota_catalog_backfill.sql` 是配额目录和旧行回填输入。升级顺序必须是 `migrations/20260922190000_quota_expand.sql` → 本脚本 → `migrations/20260922190100_quota_constraints.sql` → 后续版本迁移。先检查旧数据，再按原脚本回填；失败时停止，不跳过约束。

Atlas 结构迁移不会自动读取本目录。执行者需按 [部署流程](../../docs/deployment.md) 显式安排这个数据步骤；`app/admin/service/schema.sql` 不能替代迁移链。本批只迁址，SQL 字节和历史迁移均未改。

Image catalog: `20260930_image_permissions.sql` is an explicit, transactional
catalog import after the current OpenAPI sync dry run and apply. Use the
migration/operator identity and `psql -v ON_ERROR_STOP=1 -1 -f`; do not run it at
server startup. It requires exactly one active IMAGE API for each of 11 routes,
rejects unrelated permission links and preserves disabled permissions. It
creates no role grants or subscriptions. Assign an Image reader only the four
`image:space:get`, `image:credential:get`, `image:registration:get` and
`image:registration:list` permissions. Publishing roles receive individually
reviewed write permissions; API keys use their existing tenant role binding.
Enable IMAGE on an approved plan separately, then refresh current Casbin policy.
The isolated `scripts/image-joint-integration` gate proves import idempotency,
no implicit grants, reader/publisher separation, revocation and module denial.

`20261015_retire_stale_model_search_api.sql` retires the historical
`POST /api/v1/models:search-external` catalog row after the Model BFF has been
synced. It keeps the row for audit history and marks it `OFF`; the active
search endpoint is `GET /api/v1/models:search`.

Run `20261015_retire_legacy_model_inference_manage_links.sql` before the
explicit Model/Inference permission registration scripts when upgrading an
installation that still has the old umbrella `*:service:manage` API links.
It removes only those stale API relations and leaves permission points,
roles, plans and quotas unchanged.

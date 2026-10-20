#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
# Use a disposable service PostgreSQL supplied by CI. This is explicit test
# provisioning, never an application startup path.
: "${PGHOST:?}" "${PGPORT:?}" "${PGUSER:?}"
psql -X -v ON_ERROR_STOP=1 -d postgres -f scripts/ci/sql/create-test-database.sql
for source in migrations/20260921134442_initial.sql migrations/20260922190000_quota_expand.sql sql/data/20260922_quota_catalog_backfill.sql migrations/20260922190100_quota_constraints.sql migrations/20260923130000_gpu_sqlc.sql migrations/20260923130100_gpu_no_rls_catalog.sql migrations/20260923130200_gpu_sync_ref.sql migrations/20260923143455_drop_geo_location.sql migrations/20261019090000_instance_contract_version.sql; do
  PGUSER=gov_acc_migrate psql -X -v ON_ERROR_STOP=1 -d gov_acc_data -f "$source"
done
PGUSER=gov_acc_migrate psql -X -v ON_ERROR_STOP=1 -d gov_acc_data -v migrate_role=gov_acc_migrate -v runtime_role=gov_acc_runtime -f scripts/ops/sql/grant-runtime-role.sql
PGUSER=gov_acc_runtime psql -X -v ON_ERROR_STOP=1 -d gov_acc_data -f scripts/ops/sql/audit-runtime-privileges.sql
for database_name in gov_acc_guard gov_acc_race_data gov_acc_race_service; do
  createdb -O gov_acc_migrate -T gov_acc_data "$database_name"
  psql -X -v ON_ERROR_STOP=1 -d postgres -v database_name="$database_name" -v runtime_role=gov_acc_runtime -f scripts/ops/sql/restrict-database.sql
done

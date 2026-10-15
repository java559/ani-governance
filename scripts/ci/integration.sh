#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${ANI_TEST_DATABASE_DSN:?restricted task PostgreSQL DSN required}"
: "${ANI_TEST_GUARD_DATABASE_DSN:?exclusive guard PostgreSQL DSN required}"
: "${ANI_TEST_RACE_DATA_DSN:?exclusive race data PostgreSQL DSN required}"
: "${ANI_TEST_RACE_SERVICE_DSN:?exclusive race service PostgreSQL DSN required}"
test "${ANI_TEST_DATABASE_EXCLUSIVE:-}" = 1 || { echo 'ANI_TEST_DATABASE_EXCLUSIVE=1 required' >&2; exit 1; }
test "${ANI_TEST_GUARD_DATABASE_EXCLUSIVE:-}" = 1 || { echo 'ANI_TEST_GUARD_DATABASE_EXCLUSIVE=1 required' >&2; exit 1; }
evidence=${ANI_CI_EVIDENCE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/ani-ci-integration.XXXXXXXX")}
mkdir -p "$evidence"
log="$evidence/go-tests.jsonl"
: > "$log"
go test -json -tags integration -count=1 -run '^(TestTenantMutationGuard|TestTenantMutationGuardAccessKey|TestPositionDataScopeGuard|TestOrgUnitPathExpansionTenantBounded|TestTenantGuardDeleteOneCreate)$' ./app/admin/service/internal/data/ent >> "$log"
go test -json -tags quota_pg -count=1 -timeout=20m ./app/admin/service/internal/data >> "$log"
go test -json -tags quota_pg -count=1 -timeout=10m ./app/admin/service/internal/server >> "$log"
go test -json -tags quota_pg -count=1 -timeout=10m ./app/admin/service/internal/service >> "$log"
python3 scripts/ci/assert-test-results.py --required tests/manifests/critical-tests.json --log "$log" --summary "$evidence/critical-results.json"
echo 'race: data (three selected tests)' >&2
data_race_tests='^(TestQuotaPostgresConcurrentLimit|TestQuotaPostgresCancelClaimRace|TestQuotaPostgresLeaseGenerationGuard)$'
data_race_log="$evidence/race-data-tests.jsonl"
ANI_TEST_DATABASE_DSN="$ANI_TEST_RACE_DATA_DSN" go test -json -race -tags quota_pg -count=1 -timeout=20m -run "$data_race_tests" ./app/admin/service/internal/data > "$data_race_log"
python3 scripts/ci/assert-test-results.py --required tests/manifests/critical-tests.json --test-regex "$data_race_tests" --log "$data_race_log" --summary "$evidence/race-data-results.json"
echo 'race: service (one selected test)' >&2
service_race_tests='^TestQuotaDispatchRetryAfterLostAck$'
service_race_log="$evidence/race-service-tests.jsonl"
ANI_TEST_DATABASE_DSN="$ANI_TEST_RACE_SERVICE_DSN" go test -json -race -tags quota_pg -count=1 -timeout=20m -run "$service_race_tests" ./app/admin/service/internal/service > "$service_race_log"
python3 scripts/ci/assert-test-results.py --required tests/manifests/critical-tests.json --test-regex "$service_race_tests" --log "$service_race_log" --summary "$evidence/race-service-results.json"

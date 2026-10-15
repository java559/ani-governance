#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
bash scripts/ci/check-format.sh
python3 scripts/ci/check_quota_storage.py --repo .
python3 scripts/ci/check-layout.py --repo .
python3 -m unittest discover -s scripts/tests -p 'test_*.py'
make check-repo-entrypoints
go build -o "${TMPDIR:-/tmp}/ani-server-$$" ./app/admin/service/cmd/server
go build -o "${TMPDIR:-/tmp}/ani-admin-$$" ./app/admin/service/cmd/admin
go test -count=1 ./pkg/...
go test -count=1 ./sql/bootstrap
bash scripts/ci/audit.sh

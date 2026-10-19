#!/usr/bin/env bash
# Build the real upstream subprocesses from the versions in this module graph.
# Database provisioning is exclusive CI setup, never application startup.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${ANI_CI_JOINT_FIXTURE_DIR:?absolute empty fixture directory required}"
: "${ANI_CI_JOINT_ENV_FILE:?environment output file required}"
: "${PGHOST:?}" "${PGPORT:?}" "${PGUSER:?}"
test "${ANI_TEST_DATABASE_EXCLUSIVE:-}" = 1 || { echo 'exclusive CI PostgreSQL service required' >&2; exit 2; }
case "$ANI_CI_JOINT_FIXTURE_DIR" in /*) ;; *) echo 'absolute fixture directory required' >&2; exit 2;; esac
umask 077
mkdir "$ANI_CI_JOINT_FIXTURE_DIR"
fixture=$(realpath "$ANI_CI_JOINT_FIXTURE_DIR")
export GOWORK=off
for entry in 'accelerator acc ./tests/contract' 'resource resource ./internal/data/instance'; do
  read -r service name package <<< "$entry"
  module="github.com/zhangzhe-ctrl/ani-$service-service"
  go list -m -json "$module" > "$fixture/$name-module.json"
  version=$(python3 - "$fixture/$name-module.json" <<'PY'
import json,sys
m=json.load(open(sys.argv[1]))
if m.get('Replace') or not m.get('Version'):
    raise SystemExit('published upstream module required; replace is forbidden')
print(m['Version'])
PY
)
  go mod download -json "$module@$version" > "$fixture/$name-origin.json"
  source_dir=$(python3 - "$fixture/$name-origin.json" "$version" <<'PY'
import json,re,sys
m=json.load(open(sys.argv[1]))
suffix=re.search(r'-([0-9a-f]{12})$',sys.argv[2])
origin=m.get('Origin',{}).get('Hash','')
if m.get('Error') or not suffix or not origin.startswith(suffix.group(1)) or not m.get('Sum'):
    raise SystemExit('exact upstream module origin/checksum required')
print(m['Dir'])
PY
)
  python3 - "$fixture/$name-origin.json" <<'PY'
import json,sys
m=json.load(open(sys.argv[1]))
print(json.dumps({k:m[k] for k in ('Path','Version','Sum','Origin')}))
PY
  tags=()
  if test "$name" = resource; then tags=(-tags=instanceintegration); fi
  (cd "$source_dir" && go test -mod=readonly -c "${tags[@]}" -o "$fixture/$name.test" "$package")
  if test "$name" = acc; then acc_source=$source_dir; fi
done

# A fresh database belongs to this disposable PostgreSQL service. Accelerator
# runs with the same restricted DML role as Gov; Resource's helper creates and
# removes its own UUID database and owner/runtime roles using the admin DSN.
createdb -O gov_acc_migrate gov_acc_joint_acc
psql -X -v ON_ERROR_STOP=1 -d postgres -c 'REVOKE ALL ON DATABASE gov_acc_joint_acc FROM PUBLIC; GRANT CONNECT ON DATABASE gov_acc_joint_acc TO gov_acc_runtime;'
for migration in "$acc_source"/migrations/*.sql; do
  PGUSER=gov_acc_migrate psql -X -v ON_ERROR_STOP=1 -d gov_acc_joint_acc -f "$migration"
done
PGUSER=gov_acc_migrate psql -X -v ON_ERROR_STOP=1 -d gov_acc_joint_acc -c 'GRANT USAGE ON SCHEMA accelerator TO gov_acc_runtime; GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA accelerator TO gov_acc_runtime;'
python3 - "$fixture" <<'PY'
import os,pathlib,urllib.parse,sys
p=pathlib.Path(sys.argv[1]); host=os.environ['PGHOST']; port=os.environ['PGPORT']
def dsn(user,db):
    return 'postgresql://'+urllib.parse.quote(user,safe='')+'@'+host+':'+port+'/'+db+'?sslmode=disable'
(p/'acc-runtime.dsn').write_text(dsn('gov_acc_runtime','gov_acc_joint_acc'))
(p/'resource-admin.dsn').write_text(dsn(os.environ['PGUSER'],'postgres'))
PY
cat > "$ANI_CI_JOINT_ENV_FILE" <<EOF
ACC_INSTANCE_TEST_BINARY=$fixture/acc.test
RESOURCE_INSTANCE_TEST_BINARY=$fixture/resource.test
ACC_TEST_DSN_FILE=$fixture/acc-runtime.dsn
INSTANCE_TEST_ADMIN_DSN_FILE=$fixture/resource-admin.dsn
INSTANCE_JOINT_LIFECYCLE=1
EOF
sha256sum "$fixture/acc.test" "$fixture/resource.test" > "$fixture/binaries.sha256"

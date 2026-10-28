#!/bin/bash
# Apply the ModelDev execution-detail environment prerequisites.
#
# Four changes must exist before the detail read routes work. Three of them live
# outside the Go source, so a fresh environment has to reproduce them or the
# routes fail closed. Every step is idempotent and separately switchable.
#
#   images   set the detail-era ani-modeldev / ani-governance images
#   apis     register the four new (path, method) pairs in the API directory
#   grant    link those APIs to modeldev:execution:get / modeldev:execution:logs
#   rbac     let ani-modeldev-control list Events in the tenant namespace
#
# Usage:
#   scripts/ops/apply-modeldev-execution-detail.sh [options] <step>...
#
# Options (nothing is defaulted: pass the target explicitly so this cannot act
# on whichever cluster or namespace happens to be current):
#   -n, --namespace            namespace holding ani-modeldev / ani-governance
#   -t, --tenant-namespace     tenant namespace that runs the executions
#   -p, --postgres-namespace   namespace holding the governance StatefulSet
#   -s, --postgres-statefulset Postgres StatefulSet name
#   -r, --migrate-role         migration-owner role that owns sys_apis
#   -d, --database             governance database name
#       --admin-image          governance-admin image (carries /app/bin/admin)
#       --modeldev-image       ani-modeldev-service image reference
#       --governance-image     ani-governance image reference
#   -h, --help
#
# The admin CLI is a one-shot binary, not a Deployment: it reads ANI_DATABASE_DSN
# and exits. The apis step therefore starts its own short-lived Pod with the
# requested admin image instead of exec-ing into a running workload. It needs a
# write-capable DSN in ADMIN_DATABASE_DSN (the migration-owner role), because it
# inserts into sys_apis.
#
# The grant step needs the migration-owner role because the runtime role cannot
# read sys_apis or write sys_permission_apis.
set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PERMISSION_SQL="${SCRIPT_DIR}/sql/register-modeldev-execution-detail-permissions.sql"

NAMESPACE=""; TENANT_NAMESPACE=""; POSTGRES_NAMESPACE=""; POSTGRES_STATEFULSET=""
MIGRATE_ROLE=""; GOVERNANCE_DB=""
ADMIN_IMAGE=""; MODELDEV_IMAGE=""; GOVERNANCE_IMAGE=""

usage() {
  sed -n '3,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

fail() { echo "error: $*" >&2; exit 2; }
need() { [ -n "${2:-}" ] || fail "$1 is required for step '$3'"; }

STEPS=()
while [ $# -gt 0 ]; do
  case "$1" in
    -n|--namespace) NAMESPACE="$2"; shift 2 ;;
    -t|--tenant-namespace) TENANT_NAMESPACE="$2"; shift 2 ;;
    -p|--postgres-namespace) POSTGRES_NAMESPACE="$2"; shift 2 ;;
    -s|--postgres-statefulset) POSTGRES_STATEFULSET="$2"; shift 2 ;;
    -r|--migrate-role) MIGRATE_ROLE="$2"; shift 2 ;;
    -d|--database) GOVERNANCE_DB="$2"; shift 2 ;;
    --admin-image) ADMIN_IMAGE="$2"; shift 2 ;;
    --modeldev-image) MODELDEV_IMAGE="$2"; shift 2 ;;
    --governance-image) GOVERNANCE_IMAGE="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    -*) fail "unknown option: $1" ;;
    # Steps and options may be interleaved in any order.
    *) STEPS+=("$1"); shift ;;
  esac
done

set_images() {
  need --namespace "$NAMESPACE" images
  need --modeldev-image "$MODELDEV_IMAGE" images
  need --governance-image "$GOVERNANCE_IMAGE" images
  kubectl -n "$NAMESPACE" set image deploy/ani-modeldev modeldev="$MODELDEV_IMAGE"
  kubectl -n "$NAMESPACE" set image deploy/ani-governance governance="$GOVERNANCE_IMAGE"
  kubectl -n "$NAMESPACE" annotate deploy/ani-modeldev deploy/ani-governance \
    kubernetes.io/change-cause="modeldev execution detail" --overwrite
  kubectl -n "$NAMESPACE" rollout status deploy/ani-modeldev --timeout=300s
  kubectl -n "$NAMESPACE" rollout status deploy/ani-governance --timeout=300s
}

# Runs /app/bin/admin sync-apis in a throwaway Pod. --dry-run first, and the
# dry-run output must mention the detail routes: an admin image older than
# the routes would otherwise report success while adding nothing.
#
# The DSN carries a password, so it is read from ADMIN_DATABASE_DSN in the
# environment and delivered through a temporary Secret: never as an argument,
# where it would show up in the process list.
sync_apis() {
  need --namespace "$NAMESPACE" apis
  need --admin-image "$ADMIN_IMAGE" apis
  need ADMIN_DATABASE_DSN "${ADMIN_DATABASE_DSN:-}" apis

  local pod="modeldev-detail-sync-apis-$$"
  local secret="modeldev-detail-sync-apis-$$"
  cleanup_admin() {
    kubectl -n "$NAMESPACE" delete pod "$pod" --ignore-not-found >/dev/null 2>&1
    kubectl -n "$NAMESPACE" delete secret "$secret" --ignore-not-found >/dev/null 2>&1
    rm -f "/tmp/sync-apis-dry-run.$$"
  }
  trap cleanup_admin RETURN

  kubectl -n "$NAMESPACE" create secret generic "$secret" \
    --from-literal=ANI_DATABASE_DSN="$ADMIN_DATABASE_DSN" >/dev/null
  kubectl -n "$NAMESPACE" run "$pod" --image="$ADMIN_IMAGE" --restart=Never \
    --command -- /app/bin/admin sync-apis --dry-run \
    --overrides="{\"apiVersion\":\"v1\",\"spec\":{\"containers\":[{\"name\":\"$pod\",\"image\":\"$ADMIN_IMAGE\",\"command\":[\"/app/bin/admin\"],\"args\":[\"sync-apis\",\"--dry-run\"],\"envFrom\":[{\"secretRef\":{\"name\":\"$secret\"}}]}]}}" >/dev/null
  kubectl -n "$NAMESPACE" wait --for=condition=Ready "pod/$pod" --timeout=120s >/dev/null 2>&1 || true
  kubectl -n "$NAMESPACE" wait --for=jsonpath='{.status.phase}'=Succeeded "pod/$pod" --timeout=180s >/dev/null \
    || kubectl -n "$NAMESPACE" wait --for=jsonpath='{.status.phase}'=Failed "pod/$pod" --timeout=30s >/dev/null 2>&1 || true
  kubectl -n "$NAMESPACE" logs "$pod" > "/tmp/sync-apis-dry-run.$$" 2>&1 \
    || fail "could not read the admin dry-run log"
  cat "/tmp/sync-apis-dry-run.$$"
  grep -qE '/(pods|events|resource-metrics|logs/stream)' "/tmp/sync-apis-dry-run.$$" \
    || fail "dry-run did not report the detail routes; the admin image predates this change"
  grep -qiE '^PASS|dry run' "/tmp/sync-apis-dry-run.$$" \
    || fail "dry-run did not finish cleanly; review the log above before applying"

  local applied="modeldev-detail-apply-apis-$$"
  kubectl -n "$NAMESPACE" run "$applied" --image="$ADMIN_IMAGE" --restart=Never \
    --command -- /app/bin/admin sync-apis \
    --overrides="{\"apiVersion\":\"v1\",\"spec\":{\"containers\":[{\"name\":\"$applied\",\"image\":\"$ADMIN_IMAGE\",\"command\":[\"/app/bin/admin\"],\"args\":[\"sync-apis\"],\"envFrom\":[{\"secretRef\":{\"name\":\"$secret\"}}]}]}}" >/dev/null
  kubectl -n "$NAMESPACE" wait --for=jsonpath='{.status.phase}'=Succeeded "pod/$applied" --timeout=180s >/dev/null \
    || fail "admin sync-apis did not succeed; inspect pod/$applied"
  kubectl -n "$NAMESPACE" logs "$applied" | tail -5
  kubectl -n "$NAMESPACE" delete pod "$applied" --ignore-not-found >/dev/null 2>&1
  echo "API catalog synchronized. Permissions are unchanged; run the grant step next."
}

grant_apis() {
  need --postgres-namespace "$POSTGRES_NAMESPACE" grant
  need --postgres-statefulset "$POSTGRES_STATEFULSET" grant
  need --migrate-role "$MIGRATE_ROLE" grant
  need --database "$GOVERNANCE_DB" grant
  [ -f "$PERMISSION_SQL" ] || fail "missing $PERMISSION_SQL"
  kubectl -n "$POSTGRES_NAMESPACE" exec -i "statefulset/$POSTGRES_STATEFULSET" -- \
    psql -U "$MIGRATE_ROLE" -d "$GOVERNANCE_DB" -v ON_ERROR_STOP=1 -f - < "$PERMISSION_SQL"
  echo "Restart the governance Pod so the in-memory Casbin policy reloads."
}

# role/ani-modeldev-v21-control is created per tenant namespace by the ModelDev
# runtime. Adding verbs is the only change this feature needs there.
patch_rbac() {
  need --namespace "$NAMESPACE" rbac
  need --tenant-namespace "$TENANT_NAMESPACE" rbac
  local sa="system:serviceaccount:${NAMESPACE}:modeldev-control"
  local role="ani-modeldev-v21-control"
  if kubectl auth can-i list events -n "$TENANT_NAMESPACE" --as="$sa" | grep -qx yes; then
    echo "events already readable in $TENANT_NAMESPACE"
    return 0
  fi
  kubectl -n "$TENANT_NAMESPACE" get role "$role" >/dev/null 2>&1 \
    || fail "role/$role not found in $TENANT_NAMESPACE; it is created by the ModelDev runtime"
  kubectl -n "$TENANT_NAMESPACE" patch role "$role" --type=json -p='[
    {"op":"add","path":"/rules/-","value":{"apiGroups":[""],"resources":["events"],"verbs":["get","list"]}}
  ]'
  kubectl auth can-i list events -n "$TENANT_NAMESPACE" --as="$sa" | grep -qx yes \
    || fail "events still unreadable for $sa in $TENANT_NAMESPACE"
}

[ ${#STEPS[@]} -gt 0 ] || { usage >&2; fail "name at least one step: images apis grant rbac"; }
for step in "${STEPS[@]}"; do
  echo "=== $step ==="
  case "$step" in
    images) set_images ;;
    apis) sync_apis ;;
    grant) grant_apis ;;
    rbac) patch_rbac ;;
    *) fail "unknown step: $step" ;;
  esac || exit $?
done

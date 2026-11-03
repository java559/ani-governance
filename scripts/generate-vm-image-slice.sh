#!/usr/bin/env bash
set -Eeuo pipefail
# Tenant VM image API slice. Regenerates only the image contract files plus the
# OpenAPI catalog, so the shared output root is never cleaned or rewritten.
#
# Requirements: buf v1.60.0 via $BUF, a task-private $TMPDIR, Go toolchain and the
# localized redact plugin (built here by scripts/build-redact-plugin.sh).
cd "$(dirname "$0")/.."
BUF=${BUF:?set BUF to a verified buf v1.60.0 executable}
test "$("$BUF" --version)" = 1.60.0
test "$(go version -m "$BUF" | awk '$1 == "mod" {print $2 "@" $3; exit}')" = github.com/bufbuild/buf@v1.60.0
stage=$(mktemp -d "${TMPDIR:?set a task-private TMPDIR}/vm-image-api.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT

cp -a api "$stage/api"
cp -a pkg "$stage/pkg"
cp go.mod go.sum "$stage/"
mkdir -p "$stage/app/admin/service/cmd/server/assets" "$stage/scripts" "$stage/tools/bin"
cp scripts/build-redact-plugin.sh scripts/finalize-aksk-openapi.py "$stage/scripts/"

(cd "$stage" && bash scripts/build-redact-plugin.sh)
test -x "$stage/tools/bin/protoc-gen-go-redact"

(cd "$stage/api" && "$BUF" build && "$BUF" generate --template buf.vm-image.gen.yaml && "$BUF" generate --template buf.admin.openapi.gen.yaml)
python3 "$stage/scripts/finalize-aksk-openapi.py"

for output in vm/service/v1/image.pb.go vm/service/v1/image.pb.redact.go \
              admin/service/v1/i_vm_image.pb.go admin/service/v1/i_vm_image_grpc.pb.go admin/service/v1/i_vm_image_http.pb.go; do
  test -f "$stage/api/gen/go/$output" || { echo "missing generated output: $output" >&2; exit 1; }
  mkdir -p "api/gen/go/$(dirname "$output")"
  cp "$stage/api/gen/go/$output" "api/gen/go/$output"
done

# The redact and message layers must also stay byte-identical for the existing VM
# contract; copying them back from the same template proves the slice did not
# change them.
for output in vm/service/v1/vm.pb.go vm/service/v1/vm.pb.redact.go identity/service/v1/module.pb.go; do
  cmp -s "$stage/api/gen/go/$output" "api/gen/go/$output" || { echo "slice drifted an existing output: $output" >&2; exit 1; }
done

cp "$stage/app/admin/service/cmd/server/assets/openapi.yaml" app/admin/service/cmd/server/assets/openapi.yaml
echo "VM image API slice generated"

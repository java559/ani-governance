#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/.."
BUF=${BUF:?verified buf v1.60.0 required}
[[ $("$BUF" --version) == 1.60.0 ]]
stage=$(mktemp -d "${TMPDIR:?task-owned TMPDIR}/instance-gov-api.XXXXXX")
trap 'rm -rf -- "$stage"' EXIT
cp -a api "$stage/api"
mkdir -p "$stage/app/admin/service/cmd/server/assets" "$stage/scripts"
cp scripts/finalize-aksk-openapi.py "$stage/scripts/"
(cd "$stage/api" && "$BUF" build && "$BUF" generate --template buf.instance-slice.yaml && "$BUF" generate --template buf.admin.openapi.gen.yaml)
python3 "$stage/scripts/finalize-aksk-openapi.py"
for output in instance/service/v1/instance.pb.go admin/service/v1/i_instance.pb.go admin/service/v1/i_instance_grpc.pb.go admin/service/v1/i_instance_http.pb.go identity/service/v1/module.pb.go; do
 [[ -f $stage/api/gen/go/$output ]]
 mkdir -p "api/gen/go/$(dirname "$output")"
 cp "$stage/api/gen/go/$output" "api/gen/go/$output"
done
cp "$stage/app/admin/service/cmd/server/assets/openapi.yaml" app/admin/service/cmd/server/assets/openapi.yaml

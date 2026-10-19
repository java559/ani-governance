#!/usr/bin/env bash
set -euo pipefail
: "${GOBIN:?private GOBIN required}"
mkdir -p "$GOBIN"
case "${1:-}" in
  scanners)
    GOWORK=off go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
    GOWORK=off go install github.com/zricethezav/gitleaks/v8@v8.30.1
    ;;
  generation)
    # Build Ent with this repository's locked module graph, matching go run.
    # Prebuilding avoids including cold CLI compilation in gow's 2m deadline.
    test "$(GOWORK=off go list -m -f '{{.Version}}' entgo.io/ent)" = v0.14.6
    GOWORK=off go install -mod=readonly entgo.io/ent/cmd/ent
    GOWORK=off go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
    GOWORK=off go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
    GOWORK=off go install github.com/go-kratos/kratos/cmd/protoc-gen-go-http/v2@v2.0.0-20260404020628-f149714c1d54
    GOWORK=off go install github.com/go-kratos/kratos/cmd/protoc-gen-go-errors/v2@v2.0.0-20260404020628-f149714c1d54
    GOWORK=off go install github.com/google/gnostic/cmd/protoc-gen-openapi@v0.7.1
    GOWORK=off go install github.com/envoyproxy/protoc-gen-validate@v1.3.3
    GOWORK=off go install github.com/bufbuild/buf/cmd/buf@v1.60.0
    # Parked templates retain these exact older module identities in preflight.
    # Cache them without replacing the active generator binaries.
    GOWORK=off go mod download google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.0
    GOWORK=off go mod download github.com/go-kratos/kratos/cmd/protoc-gen-go-http/v2@v2.0.0-20251205160234-b9fab9a5a5ab
    work=$(mktemp -d "${TMPDIR:-/tmp}/ani-protoc.XXXXXXXX")
    trap 'rm -rf "$work"' EXIT
    curl --http1.1 --fail --location --silent --show-error \
      'https://github.com/protocolbuffers/protobuf/releases/download/v29.3/protoc-29.3-linux-x86_64.zip' \
      -o "$work/protoc.zip"
    printf '%s  %s\n' '3e866620c5be27664f3d2fa2d656b5f3e09b5152b42f1bedbf427b333e90021a' "$work/protoc.zip" | sha256sum -c -
    unzip -q "$work/protoc.zip" 'bin/protoc' 'include/google/protobuf/*' -d "$work"
    printf '%s  %s\n' '5ae94ad986e83f0b52bd8139e036cbf94ddbba3ad348be8e4baf44447d7e19a4' "$work/bin/protoc" | sha256sum -c -
    install -m 0755 "$work/bin/protoc" "$GOBIN/protoc"
    mkdir -p "$GOBIN/../include"
    cp -a "$work/include/." "$GOBIN/../include/"
    ;;
  *) echo 'usage: install-tools.sh scanners|generation' >&2; exit 2 ;;
esac

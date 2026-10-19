#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
git rev-parse --verify origin/main >/dev/null || { echo 'origin/main is required for changed-file format check' >&2; exit 1; }
base=$(git merge-base HEAD origin/main)
if test "$base" = "$(git rev-parse HEAD)"; then base=$(git rev-parse HEAD^); fi
# Check the current tree so pre-commit validation covers staged/unstaged edits
# and excludes files deleted by this change. CI with a clean tree is identical.
mapfile -d '' -t sources < <(git diff --name-only -z --diff-filter=ACMR "$base" -- '*.go')
unformatted=''
if ((${#sources[@]})); then unformatted=$(gofmt -l "${sources[@]}"); fi
if [[ -n "$unformatted" ]]; then
  printf '%s\n' "$unformatted" >&2
  exit 1
fi
git diff --check "$base" --

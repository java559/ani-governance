#!/usr/bin/env python3
"""Snapshot and compare every active API/Ent managed root and generation input."""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import posixpath
import re
import shutil
import subprocess
import sys


ENT_ROOT = "app/admin/service/internal/data/ent"
ENT_SCHEMA_OUTPUT = "app/admin/service/schema.sql"
REQUIRED_API_ROOTS = {
    "api/gen/go",
    "api/quota/gen/go",
    "pkg/localdeps/go-crud/api/gen/go",
    "pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact",
    "pkg/localdeps/kratos-bootstrap/api/gen/go",
    "app/admin/service/cmd/server/assets",
}
INPUT_FILES = (
    "go.mod", "go.sum", "scripts/build-redact-plugin.sh",
    "scripts/finalize-aksk-openapi.py", "tools/config/pgv-scope.json",
    "tools/config/tool-lock.json",
    "app/admin/service/cmd/schema/sandbox_parent_fence.sql",
)
TEMP_PREFIXES = ("_agent/", ".artifacts/", ".scratch/")


def generation_source(name: str) -> bool:
    """Only copy new source and managed output paths into the generation checkout."""
    path = pathlib.PurePosixPath(name)
    if name.startswith(TEMP_PREFIXES) or path.name.startswith("."):
        return False
    if name.startswith("api/protos/") or name.startswith("api/localdeps/"):
        return path.suffix == ".proto"
    if name.startswith("api/quota/"):
        return path.suffix == ".go" or path.name in ("go.mod", "go.sum")
    if name.startswith("api/gen/go/"):
        return path.suffix == ".go"
    if name.startswith("app/admin/service/"):
        return path.suffix == ".go" or name in (
            "app/admin/service/cmd/server/assets/openapi.yaml",
            "app/admin/service/cmd/schema/sandbox_parent_fence.sql",
        )
    if name.startswith("pkg/") or name.startswith("tools/localdeps/"):
        return path.suffix in (".go", ".proto", ".tmpl")
    if name.startswith("scripts/ci/") or name.startswith("scripts/tests/"):
        return path.suffix in (".sh", ".py")
    return name.startswith("api/buf") and name.endswith(".gen.yaml")


def overlay_worktree(repo: pathlib.Path, target: pathlib.Path) -> tuple[int, int]:
    """Overlay an archived HEAD with tracked edits and relevant untracked files."""
    if not target.is_dir():
        raise ValueError(f"generation target missing: {target}")
    patch = subprocess.run(["git", "-C", str(repo), "diff", "--binary", "HEAD", "--"],
                           check=True, capture_output=True).stdout
    if patch:
        subprocess.run(["git", "apply", "--binary", "-"], cwd=target,
                       input=patch, check=True, capture_output=True)
    raw = subprocess.run(["git", "-C", str(repo), "ls-files", "--others",
                          "--exclude-standard", "-z"], check=True, capture_output=True).stdout
    copied = 0
    for encoded in raw.split(b"\0"):
        if not encoded:
            continue
        name = encoded.decode("utf-8", errors="surrogateescape")
        relative = pathlib.PurePosixPath(name)
        if relative.is_absolute() or ".." in relative.parts:
            raise ValueError(f"unsafe untracked path: {name}")
        if not generation_source(name):
            continue
        source = repo / name
        if source.is_symlink() or not source.is_file():
            raise ValueError(f"unsupported untracked generation input: {name}")
        dest = target / name
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, dest)
        copied += 1
    for path in target.rglob("*"):
        if path.is_symlink():
            raise ValueError(f"symlink in generation checkout: {path.relative_to(target)}")
    return len(patch), copied


def managed_roots(repo: pathlib.Path) -> list[str]:
    source = (repo / "tools/localdeps/gow/internal/buf/cmd.go").read_text()
    block = re.search(r"var activeGenConfigs = \[\]string\{(.*?)\n\}", source, re.S)
    if not block:
        raise ValueError("activeGenConfigs not found")
    templates = re.findall(r'"(buf(?:\.[^"/]+)?\.gen\.yaml)"', block.group(1))
    if not templates or len(templates) != len(set(templates)):
        raise ValueError("active template list is empty or duplicated")
    roots = {ENT_ROOT}
    for name in templates:
        template = repo / "api" / name
        if not template.is_file():
            raise ValueError(f"active template missing: {name}")
        outputs = re.findall(r"^\s+out:\s*([^\s#]+)", template.read_text(), re.M)
        if not outputs:
            raise ValueError(f"active template has no output: {name}")
        for output in outputs:
            if output.startswith("/"):
                raise ValueError(f"absolute managed root: {name}: {output}")
            root = posixpath.normpath(posixpath.join("api", output))
            if root in (".", "..") or root.startswith("../"):
                raise ValueError(f"managed root leaves repository: {name}: {output}")
            roots.add(root)
    if not REQUIRED_API_ROOTS <= roots:
        raise ValueError(f"required managed roots missing: {sorted(REQUIRED_API_ROOTS - roots)}")
    return sorted(roots)


def digest(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def snapshot(repo: pathlib.Path, sha: str) -> dict:
    roots = managed_roots(repo)
    outputs: dict[str, str] = {}
    inputs: dict[str, str] = {}
    for root in roots:
        directory = repo / root
        if not directory.is_dir():
            raise ValueError(f"managed root missing: {root}")
        for path in directory.rglob("*"):
            if path.is_symlink():
                raise ValueError(f"symlink in managed root: {path.relative_to(repo)}")
            if path.is_file():
                outputs[path.relative_to(repo).as_posix()] = digest(path)
    if not outputs:
        raise ValueError("no managed output files")
    schema_output = repo / ENT_SCHEMA_OUTPUT
    if not schema_output.is_file() or schema_output.is_symlink():
        raise ValueError(f"Ent SQL output missing or symlinked: {ENT_SCHEMA_OUTPUT}")
    outputs[ENT_SCHEMA_OUTPUT] = digest(schema_output)
    for name in INPUT_FILES:
        path = repo / name
        if not path.is_file() or path.is_symlink():
            raise ValueError(f"generation input missing or symlinked: {name}")
        inputs[name] = digest(path)
    for root in ("api", f"{ENT_ROOT}/schema"):
        directory = repo / root
        if not directory.is_dir():
            raise ValueError(f"generation input directory missing: {root}")
        for path in directory.rglob("*"):
            if path.is_symlink():
                raise ValueError(f"symlink in generation input: {path.relative_to(repo)}")
            if path.is_file():
                name = path.relative_to(repo).as_posix()
                if not any(name == managed or name.startswith(managed + "/") for managed in roots):
                    inputs[name] = digest(path)
                elif name.startswith(f"{ENT_ROOT}/schema/"):
                    inputs[name] = digest(path)
    return {"sha": sha, "managed_roots": roots,
            "outputs": dict(sorted(outputs.items())), "inputs": dict(sorted(inputs.items()))}


def compare_pair(a: dict, b: dict, label: str) -> list[str]:
    errors = []
    if a["sha"] != b["sha"]:
        errors.append(f"{label}: source SHA changed: {a['sha']} -> {b['sha']}")
    if a["managed_roots"] != b["managed_roots"]:
        errors.append(f"{label}: managed root set changed")
    for collection in ("outputs", "inputs"):
        before, after = a[collection], b[collection]
        for path in sorted(before.keys() - after.keys()):
            errors.append(f"{label}: {collection} missing: {path}")
        for path in sorted(after.keys() - before.keys()):
            errors.append(f"{label}: {collection} extra: {path}")
        for path in sorted(before.keys() & after.keys()):
            if before[path] != after[path]:
                errors.append(f"{label}: {collection} changed: {path}")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    snap = sub.add_parser("snapshot")
    snap.add_argument("--repo", type=pathlib.Path, required=True)
    snap.add_argument("--sha", required=True)
    snap.add_argument("--output", type=pathlib.Path, required=True)
    check = sub.add_parser("compare")
    for phase in ("baseline", "first", "second"):
        check.add_argument(f"--{phase}", type=pathlib.Path, required=True)
    overlay = sub.add_parser("overlay")
    overlay.add_argument("--repo", type=pathlib.Path, required=True)
    overlay.add_argument("--target", type=pathlib.Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "overlay":
            patch_bytes, copied = overlay_worktree(args.repo.resolve(), args.target.resolve())
            print(f"overlaid worktree: {patch_bytes} patch bytes, {copied} untracked files")
            return 0
        if args.command == "snapshot":
            result = snapshot(args.repo.resolve(), args.sha)
            args.output.write_text(json.dumps(result, indent=2, ensure_ascii=False) + "\n")
            print(f"snapshot {args.output}: {len(result['outputs'])} managed files, "
                  f"{len(result['inputs'])} inputs, {len(result['managed_roots'])} roots")
            return 0
        baseline, first, second = (json.loads(getattr(args, name).read_text())
                                   for name in ("baseline", "first", "second"))
        errors = (compare_pair(baseline, first, "baseline/first") +
                  compare_pair(first, second, "first/second") +
                  compare_pair(baseline, second, "baseline/second"))
        for error in errors:
            print(error, file=sys.stderr)
        if errors:
            return 1
        print(f"generation comparison passed: {len(baseline['outputs'])} managed files, "
              f"{len(baseline['inputs'])} inputs, {len(baseline['managed_roots'])} roots")
        return 0
    except (OSError, ValueError, KeyError, TypeError, json.JSONDecodeError,
            subprocess.CalledProcessError) as exc:
        print(f"generation comparison unavailable: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Reject quota storage bypasses of the production Ent transaction path."""
from __future__ import annotations
import argparse
from pathlib import Path
import re

PATTERNS = (
    r'"github.com/jackc/pgx', r'/quotasql"', r'\.Raw\(', r'\.BeginTx\(',
    r'\.(?:ExecContext|QueryContext|QueryRowContext)\(', r'\.DB\(\)\.(?:Conn|Begin)\(',
)


def check_source(path: Path) -> list[str]:
    if path.name.endswith('_test.go'):
        return []
    source = path.read_text()
    # The dedicated current-account reader uses generated Ent BeginTx to select
    # a PostgreSQL readonly Repeatable Read snapshot. Keep the exact native Ent
    # receiver and immutable options visible; SQL DB/driver BeginTx stays banned.
    if path.name == 'quota_read_snapshot.go' and re.search(
        r'func quotaReadSnapshot\(ctx context\.Context, client \*ent\.Client, read func\(\*ent\.Tx\) error\)', source
    ):
        source = source.replace(
            'client.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})',
            'nativeEntReadonlySnapshot',
        )
    return [f'{path}: quota storage bypasses Ent: {pattern}'
            for pattern in PATTERNS if re.search(pattern, source)]


def check(repo: Path) -> list[str]:
    root = repo / 'app/admin/service/internal/data'
    if not root.is_dir():
        return [f'missing quota data directory: {root}']
    paths = sorted(set(root.glob('quota*.go')) | set(root.glob('plan_quota*.go')))
    if not paths:
        return [f'missing quota source: {root}']
    errors = [error for path in paths for error in check_source(path)]
    if (root / 'quotasql').exists() or (repo / 'sqlc.yaml').exists():
        errors.append('Governance quota sqlc artifacts must not be restored')
    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument('--repo', type=Path, default=Path(__file__).resolve().parents[2])
    args = parser.parse_args()
    errors = check(args.repo)
    if errors:
        print('\n'.join(errors))
        return 1
    print('quota Ent boundary: pass')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())

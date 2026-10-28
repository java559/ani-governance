from __future__ import annotations
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[2]


class CIScriptTests(unittest.TestCase):
    def test_evidence_wrapper_preserves_status_and_redacts_public_copy(self):
        script = REPO / 'scripts/ci/run-with-evidence.sh'
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            env = dict(os.environ, ANI_CI_EVIDENCE_ROOT=str(root / 'public'),
                       ANI_CI_RAW_EVIDENCE_ROOT=str(root / 'raw'))
            secret = 'postgres://user:example-secret@localhost/fixture'
            passed = subprocess.run(['bash', str(script), 'pass-case', '--', 'python3', '-c',
                                     f'print({secret!r})'], env=env, capture_output=True)
            self.assertEqual(passed.returncode, 0, passed.stderr)
            raw = (root / 'raw/pass-case/stdout.log').read_text()
            public = (root / 'public/pass-case/stdout.log').read_text()
            self.assertIn(secret, raw)
            self.assertNotIn(secret, public)
            self.assertIn('[REDACTED-DSN]', public)
            self.assertNotIn(secret, (root / 'public/pass-case/command.txt').read_text())
            self.assertEqual((root / 'public/pass-case/command.rc').read_text().strip(), '0')
            self.assertEqual((root / 'public/pass-case/source.sha').read_text().strip(),
                             subprocess.check_output(['git', '-C', str(REPO), 'rev-parse', 'HEAD'], text=True).strip())
            self.assertTrue((root / 'public/pass-case/raw-sha256.txt').read_text())
            failed = subprocess.run(['bash', str(script), 'fail-case', '--', 'python3', '-c',
                                     'import sys; print("fixture failure"); sys.exit(37)'],
                                    env=env, capture_output=True)
            self.assertEqual(failed.returncode, 37)
            self.assertEqual((root / 'public/fail-case/command.rc').read_text().strip(), '37')
            self.assertIn('fixture failure', (root / 'public/fail-case/stdout.log').read_text())

    def test_quota_pg_packages_run_serially_and_propagate_failure(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            ci = root / 'scripts/ci'
            ci.mkdir(parents=True)
            shutil.copyfile(REPO / 'scripts/ci/integration.sh', ci / 'integration.sh')
            (ci / 'assert-test-results.py').write_text('print("critical fixture pass")\n')
            binary = root / 'bin/go'
            binary.parent.mkdir()
            binary.write_text('''#!/usr/bin/env python3
import json, os, sys
args=sys.argv[1:]
kind=('quota-server' if '-race' not in args and '-tags' in args and 'quota_pg' in args and './app/admin/service/internal/server' in args
      else 'quota-service' if '-race' not in args and '-tags' in args and 'quota_pg' in args and './app/admin/service/internal/service' in args
      else 'other')
with open(os.environ['EVENTS'], 'a') as out: out.write(kind + '\\n')
print(json.dumps({'Action':'pass','Package':'fixture','Test':kind}))
if os.environ.get('FAIL_KIND') == kind: sys.exit(43 if kind == 'quota-server' else 44)
''')
            binary.chmod(0o755)
            env = dict(os.environ, PATH=str(binary.parent) + os.pathsep + os.environ['PATH'],
                       EVENTS=str(root / 'events'), ANI_CI_EVIDENCE_DIR=str(root / 'evidence'),
                       ANI_TEST_DATABASE_DSN='postgres://fixture/data', ANI_TEST_GUARD_DATABASE_DSN='postgres://fixture/guard',
                       ANI_TEST_RACE_DATA_DSN='postgres://fixture/race-data',
                       ANI_TEST_RACE_SERVICE_DSN='postgres://fixture/race-service',
                       ANI_TEST_DATABASE_EXCLUSIVE='1', ANI_TEST_GUARD_DATABASE_EXCLUSIVE='1')
            def run(fail=None):
                (root / 'events').write_text('')
                env['FAIL_KIND'] = fail or ''
                result = subprocess.run(['bash', str(ci / 'integration.sh')], env=env, capture_output=True)
                return result, (root / 'events').read_text().splitlines()
            passed, events = run()
            self.assertEqual(passed.returncode, 0, passed.stderr)
            self.assertEqual(events.count('quota-server'), 1)
            self.assertEqual(events.count('quota-service'), 1)
            self.assertLess(events.index('quota-server'), events.index('quota-service'))
            failed_server, events = run('quota-server')
            self.assertEqual(failed_server.returncode, 43)
            self.assertNotIn('quota-service', events)
            failed_service, events = run('quota-service')
            self.assertEqual(failed_service.returncode, 44)
            self.assertLess(events.index('quota-server'), events.index('quota-service'))

    def test_generation_overlay_preserves_dirty_worktree_without_task_artifacts(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            source, staged = root / 'source', root / 'staged'
            source.mkdir()
            staged.mkdir()
            subprocess.run(['git', 'init', '-q', str(source)], check=True)
            subprocess.run(['git', '-C', str(source), 'config', 'user.name', 'Fixture'], check=True)
            subprocess.run(['git', '-C', str(source), 'config', 'user.email', 'fixture@example.invalid'], check=True)
            (source / 'api/protos').mkdir(parents=True)
            (source / 'api/protos/changed.proto').write_text('old\n')
            (source / 'api/protos/deleted.proto').write_text('old\n')
            subprocess.run(['git', '-C', str(source), 'add', '.'], check=True)
            subprocess.run(['git', '-C', str(source), 'commit', '-qm', 'fixture'], check=True)
            archive = subprocess.run(['git', '-C', str(source), 'archive', '--format=tar', 'HEAD'],
                                     capture_output=True, check=True).stdout
            subprocess.run(['tar', '-x', '-C', str(staged)], input=archive, check=True)
            (source / 'api/protos/changed.proto').write_text('new\n')
            subprocess.run(['git', '-C', str(source), 'add', 'api/protos/changed.proto'], check=True)
            (source / 'api/protos/changed.proto').write_text('newer\n')
            (source / 'api/protos/deleted.proto').unlink()
            (source / 'api/protos/new.proto').write_text('new input\n')
            (source / 'api/protos/private.env').write_text('private fixture')
            (source / 'notes.txt').write_text('unrelated fixture')
            (source / '_agent').mkdir()
            (source / '_agent/do-not-copy.zip').write_bytes(b'private fixture')
            script = REPO / 'scripts/ci/compare-generated.py'
            result = subprocess.run(['python3', str(script), 'overlay', '--repo', str(source),
                                     '--target', str(staged)], capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((staged / 'api/protos/changed.proto').read_text(), 'newer\n')
            self.assertFalse((staged / 'api/protos/deleted.proto').exists())
            self.assertEqual((staged / 'api/protos/new.proto').read_text(), 'new input\n')
            self.assertFalse((staged / 'api/protos/private.env').exists())
            self.assertFalse((staged / 'notes.txt').exists())
            self.assertFalse((staged / '_agent').exists())
            self.assertEqual((source / 'api/protos/changed.proto').read_text(), 'newer\n')
            self.assertIn('MM api/protos/changed.proto', subprocess.check_output(
                ['git', '-C', str(source), 'status', '--short'], text=True))

    def test_generation_overlay_rejects_symlink_without_copying_external_content(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            source, staged = root / 'source', root / 'staged'
            source.mkdir()
            staged.mkdir()
            subprocess.run(['git', 'init', '-q', str(source)], check=True)
            subprocess.run(['git', '-C', str(source), 'config', 'user.name', 'Fixture'], check=True)
            subprocess.run(['git', '-C', str(source), 'config', 'user.email', 'fixture@example.invalid'], check=True)
            (source / 'api/protos').mkdir(parents=True)
            (source / 'api/protos/original.proto').write_text('baseline fixture\n')
            subprocess.run(['git', '-C', str(source), 'add', '.'], check=True)
            subprocess.run(['git', '-C', str(source), 'commit', '-qm', 'fixture'], check=True)
            archive = subprocess.run(['git', '-C', str(source), 'archive', '--format=tar', 'HEAD'],
                                     capture_output=True, check=True).stdout
            subprocess.run(['tar', '-x', '-C', str(staged)], input=archive, check=True)
            (source / 'api/protos/plain.proto').write_text('ordinary fixture\n')
            command = ['python3', str(REPO / 'scripts/ci/compare-generated.py'),
                       'overlay', '--repo', str(source), '--target', str(staged)]
            positive = subprocess.run(command, capture_output=True)
            self.assertEqual(positive.returncode, 0, positive.stderr)
            self.assertEqual((staged / 'api/protos/plain.proto').read_text(), 'ordinary fixture\n')
            (source / 'api/protos/link.proto').symlink_to(root / 'external.proto')
            (root / 'external.proto').write_text('external fixture\n')
            result = subprocess.run(command, capture_output=True)
            self.assertEqual(result.returncode, 2, result.stderr)
            self.assertIn(b'unsupported untracked generation input: api/protos/link.proto', result.stderr)
            self.assertFalse((staged / 'api/protos/link.proto').exists())
            self.assertNotIn(b'overlaid worktree:', result.stdout)

    def test_generation_overlay_rejects_tracked_symlink_and_missing_target(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            source, staged = root / 'source', root / 'staged'
            source.mkdir()
            staged.mkdir()
            subprocess.run(['git', 'init', '-q', str(source)], check=True)
            subprocess.run(['git', '-C', str(source), 'config', 'user.name', 'Fixture'], check=True)
            subprocess.run(['git', '-C', str(source), 'config', 'user.email', 'fixture@example.invalid'], check=True)
            (source / 'api/protos').mkdir(parents=True)
            (source / 'api/protos/original.proto').write_text('old\n')
            subprocess.run(['git', '-C', str(source), 'add', '.'], check=True)
            subprocess.run(['git', '-C', str(source), 'commit', '-qm', 'fixture'], check=True)
            (source / 'api/protos/link.proto').symlink_to(root / 'private.proto')
            (root / 'private.proto').write_text('external fixture')
            subprocess.run(['git', '-C', str(source), 'add', 'api/protos/link.proto'], check=True)
            result = subprocess.run(['python3', str(REPO / 'scripts/ci/compare-generated.py'),
                                     'overlay', '--repo', str(source), '--target', str(staged)],
                                    capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn(b'overlaid worktree:', result.stdout)
            missing = subprocess.run(['python3', str(REPO / 'scripts/ci/compare-generated.py'),
                                      'overlay', '--repo', str(source), '--target', str(root / 'absent')],
                                     capture_output=True)
            self.assertNotEqual(missing.returncode, 0)

    def test_generation_comparator_covers_outputs_outside_api(self):
        script = REPO / 'scripts/ci/compare-generated.py'
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            active = root / 'tools/localdeps/gow/internal/buf/cmd.go'
            active.parent.mkdir(parents=True)
            active.write_text('var activeGenConfigs = []string{\n'
                              '"buf.gen.yaml",\n"buf.pagination.gen.yaml",\n'
                              '"buf.redact.gen.yaml",\n"buf.bootstrap.conf.gen.yaml",\n'
                              '"buf.admin.openapi.gen.yaml",\n"buf.quota-release.gen.yaml",\n}\n')
            templates = {
                'buf.gen.yaml': 'gen/go',
                'buf.pagination.gen.yaml': '../pkg/localdeps/go-crud/api/gen/go',
                'buf.redact.gen.yaml': '../pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact',
                'buf.bootstrap.conf.gen.yaml': '../pkg/localdeps/kratos-bootstrap/api/gen/go',
                'buf.admin.openapi.gen.yaml': '../app/admin/service/cmd/server/assets',
                'buf.quota-release.gen.yaml': 'quota/gen/go',
            }
            for name, output in templates.items():
                target = root / 'api' / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text(f'plugins:\n  - local: protoc\n    out: {output}\n')
            outputs = [
                'api/gen/go/example.pb.go',
                'api/quota/gen/go/quota_release.pb.go',
                'pkg/localdeps/go-crud/api/gen/go/pagination.pb.go',
                'pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact/redact.pb.go',
                'pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact/README.md',
                'pkg/localdeps/kratos-bootstrap/api/gen/go/bootstrap.pb.go',
                'app/admin/service/cmd/server/assets/openapi.yaml',
                'app/admin/service/cmd/server/assets/assets.go',
                'app/admin/service/internal/data/ent/schema.sql',
                'app/admin/service/internal/data/ent/schema/entity.go',
                'app/admin/service/schema.sql',
            ]
            for name in outputs + [
                'go.mod', 'go.sum', 'scripts/build-redact-plugin.sh',
                'scripts/finalize-aksk-openapi.py', 'tools/config/pgv-scope.json',
                'tools/config/tool-lock.json', 'api/example.proto',
            ]:
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text('original\n')
            baseline, first, second = (root / f'{phase}.json' for phase in ('baseline', 'first', 'second'))
            def snap(output):
                return subprocess.run(['python3', str(script), 'snapshot', '--repo', str(root),
                                       '--sha', 'fixed-sha', '--output', str(output)], capture_output=True)
            def compare():
                return subprocess.run(['python3', str(script), 'compare', '--baseline', str(baseline),
                                       '--first', str(first), '--second', str(second)], capture_output=True)
            self.assertEqual(snap(baseline).returncode, 0)
            self.assertEqual(snap(first).returncode, 0)
            self.assertEqual(snap(second).returncode, 0)
            self.assertEqual(compare().returncode, 0)
            target = root / 'pkg/localdeps/go-crud/api/gen/go/pagination.pb.go'
            target.write_text('changed\n')
            self.assertEqual(snap(second).returncode, 0)
            self.assertIn(b'pkg/localdeps/go-crud/api/gen/go/pagination.pb.go', compare().stderr)
            target.unlink()
            self.assertEqual(snap(second).returncode, 0)
            self.assertIn(b'outputs missing: pkg/localdeps/go-crud/api/gen/go/pagination.pb.go', compare().stderr)
            target.write_text('original\n')
            extra = root / 'pkg/localdeps/go-crud/api/gen/go/unexpected.pb.go'
            extra.write_text('new\n')
            self.assertEqual(snap(second).returncode, 0)
            self.assertIn(b'outputs extra: pkg/localdeps/go-crud/api/gen/go/unexpected.pb.go', compare().stderr)
            extra.unlink()
            handwritten = root / 'pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact/README.md'
            handwritten.write_text('rewritten\n')
            self.assertEqual(snap(second).returncode, 0)
            self.assertIn(b'pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact/README.md', compare().stderr)

    def test_layout_tracked_artifacts_and_receiver_exports(self):
        script = REPO / 'scripts/ci/check-layout.py'
        good_deps = [
            {'ImportPath': 'go-wind-admin/app/admin/service/cmd/server'},
            {'ImportPath': 'go-wind-admin/app/admin/service/cmd/admin'},
        ]
        cases = {
            '_agent/input.txt': 'private input',
            '.artifacts/result.txt': 'run output',
            '.scratch/note.txt': 'temporary note',
            'app/admin/service/run.log': 'runtime log',
            'app/admin/service/backup.dump': 'full dump',
            'app/admin/service/internal/data/testdata/backup.dump': 'database dump',
            'app/admin/service/internal/data/fixture.go': 'package data\nfunc (r *Repo) OpenForTest() {}\n',
        }
        for bad_path, contents in cases.items():
            with self.subTest(path=bad_path), tempfile.TemporaryDirectory() as td:
                root = Path(td)
                subprocess.run(['git', 'init', '-q', str(root)], check=True)
                good = {
                    'app/admin/service/cmd/server/main.go': 'package main\n',
                    'app/admin/service/internal/data/repo_test.go': 'package data\nfunc TestRepoForTest() {}\n',
                    'app/admin/service/tests/testutil/client.go': 'package testutil\nimport "testing"\n',
                    'app/admin/service/internal/data/ent/enttest/enttest.go': 'package enttest\nimport "testing"\n',
                    'app/admin/service/internal/data/testdata/fixture.log': 'small fixture\n',
                    'app/admin/service/internal/data/testdata/fixture.json': '{}\n',
                    'app/admin/service/internal/data/testdata/fixture.sql': 'SELECT 1;\n',
                }
                for path, text in good.items():
                    target = root / path
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_text(text)
                subprocess.run(['git', '-C', str(root), 'add', '-f', '.'], check=True)
                deps = root / 'deps.json'
                deps.write_text(''.join(json.dumps(item) + '\n' for item in good_deps))
                def check():
                    return subprocess.run(['python3', str(script), '--repo', str(root), '--deps-json', str(deps)], capture_output=True)
                accepted = check()
                self.assertEqual(accepted.returncode, 0, accepted.stderr)
                target = root / bad_path
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text(contents)
                subprocess.run(['git', '-C', str(root), 'add', '-f', bad_path], check=True)
                rejected = check()
                self.assertNotEqual(rejected.returncode, 0)
                self.assertIn(bad_path.encode(), rejected.stderr)

    def test_layout_rejects_retired_path_and_formal_test_dependency(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            subprocess.run(['git', 'init', '-q', str(root)], check=True)
            target = root / 'app/admin/service/cmd/server/main.go'
            target.parent.mkdir(parents=True)
            target.write_text('package main\n')
            subprocess.run(['git', '-C', str(root), 'add', '.'], check=True)
            deps = root / 'deps.json'
            good = [
                {'ImportPath': 'go-wind-admin/app/admin/service/cmd/server'},
                {'ImportPath': 'go-wind-admin/app/admin/service/cmd/admin'},
            ]
            script = REPO / 'scripts/ci/check-layout.py'
            def check(items):
                deps.write_text(''.join(json.dumps(item) + '\n' for item in items))
                return subprocess.run(['python3', str(script), '--repo', str(root), '--deps-json', str(deps)], capture_output=True).returncode
            self.assertEqual(check(good), 0)
            self.assertNotEqual(check(good + [{'ImportPath': 'go-wind-admin/app/admin/service/tests/testutil'}]), 0)
            retired = root / 'migration/obsolete.txt'
            retired.parent.mkdir()
            retired.write_text('obsolete')
            subprocess.run(['git', '-C', str(root), 'add', '.'], check=True)
            self.assertNotEqual(check(good), 0)

    def test_quota_storage_rejects_bypass_and_missing_source(self):
        with tempfile.TemporaryDirectory() as td:
            repo = Path(td)
            script = REPO / 'scripts/ci/check_quota_storage.py'
            missing = subprocess.run(['python3', str(script), '--repo', str(repo)], capture_output=True)
            self.assertNotEqual(missing.returncode, 0)
            data = repo / 'app/admin/service/internal/data'
            data.mkdir(parents=True)
            (data / 'quota_repo.go').write_text('package data\n')
            passed = subprocess.run(['python3', str(script), '--repo', str(repo)], capture_output=True)
            self.assertEqual(passed.returncode, 0, passed.stdout + passed.stderr)
            (data / 'quota_repo.go').write_text('package data\nfunc f(){ db.ExecContext(ctx, "bad") }\n')
            rejected = subprocess.run(['python3', str(script), '--repo', str(repo)], capture_output=True)
            self.assertNotEqual(rejected.returncode, 0)

    def test_quota_read_snapshot_only_allows_native_ent_readonly_repeatable_read(self):
        source = (REPO / 'app/admin/service/internal/data/quota_read_snapshot.go').read_text()
        with tempfile.TemporaryDirectory() as td:
            repo = Path(td)
            data = repo / 'app/admin/service/internal/data'
            data.mkdir(parents=True)
            path = data / 'quota_read_snapshot.go'
            script = REPO / 'scripts/ci/check_quota_storage.py'
            def check(contents):
                path.write_text(contents)
                return subprocess.run(['python3', str(script), '--repo', str(repo)], capture_output=True)
            passed = check(source)
            self.assertEqual(passed.returncode, 0, passed.stdout + passed.stderr)
            for old, new in (
                ('client *ent.Client', 'client *sql.DB'),
                ('sql.LevelRepeatableRead', 'sql.LevelReadCommitted'),
                ('ReadOnly: true', 'ReadOnly: false'),
                ('client.BeginTx(', 'db.BeginTx('),
            ):
                with self.subTest(replacement=new):
                    self.assertNotEqual(check(source.replace(old, new)).returncode, 0)
            self.assertNotEqual(check(source + '\nfunc bad() { db.BeginTx(ctx, nil) }\n').returncode, 0)
            path.write_text(source)
            path.rename(data / 'quota_other_reader.go')
            self.assertNotEqual(subprocess.run(['python3', str(script), '--repo', str(repo)], capture_output=True).returncode, 0)

    def test_required_test_result_rejects_skip_missing_and_failure(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            profile = root / 'required.json'
            log = root / 'result.jsonl'
            profile.write_text(json.dumps([{'Package': 'p', 'Test': 'TestCritical'}]))
            script = REPO / 'scripts/ci/assert-test-results.py'
            def check(events):
                log.write_text(''.join(json.dumps(e) + '\n' for e in events))
                return subprocess.run(['python3', str(script), '--required', str(profile), '--log', str(log)], capture_output=True).returncode
            self.assertEqual(check([{'Package': 'p', 'Test': 'TestCritical', 'Action': 'pass'}]), 0)
            self.assertNotEqual(check([]), 0)
            self.assertNotEqual(check([{'Package': 'p', 'Test': 'TestCritical', 'Action': 'skip'}]), 0)
            self.assertNotEqual(check([{'Package': 'p', 'Action': 'fail'}]), 0)

    def test_race_evidence_requires_its_own_named_passes(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            profile = root / 'required.json'
            race_log = root / 'race.jsonl'
            normal_log = root / 'normal.jsonl'
            profile.write_text(json.dumps([
                {'Package': 'data', 'Test': 'TestRaceA'},
                {'Package': 'data', 'Test': 'TestRaceB'},
                {'Package': 'service', 'Test': 'TestRaceService'},
            ]))
            normal_log.write_text(json.dumps({'Package': 'data', 'Test': 'TestRaceA', 'Action': 'pass'}) + '\n')
            script = REPO / 'scripts/ci/assert-test-results.py'
            def check(events, pattern):
                race_log.write_text(''.join(json.dumps(e) + '\n' for e in events))
                return subprocess.run(['python3', str(script), '--required', str(profile),
                                       '--test-regex', pattern, '--log', str(race_log)],
                                      capture_output=True).returncode
            both = '^TestRace(A|B)$'
            self.assertNotEqual(check([], both), 0, 'normal log passes cannot replace absent race events')
            self.assertNotEqual(check([{'Package': 'data', 'Test': 'TestRaceA', 'Action': 'skip'}], both), 0)
            self.assertNotEqual(check([{'Package': 'data', 'Test': 'TestRaceA', 'Action': 'pass'}], both), 0)
            self.assertNotEqual(check([], '^TestDoesNotExist$'), 0)
            self.assertEqual(check([{'Package': 'data', 'Test': 'TestRaceA', 'Action': 'pass'},
                                    {'Package': 'data', 'Test': 'TestRaceB', 'Action': 'pass'}], both), 0)

    def test_generation_evidence_keeps_stage_logs_on_failure(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            env = dict(os.environ, ANI_CI_EVIDENCE_ROOT=str(root / 'public'),
                       ANI_CI_RAW_EVIDENCE_ROOT=str(root / 'raw'))
            program = ('import os,pathlib,sys; d=pathlib.Path(os.environ["ANI_CI_EVIDENCE_DIR"]); '
                       '(d/"generation-baseline.json").write_text("{}\\n"); '
                       '(d/"gow-tests.jsonl").write_text("{\\"Action\\":\\"fail\\"}\\n"); '
                       '(d/"gow-tests.rc").write_text("0\\n"); '
                       '(d/"tools-integration.log").write_text("fixture failure\\n"); '
                       '(d/"tools-integration.rc").write_text("37\\n"); sys.exit(37)')
            result = subprocess.run(['bash', str(REPO / 'scripts/ci/run-with-evidence.sh'),
                                     'generation', '--', 'python3', '-c', program],
                                    env=env, capture_output=True)
            self.assertEqual(result.returncode, 37, result.stderr)
            public = root / 'public/generation'
            for name in ('generation-baseline.json', 'gow-tests.jsonl', 'gow-tests.rc',
                         'tools-integration.log', 'tools-integration.rc'):
                self.assertTrue((public / name).is_file(), name)
            self.assertEqual((public / 'tools-integration.rc').read_text().strip(), '37')
            self.assertEqual((root / 'public/job-status.txt').read_text().strip(), 'fail')


if __name__ == '__main__':
    unittest.main()

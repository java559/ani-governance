"""Exercise the rendered migration Job against a disposable PostgreSQL.

Opt in with ANI_SANDBOX_MIGRATION_ORDER_TEST=1, ANI_ATLAS_BIN, ANI_CHART_HELM
and ANI_TEST_POSTGRES_IMAGE (an immutable digest). The test creates and removes
only its own Docker container and temporary directory; it never uses kubeconfig
or an existing database.
"""
from __future__ import annotations

import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import textwrap
import time
import unittest

ROOT = Path(__file__).resolve().parents[2]


def run(args, **kwargs):
    return subprocess.run(args, text=True, capture_output=True, timeout=90, **kwargs)


@unittest.skipUnless(os.environ.get("ANI_SANDBOX_MIGRATION_ORDER_TEST") == "1", "requires opt-in isolated PostgreSQL")
class SandboxMigrationOrderTests(unittest.TestCase):
    def test_rendered_job_handles_only_expected_out_of_order_migration(self):
        atlas = os.environ["ANI_ATLAS_BIN"]
        helm = os.environ["ANI_CHART_HELM"]
        image = os.environ["ANI_TEST_POSTGRES_IMAGE"]
        self.assertIn("sha256:", image)
        rendered = run([helm, "template", "migration-test", str(ROOT / "charts/ani-governance"),
                        "--show-only", "templates/job-migrate.yaml", "--set", "image.tag=test",
                        "--set", "adminImage.tag=test", "--set", "atlasImage.tag=test",
                        "--set", "database.migrationSecret=test", "--set", "database.runtimeSecret=test"])
        self.assertEqual(rendered.returncode, 0, rendered.stderr)
        lines = rendered.stdout.splitlines()
        marker = next(i for i, line in enumerate(lines) if line.strip() == "- |")
        end = next(i for i in range(marker + 1, len(lines)) if lines[i].startswith("          securityContext:"))
        script = textwrap.dedent("\n".join(lines[marker + 1:end]))
        source = (ROOT / "migrations/20261030010000_tenant_space_intent.sql").read_text()
        name = "sandbox-migration-test-" + secrets.token_hex(6)
        with tempfile.TemporaryDirectory(prefix="sandbox-migration-test-") as tmp:
            root = Path(tmp)
            password = secrets.token_hex(16)
            secret_file = root / "env"
            secret_file.write_text("POSTGRES_PASSWORD=" + password + "\n")
            secret_file.chmod(0o600)
            started = False
            try:
                p = run(["docker", "run", "-d", "--name", name, "--env-file", str(secret_file),
                         "-p", "127.0.0.1::5432", image])
                self.assertEqual(p.returncode, 0, p.stderr)
                started = True
                for _ in range(45):
                    if run(["docker", "exec", name, "pg_isready", "-U", "postgres"]).returncode == 0:
                        break
                    time.sleep(1)
                else:
                    self.fail("isolated PostgreSQL did not become ready")
                port = run(["docker", "port", name, "5432/tcp"]).stdout.strip().rsplit(":", 1)[1]

                def sql(db, query):
                    p = run(["docker", "exec", name, "psql", "-U", "postgres", "-d", db,
                             "-v", "ON_ERROR_STOP=1", "-At", "-c", query])
                    self.assertEqual(p.returncode, 0, p.stderr)
                    return p.stdout.strip()

                def hashdir(directory):
                    p = run([atlas, "migrate", "hash", "--dir", "file://" + str(directory)])
                    self.assertEqual(p.returncode, 0, p.stderr)

                for case in ("fresh", "main_before_sandbox", "sandbox_already_applied", "other_out_of_order"):
                    with self.subTest(case=case):
                        sql("postgres", "CREATE DATABASE " + case)
                        url = f"postgres://postgres:{password}@127.0.0.1:{port}/{case}?sslmode=disable"
                        directory = root / case
                        directory.mkdir()
                        (directory / "20261028010000_first.sql").write_text(
                            "CREATE TABLE public.sys_tenants (id bigint PRIMARY KEY); "
                            "INSERT INTO public.sys_tenants VALUES (1);\n")
                        (directory / "20261031010000_last.sql").write_text(
                            "CREATE TABLE public.latest_main_marker (id integer);\n")
                        middle = directory / "20261030010000_tenant_space_intent.sql"
                        if case in ("fresh", "sandbox_already_applied"):
                            middle.write_text(source)
                        hashdir(directory)
                        if case != "fresh":
                            p = run([atlas, "migrate", "apply", "--dir", "file://" + str(directory), "--url", url])
                            self.assertEqual(p.returncode, 0, p.stderr)
                        if case in ("main_before_sandbox", "other_out_of_order"):
                            middle.write_text(source)
                        if case == "other_out_of_order":
                            (directory / "20261029010000_other.sql").write_text("CREATE TABLE public.other_marker (id integer);\n")
                        hashdir(directory)
                        env = os.environ.copy()
                        env["ANI_DATABASE_DSN"] = url
                        command = script.replace("/bin/atlas", atlas).replace("file:///app/migrations", "file://" + str(directory))
                        p = run(["/bin/sh", "-c", command], env=env)
                        blocked = case == "other_out_of_order"
                        self.assertEqual(p.returncode, 1 if blocked else 0, p.stderr)
                        columns = int(sql(case, "SELECT count(*) FROM information_schema.columns WHERE "
                                          "table_schema='public' AND table_name='sys_tenants' "
                                          "AND column_name LIKE 'tenant_space_%'"))
                        self.assertEqual(columns, 0 if blocked else 6)
                        if not blocked:
                            self.assertEqual(sql(case, "SELECT tenant_space_state FROM public.sys_tenants WHERE id=1"), "LEGACY")
            finally:
                if started:
                    result = run(["docker", "rm", "-f", name])
                    if result.returncode:
                        raise RuntimeError("could not remove isolated PostgreSQL: " + result.stderr)


if __name__ == "__main__":
    unittest.main()

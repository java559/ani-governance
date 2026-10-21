"""Opt-in real Helm renders; the only boundary double is a read-only API.

Run with ANI_CHART_HELM=/absolute/path/to/helm. This test never reads an
existing kubeconfig or connects to a cluster. ANI_CHART_BASELINE optionally
points to the fixed previous chart for the concrete render-difference check.
No third-party Python packages or Kubernetes processes are required.
"""
from __future__ import annotations

from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import threading
import unittest


REPO = Path(__file__).resolve().parents[2]
CHART = REPO / "charts/ani-governance"
RBAC_KINDS = {"Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding"}


def documents(text):
    """Index emitted resources; rules are compared as complete rendered text."""
    result = {}
    for part in re.split(r"^---\s*$", text, flags=re.MULTILINE):
        part = re.sub(r"^#.*\n?", "", part, flags=re.MULTILINE).strip()
        if not part:
            continue
        kind = re.search(r"^kind: (\S+)$", part, re.MULTILINE).group(1)
        metadata = re.search(r"^metadata:\n(.*?)(?=^\S)", part + "\nEND", re.MULTILINE | re.DOTALL).group(1)
        name = re.search(r"^  name: (\S+)$", metadata, re.MULTILINE).group(1)
        key = kind, name
        if key in result:
            raise AssertionError(f"duplicate rendered resource {key}")
        result[key] = part
    return result


@contextmanager
def namespace_api(present):
    """Kubernetes discovery/Namespace GET only; every mutation fails closed."""
    requests = []

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def respond(self, code, body):
            raw = json.dumps(body).encode()
            self.send_response(code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_GET(self):
            path = self.path.split("?", 1)[0]
            requests.append(("GET", path))
            if path == "/version":
                return self.respond(200, {"major": "1", "minor": "34", "gitVersion": "v1.34.0"})
            if path == "/api":
                return self.respond(200, {"apiVersion": "v1", "kind": "APIVersions", "versions": ["v1"]})
            if path == "/apis":
                return self.respond(200, {"apiVersion": "v1", "kind": "APIGroupList", "groups": []})
            if path == "/api/v1":
                return self.respond(200, {"apiVersion": "v1", "kind": "APIResourceList", "groupVersion": "v1", "resources": [{"name": "namespaces", "singularName": "namespace", "namespaced": False, "kind": "Namespace", "verbs": ["get", "list"]}]})
            if path == "/api/v1/namespaces/volcano-system" and present:
                return self.respond(200, {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "volcano-system"}})
            return self.respond(404, {"apiVersion": "v1", "kind": "Status", "status": "Failure", "reason": "NotFound", "code": 404})

        def mutation(self):
            requests.append((self.command, self.path))
            self.respond(405, {"kind": "Status", "reason": "MethodNotAllowed", "code": 405})

        do_POST = do_PUT = do_PATCH = do_DELETE = mutation

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    worker = threading.Thread(target=server.serve_forever, daemon=True)
    worker.start()
    try:
        yield f"http://127.0.0.1:{server.server_port}", requests
    finally:
        server.shutdown()
        worker.join(timeout=5)
        server.server_close()
        if any(method != "GET" for method, _ in requests):
            raise AssertionError(f"Helm attempted mutation against read-only fixture: {requests}")


@unittest.skipUnless(os.environ.get("ANI_CHART_HELM"), "opt-in Helm checks: set ANI_CHART_HELM explicitly")
class GovernanceChartTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.helm = os.environ["ANI_CHART_HELM"]
        subprocess.run([cls.helm, "version", "--short"], check=True, capture_output=True)

    def render(self, *, chart=CHART, present=None, upgrade=False, values=()):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            # This explicit fixture kubeconfig also protects client-only
            # renders from inheriting a real user's cluster configuration.
            def invoke(server=None):
                config = root / "kubeconfig"
                config.write_text("apiVersion: v1\nkind: Config\nclusters:\n- name: fixture\n  cluster:\n    server: " + (server or "http://127.0.0.1:1") + "\ncontexts:\n- name: fixture\n  context:\n    cluster: fixture\n    user: fixture\ncurrent-context: fixture\nusers:\n- name: fixture\n  user: {}\n")
                config.chmod(0o600)
                args = [self.helm, "template", "chart-check", str(chart), "--namespace", "chart-test", "--kubeconfig", str(config), "--kube-context", "fixture", "--disable-openapi-validation", "--set-string", "image.tag=chart-test", "--set-string", "adminImage.tag=chart-test", "--set-string", "atlasImage.tag=chart-test"]
                if server:
                    args.append("--dry-run=server")
                if upgrade:
                    args.append("--is-upgrade")
                for value in values:
                    args.extend(["--set", value])
                completed = subprocess.run(args, capture_output=True, text=True, timeout=30)
                self.assertEqual(completed.returncode, 0, completed.stderr)
                return documents(completed.stdout)

            if present is None:
                return invoke()
            with namespace_api(present) as (server, requests):
                rendered = invoke(server)
                if "rbac.create=false" in values:
                    self.assertNotIn(("GET", "/api/v1/namespaces/volcano-system"), requests)
                else:
                    self.assertIn(("GET", "/api/v1/namespaces/volcano-system"), requests)
                return rendered

    def assert_rbac(self, rendered, config):
        resources = {kind: text for (kind, _), text in rendered.items() if kind in RBAC_KINDS}
        self.assertEqual(set(resources), {"ClusterRole", "ClusterRoleBinding"} | ({"Role", "RoleBinding"} if config else set()))
        self.assertEqual(resources["ClusterRole"].split("\nrules:\n", 1)[1], '  - apiGroups: ["scheduling.k8s.io"]\n    resources: ["priorityclasses"]\n    verbs: ["get"]\n  - apiGroups: ["scheduling.volcano.sh"]\n    resources: ["podgroups"]\n    verbs: ["get"]')
        if config:
            self.assertIn("\n  namespace: volcano-system\n", resources["Role"])
            self.assertEqual(resources["Role"].split("\nrules:\n", 1)[1], '  - apiGroups: [""]\n    resources: ["configmaps"]\n    resourceNames: ["ani-volcano-scheduler-configmap"]\n    verbs: ["get", "update"]')
        for kind in ("ClusterRoleBinding", "RoleBinding"):
            if kind in resources:
                self.assertIn("subjects:\n  - kind: ServiceAccount\n    name: chart-check-ani-governance\n    namespace: chart-test\n", resources[kind])
                self.assertIn("\n  kind: " + ("Role" if kind == "RoleBinding" else "ClusterRole") + "\n", resources[kind])

    def test_default_and_namespace_lookup_have_narrow_permissions(self):
        self.assert_rbac(self.render(), False)
        self.assert_rbac(self.render(present=False), False)
        self.assert_rbac(self.render(present=True), True)

    def test_explicit_false_and_old_upgrade_values(self):
        disabled = self.render(values=("rbac.create=false",))
        self.assertFalse(any(kind in RBAC_KINDS for kind, _ in disabled))
        for present in (False, True):
            disabled = self.render(present=present, values=("rbac.create=false",))
            self.assertFalse(any(kind in RBAC_KINDS for kind, _ in disabled))
        for present in (False, True):
            self.assert_rbac(self.render(present=present, upgrade=True), present)
        # Old release values had no rbac map. Null removes the default map
        # during Helm coalescing and exercises the template's upgrade default.
        self.assert_rbac(self.render(upgrade=True, values=("rbac=null",)), False)
        disabled_upgrade = self.render(upgrade=True, values=("rbac.create=false",))
        self.assertFalse(any(kind in RBAC_KINDS for kind, _ in disabled_upgrade))

    def test_inference_toggle_keeps_rbac_and_jobs_token_free(self):
        disabled = self.render(present=True)
        enabled = self.render(present=True, values=("downstream.inference.enabled=true", "downstream.inference.address=inference.fixture:19090", "downstream.inference.serverName=inference.fixture", "downstream.inference.caSecret=inference-test-ca"))
        for key, text in disabled.items():
            if key[0] in RBAC_KINDS:
                self.assertEqual(text, enabled[key])
        deployment = next(text for (kind, _), text in enabled.items() if kind == "Deployment")
        self.assertIn("automountServiceAccountToken: true", deployment)
        self.assertIn("name: ANI_INFERENCE_ADDR", deployment)
        self.assertIn("mountPath: /service-identity/inference", deployment)
        jobs = [text for (kind, _), text in enabled.items() if kind == "Job"]
        self.assertEqual(len(jobs), 2)
        for job in jobs:
            self.assertIn("automountServiceAccountToken: false", job)
            self.assertNotIn("/service-identity/inference", job)

    def test_fixed_main_render_diff_contains_only_expected_changes(self):
        baseline = os.environ.get("ANI_CHART_BASELINE")
        if not baseline:
            self.skipTest("set ANI_CHART_BASELINE to compare a fixed previous chart")
        previous = self.render(chart=Path(baseline))
        current = self.render()
        self.assertEqual(set(current) - set(previous), {("ClusterRole", "chart-check-ani-governance-scheduler-options"), ("ClusterRoleBinding", "chart-check-ani-governance-scheduler-options")})
        self.assertFalse(set(previous) - set(current))
        for key, value in previous.items():
            actual = current[key]
            if key[0] == "Deployment":
                actual = actual.replace("      # The server uses in-cluster credentials for scheduling configuration\n      # and capability checks. Bootstrap Jobs remain token-free.\n", "").replace("automountServiceAccountToken: true", "automountServiceAccountToken: false")
            self.assertEqual(actual, value, f"unrelated main render changed: {key}")


if __name__ == "__main__":
    unittest.main()

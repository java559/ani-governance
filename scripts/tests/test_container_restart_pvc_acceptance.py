"""Offline checks only: no API server, login, cluster or business-state double."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
import uuid

SPEC = importlib.util.spec_from_file_location("container_restart_driver", Path(__file__).resolve().parents[1] / "ops/container-restart-pvc-acceptance.py")
driver = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(driver)


class ContainerRestartDriverTests(unittest.TestCase):
    def test_private_file_permissions_and_no_create_environment_in_state(self):
        with tempfile.TemporaryDirectory() as directory:
            secret = "synthetic-private-value-for-redaction-check"
            path = Path(directory) / "credentials.json"
            path.write_text(json.dumps({"password": secret}))
            path.chmod(0o600)
            self.assertEqual(driver.private_json(path), {"password": secret})
            path.chmod(0o644)
            with self.assertRaises(driver.AcceptanceError):
                driver.private_json(path)
            target = Path(directory) / "evidence"
            evidence = driver.Evidence(target, "https://example.invalid")
            body = {"data": {"env": [{"name": "PRIVATE_VALUE", "value": secret}]}}
            evidence.intent("create", "create", body)
            saved = (target / "state.json").read_text()
            self.assertNotIn(secret, saved)
            self.assertNotIn("PRIVATE_VALUE", saved)
            self.assertEqual(os.stat(target / "state.json").st_mode & 0o777, 0o600)

    def test_resume_keeps_original_key_version_and_body_even_without_ack(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = driver.Evidence(directory, "https://example.invalid")
            first = evidence.intent("restart", "restart", {"data": {"expectedVersion": "7"}}, "7")
            key, digest = first["idempotency_key"], first["request_body_sha256"]
            resumed = driver.Evidence(directory, "https://example.invalid")
            again = resumed.intent("restart", "restart", {"data": {"expectedVersion": "7"}}, "7")
            self.assertEqual((key, digest), (again["idempotency_key"], again["request_body_sha256"]))
            for body, version in [({"data": {"expectedVersion": "8"}}, "8"), ({"data": {"expectedVersion": "7", "idempotencyKey": str(uuid.uuid4())}}, "7")]:
                with self.assertRaises(driver.AcceptanceError):
                    resumed.intent("restart", "restart", body, version)
            self.assertEqual(resumed.state["intents"]["restart"]["idempotency_key"], key)

    def test_create_snake_key_is_not_duplicated_and_changed_body_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = driver.Evidence(directory, "https://example.invalid")
            key = str(uuid.uuid4())
            first = {"data": {"idempotency_key": key, "name": "first"}}
            evidence.intent("create", "create", first)
            self.assertNotIn("idempotencyKey", first["data"])
            with self.assertRaises(driver.AcceptanceError):
                evidence.intent("create", "create", {"data": {"idempotency_key": key, "name": "changed"}})

    def test_int64_and_http_origin_contract(self):
        for value in ("9007199254740993", "9223372036854775807"):
            self.assertEqual(driver.decimal(value), value)
        for value in (7, "7e1", "07", "9223372036854775808", "0"):
            with self.assertRaises(driver.AcceptanceError):
                driver.decimal(value)
        self.assertEqual(driver.origin_url("https://example.invalid/"), "https://example.invalid")
        self.assertEqual(driver.origin_url("http://127.0.0.1:1234"), "http://127.0.0.1:1234")
        for value in ("http://example.invalid", "https://user:secret@example.invalid", "https://example.invalid/?token=secret"):
            with self.assertRaises(driver.AcceptanceError):
                driver.origin_url(value)
        for raw in ('{"data":1,"data":2}', '{"data":NaN}'):
            with self.assertRaises(driver.AcceptanceError):
                driver.object_json(raw)

    def test_live_result_requires_old_uid_absence_and_unchanged_data(self):
        before = {"instance_id": str(uuid.uuid4()), "run_id": str(uuid.uuid4()), "notebook_uid": str(uuid.uuid4()), "workspace_pvc_uid": str(uuid.uuid4()), "pod_uid": str(uuid.uuid4()), "pod_name": "workspace-0", "markers": {"/workspace": {"sha256": "a" * 64}}, "mount_identities": {"/workspace": {"pvc_uid": "saved"}}}
        after = copy.deepcopy(before)
        after["pod_uid"] = str(uuid.uuid4())
        after["old_pod_get"] = {"method": "direct_get", "name": before["pod_name"], "expected_uid": before["pod_uid"], "not_found": False, "observed_uid": after["pod_uid"]}
        driver.verify_restart(before, after)
        for mutate in (lambda value: value.update(pod_uid=before["pod_uid"]), lambda value: value.update(notebook_uid=str(uuid.uuid4())), lambda value: value["old_pod_get"].update(observed_uid=before["pod_uid"]), lambda value: value["markers"]["/workspace"].update(sha256="b" * 64), lambda value: value["mount_identities"]["/workspace"].update(pvc_uid="replaced")):
            changed = copy.deepcopy(after)
            mutate(changed)
            with self.assertRaises(driver.AcceptanceError):
                driver.verify_restart(before, changed)

    def test_instance_projection_does_not_copy_environment_or_unknown_objects(self):
        instance_id = str(uuid.uuid4())
        reply = {"id": instance_id, "version": "7", "instance_kind": "container", "compute_type": "cpu", "desiredState": "running", "observedState": "running", "env": [{"value": "synthetic-secret"}], "unknown": {"access_token": "synthetic-token"}}
        safe = driver.instance_projection(reply)
        self.assertNotIn("synthetic", json.dumps(safe))
        reply["observedState"] = {"password": "synthetic-secret"}
        with self.assertRaises(driver.AcceptanceError):
            driver.instance_projection(reply)

    def test_observer_receives_explicit_scope_without_login_environment(self):
        with tempfile.TemporaryDirectory() as directory:
            evidence = driver.Evidence(directory, "https://example.invalid")
            hook = Path(directory) / "observer"
            hook.write_text("#!" + sys.executable + "\n" + '''import datetime,json,os,sys,uuid
request=json.load(sys.stdin)
if os.environ.get("ANI_ACCEPTANCE_TENANT_NAMESPACE") != "tenant-synthetic" or "ANI_TEST_PRIVATE_LOGIN_SECRET" in os.environ:
 sys.exit(9)
workspace_uid=str(uuid.uuid4())
print(json.dumps({"source":"live","instance_id":request["instance_id"],"run_id":request["run_id"],"observed_at":datetime.datetime.now(datetime.timezone.utc).isoformat(),"notebook_uid":str(uuid.uuid4()),"workspace_pvc_uid":workspace_uid,"pod_uid":str(uuid.uuid4()),"pod_name":"workspace-0","markers":[{"mount_path":request["mounts"][0]["mount_path"],"marker_name":request["marker_name"],"sha256":request["marker_sha256"],"read_verified":True,"write_verified":True}],"mount_identities":[{"mount_path":request["mounts"][0]["mount_path"],"pvc_uid":workspace_uid}]}))
''')
            hook.chmod(0o700)
            configuration = {"workspace_mount_path": "/workspace", "volume_mounts": []}
            with patch.dict(os.environ, {"ANI_ACCEPTANCE_TENANT_NAMESPACE": "tenant-synthetic", "ANI_TEST_PRIVATE_LOGIN_SECRET": "synthetic-must-not-cross-hook"}):
                observed = driver.observation(str(hook), "before-restart", evidence, str(uuid.uuid4()), configuration, 5)
            self.assertEqual(observed["source"], "live")
            self.assertEqual(set(observed["markers"]), {"/workspace"})


if __name__ == "__main__":
    unittest.main()

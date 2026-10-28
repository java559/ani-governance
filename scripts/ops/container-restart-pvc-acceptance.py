#!/usr/bin/env python3
"""Explicit Gov HTTP acceptance for CP-01 and individual lifecycle/PVC intents.

Runs only when given a target origin, a private credential JSON and a dedicated
0700 evidence directory. No cluster defaults, shell commands, embedded account,
TLS bypass, automatic resource deletion or business-state SQL are used.

Credential JSON (0600, owned by the caller):
  {"tenant":{"tenant_name":"<tenant code>","username":"<user>","password":"<raw password>"},
   "platform":{"username":"<platform user>","password":"<raw password>"}}
The platform object is used only with --check-platform-login. Passwords are sent
unchanged to the two source-defined password endpoints. Cookies/tokens stay in
memory; each session is logged out in finally. No response body is dumped.

restart-cpu supports --create-input (a private {"data":{...}} request file) or an
explicit --instance-id/--expected-version. It durably saves the original key,
version and HTTP-body digest before POST, so repeating the same command resumes
that intent even after ACK loss. It never invents a replacement key on failure.

--observation-hook is an absolute executable. JSON goes to stdin, never a shell.
It receives only instance/operation identity, current mount paths and a synthetic
marker; it receives no access token, cookie, credential or env configuration.
The hook must use real runtime observations/exec. Before restart it writes and
reads the marker on workspace and writable borrowed mounts, and hashes an
existing file on each readonly mount. After restart it reads the same files,
returns current object UIDs and a direct GET result for the saved old Pod.
A missing hook leaves CP-01 NOT_VERIFIED; operation completion alone is not LIVE.
Hook output schema is enforced by observation() and verify_restart().

Examples contain placeholders only; use --help for required options. Run this
API driver on the Goal-authorized host after deployment identity is verified.
"""

import argparse
import datetime as dt
import hashlib
import http.cookiejar
import ipaddress
import json
import os
from pathlib import Path
import re
import ssl
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

MAX_BYTES = 1024 * 1024
TERMINAL = {"completed", "failed", "superseded", "CANCELED_UNSENT", "REJECTED"}
STATES = TERMINAL | {"accepted", "blocked", "QUEUED", "DISPATCHING", "UNKNOWN", "ACKED"}
PHASES = {"", "validating", "stopping", "waiting_stopped", "applying_mounts", "waiting_template", "committing_mounts", "starting", "waiting_ready", "waiting_network", "recovering_stop", "verifying_safe_stop", "done", "creating", "deleting"}
REASON = re.compile(r"^[A-Z][A-Z0-9_]{0,95}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")


class AcceptanceError(Exception):
    """Only a fixed safe code and whitelisted public error metadata escape."""
    def __init__(self, code, **facts):
        self.code, self.facts = code, facts
        super().__init__(code)


def now():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")


def require(condition, code):
    if not condition:
        raise AcceptanceError(code)


def object_json(raw):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, "DUPLICATE_JSON_KEY")
            result[key] = value
        return result
    try:
        value = json.loads(raw, object_pairs_hook=unique, parse_constant=lambda value: (_ for _ in ()).throw(ValueError("non-finite JSON number")))
    except (ValueError, UnicodeError):
        raise AcceptanceError("INVALID_JSON") from None
    require(isinstance(value, dict), "JSON_OBJECT_REQUIRED")
    return value


def private_json(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(fd)
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o600, "PRIVATE_JSON_REQUIRES_OWNER_MODE_0600")
        with os.fdopen(fd, "rb", closefd=False) as stream:
            raw = stream.read(MAX_BYTES + 1)
        require(len(raw) <= MAX_BYTES, "JSON_FILE_TOO_LARGE")
        return object_json(raw)
    finally:
        os.close(fd)


def decimal(value, allow_zero=False):
    require(isinstance(value, str) and re.fullmatch(r"0|[1-9][0-9]*", value) is not None, "DECIMAL_STRING_REQUIRED")
    require((0 if allow_zero else 1) <= int(value) <= 9223372036854775807, "VERSION_OUT_OF_RANGE")
    return value


def canonical_uuid(value):
    try:
        parsed = uuid.UUID(value)
    except (ValueError, TypeError, AttributeError):
        raise AcceptanceError("CANONICAL_UUID_REQUIRED") from None
    require(parsed.int != 0 and str(parsed) == value, "CANONICAL_UUID_REQUIRED")
    return value


def body_bytes(body):
    return json.dumps(body, ensure_ascii=True, separators=(",", ":"), sort_keys=True).encode()


def origin_url(value):
    url = urllib.parse.urlsplit(value)
    require(url.scheme in ("http", "https") and url.hostname and not url.username and not url.password and not url.query and not url.fragment and url.path in ("", "/"), "BASE_URL_MUST_BE_AN_ORIGIN")
    if url.scheme == "http":
        try:
            loopback = ipaddress.ip_address(url.hostname).is_loopback
        except ValueError:
            loopback = url.hostname == "localhost"
        require(loopback, "BASE_URL_REQUIRES_TLS_OR_LOOPBACK")
    return urllib.parse.urlunsplit((url.scheme, url.netloc, "", "", ""))


def safe_reason(value):
    return value if isinstance(value, str) and REASON.fullmatch(value) else "REASON_REDACTED"


class Evidence:
    def __init__(self, directory, base_url, run_id=None):
        self.directory = Path(directory).absolute()
        if not self.directory.exists():
            self.directory.mkdir(mode=0o700, parents=True)
        info = self.directory.lstat()
        require(stat.S_ISDIR(info.st_mode) and info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o700, "EVIDENCE_DIRECTORY_REQUIRES_OWNER_MODE_0700")
        self.path = self.directory / "state.json"
        self.state = private_json(self.path) if self.path.exists() else {"schema": "container-restart-pvc-http-v1", "run_id": run_id or str(uuid.uuid4()), "base_url": base_url, "intents": {}}
        require(self.state.get("schema") == "container-restart-pvc-http-v1" and self.state.get("base_url") == base_url, "EVIDENCE_TARGET_MISMATCH")
        if run_id:
            require(self.state.get("run_id") == run_id, "EVIDENCE_RUN_ID_MISMATCH")
        canonical_uuid(self.state["run_id"])
        self.save()

    def save(self):
        temporary = self.directory / (".state-" + uuid.uuid4().hex)
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            with os.fdopen(fd, "w", closefd=False) as stream:
                json.dump(self.state, stream, ensure_ascii=True, indent=2)
                stream.write("\n")
                stream.flush()
                os.fsync(fd)
            os.replace(temporary, self.path)
            directory_fd = os.open(self.directory, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory_fd)
            finally:
                os.close(directory_fd)
        finally:
            os.close(fd)
            temporary.unlink(missing_ok=True)

    def event(self, event, **facts):
        # Callers provide allowlisted facts only. Raw HTTP/hook bodies never enter this journal.
        path = self.directory / "events.jsonl"
        fd = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            info = os.fstat(fd)
            require(info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == 0o600 and stat.S_ISREG(info.st_mode), "UNSAFE_EVENT_FILE")
            os.write(fd, body_bytes({"at": now(), "event": event, **facts}) + b"\n")
        finally:
            os.close(fd)

    def intent(self, name, action, body, expected_version=None):
        intent = self.state["intents"].get(name)
        data = body.get("data", {})
        require(not ("idempotencyKey" in data and "idempotency_key" in data), "DUPLICATE_IDEMPOTENCY_KEY_SPELLING")
        key_field = "idempotency_key" if "idempotency_key" in data else "idempotencyKey"
        provided_key = data.get(key_field)
        if intent is None:
            key = provided_key or str(uuid.uuid4())
            canonical_uuid(key)
            intent = {"action": action, "idempotency_key": key, "idempotency_field": key_field, "expected_version": expected_version}
            self.state["intents"][name] = intent
        require(intent.get("action") == action and intent.get("expected_version") == expected_version, "ORIGINAL_INTENT_CHANGED")
        require(provided_key is None or provided_key == intent["idempotency_key"], "ORIGINAL_IDEMPOTENCY_KEY_CHANGED")
        require(key_field == intent["idempotency_field"], "ORIGINAL_REQUEST_BODY_CHANGED")
        body["data"][key_field] = intent["idempotency_key"]
        digest = hashlib.sha256(body_bytes(body)).hexdigest()
        require(intent.get("request_body_sha256", digest) == digest, "ORIGINAL_REQUEST_BODY_CHANGED")
        intent["request_body_sha256"] = digest
        self.save()  # Before any HTTP write, including the first uncertain attempt.
        return intent


class NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise AcceptanceError("HTTP_REDIRECT_REFUSED")


class Session:
    def __init__(self, base_url, application, credentials, evidence, timeout, ca_file=None, origin=None):
        self.base_url, self.application, self.credentials = base_url, application, credentials
        self.evidence, self.timeout, self.origin = evidence, timeout, origin
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(NoRedirects(), urllib.request.HTTPCookieProcessor(self.cookies), urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=ca_file)))
        self.token, self.expires_at, self.login_attempted = None, 0, False

    def request(self, method, path, body=None, expected=200, authenticated=True, extra_headers=None):
        require(path.startswith("/api/v1/") and "?" not in path and "#" not in path, "INVALID_FIXED_API_PATH")
        if authenticated:
            self.ensure_token()
        headers = {"Accept": "application/json"}
        if body is not None:
            headers["Content-Type"] = "application/json"
        if self.origin:
            headers["Origin"] = self.origin
        if authenticated:
            headers["Authorization"] = "Bearer " + self.token
        headers.update(extra_headers or {})
        request = urllib.request.Request(self.base_url + path, data=body_bytes(body) if body is not None else None, method=method, headers=headers)
        try:
            with self.opener.open(request, timeout=self.timeout) as response:
                status = response.status
                raw = response.read(MAX_BYTES + 1)
        except urllib.error.HTTPError as error:
            raw = error.read(MAX_BYTES + 1)
            try:
                error_body = object_json(raw)
                reason = safe_reason(error_body.get("reason"))
            except AcceptanceError:
                reason = "UNCLASSIFIED_HTTP_ERROR"
            self.evidence.event("http_error", method=method, path=path, http_status=error.code, reason=reason)
            raise AcceptanceError("HTTP_REQUEST_REJECTED", http_status=error.code, reason=reason) from None
        except (urllib.error.URLError, TimeoutError, OSError):
            self.evidence.event("http_uncertain", method=method, path=path)
            raise AcceptanceError("HTTP_TRANSPORT_UNCERTAIN") from None
        require(status == expected, "UNEXPECTED_HTTP_STATUS")
        require(len(raw) <= MAX_BYTES, "HTTP_RESPONSE_TOO_LARGE")
        return object_json(raw)

    def set_token(self, reply):
        require(reply.get("mfa_required") is not True, "AUTH_MFA_REQUIRED")
        require(isinstance(reply.get("access_token"), str) and bool(reply["access_token"]), "AUTH_TOKEN_MISSING")
        expires = decimal(reply.get("expires_in"))
        self.token, self.expires_at = reply["access_token"], time.monotonic() + int(expires)

    def login(self):
        fields = ("tenant_name", "username", "password") if self.application == "console" else ("username", "password")
        require(isinstance(self.credentials, dict) and set(self.credentials) == set(fields) and all(isinstance(self.credentials.get(key), str) and self.credentials[key] for key in fields), "INVALID_PRIVATE_CREDENTIAL_SHAPE")
        path = "/api/v1/auth/password/login" if self.application == "console" else "/api/v1/auth/platform/password/login"
        self.login_attempted = True
        reply = self.request("POST", path, {key: self.credentials[key] for key in fields}, authenticated=False)
        self.set_token(reply)
        self.evidence.event("authenticated", application=self.application, http_status=200)
        self.credentials = None

    def ensure_token(self):
        require(self.token is not None, "AUTH_SESSION_REQUIRED")
        if time.monotonic() + 30 < self.expires_at:
            return
        csrf = self.request("POST", "/api/v1/auth/csrf", {"application": self.application}, authenticated=False)
        require(isinstance(csrf.get("csrf_token"), str) and bool(csrf["csrf_token"]), "AUTH_CSRF_MISSING")
        reply = self.request("POST", "/api/v1/auth/refresh", {"application": self.application}, authenticated=False, extra_headers={"X-CSRF-Token": csrf["csrf_token"]})
        self.set_token(reply)
        self.evidence.event("session_refreshed", application=self.application, http_status=200)

    def close(self):
        result = "NOT_STARTED"
        try:
            if self.login_attempted:
                reply = self.request("POST", "/api/v1/auth/logout", {"application": self.application}, authenticated=False)
                result = "CONFIRMED" if reply.get("status") == "revoked" and reply.get("session_revoked") is True else "NOT_CONFIRMED"
        except AcceptanceError as error:
            result = "FAILED"
            self.evidence.event("logout_error", application=self.application, code=error.code, **error.facts)
        finally:
            self.token, self.credentials = None, None
            self.cookies.clear()
            self.evidence.event("session_cleanup", application=self.application, remote=result, local="CLEARED")
        return result


def instance_projection(reply):
    keys = ("id", "instance_kind", "compute_type", "version", "desiredState", "observedState", "activeOperationId", "executionRunState", "appliedMountVersion", "lastOperationId", "createOperationId", "computeCleaned", "workspaceCleaned", "networkCleaned", "volumeReferencesReleased", "quotaReturned")
    result = {key: reply[key] for key in keys if key in reply}
    canonical_uuid(result.get("id"))
    decimal(result.get("version"))
    require(result.get("instance_kind") == "container" and result.get("compute_type") in ("cpu", "gpu"), "INVALID_INSTANCE_PROTOCOL")
    for key in ("desiredState", "executionRunState"):
        if key in result:
            require(result[key] in ("running", "stopped", "deleted"), "INVALID_INSTANCE_RUN_STATE")
    require(result.get("observedState") in ("pending", "running", "stopped", "deleted"), "INVALID_INSTANCE_OBSERVATION")
    for key in ("activeOperationId", "lastOperationId", "createOperationId"):
        if result.get(key):
            canonical_uuid(result[key])
        elif key in result:
            require(result[key] == "", "INVALID_OPERATION_POINTER")
    if "appliedMountVersion" in result:
        decimal(result["appliedMountVersion"], allow_zero=True)
    for key in ("computeCleaned", "workspaceCleaned", "networkCleaned", "volumeReferencesReleased", "quotaReturned"):
        if key in result:
            require(isinstance(result[key], bool), "INVALID_INSTANCE_CLEANUP_FLAG")
    return result


def operation_projection(reply):
    require(reply.get("state") in STATES and reply.get("phase", "") in PHASES, "UNKNOWN_OPERATION_PROTOCOL")
    result = {key: reply[key] for key in ("id", "instanceId", "action", "intentVersion", "state", "phase", "final_run_state", "applied_mount_version", "completion_scope") if key in reply}
    result["reason"] = safe_reason(reply.get("reason")) if reply.get("reason") else ""
    result["failure_reason"] = safe_reason(reply.get("failure_reason")) if reply.get("failure_reason") else ""
    canonical_uuid(result.get("id"))
    canonical_uuid(result.get("instanceId"))
    decimal(result.get("intentVersion"), allow_zero=True)
    require(result.get("action") in ("create", "start", "stop", "restart", "attach-pvc", "detach-pvc", "delete"), "INVALID_OPERATION_ACTION")
    if "applied_mount_version" in result:
        decimal(result["applied_mount_version"], allow_zero=True)
    require(result.get("final_run_state", "") in ("", "running", "stopped", "deleted") and result.get("completion_scope", "") in ("", "template_applied", "runtime_verified"), "INVALID_OPERATION_RESULT")
    return result


def configuration_projection(reply, instance_id):
    require(reply.get("instance_id") == instance_id and isinstance(reply.get("workspace_mount_path"), str), "INVALID_CONFIGURATION_PROTOCOL")
    mounts = reply.get("volume_mounts")
    require(isinstance(mounts, list), "CURRENT_MOUNTS_MUST_BE_AN_ARRAY")
    result = {"instance_id": instance_id, "version": decimal(reply.get("version")), "applied_mount_version": decimal(reply.get("applied_mount_version"), allow_zero=True), "workspace_mount_path": reply["workspace_mount_path"], "volume_mounts": []}
    for mount in mounts:
        require(isinstance(mount, dict) and all(isinstance(mount.get(key), str) for key in ("pvc_name", "mount_path")) and isinstance(mount.get("read_only"), bool), "INVALID_CURRENT_MOUNT")
        result["volume_mounts"].append({key: mount[key] for key in ("pvc_name", "mount_path", "read_only")})
    paths = [result["workspace_mount_path"]] + [mount["mount_path"] for mount in result["volume_mounts"]]
    require(len(paths) == len(set(paths)), "DUPLICATE_CURRENT_MOUNT_PATH")
    # env/labels/annotations and all unknown fields are deliberately not projected.
    return result


def get_instance(session, instance_id):
    result = instance_projection(session.request("GET", "/api/v1/instances/" + instance_id))
    require(result["id"] == instance_id, "INSTANCE_ID_MISMATCH")
    return result


def submit_and_wait(session, evidence, name, action, instance_id, body, args):
    version = None if action == "create" else decimal(body["data"]["expectedVersion"])
    intent = evidence.intent(name, action, body, version)
    path = "/api/v1/instances" if action == "create" else "/api/v1/instances/" + instance_id + ":" + action
    ack = session.request("POST", path, body, expected=202)
    canonical_uuid(ack.get("operationId"))
    canonical_uuid(ack.get("resourceId"))
    require(isinstance(ack.get("replayed"), bool), "INVALID_ACK_PROTOCOL")
    require(ack.get("result") in ({"ACCEPTED", "QUEUED_FOR_OWNER"} if action == "create" else {"ACCEPTED"}), "OWNER_ACK_NOT_ACCEPTED")
    if instance_id:
        require(ack["resourceId"] == instance_id, "ACK_RESOURCE_MISMATCH")
    if intent.get("ack"):
        require(ack["operationId"] == intent["ack"]["operationId"] and ack["resourceId"] == intent["ack"]["resourceId"] and ack["replayed"] is True, "REPLAY_ACK_CHANGED")
    safe_ack = {key: ack[key] for key in ("operationId", "resourceId", "replayed", "result")}
    intent["ack"] = safe_ack
    evidence.state["instance_id"] = ack["resourceId"]
    evidence.save()
    evidence.event("acceptance", step=name, action=action, **safe_ack)
    deadline, previous = time.monotonic() + args.operation_timeout, None
    while True:
        op = operation_projection(session.request("GET", "/api/v1/instance-operations/" + ack["operationId"]))
        require(op["id"] == ack["operationId"] and op["instanceId"] == ack["resourceId"] and op.get("action") == action, "OPERATION_IDENTITY_MISMATCH")
        if op != previous:
            evidence.event("operation", step=name, **op)
            intent["operation"] = op
            evidence.save()
            previous = op
        if op["state"] in TERMINAL:
            require(op["state"] == "completed", "OPERATION_TERMINATED_UNSUCCESSFULLY")
            if version:
                require(int(op["intentVersion"]) == int(version) + 1, "OPERATION_INTENT_VERSION_MISMATCH")
            return ack["resourceId"], op
        require(time.monotonic() < deadline, "OPERATION_WAIT_TIMEOUT")
        time.sleep(args.poll_interval)


def mount_expectations(configuration):
    result = [{"mount_path": configuration["workspace_mount_path"], "read_only": False, "ownership": "workspace"}]
    result.extend({"mount_path": mount["mount_path"], "read_only": mount["read_only"], "ownership": "borrowed", "pvc_name": mount["pvc_name"]} for mount in configuration["volume_mounts"])
    return result


def observation(hook, event, evidence, instance_id, configuration, timeout, before=None, operation_id=None):
    marker_text = "ani-container-restart:" + evidence.state["run_id"] + "\n"
    request = {"schema": "container-restart-live-observation-v1", "event": event, "run_id": evidence.state["run_id"], "instance_id": instance_id, "operation_id": operation_id, "mounts": mount_expectations(configuration), "marker_name": ".ani-restart-" + evidence.state["run_id"] + ".txt", "marker_content": marker_text, "marker_sha256": hashlib.sha256(marker_text.encode()).hexdigest(), "old_pod": {"name": before["pod_name"], "uid": before["pod_uid"]} if before else None}
    executable = Path(hook)
    require(executable.is_absolute() and executable.is_file() and os.access(executable, os.X_OK), "ABSOLUTE_EXECUTABLE_HOOK_REQUIRED")
    # Explicit observer scope/relay configuration is separate from Gov login.
    # The hook validates these values; no arbitrary environment is inherited.
    environment_keys = ("PATH", "HOME", "LANG", "TZ", "SSH_AUTH_SOCK", "SSH_AGENT_PID", "ANI_ACCEPTANCE_TENANT_NAMESPACE", "ANI_ACCEPTANCE_SSH_HOST", "ANI_ACCEPTANCE_SSH_PORT", "ANI_ACCEPTANCE_SSH_KNOWN_HOSTS", "ANI_ACCEPTANCE_OBSERVER_CONFIG", "ANI_ACCEPTANCE_SSH_BRIDGE_URL", "ANI_ACCEPTANCE_SSH_BRIDGE_TOKEN_FILE")
    environment = {key: os.environ[key] for key in environment_keys if key in os.environ}
    try:
        process = subprocess.run([str(executable)], input=body_bytes(request), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, env=environment, check=False)
    except (subprocess.TimeoutExpired, OSError):
        raise AcceptanceError("LIVE_HOOK_UNAVAILABLE") from None
    require(process.returncode == 0, "LIVE_HOOK_FAILED")
    require(len(process.stdout) <= MAX_BYTES, "LIVE_HOOK_RESPONSE_TOO_LARGE")
    raw = object_json(process.stdout)
    require(raw.get("source") == "live" and raw.get("instance_id") == instance_id and raw.get("run_id") == evidence.state["run_id"], "LIVE_HOOK_IDENTITY_MISMATCH")
    observed_at = raw.get("observed_at")
    require(isinstance(observed_at, str) and len(observed_at) <= 40, "LIVE_OBSERVATION_TIME_REQUIRED")
    try:
        observed_time = dt.datetime.fromisoformat(observed_at.replace("Z", "+00:00"))
        require(observed_time.tzinfo is not None, "LIVE_OBSERVATION_TIME_REQUIRED")
    except ValueError:
        raise AcceptanceError("LIVE_OBSERVATION_TIME_REQUIRED") from None
    result = {"source": "live", "instance_id": instance_id, "run_id": raw["run_id"], "observed_at": observed_at, "hook_sha256": hashlib.sha256(executable.read_bytes()).hexdigest()}
    for key in ("notebook_uid", "workspace_pvc_uid", "pod_uid", "pod_name"):
        require(isinstance(raw.get(key), str) and bool(raw[key]) and len(raw[key]) <= 253, "LIVE_OBJECT_IDENTITY_MISSING")
        if key != "pod_name":
            canonical_uuid(raw[key])
        result[key] = raw[key]
    expected_mounts = {mount["mount_path"]: mount for mount in request["mounts"]}
    result["markers"], result["mount_identities"] = {}, {}
    for marker in raw.get("markers", []):
        require(isinstance(marker, dict) and marker.get("mount_path") in expected_mounts, "UNEXPECTED_LIVE_MARKER")
        path = marker["mount_path"]
        require(path not in result["markers"] and isinstance(marker.get("marker_name"), str) and 0 < len(marker["marker_name"]) <= 253 and isinstance(marker.get("sha256"), str) and SHA256.fullmatch(marker["sha256"]) and marker.get("read_verified") is True, "INVALID_LIVE_MARKER_PROOF")
        if event == "before-restart" and not expected_mounts[path]["read_only"]:
            require(marker.get("write_verified") is True and marker["marker_name"] == request["marker_name"] and marker["sha256"] == request["marker_sha256"], "MARKER_WRITE_NOT_PROVEN")
        result["markers"][path] = {key: marker[key] for key in ("marker_name", "sha256", "read_verified")}
    for mount in raw.get("mount_identities", []):
        require(isinstance(mount, dict) and mount.get("mount_path") in expected_mounts and isinstance(mount.get("pvc_uid"), str) and 0 < len(mount["pvc_uid"]) <= 253, "INVALID_LIVE_PVC_IDENTITY")
        path = mount["mount_path"]
        require(path not in result["mount_identities"], "DUPLICATE_LIVE_PVC_IDENTITY")
        canonical_uuid(mount["pvc_uid"])
        result["mount_identities"][path] = {"pvc_uid": mount["pvc_uid"]}
    require(set(result["markers"]) == set(expected_mounts) == set(result["mount_identities"]), "CURRENT_MOUNT_PROOF_INCOMPLETE")
    require(result["mount_identities"][configuration["workspace_mount_path"]]["pvc_uid"] == result["workspace_pvc_uid"], "WORKSPACE_IDENTITY_MISMATCH")
    if before:
        old = raw.get("old_pod_get")
        require(isinstance(old, dict) and {"name", "expected_uid", "method", "not_found", "observed_uid"} <= set(old) and old.get("name") == before["pod_name"] and old.get("expected_uid") == before["pod_uid"] and old.get("method") == "direct_get", "OLD_POD_DIRECT_GET_REQUIRED")
        require((old.get("not_found") is True and old.get("observed_uid") is None) or (old.get("not_found") is False and isinstance(old.get("observed_uid"), str) and bool(old["observed_uid"]) and old["observed_uid"] != before["pod_uid"]), "OLD_POD_EXIT_NOT_PROVEN")
        result["old_pod_get"] = {key: old[key] for key in ("name", "expected_uid", "method", "not_found", "observed_uid")}
    return result


def verify_restart(before, after):
    require(before["instance_id"] == after["instance_id"] and before["run_id"] == after["run_id"], "RESTART_OBSERVATION_MISMATCH")
    require(before["notebook_uid"] == after["notebook_uid"] and before["workspace_pvc_uid"] == after["workspace_pvc_uid"], "RESTART_CHANGED_PERSISTENT_OBJECT")
    require(before["pod_uid"] != after["pod_uid"], "RESTART_DID_NOT_CHANGE_POD_UID")
    old = after.get("old_pod_get", {})
    require(old.get("method") == "direct_get" and old.get("name") == before["pod_name"] and old.get("expected_uid") == before["pod_uid"], "OLD_POD_DIRECT_GET_REQUIRED")
    require((old.get("not_found") is True and old.get("observed_uid") is None) or (old.get("not_found") is False and isinstance(old.get("observed_uid"), str) and bool(old["observed_uid"]) and old["observed_uid"] != before["pod_uid"]), "OLD_POD_EXIT_NOT_PROVEN")
    require(bool(before["markers"]) and before["markers"] == after["markers"], "RESTART_DATA_HASH_CHANGED")
    require(before["mount_identities"] == after["mount_identities"], "RESTART_CHANGED_CURRENT_PVC_IDENTITY")


def restart_cpu(session, evidence, args):
    state = evidence.state
    if args.create_input:
        body = private_json(args.create_input)
        require(isinstance(body.get("data"), dict) and set(body) == {"data"}, "CREATE_DATA_OBJECT_REQUIRED")
        instance_id, _ = submit_and_wait(session, evidence, "create", "create", None, body, args)
    else:
        require(args.instance_id and args.expected_version, "REUSE_REQUIRES_INSTANCE_AND_VERSION")
        instance_id = canonical_uuid(args.instance_id)
        require(state.get("instance_id", instance_id) == instance_id, "EVIDENCE_INSTANCE_MISMATCH")
        state["instance_id"] = instance_id
    initial = get_instance(session, instance_id)
    require(initial.get("instance_kind") == "container" and initial.get("compute_type") == "cpu", "CP01_CPU_INSTANCE_REQUIRED")
    intent = state["intents"].get("restart")
    if intent is None:
        if args.expected_version:
            require(initial["version"] == decimal(args.expected_version), "INITIAL_VERSION_MISMATCH")
        require(initial.get("desiredState") == "running" and initial.get("observedState") == "running" and initial.get("executionRunState") == "running" and not initial.get("activeOperationId"), "INITIAL_RUNNING_STABILITY_REQUIRED")
        configuration = configuration_projection(session.request("GET", "/api/v1/instances/" + instance_id + "/configuration"), instance_id)
        require(configuration["version"] == initial["version"] and int(configuration["applied_mount_version"]) > 0, "INITIAL_CONFIGURATION_NOT_PROVEN")
        state["before_instance"], state["before_configuration"] = initial, configuration
        if args.observation_hook:
            state["before_observation"] = observation(args.observation_hook, "before-restart", evidence, instance_id, configuration, args.hook_timeout)
            evidence.event("runtime_before_restart", **state["before_observation"])
        evidence.save()
        version = initial["version"]
    else:
        version = intent["expected_version"]
        require(not args.expected_version or args.expected_version == version, "ORIGINAL_EXPECTED_VERSION_CHANGED")
    instance_id, operation = submit_and_wait(session, evidence, "restart", "restart", instance_id, {"data": {"expectedVersion": version}}, args)
    require(operation.get("phase") == "done" and operation.get("completion_scope") == "runtime_verified" and operation.get("final_run_state") == "running", "RESTART_COMPLETION_PROOF_MISSING")
    current = get_instance(session, instance_id)
    require(current["version"] == operation["intentVersion"] and current.get("desiredState") == "running" and current.get("observedState") == "running" and current.get("executionRunState") == "running" and not current.get("activeOperationId"), "RESTART_FINAL_INSTANCE_NOT_STABLE")
    configuration = configuration_projection(session.request("GET", "/api/v1/instances/" + instance_id + "/configuration"), instance_id)
    before_configuration = state["before_configuration"]
    require(all(configuration[key] == before_configuration[key] for key in ("workspace_mount_path", "volume_mounts", "applied_mount_version")), "RESTART_CHANGED_CURRENT_CONFIGURATION")
    evidence.event("instance_after_restart", **current)
    state["after_instance"], state["after_configuration"] = current, configuration
    if args.observation_hook and state.get("before_observation"):
        after = observation(args.observation_hook, "after-restart", evidence, instance_id, configuration, args.hook_timeout, state["before_observation"], operation["id"])
        verify_restart(state["before_observation"], after)
        state["after_observation"] = after
        evidence.event("runtime_after_restart", **after)
        result = "PASS"
    else:
        result = "NOT_VERIFIED"
    state["scenario_result"] = {"scenario": "CP01_CPU_RESTART", "result": result, "operation": "COMPLETED", "runtime_data_proof": "VERIFIED" if result == "PASS" else "NOT_VERIFIED"}
    evidence.save()
    return 0 if result == "PASS" else 3


def individual_command(session, evidence, args):
    instance_id = canonical_uuid(args.instance_id)
    before = get_instance(session, instance_id)
    require(before.get("compute_type") == args.compute_type and before.get("instance_kind") == "container", "INSTANCE_COMPUTE_SCOPE_MISMATCH")
    body = {"data": {"expectedVersion": decimal(args.expected_version)}}
    if args.action in ("attach-pvc", "detach-pvc"):
        require(args.mount_path, "MOUNT_PATH_REQUIRED")
        body["data"].update(mount_path=args.mount_path, restart_if_running=args.restart_if_running)
        if args.action == "attach-pvc":
            require(args.pvc_name, "PVC_NAME_REQUIRED")
            body["data"].update(pvc_name=args.pvc_name, read_only=args.read_only)
    else:
        require(not args.mount_path and not args.pvc_name and not args.read_only and not args.restart_if_running, "ARGUMENT_NOT_APPLICABLE")
    instance_id, operation = submit_and_wait(session, evidence, "command", args.action, instance_id, body, args)
    current = get_instance(session, instance_id)
    evidence.event("instance_after_command", **current)
    evidence.state["after_instance"] = current
    evidence.state["scenario_result"] = {"scenario": "INDIVIDUAL_COMMAND", "action": args.action, "result": "OPERATION_COMPLETED", "runtime_data_proof": "NOT_VERIFIED", "operation_id": operation["id"]}
    evidence.save()
    return 0


def arguments():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--credentials-file", required=True)
    parser.add_argument("--evidence-dir", required=True)
    parser.add_argument("--run-id")
    parser.add_argument("--ca-file")
    parser.add_argument("--origin", help="Optional actual allowlisted auth origin; omitted for the supported non-browser login contract")
    parser.add_argument("--check-platform-login", action="store_true", help="Separately verify and log out the source-defined platform login; no platform business mutation")
    parser.add_argument("--http-timeout", type=float, default=30)
    parser.add_argument("--operation-timeout", type=float, default=2100)
    parser.add_argument("--poll-interval", type=float, default=2)
    sub = parser.add_subparsers(dest="mode", required=True)
    cpu = sub.add_parser("restart-cpu", help="CP-01 CPU creation/reuse, Restart, UID and data proof")
    selection = cpu.add_mutually_exclusive_group(required=True)
    selection.add_argument("--create-input")
    selection.add_argument("--instance-id")
    cpu.add_argument("--expected-version")
    cpu.add_argument("--observation-hook")
    cpu.add_argument("--hook-timeout", type=float, default=60)
    command = sub.add_parser("command", help="One explicit durable action, with original-intent resume")
    command.add_argument("--action", choices=("restart", "stop", "attach-pvc", "start", "detach-pvc"), required=True)
    command.add_argument("--instance-id", required=True)
    command.add_argument("--expected-version", required=True)
    command.add_argument("--compute-type", choices=("cpu", "gpu"), required=True)
    command.add_argument("--pvc-name")
    command.add_argument("--mount-path")
    command.add_argument("--read-only", action="store_true")
    command.add_argument("--restart-if-running", action="store_true")
    args = parser.parse_args()
    require(0 < args.http_timeout <= 60 and 0 < args.operation_timeout <= 86400 and 0 < args.poll_interval <= 60, "INVALID_BOUNDED_TIMEOUT")
    if getattr(args, "instance_id", None):
        canonical_uuid(args.instance_id)
    if args.expected_version:
        decimal(args.expected_version)
    if args.mode == "restart-cpu":
        require(0 < args.hook_timeout <= 900, "INVALID_BOUNDED_TIMEOUT")
        require(not args.create_input or not args.expected_version, "CREATE_VERSION_IS_RETURNED_BY_OWNER")
        require(not args.instance_id or args.expected_version, "REUSE_REQUIRES_INSTANCE_AND_VERSION")
    return args


def main():
    evidence, sessions = None, []
    result = 2
    cleanup = []
    try:
        args = arguments()
        base_url = origin_url(args.base_url)
        evidence = Evidence(args.evidence_dir, base_url, args.run_id)
        require(evidence.state.get("mode", args.mode) == args.mode, "EVIDENCE_MODE_MISMATCH")
        evidence.state["mode"] = args.mode
        evidence.state["driver_sha256"] = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
        evidence.save()
        credentials = private_json(args.credentials_file)
        require(set(credentials) <= {"tenant", "platform"} and "tenant" in credentials, "INVALID_PRIVATE_CREDENTIAL_SHAPE")
        if args.check_platform_login:
            platform = Session(base_url, "boss", credentials.get("platform"), evidence, args.http_timeout, args.ca_file, args.origin)
            sessions.append(platform)
            platform.login()
        tenant = Session(base_url, "console", credentials["tenant"], evidence, args.http_timeout, args.ca_file, args.origin)
        sessions.append(tenant)
        tenant.login()
        credentials = None
        result = restart_cpu(tenant, evidence, args) if args.mode == "restart-cpu" else individual_command(tenant, evidence, args)
    except AcceptanceError as error:
        if evidence:
            evidence.event("stopped", code=error.code, **error.facts)
        print(json.dumps({"result": "STOPPED", "code": error.code, **error.facts}), file=sys.stderr)
    except KeyboardInterrupt:
        if evidence:
            evidence.event("stopped", code="INTERRUPTED_ORIGINAL_INTENT_PRESERVED")
        print('{"result":"STOPPED","code":"INTERRUPTED_ORIGINAL_INTENT_PRESERVED"}', file=sys.stderr)
        result = 130
    except (OSError, ValueError, TypeError, KeyError):
        if evidence:
            evidence.event("stopped", code="LOCAL_OR_PROTOCOL_INPUT_ERROR")
        print('{"result":"STOPPED","code":"LOCAL_OR_PROTOCOL_INPUT_ERROR"}', file=sys.stderr)
    finally:
        for session in reversed(sessions):
            try:
                remote = session.close()
            except (AcceptanceError, OSError, ValueError, TypeError, KeyError):
                session.token, session.credentials = None, None
                session.cookies.clear()
                remote = "FAILED"
            cleanup.append({"application": session.application, "remote": remote})
        if evidence:
            evidence.state["session_cleanup"] = cleanup
            try:
                evidence.save()
            except OSError:
                result = 2
            print(json.dumps({"scenario": evidence.state.get("scenario_result"), "session_cleanup": cleanup, "evidence_dir": str(evidence.directory)}))
    if result == 0 and any(item["remote"] not in ("CONFIRMED", "NOT_STARTED") for item in cleanup):
        return 4
    return result


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (AcceptanceError, OSError, ValueError, TypeError, KeyError):
        print('{"result":"STOPPED","code":"LOCAL_STORAGE_OR_PROTOCOL_ERROR"}', file=sys.stderr)
        sys.exit(2)

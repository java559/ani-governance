#!/usr/bin/env python3
"""Explicit, staged migration of the supplied ANI deployment profile.

Requires kubectl and PyYAML. Does not install cert-manager, rotate the root,
replace application images, or modify business configuration/credentials.
"""
import argparse
import base64
import json
import pathlib
import re
import subprocess
import time

import yaml

HERE = pathlib.Path(__file__).resolve().parent


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", required=True)
    parser.add_argument("--namespace", default="ani-system")
    parser.add_argument("--phase", required=True, choices=["issue", "trust", "identities", "finalize"])
    parser.add_argument("--issuer", default="ani-ca")
    parser.add_argument("--legacy-ca-secret", action="append", default=[], metavar="NAME:KEY")
    args = parser.parse_args()
    kube = ["kubectl", "--context", args.context, "--namespace", args.namespace, "--request-timeout=30s"]

    def call(argv, body=None):
        result = subprocess.run(kube + argv, input=json.dumps(body) if body is not None else None,
                                text=True, capture_output=True, check=True)
        return result.stdout

    def get(kind, name):
        return json.loads(call(["get", kind, name, "-o", "json"]))

    certificates = list(yaml.safe_load_all((HERE / "certificates.yaml").read_text()))
    if args.phase == "issue":
        issuer = get("clusterissuer", args.issuer)
        if not any(c["type"] == "Ready" and c["status"] == "True" for c in issuer.get("status", {}).get("conditions", [])):
            raise RuntimeError("existing ClusterIssuer is not Ready")
        for certificate in certificates:
            certificate["metadata"]["namespace"] = args.namespace
            certificate["spec"]["issuerRef"]["name"] = args.issuer
            print(call(["apply", "-f", "-"], certificate).strip())
        return

    for certificate in certificates:
        actual = get("certificate", certificate["metadata"]["name"])
        if actual["spec"]["issuerRef"]["name"] != args.issuer or not any(
                c["type"] == "Ready" and c["status"] == "True" for c in actual.get("status", {}).get("conditions", [])):
            raise RuntimeError("certificate must be Ready under the selected issuer: " + actual["metadata"]["name"])

    filename = "trust-patches.yaml" if args.phase == "trust" else "workload-patches.yaml"
    patches = list(yaml.safe_load_all((HERE / filename).read_text()))
    # Validate the active identity profile before removing any old trust root.
    if args.phase == "finalize":
        for patch in patches:
            actual = get(patch["kind"], patch["metadata"]["name"])
            status = actual.get("status", {})
            desired = actual["spec"].get("replicas", status.get("desiredNumberScheduled"))
            ready = status.get("readyReplicas", status.get("numberReady", 0))
            updated = status.get("updatedReplicas", status.get("updatedNumberScheduled", 0))
            if status.get("observedGeneration") != actual["metadata"]["generation"] or ready != desired or updated != desired:
                raise RuntimeError("identity rollout is not Ready: " + actual["metadata"]["name"])
            mounted = actual["spec"]["template"]["spec"]["volumes"]
            for expected in patch["spec"]["template"]["spec"]["volumes"]:
                candidate = next(v for v in mounted if v["name"] == expected["name"])
                if candidate.get("projected", {}).get("sources") != expected["projected"]["sources"]:
                    raise RuntimeError("identity profile is not active: " + actual["metadata"]["name"])

    if args.phase in ["trust", "finalize"]:
        root = base64.b64decode(get("secret", "ani-governance-client-tls")["data"]["ca.crt"]).decode()
        bundle = root
        if args.phase == "trust":
            if not args.legacy_ca_secret:
                parser.error("trust requires --legacy-ca-secret for every currently deployed legacy issuer")
            for reference in args.legacy_ca_secret:
                name, key = reference.split(":", 1)
                pem = base64.b64decode(get("secret", name)["data"][key]).decode()
                if not re.fullmatch(r"\s*(?:-----BEGIN CERTIFICATE-----[A-Za-z0-9+/=\s]+-----END CERTIFICATE-----\s*)+", pem):
                    raise RuntimeError("invalid PEM trust source: " + reference)
                if pem not in bundle:
                    bundle += "\n" + pem
        print(call(["apply", "-f", "-"], {"apiVersion": "v1", "kind": "ConfigMap",
                   "metadata": {"name": "ani-internal-service-ca", "namespace": args.namespace},
                   "data": {"ca.crt": bundle}}).strip())

    for patch in patches:
        patch["metadata"]["namespace"] = args.namespace
        print(call(["patch", patch["kind"], patch["metadata"]["name"], "--type=strategic", "-p", json.dumps(patch)]).strip())
    # These services load identity files at startup, including init-copy paths.
    # Explicit restarts also load a trust-only finalization when mounts are unchanged.
    if args.phase == "finalize":
        for patch in patches:
            print(call(["rollout", "restart", patch["kind"] + "/" + patch["metadata"]["name"]]).strip())
    pending = {(p["kind"], p["metadata"]["name"]) for p in patches}
    deadline = time.monotonic() + 300
    while pending and time.monotonic() < deadline:
        for kind, name in list(pending):
            obj = get(kind, name)
            status = obj.get("status", {})
            desired = obj["spec"].get("replicas", status.get("desiredNumberScheduled"))
            ready = status.get("readyReplicas", status.get("numberReady", 0))
            updated = status.get("updatedReplicas", status.get("updatedNumberScheduled", 0))
            if status.get("observedGeneration") == obj["metadata"]["generation"] and ready == updated == desired:
                pending.remove((kind, name))
        if pending:
            time.sleep(2)
    if pending:
        raise RuntimeError("rollout incomplete: " + repr(sorted(pending)))
    print("all selected workloads are Ready")


if __name__ == "__main__":
    main()

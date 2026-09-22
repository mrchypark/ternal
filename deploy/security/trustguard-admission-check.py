#!/usr/bin/env python3
"""Exercise the rendered policy on a real API server in a disposable namespace."""
import json
import copy
import subprocess
import sys
import uuid

import render_trustguard as tg


def kubectl(*args, data=None):
    return subprocess.run(["kubectl", *args], input=json.dumps(data) if data else None,
                          text=True, capture_output=True)


def require(result):
    if result.returncode:
        raise RuntimeError(result.stderr)
    return result


def main(image):
    namespace = "ternal-admission-" + uuid.uuid4().hex[:10]
    doc = tg.render(namespace, "api", "anchor", namespace, "a" * 64, [image],
                    anchor_service_account="recovery")
    policies = [x for x in doc["items"] if x["kind"].startswith("ValidatingAdmission")]
    def read():
        return json.loads(require(kubectl("-n", namespace, "get", "configmap/anchor", "-o", "json")).stdout)

    def write(data, allowed=True, identity="api", dry=False):
        cm = read()
        cm["data"] = data
        args = ["-n", namespace, "--as=system:serviceaccount:" + namespace + ":" + identity, "replace", "-f", "-"]
        if dry or not allowed:
            args += ["--dry-run=server"]
        result = kubectl(*args, data=cm)
        if allowed:
            require(result)
        else:
            assert result.returncode and "denied" in result.stderr.lower(), result.stderr or result.stdout

    try:
        require(kubectl("apply", "-f", "-", data=doc))
        require(kubectl("-n", namespace, "create", "serviceaccount", "recovery"))
        def workload(spec, allowed):
            for kind in ("Pod", "Deployment"):
                obj = {"apiVersion": "v1" if kind == "Pod" else "apps/v1", "kind": kind,
                       "metadata": {"name": "identity-probe", "namespace": namespace}}
                obj["spec"] = spec if kind == "Pod" else {
                    "selector": {"matchLabels": {"app": "probe"}},
                    "template": {"metadata": {"labels": {"app": "probe"}}, "spec": spec}}
                result = kubectl("create", "--dry-run=server", "-f", "-", data=obj)
                if allowed:
                    require(result)
                else:
                    assert result.returncode and "denied" in result.stderr.lower(), result.stderr or result.stdout

        # Wait for enforcement by probing a forbidden reset, without mutating it.
        import time
        for _ in range(30):
            result = kubectl("-n", namespace, "delete", "configmap/anchor", "--dry-run=server")
            if result.returncode and "denied" in result.stderr.lower():
                break
            time.sleep(1)
        else:
            raise AssertionError("DELETE guard never became active")
        anchor = {"serviceAccountName": "recovery", "containers": [{
            "name": tg.ANCHOR_CONTAINER, "image": image, "command": [tg.ANCHOR_EXECUTABLE],
            "env": [{"name": name, "value": value} for name, value in (
                ("TERNAL_RECOVERY_ANCHOR_ID", "anchor"), ("TERNAL_TRUST_ANCHOR_CONFIGMAP", "anchor"),
                ("TERNAL_TRUST_ANCHOR_NAMESPACE", namespace))]}]}
        workload(anchor, True)
        for probe in ("livenessProbe", "readinessProbe", "startupProbe"):
            execution = copy.deepcopy(anchor)
            execution["containers"][0][probe] = {"exec": {"command": ["/tmp/helper"]}}
            workload(execution, False)
        rogue = copy.deepcopy(anchor)
        rogue["containers"] = [{"name": "rogue", "image": "busybox:1.37"}]
        workload(rogue, False)
        sidecar = copy.deepcopy(anchor)
        sidecar["containers"].append(rogue["containers"][0])
        workload(sidecar, False)
        init = copy.deepcopy(anchor)
        init["initContainers"] = rogue["containers"]
        workload(init, False)
        overlay = copy.deepcopy(anchor)
        overlay["volumes"] = [{"name": "replace", "emptyDir": {}}]
        overlay["containers"][0]["volumeMounts"] = [{"name": "replace", "mountPath": "/usr/local/bin"}]
        workload(overlay, False)
        endpoint = copy.deepcopy(anchor)
        endpoint["containers"][0]["env"].append({"name": "KUBERNETES_SERVICE_HOST", "value": "untrusted.invalid"})
        workload(endpoint, False)
        indirect = copy.deepcopy(anchor)
        indirect["containers"][0]["envFrom"] = [{"configMapRef": {"name": "untrusted"}}]
        workload(indirect, False)
        d = read()["data"]
        d.update(recoveryGeneration="0", recoveryEvidence="evidence", recoveryTransition="null", recoveryReceipt="null")
        write(d)  # Exact legacy migration.
        pending = dict(d, pendingEpoch="1", pendingID=str(uuid.uuid4()))
        for key, value in (("recoveryGeneration", "1"), ("recoveryTransition", '{}'), ("recoveryReceipt", '{}')):
            write(dict(pending, **{key: value}), allowed=False)
        write(pending)  # Application reserve.
        final = dict(d, epoch="1", token=pending["pendingID"], bootstrap="false", recoveryEvidence="new-evidence")
        write(dict(final, clusterID="foreign"), allowed=False)
        write(dict(d, clusterID="foreign"), allowed=False)  # Abort cannot rebind either.
        write(d)  # Application abort.
        write(pending)
        write(final)  # Application finalize.
        write(dict(final, epoch="0"), allowed=False)
        write(dict(final, bootstrap="true"), allowed=False)
        write({k: v for k, v in final.items() if k in tg.FLAT_FIELDS}, allowed=False)
        reserved = dict(final, recoveryTransition='{"operation_id":"test"}')
        write(reserved, allowed=False)  # API identity cannot activate recovery.
        write(reserved, identity="recovery")
        write(dict(reserved, pendingEpoch="2", pendingID=str(uuid.uuid4())), allowed=False)
        committed = dict(final, clusterID="target", storageID="b" * 64,
                         recoveryGeneration="1", recoveryReceipt='{"operation_id":"test"}')
        write(committed, allowed=False)
        write(committed, identity="recovery")
        print("Live admission matrix passed: migration, reserve, abort, finalize, recovery; forbidden transitions denied")
    finally:
        for item in policies:
            require(kubectl("delete", item["kind"] + "/" + item["metadata"]["name"], "--ignore-not-found"))
        require(kubectl("delete", "namespace", namespace, "--ignore-not-found", "--wait=false"))


if __name__ == "__main__":
    main(sys.argv[1])

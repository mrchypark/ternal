#!/usr/bin/env python3
"""Exercise the rendered policy on a real API server in a disposable namespace."""
import json
import copy
import subprocess
import sys
import uuid
import argparse
from pathlib import Path
from urllib.parse import urlparse

import render_trustguard as tg


def kubectl(*args, data=None):
    return subprocess.run(["kubectl", *args], input=json.dumps(data) if data else None,
                          text=True, capture_output=True)


def require(result):
    if result.returncode:
        raise RuntimeError(result.stderr)
    return result


def safe_typecheck_records(policy, warnings):
    """Select compiler status only, never the policy object, spec or parameters."""
    if not isinstance(policy, str) or not isinstance(warnings, list):
        raise ValueError("invalid type-check diagnostic status")
    records = []
    for item in warnings[:64]:
        if not isinstance(item, dict) or not isinstance(item.get("fieldRef"), str) or not isinstance(item.get("warning"), str):
            raise ValueError("invalid type-check diagnostic record")
        warning = item["warning"]
        if len(warning) > 32768:
            warning = warning[:32740] + " [diagnostic truncated]"
        records.append({"policy": policy[:253], "fieldRef": item["fieldRef"][:512], "warning": warning})
    return records


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


def local_matrix(image, kubeconfig, context, diagnostics):
    """Dry-run the Local envelope on an explicitly owned loopback API only."""
    import time
    directory = Path(diagnostics)
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    sequence = 0

    def run(label, *args, data=None):
        nonlocal sequence
        sequence += 1
        command = ["kubectl", "--kubeconfig", kubeconfig, "--context", context, *args]
        try:
            result = subprocess.run(command, input=json.dumps(data) if data is not None else None,
                                    text=True, capture_output=True, timeout=45)
        except subprocess.TimeoutExpired as error:
            def decoded(value):
                return value.decode(errors="replace") if isinstance(value, bytes) else value or ""
            result = subprocess.CompletedProcess(command, 124, decoded(error.stdout), decoded(error.stderr) + "\ncommand timed out after 45s\n")
        stem = directory / ("%03d-%s" % (sequence, label))
        stem.with_suffix(".stdout").write_text(result.stdout)
        stem.with_suffix(".stderr").write_text(result.stderr)
        stem.with_suffix(".exit").write_text(str(result.returncode) + "\n")
        return result

    server = require(run("endpoint", "config", "view", "--minify", "-o", "jsonpath={.clusters[0].cluster.server}")).stdout
    if not context.startswith("kind-ternal-local-admission-") or urlparse(server).hostname not in ("127.0.0.1", "localhost", "::1"):
        raise ValueError("Local admission tests require an explicitly owned kind loopback context")
    namespace = "ternal-local-admission-" + uuid.uuid4().hex[:10]
    workload = "ternal-local"
    doc = tg.render(namespace, "api", "anchor", namespace, "a" * 64, [image],
                    anchor_service_account="recovery", data_mode="local", local_workload_name=workload)
    single_workload = "ternal-single"
    standalone_doc = tg.render(namespace, "api", "anchor", namespace, "a" * 64, [image],
                               anchor_service_account="recovery", standalone_workload_name=single_workload)
    # Compile and execute the new exact-name scale guard on the same owned API;
    # retain the existing Local envelope and never schedule application code.
    doc["items"].extend(item for item in standalone_doc["items"] if "standalone-scale" in item["metadata"]["name"])
    policies = [item for item in doc["items"] if item["kind"] == "ValidatingAdmissionPolicy"]
    bindings = [item for item in doc["items"] if item["kind"] == "ValidatingAdmissionPolicyBinding"]
    envelope = "Local recovery requires the exact approved non-root initializer, matching shared data configuration, and isolated staging"
    scale_message = "the owned Local workload permits only zero or one replica; scale is not fencing"
    security = {"runAsNonRoot": True, "runAsUser": 65532, "runAsGroup": 65532,
                "allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True,
                "capabilities": {"drop": ["ALL"]}}
    values = ["1", "0", "/data/ternal", namespace, "node-1", "/staging", "/staging/preflight.json", "1",
              "anchor", namespace, "s3", "http://127.0.0.1:1", "disposable-test", "clusters/test", "us-east-1", "true", "before-ack"]
    names = ["TERNAL_DATA_LOCAL", "TERNAL_DATA_MULTI_NODE", "TERNAL_DATA_DIR", "TERNAL_DATA_CLUSTER_ID", "TERNAL_DATA_NODE_ID",
             "TERNAL_LOCAL_PREFLIGHT_STAGING_DIR", "TERNAL_LOCAL_PREFLIGHT_RECEIPT_FILE", "TERNAL_LOCAL_WRITER_GC_QUIESCENCE_ACK",
             "TERNAL_TRUST_ANCHOR_CONFIGMAP", "TERNAL_TRUST_ANCHOR_NAMESPACE", "TERNAL_OBJECT_STORE_PROVIDER",
             "TERNAL_OBJECT_STORE_ENDPOINT", "TERNAL_OBJECT_STORE_BUCKET", "TERNAL_OBJECT_STORE_PREFIX", "TERNAL_OBJECT_STORE_REGION",
             "TERNAL_OBJECT_STORE_INSECURE", "TERNAL_OBJECT_STORE_DURABILITY"]
    data_env = [{"name": name, "value": value} for name, value in zip(names, values)]
    data_env.append({"name": "TERNAL_LOCAL_PREFLIGHT_SCOPE", "valueFrom": {"fieldRef": {"apiVersion": "v1", "fieldPath": "metadata.uid"}}})
    credentials = [{"name": name, "valueFrom": {"secretKeyRef": {"name": "runtime", "key": name, "optional": True}}}
                   for name in ("TERNAL_OBJECT_STORE_ACCESS_KEY", "TERNAL_OBJECT_STORE_SECRET_KEY", "TERNAL_OBJECT_STORE_SESSION_TOKEN")]
    init = {"name": "ternal-local-restore", "image": image, "command": ["/usr/local/bin/ternal-api"], "args": ["local-restore"],
            "securityContext": copy.deepcopy(security), "env": copy.deepcopy(data_env) + credentials,
            "volumeMounts": [{"name": "local-staging", "mountPath": "/staging"}, {"name": "data", "mountPath": "/data"}]}
    api = {"name": "ternal-api", "image": image, "securityContext": copy.deepcopy(security),
           "env": copy.deepcopy(data_env) + [{"name": "TERNAL_REQUIRE_DATA_ADMIN_TOKEN", "value": "0"}, {"name": "TERNAL_OPERATOR_BIND", "value": ""}],
           "envFrom": [{"secretRef": {"name": "runtime"}}],
           "volumeMounts": [{"name": "data", "mountPath": "/data"}, {"name": "local-staging", "mountPath": "/staging", "readOnly": True}]}
    pod_spec = {"serviceAccountName": "api", "securityContext": {"fsGroup": 65532, "seccompProfile": {"type": "RuntimeDefault"}},
                "containers": [api], "initContainers": [init],
                "volumes": [{"name": "data", "emptyDir": {}}, {"name": "local-staging", "emptyDir": {"sizeLimit": "1Gi"}}]}
    fixture = {"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": {"name": workload, "namespace": namespace},
               "spec": {"replicas": 0, "serviceName": workload, "selector": {"matchLabels": {"app": workload}},
                        "updateStrategy": {"type": "OnDelete"}, "template": {"metadata": {"labels": {"app": workload}}, "spec": pod_spec}}}

    def probe(label, obj, message=None):
        result = run(label, "create", "--dry-run=server", "-f", "-", data=obj)
        if message is None:
            require(result)
        elif not (result.returncode and message in result.stderr and "ValidatingAdmissionPolicy" in result.stderr):
            raise AssertionError(label + ": expected exact policy denial: " + result.stderr)

    try:
        require(run("apply-policy", "apply", "-f", "-", data=doc))
        require(run("create-api-sa", "-n", namespace, "create", "serviceaccount", "api"))
        require(run("create-relay-sa", "-n", namespace, "create", "serviceaccount", "relay"))
        typecheck_records = []
        has_typecheck_warnings = False
        for policy in policies:
            name = policy["metadata"]["name"]
            checked = None
            for _ in range(30):
                checked = json.loads(require(run("typecheck", "get", "validatingadmissionpolicy", name, "-o", "json")).stdout)
                if checked.get("status", {}).get("observedGeneration") == checked["metadata"]["generation"]:
                    break
                time.sleep(1)
            else:
                raise AssertionError("policy type-check status did not become current: " + name)
            warnings = checked.get("status", {}).get("typeChecking", {}).get("expressionWarnings", [])
            if warnings:
                has_typecheck_warnings = True
                typecheck_records.extend(safe_typecheck_records(name, warnings))
                typecheck_records = typecheck_records[:64]
                (directory / "cel-typecheck-warnings.json").write_text(json.dumps(typecheck_records))
        if has_typecheck_warnings:
            raise AssertionError("CEL type-check warnings; see cel-typecheck-warnings.json status-only diagnostic")
        time.sleep(2)  # Let newly observed bindings reach the admission dispatcher.
        probe("separate-relay", {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "separate-relay", "namespace": namespace},
                                 "spec": {"serviceAccountName": "relay", "containers": [{"name": "ternal-relay", "image": "busybox:1.37"}]}})
        single = copy.deepcopy(fixture)
        single["metadata"]["name"] = single_workload
        single["spec"]["serviceName"] = single_workload
        single["spec"]["selector"]["matchLabels"] = {"app": single_workload}
        single["spec"]["template"] = {"metadata": {"labels": {"app": single_workload}}, "spec": {
            "serviceAccountName": "relay", "schedulingGates": [{"name": "ternal.dev/admission-test"}],
            "containers": [{"name": "never-executed", "image": "busybox:1.37"}]}}
        single_message = "the owned standalone workload permits only zero or one replica; scale is not fencing"
        for replicas in (0, 1, 2):
            single["spec"]["replicas"] = replicas
            probe("standalone-controller-" + str(replicas), single, single_message if replicas == 2 else None)
        single["spec"]["replicas"] = 0
        require(run("standalone-zero", "create", "-f", "-", data=single))
        single_path = "/apis/apps/v1/namespaces/" + namespace + "/statefulsets/" + single_workload + "/scale"
        for replicas in (None, 0, 1, 2):
            obj = {"apiVersion": "autoscaling/v1", "kind": "Scale", "metadata": {"name": single_workload, "namespace": namespace},
                   "spec": {} if replicas is None else {"replicas": replicas}}
            result = run("standalone-scale-" + str(replicas), "replace", "--raw", single_path + "?dryRun=All", "-f", "-", data=obj)
            if replicas in (None, 0, 1):
                require(result)
            elif not (result.returncode and single_message in result.stderr and "ValidatingAdmissionPolicy" in result.stderr):
                raise AssertionError("standalone scale2 did not reach exact owned policy: " + result.stderr)
        require(run("delete-standalone-zero", "-n", namespace, "delete", "statefulset", single_workload,
                    "--cascade=foreground", "--wait=true", "--timeout=40s"))
        require(run("fixture-zero", "create", "-f", "-", data=fixture))
        live = json.loads(require(run("fixture-get", "-n", namespace, "get", "statefulset", workload, "-o", "json")).stdout)
        scale_path = "/apis/apps/v1/namespaces/" + namespace + "/statefulsets/" + workload + "/scale"
        for replicas in (None, 0, 1, 2):
            scale_spec = {} if replicas is None else {"replicas": replicas}
            obj = {"apiVersion": "autoscaling/v1", "kind": "Scale", "metadata": {"name": workload, "namespace": namespace}, "spec": scale_spec}
            label = "omitted" if replicas is None else str(replicas)
            result = run("scale-" + label, "replace", "--raw", scale_path + "?dryRun=All", "-f", "-", data=obj)
            if replicas in (None, 0, 1):
                require(result)
            elif not (result.returncode and scale_message in result.stderr and "ValidatingAdmissionPolicy" in result.stderr):
                raise AssertionError("scale2 did not reach exact owned scale policy: " + result.stderr)
        base = copy.deepcopy(fixture)
        base["metadata"]["name"] = "duplicate-local"
        probe("duplicate-controller", base, envelope)
        base["spec"]["replicas"] = 2
        probe("duplicate-controller-two", base, envelope)
        pod = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": workload + "-0", "namespace": namespace,
               "ownerReferences": [{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": workload, "uid": live["metadata"]["uid"], "controller": True}]},
               "spec": copy.deepcopy(pod_spec)}
        probe("approved-pod", pod)
        # Azure changes only its public routing fields and exact storage Secret
        # refs; no connection string/key/client secret is an inline value.
        azure = copy.deepcopy(pod)
        azure_public = {
            "TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT": "account123",
            "TERNAL_OBJECT_STORE_AZURE_BLOB_ENDPOINT": "https://account123.blob.core.windows.net",
            "TERNAL_OBJECT_STORE_AZURE_TENANT_ID": "",
            "TERNAL_OBJECT_STORE_AZURE_CLIENT_ID": "",
            "TERNAL_OBJECT_STORE_AZURE_USER_ASSIGNED_ID": "",
            "TERNAL_OBJECT_STORE_AZURE_EMULATOR": "0",
        }
        azure_secrets = ["TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY", "TERNAL_OBJECT_STORE_AZURE_CONNECTION_STRING",
                         "TERNAL_OBJECT_STORE_AZURE_CLIENT_SECRET"]
        s3_secrets = {entry["name"] for entry in credentials}
        for container in azure["spec"]["containers"] + azure["spec"]["initContainers"]:
            for entry in container["env"]:
                if entry["name"] == "TERNAL_OBJECT_STORE_PROVIDER":
                    entry["value"] = "azure"
                elif entry["name"] == "TERNAL_OBJECT_STORE_ENDPOINT":
                    entry["value"] = ""
                elif entry["name"] == "TERNAL_OBJECT_STORE_INSECURE":
                    entry["value"] = "false"
            container["env"].extend({"name": name, "value": value} for name, value in azure_public.items())
            if container["name"] == "ternal-local-restore":
                container["env"] = [entry for entry in container["env"] if entry["name"] not in s3_secrets]
                container["env"].extend({"name": name, "valueFrom": {"secretKeyRef": {"name": "runtime", "key": name, "optional": True}}} for name in azure_secrets)
        probe("approved-azure-pod", azure)
        azure_controller = copy.deepcopy(fixture)
        azure_controller["metadata"]["resourceVersion"] = live["metadata"]["resourceVersion"]
        azure_controller["spec"]["template"]["spec"] = azure["spec"]
        require(run("approved-azure-controller", "replace", "--dry-run=server", "-f", "-", data=azure_controller))
        for secret in azure_secrets:
            inline = copy.deepcopy(azure)
            entries = inline["spec"]["initContainers"][0]["env"]
            entries[:] = [{"name": secret, "value": "not-a-real-secret"} if entry["name"] == secret else entry for entry in entries]
            probe("azure-inline-" + secret.lower(), inline, envelope)
        for field, value in (("name", "other-runtime"), ("key", "OTHER_KEY")):
            wrong = copy.deepcopy(azure)
            next(entry for entry in wrong["spec"]["initContainers"][0]["env"] if entry["name"] == azure_secrets[0])["valueFrom"]["secretKeyRef"][field] = value
            probe("azure-secret-" + field, wrong, envelope)
        for name, value in (("TERNAL_OBJECT_STORE_AZURE_EMULATOR", "1"), ("TERNAL_OBJECT_STORE_ENDPOINT", "generic.invalid"),
                            ("TERNAL_OBJECT_STORE_INSECURE", "true")):
            wrong = copy.deepcopy(azure)
            for container in wrong["spec"]["containers"] + wrong["spec"]["initContainers"]:
                next(entry for entry in container["env"] if entry["name"] == name)["value"] = value
            probe("azure-override-" + name.lower(), wrong, envelope)
        mismatch = copy.deepcopy(azure)
        next(entry for entry in mismatch["spec"]["initContainers"][0]["env"] if entry["name"] == "TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT")["value"] = "otheraccount"
        probe("azure-account-mismatch", mismatch, envelope)
        for label, rejected in (("azure-account-mismatch-controller", mismatch), ("azure-inline-secret-controller", inline)):
            controller = copy.deepcopy(fixture)
            controller["metadata"]["resourceVersion"] = live["metadata"]["resourceVersion"]
            controller["spec"]["template"]["spec"] = rejected["spec"]
            result = run(label, "replace", "--dry-run=server", "-f", "-", data=controller)
            if not (result.returncode and envelope in result.stderr and "ValidatingAdmissionPolicy" in result.stderr):
                raise AssertionError(label + ": expected exact Azure Local policy denial")
        missing = copy.deepcopy(azure)
        missing["spec"]["initContainers"][0]["env"] = [entry for entry in missing["spec"]["initContainers"][0]["env"] if entry["name"] != azure_secrets[0]]
        probe("azure-secret-missing", missing, envelope)
        extra = copy.deepcopy(azure)
        extra["spec"]["initContainers"][0]["env"].append({"name": "TERNAL_OIDC_CLIENT_SECRET", "value": "not-a-real-secret"})
        probe("azure-app-secret-bloat", extra, envelope)
        gcs = copy.deepcopy(pod)
        for container in gcs["spec"]["containers"] + gcs["spec"]["initContainers"]:
            for entry in container["env"]:
                if entry["name"] == "TERNAL_OBJECT_STORE_PROVIDER": entry["value"] = "gcs"
                elif entry["name"] == "TERNAL_OBJECT_STORE_ENDPOINT": entry["value"] = ""
                elif entry["name"] == "TERNAL_OBJECT_STORE_INSECURE": entry["value"] = "false"
        probe("approved-gcs-pod", gcs)
        standalone = copy.deepcopy(pod)
        del standalone["metadata"]["ownerReferences"]
        probe("standalone-pod", standalone, envelope)
        duplicate = copy.deepcopy(pod)
        duplicate["metadata"]["name"] = "duplicate-local-0"
        probe("duplicate-pod", duplicate, envelope)
        mutations = {
            "staging-limit-missing": lambda p: p["volumes"][1]["emptyDir"].pop("sizeLimit"),
            "staging-rw": lambda p: p["containers"][0]["volumeMounts"][1].update(readOnly=False),
            "staging-rw-alias": lambda p: p["containers"][0]["volumeMounts"].append({"name": "local-staging", "mountPath": "/alias"}),
            "staging-subpath": lambda p: p["containers"][0]["volumeMounts"][1].update(subPath="receipt"),
            "staging-subpath-expr": lambda p: p["containers"][0]["volumeMounts"][1].update(subPathExpr="$(POD_NAME)"),
            "receipt-shadow": lambda p: p["containers"][0]["volumeMounts"].append({"name": "data", "mountPath": "/staging/preflight.json"}),
            "init-data": lambda p: p["initContainers"][0]["volumeMounts"].append({"name": "data", "mountPath": "/elsewhere"}),
            "init-data-missing": lambda p: p["initContainers"][0]["volumeMounts"].pop(),
            "init-data-readonly": lambda p: p["initContainers"][0]["volumeMounts"][1].update(readOnly=True),
            "init-data-subpath": lambda p: p["initContainers"][0]["volumeMounts"][1].update(subPath="other"),
            "init-data-shadow": lambda p: p["initContainers"][0]["volumeMounts"].append({"name": "local-staging", "mountPath": "/data/ternal"}),
            "extra-init": lambda p: p["initContainers"].append({"name": "other", "image": "busybox:1.37"}),
            "extra-sidecar": lambda p: p["containers"].append({"name": "rogue", "image": "busybox:1.37"}),
            "sidecar-staging-rw": lambda p: p["containers"].append({"name": "rogue", "image": "busybox:1.37", "volumeMounts": [{"name": "local-staging", "mountPath": "/staging"}]}),
            "init-command": lambda p: p["initContainers"][0].update(args=["serve"]),
            "init-preflight-only": lambda p: p["initContainers"][0].update(args=["local-preflight"]),
            "init-image": lambda p: p["initContainers"][0].update(image=tg.OFFICIAL_IMAGE + "@sha256:" + "b" * 64),
            "init-inline-credential": lambda p: p["initContainers"][0]["env"].__setitem__(-1, {"name": "TERNAL_OBJECT_STORE_SESSION_TOKEN", "value": "test"}),
            "init-ack": lambda p: p["initContainers"][0]["env"][7].update(value="0"),
            "scope-literal": lambda p: p["initContainers"][0]["env"].__setitem__(17, {"name": "TERNAL_LOCAL_PREFLIGHT_SCOPE", "value": "fixed"}),
            "scope-wrong-field": lambda p: p["containers"][0]["env"][17]["valueFrom"]["fieldRef"].update(fieldPath="metadata.name"),
        }
        for label, mutate in mutations.items():
            obj = copy.deepcopy(pod)
            mutate(obj["spec"])
            probe(label, obj, envelope)
            if label in ("extra-sidecar", "sidecar-staging-rw", "staging-limit-missing"):
                controller = copy.deepcopy(fixture)
                # Replace, not create, so the exact owned controller name is
                # retained and the sidecar is the isolated denial condition.
                controller["metadata"]["resourceVersion"] = live["metadata"]["resourceVersion"]
                controller["spec"]["template"]["spec"] = obj["spec"]
                result = run(label + "-controller", "replace", "--dry-run=server", "-f", "-", data=controller)
                if not (result.returncode and envelope in result.stderr and "ValidatingAdmissionPolicy" in result.stderr):
                    raise AssertionError(label + " controller: expected exact policy denial: " + result.stderr)
        token = copy.deepcopy(pod)
        token["spec"]["volumes"].append({"name": "token", "projected": {"sources": [{"serviceAccountToken": {"path": "token"}}]}})
        for container in token["spec"]["containers"] + token["spec"]["initContainers"]:
            container["volumeMounts"].append({"name": "token", "mountPath": "/var/run/secrets/kubernetes.io/serviceaccount", "readOnly": True})
        probe("projected-token", token)
        token["spec"]["volumes"][-1] = {"name": "token", "secret": {"secretName": "fake"}}
        probe("secret-token", token, envelope)
        token["spec"]["volumes"][-1] = {"name": "token", "emptyDir": {}}
        probe("emptydir-token", token, envelope)
        # The ephemeralcontainers subresource needs an existing Pod. A single
        # explicitly gated controller fixture cannot be assigned a node or pull
        # an image; only this owned fixture is materialized, never an app run.
        gated = copy.deepcopy(fixture)
        gated["metadata"]["resourceVersion"] = live["metadata"]["resourceVersion"]
        gated["spec"]["replicas"] = 1
        gated["spec"]["template"]["spec"]["schedulingGates"] = [{"name": "ternal.dev/admission-test"}]
        require(run("gated-controller", "replace", "-f", "-", data=gated))
        pending = None
        for _ in range(30):
            result = require(run("gated-pod-get", "-n", namespace, "get", "pod", workload + "-0", "--ignore-not-found", "-o", "json"))
            if result.stdout.strip():
                pending = json.loads(result.stdout)
                if pending.get("status", {}).get("phase") == "Pending":
                    break
            time.sleep(1)
        else:
            raise AssertionError("owned gated Pod did not become Pending")

        def assert_unexecuted(obj):
            status = obj.get("status", {})
            if (obj["spec"].get("nodeName") or status.get("phase") != "Pending" or
                    status.get("containerStatuses") or status.get("initContainerStatuses") or status.get("ephemeralContainerStatuses") or
                    obj["spec"].get("schedulingGates") != [{"name": "ternal.dev/admission-test"}]):
                raise AssertionError("owned fixture was not safely scheduling-gated before container execution")

        assert_unexecuted(pending)
        pending["spec"]["ephemeralContainers"] = [{"name": "ephemeral-sidecar", "image": "busybox:1.37", "command": ["sleep", "1"],
                                                   "volumeMounts": [{"name": "local-staging", "mountPath": "/staging"}]}]
        path = "/api/v1/namespaces/" + namespace + "/pods/" + workload + "-0/ephemeralcontainers?dryRun=All"
        result = run("ephemeral-sidecar", "replace", "--raw", path, "-f", "-", data=pending)
        if not (result.returncode and envelope in result.stderr and "ValidatingAdmissionPolicy" in result.stderr):
            raise AssertionError("ephemeral subresource did not reach exact Local policy denial: " + result.stderr)
        pending = json.loads(require(run("gated-pod-after", "-n", namespace, "get", "pod", workload + "-0", "-o", "json")).stdout)
        assert_unexecuted(pending)
        require(run("delete-gated-controller", "-n", namespace, "delete", "statefulset", workload,
                    "--cascade=foreground", "--wait=true", "--timeout=40s"))
        pods = json.loads(require(run("verify-no-pods", "-n", namespace, "get", "pods", "-o", "json")).stdout)
        if pods["items"]:
            raise AssertionError("unexpected running app Pod")
        print("Local CEL type-check and admission matrix passed; no application container executed; owned gated Pod removed")
    finally:
        original_failure = sys.exc_info()[0] is not None
        errors = []
        for item in bindings + policies:
            result = run("cleanup-policy", "delete", item["kind"], item["metadata"]["name"], "--ignore-not-found", "--wait=true")
            if result.returncode:
                errors.append(result.stderr)
        result = run("cleanup-namespace", "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=40s")
        if result.returncode:
            errors.append(result.stderr)
        if errors:
            message = "owned cleanup failed: " + "\n".join(errors)
            if original_failure:
                print(message, file=sys.stderr)
            else:
                raise RuntimeError(message)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("image")
    parser.add_argument("--local", action="store_true")
    parser.add_argument("--kubeconfig")
    parser.add_argument("--context")
    parser.add_argument("--diagnostics")
    args = parser.parse_args()
    if args.local:
        if not all((args.kubeconfig, args.context, args.diagnostics)):
            parser.error("--local requires --kubeconfig, --context and --diagnostics")
        local_matrix(args.image, args.kubeconfig, args.context, args.diagnostics)
    else:
        main(args.image)

#!/usr/bin/env python3
"""Render operator-owned Kubernetes trust-anchor guards.

This renderer deliberately emits no Helm annotations and never talks to a
cluster. Apply its resource list separately from the Ternal release; the
namespace label, ConfigMaps, RBAC, and admission policies are the external
rollback boundary.
"""
import argparse
import hashlib
import json
import posixpath
import re
import sys
import uuid

OFFICIAL_IMAGE = "ghcr.io/mrchypark/ternal"
FORMAT = "1"


def fail(message):
    raise ValueError(message)


def dns_label(value, field):
    if not re.fullmatch(r"[a-z0-9]([-a-z0-9]*[a-z0-9])?", value) or len(value) > 63:
        fail(field + " must be a DNS label")
    return value


def qualified_name(value, field):
    if not re.fullmatch(r"[a-z0-9]([-a-z0-9.]*[a-z0-9])?", value) or len(value) > 253:
        fail(field + " must be a lower-case DNS name")
    return value


def service_account(value):
    return dns_label(value, "service account")


def storage_id(provider, endpoint, bucket, prefix, cluster_id, data_mode="standalone"):
    """Match Go encoding/json for the ASCII/UTF-8 CLI contract exactly."""
    values = [provider, endpoint, bucket, prefix, cluster_id]
    if any(not isinstance(value, str) or any(0xD800 <= ord(char) <= 0xDFFF for char in value) for value in values):
        fail("storage identity values must be valid Unicode strings")
    if data_mode not in ("standalone", "ha", "local"):
        fail("unsupported data mode")
    if data_mode == "local":
        namespace = posixpath.normpath("/".join(value for value in (prefix, cluster_id, "local/v1") if value))
        values[3] = "/" + namespace.lstrip("/") if namespace.startswith("//") else namespace
    encoded = json.dumps(values, ensure_ascii=False, separators=(",", ":"))
    encoded = encoded.replace("<", "\\u003c").replace(">", "\\u003e").replace("&", "\\u0026")
    encoded = encoded.replace("\u2028", "\\u2028").replace("\u2029", "\\u2029")
    legacy = hashlib.sha256(encoded.encode("utf-8")).hexdigest()
    return hashlib.sha256(b"ternal-local-v1\x00" + legacy.encode("ascii")).hexdigest() if data_mode == "local" else legacy


def approved_images(images):
    result = []
    for image in images:
        if not re.fullmatch(re.escape(OFFICIAL_IMAGE) + r"@sha256:[0-9a-f]{64}", image):
            fail("approved API images must be official immutable SHA-256 digests")
        if image not in result:
            result.append(image)
    if not result:
        fail("at least one approved API image is required")
    # Leading/trailing separators make the CEL contains check exact, not a prefix match.
    return "," + ",".join(result) + ","


def metadata(name, labels=None, namespace=None):
    value = {"name": name}
    if labels:
        value["labels"] = labels
    if namespace:
        value["namespace"] = namespace
    return value


ANCHOR_CONTAINER = "ternal-anchor"
ANCHOR_EXECUTABLE = "/usr/local/bin/ternal-anchor"
ANCHOR_ID_ENV = "TERNAL_RECOVERY_ANCHOR_ID"
FLAT_FIELDS = ("format", "clusterID", "storageID", "epoch", "token", "pendingEpoch", "pendingID", "bootstrap")
# The application half of the flat projection: the frozen pair and its pending
# lifecycle.  A recovery commit preserves these and moves only the binding.
APPLICATION_FIELDS = ("format", "epoch", "token", "pendingEpoch", "pendingID", "bootstrap")
GUARD_FIELDS = ("recoveryGeneration", "recoveryEvidence", "recoveryTransition", "recoveryReceipt")
RECOVERY_NULL = "null"


def env_pinned(container_path, name, value):
    return ("{c}.env.filter(e, e.name == '{n}').size() == 1 && "
            "{c}.env.exists(e, e.name == '{n}' && e.value == '{v}')").format(c=container_path, n=name, v=value)


def env_ok(container_path, anchor_name, namespace):
    return (env_pinned(container_path, "TERNAL_TRUST_ANCHOR_CONFIGMAP", anchor_name) + " && " +
            env_pinned(container_path, "TERNAL_TRUST_ANCHOR_NAMESPACE", namespace))


def anchor_container_expr(container_path, anchor_name, namespace, anchor_id):
    # The anchor service runs the same official image as the API, so its role has
    # to be spelled out by name, executable, and pinned configuration rather than
    # exempted by image or service account alone.
    return ("({c}.name == '{role}' && {c}.command.size() == 1 && {c}.command[0] == '{exe}' && {ident} && {cfg})").format(
        c=container_path, role=ANCHOR_CONTAINER, exe=ANCHOR_EXECUTABLE,
        ident=env_pinned(container_path, ANCHOR_ID_ENV, anchor_id), cfg=env_ok(container_path, anchor_name, namespace))


def api_container_expr(container_path="c"):
    # A repository boundary prevents ternal-agent from being mistaken for the
    # API image, while a renamed API container cannot evade the policy.
    return ("({c}.name == 'ternal-api' || {c}.image == '" + OFFICIAL_IMAGE + "' || "
            "{c}.image.startsWith('" + OFFICIAL_IMAGE + ":') || "
            "{c}.image.startsWith('" + OFFICIAL_IMAGE + "@'))").format(c=container_path)


def local_security_expr(container_path):
    return ("has({c}.securityContext) && {c}.securityContext.runAsNonRoot && {c}.securityContext.runAsUser == 65532 && "
            "{c}.securityContext.runAsGroup == 65532 && !{c}.securityContext.allowPrivilegeEscalation && "
            "{c}.securityContext.readOnlyRootFilesystem && (!has({c}.securityContext.privileged) || !{c}.securityContext.privileged) && "
            "has({c}.securityContext.capabilities) && {c}.securityContext.capabilities.drop == ['ALL'] && "
            "(!has({c}.securityContext.capabilities.add) || {c}.securityContext.capabilities.add.size() == 0) && "
            "(!has({c}.securityContext.seccompProfile) || {c}.securityContext.seccompProfile.type == 'RuntimeDefault') && "
            "(!has({c}.securityContext.procMount) || {c}.securityContext.procMount == 'Default')").format(c=container_path)


def kept(keys):
    return " && ".join("oldObject.data['" + key + "'] == object.data['" + key + "']" for key in keys)


def canonical_uint(prefix, key):
    # Canonical decimal plus overflow rejection: uint() raises on a value that
    # does not fit, and failurePolicy Fail turns that into a denial.
    value = prefix + "['" + key + "']"
    return value + ".matches('^[0-9]+$') && string(uint(" + value + ")) == " + value


def projection_ok(prefix, key):
    value = prefix + "['" + key + "']"
    return "(" + value + " == '" + RECOVERY_NULL + "' || (" + value + ".startsWith('{') && " + value + ".endsWith('}')))"


def api_policy(name, binding_name, namespace_selector, namespace, service_account_name, anchor_service_account, anchor_name, image_list, target, local_mode=False, local_workload_name=None):
    containers = "object.spec.containers" if target == "pod" else "object.spec.template.spec.containers"
    service_path = "object.spec.serviceAccountName" if target == "pod" else "object.spec.template.spec.serviceAccountName"
    # Controller templates may omit the account; read that as Kubernetes' default.
    service = "(has(" + service_path + ") ? " + service_path + " : 'default')"
    resources = ["pods", "pods/ephemeralcontainers"] if target == "pod" else ["deployments", "statefulsets", "replicasets"]
    # The anchor service runs the same official image as the API, so its role is
    # subtracted by name, executable, and pinned configuration instead of being
    # exempted by image or by service account alone.  The anchor ID is pinned to
    # the ConfigMap name this renderer already protects.
    spec = "object.spec" if target == "pod" else "object.spec.template.spec"
    role = anchor_container_expr("c", anchor_name, namespace, anchor_name)
    local_names = ["TERNAL_DATA_LOCAL", "TERNAL_DATA_MULTI_NODE", "TERNAL_DATA_DIR", "TERNAL_DATA_CLUSTER_ID",
                   "TERNAL_DATA_NODE_ID", "TERNAL_LOCAL_PREFLIGHT_STAGING_DIR", "TERNAL_LOCAL_PREFLIGHT_RECEIPT_FILE",
                   "TERNAL_LOCAL_WRITER_GC_QUIESCENCE_ACK", "TERNAL_TRUST_ANCHOR_CONFIGMAP", "TERNAL_TRUST_ANCHOR_NAMESPACE",
                   "TERNAL_OBJECT_STORE_PROVIDER", "TERNAL_OBJECT_STORE_ENDPOINT", "TERNAL_OBJECT_STORE_BUCKET",
                   "TERNAL_OBJECT_STORE_PREFIX", "TERNAL_OBJECT_STORE_REGION", "TERNAL_OBJECT_STORE_INSECURE",
                   "TERNAL_OBJECT_STORE_DURABILITY"]
    credential_names = ["TERNAL_OBJECT_STORE_ACCESS_KEY", "TERNAL_OBJECT_STORE_SECRET_KEY", "TERNAL_OBJECT_STORE_SESSION_TOKEN"]
    scope_env = ("has({c}.env) && {c}.env.filter(e, e.name == 'TERNAL_LOCAL_PREFLIGHT_SCOPE').size() == 1 && "
                 "{c}.env.exists(e, e.name == 'TERNAL_LOCAL_PREFLIGHT_SCOPE' && !has(e.value) && has(e.valueFrom) && "
                 "has(e.valueFrom.fieldRef) && e.valueFrom.fieldRef.fieldPath == 'metadata.uid' && "
                 "(!has(e.valueFrom.fieldRef.apiVersion) || e.valueFrom.fieldRef.apiVersion == 'v1'))")
    local_init = (
        "c.name == 'ternal-local-restore' && has(c.command) && c.command == ['/usr/local/bin/ternal-api'] && "
        "has(c.args) && c.args == ['local-restore'] && '" + image_list + "'.contains(',' + c.image + ',') && "
        "(!has(c.envFrom) || c.envFrom.size() == 0) && has(c.env) && c.env.size() == 21 && "
        "(variables.localDataNames + variables.localCredentialNames).all(n, c.env.filter(e, e.name == n).size() == 1) && "
        "c.env.all(e, e.name in variables.localDataNames ? (!has(e.valueFrom) && "
        "variables.apiContainers.all(a, has(a.env) && a.env.filter(x, x.name == e.name).size() == 1 && "
        "a.env.exists(x, x.name == e.name && !has(x.valueFrom) && (has(x.value) ? x.value : '') == (has(e.value) ? e.value : '')))) : "
        "(e.name == 'TERNAL_LOCAL_PREFLIGHT_SCOPE' ? true : (!has(e.value) && has(e.valueFrom) && has(e.valueFrom.secretKeyRef) && "
        "e.valueFrom.secretKeyRef.key == e.name && has(e.valueFrom.secretKeyRef.optional) && e.valueFrom.secretKeyRef.optional && "
        "variables.apiContainers.all(a, has(a.envFrom) && a.envFrom.exists(f, has(f.secretRef) && f.secretRef.name == e.valueFrom.secretKeyRef.name)))))"
    )
    local_init += " && " + scope_env.format(c="c") + " && variables.apiContainers.all(a, " + scope_env.format(c="a") + ")"
    local_init += " && " + " && ".join(
        env_pinned("c", key, value) for key, value in (
            ("TERNAL_DATA_LOCAL", "1"), ("TERNAL_DATA_MULTI_NODE", "0"), ("TERNAL_DATA_DIR", "/data/ternal"),
            ("TERNAL_DATA_NODE_ID", "node-1"), ("TERNAL_LOCAL_PREFLIGHT_STAGING_DIR", "/staging"),
            ("TERNAL_LOCAL_PREFLIGHT_RECEIPT_FILE", "/staging/preflight.json"), ("TERNAL_LOCAL_WRITER_GC_QUIESCENCE_ACK", "1"),
            ("TERNAL_OBJECT_STORE_DURABILITY", "before-ack")))
    local_init += " && " + env_ok("c", anchor_name, namespace) + " && " + local_security_expr("c") + (
        " && "
        "!has(c.lifecycle) && !has(c.livenessProbe) && !has(c.readinessProbe) && !has(c.startupProbe) && !has(c.restartPolicy) && "
        "(!has(c.ports) || c.ports.size() == 0) && (!has(c.stdin) || !c.stdin) && (!has(c.tty) || !c.tty) && "
        "has(c.volumeMounts) && c.volumeMounts.filter(m, m.name == 'local-staging' && m.mountPath == '/staging' && "
        "(!has(m.readOnly) || !m.readOnly)).size() == 1 && "
        "c.volumeMounts.filter(m, m.name == 'data' && m.mountPath == '/data' && (!has(m.readOnly) || !m.readOnly)).size() == 1 && "
        "c.volumeMounts.all(m, !has(m.subPath) && !has(m.subPathExpr) && "
        "(!has(m.mountPropagation) || m.mountPropagation == 'None') && ((m.name == 'local-staging' && m.mountPath == '/staging') || "
        "(m.name == 'data' && m.mountPath == '/data' && (!has(m.readOnly) || !m.readOnly)) || "
        "(m.mountPath == '/var/run/secrets/kubernetes.io/serviceaccount' && has(m.readOnly) && m.readOnly && "
        "variables.serviceAccountVolumes.exists(v, v.name == m.name))))"
    )
    service_account_volumes = (
        "has(" + spec + ".volumes) ? " + spec + ".volumes.filter(v, has(v.projected) && "
        "v.projected.sources.exists(s, has(s.serviceAccountToken) && s.serviceAccountToken.path == 'token') && "
        "v.projected.sources.all(s, (has(s.serviceAccountToken) && s.serviceAccountToken.path == 'token' && !has(s.serviceAccountToken.audience)) || "
        "(has(s.configMap) && s.configMap.name == 'kube-root-ca.crt' && has(s.configMap.items) && "
        "s.configMap.items.size() == 1 && s.configMap.items[0].key == 'ca.crt' && s.configMap.items[0].path == 'ca.crt') || "
        "(has(s.downwardAPI) && s.downwardAPI.items.size() == 1 && s.downwardAPI.items[0].path == 'namespace' && "
        "has(s.downwardAPI.items[0].fieldRef) && s.downwardAPI.items[0].fieldRef.fieldPath == 'metadata.namespace'))) : []"
    )
    local_identity = "false"
    if local_mode:
        local_identity = ("request.name == '" + local_workload_name + "-0' && has(object.metadata.ownerReferences) && "
                          "object.metadata.ownerReferences.exists(r, r.kind == 'StatefulSet' && r.name == '" + local_workload_name + "' && "
                          "has(r.controller) && r.controller && r.uid != '')") if target == "pod" else (
                          "request.resource.resource == 'statefulsets' && request.name == '" + local_workload_name + "' && "
                          "(!has(object.spec.replicas) || object.spec.replicas in [0, 1])")
    policy = {
        "apiVersion": "admissionregistration.k8s.io/v1",
        "kind": "ValidatingAdmissionPolicy",
        "metadata": metadata(name),
        "spec": {
            "failurePolicy": "Fail",
            "matchConstraints": {"resourceRules": [{"apiGroups": ["" if target == "pod" else "apps"],
                "apiVersions": ["v1"], "operations": ["CREATE", "UPDATE"], "resources": resources,
                "scope": "Namespaced"}]},
            "variables": [
                {"name": "anchorContainers", "expression": containers + ".filter(c, " + role + ")"},
                {"name": "apiContainers", "expression": containers + ".filter(c, " + api_container_expr() + " && !(" + role + "))"},
                {"name": "localDataNames", "expression": json.dumps(local_names)},
                {"name": "localCredentialNames", "expression": json.dumps(credential_names)},
                {"name": "localInitContainers", "expression": "has(" + spec + ".initContainers) ? " + spec + ".initContainers : []"},
                {"name": "serviceAccountVolumes", "expression": service_account_volumes},
                {"name": "guardedLocalInitContainers", "expression": "variables.localInitContainers.filter(c, c.name in ['ternal-local-preflight', 'ternal-local-restore'] || " + api_container_expr() + ")"},
                {"name": "approvedLocalInitContainers", "expression": "variables.localInitContainers.filter(c, " + local_init + ")"},
            ],
            "validations": [
                {"expression": service + " != '" + anchor_service_account + "' || (variables.anchorContainers.size() == 1 && "
                 + containers + ".size() == 1 && (!has(" + spec + ".initContainers) || " + spec + ".initContainers.size() == 0) && "
                 + "(!has(" + spec + ".ephemeralContainers) || " + spec + ".ephemeralContainers.size() == 0))",
                 "message": "the recovery identity is reserved for a single approved anchor container"},
                {"expression": "variables.anchorContainers.all(c, (!has(c.envFrom) || c.envFrom.size() == 0) && "
                 + "c.env.all(e, !e.name.startsWith('KUBERNETES_')))",
                 "message": "the recovery authority must use the cluster-provided Kubernetes endpoint"},
                {"expression": "variables.anchorContainers.all(c, " + " && ".join(
                    "(!has(c." + probe + ") || !has(c." + probe + ".exec))"
                    for probe in ("livenessProbe", "readinessProbe", "startupProbe")) + ")",
                 "message": "the recovery container must not execute probe commands"},
                {"expression": "variables.anchorContainers.all(c, !has(c.lifecycle) && (!has(c.volumeMounts) || c.volumeMounts.all(m, "
                 + "m.mountPath in ['/tmp', '/etc/ternal-anchor/tls', '/etc/ternal-anchor/token', '/var/run/secrets/kubernetes.io/serviceaccount'])))",
                 "message": "the recovery executable must not be replaced or accompanied by lifecycle commands"},
                {"expression": "variables.apiContainers.size() == 0 || " + service + " == '" + service_account_name + "'",
                 "message": "Ternal API workloads must use the protected service account"},
                {"expression": "variables.anchorContainers.size() == 0 || " + service + " == '" + anchor_service_account + "'",
                 "message": "the recovery anchor workload must use its own protected service account"},
                {"expression": "variables.apiContainers.all(c, '" + image_list + "'.contains(',' + c.image + ','))",
                 "message": "Ternal API image is not approved by the external trustguard"},
                {"expression": "variables.anchorContainers.all(c, '" + image_list + "'.contains(',' + c.image + ','))",
                 "message": "recovery anchor image is not approved by the external trustguard"},
                {"expression": "variables.apiContainers.all(c, " + env_ok("c", anchor_name, namespace) + ")",
                 "message": "Ternal API must directly bind the external trust-anchor ConfigMap and namespace"},
                {"expression": "(" + service + " != '" + service_account_name + "' && variables.guardedLocalInitContainers.size() == 0) || "
                 "(" + ("false" if local_mode else "true") + " && variables.localInitContainers.size() == 0 && !variables.apiContainers.exists(c, has(c.env) && c.env.exists(e, e.name == 'TERNAL_DATA_LOCAL' && e.value == '1'))) || "
                 "(" + ("true" if local_mode else "false") + " && " + service + " == '" + service_account_name + "' && variables.localInitContainers.size() == 1 && "
                 "variables.approvedLocalInitContainers.size() == 1 && variables.apiContainers.size() == 1 && "
                 + containers + ".size() == 1 && variables.apiContainers[0].name == 'ternal-api' && "
                 "variables.apiContainers.all(a, a.image == variables.approvedLocalInitContainers[0].image && has(a.volumeMounts) && "
                 + local_security_expr("a") + " && (!has(a.command) || a.command == ['/usr/local/bin/ternal-api']) && "
                 "(!has(a.args) || a.args.size() == 0) && !has(a.lifecycle) && "
                 + env_pinned("a", "TERNAL_REQUIRE_DATA_ADMIN_TOKEN", "0") + " && "
                 + "a.env.filter(e, e.name == 'TERNAL_OPERATOR_BIND').size() == 1 && a.env.exists(e, e.name == 'TERNAL_OPERATOR_BIND' && !has(e.valueFrom) && (!has(e.value) || e.value == '')) && "
                 "(!has(a.livenessProbe) || !has(a.livenessProbe.exec)) && (!has(a.readinessProbe) || !has(a.readinessProbe.exec)) && "
                 "(!has(a.startupProbe) || !has(a.startupProbe.exec)) && "
                 "a.volumeMounts.filter(m, m.name == 'local-staging').size() == 1 && a.volumeMounts.filter(m, m.name == 'data').size() == 1 && "
                 "a.volumeMounts.all(m, !has(m.subPath) && !has(m.subPathExpr) && (!has(m.mountPropagation) || m.mountPropagation == 'None') && "
                 "((m.name == 'local-staging' && m.mountPath == '/staging' && has(m.readOnly) && m.readOnly) || "
                 "(m.name == 'data' && m.mountPath == '/data' && (!has(m.readOnly) || !m.readOnly)) || "
                 "(m.mountPath == '/var/run/secrets/kubernetes.io/serviceaccount' && has(m.readOnly) && m.readOnly && "
                 "variables.serviceAccountVolumes.exists(v, v.name == m.name))))) && "
                 # Quantity's oneOf schema is not declared by Kubernetes v1.34 CEL.
                 # Defer only this presence lookup, not the surrounding policy:
                 # https://github.com/kubernetes/kubernetes/blob/v1.34.3/staging/src/k8s.io/apiserver/pkg/cel/common/schemas.go
                 "has(" + spec + ".volumes) && " + spec + ".volumes.exists(v, v.name == 'local-staging' && has(v.emptyDir) && has(dyn(v.emptyDir).sizeLimit)) && "
                 + spec + ".volumes.exists(v, v.name == 'data' && has(v.emptyDir)) && (" + local_identity + ") && "
                 "has(" + spec + ".securityContext) && " + spec + ".securityContext.fsGroup == 65532 && "
                 "has(" + spec + ".securityContext.seccompProfile) && " + spec + ".securityContext.seccompProfile.type == 'RuntimeDefault' && "
                 "(!has(" + spec + ".hostNetwork) || !" + spec + ".hostNetwork) && (!has(" + spec + ".hostPID) || !" + spec + ".hostPID) && "
                 "(!has(" + spec + ".hostIPC) || !" + spec + ".hostIPC) && (!has(" + spec + ".ephemeralContainers) || " + spec + ".ephemeralContainers.size() == 0))",
                 "message": "Local recovery requires the exact approved non-root initializer, matching shared data configuration, and isolated staging"},
            ],
        },
    }
    binding = {
        "apiVersion": "admissionregistration.k8s.io/v1",
        "kind": "ValidatingAdmissionPolicyBinding",
        "metadata": metadata(binding_name),
        "spec": {
            "policyName": name,
            "validationActions": ["Deny"],
            "matchResources": {"namespaceSelector": namespace_selector},
        },
    }
    return [policy, binding]


def anchor_policy(name, binding_name, namespace_selector, anchor_name, cluster_id, object_storage_id, protected_label, namespace, anchor_service_account):
    uuid_re = "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"
    new = "object.data"
    old = "oldObject.data"
    flat = "['" + "','".join(FLAT_FIELDS) + "']"
    guard = "['" + "','".join(GUARD_FIELDS) + "']"
    # The flat projection keeps its meaning in both representations; only the
    # twelve-key shape drops the fixed binding, because a recovery commit is the
    # one operation that legitimately changes it.  The guard projections are not
    # a second authority: the Go adapter re-derives and rejects any disagreement
    # on every read, and CEL compares them without being able to decode them.
    application = (new + "['format'] == '1' && " + canonical_uint(new, "epoch") + " && " + new + "['token'].matches('" + uuid_re + "') && "
                   "(" + new + "['pendingEpoch'] == '' ? " + new + "['pendingID'] == '' : (" + canonical_uint(new, "pendingEpoch") + " && "
                   + new + "['pendingID'].matches('" + uuid_re + "') && int(" + new + "['pendingEpoch']) == int(" + new + "['epoch']) + 1)) && "
                   "(" + new + "['bootstrap'] == 'true' || " + new + "['bootstrap'] == 'false')")
    guards = (canonical_uint(new, "recoveryGeneration") + " && " + new + "['recoveryEvidence'].size() > 0 && "
              + projection_ok(new, "recoveryTransition") + " && " + projection_ok(new, "recoveryReceipt"))
    legacy = ("object.data.size() == 8 && " + flat + ".all(k, k in object.data) && "
              + new + "['clusterID'] == '" + cluster_id + "' && " + new + "['storageID'] == '" + object_storage_id + "' && " + application)
    composite = ("object.data.size() == 12 && " + flat + ".all(k, k in object.data) && " + guard + ".all(k, k in object.data) && "
                 + application + " && " + guards)
    created = (new + "['epoch'] == '0' && " + new + "['pendingEpoch'] == '' && " + new + "['pendingID'] == '' && " + new + "['bootstrap'] == 'true'")
    same_binding = old + "['clusterID'] == " + new + "['clusterID'] && " + old + "['storageID'] == " + new + "['storageID']"
    reserve = (old + "['format'] == " + new + "['format'] && " + old + "['pendingEpoch'] == '' && " + new + "['epoch'] == " + old + "['epoch'] && "
               + new + "['token'] == " + old + "['token'] && " + new + "['pendingEpoch'] == string(int(" + old + "['epoch']) + 1) && "
               + new + "['pendingID'].matches('" + uuid_re + "') && " + new + "['bootstrap'] == " + old + "['bootstrap']")
    finalize = (old + "['pendingEpoch'] != '' && " + new + "['epoch'] == " + old + "['pendingEpoch'] && " + new + "['token'] == " + old + "['pendingID'] && "
                + new + "['pendingEpoch'] == '' && " + new + "['pendingID'] == '' && " + new + "['bootstrap'] == 'false'")
    abort = (old + "['pendingEpoch'] != '' && " + new + "['epoch'] == " + old + "['epoch'] && " + new + "['token'] == " + old + "['token'] && "
             + new + "['pendingEpoch'] == '' && " + new + "['pendingID'] == '' && " + new + "['bootstrap'] == " + old + "['bootstrap']")
    # Binding equality wraps every application branch, finalize and abort
    # included: once a recovered generation may legitimately adopt another
    # binding, no global pin remains to catch them.
    application_transition = "(" + same_binding + " && (" + reserve + " || " + finalize + " || " + abort + "))"
    # Format migration preserves all eight flat values and adds their honest
    # projection.  It is not a state change, so nothing else moves.  The frozen
    # evidence bytes stay opaque to CEL and are re-derived by the adapter.
    migration = (kept(FLAT_FIELDS) + " && " + new + "['recoveryGeneration'] == '0' && "
                 + new + "['recoveryTransition'] == '" + RECOVERY_NULL + "' && " + new + "['recoveryReceipt'] == '" + RECOVERY_NULL + "'")
    # Ordinary writes update evidence but cannot acquire or rewrite recovery authority.
    composite_application = ("(" + old + "['recoveryTransition'] == 'null' && "
                             + kept(("recoveryGeneration", "recoveryTransition", "recoveryReceipt"))
                             + " && " + application_transition + ")")
    identity = "request.userInfo.username == 'system:serviceaccount:" + namespace + ":" + anchor_service_account + "'"
    recovery_reserve = (identity + " && " + old + "['pendingEpoch'] == '' && " + old + "['bootstrap'] == 'false' && " + old + "['recoveryTransition'] == '" + RECOVERY_NULL + "' && "
                        + new + "['recoveryTransition'] != '" + RECOVERY_NULL + "' && " + kept(FLAT_FIELDS) + " && "
                        + new + "['recoveryGeneration'] == " + old + "['recoveryGeneration'] && "
                        + new + "['recoveryEvidence'] == " + old + "['recoveryEvidence'] && "
                        + new + "['recoveryReceipt'] == " + old + "['recoveryReceipt']")
    recovery_commit = (identity + " && " + old + "['recoveryTransition'] != '" + RECOVERY_NULL + "' && "
                       + new + "['recoveryTransition'] == '" + RECOVERY_NULL + "' && " + new + "['recoveryReceipt'] != '" + RECOVERY_NULL + "' && "
                       # The commit is the one operation that legitimately moves the
                       # binding to the recovered target's cluster and storage identity,
                       # so it preserves the application half only.  The coordinator
                       # derives both values from the verifier's proof, never from the
                       # request, and the migration/reserve branches still pin them.
                       + kept(APPLICATION_FIELDS) + " && " + new + "['recoveryEvidence'] == " + old + "['recoveryEvidence'] && "
                       + "uint(" + new + "['recoveryGeneration']) == uint(" + old + "['recoveryGeneration']) + 1u")
    transition = ("oldObject.data == object.data || "
                  "(" + old + ".size() == 8 && " + new + ".size() == 8 && " + application_transition + ") || "
                  "(" + old + ".size() == 8 && " + new + ".size() == 12 && " + migration + ") || "
                  "(" + old + ".size() == 12 && " + new + ".size() == 12 && ("
                  + composite_application + " || " + recovery_reserve + " || " + recovery_commit + "))")
    policy = {
        "apiVersion": "admissionregistration.k8s.io/v1",
        "kind": "ValidatingAdmissionPolicy",
        "metadata": metadata(name),
        "spec": {
            "failurePolicy": "Fail",
            "matchConstraints": {"resourceRules": [{"apiGroups": [""], "apiVersions": ["v1"],
                "operations": ["CREATE", "UPDATE", "DELETE"], "resources": ["configmaps"], "scope": "Namespaced"}]},
            # request.name is present for DELETE; object is not guaranteed to
            # be, so this must not dereference object during selection.
            "matchConditions": [{"name": "exact-anchor", "expression": "request.name == '" + anchor_name + "'"}],
            "validations": [
                {"expression": "request.operation != 'DELETE'", "message": "the external trust anchor cannot be deleted"},
                {"expression": "object.metadata.labels['ternal.dev/trustguard-anchor'] == '" + protected_label + "'", "message": "the trust anchor must retain its protected label"},
                {"expression": legacy + " || " + composite, "message": "invalid trust-anchor contract"},
                {"expression": "oldObject == null ? (" + created + ") : (" + transition + ")", "message": "trust-anchor transition is not monotonic"},
            ],
        },
    }
    binding = {"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicyBinding", "metadata": metadata(binding_name),
               "spec": {"policyName": name, "validationActions": ["Deny"], "matchResources": {"namespaceSelector": namespace_selector}}}
    return [policy, binding]


def render(namespace, service_account_name, anchor_name, cluster_id, storage_identity, images, token=None, anchor_service_account=None, data_mode="standalone", local_workload_name=None):
    namespace = dns_label(namespace, "namespace")
    service_account_name = service_account(service_account_name)
    anchor_name = dns_label(anchor_name, "anchor ConfigMap")
    cluster_id = dns_label(cluster_id, "cluster ID")
    if data_mode not in ("standalone", "ha", "local"):
        fail("unsupported data mode")
    if data_mode == "local":
        if not local_workload_name:
            fail("Local mode requires an explicit local workload name")
        local_workload_name = dns_label(local_workload_name, "local workload name")
    elif local_workload_name is not None:
        fail("local workload name requires Local mode")
    if anchor_service_account is None:
        fail("the recovery anchor service account is required")
    anchor_service_account = service_account(anchor_service_account)
    if anchor_service_account == service_account_name:
        fail("recovery and application service accounts must differ")
    if not re.fullmatch(r"[0-9a-f]{64}", storage_identity):
        fail("storage ID must be a SHA-256 hex digest")
    image_list = approved_images(images)
    token = token or str(uuid.uuid4())
    if not re.fullmatch(r"[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}", token):
        fail("token must be a lower-case UUID")
    # Readable prefix plus a hash of the full identity: two cluster IDs that
    # share a suffix must not map to the same resources. Pre-fix manifests
    # used the raw 35-char suffix; delete those resources before applying a
    # re-rendered manifest, as the old selectors no longer match.
    readable = cluster_id[:12].rstrip("-") or "id"
    digest = hashlib.sha256(cluster_id.encode()).hexdigest()[:16]
    suffix = readable + "-" + digest
    anchor_label = "anchor-" + suffix
    scope_label = "scope-" + suffix
    names = {"anchor": "ternal-trustguard-anchor-" + suffix, "api_pods": "ternal-trustguard-api-pods-" + suffix,
             "api_workloads": "ternal-trustguard-api-workloads-" + suffix, "anchor_policy": "ternal-trustguard-anchor-policy-" + suffix}
    selector = {"matchLabels": {"ternal.dev/trustguard-scope": scope_label}}
    operator_labels = {"app.kubernetes.io/managed-by": "ternal-trustguard-operator", "ternal.dev/trustguard-scope": scope_label}
    anchor_labels = dict(operator_labels, **{"ternal.dev/trustguard-anchor": anchor_label})
    # Greenfield provisioning still emits the legacy eight-key bootstrap, which
    # is also the format migration's starting point.  A recovered generation
    # never reapplies this output: migration is the only writer that adds the
    # common representation, and it preserves these values.
    anchor = {"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata(anchor_name, anchor_labels, namespace), "data":
              {"format": FORMAT, "clusterID": cluster_id, "storageID": storage_identity, "epoch": "0", "token": token,
               "pendingEpoch": "", "pendingID": "", "bootstrap": "true"}}
    role = {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": metadata("ternal-trustguard-runtime-" + suffix, operator_labels, namespace),
            "rules": [{"apiGroups": [""], "resources": ["configmaps"], "resourceNames": [anchor_name], "verbs": ["get", "update"]}]}
    binding = {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": metadata("ternal-trustguard-runtime-" + suffix, operator_labels, namespace),
               "subjects": [{"kind": "ServiceAccount", "name": service_account_name, "namespace": namespace}],
               "roleRef": {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": role["metadata"]["name"]}}
    # The recovery authority is a separate identity: the admission policy names
    # it, so the account that may commit a transition must not be the account
    # that serves application traffic.  It reaches the same ConfigMap and
    # nothing else.
    anchor_role = {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": metadata("ternal-trustguard-anchor-runtime-" + suffix, operator_labels, namespace),
                   "rules": [{"apiGroups": [""], "resources": ["configmaps"], "resourceNames": [anchor_name], "verbs": ["get", "update"]}]}
    anchor_binding = {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": metadata("ternal-trustguard-anchor-runtime-" + suffix, operator_labels, namespace),
                      "subjects": [{"kind": "ServiceAccount", "name": anchor_service_account, "namespace": namespace}],
                      "roleRef": {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": anchor_role["metadata"]["name"]}}
    namespace_resource = {"apiVersion": "v1", "kind": "Namespace", "metadata": metadata(namespace, {"ternal.dev/trustguard-scope": scope_label, "app.kubernetes.io/managed-by": "ternal-trustguard-operator"})}
    items = [namespace_resource, anchor, role, binding, anchor_role, anchor_binding]
    items += api_policy(names["api_pods"], names["api_pods"] + "-binding", selector, namespace, service_account_name,
                        anchor_service_account, anchor_name, image_list, "pod", data_mode == "local", local_workload_name)
    items += api_policy(names["api_workloads"], names["api_workloads"] + "-binding", selector, namespace, service_account_name,
                        anchor_service_account, anchor_name, image_list, "workload", data_mode == "local", local_workload_name)
    items += anchor_policy(names["anchor_policy"], names["anchor_policy"] + "-binding", selector, anchor_name, cluster_id,
                           storage_identity, anchor_label, namespace, anchor_service_account)
    if data_mode == "local":
        scale_name = "ternal-trustguard-local-scale-" + suffix
        items.append({"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicy",
                      "metadata": metadata(scale_name), "spec": {"failurePolicy": "Fail",
                          "matchConstraints": {"resourceRules": [{"apiGroups": ["apps"], "apiVersions": ["v1"],
                              "operations": ["CREATE", "UPDATE"], "resources": ["statefulsets", "statefulsets/scale"],
                              "resourceNames": [local_workload_name], "scope": "Namespaced"}]},
                          "matchConditions": [{"name": "owned-namespace", "expression": "request.namespace == '" + namespace + "'"}],
                          # ScaleSpec.replicas is a zero-valued int32 with json omitempty.
                          # https://github.com/kubernetes/api/blob/v0.34.3/autoscaling/v1/types.go
                          "validations": [{"expression": "!has(object.spec.replicas) || object.spec.replicas in [0, 1]",
                                           "message": "the owned Local workload permits only zero or one replica; scale is not fencing"}]}})
        items.append({"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicyBinding",
                      "metadata": metadata(scale_name + "-binding"), "spec": {"policyName": scale_name,
                          "validationActions": ["Deny"], "matchResources": {"namespaceSelector": selector}}})
    return {"apiVersion": "v1", "kind": "List", "items": items}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--service-account", required=True)
    parser.add_argument("--anchor-service-account", required=True)
    parser.add_argument("--anchor-configmap", required=True)
    parser.add_argument("--cluster-id", required=True)
    parser.add_argument("--data-mode", choices=("standalone", "ha", "local"), default="standalone")
    parser.add_argument("--local-workload-name")
    identity = parser.add_mutually_exclusive_group()
    identity.add_argument("--storage-id")
    parser.add_argument("--provider")
    parser.add_argument("--endpoint", default="")
    parser.add_argument("--bucket")
    parser.add_argument("--prefix")
    parser.add_argument("--approved-api-image", action="append", default=[])
    args = parser.parse_args(argv)
    if args.storage_id is None:
        if not args.provider or not args.bucket or not args.prefix:
            parser.error("--provider, --bucket, and --prefix are required without --storage-id")
        identity_value = storage_id(args.provider, args.endpoint, args.bucket, args.prefix, args.cluster_id, args.data_mode)
    elif args.provider or args.bucket or args.prefix or args.endpoint:
        parser.error("storage inputs cannot accompany --storage-id")
    else:
        identity_value = args.storage_id
    try:
        output = render(args.namespace, args.service_account, args.anchor_configmap, args.cluster_id,
                        identity_value, args.approved_api_image, anchor_service_account=args.anchor_service_account,
                        data_mode=args.data_mode, local_workload_name=args.local_workload_name)
    except ValueError as error:
        parser.error(str(error))
    json.dump(output, sys.stdout, sort_keys=True, separators=(",", ":"))
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

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


def storage_id(provider, endpoint, bucket, prefix, cluster_id):
    """Match Go encoding/json for the ASCII/UTF-8 CLI contract exactly."""
    values = [provider, endpoint, bucket, prefix, cluster_id]
    if any(not isinstance(value, str) or any(0xD800 <= ord(char) <= 0xDFFF for char in value) for value in values):
        fail("storage identity values must be valid Unicode strings")
    encoded = json.dumps(values, ensure_ascii=False, separators=(",", ":"))
    encoded = encoded.replace("<", "\\u003c").replace(">", "\\u003e").replace("&", "\\u0026")
    encoded = encoded.replace("\u2028", "\\u2028").replace("\u2029", "\\u2029")
    return hashlib.sha256(encoded.encode("utf-8")).hexdigest()


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


def api_policy(name, binding_name, namespace_selector, namespace, service_account_name, anchor_service_account, anchor_name, image_list, target):
    containers = "object.spec.containers" if target == "pod" else "object.spec.template.spec.containers"
    service = "object.spec.serviceAccountName" if target == "pod" else "object.spec.template.spec.serviceAccountName"
    resources = ["pods"] if target == "pod" else ["deployments", "statefulsets", "replicasets"]
    # The anchor service runs the same official image as the API, so its role is
    # subtracted by name, executable, and pinned configuration instead of being
    # exempted by image or by service account alone.  The anchor ID is pinned to
    # the ConfigMap name this renderer already protects.
    role = anchor_container_expr("c", anchor_name, namespace, anchor_name)
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
            ],
            "validations": [
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
    identity = "request.userInfo.username == 'system:serviceaccount:" + namespace + ":" + anchor_service_account + "'"
    recovery_reserve = (identity + " && " + old + "['recoveryTransition'] == '" + RECOVERY_NULL + "' && "
                        + new + "['recoveryTransition'] != '" + RECOVERY_NULL + "' && " + kept(FLAT_FIELDS) + " && "
                        + new + "['recoveryGeneration'] == " + old + "['recoveryGeneration'] && "
                        + new + "['recoveryEvidence'] == " + old + "['recoveryEvidence'] && "
                        + new + "['recoveryReceipt'] == " + old + "['recoveryReceipt']")
    recovery_commit = (identity + " && " + old + "['recoveryTransition'] != '" + RECOVERY_NULL + "' && "
                       + new + "['recoveryTransition'] == '" + RECOVERY_NULL + "' && " + new + "['recoveryReceipt'] != '" + RECOVERY_NULL + "' && "
                       + kept(FLAT_FIELDS) + " && " + new + "['recoveryEvidence'] == " + old + "['recoveryEvidence'] && "
                       + "uint(" + new + "['recoveryGeneration']) == uint(" + old + "['recoveryGeneration']) + 1u")
    transition = ("oldObject.data == object.data || "
                  "(" + old + ".size() == 8 && " + new + ".size() == 8 && " + application_transition + ") || "
                  "(" + old + ".size() == 8 && " + new + ".size() == 12 && " + migration + ") || "
                  "(" + old + ".size() == 12 && " + new + ".size() == 12 && ("
                  + application_transition + " || " + recovery_reserve + " || " + recovery_commit + "))")
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


def render(namespace, service_account_name, anchor_name, cluster_id, storage_identity, images, token=None, anchor_service_account=None):
    namespace = dns_label(namespace, "namespace")
    service_account_name = service_account(service_account_name)
    anchor_name = dns_label(anchor_name, "anchor ConfigMap")
    cluster_id = dns_label(cluster_id, "cluster ID")
    if anchor_service_account is None:
        fail("the recovery anchor service account is required")
    anchor_service_account = service_account(anchor_service_account)
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
    namespace_resource = {"apiVersion": "v1", "kind": "Namespace", "metadata": metadata(namespace, {"ternal.dev/trustguard-scope": scope_label, "app.kubernetes.io/managed-by": "ternal-trustguard-operator"})}
    items = [namespace_resource, anchor, role, binding]
    items += api_policy(names["api_pods"], names["api_pods"] + "-binding", selector, namespace, service_account_name,
                        anchor_service_account, anchor_name, image_list, "pod")
    items += api_policy(names["api_workloads"], names["api_workloads"] + "-binding", selector, namespace, service_account_name,
                        anchor_service_account, anchor_name, image_list, "workload")
    items += anchor_policy(names["anchor_policy"], names["anchor_policy"] + "-binding", selector, anchor_name, cluster_id,
                           storage_identity, anchor_label, namespace, anchor_service_account)
    return {"apiVersion": "v1", "kind": "List", "items": items}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--service-account", required=True)
    parser.add_argument("--anchor-service-account", required=True)
    parser.add_argument("--anchor-configmap", required=True)
    parser.add_argument("--cluster-id", required=True)
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
        identity_value = storage_id(args.provider, args.endpoint, args.bucket, args.prefix, args.cluster_id)
    elif args.provider or args.bucket or args.prefix or args.endpoint:
        parser.error("storage inputs cannot accompany --storage-id")
    else:
        identity_value = args.storage_id
    try:
        output = render(args.namespace, args.service_account, args.anchor_configmap, args.cluster_id,
                        identity_value, args.approved_api_image, anchor_service_account=args.anchor_service_account)
    except ValueError as error:
        parser.error(str(error))
    json.dump(output, sys.stdout, sort_keys=True, separators=(",", ":"))
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

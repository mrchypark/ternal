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


def env_ok(container_path):
    return (
        "{c}.env.filter(e, e.name == 'TERNAL_TRUST_ANCHOR_CONFIGMAP').size() == 1 && "
        "{c}.env.exists(e, e.name == 'TERNAL_TRUST_ANCHOR_CONFIGMAP' && e.value == params.data['anchorConfigMap']) && "
        "{c}.env.filter(e, e.name == 'TERNAL_TRUST_ANCHOR_NAMESPACE').size() == 1 && "
        "{c}.env.exists(e, e.name == 'TERNAL_TRUST_ANCHOR_NAMESPACE' && e.value == params.data['namespace'])"
    ).format(c=container_path)


def api_container_expr(container_path="c"):
    # A repository boundary prevents ternal-agent from being mistaken for the
    # API image, while a renamed API container cannot evade the policy.
    return ("({c}.name == 'ternal-api' || {c}.image == '" + OFFICIAL_IMAGE + "' || "
            "{c}.image.startsWith('" + OFFICIAL_IMAGE + ":') || "
            "{c}.image.startsWith('" + OFFICIAL_IMAGE + "@'))").format(c=container_path)


def api_policy(name, binding_name, namespace_selector, params_name, target):
    containers = "object.spec.containers" if target == "pod" else "object.spec.template.spec.containers"
    service = "object.spec.serviceAccountName" if target == "pod" else "object.spec.template.spec.serviceAccountName"
    resources = ["pods"] if target == "pod" else ["deployments", "statefulsets", "replicasets"]
    policy = {
        "apiVersion": "admissionregistration.k8s.io/v1",
        "kind": "ValidatingAdmissionPolicy",
        "metadata": metadata(name),
        "spec": {
            "failurePolicy": "Fail",
            "paramKind": {"apiVersion": "v1", "kind": "ConfigMap"},
            "matchConstraints": {"resourceRules": [{"apiGroups": ["" if target == "pod" else "apps"],
                "apiVersions": ["v1"], "operations": ["CREATE", "UPDATE"], "resources": resources,
                "scope": "Namespaced"}]},
            "variables": [{"name": "apiContainers", "expression": containers + ".filter(c, " + api_container_expr() + ")"}],
            "validations": [
                {"expression": "['format', 'namespace', 'serviceAccount', 'anchorConfigMap', 'approvedAPIImages'].all(k, k in params.data) && params.data['format'] == '1'",
                 "message": "trustguard parameters are incomplete"},
                {"expression": "variables.apiContainers.size() == 0 || " + service + " == params.data['serviceAccount']",
                 "message": "Ternal API workloads must use the protected service account"},
                {"expression": "variables.apiContainers.all(c, params.data['approvedAPIImages'].contains(',' + c.image + ','))",
                 "message": "Ternal API image is not approved by the external trustguard"},
                {"expression": "variables.apiContainers.all(c, " + env_ok("c") + ")",
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
            "paramRef": {"name": params_name, "parameterNotFoundAction": "Deny"},
            "matchResources": {"namespaceSelector": namespace_selector},
        },
    }
    return [policy, binding]


def anchor_policy(name, binding_name, namespace_selector, anchor_name, cluster_id, object_storage_id, protected_label):
    uuid_re = "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"
    fields = "['format','clusterID','storageID','epoch','token','pendingEpoch','pendingID','bootstrap']"
    shape = (fields + ".all(k, k in object.data) && object.data.size() == 8 && "
             "object.data['format'] == '1' && object.data['clusterID'] == '" + cluster_id + "' && "
             "object.data['storageID'] == '" + object_storage_id + "' && "
             "object.data['epoch'].matches('^[0-9]+$') && object.data['token'].matches('" + uuid_re + "') && "
             "(object.data['pendingEpoch'] == '' ? object.data['pendingID'] == '' : "
             "(object.data['pendingEpoch'].matches('^[0-9]+$') && int(object.data['pendingEpoch']) == int(object.data['epoch']) + 1 && object.data['pendingID'].matches('" + uuid_re + "'))) && "
             "(object.data['bootstrap'] == 'true' || object.data['bootstrap'] == 'false')")
    created = ("object.data['epoch'] == '0' && object.data['pendingEpoch'] == '' && object.data['pendingID'] == '' && object.data['bootstrap'] == 'true'")
    transition = (
        "oldObject.data == object.data || "
        "(oldObject.data['format'] == object.data['format'] && oldObject.data['clusterID'] == object.data['clusterID'] && oldObject.data['storageID'] == object.data['storageID'] && "
        "oldObject.data['pendingEpoch'] == '' && object.data['epoch'] == oldObject.data['epoch'] && object.data['token'] == oldObject.data['token'] && "
        "object.data['pendingEpoch'] == string(int(oldObject.data['epoch']) + 1) && object.data['pendingID'].matches('" + uuid_re + "') && object.data['bootstrap'] == oldObject.data['bootstrap']) || "
        "(oldObject.data['pendingEpoch'] != '' && ((object.data['epoch'] == oldObject.data['pendingEpoch'] && object.data['token'] == oldObject.data['pendingID'] && object.data['pendingEpoch'] == '' && object.data['pendingID'] == '' && object.data['bootstrap'] == 'false') || "
        "(object.data['epoch'] == oldObject.data['epoch'] && object.data['token'] == oldObject.data['token'] && object.data['pendingEpoch'] == '' && object.data['pendingID'] == '' && object.data['bootstrap'] == oldObject.data['bootstrap'])))"
    )
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
                {"expression": shape, "message": "invalid trust-anchor contract"},
                {"expression": "oldObject == null ? (" + created + ") : (" + transition + ")", "message": "trust-anchor transition is not monotonic"},
            ],
        },
    }
    binding = {"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicyBinding", "metadata": metadata(binding_name),
               "spec": {"policyName": name, "validationActions": ["Deny"], "matchResources": {"namespaceSelector": namespace_selector}}}
    return [policy, binding]


def params_policy(name, binding_name, namespace_selector, params_name, namespace, service_account_name, protected_label):
    """RBAC is the normal boundary; this also denies a pre-existing broad SA grant."""
    principal = "system:serviceaccount:%s:%s" % (namespace, service_account_name)
    policy = {
        "apiVersion": "admissionregistration.k8s.io/v1",
        "kind": "ValidatingAdmissionPolicy",
        "metadata": metadata(name),
        "spec": {
            "failurePolicy": "Fail",
            "matchConstraints": {"resourceRules": [{"apiGroups": [""], "apiVersions": ["v1"],
                "operations": ["CREATE", "UPDATE", "DELETE"], "resources": ["configmaps"], "scope": "Namespaced"}]},
            "matchConditions": [{"name": "exact-parameters", "expression": "request.name == '" + params_name + "'"}],
            "validations": [
                {"expression": "request.operation != 'DELETE'", "message": "trustguard parameters cannot be deleted"},
                {"expression": "object.metadata.labels['ternal.dev/trustguard-params'] == '" + protected_label + "'", "message": "trustguard parameters must retain their protected label"},
                {"expression": "request.userInfo.username != '" + principal + "'", "message": "the Ternal API service account cannot modify trustguard parameters"},
            ],
        },
    }
    binding = {"apiVersion": "admissionregistration.k8s.io/v1", "kind": "ValidatingAdmissionPolicyBinding", "metadata": metadata(binding_name),
               "spec": {"policyName": name, "validationActions": ["Deny"], "matchResources": {"namespaceSelector": namespace_selector}}}
    return [policy, binding]


def render(namespace, service_account_name, anchor_name, cluster_id, storage_identity, images, token=None):
    namespace = dns_label(namespace, "namespace")
    service_account_name = service_account(service_account_name)
    anchor_name = dns_label(anchor_name, "anchor ConfigMap")
    cluster_id = dns_label(cluster_id, "cluster ID")
    if not re.fullmatch(r"[0-9a-f]{64}", storage_identity):
        fail("storage ID must be a SHA-256 hex digest")
    image_list = approved_images(images)
    token = token or str(uuid.uuid4())
    if not re.fullmatch(r"[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}", token):
        fail("token must be a lower-case UUID")
    suffix = cluster_id[-35:]
    anchor_label = "anchor-" + suffix
    scope_label = "scope-" + suffix
    params_name = "ternal-trustguard-params-" + suffix
    names = {"anchor": "ternal-trustguard-anchor-" + suffix, "api_pods": "ternal-trustguard-api-pods-" + suffix,
             "api_workloads": "ternal-trustguard-api-workloads-" + suffix, "anchor_policy": "ternal-trustguard-anchor-policy-" + suffix,
             "params_policy": "ternal-trustguard-params-policy-" + suffix}
    selector = {"matchLabels": {"ternal.dev/trustguard-scope": scope_label}}
    operator_labels = {"app.kubernetes.io/managed-by": "ternal-trustguard-operator", "ternal.dev/trustguard-scope": scope_label}
    anchor_labels = dict(operator_labels, **{"ternal.dev/trustguard-anchor": anchor_label})
    params_labels = dict(operator_labels, **{"ternal.dev/trustguard-params": "params-" + suffix})
    anchor = {"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata(anchor_name, anchor_labels, namespace), "data":
              {"format": FORMAT, "clusterID": cluster_id, "storageID": storage_identity, "epoch": "0", "token": token,
               "pendingEpoch": "", "pendingID": "", "bootstrap": "true"}}
    params = {"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata(params_name, params_labels, namespace), "data":
              {"format": FORMAT, "namespace": namespace, "serviceAccount": service_account_name, "anchorConfigMap": anchor_name,
               "clusterID": cluster_id, "storageID": storage_identity, "approvedAPIImages": image_list}}
    role = {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": metadata("ternal-trustguard-runtime-" + suffix, operator_labels, namespace),
            "rules": [{"apiGroups": [""], "resources": ["configmaps"], "resourceNames": [anchor_name], "verbs": ["get", "update"]}]}
    binding = {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": metadata("ternal-trustguard-runtime-" + suffix, operator_labels, namespace),
               "subjects": [{"kind": "ServiceAccount", "name": service_account_name, "namespace": namespace}],
               "roleRef": {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": role["metadata"]["name"]}}
    namespace_resource = {"apiVersion": "v1", "kind": "Namespace", "metadata": metadata(namespace, {"ternal.dev/trustguard-scope": scope_label, "app.kubernetes.io/managed-by": "ternal-trustguard-operator"})}
    items = [namespace_resource, anchor, params, role, binding]
    items += api_policy(names["api_pods"], names["api_pods"] + "-binding", selector, params_name, "pod")
    items += api_policy(names["api_workloads"], names["api_workloads"] + "-binding", selector, params_name, "workload")
    items += anchor_policy(names["anchor_policy"], names["anchor_policy"] + "-binding", selector, anchor_name, cluster_id, storage_identity, anchor_label)
    items += params_policy(names["params_policy"], names["params_policy"] + "-binding", selector, params_name, namespace, service_account_name, "params-" + suffix)
    return {"apiVersion": "v1", "kind": "List", "items": items}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--namespace", required=True)
    parser.add_argument("--service-account", required=True)
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
                        identity_value, args.approved_api_image)
    except ValueError as error:
        parser.error(str(error))
    json.dump(output, sys.stdout, sort_keys=True, separators=(",", ":"))
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()

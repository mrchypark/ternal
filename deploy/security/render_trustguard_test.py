#!/usr/bin/env python3
import hashlib
import importlib.util
import json
import unittest
import sys
sys.dont_write_bytecode = True
from pathlib import Path

PATH = Path(__file__).with_name("render_trustguard.py")
SPEC = importlib.util.spec_from_file_location("trustguard", PATH)
trustguard = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(trustguard)
CHECK_PATH = PATH.with_name("trustguard-admission-check.py")
CHECK_SPEC = importlib.util.spec_from_file_location("trustguard_admission", CHECK_PATH)
admission = importlib.util.module_from_spec(CHECK_SPEC)
CHECK_SPEC.loader.exec_module(admission)

IMAGE = "ghcr.io/mrchypark/ternal@sha256:" + "a" * 64
TOKEN = "12345678-1234-1234-1234-123456789abc"
ANCHOR_SA = "ternal-anchor"


class TrustGuardRendererTest(unittest.TestCase):
    def test_typecheck_diagnostics_allow_only_status_fields_and_keep_schema_names(self):
        result = admission.safe_typecheck_records("owned-policy", [{
            "fieldRef": "spec.variables[7].expression",
            "warning": "undefined field secretKeyRef or serviceAccountToken",
            "params": {"credential": "not-exported"},
            "object": {"data": {"password": "not-exported"}},
        }])
        self.assertEqual(result, [{"policy": "owned-policy", "fieldRef": "spec.variables[7].expression",
                                   "warning": "undefined field secretKeyRef or serviceAccountToken"}])
        with self.assertRaisesRegex(ValueError, "invalid type-check diagnostic record"):
            admission.safe_typecheck_records("owned-policy", [{"fieldRef": "spec.variables[0]", "warning": {"secret": "not-exported"}}])
        self.assertLessEqual(len(admission.safe_typecheck_records("p", [{"fieldRef": "x", "warning": "w" * 40000}])[0]["warning"]), 32768)

    def render(self):
        return trustguard.render("ternal-test", "ternal-data", "ternal-trust-anchor",
                                 "ternal-cluster-a1", "b" * 64, [IMAGE], token=TOKEN, anchor_service_account=ANCHOR_SA)

    def by_kind(self, result, kind):
        return [item for item in result["items"] if item["kind"] == kind]

    def test_deterministic_render_matches_golden(self):
        expected = json.loads((PATH.parent / "testdata" / "trustguard-golden.json").read_text())
        self.assertEqual(self.render(), expected)

    def test_storage_id_matches_go_json_contract(self):
        value = trustguard.storage_id("gcs", "", "bucket<&>", "p\u2028refix", "cluster")
        # Go encoding/json escapes <, >, &, U+2028 and U+2029 before SHA-256.
        encoded = '["gcs","","bucket\\u003c\\u0026\\u003e","p\\u2028refix","cluster"]'.encode()
        self.assertEqual(value, hashlib.sha256(encoded).hexdigest())

    def test_local_storage_id_has_a_clean_namespace_and_separate_domain(self):
        legacy = trustguard.storage_id("gcs", "", "bucket<&>", "p\u2028refix", "cluster")
        self.assertEqual(legacy, trustguard.storage_id("gcs", "", "bucket<&>", "p\u2028refix", "cluster", "ha"))
        encoded = '["gcs","","bucket\\u003c\\u0026\\u003e","p\\u2028refix/cluster/local/v1","cluster"]'.encode()
        namespaced = hashlib.sha256(encoded).hexdigest()
        expected = hashlib.sha256(b"ternal-local-v1\x00" + namespaced.encode("ascii")).hexdigest()
        local = trustguard.storage_id("gcs", "", "bucket<&>", "p\u2028refix", "cluster", "local")
        self.assertEqual(local, expected)
        self.assertNotEqual(local, legacy)
        self.assertEqual(trustguard.storage_id("s3", "", "b", "a/../p", "c", "local"),
                         trustguard.storage_id("s3", "", "b", "p", "c", "local"))
        self.assertEqual(trustguard.storage_id("s3", "", "b", "//p", "c", "local"),
                         trustguard.storage_id("s3", "", "b", "/p", "c", "local"))

    def test_azure_identity_binds_account_effective_url_and_go_json(self):
        endpoint = "https://account123.blob.core.windows.net"
        prefix = "p<&>\u2028\u2029"
        encoded = '["azure","account123","https://account123.blob.core.windows.net","container","p\\u003c\\u0026\\u003e\\u2028\\u2029","cluster"]'.encode()
        expected = hashlib.sha256(encoded).hexdigest()
        args = ("azure", "", "container", prefix, "cluster")
        self.assertEqual(trustguard.storage_id(*args, azure_storage_account="account123"), expected)
        self.assertEqual(trustguard.storage_id(*args, azure_storage_account="account123", azure_blob_endpoint=endpoint), expected)
        self.assertNotEqual(trustguard.storage_id(*args, azure_storage_account="account456"), expected)
        local = trustguard.storage_id(*args, data_mode="local", azure_storage_account="account123")
        local_base = trustguard.storage_id("azure", "", "container", prefix + "/cluster/local/v1", "cluster", azure_storage_account="account123")
        self.assertEqual(local, hashlib.sha256(b"ternal-local-v1\x00" + local_base.encode()).hexdigest())

    def test_azure_identity_rejects_ambiguous_or_credential_urls(self):
        for endpoint in ("https://account123.blob.core.windows.net/", "http://account123.blob.core.windows.net",
                         "https://user:secret@account123.blob.core.windows.net", "https://account123.blob.core.windows.net?sig=secret",
                         "https://account123.blob.core.windows.net#fragment", "https://ACCOUNT123.blob.core.windows.net",
                         "https://account123.blob.core.windows.net:", "https://account123.blob.core.windows.net:0",
                         "https://account123.blob.core.windows.net:65536"):
            with self.assertRaises(ValueError):
                trustguard.storage_id("azure", "", "container", "prefix", "cluster", azure_storage_account="account123", azure_blob_endpoint=endpoint)
        for kwargs in ({}, {"azure_storage_account": "INVALID"}, {"azure_storage_account": "account123", "azure_blob_endpoint": "http://127.0.0.1:4577/account123"}):
            with self.assertRaises(ValueError):
                trustguard.storage_id("azure", "", "container", "prefix", "cluster", **kwargs)
        with self.assertRaises(ValueError):
            trustguard.storage_id("azure", "generic.invalid", "container", "prefix", "cluster", azure_storage_account="account123")
        with self.assertRaises(ValueError):
            trustguard.storage_id("s3", "", "bucket", "prefix", "cluster", azure_storage_account="account123")
        self.assertEqual(trustguard.azure_location("devstoreaccount1", "http://127.0.0.1:4577/devstoreaccount1", True),
                         "http://127.0.0.1:4577/devstoreaccount1")
        self.assertEqual(trustguard.azure_location("devstoreaccount1", "http://127.0.0.2:4577/devstoreaccount1", True),
                         "http://127.0.0.2:4577/devstoreaccount1")
        self.assertEqual(trustguard.azure_location("account123", "https://account123.blob.core.windows.net:443"),
                         "https://account123.blob.core.windows.net:443")
        with self.assertRaises(ValueError):
            trustguard.azure_location("devstoreaccount1", "http://[::1%25lo0]:4577/devstoreaccount1", True)

    def local_render(self):
        return trustguard.render("ternal-test", "ternal-data", "ternal-trust-anchor", "ternal-cluster-a1", "b" * 64,
                                 [IMAGE], token=TOKEN, anchor_service_account=ANCHOR_SA,
                                 data_mode="local", local_workload_name="ternal-local")

    def test_local_renderer_requires_an_explicit_owned_workload_name(self):
        with self.assertRaisesRegex(ValueError, "explicit local workload name"):
            trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, [IMAGE],
                              anchor_service_account=ANCHOR_SA, data_mode="local")
        with self.assertRaisesRegex(ValueError, "requires Local mode"):
            trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, [IMAGE],
                              anchor_service_account=ANCHOR_SA, local_workload_name="unexpected")

    def test_local_scale_guard_is_exact_name_bound_and_includes_scale_subresource(self):
        policy = next(item for item in self.by_kind(self.local_render(), "ValidatingAdmissionPolicy")
                      if "local-scale" in item["metadata"]["name"])
        rule = policy["spec"]["matchConstraints"]["resourceRules"][0]
        self.assertEqual(rule["resources"], ["statefulsets", "statefulsets/scale"])
        self.assertEqual(rule["resourceNames"], ["ternal-local"])
        self.assertEqual(policy["spec"]["matchConditions"][0]["expression"], "request.namespace == 'ternal-test'")
        self.assertEqual(policy["spec"]["validations"][0]["expression"], "!has(object.spec.replicas) || object.spec.replicas in [0, 1]")
        self.assertNotIn("label", json.dumps(policy))

    def test_standalone_scale_guard_is_opt_in_and_exact_name_bound(self):
        result = trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, [IMAGE],
                                  anchor_service_account=ANCHOR_SA, standalone_workload_name="single")
        policy = next(item for item in self.by_kind(result, "ValidatingAdmissionPolicy")
                      if "standalone-scale" in item["metadata"]["name"])
        rule = policy["spec"]["matchConstraints"]["resourceRules"][0]
        self.assertEqual(rule["resources"], ["statefulsets", "statefulsets/scale"])
        self.assertEqual(rule["resourceNames"], ["single"])
        self.assertEqual(policy["spec"]["matchConditions"][0]["expression"], "request.namespace == 'ns'")
        self.assertEqual(policy["spec"]["validations"][0]["expression"], "!has(object.spec.replicas) || object.spec.replicas in [0, 1]")
        self.assertIn("not fencing", policy["spec"]["validations"][0]["message"])
        self.assertFalse(any("standalone-scale" in item["metadata"]["name"] for item in self.render()["items"]))
        for mode in ("ha", "local"):
            with self.assertRaisesRegex(ValueError, "standalone workload name requires standalone mode"):
                trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, [IMAGE],
                                  anchor_service_account=ANCHOR_SA, data_mode=mode,
                                  local_workload_name="local" if mode == "local" else None,
                                  standalone_workload_name="single")

    def test_local_init_envelope_and_main_readonly_mount_guards_are_rendered(self):
        for policy in self.api_policies(self.local_render()):
            variables = {item["name"]: item["expression"] for item in policy["spec"]["variables"]}
            names = json.loads(variables["localDataNames"]) + json.loads(variables["localCredentialNames"])
            self.assertEqual(len(names), 20)
            self.assertFalse(any("OIDC" in name or "ADMIN_TOKEN" in name or "SESSION_KEY" in name for name in names))
            initializer = variables["approvedLocalInitContainers"]
            for required in ("c.name == 'ternal-local-restore'", "c.command == ['/usr/local/bin/ternal-api']",
                             "c.args == ['local-restore']", "c.env.size() == variables.localRequiredDataNames.size() + variables.localRequiredCredentialNames.size() + 1", "c.securityContext.runAsUser == 65532",
                             "c.securityContext.readOnlyRootFilesystem", "!has(c.lifecycle)", "!has(m.subPath)",
                             "!has(m.subPathExpr)", "variables.serviceAccountVolumes.exists(v, v.name == m.name)",
                             "c.volumeMounts.filter(m, m.name == 'data' && m.mountPath == '/data' && (!has(m.readOnly) || !m.readOnly)).size() == 1"):
                self.assertIn(required, initializer)
            self.assertIn("e.valueFrom.fieldRef.fieldPath == 'metadata.uid'", initializer)
            self.assertIn("TERNAL_LOCAL_PREFLIGHT_SCOPE", initializer)
            for required in ("has(v.projected)", "has(s.serviceAccountToken)", "s.serviceAccountToken.path == 'token'",
                             "s.configMap.name == 'kube-root-ca.crt'", "fieldPath == 'metadata.namespace'"):
                self.assertIn(required, variables["serviceAccountVolumes"])
            envelope = policy["spec"]["validations"][-1]["expression"]
            self.assertIn("has(v.emptyDir) && has(dyn(v.emptyDir).sizeLimit)", envelope)
            self.assertNotIn("has(v.emptyDir.sizeLimit)", envelope)
            self.assertEqual(envelope.count("dyn("), 1)
            container_path = "object.spec.containers" if policy["spec"]["matchConstraints"]["resourceRules"][0]["resources"][0] == "pods" else "object.spec.template.spec.containers"
            self.assertIn(container_path + ".size() == 1", envelope)
            self.assertIn("variables.apiContainers[0].name == 'ternal-api'", envelope)
            for required in ("a.volumeMounts.filter(m, m.name == 'local-staging').size() == 1",
                             "a.volumeMounts.filter(m, m.name == 'data').size() == 1", "!has(m.subPath)",
                             "m.mountPath == '/staging' && has(m.readOnly) && m.readOnly", "request.name == 'ternal-local"):
                self.assertIn(required, envelope)
            if policy["spec"]["matchConstraints"]["resourceRules"][0]["resources"][0] == "pods":
                self.assertIn("r.kind == 'StatefulSet' && r.name == 'ternal-local'", envelope)
                self.assertIn("request.name == 'ternal-local-0'", envelope)
            else:
                self.assertIn("request.resource.resource == 'statefulsets'", envelope)

    def test_local_azure_env_remains_exact_and_secret_referenced(self):
        for policy in self.api_policies(self.local_render()):
            variables = {item["name"]: item["expression"] for item in policy["spec"]["variables"]}
            public = json.loads(variables["localAzureDataNames"])
            credentials = json.loads(variables["localAzureCredentialNames"])
            self.assertEqual(len(public), 6)
            self.assertEqual(credentials, ["TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT_KEY", "TERNAL_OBJECT_STORE_AZURE_CONNECTION_STRING", "TERNAL_OBJECT_STORE_AZURE_CLIENT_SECRET"])
            self.assertFalse(any(name in public for name in credentials))
            self.assertFalse(any("OIDC" in name or "SESSION_KEY" in name or "ADMIN_TOKEN" in name for name in public + credentials))
            self.assertIn("e.value == 'azure'", variables["localAzure"])
            self.assertEqual(variables["localRequiredCredentialNames"], "variables.localAzure ? variables.localAzureCredentialNames : variables.localCredentialNames")
            initializer = variables["approvedLocalInitContainers"]
            self.assertIn("e.valueFrom.secretKeyRef.key == e.name", initializer)
            self.assertIn("TERNAL_OBJECT_STORE_AZURE_EMULATOR' && e.value == '0'", initializer)

    def test_local_ephemeral_subresource_and_extra_container_guards_are_rendered(self):
        for policy in self.api_policies(self.local_render()):
            resources = policy["spec"]["matchConstraints"]["resourceRules"][0]["resources"]
            envelope = policy["spec"]["validations"][-1]["expression"]
            path = "object.spec" if resources[0] == "pods" else "object.spec.template.spec"
            self.assertIn(path + ".containers.size() == 1", envelope)
            self.assertIn("variables.apiContainers[0].name == 'ternal-api'", envelope)
            self.assertIn("(!has(" + path + ".ephemeralContainers) || " + path + ".ephemeralContainers.size() == 0)", envelope)
            if resources[0] == "pods":
                self.assertIn("pods/ephemeralcontainers", resources)
                self.assertIn("UPDATE", policy["spec"]["matchConstraints"]["resourceRules"][0]["operations"])
                self.assertNotIn("pods/status", resources)

    def test_resource_scope_and_anchor_contract(self):
        result = self.render()
        anchor = next(item for item in self.by_kind(result, "ConfigMap") if item["metadata"]["name"] == "ternal-trust-anchor")
        self.assertEqual(anchor["data"], {"format": "1", "clusterID": "ternal-cluster-a1", "storageID": "b" * 64,
                                          "epoch": "0", "token": TOKEN, "pendingEpoch": "", "pendingID": "", "bootstrap": "true"})
        role = self.by_kind(result, "Role")[0]
        self.assertEqual(role["rules"], [{"apiGroups": [""], "resources": ["configmaps"], "resourceNames": ["ternal-trust-anchor"], "verbs": ["get", "update"]}])
        namespace = self.by_kind(result, "Namespace")[0]
        selector = self.by_kind(result, "ValidatingAdmissionPolicyBinding")[0]["spec"]["matchResources"]["namespaceSelector"]
        self.assertEqual(selector, {"matchLabels": {"ternal.dev/trustguard-scope": namespace["metadata"]["labels"]["ternal.dev/trustguard-scope"]}})

    def test_api_and_anchor_guards_are_fail_closed(self):
        result = self.render()
        policies = self.by_kind(result, "ValidatingAdmissionPolicy")
        self.assertEqual(len(policies), 3)
        rendered = json.dumps(result, sort_keys=True)
        self.assertNotIn("paramKind", rendered)
        self.assertNotIn("paramRef", rendered)
        self.assertNotIn("parameters", rendered)
        self.assertIn("TER NAL", rendered.replace("TERNAL", "TER NAL"))
        self.assertIn("request.operation != 'DELETE'", rendered)
        self.assertIn("pendingEpoch", rendered)
        self.assertEqual(self.by_kind(result, "Secret"), [])
        api = [p for p in policies if "api-" in p["metadata"]["name"]]
        self.assertEqual({p["spec"]["matchConstraints"]["resourceRules"][0]["resources"][0] for p in api}, {"pods", "deployments"})
        for policy in policies:
            self.assertEqual(policy["spec"]["failurePolicy"], "Fail")

    def test_configmap_delete_selection_never_dereferences_object(self):
        result = self.render()
        configmap_policies = [p for p in self.by_kind(result, "ValidatingAdmissionPolicy")
                               if p["spec"]["matchConstraints"]["resourceRules"][0]["resources"] == ["configmaps"]]
        self.assertEqual(len(configmap_policies), 1)
        for policy in configmap_policies:
            condition = policy["spec"]["matchConditions"][0]["expression"]
            self.assertTrue(condition.startswith("request.name == "))
            self.assertNotIn("object.metadata", condition)
        # A ConfigMap named something else does not satisfy either exact-name
        # selection expression, including when DELETE has no object payload.
        self.assertTrue(all("unrelated-configmap" not in p["spec"]["matchConditions"][0]["expression"]
                            for p in configmap_policies))

    def test_official_repository_match_has_a_boundary(self):
        expression = trustguard.api_container_expr()
        self.assertIn("image == 'ghcr.io/mrchypark/ternal'", expression)
        self.assertIn("image.startsWith('ghcr.io/mrchypark/ternal:')", expression)
        self.assertIn("image.startsWith('ghcr.io/mrchypark/ternal@')", expression)
        self.assertNotIn("ternal-agent'", expression)

    def anchor_policy(self, result):
        return next(p for p in self.by_kind(result, "ValidatingAdmissionPolicy")
                    if p["spec"]["matchConstraints"]["resourceRules"][0]["resources"] == ["configmaps"])

    def test_anchor_contract_is_exactly_eight_or_twelve_keys(self):
        contract = self.anchor_policy(self.render())["spec"]["validations"][2]["expression"]
        for key in ("recoveryGeneration", "recoveryEvidence", "recoveryTransition", "recoveryReceipt"):
            self.assertIn("'" + key + "'", contract)
        self.assertIn("object.data.size() == 8", contract)
        self.assertIn("object.data.size() == 12", contract)
        self.assertNotIn("object.data.size() >= ", contract)

    def test_recovery_branches_are_identity_bound_and_monotonic(self):
        expression = self.anchor_policy(self.render())["spec"]["validations"][3]["expression"]
        identity = "request.userInfo.username == 'system:serviceaccount:ternal-test:ternal-anchor'"
        # Both recovery branches are gated on the protected anchor identity, and
        # the commit is the only place the binding and generation may move.
        self.assertEqual(expression.count(identity), 2)
        self.assertIn("uint(object.data['recoveryGeneration']) == uint(oldObject.data['recoveryGeneration']) + 1u", expression)
        self.assertIn("oldObject.data['recoveryTransition'] == 'null'", expression)
        self.assertIn("object.data['recoveryReceipt'] != 'null'", expression)
        # The commit is the one branch that moves the binding, so it must not
        # pin it: only the application half survives a recovery commit.
        # The commit branch is last, so everything after its marker is the
        # commit branch and nothing else.
        commit = expression.split("oldObject.data['recoveryTransition'] != 'null'", 1)[1]
        for key in trustguard.APPLICATION_FIELDS:
            self.assertIn("oldObject.data['" + key + "'] == object.data['" + key + "']", commit)
        for key in ("clusterID", "storageID"):
            self.assertNotIn("oldObject.data['" + key + "'] == object.data['" + key + "']", commit)
        # Every application branch keeps both binding fields, finalize and abort
        # included: no global binding pin remains once recovery may change it.
        self.assertIn("oldObject.data['clusterID'] == object.data['clusterID'] && oldObject.data['storageID'] == object.data['storageID'] && (oldObject.data['format']", expression)

    def test_migration_preserves_every_flat_value(self):
        expression = self.anchor_policy(self.render())["spec"]["validations"][3]["expression"]
        for key in trustguard.FLAT_FIELDS:
            self.assertIn("oldObject.data['" + key + "'] == object.data['" + key + "']", expression)
        self.assertIn("object.data['recoveryGeneration'] == '0'", expression)

    def test_anchor_role_needs_executable_and_its_own_account(self):
        result = self.render()
        rendered = json.dumps(result, sort_keys=True)
        self.assertIn(trustguard.ANCHOR_EXECUTABLE, rendered)
        self.assertIn("c.command.size() == 1", rendered)
        self.assertIn("c.env.exists(e, e.name == 'TERNAL_RECOVERY_ANCHOR_ID' && e.value == 'ternal-trust-anchor')", rendered)
        self.assertIn("(has(object.spec.serviceAccountName) ? object.spec.serviceAccountName : 'default') == 'ternal-anchor'", rendered)
        self.assertIn("(has(object.spec.serviceAccountName) ? object.spec.serviceAccountName : 'default') == 'ternal-data'", rendered)
        self.assertIn("variables.anchorContainers.size() == 0 || ", rendered)
        self.assertIn("variables.apiContainers.size() == 0 || ", rendered)

    def api_policies(self, result):
        return [p for p in self.by_kind(result, "ValidatingAdmissionPolicy")
                if p["spec"]["matchConstraints"]["resourceRules"][0]["resources"][0]
                in ("pods", "deployments")]

    def test_omitted_service_account_is_default_not_an_evaluation_error(self):
        result = self.render()
        for policy in self.api_policies(result):
            resources = policy["spec"]["matchConstraints"]["resourceRules"][0]["resources"]
            path = "object.spec.serviceAccountName" if resources == ["pods", "pods/ephemeralcontainers"] \
                else "object.spec.template.spec.serviceAccountName"
            # VAP evaluates before the ServiceAccount admission plugin defaults
            # an omitted name; a bare key access would be "no such key" and
            # fail-closed deny every controller/Pod that omits the field.
            guard = "(has({p}) ? {p} : 'default')".format(p=path)
            expressions = [v["expression"] for v in policy["spec"]["validations"]]
            self.assertTrue(any(guard in e for e in expressions), policy["metadata"]["name"])
            self.assertFalse(any("has(" not in e and path in e for e in expressions), policy["metadata"]["name"])

    def test_protected_service_account_gates_remain_fail_closed(self):
        result = self.render()
        pods, workloads = sorted(self.api_policies(result),
                                 key=lambda p: p["metadata"]["name"])
        pod_guard = "(has(object.spec.serviceAccountName) ? object.spec.serviceAccountName : 'default')"
        workload_guard = "(has(object.spec.template.spec.serviceAccountName) ? object.spec.template.spec.serviceAccountName : 'default')"
        for policy, guard, protected, anchor in (
                (pods, pod_guard, "ternal-data", "ternal-anchor"),
                (workloads, workload_guard, "ternal-data", "ternal-anchor")):
            expressions = [v["expression"] for v in policy["spec"]["validations"]]
            self.assertIn("variables.apiContainers.size() == 0 || " + guard + " == '" + protected + "'", expressions)
            self.assertIn("variables.anchorContainers.size() == 0 || " + guard + " == '" + anchor + "'", expressions)
            # An omitted name defaults to 'default', which never equals either
            # protected account, so protected containers stay denied.
            self.assertNotEqual("default", protected)
            self.assertNotEqual("default", anchor)
            self.assertEqual(policy["spec"]["failurePolicy"], "Fail")

    def test_rejects_tag_and_missing_image(self):
        with self.assertRaises(ValueError):
            trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, ["ghcr.io/mrchypark/ternal:old"], token=TOKEN, anchor_service_account=ANCHOR_SA)
        with self.assertRaises(ValueError):
            trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, [], token=TOKEN, anchor_service_account=ANCHOR_SA)
        for mode in ("ha", "local"):
            with self.assertRaisesRegex(ValueError, "recovery anchor service account is required"):
                trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, [IMAGE], token=TOKEN,
                                  data_mode=mode, local_workload_name="local" if mode == "local" else None)

    def test_standalone_without_recovery_keeps_anchor_guards_but_no_recovery_rbac(self):
        result = trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, [IMAGE], token=TOKEN)
        self.assertEqual(len(self.by_kind(result, "Role")), 1)
        bindings = self.by_kind(result, "RoleBinding")
        self.assertEqual(len(bindings), 1)
        self.assertEqual(bindings[0]["subjects"][0]["name"], "api")
        policies = self.by_kind(result, "ValidatingAdmissionPolicy")
        anchor = next(p for p in policies if "anchor-policy" in p["metadata"]["name"])
        expressions = [v["expression"] for v in anchor["spec"]["validations"]]
        self.assertEqual(expressions[0], "request.operation != 'DELETE'")
        self.assertIn("false && oldObject.data['pendingEpoch']", expressions[3])
        self.assertIn("false && oldObject.data['recoveryTransition']", expressions[3])
        self.assertNotIn("request.userInfo.username", expressions[3])
        for policy in self.api_policies(result):
            self.assertEqual(policy["spec"]["failurePolicy"], "Fail")
            expressions = [v["expression"] for v in policy["spec"]["validations"]]
            self.assertIn("variables.anchorContainers.size() == 0 || false", expressions)
            self.assertTrue(any("must directly bind" in v["message"] for v in policy["spec"]["validations"]))
            self.assertTrue(any("API image is not approved" in v["message"] for v in policy["spec"]["validations"]))

    def test_explicit_recovery_authority_is_preserved_and_empty_is_not_omission(self):
        args = ("ternal-test", "ternal-data", "ternal-trust-anchor", "ternal-cluster-a1", "b" * 64, [IMAGE])
        for mode in ("standalone", "ha"):
            result = trustguard.render(*args, token=TOKEN, anchor_service_account=ANCHOR_SA, data_mode=mode)
            self.assertEqual(result, self.render())
            self.assertEqual(len(self.by_kind(result, "Role")), 2)
            subjects = [item["subjects"][0]["name"] for item in self.by_kind(result, "RoleBinding")]
            self.assertEqual(subjects, ["ternal-data", ANCHOR_SA])
        with self.assertRaises(ValueError):
            trustguard.render(*args, token=TOKEN, anchor_service_account="")

    def test_recovery_identity_must_differ_from_application_identity(self):
        with self.assertRaisesRegex(ValueError, "must differ"):
            trustguard.render("ns", "api", "anchor", "cluster", "a" * 64,
                              [IMAGE], anchor_service_account="api")

    def test_api_guard_inlines_exact_current_and_candidate_images(self):
        candidate = "ghcr.io/mrchypark/ternal@sha256:" + "c" * 64
        result = trustguard.render("ternal-test", "ternal-data", "ternal-trust-anchor",
                                   "ternal-cluster-a1", "b" * 64, [IMAGE, candidate], token=TOKEN, anchor_service_account=ANCHOR_SA)
        rendered = json.dumps(result, sort_keys=True)
        self.assertIn("," + IMAGE + "," + candidate + ",", rendered)
        self.assertNotIn("params.data", rendered)
        for binding in self.by_kind(result, "ValidatingAdmissionPolicyBinding"):
            self.assertNotIn("paramRef", binding["spec"])

    def test_shared_suffix_cluster_ids_do_not_collide(self):
        shared = "x" * 35
        first = trustguard.render("ternal-test", "ternal-data", "ternal-trust-anchor",
                                  "aaaa" + shared, "b" * 64, [IMAGE], token=TOKEN, anchor_service_account=ANCHOR_SA)
        second = trustguard.render("ternal-test", "ternal-data", "ternal-trust-anchor",
                                   "bbbb" + shared, "b" * 64, [IMAGE], token=TOKEN, anchor_service_account=ANCHOR_SA)
        names = lambda result: sorted(item["metadata"]["name"] for item in result["items"])
        self.assertNotEqual(names(first), names(second))
        for name in names(first):
            # Bindings are DNS subdomains (253); the rest stay DNS labels.
            limit = 253 if name.endswith("-binding") else 63
            self.assertLessEqual(len(name), limit)


if __name__ == "__main__":
    unittest.main()

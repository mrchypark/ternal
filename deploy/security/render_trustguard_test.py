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

IMAGE = "ghcr.io/mrchypark/ternal@sha256:" + "a" * 64
TOKEN = "12345678-1234-1234-1234-123456789abc"
ANCHOR_SA = "ternal-anchor"


class TrustGuardRendererTest(unittest.TestCase):
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
        self.assertNotIn("secret", rendered.lower())
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
        self.assertIn("variables.anchorContainers.size() == 0 || object.spec.serviceAccountName == 'ternal-anchor'", rendered)
        self.assertIn("variables.apiContainers.size() == 0 || object.spec.serviceAccountName == 'ternal-data'", rendered)

    def test_rejects_tag_and_missing_image(self):
        with self.assertRaises(ValueError):
            trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, ["ghcr.io/mrchypark/ternal:old"], token=TOKEN, anchor_service_account=ANCHOR_SA)
        with self.assertRaises(ValueError):
            trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, [], token=TOKEN, anchor_service_account=ANCHOR_SA)
        with self.assertRaises(ValueError):
            trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, [IMAGE], token=TOKEN)

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

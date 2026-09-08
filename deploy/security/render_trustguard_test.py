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


class TrustGuardRendererTest(unittest.TestCase):
    def render(self):
        return trustguard.render("ternal-test", "ternal-data", "ternal-trust-anchor",
                                 "ternal-cluster-a1", "b" * 64, [IMAGE], token=TOKEN)

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

    def test_rejects_tag_and_missing_image(self):
        with self.assertRaises(ValueError):
            trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, ["ghcr.io/mrchypark/ternal:old"], token=TOKEN)
        with self.assertRaises(ValueError):
            trustguard.render("ns", "api", "anchor", "cluster", "a" * 64, [], token=TOKEN)

    def test_api_guard_inlines_exact_current_and_candidate_images(self):
        candidate = "ghcr.io/mrchypark/ternal@sha256:" + "c" * 64
        result = trustguard.render("ternal-test", "ternal-data", "ternal-trust-anchor",
                                   "ternal-cluster-a1", "b" * 64, [IMAGE, candidate], token=TOKEN)
        rendered = json.dumps(result, sort_keys=True)
        self.assertIn("," + IMAGE + "," + candidate + ",", rendered)
        self.assertNotIn("params.data", rendered)
        for binding in self.by_kind(result, "ValidatingAdmissionPolicyBinding"):
            self.assertNotIn("paramRef", binding["spec"])


if __name__ == "__main__":
    unittest.main()

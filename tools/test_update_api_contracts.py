"""Focused contracts for explicitly bounded recall candidate pools."""

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from update_api_contracts import Schemas


class RecallCandidateContractTest(unittest.TestCase):
    def schema(self, mode, name="memory.SpaceRecallQuality"):
        types = {name: {
            "params": [], "source": "internal/memory/recall_quality.go",
            "comment": "Recall quality settings.",
            "type": {"kind": "struct", "fields": [
                {"name": "candidate_pool", "optional": True, "comment": "",
                 "type": {"kind": "name", "name": "int"}},
                {"name": "evidence_selection", "optional": True, "comment": "",
                 "type": {"kind": "name", "name": "bool"}},
                {"name": "local_expand", "optional": True, "comment": "",
                 "type": {"kind": "name", "name": "bool"}},
            ]},
        }}
        generator = Schemas(types)
        ref = generator.named(name, mode)
        return generator.out[ref["$ref"].rsplit("/", 1)[1]]

    def test_request_and_response_share_exact_supported_budgets(self):
        for mode in ("Request", "Response"):
            with self.subTest(mode=mode):
                shape = self.schema(mode)
                pool = shape["properties"]["candidate_pool"]
                self.assertEqual(pool["type"], "integer")
                self.assertEqual(pool["enum"], [0, 50, 100])
                self.assertEqual(pool["default"], 0)
                self.assertNotIn("candidate_pool", shape.get("required", []))
                self.assertFalse(shape["additionalProperties"])

    def test_evidence_selection_is_an_optional_disabled_boolean(self):
        for mode in ("Request", "Response"):
            shape = self.schema(mode)
            flag = shape["properties"]["evidence_selection"]
            self.assertEqual(flag["type"], "boolean")
            self.assertIs(flag["default"], False)
            self.assertNotIn("evidence_selection", shape.get("required", []))

    def test_pool_constraint_does_not_change_local_expansion(self):
        for mode in ("Request", "Response"):
            with self.subTest(mode=mode):
                self.assertEqual(self.schema(mode)["properties"]["local_expand"],
                                 {"type": "boolean"})

    def test_same_field_name_in_other_dto_is_not_constrained(self):
        pool = self.schema("Request", "memory.UnrelatedSettings")["properties"]["candidate_pool"]
        self.assertEqual(pool, {"type": "integer"})


if __name__ == "__main__":
    unittest.main()

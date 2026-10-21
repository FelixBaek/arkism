import copy
import unittest
from jsonschema import ValidationError
from check_openapi import check_document, check_cases, read_json, ROOT


class OpenAPIContracts(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.doc = read_json(ROOT / "api/openapi.yaml")
        cls.meta = read_json(ROOT / "api/schema/openapi-3.1-2022-10-07.json")

    def test_published_document_is_structurally_valid(self):
        self.assertGreater(check_document(self.doc, self.meta), 200)

    def test_broken_ref_is_rejected(self):
        bad = copy.deepcopy(self.doc)
        bad["components"]["schemas"]["MemoryPage"]["properties"]["items"]["items"] = {"$ref": "#/components/schemas/Missing"}
        with self.assertRaises(KeyError):
            check_document(bad, self.meta)

    def test_missing_path_parameter_is_rejected(self):
        bad = copy.deepcopy(self.doc)
        bad["paths"]["/broken/{id}"] = {"get": {"responses": {"200": {"description": "ok"}}}}
        with self.assertRaisesRegex(ValueError, "path parameter"):
            check_document(bad, self.meta)

    def test_actual_response_validator_is_not_a_noop(self):
        case = {"operation": "GET /v1/memories", "status": 200, "body": {"items": [], "next_cursor": "", "authority": "data_only_untrusted"}}
        self.assertEqual(check_cases(self.doc, [case]), 1)
        for change in [{"authority": "trusted"}, {"unexpected": True}, {"next_cursor": 3}]:
            bad = copy.deepcopy(case)
            bad["body"].update(change)
            with self.assertRaises(ValidationError):
                check_cases(self.doc, [bad])

    def test_management_evidence_cannot_be_empty(self):
        with self.assertRaisesRegex(ValueError, "incomplete real management coverage"):
            check_cases(self.doc, [], require_management=True)

    def test_real_request_body_is_validated(self):
        case = {"operation": "POST /v1/context/tokens", "status": 200,
                "body": {"tokens": 1, "chars": 1, "token_encoding": "cl100k_base"},
                "request": {"scope": {"workspace_id": "a", "project_id": "b"}, "text": "x"}}
        self.assertEqual(check_cases(self.doc, [case]), 1)
        for request in [{"scope": {}, "text": 42}, {"text": "x"}, {"scope": {}, "text": "x", "unexpected": True}]:
            bad = copy.deepcopy(case)
            bad["request"] = request
            with self.assertRaises(ValidationError):
                check_cases(self.doc, [bad])


if __name__ == "__main__":
    unittest.main()

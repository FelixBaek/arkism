import copy
import unittest
from jsonschema import ValidationError
from check_openapi import check_document, check_cases, read_json, validate_value, ROOT


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
        bad["paths"]["/broken/{id}"] = {"get": {"operationId": "broken", "responses": {"200": {"description": "ok"}}}}
        with self.assertRaisesRegex(ValueError, "path parameter"):
            check_document(bad, self.meta)

    def test_operation_ids_are_required_and_unique(self):
        for op_id in (None, "getV1Memories", "bad-id"):
            bad = copy.deepcopy(self.doc)
            bad["paths"]["/broken"] = {"get": {"responses": {"200": {"description": "ok"}}}}
            if op_id:
                bad["paths"]["/broken"]["get"]["operationId"] = op_id
            with self.assertRaisesRegex(ValueError, "operationId"):
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

    def test_error_detail_is_optional_and_typed_without_opening_the_envelope(self):
        schema = {"$ref": "#/components/schemas/Error"}
        for body in ({"code": "invalid_request"},
                     {"code": "invalid_request", "detail": 'body: unknown field "extra"'},
                     {"code": "revision_conflict", "detail": "revision: sent 3, stored 5"},
                     {"code": "access_denied", "detail": "row policy refused the write"}):
            validate_value(self.doc, schema, body)
        for body in ({"detail": "missing code"},
                     {"code": "invalid_request", "detail": 3},
                     {"code": "invalid_request", "detail": None},
                     {"code": "invalid_request", "extra": "not documented"},
                     {"code": "not_a_real_error"}):
            with self.subTest(body=body), self.assertRaises(ValidationError):
                validate_value(self.doc, schema, body)

    def test_additional_emitted_error_codes_are_documented(self):
        for code in ("defense_blocked", "extraction_disabled", "no_extractable_text",
                     "model_not_configured", "invalid_model_output", "handle_taken",
                     "model_budget_exceeded", "rate_limited"):
            with self.subTest(code=code):
                validate_value(self.doc, {"$ref": "#/components/schemas/Error"}, {"code": code})
        self.assertEqual(check_cases(self.doc, [{"operation": "POST /v1/auth/google",
                                               "status": 429, "body": {"code": "rate_limited"}}]), 1)

    def test_anonymous_evidence_requires_authentication_challenge_status(self):
        doc = copy.deepcopy(self.doc)
        for path in doc["paths"].values():
            for operation in path.values():
                if isinstance(operation, dict):
                    operation.pop("x-go-contract", None)
        doc["paths"]["/v1/memories"]["get"]["x-go-contract"] = True
        cases = [
            {"operation": "GET /v1/memories", "status": 200,
             "body": {"items": [], "next_cursor": "", "authority": "data_only_untrusted"}},
            {"operation": "GET /v1/memories", "status": 401,
             "body": {"code": "authentication_required"}},
        ]
        self.assertEqual(check_cases(doc, cases, require_management=True), 2)
        cases[1] = {"operation": "GET /v1/memories", "status": 403,
                    "body": {"code": "access_denied"}}
        with self.assertRaisesRegex(ValueError, "incomplete real management coverage"):
            check_cases(doc, cases, require_management=True)

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

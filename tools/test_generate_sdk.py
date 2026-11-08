import importlib.util
import json
import os
import shutil
import subprocess
import tempfile
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import generate_sdk  # noqa: E402
import update_api_contracts  # noqa: E402


class GenerateSDKTest(unittest.TestCase):
    def test_types(self):
        t = generate_sdk.ts_type
        self.assertEqual(t({"type": ["string", "null"]}), "(string | null)")
        self.assertEqual(t({"$ref": "#/components/schemas/Scope"}), "Scope")
        self.assertEqual(t({"enum": ["a", "b"]}), '"a" | "b"')
        self.assertEqual(t({"type": "array", "items": {"type": "integer"}}), "Array<number>")
        self.assertEqual(t({"type": "object", "properties": {"id": {"type": "string"}}, "required": ["id"], "additionalProperties": False}), '{ "id": string }')
        self.assertEqual(t({"type": "object", "additionalProperties": {"type": "boolean"}}), "{ [key: string]: boolean }")

    def test_composed_schemas_preserve_known_siblings(self):
        t = generate_sdk.ts_type
        schema = {"type": "object", "properties": {"scope": {"type": "string"}}, "required": ["scope"], "additionalProperties": False,
                  "allOf": [{"if": {"required": ["extra"]}, "then": {"required": ["scope"]}}]}
        self.assertEqual(t(schema), '({ "scope": string })')
        self.assertEqual(t({"allOf": [{"$ref": "#/X"}, {"type": "object", "properties": {"id": {"type": "string"}}}]}), '(X & { "id"?: string })')
        self.assertIn('"scope": string', t(dict(schema, anyOf=[{"type": "object", "properties": {"limit": {"type": "number"}}}])))

    def test_real_search_context_types_compile(self):
        compiler = os.environ.get("TSC_BIN") or shutil.which("tsc")
        if not compiler:
            self.skipTest("set TSC_BIN to run the no-emit type acceptance check")
        ts, _ = generate_sdk.render(json.loads(generate_sdk.SPEC.read_text()))
        with tempfile.TemporaryDirectory(prefix="arkism-sdk-types-") as tmp:
            root = Path(tmp)
            (root / "client.ts").write_text(ts)
            (root / "acceptance.ts").write_text("""import type { SearchInput, ContextBuildInput, WireRequestSpaceRecallQuality, WireResponseSpaceRecallQuality } from './client';
const scope = { workspace_id: 'w', project_id: 'p' };
const search: SearchInput = {scope, query:'valve', conversation_context:true, budget_tokens:1000, token_encoding:'cl100k_base'};
const context: ContextBuildInput = {scope, task:'valve', token_encoding:'cl100k_base', whole_items:true, best_effort_lanes:true};
// @ts-expect-error required scope
const noScope: SearchInput = {query:'valve'};
// @ts-expect-error budget must be numeric
const badBudget: ContextBuildInput = {scope, task:'valve', token_encoding:'cl100k_base', budget_tokens:'many'};
// @ts-expect-error option must be boolean
const badOption: SearchInput = {scope, query:'valve', whole_items:'yes'};
const legacyPool: WireRequestSpaceRecallQuality = {};
const explicitLegacy: WireRequestSpaceRecallQuality = {candidate_pool:0};
const pool50: WireRequestSpaceRecallQuality = {candidate_pool:50, local_expand:false};
const pool100: WireResponseSpaceRecallQuality = {candidate_pool:100};
// @ts-expect-error only the explicit comparison budgets are accepted
const arbitraryPool: WireRequestSpaceRecallQuality = {candidate_pool:51};
// @ts-expect-error response budgets follow the same contract
const oversizedPool: WireResponseSpaceRecallQuality = {candidate_pool:101};
void [search,context,noScope,badBudget,badOption,legacyPool,explicitLegacy,pool50,pool100,arbitraryPool,oversizedPool];
""")
            command = [compiler, "--noEmit", "--strict", "--skipLibCheck", "--target", "ES2022", "--module", "ESNext", "--moduleResolution", "bundler", str(root / "acceptance.ts")]
            result = subprocess.run(command, capture_output=True, text=True, timeout=120)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_missing_operation_ids_are_filled_stably(self):
        spec = {"paths": {"/v1/x/{id}": {"get": {}, "post": {"operationId": "make_x"}}, "/v1/x/by_id": {"get": {}}}}
        update_api_contracts.fill_operation_ids(spec)
        names = [n for n, *_ in generate_sdk.operations(spec)]
        self.assertEqual(names, ["getV1XById", "make_x", "getV1XById2"])

    def test_path_level_parameters_merge(self):
        spec = {"paths": {"/v1/x/{id}": {"parameters": [{"name": "id", "in": "path"}, {"name": "X-A", "in": "header"}],
                                         "get": {"operationId": "x", "parameters": [{"name": "X-A", "in": "header", "required": True}]}}}}
        (_, _, _, op), = generate_sdk.operations(spec)
        self.assertEqual(op["parameters"], [{"name": "id", "in": "path"}, {"name": "X-A", "in": "header", "required": True}])
        ts = generate_sdk.ts_method("x", "get", "/v1/x/{id}", op)
        self.assertIn('x(id: string, headers: { "X-A": unknown })', ts)
        self.assertIn("undefined, undefined, headers);", ts)

    def test_multipart_body(self):
        op = {"requestBody": {"required": True, "content": {"multipart/form-data": {"schema": {"required": ["meta", "file"], "properties": {
            "meta": {"$ref": "#/components/schemas/M"}, "file": {"type": "string", "format": "binary"}}}}}}}
        ts = generate_sdk.ts_method("up", "post", "/v1/up", op)
        self.assertIn('up(body: { "meta": M; "file": Blob | string })', ts)
        self.assertIn('formData(body, [["meta", true], ["file", false]])', ts)
        self.assertIn("form=None if body is None else [(k, body.get(k), j) for k, j in [('meta', True), ('file', False)]]", generate_sdk.py_method("up", "post", "/v1/up", op))

    def test_committed_clients_are_current(self):
        self.assertEqual(generate_sdk.main(["--check"]), 0)

    def test_python_client_round_trip(self):
        seen = {}

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                raw = self.rfile.read(int(self.headers["Content-Length"]))
                if self.headers["Content-Type"].startswith("multipart/"):
                    seen["form"] = (self.headers["X-Arkism-Client"], self.headers["X-Skip"], raw)
                    self.send_response(200)
                    self.end_headers()
                    return
                seen["path"], seen["auth"] = self.path, self.headers["Authorization"]
                seen["body"] = json.loads(raw)
                self.send_response(403)
                self.end_headers()
                self.wfile.write(b'{"code":"denied"}')

            def log_message(self, *args):
                pass

        server = HTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        spec = importlib.util.spec_from_file_location("arkism_client", generate_sdk.PY_OUT)
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        client = mod.ArkismClient(f"http://127.0.0.1:{server.server_port}", token="t")
        with self.assertRaises(mod.ArkismError) as caught:
            client.request("POST", "/v1/a%20b", {"limit": 2, "skip": None}, {"q": 1})
        client.request("POST", "/v1/up", headers={"X-Arkism-Client": "sdk", "X-Skip": None}, form=[("meta", {"a": 1}, True), ("file", b"hi", False), ("none", None, False)])
        server.shutdown()
        client_header, skipped, form = seen.pop("form")
        self.assertEqual((client_header, skipped), ("sdk", None))
        self.assertLess(form.index(b'name="meta"\r\n\r\n{"a": 1}'), form.index(b'name="file"\r\n\r\nhi\r\n'))
        self.assertNotIn(b'name="none"', form)
        self.assertEqual(caught.exception.status, 403)
        self.assertEqual(caught.exception.body, {"code": "denied"})
        self.assertEqual(seen, {"path": "/v1/a%20b?limit=2", "auth": "Bearer t", "body": {"q": 1}})


if __name__ == "__main__":
    unittest.main()

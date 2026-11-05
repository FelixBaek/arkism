import importlib.util
import json
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import generate_sdk  # noqa: E402


class GenerateSDKTest(unittest.TestCase):
    def test_types(self):
        t = generate_sdk.ts_type
        self.assertEqual(t({"type": ["string", "null"]}), "(string | null)")
        self.assertEqual(t({"$ref": "#/components/schemas/Scope"}), "Scope")
        self.assertEqual(t({"enum": ["a", "b"]}), '"a" | "b"')
        self.assertEqual(t({"type": "array", "items": {"type": "integer"}}), "Array<number>")
        self.assertEqual(t({"type": "object", "properties": {"id": {"type": "string"}}, "required": ["id"], "additionalProperties": False}), '{ "id": string }')
        self.assertEqual(t({"type": "object", "additionalProperties": {"type": "boolean"}}), "{ [key: string]: boolean }")

    def test_names_are_unique_and_derived(self):
        spec = {"paths": {"/v1/x/{id}": {"get": {}, "post": {"operationId": "make_x"}}, "/v1/x/by_id": {"get": {}}}}
        names = [n for n, *_ in generate_sdk.operations(spec)]
        self.assertEqual(names, ["getV1XById", "make_x", "getV1XById2"])

    def test_committed_clients_are_current(self):
        self.assertEqual(generate_sdk.main(["--check"]), 0)

    def test_python_client_round_trip(self):
        seen = {}

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                seen["path"], seen["auth"] = self.path, self.headers["Authorization"]
                seen["body"] = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
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
        server.shutdown()
        self.assertEqual(caught.exception.status, 403)
        self.assertEqual(caught.exception.body, {"code": "denied"})
        self.assertEqual(seen, {"path": "/v1/a%20b?limit=2", "auth": "Bearer t", "body": {"q": 1}})


if __name__ == "__main__":
    unittest.main()

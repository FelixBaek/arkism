"""Offline tests of the generated client against a local fake server."""

import datetime
import email.utils
import http.server
import json
import os
import sys
import threading
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))

from arkism_client import RECALL_HEADER, ArkismClient, ArkismError, retry_after_seconds  # noqa: E402

SCOPE = {"workspace_id": "w", "project_id": "p"}


class FakeServer:
    """Answers queued (status, body, headers) replies in order; the last one repeats."""

    def __init__(self, replies):
        self.replies, self.calls = list(replies), []
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def handle_one(self):
                size = int(self.headers.get("Content-Length") or 0)
                body = json.loads(self.rfile.read(size)) if size else None
                outer.calls.append((self.command, self.path, dict(self.headers), body))
                status, payload, headers = outer.replies[min(len(outer.calls), len(outer.replies)) - 1]
                raw = json.dumps(payload).encode() if payload is not None else b""
                self.send_response(status)
                for k, v in (headers or {}).items():
                    self.send_header(k, v)
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            do_GET = do_POST = handle_one

        self.httpd = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.url = "http://127.0.0.1:%d" % self.httpd.server_address[1]

    def close(self):
        self.httpd.shutdown()
        self.httpd.server_close()


class ClientTest(unittest.TestCase):
    def serve(self, replies):
        server = FakeServer(replies)
        self.addCleanup(server.close)
        return server

    def test_retries_429_and_503_with_retry_after_bounded(self):
        server = self.serve([(429, None, {"Retry-After": "2"}), (503, None, {"Retry-After": "120"}), (200, {"ok": True}, None)])
        waits = []
        client = ArkismClient(server.url, token="t", max_delay=5, sleep=waits.append)
        self.assertEqual(client.request("GET", "/v1/ping"), {"ok": True})
        self.assertEqual(waits, [2.0, 5])
        self.assertEqual(len(server.calls), 3)
        self.assertEqual(server.calls[0][2]["Authorization"], "Bearer t")

    def test_backoff_doubles_then_error_surfaces(self):
        server = self.serve([(503, {"code": "busy"}, None)])
        waits = []
        client = ArkismClient(server.url, retries=3, base_delay=0.01, sleep=waits.append)
        with self.assertRaises(ArkismError) as caught:
            client.request("GET", "/v1/ping")
        self.assertEqual((caught.exception.status, caught.exception.body), (503, {"code": "busy"}))
        self.assertEqual(waits, [0.01, 0.02, 0.04])
        self.assertEqual(len(server.calls), 4)

    def test_other_errors_and_zero_retries_are_not_retried(self):
        for status, retries in ((500, 2), (429, 0)):
            server = self.serve([(status, None, None)])
            client = ArkismClient(server.url, retries=retries, sleep=lambda s: self.fail("slept"))
            with self.assertRaises(ArkismError):
                client.request("GET", "/v1/ping")
            self.assertEqual(len(server.calls), 1)

    def test_post_is_retried_only_when_turned_away_or_opted_in(self):
        server = self.serve([(503, {"code": "dependency_unavailable"}, None)])
        with self.assertRaises(ArkismError):
            ArkismClient(server.url, sleep=lambda s: self.fail("slept")).request("POST", "/v1/capture", None, {})
        self.assertEqual(len(server.calls), 1)
        server = self.serve([(429, None, {"Retry-After": "1"}), (200, {"ok": True}, None)])
        self.assertEqual(ArkismClient(server.url, sleep=lambda s: None).request("POST", "/v1/capture", None, {}), {"ok": True})
        self.assertEqual(len(server.calls), 2)
        server = self.serve([(503, None, None), (200, {"ok": True}, None)])
        self.assertEqual(ArkismClient(server.url, retry_unsafe=True, sleep=lambda s: None).request("POST", "/v1/search", None, {}), {"ok": True})
        self.assertEqual(len(server.calls), 2)

    def test_retry_after_reads_seconds_and_dates(self):
        now = datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc)
        later = email.utils.format_datetime(now + datetime.timedelta(seconds=4), usegmt=True)
        self.assertEqual(retry_after_seconds("3"), 3.0)
        self.assertEqual(retry_after_seconds(later, now=now), 4.0)
        self.assertIsNone(retry_after_seconds("soon"))
        self.assertIsNone(retry_after_seconds(None))

    def test_helpers_send_documented_bodies(self):
        server = self.serve([(200, {"items": []}, None)])
        client = ArkismClient(server.url)
        client.recall(SCOPE, "deploy steps", limit=5)
        client.reflect(SCOPE, "why?", max_tokens=100)
        client.remember(SCOPE, "Run staging first", kind="procedural", session_id="s1")
        self.assertEqual(server.calls[0][1:4:2], ("/v1/search", {"limit": 5, "scope": SCOPE, "query": "deploy steps"}))
        self.assertEqual(server.calls[1][1:4:2], ("/v1/reflect", {"max_tokens": 100, "scope": SCOPE, "query": "why?"}))
        _, path, _, capture = server.calls[2]
        self.assertEqual(path, "/v1/capture")
        self.assertTrue(capture["event"]["id"].startswith("sdk:"))
        self.assertEqual((capture["event"]["type"], capture["event"]["session_id"]), ("explicit_remember", "s1"))
        self.assertTrue(capture["event"]["occurred_at"].endswith("Z"))
        claim = capture["claims"][0]
        self.assertEqual((claim["kind"], claim["text"], claim["basis"]), ("procedural", "Run staging first", "inferred"))

    def test_recall_text_and_capture_text(self):
        hits = {"items": [{"kind": "procedural", "text": "Run staging first"}, {"kind": "semantic"}]}
        server = self.serve([(200, hits, None), (200, {"items": []}, None)])
        client = ArkismClient(server.url)
        self.assertEqual(client.recall_text(SCOPE, "deploy", limit=3), RECALL_HEADER + "\n- [procedural] Run staging first")
        self.assertEqual(client.recall_text(SCOPE, "nothing"), "")
        client.capture_text(SCOPE, "user: hi", document_id="s1", context="chat")
        client.capture_text(SCOPE, "plain")
        self.assertEqual(server.calls[0][3], {"limit": 3, "scope": SCOPE, "query": "deploy"})
        self.assertEqual(server.calls[2][1:4:2], ("/v1/memories/ingest", {"scope": SCOPE, "async": True, "items": [
            {"content": "user: hi", "context": "chat", "document_id": "s1", "update_mode": "append"}]}))
        self.assertEqual(server.calls[3][3]["items"], [{"content": "plain"}])

    def test_pages_follow_cursor(self):
        seen = []

        def page(cursor):
            seen.append(cursor)
            return {"c2": {"items": [3], "next_cursor": None}, "c1": {"items": [2], "next_cursor": "c2"}}.get(cursor, {"items": [1], "next_cursor": "c1"})

        client = ArkismClient("http://127.0.0.1:1")
        self.assertEqual(list(client.pages(page)), [1, 2, 3])
        self.assertEqual(seen, [None, "c1", "c2"])
        self.assertEqual(list(client.pages(page, max_pages=2)), [1, 2])

    def test_generated_method_builds_query(self):
        server = self.serve([(200, {"items": [], "next_cursor": None}, None)])
        ArkismClient(server.url).get_v1_memories({"workspace_id": "w s", "project_id": "p", "limit": 2})
        self.assertEqual(server.calls[0][1], "/v1/memories?workspace_id=w+s&project_id=p&limit=2")


if __name__ == "__main__":
    unittest.main()

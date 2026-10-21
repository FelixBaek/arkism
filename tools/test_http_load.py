import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread
from http_load import summarize, run


class HTTPMeasurementTests(unittest.TestCase):
    def test_unavailable_responses_are_counted_without_retry_or_body_output(self):
        class Unavailable(BaseHTTPRequestHandler):
            def do_POST(self):
                self.send_response(503)
                self.end_headers()
            def log_message(self, *args):
                # Keep the local fixture quiet; the report contains aggregate counts.
                return None
        server=ThreadingHTTPServer(("127.0.0.1",0),Unavailable)
        thread=Thread(target=server.serve_forever,daemon=True);thread.start()
        try:
            report=run(f"http://127.0.0.1:{server.server_port}","inert-token",{},requests=30,concurrency=2)
            self.assertEqual(report["http_statuses"],{"503":30})
            self.assertEqual(report["failed"],30)
            self.assertFalse(report["passed"])
        finally:
            server.shutdown();thread.join(timeout=2);server.server_close()

    def test_p99_and_regression_use_observed_http_samples(self):
        report=summarize([(True,float(n)) for n in range(1,101)],95)
        self.assertEqual((report["p50_ms"],report["p95_ms"],report["p99_ms"]),(50,95,99))
        self.assertTrue(report["passed"])
        self.assertFalse(summarize([(True,float(n)) for n in range(1,101)],94)["passed"])
    def test_failed_or_too_small_runs_cannot_pass(self):
        self.assertFalse(summarize([(True,1)]*29,800)["passed"])
        self.assertFalse(summarize([(True,1)]*30+[(False,1)],800)["passed"])
        self.assertFalse(summarize([],800)["passed"])

    def test_remote_or_ambiguous_hosts_are_rejected_before_egress(self):
        for base in ("https://outside.example", "http://localhost.evil", "http://user@127.0.0.1", "http://127.0.0.1/?redirect=outside"):
            with self.assertRaises(ValueError):
                run(base,"inert-token",{})

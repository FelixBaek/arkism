"""Preview server for the desktop UI with made-up data (no real Arkism needed).

    python3 -I testdata/preview/serve.py [port]   # serves desktop/ui, answers /v1/* from testdata/preview/v1/*.json

A GET for /v1/memories reads v1/memories.json; a missing file answers {"items": []}. Writes answer {} so buttons don't break.
All names, projects and texts in the data are made up.
"""
import http.server, json, os, sys
HERE = os.path.dirname(os.path.abspath(__file__))
UI = os.path.dirname(os.path.dirname(HERE))
PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8790

class H(http.server.SimpleHTTPRequestHandler):
    def __init__(s, *a, **k): super().__init__(*a, directory=UI, **k)
    def log_message(s, *a): pass
    def _json(s, code, obj):
        body = json.dumps(obj, ensure_ascii=False).encode()
        s.send_response(code); s.send_header("Content-Type", "application/json"); s.send_header("Content-Length", str(len(body))); s.end_headers(); s.wfile.write(body)
    def _fixture(s, suffix=""):
        # <path>.<status>.json wins for ?status=… (e.g. memory_candidates.conflicted.json); POST reads <path>.post.json.
        path, _, qs = s.path.partition("?")
        q = dict(p.split("=", 1) for p in qs.split("&") if "=" in p)
        status, kind = q.get("status", ""), q.get("kind", "")
        base = path.strip("/")
        # ?kind=… (digests) adds <path>.<kind>.<status>.json and <path>.<kind>.json in front.
        names = [base + "." + kind + "." + status] if kind and status and not suffix else []
        names += [base + "." + kind] if kind and not suffix else []
        names += [base + "." + status] if status and not suffix else []
        for name in names + [base + suffix]:
            f = os.path.normpath(os.path.join(HERE, name + ".json"))
            if f.startswith(HERE + os.sep) and os.path.isfile(f):
                with open(f, encoding="utf-8") as fh: return json.load(fh)
        return {"items": []} if not suffix else {}
    def do_GET(s):
        p = s.path.split("?")[0]
        if p in ("/readyz", "/healthz"): return s._json(200, {"status": "ready"})
        if p.startswith("/v1/"): return s._json(200, s._fixture())
        return super().do_GET()
    def _write(s):
        n = int(s.headers.get("Content-Length") or 0)
        if n: s.rfile.read(n)
        return s._json(200, s._fixture(".post") if s.command == "POST" else {})
    do_POST = do_PUT = do_PATCH = do_DELETE = _write

http.server.ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()

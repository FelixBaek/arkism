"""Preview server for the desktop UI with made-up data (no real Arkism needed).

    python3 -I testdata/preview/serve.py [port]   # serves desktop/ui, answers /v1/* from testdata/preview/v1/*.json

PREVIEW_MANY=300 swaps in a made-up big map: every topic gets that many memories, plus that many unsorted.
A GET for /v1/memories reads v1/memories.json; a missing file answers {"items": []}. Writes answer {} so buttons don't break.
All names, projects and texts in the data are made up.
"""
import http.server, json, os, sys
HERE = os.path.dirname(os.path.abspath(__file__))
UI = os.path.dirname(os.path.dirname(HERE))
PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8790

def many(n):
    import random
    r = random.Random(7); reach = ["personal", "general", "project"]; kinds = ["decision", "procedural", "lesson", "user", "episodic"]
    with open(os.path.join(HERE, "v1/digests.topic.active.json"), encoding="utf-8") as fh: topics = json.load(fh)
    nodes, mems, edges = [], [], []
    for t in topics["items"] + [None]:
        ids = [f"{t['id'] if t else 'un'}-{i}" for i in range(n)]
        if t: t["members"] = [{"collection": "memories", "id": x, "revision": 1} for x in ids]
        for x in ids:
            k = r.choice(kinds); nodes.append({"id": x, "reach": r.choice(reach), "kind": k, "uses": int(r.paretovariate(1.5)) - 1, "vector_pending": False})
            mems.append({"id": x, "text": f"{t['title'] if t else '분류 대기'} 예시 기억 {x}", "kind": k, "status": "active", "revision": 1, "created_at": "2026-10-13T11:16:34Z", "updated_at": "2026-10-13T11:16:34Z", "tags": []})
        for x in ids[1:]:
            if r.random() < 0.5: edges.append({"from": x, "to": r.choice(ids), "type": r.choice(["supports", "same_session", "same_session", "conflicts", "supersedes"])})
    for _ in range(n // 10): edges.append({"from": r.choice(nodes)["id"], "to": r.choice(nodes)["id"], "type": "supports"})
    return {"/v1/memory-graph": {"nodes": nodes, "edges": edges, "truncated": False}, "/v1/memories": {"items": mems}, "/v1/digests?topic": topics}
MANY = many(int(os.environ["PREVIEW_MANY"])) if os.environ.get("PREVIEW_MANY") else {}

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
        key = p + ("?topic" if "kind=topic" in s.path and "status=active" in s.path else "")
        if key in MANY: return s._json(200, MANY[key])
        if p.startswith("/v1/"): return s._json(200, s._fixture())
        if p in ("/", "/index.html"): # a plain browser gets the desktop stand-in so it opens on the local Arkism, not the SSH form
            with open(os.path.join(UI, "index.html"), encoding="utf-8") as fh: html = fh.read().replace("<head>", '<head><script src="/testdata/preview/fake-app.js"></script>', 1)
            body = html.encode(); s.send_response(200); s.send_header("Content-Type", "text/html; charset=utf-8"); s.send_header("Content-Length", str(len(body))); s.end_headers(); s.wfile.write(body); return
        return super().do_GET()
    def _write(s):
        n = int(s.headers.get("Content-Length") or 0)
        if n: s.rfile.read(n)
        return s._json(200, s._fixture(".post") if s.command == "POST" else {})
    do_POST = do_PUT = do_PATCH = do_DELETE = _write

http.server.ThreadingHTTPServer(("127.0.0.1", PORT), H).serve_forever()

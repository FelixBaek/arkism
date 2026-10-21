"""Measure actual HTTP search latency without emitting queries or credentials."""
import argparse
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
import ipaddress
import json
import math
import os
from pathlib import Path
import time
import urllib.parse
import urllib.error
import urllib.request
from operator_files import input_file


def summarize(samples, budget_ms):
    durations = sorted(ms for ok, ms in samples if ok)
    def percentile(p):
        return durations[max(0, math.ceil(len(durations) * p) - 1)] if durations else None
    return {"requests": len(samples), "successful": len(durations), "failed": len(samples)-len(durations),
            "p50_ms": percentile(.5), "p95_ms": percentile(.95), "p99_ms": percentile(.99),
            "p95_budget_ms": budget_ms,
            "passed": len(samples) >= 30 and len(durations) == len(samples) and percentile(.95) <= budget_ms,
            "boundary": "client HTTP roundtrip including auth, transport, service and response read"}


def run(base, token, body, requests=100, concurrency=4, budget_ms=800):
    url = urllib.parse.urlsplit(base)
    if url.username or url.password or url.query or url.fragment or url.path not in ("", "/"):
        raise ValueError("base URL must not contain credentials, path or query")
    try:
        local = ipaddress.ip_address(url.hostname).is_loopback
    except ValueError:
        local = False
    if not local or url.scheme not in ("http", "https"):
        raise ValueError("measurement requires a loopback IP; use SSH forwarding for remote servers")
    if not token or "\n" in token or "\r" in token or not 30 <= requests <= 10000 or not 1 <= concurrency <= 32:
        raise ValueError("invalid load budget or token")
    payload = json.dumps(body).encode()
    if len(payload) > 256 << 10:
        raise ValueError("request body too large")
    def call(_):
        # No proxy inheritance or redirects: credentials stay on the chosen endpoint.
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *args, **kwargs):
                return None
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect)
        req = urllib.request.Request(base.rstrip("/")+"/v1/search", data=payload,
            headers={"Authorization": "Bearer "+token, "Content-Type": "application/json"})
        start = time.perf_counter_ns()
        status = 0
        try:
            with opener.open(req, timeout=10) as response:
                status = response.status
                raw = response.read((2 << 20) + 1)
                ok = response.status == 200 and len(raw) <= 2 << 20 and isinstance(json.loads(raw), dict)
        except urllib.error.HTTPError as error:
            status = error.code
            error.close()
            ok = False
        except (OSError, ValueError):
            ok = False
        return ok, (time.perf_counter_ns()-start)/1e6, status
    with ThreadPoolExecutor(max_workers=concurrency) as pool:
        observations=list(pool.map(call, range(requests)))
        report=summarize([(ok,ms) for ok,ms,_ in observations],budget_ms)
        report["http_statuses"]=dict(Counter(str(status) for _,_,status in observations))
        return report


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("--base-url",required=True);p.add_argument("--token-file",required=True)
    p.add_argument("--body-file",required=True);p.add_argument("--requests",type=int,default=100)
    p.add_argument("--concurrency",type=int,default=4);p.add_argument("--p95-ms",type=float,default=800)
    args=p.parse_args()
    root=Path(os.environ.get("ARKISM_HTTP_LOAD_DIR",Path.home()/".config/arkism/http-load"))
    token_file=input_file(args.token_file,root,4096,private=True)
    body_file=input_file(args.body_file,root,256<<10)
    result=run(args.base_url,token_file.read_text().strip(),json.loads(body_file.read_text()),
               args.requests,args.concurrency,args.p95_ms)
    print(json.dumps(result,sort_keys=True))
    raise SystemExit(0 if result["passed"] else 1)


if __name__=="__main__":
    main()

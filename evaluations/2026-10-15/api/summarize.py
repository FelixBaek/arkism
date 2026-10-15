#!/usr/bin/env python3
"""Summarize observed test/HTTP timings; never infer model tokens or charges."""
import csv
import json
import re
import statistics
import sys
from collections import defaultdict
from pathlib import Path

root = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).parent
rows = [json.loads(line) for line in (root / 'results.jsonl').read_text().splitlines()]
counts = defaultdict(int)
checks = []
requests = []
for row in rows:
    if row.get('Action') in ('pass', 'fail', 'skip') and row.get('Test'):
        name = row['Test']
        counts[name] += 1
        checks.append(dict(test=name, replicate=counts[name], status=row['Action'], seconds=row.get('Elapsed')))
    match = re.search(r'request method=(\S+) path=(\S+) status=(\d+) elapsed_ms=([\d.]+)', row.get('Output', ''))
    if match:
        method, path, status, elapsed = match.groups()
        requests.append(dict(test=row.get('Test'), method=method, path=path, status=int(status), elapsed_ms=float(elapsed)))
for filename, entries in [('runs.csv', checks), ('requests.csv', requests)]:
    with (root / filename).open('w', newline='') as file:
        writer = csv.DictWriter(file, fieldnames=list(entries[0]))
        writer.writeheader()
        writer.writerows(entries)
search = [r['elapsed_ms'] for r in requests if r['path'] == '/v1/search' and r['status'] == 200]
summary = dict(
    baseline_commit='9379ee5f493716e1c80027c0beb4fd12283100cf',
    product_changes=False, postgres_image_id='sha256:f371b5f3f2ac0a05703f33d6e6134515fb2498cab708fb948a0aeb7481467c00',
    go_version='go1.27.1 linux/amd64', repeats=3,
    subtests_pass=sum(r['status']=='pass' and '/' in r['test'] for r in checks),
    subtests_fail=sum(r['status']=='fail' and '/' in r['test'] for r in checks),
    subtests_skip=sum(r['status']=='skip' and '/' in r['test'] for r in checks),
    actual_model_runs=0, human_approval='synthetic credential fixture, no person review',
    successful_http_search_ms=dict(n=len(search), mean=statistics.mean(search), stdev=statistics.stdev(search), minimum=min(search), maximum=max(search)),
    model_input_tokens=None, model_output_tokens=None, actual_cost_usd=None, human_review_seconds=None,
    limitations=['Search latency includes loopback HTTP and DB; no embedding provider.', 'MCP subprocess runtime labels do not constitute actual Claude/Codex model runs.', 'Timings under race detector and synthetic data are not production performance.'])
(root / 'summary.json').write_text(json.dumps(summary, indent=2)+'\n')
print(json.dumps(summary, indent=2))

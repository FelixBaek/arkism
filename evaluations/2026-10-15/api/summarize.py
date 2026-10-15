#!/usr/bin/env python3
"""Summarize observed test/HTTP timings; never infer model tokens or charges."""
import csv
import json
import re
import statistics
import sys
import tempfile
from collections import defaultdict
from pathlib import Path

REQUEST_PATTERN = re.compile(
    r'request method=(\S+) path=(\S+) status=(\d+) elapsed_ms=([\d.]+)'
)
RESULT_FILES = ('results.jsonl', 'runs.csv', 'requests.csv', 'summary.json')


def result_directory(argument=None):
    """Restrict CLI input to this evaluation or a disposable temp directory."""
    default = Path(__file__).resolve().parent
    candidate = Path(argument).resolve(strict=True) if argument else default
    allowed_roots = (default, Path(tempfile.gettempdir()).resolve())
    if not candidate.is_dir() or not any(
        candidate.is_relative_to(allowed) for allowed in allowed_roots
    ):
        raise ValueError('results directory must be within this evaluation or the temp directory')
    return candidate


def result_file(root, name):
    """Keep every fixed input/output inside the validated directory."""
    if name not in RESULT_FILES:
        raise ValueError('unknown result filename')
    target = root / name
    if target.is_symlink() or target.resolve().parent != root:
        raise ValueError('result files must not escape the results directory')
    return target


def observations(rows):
    counts = defaultdict(int)
    checks, requests = [], []
    for row in rows:
        if row.get('Action') in ('pass', 'fail', 'skip') and row.get('Test'):
            name = row['Test']
            counts[name] += 1
            checks.append({
                'test': name, 'replicate': counts[name],
                'status': row['Action'], 'seconds': row.get('Elapsed'),
            })
        match = REQUEST_PATTERN.search(row.get('Output', ''))
        if match:
            method, path, status, elapsed = match.groups()
            requests.append({
                'test': row.get('Test'), 'method': method, 'path': path,
                'status': int(status), 'elapsed_ms': float(elapsed),
            })
    return checks, requests


def write_csv(path, entries):
    with path.open('w', newline='') as file:
        writer = csv.DictWriter(file, fieldnames=list(entries[0]), lineterminator='\n')
        writer.writeheader()
        writer.writerows(entries)


def search_statistics(requests):
    search = [
        r['elapsed_ms'] for r in requests
        if r['path'] == '/v1/search' and r['status'] == 200
    ]
    return {
        'n': len(search), 'mean': statistics.mean(search),
        'stdev': statistics.stdev(search), 'minimum': min(search),
        'maximum': max(search),
    }


def subtest_count(checks, status):
    return sum(r['status'] == status and '/' in r['test'] for r in checks)


def build_summary(checks, requests):
    return {
        'baseline_commit': '9379ee5f493716e1c80027c0beb4fd12283100cf',
        'product_changes': False,
        'postgres_image_id': 'sha256:f371b5f3f2ac0a05703f33d6e6134515fb2498cab708fb948a0aeb7481467c00',
        'go_version': 'go1.27.1 linux/amd64', 'repeats': 3,
        'subtests_pass': subtest_count(checks, 'pass'),
        'subtests_fail': subtest_count(checks, 'fail'),
        'subtests_skip': subtest_count(checks, 'skip'),
        'actual_model_runs': 0,
        'human_approval': 'synthetic credential fixture, no person review',
        'successful_http_search_ms': search_statistics(requests),
        'model_input_tokens': None, 'model_output_tokens': None,
        'actual_cost_usd': None, 'human_review_seconds': None,
        'limitations': [
            'Search latency includes loopback HTTP and DB; no embedding provider.',
            'MCP subprocess runtime labels do not constitute actual Claude/Codex model runs.',
            'Timings under race detector and synthetic data are not production performance.',
        ],
    }


def main(argument=None):
    root = result_directory(argument)
    files = {name: result_file(root, name) for name in RESULT_FILES}
    rows = [json.loads(line) for line in files['results.jsonl'].read_text().splitlines()]
    checks, requests = observations(rows)
    write_csv(files['runs.csv'], checks)
    write_csv(files['requests.csv'], requests)
    summary_text = json.dumps(build_summary(checks, requests), indent=2)
    files['summary.json'].write_text(summary_text + '\n')
    print(summary_text)


if __name__ == '__main__':
    main(sys.argv[1] if len(sys.argv) > 1 else None)

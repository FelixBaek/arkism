#!/usr/bin/env python3
"""Measure per-release metrics on the synthetic fixture and append evaluation/metrics/<version>.json.

Metrics: capture compression, UserPromptSubmit recall hook latency, injected prompt tokens.
Raw numbers come from TestReleaseMetrics (internal/host/capture); no network, model or personal data.
"""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent
METRICS = ROOT / 'evaluation' / 'metrics'
START, END = '<!-- metrics:start -->', '<!-- metrics:end -->'


def quantile(values, p):
    """Nearest-rank quantile, matching tools/recall_inject_audit.py."""
    v = sorted(values)
    return v[round(p * (len(v) - 1))]


def summarize(raw):
    comp, inj, lat = raw['compression'], raw['injection'], raw['latency_ms']
    if not comp or not inj or not lat:
        raise ValueError('raw metrics are empty')
    recall = sum(r['recall_tokens'] for r in inj)
    static = sum(r['static_tokens'] for r in inj)
    return {
        'compression': {
            'tasks': len(comp),
            'ratio_bytes': round(sum(r['transcript_bytes'] for r in comp) / sum(r['memory_bytes'] for r in comp), 2),
            'ratio_tokens': round(sum(r['transcript_tokens'] for r in comp) / sum(r['memory_tokens'] for r in comp), 2),
        },
        'hook_latency_ms': {'n': len(lat), 'p50': round(quantile(lat, 0.5), 3),
                            'p95': round(quantile(lat, 0.95), 3), 'max': round(max(lat), 3)},
        'prompt_tokens': {
            'tasks': len(inj),
            'none_mean': 0,
            'recall_mean': round(recall / len(inj), 1),
            'static_mean': round(static / len(inj), 1),
            'savings_vs_static': round(1 - recall / static, 3),
            'relevant_hit_rate': sum(r['relevant_hit'] for r in inj) / len(inj),
            'distractor_hits': sum(r['distractor_hits'] for r in inj),
        },
    }


def table(results):
    """Markdown rows, oldest first, from the saved per-version results."""
    rows = ['| Version | Date | Commit | Compression (tokens) | Hook p50 / p95 / max ms | '
            'Recall tokens (static) | Savings vs static |',
            '| --- | --- | --- | --- | --- | --- | --- |']
    for r in sorted(results, key=lambda r: (r['date'], r['version'])):
        s = r['summary']
        lat, tok = s['hook_latency_ms'], s['prompt_tokens']
        rows.append(f"| {r['version']} | {r['date']} | `{r['commit'][:7]}` | "
                    f"{s['compression']['ratio_tokens']}x | {lat['p50']} / {lat['p95']} / {lat['max']} | "
                    f"{tok['recall_mean']} ({tok['static_mean']}) | {tok['savings_vs_static']:.1%} |")
    return '\n'.join(rows)


def update_readme(readme, results):
    text = readme.read_text()
    head, rest = text.split(START, 1)
    _, tail = rest.split(END, 1)
    readme.write_text(f'{head}{START}\n{table(results)}\n{END}{tail}')


def git(*args):
    return subprocess.run(['git', *args], cwd=ROOT, check=True, capture_output=True, text=True).stdout.strip()


def measure(rounds):
    with tempfile.TemporaryDirectory() as tmp:
        out = Path(tmp) / 'raw.json'
        env = {**os.environ, 'ARKISM_METRICS_OUT': str(out), 'ARKISM_METRICS_ROUNDS': str(rounds)}
        subprocess.run(['go', 'test', './internal/host/capture', '-run', '^TestReleaseMetrics$', '-count=1'],
                       cwd=ROOT, env=env, check=True)
        return json.loads(out.read_text())


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', help='result name; default `git describe --tags`')
    parser.add_argument('--rounds', type=int, default=40, help='hook calls per task')
    args = parser.parse_args(argv)
    version = args.version or git('describe', '--tags')
    raw = measure(args.rounds)
    result = {
        'version': version,
        'commit': git('rev-parse', 'HEAD'),
        'date': datetime.now(timezone.utc).date().isoformat(),
        'environment': {'go': subprocess.run(['go', 'env', 'GOVERSION'], capture_output=True, text=True).stdout.strip(),
                        'os': platform.system(), 'arch': platform.machine(), 'cpus': os.cpu_count()},
        'token_encoding': raw['token_encoding'],
        'rounds': raw['rounds'],
        'summary': summarize(raw),
        'compression': raw['compression'],
        'injection': raw['injection'],
    }
    METRICS.mkdir(exist_ok=True)
    (METRICS / f'{version}.json').write_text(json.dumps(result, indent=1) + '\n')
    update_readme(METRICS / 'README.md', [json.loads(p.read_text()) for p in METRICS.glob('v*.json')])
    json.dump(result['summary'], sys.stdout, indent=1)
    print()


if __name__ == '__main__':
    main()

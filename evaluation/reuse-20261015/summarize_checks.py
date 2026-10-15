#!/usr/bin/env python3
"""Retain compressed Go JSONL evidence and machine-readable check outcomes."""
import argparse
import collections
import gzip
import hashlib
import json
import pathlib
import re
from live_agents import confined_path


def source_path(raw):
    path = pathlib.Path(raw).resolve()
    evidence = pathlib.Path.home() / 'arkism-reuse-evidence-20261015'
    if path.is_relative_to(evidence.resolve()):
        return path
    return confined_path(raw)


def destination_file(directory, filename):
    path = (directory / filename).resolve()
    if path.parent != directory.resolve():
        raise ValueError('artifact symlink escapes destination')
    return path


def summarize(raw):
    counts = collections.Counter()
    top = collections.Counter()
    skips, failures, packages = [], [], []
    for line in raw.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        action = event['Action']
        test = event.get('Test')
        if test and action in ('pass', 'fail', 'skip'):
            counts[action] += 1
            if '/' not in test:
                top[action] += 1
            if action == 'skip':
                skips.append({'package': event['Package'], 'test': test})
        if action == 'fail':
            failures.append(event)
        if not test and action in ('pass', 'fail', 'skip'):
            packages.append({'package': event['Package'], 'result': action,
                             'seconds': event.get('Elapsed')})
    return counts, top, skips, failures, packages


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('source')
    parser.add_argument('destination')
    parser.add_argument('--label', required=True)
    parser.add_argument('--commit', required=True)
    args = parser.parse_args()
    if re.fullmatch(r'[A-Za-z0-9_-]{1,80}', args.label) is None:
        parser.error('label must contain only letters, digits, underscore or hyphen')
    source = source_path(args.source)
    destination = confined_path(args.destination)
    destination.mkdir(parents=True, exist_ok=True)
    raw = source.read_bytes()
    destination_file(destination, args.label + '.jsonl.gz').write_bytes(
        gzip.compress(raw, mtime=0))
    counts, top, skips, failures, packages = summarize(raw)
    result = {'label': args.label, 'product_commit': args.commit,
              'raw_sha256': hashlib.sha256(raw).hexdigest(),
              'counts_including_subtests': dict(counts), 'top_level_counts': dict(top),
              'skips': skips, 'failures': failures, 'packages': packages,
              'command': 'go test -race -tags=integration -json -count=1 ./...',
              'environment': 'disposable loopback pgvector Postgres 17; no model providers'}
    destination_file(destination, args.label + '-summary.json').write_text(
        json.dumps(result, indent=2) + '\n')
    print(json.dumps({'label': args.label, 'top_level': dict(top),
                      'all': dict(counts), 'packages': len(packages)}))


if __name__ == '__main__':
    main()

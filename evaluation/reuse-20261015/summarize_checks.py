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


def parsed_events(raw):
    for number, line in enumerate(raw.splitlines(), 1):
        try:
            event = json.loads(line)
        except ValueError as error:
            raise ValueError(f'invalid JSON event at line {number}') from error
        if not isinstance(event, dict):
            raise ValueError(f'invalid event object at line {number}')
        yield event


def track_completion(event, seen, finished, running_tests):
    action, package, test = event['Action'], event['Package'], event.get('Test')
    seen.add(package)
    if test and action == 'run':
        running_tests.add((package, test))
    if action in ('pass', 'fail', 'skip'):
        if test:
            running_tests.discard((package, test))
        else:
            finished.add(package)
            if action == 'fail':
                # A panic or timeout terminates tests without per-test events.
                running_tests.difference_update({item for item in running_tests if item[0] == package})


def track_test(event, counts, top, skips):
    action, test = event['Action'], event.get('Test')
    if test and action in ('pass', 'fail', 'skip'):
        counts[action] += 1
        top[action] += '/' not in test
        if action == 'skip':
            skips.append({'package': event['Package'], 'test': test})


def summarize(raw):
    counts, top = collections.Counter(), collections.Counter()
    skips, failures, packages = [], [], []
    seen, finished, running_tests = set(), set(), set()
    for event in parsed_events(raw):
        track_completion(event, seen, finished, running_tests)
        track_test(event, counts, top, skips)
        action, test = event['Action'], event.get('Test')
        if action == 'fail':
            failures.append(event)
        if not test and action in ('pass', 'fail', 'skip'):
            packages.append({'package': event['Package'], 'result': action,
                             'seconds': event.get('Elapsed')})
    if not seen or seen != finished or running_tests:
        raise ValueError('incomplete Go log: package or test completion is missing')
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
    counts, top, skips, failures, packages = summarize(raw)
    destination_file(destination, args.label + '.jsonl.gz').write_bytes(
        gzip.compress(raw, mtime=0))
    result = {'label': args.label, 'product_commit': args.commit,
              'raw_sha256': hashlib.sha256(raw).hexdigest(),
              'counts_including_subtests': dict(counts), 'top_level_counts': dict(top),
              'skips': skips, 'failures': failures, 'packages': packages,
              'command': 'go test -race -tags=integration -json -count=1 ./...',
              'environment': 'disposable loopback pgvector Postgres 17; no model providers'}
    with destination_file(destination, args.label + '-summary.json').open('w') as output:
        json.dump(result, output, indent=2)
        output.write('\n')
    print(json.dumps({'label': args.label, 'top_level': dict(top),
                      'all': dict(counts), 'packages': len(packages)}))


if __name__ == '__main__':
    main()

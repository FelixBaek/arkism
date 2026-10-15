#!/usr/bin/env python3
"""Audit recorded tokens and timing without estimating provider charges."""
import argparse
from decimal import Decimal, InvalidOperation
import hashlib
import json
import math
from pathlib import Path
import statistics
import fcntl
import os
from private_storage import private_file

CONDITIONS = ('none', 'static', 'arkism')
FIELDS = ('input_tokens', 'cached_input_tokens', 'output_tokens')


def read_observation(path):
    row = json.loads(path.read_text())
    run_id = path.parent.name
    events = json.loads(path.with_name('events.json').read_text())
    turns = [event['usage'] for event in events
             if event.get('type') == 'turn.completed' and 'usage' in event]
    if len(turns) != 1:
        raise ValueError(f'expected one completed usage event: {run_id}')
    usage = turns[0]
    for field in FIELDS:
        value = usage.get(field)
        if type(value) is not int or value < 0 or row.get(field) != value:
            raise ValueError(f'usage mismatch or missing {field}: {run_id}')
    if usage['cached_input_tokens'] > usage['input_tokens']:
        raise ValueError(f'cached input exceeds input: {run_id}')
    seconds = row.get('task_seconds')
    if type(seconds) not in (int, float) or not math.isfinite(seconds) or seconds < 0:
        raise ValueError(f'missing task timing: {run_id}')
    return dict(row, run_id=run_id,
                uncached_input_tokens=usage['input_tokens'] - usage['cached_input_tokens'])


def load_observations(results):
    observations, evidence, identities = [], [], set()
    for path in sorted(results.glob('*/result.json')):
        row = read_observation(path)
        identity = (row['task_id'], row['condition'], row['replicate'])
        if identity in identities or row['condition'] not in CONDITIONS:
            raise ValueError(f'duplicate or invalid cell: {row["run_id"]}')
        identities.add(identity)
        observations.append(row)
        for file in (path, path.with_name('events.json'), path.with_name('prompt.txt')):
            evidence.append({'path': file.relative_to(results).as_posix(),
                             'sha256': hashlib.sha256(file.read_bytes()).hexdigest()})
    if not observations:
        raise ValueError('no recorded runs')
    return observations, evidence, identities


def billed_amounts(observations, charges):
    billed = {}
    known_ids = {row['run_id'] for row in observations}
    for charge in charges or []:
        run_id = charge['run_id']
        amount = Decimal(str(charge['amount_usd']))
        if (run_id not in known_ids or run_id in billed or not amount.is_finite() or amount < 0
                or charge.get('basis') != 'provider_billed' or not charge.get('evidence_ref')):
            raise ValueError('charges require unique known runs, actual billed amounts and evidence references')
        billed[run_id] = amount
    return billed


def summarize_condition(rows, billed):
    costs = [billed[row['run_id']] for row in rows if row['run_id'] in billed]
    return {
        'runs': len(rows), 'successes': sum(row['success'] is True for row in rows),
        **{field: sum(row[field] for row in rows) for field in (*FIELDS, 'uncached_input_tokens')},
        'task_seconds_mean': statistics.mean(row['task_seconds'] for row in rows),
        'task_seconds_total': sum(row['task_seconds'] for row in rows),
        'billed_cost_observations': len(costs),
        'billed_cost_usd': str(sum(costs)) if len(costs) == len(rows) else None,
        'human_review_time_observations': sum(row.get('review_seconds') is not None for row in rows),
    }


def compare_pair(results, static, arkism, billed):
    costs_complete = all(row['run_id'] in billed for row in (static, arkism))
    return {
        'task_id': static['task_id'], 'replicate': static['replicate'],
        'prompt_bytes_equal': ((results / static['run_id'] / 'prompt.txt').read_bytes()
                               == (results / arkism['run_id'] / 'prompt.txt').read_bytes()),
        'arkism_minus_static_task_seconds': arkism['task_seconds'] - static['task_seconds'],
        **{f'arkism_minus_static_{field}': arkism[field] - static[field]
           for field in (*FIELDS, 'uncached_input_tokens')},
        'static_minus_arkism_billed_usd': str(billed[static['run_id']] - billed[arkism['run_id']])
        if costs_complete else None,
    }


def paired_comparisons(results, observations, identities, billed):
    comparisons = []
    for task_id, replicate in sorted({(task, rep) for task, _, rep in identities}):
        pair = {row['condition']: row for row in observations
                if row['task_id'] == task_id and row['replicate'] == replicate}
        if 'static' in pair and 'arkism' in pair:
            comparisons.append(compare_pair(results, pair['static'], pair['arkism'], billed))
    return comparisons


def audit(results, charges=None):
    results = Path(results)
    observations, evidence, identities = load_observations(results)
    billed = billed_amounts(observations, charges)
    groups = {}
    for condition in CONDITIONS:
        rows = [row for row in observations if row['condition'] == condition]
        if rows:
            groups[condition] = summarize_condition(rows, billed)
    comparisons = paired_comparisons(results, observations, identities, billed)
    return {'schema_version': 1, 'recorded_runs': len(observations),
            'usage_crosschecked_runs': len(observations), 'conditions': groups,
            'billed_evidence': list(charges or []),
            'paired_static_arkism': comparisons, 'evidence': evidence,
            'limitations': [
                'Tokens are provider-reported observations, not billed charges.',
                'Cached input is a subset of input; do not add it again.',
                'Task time excludes retrieval, setup and human review.',
                'Provider prefix caching and execution order confound latency comparisons.',
                'Missing billed cost and human time remain unknown, never zero.',
                'These short synthetic tasks do not establish savings in repository work.',
            ]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--charges', action='store_true', help='Read private charges.jsonl')
    parser.add_argument('--output', action='store_true', help='Write private measurement-audit.json')
    args = parser.parse_args()
    try:
        charges = None
        if args.charges:
            with private_file('charges') as stream:
                charges = [json.loads(line) for line in stream if line.strip()]
        report = audit(Path(__file__).resolve().parent / 'results', charges)
        rendered = json.dumps(report, ensure_ascii=False, indent=2) + '\n'
        if args.output:
            with private_file('audit', create=True) as stream:
                fcntl.flock(stream, fcntl.LOCK_EX)
                stream.seek(0)
                stream.write(rendered)
                stream.truncate()
                stream.flush()
                os.fsync(stream.fileno())
        else:
            print(rendered, end='')
    except (ValueError, OSError, KeyError, InvalidOperation) as error:
        parser.error(str(error))


if __name__ == '__main__':
    main()

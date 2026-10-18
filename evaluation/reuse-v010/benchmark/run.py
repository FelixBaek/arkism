#!/usr/bin/env python3
"""Frozen synthetic code benchmark; no production memories/settings loaded.
Requires codex auth; auth bytes are copied to a private temporary profile and removed.
The model sees task + selected fact, never evaluator checks.
"""
import argparse
import csv
import hashlib
import json
import os
import pathlib
import shutil
import statistics
import stat
import subprocess
import tempfile
import time
import uuid
ROOT = pathlib.Path(__file__).resolve().parent
BASELINE = '9379ee5f493716e1c80027c0beb4fd12283100cf'
MODEL = 'gpt-6-astra'
TASKS = [
 {'id':'billing','scenario':'past_rounding_error','fact':'Amounts are integer cents; partial-cent charges round down.', 'task':'Implement charge(cents, numerator, denominator) returning a nonnegative integer charge. Inputs are nonnegative integers and denominator is positive. Follow the project billing policy.', 'starter':'def charge(cents, numerator, denominator):\n    raise NotImplementedError\n', 'checks':'assert charge(105,1,2)==52\nassert charge(7,1,2)==3\nassert charge(100,1,2)==50\nassert charge(0,3,2)==0'},
 {'id':'roster','scenario':'persistent_project_rule','fact':'Jobs are permitted Monday–Thursday; Friday–Sunday are blackout days.', 'task':'Implement allowed(weekday) returning whether a job may run. weekday is an integer 0 (Monday) through 6 (Sunday). Follow the project scheduling policy.', 'starter':'def allowed(weekday):\n    raise NotImplementedError\n','checks':'assert [allowed(i) for i in range(7)] == [True,True,True,True,False,False,False]'},
 {'id':'labels','scenario':'past_normalization_error','fact':'Trim surrounding whitespace but preserve letter case.', 'task':'Implement normalize(label) for a string label according to the project normalization policy.', 'starter':'def normalize(label):\n    raise NotImplementedError\n','checks':'assert normalize("  AbC  ")=="AbC"\nassert normalize("\\n MiXeD\\t")=="MiXeD"\nassert normalize("a b")=="a b"\nassert normalize("")==""'},
 {'id':'cache','scenario':'conditional_zero_ttl','fact':'Zero TTL means do not cache; positive TTL stays unchanged.', 'task':'Implement cache_ttl(ttl) for a nonnegative integer TTL. Return None to bypass caching or an integer expiration TTL according to the project policy.', 'starter':'def cache_ttl(ttl):\n    raise NotImplementedError\n','checks':'assert cache_ttl(0) is None\nassert cache_ttl(1)==1\nassert cache_ttl(120)==120'},
 {'id':'handoff','scenario':'agent_handoff_retry','fact':'Retrying an event with an already-recorded event_id must leave the ledger unchanged; new events append in arrival order.', 'task':'Implement apply_event(ledger, event), returning a list of event dicts. Each event has event_id and value. This function is called by a different worker after handoff. Follow the project retry policy.', 'starter':'def apply_event(ledger, event):\n    raise NotImplementedError\n','checks':'a=[{"event_id":"a","value":1}]\nassert apply_event(a,{"event_id":"a","value":999}) == [{"event_id":"a","value":1}]\nb=[{"event_id":"z","value":2}]\nassert apply_event(b,{"event_id":"a","value":3}) == [{"event_id":"z","value":2},{"event_id":"a","value":3}]\nassert apply_event([],{"event_id":"a","value":3}) == [{"event_id":"a","value":3}]'}
]

# Replay the original seed-20261015 permutation, not security-sensitive randomness.
FROZEN_ORDER = (9, 10, 24, 3, 4, 16, 44, 23, 6, 19, 18, 32, 26, 20, 15, 40, 36, 37, 22, 29, 11, 39, 8, 41, 42, 5, 17, 7, 38, 35, 21, 31, 14, 34, 25, 27, 33, 43, 1, 2, 0, 30, 28, 12, 13)
PRIVATE_ROOT = pathlib.Path.home() / '.local' / 'state' / 'arkism-benchmark'
EXPORTER = str(PRIVATE_ROOT / 'exporter.test')
OUTPUT_LOCATIONS = {
    'evaluation/reuse-v010/benchmark/results': ROOT / 'results',
    str(ROOT / 'results'): ROOT / 'results',
    str(PRIVATE_ROOT / 'results'): PRIVATE_ROOT / 'results',
}
CONDITIONS = ('none', 'static', 'arkism')
PROTOCOL_FIELDS = ('experiment_id', 'commit', 'fixture_sha256', 'model')


def private_directory(path):
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.lstat()
    if path.is_symlink() or not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise ValueError('evaluation directory must be user-owned with mode 0700')


def validate_exporter():
    private_directory(PRIVATE_ROOT)
    path = pathlib.Path(EXPORTER)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o022 or not os.access(path, os.X_OK):
        raise ValueError('exporter must be a user-owned executable, not a symlink or shared-writable file')


def write_json(path, value):
    with path.open('w') as handle:
        json.dump(value, handle, indent=2)


def remove_judge_container(name):
    cleaned = subprocess.run(['docker', 'rm', '--force', name], text=True,
                             capture_output=True, timeout=10)
    if cleaned.returncode and 'No such container' not in cleaned.stderr:
        raise RuntimeError('judge container cleanup failed: ' + cleaned.stderr[-2000:])


def evaluate(code, checks, *, timeout=20):
    # Generated code gets no credentials, host mounts, or network. A unique name
    # lets the host remove the daemon-owned container even if docker CLI times out.
    name = 'arkism-judge-' + uuid.uuid4().hex
    marker = 'ARKISM_JUDGE_COMPLETED_' + uuid.uuid4().hex
    driver = ("import sys; source=sys.stdin.read(); "
              "exec(compile(source,'<judge>','exec'),{}); "
              "sys.stdout.write(" + repr('\n' + marker + '\n') + ")")
    command = [
        'docker', 'run', '--rm', '--name', name, '--network', 'none', '--read-only',
        '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
        '--user', '65534:65534', '--memory', '128m', '--cpus', '1', '-i',
        'python:3.12-alpine', 'python', '-I', '-c', driver,
    ]
    try:
        process = subprocess.run(command, input=code + '\n' + checks, text=True,
                                 capture_output=True, timeout=timeout)
        completed = marker in process.stdout.splitlines()
        success = process.returncode == 0 and completed
        diagnostic = process.stderr[-2000:]
        if process.returncode == 0 and not completed:
            diagnostic = 'judge exited before completing all checks'
        return success, diagnostic
    finally:
        remove_judge_container(name)


def expected_protocol():
    """Keep new records and resume checks bound to the same execution identity."""
    return {
        'experiment_id': 'v010-synthetic-code-20261015', 'commit': BASELINE,
        'fixture_sha256': hashlib.sha256(json.dumps(TASKS, sort_keys=True).encode()).hexdigest(),
        'model': MODEL,
    }


def initial_record(task, condition, rep, ordinal, run_dir, approval):
    protocol = expected_protocol()
    return {
        'experiment_id': protocol['experiment_id'], 'commit': protocol['commit'],
        'fixture_sha256': protocol['fixture_sha256'],
        'task_id': task['id'], 'scenario': task['scenario'], 'condition': condition,
        'direction': 'host-retrieval-to-codex', 'replicate': rep, 'order': ordinal,
        'model': protocol['model'], 'reasoning_effort': 'low', 'temperature': None, 'seed': None,
        'success': None, 'timeout': False, 'rule_violations': None, 'rework_count': 0,
        'input_tokens': None, 'output_tokens': None, 'cached_input_tokens': None,
        'provider_cost_usd': None, 'setup_seconds': None, 'maintenance_seconds': None,
        'review_seconds': None, 'retrieval_ms': None, 'task_seconds': None,
        'manual_interventions': 0,
        'approval_basis': approval if condition == 'arkism' else None,
        'evidence_path': run_dir.name,
    }


def retrieve_fact(task, run_dir, record, exporter_enabled):
    if not exporter_enabled:
        raise RuntimeError('Arkism requires real HTTP exporter; no simulation fallback')
    validate_exporter()
    env = os.environ.copy()
    env.update(ARKISM_BENCH_FACT=task['fact'],
               ARKISM_BENCH_OUTPUT=str((run_dir / 'retrieval.json').resolve()))
    # The CLI can opt into this fixed test binary, never supply an executable.
    process = subprocess.run(
        [EXPORTER, '-test.run=^TestBenchmarkHTTPFactExport$', '-test.v'],
        env=env, text=True, capture_output=True, timeout=90,
    )
    (run_dir / 'retrieval.log').write_text(process.stdout + process.stderr)
    if process.returncode:
        raise RuntimeError('HTTP exporter failed; see retrieval.log')
    retrieved = json.loads((run_dir / 'retrieval.json').read_text())
    if retrieved['text'] != task['fact']:
        raise RuntimeError('static and Arkism information differ')
    record.update(retrieval_ms=retrieved['retrieval_ms'],
                  setup_seconds=retrieved['machine_setup_seconds'],
                  returned_rule_ids=retrieved['returned_rule_ids'],
                  source_ids=retrieved['source_ids'])
    return retrieved['text']


def make_prompt(task, fact):
    return (
        'Write Python code completing the supplied module. Return JSON only, '
        'with one string key "code" containing the whole module. Use no tools. '
        'Do not ask questions; make the best implementation from the available information.\nTask: '
        + task['task'] + '\nStarting module:\n' + task['starter']
        + '\nProject context:\n' + (fact or '(No additional project context.)')
    )


def isolated_environment(home, profile):
    excluded = ('CODEX', 'CLAUDE', 'ANTHROPIC', 'OPENAI', 'ARKISM', 'MEMORY')
    env = {key: value for key, value in os.environ.items()
           if not key.startswith(excluded) and key != 'POSTGRES_TEST_DSN'}
    env.update(HOME=str(home), CODEX_HOME=str(profile))
    env['PATH'] = os.environ['PATH']
    return env


def model_command(home):
    # Preserve the observed experiment's settings, including its documented
    # ignored output-cap warning, rather than silently changing reproduction.
    return [
        'codex', 'exec', '--ignore-user-config', '--ignore-rules', '--ephemeral',
        '--skip-git-repo-check', '--json', '-m', MODEL,
        '-c', 'model_reasoning_effort="low"', '-c', 'project_doc_max_bytes=0',
        '-c', 'model_max_output_tokens=2048', '-C', str(home), '-',
    ]


def parse_events(stdout, record):
    events = []
    answer = ''
    for line in stdout.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get('type') == 'thread.started':
            continue
        events.append(event)
        if event.get('type') == 'turn.completed':
            usage = event.get('usage', {})
            record.update({key: usage.get(key) for key in
                           ('input_tokens', 'output_tokens', 'cached_input_tokens')})
        item = event.get('item', {})
        if event.get('type') == 'item.completed' and item.get('type') == 'agent_message':
            answer = item['text']
    return events, answer


def judge_answer(answer, task, run_dir, record):
    if answer.startswith('```'):
        answer = '\n'.join(answer.splitlines()[1:-1])
    try:
        code = json.loads(answer)['code']
        (run_dir / 'answer.py').write_text(code)
        try:
            success, error = evaluate(code, task['checks'])
        except subprocess.TimeoutExpired:
            success, error = False, 'judge exceeded 20-second limit'
            record['judge_timeout'] = True
        record.update(success=success, rule_violations=int(not success))
        write_json(run_dir / 'judge.json', {'success': success, 'stderr': error})
    except (ValueError, KeyError, TypeError) as exc:
        record.update(success=False, rule_violations=1, parse_error=type(exc).__name__)


def collect_model_result(process, home, task, run_dir, record):
    events, answer = parse_events(process.stdout, record)
    write_json(run_dir / 'events.json', events)
    write_json(run_dir / 'cli-status.json', {
        'returncode': process.returncode,
        'stderr': process.stderr.replace(str(home), '<isolated-profile>'),
    })
    if process.returncode != 0:
        record.update(success=False, rule_violations=None, cli_returncode=process.returncode)
        return
    judge_answer(answer, task, run_dir, record)


def run_model(prompt, task, run_dir, auth, record):
    with tempfile.TemporaryDirectory(prefix='arkism-model-') as tmp:
        home = pathlib.Path(tmp)
        profile = home / 'profile'
        profile.mkdir()
        shutil.copyfile(auth, profile / 'auth.json')
        (profile / 'auth.json').chmod(0o600)
        started = time.monotonic()
        try:
            process = subprocess.run(
                model_command(home), input=prompt,
                env=isolated_environment(home, profile), cwd=home,
                text=True, capture_output=True, timeout=90,
            )
        except subprocess.TimeoutExpired:
            record.update(timeout=True, success=False,
                          task_seconds=time.monotonic() - started)
            return
        record['task_seconds'] = time.monotonic() - started
        collect_model_result(process, home, task, run_dir, record)


def run_one(task, condition, rep, ordinal, out, auth, exporter, approval):
    run_dir = out / f'{task["id"]}-{condition}-{rep}'
    run_dir.mkdir(exist_ok=True)
    record = initial_record(task, condition, rep, ordinal, run_dir, approval)
    fact = task['fact'] if condition == 'static' else ''
    if condition == 'arkism':
        fact = retrieve_fact(task, run_dir, record, exporter)
    prompt = make_prompt(task, fact)
    (run_dir / 'prompt.txt').write_text(prompt)
    run_model(prompt, task, run_dir, auth, record)
    write_json(run_dir / 'result.json', record)
    fields = ('task_id', 'condition', 'replicate', 'success', 'task_seconds')
    print(json.dumps({key: record[key] for key in fields}), flush=True)
    return record


def frozen_schedule():
    cells = [(task, condition, rep) for task in TASKS for condition in CONDITIONS
             for rep in range(1, 4)]
    return [cells[index] for index in FROZEN_ORDER]


def frozen_spec(schedule):
    """Build the original spec without adding metadata to archived experiments."""
    return {
        'baseline': BASELINE, 'tasks': TASKS,
        'schedule': [(task['id'], condition, rep) for task, condition, rep in schedule],
        'limitations': ['host retrieves context; model does not call MCP',
                        'no real agent A capture', 'synthetic short code tasks',
                        'equal configured budgets, different observed input token counts'],
        'improvement_threshold': 'At least 3 additional successes over static in 15 paired attempts, without extra rule violations; exploratory signal only, not general productivity proof.',
    }


def write_spec(out, schedule):
    write_json(out / 'frozen-spec.json', frozen_spec(schedule))


def validate_resume_spec(out, schedule, has_results):
    """Validate the full legacy spec and leave matching original bytes intact."""
    try:
        saved = json.loads((out / 'frozen-spec.json').read_text())
    except FileNotFoundError:
        if has_results:
            raise ValueError('benchmark results require their frozen spec') from None
        return False
    # JSON serialization normalizes tuples but preserves bool/int/float types.
    canonical = {'sort_keys': True, 'separators': (',', ':'), 'allow_nan': False}
    if json.dumps(saved, **canonical) != json.dumps(frozen_spec(schedule), **canonical):
        raise ValueError('frozen benchmark spec differs from the requested experiment')
    return True


def token_total(rows, field):
    observed = [row[field] for row in rows if row[field] is not None]
    total = sum(observed) if rows and len(observed) == len(rows) else None
    return total, len(observed)


def summarize_condition(rows):
    times = [row['task_seconds'] for row in rows if row['task_seconds'] is not None]
    inputs, input_coverage = token_total(rows, 'input_tokens')
    outputs, output_coverage = token_total(rows, 'output_tokens')
    return {
        'runs': len(rows), 'successes': sum(row['success'] is True for row in rows),
        'rule_violations': sum(row['rule_violations'] or 0 for row in rows),
        'timeouts': sum(row['timeout'] for row in rows),
        'seconds_mean': statistics.mean(times) if times else None,
        'seconds_stdev': statistics.stdev(times) if len(times) > 1 else None,
        'seconds_min': min(times) if times else None,
        'seconds_max': max(times) if times else None,
        'input_tokens': inputs, 'output_tokens': outputs,
        'input_token_observations': input_coverage,
        'output_token_observations': output_coverage, 'actual_cost_usd': None,
    }


def result_identity(row):
    """Validate one result and return its cell and execution identity."""
    if not isinstance(row, dict):
        raise ValueError('benchmark result must be a JSON object')
    for field in ('task_id', 'condition', *PROTOCOL_FIELDS):
        value = row.get(field)
        if not isinstance(value, str) or not value.strip():
            raise ValueError(f'missing or invalid benchmark identity: {field}')
    if type(row.get('replicate')) is not int or row['replicate'] < 1:
        raise ValueError('missing or invalid benchmark identity: replicate')
    if row['condition'] not in CONDITIONS:
        raise ValueError('invalid benchmark condition')
    identity = (row['task_id'], row['condition'], row['replicate'])
    return identity, tuple(row[field] for field in PROTOCOL_FIELDS)


def validate_result_identities(rows):
    """Require usable identities; reject duplicate cells and mixed executions."""
    identities, protocol = set(), None
    for row in rows:
        identity, current = result_identity(row)
        if identity in identities:
            raise ValueError('duplicate benchmark result cell')
        identities.add(identity)
        if protocol is not None and current != protocol:
            raise ValueError('inconsistent benchmark experiment, commit, fixture or model')
        protocol = current
    return identities


def resume_results(out, schedule):
    """Bind all saved rows to the requested execution and their planned folders."""
    saved = [(path, json.loads(path.read_text())) for path in out.glob('*/result.json')]
    rows = [row for _, row in saved]
    completed = validate_result_identities(rows)
    requested = expected_protocol()
    expected = tuple(requested[field] for field in PROTOCOL_FIELDS)
    planned = {(task['id'], condition, rep): ordinal
               for ordinal, (task, condition, rep) in enumerate(schedule)}
    for path, row in saved:
        identity, protocol = result_identity(row)
        if protocol != expected:
            raise ValueError('saved benchmark result differs from the requested experiment')
        if identity not in planned:
            raise ValueError('saved benchmark result is outside the frozen schedule')
        if type(row.get('order')) is not int or row['order'] != planned[identity]:
            raise ValueError('saved benchmark result order differs from the frozen schedule')
        if path.parent.name != f'{identity[0]}-{identity[1]}-{identity[2]}':
            raise ValueError('saved benchmark result does not match its folder')
    return rows, completed


def write_results(out, rows=None):
    if rows is None:
        rows = [json.loads(path.read_text()) for path in out.glob('*/result.json')]
    validate_result_identities(rows)
    rows.sort(key=lambda row: row['order'])
    write_json(out / 'results.json', rows)
    fields = sorted(set().union(*(row.keys() for row in rows)))
    with (out / 'results.csv').open('w') as handle:
        writer = csv.DictWriter(handle, fields, lineterminator='\n')
        writer.writeheader()
        writer.writerows(rows)
    summary = {condition: summarize_condition([row for row in rows if row['condition'] == condition])
               for condition in CONDITIONS}
    write_json(out / 'summary.json', summary)
    print(json.dumps(summary))


def arguments():
    parser = argparse.ArgumentParser()
    parser.add_argument('--conditions', default='none,static,arkism')
    parser.add_argument('--out', choices=tuple(OUTPUT_LOCATIONS), required=True)
    parser.add_argument('--auth', default=str(pathlib.Path.home() / '.codex/auth.json'))
    parser.add_argument('--exporter', choices=[EXPORTER])
    parser.add_argument('--approval', choices=['synthetic-role-fixture', 'explicit-human-approved'],
                        default='synthetic-role-fixture')
    parser.add_argument('--repetitions', type=int, choices=[3], default=3)
    return parser.parse_args()


def main():
    args = arguments()
    # Select predeclared destinations; no CLI bytes become a filesystem path.
    out = OUTPUT_LOCATIONS[args.out]
    if out.is_relative_to(PRIVATE_ROOT):
        private_directory(PRIVATE_ROOT)
        private_directory(out)
    else:
        out.mkdir(parents=True, exist_ok=True)
        if out.is_symlink() or not out.resolve().is_relative_to(ROOT):
            raise ValueError('results directory escapes benchmark root')
    schedule = frozen_schedule()
    rows, completed = resume_results(out, schedule)
    if not validate_resume_spec(out, schedule, bool(rows)):
        write_spec(out, schedule)
    selected = args.conditions.split(',')
    for ordinal, (task, condition, rep) in enumerate(schedule):
        identity = (task['id'], condition, rep)
        if condition in selected and identity not in completed:
            rows.append(run_one(task, condition, rep, ordinal, out, args.auth,
                                args.exporter is not None, args.approval))
    write_results(out, rows=rows)


if __name__ == '__main__':
    main()

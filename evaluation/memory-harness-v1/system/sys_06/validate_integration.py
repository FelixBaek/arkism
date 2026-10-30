"""SYS-06 integration report validator. Offline; it judges evidence, it runs nothing.

Statuses, strongest first: needs_fix (a protection boundary or claim is wrong),
hold (evidence missing), synthetic_pass, accepted_local_integration.
Production is never a reachable status here.
"""
import hashlib
import json
from pathlib import Path
import re
import sys

HERE = Path(__file__).resolve().parent
SYSTEM = HERE.parent
SHA = re.compile(r'^[0-9a-f]{64}$')
STORY = re.compile(r'^(ST|FR|TC)-[0-9]{3,4}$')
REPORT = {'protocol', 'claimed_state', 'story_ids', 'product_diff', 'code_sha256', 'config_sha256', 'fixture_sha256',
          'system_contract_sha256', 'independent_review', 'commands', 'cases'}
OBSERVED = {'returned', 'baseline_equal', 'receipts_delta', 'reinforcement_delta', 'fallback_reason', 'fallback_count',
            'fence_rechecked', 'permission_checked', 'deletion_checked', 'revision_checked', 'revived_ref',
            'rolled_back_to_snapshot', 'error_hidden'}
CLAIMS = ('synthetic_pass', 'local_integration', 'production')
EVIDENCE = ('synthetic', 'isolated_real')


def load(name):
    return json.loads((HERE / name).read_text(encoding='utf-8'))


def contract_index():
    """The SYS-01~05 index; a drifted file means earlier evidence no longer applies."""
    raw = (HERE / 'system_contract.json').read_bytes()
    drift = [c['path'] for c in json.loads(raw)['contracts']
             if hashlib.sha256((SYSTEM / c['path']).read_bytes()).hexdigest() != c['sha256']]
    return hashlib.sha256(raw).hexdigest(), drift


def _case_problems(cell, case):
    """Return (needs_fix reasons, hold reasons) for one matrix cell."""
    fix, hold = [], []
    obs, exp, where = case['observed'], cell['expect'], cell['mode'] + '/' + cell['condition']
    if type(obs) is not dict or set(obs) != OBSERVED:
        return [], [where + ':invalid_observation']
    for key in ('permission_checked', 'deletion_checked', 'revision_checked', 'fence_rechecked'):
        if obs[key] is not True:
            fix.append(where + ':' + key + '_missing')
    for key in ('revived_ref', 'rolled_back_to_snapshot', 'error_hidden'):
        if obs[key] is not False:
            fix.append(where + ':' + key)
    if obs['returned'] == 'none':
        fix.append(where + ':blocked_instead_of_served')  # blocking everything is not a safe pass
    elif obs['returned'] != exp['returned']:
        fix.append(where + ':wrong_path')
    for key in ('baseline_equal', 'receipts_delta', 'reinforcement_delta'):
        if exp[key] is not None and obs[key] != exp[key]:
            fix.append(where + ':' + key)
    if exp['fallback_reason'] is not None:
        if obs['fallback_reason'] != exp['fallback_reason'] or type(obs['fallback_count']) is not int or obs['fallback_count'] < 1:
            fix.append(where + ':fallback_unrecorded')
    elif obs['fallback_reason'] is not None:
        fix.append(where + ':unexpected_fallback')
    return fix, hold


def _command_problems(commands):
    if type(commands) is not list or not commands:
        return ['no_commands']
    out = []
    for c in commands:
        if type(c) is not dict or set(c) != {'cmd', 'exit_code', 'tests_run', 'skipped', 'failed'}:
            out.append('invalid_command')
        elif c['exit_code'] != 0 or c['failed'] != 0 or c['skipped'] != 0 or type(c['tests_run']) is not int or c['tests_run'] < 1:
            out.append('command_not_clean:' + str(c['cmd'])[:80])
    return out


def validate(report):
    if type(report) is not dict or set(report) != REPORT or report['protocol'] != 'arkism-sys06-integration-report-v1':
        return {'status': 'needs_fix', 'reasons': ['invalid_report']}
    if report['claimed_state'] not in CLAIMS:
        return {'status': 'needs_fix', 'reasons': ['invalid_claim']}
    fix, hold = [], []
    if report['claimed_state'] == 'production':
        fix.append('production_not_in_scope')
    index_sha, drift = contract_index()
    if report['system_contract_sha256'] != index_sha:
        hold.append('system_contract_mismatch')
    hold += ['contract_drift:' + p for p in drift]
    for key in ('code_sha256', 'config_sha256', 'fixture_sha256'):
        if type(report[key]) is not str or not SHA.fullmatch(report[key]):
            hold.append(key + '_missing')
    review = report['independent_review']
    if type(review) is not dict or review.get('verdict') != 'accept' or not SHA.fullmatch(str(review.get('report_sha256'))):
        hold.append('independent_review_missing')
    hold += _command_problems(report['commands'])
    matrix = load('flag_matrix.json')
    cases = report['cases'] if type(report['cases']) is list else []
    seen = {}
    for case in cases:
        if type(case) is not dict or set(case) != {'mode', 'condition', 'evidence', 'observed'} or case['evidence'] not in EVIDENCE:
            hold.append('invalid_case')
            continue
        key = (case['mode'], case['condition'])
        if key in seen:
            fix.append('duplicate_case:' + '/'.join(key))
        seen[key] = case
    for cell in matrix['cells']:
        case = seen.pop((cell['mode'], cell['condition']), None)
        if case is None:
            hold.append('missing_case:' + cell['mode'] + '/' + cell['condition'])
            continue
        f, h = _case_problems(cell, case)
        fix += f
        hold += h
    fix += ['unknown_case:' + '/'.join(k) for k in seen]
    synthetic = any(c.get('evidence') == 'synthetic' for c in cases if type(c) is dict)
    if report['claimed_state'] == 'local_integration':
        if synthetic:
            fix.append('synthetic_evidence_claimed_as_integration')
        if type(report['story_ids']) is not list or not report['story_ids'] or not all(type(s) is str and STORY.fullmatch(s) for s in report['story_ids']):
            hold.append('no_story_link')
        diff = report['product_diff']
        if type(diff) is not list or not diff or not all(type(d) is dict and set(d) == {'path', 'sha256'} and SHA.fullmatch(str(d['sha256'])) for d in diff):
            hold.append('no_product_diff')
    if fix:
        return {'status': 'needs_fix', 'reasons': sorted(set(fix))}
    if hold:
        return {'status': 'hold', 'reasons': sorted(set(hold))}
    return {'status': 'accepted_local_integration' if report['claimed_state'] == 'local_integration' else 'synthetic_pass', 'reasons': []}


if __name__ == '__main__':
    if len(sys.argv) != 2:
        print('usage: validate_integration.py REPORT.json', file=sys.stderr)
        raise SystemExit(2)
    result = validate(json.loads(Path(sys.argv[1]).read_text(encoding='utf-8')))
    print(json.dumps(result, ensure_ascii=False))
    raise SystemExit(0 if result['status'] in ('synthetic_pass', 'accepted_local_integration') else 1)

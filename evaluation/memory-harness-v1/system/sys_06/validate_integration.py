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
STORY = re.compile(r'^(ST|FR|TC)-\d{3,4}$', re.ASCII)
REPORT = {'protocol', 'claimed_state', 'story_ids', 'product_diff', 'code_sha256', 'config_sha256', 'fixture_sha256',
          'system_contract_sha256', 'independent_review', 'commands', 'cases'}
RECHECKS = ('scope_checked', 'approval_checked', 'source_checked', 'permission_checked', 'deletion_checked',
            'revision_checked', 'fence_rechecked')
OBSERVED = {'returned', 'baseline_equal', 'receipts_delta', 'reinforcement_delta', 'fallback_reason', 'fallback_count',
            'revived_ref', 'rolled_back_to_snapshot', 'error_hidden', *RECHECKS}
COMMAND = {'cmd', 'exit_code', 'tests_run', 'skipped', 'failed'}
CLAIMS = ('synthetic_pass', 'local_integration', 'production')
EVIDENCE = ('synthetic', 'isolated_real')


def load(name):
    return json.loads((HERE / name).read_text(encoding='utf-8'))


def _int(value):
    return type(value) is int  # bool and float are not counts


def contract_index():
    """The SYS-01~05 index: (hash, drifted paths, paths not yet accepted)."""
    raw = (HERE / 'system_contract.json').read_bytes()
    contracts = json.loads(raw)['contracts']
    drift = [c['path'] for c in contracts if hashlib.sha256((SYSTEM / c['path']).read_bytes()).hexdigest() != c['sha256']]
    return hashlib.sha256(raw).hexdigest(), drift, [c['path'] for c in contracts if c['review_state'] != 'accepted']


def _case_problems(cell, case):
    """Return (needs_fix reasons, hold reasons) for one matrix cell."""
    fix, hold = [], []
    obs, exp, where = case['observed'], cell['expect'], cell['mode'] + '/' + cell['condition']
    if type(obs) is not dict or set(obs) != OBSERVED:
        return [], [where + ':invalid_observation']
    for key in RECHECKS:
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
        if exp[key] is not None and (type(obs[key]) is not type(exp[key]) or obs[key] != exp[key]):
            fix.append(where + ':' + key)
    return fix + _fallback_problems(where, obs, exp), hold


def _fallback_problems(where, obs, exp):
    """An expected fallback needs its reason and a count; any other cell must report none."""
    if exp['fallback_reason'] is not None:
        if obs['fallback_reason'] != exp['fallback_reason'] or not _int(obs['fallback_count']) or obs['fallback_count'] < 1:
            return [where + ':fallback_unrecorded']
    elif obs['fallback_reason'] is not None or not _int(obs['fallback_count']) or obs['fallback_count'] != 0:
        return [where + ':unexpected_fallback']
    return []


def _command_problems(commands, required):
    if type(commands) is not list or not commands:
        return ['no_commands']
    out, clean = [], set()
    for c in commands:
        if type(c) is not dict or set(c) != COMMAND or type(c['cmd']) is not str or not all(_int(c[k]) for k in COMMAND - {'cmd'}):
            out.append('invalid_command')
        elif c['exit_code'] != 0 or c['failed'] != 0 or c['skipped'] != 0 or c['tests_run'] < 1:
            out.append('command_not_clean:' + c['cmd'][:80])
        else:
            clean.add(c['cmd'])
    return out + ['required_check_missing:' + r[:80] for r in required if r not in clean]


def _cases(cases, cells):
    """Match report cases to matrix cells exactly once; return (fix, hold, synthetic)."""
    fix, hold, seen = [], [], {}
    for case in cases if type(cases) is list else []:
        if type(case) is not dict or set(case) != {'mode', 'condition', 'evidence', 'observed'} or case['evidence'] not in EVIDENCE \
                or type(case['mode']) is not str or type(case['condition']) is not str:
            hold.append('invalid_case')
            continue
        key = (case['mode'], case['condition'])
        if key in seen:
            fix.append('duplicate_case:' + '/'.join(key))
        seen[key] = case
    synthetic = any(c['evidence'] == 'synthetic' for c in seen.values())
    for cell in cells:
        case = seen.pop((cell['mode'], cell['condition']), None)
        if case is None:
            hold.append('missing_case:' + cell['mode'] + '/' + cell['condition'])
            continue
        f, h = _case_problems(cell, case)
        fix += f
        hold += h
    return fix + ['unknown_case:' + '/'.join(k) for k in seen], hold, synthetic


def _integration_links(report, stage):
    """Hold reasons for a local_integration claim: prerequisites, Story and allowlisted diff."""
    hold = ['prerequisite_unaccepted:' + p for p in contract_index()[2]]
    if type(report['story_ids']) is not list or not report['story_ids'] or not all(type(s) is str and STORY.fullmatch(s) for s in report['story_ids']):
        hold.append('no_story_link')
    diff = report['product_diff']
    if type(diff) is not list or not diff or not all(type(d) is dict and set(d) == {'path', 'sha256'} and SHA.fullmatch(str(d['sha256'])) for d in diff):
        hold.append('no_product_diff')
    elif any(d['path'] not in stage['candidate_files'] for d in diff):
        hold.append('product_diff_outside_allowlist')
    return hold


def _evidence_holds(report):
    """Hold reasons for the contract index, code/config/fixture hashes and the independent review."""
    index_sha, drift, _ = contract_index()
    hold = ['contract_drift:' + p for p in drift]
    if report['system_contract_sha256'] != index_sha:
        hold.append('system_contract_mismatch')
    hold += [key + '_missing' for key in ('code_sha256', 'config_sha256', 'fixture_sha256')
             if type(report[key]) is not str or not SHA.fullmatch(report[key])]
    review = report['independent_review']
    if type(review) is not dict or review.get('verdict') != 'accept' or not SHA.fullmatch(str(review.get('report_sha256'))):
        hold.append('independent_review_missing')
    return hold


def validate(report):
    if type(report) is not dict or set(report) != REPORT or report['protocol'] != 'arkism-sys06-integration-report-v1':
        return {'status': 'needs_fix', 'reasons': ['invalid_report']}
    if report['claimed_state'] not in CLAIMS:
        return {'status': 'needs_fix', 'reasons': ['invalid_claim']}
    fix, hold = [], []
    if report['claimed_state'] == 'production':
        fix.append('production_not_in_scope')
    hold += _evidence_holds(report)
    integration = report['claimed_state'] == 'local_integration'
    stage = load('integration_plan.json')['stages']['B']
    hold += _command_problems(report['commands'], stage['required_checks'] if integration else [])
    f, h, synthetic = _cases(report['cases'], load('flag_matrix.json')['cells'])
    fix += f
    hold += h
    if integration:
        if synthetic:
            fix.append('synthetic_evidence_claimed_as_integration')
        hold += _integration_links(report, stage)
    if fix:
        return {'status': 'needs_fix', 'reasons': sorted(set(fix))}
    if hold:
        return {'status': 'hold', 'reasons': sorted(set(hold))}
    return {'status': 'accepted_local_integration' if report['claimed_state'] == 'local_integration' else 'synthetic_pass', 'reasons': []}


if __name__ == '__main__':
    if len(sys.argv) != 1:
        print('usage: validate_integration.py < REPORT.json', file=sys.stderr)
        raise SystemExit(2)
    result = validate(json.load(sys.stdin))
    print(json.dumps(result, ensure_ascii=False))
    raise SystemExit(0 if result['status'] in ('synthetic_pass', 'accepted_local_integration') else 1)

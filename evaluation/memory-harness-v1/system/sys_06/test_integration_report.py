import copy
import importlib.util
from pathlib import Path
import unittest

_spec = importlib.util.spec_from_file_location('sys06_validate', Path(__file__).with_name('validate_integration.py'))
V = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(V)

H = 'a' * 64


def observed(expect):
    return {'returned': expect['returned'], 'baseline_equal': expect['baseline_equal'], 'receipts_delta': expect['receipts_delta'],
            'reinforcement_delta': expect['reinforcement_delta'], 'fallback_reason': expect['fallback_reason'],
            'fallback_count': 1 if expect['fallback_reason'] else 0, **{k: True for k in V.RECHECKS}, 'revived_ref': False, 'rolled_back_to_snapshot': False, 'error_hidden': False}


def good(evidence='isolated_real', claim='local_integration'):
    cells = V.load('flag_matrix.json')['cells']
    return {'protocol': 'arkism-sys06-integration-report-v1', 'claimed_state': claim, 'story_ids': ['ST-0001'],
            'product_diff': [{'path': 'internal/memory/service.go', 'sha256': H}], 'code_sha256': H, 'config_sha256': H,
            'fixture_sha256': H, 'system_contract_sha256': V.contract_index()[0],
            'independent_review': {'verdict': 'accept', 'report_sha256': H},
            'commands': [{'cmd': c, 'exit_code': 0, 'tests_run': 32, 'skipped': 0, 'failed': 0} for c in REQUIRED],
            'cases': [{'mode': c['mode'], 'condition': c['condition'], 'evidence': evidence, 'observed': observed(c['expect'])} for c in cells]}


REQUIRED = V.load('integration_plan.json')['stages']['B']['required_checks']


def case(report, mode, condition):
    return next(c for c in report['cases'] if c['mode'] == mode and c['condition'] == condition)['observed']


class IntegrationReport(unittest.TestCase):
    def setUp(self):
        # Cases run against an index with every prerequisite accepted; one test checks the unaccepted hold.
        real = V.contract_index
        self.addCleanup(setattr, V, 'contract_index', real)
        V.contract_index = lambda: real()[:2] + ([],)

    def assertStatus(self, report, status, reason=None):
        result = V.validate(report)
        self.assertEqual(result['status'], status, result)
        if reason:
            self.assertTrue(any(r.startswith(reason) for r in result['reasons']), result)

    def test_contract_index_matches_sys01_to_sys05_files(self):
        self.assertEqual(V.contract_index()[1], [])
        self.doCleanups()
        real = V.contract_index
        self.assertEqual(real()[2], [])  # SYS-05 accepted on 2026-10-31
        V.contract_index = lambda: real()[:2] + (['sys_05/load_contract.json'],)
        self.addCleanup(setattr, V, 'contract_index', real)
        self.assertStatus(good(), 'hold', 'prerequisite_unaccepted:sys_05/load_contract.json')
        self.assertStatus(good('synthetic', 'synthetic_pass'), 'synthetic_pass')
        cards = {c['card'] for c in V.load('system_contract.json')['contracts']}
        self.assertEqual(cards, {'SYS-01', 'SYS-02', 'SYS-03', 'SYS-04', 'SYS-05'})

    def test_matrix_covers_every_mode_and_condition_and_defaults_off(self):
        m = V.load('flag_matrix.json')
        self.assertEqual(m['default_mode'], 'off')
        self.assertEqual(m['unknown_mode'], 'startup_error')
        self.assertEqual(len(m['cells']), len(m['modes']) * len(m['conditions']))
        self.assertTrue(all(c['expect']['returned'] == 'baseline' for c in m['cells'] if c['mode'] in ('off', 'shadow', 'emergency_fallback')))

    def test_complete_real_evidence_is_accepted_as_local_integration_only(self):
        self.assertStatus(good(), 'accepted_local_integration')
        self.assertStatus(good(claim='production'), 'needs_fix', 'production_not_in_scope')

    def test_synthetic_evidence_never_becomes_integration(self):
        self.assertStatus(good('synthetic', 'synthetic_pass'), 'synthetic_pass')
        self.assertStatus(good('synthetic'), 'needs_fix', 'synthetic_evidence_claimed_as_integration')

    def test_off_and_shadow_must_equal_baseline_without_side_effects(self):
        r = good(); case(r, 'off', 'normal')['baseline_equal'] = False
        self.assertStatus(r, 'needs_fix', 'off/normal:baseline_equal')
        r = good(); case(r, 'shadow', 'normal')['receipts_delta'] = 1
        self.assertStatus(r, 'needs_fix', 'shadow/normal:receipts_delta')
        r = good(); case(r, 'shadow', 'timeout')['reinforcement_delta'] = 2
        self.assertStatus(r, 'needs_fix', 'shadow/timeout:reinforcement_delta')

    def test_fallback_must_record_reason_and_count(self):
        r = good(); case(r, 'dynamic', 'timeout')['fallback_reason'] = None
        self.assertStatus(r, 'needs_fix', 'dynamic/timeout:fallback_unrecorded')
        r = good(); case(r, 'dynamic', 'version_error')['fallback_count'] = 0
        self.assertStatus(r, 'needs_fix', 'dynamic/version_error:fallback_unrecorded')
        r = good(); case(r, 'dynamic', 'nan_score')['error_hidden'] = True
        self.assertStatus(r, 'needs_fix', 'dynamic/nan_score:error_hidden')

    def test_fallback_cannot_skip_deletion_permission_or_revision(self):
        for key in ('deletion_checked', 'permission_checked', 'revision_checked', 'fence_rechecked'):
            r = good(); case(r, 'emergency_fallback', 'delete_race')[key] = False
            self.assertStatus(r, 'needs_fix', 'emergency_fallback/delete_race:' + key)
        r = good(); case(r, 'dynamic', 'stale_state')['rolled_back_to_snapshot'] = True
        self.assertStatus(r, 'needs_fix', 'dynamic/stale_state:rolled_back_to_snapshot')
        r = good(); case(r, 'dynamic', 'correction_race')['revived_ref'] = True
        self.assertStatus(r, 'needs_fix', 'dynamic/correction_race:revived_ref')

    def test_blocking_normal_work_is_not_a_safe_pass(self):
        r = good(); case(r, 'dynamic', 'normal')['returned'] = 'none'
        self.assertStatus(r, 'needs_fix', 'dynamic/normal:blocked_instead_of_served')

    def test_missing_review_hash_commands_or_skips_hold(self):
        r = good(); r['independent_review'] = {'verdict': 'reject', 'report_sha256': H}
        self.assertStatus(r, 'hold', 'independent_review_missing')
        r = good(); r['code_sha256'] = None
        self.assertStatus(r, 'hold', 'code_sha256_missing')
        r = good(); r['commands'][0]['skipped'] = 3
        self.assertStatus(r, 'hold', 'command_not_clean')
        r = good(); r['commands'][0]['tests_run'] = 0
        self.assertStatus(r, 'hold', 'command_not_clean')
        r = good(); r['commands'] = []
        self.assertStatus(r, 'hold', 'no_commands')

    def test_story_and_diff_links_are_required_for_integration(self):
        r = good(); r['story_ids'] = []
        self.assertStatus(r, 'hold', 'no_story_link')
        r = good(); r['story_ids'] = ['ST-NEW']
        self.assertStatus(r, 'hold', 'no_story_link')
        r = good(); r['product_diff'] = []
        self.assertStatus(r, 'hold', 'no_product_diff')

    def test_missing_duplicate_or_unknown_cells(self):
        r = good(); r['cases'].pop()
        self.assertStatus(r, 'hold', 'missing_case')
        r = good(); r['cases'].append(copy.deepcopy(r['cases'][0]))
        self.assertStatus(r, 'needs_fix', 'duplicate_case')
        r = good(); r['cases'].append(dict(copy.deepcopy(r['cases'][0]), mode='turbo'))
        self.assertStatus(r, 'needs_fix', 'unknown_case')

    def test_drifted_contract_file_holds_even_with_matching_index_hash(self):
        real = V.contract_index
        try:
            V.contract_index = lambda: (real()[0], ['sys_05/load_contract.json'], [])
            self.assertStatus(good(), 'hold', 'contract_drift:sys_05/load_contract.json')
        finally:
            V.contract_index = real

    def test_contract_hash_mismatch_and_bad_shape(self):
        r = good(); r['system_contract_sha256'] = H
        self.assertStatus(r, 'hold', 'system_contract_mismatch')
        r = good(); r['extra'] = 1
        self.assertStatus(r, 'needs_fix', 'invalid_report')
        r = good(); case(r, 'off', 'normal')['note'] = 'x'
        self.assertStatus(r, 'hold', 'off/normal:invalid_observation')

    def test_non_string_case_keys_hold_without_crashing(self):
        for bad in (['off'], 7, None):
            r = good(); r['cases'][0]['mode'] = bad
            self.assertStatus(r, 'hold', 'invalid_case')
        r = good(); r['cases'].append(dict(copy.deepcopy(r['cases'][0]), condition=3))
        self.assertStatus(r, 'hold', 'invalid_case')

    def test_required_checks_must_run_clean(self):
        r = good(); r['commands'] = [{'cmd': 'echo ok', 'exit_code': 0, 'tests_run': 1, 'skipped': 0, 'failed': 0}]
        self.assertStatus(r, 'hold', 'required_check_missing')
        for key, bad in (('exit_code', False), ('skipped', 0.0), ('tests_run', True)):
            r = good(); r['commands'][0][key] = bad
            self.assertStatus(r, 'hold', 'invalid_command')
        self.assertStatus(dict(good('synthetic', 'synthetic_pass'), commands=[{'cmd': 'echo ok', 'exit_code': 0, 'tests_run': 1, 'skipped': 0, 'failed': 0}]), 'synthetic_pass')

    def test_product_diff_must_stay_in_the_allowlist(self):
        for path in ('', '../../etc/passwd', 'README.md'):
            r = good(); r['product_diff'][0]['path'] = path
            self.assertStatus(r, 'hold', 'product_diff_outside_allowlist')

    def test_observations_are_strictly_typed(self):
        r = good(); case(r, 'off', 'normal')['baseline_equal'] = 1
        self.assertStatus(r, 'needs_fix', 'off/normal:baseline_equal')
        r = good(); case(r, 'shadow', 'normal')['receipts_delta'] = False
        self.assertStatus(r, 'needs_fix', 'shadow/normal:receipts_delta')

    def test_hidden_fallback_count_is_caught(self):
        for mode, condition, bad in (('off', 'normal', 99), ('shadow', 'timeout', 'lots')):
            r = good(); case(r, mode, condition)['fallback_count'] = bad
            self.assertStatus(r, 'needs_fix', mode + '/' + condition + ':unexpected_fallback')

    def test_duplicate_product_diff_path_holds(self):
        r = good(); r['product_diff'].append(dict(r['product_diff'][0]))
        self.assertStatus(r, 'hold', 'duplicate_product_diff')

    def test_scope_approval_source_and_correction_are_rechecked(self):
        for key in ('scope_checked', 'approval_checked', 'source_checked', 'correction_checked'):
            r = good(); case(r, 'dynamic', 'stale_state')[key] = False
            self.assertStatus(r, 'needs_fix', 'dynamic/stale_state:' + key)


if __name__ == '__main__':
    unittest.main()

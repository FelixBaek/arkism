"""Offline SYS03 A tests. Literal statuses/costs are hand-authored fixture oracles.

Constructed positive claim ledgers exercise reconciliation only; they are not
independent native runtime evidence. Every mutation must be rejected separately.
"""
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location('_sys03_model', HERE / 'validate_ledger.py')
MODEL = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODEL)
FIXTURES_SHA256 = '03864092ed7ff14d70308354acd284c7a35abde92fe55656f606c5ac731997b5'
_FIXTURE_RAW = (HERE / 'fault_schedules.json').read_bytes()
if hashlib.sha256(_FIXTURE_RAW).hexdigest() != FIXTURES_SHA256:
    raise RuntimeError('fixed SYS03 fixture mismatch')
FIXTURES = json.loads(_FIXTURE_RAW)
CASES = {row['id']: row for row in FIXTURES['cases']}


class AtomicityTests(unittest.TestCase):
    def case(self, name='normal'):
        return copy.deepcopy(CASES[name]['external'])

    def error(self, code, function):
        with self.assertRaises(MODEL.LedgerError) as caught:
            function()
        self.assertEqual(caught.exception.code, code)

    def mutated(self, change, code='claim_mismatch'):
        source = self.case()
        claims = MODEL.replay(source)
        change(claims)
        self.error(code, lambda: MODEL.validate_ledger(source, claims))

    def test_duplicate_event_retains_original_commit_and_consumption(self):
        result = MODEL.replay(self.case('postcommit_reply_loss'))
        first, retry = result['claims'][2], result['claims'][4]
        for key in ('commit_id', 'first_consumption_receipt', 'state_sha256',
                    'state_revision', 'checkpoint_seq'):
            with self.subTest(key=key):
                self.assertEqual(first[key], retry[key])
        self.assertEqual(retry['semantic_updates'], 0)

    def test_distinct_id_same_evidence_first_receipt_only(self):
        rows = MODEL.replay(self.case('same_evidence_new_id'))['claims']
        self.assertEqual(rows[2]['first_consumption_receipt'], rows[5]['first_consumption_receipt'])
        self.assertNotEqual(rows[2]['commit_id'], rows[5]['commit_id'])
        self.assertEqual(rows[5]['semantic_updates'], 0)
        self.assertEqual(rows[5]['state_revision'], 2)

    def test_missing_event_rejected_despite_preserved_final_state_hash(self):
        self.mutated(lambda d: d['claims'].pop(0), 'missing_or_duplicate_claim')

    def test_duplicate_claim_rejected(self):
        self.mutated(lambda d: d['claims'].append(copy.deepcopy(d['claims'][-1])),
                     'missing_or_duplicate_claim')

    def test_partial_atomic_state_only_rejected(self):
        self.mutated(lambda d: d['claims'][2].update(first_consumption_receipt=None))

    def test_partial_atomic_consumption_only_rejected(self):
        self.mutated(lambda d: d['claims'][2].update(state_revision=0, state_sha256=None))

    def test_missing_durable_link_rejected(self):
        self.mutated(lambda d: d['claims'][2].update(durable_receipt=None))

    def test_missing_commit_link_rejected(self):
        self.mutated(lambda d: d['claims'][3].update(commit_id=None))

    def test_scope_full_ref_version_cannot_be_relaxed(self):
        for field in ('scope', 'target', 'dimension', 'contract_sha256', 'config_sha256'):
            with self.subTest(field=field):
                self.mutated(lambda d: d['claims'][2]['key'].__setitem__(field, None))

    def test_logical_claim_and_sequence_request_checkpoint_bindings(self):
        for field in ('logical_evidence_identity_sha256', 'claim_sha256', 'accepted_seq',
                      'request_id', 'checkpoint_seq', 'payload_sha256', 'event_id'):
            with self.subTest(field=field):
                self.mutated(lambda d: d['claims'][2].__setitem__(field, None))

    def test_cost_dimensions_separately_checked(self):
        for field in MODEL.COST_KEYS:
            with self.subTest(field=field):
                self.mutated(lambda d: d['costs'].__setitem__(field, d['costs'][field] + 1),
                             'cost_mismatch')

    def test_false_equals_zero_rejected_in_claim(self):
        self.mutated(lambda d: d['claims'][0].update(semantic_updates=False))

    def test_native_plan_never_constructs_learning_or_durable_receipt(self):
        result = MODEL.replay(self.case('native_intent_not_event'))
        self.assertTrue(all(r['key'] is None and r['durable_receipt'] is None and
                            r['logical_evidence_identity_sha256'] is None and
                            r['commit_id'] is None for r in result['claims']))
        self.assertEqual(sum(result['costs'].values()), 0)

    def test_prepared_native_success_cannot_be_claimed(self):
        source = self.case('native_intent_not_event')
        ledger = MODEL.replay(source)
        ledger['claims'][-1]['status'] = 'committed_learning'
        self.error('claim_mismatch', lambda: MODEL.validate_ledger(source, ledger))

    def test_revision_invalidation_survives_retry_and_stale_publish(self):
        statuses = [r['status'] for r in MODEL.replay(self.case('invalidated_postcommit'))['claims']]
        self.assertEqual(statuses[-2:], ['source_invalidated', 'source_invalidated'])

    def test_source_schedule_unknown_missing_and_extra_fields(self):
        for mode in ('missing', 'extra'):
            source = self.case()
            if mode == 'missing':
                del source['schedule'][0]['reader_id']
            else:
                source['schedule'][0]['verified'] = True
            with self.subTest(mode=mode):
                self.error('missing_field' if mode == 'missing' else 'unknown_field',
                           lambda: MODEL.replay(source))

    def test_negative_bool_float_and_overflow_counters_rejected(self):
        for value in (-1, True, 1.0, 1 << 63):
            with self.subTest(value=value):
                self.mutated(lambda d: d['costs'].update(db_flushes=value), 'invalid_count')

    def test_scope_cannot_be_upgraded_to_isolated_service(self):
        source = self.case()
        source['verification_scope'] = 'isolated_service'
        self.error('unsupported_scope', lambda: MODEL.replay(source))

    def test_empty_schedules_are_not_acceptance(self):
        source = self.case()
        source['schedule'] = []
        self.error('invalid_schedule', lambda: MODEL.replay(source))

    def test_inputs_unchanged_and_no_external_registries_mutated(self):
        source = self.case()
        before = copy.deepcopy(source)
        dep = MODEL.load_dependencies()
        registries = copy.deepcopy((dep.MODELS, dep.DYN_MODELS, dep.SYS._MODELS))
        MODEL.replay(source)
        self.assertEqual(before, source)
        self.assertEqual(registries, (dep.MODELS, dep.DYN_MODELS, dep.SYS._MODELS))

    def test_duplicate_json_keys_and_nonfinite_rejected(self):
        for wire in (b'{"a":1,"a":2}', b'NaN', b'Infinity'):
            with self.subTest(wire=wire):
                self.error('duplicate_key' if b'"a"' in wire else 'invalid_json',
                           lambda: MODEL.decode(wire))

    def test_invalid_unicode_and_document_limits(self):
        self.error('invalid_unicode', lambda: MODEL.decode(b'"\\ud800"'))
        self.error('resource_limit', lambda: MODEL.decode(b' ' * 4194305))
        self.error('resource_limit', lambda: MODEL.decode(b'[' * 34 + b'0' + b']' * 34))

    def test_return_only_does_not_create_reader_or_use_fact(self):
        row = MODEL.replay(self.case('return_not_use'))
        self.assertEqual(row['claims'][2]['status'], 'use_unobserved')
        self.assertEqual(row['claims'][3]['status'], 'outcome_separate_not_use_proof')

    def test_other_reader_cannot_inherit_primary_packing(self):
        source = self.case('normal')
        template = copy.deepcopy(source['schedule'][0])
        source['schedule'] = []
        for op, reader in [('return', 'one'), ('pack', 'one'), ('reader', 'two')]:
            source['schedule'].append({**template, 'op': op, 'reader_id': reader})
        self.assertEqual(MODEL.replay(source)['claims'][-1]['status'], 'reader_without_packing')

    def test_sequence_regression_not_hidden_by_same_time(self):
        source = self.case('same_evidence_new_id')
        source['observations'][1]['dyn']['accepted_seq'] = 2
        self.assertEqual(MODEL.replay(source)['claims'][3]['status'], 'sequence_regression')

    def test_model_hash_not_fixed_dyn_numeric_state(self):
        contract = json.loads((HERE / 'atomicity_contract.json').read_bytes())
        self.assertIn('not numeric DYN state', contract['semantics']['state'])
        self.assertFalse(contract['is_product_contract'])
        self.assertIn('unselected', contract['transaction_model']['storage_ADR'])


    def test_volatile_crash_allows_reaccept_without_durable_sequence_leak(self):
        source = self.case('volatile_crash')
        first = copy.deepcopy(source['schedule'][0])
        source['schedule'] = source['schedule'][:2] + [first,
            {**first, 'op': 'durable', 'writer_epoch': 1},
            {**first, 'op': 'commit', 'writer_epoch': 1}]
        self.assertEqual(MODEL.replay(source)['claims'][-1]['status'], 'committed_learning')

    def test_unknown_pending_not_mislabeled_native_or_committed(self):
        for value in ('unknown', 'pending'):
            with self.subTest(value=value):
                source = self.case()
                source['observations'][0]['task_success'] = value
                result = MODEL.replay(source)
                self.assertEqual([r['status'] for r in result['claims']], ['unobserved_no_event'] * 4)
                self.assertEqual(sum(result['costs'].values()), 0)

    def test_first_arrival_future_rejected_but_later_retry_preserves_receipt(self):
        source = self.case()
        source['observations'][0]['dyn']['arrived_at_ns'] = 101
        self.assertEqual(MODEL.replay(source)['claims'][0]['status'], 'future_arrival')
        source = self.case('exact_retry')
        retry = copy.deepcopy(source['observations'][0])
        retry['dyn']['arrived_at_ns'] = 101
        source['observations'].append(retry)
        source['schedule'][-1]['event_index'] = len(source['observations']) - 1
        self.assertEqual(MODEL.replay(source)['claims'][-1]['status'], 'duplicate_event_original_receipt')

    def test_state_key_version_isolates_consumption_storage_without_changing_dyn_hash(self):
        source = self.case('same_evidence_new_id')
        source['observations'][1]['dyn']['key']['config_sha256'] = 'e' * 64
        source['schedule'][-1].update(expected_revision=0, checkpoint_seq=0)
        result = MODEL.replay(source)
        self.assertEqual(result['claims'][-1]['status'], 'committed_learning')
        self.assertEqual(result['claims'][2]['logical_evidence_identity_sha256'],
                         result['claims'][-1]['logical_evidence_identity_sha256'])
        self.assertNotEqual(result['claims'][2]['key'], result['claims'][-1]['key'])


    def presentation(self, second_cycle='second', second_event=False):
        source = self.case('normal')
        template = source['schedule'][0]
        first = [('return', True), ('pack', True), ('reader', True)]
        second = [('return', True), ('pack', False), ('reader', True), ('use', True)]
        source['schedule'] = [{**template, 'op': op, 'included': included,
                              'presentation_id': 'first'} for op, included in first]
        if second_event:
            event = copy.deepcopy(source['observations'][0])
            event['dyn']['event_id'] = 'synthetic_next_event'
            event['dyn']['accepted_seq'] += 1
            source['observations'].append(event)
        source['schedule'] += [{**template, 'op': op, 'included': included,
                                'presentation_id': second_cycle,
                                'event_index': int(second_event)} for op, included in second]
        return source

    def test_new_cycle_never_inherits_historical_reader(self):
        result = MODEL.replay(self.presentation())
        self.assertEqual([r['status'] for r in result['claims']],
                         ['return_included', 'pack_included', 'reader_included',
                          'return_included', 'pack_excluded', 'reader_without_packing', 'use_unobserved'])
        self.assertEqual(result['claims'][2]['presentation_id'], 'first')
        self.assertEqual(result['claims'][-1]['presentation_id'], 'second')
        self.assertEqual(sum(result['costs'].values()), 0)

    def test_new_event_cannot_inherit_same_named_cycle(self):
        result = MODEL.replay(self.presentation('first', second_event=True))
        self.assertEqual([r['status'] for r in result['claims']][-4:],
                         ['return_included', 'pack_excluded', 'reader_without_packing', 'use_unobserved'])

    def test_cycle_stage_conflicts_do_not_erase_original_observation(self):
        source = self.presentation('first')
        result = MODEL.replay(source)
        self.assertEqual([r['status'] for r in result['claims']][-4:],
                         ['duplicate_stage_observation', 'presentation_conflict',
                          'duplicate_stage_observation', 'use_included'])
        # The conflicting excluded claim is rejected, original read is historical fact.
        self.assertEqual(result['claims'][4]['status'], 'presentation_conflict')

    def test_new_cycle_can_have_its_own_valid_reader(self):
        source = self.presentation()
        source['schedule'][4]['included'] = True
        result = MODEL.replay(source)
        self.assertEqual([r['status'] for r in result['claims']][-3:],
                         ['pack_included', 'reader_included', 'use_included'])

    def test_cycle_id_bound_in_reconciliation(self):
        source = self.presentation()
        claims = MODEL.replay(source)
        claims['claims'][-1]['presentation_id'] = 'first'
        self.error('claim_mismatch', lambda: MODEL.validate_ledger(source, claims))

    def test_missing_or_invalid_cycle_identity_rejected(self):
        for value in (None, '', 1, True):
            source = self.case()
            source['schedule'][0]['presentation_id'] = value
            with self.subTest(value=value):
                self.error('invalid_identifier', lambda: MODEL.replay(source))
        source = self.case()
        del source['schedule'][0]['presentation_id']
        self.error('missing_field', lambda: MODEL.replay(source))

    def test_exponent_overflow_rejected_at_decode_boundary(self):
        for wire in (b'1e309', b'-1e309', b'{"strength":1e309}'):
            with self.subTest(wire=wire):
                self.error('invalid_json', lambda: MODEL.decode(wire))

    def test_direct_nonfinite_observation_uses_typed_error_boundary(self):
        for value in (float('inf'), float('-inf'), float('nan')):
            source = self.case()
            source['observations'][0]['dyn']['strength'] = value
            with self.subTest(value=str(value)):
                self.error('invalid_json', lambda: MODEL.replay(source))


    def test_outcome_repeat_is_immutable_but_never_use_proof(self):
        source = self.presentation()
        template = source['schedule'][0]
        source['schedule'] = [{**template, 'op': 'outcome', 'included': included}
                              for included in (True, True, False)]
        self.assertEqual([r['status'] for r in MODEL.replay(source)['claims']],
                         ['outcome_separate_not_use_proof', 'duplicate_stage_observation',
                          'presentation_conflict'])

    def test_same_cycle_changed_payload_cannot_reuse_reader(self):
        source = self.presentation('first', second_event=True)
        event = source['observations'][1]['dyn']
        event['event_id'] = source['observations'][0]['dyn']['event_id']
        event['accepted_seq'] = source['observations'][0]['dyn']['accepted_seq']
        event['strength'] = 0.25
        self.assertEqual([r['status'] for r in MODEL.replay(source)['claims']][-4:],
                         ['event_conflict'] * 4)

    def test_same_cycle_original_acceptance_coordinates_are_bound(self):
        for field in ('accepted_seq', 'accepted_at_ns'):
            source = self.presentation('first', second_event=True)
            event = source['observations'][1]['dyn']
            event['event_id'] = source['observations'][0]['dyn']['event_id']
            event['accepted_seq'] = source['observations'][0]['dyn']['accepted_seq']
            event[field] += 1
            with self.subTest(field=field):
                self.assertEqual([r['status'] for r in MODEL.replay(source)['claims']][-4:],
                                 ['event_conflict'] * 4)

    def test_accepted_event_conflict_precedes_new_presentation(self):
        source = self.case('normal')
        source['schedule'] = source['schedule'][:1]
        event = copy.deepcopy(source['observations'][0])
        event['dyn']['strength'] = 0.25
        source['observations'].append(event)
        source['schedule'].append({**source['schedule'][0], 'op': 'return',
                                   'event_index': len(source['observations']) - 1, 'presentation_id': 'new'})
        self.assertEqual(MODEL.replay(source)['claims'][-1]['status'], 'event_conflict')


    def test_rejected_payload_does_not_poison_correct_cycle(self):
        source = self.case('normal')
        first = copy.deepcopy(source['schedule'][0])
        source['schedule'] = [first]
        event = copy.deepcopy(source['observations'][0])
        event['dyn']['strength'] = 0.25
        source['observations'].append(event)
        source['schedule'].append({**first, 'op': 'return',
                                   'event_index': len(source['observations']) - 1})
        source['schedule'] += [{**first, 'op': op} for op in ('return', 'pack', 'reader', 'use')]
        result = MODEL.replay(source)
        self.assertEqual([r['status'] for r in result['claims']],
                         ['accepted_volatile', 'event_conflict', 'return_included',
                          'pack_included', 'reader_included', 'use_included'])


    def test_rejected_prerequisite_stage_does_not_reserve_cycle(self):
        for first_op, expected in (('pack', 'packing_without_return'),
                                   ('reader', 'reader_without_packing'),
                                   ('use', 'use_unobserved')):
            source = self.case('normal')
            first = copy.deepcopy(source['schedule'][0])
            wrong = copy.deepcopy(source['observations'][0])
            wrong['dyn']['strength'] = 0.25
            source['observations'].append(wrong)
            source['schedule'] = [{**first, 'op': first_op,
                                   'event_index': len(source['observations']) - 1}]
            source['schedule'] += [{**first, 'op': op} for op in ('return', 'pack', 'reader', 'use')]
            with self.subTest(first_op=first_op):
                result = MODEL.replay(source)
                self.assertEqual([r['status'] for r in result['claims']],
                                 [expected, 'return_included', 'pack_included',
                                  'reader_included', 'use_included'])


def _literal_case(name):
    def test(self):
        case = CASES[name]
        got = MODEL.replay(copy.deepcopy(case['external']))
        self.assertEqual([r['status'] for r in got['claims']], case['expected_statuses'])
        self.assertEqual(got['costs'], case['expected_costs'])
        result = MODEL.validate_ledger(case['external'], got)
        self.assertEqual(result['status'], 'matched_declared_model')
        self.assertFalse(result['database_verified'])
    return test


for _name in CASES:
    setattr(AtomicityTests, 'test_literal_' + _name, _literal_case(_name))


class RequiredSuite(unittest.TestSuite):
    def run(self, result, debug=False):
        before = result.testsRun
        super().run(result, debug)
        if result.testsRun == before or result.skipped or result.expectedFailures:
            result.addFailure(unittest.FunctionTestCase(lambda: None),
                              (AssertionError, AssertionError('required suite empty/skip'), None))
        return result


def load_tests(loader, standard_tests, pattern):
    return RequiredSuite(loader.loadTestsFromTestCase(AtomicityTests))

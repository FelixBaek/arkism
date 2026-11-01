"""Independent hand examples and actual file/CLI boundaries for EXP-03."""
import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location('arkism_exp03_analysis_impl', HERE / 'analyze_paired.py')
A = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(A)
HAND = json.loads((HERE / 'hand_examples.json').read_text())


def ref(name):
    return {'origin': 'memory', 'collection': 'memories', 'id': name,
            'revision': 1, 'content_sha256': hashlib.sha256(name.encode()).hexdigest()}


def reseal(plan, schedule, observations, ledger):
    plan['plan_sha256'] = A.digest({k: v for k, v in plan.items() if k != 'plan_sha256'})
    schedule['analysis_plan_sha256'] = plan['plan_sha256']
    observations['schedule_sha256'] = ledger['schedule_sha256'] = A.digest(schedule)


def fixture():
    plan = dict(protocol='exp03-analysis-plan-v1', state='sealed_computational_plan',
                contract_sha256=A.sha(A.CONTRACT_RAW), plan_sha256='', stage='synthetic',
                experiment_family='exp03_hand_fixture', population='synthetic', primary_metric='recall_at_k',
                request_modes=['current'], policies=['B0', 'B2'], replications=1, minimum_valid_replications=1,
                cluster_unit='history', bootstrap=dict(seed=42, replicates=2000, confidence='0.95', percentile='linear'),
                contrasts=[dict(id='primary', treatment='B2', control='B0', role='primary')], delta_min='0.05',
                equivalence_margin=None, max_missing_fraction='0.02', max_missing_gap='0.01',
                truncation_rule='score_zero', alternative_set_rule='maximum_recall_over_alternatives',
                grader_sha256=None, interim_looks=0, tuning_trials=0, analysis_changed_after_results=False,
                cost_requirements=[dict(phase='retrieval', resource='calls', allocation='policy_incremental')])
    schedule = dict(protocol='exp03-schedule-v1', analysis_plan_sha256='', queries=[])
    observations = dict(protocol='exp03-observations-v1', schedule_sha256='',
                        selection_stage='post_packing_final_fence', rows=[])
    ledger = dict(protocol='exp03-cost-ledger-v1', schedule_sha256='', entries=[])
    for example in HAND['rows']:
        query = dict(stream_id=example['history'], cluster_id=example['history'], query_id=example['query'],
                     scope={'workspace': 'ws', 'project': 'proj', 'user': 'usr', 'agent': 'agt'},
                     kind='answerable', request_mode='current', gold_sets=[[ref(x) for x in example['relevant']]],
                     budget=dict(final_k=3, max_input_tokens=100, max_output_tokens=100), input_sha256='1'*64)
        schedule['queries'].append(query)
        for policy in plan['policies']:
            observations['rows'].append(dict(stream_id=query['stream_id'], query_id=query['query_id'], policy_id=policy,
                repetition=0, input_sha256=query['input_sha256'], gold_sha256=A.digest(query['gold_sets']),
                budget_sha256=A.digest(query['budget']), final_refs=[ref(x) for x in example[policy]], status='success',
                correct=None, fallback=False, grader_sha256=None))
            job = A.job_id(query['stream_id'], query['query_id'], policy, 0)
            ledger['entries'].append(dict(measurement_id='cost-'+job, job_id=job, policy_id=policy, phase='retrieval',
                resource='calls', allocation='policy_incremental', value='1', unit='count', basis='observed',
                missing_reason=None, source_sha256='2'*64, price_source=None))
    reseal(plan, schedule, observations, ledger)
    return plan, schedule, observations, ledger


class PairedAnalysisTests(unittest.TestCase):
    def rejects(self, inputs, code):
        with self.assertRaises(A.AnalysisError) as caught:
            A.analyze(*inputs)
        self.assertEqual(caught.exception.code, code)

    def test_hand_history_macro_not_query_weighted(self):
        result = A.analyze(*fixture())
        row, expected = result['contrasts'][0], HAND['expected']
        self.assertEqual(row['difference'], expected['paired_history_macro_difference'])
        self.assertEqual(row['treatment_history_macro'], expected['B2_history_macro'])
        self.assertEqual(row['control_history_macro'], expected['B0_history_macro'])
        self.assertAlmostEqual(row['query_weighted_difference_diagnostic'], expected['query_weighted_difference'])
        self.assertNotAlmostEqual(row['difference'], expected['query_weighted_difference'])
        for stream, difference in expected['history_differences'].items():
            self.assertEqual(row['histories'][stream]['difference'], difference)
        self.assertEqual(row['bootstrap_ci'], expected['bootstrap_ci'])
        self.assertEqual(row['cluster_t']['se'], expected['paired_se'])
        self.assertEqual(row['independent_clusters'], 2)
        self.assertEqual(result['arms']['B0']['answerable_hit_any'], 2)
        self.assertEqual(result['costs'][0]['complete_observed_total'], '3')
        self.assertFalse(result['final_card_acceptance'])

    def test_same_policy_outputs_have_zero_difference(self):
        inputs = fixture()
        rows = inputs[2]['rows']
        for i in range(0, len(rows), 2):
            rows[i+1]['final_refs'] = copy.deepcopy(rows[i]['final_refs'])
        row = A.analyze(*inputs)['contrasts'][0]
        self.assertEqual(row['difference'], HAND['same_policy_expected_difference'])
        self.assertEqual(row['bootstrap_ci'], [0, 0])
        self.assertNotEqual(row['verdict'], 'equivalence_interval_contained')

    def test_constant_history_difference_interval(self):
        inputs = fixture()
        for row in inputs[2]['rows']:
            query = next(q for q in inputs[1]['queries'] if q['query_id'] == row['query_id'])
            row['final_refs'] = copy.deepcopy(query['gold_sets'][0]) if row['policy_id'] == 'B2' else []
        row = A.analyze(*inputs)['contrasts'][0]
        self.assertEqual(row['bootstrap_ci'], HAND['constant_difference_expected_ci'])
        self.assertTrue(row['cluster_t']['degenerate'])

    def test_student_t_independent_table(self):
        self.assertAlmostEqual(A.t_two_sided_p(1, 1), 0.5, places=12)
        for df, expected in HAND['student_t_975_quantiles'].items():
            self.assertAlmostEqual(A.t_critical(0.95, int(df)), expected, places=8)
        self.assertEqual(A.t_two_sided_p(0, 9), 1)

    def test_holm_independent_example(self):
        self.assertEqual(A.holm({'a': 0.01, 'b': 0.04, 'c': 0.03}), {'a': 0.03, 'c': 0.06, 'b': 0.06})

    def test_duplicate_returned_ref_rejected(self):
        inputs = fixture()
        inputs[2]['rows'][0]['final_refs'] *= 2
        self.rejects(inputs, 'duplicate_returned_or_gold_ref')

    def test_alternative_gold_sets_not_flattened(self):
        query = fixture()[1]['queries'][0]
        query['gold_sets'] = [[ref('a'), ref('b')], [ref('c')]]
        row = fixture()[2]['rows'][0]
        row['final_refs'] = [ref('c')]
        self.assertEqual(A.score(query, row, 'recall_at_k', 'score_zero'), 1)

    def test_zero_gold_denominator_and_ambiguous_separate(self):
        inputs = fixture()
        inputs[1]['queries'][0]['kind'], inputs[1]['queries'][0]['gold_sets'] = 'clear_negative', []
        for row in inputs[2]['rows'][:2]:
            row['gold_sha256'] = A.digest([])
        reseal(*inputs)
        result = A.analyze(*inputs)
        self.assertEqual(result['arms']['B0']['negative_scheduled'], 1)
        self.assertEqual(result['arms']['B0']['negative_false_returns'], 1)
        self.assertEqual(result['contrasts'][0]['histories']['s1']['paired_queries'], 1)

    def test_absent_result_preserves_denominator_and_withholds_conclusion(self):
        inputs = fixture()
        del inputs[2]['rows'][0]
        result = A.analyze(*inputs)
        self.assertEqual(result['arms']['B0']['planned'], 3)
        self.assertEqual(result['arms']['B0']['status_counts']['missing'], 1)
        self.assertEqual(result['contrasts'][0]['verdict'], 'undetermined')
        self.assertIsNotNone(result['contrasts'][0]['missingness']['bounds'])

    def test_timeout_never_replaced_by_zero_or_silently_dropped(self):
        inputs = fixture()
        inputs[2]['rows'][1]['status'] = 'timeout'
        result = A.analyze(*inputs)
        row = result['contrasts'][0]
        self.assertEqual(result['arms']['B2']['planned'], 3)
        self.assertEqual(result['arms']['B2']['status_counts']['timeout'], 1)
        self.assertEqual(row['verdict'], 'undetermined')
        self.assertEqual(row['missingness']['missing_by_arm'], [1, 0])

    def test_truncation_primary_zero_and_exclusion_sensitivity(self):
        inputs = fixture()
        inputs[2]['rows'][1]['status'] = 'truncated'
        row = A.analyze(*inputs)['contrasts'][0]
        self.assertEqual(row['histories']['s1']['treatment'], 0)
        self.assertNotEqual(row['difference'], row['truncation_exclusion_sensitivity']['difference'])

    def test_user_cluster_not_histories_or_seeds(self):
        inputs = fixture()
        inputs[0]['cluster_unit'] = 'preassigned_cluster'
        for query in inputs[1]['queries']:
            query['cluster_id'] = 'same-user'
        reseal(*inputs)
        row = A.analyze(*inputs)['contrasts'][0]
        self.assertEqual(row['history_count'], 2)
        self.assertEqual(row['independent_clusters'], 1)
        self.assertIsNone(row['bootstrap_ci'])
        self.assertEqual(row['verdict'], 'undetermined')

    def test_repetitions_do_not_become_independent_histories(self):
        inputs = fixture()
        original_rows, original_costs = copy.deepcopy(inputs[2]['rows']), copy.deepcopy(inputs[3]['entries'])
        inputs[0]['replications'] = inputs[0]['minimum_valid_replications'] = 5
        for repetition in range(1, 5):
            for row, cost in zip(original_rows, original_costs):
                row, cost = copy.deepcopy(row), copy.deepcopy(cost)
                row['repetition'] = repetition
                cost['job_id'] = A.job_id(row['stream_id'], row['query_id'], row['policy_id'], repetition)
                cost['measurement_id'] = 'cost-'+cost['job_id']
                inputs[2]['rows'].append(row)
                inputs[3]['entries'].append(cost)
        reseal(*inputs)
        row = A.analyze(*inputs)['contrasts'][0]
        self.assertEqual(row['history_count'], 2)
        self.assertEqual(row['independent_clusters'], 2)
        self.assertEqual(row['cluster_t']['se'], 0.625)
        self.assertEqual(row['difference'], 0.375)

    def test_explicit_missing_row_is_recorded_but_not_observed_success(self):
        inputs = fixture()
        inputs[2]['rows'][0]['status'] = 'missing'
        result = A.analyze(*inputs)
        self.assertEqual(result['arms']['B0']['recorded'], 3)
        self.assertEqual(result['arms']['B0']['status_counts']['missing'], 1)
        self.assertEqual(result['contrasts'][0]['verdict'], 'undetermined')

    def test_costs_not_added_across_shared_and_nonadditive_allocations(self):
        inputs = fixture()
        for allocation in ['shared_preprocessing', 'whole_run_nonadditive']:
            extra = copy.deepcopy(inputs[3]['entries'][0])
            extra.update(measurement_id=allocation, job_id=None, policy_id=None, allocation=allocation, value='9')
            inputs[3]['entries'].append(extra)
        table = A.analyze(*inputs)['costs']
        self.assertEqual(len(table), 4)
        self.assertEqual({row['allocation']: row['complete_observed_total'] for row in table if row['policy_id'] is None},
                         {'shared_preprocessing': '9', 'whole_run_nonadditive': '9'})

    def test_multiple_nonadditive_peaks_preserve_values_not_sum(self):
        inputs = fixture()
        for n, value in enumerate(['9', '12']):
            extra = copy.deepcopy(inputs[3]['entries'][0])
            extra.update(measurement_id='peak-'+str(n), job_id=None, policy_id=None,
                         resource='peak_memory_bytes', unit='byte', allocation='whole_run_nonadditive', value=value)
            inputs[3]['entries'].append(extra)
        row = next(row for row in A.analyze(*inputs)['costs'] if row['resource'] == 'peak_memory_bytes')
        self.assertIsNone(row['complete_observed_total'])
        self.assertIsNone(row['observed_subtotal'])
        self.assertEqual(row['aggregation'], 'individual_measurements')
        self.assertEqual([item['value'] for item in row['individual_measurements']], ['9', '12'])
        self.assertEqual([item['measurement_id'] for item in row['individual_measurements']], ['peak-0', 'peak-1'])

    def test_peak_memory_never_summed_across_incremental_jobs(self):
        inputs = fixture()
        for cost in inputs[3]['entries']:
            cost.update(resource='peak_memory_bytes', unit='byte')
        inputs[0]['cost_requirements'][0]['resource'] = 'peak_memory_bytes'
        reseal(*inputs)
        row = A.analyze(*inputs)['costs'][0]
        self.assertIsNone(row['complete_observed_total'])
        self.assertEqual(len(row['individual_measurements']), 3)

    def test_unequal_cluster_residual_se_hand_value(self):
        result = A.cluster_t({'s1': -0.25, 's2': 1.0, 's3': -0.25},
                             {'s1': 'u1', 's2': 'u2', 's3': 'u1'}, 0.95)
        self.assertAlmostEqual(result['estimate'], 1/6)
        self.assertAlmostEqual(result['se'], 5/9)
        self.assertEqual(result['df'], 1)
        self.assertNotAlmostEqual(result['se'], 5/12)

    def test_unequal_cluster_bootstrap_keeps_whole_groups(self):
        # Two draws of whole u1/u2 groups have differences -.25, 1/6, 1
        # with probabilities 1/4, 1/2, 1/4. The 10th/90th percentiles
        # are -.25 and 1; independent-history resampling would differ.
        interval = A.cluster_bootstrap({'s1': -0.25, 's2': 1.0, 's3': -0.25},
            {'s1': 'u1', 's2': 'u2', 's3': 'u1'},
            dict(seed=42, replicates=2000, confidence='0.8', percentile='nearest_rank'), 'hand')
        self.assertEqual(interval, [-0.25, 1])

    def test_decimal_cost_sum_preserves_low_digits(self):
        inputs = fixture()
        for cost in inputs[3]['entries']:
            cost['value'] = '999999999999999999.123456789012'
        table = A.analyze(*inputs)['costs']
        self.assertEqual(table[0]['complete_observed_total'], '2999999999999999997.370370367036')

    def test_empty_relevant_history_not_fabricated_as_zero(self):
        inputs = fixture()
        for query in inputs[1]['queries']:
            query.update(kind='clear_negative', gold_sets=[])
        for row in inputs[2]['rows']:
            row['gold_sha256'] = A.digest([])
        reseal(*inputs)
        row = A.analyze(*inputs)['contrasts'][0]
        self.assertIsNone(row['difference'])
        self.assertEqual(row['history_count'], 0)
        self.assertIsNone(row['bootstrap_ci'])

    def test_explicit_equivalence_margin_does_not_come_from_delta(self):
        inputs = fixture()
        for i in range(0, len(inputs[2]['rows']), 2):
            inputs[2]['rows'][i+1]['final_refs'] = copy.deepcopy(inputs[2]['rows'][i]['final_refs'])
        self.assertNotEqual(A.analyze(*inputs)['contrasts'][0]['verdict'], 'equivalence_interval_contained')
        inputs[0]['equivalence_margin'] = '0.01'
        reseal(*inputs)
        self.assertEqual(A.analyze(*inputs)['contrasts'][0]['verdict'], 'equivalence_interval_contained')

    def test_duplicate_result_identity_rejected(self):
        inputs = fixture()
        inputs[2]['rows'][0] = copy.deepcopy(inputs[2]['rows'][1])
        self.rejects(inputs, 'result_identity_mismatch')

    def test_gold_input_budget_and_final_stage_bindings(self):
        for key in ['gold_sha256', 'budget_sha256', 'input_sha256']:
            inputs = fixture()
            inputs[2]['rows'][0][key] = '9'*64
            self.rejects(inputs, 'gold_input_or_budget_mismatch')
        inputs = fixture()
        inputs[2]['selection_stage'] = 'pre_packing'
        self.rejects(inputs, 'wrong_selection_stage')

    def test_unknown_budget_is_not_invented_or_used_for_adoption(self):
        inputs = fixture()
        for query in inputs[1]['queries']:
            query['budget']['max_input_tokens'] = None
        for row in inputs[2]['rows']:
            query = next(q for q in inputs[1]['queries'] if q['query_id'] == row['query_id'])
            row['budget_sha256'] = A.digest(query['budget'])
        reseal(*inputs)
        row = A.analyze(*inputs)['contrasts'][0]
        self.assertEqual(row['difference'], 0.375)
        self.assertEqual(row['inference_status'], 'budget_unobserved')
        self.assertEqual(row['verdict'], 'undetermined')

    def test_fallback_and_failed_cost_preserved(self):
        inputs = fixture()
        inputs[2]['rows'][0]['fallback'] = True
        inputs[2]['rows'][0]['status'] = 'api_error'
        result = A.analyze(*inputs)
        self.assertEqual(result['arms']['B0']['fallbacks'], 1)
        self.assertEqual(result['costs'][0]['complete_observed_total'], '3')

    def test_missing_cost_is_not_free(self):
        inputs = fixture()
        cost = inputs[3]['entries'][0]
        cost.update(value=None, basis='missing', missing_reason='not_measured', source_sha256=None)
        result = A.analyze(*inputs)
        self.assertIsNone(result['costs'][0]['complete_observed_total'])
        self.assertEqual(result['costs'][0]['missing_measurements'], 1)
        self.assertEqual(result['costs'][0]['observed_subtotal'], '2')

    def test_omitted_cost_row_and_duplicate_cost_rejected(self):
        inputs = fixture()
        del inputs[3]['entries'][0]
        self.rejects(inputs, 'missing_cost_measurement')
        inputs = fixture()
        inputs[3]['entries'][1] = copy.deepcopy(inputs[3]['entries'][0])
        self.rejects(inputs, 'duplicate_cost_measurement')

    def test_estimated_zero_is_still_estimated(self):
        inputs = fixture()
        inputs[3]['entries'][0].update(value='0', basis='estimated')
        self.assertIsNone(A.analyze(*inputs)['costs'][0]['complete_observed_total'])

    def test_currency_needs_price_source_and_time(self):
        inputs = fixture()
        for cost in inputs[3]['entries']:
            cost.update(resource='actual_currency', unit='USD')
        inputs[0]['cost_requirements'][0]['resource'] = 'actual_currency'
        reseal(*inputs)
        self.rejects(inputs, 'invalid_fields')
        for cost in inputs[3]['entries']:
            cost['price_source'] = {'sha256': '7'*64, 'as_of': '2026-11-01'}
        self.assertEqual(A.analyze(*inputs)['costs'][0]['unit'], 'USD')

    def test_sealed_plan_and_unapproved_confirmatory_rejected(self):
        inputs = fixture()
        inputs[0]['bootstrap']['seed'] = 43
        self.rejects(inputs, 'plan_hash_mismatch')
        inputs = fixture()
        inputs[0]['stage'] = 'confirmatory'
        reseal(*inputs)
        self.rejects(inputs, 'confirmatory_execution_evidence_required')

    def test_unknown_fields_and_parser_safety(self):
        inputs = fixture()
        inputs[0]['prompt'] = 'private-canary'
        self.rejects(inputs, 'invalid_fields')
        for raw in [b'{"a":1,"a":2}', b'{"x":NaN}', b'{"x":0.5}', b'{"x":"\\ud800"}', b'{"x":9223372036854775808}']:
            with self.assertRaises(A.AnalysisError):
                A.parse(raw)

    def test_cli_actual_files_provenance_immutable_output_and_no_source_text(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = [sys.executable, '-B', str(HERE / 'analyze_paired.py')]
            raw_hashes = {}
            for name, value in zip(('plan', 'schedule', 'observations', 'ledger'), fixture()):
                raw = A.canonical(value) + b'\n'
                path = root / (name+'.json')
                path.write_bytes(raw)
                raw_hashes[name] = hashlib.sha256(raw).hexdigest()
                args += ['--'+name, str(path)]
            out = root / 'analysis.json'
            args += ['--out', str(out)]
            process = subprocess.run(args, capture_output=True, check=False)
            self.assertEqual(process.returncode, 0, process.stdout)
            raw = out.read_bytes()
            result = json.loads(raw)
            self.assertEqual(result['provenance']['raw_input_sha256'], raw_hashes)
            self.assertEqual(result['contrasts'][0]['difference'], 0.375)
            self.assertNotIn(str(root).encode(), raw)
            self.assertEqual(json.loads(process.stdout)['output_sha256'], hashlib.sha256(raw).hexdigest())
            again = subprocess.run(args, capture_output=True, check=False)
            self.assertEqual(again.returncode, 2)
            self.assertEqual(out.read_bytes(), raw)
            self.assertNotIn(str(root).encode(), again.stdout+again.stderr)


if __name__ == '__main__':
    unittest.main()

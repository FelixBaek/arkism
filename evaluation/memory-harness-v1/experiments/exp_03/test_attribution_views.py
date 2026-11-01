"""Two views of exactly one execution: independent outcomes and strict bindings."""
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, HERE / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


V = load('_exp03_attribution_tests_impl', 'attribution_views.py')
T = load('_exp03_attribution_hand_fixture', 'test_analysis.py')
A = V.A


def seal(definition, labels):
    definition['definition_sha256'] = A.digest({k: v for k, v in definition.items() if k != 'definition_sha256'})
    labels['definition_sha256'] = definition['definition_sha256']


def fixture(accuracy=False):
    plan, schedule, observations, ledger = T.fixture()
    if accuracy:
        plan.update(primary_metric='provided_grader_accuracy', grader_sha256='a'*64)
        for row in observations['rows']:
            row.update(correct=False, grader_sha256='a'*64)
        T.reseal(plan, schedule, observations, ledger)
    definition = dict(protocol='exp03-attribution-views-v1', state='sealed_computational_views',
                      analysis_plan_sha256=plan['plan_sha256'], schedule_sha256=A.digest(schedule),
                      observations_sha256=A.digest(observations), ledger_sha256=A.digest(ledger),
                      reference_view='original', labels_timing='before_results', definition_sha256='',
                      views=[dict(id=name, criterion_sha256=str(i)*64, evaluator_sha256=str(i+2)*64)
                             for i, name in enumerate(('original', 'strict'), 1)])
    labels = dict(protocol='exp03-attribution-labels-v1', definition_sha256='', views=[])
    for name in ('original', 'strict'):
        item = dict(id=name, source_sha256='9'*64, queries=[], grades=[])
        for query in schedule['queries']:
            item['queries'].append(dict(stream_id=query['stream_id'], query_id=query['query_id'], available=True,
                                       kind=query['kind'], gold_sets=copy.deepcopy(query['gold_sets'])))
        for row in observations['rows']:
            item['grades'].append({k: row[k] for k in ('stream_id', 'query_id', 'policy_id', 'repetition', 'correct')})
        labels['views'].append(item)
    seal(definition, labels)
    return plan, schedule, observations, ledger, definition, labels


class AttributionViewTests(unittest.TestCase):
    def rejects(self, inputs, code):
        with self.assertRaises(A.AnalysisError) as caught:
            V.analyze_views(*inputs)
        self.assertEqual(caught.exception.code, code)

    def test_same_gold_same_difference_same_costs_and_no_input_mutation(self):
        inputs = fixture()
        before = copy.deepcopy(inputs)
        result = V.analyze_views(*inputs)
        self.assertEqual(inputs, before)
        self.assertEqual([r['analysis']['contrasts'][0]['difference'] for r in result['views']], [0.375, 0.375])
        self.assertEqual(result['comparisons'][0]['contrasts'][0]['difference_change'], 0)
        self.assertEqual(len({r['cost_table_sha256'] for r in result['views']}), 1)
        self.assertEqual(len({r['execution_sha256'] for r in result['views']}), 1)
        self.assertEqual(result['frozen_observations_sha256'], A.digest(inputs[2]))

    def test_recall_view_changes_labels_only_hand_difference(self):
        inputs = fixture()
        # Strict view changes q1 gold from {a,b} to {a}: B0 .5->1,
        # B2 remains1. History1 difference -.25->-.5, overall .375->.25.
        inputs[5]['views'][1]['queries'][0]['gold_sets'] = [[T.ref('a')]]
        result = V.analyze_views(*inputs)
        row = result['comparisons'][0]['contrasts'][0]
        self.assertEqual(row['view_difference'], 0.25)
        self.assertEqual(row['difference_change'], -0.125)
        self.assertEqual(row['reference_bootstrap_ci'], [-0.25, 1])
        self.assertEqual(row['view_bootstrap_ci'], [-0.5, 1])
        self.assertEqual(row['paired_change_bootstrap_ci'], [-0.25, 0])
        self.assertEqual(row['paired_change_independent_clusters'], 2)
        self.assertNotIn('cross_view_p', row)

    def test_accuracy_view_reversal_with_same_actual_refs(self):
        inputs = fixture(accuracy=True)
        for view in inputs[5]['views']:
            for grade in view['grades']:
                grade['correct'] = grade['policy_id'] == ('B2' if view['id'] == 'original' else 'B0')
        result = V.analyze_views(*inputs)
        row = result['comparisons'][0]['contrasts'][0]
        self.assertEqual((row['reference_difference'], row['view_difference'], row['difference_change']), (1, -1, -2))
        self.assertEqual(row['paired_change_bootstrap_ci'], [-2, -2])
        self.assertEqual((row['reference_order'], row['view_order']), ('treatment_above_control', 'control_above_treatment'))
        self.assertIn('not_closed_loop', result['claim_scope'])
        first, second = [v['analysis']['policy_rank_uncertainty'] for v in result['views']]
        self.assertEqual(first['pair_orders'], [dict(first='B0', second='B2', first_above=0, second_above=1, tied=0)])
        self.assertEqual(second['pair_orders'], [dict(first='B0', second='B2', first_above=1, second_above=0, tied=0)])
        self.assertEqual(first['policy_rank_ranges'], [dict(policy_id='B0', lower_best_rank=2, upper_worst_rank=2),
                                                     dict(policy_id='B2', lower_best_rank=1, upper_worst_rank=1)])

    def test_unknown_success_grade_withholds_whole_view_not_complete_case(self):
        inputs = fixture(accuracy=True)
        inputs[5]['views'][1]['grades'][0]['correct'] = None
        result = V.analyze_views(*inputs)
        row = result['views'][1]
        self.assertEqual(row['status'], 'uncomputable_missing_labels')
        self.assertEqual(row['unavailable_success_grades'], 1)
        self.assertEqual(row['planned_rows'], 6)
        self.assertIsNone(row['analysis'])
        self.assertEqual(result['comparisons'][0]['contrasts'], [])

    def test_unknown_query_labels_withhold_view(self):
        inputs = fixture()
        inputs[5]['views'][1]['queries'][0].update(available=False, kind=None, gold_sets=None)
        result = V.analyze_views(*inputs)
        self.assertEqual(result['views'][1]['unavailable_query_labels'], 1)
        self.assertIsNone(result['views'][1]['analysis'])

    def test_missing_execution_never_manufactured_from_available_labels(self):
        inputs = fixture(accuracy=True)
        del inputs[2]['rows'][0]
        for view in inputs[5]['views']:
            view['grades'][0]['correct'] = None
        inputs[4]['observations_sha256'] = A.digest(inputs[2])
        seal(inputs[4], inputs[5])
        result = V.analyze_views(*inputs)
        for row in result['views']:
            self.assertEqual((row['planned_rows'], row['recorded_execution_rows']), (6, 5))
            self.assertEqual(row['analysis']['arms']['B0']['status_counts']['missing'], 1)
            self.assertEqual(row['analysis']['contrasts'][0]['verdict'], 'undetermined')

    def test_timeout_and_fallback_remain_in_each_view(self):
        inputs = fixture()
        inputs[2]['rows'][0].update(status='timeout', fallback=True)
        inputs[4]['observations_sha256'] = A.digest(inputs[2])
        seal(inputs[4], inputs[5])
        result = V.analyze_views(*inputs)
        for row in result['views']:
            self.assertEqual(row['analysis']['arms']['B0']['status_counts']['timeout'], 1)
            self.assertEqual(row['analysis']['contrasts'][0]['verdict'], 'undetermined')

    def test_changed_execution_or_ledger_rejected(self):
        for target in ('refs', 'status', 'cost'):
            inputs = fixture()
            if target == 'refs':
                inputs[2]['rows'][0]['final_refs'] = []
            elif target == 'status':
                inputs[2]['rows'][0]['status'] = 'timeout'
            else:
                inputs[3]['entries'][0]['value'] = '2'
            with self.subTest(target=target):
                self.rejects(inputs, 'view_source_mismatch')

    def test_resealed_definition_must_rebind_labels(self):
        inputs = fixture()
        inputs[4]['views'][1]['criterion_sha256'] = 'e'*64
        self.rejects(inputs, 'view_definition_hash_mismatch')
        inputs[4]['definition_sha256'] = A.digest({k: v for k, v in inputs[4].items() if k != 'definition_sha256'})
        self.rejects(inputs, 'view_labels_binding_mismatch')

    def test_duplicate_or_missing_grade_and_query_rejected(self):
        for slot in ('queries', 'grades'):
            for mutation in ('duplicate', 'missing'):
                inputs = fixture()
                rows = inputs[5]['views'][1][slot]
                if mutation == 'duplicate':
                    rows[1] = copy.deepcopy(rows[0])
                else:
                    rows.pop()
                with self.subTest(slot=slot, mutation=mutation):
                    self.rejects(inputs, 'view_query_coverage_mismatch' if slot == 'queries' else 'view_grade_coverage_mismatch')

    def test_no_changing_history_scope_or_execution_fields_in_labels(self):
        inputs = fixture()
        inputs[5]['views'][1]['queries'][0]['cluster_id'] = 'new'
        self.rejects(inputs, 'invalid_fields')
        inputs = fixture()
        inputs[5]['views'][1]['grades'][0]['final_refs'] = []
        self.rejects(inputs, 'invalid_fields')

    def test_failed_execution_cannot_have_success_grade(self):
        inputs = fixture(accuracy=True)
        inputs[2]['rows'][0]['status'] = 'checker_failure'
        inputs[4]['observations_sha256'] = A.digest(inputs[2])
        seal(inputs[4], inputs[5])
        self.rejects(inputs, 'grade_without_successful_execution')

    def test_posthoc_label_timing_marks_every_view_exploratory(self):
        inputs = fixture()
        inputs[4]['labels_timing'] = 'after_results'
        seal(inputs[4], inputs[5])
        result = V.analyze_views(*inputs)
        self.assertTrue(result['exploratory'])
        self.assertTrue(all(row['analysis']['analysis_changes']['after_results'] for row in result['views']))

    def test_original_exploratory_stage_remains_exploratory_with_early_labels(self):
        inputs = fixture()
        inputs[0]['stage'] = 'exploratory'
        T.reseal(*inputs[:4])
        inputs[4].update(analysis_plan_sha256=inputs[0]['plan_sha256'], schedule_sha256=A.digest(inputs[1]),
                         observations_sha256=A.digest(inputs[2]), ledger_sha256=A.digest(inputs[3]))
        seal(inputs[4], inputs[5])
        result = V.analyze_views(*inputs)
        self.assertTrue(result['exploratory'])
        self.assertEqual(result['stage'], 'exploratory')

    def test_changed_query_populations_never_report_paired_change(self):
        inputs = fixture()
        inputs[5]['views'][1]['queries'][0].update(kind='clear_negative', gold_sets=[])
        result = V.analyze_views(*inputs)
        self.assertIsNotNone(result['views'][1]['analysis'])
        row = result['comparisons'][0]
        self.assertEqual(row['changed_query_kinds'], 1)
        self.assertEqual(row['status'], 'uncomputable_changed_query_population')
        self.assertEqual(row['contrasts'], [])

    def test_rank_ties_keep_full_range_instead_of_arbitrary_winner(self):
        result = V.analyze_views(*fixture(accuracy=True))
        rank = result['views'][0]['analysis']['policy_rank_uncertainty']
        self.assertEqual(rank['pair_orders'][0]['tied'], 1)
        self.assertEqual([(r['lower_best_rank'], r['upper_worst_rank']) for r in rank['policy_rank_ranges']], [(1, 2), (1, 2)])

    def test_rank_missing_query_does_not_select_complete_cases(self):
        inputs = fixture()
        inputs[2]['rows'][0]['status'] = 'timeout'
        inputs[4]['observations_sha256'] = A.digest(inputs[2])
        seal(inputs[4], inputs[5])
        rank = V.analyze_views(*inputs)['views'][0]['analysis']['policy_rank_uncertainty']
        self.assertEqual(rank['status'], 'uncomputable_incomplete_joint_population')
        self.assertEqual(rank['eligible_queries'], 3)
        self.assertEqual(rank['invalid_queries'], 1)
        self.assertEqual(rank['policy_rank_ranges'], [])

    def test_cluster_rank_keeps_histories_in_one_cluster_insufficient(self):
        inputs = fixture()
        inputs[0]['cluster_unit'] = 'preassigned_cluster'
        for query in inputs[1]['queries']:
            query['cluster_id'] = 'one-user'
        T.reseal(*inputs[:4])
        inputs[4].update(analysis_plan_sha256=inputs[0]['plan_sha256'], schedule_sha256=A.digest(inputs[1]),
                         observations_sha256=A.digest(inputs[2]), ledger_sha256=A.digest(inputs[3]))
        seal(inputs[4], inputs[5])
        rank = V.analyze_views(*inputs)['views'][0]['analysis']['policy_rank_uncertainty']
        self.assertEqual(rank['status'], 'insufficient_independent_clusters')
        self.assertEqual(rank['history_count'], 2)
        self.assertEqual(rank['independent_clusters'], 1)

    def test_real_cli_writes_two_view_comparison_and_hashes(self):
        inputs = fixture()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = [sys.executable, '-B', str(HERE / 'analyze_paired.py')]
            for flag, value in zip(('plan', 'schedule', 'observations', 'ledger', 'attribution-views', 'attribution-labels'), inputs):
                path = root / (flag+'.json')
                path.write_bytes(A.canonical(value))
                args.extend(('--'+flag, str(path)))
            output = root / 'result.json'
            args.extend(('--out', str(output)))
            proc = subprocess.run(args, capture_output=True, text=True, timeout=30)
            self.assertEqual(proc.returncode, 0, proc.stdout+proc.stderr)
            data = json.loads(output.read_bytes())
            self.assertEqual(len(data['attribution_sensitivity']['views']), 2)
            self.assertIn('attribution_source_sha256', data['provenance'])
            self.assertNotIn(directory, output.read_text())
            self.assertEqual(data['provenance']['raw_input_sha256']['attribution_labels'], A.digest(inputs[5]))

    def test_view_without_label_cli_rejected_without_output(self):
        inputs = fixture()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = [sys.executable, '-B', str(HERE / 'analyze_paired.py')]
            for flag, value in zip(('plan', 'schedule', 'observations', 'ledger', 'attribution-views'), inputs):
                path = root / (flag+'.json')
                path.write_bytes(A.canonical(value))
                args.extend(('--'+flag, str(path)))
            output = root / 'result.json'
            proc = subprocess.run(args+['--out', str(output)], capture_output=True, text=True, timeout=30)
            self.assertEqual(proc.returncode, 2)
            self.assertEqual(json.loads(proc.stdout)['code'], 'incomplete_attribution_arguments')
            self.assertFalse(output.exists())


if __name__ == '__main__':
    unittest.main()

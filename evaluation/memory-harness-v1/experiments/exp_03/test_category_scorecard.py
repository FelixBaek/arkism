"""Category gates preserve scheduled failures and existing EXP03 provenance checks."""
import copy
import importlib.util
from pathlib import Path
import unittest

HERE = Path(__file__).resolve().parent

def load(name):
    spec = importlib.util.spec_from_file_location('_category_test_'+name, HERE / (name+'.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

C, T = load('category_scorecard'), load('test_analysis')
A = C.A


def fixture():
    plan, schedule, observations, ledger = T.fixture()
    plan.update(primary_metric='provided_grader_accuracy', grader_sha256='3'*64)
    query, row = schedule['queries'][0], observations['rows'][0]
    schedule['queries'], observations['rows'] = [], []
    for i in range(10):
        q = copy.deepcopy(query)
        q['query_id'] = 'q'+str(i)
        schedule['queries'].append(q)
        for policy in plan['policies']:
            r = copy.deepcopy(row)
            r.update(query_id=q['query_id'], policy_id=policy, correct=i != 0, grader_sha256=plan['grader_sha256'])
            observations['rows'].append(r)
    T.reseal(plan, schedule, observations, ledger)
    mapping = dict(protocol='exp03-category-map-v1', schedule_sha256=A.digest(schedule),
                   categories=['weak', 'strong'], assignments=[dict(stream_id=q['stream_id'],query_id=q['query_id'],
                   category='weak' if i==0 else 'strong') for i,q in enumerate(schedule['queries'])])
    raw = A.canonical(mapping)
    return [plan, schedule, observations, raw, A.sha(raw)]


class CategoryScorecardTests(unittest.TestCase):
    def test_overall_ninety_does_not_hide_zero_category(self):
        result = C.report(*fixture())
        for arm in result['arms'].values():
            self.assertFalse(arm['all_categories_numerically_at_least_90'])
            self.assertEqual(arm['categories']['strong']['accuracy'], 1)
            self.assertEqual(arm['categories']['weak']['additional_correct_needed'], 1)
        self.assertFalse(result['scientific_acceptance'])
        self.assertFalse(result['holdout_independence_verified'])

    def test_missing_and_error_are_not_removed_from_denominator(self):
        args = fixture()
        args[2]['rows'] = [r for r in args[2]['rows'] if r['query_id'] != 'q1']
        for r in args[2]['rows']:
            if r['query_id'] == 'q2':
                r.update(status='timeout', correct=True)
        result = C.report(*args)['arms']['B0']['categories']['strong']
        self.assertEqual(result['scheduled'], 9)
        self.assertEqual(result['correct'], 7)
        self.assertEqual(result['status_counts'], dict(missing=1, timeout=1, success=7))
        self.assertFalse(result['numerical_threshold_met'])

    def test_duplicates_rejected_instead_of_choosing_better_answer(self):
        args = fixture()
        args[2]['rows'].append(copy.deepcopy(args[2]['rows'][0]))
        with self.assertRaises(A.AnalysisError): C.report(*args)

    def test_changed_map_requires_external_pin_and_cannot_omit_category(self):
        args = fixture()
        mapping = A.parse(args[3])
        mapping['assignments'][0]['category'] = 'strong'
        args[3] = A.canonical(mapping)
        with self.assertRaises(A.AnalysisError): C.report(*args)
        args[4] = A.sha(args[3])
        with self.assertRaises(A.AnalysisError): C.report(*args)

    def test_missing_grade_rejected_and_all_correct_is_only_numerical(self):
        args = fixture()
        for r in args[2]['rows']: r['correct'] = True
        result = C.report(*args)
        self.assertTrue(result['arms']['B0']['all_categories_numerically_at_least_90'])
        self.assertFalse(result['scientific_acceptance'])
        args[2]['rows'][0]['correct'] = None
        with self.assertRaises(A.AnalysisError): C.report(*args)

    def test_cli_reports_weak_category_without_acceptance_or_answer_text(self):
        import json
        import subprocess
        import sys
        import tempfile
        args = fixture()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            command = [sys.executable, str(HERE / 'category_scorecard.py')]
            for name, raw in zip(('plan', 'schedule', 'observations', 'category-map'),
                                 [*(A.canonical(v) for v in args[:3]), args[3]]):
                path = root / (name+'.json')
                path.write_bytes(raw)
                command += ['--'+name, str(path)]
            command += ['--trusted-category-map-sha256', args[4]]
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            out = json.loads(result.stdout)
            self.assertFalse(out['arms']['B0']['all_categories_numerically_at_least_90'])
            self.assertFalse(out['scientific_acceptance'])
            command[-1] = 'f'*64
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(result.stdout, '')
            self.assertEqual(json.loads(result.stderr), {'status': 'invalid_inputs'})

    def test_grader_and_source_bindings_remain_enforced(self):
        for field in ('grader_sha256', 'input_sha256', 'gold_sha256', 'budget_sha256'):
            args = fixture()
            args[2]['rows'][0][field] = 'f'*64
            with self.subTest(field=field), self.assertRaises(A.AnalysisError): C.report(*args)


if __name__ == '__main__':
    unittest.main()

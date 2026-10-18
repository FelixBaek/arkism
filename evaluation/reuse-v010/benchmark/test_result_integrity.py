"""Offline validation of benchmark result identity; no inference or services."""
import contextlib
import hashlib
import io
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

import measurement_audit
import run as benchmark


class ResultIntegrityTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        for module, name in ((benchmark, 'run_model'), (benchmark, 'retrieve_fact'),
                             (benchmark.subprocess, 'run'), (measurement_audit, 'private_file')):
            guard = patch.object(module, name, side_effect=AssertionError('offline test only'))
            guard.start()
            self.addCleanup(guard.stop)

    def fixture(self, name, condition='static', *, order=0, task='synthetic-task', **changes):
        row = {
            'experiment_id': 'synthetic-experiment-one', 'commit': 'a' * 40,
            'fixture_sha256': '1' * 64, 'model': 'synthetic-model-one',
            'task_id': task, 'condition': condition, 'replicate': 1, 'order': order,
            'success': True, 'timeout': False, 'rule_violations': 0, 'task_seconds': 2.0,
            'input_tokens': 100, 'cached_input_tokens': 80, 'output_tokens': 5,
            'review_seconds': None,
        }
        row.update(changes)
        directory = self.root / name
        directory.mkdir()
        (directory / 'result.json').write_text(json.dumps(row))
        usage = {field: row[field] for field in measurement_audit.FIELDS}
        (directory / 'events.json').write_text(json.dumps([{'type': 'turn.completed', 'usage': usage}]))
        (directory / 'prompt.txt').write_text('synthetic matching prompt')
        return directory

    def aggregate(self):
        with contextlib.redirect_stdout(io.StringIO()):
            benchmark.write_results(self.root)

    def assert_rejected_without_overwriting(self):
        paths = [self.root / name for name in ('results.json', 'results.csv', 'summary.json')]
        for path in paths:
            path.write_text('preserve existing output')
        with self.assertRaises(ValueError):
            self.aggregate()
        for path in paths:
            self.assertEqual(path.read_text(), 'preserve existing output')
        with self.assertRaises(ValueError):
            measurement_audit.audit(self.root)

    def test_duplicate_cells_rejected_before_aggregation(self):
        self.fixture('run-one')
        self.fixture('run-two', order=1)
        self.assert_rejected_without_overwriting()

    def test_each_protocol_field_mismatch_rejected_even_with_equal_prompt(self):
        changes = {'experiment_id': 'synthetic-experiment-two', 'commit': 'b' * 40,
                   'fixture_sha256': '2' * 64, 'model': 'synthetic-model-two'}
        for field, value in changes.items():
            with self.subTest(field=field):
                self.fixture('static')
                self.fixture('arkism', 'arkism', order=1, **{field: value})
                try:
                    self.assert_rejected_without_overwriting()
                finally:
                    shutil.rmtree(self.root / 'static')
                    shutil.rmtree(self.root / 'arkism')

    def test_audit_rejects_each_protocol_field_mismatch(self):
        changes = {'experiment_id': 'synthetic-experiment-two', 'commit': 'b' * 40,
                   'fixture_sha256': '2' * 64, 'model': 'synthetic-model-two'}
        for field, value in changes.items():
            with self.subTest(field=field):
                self.fixture('static')
                self.fixture('arkism', 'arkism', order=1, **{field: value})
                try:
                    with self.assertRaises(ValueError):
                        measurement_audit.audit(self.root)
                finally:
                    shutil.rmtree(self.root / 'static')
                    shutil.rmtree(self.root / 'arkism')

    def test_mixed_protocol_does_not_require_a_complete_pair_to_be_rejected(self):
        self.fixture('static')
        self.fixture('unpaired', 'arkism', task='different-task', order=1,
                     experiment_id='synthetic-experiment-two')
        self.assert_rejected_without_overwriting()

    def test_valid_pair_keeps_observations_and_unknown_cost(self):
        self.fixture('static')
        self.fixture('arkism', 'arkism', order=1)
        self.aggregate()
        rows = json.loads((self.root / 'results.json').read_text())
        self.assertEqual(len(rows), 2)
        summary = json.loads((self.root / 'summary.json').read_text())
        self.assertEqual(summary['static']['runs'], 1)
        self.assertEqual(summary['arkism']['successes'], 1)
        report = measurement_audit.audit(self.root)
        self.assertEqual(report['recorded_runs'], 2)
        self.assertEqual(len(report['paired_static_arkism']), 1)
        self.assertTrue(report['paired_static_arkism'][0]['prompt_bytes_equal'])
        self.assertEqual(report['conditions']['arkism']['uncached_input_tokens'], 20)
        self.assertIsNone(report['conditions']['arkism']['billed_cost_usd'])

    def test_valid_partial_dataset_remains_an_audit_of_observed_rows(self):
        self.fixture('none', 'none')
        self.aggregate()
        report = measurement_audit.audit(self.root)
        self.assertEqual(report['recorded_runs'], 1)
        self.assertEqual(report['paired_static_arkism'], [])
        self.assertEqual(report['conditions']['none']['runs'], 1)

    def test_public_historical_observations_and_aggregates_remain_unchanged(self):
        public = Path(__file__).resolve().parent / 'results'
        before = {p.relative_to(public): hashlib.sha256(p.read_bytes()).hexdigest()
                  for p in public.rglob('*') if p.is_file()}
        shutil.copytree(public, self.root, dirs_exist_ok=True)
        expected_rows = json.loads((public / 'results.json').read_text())
        expected_summary = json.loads((public / 'summary.json').read_text())
        expected_csv = (public / 'results.csv').read_bytes()
        expected_audit = json.loads(public.with_name('measurement-audit.json').read_text())
        self.aggregate()
        self.assertEqual(json.loads((self.root / 'results.json').read_text()), expected_rows)
        self.assertEqual(json.loads((self.root / 'summary.json').read_text()), expected_summary)
        self.assertEqual((self.root / 'results.csv').read_bytes(), expected_csv)
        self.assertEqual(measurement_audit.audit(self.root), expected_audit)
        self.assertEqual(expected_audit['recorded_runs'], 45)
        self.assertEqual(len(expected_audit['paired_static_arkism']), 15)
        self.assertEqual(len(expected_audit['evidence']), 135)
        after = {p.relative_to(public): hashlib.sha256(p.read_bytes()).hexdigest()
                 for p in public.rglob('*') if p.is_file()}
        self.assertEqual(after, before)


if __name__ == '__main__':
    unittest.main()

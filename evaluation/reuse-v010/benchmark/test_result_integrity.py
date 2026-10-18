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

    def assert_invalid_identity_record(self, record):
        directory = self.fixture('invalid')
        path = directory / 'result.json'
        path.write_text(json.dumps(record))
        original = path.read_bytes()
        try:
            for entrypoint, action in (('aggregation', self.aggregate),
                                       ('audit', lambda: measurement_audit.audit(self.root))):
                with self.subTest(entrypoint=entrypoint):
                    outputs = [self.root / name for name in ('results.json', 'results.csv', 'summary.json')]
                    for output in outputs:
                        output.write_text('preserve existing output')
                    error = None
                    try:
                        action()
                    except Exception as actual:
                        error = actual
                    self.assertIsInstance(error, ValueError)
                    for output in outputs:
                        self.assertEqual(output.read_text(), 'preserve existing output')
            self.assertEqual(path.read_bytes(), original)
        finally:
            shutil.rmtree(directory)

    def valid_identity_record(self):
        directory = self.fixture('template')
        row = json.loads((directory / 'result.json').read_text())
        shutil.rmtree(directory)
        return row

    def test_missing_and_null_identity_fields_are_rejected(self):
        for field in ('task_id', 'condition', 'replicate', 'experiment_id', 'commit',
                      'fixture_sha256', 'model'):
            for form in ('missing', 'null'):
                with self.subTest(field=field, form=form):
                    row = self.valid_identity_record()
                    if form == 'missing':
                        del row[field]
                    else:
                        row[field] = None
                    self.assert_invalid_identity_record(row)

    def test_blank_and_nonstring_identity_fields_are_rejected(self):
        for field in ('task_id', 'condition', 'experiment_id', 'commit', 'fixture_sha256', 'model'):
            for value in ('', ' \t\n', False, 7, [], {}):
                with self.subTest(field=field, value=value):
                    row = self.valid_identity_record()
                    row[field] = value
                    self.assert_invalid_identity_record(row)

    def test_replicate_requires_a_positive_integer(self):
        for value in ('1', 1.0, True, False, 0, -1, [], {}, ''):
            with self.subTest(value=value):
                row = self.valid_identity_record()
                row['replicate'] = value
                self.assert_invalid_identity_record(row)

    def test_unknown_condition_is_rejected_before_reporting(self):
        row = self.valid_identity_record()
        row['condition'] = 'synthetic-unknown-condition'
        self.assert_invalid_identity_record(row)

    def test_result_identity_requires_a_json_object(self):
        for value in (None, [], 'synthetic wrong result type', 1):
            with self.subTest(value=value):
                self.assert_invalid_identity_record(value)

    def test_identity_validation_does_not_require_sha_format(self):
        self.fixture('static', commit='synthetic-commit', fixture_sha256='synthetic-fixture')
        self.fixture('arkism', 'arkism', order=1, commit='synthetic-commit', fixture_sha256='synthetic-fixture')
        self.aggregate()
        report = measurement_audit.audit(self.root)
        self.assertEqual(report['recorded_runs'], 2)
        self.assertEqual(len(report['paired_static_arkism']), 1)

    def test_dataset_preflight_precedes_missing_evidence_access(self):
        for scenario, missing in (('duplicate', 'events.json'), ('mixed', 'prompt.txt')):
            with self.subTest(scenario=scenario):
                first = self.fixture('a-static')
                if scenario == 'duplicate':
                    second = self.fixture('b-static', order=1)
                else:
                    second = self.fixture('b-arkism', 'arkism', order=1,
                                          experiment_id='synthetic-other-experiment')
                try:
                    (first / missing).unlink()
                    outputs = [self.root / name for name in ('results.json', 'results.csv', 'summary.json')]
                    for output in outputs:
                        output.write_text('preserve existing output')
                    evidence_accesses = []
                    original_open = Path.open
                    def observe_open(path, *args, evidence_accesses=evidence_accesses,
                                     original_open=original_open, **kwargs):
                        if path.name in ('events.json', 'prompt.txt'):
                            evidence_accesses.append(path)
                        return original_open(path, *args, **kwargs)
                    error = None
                    with patch.object(Path, 'open', observe_open):
                        try:
                            measurement_audit.audit(self.root)
                        except Exception as actual:
                            error = actual
                    self.assertIsInstance(error, ValueError)
                    self.assertEqual(evidence_accesses, [])
                    for output in outputs:
                        self.assertEqual(output.read_text(), 'preserve existing output')
                finally:
                    shutil.rmtree(first)
                    shutil.rmtree(second)

    def test_preflight_reuses_result_and_event_bytes_for_observations_and_hashes(self):
        first = self.fixture('a-static')
        second = self.fixture('b-arkism', 'arkism', order=1)
        result_path = first / 'result.json'
        original_bytes = result_path.read_bytes()
        changed = json.loads(original_bytes)
        changed.update(experiment_id='synthetic-changed-after-validation', input_tokens=999)
        original_validate = measurement_audit.validate_result_identities
        validation_calls = 0
        def validate_then_change_file(rows):
            nonlocal validation_calls
            identities = original_validate(rows)
            validation_calls += 1
            if validation_calls == 1:
                result_path.write_text(json.dumps(changed))
            return identities
        original_open = Path.open
        reads = {}
        def observe_open(path, *args, **kwargs):
            mode = kwargs.get('mode', args[0] if args else 'r')
            if mode.startswith('r') and path.name in ('result.json', 'events.json'):
                reads[path] = reads.get(path, 0) + 1
            return original_open(path, *args, **kwargs)
        with patch.object(Path, 'open', observe_open), patch.object(
                measurement_audit, 'validate_result_identities', side_effect=validate_then_change_file):
            report = measurement_audit.audit(self.root)
        evidence = {item['path']: item['sha256'] for item in report['evidence']}
        self.assertEqual(evidence['a-static/result.json'], hashlib.sha256(original_bytes).hexdigest())
        self.assertNotEqual(result_path.read_bytes(), original_bytes)
        self.assertEqual(report['recorded_runs'], 2)
        self.assertEqual(len(report['paired_static_arkism']), 1)
        self.assertEqual(report['conditions']['static']['input_tokens'], 100)
        for directory in (first, second):
            for filename in ('result.json', 'events.json'):
                self.assertEqual(reads[directory / filename], 1)

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

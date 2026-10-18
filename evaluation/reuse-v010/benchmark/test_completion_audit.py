"""Planned result coverage through the read-only API and actual CLI entrypoint."""
import contextlib
import hashlib
import io
import json
from pathlib import Path
import runpy
import shutil
import socket
import sys
import tempfile
import unittest
from unittest.mock import patch
import urllib.request

import completion_audit
import measurement_audit
import run as benchmark


class CompletionAuditTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.out = Path(directory.name) / 'results'
        self.out.mkdir()
        self.schedule = benchmark.frozen_schedule()
        mapping = patch.object(benchmark, 'OUTPUT_LOCATIONS', {'synthetic-output': self.out})
        mapping.start()
        self.addCleanup(mapping.stop)
        self.guards = []
        for owner, name in ((benchmark, 'run_model'), (benchmark, 'retrieve_fact'),
                            (benchmark, 'evaluate'), (benchmark, 'run_one'),
                            (benchmark.subprocess, 'run'), (measurement_audit, 'private_file'),
                            (socket, 'create_connection'), (urllib.request, 'urlopen')):
            guard = patch.object(owner, name, side_effect=AssertionError('offline inspection only'))
            self.guards.append(guard.start())
            self.addCleanup(guard.stop)

    def tearDown(self):
        for guard in self.guards:
            guard.assert_not_called()

    def save(self, row, directory):
        directory.mkdir(exist_ok=True)
        benchmark.write_json(directory / 'result.json', row)
        benchmark.write_json(directory / 'events.json', [{'type': 'turn.completed', 'usage': {
            field: row[field] for field in measurement_audit.FIELDS}}])
        (directory / 'prompt.txt').write_text('Synthetic identical prompt, never sent to a model.')

    def seed(self):
        benchmark.write_spec(self.out, self.schedule)
        records = []
        for ordinal, (task, condition, rep) in enumerate(self.schedule):
            directory = self.out / f'{task["id"]}-{condition}-{rep}'
            row = benchmark.initial_record(task, condition, rep, ordinal, directory,
                                           'synthetic-role-fixture')
            row.update(success=True, timeout=False, rule_violations=0, task_seconds=1.0,
                       input_tokens=10, cached_input_tokens=0, output_tokens=2)
            self.save(row, directory)
            records.append((row, directory))
        return records

    def snapshot(self, directory=None):
        directory = directory or self.out
        return {str(path.relative_to(directory)): path.read_bytes()
                for path in directory.rglob('*') if path.is_file()}

    def inspect(self):
        with patch.object(benchmark, 'write_json', side_effect=AssertionError('read-only audit')):
            return completion_audit.audit(self.out)

    def cli(self, *flags, entrypoint=False):
        stdout, stderr = io.StringIO(), io.StringIO()
        args = ['--out', 'synthetic-output', *flags]
        with (
            contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr),
            patch.object(benchmark, 'write_json', side_effect=AssertionError('read-only CLI')),
        ):
            try:
                if entrypoint:
                    with patch.object(sys, 'argv', ['completion_audit.py', *args]):
                        runpy.run_path(completion_audit.__file__, run_name='__main__')
                    self.fail('The actual script entrypoint did not exit')
                status = completion_audit.main(args)
            except SystemExit as error:
                status = error.code
        return status, stdout.getvalue(), stderr.getvalue()

    def test_complete_45_reports_condition_denominators_without_writes(self):
        self.seed()
        before = self.snapshot()
        report = self.inspect()
        self.assertEqual(report['scheduled_cells'], 45)
        self.assertEqual(report['recorded_cells'], 45)
        self.assertEqual(report['missing_cell_count'], 0)
        self.assertEqual(report['missing_cells'], [])
        self.assertTrue(report['coverage_complete'])
        for condition in benchmark.CONDITIONS:
            self.assertEqual(report['conditions'][condition], {
                'scheduled_cells': 15, 'recorded_cells': 15, 'missing_cell_count': 0})
        status, stdout, stderr = self.cli('--require-complete', entrypoint=True)
        self.assertEqual(status, 0)
        self.assertEqual(json.loads(stdout), report)
        self.assertEqual(stderr, '')
        self.assertEqual(self.snapshot(), before)

    def test_removed_failed_result_keeps_partial_audit_and_fails_strict_coverage(self):
        records = self.seed()
        failed, directory = next(item for item in records if item[0]['condition'] == 'arkism')
        failed.update(success=False, rule_violations=1)
        benchmark.write_json(directory / 'result.json', failed)
        full = measurement_audit.audit(self.out)
        self.assertEqual(full['conditions']['arkism']['runs'], 15)
        self.assertEqual(full['conditions']['arkism']['successes'], 14)
        (directory / 'result.json').unlink()
        before = self.snapshot()
        report = self.inspect()
        self.assertEqual(report['recorded_cells'], 44)
        self.assertEqual(report['missing_cell_count'], 1)
        self.assertEqual(report['missing_cells'], [{
            field: failed[field] for field in ('task_id', 'condition', 'replicate')}])
        self.assertFalse(report['coverage_complete'])
        self.assertEqual(report['conditions']['arkism'], {
            'scheduled_cells': 15, 'recorded_cells': 14, 'missing_cell_count': 1})
        partial = measurement_audit.audit(self.out)
        self.assertEqual(partial['recorded_runs'], 44)
        self.assertEqual(partial['conditions']['arkism']['runs'], 14)
        self.assertEqual(partial['conditions']['arkism']['successes'], 14)
        self.assertNotIn('coverage_complete', partial)
        status, stdout, stderr = self.cli('--require-complete', entrypoint=True)
        self.assertEqual(status, 1)
        self.assertEqual(json.loads(stdout), report)
        self.assertEqual(stderr, '')
        self.assertEqual(self.cli()[0], 0)
        self.assertEqual(self.snapshot(), before)

    def test_empty_matching_plan_is_zero_of_45_and_never_complete(self):
        benchmark.write_spec(self.out, self.schedule)
        before = self.snapshot()
        report = self.inspect()
        self.assertEqual(report['scheduled_cells'], 45)
        self.assertEqual(report['recorded_cells'], 0)
        self.assertEqual(report['missing_cell_count'], 45)
        self.assertFalse(report['coverage_complete'])
        self.assertEqual(self.cli()[0], 0)
        self.assertEqual(self.cli('--require-complete')[0], 1)
        self.assertEqual(self.snapshot(), before)

    def test_failed_timeout_and_missing_outcomes_all_count_as_recorded(self):
        records = self.seed()
        failure, timeout, unknown = records[:3]
        failure[0].update(success=False, rule_violations=1)
        timeout[0].update(success=False, timeout=True)
        del unknown[0]['success']
        for row, directory in (failure, timeout, unknown):
            benchmark.write_json(directory / 'result.json', row)
        before = self.snapshot()
        report = self.inspect()
        self.assertEqual(report['recorded_cells'], 45)
        self.assertTrue(report['coverage_complete'])
        self.assertEqual(self.cli('--require-complete')[0], 0)
        self.assertEqual(self.snapshot(), before)
        self.assertNotIn('success_rate', report)

    def invalidate(self, scenario):
        records = self.seed()
        row, directory = records[0]
        if scenario == 'duplicate':
            self.save(row, self.out / 'synthetic-duplicate')
        elif scenario == 'mixed':
            row['model'] = 'synthetic-other-model'
        elif scenario == 'out_of_plan':
            row['task_id'] = 'synthetic-unknown-task'
        elif scenario == 'ordinal':
            row['order'] = 999
        elif scenario == 'folder':
            directory.rename(self.out / 'synthetic-wrong-folder')
            return
        elif scenario == 'missing_spec':
            (self.out / 'frozen-spec.json').unlink()
        elif scenario == 'wrong_spec':
            path = self.out / 'frozen-spec.json'
            saved = json.loads(path.read_text())
            saved['baseline'] = 'synthetic-other-baseline'
            benchmark.write_json(path, saved)
        benchmark.write_json(directory / 'result.json', row)

    def test_invalid_inputs_fail_before_any_report_or_write(self):
        for scenario in ('duplicate', 'mixed', 'out_of_plan', 'ordinal', 'folder',
                         'missing_spec', 'wrong_spec'):
            with self.subTest(scenario=scenario):
                shutil.rmtree(self.out)
                self.out.mkdir()
                self.invalidate(scenario)
                (self.out / 'summary.json').write_text('preserve synthetic existing aggregate')
                before = self.snapshot()
                status, stdout, stderr = self.cli('--require-complete')
                self.assertEqual(status, 2)
                self.assertEqual(stdout, '')
                self.assertIn('error:', stderr)
                self.assertEqual(self.snapshot(), before)

    def test_missing_plan_with_no_results_is_invalid_and_not_created(self):
        before = self.snapshot()
        status, stdout, stderr = self.cli()
        self.assertEqual(status, 2)
        self.assertEqual(stdout, '')
        self.assertIn('existing frozen benchmark spec', stderr)
        self.assertEqual(self.snapshot(), before)

    def test_result_rows_are_read_once_without_reading_usage_or_prompt(self):
        self.seed()
        original = Path.read_text
        reads = {}
        def read_once(path, *args, **kwargs):
            text = original(path, *args, **kwargs)
            self.assertNotIn(path.name, ('events.json', 'prompt.txt'))
            if path.name == 'result.json':
                reads[path] = reads.get(path, 0) + 1
                self.assertEqual(reads[path], 1)
            return text
        with patch.object(Path, 'read_text', read_once):
            self.assertTrue(self.inspect()['coverage_complete'])
        self.assertEqual(len(reads), 45)

    def test_archived_result_coverage_is_read_only(self):
        archived = benchmark.ROOT / 'results'
        before = {name: hashlib.sha256(value).hexdigest()
                  for name, value in self.snapshot(archived).items()}
        report = completion_audit.audit(archived)
        self.assertEqual(report['scheduled_cells'], 45)
        self.assertEqual(report['recorded_cells'], 45)
        self.assertTrue(report['coverage_complete'])
        after = {name: hashlib.sha256(value).hexdigest()
                 for name, value in self.snapshot(archived).items()}
        self.assertEqual(after, before)


if __name__ == '__main__':
    unittest.main()

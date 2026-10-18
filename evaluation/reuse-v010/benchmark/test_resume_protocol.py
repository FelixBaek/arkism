"""Actual-main resume gates with disposable rows and no external execution."""
import contextlib
import io
import json
from pathlib import Path
import shutil
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import measurement_audit
import run as benchmark


class ResumeProtocolTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.private = Path(directory.name)
        self.out = self.private / 'results'
        self.out.mkdir(mode=0o700)
        self.args = SimpleNamespace(out='synthetic-test-output', conditions='none',
            auth=str(self.private / 'must-not-read-auth.json'), exporter=None,
            approval='synthetic-role-fixture', repetitions=3)
        self.bind(benchmark, 'PRIVATE_ROOT', self.private)
        self.bind(benchmark, 'OUTPUT_LOCATIONS', {self.args.out: self.out})
        self.bind(benchmark, 'arguments', return_value=self.args)
        self.effects = [self.bind(benchmark, name, side_effect=AssertionError('offline boundary only'))
                        for name in ('run_model', 'retrieve_fact', 'evaluate')]
        self.effects.append(self.bind(benchmark.subprocess, 'run',
                                      side_effect=AssertionError('offline subprocess only')))
        self.runner = self.bind(benchmark, 'run_one', side_effect=AssertionError('unexpected run'))
        self.schedule = benchmark.frozen_schedule()

    def bind(self, owner, name, *args, **kwargs):
        binding = patch.object(owner, name, *args, **kwargs)
        value = binding.start()
        self.addCleanup(binding.stop)
        return value

    def main(self, conditions='none'):
        self.args.conditions = conditions
        with contextlib.redirect_stdout(io.StringIO()):
            benchmark.main()

    def row(self, ordinal, task, condition, rep):
        directory = self.out / f'{task["id"]}-{condition}-{rep}'
        row = benchmark.initial_record(task, condition, rep, ordinal, directory,
                                       'synthetic-role-fixture')
        row.update(success=False, rule_violations=1, task_seconds=1.0,
                   input_tokens=10, cached_input_tokens=0, output_tokens=2)
        return row

    def save(self, row, folder=None):
        directory = self.out / (folder or f'{row["task_id"]}-{row["condition"]}-{row["replicate"]}')
        directory.mkdir(exist_ok=True)
        benchmark.write_json(directory / 'result.json', row)
        return directory / 'result.json'

    def seed(self, condition='none', **changes):
        for ordinal, (task, current, rep) in enumerate(self.schedule):
            if current == condition:
                row = self.row(ordinal, task, current, rep)
                row.update(changes)
                self.save(row)
        benchmark.write_spec(self.out, self.schedule)

    def snapshot(self):
        return {str(path.relative_to(self.out)): path.read_bytes()
                for path in self.out.rglob('*') if path.is_file()}

    def assert_no_external_effects(self):
        for effect in self.effects:
            effect.assert_not_called()

    def assert_rejected(self, error=ValueError, conditions='none'):
        for name in ('results.json', 'results.csv', 'summary.json'):
            (self.out / name).write_text('preserve synthetic existing output')
        original = self.snapshot()
        with (
            patch.object(benchmark, 'write_spec', wraps=benchmark.write_spec) as spec_writer,
            patch.object(benchmark, 'write_results', wraps=benchmark.write_results) as writer,
        ):
            with self.assertRaises(error):
                self.main(conditions)
            spec_writer.assert_not_called()
            writer.assert_not_called()
        self.assertEqual(self.snapshot(), original)
        self.runner.assert_not_called()
        self.assert_no_external_effects()

    def reset_output(self):
        shutil.rmtree(self.out)
        self.out.mkdir(mode=0o700)

    def fake_run(self, task, condition, rep, ordinal, out, auth, exporter, approval):
        self.assertEqual(out, self.out)
        self.assertEqual(auth, self.args.auth)
        self.assertFalse(exporter)
        self.assertEqual(approval, self.args.approval)
        row = self.row(ordinal, task, condition, rep)
        self.save(row)
        return row

    def test_consistent_foreign_protocol_is_rejected_before_writes(self):
        for field in benchmark.PROTOCOL_FIELDS:
            with self.subTest(field=field):
                self.reset_output()
                self.seed(**{field: 'synthetic-other-' + field})
                self.assert_rejected()

    def test_unselected_foreign_result_is_still_rejected(self):
        self.seed('static', model='synthetic-other-model')
        self.assert_rejected(conditions='none')

    def test_out_of_plan_cells_and_wrong_folders_are_rejected(self):
        for change, value in (('task_id', 'synthetic-unknown-task'), ('replicate', 4),
                              ('folder', 'billing-none-1')):
            with self.subTest(change=change):
                self.reset_output()
                task, condition, rep = self.schedule[0]
                row = self.row(0, task, condition, rep)
                folder = value if change == 'folder' else None
                if change != 'folder':
                    row[change] = value
                self.save(row, folder)
                benchmark.write_spec(self.out, self.schedule)
                self.assert_rejected()

    def test_missing_or_wrong_order_is_rejected_before_runs(self):
        for value in (None, 1, True, 0.0):
            with self.subTest(order=value):
                self.reset_output()
                task, condition, rep = self.schedule[0]
                row = self.row(0, task, condition, rep)
                if value is None:
                    del row['order']
                else:
                    row['order'] = value
                self.save(row)
                benchmark.write_spec(self.out, self.schedule)
                self.assert_rejected(conditions='none,static,arkism')

    def test_existing_spec_must_match_the_whole_legacy_template(self):
        for field in ('baseline', 'tasks', 'schedule', 'limitations', 'improvement_threshold'):
            with self.subTest(field=field):
                self.reset_output()
                self.seed()
                path = self.out / 'frozen-spec.json'
                saved = json.loads(path.read_text())
                saved[field] = 'synthetic incompatible spec field'
                benchmark.write_json(path, saved)
                self.assert_rejected()

    def test_spec_json_boolean_and_float_do_not_equal_integer(self):
        for replacement in (True, 1.0):
            with self.subTest(replacement=replacement):
                self.reset_output()
                self.seed()
                path = self.out / 'frozen-spec.json'
                saved = json.loads(path.read_text())
                self.assertEqual(saved['schedule'][0][2], 1)
                saved['schedule'][0][2] = replacement
                benchmark.write_json(path, saved)
                self.assert_rejected()

    def test_missing_spec_with_results_is_not_recreated(self):
        self.seed()
        (self.out / 'frozen-spec.json').unlink()
        self.assert_rejected()

    def test_malformed_spec_is_not_repaired(self):
        self.seed()
        (self.out / 'frozen-spec.json').write_text('{invalid synthetic spec')
        self.assert_rejected()

    def test_unreadable_spec_is_not_repaired(self):
        self.seed()
        original_read = Path.read_text
        def denied_spec(path, *args, **kwargs):
            if path == self.out / 'frozen-spec.json':
                raise PermissionError('synthetic inaccessible spec')
            return original_read(path, *args, **kwargs)
        with patch.object(Path, 'read_text', denied_spec):
            self.assert_rejected(PermissionError)

    def test_incompatible_spec_without_results_fails_before_starting(self):
        benchmark.write_json(self.out / 'frozen-spec.json', {'synthetic': 'incompatible'})
        self.assert_rejected()

    def test_matching_legacy_spec_formatting_and_bytes_are_preserved(self):
        self.seed()
        path = self.out / 'frozen-spec.json'
        saved = json.loads(path.read_text())
        self.assertNotIn('model', saved)
        self.assertNotIn('experiment_id', saved)
        path.write_text(json.dumps(saved, sort_keys=True, indent=1) + '\n\t')
        original = path.read_bytes()
        with patch.object(benchmark, 'write_spec', wraps=benchmark.write_spec) as writer:
            self.main()
            writer.assert_not_called()
        self.assertEqual(path.read_bytes(), original)
        self.runner.assert_not_called()
        self.assert_no_external_effects()
        self.assertEqual(len(json.loads((self.out / 'results.json').read_text())), 15)

    def test_matching_partial_runs_only_missing_selected_cells(self):
        present = []
        for ordinal, (task, condition, rep) in enumerate(self.schedule):
            if condition in ('static', 'none') and condition not in present:
                self.save(self.row(ordinal, task, condition, rep))
                present.append(condition)
        benchmark.write_spec(self.out, self.schedule)
        original = self.snapshot()
        self.runner.side_effect = self.fake_run
        self.main('static')
        expected = [(task['id'], condition, rep, ordinal)
                    for ordinal, (task, condition, rep) in enumerate(self.schedule)
                    if condition == 'static'][1:]
        actual = [(call.args[0]['id'], call.args[1], call.args[2], call.args[3])
                  for call in self.runner.call_args_list]
        self.assertEqual(actual, expected)
        self.assertEqual(len(actual), 14)
        for name, data in original.items():
            self.assertEqual((self.out / name).read_bytes(), data)
        rows = json.loads((self.out / 'results.json').read_text())
        self.assertEqual(len(rows), 16)
        self.assertEqual(sum(row['condition'] == 'none' for row in rows), 1)
        self.assert_no_external_effects()

    def test_fresh_output_creates_original_spec_once_then_runs_selected_cells(self):
        self.runner.side_effect = self.fake_run
        with patch.object(benchmark, 'write_spec', wraps=benchmark.write_spec) as writer:
            self.main()
            writer.assert_called_once()
        self.assertEqual(self.runner.call_count, 15)
        saved = json.loads((self.out / 'frozen-spec.json').read_text())
        archived = json.loads((benchmark.ROOT / 'results/frozen-spec.json').read_text())
        self.assertEqual(saved, archived)
        self.assert_no_external_effects()

    def test_validated_rows_are_read_once_and_reused_for_aggregation(self):
        self.seed()
        reads = {}
        original_read = Path.read_text
        def changing_result(path, *args, **kwargs):
            text = original_read(path, *args, **kwargs)
            if path.name == 'result.json':
                reads[path] = reads.get(path, 0) + 1
                if reads[path] > 1:
                    row = json.loads(text)
                    row['model'] = 'synthetic-unvalidated-second-read'
                    return json.dumps(row)
            return text
        with patch.object(Path, 'read_text', changing_result):
            self.main()
        self.assertEqual(len(reads), 15)
        self.assertEqual(set(reads.values()), {1})
        rows = json.loads((self.out / 'results.json').read_text())
        self.assertEqual({row['model'] for row in rows}, {benchmark.MODEL})
        self.runner.assert_not_called()
        self.assert_no_external_effects()

    def test_archived_45_cells_resume_without_rewriting_evidence(self):
        shutil.copytree(benchmark.ROOT / 'results', self.out, dirs_exist_ok=True)
        self.out.chmod(0o700)
        original = self.snapshot()
        self.main('none,static,arkism')
        self.assertEqual(self.snapshot(), original)
        self.runner.assert_not_called()
        self.assert_no_external_effects()
        report = measurement_audit.audit(self.out)
        archived = json.loads((benchmark.ROOT / 'measurement-audit.json').read_text())
        self.assertEqual(report, archived)
        self.assertEqual(report['recorded_runs'], 45)
        self.assertEqual(len(report['paired_static_arkism']), 15)
        self.assertEqual(len(report['evidence']), 135)


if __name__ == '__main__':
    unittest.main()

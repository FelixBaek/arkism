"""R8 report regressions against actual R6 metrics and independent CSV rules."""
import contextlib
import copy
import csv
import io as streams
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

import contract_io as contract_io
import metrics
import report
from test_metrics import fixture, subset

QUERY_COLUMNS = ['run_id', 'policy_id', 'input_manifest_sha256', 'predictions_sha256',
                 'query_id', 'stream_id', 'label_status', 'status', 'raw_selected_count',
                 'unique_selected_count', 'relevant_count', 'matched_count', 'recall_at_k', 'hit_any']
STREAM_COLUMNS = ['run_id', 'policy_id', 'input_manifest_sha256', 'stream_id',
                  'scheduled_queries', 'relevant_queries', 'numerator', 'denominator', 'recall_at_k', 'null_reason']
SUMMARY_COLUMNS = ['run_id', 'policy_id', 'input_manifest_sha256', 'metric', 'numerator',
                   'denominator', 'value', 'null_reason']
SUMMARY_ORDER = ['stream_macro_recall_at_k', 'query_weighted_recall_at_k', 'query_hit_any_rate',
                 'all_query_failure_rate', 'observed_negative_false_positive_rate', 'negative_service_failure_rate']
FILES = {'queries.csv', 'streams.csv', 'summary.csv', 'summary.json'}


def metric_run(empty=False):
    arguments = subset(fixture(), []) if empty else fixture()
    return metrics.calculate_metrics(*arguments)


def raw_metric(value):
    return (json.dumps(value, sort_keys=True, indent=2, ensure_ascii=False) + '\n').encode('utf-8')


class ReportTests(unittest.TestCase):
    def assertCode(self, code, callback):
        with self.assertRaises(contract_io.ContractError) as caught:
            callback()
        self.assertEqual(caught.exception.code, code)
        return caught.exception

    def make(self, parent, value=None, name='report'):
        value = metric_run() if value is None else value
        raw = raw_metric(value)
        out = parent / name
        summary = report.make_report(value, contract_io.sha256_bytes(raw), out)
        return out, summary, value, raw

    def read_csv(self, path):
        raw = path.read_bytes()
        reader = csv.DictReader(streams.StringIO(raw.decode('utf-8'), newline=''))
        return reader.fieldnames, list(reader), raw

    def test_real_r6_metric_four_files_and_exact_summary(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            out, summary, metric, raw = self.make(Path(directory))
            self.assertEqual({p.name for p in out.iterdir()}, FILES)
            decoded = contract_io.decode_typed((out / 'summary.json').read_bytes(), 'ReportSummary')
            self.assertEqual(summary, decoded)
            self.assertEqual(summary['metrics_sha256'], contract_io.sha256_bytes(raw))
            self.assertEqual(summary['counts'], metric['counts'])
            self.assertEqual(summary['summary'], metric['summary'])
            self.assertEqual(summary['dataset_role'], 'unit_fixture')
            self.assertEqual(summary['status'], 'offline_contract_measurement')
            for field in ('run_id', 'policy_id', 'input_manifest_sha256', 'predictions_sha256', 'evaluation_manifest_sha256'):
                self.assertEqual(summary[field], metric[field])
            self.assertNotIn('artifacts', summary)

    def test_exact_headers_crlf_utf8_no_bom_and_summary_order(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            out, _, _, _ = self.make(Path(directory))
            for name, columns in (('queries.csv', QUERY_COLUMNS), ('streams.csv', STREAM_COLUMNS), ('summary.csv', SUMMARY_COLUMNS)):
                header, rows, raw = self.read_csv(out / name)
                self.assertEqual(header, columns)
                self.assertFalse(raw.startswith(b'\xef\xbb\xbf'))
                self.assertTrue(raw.endswith(b'\r\n'))
                self.assertNotIn(b'\n', raw.replace(b'\r\n', b''))
            _, rows, _ = self.read_csv(out / 'summary.csv')
            self.assertEqual([r['metric'] for r in rows], SUMMARY_ORDER)

    def test_failed_rows_zero_and_null_are_preserved(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            out, _, metric, _ = self.make(Path(directory))
            _, queries, _ = self.read_csv(out / 'queries.csv')
            self.assertEqual([r['query_id'] for r in queries], [r['query_id'] for r in metric['queries']])
            self.assertEqual(len(queries), 7)
            timeout = queries[1]
            self.assertEqual((timeout['status'], timeout['raw_selected_count'], timeout['unique_selected_count'],
                              timeout['matched_count'], timeout['recall_at_k'], timeout['hit_any']), ('timeout', '1', '1', '0', '0', '0'))
            for index in (3, 4, 5, 6):
                self.assertEqual(queries[index]['recall_at_k'], '')
                self.assertEqual(queries[index]['hit_any'], '')
            self.assertEqual(queries[5]['status'], 'timeout')
            _, streams_csv, _ = self.read_csv(out / 'streams.csv')
            self.assertEqual((streams_csv[2]['stream_id'], streams_csv[2]['denominator'], streams_csv[2]['recall_at_k'], streams_csv[2]['null_reason']), ('s3', '0', '', 'no_denominator'))

    def test_csv_values_match_every_metric_field(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            out, _, metric, _ = self.make(Path(directory))
            _, queries, _ = self.read_csv(out / 'queries.csv')
            for original, row in zip(metric['queries'], queries):
                for field, value in original.items():
                    expected = '' if value is None else format(value, '.17g') if type(value) is float else str(value)
                    self.assertEqual(row[field], expected)
            _, rows, _ = self.read_csv(out / 'summary.csv')
            stats = {r['metric']: r for r in rows}
            self.assertEqual(stats['stream_macro_recall_at_k']['value'], '0.625')
            self.assertEqual(stats['query_weighted_recall_at_k']['value'], '0.5')
            self.assertEqual(stats['query_hit_any_rate']['value'], '0.66666666666666663')
            self.assertEqual(stats['all_query_failure_rate']['value'], format(2/7, '.17g'))

    def test_empty_schedule_keeps_headers_and_six_null_statistics(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            out, _, _, _ = self.make(Path(directory), metric_run(empty=True))
            for name, columns in (('queries.csv', QUERY_COLUMNS), ('streams.csv', STREAM_COLUMNS)):
                header, rows, _ = self.read_csv(out / name)
                self.assertEqual(header, columns)
                self.assertEqual(rows, [])
            _, rows, _ = self.read_csv(out / 'summary.csv')
            self.assertEqual(len(rows), 6)
            for row in rows:
                self.assertEqual((row['numerator'], row['denominator'], row['value'], row['null_reason']), ('0', '0', '', 'no_denominator'))

    def test_unicode_rfc4180_escaping_internal_encoder(self):
        # Public DTO IDs remain ASCII; only the cell encoder primitive accepts
        # arbitrary Unicode for this independent UTF8/CSV escaping unit check.
        cell = '가,😀"\nnext'
        encoded = report._csv_bytes(('cell',), [{'cell': cell}])
        self.assertFalse(encoded.startswith(b'\xef\xbb\xbf'))
        self.assertIn('가'.encode('utf-8'), encoded)
        self.assertEqual(list(csv.reader(streams.StringIO(encoded.decode('utf-8'), newline=''))), [['cell'], [cell]])
        self.assertIn(b'""', encoded)

    def test_unicode_id_still_rejected_by_frozen_schema(self):
        value = metric_run()
        value['queries'][0]['query_id'] = '가'
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            self.assertCode('invalid_value', lambda: self.make(Path(directory), value))
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_callers_are_not_mutated_and_negative_zero_is_normalized(self):
        value = metric_run(empty=True)
        value['summary']['all_query_failure_rate']['numerator'] = -0.0
        before = copy.deepcopy(value)
        before_bytes = raw_metric(value)
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            out, _, _, _ = self.make(Path(directory), value)
            self.assertEqual(value, before)
            self.assertEqual(raw_metric(value), before_bytes)
            _, rows, _ = self.read_csv(out / 'summary.csv')
            row = next(row for row in rows if row['metric'] == 'all_query_failure_rate')
            self.assertEqual((row['numerator'], row['value']), ('0', ''))

    def test_invalid_metric_shape_and_hash_rejected_before_output(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            for change in ('extra', 'missing', 'nonfinite'):
                value = metric_run()
                if change == 'extra': value['secret-field'] = 'secret-payload'
                elif change == 'missing': del value['counts']
                else: value['summary']['stream_macro_recall_at_k']['value'] = float('nan')
                code = {'extra': 'unknown_field', 'missing': 'missing_field', 'nonfinite': 'invalid_value'}[change]
                self.assertCode(code, lambda: report.make_report(value, 'a' * 64, parent / 'bad'))
            for digest, code in (('A'*64, 'invalid_value'), ('a'*63, 'invalid_value'), (1, 'invalid_type')):
                self.assertCode(code, lambda: report.make_report(metric_run(), digest, parent / 'bad'))
            self.assertEqual(list(parent.iterdir()), [])

    def test_same_input_reports_are_byte_identical(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            a, _, _, _ = self.make(Path(directory), name='a')
            b, _, _, _ = self.make(Path(directory), name='b')
            for name in FILES:
                self.assertEqual((a / name).read_bytes(), (b / name).read_bytes())

    def test_artifact_actual_lengths_and_hashes_match_encoded_payloads(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            out, summary, value, _ = self.make(Path(directory))
            payloads = report._payloads(contract_io._normalize('MetricRun', value), summary)
            for name in FILES:
                actual = (out / name).read_bytes()
                self.assertEqual(len(actual), len(payloads[name]))
                self.assertEqual(contract_io.sha256_bytes(actual), contract_io.sha256_bytes(payloads[name]))

    def test_existing_empty_nonempty_and_symlink_outputs_preserved(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            target = parent / 'report'
            target.mkdir()
            self.assertCode('output_exists', lambda: self.make(parent))
            self.assertEqual(list(target.iterdir()), [])
            (target / 'original').write_bytes(b'original')
            self.assertCode('output_exists', lambda: self.make(parent))
            self.assertEqual((target / 'original').read_bytes(), b'original')
            (parent / 'alias').symlink_to(target, target_is_directory=True)
            self.assertCode('output_exists', lambda: self.make(parent, name='alias'))
            self.assertEqual({p.name for p in parent.iterdir()}, {'report', 'alias'})

    def test_parent_symlink_and_traversal_rejected(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            (parent / 'real').mkdir()
            (parent / 'alias').symlink_to('real', target_is_directory=True)
            self.assertCode('unsafe_path', lambda: self.make(parent / 'alias'))
            self.assertCode('unsafe_path', lambda: self.make(parent / 'real' / '..'))
            self.assertEqual(list((parent / 'real').iterdir()), [])

    def test_summary_is_published_last_after_completed_csvs(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            seen = []
            original = report._publish_file
            def publish(source, target, name):
                seen.append(name)
                if name == 'summary.json':
                    self.assertEqual(set(os.listdir(target)), FILES - {'summary.json'})
                return original(source, target, name)
            with mock.patch.object(report, '_publish_file', side_effect=publish):
                self.make(Path(directory))
            self.assertEqual(seen, ['queries.csv', 'streams.csv', 'summary.csv', 'summary.json'])

    def test_each_publication_failure_leaves_no_failed_final_or_staging(self):
        for failure_index in range(4):
            with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
                original = report._publish_file
                calls = 0
                def publish(source, target, name):
                    nonlocal calls
                    index = calls
                    calls += 1
                    if index == failure_index:
                        raise OSError('injected')
                    return original(source, target, name)
                with mock.patch.object(report, '_publish_file', side_effect=publish):
                    self.assertCode('io_error', lambda: self.make(Path(directory)))
                self.assertEqual(list(Path(directory).iterdir()), [])

    def test_directory_open_failure_cleans_reservations(self):
        import errno
        for which in ('staging', 'output'):
            with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
                original = report.os.open
                failed = False
                def opened(name, flags, *args, **kwargs):
                    nonlocal failed
                    target = name == 'report' if which == 'output' else str(name).startswith('.arkism-harness-report-')
                    if target and flags & os.O_DIRECTORY and not failed:
                        failed = True
                        raise OSError(errno.EMFILE, 'injected')
                    return original(name, flags, *args, **kwargs)
                with mock.patch.object(report.os, 'open', side_effect=opened):
                    self.assertCode('io_error', lambda: self.make(Path(directory)))
                self.assertTrue(failed)
                self.assertEqual(list(Path(directory).iterdir()), [])

    def test_staging_cleanup_failure_retracts_final_and_reports_artifacts(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            original = report._cleanup
            def cleanup(parent, name, descriptor, identity, owned):
                if name.startswith('.arkism-harness-report-'):
                    return False
                return original(parent, name, descriptor, identity, owned)
            with mock.patch.object(report, '_cleanup', side_effect=cleanup):
                error = self.assertCode('io_error', lambda: self.make(Path(directory)))
            self.assertFalse((Path(directory) / 'report').exists())
            self.assertIn('temporary artifacts', error.detail)
            self.assertEqual(len(list(Path(directory).iterdir())), 1)

    def test_competing_existing_directory_is_preserved(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            original = report.os.mkdir
            def mkdir(name, *args, **kwargs):
                if name == 'report':
                    original(name, *args, **kwargs)
                    (parent / 'report' / 'competitor').write_bytes(b'preserve')
                return original(name, *args, **kwargs)
            with mock.patch.object(report.os, 'mkdir', side_effect=mkdir):
                self.assertCode('output_exists', lambda: self.make(parent))
            self.assertEqual((parent / 'report' / 'competitor').read_bytes(), b'preserve')
            self.assertEqual([p.name for p in parent.iterdir()], ['report'])

    def test_replaced_output_inode_and_foreign_file_are_not_deleted(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            def publish(source, target, name):
                (parent / 'report').rename(parent / 'moved-owned')
                (parent / 'report').mkdir()
                (parent / 'report' / 'foreign').write_bytes(b'preserve')
                raise OSError('injected')
            with mock.patch.object(report, '_publish_file', side_effect=publish):
                error = self.assertCode('io_error', lambda: self.make(parent))
            self.assertEqual((parent / 'report' / 'foreign').read_bytes(), b'preserve')
            self.assertIn('cleanup incomplete', error.detail)

    def test_cli_binds_raw_metric_hash_and_four_artifacts(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            raw = raw_metric(metric_run()) + b'\n'  # valid bytes, different from canonical JSON
            source = parent / 'metrics.json'
            source.write_bytes(raw)
            output = parent / 'report'
            process = subprocess.run([sys.executable, str(Path(report.__file__)), '--metrics', str(source), '--out', str(output)], capture_output=True, text=True)
            self.assertEqual(process.returncode, 0, process.stderr)
            self.assertEqual(process.stderr, '')
            self.assertEqual(json.loads(process.stdout), {'files': 4, 'output_dir': str(output)})
            summary = contract_io.decode_typed((output / 'summary.json').read_bytes(), 'ReportSummary')
            self.assertEqual(summary['metrics_sha256'], contract_io.sha256_bytes(raw))
            self.assertNotEqual(summary['metrics_sha256'], contract_io.canonical_sha256('MetricRun', metric_run()))
            self.assertEqual(source.read_bytes(), raw)
            self.assertEqual({p.name for p in output.iterdir()}, FILES)

    def test_cli_strict_flags_and_help(self):
        for args in ([], ['--metrics', 'secret'], ['--metrics', 'x', '--metrics', 'y', '--out', 'z'],
                     ['--metrics', 'x', '--out', 'y', '--unknown-secret'], ['--metrics', 'x', '--out', 'y', 'secret-positional']):
            stdout, stderr = streams.StringIO(), streams.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                self.assertEqual(report.main(args), 2)
            self.assertEqual(stdout.getvalue(), '')
            self.assertEqual(set(json.loads(stderr.getvalue())), {'code', 'path', 'detail'})
            self.assertNotIn('secret', stderr.getvalue())
        with contextlib.redirect_stdout(streams.StringIO()), contextlib.redirect_stderr(streams.StringIO()):
            self.assertEqual(report.main(['--help']), 0)

    def test_cli_missing_symlink_and_malformed_metric(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            good = parent / 'metrics.json'
            good.write_bytes(raw_metric(metric_run()))
            alias = parent / 'alias'
            alias.symlink_to(good)
            bad = parent / 'bad'
            bad.write_bytes(b'{"secret":NaN}')
            for path, code, error in ((parent / 'missing', 1, 'io_error'), (alias, 2, 'unsafe_path'), (bad, 2, 'invalid_json')):
                stdout, stderr = streams.StringIO(), streams.StringIO()
                with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                    self.assertEqual(report.main(['--metrics', str(path), '--out', str(parent / 'report')]), code)
                self.assertEqual(json.loads(stderr.getvalue())['code'], error)
                self.assertFalse((parent / 'report').exists())

    def test_cli_status_write_failure_leaves_no_failed_output(self):
        class Broken:
            def write(self, text):
                raise OSError('secret stdout failure')
            def flush(self):
                pass
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            source = parent / 'metrics.json'
            source.write_bytes(raw_metric(metric_run()))
            stderr = streams.StringIO()
            with contextlib.redirect_stdout(Broken()), contextlib.redirect_stderr(stderr):
                self.assertEqual(report.main(['--metrics', str(source), '--out', str(parent / 'report')]), 1)
            self.assertFalse((parent / 'report').exists())
            self.assertEqual(json.loads(stderr.getvalue())['code'], 'io_error')
            self.assertNotIn('secret', stderr.getvalue())


if __name__ == '__main__':
    unittest.main()

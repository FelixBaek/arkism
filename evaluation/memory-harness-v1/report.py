"""Export the fixed offline MetricRun to three CSVs and one ReportSummary.

make_report binds the caller-supplied raw metrics hash. The CLI computes that
hash from the same bytes it strictly decodes; typed canonical hashes cannot
replace raw file provenance. No ranking, scoring, or quality claims are added.
"""
import argparse
import csv
import errno
import io as streams
import json
import os
from pathlib import Path
import secrets
import sys

import contract_io as contract_io
import fixture_gen as fixture_io

_COLUMNS = {name: tuple(columns) for name, columns in contract_io._CONTRACT['report_columns'].items()}
_SUMMARY_ORDER = tuple(contract_io._CONTRACT['report_rules']['summary_order'])
_FILES = ('queries.csv', 'streams.csv', 'summary.csv', 'summary.json')
# Reuse the accepted R3 inode-scoped I/O helpers, without generating fixtures or
# reading their spec. summary.json is the completion marker, exposed last.
_cleanup = fixture_io._cleanup
_publish_file = fixture_io._publish_file


def _fail(code, path='$', detail=''):
    raise contract_io.ContractError(code, path, detail)


def _cell(value):
    if value is None:
        return ''
    if type(value) is float:
        return format(0.0 if value == 0 else value, '.17g')
    return str(value)


def _csv_bytes(columns, rows):
    output = streams.StringIO(newline='')
    writer = csv.writer(output, dialect='excel', lineterminator='\r\n')
    writer.writerow(columns)
    for row in rows:
        writer.writerow([_cell(row[column]) for column in columns])
    return output.getvalue().encode('utf-8')


def _payloads(metric, summary):
    common = {key: metric[key] for key in ('run_id', 'policy_id', 'input_manifest_sha256')}
    queries = [dict(common, predictions_sha256=metric['predictions_sha256'], **query)
               for query in metric['queries']]
    streams = []
    for stream in metric['streams']:
        statistic = stream['recall_at_k']
        streams.append(dict(common, stream_id=stream['stream_id'],
                            scheduled_queries=stream['scheduled_queries'],
                            relevant_queries=stream['relevant_queries'],
                            numerator=statistic['numerator'], denominator=statistic['denominator'],
                            recall_at_k=statistic['value'], null_reason=statistic['null_reason']))
    statistics = [dict(common, metric=name, **metric['summary'][name]) for name in _SUMMARY_ORDER]
    return {'queries.csv': _csv_bytes(_COLUMNS['queries.csv'], queries),
            'streams.csv': _csv_bytes(_COLUMNS['streams.csv'], streams),
            'summary.csv': _csv_bytes(_COLUMNS['summary.csv'], statistics),
            'summary.json': (json.dumps(summary, ensure_ascii=False, sort_keys=True,
                                        indent=2, allow_nan=False) + '\n').encode('utf-8')}


def _summary(metric, metrics_sha256):
    result = {key: metric[key] for key in ('contract_id', 'run_id', 'policy_id',
              'input_manifest_sha256', 'predictions_sha256', 'evaluation_manifest_sha256',
              'counts', 'summary')}
    result.update(metrics_sha256=metrics_sha256, dataset_role='unit_fixture',
                  status='offline_contract_measurement')
    contract_io.validate_typed('ReportSummary', result)
    return result


def _make_report(metric_run: dict, metrics_sha256: str, output_dir: Path, status_writer=None) -> dict:
    """Validate and export exact DTO fields; return only the ReportSummary.

    The pure API has no raw input bytes from which to verify metrics_sha256's
    contents. Its caller supplies that binding; the file CLI derives it itself.
    Report artifact byte lengths/hashes are computed by acceptance from the four
    completed files, without extending ReportSummary with additional fields.
    """
    contract_io.validate_typed('MetricRun', metric_run)
    # This schema-aware owned copy normalizes float -0 without mutating callers.
    metric = contract_io._normalize('MetricRun', metric_run)
    summary = _summary(metric, metrics_sha256)
    payloads = _payloads(metric, summary)
    output = Path(output_dir)
    if output.name in ('', '.', '..') or '..' in output.parts or any('\x00' in part for part in output.parts):
        _fail('unsafe_path')
    parent_path = contract_io._safe_absolute(output.parent)
    output = parent_path / output.name
    parent = contract_io._directory_fd(parent_path)
    staging_name = None
    staging_fd = output_fd = None
    staging_identity = output_identity = None
    staged, published = {}, {}
    output_reserved = False
    success = False
    failure = None
    try:
        try:
            os.stat(output.name, dir_fd=parent, follow_symlinks=False)
        except FileNotFoundError:
            pass
        else:
            _fail('output_exists')
        for _ in range(10):
            candidate = '.arkism-harness-report-' + secrets.token_hex(16)
            try:
                os.mkdir(candidate, mode=0o700, dir_fd=parent)
            except FileExistsError:
                continue
            staging_name = candidate
            break
        else:
            _fail('io_error')
        staging_identity = fixture_io._identity(os.stat(staging_name, dir_fd=parent, follow_symlinks=False))
        staging_fd = os.open(staging_name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        if fixture_io._identity(os.fstat(staging_fd)) != staging_identity:
            _fail('unsafe_path')
        for name in _FILES:
            fixture_io._write_bytes(staging_fd, name, payloads[name], staged)
        staging = parent_path / staging_name
        # All four artifacts are complete before the final name is reserved.
        contract_io.decode_typed(contract_io._read_regular(staging / 'summary.json'), 'ReportSummary')
        os.mkdir(output.name, mode=0o700, dir_fd=parent)
        output_reserved = True
        output_identity = fixture_io._identity(os.stat(output.name, dir_fd=parent, follow_symlinks=False))
        output_fd = os.open(output.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        if fixture_io._identity(os.fstat(output_fd)) != output_identity:
            _fail('unsafe_path')
        for name in _FILES:
            _publish_file(staging_fd, output_fd, name)
            published[name] = staged[name]
        if set(os.listdir(output_fd)) != set(_FILES):
            _fail('invalid_state')
        for name in _FILES:
            actual = contract_io._read_regular(output / name)
            if len(actual) != len(payloads[name]) or contract_io.sha256_bytes(actual) != contract_io.sha256_bytes(payloads[name]):
                _fail('hash_mismatch')
        if status_writer is not None:
            status_writer(output)
        success = True
    except contract_io.ContractError as error:
        failure = error
    except OSError as error:
        code = ('output_exists' if error.errno == errno.EEXIST else
                'unsafe_path' if error.errno in (errno.ELOOP, errno.ENOTDIR) else 'io_error')
        failure = contract_io.ContractError(code, '$', '')
    finally:
        if staging_name is not None and not _cleanup(parent, staging_name, staging_fd, staging_identity, staged):
            success = False
            if failure is None:
                failure = contract_io.ContractError('io_error', '$', 'owned temporary artifacts remain')
            else:
                failure.detail = 'cleanup incomplete; inspect requested output parent'
        if not success and output_reserved:
            if not _cleanup(parent, output.name, output_fd, output_identity, published):
                if failure is None:
                    failure = contract_io.ContractError('io_error', '$', 'cleanup incomplete; inspect requested output parent')
                else:
                    failure.detail = 'cleanup incomplete; inspect requested output parent'
        for descriptor in (output_fd, staging_fd, parent):
            if descriptor is not None:
                os.close(descriptor)
    if failure is not None:
        raise failure
    return summary


def make_report(metric_run: dict, metrics_sha256: str, output_dir: Path) -> dict:
    """Export four artifacts and return exact ReportSummary with a caller hash.

    The CLI binds metrics_sha256 to its actual raw input bytes. The pure API
    validates that explicit hash's form; it cannot reconstruct original bytes.
    """
    return _make_report(metric_run, metrics_sha256, output_dir)


def _status(output):
    message = json.dumps({'output_dir': str(output), 'files': 4}, sort_keys=True) + '\n'
    if sys.stdout.write(message) != len(message):
        raise OSError('status write incomplete')
    sys.stdout.flush()


class _Parser(argparse.ArgumentParser):
    def error(self, message):
        _fail('invalid_value')


class _Once(argparse.Action):
    def __call__(self, parser, namespace, values, option_string=None):
        seen = vars(namespace).setdefault('_seen', set())
        if self.dest in seen:
            _fail('invalid_value')
        seen.add(self.dest)
        setattr(namespace, self.dest, values)


def main(args=None):
    parser = _Parser(description='Export the offline MetricRun report.', allow_abbrev=False)
    parser.add_argument('--metrics', required=True, action=_Once)
    parser.add_argument('--out', required=True, action=_Once)
    try:
        options = parser.parse_args(args)
        raw = contract_io._read_regular(Path(options.metrics))
        metric = contract_io.decode_typed(raw, 'MetricRun')
        _make_report(metric, contract_io.sha256_bytes(raw), Path(options.out), status_writer=_status)
        return 0
    except contract_io.ContractError as error:
        print(json.dumps({'code': error.code, 'path': error.path, 'detail': error.detail}, sort_keys=True), file=sys.stderr)
        return 1 if error.code == 'io_error' else 2
    except SystemExit as error:
        return error.code
    except Exception:
        print(json.dumps({'code': 'io_error', 'path': '$', 'detail': ''}), file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())

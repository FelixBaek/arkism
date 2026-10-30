"""Score the fixed offline schedule, preserving failures and null denominators.

This evaluator reads gold separately from the baseline ranker. These synthetic
retrieval metrics do not measure task success, learning, or user experience.
"""
import argparse
import json
import math
import os
from pathlib import Path
import re
import sys

import contract_io as io

_BINDINGS = ('input_manifest_sha256', 'policy_sha256', 'predictions_sha256',
             'evaluation_manifest_sha256')
_REF_FIELDS = ('origin', 'collection', 'id', 'revision', 'content_sha256')


def _fail(code, path='$'):
    raise io.ContractError(code, path, '')


def _bindings(prediction_run, bindings):
    if type(bindings) is not dict:
        _fail('invalid_type', '$.bindings')
    if set(bindings) != set(_BINDINGS):
        _fail('invalid_value', '$.bindings')
    for name in _BINDINGS:
        value = bindings[name]
        if type(value) is not str or re.fullmatch('[0-9a-f]{64}', value, re.ASCII) is None:
            _fail('invalid_value', '$.bindings.' + name)
    for name in ('input_manifest_sha256', 'policy_sha256'):
        if prediction_run[name] != bindings[name]:
            _fail('hash_mismatch', '$.' + name)


def _gold_by_query(probes, gold_file):
    labels = {}
    for label in gold_file['labels']:
        query = label['query_id']
        if query in labels:
            _fail('schedule_mismatch', '$.labels')
        labels[query] = label
    if set(labels) != {p['query_id'] for p in probes}:
        _fail('schedule_mismatch', '$.labels')
    return labels


def _validate_schedule(prediction_run, probe_file, gold_file):
    probes = probe_file['probes']
    predictions = prediction_run['predictions']
    # Predictions have an execution-order contract. Gold only requires coverage.
    if [p['query_id'] for p in predictions] != [p['query_id'] for p in probes]:
        _fail('schedule_mismatch', '$.predictions')
    labels = _gold_by_query(probes, gold_file)
    for probe, prediction in zip(probes, predictions):
        label = labels[probe['query_id']]
        if any(label[name] != probe[name] for name in ('stream_id', 'scope', 'at_ns', 'cutoff_accepted_seq')):
            _fail('snapshot_binding', '$.labels[*]')
        if any(prediction[name] != probe[name] for name in ('stream_id', 'scope')):
            _fail('snapshot_binding', '$.predictions[*]')
        if probe['policy_id'] != prediction_run['policy_id']:
            _fail('snapshot_binding', '$.probes[*].policy_id')
        # Check the raw count before deduplication, only for successful rows.
        if prediction['status'] == 'success' and len(prediction['selected_refs']) > probe['final_k']:
            _fail('schedule_mismatch', '$.predictions[*].selected_refs')
    return labels


def _ref_key(ref):
    return tuple(ref[name] for name in _REF_FIELDS)


def _query(probe, prediction, label):
    # Both row scopes have already been required to equal all four probe scope
    # fields, so this full Ref match has the same nine-field identity as scope+Ref.
    selected = {_ref_key(ref) for ref in prediction['selected_refs']}
    relevant = {_ref_key(ref) for ref in label['relevant_refs']}
    is_relevant = label['label_status'] == 'relevant'
    matched = len(selected & relevant) if is_relevant and prediction['status'] == 'success' else 0
    return {'query_id': probe['query_id'], 'stream_id': probe['stream_id'],
            'label_status': label['label_status'], 'status': prediction['status'],
            'raw_selected_count': len(prediction['selected_refs']), 'unique_selected_count': len(selected),
            'relevant_count': len(relevant), 'matched_count': matched,
            'recall_at_k': matched / len(relevant) if is_relevant else None,
            'hit_any': int(matched > 0) if is_relevant else None}


def _stat(numerator, denominator):
    return {'numerator': float(numerator), 'denominator': denominator,
            'value': numerator / denominator if denominator else None,
            'null_reason': 'none' if denominator else 'no_denominator'}


def _streams(queries):
    grouped = {}
    for query in queries:
        grouped.setdefault(query['stream_id'], []).append(query)
    result = []
    for stream in sorted(grouped):
        rows = grouped[stream]
        relevant = [row for row in rows if row['label_status'] == 'relevant']
        result.append({'stream_id': stream, 'scheduled_queries': len(rows), 'relevant_queries': len(relevant),
                       'recall_at_k': _stat(math.fsum(row['recall_at_k'] for row in relevant), len(relevant))})
    return result


def _aggregate(queries, streams):
    relevant = [row for row in queries if row['label_status'] == 'relevant']
    negative = [row for row in queries if row['label_status'] == 'clear_negative']
    successful_negative = [row for row in negative if row['status'] == 'success']
    relevant_streams = [row for row in streams if row['relevant_queries'] > 0]
    failed = sum(row['status'] != 'success' for row in queries)
    counts = {'scheduled_queries': len(queries), 'streams': len(streams), 'relevant_queries': len(relevant),
              'clear_negative_queries': len(negative),
              'ambiguous_queries': sum(row['label_status'] == 'ambiguous' for row in queries),
              'successful_queries': len(queries) - failed, 'failed_queries': failed,
              'streams_without_relevant': len(streams) - len(relevant_streams)}
    summary = {
        'stream_macro_recall_at_k': _stat(math.fsum(row['recall_at_k']['value'] for row in relevant_streams), len(relevant_streams)),
        'query_weighted_recall_at_k': _stat(math.fsum(row['recall_at_k'] for row in relevant), len(relevant)),
        'query_hit_any_rate': _stat(sum(row['hit_any'] for row in relevant), len(relevant)),
        'all_query_failure_rate': _stat(failed, len(queries)),
        'observed_negative_false_positive_rate': _stat(sum(row['unique_selected_count'] > 0 for row in successful_negative), len(successful_negative)),
        'negative_service_failure_rate': _stat(sum(row['status'] != 'success' or row['unique_selected_count'] > 0 for row in negative), len(negative)),
    }
    return counts, summary


def calculate_metrics(prediction_run: dict, probe_file: dict, gold_file: dict, bindings: dict) -> dict:
    """Calculate MetricRun from a complete schedule and verified hash bindings.

    The caller supplies the four raw-file hashes; this pure function validates
    their form and the two hashes carried by PredictionRun. File verification
    belongs to the CLI/loader. Inputs are neither reordered nor mutated.
    """
    for name, value in (('PredictionRun', prediction_run), ('ProbeFile', probe_file), ('GoldFile', gold_file)):
        io.validate_typed(name, value)
    _bindings(prediction_run, bindings)
    labels = _validate_schedule(prediction_run, probe_file, gold_file)
    queries = [_query(probe, prediction, labels[probe['query_id']])
               for probe, prediction in zip(probe_file['probes'], prediction_run['predictions'])]
    streams = _streams(queries)
    counts, summary = _aggregate(queries, streams)
    result = {'contract_id': prediction_run['contract_id'], 'run_id': prediction_run['run_id'],
              'policy_id': prediction_run['policy_id'], **bindings,
              'counts': counts, 'queries': queries, 'streams': streams, 'summary': summary}
    io.validate_typed('MetricRun', result)
    return result


class _Parser(argparse.ArgumentParser):
    def error(self, message):
        # Do not echo arbitrary raw arguments in argparse diagnostics.
        _fail('invalid_value')


class _Once(argparse.Action):
    def __call__(self, parser, namespace, values, option_string=None):
        seen = vars(namespace).setdefault('_seen', set())
        if self.dest in seen:
            _fail('invalid_value')
        seen.add(self.dest)
        setattr(namespace, self.dest, values)


def _run(options):
    evaluation = io.load_evaluation_bundle(Path(options.evaluation_manifest))
    raw_predictions = io._read_regular(Path(options.predictions))
    predictions = io.decode_typed(raw_predictions, 'PredictionRun')
    inputs = evaluation['input_bundle']
    bindings = {'input_manifest_sha256': inputs['manifest_sha256'], 'policy_sha256': inputs['policy_sha256'],
                'predictions_sha256': io.sha256_bytes(raw_predictions),
                'evaluation_manifest_sha256': evaluation['manifest_sha256']}
    result = calculate_metrics(predictions, inputs['probes'], evaluation['gold'], bindings)
    output = Path(options.out)
    name = os.path.basename(os.fspath(output))
    if '..' in output.parts or any('\x00' in part for part in output.parts) or name in ('', '.', '..') or name != output.name:
        _fail('unsafe_path')
    io.write_json_new(output.parent / name, result)
    return {'status': 'success', 'output': str(output.absolute()), 'queries': len(result['queries'])}


def main(args=None):
    parser = _Parser(description='Score the offline scheduled retrieval run.', allow_abbrev=False)
    for flag in ('evaluation-manifest', 'predictions', 'out'):
        parser.add_argument('--' + flag, required=True, action=_Once)
    try:
        options = parser.parse_args(args)
        result = _run(options)
        print(json.dumps(result, sort_keys=True, separators=(',', ':')))
        return 0
    except io.ContractError as error:
        print(json.dumps({'code': error.code, 'path': error.path, 'detail': error.detail},
                         sort_keys=True, separators=(',', ':')), file=sys.stderr)
        return 1 if error.code == 'io_error' else 2
    except SystemExit as error:
        return error.code
    except Exception:
        print(json.dumps({'code': 'io_error', 'path': '$', 'detail': ''}, separators=(',', ':')), file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())

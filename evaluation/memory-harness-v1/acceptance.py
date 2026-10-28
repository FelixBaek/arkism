"""Run the original standalone offline acceptance protocol and retain evidence.

The default executor includes go build and a separate binary invocation. Calling
it requires authorization for those operations. Tests patch _execute; a mocked
suite or Go run()-based integration does not establish standalone completion.
"""
import argparse
import copy
import contextlib
import csv
import io as text_io
import json
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import sys

import contract_io as io
import metrics

_REFERENCE = '27bb41807ff48d47559ba2cfc30feb300caacecf'
_UNKNOWN_IDENTITY = 'owned temporary cleanup incomplete: inode identity unavailable'
_LIMITATIONS = ['offline synthetic baseline only', 'no production API/DB integration',
                'no dynamic state or real token budget', 'no new holdout performance',
                'no independent review yet']
_PYTHON_TESTS = ('test_contract_io.py', 'test_fixture_gen.py', 'test_metrics.py', 'test_report.py')
_FILES = ('candidates.json', 'gold.json', 'implementation_contract.json', 'input_manifest.json',
          'policy.json', 'probes.json', 'split_map.json', 'evaluation_manifest.json')
_REPORTS = ('queries.csv', 'streams.csv', 'summary.csv', 'summary.json')
_SELECTIONS = {'q01':['memory:a','memory:c','memory:b'], 'q02':['fixture_alt:a','memory:a'],
               'q03':['memory:b'], 'q04':['memory:b','memory:c'], 'q05':[], 'q06':[], 'q07':['memory:a']}


def _fail(code, path='$'):
    raise io.ContractError(code, path, '')


def _environment():
    # Only local tool/cache configuration is inherited; no service credentials.
    env = {key: os.environ[key] for key in ('PATH', 'HOME', 'TMPDIR', 'GOCACHE', 'GOMODCACHE') if key in os.environ}
    env.update(GOTOOLCHAIN='local', GOPROXY='off', GOSUMDB='off', PYTHONDONTWRITEBYTECODE='1')
    return env


def _execute(argv, cwd, env):
    return subprocess.run(argv, cwd=cwd, env=env, capture_output=True, check=False)


def _git(root, *args):
    result = subprocess.run(['git', '-c', 'core.fsmonitor=false', *args], cwd=root,
                            env=_environment(), capture_output=True, check=False)
    if result.returncode:
        _fail('io_error')
    return result.stdout


def _contract(root):
    path = root / 'evaluation/memory-harness-v1/contracts/implementation_contract.json'
    raw = io._read_regular(path)
    if io.sha256_bytes(raw) != io.CONTRACT_SHA256:
        _fail('hash_mismatch')
    return json.loads(raw)


def _relative(name):
    path = Path(name)
    if not name or path.is_absolute() or '..' in path.parts or '\x00' in name:
        _fail('unsafe_path')
    return path


def _repository(root, contract):
    observed = _git(root, 'rev-parse', '--verify', 'HEAD').decode('ascii').strip()
    if re.fullmatch('[0-9a-f]{40}', observed) is None:
        _fail('invalid_value')
    allowlist = {p for paths in contract['file_ownership'].values() for p in paths}
    try:
        entries = _git(root, 'status', '--porcelain=v1', '-z', '--untracked-files=all').decode('utf-8').split('\0')
    except UnicodeError:
        _fail('invalid_unicode')
    dirty = []
    cursor = 0
    while cursor < len(entries) and entries[cursor]:
        entry = entries[cursor]; cursor += 1
        if len(entry) < 4 or entry[2] != ' ':
            _fail('invalid_value')
        status, name = entry[:2], entry[3:]
        _relative(name)
        row = {'path': name, 'status': status, 'sha256': None}
        if 'R' in status or 'C' in status:
            if cursor >= len(entries) or not entries[cursor]:
                _fail('invalid_value')
            row['original_path'] = str(_relative(entries[cursor])); cursor += 1
        if name in allowlist and 'D' not in status:
            row['sha256'] = io.sha256_bytes(io._read_regular(root / name))
        dirty.append(row)
    dirty.sort(key=lambda row: row['path'])
    digest = io.sha256_bytes(json.dumps(dirty, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode())
    return {'reference_commit': _REFERENCE, 'observed_commit': observed,
            'dirty_files_sha256': digest, 'dirty_files': dirty}


def _outside(repository, contract):
    allowed = {p for paths in contract['file_ownership'].values() for p in paths}
    return {(row['path'], row['status'], row.get('original_path')) for row in repository['dirty_files']
            if row['path'] not in allowed or row.get('original_path', row['path']) not in allowed}


def _plan(root, work):
    scripts = root / 'evaluation/memory-harness-v1'
    result = [{'check_id':'go_tests', 'argv':['go','test','-json','./internal/memoryharness',
               './cmd/memory-harness-baseline','-count=1'], 'kind':'go_tests'}]
    for name in _PYTHON_TESTS:
        result.append({'check_id':name[:-3], 'argv':[sys.executable,'-m','unittest','discover',
                       '-s',str(scripts),'-p',name,'-v'], 'kind':'python_tests'})
    result.extend([
        {'check_id':'fixture','argv':[sys.executable,str(scripts/'fixture_gen.py'),'--seed','17',
         '--contract',str(scripts/'contracts/implementation_contract.json'),'--out',str(work/'fixture')], 'kind':'status'},
        {'check_id':'baseline_build','argv':['go','build','-o',str(work/'baseline'),
         './cmd/memory-harness-baseline'], 'kind':'build'},
        {'check_id':'baseline','argv':[str(work/'baseline'),'-manifest',str(work/'fixture/input_manifest.json'),
         '-run-id','baseline-seed17','-output',str(work/'predictions.json')], 'kind':'status'},
        {'check_id':'metrics','argv':[sys.executable,str(scripts/'metrics.py'),'--evaluation-manifest',
         str(work/'fixture/evaluation_manifest.json'),'--predictions',str(work/'predictions.json'),
         '--out',str(work/'metrics.json')], 'kind':'status'},
        {'check_id':'report','argv':[sys.executable,str(scripts/'report.py'),'--metrics',str(work/'metrics.json'),
         '--out',str(work/'report')], 'kind':'status'},
    ])
    return result


def _go_counts(raw):
    packages = {'github.com/FelixBaek/arkism/internal/memoryharness': set(),
                'github.com/FelixBaek/arkism/cmd/memory-harness-baseline': set()}
    try:
        for line in raw.decode('utf-8').splitlines():
            event = json.loads(line)
            if event.get('Action') in ('fail', 'skip'):
                _fail('invalid_value', '$.checks')
            if event.get('Action') == 'pass' and event.get('Test') and event.get('Package') in packages:
                packages[event['Package']].add(event['Test'])
    except (ValueError, UnicodeError, AttributeError):
        _fail('invalid_value', '$.checks')
    if any(not tests for tests in packages.values()):
        _fail('invalid_value', '$.checks')
    harness = packages['github.com/FelixBaek/arkism/internal/memoryharness']
    if any(not any(name.startswith(prefix) for name in harness) for prefix in
           ('TestCodec', 'TestCanonical', 'TestDecay', 'TestRRF', 'TestPack', 'TestRankAndPack')):
        _fail('invalid_value', '$.checks')
    if not any(name.startswith('TestCLI') for name in packages['github.com/FelixBaek/arkism/cmd/memory-harness-baseline']):
        _fail('invalid_value', '$.checks')
    return {'nodes': sum(map(len, packages.values())),
            'leaf': sum(not any(other.startswith(name+'/') for other in tests) for tests in packages.values() for name in tests),
            'names': {package: sorted(tests) for package, tests in packages.items()}}


def _python_counts(raw):
    text = raw.decode('utf-8', errors='strict')
    matches = re.findall(r'^Ran ([0-9]+) tests? in .+$', text, re.MULTILINE)
    if len(matches) != 1 or int(matches[0]) == 0 or re.search(r'^OK\s*$', text, re.MULTILINE) is None:
        _fail('invalid_value', '$.checks')
    # OK(skipped=...) and any skip summary are deliberately not accepted above.
    names = re.findall(r'^(test\S+) \([^\n]+\) \.\.\. ok$', text, re.MULTILINE)
    if len(names) != int(matches[0]):
        _fail('invalid_value', '$.checks')
    return {'tests': int(matches[0]), 'names': names}


def _judge(spec, completed):
    if completed.returncode != spec.get('expected_exit', 0):
        _fail('invalid_value' if spec['kind'] == 'negative' else 'io_error', '$.checks')
    if spec['kind'] == 'go_tests':
        return _go_counts(completed.stdout)
    if spec['kind'] == 'python_tests':
        return _python_counts(completed.stdout + completed.stderr)
    if spec['kind'] == 'negative':
        try:
            error = json.loads(completed.stderr)
        except (ValueError, UnicodeError):
            _fail('invalid_value', '$.checks')
        if completed.stdout or type(error) is not dict or set(error) != {'code','path','detail'} or error['code'] != spec['expected_error']:
            _fail('invalid_value', '$.checks')
        if Path(spec['forbidden_output']).exists() or Path(spec['forbidden_output']).is_symlink():
            _fail('invalid_value', '$.checks')
    elif spec['kind'] == 'status':
        try:
            status = json.loads(completed.stdout)
        except (ValueError, UnicodeError):
            _fail('invalid_value', '$.checks')
        if completed.stderr or type(status) is not dict or len(completed.stdout.splitlines()) != 1:
            _fail('invalid_value', '$.checks')
        output_flag = '-output' if spec['check_id'] == 'baseline' else '--out'
        destination = str(Path(spec['argv'][spec['argv'].index(output_flag)+1]).absolute())
        if spec['check_id'] in ('fixture','report'):
            expected = {'output_dir':destination, 'files':8 if spec['check_id']=='fixture' else 4}
        else:
            expected = {'status':'success','output':destination,'queries':7}
        if status != expected:
            _fail('invalid_value', '$.checks')
    return None


def _log(work, name, raw):
    directory = io._directory_fd(work / 'logs')
    try:
        fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
    finally:
        os.close(directory)


def _check(spec, root, work, counts):
    row = {'check_id':spec['check_id'], 'argv':spec['argv'], 'cwd':str(root),
           'exit_code':None, 'status':'not_run', 'evidence_path':None, 'evidence_sha256':None}
    error = None
    try:
        completed = _execute(spec['argv'], root, _environment())
        row['exit_code'] = completed.returncode
        row['status'] = 'failed'
        raw = b'STDOUT\n' + completed.stdout + b'\nSTDERR\n' + completed.stderr
        relative = 'logs/' + spec['check_id'] + '.log'
        _log(work, spec['check_id'] + '.log', raw)
        row.update(evidence_path=relative, evidence_sha256=io.sha256_bytes(raw), status='failed')
        measured = _judge(spec, completed)
        if measured is not None:
            counts[spec['check_id']] = measured
        row['status'] = 'passed'
    except io.ContractError as caught:
        error = caught.code
    except Exception:
        error = 'io_error'
    return row, error


def _format(value):
    if value is None:
        return ''
    return format(value, '.17g') if type(value) is float else str(value)


def _csv_rows(metric, contract, name):
    common = {key:metric[key] for key in ('run_id','policy_id','input_manifest_sha256','predictions_sha256')}
    if name == 'queries.csv':
        rows = [dict(common, **row) for row in metric['queries']]
    elif name == 'streams.csv':
        rows = []
        for row in metric['streams']:
            record = dict(common, **row)
            record.update(numerator=row['recall_at_k']['numerator'], denominator=row['recall_at_k']['denominator'],
                          recall_at_k=row['recall_at_k']['value'], null_reason=row['recall_at_k']['null_reason'])
            rows.append(record)
    else:
        rows = [dict(common, metric=key, **metric['summary'][key]) for key in contract['report_rules']['summary_order']]
    columns = contract['report_columns'][name]
    return [columns] + [[_format(row[key]) for key in columns] for row in rows]


def _verify_outputs(root, work, contract):
    if set(os.listdir(work/'fixture')) != set(_FILES) or set(os.listdir(work/'report')) != set(_REPORTS):
        _fail('invalid_value', '$.artifacts')
    evaluation = io.load_evaluation_bundle(work/'fixture/evaluation_manifest.json')
    inputs = evaluation['input_bundle']
    raw_predictions = io._read_regular(work/'predictions.json')
    prediction = io.decode_typed(raw_predictions, 'PredictionRun')
    if prediction['run_id'] != 'baseline-seed17':
        _fail('snapshot_binding')
    if {row['query_id'] for row in prediction['predictions']} != set(_SELECTIONS):
        _fail('schedule_mismatch')
    for row in prediction['predictions']:
        expected_refs = []
        for key in _SELECTIONS[row['query_id']]:
            origin, symbol = key.split(':')
            expected_refs.append({'origin':origin, 'collection':'memories', 'id':symbol, 'revision':1,
                                  'content_sha256':io.sha256_bytes(symbol.encode('utf-8'))})
        if row['status'] != 'success' or row['selected_refs'] != expected_refs:
            _fail('invalid_value', '$.predictions')
    bindings = {'input_manifest_sha256':inputs['manifest_sha256'], 'policy_sha256':inputs['policy_sha256'],
                'predictions_sha256':io.sha256_bytes(raw_predictions), 'evaluation_manifest_sha256':evaluation['manifest_sha256']}
    expected = metrics.calculate_metrics(prediction, inputs['probes'], evaluation['gold'], bindings)
    raw_metric = io._read_regular(work/'metrics.json')
    metric = io.decode_typed(raw_metric, 'MetricRun')
    if io.canonical_bytes('MetricRun', metric) != io.canonical_bytes('MetricRun', expected):
        _fail('hash_mismatch', '$.metrics')
    fixed_values = {'stream_macro_recall_at_k':1.0, 'query_weighted_recall_at_k':1.0,
                    'query_hit_any_rate':1.0, 'all_query_failure_rate':0.0,
                    'observed_negative_false_positive_rate':0.5, 'negative_service_failure_rate':0.5}
    if any(metric['summary'][name]['value'] != value for name, value in fixed_values.items()):
        _fail('invalid_value', '$.metrics')
    summary = io.decode_typed(io._read_regular(work/'report/summary.json'), 'ReportSummary')
    expected_summary = {key:metric[key] for key in ('contract_id','run_id','policy_id','input_manifest_sha256','predictions_sha256','evaluation_manifest_sha256','counts','summary')}
    expected_summary.update(metrics_sha256=io.sha256_bytes(raw_metric), dataset_role='unit_fixture', status='offline_contract_measurement')
    if io.canonical_bytes('ReportSummary', summary) != io.canonical_bytes('ReportSummary', expected_summary):
        _fail('hash_mismatch', '$.report')
    for name in ('queries.csv','streams.csv','summary.csv'):
        raw = io._read_regular(work/'report'/name)
        if raw.startswith(b'\xef\xbb\xbf') or not raw.endswith(b'\r\n') or b'\n' in raw.replace(b'\r\n',b''):
            _fail('invalid_value', '$.report')
        rows = list(csv.reader(text_io.StringIO(raw.decode('utf-8'), newline='')))
        if rows != _csv_rows(metric, contract, name):
            _fail('invalid_value', '$.report')
    return len(metric['queries'])



def _negative_plan(root, work):
    scripts = root/'evaluation/memory-harness-v1'
    negative = work/'negative'
    return [
        {'check_id':'reject_broken_hash','argv':[str(work/'baseline'),'-manifest',str(negative/'fixture/input_manifest.json'),
         '-run-id','negative-hash','-output',str(negative/'hash-output.json')], 'expected_error':'hash_mismatch',
         'forbidden_output':str(negative/'hash-output.json')},
        {'check_id':'reject_other_run_hash','argv':[sys.executable,str(scripts/'metrics.py'),'--evaluation-manifest',
         str(work/'fixture/evaluation_manifest.json'),'--predictions',str(negative/'wrong-hash.json'),
         '--out',str(negative/'wrong-metrics.json')], 'expected_error':'hash_mismatch','forbidden_output':str(negative/'wrong-metrics.json')},
        {'check_id':'reject_missing_query','argv':[sys.executable,str(scripts/'metrics.py'),'--evaluation-manifest',
         str(work/'fixture/evaluation_manifest.json'),'--predictions',str(negative/'missing-query.json'),
         '--out',str(negative/'missing-metrics.json')], 'expected_error':'schedule_mismatch','forbidden_output':str(negative/'missing-metrics.json')},
        {'check_id':'reject_gold_flag','argv':[str(work/'baseline'),'-manifest',str(work/'fixture/input_manifest.json'),
         '-run-id','negative-gold','-output',str(negative/'gold-output.json'),'-gold',str(work/'fixture/gold.json')],
         'expected_error':'invalid_value','forbidden_output':str(negative/'gold-output.json')},
    ]


def _prepare_negatives(work):
    negative = work/'negative'; negative.mkdir()
    (negative/'fixture').mkdir()
    for name in _FILES:
        raw = io._read_regular(work/'fixture'/name)
        if name == 'candidates.json':
            raw += b'\n'
        directory = io._directory_fd(negative/'fixture')
        try:
            fd = os.open(name, os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW, 0o600, dir_fd=directory)
            with os.fdopen(fd, 'wb') as stream:
                stream.write(raw)
        finally:
            os.close(directory)
    predictions = io.decode_typed(io._read_regular(work/'predictions.json'), 'PredictionRun')
    wrong = copy.deepcopy(predictions)
    wrong['input_manifest_sha256'] = '0'*64 if predictions['input_manifest_sha256'] != '0'*64 else '1'*64
    io.write_json_new(negative/'wrong-hash.json', wrong)
    missing = copy.deepcopy(predictions); missing['predictions'].pop()
    io.write_json_new(negative/'missing-query.json', missing)


def _artifacts(work, checks, include_acceptance=True):
    paths = ['fixture/'+name for name in _FILES] + ['baseline','predictions.json','metrics.json']
    paths += ['report/'+name for name in _REPORTS]
    paths += [row['evidence_path'] for row in checks if row['evidence_path'] is not None]
    if include_acceptance:
        paths.append('acceptance.json')
    result = []
    for relative in paths:
        _relative(relative)
        raw = io._read_regular(work/relative)
        result.append({'logical_name':relative, 'relative_path':relative,
                       'sha256':io.sha256_bytes(raw), 'byte_length':len(raw)})
    return result


def _validate_evidence(work, checks):
    for row in checks:
        if row['status'] == 'not_run':
            if any(row[key] is not None for key in ('exit_code','evidence_path','evidence_sha256')):
                _fail('invalid_value', '$.checks')
        elif (row['exit_code'] is None or row['evidence_path'] is None or row['evidence_sha256'] is None
              or io.sha256_bytes(io._read_regular(work / _relative(row['evidence_path']))) != row['evidence_sha256']):
            _fail('hash_mismatch', '$.checks')


def _bundle(work, repository, checks, status):
    # Missing required artifacts must suppress bundle publication entirely.
    _validate_evidence(work, checks)
    artifacts = _artifacts(work, checks)
    hashes = {row['relative_path']:row['sha256'] for row in artifacts}
    return {'bundle_version':'baseline-bundle-v1', 'contract_id':'arkism-offline-b0-v1', 'status':status,
            'review_status':'requires_independent_review', 'repository':repository,
            'contract_sha256':hashes['fixture/implementation_contract.json'],
            'policy_sha256':hashes['fixture/policy.json'], 'input_manifest_sha256':hashes['fixture/input_manifest.json'],
            'evaluation_manifest_sha256':hashes['fixture/evaluation_manifest.json'],
            'artifacts':artifacts, 'checks':checks, 'limitations':list(_LIMITATIONS)}


def _remove_owned(directory, name, owned):
    identity = owned.get(name)
    if identity is None:
        return True
    try:
        current = os.stat(name, dir_fd=directory, follow_symlinks=False)
        if not stat.S_ISREG(current.st_mode) or (current.st_dev, current.st_ino) != identity:
            return False
        os.unlink(name, dir_fd=directory)
    except FileNotFoundError:
        pass
    except OSError:
        return False
    owned.pop(name, None)
    return True


def _publish_json(directory, name, value, owned):
    """Publish exclusively; establish inode ownership before any public link."""
    try:
        raw = (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2, allow_nan=False)+'\n').encode('utf-8')
    except (TypeError, ValueError, UnicodeError):
        _fail('invalid_value')
    temporary = None
    try:
        for _ in range(10):
            candidate = '.arkism-acceptance-' + secrets.token_hex(16)
            try:
                fd = os.open(candidate, os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW, 0o600, dir_fd=directory)
                temporary = candidate
                break
            except FileExistsError:
                continue
        else:
            _fail('io_error')
        # Own the descriptor lifetime before metadata lookup can fail. The
        # stream does not close the raw fd; ExitStack always closes it, even if
        # fdopen, fstat, buffered I/O, or stream.close fails.
        with contextlib.ExitStack() as resources:
            resources.callback(os.close, fd)
            with os.fdopen(fd, 'wb', closefd=False) as stream:
                try:
                    info = os.fstat(stream.fileno())
                except OSError:
                    # One metadata recovery attempt is only for safe cleanup;
                    # the original failure still aborts publication.
                    try:
                        recovered = os.fstat(stream.fileno())
                    except OSError:
                        pass
                    else:
                        owned[temporary] = (recovered.st_dev, recovered.st_ino)
                    raise
                identity = (info.st_dev, info.st_ino)
                owned[temporary] = identity
                stream.write(raw)
                stream.flush()
                os.fsync(stream.fileno())
        current = os.stat(temporary, dir_fd=directory, follow_symlinks=False)
        if not stat.S_ISREG(current.st_mode) or (current.st_dev, current.st_ino) != identity:
            _fail('unsafe_path')
        os.link(temporary, name, src_dir_fd=directory, dst_dir_fd=directory, follow_symlinks=False)
        owned[name] = identity
        current = os.stat(name, dir_fd=directory, follow_symlinks=False)
        if not stat.S_ISREG(current.st_mode) or (current.st_dev, current.st_ino) != identity:
            _fail('unsafe_path')
    except FileExistsError:
        _fail('output_exists')
    except OSError as error:
        io._path_error(error)
    finally:
        if temporary is not None:
            unknown = temporary not in owned
            clean = not unknown and _remove_owned(directory, temporary, owned)
            if not clean:
                detail = _UNKNOWN_IDENTITY if unknown else 'owned temporary cleanup incomplete'
                active = sys.exc_info()[1]
                if isinstance(active, io.ContractError):
                    active.detail = detail
                elif active is None:
                    raise io.ContractError('io_error', '$', detail)


def _finalize(work, result, repository, checks, complete):
    directory = io._directory_fd(work)
    owned = {}
    try:
        try:
            _publish_json(directory, 'acceptance.json', result, owned)
            if complete:
                bundle = _bundle(work, repository, checks, result['status'])
                _publish_json(directory, 'baseline_bundle.json', bundle, owned)
        except Exception as error:
            original = error.code if isinstance(error, io.ContractError) else 'io_error'
            if original not in io._CONTRACT['error_codes']:
                original = 'io_error'
            unknown = isinstance(error, io.ContractError) and error.detail == _UNKNOWN_IDENTITY
            clean = not unknown
            for name in list(owned):
                clean = _remove_owned(directory, name, owned) and clean
            result['status'] = 'checks_failed'
            result['baseline_bundle'] = None
            if original not in result['errors']:
                result['errors'].append(original)
            if unknown and _UNKNOWN_IDENTITY not in result['limitations']:
                result['limitations'].append(_UNKNOWN_IDENTITY)
            try:
                _publish_json(directory, 'acceptance.json', result, owned)
            except Exception as recovery_error:
                unknown = unknown or (isinstance(recovery_error, io.ContractError)
                                      and recovery_error.detail == _UNKNOWN_IDENTITY)
                clean = not unknown and clean
                for name in list(owned):
                    clean = _remove_owned(directory, name, owned) and clean
                detail = 'failure diagnosis unavailable; original error: ' + original
                if unknown:
                    detail += '; ' + _UNKNOWN_IDENTITY
                elif not clean:
                    detail += '; competing output preserved or owned temporary cleanup incomplete'
                raise io.ContractError('io_error', '$', detail) from None
            if not clean:
                detail = _UNKNOWN_IDENTITY if unknown else 'competing output preserved or owned temporary cleanup incomplete'
                raise io.ContractError('io_error', '$', detail + '; original error: ' + original)
    finally:
        os.close(directory)


def run_acceptance(repo_root: Path, work_dir: Path) -> dict:
    """Execute the original build/binary protocol; tests must mock _execute.

    A failed upstream stage publishes acceptance.json diagnosis only when any
    required artifact is absent. It never pads hashes to create a bundle.
    """
    root = io._safe_absolute(repo_root)
    contract = _contract(root)
    before = _repository(root, contract)
    work = Path(work_dir)
    if work.name in ('', '.', '..') or '..' in work.parts:
        _fail('unsafe_path')
    work = io._safe_absolute(work.parent)/work.name
    try:
        work.mkdir(mode=0o700)
    except FileExistsError:
        _fail('output_exists')
    except OSError:
        _fail('io_error')
    (work/'logs').mkdir(mode=0o700)
    checks, counts, errors = [], {}, []
    plan = _plan(root, work)
    for spec in plan:
        if errors:
            row = {'check_id':spec['check_id'], 'argv':spec['argv'], 'cwd':str(root), 'exit_code':None,
                   'status':'not_run', 'evidence_path':None, 'evidence_sha256':None}
        else:
            row, error = _check(spec, root, work, counts)
            if error:
                errors.append(error)
        checks.append(row)
    if not errors:
        try:
            _verify_outputs(root, work, contract)
            _prepare_negatives(work)
        except io.ContractError as error:
            errors.append(error.code)
        except Exception:
            errors.append('io_error')
    negative_ready = not errors
    for spec in _negative_plan(root, work):
        spec.update(kind='negative', expected_exit=2)
        if negative_ready:
            row, error = _check(spec, root, work, counts)
            if error:
                errors.append(error)
        else:
            row = {'check_id':spec['check_id'], 'argv':spec['argv'], 'cwd':str(root), 'exit_code':None,
                   'status':'not_run', 'evidence_path':None, 'evidence_sha256':None}
        checks.append(row)
    if negative_ready:
        try:
            _verify_outputs(root, work, contract)
        except io.ContractError as error:
            errors.append(error.code)
        except Exception:
            errors.append('io_error')
    try:
        after = _repository(root, contract)
        if (before['observed_commit'] != after['observed_commit']
                or before['dirty_files_sha256'] != after['dirty_files_sha256']
                or _outside(before, contract) != _outside(after, contract)):
            errors.append('invalid_value')
    except Exception:
        after = before
        errors.append('io_error')
    complete = True
    try:
        _artifacts(work, checks, include_acceptance=False)
        _validate_evidence(work, checks)
    except io.ContractError as error:
        complete = False
        if error.code not in errors:
            errors.append(error.code)
    status = 'checks_failed' if errors else 'checks_passed'
    result = {'acceptance_version':'offline-b0-acceptance-v1', 'status':status, 'review_status':'requires_independent_review',
              'checks':checks, 'test_counts':counts, 'errors':errors, 'limitations':list(_LIMITATIONS),
              'go_environment':{'GOTOOLCHAIN':'local','GOPROXY':'off','GOSUMDB':'off'},
              'baseline_bundle':'baseline_bundle.json' if complete else None}
    _finalize(work, result, after, checks, complete)
    return dict(result, work_dir=str(work))


class _Parser(argparse.ArgumentParser):
    def error(self, message):
        _fail('invalid_value')


class _Once(argparse.Action):
    def __call__(self, parser, namespace, values, option_string=None):
        seen = vars(namespace).setdefault('_seen', set())
        if self.dest in seen:
            _fail('invalid_value')
        seen.add(self.dest); setattr(namespace, self.dest, values)


def main(args=None):
    parser = _Parser(description='Run the standalone offline acceptance protocol (includes go build).', allow_abbrev=False)
    for name in ('repo-root','work-dir'):
        parser.add_argument('--'+name, required=True, action=_Once)
    try:
        options = parser.parse_args(args)
        result = run_acceptance(Path(options.repo_root), Path(options.work_dir))
        print(json.dumps({'status':result['status'],'output':result['work_dir'],'checks':len(result['checks'])}, separators=(',',':')))
        if result['status'] != 'checks_passed':
            code = 'io_error' if 'io_error' in result['errors'] else 'invalid_value'
            print(json.dumps({'code':code,'path':'$.checks','detail':''}, separators=(',',':')), file=sys.stderr)
            return 1 if code == 'io_error' else 2
        return 0
    except io.ContractError as error:
        print(json.dumps({'code':error.code,'path':error.path,'detail':error.detail}, separators=(',',':')), file=sys.stderr)
        return 1 if error.code == 'io_error' else 2
    except SystemExit as error:
        return error.code
    except Exception:
        print(json.dumps({'code':'io_error','path':'$','detail':''}, separators=(',',':')), file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())

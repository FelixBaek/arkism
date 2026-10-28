"""Generate the eight-file offline unit fixture, with evaluator labels separated."""
import argparse
import errno
import json
import os
from pathlib import Path
import re
import secrets
import stat
import sys

import contract_io as io

_FILES = ('candidates.json', 'gold.json', 'implementation_contract.json',
          'policy.json', 'probes.json', 'split_map.json',
          'input_manifest.json', 'evaluation_manifest.json')
_CID = 'arkism-offline-b0-v1'
_POLICY_ID = 'b0-rrf-synthetic-v1'
_SPEC_PATH = Path(__file__).with_name('fixtures_spec.json')


def _fail(code, path='$', detail=''):
    raise io.ContractError(code, path, detail)


def _seed(value):
    if type(value) is not int:
        _fail('invalid_type', '$.seed')
    if value < 0 or value > (1 << 64) - 1:
        _fail('invalid_value', '$.seed')
    return value


def _ordered_ids(seed, item_ids):
    prefix = b'fixture-v0\n' + str(_seed(seed)).encode('ascii') + b'\n'
    return sorted(item_ids, key=lambda name: (io.sha256_bytes(prefix + name.encode('utf-8')), name))


def _ref(symbol, origin='memory'):
    return {'origin': origin, 'collection': 'memories', 'id': symbol,
            'revision': 1, 'content_sha256': io.sha256_bytes(symbol.encode('utf-8'))}


def _objects(seed, spec):
    """Build immutable snapshots; no ranking or metric implementation lives here."""
    cases = {case['query_id']: case for case in spec['cases']}
    probes, snapshots, labels = [], [], []
    for query_id in _ordered_ids(seed, cases):
        case = cases[query_id]
        scope = dict(spec['scope'])
        binding = {'query_id': query_id, 'stream_id': case['stream_id'],
                   'scope': scope, 'at_ns': spec['at_ns'],
                   'cutoff_accepted_seq': spec['cutoff_accepted_seq']}
        probes.append(dict(binding, candidate_snapshot_id='snap-' + query_id,
                           final_k=case['final_k'],
                           context_budget={'unit': 'fixture_units_v0', 'limit': case['budget']},
                           policy_id=_POLICY_ID))
        lanes = []
        for source_lane in case['lanes']:
            lane = []
            for source in source_lane:
                candidate = dict(spec['candidate_defaults'])
                candidate.update(scope=dict(scope), ref=_ref(source['symbol'], source.get('origin', 'memory')),
                                 reasons=list(source['reasons']))
                # Explicit allowed synthetic metadata only; no oracle/label fields.
                for field in spec['candidate_defaults']:
                    if field in source:
                        candidate[field] = source[field]
                lane.append(candidate)
            lanes.append(lane)
        snapshots.append({'snapshot_id': 'snap-' + query_id, 'stream_id': case['stream_id'],
                          'scope': dict(scope), 'as_of_ns': spec['at_ns'],
                          'as_of_seq': spec['cutoff_accepted_seq'], 'lanes': lanes})
        gold = case['gold']
        labels.append(dict(binding, temporal_kind='none', label_status=gold['label_status'],
                           relevant_refs=[_ref(symbol) for symbol in gold['symbols']]))
    policy = {'contract_id': _CID, 'policy_id': _POLICY_ID, 'rank_offset': 60,
              'exact_reason_weight': 2.0, 'other_weight': 1.0, 'context_unit': 'fixture_units_v0',
              'eligibility_order': 'validate-filter-preserve-ordinal-fuse-pack'}
    return {'policy.json': policy, 'probes.json': {'contract_id': _CID, 'probes': probes},
            'candidates.json': {'contract_id': _CID, 'snapshots': snapshots},
            'gold.json': {'contract_id': _CID, 'labels': labels},
            'split_map.json': {'contract_id': _CID, 'streams': [
                {'stream_id': stream, 'root_stream_id': stream, 'split_role': 'unit_fixture'}
                for stream in sorted({case['stream_id'] for case in cases.values()})]}}


def _identity(info):
    return info.st_dev, info.st_ino


def _track(directory, name, owned):
    info = os.stat(name, dir_fd=directory, follow_symlinks=False)
    if not stat.S_ISREG(info.st_mode):
        _fail('unsafe_path')
    owned[name] = _identity(info)


def _write_bytes(directory, name, data, owned):
    descriptor = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                         0o600, dir_fd=directory)
    owned[name] = _identity(os.fstat(descriptor))
    with os.fdopen(descriptor, 'wb') as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def _write_json(staging, descriptor, name, value, owned):
    io.write_json_new(staging / name, value)
    _track(descriptor, name, owned)


def _manifest(kind, names, staging, seed, spec):
    entries = []
    for name in sorted(names):
        data = io._read_regular(staging / name)
        entries.append({'logical_path': name, 'sha256': io.sha256_bytes(data), 'byte_length': len(data)})
    value = {'contract_id': _CID,
             'manifest_kind': 'baseline_input' if kind == 'InputManifest' else 'baseline_evaluation',
             'dataset_role': 'unit_fixture', 'files': entries}
    if kind == 'InputManifest':
        value.update(seed=seed, source_commit=spec['source_commit'])
    io.validate_typed(kind, value)
    return value


def _cleanup(parent, name, descriptor, identity, owned):
    """Delete only tracked inodes; never recurse through foreign/replaced content."""
    # mkdir can succeed even when the immediately following open fails (EMFILE).
    # Such an unopened reservation is empty and can be removed by parent/name
    # only after checking the identity captured immediately after mkdir.
    if descriptor is None:
        try:
            info = os.stat(name, dir_fd=parent, follow_symlinks=False)
            if identity is None or _identity(info) != identity or not stat.S_ISDIR(info.st_mode):
                return False
            os.rmdir(name, dir_fd=parent)  # Refuses any foreign contents.
            return True
        except OSError:
            return False
    if _identity(os.fstat(descriptor)) != identity:
        return False
    complete = True
    for basename, expected in owned.items():
        try:
            info = os.stat(basename, dir_fd=descriptor, follow_symlinks=False)
            if _identity(info) != expected or not stat.S_ISREG(info.st_mode):
                complete = False
                continue
            os.unlink(basename, dir_fd=descriptor)
        except FileNotFoundError:
            pass
        except OSError:
            complete = False
    try:
        info = os.stat(name, dir_fd=parent, follow_symlinks=False)
        if _identity(info) != identity or not stat.S_ISDIR(info.st_mode):
            return False
        if os.listdir(descriptor):
            return False
        os.rmdir(name, dir_fd=parent)
    except FileNotFoundError:
        return False
    except OSError:
        return False
    return complete


def _publish_file(source, destination, basename):
    os.link(basename, basename, src_dir_fd=source, dst_dir_fd=destination,
            follow_symlinks=False)


def generate_fixture(seed: int, output_dir: Path, contract_path: Path) -> dict:
    seed = _seed(seed)
    output = Path(output_dir)
    if output.name in ('', '.', '..') or '..' in output.parts or any('\x00' in part for part in output.parts):
        _fail('unsafe_path')
    parent_path = io._safe_absolute(output.parent)
    output = parent_path / output.name
    parent = io._directory_fd(parent_path)
    staging_name = None
    staging_fd = output_fd = None
    staging_identity = output_identity = None
    staged, published = {}, {}
    success = False
    failure = None
    result = None
    try:
        try:
            os.stat(output.name, dir_fd=parent, follow_symlinks=False)
        except FileNotFoundError:
            pass
        else:
            _fail('output_exists')
        contract_bytes = io._read_regular(contract_path)
        if io.sha256_bytes(contract_bytes) != io.CONTRACT_SHA256:
            _fail('hash_mismatch')
        spec = json.loads(_SPEC_PATH.read_bytes())
        if (spec['format'] != 'arkism-fixtures-spec-v1' or spec['data_role'] != 'unit_fixture'
                or spec['is_runner_input'] is not False or spec['contract_sha256'] != io.CONTRACT_SHA256):
            _fail('unsupported_version')
        # Use the already accepted R2 path/descriptor helpers; never discover inputs.
        for _ in range(10):
            candidate = '.arkism-harness-fixture-' + secrets.token_hex(16)
            try:
                os.mkdir(candidate, mode=0o700, dir_fd=parent)
            except FileExistsError:
                continue
            staging_name = candidate
            break
        else:
            _fail('io_error')
        staging_identity = _identity(os.stat(staging_name, dir_fd=parent, follow_symlinks=False))
        staging_fd = os.open(staging_name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        if _identity(os.fstat(staging_fd)) != staging_identity:
            _fail('unsafe_path')
        staging = parent_path / staging_name
        # The contract is copied as opaque original bytes, never reserialized.
        _write_bytes(staging_fd, 'implementation_contract.json', contract_bytes, staged)
        objects = _objects(seed, spec)
        for basename, value in objects.items():
            _write_json(staging, staging_fd, basename, value, staged)
        input_manifest = _manifest('InputManifest', ['implementation_contract.json', 'policy.json',
                                                   'probes.json', 'candidates.json'], staging, seed, spec)
        _write_json(staging, staging_fd, 'input_manifest.json', input_manifest, staged)
        evaluation_manifest = _manifest('EvaluationManifest', ['input_manifest.json', 'gold.json',
                                                             'split_map.json'], staging, seed, spec)
        _write_json(staging, staging_fd, 'evaluation_manifest.json', evaluation_manifest, staged)
        io.load_input_bundle(staging / 'input_manifest.json')
        io.load_evaluation_bundle(staging / 'evaluation_manifest.json')
        # mkdir exclusively reserves a new destination, preserving even empty dirs.
        # Completed individual files are published; whole-directory atomicity is
        # neither required nor claimed. Both manifests are exposed last.
        os.mkdir(output.name, mode=0o700, dir_fd=parent)
        output_identity = _identity(os.stat(output.name, dir_fd=parent, follow_symlinks=False))
        output_fd = os.open(output.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        if _identity(os.fstat(output_fd)) != output_identity:
            _fail('unsafe_path')
        for basename in _FILES:
            _publish_file(staging_fd, output_fd, basename)
            published[basename] = staged[basename]
        loaded_input = io.load_input_bundle(output / 'input_manifest.json')
        loaded_evaluation = io.load_evaluation_bundle(output / 'evaluation_manifest.json')
        result = {'output_dir': str(output), 'input_manifest_sha256': loaded_input['manifest_sha256'],
                  'evaluation_manifest_sha256': loaded_evaluation['manifest_sha256']}
        success = True
    except io.ContractError as error:
        failure = error
    except OSError as error:
        code = ('output_exists' if error.errno == errno.EEXIST else
                'unsafe_path' if error.errno in (errno.ELOOP, errno.ENOTDIR) else 'io_error')
        failure = io.ContractError(code, '$', '')
    except (ValueError, KeyError, TypeError):
        failure = io.ContractError('invalid_value', '$', '')
    finally:
        staging_clean = True
        if staging_name is not None:
            staging_clean = _cleanup(parent, staging_name, staging_fd, staging_identity, staged)
        if not staging_clean:
            # A cleanup failure is still a failed operation: retract our final
            # output as well, while retaining/reporting unsafe-to-remove staging.
            success = False
            if failure is None:
                failure = io.ContractError('io_error', '$', 'owned temporary artifacts remain')
            else:
                failure.detail = 'cleanup incomplete; inspect requested output parent'
        if not success and output_identity is not None:
            if not _cleanup(parent, output.name, output_fd, output_identity, published):
                if failure is None:
                    failure = io.ContractError('io_error', '$', 'cleanup incomplete; inspect requested output parent')
                else:
                    failure.detail = 'cleanup incomplete; inspect requested output parent'
        for descriptor in (output_fd, staging_fd, parent):
            if descriptor is not None:
                os.close(descriptor)
    if failure is not None:
        raise failure
    return result


class _Parser(argparse.ArgumentParser):
    def error(self, message):
        # argparse's default message can echo arbitrary raw argument contents.
        _fail('invalid_value')


class _Once(argparse.Action):
    def __call__(self, parser, namespace, values, option_string=None):
        seen = vars(namespace).setdefault('_seen', set())
        if self.dest in seen:
            _fail('invalid_value')
        seen.add(self.dest)
        setattr(namespace, self.dest, values)


def main(args=None):
    parser = _Parser(description='Generate the offline eight-file unit fixture.', allow_abbrev=False)
    for flag in ('seed', 'contract', 'out'):
        parser.add_argument('--' + flag, required=True, action=_Once)
    try:
        options = parser.parse_args(args)
        if re.fullmatch(r'0|[1-9][0-9]*', options.seed, re.ASCII) is None or len(options.seed) > 20:
            _fail('invalid_value', '$.seed')
        result = generate_fixture(int(options.seed), Path(options.out), Path(options.contract))
        print(json.dumps({'output_dir': result['output_dir'], 'files': 8}, sort_keys=True))
        return 0
    except io.ContractError as error:
        print(json.dumps({'code': error.code, 'path': error.path, 'detail': error.detail}, sort_keys=True), file=sys.stderr)
        return 1 if error.code == 'io_error' else 2
    except SystemExit as error:
        return error.code
    except Exception:
        print(json.dumps({'code': 'io_error', 'path': '$', 'detail': ''}), file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())

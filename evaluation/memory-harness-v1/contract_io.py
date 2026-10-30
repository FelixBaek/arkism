"""Strict, offline I/O for the single frozen arkism-offline-b0-v1 contract.

Byte hashes track files; typed canonical hashes track logical values. This module
never discovers datasets, consults environment variables, or reads product data.
"""

import errno
import hashlib
import json
import math
import os
from pathlib import Path
import re
import stat
import struct
import secrets

CONTRACT_SHA256 = '04463d838f397772743c5740a346230eb8e9db60db8dddc824d52e1f083790ef'
_CONTRACT_PATH = Path(__file__).parent / 'contracts' / 'implementation_contract.json'
_CONTRACT_BYTES = _CONTRACT_PATH.read_bytes()
if hashlib.sha256(_CONTRACT_BYTES).hexdigest() != CONTRACT_SHA256:
    raise RuntimeError('fixed contract hash mismatch')
_CONTRACT = json.loads(_CONTRACT_BYTES)
_TYPES = (
    'Scope', 'Ref', 'Candidate', 'CandidateSnapshot', 'ContextBudget', 'Probe',
    'Gold', 'Prediction', 'Policy', 'ProbeFile', 'CandidateFile', 'GoldFile',
    'ArtifactEntry', 'InputManifest', 'EvaluationManifest', 'SplitEntry',
    'SplitMap', 'RankedCandidate', 'BaselineSelection', 'PredictionRun',
    'Statistic', 'QueryMetric', 'StreamMetric', 'MetricCounts', 'MetricSummary',
    'MetricRun', 'ReportSummary',
)
assert tuple(_CONTRACT['objects']) == _TYPES
_OBJECTS = _CONTRACT['objects']
_INPUT_FILES = ['candidates.json', 'implementation_contract.json', 'policy.json', 'probes.json']
_EVALUATION_FILES = ['gold.json', 'input_manifest.json', 'split_map.json']
_REFERENCE_COMMIT = '27bb41807ff48d47559ba2cfc30feb300caacecf'
_ID = re.compile(r'[a-z][a-z0-9_-]{0,63}\Z', re.ASCII)
_SHA = re.compile(r'[0-9a-f]{64}\Z', re.ASCII)
_HEX40 = re.compile(r'[0-9a-f]{40}\Z', re.ASCII)


class ContractError(Exception):
    """A stable code and schema path; details never include input contents."""

    def __init__(self, code: str, path: str, detail: str = ''):
        self.code = code
        self.path = path
        self.detail = detail
        super().__init__(code + (': ' + path if path else ''))


def _fail(code, path='$'):
    raise ContractError(code, path, '')


def _supported(type_name):
    if type(type_name) is not str or type_name not in _TYPES:
        _fail('unsupported_type')


class _Pairs(list):
    pass


class _OverflowInteger:
    pass


def _parse_int(token):
    # Avoid the interpreter digit limit while retaining numeric-token identity.
    # Some 309-digit integers are finite binary64 (including 10**308).
    # Every integer with 310 or more digits exceeds finite binary64 magnitude.
    if len(token.lstrip('-')) > 309:
        return _OverflowInteger()
    return int(token)


# Match Go encoding/json's fixed maximum nesting depth. This is a parser
# resource boundary, not an additional field or research-schema restriction.
_MAX_JSON_DEPTH = 10000
_JSON_NUMBER = re.compile(r'-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?', re.ASCII)


def _parse_json(text):
    """Iterative container parsing avoids interpreter-dependent recursion caps.

    The standard library decodes individual strings and binary64 scalars; the
    explicit stack preserves object pairs for later global duplicate checks.
    """
    position = 0
    length = len(text)
    frames = []
    root = None
    root_set = False

    def scalar_or_container(start):
        if start >= length:
            _fail('invalid_json')
        char = text[start]
        if char in '{[':
            return (_Pairs() if char == '{' else []), start + 1, True
        if char == '"':
            try:
                value, end = json.decoder.scanstring(text, start + 1, True)
            except ValueError:
                _fail('invalid_json')
            return value, end, False
        for literal, value in (('true', True), ('false', False), ('null', None)):
            if text.startswith(literal, start):
                return value, start + len(literal), False
        match = _JSON_NUMBER.match(text, start)
        if match is None:
            _fail('invalid_json')
        token = match.group()
        value = float(token) if any(c in token for c in '.eE') else _parse_int(token)
        return value, match.end(), False

    while True:
        while position < length and text[position] in ' \t\r\n':
            position += 1
        if not frames:
            if root_set:
                if position != length:
                    _fail('invalid_json')
                return root
            root, position, container = scalar_or_container(position)
            root_set = True
            if container:
                frames.append([root, 'key_or_end' if isinstance(root, _Pairs) else 'value_or_end', None])
            continue
        frame = frames[-1]
        container, state, key = frame
        char = text[position] if position < length else ''
        is_object = isinstance(container, _Pairs)
        closing = '}' if is_object else ']'
        if state in ('key_or_end', 'value_or_end') and char == closing:
            position += 1
            frames.pop()
            continue
        if state in ('key_or_end', 'key'):
            if char != '"':
                _fail('invalid_json')
            try:
                frame[2], position = json.decoder.scanstring(text, position + 1, True)
            except ValueError:
                _fail('invalid_json')
            frame[1] = 'colon'
            continue
        if state == 'colon':
            if char != ':':
                _fail('invalid_json')
            position += 1
            frame[1] = 'value'
            continue
        if state == 'comma_or_end':
            if char == closing:
                position += 1
                frames.pop()
                continue
            if char != ',':
                _fail('invalid_json')
            position += 1
            frame[1] = 'key' if is_object else 'value'
            continue
        value, position, nested = scalar_or_container(position)
        if is_object:
            container.append((key, value))
        else:
            container.append(value)
        frame[1] = 'comma_or_end'
        if nested:
            if len(frames) >= _MAX_JSON_DEPTH:
                _fail('invalid_json')
            frames.append([value, 'key_or_end' if isinstance(value, _Pairs) else 'value_or_end', None])


def _unicode(value):
    pending = [value]
    visited = set()
    while pending:
        item = pending.pop()
        if isinstance(item, str):
            try:
                item.encode('utf-8', errors='strict')
            except UnicodeError:
                _fail('invalid_unicode')
        elif isinstance(item, (dict, list)):
            # Caller-owned typed values can alias containers; no recursion or
            # global interpreter settings are needed to inspect their strings.
            identity = id(item)
            if identity in visited:
                continue
            visited.add(identity)
            if isinstance(item, dict):
                pending.extend(item.keys())
                pending.extend(item.values())
            elif isinstance(item, _Pairs):
                for key, child in item:
                    pending.extend((key, child))
            else:
                pending.extend(item)


def _duplicates(value):
    pending = [value]
    while pending:
        item = pending.pop()
        if isinstance(item, _Pairs):
            keys = [key for key, _ in item]
            if len(set(keys)) != len(keys):
                _fail('duplicate_key')
            pending.extend(child for _, child in item)
        elif type(item) is list:
            pending.extend(item)


def _plain(value):
    holder = [None]
    pending = [(value, holder, 0)]
    while pending:
        source, parent, key = pending.pop()
        if isinstance(source, _Pairs):
            result = {}
            parent[key] = result
            pending.extend((child, result, name) for name, child in source)
        elif type(source) is list:
            result = [None] * len(source)
            parent[key] = result
            pending.extend((child, result, index) for index, child in enumerate(source))
        else:
            parent[key] = source
    return holder[0]


def _children(type_name, value, path):
    """Traverse only known schema paths, never echo an unknown input key."""
    if type(value) is not dict:
        return
    for name, spec in _OBJECTS[type_name]['fields'].items():
        if name not in value:
            continue
        item = value[name]
        child = path + '.' + name
        if spec['type'] == 'object':
            yield spec['object'], item, child
        elif spec['type'] == 'array' and 'items_object' in spec and type(item) is list:
            for element in item:
                yield spec['items_object'], element, child + '[*]'
        elif spec['type'] == 'array_of_arrays' and type(item) is list:
            for lane in item:
                if type(lane) is list:
                    for element in lane:
                        yield spec['items_object'], element, child + '[*][*]'


def _shape(type_name, value, path, stage):
    if type(value) is dict:
        fields = _OBJECTS[type_name]['fields']
        if stage == 'unknown' and any(type(k) is not str or k not in fields for k in value):
            _fail('unknown_field', path)
        if stage == 'missing':
            for key in fields:
                if key not in value:
                    _fail('missing_field', path + '.' + key)
        for nested_type, item, child_path in _children(type_name, value, path):
            _shape(nested_type, item, child_path, stage)


def _number(spec, value, path):
    kind = spec['type']
    if isinstance(value, _OverflowInteger):
        _fail('invalid_value', path)
    if kind.startswith('nullable_'):
        if value is None:
            return
        kind = kind[9:]
    if kind in ('int64', 'uint64', 'const_int'):
        if type(value) is not int:
            _fail('invalid_type', path)
        maximum = (1 << 64) - 1 if kind == 'uint64' else (1 << 63) - 1
        if value < spec.get('min', 0) or value > min(spec.get('max', maximum), maximum):
            _fail('invalid_value', path)
    else:
        if type(value) not in (int, float):
            _fail('invalid_type', path)
        try:
            value = float(value)
        except (ValueError, OverflowError):
            _fail('invalid_value', path)
        if not math.isfinite(value):
            _fail('invalid_value', path)
        if value < spec.get('min', -math.inf) or value > spec.get('max', math.inf):
            _fail('invalid_value', path)
    if kind.startswith('const_') and value != spec['value']:
        _fail('invalid_value', path)


def _field(spec, value, path):
    kind = spec['type']
    if kind in ('int64', 'uint64', 'const_int', 'float64', 'const_float', 'nullable_float64', 'nullable_int64'):
        _number(spec, value, path)
    elif kind == 'bool':
        if type(value) is not bool:
            _fail('invalid_type', path)
    elif kind == 'object':
        _typed_fields(spec['object'], value, path)
    elif kind in ('array', 'array_of_arrays'):
        if type(value) is not list:
            _fail('invalid_type', path)
        if kind == 'array_of_arrays':
            if len(value) != spec['outer_items']:
                _fail('invalid_value', path)
            for lane in value:
                if type(lane) is not list:
                    _fail('invalid_type', path + '[*]')
                for item in lane:
                    _typed_fields(spec['items_object'], item, path + '[*][*]')
            return
        if len(value) < spec.get('min_items', 0) or len(value) > spec.get('max_items', math.inf):
            _fail('invalid_value', path)
        for item in value:
            if 'items_object' in spec:
                _typed_fields(spec['items_object'], item, path + '[*]')
            elif 'items_enum' in spec:
                _field({'type': 'enum', 'values': spec['items_enum']}, item, path + '[*]')
            else:
                item_spec = {'type': spec['items_type']}
                for key in ('min', 'max'):
                    if 'item_' + key in spec:
                        item_spec[key] = spec['item_' + key]
                _field(item_spec, item, path + '[*]')
        if spec.get('unique'):
            for i, item in enumerate(value):
                if item in value[:i]:
                    _fail('invalid_value', path)
    else:
        if type(value) is not str:
            _fail('invalid_type', path)
        valid = True
        if kind == 'id':
            valid = _ID.fullmatch(value) is not None
        elif kind == 'sha256':
            valid = _SHA.fullmatch(value) is not None
        elif kind == 'hex40':
            valid = _HEX40.fullmatch(value) is not None
        elif kind == 'enum':
            valid = value in spec['values']
        elif kind == 'const_string':
            valid = value == spec['value']
        if not valid:
            _fail('unsupported_version' if path.endswith('.contract_id') else 'invalid_value', path)


def _typed_fields(type_name, value, path):
    if type(value) is not dict:
        _fail('invalid_type', path)
    for key, spec in _OBJECTS[type_name]['fields'].items():
        _field(spec, value[key], path + '.' + key)


def _unique(rows, key, path, code='invalid_value'):
    seen = set()
    for row in rows:
        if row[key] in seen:
            _fail(code, path)
        seen.add(row[key])


def _semantics(type_name, value, path):
    for nested_type, item, child_path in _children(type_name, value, path):
        _semantics(nested_type, item, child_path)
    if type_name == 'CandidateSnapshot':
        seen = {}
        keys = ('scope', 'ref', 'approved', 'active', 'deleted', 'source_verified',
                'context_cost_units', 'observed_seq', 'effective_at_ns')
        for lane in value['lanes']:
            for candidate in lane:
                if (candidate['scope'] != value['scope'] or candidate['observed_seq'] > value['as_of_seq']
                        or candidate['effective_at_ns'] > value['as_of_ns']
                        or candidate['updated_at_ns'] > value['as_of_ns']):
                    _fail('snapshot_conflict', path)
                ref = candidate['ref']
                key = (ref['origin'], ref['collection'], ref['id'])
                metadata = [candidate[k] for k in keys]
                if key in seen and seen[key] != metadata:
                    _fail('snapshot_conflict', path)
                seen[key] = metadata
    elif type_name == 'Gold':
        if bool(value['relevant_refs']) != (value['label_status'] == 'relevant'):
            _fail('invalid_value', path + '.relevant_refs')
    elif type_name == 'Prediction':
        if value['error_code'] != {'success': 'none', 'timeout': 'timeout', 'error': 'execution_error'}[value['status']]:
            _fail('invalid_value', path + '.error_code')
    elif type_name == 'ProbeFile':
        _unique(value['probes'], 'query_id', path + '.probes', 'schedule_mismatch')
        streams = {}
        for probe in value['probes']:
            stream = probe['stream_id']
            if stream in streams and streams[stream] != probe['scope']:
                _fail('snapshot_binding', path + '.probes[*].scope')
            streams[stream] = probe['scope']
    elif type_name == 'CandidateFile':
        _unique(value['snapshots'], 'snapshot_id', path + '.snapshots', 'schedule_mismatch')
    elif type_name in ('InputManifest', 'EvaluationManifest'):
        expected = _INPUT_FILES if type_name == 'InputManifest' else _EVALUATION_FILES
        if [entry['logical_path'] for entry in value['files']] != expected:
            _fail('invalid_value', path + '.files')
        if type_name == 'InputManifest' and value['source_commit'] != _REFERENCE_COMMIT:
            _fail('invalid_value', path + '.source_commit')
    elif type_name == 'SplitMap':
        names = [row['stream_id'] for row in value['streams']]
        if names != sorted(set(names)):
            _fail('invalid_value', path + '.streams')
    elif type_name == 'Statistic':
        denominator = value['denominator']
        if denominator == 0:
            if value['numerator'] != 0 or value['value'] is not None or value['null_reason'] != 'no_denominator':
                _fail('invalid_value', path)
        elif (value['value'] is None or value['null_reason'] != 'none'
              or abs(value['value'] - value['numerator'] / denominator) > 1e-12 + 1e-10 * abs(value['numerator'] / denominator)):
            _fail('invalid_value', path)
    elif type_name == 'QueryMetric':
        if value['label_status'] != 'relevant':
            if value['matched_count'] != 0 or value['recall_at_k'] is not None or value['hit_any'] is not None:
                _fail('invalid_value', path)
        elif value['status'] != 'success':
            if value['matched_count'] != 0 or value['recall_at_k'] != 0 or value['hit_any'] != 0:
                _fail('invalid_value', path)


def validate_typed(type_name: str, value: dict) -> None:
    _supported(type_name)
    _unicode(value)
    _shape(type_name, value, '$', 'unknown')
    _shape(type_name, value, '$', 'missing')
    _typed_fields(type_name, value, '$')
    _semantics(type_name, value, '$')


def _normalize(type_name, value):
    result = {}
    for key, spec in _OBJECTS[type_name]['fields'].items():
        item = value[key]
        kind = spec['type']
        if kind in ('float64', 'const_float', 'nullable_float64') and item is not None:
            item = float(item)
            if item == 0:
                item = 0.0
        elif kind == 'object':
            item = _normalize(spec['object'], item)
        elif kind == 'array' and 'items_object' in spec:
            item = [_normalize(spec['items_object'], x) for x in item]
        elif kind == 'array_of_arrays':
            item = [[_normalize(spec['items_object'], x) for x in lane] for lane in item]
        elif kind == 'array':
            item = list(item)
        result[key] = item
    return result


def decode_typed(data: bytes, type_name: str) -> dict:
    _supported(type_name)
    if type(data) is not bytes:
        _fail('invalid_type')
    try:
        text = data.decode('utf-8', errors='strict')
    except UnicodeError:
        _fail('invalid_unicode')
    value = _parse_json(text)
    _unicode(value)
    _duplicates(value)
    value = _plain(value)
    validate_typed(type_name, value)
    return _normalize(type_name, value)


def _c14n_field(spec, value):
    kind = spec['type']
    if value is None:
        return b'n'
    if kind == 'bool':
        return b't' if value else b'f'
    if kind in ('int64', 'uint64', 'const_int', 'nullable_int64'):
        return b'i' + str(value).encode('ascii') + b';'
    if kind in ('float64', 'const_float', 'nullable_float64'):
        number = float(value)
        return b'd' + struct.pack('>d', 0.0 if number == 0 else number).hex().encode('ascii') + b';'
    if kind == 'object':
        return _c14n_object(spec['object'], value)
    if kind == 'array_of_arrays':
        arrays = [_c14n_array([_c14n_object(spec['items_object'], x) for x in lane]) for lane in value]
        return _c14n_array(arrays)
    if kind == 'array':
        if 'items_object' in spec:
            items = [_c14n_object(spec['items_object'], x) for x in value]
        else:
            item_spec = {'type': spec.get('items_type', 'enum')}
            items = [_c14n_field(item_spec, x) for x in value]
        return _c14n_array(items)
    return _c14n_string(value)


def _c14n_string(value):
    encoded = value.encode('utf-8')
    return b's' + str(len(encoded)).encode('ascii') + b':' + encoded


def _c14n_array(items):
    return b'a' + str(len(items)).encode('ascii') + b'[' + b''.join(items) + b']'


def _c14n_object(type_name, value):
    fields = _OBJECTS[type_name]['fields']
    return b'o' + str(len(fields)).encode('ascii') + b'{' + b''.join(
        _c14n_string(key) + _c14n_field(fields[key], value[key]) for key in sorted(fields)
    ) + b'}'


def canonical_bytes(type_name: str, value: dict) -> bytes:
    validate_typed(type_name, value)
    return _c14n_object(type_name, value)


def sha256_bytes(data: bytes) -> str:
    if type(data) is not bytes:
        _fail('invalid_type')
    return hashlib.sha256(data).hexdigest()


def canonical_sha256(type_name: str, value: dict) -> str:
    return sha256_bytes(canonical_bytes(type_name, value))


def _safe_absolute(path):
    path = Path(path)
    if '..' in path.parts or any('\x00' in part for part in path.parts):
        _fail('unsafe_path')
    # abspath anchors explicitly supplied relative paths; it never expands ~ or env.
    path = Path(os.path.abspath(path))
    cursor = Path(path.anchor)
    for component in path.parts[1:]:
        cursor = cursor / component
        try:
            mode = cursor.lstat().st_mode
        except FileNotFoundError:
            _fail('io_error')
        except OSError:
            _fail('io_error')
        if stat.S_ISLNK(mode):
            _fail('unsafe_path')
    return path


def _path_error(error):
    _fail('unsafe_path' if error.errno in (errno.ELOOP, errno.ENOTDIR) else 'io_error')


def _directory_fd(path):
    """Open each directory without following links, pinning the resulting inode."""
    descriptor = None
    try:
        descriptor = os.open(path.anchor, os.O_RDONLY | os.O_DIRECTORY)
        for component in path.parts[1:]:
            child = os.open(component, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=descriptor)
            os.close(descriptor)
            descriptor = child
        return descriptor
    except OSError as error:
        if descriptor is not None:
            os.close(descriptor)
        _path_error(error)


def _read_regular(path):
    path = _safe_absolute(path)
    directory = _directory_fd(path.parent)
    try:
        mode = os.stat(path.name, dir_fd=directory, follow_symlinks=False).st_mode
        if not stat.S_ISREG(mode):
            _fail('unsafe_path')
        fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
        with os.fdopen(fd, 'rb') as stream:
            if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                _fail('unsafe_path')
            return stream.read()
    except OSError as error:
        _path_error(error)
    finally:
        os.close(directory)


def _verified_files(manifest, root):
    result = {}
    for entry in manifest['files']:
        name = entry['logical_path']
        data = _read_regular(root / name)
        if len(data) != entry['byte_length'] or sha256_bytes(data) != entry['sha256']:
            _fail('hash_mismatch', '$.files[*]')
        result[name] = data
    return result


def _baseline(probes, candidates, policy):
    snapshots = {snapshot['snapshot_id']: snapshot for snapshot in candidates['snapshots']}
    requested = {probe['candidate_snapshot_id'] for probe in probes['probes']}
    if requested != set(snapshots):
        _fail('schedule_mismatch')
    for probe in probes['probes']:
        snapshot = snapshots[probe['candidate_snapshot_id']]
        if (snapshot['stream_id'] != probe['stream_id'] or snapshot['scope'] != probe['scope']
                or snapshot['as_of_ns'] != probe['at_ns']
                or snapshot['as_of_seq'] != probe['cutoff_accepted_seq']
                or probe['policy_id'] != policy['policy_id']):
            _fail('snapshot_binding', '$.probes[*]')


def _input_from_bytes(path, data):
    manifest = decode_typed(data, 'InputManifest')
    raw = _verified_files(manifest, path.parent)
    if sha256_bytes(raw['implementation_contract.json']) != CONTRACT_SHA256:
        _fail('hash_mismatch', '$.files[*]')
    # Fixed argument order matches Go ValidateBaselineInputs for invalid bundles.
    probes = decode_typed(raw['probes.json'], 'ProbeFile')
    candidates = decode_typed(raw['candidates.json'], 'CandidateFile')
    policy = decode_typed(raw['policy.json'], 'Policy')
    _baseline(probes, candidates, policy)
    return {'manifest': manifest, 'manifest_sha256': sha256_bytes(data),
            'policy': policy, 'policy_sha256': sha256_bytes(raw['policy.json']),
            'probes': probes, 'candidates': candidates, 'contract_sha256': CONTRACT_SHA256}


def load_input_bundle(manifest_path: Path) -> dict:
    path = _safe_absolute(manifest_path)
    return _input_from_bytes(path, _read_regular(path))


def load_evaluation_bundle(manifest_path: Path) -> dict:
    path = _safe_absolute(manifest_path)
    data = _read_regular(path)
    manifest = decode_typed(data, 'EvaluationManifest')
    raw = _verified_files(manifest, path.parent)
    input_bundle = _input_from_bytes(path.parent / 'input_manifest.json', raw['input_manifest.json'])
    gold = decode_typed(raw['gold.json'], 'GoldFile')
    split_map = decode_typed(raw['split_map.json'], 'SplitMap')
    probes = input_bundle['probes']['probes']
    labels = gold['labels']
    _unique(labels, 'query_id', '$.labels', 'schedule_mismatch')
    by_query = {probe['query_id']: probe for probe in probes}
    if set(by_query) != {row['query_id'] for row in labels}:
        _fail('schedule_mismatch', '$.labels')
    for row in labels:
        probe = by_query[row['query_id']]
        if any(row[key] != probe[key] for key in ('stream_id', 'scope', 'at_ns', 'cutoff_accepted_seq')):
            _fail('snapshot_binding', '$.labels[*]')
    if {row['stream_id'] for row in split_map['streams']} != {probe['stream_id'] for probe in probes}:
        _fail('schedule_mismatch', '$.streams')
    return {'input_bundle': input_bundle, 'manifest': manifest,
            'manifest_sha256': sha256_bytes(data), 'gold': gold, 'split_map': split_map}


def write_json_new(path: Path, value: dict) -> None:
    path = Path(path)
    name = os.path.basename(os.fspath(path))
    if '..' in path.parts or any('\x00' in part for part in path.parts) or name in ('', '.', '..') or name != path.name:
        _fail('unsafe_path')
    parent = _safe_absolute(path.parent)
    if type(value) is not dict:
        _fail('invalid_type')
    _unicode(value)
    try:
        data = (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2, allow_nan=False) + '\n').encode('utf-8')
    except (TypeError, ValueError, OverflowError, RecursionError):
        _fail('invalid_value')
    directory = _directory_fd(parent)
    temporary = None
    try:
        try:
            os.stat(name, dir_fd=directory, follow_symlinks=False)
        except FileNotFoundError:
            pass
        else:
            _fail('output_exists')
        for _ in range(10):
            candidate = '.arkism-harness-' + secrets.token_hex(16)
            try:
                fd = os.open(candidate, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory)
            except FileExistsError:
                continue
            temporary = candidate
            break
        else:
            _fail('io_error')
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        # The completed inode becomes visible atomically, never replacing a name.
        os.link(temporary, name, src_dir_fd=directory, dst_dir_fd=directory, follow_symlinks=False)
    except FileExistsError:
        _fail('output_exists')
    except OSError as error:
        _path_error(error)
    finally:
        if temporary is not None:
            try:
                os.unlink(temporary, dir_fd=directory)
            except OSError:
                # Publication succeeded or already failed; cleanup cannot change it.
                # A leftover hidden temporary is never a completed output filename.
                pass
        os.close(directory)

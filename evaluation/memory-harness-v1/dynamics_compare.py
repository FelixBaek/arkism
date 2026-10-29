"""DYN-05.p1 declared-binding comparison; no gold, scoring or REF selection.

The nine checks have the evidence scope fixed by native parent b7598e03.
Only explicitly supplied run/registry artifacts and pinned codec dependencies
are read. Commitment equality does not reconstruct source bodies or authority.
"""
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import stat
import struct

HERE = Path(__file__).absolute().parent
MAX_BYTES, MAX_ARRAY, MAX_ROWS, MAX_INT = 67108864, 200000, 100000, 9223372036854775807
CODEC_SHA = 'd575bf82f5d1088883fddf5191e97a60a2260cc46a37051af9a89cdd5b7aa74e'


class ContractErrorV2(Exception):
    def __init__(self, code, path='$'):
        self.code, self.path = code, path
        super().__init__(code + ': ' + path)


def fail(code, path='$'):
    raise ContractErrorV2(code, path) from None


def require(condition, code='invalid_value', path='$'):
    if not condition:
        fail(code, path)


def _codec():
    path = HERE / 'contract_io.py'
    try:
        raw = path.read_bytes()
        require(hashlib.sha256(raw).hexdigest() == CODEC_SHA, 'hash_mismatch')
        spec = importlib.util.spec_from_file_location('_dyn05_fixed_baseline_codec', path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module
    except ContractErrorV2:
        raise
    except Exception:
        fail('io_error')


IO = _codec()

SCHEMA_SHA = '952d663b76254e15a3a65ab09dcd580504bc4c2814ce4903a8883ece1ca8f0da'
SCHEMA_PATH = HERE.parents[1] / 'docs/research/memory-harness/dynamics_policy_contract.json'
_SCHEMA_NAMES = ('ContractErrorV2', 'DynamicsConfigV2', 'PolicyDecisionV2', 'PolicyRegistryV2', 'PredictionRunV2', 'PredictionV2', 'RankerConfigV2', 'RefV2', 'ScopeV2', 'AuxRank', 'CompareResult', 'ComparedStatus', 'ComparisonCheck', 'OfflineCost', 'PolicyEntry', 'RankLane', 'RankRow', 'ResourceLimits', 'DynamicsConfigV2HashProjection', 'PolicyDecisionV2HashProjection', 'PolicyRegistryGraphProjectionV2', 'PredictionRunV2HashProjection', 'RankerConfigParametersProjectionV2', 'RegistryBoundRankerProjectionV2', 'RegistryPolicyEntryProjectionV2')
_ENUM_NAMES = ('AttemptDisposition', 'Attribution', 'Dimension', 'ErrorCode', 'EventKind', 'FallbackMode', 'InputRole', 'Outcome', 'OutputRole', 'OutputStatus', 'PolicyID', 'Reason', 'ReceiptDisposition', 'V2ErrorCode')
_PRIMITIVE_NAMES = ('Bool', 'Count', 'FinalK', 'LegacyContextBudget', 'LegacyID', 'NS', 'NonnegativeFloat', 'PositiveInt64', 'PositiveNS', 'SHA', 'SafePath', 'SchemaPath', 'Seq', 'Seq0', 'Tolerance', 'UnitFloat')


def _schema():
    """Reuse exact immutable type declarations instead of maintaining a second copy.

    This fixed local schema is a pinned codec dependency, never a run-selected path
    or hidden input snapshot. No user artifact controls the filename or its hash.
    """
    try:
        data = IO._read_regular(SCHEMA_PATH)
    except Exception:
        fail('io_error')
    require(hashlib.sha256(data).hexdigest() == SCHEMA_SHA, 'hash_mismatch')
    contract = json.loads(data)
    types = {**contract['models'], **contract['closed_inline_records'], **contract['hash_projections']}
    return ({name: types[name] for name in _SCHEMA_NAMES},
            {name: contract['enums'][name] for name in _ENUM_NAMES},
            {name: contract['primitives'][name] for name in _PRIMITIVE_NAMES})


MODELS, ENUMS, PRIMITIVES = _schema()
SELF_HASHES = {'DynamicInputManifestV2': {'field': 'manifest_sha256', 'projection': 'DynamicInputManifestV2HashProjection'}, 'DynamicsConfigV2': {'field': 'config_sha256', 'projection': 'DynamicsConfigV2HashProjection'}, 'DynamicsRunConfigV2': {'field': 'config_sha256', 'projection': 'DynamicsRunConfigV2HashProjection'}, 'EvidenceBinding': {'field': 'binding_sha256', 'projection': 'EvidenceBindingHashProjection'}, 'ImportanceFeatureV1': {'field': 'feature_sha256', 'projection': 'ImportanceFeatureV1HashProjection'}, 'LifecycleFence': {'field': 'fence_sha256', 'projection': 'LifecycleFenceHashProjection'}, 'P1InputManifestV1': {'field': 'manifest_sha256', 'projection': 'P1InputManifestV1HashProjection'}, 'PolicyDecisionV2': {'field': 'decision_sha256', 'projection': 'PolicyDecisionV2HashProjection'}, 'PredictionRunV2': {'field': 'run_sha256', 'projection': 'PredictionRunV2HashProjection'}, 'ReceiptIndexV2': {'field': 'index_sha256', 'projection': 'ReceiptIndexV2HashProjection'}, 'ReceiptV2': {'field': 'receipt_sha256', 'projection': 'ReceiptV2HashProjection'}, 'SnapshotV2': {'field': 'snapshot_sha256', 'projection': 'SnapshotV2HashProjection'}, 'StateV2': {'field': 'state_sha256', 'projection': 'StateV2HashProjection'}, 'TransitionV2': {'field': 'transition_sha256', 'projection': 'TransitionV2HashProjection'}}

NATIVE_PATH_FIELDS = ('ablation', 'accepted_at_ns', 'accepted_seq', 'activated_at_ns', 'activation_accepted_seq', 'activation_event_id', 'activation_payload_sha256', 'active', 'after_state_revision', 'agent', 'allowed_roles', 'applied_count', 'approved', 'arrived_at_ns', 'as_of_ns', 'as_of_seq', 'at_ns', 'atol', 'attempt_disposition', 'attribution', 'authority', 'auxiliary', 'available_accepted_seq', 'available_at_ns', 'base_dense_rank', 'base_dynamic_manifest_sha256', 'base_equal_rank', 'base_rrf_score', 'baseline_bundle_sha256', 'before_state_revision', 'before_state_sha256', 'binding_sha256', 'candidate', 'candidate_snapshot_id', 'candidate_snapshot_sha256', 'checker_sha256', 'checks', 'claim_sha256', 'clock', 'clock_domain', 'code', 'collection', 'config_sha256', 'configs', 'content_sha256', 'context_budget', 'context_cost_units', 'context_cost_used', 'context_unit', 'contract_id', 'contract_sha256', 'cost', 'cursor', 'cutoff_accepted_seq', 'dataset_id', 'decay_enabled', 'decision', 'decision_sha256', 'deleted', 'dense_rank', 'dimension', 'disposition', 'duplicate_event_count', 'duplicate_events', 'duplicate_evidence', 'duplicate_evidence_count', 'duplicate_of_receipt_sha256', 'dynamics', 'dynamics_config_sha256', 'effective_at_ns', 'elapsed_wall_ns', 'eligibility_order', 'eligible_unique_count', 'entries', 'epoch', 'equal', 'equal_conditions', 'error', 'eta_negative', 'eta_positive', 'event_id', 'event_inputs', 'events', 'evidence', 'evidence_consumption_sha256', 'evidence_consumptions', 'evidence_refs', 'evidence_rule_sha256', 'exact_reason_weight', 'failed', 'failed_predictions', 'failed_rows', 'fallback', 'fallback_predictions', 'feature_contract_sha256', 'feature_equal_rank', 'feature_file', 'feature_id', 'feature_name', 'feature_reads', 'feature_sha256', 'feature_version', 'features', 'fence_sha256', 'files', 'final_k', 'final_ordinal', 'final_tie', 'first_event_id', 'first_receipt_sha256', 'fusion_score', 'generator_config_sha256', 'id', 'identity_sha256', 'implemented_mode', 'independence', 'index_sha256', 'inherited_b2_config_sha256', 'initial', 'input_count', 'input_manifest_sha256', 'input_refs', 'key', 'kind', 'lanes', 'last_applied_at_ns', 'last_applied_seq', 'lifecycle_fence', 'manifest_sha256', 'max_array_items', 'max_events', 'max_features', 'max_file_bytes', 'max_json_depth', 'max_probes', 'max_receipts', 'max_states', 'max_string_bytes', 'minimum_accepted_seq', 'missing_feature', 'missing_reason', 'missing_state', 'mode', 'name', 'namespace', 'negative_policy', 'non_learning_count', 'observed_at_ns', 'observed_seq', 'ok', 'origin', 'other_weight', 'outcome', 'outcome_jump_enabled', 'outer_offset', 'outputs', 'parameter_provenance', 'path', 'payload_sha256', 'planned', 'planned_rows', 'policy_contract_sha256', 'policy_id', 'policy_ids', 'policy_version', 'predictions', 'prefix_sha256', 'previous_prefix_sha256', 'probes', 'producer_version', 'project', 'protocol', 'provider_usd', 'qualification', 'query_id', 'rank_offset', 'ranker', 'ranker_config_sha256', 'ranks', 'raw_sha256', 'reason', 'reasons', 'receipt', 'receipt_sha256', 'receipts', 'recency_anchor', 'recency_tau_ns', 'ref', 'refs', 'registry', 'registry_sha256', 'rejected_count', 'relative_path', 'resource_limits', 'revision', 'role', 'rtol', 'rule_sha256', 'run_config_raw_sha256', 'run_config_sha256', 'run_id', 'run_ids', 'run_ref', 'run_sha256', 'scope', 'score', 'selected', 'size_bytes', 'snapshot', 'snapshot_cursor', 'snapshot_id', 'snapshot_sha256', 'snapshots', 'source', 'source_refs', 'source_state_revision', 'source_status', 'source_verified', 'state', 'state_revision', 'state_sha256', 'state_updates', 'states', 'status', 'statuses', 'stream_id', 'strength', 'target', 'task', 'task_ref', 'task_sha256', 'tau_ns', 'transition_sha256', 'transport_attempt_id', 'trial_ref', 'trust', 'unique_events', 'unit', 'unit_strength', 'updated_at_ns', 'used_context_units', 'user', 'uses_elapsed_decay', 'uses_outcomes', 'value', 'version', 'weight', 'workspace')


def external(call, *args):
    try:
        return call(*args)
    except IO.ContractError as error:
        code = error.code if error.code in ENUMS['V2ErrorCode'] else 'io_error'
        fail(code, error.path if known_path(error.path) else '$')
    except ContractErrorV2:
        raise
    except Exception:
        fail('io_error')


def known_path(value):
    if type(value) is not str or not re.fullmatch(r'\$(?:\.[a-z][a-z0-9_]*|\[\*\])*', value, re.ASCII):
        return False
    return all(token in NATIVE_PATH_FIELDS for token in re.findall(r'\.([a-z][a-z0-9_]*)', value))


def _depth(text):
    depth, quoted, escaped = 0, False, False
    for char in text:
        if quoted:
            if escaped:
                escaped = False
            elif char == '\\':
                escaped = True
            elif char == '"':
                quoted = False
        elif char == '"':
            quoted = True
        elif char in '[{':
            depth += 1
            require(depth <= 32, 'resource_limit')
        elif char in ']}':
            depth -= 1


def parse(raw):
    require(type(raw) is bytes, 'invalid_type')
    require(len(raw) <= MAX_BYTES, 'resource_limit')
    try:
        text = raw.decode('utf8')
    except UnicodeError:
        fail('invalid_unicode')
    tree = external(IO._parse_json, text)
    external(IO._unicode, tree)
    _depth(text)
    external(IO._duplicates, tree)
    pending = [tree]
    while pending:
        value = pending.pop()
        if isinstance(value, IO._Pairs):
            for key, item in value:
                pending.extend((key, item))
        elif type(value) is list:
            require(len(value) <= MAX_ARRAY, 'resource_limit')
            pending.extend(value)
        elif isinstance(value, IO._OverflowInteger):
            fail('invalid_value')
        elif type(value) is str:
            require(len(value.encode('utf8')) <= 4096, 'resource_limit')
        elif type(value) is float:
            require(math.isfinite(value))
    return external(IO._plain, tree)


def read(path):
    path = external(IO._safe_absolute, path)
    parent = external(IO._directory_fd, path.parent)
    try:
        before = os.stat(path.name, dir_fd=parent, follow_symlinks=False)
        require(stat.S_ISREG(before.st_mode), 'unsafe_path')
        fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        with os.fdopen(fd, 'rb') as stream:
            info = os.fstat(stream.fileno())
            require(stat.S_ISREG(info.st_mode) and (info.st_dev, info.st_ino) == (before.st_dev, before.st_ino), 'unsafe_path')
            raw = stream.read(MAX_BYTES + 1)
        require(len(raw) <= MAX_BYTES, 'resource_limit')
        return raw
    except OSError:
        fail('io_error')
    finally:
        os.close(parent)


def _value_code(field):
    if field.get('ref') == 'PolicyID':
        return 'unsupported_policy'
    if field.get('const') == 'error':
        return 'unsupported_fallback'
    if field.get('const') == 'scalar':
        return 'unsupported_mode'
    constant = field.get('const')
    if type(constant) is str and constant.startswith(('dynamics-', 'p1-')):
        return 'unsupported_protocol'
    return 'invalid_value'


def _primitive(kind, value, path):
    if kind in ENUMS:
        require(type(value) is str, 'invalid_type', path)
        require(value in ENUMS[kind], 'unsupported_policy' if kind == 'PolicyID' else 'invalid_value', path)
        return
    if kind == 'LegacyContextBudget':
        external(IO.validate_typed, 'ContextBudget', value)
        return
    if kind == 'SchemaPath':
        require(known_path(value), 'invalid_value', path)
        return
    if kind in ('SHA', 'LegacyID', 'SafePath'):
        require(type(value) is str, 'invalid_type', path)
        patterns = {'SHA': r'[0-9a-f]{64}', 'LegacyID': r'[a-z][a-z0-9_-]{0,63}'}
        if kind in patterns:
            require(re.fullmatch(patterns[kind], value, re.ASCII) is not None, 'invalid_value', path)
        else:
            require(bool(value) and not value.startswith('/') and '\\' not in value and '\0' not in value and all(p not in ('', '.', '..') for p in value.split('/')), 'unsafe_path', path)
        return
    rule = PRIMITIVES[kind]
    if rule['type'] == 'bool':
        require(type(value) is bool, 'invalid_type', path)
        return
    if rule['type'] == 'int64':
        require(type(value) is int, 'invalid_type', path)
    else:
        require(type(value) in (int, float), 'invalid_type', path)
        require(math.isfinite(value), 'invalid_value', path)
    require(rule['minimum'] <= value <= rule['maximum'], 'invalid_value', path)


def _field(field, value, path):
    if value is None:
        require(field.get('nullable', False), 'invalid_type', path)
        return
    kind = field['type']
    if kind in ('ref', 'projection_ref'):
        _typed(field['ref'], value, path)
    elif kind == 'array':
        require(type(value) is list, 'invalid_type', path)
        cap = field.get('max_items', MAX_ARRAY)
        require(len(value) <= (cap if type(cap) is int else MAX_ARRAY), 'resource_limit', path)
        for item in value:
            _field(field['items'], item, path + '[*]')
    elif kind == 'string':
        require(type(value) is str, 'invalid_type', path)
    elif kind == 'int64':
        require(type(value) is int, 'invalid_type', path)
    else:
        fail('invalid_type', path)
    if 'const' in field:
        require(value == field['const'], _value_code(field), path)
    if 'enum' in field:
        require(value in field['enum'], 'unsupported_mode' if path.endswith(('.mode', '.implemented_mode')) else 'invalid_value', path)


def _shape(kind, value, unknown, path='$'):
    if kind not in MODELS or type(value) is not dict:
        return
    fields = MODELS[kind]['fields']
    if unknown:
        require(not set(value).difference(fields), 'unknown_field', path)
    else:
        for key in MODELS[kind]['required']:
            require(key in value, 'missing_field', path + '.' + key)
    for key, field in fields.items():
        if key in value and value[key] is not None:
            _shape_field(field, value[key], unknown, path + '.' + key)


def _shape_field(field, value, unknown, path):
    if field['type'] in ('ref', 'projection_ref'):
        _shape(field['ref'], value, unknown, path)
    elif field['type'] == 'array' and type(value) is list:
        for item in value:
            _shape(field['items'].get('ref', ''), item, unknown, path + '[*]')


def _typed(kind, value, path='$'):
    if kind not in MODELS:
        return _primitive(kind, value, path)
    require(type(value) is dict, 'invalid_type', path)
    fields = MODELS[kind]['fields']
    for key in MODELS[kind]['required']:
        _field(fields[key], value[key], path + '.' + key)


def _string(value):
    raw = value.encode('utf8')
    return b's' + str(len(raw)).encode('ascii') + b':' + raw


def _canonical_field(field, value):
    if value is None:
        return b'n'
    kind = field['type']
    if kind in ('ref', 'projection_ref'):
        return _encode(field['ref'], value)
    if kind == 'array':
        return b'a' + str(len(value)).encode('ascii') + b'[' + b''.join(_canonical_field(field['items'], item) for item in value) + b']'
    if kind == 'int64':
        return b'i' + str(value).encode('ascii') + b';'
    return _string(value)


def _encode(kind, value):
    if value is None:
        return b'n'
    if kind in MODELS:
        fields = MODELS[kind]['fields']
        return b'o' + str(len(fields)).encode('ascii') + b'{' + b''.join(_string(key) + _canonical_field(fields[key], value[key]) for key in sorted(fields)) + b'}'
    if kind == 'LegacyContextBudget':
        return b'o2{' + _string('limit') + b'i' + str(value['limit']).encode() + b';' + _string('unit') + _string(value['unit']) + b'}'
    if kind in ENUMS or kind in ('SHA', 'LegacyID', 'SchemaPath', 'SafePath'):
        return _string(value)
    rule = PRIMITIVES[kind]
    if rule['type'] == 'bool':
        return b't' if value else b'f'
    if rule['type'] == 'int64':
        return b'i' + str(value).encode('ascii') + b';'
    return b'd' + struct.pack('>d', 0.0 if value == 0 else float(value)).hex().encode() + b';'


def canonical(kind, value):
    require(kind in MODELS or kind in PRIMITIVES or kind in ENUMS, 'unsupported_type')
    _shape(kind, value, True)
    _shape(kind, value, False)
    _typed(kind, value)
    _hashes(kind, value)
    return _encode(kind, value)


def _projection(kind, value):
    model = MODELS[kind]
    source = model.get('source_type')
    allowed = MODELS[source]['fields'] if source in MODELS else model['fields']
    require(not set(value).difference(allowed), 'unknown_field')
    projected = {}
    for key, field in model['fields'].items():
        require(key in value, 'missing_field', '$.' + key)
        child = value[key]
        if child is not None and field['type'] == 'projection_ref':
            child = _projection(field['ref'], child)
        elif child is not None and field['type'] == 'array' and field['items']['type'] == 'projection_ref':
            child = [_projection(field['items']['ref'], item) for item in child]
        projected[key] = child
    return projected


def semantic_sha(kind, value):
    require(kind in MODELS, 'unsupported_type')
    if MODELS[kind].get('kind') == 'closed_typed_projection':
        value = _projection(kind, value)
    encoded = canonical(kind, value)
    return hashlib.sha256(b'dynamics-contract-v2\0' + kind.encode('ascii') + b'\0' + encoded).hexdigest()


def _hashes(kind, value):
    if kind not in MODELS or value is None:
        return
    for key, field in MODELS[kind]['fields'].items():
        child = value[key]
        if field['type'] in ('ref', 'projection_ref'):
            _hashes(field['ref'], child)
        elif field['type'] == 'array':
            for item in child:
                _hashes(field['items'].get('ref', ''), item)
    if kind in SELF_HASHES:
        binding = SELF_HASHES[kind]
        require(value[binding['field']] == semantic_sha(binding['projection'], value), 'hash_mismatch', '$.' + binding['field'])
    if kind in ('RankerConfigV2', 'RegistryBoundRankerProjectionV2'):
        require(value['config_sha256'] == semantic_sha('RankerConfigParametersProjectionV2', value), 'hash_mismatch', '$.config_sha256')
    if kind == 'PolicyRegistryV2':
        require(value['registry_sha256'] == semantic_sha('PolicyRegistryGraphProjectionV2', value), 'hash_mismatch', '$.registry_sha256')


def decode(raw, kind):
    value = parse(raw)
    require(kind in ('PolicyRegistryV2', 'PredictionRunV2'), 'unsupported_type')
    _shape(kind, value, True)
    _shape(kind, value, False)
    _typed(kind, value)
    _hashes(kind, value)
    return value


def _ranker(ranker):
    policy = ranker['policy_id']
    shapes = {'B0': [], 'B1': ['relevance', 'recency'], 'V3-P1-IR': ['relevance', 'importance', 'recency']}
    shape = shapes.get(policy, ['relevance', 'state'])
    require([lane['kind'] for lane in ranker['lanes']] == shape, 'config_mismatch')
    require(policy == 'B0' or any(lane['weight'] > 0 for lane in ranker['lanes']), 'config_mismatch')
    stateful = policy in ('B2', 'V3-P1-FD', 'V3-P1-NOTIME', 'V3-P1-NOOUT')
    require((ranker['dynamics_config_sha256'] is not None) == stateful, 'config_mismatch')
    require((ranker['feature_contract_sha256'] is not None) == (policy == 'V3-P1-IR'), 'config_mismatch')
    if ranker['feature_contract_sha256'] is not None:
        require(ranker['feature_contract_sha256'] == 'b6e965bbfecb4a0f20365d11709ff168043293fe1c5b848b06bc886e95f5f0d8', 'hash_mismatch')
    ablation = {'V3-P1-NOTIME': 'disable_decay',
                'V3-P1-NOOUT': 'disable_outcome_jumps'}.get(policy, 'none')
    require(ranker['ablation'] == ablation and (ranker['inherited_b2_config_sha256'] is None) == (ablation == 'none'), 'config_mismatch')


def _registry(registry):
    require([e['policy_id'] for e in registry['entries']] == ENUMS['PolicyID'], 'config_mismatch')
    limits = dict(max_array_items=200000, max_events=100000, max_features=100000, max_file_bytes=67108864,
                  max_json_depth=32, max_probes=100000, max_receipts=200000, max_states=100000, max_string_bytes=4096)
    require(registry['resource_limits'] == limits, 'resource_limit')
    by_id = {entry['policy_id']: entry for entry in registry['entries']}
    for entry in registry['entries']:
        r, dynamics = entry['ranker'], entry['dynamics']
        _ranker(r)
        require(r['registry_sha256'] == registry['registry_sha256'] and entry['policy_id'] == r['policy_id'] and entry['version'] == r['policy_version'], 'config_mismatch')
        state = r['dynamics_config_sha256'] is not None
        require((dynamics is not None) == state and entry['implemented_mode'] == ('scalar' if state else 'none'), 'config_mismatch')
        if dynamics is not None:
            require(dynamics['config_sha256'] == r['dynamics_config_sha256'], 'hash_mismatch')
            decay = entry['policy_id'] != 'V3-P1-NOTIME'
            jumps = entry['policy_id'] not in ('V3-P1-FD', 'V3-P1-NOOUT')
            require(dynamics['decay_enabled'] == decay and dynamics['outcome_jump_enabled'] == jumps and
                    entry['uses_elapsed_decay'] == decay and entry['uses_outcomes'] == jumps, 'config_mismatch')
    _ablations(by_id)


def _ablations(entries):
    base = entries['B2']
    for policy, flag in [('V3-P1-NOTIME', 'decay_enabled'), ('V3-P1-NOOUT', 'outcome_jump_enabled')]:
        entry = entries[policy]
        require(entry['ranker']['inherited_b2_config_sha256'] == base['ranker']['config_sha256'], 'config_mismatch')
        ignored = {'config_sha256', flag}
        require({k: v for k, v in entry['dynamics'].items() if k not in ignored} ==
                {k: v for k, v in base['dynamics'].items() if k not in ignored}, 'config_mismatch')
        ignored = {'policy_id', 'policy_version', 'dynamics_config_sha256', 'inherited_b2_config_sha256', 'ablation', 'config_sha256', 'registry_sha256'}
        require({k: v for k, v in entry['ranker'].items() if k not in ignored} ==
                {k: v for k, v in base['ranker'].items() if k not in ignored}, 'config_mismatch')


def _run(run, registry):
    require(run['registry_sha256'] == registry['registry_sha256'], 'hash_mismatch')
    entry = next(e for e in registry['entries'] if e['policy_id'] == run['policy_id'])
    require(run['policy_version'] == entry['version'], 'config_mismatch')
    seen, failed = set(), 0
    for prediction in run['predictions']:
        require(prediction['query_id'] not in seen, 'schedule_mismatch')
        seen.add(prediction['query_id'])
        if prediction['status'] == 'failed':
            require(prediction['decision'] is None and prediction['error'] is not None, 'invalid_state')
            failed += 1
        else:
            require(prediction['status'] != 'fallback', 'unsupported_fallback')
            decision = prediction['decision']
            require(decision is not None and prediction['error'] is None, 'invalid_state')
            require(decision['status'] != 'fallback', 'unsupported_fallback')
            require(decision['query_id'] == prediction['query_id'] and decision['policy_id'] == run['policy_id'] and
                    decision['policy_version'] == run['policy_version'] and decision['ranker_config_sha256'] == entry['ranker']['config_sha256'], 'snapshot_binding')
            _decision(decision)
    require(failed == run['cost']['failed_predictions'] and run['cost']['fallback_predictions'] == 0, 'schedule_mismatch')


def _decision(decision):
    require(decision['context_cost_used'] <= decision['context_budget']['limit'], 'invalid_value')
    refs = set()
    for ordinal, row in enumerate(decision['ranks'], 1):
        key = canonical('RefV2', row['ref'])
        require(key not in refs and row['final_ordinal'] == ordinal, 'snapshot_conflict')
        refs.add(key)
    selected = [canonical('RefV2', ref) for ref in decision['selected']]
    require(len(set(selected)) == len(selected) and set(selected).issubset(refs), 'snapshot_binding')


def compare_runs(run_paths, registry_path):
    require(type(run_paths) is list and len(run_paths) >= 2, 'invalid_value')
    require(len({str(path) for path in run_paths}) == len(run_paths), 'snapshot_conflict')
    registry = decode(read(registry_path), 'PolicyRegistryV2')
    _registry(registry)
    runs, hashes, identities = [], set(), set()
    for path in run_paths:
        raw = read(path)
        raw_hash = hashlib.sha256(raw).hexdigest()
        require(raw_hash not in hashes, 'snapshot_conflict')
        hashes.add(raw_hash)
        run = decode(raw, 'PredictionRunV2')
        _run(run, registry)
        identity = (run['run_id'], run['policy_id'])
        require(identity not in identities, 'snapshot_conflict')
        identities.add(identity)
        runs.append(run)
    return _compare(runs, registry['registry_sha256'])


def _compare(runs, registry_sha):
    commitments = {(r['input_manifest_sha256'], r['run_config_sha256']) for r in runs}
    shared = len(commitments) == 1
    predictions = [{p['query_id']: p for p in run['predictions']} for run in runs]
    query_sets = [set(rows) for rows in predictions]
    observed = bool(query_sets[0]) and all(keys == query_sets[0] for keys in query_sets)
    observed = observed and all(p['status'] == 'ok' for rows in predictions for p in rows.values())
    candidates, budget = observed and shared, observed and shared
    if observed:
        for query in query_sets[0]:
            decisions = [rows[query]['decision'] for rows in predictions]
            candidates = candidates and len({d['candidate_snapshot_sha256'] for d in decisions}) == 1
            budget = budget and all(d['context_budget'] == decisions[0]['context_budget'] for d in decisions)
    source = observed and shared and candidates
    values = {'registry': True, 'candidates': candidates, 'budget': budget, 'source_snapshot': source,
              'events': shared, 'features': shared, 'scope': shared, 'clock': shared, 'cutoff': shared}
    order = ['candidates', 'events', 'scope', 'clock', 'cutoff', 'budget', 'registry', 'features', 'source_snapshot']
    checks = [{'name': name, 'equal': bool(values[name]), 'reason': 'none' if values[name] else 'source_unknown'} for name in order]
    statuses = []
    for run in runs:
        rows = run['predictions']
        statuses.append({'run_id': run['run_id'], 'policy_id': run['policy_id'], 'planned': len(rows),
                         'ok': sum(p['status'] == 'ok' for p in rows), 'failed': sum(p['status'] == 'failed' for p in rows),
                         'fallback': sum(p['status'] == 'fallback' for p in rows)})
    return {'protocol': 'dynamics-compare-v2', 'run_ids': sorted({r['run_id'] for r in runs}),
            'registry_sha256': registry_sha, 'equal_conditions': all(c['equal'] for c in checks), 'checks': checks, 'statuses': statuses}

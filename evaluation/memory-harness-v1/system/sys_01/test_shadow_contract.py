"""SYS-01 A: fixed offline metadata validator and synthetic model checks.

This module never calls Search, a provider, a database or a product fence.
Declared fixtures are not authenticated execution or permission evidence.
"""

import copy
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import re
import struct
import unittest
import unicodedata

CONTRACT_SHA256 = '82936159380c3d34720ea80822db0b8c10c3242dba8bb493876c7a8895bad5f0'
_CODEC_SHA256 = 'd575bf82f5d1088883fddf5191e97a60a2260cc46a37051af9a89cdd5b7aa74e'
_HERE = Path(__file__).parent
_CONTRACT_RAW = (_HERE / 'adapter_contract.json').read_bytes()
if hashlib.sha256(_CONTRACT_RAW).hexdigest() != CONTRACT_SHA256:
    raise RuntimeError('fixed SYS contract mismatch')
_CONTRACT = json.loads(_CONTRACT_RAW)
_CODEC_PATH = _HERE.parents[1] / 'contract_io.py'
if hashlib.sha256(_CODEC_PATH.read_bytes()).hexdigest() != _CODEC_SHA256:
    raise RuntimeError('fixed canonical dependency mismatch')
_spec = importlib.util.spec_from_file_location('_sys01_frozen_baseline_codec', _CODEC_PATH)
_legacy = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_legacy)
_MODELS = copy.deepcopy(_CONTRACT['closed_schema']['models'])
for _name, _base, _fields in [
    ('SnapshotHashProjection', 'CandidateSnapshot', None),
    ('LedgerHashProjection', 'ShadowLedger', None),
    ('CandidateSetHashProjection', 'CandidateSnapshot', ['scope', 'candidates', 'lanes', 'budget']),
]:
    _source = _MODELS[_base]['fields']
    if _fields is None:
        _exclude = 'snapshot_revision' if _base == 'CandidateSnapshot' else 'ledger_sha256'
        _fields = [key for key in _source if key != _exclude]
    _MODELS[_name] = {'fields': {key: copy.deepcopy(_source[key]) for key in _fields}}
_TYPES = frozenset(_MODELS)
_PRIMITIVES = frozenset(_CONTRACT['closed_schema']['primitives'])
_CODES = frozenset(_CONTRACT['fixed_error_codes'])
_HEX = re.compile(r'[0-9a-f]{64}\Z', re.ASCII)
_BITS = re.compile(r'[0-9a-f]{16}\Z', re.ASCII)
_OPAQUE = re.compile(r'[a-z][a-z0-9_-]{0,63}\Z', re.ASCII)
_DEGRADED = frozenset(['memory_vector_disabled', 'memory_vector_unavailable',
                       'external_content_filtered', 'context_content_filtered'])
_STAGES = ['baseline_rrf_window', 'baseline_final_limit', 'baseline_char_packing',
           'baseline_token_packing', 'shadow_policy_selection']
_MAX_BYTES = 4 * 1024 * 1024
_MAX_DEPTH = 32
_MIN_INT64 = -(1 << 63)
_MAX_INT64 = (1 << 63) - 1


class SYSContractError(Exception):
    def __init__(self, code, path='$'):
        self.code, self.path = code, path
        super().__init__(code + ': ' + path)


def _fail(code, path='$'):
    if code not in _CODES:
        raise RuntimeError('unknown fixed SYS error')
    raise SYSContractError(code, path) from None


def _supported(name):
    if type(name) is not str or name not in _TYPES:
        _fail('unsupported_type')


def _unicode(value, path):
    try:
        return value.encode('utf8')
    except UnicodeEncodeError:
        _fail('invalid_unicode', path)


class _HugeInteger:
    pass


class _FloatToken:
    pass


def _integer(token):
    return _HugeInteger() if len(token.lstrip('-')) > 20 else int(token)


def _pairs(items):
    out = {}
    for key, value in items:
        if key in out:
            _fail('duplicate_key')
        out[key] = value
    return out


def _depth(value, level=1):
    if level > _MAX_DEPTH:
        _fail('input_limit')
    if type(value) is dict:
        for key, child in value.items():
            if type(key) is not str:
                _fail('invalid_type')
            _unicode(key, '$')
            _depth(child, level + 1)
    elif type(value) is list:
        for child in value:
            _depth(child, level + 1)
    elif type(value) is str:
        _unicode(value, '$')


def decode_typed(data, type_name):
    """Decode only a pinned compiled SYS type; no caller schema is accepted."""
    _supported(type_name)
    if type(data) is not bytes:
        _fail('invalid_type')
    if len(data) > _MAX_BYTES:
        _fail('input_limit')
    try:
        text = data.decode('utf8')
    except UnicodeDecodeError:
        _fail('invalid_unicode')
    try:
        value = json.loads(text, object_pairs_hook=_pairs, parse_int=_integer,
                           parse_float=lambda _: _FloatToken(),
                           parse_constant=lambda _: _fail('invalid_json'))
    except SYSContractError:
        raise
    except RecursionError:
        _fail('input_limit')
    except (ValueError, TypeError):
        _fail('invalid_json')
    validate_typed(type_name, value)
    return value


def _shape(name, value, path):
    if type(value) is not dict:
        return
    fields = _MODELS[name]['fields']
    if set(value) - set(fields):
        _fail('unknown_field', path)
    if set(fields) - set(value):
        _fail('missing_field', path)
    for key, spec in fields.items():
        child = value[key]
        if child is None:
            continue
        if spec['type'] in _MODELS:
            _shape(spec['type'], child, path + '.' + key)
        elif spec['type'] == 'array' and type(child) is list and spec['items'] in _MODELS:
            for item in child:
                _shape(spec['items'], item, path + '.' + key + '[*]')


def _float_bits(value, path):
    if type(value) is not str:
        _fail('invalid_type', path)
    if not _BITS.fullmatch(value):
        _fail('invalid_value', path)
    number = struct.unpack('>d', bytes.fromhex(value))[0]
    if not math.isfinite(number) or (number == 0 and value != '0000000000000000'):
        _fail('invalid_value', path)
    return number


def _known_path(value):
    if type(value) is not str or len(value.encode('utf8', errors='replace')) > 512:
        return False
    if value == '$':
        return True
    tokens = re.findall(r'\.[a-z][a-z0-9_]*|\[\*\]', value[1:])
    if not value.startswith('$') or ''.join(tokens) != value[1:]:
        return False
    states = set(_MODELS)
    for token in tokens:
        next_states = set()
        if token == '[*]':
            for state in states:
                if isinstance(state, tuple) and state[0] == 'array':
                    next_states.add(state[1])
        else:
            key = token[1:]
            for state in states:
                if type(state) is str and state in _MODELS:
                    spec = _MODELS[state]['fields'].get(key)
                    if spec:
                        next_states.add(('array', spec['items']) if spec['type'] == 'array'
                                        else spec['type'])
        states = next_states
    return bool(states)


def _primitive(name, value, path):
    if name == 'Bool':
        if type(value) is not bool:
            _fail('invalid_type', path)
        return
    if name in ('Count', 'PositiveInt64', 'Int64'):
        if isinstance(value, _HugeInteger):
            _fail('invalid_value', path)
        if type(value) is not int:
            _fail('invalid_type', path)
        low = _MIN_INT64 if name == 'Int64' else (1 if name == 'PositiveInt64' else 0)
        if not low <= value <= _MAX_INT64:
            _fail('invalid_value', path)
        return
    if name in ('FiniteF64Bits', 'FiniteNonnegativeF64Bits'):
        number = _float_bits(value, path)
        if name == 'FiniteNonnegativeF64Bits' and number < 0:
            _fail('invalid_value', path)
        return
    if type(value) is not str:
        _fail('invalid_type', path)
    raw = _unicode(value, path)
    if name == 'SHA256':
        valid = bool(_HEX.fullmatch(value))
    elif name == 'OpaqueID':
        valid = bool(_OPAQUE.fullmatch(value))
    elif name == 'KnownSchemaPath':
        valid = _known_path(value)
    elif name in ('NativeID', 'NativeOptionalID', 'NativeLabel'):
        valid = len(raw) <= 256 and (bool(value) or name != 'NativeID')
    else:
        raise RuntimeError('unknown fixed SYS primitive')
    if not valid:
        _fail('invalid_value', path)


def _typed_field(spec, value, path):
    if value is None:
        if not spec.get('nullable', False):
            _fail('invalid_type', path)
        return
    kind = spec['type']
    if kind in _MODELS:
        _typed(kind, value, path)
    elif kind == 'array':
        if type(value) is not list:
            _fail('invalid_type', path)
        if len(value) > spec['max_items']:
            _fail('input_limit', path)
        for item in value:
            _typed_field({'type': spec['items'], 'nullable': False}, item, path + '[*]')
    elif kind == 'enum':
        if type(value) is not str:
            _fail('invalid_type', path)
        _unicode(value, path)
        if value not in spec['values']:
            _fail('invalid_value', path)
    elif kind == 'null':
        _fail('invalid_type', path)
    else:
        _primitive(kind, value, path)


def _typed(name, value, path):
    if type(value) is not dict:
        _fail('invalid_type', path)
    for key, spec in _MODELS[name]['fields'].items():
        _typed_field(spec, value[key], path + '.' + key)
    _semantics(name, value, path)


def validate_typed(type_name, value):
    _supported(type_name)
    _depth(value)
    _shape(type_name, value, '$')
    _typed(type_name, value, '$')


def _encode_field(spec, value):
    if value is None:
        return b'n'
    kind = spec['type']
    if kind in _MODELS:
        return _encode_object(kind, value)
    if kind == 'array':
        return _legacy._c14n_array([_encode_field({'type': spec['items']}, x) for x in value])
    if kind in ('FiniteF64Bits', 'FiniteNonnegativeF64Bits'):
        return _legacy._c14n_field({'type': 'float64'}, _float_bits(value, '$'))
    if kind in ('Count', 'PositiveInt64', 'Int64'):
        return _legacy._c14n_field({'type': 'int64'}, value)
    if kind == 'Bool':
        return _legacy._c14n_field({'type': 'bool'}, value)
    return _legacy._c14n_string(value)


def _encode_object(name, value):
    fields = _MODELS[name]['fields']
    return b'o' + str(len(fields)).encode('ascii') + b'{' + b''.join(
        _legacy._c14n_string(key) + _encode_field(fields[key], value[key])
        for key in sorted(fields)) + b'}'


def canonical_bytes(type_name, value):
    validate_typed(type_name, value)
    return _encode_object(type_name, value)


def semantic_sha256(type_name, value):
    return hashlib.sha256(b'sys-shadow-contract-v1\0' + type_name.encode('ascii')
                          + b'\0' + canonical_bytes(type_name, value)).hexdigest()


def _projection(value, excluded):
    return {key: child for key, child in value.items() if key != excluded}


def _identity(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False)


def _unique(values, path):
    keys = [_identity(x) for x in values]
    if len(keys) != len(set(keys)):
        _fail('input_conflict', path)


def _indices(value):
    return set(value['indices']) if value['state'] == 'known' else None


def _subset(left, right, path):
    a, b = _indices(left), _indices(right)
    if a is not None and b is not None and not a <= b:
        _fail('invalid_stage_binding', path)


def _eq(left, right, path, code='snapshot_binding'):
    if left != right:
        _fail(code, path)


def _semantics(name, x, p):
    if name == 'Scope':
        # Port native Search validScope/scopeName only. ContextScopeV1.Valid
        # has a different identifier contract; it is not imposed on Search.
        for field in ('workspace_id','project_id','user_id','agent_id'):
            value=x[field]
            if len(value.encode('utf8'))>=256:
                _fail('invalid_value',p + '.' + field)
            if field in ('workspace_id','project_id') and (
                    not value or value in ('.','..') or '/' in value or '\\' in value
                    or any(unicodedata.category(ch)=='Cc' for ch in value)):
                _fail('invalid_value',p + '.' + field)
    elif name == 'NativeClock':
        if (x['state'] == 'known') != (x['unix_ns'] is not None):
            _fail('invalid_value', p + '.unix_ns')
    elif name == 'Budget':
        if x['final_limit'] > 50 or x['lane_limit'] != x['final_limit'] or x['max_chars'] > 64000 or x['budget_tokens'] > 64000:
            _fail('invalid_value', p)
        if (x['budget_tokens'] == 0) != (x['token_encoding'] == ''):
            _fail('invalid_value', p + '.token_encoding')
        if x['native_rrf_window'] not in (x['final_limit'], 3*x['final_limit']):
            _fail('invalid_value', p + '.native_rrf_window')
    elif name == 'BaselineContext':
        expected='native_recency_outcome' if x['collection']=='memories' and not x['include_inactive'] else 'native_rrf_only'
        _eq(x['ranking_mode'],expected,p + '.ranking_mode','invalid_value')
    elif name == 'Lane':
        if [v['ordinal'] for v in x['entries']] != list(range(1,len(x['entries'])+1)):
            _fail('invalid_value',p + '.entries')
    elif name == 'Candidate':
        for source in x['source_bindings']:
            _eq(source['record']['scope'],x['scope'],p + '.source_bindings[*].record.scope','scope_mismatch')
    elif name == 'SourceBinding':
        if x['record']['collection'] != 'sources':
            _fail('invalid_value', p + '.record.collection')
        if x['coverage'] == 'exact_observed' and (x['revision'] is None or x['guard_sha256'] is None):
            _fail('incomplete_evidence', p)
        if x['coverage'] == 'revision_not_recorded' and x['revision'] is not None:
            _fail('invalid_value', p + '.revision')
    elif name == 'OutcomeWeight':
        basis = x['basis']
        if basis == 'not_queried':
            if any(x[k] is not None for k in ('rrf_window_ordinal','effective_factor_bits','signal_factor_bits','signal_ref')) or x['read_at']['state'] != 'not_recorded':
                _fail('invalid_value', p)
        elif basis == 'native_signal_observed':
            if x['effective_factor_bits'] is None or x['signal_factor_bits'] is None:
                _fail('incomplete_evidence', p)
            _eq(x['effective_factor_bits'], x['signal_factor_bits'], p + '.effective_factor_bits')
        elif basis.startswith('native_default_') or basis == 'ranking_disabled':
            if x['effective_factor_bits'] != '3ff0000000000000' or x['signal_factor_bits'] is not None or x['signal_ref'] is not None:
                _fail('invalid_value', p)
        elif basis == 'not_recorded' and any(x[k] is not None for k in ('effective_factor_bits','signal_factor_bits','signal_ref')):
            _fail('invalid_value', p)
    elif name == 'ObservedIndexSet':
        if (x['state'] == 'known') != (x['indices'] is not None):
            _fail('invalid_stage_binding', p)
        if x['indices'] is not None and x['indices'] != sorted(set(x['indices'])):
            _fail('invalid_stage_binding', p + '.indices')
    elif name == 'SelectionStages':
        _subset(x['rrf_window'], x['verified_input'], p + '.rrf_window')
        _subset(x['selected_prepack'], x['rrf_window'] if x['branch']=='baseline' else x['verified_input'], p + '.selected_prepack')
        for key in ('char_current','char_history'):
            _subset(x[key], x['selected_prepack'], p + '.' + key)
        _subset(x['token_current'], x['char_current'], p + '.token_current')
        a,b = _indices(x['char_current']),_indices(x['char_history'])
        if a is not None and b is not None and a & b:
            _fail('invalid_stage_binding', p + '.char_history')
    elif name == 'LossMetric':
        a,b,c = [_indices(x[k]) for k in ('input_indices','output_indices','lost_indices')]
        if x['coverage'] in ('known','undefined_empty_denominator'):
            if a is None or b is None or c is None or not b <= a or c != a-b or x['numerator'] != len(c) or x['denominator'] != len(a):
                _fail('invalid_stage_binding', p)
            if (x['coverage'] == 'undefined_empty_denominator') != (len(a)==0):
                _fail('invalid_stage_binding', p + '.coverage')
        elif x['numerator'] is not None or x['denominator'] is not None or c is not None:
            _fail('invalid_stage_binding', p)
    elif name == 'LossAccounting':
        _loss_accounting(x,p)
    elif name == 'ShadowDecision':
        if x['status'] == 'selected':
            if x['reason'] != 'none':
                _fail('invalid_selection', p + '.reason')
        elif x['selected_indices']:
            _fail('invalid_selection', p + '.selected_indices')
        elif x['reason']=='none':
            _fail('invalid_selection',p + '.reason')
        _unique(x['selected_indices'],p + '.selected_indices')
    elif name == 'ViewFact':
        if x['basis']=='not_recorded' and (x['reader_inclusion']!='unknown' or any(x[k] is not None for k in ('reader_binding_sha256','session_binding_sha256','view_binding_sha256'))):
            _fail('incomplete_evidence',p)
        if x['reader_inclusion'] != 'unknown' and any(x[k] is None for k in ('reader_binding_sha256','session_binding_sha256','view_binding_sha256')):
            _fail('unknown_reader',p)
        if x['exclusion_kind']=='one_shot_context' and (x['phase']!='current_turn' or x['material_lifecycle']!='current' or x['reader_inclusion']!='excluded'):
            _fail('invalid_value',p)
        if x['reader_inclusion']=='included' and x['exclusion_kind'] not in ('none','unknown'):
            _fail('invalid_value',p + '.exclusion_kind')
    elif name == 'AdvisoryCheck':
        if x['status']=='eligible_at_observation' and x['reason']!='none':
            _fail('invalid_value',p + '.reason')
        if x['status']=='unknown' and x['reason'] not in ('reader_unknown','not_observed','policy_unavailable'):
            _fail('invalid_value',p + '.reason')
    elif name == 'NativeOutcome':
        if x['status']=='returned':
            if x['native_error']!='none' or x['final_fence_status']!='committed':
                _fail('invalid_value',p)
        elif x['status']=='failed':
            if x['baseline_returned_refs'] or x['history_returned_refs'] or x['native_error']=='none' or x['final_fence_status']=='committed':
                _fail('invalid_value',p)
        elif x['baseline_returned_refs'] or x['history_returned_refs'] or x['final_fence_status']!='not_run':
            _fail('invalid_value',p)
        if x['final_fence_status']!='committed' and x['context_receipt_sha256'] is not None:
            _fail('invalid_value',p + '.context_receipt_sha256')
    elif name == 'FenceCounterSample':
        if x['key'] is not None:
            _eq(x['key']['record'],x['record'],p + '.key.record')
        if x['coverage']=='exact':
            if x['key'] is None or x['counter_before'] is None or x['counter_after'] is None or x['counter_after']<x['counter_before']:
                _fail('incomplete_fence_witness',p)
    elif name == 'FenceCall':
        if x['key'] is not None:
            _eq(x['key']['record'],x['record'],p + '.key.record')
    elif name == 'FenceTrace':
        _fence_trace(x,p)
    elif name == 'SideEffects':
        if x['reference_fence_delta'] is not None and x['fence_trace'] is not None and _complete_trace(x['fence_trace']):
            want=sum(s['counter_after']-s['counter_before'] for s in x['fence_trace']['counter_samples'])
            _eq(x['reference_fence_delta'],want,p + '.reference_fence_delta','invalid_value')
    elif name == 'SideEffectEvidence':
        _eq(x['equivalence'],_effect_equivalence(x),p + '.equivalence','incomplete_evidence')
    elif name == 'CostEvidence':
        if x['basis']=='not_run' and any(x[k] is not None for k in ('baseline_duration_ns','shadow_duration_ns','shadow_local_steps','provider_calls')):
            _fail('invalid_value',p)
    elif name == 'ReturnItem':
        for key in ('lanes','reasons'):
            if any(not v for v in x[key]):
                _fail('invalid_value',p + '.' + key)
    elif name == 'NativeSemanticWitness':
        _native_witness(x,p)
    elif name == 'PairedSemanticEvidence':
        want_result,want_receipt=_pair_equivalence(x)
        _eq(x['result_equivalence'],want_result,p + '.result_equivalence','incomplete_evidence')
        _eq(x['receipt_equivalence'],want_receipt,p + '.receipt_equivalence','incomplete_evidence')
    elif name == 'RankingCapture':
        if x['lookup_order_indices'] is not None:
            _unique(x['lookup_order_indices'],p + '.lookup_order_indices')
        if x['state']=='not_run' and x['lookup_order_indices'] is not None:
            _fail('invalid_stage_binding',p + '.lookup_order_indices')
    elif name == 'CandidateSnapshot':
        _snapshot(x,p)
    elif name == 'ShadowRequest':
        if x['dynamics_contract_sha256'] != _CONTRACT['accepted_dependencies']['dynamics_contract_sha256']:
            _fail('hash_mismatch',p + '.dynamics_contract_sha256')
    elif name == 'ShadowLedger':
        _ledger(x,p)


def _known_count(observed):
    value=_indices(observed)
    return None if value is None else len(value)


def _union_stage(a,b):
    left,right=_indices(a),_indices(b)
    if left is None or right is None:
        return None
    return left|right


def _loss_accounting(x,p):
    if [v['stage'] for v in x['metrics']] != _STAGES:
        _fail('invalid_value',p + '.metrics')
    a,b=x['baseline_stages'],x['shadow_stages']
    if a['branch']!='baseline' or b['branch']!='shadow':
        _fail('invalid_stage_binding',p)
    _eq(a['verified_input'],b['verified_input'],p + '.shadow_stages.verified_input','invalid_stage_binding')
    mappings=[(a['verified_input'],a['rrf_window']), (a['rrf_window'],a['selected_prepack']),
              (a['selected_prepack'],_union_stage(a['char_current'],a['char_history'])),
              (a['char_current'],a['token_current']), (b['verified_input'],b['selected_prepack'])]
    for metric,(left,right) in zip(x['metrics'],mappings):
        _eq(metric['input_indices'],left,p + '.metrics[*].input_indices','invalid_stage_binding')
        if isinstance(right,set):
            _eq(_indices(metric['output_indices']),right,p + '.metrics[*].output_indices','invalid_stage_binding')
        elif right is None:
            if metric['coverage'] in ('known','undefined_empty_denominator'):
                _fail('invalid_stage_binding',p + '.metrics[*].coverage')
        else:
            _eq(metric['output_indices'],right,p + '.metrics[*].output_indices','invalid_stage_binding')
    fields={'unique_verified_candidates':a['verified_input'],'baseline_rrf_window_count':a['rrf_window'],
            'baseline_prepack_count':a['selected_prepack'],'shadow_selected_count':b['selected_prepack']}
    for field,stage in fields.items():
        _eq(x[field],_known_count(stage),p + '.' + field,'invalid_stage_binding')
    current,history=_known_count(a['token_current']),_known_count(a['char_history'])
    post=None if current is None or history is None else current+history
    _eq(x['baseline_postpack_count'],post,p + '.baseline_postpack_count','invalid_stage_binding')
    pre=_known_count(a['selected_prepack'])
    pack=None if pre is None or post is None else pre-post
    _eq(x['packing_losses'],pack,p + '.packing_losses','invalid_stage_binding')
    final=x['metrics'][1]['numerator']
    _eq(x['final_limit_losses'],final,p + '.final_limit_losses','invalid_stage_binding')


def _record_key(record):
    return _identity(record)


def _complete_trace(trace):
    return trace is not None and trace['coverage']=='complete_watched_set'


def _fence_trace(x,p):
    for field in ('watched_records','missing_records','shadow_only_records'):
        _unique(x[field],p + '.' + field)
    watched={_record_key(r) for r in x['watched_records']}
    if not {_record_key(r) for r in x['missing_records']+x['shadow_only_records']} <= watched:
        _fail('incomplete_fence_witness',p)
    _unique([s['record'] for s in x['counter_samples']],p + '.counter_samples')
    if [c['ordinal'] for c in x['calls']] != list(range(1,len(x['calls'])+1)):
        _fail('incomplete_fence_witness',p + '.calls')
    if any(_record_key(c['record']) not in watched for c in x['calls']):
        _fail('incomplete_fence_witness',p + '.calls')
    if _complete_trace(x):
        samples={_record_key(s['record']) for s in x['counter_samples']}
        if samples!=watched or x['missing_records'] or any(s['coverage']!='exact' for s in x['counter_samples']):
            _fail('incomplete_fence_witness',p)


def _effect_equivalence(x):
    if x['basis']=='not_run':
        if x['off'] is not None or x['shadow'] is not None:
            return 'unknown'
        return 'not_run'
    a,b=x['off'],x['shadow']
    if a is None or b is None:
        return 'unknown'
    for side in (a,b):
        if side['fence_trace'] is not None and any(c['origin']=='shadow' for c in side['fence_trace']['calls']):
            return 'different'
    required=['search_calls','usage_receipts','search_audits','outcome_events','dynamics_state_sha256','canonical_mutation_sha256']
    if any(side[k] is None for side in (a,b) for k in required) or not all(_complete_trace(side['fence_trace']) for side in (a,b)):
        return 'unknown'
    def normalized(side):
        trace=side['fence_trace']
        counters=sorted([(_record_key(s['record']),s['key']['revision'],s['counter_after']-s['counter_before']) for s in trace['counter_samples']])
        calls=[{k:v for k,v in c.items() if k!='ordinal'} for c in trace['calls']]
        return [side[k] for k in required],counters,calls
    return 'equal' if normalized(a)==normalized(b) else 'different'


def _native_witness(x,p):
    if x['degraded'] is not None and any(v not in _DEGRADED for v in x['degraded']):
        _fail('invalid_value',p + '.degraded')
    if x['coverage']=='complete_projection':
        required=['items','history','tokens_used','token_encoding','context_data_sha256','usage_receipts','bundle_authority','degraded']
        if any(x[k] is None for k in required):
            _fail('incomplete_evidence',p)
        if x['native_status']=='returned' and (x['bundle_authority']!='data_only_untrusted' or x['native_error']!='none'):
            _fail('invalid_value',p + '.bundle_authority')
        if x['native_status']=='failed' and (x['bundle_authority']!='' or x['degraded'] or x['items'] or x['history'] or x['usage_receipts'] or x['native_error']=='none'):
            _fail('invalid_value',p)
        if x['native_status']=='not_run':
            _fail('incomplete_evidence',p)
    for field in ('items','history'):
        if x[field] is not None and [r['position'] for r in x[field]] != list(range(1,len(x[field])+1)):
            _fail('invalid_value',p + '.' + field)


def _pair_equivalence(x):
    if x['basis']=='not_run' and x['off'] is None and x['shadow'] is None:
        return 'not_run','not_run'
    a,b=x['off'],x['shadow']
    if a is None or b is None or x['fixture_initial_state_sha256'] is None or x['normalized_search_input_sha256'] is None or x['native_clock']['state']!='known':
        return 'unknown','unknown'
    if a['coverage']!='complete_projection' or b['coverage']!='complete_projection':
        return 'unknown','unknown'
    if a['snapshot_revision'] != b['snapshot_revision']:
        return 'unknown','unknown'
    keys=[k for k in a if k not in ('coverage','usage_receipts','normalization_policy')]
    result='equal' if all(a[k]==b[k] for k in keys) else 'different'
    receipt='equal' if a['usage_receipts']==b['usage_receipts'] else 'different'
    return result,receipt


def _snapshot(x,p):
    budget=x['budget'];active=x['baseline']['collection']=='memories' and not x['baseline']['include_inactive']
    expected='native_recency_outcome' if active else 'native_rrf_only'
    _eq(x['baseline']['ranking_mode'],expected,p + '.baseline.ranking_mode','invalid_value')
    _eq(budget['native_rrf_window'],budget['final_limit']*(3 if active else 1),p + '.budget.native_rrf_window','invalid_value')
    _unique([r['material'] for r in x['candidates']],p + '.candidates')
    seen={};encounter=[]
    for i,lane in enumerate(x['lanes']):
        if lane['lane_index']!=i or len(lane['entries'])>budget['lane_limit']:
            _fail('invalid_value',p + '.lanes')
        if [v['ordinal'] for v in lane['entries']] != list(range(1,len(lane['entries'])+1)):
            _fail('invalid_value',p + '.lanes[*].entries')
        for item in lane['entries']:
            n=item['candidate_index']
            if n>=len(x['candidates']):
                _fail('invalid_selection',p + '.lanes[*].entries[*].candidate_index')
            if n not in seen:
                seen[n]=True;encounter.append(n)
    if encounter!=list(range(len(x['candidates']))):
        _fail('input_conflict',p + '.candidates')
    for c in x['candidates']:
        _eq(c['scope'],x['scope'],p + '.candidates[*].scope','scope_mismatch')
    if len(x['outcome_weights'])!=len(x['candidates']):
        _fail('snapshot_binding',p + '.outcome_weights')
    for c,w in zip(x['candidates'],x['outcome_weights']):
        _eq(c['material'],w['material'],p + '.outcome_weights[*].material')
    capture=x['ranking_capture'];r=_indices(capture['rrf_window']);notq=_indices(capture['not_queried_indices']);C=set(range(len(x['candidates'])))
    if r is not None and (not r<=C or len(r)>budget['native_rrf_window']):
        _fail('invalid_stage_binding',p + '.ranking_capture.rrf_window')
    if capture['state']=='complete_original_path':
        if r is None or notq is None or (active and capture['native_clock']['state']!='known'):
            _fail('incomplete_evidence',p + '.ranking_capture')
        _eq(notq,C-r,p + '.ranking_capture.not_queried_indices','invalid_stage_binding')
        _eq(x['baseline']['clock'],capture['native_clock'],p + '.baseline.clock')
        order=capture['lookup_order_indices']
        disabled=x['baseline']['signal_ranking_off'] or not active
        if order is None or (set(order)!=r if not disabled else bool(order)):
            _fail('invalid_stage_binding',p + '.ranking_capture.lookup_order_indices')
        ordinals=[]
        for i,w in enumerate(x['outcome_weights']):
            if i not in r:
                if w['basis']!='not_queried':
                    _fail('snapshot_binding',p + '.outcome_weights[*].basis')
            elif w['rrf_window_ordinal'] is None or w['basis'] in ('not_queried','not_recorded'):
                _fail('incomplete_evidence',p + '.outcome_weights')
            else:
                ordinals.append(w['rrf_window_ordinal'])
                if disabled and w['basis']!='ranking_disabled':
                    _fail('snapshot_binding',p + '.outcome_weights[*].basis')
                if w['rrf_window_ordinal'] > len(r):
                    _fail('invalid_stage_binding',p + '.outcome_weights[*].rrf_window_ordinal')
                if not disabled and order[w['rrf_window_ordinal']-1]!=i:
                    _fail('invalid_stage_binding',p + '.ranking_capture.lookup_order_indices')
        if sorted(ordinals)!=list(range(1,len(r)+1)):
            _fail('invalid_stage_binding',p + '.outcome_weights[*].rrf_window_ordinal')
    want=semantic_sha256('CandidateSetHashProjection',{k:x[k] for k in ['scope','candidates','lanes','budget']})
    _eq(x['candidate_set_sha256'],want,p + '.candidate_set_sha256','hash_mismatch')
    _eq(x['snapshot_revision'],semantic_sha256('SnapshotHashProjection',_projection(x,'snapshot_revision')),p + '.snapshot_revision','hash_mismatch')


def validate_snapshot(snapshot):
    validate_typed('CandidateSnapshot',snapshot)


def _ledger(x,p):
    _eq(x['snapshot_revision'],x['native']['snapshot_revision'],p + '.native.snapshot_revision')
    _eq(x['snapshot_revision'],x['shadow']['snapshot_revision'],p + '.shadow.snapshot_revision')
    want=x['shadow']['reason'] if x['shadow']['status']!='selected' else 'none'
    _eq(x['fallback_reason'],want,p + '.fallback_reason','invalid_value')
    _eq(x['ledger_sha256'],semantic_sha256('LedgerHashProjection',_projection(x,'ledger_sha256')),p + '.ledger_sha256','hash_mismatch')


def _check_indices(values,n,path):
    if len(values)!=len(set(values)) or any(i>=n for i in values):
        _fail('invalid_selection',path)


def _check_effect_coverage(snapshot,decision,native,paired,effects,current_mode):
    scope=snapshot['scope'];candidates=snapshot['candidates']
    def record(col,id):return {'scope':scope,'collection':col,'id':id}
    def material_records(materials):
        out=[]
        for material in materials:
            out.append(record(material['collection'],material['id']))
            candidate=next((c for c in candidates if c['material']==material),None)
            if candidate is not None:
                out.extend(s['record'] for s in candidate['source_bindings'])
        return out
    for mode in ('off','shadow'):
        side=effects[mode];witness=paired[mode]
        if side is None:
            continue
        trace=side['fence_trace']
        if trace is None:
            continue
        for observed in trace['watched_records']:
            _eq(observed['scope'],scope,'$.fence_trace.watched_records[*].scope','scope_mismatch')
        for sample in trace['counter_samples']:
            _eq(sample['record']['scope'],scope,'$.fence_trace.counter_samples[*].record.scope','scope_mismatch')
        for call in trace['calls']:
            _eq(call['record']['scope'],scope,'$.fence_trace.calls[*].record.scope','scope_mismatch')
        required=[]
        if witness is not None and witness['coverage']=='complete_projection':
            required+=material_records([r['material'] for r in witness['items']+witness['history']])
            if side['usage_receipts'] is not None:
                _eq(side['usage_receipts'],len(witness['usage_receipts']),'$.usage_receipts','invalid_pair_binding')
        if mode==current_mode:
            required+=material_records([candidates[i]['material'] for i in native['pre_final_indices']])
        required+=material_records([candidates[i]['material'] for i in decision['selected_indices']])
        for field in ('related_to','run_ref'):
            ref=snapshot['baseline'][field]
            if ref is not None:
                required.append(record(ref['collection'],ref['id']))
        if _complete_trace(trace):
            watched={_record_key(r) for r in trace['watched_records']}
            if not {_record_key(r) for r in required}<=watched:
                _fail('incomplete_fence_witness','$.fence_trace.watched_records')


def _current_pipeline_join(snapshot,native,losses):
    # These are observations of the current native pipeline, not the other
    # mode's independent result. Pre-final is post-pack, never S_B pre-pack.
    stages=losses['baseline_stages']
    current=_indices(stages['token_current'])
    history=_indices(stages['char_history'])
    prefinal=set(native['pre_final_indices'])
    known=[group for group in (current,history) if group is not None]
    if any(not group<=prefinal for group in known):
        _fail('invalid_stage_binding','$.pre_final_indices')
    if current is not None and history is not None and prefinal!=current|history:
        _fail('invalid_stage_binding','$.pre_final_indices')
    if native['status']=='returned':
        candidates=snapshot['candidates']
        for indices,field in [(current,'baseline_returned_refs'),(history,'history_returned_refs')]:
            if indices is not None:
                expected={_identity(candidates[i]['material']) for i in indices}
                actual={_identity(ref) for ref in native[field]}
                _eq(actual,expected,'$.'+field,'invalid_stage_binding')
    # Failed final transactions retain known packed pre-final observations;
    # their empty published return is already checked by NativeOutcome.


def compare_shadow(request,decision,native,view_facts,advisory,losses,side_effects,cost,paired_semantics):
    """Validate a declared synthetic relationship; does not execute either ranker."""
    args=[('ShadowRequest',request),('ShadowDecision',decision),('NativeOutcome',native),
          ('LossAccounting',losses),('SideEffectEvidence',side_effects),('CostEvidence',cost),
          ('PairedSemanticEvidence',paired_semantics)]
    for name,value in args:
        validate_typed(name,value)
    for name,values in [('ViewFact',view_facts),('AdvisoryCheck',advisory)]:
        if type(values) is not list:
            _fail('invalid_type')
        if len(values)>(800 if name=='ViewFact' else 200):
            _fail('input_limit')
        for value in values:
            validate_typed(name,value)
    snapshot=request['snapshot'];sha=snapshot['snapshot_revision'];C=snapshot['candidates'];n=len(C)
    paired_clock=paired_semantics['native_clock']
    if paired_clock['state']!='not_recorded':
        _eq(paired_clock,snapshot['baseline']['clock'],'$.native_clock')
    for value in (decision,native):
        _eq(value['snapshot_revision'],sha,'$.snapshot_revision')
    _eq(decision['policy_id'],request['policy_id'],'$.policy_id')
    _eq(decision['policy_config_sha256'],request['policy_config_sha256'],'$.policy_config_sha256')
    _check_indices(decision['selected_indices'],n,'$.selected_indices')
    _check_indices(native['pre_final_indices'],n,'$.pre_final_indices')
    if len(decision['selected_indices'])>snapshot['budget']['final_limit'] or decision['local_steps']>request['max_local_steps']:
        _fail('invalid_selection','$.selected_indices')
    if request['mode']=='off' and (decision['status']!='not_run' or decision['reason']!='off' or decision['local_steps']!=0):
        _fail('invalid_selection','$.status')
    if native['source']!='synthetic_fixture' or side_effects['basis'] not in ('synthetic_model','not_run') or paired_semantics['basis'] not in ('synthetic_model','not_run'):
        _fail('incomplete_evidence','$.source')
    materials=[v['material'] for v in C]
    if native['history_returned_refs'] and not snapshot['baseline']['include_inactive']:
        _fail('invalid_selection','$.history_returned_refs')
    for ref in native['baseline_returned_refs']:
        if ref in materials and C[materials.index(ref)]['status']!='active':
            _fail('invalid_selection','$.baseline_returned_refs')
    for ref in native['history_returned_refs']:
        if ref in materials and C[materials.index(ref)]['status']=='active':
            _fail('invalid_selection','$.history_returned_refs')
    for refs in (native['baseline_returned_refs'],native['history_returned_refs']):
        _unique(refs,'$.baseline_returned_refs')
        if any(ref not in materials for ref in refs):
            _fail('snapshot_binding','$.baseline_returned_refs')
    if set(map(_identity,native['baseline_returned_refs']))&set(map(_identity,native['history_returned_refs'])):
        _fail('invalid_selection','$.history_returned_refs')
    if len(native['baseline_returned_refs'])+len(native['history_returned_refs'])>snapshot['budget']['final_limit']:
        _fail('invalid_selection','$.baseline_returned_refs')
    for fact in view_facts:
        _eq(fact['scope'],snapshot['scope'],'$.scope','scope_mismatch')
        if fact['material'] not in materials:
            _fail('snapshot_binding','$.material')
    _unique([{'scope':v['scope'],'material':v['material'],'reader':v['reader_binding_sha256'],'session':v['session_binding_sha256'],'view':v['view_binding_sha256'],'phase':v['phase'],'tag':v['active_tag_binding_sha256']} for v in view_facts],'$.view_facts')
    for check in advisory:
        if check['candidate_index']>=n:
            _fail('invalid_selection','$.candidate_index')
        i=check['view_fact_index']
        if i is None:
            if check['status'] in ('eligible_at_observation','ineligible_at_observation'):
                _fail('unknown_reader','$.view_fact_index')
        elif i>=len(view_facts):
            _fail('invalid_selection','$.view_fact_index')
        else:
            fact=view_facts[i];candidate=C[check['candidate_index']]
            _eq(fact['material'],candidate['material'],'$.view_fact_index')
            if fact['reader_inclusion']=='unknown' and check['status'] not in ('unknown','not_run'):
                _fail('unknown_reader','$.status')
            if check['status']=='eligible_at_observation' and (fact['reader_inclusion']!='included' or fact['material_lifecycle']!='current' or fact['phase']=='history'):
                _fail('invalid_selection','$.status')
    a,b=losses['baseline_stages'],losses['shadow_stages']
    for stages in (a,b):
        _eq(_indices(stages['verified_input']),set(range(n)),'$.verified_input','invalid_stage_binding')
        for stage in ('rrf_window','selected_prepack','char_current','char_history','token_current'):
            ids=_indices(stages[stage])
            if ids is not None and not ids<=set(range(n)):
                _fail('invalid_stage_binding','$.selected_prepack')
    _eq(losses['lane_entries_seen'],sum(len(v['entries']) for v in snapshot['lanes']),'$.lane_entries_seen','invalid_stage_binding')
    _eq(_indices(a['rrf_window']),_indices(snapshot['ranking_capture']['rrf_window']),'$.rrf_window','invalid_stage_binding')
    if decision['status']=='selected':
        _eq(_indices(b['selected_prepack']),set(decision['selected_indices']),'$.selected_prepack','invalid_stage_binding')
    elif _indices(b['selected_prepack']) is not None:
        _fail('invalid_stage_binding','$.selected_prepack')
    _current_pipeline_join(snapshot,native,losses)
    for mode in ('off','shadow'):
        witness=paired_semantics[mode]
        if witness is None:
            continue
        _eq(witness['snapshot_revision'],sha,'$.snapshot_revision')
        for channel in ('items','history','usage_receipts'):
            for observed in witness[channel] or []:
                _eq(observed['scope'],snapshot['scope'],'$.scope','scope_mismatch')
                if channel!='usage_receipts' and observed['material'] not in materials:
                    _fail('snapshot_binding','$.material')
        if witness['coverage']=='complete_projection':
            refs=[v['material'] for v in witness['items']]
            hist=[v['material'] for v in witness['history']]
            _unique(refs,'$.items');_unique(hist,'$.history')
            if set(map(_identity,refs))&set(map(_identity,hist)) or len(refs)+len(hist)>snapshot['budget']['final_limit']:
                _fail('invalid_selection','$.items')
            if mode==request['mode']:
                _eq(witness['native_status'],native['status'],'$.native_status')
                _eq(witness['native_error'],native['native_error'],'$.native_error')
                _eq(refs,native['baseline_returned_refs'],'$.items')
                _eq(hist,native['history_returned_refs'],'$.history')
    _check_effect_coverage(snapshot,decision,native,paired_semantics,side_effects,request['mode'])
    if native['final_rejected_refs'] is not None:
        for rejected in native['final_rejected_refs']:
            if rejected['candidate_index']>=n:
                _fail('invalid_selection','$.final_rejected_refs[*].candidate_index')
    ledger={'protocol':'sys-shadow-ledger-v1','request_id':snapshot['request_id'],'mode':request['mode'],
            'snapshot_revision':sha,'baseline_namespace':'arkism_native_recall_current','offline_reference_namespace':'not_used',
            'native':native,'shadow':decision,'advisory_checks':advisory,'view_facts':view_facts,
            'fallback_reason':decision['reason'] if decision['status']!='selected' else 'none','losses':losses,
            'side_effects':side_effects,'cost':cost,'authority':'data_only_untrusted','verification_scope':'synthetic_model',
            'implementation_status':'synthetic_only','paired_semantics':paired_semantics}
    ledger=copy.deepcopy(ledger)
    ledger['ledger_sha256']=semantic_sha256('LedgerHashProjection',ledger)
    validate_typed('ShadowLedger',ledger)
    return ledger

# The following model is deliberately test-only. It reproduces only the declared
# native RRF/recency arithmetic, never calls the actual Go product, and never
# certifies permission, body retrieval, packing or a transaction.
def _fixture_rrf(snapshot):
    scores={i:0.0 for i in range(len(snapshot['candidates']))}
    fractions={i:[] for i in scores}
    for lane in snapshot['lanes']:
        seen=set()
        for item in lane['entries']:
            i=item['candidate_index'];r=snapshot['candidates'][i]['material']
            key=(r['origin'],r['collection'],r['id'])
            if key in seen:
                continue
            seen.add(key)
            numerator=2 if item['exact_keyword'] else 1
            denominator=60+item['ordinal']
            scores[i]+=numerator/denominator
            fractions[i].append((numerator,denominator))
    return scores,fractions


def _fixture_native_scores(snapshot):
    scores,_=_fixture_rrf(snapshot)
    half_days={'episodic':14,'semantic':30,'decision':60,'lesson':180}
    now=snapshot['baseline']['clock']['unix_ns']
    for i,c in enumerate(snapshot['candidates']):
        b=c['recency'];half=half_days.get(b['kind']);factor=1.0
        if half is not None:
            at=b['updated_at']
            if b['created_at']['state']=='known':at=b['created_at']
            if b['effective_at']['state']=='known':at=b['effective_at']
            if at['state']=='known' and now is not None and at['unix_ns']<now:
                age=min(now-at['unix_ns'],_MAX_INT64)
                factor=0.5+0.5*math.exp2(-age/(half*24*3600*1e9))
        weight=snapshot['outcome_weights'][i]['effective_factor_bits']
        if weight is not None:
            scores[i]*=factor
            scores[i]*=_float_bits(weight,'$')
    return scores


def _fixture_observed_degraded(value, observed):
    """Synthetic observation normalization only; the flag authenticates nothing."""
    if type(observed) is not bool:
        _fail('invalid_type')
    if not observed:
        return None
    if value is None:
        return []
    if type(value) is not list or any(type(v) is not str for v in value):
        _fail('invalid_type')
    if len(value)>1024:
        _fail('input_limit')
    if any(v not in _DEGRADED for v in value):
        _fail('invalid_value')
    return copy.deepcopy(value)


def _fixture_usage_projection(raw):
    """Fixed synthetic wrapper for the three allowed generated exclusions."""
    if type(raw) is not dict or set(raw)!={'generated_id','created_at_ns','updated_at_ns','projection'}:
        _fail('invalid_type')
    validate_typed('UsageReceiptProjection',raw['projection'])
    return copy.deepcopy(raw['projection'])


_CASES_SHA256='0e3133b31e8bfdf45ac50cf8720aee574bba5310833b1109e2247b9fa396e5e6'
_CASES_RAW=(_HERE/'race_cases.json').read_bytes()
if hashlib.sha256(_CASES_RAW).hexdigest()!=_CASES_SHA256:
    raise RuntimeError('fixed synthetic cases mismatch')
_CASES_DOCUMENT=json.loads(_CASES_RAW,object_pairs_hook=_pairs)
if _CASES_DOCUMENT['contract_sha256']!=CONTRACT_SHA256:
    raise RuntimeError('synthetic contract binding mismatch')
_CASES=_CASES_DOCUMENT['cases']


class TestDeclaredCases(unittest.TestCase):
    """One discoverable method per concrete input case; no declared-only PASS."""

    def run_declared(self,case):
        value=copy.deepcopy(case['input']);before=copy.deepcopy(value)
        if case['expected']['status']=='error':
            with self.assertRaises(SYSContractError) as caught:
                compare_shadow(**value)
            self.assertEqual((caught.exception.code,caught.exception.path),
                             (case['expected']['code'],case['expected']['path']))
            self.assertEqual(value,before)
            return
        op=case['operation'];extra=case['extra']
        if op=='compare':
            ledger=compare_shadow(**value)
            self.assertEqual(ledger['ledger_sha256'],case['expected']['ledger_sha256'])
            self.assertEqual(ledger['verification_scope'],'synthetic_model')
            self.assertEqual(ledger['implementation_status'],'synthetic_only')
            self.assertEqual(value,before)
            self.assertEqual(decode_typed(json.dumps(ledger,ensure_ascii=False).encode(),'ShadowLedger'),ledger)
            if extra.get('check')=='deep_immutability':
                ledger['native']['baseline_returned_refs'][0]['id']='changed_output'
                self.assertEqual(value,before)
                fresh=compare_shadow(**value)
                value['native']['baseline_returned_refs'][0]['id']='changed_input'
                self.assertEqual(fresh['native']['baseline_returned_refs'][0]['id'],'a')
            elif extra.get('check')=='all_failure_reasons':
                for reason in ['timeout','nonfinite_score','missing_state']:
                    with self.subTest(reason=reason):
                        altered=copy.deepcopy(value);altered['decision']['reason']=reason
                        out=compare_shadow(**altered)
                        self.assertEqual(out['fallback_reason'],reason)
                        self.assertEqual(out['native'],before['native'])
                        self.assertEqual(out['side_effects'],before['side_effects'])
            elif extra.get('check')=='shadow_only_no_call':
                trace=ledger['side_effects']['shadow']['fence_trace']
                self.assertTrue(all(call['origin']=='native_baseline' for call in trace['calls']))
                self.assertEqual(ledger['side_effects']['equivalence'],'equal')
            elif extra.get('check')=='fixed_normalization':
                self.assertEqual(ledger['paired_semantics']['receipt_equivalence'],'equal')
                projection=value['paired_semantics']['off']['usage_receipts'][0]
                first={'generated_id':'fixture_context_a','created_at_ns':1,'updated_at_ns':1,'projection':copy.deepcopy(projection)}
                second={'generated_id':'fixture_context_b','created_at_ns':2,'updated_at_ns':2,'projection':copy.deepcopy(projection)}
                self.assertNotEqual(first,second)
                self.assertEqual(_fixture_usage_projection(first),_fixture_usage_projection(second))
                second['projection']['items'][0]['rank']=2
                self.assertNotEqual(_fixture_usage_projection(first),_fixture_usage_projection(second))
                self.assertNotIn('ContextID',canonical_bytes('NativeSemanticWitness',value['paired_semantics']['off']).decode())
            elif extra.get('check')=='unknown_cost_not_zero':
                self.assertIsNone(ledger['cost']['provider_calls'])
                self.assertIsNone(ledger['cost']['real_vendor_usd'])
            elif extra.get('check')=='known_empty_vs_unknown':
                self.assertEqual(ledger['paired_semantics']['off']['degraded'],[])
                self.assertIsNone(ledger['paired_semantics']['shadow']['degraded'])
                self.assertEqual(ledger['paired_semantics']['result_equivalence'],'unknown')
                self.assertEqual(_fixture_observed_degraded(None,True),[])
                self.assertEqual(_fixture_observed_degraded([],True),[])
                self.assertIsNone(_fixture_observed_degraded(None,False))
                known=copy.deepcopy(value)
                known['paired_semantics']['shadow'].update(coverage='complete_projection',degraded=_fixture_observed_degraded(None,True))
                known['paired_semantics'].update(result_equivalence='equal',receipt_equivalence='equal')
                self.assertEqual(compare_shadow(**known)['paired_semantics']['result_equivalence'],'equal')
            if case['id']=='one_shot':
                facts=ledger['view_facts']
                self.assertEqual([f['reader_inclusion'] for f in facts],['excluded','included','included'])
                self.assertTrue(all(f['material_lifecycle']=='current' for f in facts))
            if case['id']=='persistent_correction':
                new=extra['new_revision'];validate_typed('MaterialRef',new)
                old=ledger['view_facts'][0]['material']
                self.assertEqual(new['id'],old['id']);self.assertGreater(new['revision'],old['revision'])
                self.assertTrue(all(f['reader_inclusion']=='excluded' for f in ledger['view_facts']))
                self.assertEqual(ledger['native']['baseline_returned_refs'],[])
            if case['id']=='active_tag_other_reader':
                facts=ledger['view_facts']
                self.assertEqual([f['reader_inclusion'] for f in facts],['excluded','included'])
                self.assertNotEqual(facts[0]['reader_binding_sha256'],facts[1]['reader_binding_sha256'])
            if case['id'] in ('hist_fresh','mixed_history_token_loss'):
                self.assertTrue(ledger['native']['history_returned_refs'])
                self.assertEqual(ledger['losses']['baseline_postpack_count'],
                                 len(ledger['native']['baseline_returned_refs'])+len(ledger['native']['history_returned_refs']))
            return
        if op=='canonical':
            got=canonical_bytes(value['type_name'],value['value'])
            self.assertEqual(got,case['expected']['typed_literal'].encode())
            self.assertEqual(semantic_sha256(value['type_name'],value['value']),case['expected']['sha256'])
        elif op=='typed':
            validate_typed(value['type_name'],value['value'])
            self.assertEqual(decode_typed(json.dumps(value['value']).encode(),value['type_name']),value['value'])
            if 'unqueried' in case['expected']:
                for i in case['expected']['unqueried']:
                    self.assertEqual(value['value']['outcome_weights'][i]['basis'],'not_queried')
                    self.assertIsNone(value['value']['outcome_weights'][i]['effective_factor_bits'])
            if 'native_clock' in case['expected']:
                self.assertEqual(value['value']['baseline']['clock']['unix_ns'],case['expected']['native_clock'])
                self.assertEqual(value['value']['outcome_weights'][0]['read_at']['unix_ns'],case['expected']['read_at'])
        elif op=='rrf':
            from fractions import Fraction
            validate_snapshot(value);_,parts=_fixture_rrf(value)
            for i,c in enumerate(value['candidates']):
                actual=sum((Fraction(n,d) for n,d in parts[i]),Fraction(0))
                self.assertEqual(actual,Fraction(case['expected']['fractions'][c['material']['id']]))
        elif op=='native_scores':
            validate_snapshot(value);scores=_fixture_native_scores(value)
            for name,want in case['expected']['scores'].items():
                i=next(i for i,c in enumerate(value['candidates']) if c['material']['id']==name)
                self.assertEqual(struct.pack('>d',scores[i]).hex(),want)
        elif op=='loss_algebra':
            validate_typed(value['type_name'],value['value'])
            actual=[str(v['numerator'])+'/'+str(v['denominator']) for v in value['value']['metrics']]
            self.assertEqual(actual,case['expected']['fractions'])
        else:
            self.fail('unknown fixed test operation')
        self.assertEqual(value,before)


for _case in _CASES:
    if not re.fullmatch(r'[a-z][a-z0-9_]{0,127}', _case['id'], re.ASCII):
        raise RuntimeError('invalid fixed case identifier')
    def _test(self,case=_case):
        self.run_declared(case)
    _test.__name__='test_'+_case['id']
    if hasattr(TestDeclaredCases,_test.__name__):
        raise RuntimeError('duplicate fixed case identifier')
    setattr(TestDeclaredCases,_test.__name__,_test)


class TestStrictBoundary(unittest.TestCase):
    def assert_code(self,code,call,path=None):
        with self.assertRaises(SYSContractError) as caught:
            call()
        self.assertEqual(caught.exception.code,code)
        if path is not None:self.assertEqual(caught.exception.path,path)
        self.assertTrue(_known_path(caught.exception.path))

    def test_count_raw_lexical_and_range(self):
        sample=copy.deepcopy(_CASES[3]['input']['decision'])
        for raw in [b'true',b'1.0',b'1e0',b'"1"']:
            with self.subTest(raw=raw):
                text=json.dumps(sample).encode().replace(b'"local_steps": 5',b'"local_steps": '+raw)
                self.assert_code('invalid_type',lambda:decode_typed(text,'ShadowDecision'))
        for raw in [b'-1',b'9223372036854775808',b'9'*5000]:
            with self.subTest(length=len(raw)):
                text=json.dumps(sample).encode().replace(b'"local_steps": 5',b'"local_steps": '+raw)
                self.assert_code('invalid_value',lambda:decode_typed(text,'ShadowDecision'))

    def test_max_int64_clock_exact(self):
        value={'state':'known','unix_ns':_MAX_INT64}
        self.assertEqual(decode_typed(json.dumps(value).encode(),'NativeClock')['unix_ns'],_MAX_INT64)
        value['unix_ns']=_MIN_INT64
        self.assertEqual(decode_typed(json.dumps(value).encode(),'NativeClock')['unix_ns'],_MIN_INT64)

    def test_json_unicode_duplicate_and_nonfinite(self):
        self.assert_code('invalid_unicode',lambda:decode_typed(b'\xff','Scope'))
        self.assert_code('invalid_unicode',lambda:decode_typed(b'{"workspace_id":"\\ud800","project_id":"p","user_id":"","agent_id":""}','Scope'))
        self.assert_code('duplicate_key',lambda:decode_typed(b'{"workspace_id":"w","workspace_id":"x"}','Scope'))
        for raw in [b'NaN',b'Infinity',b'-Infinity',b'{']:
            with self.subTest(raw=raw):self.assert_code('invalid_json',lambda:decode_typed(raw,'Scope'))

    def test_unknown_fields_and_redacted_errors(self):
        value={'workspace_id':'w','project_id':'p','user_id':'','agent_id':'','private_body_do_not_echo':'hidden-body'}
        with self.assertRaises(SYSContractError) as caught:
            decode_typed(json.dumps(value).encode(),'Scope')
        self.assertEqual((caught.exception.code,caught.exception.path),('unknown_field','$'))
        self.assertNotIn('hidden-body',str(caught.exception));self.assertNotIn('private_body',str(caught.exception))
        self.assert_code('missing_field',lambda:decode_typed(b'{}','Scope'))
        self.assert_code('unsupported_type',lambda:decode_typed(b'{}','CallerSchema'))

    def test_non_string_typed_dict_key_safe(self):
        self.assert_code('invalid_type',lambda:validate_typed('Scope',{1:'not-a-schema-key'}))

    def test_finite_bits_and_minus_zero(self):
        sample=copy.deepcopy(_CASES[3]['input']['request']['snapshot']['candidates'][0])
        for bits in ['7ff8000000000000','7ff0000000000000','fff0000000000000','8000000000000000']:
            with self.subTest(bits=bits):
                sample['trust_bits']=bits
                self.assert_code('invalid_value',lambda:validate_typed('Candidate',sample))

    def test_nullable_empty_and_bool_do_not_coerce(self):
        self.assert_code('invalid_value',lambda:validate_typed('NativeClock',{'state':'known','unix_ns':None}))
        self.assert_code('invalid_type',lambda:validate_typed('NativeClock',{'state':'known','unix_ns':True}))
        value=copy.deepcopy(_CASES[3]['input']['decision']);value['selected_indices']=None
        self.assert_code('invalid_type',lambda:validate_typed('ShadowDecision',value))

    def test_document_and_depth_bounds(self):
        self.assert_code('input_limit',lambda:decode_typed(b' '*(_MAX_BYTES+1),'Scope'))
        self.assert_code('input_limit',lambda:decode_typed(b'['*40+b'0'+b']'*40,'Scope'))

    def test_no_arbitrary_error_path(self):
        for path in ['$.private_body','$.metrics[0].stage','$.__dict__','/absolute-invalid-error-path']:
            with self.subTest(path=path):
                self.assert_code('invalid_value',lambda:validate_typed('SYSContractError',{'code':'invalid_value','path':path}))
        validate_typed('SYSContractError',{'code':'invalid_value','path':'$.metrics[*].stage'})

    def test_registry_unchanged_and_projection_domains(self):
        before=copy.deepcopy(_legacy._OBJECTS)
        scope={'workspace_id':'w','project_id':'p','user_id':'u','agent_id':'a'}
        sha=semantic_sha256('Scope',scope)
        self.assertNotEqual(sha,hashlib.sha256(canonical_bytes('Scope',scope)).hexdigest())
        self.assertEqual(_legacy._OBJECTS,before)
        self.assertNotIn('ShadowLedger',_legacy._OBJECTS)

    def test_hash_and_scope_binding_resealed(self):
        value=copy.deepcopy(_CASES[3]['input']);value['request']['snapshot']['candidates'][0]['scope']['project_id']='foreign'
        self.assert_code('scope_mismatch',lambda:compare_shadow(**value))
        value=copy.deepcopy(_CASES[3]['input']);value['request']['snapshot']['snapshot_revision']='a'*64
        self.assert_code('hash_mismatch',lambda:compare_shadow(**value))

    def test_false_equality_rejected(self):
        value=copy.deepcopy(next(c['input'] for c in _CASES if c['id']=='same_ref_changed_emitted_score'))
        value['paired_semantics']['result_equivalence']='equal'
        self.assert_code('incomplete_evidence',lambda:compare_shadow(**value))
        value=copy.deepcopy(next(c['input'] for c in _CASES if c['id']=='fence_equal_sums_different_refs'))
        value['side_effects']['equivalence']='equal'
        self.assert_code('incomplete_evidence',lambda:compare_shadow(**value))

    def test_weight_ordinal_bounds_fixed_error(self):
        value=copy.deepcopy(_CASES[3]['input'])
        value['request']['snapshot']['outcome_weights'][0]['rrf_window_ordinal']=999
        self.assert_code('invalid_stage_binding',lambda:compare_shadow(**value))

    def test_paired_return_and_receipt_scope_binding(self):
        for channel in ['items','usage_receipts']:
            with self.subTest(channel=channel):
                value=copy.deepcopy(_CASES[3]['input'])
                for side in ['off','shadow']:
                    value['paired_semantics'][side][channel][0]['scope']['project_id']='foreign'
                self.assert_code('scope_mismatch',lambda:compare_shadow(**value))

    def test_history_requires_native_history_mode(self):
        value=copy.deepcopy(_CASES[3]['input'])
        material=value['request']['snapshot']['candidates'][2]['material']
        value['native']['history_returned_refs']=[copy.deepcopy(material)]
        for side in ['off','shadow']:
            item=copy.deepcopy(value['paired_semantics'][side]['items'][0])
            item.update(material=copy.deepcopy(material),history_expires_at={'state':'absent','unix_ns':None})
            value['paired_semantics'][side]['history']=[item]
        self.assert_code('invalid_selection',lambda:compare_shadow(**value))

    def test_complete_watchset_covers_known_native_and_shadow_refs(self):
        value=copy.deepcopy(_CASES[3]['input'])
        for side in ['off','shadow']:
            effect=value['side_effects'][side]
            effect['reference_fence_delta']=0
            effect['fence_trace'].update(watched_records=[],counter_samples=[],calls=[],missing_records=[],shadow_only_records=[])
        self.assert_code('incomplete_fence_witness',lambda:compare_shadow(**value))

    def test_captured_receipt_count_matches_effect_counter(self):
        value=copy.deepcopy(_CASES[3]['input'])
        for side in ['off','shadow']:
            value['paired_semantics'][side]['usage_receipts']=[]
        self.assert_code('invalid_pair_binding',lambda:compare_shadow(**value))

    def test_source_and_fence_scope_relationships(self):
        candidate=copy.deepcopy(_CASES[3]['input']['request']['snapshot']['candidates'][0])
        foreign=copy.deepcopy(candidate['scope']);foreign['project_id']='foreign'
        candidate['source_bindings']=[{'record':{'scope':foreign,'collection':'sources','id':'fixture_source'},'revision':None,'guard_sha256':None,'coverage':'revision_not_recorded'}]
        with self.subTest(part='source'):
            self.assert_code('scope_mismatch',lambda:validate_typed('Candidate',candidate))
        value=copy.deepcopy(_CASES[3]['input'])
        def foreign_scopes(item):
            if type(item) is dict:
                for key,child in item.items():
                    if key=='scope':child['project_id']='foreign'
                    else:foreign_scopes(child)
            elif type(item) is list:
                for child in item:foreign_scopes(child)
        foreign_scopes(value['side_effects'])
        with self.subTest(part='fence'):
            self.assert_code('scope_mismatch',lambda:compare_shadow(**value))

    def test_failed_decision_requires_explicit_reason(self):
        value=copy.deepcopy(_CASES[3]['input']['decision'])
        value.update(status='failed',reason='none',selected_indices=[])
        self.assert_code('invalid_selection',lambda:validate_typed('ShadowDecision',value))

    def test_standalone_baseline_and_lane_semantics(self):
        value=copy.deepcopy(_CASES[3]['input']['request']['snapshot']['baseline'])
        value['collection']='skills'
        with self.subTest(part='baseline'):
            self.assert_code('invalid_value',lambda:validate_typed('BaselineContext',value))
        lane=copy.deepcopy(_CASES[3]['input']['request']['snapshot']['lanes'][0])
        lane['entries'][0]['ordinal']=2
        with self.subTest(part='lane'):
            self.assert_code('invalid_value',lambda:validate_typed('Lane',lane))

    def test_pair_clock_binds_immutable_current_snapshot(self):
        value=copy.deepcopy(_CASES[3]['input'])
        value['paired_semantics']['native_clock']['unix_ns']=999
        self.assert_code('snapshot_binding',lambda:compare_shadow(**value))

    def test_pair_clock_cannot_backfill_unknown_snapshot(self):
        value=copy.deepcopy(next(c['input'] for c in _CASES if c['id']=='early_failure_stages_unknown'))
        value['paired_semantics']['native_clock']={'state':'known','unix_ns':100}
        self.assert_code('snapshot_binding',lambda:compare_shadow(**value))

    def test_unknown_pair_clock_stays_unknown(self):
        value=copy.deepcopy(next(c['input'] for c in _CASES if c['id']=='policy_revocation_limit'))
        self.assertEqual(compare_shadow(**value)['paired_semantics']['result_equivalence'],'unknown')

    def test_current_return_matches_observed_packed_stages(self):
        value=copy.deepcopy(_CASES[3]['input'])
        a=value['losses']['baseline_stages']
        a['char_current']={'state':'known','indices':[2]}
        a['token_current']={'state':'known','indices':[2]}
        value['losses']['metrics'][2].update(output_indices={'state':'known','indices':[2]},lost_indices={'state':'known','indices':[0]})
        value['losses']['metrics'][3].update(input_indices={'state':'known','indices':[2]},output_indices={'state':'known','indices':[2]})
        self.assert_code('invalid_stage_binding',lambda:compare_shadow(**value))

    def test_current_return_join_not_only_prefinal_consistency(self):
        value=copy.deepcopy(_CASES[3]['input'])
        value['native']['pre_final_indices']=[2]
        a=value['losses']['baseline_stages']
        a['char_current']={'state':'known','indices':[2]}
        a['token_current']={'state':'known','indices':[2]}
        value['losses']['metrics'][2].update(output_indices={'state':'known','indices':[2]},lost_indices={'state':'known','indices':[0]})
        value['losses']['metrics'][3].update(input_indices={'state':'known','indices':[2]},output_indices={'state':'known','indices':[2]})
        self.assert_code('invalid_stage_binding',lambda:compare_shadow(**value))

    def test_prefinal_is_postpack_and_failed_returns_stay_empty(self):
        value=copy.deepcopy(_CASES[3]['input'])
        self.assertNotEqual(value['native']['pre_final_indices'],value['losses']['baseline_stages']['selected_prepack']['indices'])
        self.assertEqual(compare_shadow(**value)['native']['pre_final_indices'],[0])
        late=copy.deepcopy(next(c['input'] for c in _CASES if c['id']=='revision_race'))
        out=compare_shadow(**late)
        self.assertEqual(out['native']['baseline_returned_refs'],[])
        self.assertEqual(out['native']['pre_final_indices'],[0])
        self.assertEqual(out['losses']['baseline_postpack_count'],1)
        late['native']['pre_final_indices']=[]
        self.assert_code('invalid_stage_binding',lambda:compare_shadow(**late))

    def test_scope_search_name_restrictions(self):
        for field in ['workspace_id','project_id']:
            for bad in ['..','.','/bad','bad\\path','bad\x00name','bad\x85name','bad\nname','x'*256,'한'*86]:
                with self.subTest(field=field,bad=bad):
                    scope={'workspace_id':'w','project_id':'p','user_id':'','agent_id':''}
                    scope[field]=bad
                    self.assert_code('invalid_value',lambda:validate_typed('Scope',scope))
        for field in ['user_id','agent_id']:
            with self.subTest(field=field,length=256):
                scope={'workspace_id':'w','project_id':'p','user_id':'','agent_id':''}
                scope[field]='x'*256
                self.assert_code('invalid_value',lambda:validate_typed('Scope',scope))

    def test_search_scope_plain_labels_and_other_ids_not_narrowed(self):
        values=[{'workspace_id':'한글 공간','project_id':'p q','user_id':'','agent_id':''},
                {'workspace_id':'한'*85,'project_id':'x'*255,'user_id':'u/name','agent_id':'..'}]
        for scope in values:
            with self.subTest(scope=scope):
                validate_typed('Scope',scope)
                self.assertEqual(decode_typed(json.dumps(scope,ensure_ascii=False).encode(),'Scope'),scope)
        material=copy.deepcopy(_CASES[3]['input']['request']['snapshot']['candidates'][0]['material'])
        material['id']='fixture/path'
        validate_typed('MaterialRef',material)

    def test_unknown_authority_and_degraded_rejected(self):
        value=copy.deepcopy(_CASES[3]['input']['paired_semantics']['off']);value['bundle_authority']='trusted'
        self.assert_code('invalid_value',lambda:validate_typed('NativeSemanticWitness',value))
        value=copy.deepcopy(_CASES[3]['input']['paired_semantics']['off']);value['degraded']=['unknown_private_reason']
        self.assert_code('invalid_value',lambda:validate_typed('NativeSemanticWitness',value))

    def test_advisory_not_actual_permission(self):
        value=copy.deepcopy(next(c['input'] for c in _CASES if c['id']=='unknown_reader'))
        value['advisory'][0].update(status='eligible_at_observation',reason='none')
        self.assert_code('unknown_reader',lambda:compare_shadow(**value))
        value=copy.deepcopy(_CASES[3]['input']);value['native']['source']='actual_native_search'
        self.assert_code('incomplete_evidence',lambda:compare_shadow(**value))


if __name__=='__main__':
    unittest.main()

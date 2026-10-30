"""SYS04 synthetic lifecycle planner and tests; no product writes or authorization."""
import copy
import hashlib
import json
import math
from pathlib import Path
import re
import subprocess
import sys
import unittest

HERE = Path(__file__).resolve().parent
PROTOCOL = 'sys-lifecycle-model-v1'
METADATA_PROTOCOL = 'sys-lifecycle-metadata-v2'
METADATA_PROFILE = 'sys04-main-eval-roots-v1'
MAIN_SOURCE = 'a389082b77cc57f45c174a561cb443a2d533e83f'
METADATA_COLLECTIONS = ('sources', 'memories', 'evaluations', 'eval_tasks', 'eval_runs', 'eval_trials', 'judge_calibrations')
SCOPE_FIELDS = ('workspace_id', 'project_id', 'user_id', 'agent_id')
RESTORE_FIELDS = ('state', 'receipt', 'consumption', 'event_cursor', 'checkpoint', 'dependency_index')
FACETS = ('serving_blocked', 'derived_invalidated', 'runtime_purged', 'physical_purged', 'artifacts_purged', 'backup_expired', 'verified')
KINDS = ('source', 'memory_revision', 'fact', 'state', 'receipt', 'cache', 'snapshot', 'checkpoint', 'pending_event', 'usage_receipt', 'crew_receipt', 'compressed')
SHA = re.compile(r'^[0-9a-f]{64}$')
LABEL = re.compile(r'^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$')
MAX_INT = (1 << 63) - 1
MIN_INT = -(1 << 63)
MAX_BYTES = 2 * 1024 * 1024


class LifecycleError(ValueError):
    """Fixed diagnostic only; never interpolate input contents."""


def require(condition, code='invalid_shape'):
    if not condition:
        raise LifecycleError(code)


def obj(value, fields):
    require(type(value) is dict and set(value) == set(fields))


def seq(value, limit=512):
    require(type(value) is list and len(value) <= limit)


def label(value):
    require(type(value) is str and LABEL.fullmatch(value) is not None, 'invalid_label')


def sha(value):
    require(type(value) is str and SHA.fullmatch(value) is not None, 'invalid_hash')


def integer(value, minimum=0):
    require(type(value) is int and minimum <= value <= MAX_INT, 'invalid_integer')


def timestamp(value):
    require(type(value) is int and MIN_INT <= value <= MAX_INT, 'invalid_integer')


def boolean(value):
    require(type(value) is bool, 'invalid_boolean')


GO_TRIM_SPACE = '\u0009\u000a\u000b\u000c\u000d\u0020\u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000'


def metadata_bytes(value):
    require(type(value) is str, 'metadata_string')
    try:
        return len(value.encode('utf-8'))
    except UnicodeError:
        raise LifecycleError('metadata_string') from None


def metadata_controls(value):
    # Go unicode.IsControl is Unicode Cc, not Cf or Python isspace.
    return any(ord(c) < 32 or 127 <= ord(c) <= 159 for c in value)


def metadata_scope_values(value):
    for field in ('workspace_id', 'project_id'):
        v = value[field]
        require(0 < metadata_bytes(v) < 256 and v not in ('.', '..') and '/' not in v and '\\' not in v and not metadata_controls(v), 'metadata_scope')
    for field in ('user_id', 'agent_id'):
        require(metadata_bytes(value[field]) < 256, 'metadata_scope')


def metadata_id(value):
    require(0 < metadata_bytes(value) <= 256 and value.strip(GO_TRIM_SPACE) == value and not metadata_controls(value), 'metadata_id')


def _scope_key(value):
    return tuple(value[k] for k in SCOPE_FIELDS)


def scope(value, metadata=False):
    obj(value, SCOPE_FIELDS)
    if metadata:
        metadata_scope_values(value)
    else:
        for v in value.values():
            label(v)
    return _scope_key(value)


def ref(value, collections=None, metadata=False):
    obj(value, ('namespace', 'collection', 'id', 'revision', 'content_sha256'))
    require(value['namespace'] == 'fixture', 'non_synthetic_namespace')
    label(value['collection'])
    if metadata:
        metadata_id(value['id'])
    else:
        label(value['id'])
    integer(value['revision'], 1); sha(value['content_sha256'])
    if collections is not None:
        require(value['collection'] in collections, 'invalid_collection')
    return (value['namespace'], value['collection'], value['id'], value['revision'], value['content_sha256'])


def entity(sc, r):
    return (_scope_key(sc), r['namespace'], r['collection'], r['id'])


def raw(value):
    try:
        return (json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=False, allow_nan=False) + '\n').encode('utf-8')
    except (ValueError, TypeError, UnicodeError, RecursionError):
        raise LifecycleError('invalid_json') from None


def digest(value):
    domain = METADATA_PROTOCOL if value.get('protocol') == METADATA_PROTOCOL else PROTOCOL
    return hashlib.sha256(domain.encode('utf-8') + b'\0' + raw(value)).hexdigest()


def decode(data):
    require(type(data) is bytes and 0 < len(data) <= MAX_BYTES, 'invalid_size')
    def pairs(items):
        out = {}
        for k, v in items:
            require(k not in out, 'duplicate_field')
            out[k] = v
        return out
    def floating(text):
        value = float(text)
        require(math.isfinite(value), 'nonfinite_number')
        return value
    def constant(_):
        raise LifecycleError('nonfinite_number')
    try:
        return json.loads(data.decode('utf-8'), object_pairs_hook=pairs, parse_float=floating, parse_constant=constant)
    except LifecycleError:
        raise
    except (ValueError, TypeError, UnicodeError, RecursionError):
        raise LifecycleError('invalid_json') from None


def validate_record(r, now, collections=('sources', 'memories'), metadata=False):
    obj(r, ('scope', 'ref', 'status', 'approved', 'privacy_quarantined', 'content_erased', 'effective_at_ns', 'expires_at_ns', 'history', 'correction'))
    scope(r['scope'], metadata); ref(r['ref'], collections, metadata)
    require(r['status'] in ('active', 'superseded', 'conflicted', 'rejected', 'expired', 'deleted'))
    require(r['correction'] in ('none', 'pending', 'confirmed'))
    for k in ('approved', 'privacy_quarantined', 'content_erased'):
        boolean(r[k])
    for k in ('effective_at_ns', 'expires_at_ns'):
        if r[k] is not None:
            timestamp(r[k])
    if r['history'] is not None:
        seq(r['history']); last = None
        for h in r['history']:
            obj(h, ('observed_at_ns', 'revision', 'status', 'approved', 'content_sha256', 'withheld', 'creation', 'effective_at_ns', 'expires_at_ns'))
            timestamp(h['observed_at_ns']); integer(h['revision'], 1); sha(h['content_sha256'])
            require((last is None or last <= h['observed_at_ns']) and h['observed_at_ns'] <= now, 'history_clock')
            last = h['observed_at_ns']
            require(h['status'] in ('active', 'superseded', 'conflicted', 'rejected', 'expired', 'deleted'))
            for k in ('approved', 'withheld', 'creation'):
                boolean(h[k])
            for k in ('effective_at_ns', 'expires_at_ns'):
                if h[k] is not None:
                    timestamp(h[k])


def validate_input(p):
    is_metadata = p.get('protocol') == METADATA_PROTOCOL if type(p) is dict else False
    fields = ('protocol', 'scope', 'now_ns', 'operation', 'query', 'as_of_ns', 'snapshot', 'current', 'nodes', 'fact_withdrawals', 'deletion_evidence', 'restore_fields')
    obj(p, fields + (('metadata_profile', 'root_metadata') if is_metadata else ()))
    require(p['protocol'] in (PROTOCOL, METADATA_PROTOCOL), 'protocol_mismatch'); scope(p['scope'], is_metadata); timestamp(p['now_ns'])
    now = p['now_ns']
    require(p['operation'] in ('read', 'retry', 'publish', 'restore'))
    require(p['query'] in ('current', 'historical'))
    if p['query'] == 'historical':
        timestamp(p['as_of_ns']); require(p['as_of_ns'] <= now, 'future_history')
        require(p['operation'] == 'read', 'history_not_execution')
    else:
        require(p['as_of_ns'] is None)
    obj(p['snapshot'], ('captured_at_ns', 'ledger_sha256'))
    timestamp(p['snapshot']['captured_at_ns']); sha(p['snapshot']['ledger_sha256'])
    require(p['snapshot']['captured_at_ns'] <= now, 'snapshot_clock')
    c = p['current']
    obj(c, ('checked_at_ns', 'scope', 'read_authorization', 'ledger_sha256', 'deletions', 'records', 'publication_gate'))
    timestamp(c['checked_at_ns']); require(c['checked_at_ns'] <= now, 'current_clock'); scope(c['scope'], is_metadata)
    require(c['read_authorization'] in ('confirmed', 'denied', 'unknown'))
    require(c['publication_gate'] in ('open', 'pending_review', 'unknown'))
    if c['ledger_sha256'] is not None:
        sha(c['ledger_sha256'])
    require((c['ledger_sha256'] is None) == (c['deletions'] is None), 'ledger_coverage_conflict')
    if c['deletions'] is not None:
        seq(c['deletions']); seen = set()
        for d in c['deletions']:
            obj(d, ('scope', 'collection', 'id')); scope(d['scope'], is_metadata); label(d['collection'])
            if is_metadata:
                metadata_id(d['id'])
            else:
                label(d['id'])
            key = (scope(d['scope'], is_metadata), d['collection'], d['id'])
            require(key not in seen, 'duplicate_deletion'); seen.add(key)
    seq(c['records']); seen = set()
    for r in c['records']:
        validate_record(r, now, METADATA_COLLECTIONS if is_metadata else ('sources', 'memories'), is_metadata); key = entity(r['scope'], r['ref'])
        require(key not in seen, 'ambiguous_current_record'); seen.add(key)
    seq(p['nodes']); require(len(p['nodes']) > 0, 'empty_nodes'); seen = set()
    for n in p['nodes']:
        obj(n, ('id', 'kind', 'scope', 'ref', 'fact_id', 'dependency_groups', 'invalidation'))
        label(n['id']); scope(n['scope'], is_metadata); require(n['kind'] in KINDS or is_metadata and n['kind'] == 'metadata_root')
        require(n['id'] not in seen, 'duplicate_node'); seen.add(n['id'])
        require(n['invalidation'] in ('supported', 'unsupported', 'unknown'))
        seq(n['dependency_groups'], 32)
        for group in n['dependency_groups']:
            seq(group, 64)
            for k in group:
                label(k)
            require(len(group) > 0 and len(group) == len(set(group)), 'invalid_dependency_group')
        if is_metadata and n['kind'] == 'metadata_root':
            ref(n['ref'], METADATA_COLLECTIONS, True); require(n['fact_id'] is None)
        elif n['kind'] in ('source', 'memory_revision'):
            require(not is_metadata, 'metadata_root_kind'); ref(n['ref'], ('sources',) if n['kind'] == 'source' else ('memories',))
            require(n['fact_id'] is None)
            if n['kind'] == 'source':
                require(not n['dependency_groups'])
        else:
            require(n['ref'] is None)
            if n['kind'] == 'fact':
                label(n['fact_id']); require(len(n['dependency_groups']) == 1 and len(n['dependency_groups'][0]) == 1)
            else:
                require(n['fact_id'] is None)
            # Empty provenance is an explicit missing binding, handled as a hold.
    if is_metadata:
        validate_metadata_profile(p)
    seq(p['fact_withdrawals']); seen = set()
    for w in p['fact_withdrawals']:
        obj(w, ('scope', 'memory', 'fact_id', 'basis'))
        scope(w['scope'], is_metadata); ref(w['memory'], ('memories',)); label(w['fact_id'])
        require(w['basis'] == 'synthetic_explicit_fact', 'unsupported_fact_basis')
        key = (scope(w['scope'], is_metadata), ref(w['memory']), w['fact_id'])
        require(key not in seen, 'duplicate_withdrawal'); seen.add(key)
    seq(p['deletion_evidence']); seen = set()
    for e in p['deletion_evidence']:
        obj(e, ('scope', 'collection', 'id', 'facets')); scope(e['scope'], is_metadata); label(e['collection'])
        if is_metadata:
            metadata_id(e['id'])
        else:
            label(e['id'])
        key = (scope(e['scope'], is_metadata), e['collection'], e['id']); require(key not in seen, 'duplicate_evidence'); seen.add(key)
        obj(e['facets'], FACETS)
        for f in e['facets'].values():
            obj(f, ('status', 'receipt_ref'))
            require(f['status'] in ('observed_complete', 'observed_incomplete', 'unknown', 'held'))
            if f['status'].startswith('observed_'):
                ref(f['receipt_ref'], ('jobs',), is_metadata)
            else:
                require(f['receipt_ref'] is None, 'unknown_evidence_claim')
    if p['operation'] == 'restore':
        obj(p['restore_fields'], ('snapshot', 'readback'))
        for mapping in p['restore_fields'].values():
            obj(mapping, RESTORE_FIELDS)
            for value in mapping.values():
                if value is not None:
                    sha(value)
    else:
        require(p['restore_fields'] is None)


def metadata_binding(p):
    profile = p['metadata_profile']
    payload = {k: profile[k] for k in ('id', 'source_commit', 'collections', 'expected_roots', 'extraction_sha256')}
    return hashlib.sha256(METADATA_PROFILE.encode('utf-8') + b'\0' + raw(payload)).hexdigest()


def metadata_key(sc, r):
    return (scope(sc, True), ref(r, METADATA_COLLECTIONS, True))


def metadata_evaluation_refs(row):
    e = row['evaluation']
    if row['ref']['collection'] != 'evaluations':
        require(e is None, 'metadata_evaluation_shape')
        return []
    obj(e, ('eval_type', 'spec_ref', 'trial_ref', 'calibration_ref'))
    require(e['eval_type'] in ('automated_check', 'user_rating', 'model_self', 'llm_judge'), 'metadata_evaluation_type')
    require((e['eval_type'] == 'llm_judge') == (e['calibration_ref'] is not None), 'metadata_calibration')
    out = []
    for field, collection in (('spec_ref', 'eval_tasks'), ('trial_ref', 'eval_trials'), ('calibration_ref', 'judge_calibrations')):
        if e[field] is not None:
            ref(e[field], (collection,), True); out.append(e[field])
    return out


def metadata_refs(row):
    out = []
    for field in ('sources', 'relations'):
        seq(row[field], 64); seen = set()
        for r in row[field]:
            value = ref(r, ('sources',) if field == 'sources' else METADATA_COLLECTIONS, True)
            require(value not in seen, 'duplicate_metadata_ref'); seen.add(value); out.append(r)
    if row['target'] is not None:
        ref(row['target'], METADATA_COLLECTIONS, True); out.append(row['target'])
    if row['ref']['collection'] == 'eval_trials':
        collections = {r['collection'] for r in row['relations']}
        require({'eval_tasks', 'eval_runs'} <= collections, 'metadata_trial_dependencies')
    return out + metadata_evaluation_refs(row)


def validate_metadata_edges(p, roots):
    seq(p['root_metadata']); descriptions = {}
    for row in p['root_metadata']:
        obj(row, ('scope', 'ref', 'sources', 'target', 'relations', 'evaluation'))
        key = metadata_key(row['scope'], row['ref'])
        require(key not in descriptions, 'duplicate_metadata_root'); descriptions[key] = row
    require(set(descriptions) == set(roots), 'metadata_coverage')
    for key, row in descriptions.items():
        ids = set()
        for r in metadata_refs(row):
            target = metadata_key(row['scope'], r)
            require(target in roots, 'metadata_dependency_missing')
            ids.add(roots[target]['id'])
        groups = roots[key]['dependency_groups']
        require((not ids and not groups) or (len(groups) == 1 and set(groups[0]) == ids), 'metadata_required_and_edges')


def validate_metadata_profile(p):
    require(p['query'] == 'current' and p['fact_withdrawals'] == [], 'unsupported_metadata_history_or_fact')
    profile = p['metadata_profile']
    obj(profile, ('id', 'source_commit', 'collections', 'coverage_status', 'expected_roots', 'snapshot_coverage_sha256', 'readback_coverage_sha256', 'extraction_sha256'))
    require(profile['id'] == METADATA_PROFILE and profile['source_commit'] == MAIN_SOURCE, 'metadata_profile_binding')
    require(profile['collections'] == list(METADATA_COLLECTIONS), 'metadata_collection_profile')
    require(profile['coverage_status'] in ('complete', 'unknown'), 'metadata_coverage_status')
    for field in ('snapshot_coverage_sha256', 'readback_coverage_sha256'):
        if profile[field] is not None:
            sha(profile[field])
    seq(profile['expected_roots']); expected = set()
    for item in profile['expected_roots']:
        obj(item, ('scope', 'ref')); key = metadata_key(item['scope'], item['ref'])
        require(key not in expected, 'duplicate_metadata_root'); expected.add(key)
    roots = {}
    for n in p['nodes']:
        if n['kind'] == 'metadata_root':
            key = metadata_key(n['scope'], n['ref'])
            require(key not in roots, 'duplicate_metadata_root'); roots[key] = n
        else:
            require(n['kind'] != 'fact', 'unsupported_metadata_history_or_fact')
    require(bool(roots) and expected == set(roots), 'metadata_coverage')
    validate_metadata_edges(p, roots)
    sha(profile['extraction_sha256'])
    extraction = hashlib.sha256((METADATA_PROFILE + ':extraction').encode('utf-8') + b'\0' + raw(p['root_metadata'])).hexdigest()
    require(profile['extraction_sha256'] == extraction, 'metadata_extraction_binding')


def metadata_global_reason(p):
    if p['protocol'] != METADATA_PROTOCOL:
        return None
    profile = p['metadata_profile']
    if profile['coverage_status'] != 'complete':
        return 'metadata_coverage_unknown'
    if p['operation'] == 'restore' and any(profile[k] != metadata_binding(p) for k in ('snapshot_coverage_sha256', 'readback_coverage_sha256')):
        return 'metadata_profile_readback'
    return None


def metadata_root_result(n, p, records, deletions):
    key = entity(n['scope'], n['ref']); current = records.get(key)
    if (_scope_key(n['scope']), n['ref']['collection'], n['ref']['id']) in deletions:
        return result('invalidate', 'deleted')
    if current is None:
        return result('hold', 'current_record_missing')
    if current['content_erased'] or current['privacy_quarantined'] or current['status'] == 'deleted':
        return result('invalidate', 'withheld')
    if current['ref'] != n['ref']:
        return result('hold', 'metadata_current_ref_mismatch')
    if current['status'] != 'active' or n['ref']['collection'] == 'memories' and not current['approved']:
        return result('skip', 'not_active_approved')
    if any(current[k] is not None and (current[k] > p['now_ns'] if k == 'effective_at_ns' else current[k] <= p['now_ns']) for k in ('effective_at_ns', 'expires_at_ns')):
        return result('skip', 'outside_effective_window')
    return result('keep', 'metadata_current_exact_ref')


def result(action, reason):
    return {'action': action, 'reason': reason}


def historical(r, at):
    """Matches memoryAt metadata semantics, not old body reconstruction."""
    history = r['history']
    if not history:
        return result('hold', 'history_not_recorded')
    found = next((h for h in reversed(history) if h['observed_at_ns'] <= at), None)
    if found is None:
        return result('skip', 'not_created') if history[0]['creation'] else result('hold', 'history_not_recorded')
    if found['withheld'] or found['status'] != 'active' or not found['approved'] or found['effective_at_ns'] is not None and found['effective_at_ns'] > at or found['expires_at_ns'] is not None and found['expires_at_ns'] <= at:
        return result('skip', 'historically_ineligible')
    if found['content_sha256'] != r['ref']['content_sha256']:
        return result('hold', 'content_unavailable')
    return result('historical_only', 'historical_body_current_hash')


def root_result(n, p, records, deletions):
    key = entity(n['scope'], n['ref']); current = records.get(key)
    if (_scope_key(n['scope']), n['ref']['collection'], n['ref']['id']) in deletions:
        return result('invalidate', 'deleted')
    if current is None:
        return result('hold', 'current_record_missing')
    if current['privacy_quarantined'] or current['content_erased'] or current['status'] == 'deleted':
        return result('invalidate', 'withheld')
    if p['query'] == 'historical' and n['kind'] == 'memory_revision':
        if not current['approved'] or current['status'] not in ('active', 'superseded', 'conflicted', 'rejected', 'expired'):
            return result('skip', 'current_history_not_searchable')
        h = historical(current, p['as_of_ns'])
        if h['action'] == 'historical_only':
            state = next(x for x in reversed(current['history']) if x['observed_at_ns'] <= p['as_of_ns'])
            if n['ref']['revision'] != state['revision'] or n['ref']['content_sha256'] != state['content_sha256']:
                return result('hold', 'historical_ref_mismatch')
        return h
    if n['ref']['revision'] > current['ref']['revision']:
        return result('hold', 'current_revision_regression')
    if n['ref']['revision'] < current['ref']['revision']:
        return result('invalidate', 'revision_changed')
    if n['ref']['content_sha256'] != current['ref']['content_sha256']:
        return result('invalidate', 'same_revision_hash_conflict')
    if current['status'] != 'active' or n['kind'] == 'memory_revision' and not current['approved']:
        return result('skip', 'not_active_approved')
    if current['effective_at_ns'] is not None and current['effective_at_ns'] > p['now_ns'] or current['expires_at_ns'] is not None and current['expires_at_ns'] <= p['now_ns']:
        return result('skip', 'outside_effective_window')
    return result('keep', 'current_exact_ref')


def global_reason(p):
    c = p['current']
    if _scope_key(c['scope']) != _scope_key(p['scope']):
        return 'foreign_current_scope'
    if c['read_authorization'] != 'confirmed':
        return 'current_read_' + c['read_authorization']
    if c['deletions'] is None:
        return 'current_ledger_unknown'
    if c['checked_at_ns'] < p['snapshot']['captured_at_ns']:
        return 'stale_current_metadata'
    if p['operation'] in ('publish', 'restore') and c['publication_gate'] != 'open':
        return 'publication_review_pending'
    return None


def derived_result(n, nodes, decisions, withdrawals):
    groups = n['dependency_groups']
    if not groups:
        return result('hold', 'missing_provenance')
    ids = set(x for group in groups for x in group)
    if any(k not in nodes for k in ids):
        return result('hold', 'missing_dependency')
    if any(_scope_key(nodes[k]['scope']) != _scope_key(n['scope']) for k in ids):
        return result('hold', 'cross_scope_dependency')
    if n['kind'] == 'memory_revision' and any(nodes[k]['kind'] != 'source' for k in ids):
        return result('hold', 'memory_origin_not_source')
    if n['kind'] == 'fact':
        parent = nodes[groups[0][0]]
        if parent['kind'] != 'memory_revision':
            return result('hold', 'fact_origin_not_memory')
        if (_scope_key(n['scope']), ref(parent['ref']), n['fact_id']) in withdrawals:
            return result('invalidate', 'fact_withdrawn')
    # Any lost branch taints stored bytes; an alternative allows a NEW rebuild,
    # never silently relabels the original artifact as unaffected.
    states = [decisions[k]['action'] for k in ids]
    if all(a == 'keep' for a in states):
        return result('keep', 'all_dependencies_current')
    if any(all(decisions[k]['action'] == 'keep' for k in group) for group in groups):
        return result('rebuild', 'alternative_provenance')
    if 'hold' in states or 'out_of_scope' in states:
        return result('hold', 'dependency_unresolved')
    if 'historical_only' in states:
        return result('skip', 'history_not_execution')
    if 'rebuild' in states:
        return result('rebuild', 'dependency_rebuild')
    if 'invalidate' in states:
        return result('invalidate', 'dependency_invalidated')
    return result('skip', 'dependency_ineligible')


def plan(value):
    """Pure model plan. Supplied observations are NOT authenticated native facts."""
    p = decode(value) if type(value) is bytes else copy.deepcopy(value)
    validate_input(p)
    nodes = {n['id']: n for n in p['nodes']}
    records = {entity(r['scope'], r['ref']): r for r in p['current']['records']}
    deletions = {(_scope_key(d['scope']), d['collection'], d['id']) for d in p['current']['deletions'] or []}
    withdrawals = {(_scope_key(w['scope']), ref(w['memory']), w['fact_id']) for w in p['fact_withdrawals']}
    decisions = {}; pending = set(nodes); blocked = metadata_global_reason(p) or global_reason(p)
    for k, n in nodes.items():
        if _scope_key(n['scope']) != _scope_key(p['scope']):
            decisions[k] = result('out_of_scope', 'scope_isolated'); pending.remove(k)
        elif blocked:
            decisions[k] = result('hold', blocked); pending.remove(k)
        elif n['kind'] == 'source':
            decisions[k] = root_result(n, p, records, deletions); pending.remove(k)
    while pending:
        ready = [k for k in sorted(pending) if all(x not in pending for group in nodes[k]['dependency_groups'] for x in group)]
        if not ready:
            for k in pending:
                decisions[k] = result('hold', 'dependency_cycle_or_dependent')
            break
        for k in ready:
            n = nodes[k]
            if n['kind'] == 'metadata_root':
                own = metadata_root_result(n, p, records, deletions)
                dependencies = derived_result(n, nodes, decisions, withdrawals) if n['dependency_groups'] else result('keep', 'metadata_anchor')
                decisions[k] = dependencies if own['action'] == 'keep' and dependencies['action'] != 'keep' else own
            elif n['kind'] == 'memory_revision':
                own = root_result(n, p, records, deletions)
                dependencies = derived_result(n, nodes, decisions, withdrawals)
                decisions[k] = dependencies if own['action'] in ('keep', 'historical_only') and dependencies['action'] != 'keep' else own
            else:
                decisions[k] = derived_result(n, nodes, decisions, withdrawals)
            pending.remove(k)
    # Unsupported invalidation must remain visible and blocks restoration.
    # Propagate that hold to downstream consumers in the same pure plan.
    unsupported = {k for k, d in decisions.items() if d['action'] in ('invalidate', 'rebuild') and nodes[k]['invalidation'] != 'supported'}
    for k in unsupported:
        decisions[k] = result('hold', 'invalidation_' + nodes[k]['invalidation'])
    if unsupported:
        changed = True
        while changed:
            changed = False
            for k, n in nodes.items():
                if decisions[k]['action'] in ('keep', 'rebuild', 'invalidate', 'skip') and any(decisions.get(x, {}).get('action') == 'hold' for g in n['dependency_groups'] for x in g):
                    decisions[k] = result('hold', 'dependency_unresolved'); changed = True
    reconciled = True
    if p['operation'] == 'restore':
        fields = p['restore_fields']
        reconciled = all(fields['snapshot'][k] is not None and fields['snapshot'][k] == fields['readback'][k] for k in RESTORE_FIELDS)
        if not reconciled:
            for k, d in decisions.items():
                if d['action'] == 'keep':
                    decisions[k] = result('hold', 'restore_field_mismatch_or_unknown')
    resets = []
    for k, d in sorted(decisions.items()):
        if d == result('invalidate', 'revision_changed') and nodes[k]['kind'] == 'memory_revision':
            old = nodes[k]['ref']; current = records[entity(nodes[k]['scope'], old)]['ref']
            if current['revision'] > old['revision']:
                resets.append({'node': k, 'new_ref': current, 'action': 'new_pending_template_requires_DYN_ResetRevision', 'copy_old_state': False, 'activation_granted': False})
    out = {'protocol': p['protocol'], 'input_sha256': digest(p), 'objects': {k: decisions[k] for k in sorted(decisions)}, 'planned_invalidations': sorted(k for k,d in decisions.items() if d['action'] in ('invalidate', 'rebuild')), 'held': sorted(k for k,d in decisions.items() if d['action'] == 'hold'), 'rebuild': sorted(k for k,d in decisions.items() if d['action'] == 'rebuild'), 'new_revision_requests': resets, 'restore_fields_reconciled': reconciled if p['operation']=='restore' else None, 'restore_candidates': sorted(k for k,d in decisions.items() if d['action']=='keep') if p['operation']=='restore' else [], 'deletion_evidence': copy.deepcopy(p['deletion_evidence']), 'current_ledger_changed': p['snapshot']['ledger_sha256'] != p['current']['ledger_sha256'], 'model_only': True, 'actual_publication_authorized': False, 'requires_current_publication_refence': True, 'physical_erasure_verified_by_model': False, 'numeric_state_updates': 0, 'inverse_jumps': 0}
    if p['protocol'] == METADATA_PROTOCOL:
        out['metadata_profile'] = {'id': METADATA_PROFILE, 'source_commit': MAIN_SOURCE, 'coverage_sha256': metadata_binding(p), 'coverage_complete': p['metadata_profile']['coverage_status'] == 'complete', 'coverage_readback_reconciled': metadata_global_reason(p) is None if p['operation'] == 'restore' else None, 'learning_qualification': 'not_established'}
    return out


def validate_claim(value, claim):
    require(raw(plan(value)) == raw(claim), 'claim_mismatch')
    return True


def load_cases(filename):
    doc = decode((HERE / filename).read_bytes())
    obj(doc, ('protocol', 'cases')); require(doc['protocol'] == PROTOCOL); seq(doc['cases'], 128)
    return doc['cases']


class DeclaredSchedules(unittest.TestCase):
    def exercise(self, filename):
        names = set()
        for c in load_cases(filename):
            with self.subTest(case=c['id']):
                obj(c, ('id', 'schedule', 'input', 'expected')); label(c['id']); require(c['id'] not in names); names.add(c['id'])
                self.assertGreater(len(c['schedule']), 0)
                times = [x['at_ns'] for x in c['schedule']]
                self.assertEqual(times, sorted(times)); self.assertLessEqual(times[-1], c['input']['now_ns'])
                out = plan(c['input']); expected = c['expected']
                self.assertEqual({k:d['action'] for k,d in out['objects'].items()}, expected['actions'])
                for k, reason in expected['reasons'].items(): self.assertEqual(out['objects'][k]['reason'], reason)
                self.assertEqual([r['node'] for r in out['new_revision_requests']], expected['new_revision_roots'])
                self.assertEqual(out['restore_fields_reconciled'], expected['restore_fields_reconciled'])
                self.assertEqual(out['restore_candidates'], expected['restore_candidates'])
                self.assertFalse(out['actual_publication_authorized']); self.assertFalse(out['physical_erasure_verified_by_model'])
                self.assertEqual(out['deletion_evidence'], c['input']['deletion_evidence'])
    def test_dependency_schedules(self): self.exercise('dependency_cases.json')
    def test_restore_schedules(self): self.exercise('restore_cases.json')


class BoundaryTests(unittest.TestCase):
    def seed(self): return copy.deepcopy(load_cases('dependency_cases.json')[0]['input'])
    def test_input_immutable_and_repeatable(self):
        p=self.seed(); before=raw(p); a=plan(p); self.assertEqual(raw(p),before); self.assertEqual(a,plan(p)); self.assertEqual(a,plan(raw(p)))
    def test_claim_every_top_level_field(self):
        p=self.seed(); out=plan(p)
        for k in out:
            with self.subTest(field=k):
                bad=copy.deepcopy(out); del bad[k]
                with self.assertRaisesRegex(LifecycleError,'claim_mismatch'): validate_claim(p,bad)
        self.assertTrue(validate_claim(p,out))
    def test_extra_and_missing_fields(self):
        for target in ['root','current','node','ref','scope']:
            for mutation in ['extra','missing']:
                with self.subTest(target=target,mutation=mutation):
                    p=self.seed(); t={'root':p,'current':p['current'],'node':p['nodes'][0],'ref':p['nodes'][0]['ref'],'scope':p['scope']}[target]
                    if mutation=='extra':t['body']='canary_body_never_logged'
                    else:del t[next(iter(t))]
                    with self.assertRaises(LifecycleError) as raised:plan(p)
                    self.assertNotIn('canary_body_never_logged',str(raised.exception))
    def test_integer_precision_and_reject_bad_numbers(self):
        p=self.seed(); p['now_ns']=MAX_INT; p['current']['checked_at_ns']=MAX_INT; self.assertTrue(plan(p)['model_only'])
        for bad in [True,1.5,MIN_INT-1,MAX_INT+1,float('nan'),float('inf'),'100']:
            with self.subTest(value=repr(bad)):
                p=self.seed();p['now_ns']=bad
                with self.assertRaises(LifecycleError):plan(p)
    def test_revision_boolean_and_scope_bindings(self):
        for bad in [True,0,-1,1.5,'1',MAX_INT+1]:
            with self.subTest(revision=bad):
                p=self.seed();p['nodes'][0]['ref']['revision']=bad
                with self.assertRaises(LifecycleError):plan(p)
    def test_decode_boundaries(self):
        for bad in [b'',b'{} {}',b'\xff',b'{"a":1,"a":2}',b'{"a":NaN}',b'{"a":Infinity}',b'{"a":1e309}',b' '*(MAX_BYTES+1)]:
            with self.subTest(size=len(bad)):
                with self.assertRaises(LifecycleError):decode(bad)
    def test_wrong_namespace_no_native_auth(self):
        p=self.seed();p['nodes'][0]['ref']['namespace']='actual_native'
        with self.assertRaisesRegex(LifecycleError,'non_synthetic_namespace'):plan(p)
    def test_duplicate_ids_and_ambiguous_current(self):
        for which in ['nodes','records']:
            with self.subTest(which=which):
                p=self.seed(); t=p['nodes'] if which=='nodes' else p['current']['records'];t.append(copy.deepcopy(t[0]))
                with self.assertRaises(LifecycleError):plan(p)
    def test_missing_vs_known_empty_ledger(self):
        p=self.seed();self.assertEqual(plan(p)['held'],[]);p['current']['deletions']=None;p['current']['ledger_sha256']=None
        self.assertEqual(plan(p)['held'],sorted(n['id'] for n in p['nodes']))
    def test_unknown_current_never_implies_false_or_deleted(self):
        p=self.seed();p['current']['records']=[];out=plan(p);self.assertTrue(out['held']);self.assertEqual(out['planned_invalidations'],[])
    def test_future_clock_and_history_boundaries(self):
        for field in ['snapshot','current']:
            with self.subTest(field=field):
                p=self.seed(); p[field]['captured_at_ns' if field=='snapshot' else 'checked_at_ns']=p['now_ns']+1
                with self.assertRaises(LifecycleError):plan(p)
    def test_fact_controls_not_public_commands(self):
        p=self.seed();p['fact_withdrawals']=[{'scope':p['scope'],'memory':p['nodes'][1]['ref'],'fact_id':'fact_a','basis':'caller_withdrawal_is_authorization'}]
        with self.assertRaisesRegex(LifecycleError,'unsupported_fact_basis'):plan(p)
    def test_evidence_unknown_not_complete(self):
        c=next(x for x in load_cases('restore_cases.json') if x['id']=='quarantine_is_not_physical_erasure');p=copy.deepcopy(c['input']);out=plan(p)
        self.assertEqual(out['deletion_evidence'][0]['facets']['physical_purged']['status'],'unknown');self.assertFalse(out['physical_erasure_verified_by_model'])
        p['deletion_evidence'][0]['facets']['physical_purged']['status']='observed_complete'
        with self.assertRaises(LifecycleError):plan(p)
    def test_new_revision_does_not_copy_values_or_claim_activation(self):
        c=next(x for x in load_cases('dependency_cases.json') if x['id']=='content_revision_confirmed');out=plan(c['input']);self.assertEqual(len(out['new_revision_requests']),1)
        r=out['new_revision_requests'][0];self.assertFalse(r['copy_old_state']);self.assertFalse(r['activation_granted']);self.assertEqual(out['inverse_jumps'],0)
    def test_returned_values_not_alias_input(self):
        p=self.seed();a=plan(p);a['objects'].clear();self.assertTrue(plan(p)['objects'])
    def test_restore_every_field_missing_unknown_or_changed(self):
        c=load_cases('restore_cases.json')[0]
        for field in RESTORE_FIELDS:
            for value in [None,'f'*64]:
                with self.subTest(field=field,value=value):
                    p=copy.deepcopy(c['input']);p['restore_fields']['readback'][field]=value;out=plan(p)
                    self.assertFalse(out['restore_fields_reconciled']);self.assertEqual(out['restore_candidates'],[])
    def test_input_node_order_does_not_change_decisions(self):
        p=self.seed();a=plan(p);p['nodes'].reverse();b=plan(p);self.assertEqual(a['objects'],b['objects']);self.assertNotEqual(a['input_sha256'],b['input_sha256'])
    def test_restore_in_fresh_python_process(self):
        fixture=load_cases('restore_cases.json')[0]['input']
        script="import json,runpy,sys;m=runpy.run_path(sys.argv[1],run_name='lifecycle_fresh_process');sys.stdout.buffer.write(m['raw'](m['plan'](sys.stdin.buffer.read())))"
        completed=subprocess.run([sys.executable,'-B','-c',script,str(HERE/'test_lifecycle.py')],input=raw(fixture),capture_output=True,timeout=10,check=False)
        self.assertEqual(completed.returncode,0);self.assertEqual(completed.stderr,b'');self.assertEqual(completed.stdout,raw(plan(fixture)))
    def test_fixed_dependency_bytes(self):
        c=decode((HERE/'lifecycle_contract.json').read_bytes())
        roots=[*HERE.parents,Path.cwd(),*Path.cwd().parents]
        root=next((x for x in roots if (x/'docs/research/memory-harness/dynamics_policy_contract.json').is_file()),None)
        self.assertIsNotNone(root,'fixed lifecycle dependencies not found; required check cannot skip')
        for path,expected in c['fixed_dependencies'].items():
            with self.subTest(path=path):self.assertEqual(hashlib.sha256((root/path).read_bytes()).hexdigest(),expected)
    def test_current_source_also_checked_for_historical_read(self):
        c=next(x for x in load_cases('dependency_cases.json') if x['id']=='historical_same_body_metadata_not_execution');p=copy.deepcopy(c['input']);p['current']['records'][0]['ref']['revision']=2
        out=plan(p);self.assertEqual(out['objects']['source'],result('invalidate','revision_changed'));self.assertEqual(out['objects']['memory'],result('invalidate','dependency_invalidated'))
    def test_wrong_memory_provenance_kind_holds(self):
        p=self.seed();p['nodes'][1]['dependency_groups']=[['state_a']];p['nodes'][4]['dependency_groups']=[['source']]
        self.assertEqual(plan(p)['objects']['memory'],result('hold','memory_origin_not_source'))
    def test_known_false_approved_is_not_missing(self):
        p=self.seed();p['current']['records'][1]['approved']=False;self.assertEqual(plan(p)['objects']['memory'],result('skip','not_active_approved'))
        p['current']['records'][1]['approved']=None
        with self.assertRaisesRegex(LifecycleError,'invalid_boolean'):plan(p)
    def test_evidence_each_target_kept_separate(self):
        c=next(x for x in load_cases('restore_cases.json') if x['id']=='quarantine_is_not_physical_erasure');p=copy.deepcopy(c['input']);foreign=copy.deepcopy(p['deletion_evidence'][0]);foreign['scope']['user_id']='fixture_other';foreign['facets']['serving_blocked']={'status':'unknown','receipt_ref':None};p['deletion_evidence'].append(foreign)
        out=plan(p);self.assertEqual(out['deletion_evidence'],p['deletion_evidence']);self.assertEqual(out['deletion_evidence'][1]['facets']['serving_blocked']['status'],'unknown')
    def test_bounded_graph_rejects_oversize(self):
        p=self.seed();p['nodes']=p['nodes']*37
        with self.assertRaises(LifecycleError):plan(p)
    def test_delete_dominates_old_history_and_retry(self):
        c=next(x for x in load_cases('dependency_cases.json') if x['id']=='historical_same_body_metadata_not_execution');p=copy.deepcopy(c['input']);p['current']['deletions']=[{'scope':p['scope'],'collection':'memories','id':'memory_one'}]
        out=plan(p);self.assertEqual(out['objects']['memory'],result('invalidate','deleted'));self.assertFalse(out['actual_publication_authorized'])
    def test_rejected_input_cannot_poison_subsequent_plan(self):
        p=self.seed();before=plan(p);bad=copy.deepcopy(p);bad['current']['records'][0]['ref']['revision']=True
        with self.assertRaises(LifecycleError):plan(bad)
        self.assertEqual(plan(p),before)
    def test_contract_limits_are_explicit(self):
        c=decode((HERE/'lifecycle_contract.json').read_bytes());self.assertEqual(c['protocol'],PROTOCOL);self.assertEqual(c['verification_scope'],'synthetic_model');self.assertFalse(c['native_lifecycle_producer_implemented']);self.assertGreater(len(c['unresolved_B']),0)


class SignedClockTests(unittest.TestCase):
    def seed(self):
        return copy.deepcopy(next(c['input'] for c in load_cases('dependency_cases.json') if c['id']=='independent_negative_epoch_regression'))
    def historical_seed(self):
        p=self.seed();p['now_ns']=0;p['snapshot']['captured_at_ns']=-10;p['current']['checked_at_ns']=0;p['query']='historical';p['as_of_ns']=-1
        memory=p['current']['records'][1]
        def observation(at,creation):
            return {'observed_at_ns':at,'revision':memory['ref']['revision'],'status':'active','approved':True,'content_sha256':memory['ref']['content_sha256'],'withheld':False,'creation':creation,'effective_at_ns':None,'expires_at_ns':None}
        memory['history']=[observation(-9,True),observation(-5,False)]
        return p
    def assert_historical(self,p):
        out=plan(p);expected={n['id']:'skip' for n in p['nodes']};expected['src']='keep';expected['memo']='historical_only'
        self.assertEqual({k:d['action'] for k,d in out['objects'].items()},expected)
        self.assertEqual(out['objects']['memo']['reason'],'historical_body_current_hash')
    def test_signed_epoch_and_int64_limits_are_known(self):
        for now,snapshot,checked in [(-1,-3,-2),(MIN_INT,MIN_INT,MIN_INT),(MAX_INT,MAX_INT,MAX_INT),(0,0,0)]:
            with self.subTest(now=now,snapshot=snapshot,checked=checked):
                p=self.seed();p['now_ns']=now;p['snapshot']['captured_at_ns']=snapshot;p['current']['checked_at_ns']=checked
                self.assertEqual({d['action'] for d in plan(p)['objects'].values()},{'keep'})
                self.assertEqual(plan(p),plan(raw(p)))
    def test_all_timestamp_fields_reject_overflow_and_boolean(self):
        paths=[('now_ns',),('snapshot','captured_at_ns'),('current','checked_at_ns'),('as_of_ns',),('current','records',1,'effective_at_ns'),('current','records',1,'expires_at_ns'),('current','records',1,'history',0,'observed_at_ns'),('current','records',1,'history',0,'effective_at_ns'),('current','records',1,'history',0,'expires_at_ns')]
        for path in paths:
            for bad in [MIN_INT-1,MAX_INT+1,True,False,1.5,'0']:
                with self.subTest(path=path,value=repr(bad)):
                    p=self.historical_seed();p['now_ns']=MAX_INT;p['current']['checked_at_ns']=MAX_INT
                    t=p
                    for key in path[:-1]:t=t[key]
                    t[path[-1]]=bad
                    with self.assertRaisesRegex(LifecycleError,'^invalid_integer$'):plan(p)
    def test_missing_clock_distinct_from_zero(self):
        for path in [('now_ns',),('snapshot','captured_at_ns'),('current','checked_at_ns'),('as_of_ns',),('current','records',1,'history',0,'observed_at_ns')]:
            with self.subTest(path=path):
                p=self.historical_seed();t=p
                for key in path[:-1]:t=t[key]
                t[path[-1]]=None
                with self.assertRaisesRegex(LifecycleError,'^invalid_integer$'):plan(p)
        p=self.historical_seed();p['as_of_ns']=0;self.assert_historical(p)
    def test_counter_helper_and_positive_revisions_not_signed(self):
        for value,minimum in [(-1,0),(True,0),(MIN_INT,0),(0,1),(-1,1)]:
            with self.subTest(value=value,minimum=minimum):
                with self.assertRaisesRegex(LifecycleError,'^invalid_integer$'):integer(value,minimum)
        self.assertIsNone(integer(0));self.assertIsNone(integer(1,1))
        for target in ['snapshot_ref','current_ref','history_ref']:
            with self.subTest(target=target):
                p=self.historical_seed();r={'snapshot_ref':p['nodes'][0]['ref'],'current_ref':p['current']['records'][0]['ref'],'history_ref':p['current']['records'][1]['history'][0]}[target];r['revision']=-1
                with self.assertRaisesRegex(LifecycleError,'^invalid_integer$'):plan(p)
    def test_negative_history_and_equal_time_last_entry(self):
        p=self.historical_seed();self.assert_historical(p)
        history=p['current']['records'][1]['history'];history[0]['observed_at_ns']=-5;history[0]['approved']=False
        self.assert_historical(p)
        history[0]['approved']=True;history[1]['approved']=False
        self.assertEqual(plan(p)['objects']['memo'],result('skip','historically_ineligible'))
    def test_history_non_decreasing_and_future_still_enforced(self):
        for second in [-10,1]:
            with self.subTest(second=second):
                p=self.historical_seed();p['current']['records'][1]['history'][1]['observed_at_ns']=second
                with self.assertRaisesRegex(LifecycleError,'^history_clock$'):plan(p)
    def test_negative_clocks_keep_temporal_guards(self):
        for now,snapshot,checked,error in [(-5,-4,-5,'snapshot_clock'),(-5,-6,-4,'current_clock')]:
            with self.subTest(error=error):
                p=self.seed();p['now_ns']=now;p['snapshot']['captured_at_ns']=snapshot;p['current']['checked_at_ns']=checked
                with self.assertRaisesRegex(LifecycleError,'^'+error+'$'):plan(p)
        p=self.seed();p['now_ns']=0;p['snapshot']['captured_at_ns']=-3;p['current']['checked_at_ns']=-4
        self.assertEqual({(x['action'],x['reason']) for x in plan(p)['objects'].values()},{('hold','stale_current_metadata')})
        p=self.seed();p['query']='historical';p['as_of_ns']=0
        with self.assertRaisesRegex(LifecycleError,'^future_history$'):plan(p)
    def test_effective_expiry_zero_and_negative_are_not_missing(self):
        for effective,expires,expected in [(-1,None,'keep'),(0,None,'keep'),(1,None,'skip'),(None,-1,'skip'),(None,0,'skip'),(None,1,'keep')]:
            with self.subTest(effective=effective,expires=expires):
                p=self.seed();p['now_ns']=0;p['current']['checked_at_ns']=0;r=p['current']['records'][1];r['effective_at_ns']=effective;r['expires_at_ns']=expires
                self.assertEqual(plan(p)['objects']['memo']['action'],expected)
        p=self.historical_seed();p['as_of_ns']=0;h=p['current']['records'][1]['history'][1];h['effective_at_ns']=0;self.assert_historical(p)
        h['expires_at_ns']=0;self.assertEqual(plan(p)['objects']['memo'],result('skip','historically_ineligible'))
    def test_min_max_history_first_entry_has_no_fake_lower_bound(self):
        for instant in [MIN_INT,0,MAX_INT]:
            with self.subTest(instant=instant):
                p=self.historical_seed();p['now_ns']=instant;p['snapshot']['captured_at_ns']=instant;p['current']['checked_at_ns']=instant;p['as_of_ns']=instant;h=p['current']['records'][1]['history'][0];h['observed_at_ns']=instant;p['current']['records'][1]['history']=[h]
                self.assert_historical(p)
    def test_restore_and_retry_accept_negative_epoch_without_grant(self):
        for operation in ['read','retry','publish','restore']:
            with self.subTest(operation=operation):
                p=self.seed();p['operation']=operation
                if operation=='restore':p['restore_fields']=copy.deepcopy(load_cases('restore_cases.json')[0]['input']['restore_fields'])
                out=plan(p);self.assertEqual({d['action'] for d in out['objects'].values()},{'keep'});self.assertFalse(out['actual_publication_authorized']);self.assertFalse(out['physical_erasure_verified_by_model'])
                if operation=='restore':self.assertTrue(out['restore_fields_reconciled']);self.assertEqual(out['restore_candidates'],sorted(n['id'] for n in p['nodes']))


DOMAIN_VECTORS = [{'name': 'id_plain', 'kind': 'id', 'value': 'x', 'expected': True}, {'name': 'id_bytes256', 'kind': 'id', 'value': 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx', 'expected': True}, {'name': 'id_bytes257', 'kind': 'id', 'value': 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx', 'expected': False}, {'name': 'id_emoji256', 'kind': 'id', 'value': '😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀', 'expected': True}, {'name': 'id_emoji257', 'kind': 'id', 'value': '😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀a', 'expected': False}, {'name': 'id_korean256', 'kind': 'id', 'value': '가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가a', 'expected': True}, {'name': 'id_empty', 'kind': 'id', 'value': '', 'expected': False}, {'name': 'id_C1inside', 'kind': 'id', 'value': 'x\x85y', 'expected': False}, {'name': 'id_NBSPinside', 'kind': 'id', 'value': 'x\xa0y', 'expected': True}, {'name': 'id_Cfstart', 'kind': 'id', 'value': '\u200bx', 'expected': True}, {'name': 'id_BOMCf', 'kind': 'id', 'value': '\ufeffx', 'expected': True}, {'name': 'id_joinerCf', 'kind': 'id', 'value': 'x\u200dy', 'expected': True}, {'name': 'id_C0inside', 'kind': 'id', 'value': 'x\x1cy', 'expected': False}, {'name': 'id_GoSpaceLeading0', 'kind': 'id', 'value': '\tx', 'expected': False}, {'name': 'id_GoSpaceTrailing0', 'kind': 'id', 'value': 'x\t', 'expected': False}, {'name': 'id_GoSpaceLeading1', 'kind': 'id', 'value': '\nx', 'expected': False}, {'name': 'id_GoSpaceTrailing1', 'kind': 'id', 'value': 'x\n', 'expected': False}, {'name': 'id_GoSpaceLeading2', 'kind': 'id', 'value': '\x0bx', 'expected': False}, {'name': 'id_GoSpaceTrailing2', 'kind': 'id', 'value': 'x\x0b', 'expected': False}, {'name': 'id_GoSpaceLeading3', 'kind': 'id', 'value': '\x0cx', 'expected': False}, {'name': 'id_GoSpaceTrailing3', 'kind': 'id', 'value': 'x\x0c', 'expected': False}, {'name': 'id_GoSpaceLeading4', 'kind': 'id', 'value': '\rx', 'expected': False}, {'name': 'id_GoSpaceTrailing4', 'kind': 'id', 'value': 'x\r', 'expected': False}, {'name': 'id_GoSpaceLeading5', 'kind': 'id', 'value': ' x', 'expected': False}, {'name': 'id_GoSpaceTrailing5', 'kind': 'id', 'value': 'x ', 'expected': False}, {'name': 'id_GoSpaceLeading6', 'kind': 'id', 'value': '\x85x', 'expected': False}, {'name': 'id_GoSpaceTrailing6', 'kind': 'id', 'value': 'x\x85', 'expected': False}, {'name': 'id_GoSpaceLeading7', 'kind': 'id', 'value': '\xa0x', 'expected': False}, {'name': 'id_GoSpaceTrailing7', 'kind': 'id', 'value': 'x\xa0', 'expected': False}, {'name': 'id_GoSpaceLeading8', 'kind': 'id', 'value': '\u1680x', 'expected': False}, {'name': 'id_GoSpaceTrailing8', 'kind': 'id', 'value': 'x\u1680', 'expected': False}, {'name': 'id_GoSpaceLeading9', 'kind': 'id', 'value': '\u2000x', 'expected': False}, {'name': 'id_GoSpaceTrailing9', 'kind': 'id', 'value': 'x\u2000', 'expected': False}, {'name': 'id_GoSpaceLeading10', 'kind': 'id', 'value': '\u2001x', 'expected': False}, {'name': 'id_GoSpaceTrailing10', 'kind': 'id', 'value': 'x\u2001', 'expected': False}, {'name': 'id_GoSpaceLeading11', 'kind': 'id', 'value': '\u2002x', 'expected': False}, {'name': 'id_GoSpaceTrailing11', 'kind': 'id', 'value': 'x\u2002', 'expected': False}, {'name': 'id_GoSpaceLeading12', 'kind': 'id', 'value': '\u2003x', 'expected': False}, {'name': 'id_GoSpaceTrailing12', 'kind': 'id', 'value': 'x\u2003', 'expected': False}, {'name': 'id_GoSpaceLeading13', 'kind': 'id', 'value': '\u2004x', 'expected': False}, {'name': 'id_GoSpaceTrailing13', 'kind': 'id', 'value': 'x\u2004', 'expected': False}, {'name': 'id_GoSpaceLeading14', 'kind': 'id', 'value': '\u2005x', 'expected': False}, {'name': 'id_GoSpaceTrailing14', 'kind': 'id', 'value': 'x\u2005', 'expected': False}, {'name': 'id_GoSpaceLeading15', 'kind': 'id', 'value': '\u2006x', 'expected': False}, {'name': 'id_GoSpaceTrailing15', 'kind': 'id', 'value': 'x\u2006', 'expected': False}, {'name': 'id_GoSpaceLeading16', 'kind': 'id', 'value': '\u2007x', 'expected': False}, {'name': 'id_GoSpaceTrailing16', 'kind': 'id', 'value': 'x\u2007', 'expected': False}, {'name': 'id_GoSpaceLeading17', 'kind': 'id', 'value': '\u2008x', 'expected': False}, {'name': 'id_GoSpaceTrailing17', 'kind': 'id', 'value': 'x\u2008', 'expected': False}, {'name': 'id_GoSpaceLeading18', 'kind': 'id', 'value': '\u2009x', 'expected': False}, {'name': 'id_GoSpaceTrailing18', 'kind': 'id', 'value': 'x\u2009', 'expected': False}, {'name': 'id_GoSpaceLeading19', 'kind': 'id', 'value': '\u200ax', 'expected': False}, {'name': 'id_GoSpaceTrailing19', 'kind': 'id', 'value': 'x\u200a', 'expected': False}, {'name': 'id_GoSpaceLeading20', 'kind': 'id', 'value': '\u2028x', 'expected': False}, {'name': 'id_GoSpaceTrailing20', 'kind': 'id', 'value': 'x\u2028', 'expected': False}, {'name': 'id_GoSpaceLeading21', 'kind': 'id', 'value': '\u2029x', 'expected': False}, {'name': 'id_GoSpaceTrailing21', 'kind': 'id', 'value': 'x\u2029', 'expected': False}, {'name': 'id_GoSpaceLeading22', 'kind': 'id', 'value': '\u202fx', 'expected': False}, {'name': 'id_GoSpaceTrailing22', 'kind': 'id', 'value': 'x\u202f', 'expected': False}, {'name': 'id_GoSpaceLeading23', 'kind': 'id', 'value': '\u205fx', 'expected': False}, {'name': 'id_GoSpaceTrailing23', 'kind': 'id', 'value': 'x\u205f', 'expected': False}, {'name': 'id_GoSpaceLeading24', 'kind': 'id', 'value': '\u3000x', 'expected': False}, {'name': 'id_GoSpaceTrailing24', 'kind': 'id', 'value': 'x\u3000', 'expected': False}, {'name': 'scope_workspace_id_empty', 'kind': 'scope', 'value': {'workspace_id': '', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_workspace_id_dot', 'kind': 'scope', 'value': {'workspace_id': '.', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_workspace_id_dotdot', 'kind': 'scope', 'value': {'workspace_id': '..', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_workspace_id_slash', 'kind': 'scope', 'value': {'workspace_id': '/', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_workspace_id_backslash', 'kind': 'scope', 'value': {'workspace_id': '\\', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_workspace_id_C1', 'kind': 'scope', 'value': {'workspace_id': '\x85', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_workspace_id_C0', 'kind': 'scope', 'value': {'workspace_id': '\x00', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_workspace_id_space', 'kind': 'scope', 'value': {'workspace_id': ' ', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_workspace_id_NBSP', 'kind': 'scope', 'value': {'workspace_id': '\xa0', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_workspace_id_Cf', 'kind': 'scope', 'value': {'workspace_id': '\u200b', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_workspace_id_bytes255', 'kind': 'scope', 'value': {'workspace_id': 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_workspace_id_bytes256', 'kind': 'scope', 'value': {'workspace_id': 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_workspace_id_unicode255', 'kind': 'scope', 'value': {'workspace_id': '가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_workspace_id_unicode256', 'kind': 'scope', 'value': {'workspace_id': '😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_project_id_empty', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': '', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_project_id_dot', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': '.', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_project_id_dotdot', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': '..', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_project_id_slash', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': '/', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_project_id_backslash', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': '\\', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_project_id_C1', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': '\x85', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_project_id_C0', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': '\x00', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_project_id_space', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': ' ', 'user_id': 'u', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_project_id_NBSP', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': '\xa0', 'user_id': 'u', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_project_id_Cf', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': '\u200b', 'user_id': 'u', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_project_id_bytes255', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx', 'user_id': 'u', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_project_id_bytes256', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_project_id_unicode255', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': '가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가', 'user_id': 'u', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_project_id_unicode256', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': '😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀', 'user_id': 'u', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_user_id_empty', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': '', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_user_id_dot', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': '.', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_user_id_dotdot', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': '..', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_user_id_slash', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': '/', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_user_id_backslash', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': '\\', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_user_id_C1', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': '\x85', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_user_id_C0', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': '\x00', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_user_id_space', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': ' ', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_user_id_NBSP', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': '\xa0', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_user_id_Cf', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': '\u200b', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_user_id_bytes255', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_user_id_bytes256', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_user_id_unicode255', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': '가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가', 'agent_id': 'a'}, 'expected': True}, {'name': 'scope_user_id_unicode256', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': '😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀', 'agent_id': 'a'}, 'expected': False}, {'name': 'scope_agent_id_empty', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': ''}, 'expected': True}, {'name': 'scope_agent_id_dot', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': '.'}, 'expected': True}, {'name': 'scope_agent_id_dotdot', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': '..'}, 'expected': True}, {'name': 'scope_agent_id_slash', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': '/'}, 'expected': True}, {'name': 'scope_agent_id_backslash', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': '\\'}, 'expected': True}, {'name': 'scope_agent_id_C1', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': '\x85'}, 'expected': True}, {'name': 'scope_agent_id_C0', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': '\x00'}, 'expected': True}, {'name': 'scope_agent_id_space', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': ' '}, 'expected': True}, {'name': 'scope_agent_id_NBSP', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': '\xa0'}, 'expected': True}, {'name': 'scope_agent_id_Cf', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': '\u200b'}, 'expected': True}, {'name': 'scope_agent_id_bytes255', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'}, 'expected': True}, {'name': 'scope_agent_id_bytes256', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'}, 'expected': False}, {'name': 'scope_agent_id_unicode255', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': '가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가가'}, 'expected': True}, {'name': 'scope_agent_id_unicode256', 'kind': 'scope', 'value': {'workspace_id': 'w', 'project_id': 'p', 'user_id': 'u', 'agent_id': '😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀😀'}, 'expected': False}]


class MainMetadataTests(unittest.TestCase):
    def seed(self, restore=False):
        name = 'main_eval_restore_complete_profile' if restore else 'main_eval_required_and_graph'
        return copy.deepcopy(next(c['input'] for c in load_cases('restore_cases.json' if restore else 'dependency_cases.json') if c['id'] == name))
    def mutate_current(self, p, collection):
        return next(r for r in p['current']['records'] if r['ref']['collection'] == collection)
    def test_literal_projection_and_input_hashes(self):
        p = self.seed(); out = plan(p)
        self.assertEqual(p['metadata_profile']['extraction_sha256'], 'f179521dc3a6a22fda872e763a994450f91f5cda14026b97530f95299724f324')
        self.assertEqual(out['metadata_profile']['coverage_sha256'], '7cd12e3491d47ee0f61bed7f66e222676121de80592b1d627bce89ce7363f5f0')
        self.assertEqual(out['input_sha256'], 'd0a2ca6a7188e3c5b5024dbf041e6b8ec6d1aebe5c421c388f3350e775488198')
        self.assertEqual({k:d['action'] for k,d in out['objects'].items()}, dict.fromkeys(('source','memory','evaluation','task','run','trial','calibration','state','receipt','checkpoint','snapshot'), 'keep'))
        self.assertFalse(out['actual_publication_authorized']); self.assertEqual(out['metadata_profile']['learning_qualification'], 'not_established')
        self.assertEqual(out, plan(raw(p))); self.assertIsNone(out['restore_fields_reconciled'])
    def test_all_seven_current_ref_hash_missing_scope(self):
        for collection in METADATA_COLLECTIONS:
            for mutation in ('missing','revision','hash','scope'):
                with self.subTest(collection=collection, mutation=mutation):
                    p=self.seed(); r=self.mutate_current(p,collection)
                    if mutation=='missing': p['current']['records'].remove(r)
                    elif mutation=='revision': r['ref']['revision']+=1
                    elif mutation=='hash': r['ref']['content_sha256']='f'*64
                    else: r['scope']['user_id']='foreign_person'
                    out=plan(p); node=next(n for n in p['nodes'] if n['ref'] and n['ref']['collection']==collection)
                    self.assertEqual(out['objects'][node['id']]['action'],'hold'); self.assertEqual(out['objects']['snapshot']['action'],'hold')
    def test_all_seven_deleted_roots_cannot_restore(self):
        for collection in METADATA_COLLECTIONS:
            with self.subTest(collection=collection):
                p=self.seed(True);r=self.mutate_current(p,collection);p['current']['deletions']=[{'scope':r['scope'],'collection':collection,'id':r['ref']['id']}]
                out=plan(p);node=next(n for n in p['nodes'] if n['ref'] and n['ref']['collection']==collection)
                self.assertEqual(out['objects'][node['id']]['action'],'invalidate');self.assertEqual(out['objects']['snapshot']['action'],'invalidate');self.assertNotIn('snapshot',out['restore_candidates'])
    def test_every_required_edge_cannot_be_dropped_or_alternative(self):
        for node in ('memory','evaluation','task','run','trial','calibration'):
            for mutation in ('drop','or'):
                with self.subTest(node=node,mutation=mutation):
                    p=self.seed();n=next(n for n in p['nodes'] if n['id']==node)
                    n['dependency_groups']=[] if mutation=='drop' else [n['dependency_groups'][0], ['state']]
                    with self.assertRaises(LifecycleError):plan(p)
    def test_all_extraction_channels_bound_to_coverage(self):
        for field in ('sources','target','relations','spec_ref','trial_ref','calibration_ref'):
            with self.subTest(field=field):
                p=self.seed();r=next(r for r in p['root_metadata'] if r['ref']['collection']==('eval_trials' if field=='relations' else 'evaluations'))
                if field in ('sources','relations'):r[field]=[]
                elif field=='target':r[field]=None
                else:r['evaluation'][field]=None
                with self.assertRaises(LifecycleError):plan(p)
    def test_extraction_claim_cannot_be_rewritten_without_rebinding(self):
        p=self.seed();r=next(r for r in p['root_metadata'] if r['ref']['collection']=='evaluations');r['evaluation']['eval_type']='automated_check';r['evaluation']['calibration_ref']=None
        n=next(n for n in p['nodes'] if n['id']=='evaluation');n['dependency_groups'][0].remove('calibration')
        with self.assertRaisesRegex(LifecycleError,'^metadata_extraction_binding$'):plan(p)
    def test_six_equal_digests_do_not_hide_incomplete_graph(self):
        for mutation in ('root','descriptor','coverage_root','edge'):
            with self.subTest(mutation=mutation):
                p=self.seed(True)
                if mutation=='root':p['nodes']=[n for n in p['nodes'] if n['id']!='calibration']
                elif mutation=='descriptor':p['root_metadata'].pop()
                elif mutation=='coverage_root':p['metadata_profile']['expected_roots'].pop()
                else:next(n for n in p['nodes'] if n['id']=='evaluation')['dependency_groups'][0].remove('calibration')
                self.assertEqual(p['restore_fields']['snapshot'],p['restore_fields']['readback'])
                with self.assertRaises(LifecycleError):plan(p)
    def test_profile_readback_is_separate_from_six_fields(self):
        for field in ('snapshot_coverage_sha256','readback_coverage_sha256'):
            for value in (None,'f'*64):
                with self.subTest(field=field,value=value):
                    p=self.seed(True);p['metadata_profile'][field]=value;out=plan(p)
                    self.assertTrue(out['restore_fields_reconciled']);self.assertFalse(out['metadata_profile']['coverage_readback_reconciled']);self.assertEqual(out['restore_candidates'],[])
                    self.assertEqual({d['reason']for d in out['objects'].values()},{'metadata_profile_readback'})
    def test_source_version_collection_unsupported_extra_keys(self):
        for mutation in ('source','collections','unsupported','note','by','profile_extra'):
            with self.subTest(mutation=mutation):
                p=self.seed()
                if mutation=='source':p['metadata_profile']['source_commit']='b'*40
                elif mutation=='collections':p['metadata_profile']['collections'].reverse()
                elif mutation=='unsupported':p['root_metadata'][0]['target']={'namespace':'fixture','collection':'blobs','id':'unsupported','revision':1,'content_sha256':'f'*64}
                elif mutation=='profile_extra':p['metadata_profile']['accepted']=True
                else:p['current']['records'][1]['removal_'+mutation]='private_reason_never_logged'
                with self.assertRaises(LifecycleError):plan(p)
    def test_unknown_coverage_never_zero_or_complete(self):
        p=self.seed(True);p['metadata_profile']['coverage_status']='unknown';out=plan(p)
        self.assertEqual({d['action'] for d in out['objects'].values()},{'hold'});self.assertFalse(out['metadata_profile']['coverage_complete']);self.assertEqual(out['restore_candidates'],[])
    def test_removal_status_not_physical_erasure_or_fact_withdrawal(self):
        p=self.seed(True);self.mutate_current(p,'memories')['status']='rejected';out=plan(p)
        self.assertEqual(out['objects']['memory']['action'],'skip');self.assertNotIn('snapshot',out['restore_candidates']);self.assertEqual(out['new_revision_requests'],[])
        self.assertFalse(out['physical_erasure_verified_by_model']);self.assertEqual(out['numeric_state_updates'],0)
        for col in ('evaluations','eval_tasks','eval_runs','eval_trials','judge_calibrations'):
            self.assertFalse(self.mutate_current(p,col)['approved'])
        self.assertEqual(plan(self.seed())['objects']['evaluation']['action'],'keep')
    def test_claim_tampering_is_not_metadata_authority(self):
        p=self.seed(True);out=plan(p)
        for field,value in [('learning_qualification','qualified'),('coverage_sha256','f'*64),('coverage_readback_reconciled',False),('coverage_complete',False)]:
            with self.subTest(field=field):
                forged=copy.deepcopy(out);forged['metadata_profile'][field]=value
                with self.assertRaisesRegex(LifecycleError,'^claim_mismatch$'):validate_claim(p,forged)
    def test_profile_new_process_and_no_input_alias(self):
        p=self.seed(True);before=raw(p);expected=plan(p)
        script="import json,runpy,sys;m=runpy.run_path(sys.argv[1],run_name='child');sys.stdout.buffer.write(m['raw'](m['plan'](sys.stdin.buffer.read())))"
        run=subprocess.run([sys.executable,'-B','-c',script,str(Path(__file__).resolve())],input=before,capture_output=True,timeout=10,check=False)
        self.assertEqual(run.returncode,0);self.assertEqual(run.stderr,b'');self.assertEqual(run.stdout,raw(expected));self.assertEqual(raw(p),before)
    def test_new_profile_signed_clock_and_boolean_boundary(self):
        for value in (MIN_INT,-1,0,MAX_INT):
            with self.subTest(value=value):
                p=self.seed();p['now_ns']=value;p['snapshot']['captured_at_ns']=value;p['current']['checked_at_ns']=value
                self.assertEqual({d['action']for d in plan(p)['objects'].values()},{'keep'})
        for value in (True,MIN_INT-1,MAX_INT+1):
            with self.subTest(invalid=value):
                p=self.seed();p['now_ns']=value
                with self.assertRaisesRegex(LifecycleError,'^invalid_integer$'):plan(p)


    def test_declared_cycle_and_downstream_never_self_support(self):
        p=copy.deepcopy(next(c['input'] for c in load_cases('dependency_cases.json') if c['id']=='main_eval_cycle_closure'))
        out=plan(p)
        self.assertEqual({k for k,d in out['objects'].items() if d['action']=='keep'}, {'source','memory','task','calibration'})
        self.assertEqual(set(out['held']), {'run','trial','evaluation','state','receipt','checkpoint','snapshot'})
        self.assertEqual({out['objects'][k]['reason'] for k in out['held']}, {'dependency_cycle_or_dependent'})
    def test_duplicate_roots_and_foreign_required_ref_reject(self):
        for mutation in ('manifest','descriptor','node_ref','foreign'):
            with self.subTest(mutation=mutation):
                p=self.seed()
                if mutation=='manifest':p['metadata_profile']['expected_roots'].append(copy.deepcopy(p['metadata_profile']['expected_roots'][0]))
                elif mutation=='descriptor':p['root_metadata'].append(copy.deepcopy(p['root_metadata'][0]))
                elif mutation=='node_ref':
                    n=copy.deepcopy(p['nodes'][0]);n['id']='duplicate_root';p['nodes'].append(n)
                else:
                    for n in p['nodes']:
                        if n['id']=='run':n['scope']['user_id']='foreign_person'
                    for row in p['root_metadata']+p['metadata_profile']['expected_roots']:
                        if row['ref']['collection']=='eval_runs':row['scope']['user_id']='foreign_person'
                with self.assertRaises(LifecycleError):plan(p)

    def test_v2_external_strings_exact_main_rules(self):
        vectors = DOMAIN_VECTORS
        for row in vectors:
            with self.subTest(case=row['name']):
                try:
                    if row['kind']=='scope':scope(row['value'], True)
                    else:metadata_id(row['value'])
                    accepted=True
                except LifecycleError:accepted=False
                self.assertEqual(accepted,row['expected'])
    def test_v2_full_unicode_wire_preserves_values(self):
        p=self.seed();native_scope={'workspace_id':'가'*85,'project_id':'project\u200b','user_id':'person/\u0085','agent_id':''}
        def replace(value):
            if isinstance(value,dict):
                if set(value)==set(SCOPE_FIELDS):value.update(native_scope)
                elif set(value)=={'namespace','collection','id','revision','content_sha256'}:value['id']='😀'*64
                else:
                    for child in value.values():replace(child)
            elif isinstance(value,list):
                for child in value:replace(child)
        replace(p)
        extraction_payload=(json.dumps(p['root_metadata'],sort_keys=True,separators=(',',':'),ensure_ascii=False)+'\n').encode()
        profile=p['metadata_profile'];profile['extraction_sha256']=hashlib.sha256(b'sys04-main-eval-roots-v1:extraction\0'+extraction_payload).hexdigest()
        out=plan(p);self.assertEqual({d['action']for d in out['objects'].values()},{'keep'});self.assertEqual(out,plan(raw(p)))
        self.assertEqual(p['scope'],native_scope);self.assertEqual(p['nodes'][0]['ref']['id'],'😀'*64)
        bad=copy.deepcopy(p);bad['scope']['workspace_id']='😀'*64
        with self.assertRaisesRegex(LifecycleError,'^metadata_scope$'):plan(bad)
        legacy=copy.deepcopy(next(c['input']for c in load_cases('dependency_cases.json')if c['id']=='independent_negative_epoch_regression'));legacy['scope']['workspace_id']='가'
        with self.assertRaisesRegex(LifecycleError,'^invalid_label$'):plan(legacy)



class MetadataDeletionDomainTests(unittest.TestCase):
    def seed(self):
        return copy.deepcopy(next(c['input'] for c in load_cases('dependency_cases.json') if c['id'] == 'main_eval_required_and_graph'))
    def rename_ref(self, p, collection, replacement):
        chosen = next(n for n in p['nodes'] if n['kind'] == 'metadata_root' and n['ref']['collection'] == collection)
        old_id = chosen['ref']['id']
        def visit(value):
            if type(value) is dict:
                if value.get('namespace') == 'fixture' and value.get('collection') == collection and value.get('id') == old_id and 'revision' in value:
                    value['id'] = replacement
                for item in value.values():
                    visit(item)
            elif type(value) is list:
                for item in value:
                    visit(item)
        visit(p)
        canonical = json.dumps(p['root_metadata'], sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode('utf-8') + b'\n'
        p['metadata_profile']['extraction_sha256'] = hashlib.sha256(b'sys04-main-eval-roots-v1:extraction\0' + canonical).hexdigest()
        return chosen
    def target(self, node):
        return {'scope': copy.deepcopy(node['scope']), 'collection': node['ref']['collection'], 'id': node['ref']['id']}
    def evidence(self, target):
        return {**copy.deepcopy(target), 'facets': {name: {'status': 'unknown', 'receipt_ref': None} for name in FACETS}}
    def test_all_metadata_deletion_targets_share_the_ref_id_domain(self):
        for collection in METADATA_COLLECTIONS:
            for identity in ('평가', 'x' * 129, 'x' * 256, 'a\u200bb'):
                for channel in ('current.deletions', 'deletion_evidence'):
                    with self.subTest(collection=collection, identity_bytes=len(identity.encode('utf-8')), channel=channel):
                        p = self.seed(); node = self.rename_ref(p, collection, identity)
                        self.assertEqual(plan(p)['objects'][node['id']]['action'], 'keep')
                        if channel == 'current.deletions':
                            p['current']['deletions'] = [self.target(node)]
                            self.assertEqual(plan(p)['objects'][node['id']], {'action': 'invalidate', 'reason': 'deleted'})
                        else:
                            p['deletion_evidence'] = [self.evidence(self.target(node))]
                            out = plan(p)
                            self.assertEqual(out['deletion_evidence'], p['deletion_evidence'])
                            self.assertEqual(out['objects'][node['id']]['action'], 'keep')
                            self.assertFalse(out['physical_erasure_verified_by_model'])
    def test_deletion_domains_reject_invalid_metadata_and_preserve_legacy(self):
        for channel in ('current.deletions', 'deletion_evidence'):
            for identity in ('', 'x' * 257, '가' * 86, ' x', 'x\u00a0', 'a\x00b', 'a\x85b', '\ud800'):
                with self.subTest(channel=channel, identity=repr(identity), protocol='metadata'):
                    p = self.seed(); target = {'scope': copy.deepcopy(p['scope']), 'collection': 'evaluations', 'id': identity}
                    if channel == 'current.deletions': p['current']['deletions'] = [target]
                    else: p['deletion_evidence'] = [self.evidence(target)]
                    with self.assertRaises(LifecycleError): plan(p)
            for identity in ('평가', 'x' * 129, 'a\u200bb'):
                with self.subTest(channel=channel, identity=repr(identity), protocol='legacy'):
                    p = BoundaryTests().seed(); target = {'scope': copy.deepcopy(p['scope']), 'collection': 'memories', 'id': identity}
                    if channel == 'current.deletions': p['current']['deletions'] = [target]
                    else: p['deletion_evidence'] = [self.evidence(target)]
                    with self.assertRaisesRegex(LifecycleError, '^invalid_label$'): plan(p)


if __name__ == '__main__':
    unittest.main()

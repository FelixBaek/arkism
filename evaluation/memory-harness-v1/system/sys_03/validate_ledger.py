"""Declared synthetic SYS03 atomicity model, NOT a storage adapter or authorization.

External planned schedules are independent inputs; internal claims never drive replay.
No DB/API/model/checker call, schema migration, policy registration or real clock.
"""
import copy
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import sys

HERE = Path(__file__).resolve().parent
CONTRACT_SHA256 = '4c849e0d4778fe9311223cb8fba1e21da6c8d458a31218ee819d52e2b366274d'
MAX_INT = (1 << 63) - 1
OPS = ('accept', 'durable', 'commit', 'publish', 'retry', 'crash', 'checkpoint',
       'invalidate_revision', 'invalidate_scope', 'invalidate_source', 'revoke',
       'display', 'return', 'pack', 'reader', 'use', 'outcome')
COST_KEYS = ('semantic_updates', 'durable_receipt_writes', 'application_receipt_writes',
             'state_writes', 'consumption_writes', 'checkpoint_writes', 'request_writes', 'db_writes',
             'db_transactions', 'db_flushes')
CLAIM_KEYS = ('command_index', 'op', 'request_id', 'event_id', 'payload_sha256', 'key',
              'logical_evidence_identity_sha256', 'claim_sha256', 'accepted_seq',
              'durable_receipt', 'first_consumption_receipt', 'commit_id',
              'state_revision', 'state_sha256', 'checkpoint_seq', 'status', 'semantic_updates',
              'presentation_id')


class LedgerError(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def need(ok, code):
    if not ok:
        raise LedgerError(code)


def raw(value):
    try:
        return (json.dumps(value, ensure_ascii=False, sort_keys=True,
                           separators=(',', ':'), allow_nan=False) + '\n').encode()
    except UnicodeError:
        raise LedgerError('invalid_unicode') from None
    except (ValueError, TypeError, RecursionError):
        raise LedgerError('invalid_json') from None


def digest(label, value):
    # Own explicitly JSON-semantic model domain, never a fixed DYN/SYS native hash.
    return hashlib.sha256(b'sys-atomicity-model-v1\0' + label.encode() + b'\0' + raw(value)).hexdigest()


def closed(value, fields):
    need(type(value) is dict, 'invalid_type')
    need(not (set(value) - set(fields)), 'unknown_field')
    need(not (set(fields) - set(value)), 'missing_field')


def count(value):
    need(type(value) is int and 0 <= value <= MAX_INT, 'invalid_count')


def identifier(value):
    need(type(value) is str, 'invalid_identifier')
    need(all(ord(ch) >= 32 and not 0xD800 <= ord(ch) <= 0xDFFF for ch in value),
         'invalid_identifier')
    need(1 <= len(value.encode('utf-8')) <= 256, 'invalid_identifier')


def dependency_root():
    """Discover only the fixed harness path; candidate runner supplies PYTHONPATH.

    Discovery is not permission to import arbitrary input modules: every dependency
    is raw-hash checked against this candidate's immutable local contract first.
    """
    candidates = list(HERE.parents) + [Path(p).resolve() for p in sys.path if p]
    for p in candidates:
        root = p if (p / 'evaluation/memory-harness-v1/system/sys_02/outcome_contract.json').is_file() else None
        if root is None and (p / 'system/sys_02/outcome_contract.json').is_file():
            root = p.parents[1]
        if root is not None:
            return root
    raise LedgerError('dependency_missing')


def load_dependencies(root=None):
    root = dependency_root() if root is None else Path(root)
    contract_raw = (HERE / 'atomicity_contract.json').read_bytes()
    need(hashlib.sha256(contract_raw).hexdigest() == CONTRACT_SHA256, 'contract_mismatch')
    contract = json.loads(contract_raw)
    for relative, wanted in contract['dependency_pins'].items():
        need(hashlib.sha256((root / relative).read_bytes()).hexdigest() == wanted,
             'dependency_mismatch')
    path = root / 'evaluation/memory-harness-v1/system/sys_02/test_outcome_contract.py'
    spec = importlib.util.spec_from_file_location('_sys03_pinned_sys02', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _pairs(items):
    out = {}
    for key, value in items:
        need(key not in out, 'duplicate_key')
        out[key] = value
    return out


def decode(data):
    need(type(data) is bytes and len(data) <= 4194304, 'resource_limit')
    def integer(token):
        value = int(token)
        need(-MAX_INT - 1 <= value <= MAX_INT, 'invalid_count')
        return value
    def real(token):
        value = float(token)
        need(math.isfinite(value), 'invalid_json')
        return value
    def constant(_):
        raise LedgerError('invalid_json')
    try:
        result = json.loads(data.decode('utf-8'), object_pairs_hook=_pairs,
                            parse_int=integer, parse_float=real, parse_constant=constant)
    except (ValueError, UnicodeError, RecursionError):
        raise LedgerError('invalid_json') from None
    def bounded(v, depth=0):
        need(depth <= 32, 'resource_limit')
        if isinstance(v, (dict, list)):
            need(len(v) <= 4096, 'resource_limit')
            for value in (list(v.keys()) + list(v.values()) if type(v) is dict else v):
                bounded(value, depth + 1)
        elif type(v) is str:
            need(not any(0xD800 <= ord(ch) <= 0xDFFF for ch in v), 'invalid_unicode')
            need(len(v.encode('utf-8')) <= 4096, 'resource_limit')
    bounded(result)
    return result


def _command(c, size):
    closed(c, ('op', 'request_id', 'event_index', 'writer_epoch',
               'expected_revision', 'checkpoint_seq', 'reader_id', 'path', 'included',
               'presentation_id'))
    need(c['op'] in OPS, 'invalid_op')
    identifier(c['request_id'])
    for name in ('event_index', 'writer_epoch', 'expected_revision', 'checkpoint_seq'):
        count(c[name])
    need(c['event_index'] < size, 'invalid_event_index')
    identifier(c['reader_id'])
    identifier(c['presentation_id'])
    need(c['path'] in ('primary', 'other'), 'invalid_path')
    need(type(c['included']) is bool, 'invalid_type')


def replay(external, root=None):
    """Reference model for declared fixture plans; no writes outside local objects.

    The fixture state is an event-chain commitment and revision, not scalar state.
    A commit owns four persistent logical records and one checkpoint in ONE model
    transaction. Receipt-first durable acceptance is a separate model transaction.
    """
    closed(external, ('protocol', 'verification_scope', 'observations', 'schedule'))
    need(external['protocol'] == 'sys-atomicity-external-v1' and
         external['verification_scope'] == 'synthetic_model', 'unsupported_scope')
    need(type(external['observations']) is list and 0 < len(external['observations']) <= 4096,
         'invalid_observations')
    need(type(external['schedule']) is list and 0 < len(external['schedule']) <= 4096,
         'invalid_schedule')
    dep = load_dependencies(root)
    conversions = []
    for observation in external['observations']:
        try:
            conversions.append(dep.convert(raw(observation)))
        except dep.OutcomeContractError as error:
            raise LedgerError('source_' + error.code) from None
    accepted, durable, applied, evidence, requests, state, valid, presented = {}, {}, {}, {}, {}, {}, {}, {}
    stream_last = {}
    writer_epoch = 0
    costs = dict.fromkeys(COST_KEYS, 0)
    claims = []
    for index, command in enumerate(external['schedule']):
        _command(command, len(conversions))
        c = command
        conversion = conversions[c['event_index']]
        e = conversion['dyn_event']
        native = e is None
        key = None if native else e['key']
        key_hash = None if native else digest('model_key', key)
        event_key = None if native else (e['stream_id'], e['event_id'])
        ident = conversion.get('logical_evidence_identity_sha256')
        claim_hash = conversion.get('claim_sha256')
        evidence_key = (key_hash, ident) if ident is not None else None
        payload = None if native else e['payload_sha256']
        old = applied.get(event_key)
        revision, state_hash, checkpoint = state.get(key_hash, (0, None, 0))
        row = dict(zip(CLAIM_KEYS, [index, c['op'], c['request_id'],
                  None if native else e['event_id'], payload, copy.deepcopy(key), ident,
                  claim_hash, None if native else e['accepted_seq'],
                  durable.get(event_key), None, None, revision, state_hash,
                  checkpoint, 'not_applied', 0, c['presentation_id']]))
        status = None
        op = c['op']
        if native and op != 'crash':
            status = ('native_qualification_unimplemented' if conversion['status'] ==
                      'native_metadata_plan_only' else 'unobserved_no_event')
        elif op.startswith('invalidate_') or op == 'revoke':
            valid[key_hash] = False
            status = op
        elif op == 'crash':
            writer_epoch += 1
            accepted = {k: v for k, v in accepted.items() if k in durable}
            stream_last = {}
            for retained in accepted.values():
                sid = retained['stream_id']
                pair = (retained['accepted_seq'], retained['accepted_at_ns'])
                if sid not in stream_last or pair[0] > stream_last[sid][0]:
                    stream_last[sid] = pair
            # Already committed state/receipts survive worker crash. No fabricated IO.
            status = 'worker_restarted'
        elif op in ('display', 'return', 'pack', 'reader', 'use', 'outcome'):
            # Historical observations remain in their own explicit cycle.
            pkey = (key_hash, event_key, c['presentation_id'], c['reader_id'], c['path'])
            binding = (payload, e['accepted_seq'], e['accepted_at_ns'])
            accepted_event = accepted.get(event_key)
            accepted_binding = None if accepted_event is None else (
                accepted_event['payload_sha256'], accepted_event['accepted_seq'],
                accepted_event['accepted_at_ns'])
            # Rejected input must not reserve or poison the presentation cycle.
            if accepted_binding is not None and accepted_binding != binding:
                row['status'] = 'event_conflict'
                claims.append(row)
                continue
            history = presented.get(pkey, {'_binding': binding})
            if history['_binding'] != binding:
                status = 'event_conflict'
            elif op in history:
                status = ('duplicate_stage_observation' if history[op] == c['included']
                          else 'presentation_conflict')
            elif op == 'pack' and c['included'] and not history.get('return', False):
                status = 'packing_without_return'
            elif op == 'reader' and c['included'] and not history.get('pack', False):
                status = 'reader_without_packing'
            elif op == 'use' and c['included'] and not history.get('reader', False):
                status = 'use_unobserved'
            elif op == 'outcome':
                history[op] = c['included']
                presented[pkey] = history
                status = 'outcome_separate_not_use_proof'
            else:
                history[op] = c['included']
                presented[pkey] = history
                status = op + ('_included' if c['included'] else '_excluded')
        elif not valid.get(key_hash, True):
            status = 'source_invalidated'
        elif event_key in accepted and (accepted[event_key]['payload_sha256'] != payload or
                  accepted[event_key]['accepted_seq'] != e['accepted_seq'] or
                  accepted[event_key]['accepted_at_ns'] != e['accepted_at_ns']):
            status = 'event_conflict'
        elif c['request_id'] in requests and requests[c['request_id']][0] != event_key:
            status = 'request_conflict'
        elif op == 'accept':
            previous = stream_last.get(e['stream_id'])
            if event_key in accepted:
                status = 'duplicate_acceptance'
            elif e['arrived_at_ns'] is not None and e['arrived_at_ns'] > e['accepted_at_ns']:
                status = 'future_arrival'
            elif previous is not None and (e['accepted_seq'] <= previous[0] or
                                          e['accepted_at_ns'] < previous[1]):
                status = 'sequence_regression'
            else:
                accepted[event_key] = copy.deepcopy(e)
                stream_last[e['stream_id']] = (e['accepted_seq'], e['accepted_at_ns'])
                status = 'accepted_volatile'
        elif event_key not in accepted:
            status = 'missing_acceptance'
        elif op == 'durable':
            if event_key in durable:
                status = 'duplicate_durable'
            else:
                durable[event_key] = digest('durable', [event_key, payload, e['accepted_seq'], key])
                costs['durable_receipt_writes'] += 1
                costs['db_writes'] += 1
                costs['db_transactions'] += 1
                costs['db_flushes'] += 1
                status = 'durable_not_applied'
            row['durable_receipt'] = durable[event_key]
        elif op in ('commit', 'retry'):
            if old is not None:
                # Return exact first commit linkage, never recompute receipt on retry.
                for name in ('first_consumption_receipt', 'commit_id', 'state_revision',
                             'state_sha256', 'checkpoint_seq'):
                    row[name] = old[name]
                status = 'duplicate_event_original_receipt'
            elif event_key not in durable:
                status = 'missing_durable_receipt'
            elif c['writer_epoch'] != writer_epoch:
                status = 'stale_writer'
            elif c['expected_revision'] != revision:
                status = 'cas_conflict'
            elif c['checkpoint_seq'] != checkpoint:
                status = 'stale_checkpoint'
            elif e['accepted_seq'] <= checkpoint:
                status = 'sequence_regression'
            elif evidence_key in evidence and evidence[evidence_key]['claim_sha256'] != claim_hash:
                status = 'evidence_conflict'
            else:
                first = evidence.get(evidence_key)
                update = int(conversion['jump_eligible'] and first is None)
                # Even zero numeric jumps have an accepted logical event receipt.
                next_hash = digest('model_event_chain', [state_hash, payload, e['accepted_seq'], key])
                commit = digest('commit', [key, event_key, payload, revision + 1,
                                           e['accepted_seq'], durable[event_key]])
                receipt = first['first_consumption_receipt'] if first else (digest('consumption',
                             [ident, claim_hash, commit]) if ident is not None else None)
                row.update(first_consumption_receipt=receipt, commit_id=commit,
                           state_revision=revision + 1, state_sha256=next_hash,
                           checkpoint_seq=e['accepted_seq'], semantic_updates=update)
                # Declared atomic boundary: no observer sees a subset of these writes.
                state[key_hash] = (revision + 1, next_hash, e['accepted_seq'])
                requests[c['request_id']] = (event_key, commit)
                if ident is not None and first is None:
                    evidence[evidence_key] = {'claim_sha256': claim_hash, 'first_consumption_receipt': receipt}
                    costs['consumption_writes'] += 1
                    costs['db_writes'] += 1
                costs['semantic_updates'] += update
                costs['state_writes'] += 1
                costs['application_receipt_writes'] += 1
                costs['checkpoint_writes'] += 1
                costs['request_writes'] += 1
                costs['db_writes'] += 4
                costs['db_transactions'] += 1
                costs['db_flushes'] += 1
                applied[event_key] = copy.deepcopy(row)
                status = 'committed_nonlearning' if not update else 'committed_learning'
        elif op == 'publish':
            if old is None:
                status = 'publish_before_commit'
            elif c['writer_epoch'] != writer_epoch:
                status = 'stale_writer'
            elif (c['expected_revision'] != revision or
                  c['checkpoint_seq'] != checkpoint or
                  old['state_revision'] != revision):
                status = 'stale_publish'
            else:
                row.update(commit_id=old['commit_id'],
                           first_consumption_receipt=old['first_consumption_receipt'])
                status = 'query_visible'
        elif op == 'checkpoint':
            status = 'checkpoint_matches' if c['checkpoint_seq'] == checkpoint else 'stale_checkpoint'
        row['status'] = status
        claims.append(row)
    return {'protocol': 'sys-atomicity-internal-v1', 'verification_scope': 'synthetic_model',
            'claims': claims, 'costs': costs}


def validate_ledger(external, internal, root=None):
    """Exact external-to-internal reconciliation, including negative/zero events.

    Hash equality alone cannot forgive missing events, receipts, checkpoints,
    request/commit linkages, scope/ref/version differences, stages or costs.
    """
    closed(internal, ('protocol', 'verification_scope', 'claims', 'costs'))
    need(internal['protocol'] == 'sys-atomicity-internal-v1' and
         internal['verification_scope'] == 'synthetic_model', 'unsupported_scope')
    need(type(internal['claims']) is list, 'invalid_claims')
    for row in internal['claims']:
        closed(row, CLAIM_KEYS)
    closed(internal['costs'], COST_KEYS)
    for value in internal['costs'].values():
        count(value)
    expected = replay(external, root)
    need(len(internal['claims']) == len(expected['claims']), 'missing_or_duplicate_claim')
    for actual, planned in zip(internal['claims'], expected['claims']):
        # Strict JSON semantics, not Python True==1 coercion.
        need(raw(actual) == raw(planned), 'claim_mismatch')
    need(raw(internal['costs']) == raw(expected['costs']), 'cost_mismatch')
    return {'verification_scope': 'synthetic_model', 'status': 'matched_declared_model',
            'commands_checked': len(expected['claims']), 'costs': copy.deepcopy(expected['costs']),
            'database_verified': False, 'native_learning_supported': False}

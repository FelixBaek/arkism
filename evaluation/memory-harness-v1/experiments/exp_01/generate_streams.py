"""EXP-01.draft provisional plans; no native policy, state or permission engine."""
import errno
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import secrets
import stat
import sys

CONTRACT_SHA256 = 'a1296cea0119cdb11b4f6f2c0c1f6aeafe3e7f33ee4d48b3bef1aeda70c92a1a'
TEMPLATE_SHA256 = '3c1d63f004fc84b835006f4bc0f60a9597996b5ad2f03e1c7663d97504af2650'
CODEC_SHA256 = 'd575bf82f5d1088883fddf5191e97a60a2260cc46a37051af9a89cdd5b7aa74e'
FIXTURE_SHA256 = '3bf38dd6a2be9ff67566699019a3d2fb2d32048e0d1d4a1648e4fdf2c47f4c32'
BASELINE_SHA256 = '04463d838f397772743c5740a346230eb8e9db60db8dddc824d52e1f083790ef'
DYNAMICS_SHA256 = '952d663b76254e15a3a65ab09dcd580504bc4c2814ce4903a8883ece1ca8f0da'
HERE = Path(__file__).parent
BASE = HERE.parents[1]
MAX_BYTES, MAX_ROWS, MAX_INT = 67108864, 100000, 9223372036854775807
FILES = ('events.jsonl', 'queries.jsonl', 'gold.jsonl', 'split_manifest.json')
FILE_FIELDS = ('events_sha256', 'queries_sha256', 'gold_sha256')
DEPENDENCIES = dict(baseline_contract_sha256=BASELINE_SHA256, baseline_codec_sha256=CODEC_SHA256,
                    dynamics_contract_sha256=DYNAMICS_SHA256)


class ExpError(Exception):
    def __init__(self, code, path='$'):
        self.code, self.path = code, path
        super().__init__(code + ': ' + path)


def fail(code, path='$'):
    raise ExpError(code, path) from None


def sha(data):
    return hashlib.sha256(data).hexdigest()


def _fixed_bytes(path, digest):
    try:
        data = path.read_bytes()
    except OSError:
        fail('dependency_mismatch', '$.external_dependencies')
    if sha(data) != digest:
        fail('dependency_mismatch', '$.external_dependencies')
    return data


def _load_codec():
    _fixed_bytes(BASE / 'contract_io.py', CODEC_SHA256)
    spec = importlib.util.spec_from_file_location('_exp01_fixed_codec', BASE / 'contract_io.py')
    module = importlib.util.module_from_spec(spec)
    try:
        spec.loader.exec_module(module)
    except Exception:
        fail('dependency_mismatch', '$.external_dependencies')
    return module


try:
    CONTRACT = json.loads(_fixed_bytes(HERE / 'dataset_contract.json', CONTRACT_SHA256))
    _fixed_bytes(HERE / 'protocol.template.json', TEMPLATE_SHA256)
    IO = _load_codec()
except Exception:
    if __name__ == '__main__':
        print('{"protocol":"exp01-error-v1","code":"dependency_mismatch","path":"$.external_dependencies"}')
        raise SystemExit(2) from None
    raise
MODELS, ENUMS = CONTRACT['closed_wire']['models'], CONTRACT['closed_wire']['enums']
CODES = CONTRACT['error_contract']['ErrorCode']
ID = re.compile(r'[a-z][a-z0-9_-]{0,63}\Z', re.ASCII)
HEX = re.compile(r'[0-9a-f]{64}\Z', re.ASCII)


def _legacy(call, *args):
    try:
        return call(*args)
    except IO.ContractError as error:
        fail(CONTRACT['error_contract']['fixed_helper_code_mapping'].get(error.code, 'internal_error'))


def _depth_guard(text):
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
        elif char in '{[':
            depth += 1
            if depth > 32:
                fail('input_limit')
        elif char in '}]':
            depth -= 1


def _raw_limits(value):
    pending = [value]
    while pending:
        item = pending.pop()
        if isinstance(item, IO._Pairs):
            if len(item) > 200000:
                fail('input_limit')
            for key, child in item:
                pending.extend((key, child))
        elif type(item) is list:
            if len(item) > 200000:
                fail('input_limit')
            pending.extend(item)
        elif isinstance(item, IO._OverflowInteger):
            fail('invalid_value')
        elif type(item) is int and not 0 <= item <= MAX_INT:
            fail('invalid_value')
        elif type(item) is float:
            fail('invalid_type' if math.isfinite(item) else 'invalid_value')
        elif type(item) is str and len(item.encode('utf8')) > 4096:
            fail('input_limit')


def parse_raw(data):
    if type(data) is not bytes:
        fail('invalid_type')
    if len(data) > MAX_BYTES:
        fail('input_limit')
    try:
        text = data.decode('utf8')
    except UnicodeError:
        fail('invalid_unicode')
    _depth_guard(text)
    tree = _legacy(IO._parse_json, text)
    _legacy(IO._unicode, tree)
    _legacy(IO._duplicates, tree)
    _raw_limits(tree)
    return IO._plain(tree)


def known_path(value):
    if type(value) is not str or len(value.encode('utf8', errors='replace')) > 512:
        return False
    if value == '$':
        return True
    tokens = re.findall(r'\.[a-zA-Z][a-zA-Z0-9_]*|\[\*\]', value[1:])
    if not value.startswith('$') or ''.join(tokens) != value[1:]:
        return False
    states = set(MODELS)
    for token in tokens:
        next_states = set()
        for state in states:
            if token == '[*]' and state.endswith('[]'):
                next_states.add(state[:-2])
            elif state in MODELS and token.startswith('.'):
                field = MODELS[state]['fields'].get(token[1:])
                if field:
                    next_states.add(field.rstrip('?'))
        states = next_states
    return bool(states)


def _literal(text):
    if text.startswith('['):
        return text[1:-1].split(',')
    return {'null': None, 'false': False, 'true': True, '4': 4}.get(text, text)


def _primitive(kind, value, path):
    if kind in ('NS', 'PositiveInt64', 'Count', 'Seq0'):
        if type(value) is not int:
            fail('invalid_type', path)
        if not (1 if kind == 'PositiveInt64' else 0) <= value <= MAX_INT:
            fail('invalid_value', path)
        return
    if type(value) is not str:
        fail('invalid_type', path)
    if kind == 'ID':
        valid = ID.fullmatch(value) is not None
    elif kind == 'SHA':
        valid = HEX.fullmatch(value) is not None
    elif kind == 'Text':
        valid = '\x00' not in value and len(value.encode('utf8')) <= 4096
    elif kind == 'SafeRelativePath':
        valid = value in FILES + ('review.json',)
    elif kind == 'KnownExpSchemaPath':
        valid = known_path(value)
    else:
        fail('internal_error')
    if not valid:
        fail('invalid_value', path)


def _typed_array(kind, value, path):
    if type(value) is not list:
        fail('invalid_type', path)
    if len(value) > 200000:
        fail('input_limit', path)
    for item in value:
        _typed(kind[:-2], item, path + '[*]')


def _typed_model(kind, value, path):
    if type(value) is not dict:
        fail('invalid_type', path)
    fields = MODELS[kind]['fields']
    if set(value) - set(fields):
        fail('unknown_field', path)
    missing = sorted(set(fields) - set(value))
    if missing:
        fail('missing_field', path + '.' + missing[0])
    for field, child in fields.items():
        _typed(child, value[field], path + '.' + field)


def _typed_external(kind, value, path):
    try:
        IO.validate_typed(kind[12:], value)
    except IO.ContractError as error:
        fail(CONTRACT['error_contract']['fixed_helper_code_mapping'].get(error.code, 'internal_error'), path)


def _typed(kind, value, path):
    if kind.endswith('?'):
        if value is not None:
            _typed(kind[:-1], value, path)
        return
    if kind.startswith('literal:'):
        expected = _literal(kind[8:])
        if type(value) is not type(expected) or value != expected:
            fail('invalid_value', path)
    elif kind.endswith('[]'):
        _typed_array(kind, value, path)
    elif kind in MODELS:
        _typed_model(kind, value, path)
    elif kind.startswith('external:v1.'):
        _typed_external(kind, value, path)
    elif kind in ENUMS or kind == 'ErrorCode':
        if type(value) is not str:
            fail('invalid_type', path)
        if value not in (CODES if kind == 'ErrorCode' else ENUMS[kind]):
            fail('invalid_value', path)
    else:
        _primitive(kind, value, path)


def _value_limits(value):
    pending = [(value, 1)]
    while pending:
        item, depth = pending.pop()
        if depth > 32:
            fail('input_limit')
        if type(item) is dict:
            if any(type(key) is not str for key in item):
                fail('invalid_type')
            pending.extend((child, depth + 1) for child in item.values())
        elif type(item) is list:
            if len(item) > 200000:
                fail('input_limit')
            pending.extend((child, depth + 1) for child in item)


def validate_typed(kind, value):
    _value_limits(value)
    if kind not in MODELS:
        fail('invalid_type')
    _legacy(IO._unicode, value)
    _typed(kind, value, '$')


def decode_typed(data, kind):
    value = parse_raw(data)
    validate_typed(kind, value)
    return value


def compact(value):
    try:
        return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':'), allow_nan=False).encode('utf8')
    except (ValueError, TypeError, UnicodeError, RecursionError):
        fail('invalid_value')


def semantic_sha(kind, value):
    validate_typed(kind, value)
    return sha(b'exp01-draft-v1\0' + kind.encode('ascii') + b'\0' + compact(value))


def os_error(error):
    if error.errno == errno.EEXIST:
        fail('output_exists')
    fail('unsafe_path' if error.errno in (errno.ELOOP, errno.ENOTDIR) else 'io_error')


def checked_directory(path):
    path = Path(path)
    if not path.is_absolute():
        fail('unsafe_path')
    path = _legacy(IO._safe_absolute, path)
    descriptor = _legacy(IO._directory_fd, path)
    os.close(descriptor)
    return path


def bounded_read(path):
    path = Path(path)
    if not path.is_absolute():
        fail('unsafe_path')
    path = _legacy(IO._safe_absolute, path)
    parent = _legacy(IO._directory_fd, path.parent)
    try:
        if not stat.S_ISREG(os.stat(path.name, dir_fd=parent, follow_symlinks=False).st_mode):
            fail('unsafe_path')
        fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        with os.fdopen(fd, 'rb') as stream:
            if not stat.S_ISREG(os.fstat(stream.fileno()).st_mode):
                fail('unsafe_path')
            data = stream.read(MAX_BYTES + 1)
        if len(data) > MAX_BYTES:
            fail('input_limit')
        return data
    except OSError as error:
        os_error(error)
    finally:
        os.close(parent)


def unique(items, path='$', nonempty=False):
    keys = [compact(item) for item in items]
    if (nonempty and not items) or len(keys) != len(set(keys)):
        fail('identity_conflict', path)


def index(items, field, path='$', nonempty=True):
    unique([item[field] for item in items], path, nonempty)
    return {item[field]: item for item in items}


def require(condition, code='invalid_value', path='$'):
    if not condition:
        fail(code, path)


def _hash_tuple(domain, *parts):
    return hashlib.sha256(b'\0'.join(str(part).encode('utf8') for part in (domain,) + parts)).digest()


def choice(global_seed, stream_seed, root, instance, name, length):
    require(length > 0)
    return int.from_bytes(_hash_tuple('exp01-expand-v1', global_seed, stream_seed, root, instance, name), 'big') % length


def opaque(kind, global_seed, stream_seed, root, instance, symbol):
    prefix = {'stream': 's', 'event': 'e', 'query': 'q', 'material': 'm', 'task': 't', 'evidence': 'v'}[kind]
    return prefix + _hash_tuple('exp01-id-v1', kind, global_seed, stream_seed, root, instance, symbol).hex()[:62]


def pending_review(label_version):
    return dict(reviewer_id=None, review_record=None, reviewed_gold_sha256=None,
                label_version=label_version, status='pending_independent_review')


def lookup(mapping, key):
    if key not in mapping:
        fail('identity_conflict')
    return mapping[key]


def _segments(segments, slots):
    used, topics = set(), 0
    for segment in segments:
        kind = segment['kind']
        if kind == 'literal':
            require(segment['literal'] is not None and segment['slot_name'] is None)
        elif kind == 'entity_slot':
            require(segment['literal'] is None and segment['slot_name'] in slots)
            used.add(segment['slot_name'])
        else:
            require(segment['literal'] is None and segment['slot_name'] is None)
            topics += 1
    return used, topics


def _template_events(variant, materials):
    events = index(variant['events'], 'symbol')
    seen = {}
    for event in variant['events']:
        unique(event['cause_symbols'])
        causes = [lookup(seen, symbol) for symbol in event['cause_symbols']]
        target = event['target_symbol']
        if event['kind'] == 'task_started':
            require(target is None and event['task_symbol'] is not None and not event['related_symbols'] and event['outcome'] is None)
        else:
            lookup(materials, target)
            for symbol in event['related_symbols']:
                lookup(materials, symbol)
        if event['kind'] == 'outcome_observed':
            require(event['outcome'] is not None and event['task_symbol'] is not None)
            require(sum(c['kind'] == 'task_started' and c['task_symbol'] == event['task_symbol'] for c in causes) == 1, 'causal_order')
            require(sum(c['kind'] == 'material_available' and c['target_symbol'] == target for c in causes) == 1, 'causal_order')
        else:
            require(event['outcome'] is None)
        if event['kind'] in ('correction_proposed', 'correction_committed'):
            require(len(event['related_symbols']) == 1 and event['related_symbols'][0] != target)
            if event['kind'] == 'correction_committed':
                require(sum(c['kind'] == 'correction_proposed' and c['target_symbol'] == target and c['related_symbols'] == event['related_symbols'] for c in causes) == 1, 'causal_order')
        seen[event['symbol']] = event
    available = {e['target_symbol'] for e in events.values() if e['kind'] == 'material_available'}
    require(available == set(materials), 'identity_conflict')
    evidence = [e['outcome']['evidence_symbol'] for e in events.values() if e['outcome'] is not None]
    unique(evidence)
    return events


def answer_sets(required, alternatives, forbidden, plausible, cohort):
    for group in (required, forbidden, plausible):
        unique(group)
    forbidden_keys = {compact(ref) for ref in forbidden}
    require(not forbidden_keys & {compact(ref) for ref in required + plausible}, 'gold_semantics')
    normalized = []
    for group in alternatives:
        unique(group, nonempty=True)
        keys = sorted(compact(ref) for ref in group)
        require(not forbidden_keys & set(keys), 'gold_semantics')
        normalized.append(keys)
    require(len(normalized) == len(set(tuple(group) for group in normalized)), 'gold_semantics')
    allowed = {
        'primary_answerable': bool(required) and not alternatives and not plausible,
        'clear_negative': not required and not alternatives and not plausible,
        'ambiguous': not required and not alternatives,
        'alternative_answers': not required and not plausible and bool(alternatives),
    }
    require(allowed[cohort], 'gold_semantics')


def _gold_template_rules(variant, questions):
    primary = 0
    for rule in variant['gold_rules']:
        question = questions[rule['question_symbol']]
        require(question['request_mode'] == rule['temporal_intent'], 'gold_semantics')
        if question['request_mode'] == 'current':
            require(question['requested_as_of_anchor'] is None and rule['historical_as_of_anchor'] is None, 'gold_semantics')
        else:
            require(question['requested_as_of_anchor'] is not None and rule['historical_as_of_anchor'] is not None, 'gold_semantics')
        answer_sets(rule['required_symbols'], rule['alternative_symbol_sets'], rule['forbidden_symbols'], rule['plausible_symbols'], rule['cohort'])
        primary += rule['cohort'] == 'primary_answerable'
    require(bool(primary) == (variant['stream_cohort'] == 'primary_eligible'), 'gold_semantics')


def _validate_variant(variant):
    require(len(variant['events']) <= MAX_ROWS and len(variant['questions']) <= MAX_ROWS, 'input_limit')
    unique(variant['slot_names'], nonempty=True)
    materials = index(variant['materials'], 'symbol')
    unique([(m['identity_symbol'], m['revision']) for m in materials.values()])
    questions = index(variant['questions'], 'symbol')
    rules = index(variant['gold_rules'], 'question_symbol')
    require(set(questions) == set(rules), 'gold_incomplete')
    _gold_template_rules(variant, questions)
    used, topics = set(), 0
    for material in materials.values():
        require(bool(material['text']))
        for field in ('text', 'summary'):
            found, count = _segments(material[field], variant['slot_names'])
            used |= found
            topics += count
    events = _template_events(variant, materials)
    for question in questions.values():
        require(bool(question['query_text']))
        found, count = _segments(question['query_text'], variant['slot_names'])
        used |= found
        topics += count
        if question['cutoff_event_symbol'] is not None:
            lookup(events, question['cutoff_event_symbol'])
    require(used == set(variant['slot_names']) and topics > 0)
    for rule in rules.values():
        for field in ('required_symbols', 'forbidden_symbols', 'plausible_symbols'):
            unique(rule[field])
            for symbol in rule[field]:
                lookup(materials, symbol)
        for group in rule['alternative_symbol_sets']:
            unique(group, nonempty=True)
            for symbol in group:
                lookup(materials, symbol)
        unique(rule['cause_symbols'])
        for symbol in rule['cause_symbols']:
            lookup(events, symbol)
    return materials


def _partition(pool):
    entities = index(pool['entity_slots'], 'entity_id')
    topics = index(pool['topic_slots'], 'topic_id')
    unique(pool['fact_group_ids'], nonempty=True)
    unique(pool['entity_topic_pairs'], nonempty=True)
    require(all(x['display_text'] for x in entities.values()) and all(x['display_text'] for x in topics.values()))
    for pair in pool['entity_topic_pairs']:
        require(pair['entity_id'] in entities and pair['topic_id'] in topics)
    return entities, topics


def _expansion_bindings(expansion, family, regime, pool):
    unique(expansion['instance_symbols'], nonempty=True)
    require(len(expansion['instance_symbols']) == len(expansion['stream_seeds']))
    unique(expansion['allowed_entity_topic_pairs'], nonempty=True)
    require(all(pair in pool['entity_topic_pairs'] for pair in expansion['allowed_entity_topic_pairs']))
    slots = index(expansion['slot_entity_bindings'], 'slot_name')
    facts = index(expansion['fact_group_bindings'], 'slot_name')
    for binding in facts.values():
        require(binding['fact_group_id'] in pool['fact_group_ids'])
    for binding in slots.values():
        unique(binding['pool_entity_ids'], nonempty=True)
        require(set(binding['pool_entity_ids']) <= {e['entity_id'] for e in pool['entity_slots']})
    anchors = {step['anchor_id'] for step in regime['steps']}
    for variant in family['variants']:
        require(set(slots) == set(variant['slot_names']))
        require(set(facts) == {m['fact_group_slot'] for m in variant['materials']})
        for pair in expansion['allowed_entity_topic_pairs']:
            first = slots[variant['slot_names'][0]]['pool_entity_ids']
            require(pair['entity_id'] in first)
            eligible = {p['entity_id'] for p in expansion['allowed_entity_topic_pairs'] if p['topic_id'] == pair['topic_id']}
            require(all(eligible & set(binding['pool_entity_ids']) for binding in slots.values()))
        _template_anchors(variant, anchors)


def _template_anchors(variant, anchors):
    groups = [('materials', ('created_anchor', 'updated_anchor', 'effective_anchor')),
              ('events', ('available_anchor', 'source_anchor', 'arrived_anchor')),
              ('questions', ('at_anchor', 'requested_as_of_anchor')),
              ('gold_rules', ('historical_as_of_anchor',))]
    for group, fields in groups:
        for row in variant[group]:
            for field in fields:
                require(row[field] is None or row[field] in anchors)


def validate_spec(spec):
    validate_typed('GeneratorSpec', spec)
    require(spec['external_dependencies'] == DEPENDENCIES, 'dependency_mismatch', '$.external_dependencies')
    for field in ('generator_author_ids', 'label_author_ids'):
        unique(spec[field], nonempty=True)
    partitions = index(spec['partitions'], 'split')
    require(set(partitions) == set(ENUMS['SplitRole']))
    for pool in partitions.values():
        _partition(pool)
    families = index(spec['template_families'], 'family_id')
    regimes = index(spec['time_regimes'], 'regime_id')
    for family in families.values():
        index(family['variants'], 'variant_id')
        for variant in family['variants']:
            _validate_variant(variant)
    for regime in regimes.values():
        index(regime['steps'], 'anchor_id')
        for step in regime['steps']:
            require(step['gap_min_ns'] <= step['gap_max_ns'])
            if regime['kind'] == 'regular':
                require(step['gap_min_ns'] == step['gap_max_ns'])
    index(spec['expansions'], 'root_stream_symbol')
    totals = [0, 0, 0]
    for expansion in spec['expansions']:
        family = lookup(families, expansion['template_family_id'])
        regime = lookup(regimes, expansion['time_regime_id'])
        require(family['assigned_split'] == expansion['split'], 'split_overlap')
        _expansion_bindings(expansion, family, regime, partitions[expansion['split']])
        for instance, seed in zip(expansion['instance_symbols'], expansion['stream_seeds']):
            variant = family['variants'][choice(spec['seed'], seed, expansion['root_stream_symbol'], instance, 'variant', len(family['variants']))]
            totals[0] += 1
            totals[1] += len(variant['events'])
            totals[2] += len(variant['questions'])
            require(all(n <= MAX_ROWS for n in totals), 'input_limit')
    require({e['split'] for e in spec['expansions']} == set(ENUMS['SplitRole']))
    return partitions, families, regimes


def _text(segments, slots, entities, topic):
    pieces = []
    for segment in segments:
        if segment['kind'] == 'literal':
            pieces.append(segment['literal'])
        elif segment['kind'] == 'entity_slot':
            pieces.append(entities[slots[segment['slot_name']]]['display_text'])
        else:
            pieces.append(topic['display_text'])
    result = ''.join(pieces)
    _primitive('Text', result, '$')
    return result


def _times(regime, choose):
    times, now = {}, regime['base_ns']
    for step in regime['steps']:
        gap = step['gap_min_ns'] + choose('gap/' + step['anchor_id'], step['gap_max_ns'] - step['gap_min_ns'] + 1)
        now += gap
        require(now <= MAX_INT, 'invalid_value')
        times[step['anchor_id']] = now
    return times


def _source_materials(variant, oid, times, render):
    sources, refs = {}, {}
    for material in variant['materials']:
        source = dict(text=render(material['text']), summary=render(material['summary']), kind=material['kind'],
                      created_at_ns=times[material['created_anchor']], updated_at_ns=times[material['updated_anchor']],
                      effective_at_ns=None if material['effective_anchor'] is None else times[material['effective_anchor']])
        require(bool(source['text']))
        symbol = material['symbol']
        sources[symbol] = source
        refs[symbol] = dict(origin='memory', collection='memories', id=oid('material', material['identity_symbol']),
                            revision=material['revision'], content_sha256=sha((source['text'] + '\n' + source['summary']).encode('utf8')))
    return sources, refs


def _events(variant, oid, times, scope, clock, stream, sources, refs):
    rows = []
    for seq, template in enumerate(variant['events'], 1):
        target = template['target_symbol']
        outcome = template['outcome']
        if outcome is not None:
            outcome = dict(outcome)
            outcome['evidence_id'] = oid('evidence', outcome.pop('evidence_symbol'))
        row = dict(stream_id=stream, event_id=oid('event', template['symbol']), plan_seq=seq, scope=dict(scope),
                   clock_domain=clock, available_at_ns=times[template['available_anchor']],
                   source_at_ns=None if template['source_anchor'] is None else times[template['source_anchor']],
                   arrived_at_ns=None if template['arrived_anchor'] is None else times[template['arrived_anchor']],
                   kind=template['kind'], target=None if target is None else dict(refs[target]),
                   related_refs=[dict(refs[s]) for s in template['related_symbols']],
                   cause_event_ids=[oid('event', s) for s in template['cause_symbols']],
                   task_id=None if template['task_symbol'] is None else oid('task', template['task_symbol']),
                   outcome=outcome, source_payload=sources[target] if template['kind'] == 'material_available' else None)
        rows.append(row)
    return rows


def ref_key(ref):
    return tuple(ref[name] for name in ('origin', 'collection', 'id', 'revision', 'content_sha256'))


def _questions(variant, oid, times, scope, clock, stream, render, refs, label_version):
    queries, gold = [], []
    sequence = {event['symbol']: i for i, event in enumerate(variant['events'], 1)}
    rules = {rule['question_symbol']: rule for rule in variant['gold_rules']}
    for template in variant['questions']:
        rule = rules[template['symbol']]
        query = dict(stream_id=stream, query_id=oid('query', template['symbol']), scope=dict(scope), clock_domain=clock,
                     at_ns=times[template['at_anchor']], cutoff_plan_seq=0 if template['cutoff_event_symbol'] is None else sequence[template['cutoff_event_symbol']],
                     query_text=render(template['query_text']), request_mode=template['request_mode'],
                     requested_as_of_ns=None if template['requested_as_of_anchor'] is None else times[template['requested_as_of_anchor']])
        require(bool(query['query_text']))
        row = {key: query[key] for key in ('stream_id', 'query_id', 'scope', 'clock_domain', 'at_ns', 'cutoff_plan_seq')}
        row.update(cohort=rule['cohort'], temporal_intent=rule['temporal_intent'],
                   historical_as_of_ns=None if rule['historical_as_of_anchor'] is None else times[rule['historical_as_of_anchor']],
                   correction_phase=rule['correction_phase'], required_refs=[dict(refs[s]) for s in rule['required_symbols']],
                   alternative_ref_sets=[sorted([dict(refs[s]) for s in group], key=ref_key) for group in rule['alternative_symbol_sets']],
                   forbidden_refs=[dict(refs[s]) for s in rule['forbidden_symbols']], plausible_refs=[dict(refs[s]) for s in rule['plausible_symbols']],
                   cause_event_ids=[oid('event', s) for s in rule['cause_symbols']], label_version=label_version,
                   label_status='provisional', label_rule_id=rule['label_rule_id'])
        queries.append(query)
        gold.append(row)
    by_id = {g['query_id']: g for g in gold}
    queries.sort(key=lambda q: (q['at_ns'], q['cutoff_plan_seq'], q['query_id']))
    return queries, [by_id[q['query_id']] for q in queries]


def _instantiate(spec, expansion, instance, seed, pool, family, regime, identities):
    global_seed, root = spec['seed'], expansion['root_stream_symbol']
    def choose(name, count):
        return choice(global_seed, seed, root, instance, name, count)
    def oid(kind, symbol):
        value = opaque(kind, global_seed, seed, root, instance, symbol)
        origin = (global_seed, seed, root, instance, symbol)
        key = (kind, value)
        require(key not in identities or identities[key] == origin, 'identity_conflict')
        identities[key] = origin
        return value
    variant = family['variants'][choose('variant', len(family['variants']))]
    pair = expansion['allowed_entity_topic_pairs'][choose('entity_topic_pair', len(expansion['allowed_entity_topic_pairs']))]
    slots = {variant['slot_names'][0]: pair['entity_id']}
    pools = {b['slot_name']: b['pool_entity_ids'] for b in expansion['slot_entity_bindings']}
    for slot in variant['slot_names'][1:]:
        eligible = [entity for entity in pools[slot] if dict(entity_id=entity, topic_id=pair['topic_id']) in expansion['allowed_entity_topic_pairs']]
        slots[slot] = eligible[choose('slot/' + slot, len(eligible))]
    entities, topics = _partition(pool)
    topic = topics[pair['topic_id']]
    def render(segments):
        return _text(segments, slots, entities, topic)
    times = _times(regime, choose)
    stream = oid('stream', 'stream')
    scope = {wire: prefix + _hash_tuple('exp01-scope-v1', component, stream).hex()[:62]
             for wire, component, prefix in [('workspace', 'workspace_id', 'w'), ('project', 'project_id', 'p'),
                                             ('user', 'user_id', 'u'), ('agent', 'agent_id', 'a')]}
    clock = 'c' + _hash_tuple('exp01-clock-v1', stream).hex()[:62]
    sources, refs = _source_materials(variant, oid, times, render)
    events = _events(variant, oid, times, scope, clock, stream, sources, refs)
    queries, gold = _questions(variant, oid, times, scope, clock, stream, render, refs, spec['label_version'])
    facts = {b['slot_name']: b['fact_group_id'] for b in expansion['fact_group_bindings']}
    entry = dict(stream_id=stream, root_stream_id='r' + _hash_tuple('exp01-root-v1', global_seed, root).hex()[:62],
                 split=expansion['split'], fact_group_ids=sorted({facts[m['fact_group_slot']] for m in variant['materials']}),
                 template_family_ids=[family['family_id']], entity_ids=sorted(set(slots.values())), topic_ids=[pair['topic_id']],
                 entity_topic_pairs=[dict(entity_id=e, topic_id=pair['topic_id']) for e in sorted(set(slots.values()))], seed=seed,
                 stream_cohort=variant['stream_cohort'], template_variant_id=variant['variant_id'], template_version=family['template_version'],
                 time_regime_id=regime['regime_id'], slot_bindings=[dict(slot_name=s, entity_id=slots[s]) for s in variant['slot_names']])
    return events, queries, gold, entry


def split_projection(manifest):
    return {field: manifest[field] for field in MODELS['SplitAssignmentProjection']['fields']}


def expand_spec(spec_raw):
    spec = decode_typed(spec_raw, 'GeneratorSpec')
    partitions, families, regimes = validate_spec(spec)
    streams = []
    identities = {}
    for expansion in sorted(spec['expansions'], key=lambda row: row['root_stream_symbol']):
        for instance, seed in zip(expansion['instance_symbols'], expansion['stream_seeds']):
            streams.append(_instantiate(spec, expansion, instance, seed, partitions[expansion['split']],
                                        families[expansion['template_family_id']], regimes[expansion['time_regime_id']], identities))
    streams.sort(key=lambda rows: (_hash_tuple('exp01-stream-order-v1', spec['seed'], rows[3]['stream_id']), rows[3]['stream_id']))
    events, queries, gold = ([row for stream in streams for row in stream[i]] for i in range(3))
    data = {name: b''.join(compact(row) + b'\n' for row in rows) for name, rows in zip(FILES, (events, queries, gold))}
    manifest = dict(protocol='exp01-plan-split-v1', dataset_id=spec['dataset_id'], dataset_version=spec['dataset_version'],
                    generator_version=spec['generator_version'], label_version=spec['label_version'], spec_sha256=sha(spec_raw),
                    contract_sha256=CONTRACT_SHA256, external_dependencies=dict(DEPENDENCIES), seed=spec['seed'],
                    verification_scope='synthetic_plan_only', implementation_status='materialization_pending',
                    files={field: sha(data[name]) for field, name in zip(FILE_FIELDS, FILES)}, entries=[s[3] for s in streams],
                    review=pending_review(spec['label_version']), split_mode=spec['split_mode'],
                    generator_author_ids=list(spec['generator_author_ids']), label_author_ids=list(spec['label_author_ids']))
    manifest['split_assignment_sha256'] = semantic_sha('SplitAssignmentProjection', split_projection(manifest))
    data[FILES[3]] = compact(manifest) + b'\n'
    require(all(len(raw) <= MAX_BYTES for raw in data.values()), 'input_limit')
    return data, (events, queries, gold, manifest)


def _fixture_helpers():
    _fixed_bytes(BASE / 'fixture_gen.py', FIXTURE_SHA256)
    original = list(sys.path)
    try:
        sys.path.insert(0, str(BASE))
        spec = importlib.util.spec_from_file_location('_exp01_fixed_fixture', BASE / 'fixture_gen.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        _fixed_bytes(Path(module.io.__file__), CODEC_SHA256)
        return module
    except ExpError:
        raise
    except Exception:
        fail('dependency_mismatch', '$.external_dependencies')
    finally:
        sys.path[:] = original


def load_sibling(name):
    require(name in ('generate_streams', 'validate_splits'), 'internal_error')
    target = HERE / (name + '.py')
    alias = '_arkism_exp01_' + name
    cached = sys.modules.get(alias)
    if cached is not None:
        require(Path(cached.__file__).absolute() == target.absolute(), 'dependency_mismatch', '$.external_dependencies')
        return cached
    spec = importlib.util.spec_from_file_location(alias, target)
    module = importlib.util.module_from_spec(spec)
    sys.modules[alias] = module
    try:
        spec.loader.exec_module(module)
    except BaseException:
        sys.modules.pop(alias, None)
        raise
    return module


def _reserve(parent, name, helpers):
    basename = os.path.basename(name)
    if basename != name or basename in ('', '.', '..'):
        fail('unsafe_path')
    name = basename
    os.mkdir(name, 0o700, dir_fd=parent)
    identity = helpers._identity(os.stat(name, dir_fd=parent, follow_symlinks=False))
    descriptor = None
    try:
        descriptor = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        require(helpers._identity(os.fstat(descriptor)) == identity, 'unsafe_path')
        return descriptor, identity
    except BaseException:
        helpers._cleanup(parent, name, descriptor, identity, {})
        if descriptor is not None:
            os.close(descriptor)
        raise


def _verify_at(directory, name, expected, identity, helpers):
    fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and helpers._identity(info) == identity, 'unsafe_path')
        actual = stream.read(MAX_BYTES + 1)
    require(len(actual) == len(expected) and sha(actual) == sha(expected), 'hash_mismatch')


def _publish(data, output):
    helpers = _fixture_helpers()
    output = Path(output)
    require(output.is_absolute() and output.name not in ('', '.', '..') and '..' not in output.parts, 'unsafe_path')
    parent_path = checked_directory(output.parent)
    parent = _legacy(IO._directory_fd, parent_path)
    staging_name, staging_fd, staging_identity = None, None, None
    output_fd, output_identity = None, None
    staged, published = {}, {}
    complete, error = False, None
    try:
        try:
            os.stat(output.name, dir_fd=parent, follow_symlinks=False)
        except FileNotFoundError:
            pass
        else:
            fail('output_exists')
        staging_name = '.exp01-' + secrets.token_hex(16)
        staging_fd, staging_identity = _reserve(parent, staging_name, helpers)
        for name in FILES:
            helpers._write_bytes(staging_fd, name, data[name], staged)
            _verify_at(staging_fd, name, data[name], staged[name], helpers)
        require(set(os.listdir(staging_fd)) == set(FILES), 'io_error')
        os.fsync(staging_fd)
        output_fd, output_identity = _reserve(parent, output.name, helpers)
        for name in FILES:
            require(helpers._identity(os.stat(output.name, dir_fd=parent, follow_symlinks=False)) == output_identity, 'unsafe_path')
            helpers._publish_file(staging_fd, output_fd, name)
            published[name] = helpers._identity(os.stat(name, dir_fd=output_fd, follow_symlinks=False))
            _verify_at(output_fd, name, data[name], published[name], helpers)
        os.fsync(output_fd)
        os.fsync(parent)
        require(helpers._identity(os.stat(output.name, dir_fd=parent, follow_symlinks=False)) == output_identity, 'unsafe_path')
        complete = True
    except ExpError as caught:
        error = caught
    except OSError as caught:
        error = ExpError('output_exists' if caught.errno == errno.EEXIST else 'unsafe_path' if caught.errno in (errno.ELOOP, errno.ENOTDIR) else 'io_error')
    finally:
        if output_identity is not None and not complete:
            if not helpers._cleanup(parent, output.name, output_fd, output_identity, published):
                error = ExpError('io_error')
        if staging_name is not None and staging_identity is not None:
            if not helpers._cleanup(parent, staging_name, staging_fd, staging_identity, staged):
                error = ExpError('io_error')
        for descriptor in (staging_fd, output_fd, parent):
            if descriptor is not None:
                os.close(descriptor)
    if error is not None:
        raise error


def generate_streams(spec_raw, output_dir):
    data, rows = expand_spec(spec_raw)
    validation = load_sibling('validate_splits')
    try:
        validation.validate_artifacts(*rows)
    except validation.G.ExpError as error:
        fail(error.code, error.path)
    _publish(data, output_dir)
    return dict(protocol='exp01-generation-result-v1', status='published_provisional_draft',
                manifest_sha256=sha(data[FILES[3]]), artifact_count=4, verification_scope='synthetic_plan_only')


def cli_args(argv, names):
    values = {}
    require(len(argv) % 2 == 0, 'invalid_argv')
    for pos in range(0, len(argv), 2):
        flag, value = argv[pos:pos + 2]
        require(flag in names and flag not in values and not value.startswith('--'), 'invalid_argv')
        values[flag] = value
    require(set(values) == set(names), 'invalid_argv')
    return values


def cli_result(action):
    try:
        value = action()
        sys.stdout.write(compact(value).decode('utf8') + '\n')
        return 0
    except ExpError as error:
        value = dict(protocol='exp01-error-v1', code=error.code, path=error.path)
        status = 1 if error.code in ('io_error', 'internal_error') else 2
    except Exception:
        value, status = dict(protocol='exp01-error-v1', code='internal_error', path='$'), 1
    try:
        sys.stdout.write(json.dumps(value, separators=(',', ':')) + '\n')
    except OSError:
        return 1
    return status


def main(argv=None):
    def run():
        args = cli_args(sys.argv[1:] if argv is None else argv, ('--spec', '--out'))
        return generate_streams(bounded_read(args['--spec']), Path(args['--out']))
    return cli_result(run)


if __name__ == '__main__':
    # Use the same module identity as the validator's import, including errors.
    raise SystemExit(load_sibling('generate_streams').main())

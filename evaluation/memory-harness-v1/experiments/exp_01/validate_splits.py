"""Validate provisional EXP plans, gold joins and evidence; never execute policies."""
import importlib.util
from pathlib import Path
import sys

def _load_generator():
    alias = '_arkism_exp01_generate_streams'
    target = Path(__file__).with_name('generate_streams.py')
    cached = sys.modules.get(alias)
    if cached is not None:
        if Path(cached.__file__).absolute() != target.absolute():
            raise RuntimeError('fixed sibling mismatch')
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


try:
    G = _load_generator()
except Exception:
    if __name__ == '__main__':
        print('{"protocol":"exp01-error-v1","code":"dependency_mismatch","path":"$.external_dependencies"}')
        raise SystemExit(2) from None
    raise


def _same(actual, expected, code='identity_conflict', path='$'):
    G.require(actual == expected, code, path)


def _events_by_stream(events, entries):
    G.index(events, 'event_id')
    grouped = {entry['stream_id']: [] for entry in entries}
    evidence = []
    sources = {}
    for event in events:
        G.require(event['stream_id'] in grouped, 'identity_conflict')
        grouped[event['stream_id']].append(event)
        if event['outcome'] is not None:
            evidence.append(event['outcome']['evidence_id'])
        if event['kind'] == 'material_available':
            G.require(event['target'] is not None and event['source_payload'] is not None)
            key = G.ref_key(event['target'])
            if key in sources:
                _same(event['source_payload'], sources[key])
            sources[key] = event['source_payload']
    G.unique(evidence)
    _same([event for entry in entries for event in grouped[entry['stream_id']]], events, 'schedule_mismatch')
    return grouped, sources


def _event_shape(event):
    kind = event['kind']
    if kind == 'task_started':
        G.require(event['task_id'] is not None and event['target'] is None and not event['related_refs'] and event['outcome'] is None and event['source_payload'] is None)
        return
    G.require(event['target'] is not None)
    if kind == 'outcome_observed':
        G.require(event['outcome'] is not None and event['task_id'] is not None and event['source_payload'] is None)
    else:
        G.require(event['outcome'] is None)
    if kind == 'material_available':
        G.require(event['source_payload'] is not None and event['task_id'] is None and not event['related_refs'])
    else:
        G.require(event['source_payload'] is None)
    if kind in ('correction_proposed', 'correction_committed'):
        G.require(len(event['related_refs']) == 1 and event['target'] != event['related_refs'][0])


def _source_guard(event):
    source = event['source_payload']
    if source is None:
        return
    G.require(source['text'] and source['created_at_ns'] <= source['updated_at_ns'] <= event['available_at_ns'])
    G.require(source['effective_at_ns'] is None or source['effective_at_ns'] <= event['available_at_ns'])
    expected = G.sha((source['text'] + '\n' + source['summary']).encode('utf8'))
    _same(event['target']['content_sha256'], expected, 'hash_mismatch')


def _event_causes(event, causes, previous_sources):
    kind, target = event['kind'], event['target']
    if kind == 'outcome_observed':
        G.require(sum(c['kind'] == 'task_started' and c['task_id'] == event['task_id'] for c in causes) == 1, 'causal_order')
        G.require(sum(c['kind'] == 'material_available' and c['target'] == target for c in causes) == 1, 'causal_order')
    elif kind == 'exposure_observed':
        G.require(G.ref_key(target) in previous_sources, 'causal_order')
    elif kind in ('correction_proposed', 'correction_committed'):
        G.require(all(G.ref_key(ref) in previous_sources for ref in [target] + event['related_refs']), 'causal_order')
        if kind == 'correction_committed':
            G.require(sum(c['kind'] == 'correction_proposed' and c['target'] == target and c['related_refs'] == event['related_refs'] for c in causes) == 1, 'causal_order')


def _stream_events(events):
    G.require(bool(events), 'schedule_mismatch')
    seen, sources, versions = {}, set(), {}
    previous_time = 0
    scope, clock = events[0]['scope'], events[0]['clock_domain']
    for seq, event in enumerate(events, 1):
        _same(event['plan_seq'], seq, 'causal_order')
        _same(event['scope'], scope)
        _same(event['clock_domain'], clock)
        G.require(event['available_at_ns'] >= previous_time, 'causal_order')
        for field in ('source_at_ns', 'arrived_at_ns'):
            G.require(event[field] is None or event[field] <= event['available_at_ns'], 'causal_order')
        G.unique(event['cause_event_ids'])
        causes = [G.lookup(seen, symbol) for symbol in event['cause_event_ids']]
        G.require(all(c['available_at_ns'] <= event['available_at_ns'] for c in causes), 'causal_order')
        _event_shape(event)
        _event_causes(event, causes, sources)
        _source_guard(event)
        if event['kind'] == 'material_available':
            target, source = event['target'], event['source_payload']
            sources.add(G.ref_key(target))
            identity = tuple(target[f] for f in ('origin', 'collection', 'id'))
            versions.setdefault(identity, {})[target['revision']] = source
        seen[event['event_id']] = event
        previous_time = event['available_at_ns']
    for group in versions.values():
        rows = [group[revision] for revision in sorted(group)]
        G.require(len({r['created_at_ns'] for r in rows}) == 1)
        G.require([r['updated_at_ns'] for r in rows] == sorted(r['updated_at_ns'] for r in rows))


def _query_prefix(query, events):
    cutoff = query['cutoff_plan_seq']
    G.require(cutoff <= len(events), 'schedule_mismatch')
    if cutoff:
        G.require(events[cutoff - 1]['available_at_ns'] <= query['at_ns'], 'schedule_mismatch')
    if cutoff < len(events):
        G.require(events[cutoff]['available_at_ns'] >= query['at_ns'], 'schedule_mismatch')
    G.require(bool(query['query_text']))
    if query['request_mode'] == 'current':
        G.require(query['requested_as_of_ns'] is None)
    else:
        G.require(query['requested_as_of_ns'] is not None and query['requested_as_of_ns'] <= query['at_ns'])
    if events:
        _same(query['scope'], events[0]['scope'])
        _same(query['clock_domain'], events[0]['clock_domain'])
    return events[:cutoff]


def _gold_sets(gold):
    G.answer_sets(gold['required_refs'], gold['alternative_ref_sets'], gold['forbidden_refs'], gold['plausible_refs'], gold['cohort'])
    for group in gold['alternative_ref_sets']:
        _same(group, sorted(group, key=G.ref_key), 'gold_semantics')


def _gold_source_guard(gold, prefix, all_sources):
    available = {G.ref_key(e['target']) for e in prefix if e['kind'] == 'material_available'}
    withdrawn = {G.ref_key(e['target']) for e in prefix if e['kind'] in ('material_deleted', 'scope_changed')}
    positive = gold['required_refs'] + gold['plausible_refs'] + [r for group in gold['alternative_ref_sets'] for r in group]
    G.require(all(G.ref_key(ref) in available - withdrawn for ref in positive), 'gold_semantics')
    G.require(all(G.ref_key(ref) in all_sources for ref in gold['forbidden_refs']), 'gold_semantics')


def _gold_join(query, gold, prefix, all_sources):
    for field in ('stream_id', 'query_id', 'scope', 'clock_domain', 'at_ns', 'cutoff_plan_seq'):
        _same(gold[field], query[field], 'gold_incomplete')
    _same(gold['temporal_intent'], query['request_mode'], 'gold_semantics')
    _same(gold['historical_as_of_ns'], query['requested_as_of_ns'], 'gold_semantics')
    G.unique(gold['cause_event_ids'])
    G.require(set(gold['cause_event_ids']) <= {e['event_id'] for e in prefix}, 'causal_order')
    _gold_sets(gold)
    _gold_source_guard(gold, prefix, all_sources)


def _entry_axes(entry):
    fields = ('fact_group_ids', 'template_family_ids', 'entity_ids', 'topic_ids', 'entity_topic_pairs')
    for field in fields:
        G.unique(entry[field], nonempty=True)
    G.index(entry['slot_bindings'], 'slot_name')
    _same(set(entry['entity_ids']), {s['entity_id'] for s in entry['slot_bindings']}, 'split_overlap')
    pairs = entry['entity_topic_pairs']
    _same({p['entity_id'] for p in pairs}, set(entry['entity_ids']), 'split_overlap')
    _same({p['topic_id'] for p in pairs}, set(entry['topic_ids']), 'split_overlap')
    axes = {field: {G.compact(v) for v in entry[field]} for field in fields}
    axes['root_stream'] = {G.compact(entry['root_stream_id'])}
    axes['seed'] = {G.compact(entry['seed'])}
    return axes


def _split_axes(manifest):
    seen = {split: {} for split in G.ENUMS['SplitRole']}
    for entry in manifest['entries']:
        axes = _entry_axes(entry)
        checked = ['fact_group_ids', 'template_family_ids', 'entity_topic_pairs', 'root_stream', 'seed']
        if manifest['split_mode'] == 'entity_and_topic_disjoint':
            checked += ['entity_ids', 'topic_ids']
        for field in checked:
            for split, values in seen.items():
                if split != entry['split']:
                    G.require(not axes[field] & values.get(field, set()), 'split_overlap')
            seen[entry['split']].setdefault(field, set()).update(axes[field])
    _same({entry['split'] for entry in manifest['entries']}, set(G.ENUMS['SplitRole']), 'split_overlap')


def validate_artifacts(events, queries, gold, manifest):
    for kind, rows in [('PlanEvent', events), ('QueryPlan', queries), ('GoldRow', gold)]:
        G.require(bool(rows) and len(rows) <= G.MAX_ROWS, 'input_limit')
        for row in rows:
            G.validate_typed(kind, row)
    G.validate_typed('SplitManifest', manifest)
    entries = list(G.index(manifest['entries'], 'stream_id').values())
    G.require(len(entries) <= G.MAX_ROWS, 'input_limit')
    grouped, all_sources = _events_by_stream(events, entries)
    for rows in grouped.values():
        _stream_events(rows)
    G.index(queries, 'query_id')
    G.index(gold, 'query_id')
    _same([r['query_id'] for r in gold], [q['query_id'] for q in queries], 'gold_incomplete')
    query_groups = {e['stream_id']: [] for e in entries}
    primary = dict.fromkeys(query_groups, 0)
    for query, label in zip(queries, gold):
        G.require(query['stream_id'] in grouped, 'identity_conflict')
        query_groups[query['stream_id']].append(query)
        prefix = _query_prefix(query, grouped[query['stream_id']])
        _gold_join(query, label, prefix, all_sources)
        _same(label['label_version'], manifest['label_version'], 'gold_incomplete')
        primary[query['stream_id']] += label['cohort'] == 'primary_answerable'
    _same([q for e in entries for q in query_groups[e['stream_id']]], queries, 'schedule_mismatch')
    for entry in entries:
        rows = query_groups[entry['stream_id']]
        G.require(bool(rows), 'schedule_mismatch')
        _same(rows, sorted(rows, key=lambda q: (q['at_ns'], q['cutoff_plan_seq'], q['query_id'])), 'schedule_mismatch')
        G.require(bool(primary[entry['stream_id']]) == (entry['stream_cohort'] == 'primary_eligible'), 'gold_semantics')
    _split_axes(manifest)
    _same(manifest['external_dependencies'], G.DEPENDENCIES, 'dependency_mismatch', '$.external_dependencies')
    _same(manifest['contract_sha256'], G.CONTRACT_SHA256, 'dependency_mismatch')
    _same(manifest['split_assignment_sha256'], G.semantic_sha('SplitAssignmentProjection', G.split_projection(manifest)), 'hash_mismatch')


def _review_reference(reference, label_version):
    values = [reference[k] for k in ('reviewer_id', 'review_record', 'reviewed_gold_sha256')]
    pending = all(v is None for v in values)
    supplied = all(v is not None for v in values)
    G.require((pending and reference['status'] == 'pending_independent_review') or
              (supplied and reference['status'] == 'record_supplied'), 'review_unresolved', '$.independent_gold_review')
    _same(reference['label_version'], label_version, 'review_unresolved', '$.independent_gold_review')
    return supplied


def _protocol(value):
    G.validate_typed('ProtocolDraft', value)
    G.unique(value['unresolved'])
    _same(set(value['unresolved']), set(G.ENUMS['ResearchDecisionID']))
    G.unique(value['ref_contract']['unresolved'])
    _same(set(value['ref_contract']['unresolved']), set(G.ENUMS['REFDecisionID']))
    _same(value['ref_contract_sha256'], G.semantic_sha('REFContract', value['ref_contract']), 'hash_mismatch', '$.ref_contract_sha256')


def validate_protocol(protocol_raw, requested_state):
    G.require(requested_state in ('draft', 'final_locked'), 'invalid_value')
    value = G.decode_typed(protocol_raw, 'ProtocolDraft')
    _protocol(value)
    _review_reference(value['independent_gold_review'], value['independent_gold_review']['label_version'])
    if requested_state == 'final_locked':
        G.fail('final_lock_forbidden_in_draft')


def _artifact_reference(reference, name, raw):
    G.require(reference['path'] == name, 'review_unresolved')
    _same(reference['sha256'], G.sha(raw), 'hash_mismatch')
    _same(reference['size_bytes'], len(raw), 'hash_mismatch')


def _review_record(record, reference, manifest, gold, data):
    fields = ('dataset_id', 'dataset_version', 'label_version', 'generator_author_ids', 'label_author_ids', 'split_assignment_sha256')
    for field in fields:
        _same(record[field], manifest[field], 'review_unresolved')
    _same(record['dataset_contract_sha256'], manifest['contract_sha256'], 'review_unresolved')
    _same(record['generator_spec_sha256'], manifest['spec_sha256'], 'review_unresolved')
    for field, filename in zip(G.FILE_FIELDS, G.FILES):
        _same(record[field], G.sha(data[filename]), 'review_unresolved')
    _same(reference['reviewer_id'], record['reviewer_id'], 'review_unresolved')
    _same(reference['reviewed_gold_sha256'], record['gold_sha256'], 'review_unresolved')
    authors = record['generator_author_ids'] + record['label_author_ids']
    G.require(record['reviewer_id'] not in authors, 'review_unresolved')
    for field in ('generator_author_ids', 'label_author_ids'):
        G.unique(record[field], nonempty=True)
    labels = {row['query_id']: row for row in gold}
    items = G.index(record['items'], 'query_id')
    _same(set(items), set(labels), 'review_unresolved')
    for query_id, item in items.items():
        _same(item['gold_row_sha256'], G.semantic_sha('GoldRow', labels[query_id]), 'review_unresolved')
        _same(item['rule_id'], labels[query_id]['label_rule_id'], 'review_unresolved')
        _same(item['judgment'], 'confirmed_provisional', 'review_unresolved')
    G.unique([(d['query_id'], d['code']) for d in record['disagreements']])
    for disagreement in record['disagreements']:
        G.require(disagreement['query_id'] in labels, 'review_unresolved')
        G.require(disagreement['resolution'] == 'resolved_without_gold_change' and disagreement['adjudicator_id'] is not None and
                  disagreement['adjudicator_id'] not in authors, 'review_unresolved')
    _same(record['status'], 'completed_provisional_review', 'review_unresolved')


def _review(manifest, protocol, gold, data, evidence_root):
    reference = manifest['review']
    _same(reference, protocol['independent_gold_review'], 'review_unresolved', '$.independent_gold_review')
    supplied = _review_reference(reference, manifest['label_version'])
    if not supplied:
        return 'pending'
    raw = G.bounded_read(evidence_root / 'review.json')
    _artifact_reference(reference['review_record'], 'review.json', raw)
    record = G.decode_typed(raw, 'ReviewRecord')
    _review_record(record, reference, manifest, gold, data)
    return 'record_joins_verified_not_authority'


def _rows(raw, kind):
    G.require(raw.endswith(b'\n') and not raw.startswith(b'\xef\xbb\xbf'), 'invalid_json')
    rows = []
    for line in raw.splitlines():
        G.require(bool(line), 'invalid_json')
        G.require(len(rows) < G.MAX_ROWS, 'input_limit')
        row = G.decode_typed(line, kind)
        _same(G.compact(row), line, 'invalid_value')
        rows.append(row)
    _same(b''.join(G.compact(row) + b'\n' for row in rows), raw, 'invalid_value')
    return rows


def validate_dataset(dataset_dir, spec_raw, protocol_raw, evidence_root):
    dataset = G.checked_directory(dataset_dir)
    evidence = G.checked_directory(evidence_root)
    manifest_raw = G.bounded_read(dataset / 'split_manifest.json')
    manifest = G.decode_typed(manifest_raw, 'SplitManifest')
    _same(G.compact(manifest) + b'\n', manifest_raw, 'invalid_value')
    spec = G.decode_typed(spec_raw, 'GeneratorSpec')
    protocol = G.decode_typed(protocol_raw, 'ProtocolDraft')
    _same(manifest['spec_sha256'], G.sha(spec_raw), 'hash_mismatch')
    data, all_rows = {'split_manifest.json': manifest_raw}, []
    for name, field, kind in zip(G.FILES, G.FILE_FIELDS, ('PlanEvent', 'QueryPlan', 'GoldRow')):
        raw = G.bounded_read(dataset / name)
        _same(G.sha(raw), manifest['files'][field], 'hash_mismatch')
        data[name] = raw
        all_rows.append(_rows(raw, kind))
    validate_artifacts(*all_rows, manifest)
    expected, expected_rows = G.expand_spec(spec_raw)
    for name in G.FILES[:3]:
        _same(data[name], expected[name], 'identity_conflict')
    regenerated = expected_rows[3]
    actual_except_review = {k: v for k, v in manifest.items() if k != 'review'}
    expected_except_review = {k: v for k, v in regenerated.items() if k != 'review'}
    _same(actual_except_review, expected_except_review, 'identity_conflict')
    review = _review(manifest, protocol, all_rows[2], data, evidence)
    if protocol['dataset_reference'] is not None:
        _artifact_reference(protocol['dataset_reference'], 'split_manifest.json', manifest_raw)
    _protocol(protocol)
    _same(spec['label_version'], manifest['label_version'], 'identity_conflict')
    result = dict(protocol='exp01-validation-result-v1', status='valid_provisional_draft', dataset_manifest_sha256=G.sha(manifest_raw),
                  protocol_sha256=G.sha(protocol_raw), verification_scope='synthetic_plan_only', final_locked=False,
                  materialization='not_run', gold_review=review, unresolved=list(protocol['unresolved']))
    G.validate_typed('ValidationResult', result)
    return result


def main(argv=None):
    def run():
        args = G.cli_args(sys.argv[1:] if argv is None else argv,
                          ('--dataset', '--spec', '--protocol', '--evidence-root', '--request-state'))
        G.require(args['--request-state'] in ('draft', 'final_locked'), 'invalid_argv')
        spec_raw = G.bounded_read(args['--spec'])
        protocol_raw = G.bounded_read(args['--protocol'])
        value = validate_dataset(Path(args['--dataset']), spec_raw, protocol_raw, Path(args['--evidence-root']))
        if args['--request-state'] == 'final_locked':
            G.fail('final_lock_forbidden_in_draft')
        return value
    return G.cli_result(run)


if __name__ == '__main__':
    raise SystemExit(main())

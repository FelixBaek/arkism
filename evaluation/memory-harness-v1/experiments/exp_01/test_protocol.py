"""Hand-authored provisional scenarios and independent byte/boundary oracles."""
import copy
from functools import partial
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

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
    spec.loader.exec_module(module)
    return module


G = _load_generator()
V = G.load_sibling('validate_splits')


def literal(text):
    return dict(kind='literal', literal=text, slot_name=None)


def scenario():
    text = [dict(kind='entity_slot', literal=None, slot_name='who'), literal(' '),
            dict(kind='topic_slot', literal=None, slot_name=None)]
    materials = []
    for symbol, revision, word, anchor in [('old', 1, ' old', 'a'), ('new', 2, ' new', 'b')]:
        materials.append(dict(symbol=symbol, revision=revision, text=text + [literal(word)], summary=[], kind='semantic',
                              created_anchor='a', updated_anchor=anchor, effective_anchor=None,
                              identity_symbol='fact', fact_group_slot='fact'))
    def event(symbol, kind, at, target=None, related=(), causes=(), task=None, outcome=None):
        return dict(symbol=symbol, available_anchor=at, source_anchor=at, arrived_anchor=at, kind=kind,
                    target_symbol=target, related_symbols=list(related), cause_symbols=list(causes), task_symbol=task, outcome=outcome)
    outcome = dict(judgment='passed', evidence_symbol='external_check', source_rule_sha256=hashlib.sha256(b'synthetic rule').hexdigest(),
                   generation='exogenous_synthetic', validity='declared_not_authenticated')
    events = [event('source_old', 'material_available', 'a', 'old'), event('task', 'task_started', 'a', task='task'),
              event('source_new', 'material_available', 'b', 'new'),
              event('proposal', 'correction_proposed', 'b', 'old', ('new',), ('source_old', 'source_new')),
              event('commit', 'correction_committed', 'c', 'old', ('new',), ('proposal',)),
              event('late', 'outcome_observed', 'd', 'old', causes=('source_old', 'task'), task='task', outcome=outcome),
              event('withdraw', 'scope_changed', 'd', 'old', causes=('source_old',))]
    questions, rules = [], []
    cases = [('before', 'a', 'task', 'primary_answerable', ['old'], [], [], 'none', None),
             ('pending', 'b', 'proposal', 'primary_answerable', ['old'], [], [], 'pending', None),
             ('current', 'c', 'commit', 'primary_answerable', ['new'], [], ['old'], 'committed', None),
             ('history', 'c', 'commit', 'primary_answerable', ['old'], [], [], 'committed', 'a'),
             ('alternative', 'b', 'proposal', 'alternative_answers', [], [['old'], ['new']], [], 'pending', None),
             ('ambiguous', 'b', 'proposal', 'ambiguous', [], [], [], 'pending', None),
             ('after', 'd', 'withdraw', 'clear_negative', [], [], ['old'], 'committed', None)]
    for symbol, at, cutoff, cohort, required, alternatives, forbidden, phase, history in cases:
        questions.append(dict(symbol=symbol, at_anchor=at, cutoff_event_symbol=cutoff, query_text=text + [literal(' request ' + symbol)],
                              request_mode='historical' if history else 'current', requested_as_of_anchor=history))
        rules.append(dict(question_symbol=symbol, cohort=cohort, temporal_intent='historical' if history else 'current',
                          historical_as_of_anchor=history, correction_phase=phase, required_symbols=required,
                          alternative_symbol_sets=alternatives, forbidden_symbols=forbidden,
                          plausible_symbols=['old'] if cohort == 'ambiguous' else [], cause_symbols=[], label_rule_id='manual_rule'))
    return dict(variant_id='manual', slot_names=['who'], materials=materials, events=events, questions=questions,
                gold_rules=rules, stream_cohort='primary_eligible')


def fixture_spec():
    partitions, families, expansions = [], [], []
    for number, split in enumerate(('development', 'validation', 'final_test')):
        topic, family, fact = 'topic_' + str(number), 'family_' + str(number), 'fact_' + str(number)
        partitions.append(dict(split=split, entity_slots=[dict(entity_id='entity', display_text='Mina')],
                               topic_slots=[dict(topic_id=topic, display_text='alpha')], fact_group_ids=[fact],
                               entity_topic_pairs=[dict(entity_id='entity', topic_id=topic)]))
        families.append(dict(family_id=family, assigned_split=split, template_version='v1', semantic_review_state='provisional', variants=[scenario()]))
        expansions.append(dict(root_stream_symbol='root_' + str(number), instance_symbols=['one'], split=split,
                               template_family_id=family, time_regime_id='regular', stream_seeds=[number + 1],
                               allowed_entity_topic_pairs=[dict(entity_id='entity', topic_id=topic)],
                               slot_entity_bindings=[dict(slot_name='who', pool_entity_ids=['entity'])],
                               fact_group_bindings=[dict(slot_name='fact', fact_group_id=fact)], scope_strategy='independent_virtual_stream'))
    return dict(protocol='exp01-generator-spec-v1', dataset_id='unit_plan', dataset_version='v1', generator_version='v1', label_version='labels_v1',
                generator_author_ids=['generator_author'], label_author_ids=['label_author'], seed=11,
                external_dependencies=dict(G.DEPENDENCIES), split_mode='combination_disjoint', partitions=partitions,
                template_families=families, time_regimes=[dict(regime_id='regular', kind='regular', base_ns=10,
                steps=[dict(anchor_id=anchor, gap_min_ns=gap, gap_max_ns=gap) for anchor, gap in [('a', 0), ('b', 5), ('c', 5), ('d', 5)]])], expansions=expansions)


def raw_spec(spec=None):
    return json.dumps(fixture_spec() if spec is None else spec, ensure_ascii=False).encode('utf8')


def protocol(label='labels_v1'):
    value = json.loads((G.HERE / 'protocol.template.json').read_bytes())
    value['independent_gold_review']['label_version'] = label
    return value


class Checks(unittest.TestCase):
    def assert_error(self, code, call):
        with self.assertRaises(G.ExpError) as caught:
            call()
        self.assertEqual(caught.exception.code, code)
        self.assertTrue(G.known_path(caught.exception.path), caught.exception.path)
        return caught.exception

    def test_closed_contract_pins_registry_and_REF_literal(self):
        self.assertEqual(G.sha((G.HERE / 'dataset_contract.json').read_bytes()), G.CONTRACT_SHA256)
        self.assertEqual(len(G.MODELS), 40)
        old = copy.deepcopy(G.IO._OBJECTS)
        V.validate_protocol((G.HERE / 'protocol.template.json').read_bytes(), 'draft')
        self.assertEqual(G.IO._OBJECTS, old)
        self.assertNotIn('GeneratorSpec', old)
        value = protocol()
        self.assertEqual(value['ref_contract']['eligible_base'], ['B0', 'B1', 'V3-P1-FD', 'V3-P1-IR'])
        self.assertEqual(value['ref_contract_sha256'], '435819cc8d6ad7d769703a23c7a1f75130793db9b0b16962c33ea4ddadf098af')
        self.assertTrue(all(v is None for v in value['research'].values()))

    def test_source_time_revision_and_delayed_target_hand_oracle(self):
        data, (events, queries, gold, manifest) = G.expand_spec(raw_spec())
        V.validate_artifacts(events, queries, gold, manifest)
        source = next(e for e in events if e['kind'] == 'material_available')
        self.assertEqual(source['source_payload']['text'], 'Mina alpha old')
        self.assertEqual(source['target']['content_sha256'], hashlib.sha256(b'Mina alpha old\n').hexdigest())
        rows = [e for e in events if e['stream_id'] == source['stream_id']]
        self.assertEqual([e['available_at_ns'] for e in rows], [10, 10, 15, 15, 20, 25, 25])
        self.assertEqual(rows[0]['target']['id'], rows[2]['target']['id'])
        self.assertEqual([rows[0]['target']['revision'], rows[2]['target']['revision']], [1, 2])
        self.assertEqual(rows[5]['target'], rows[0]['target'])
        self.assertNotEqual(rows[5]['target'], rows[2]['target'])
        self.assertEqual(set(data), set(G.FILES))

    def test_exact_ID_tuple_independent_bytes(self):
        expected = 'q' + hashlib.sha256(b'exp01-id-v1\0query\x0011\x001\0root_0\0one\0history').hexdigest()[:62]
        self.assertEqual(G.opaque('query', 11, 1, 'root_0', 'one', 'history'), expected)
        body = b'{"judgment":"passed"}'
        self.assertNotEqual(hashlib.sha256(b'exp01-draft-v1\0GoldRow\0' + body).hexdigest(), hashlib.sha256(body).hexdigest())

    def test_roundtrip_safe_publication_nooverwrite_and_determinism(self):
        spec = raw_spec()
        before = bytes(spec)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first, second = root / 'first', root / 'second'
            result = G.generate_streams(spec, first)
            G.generate_streams(spec, second)
            for name in G.FILES:
                self.assertEqual((first / name).read_bytes(), (second / name).read_bytes())
                self.assertEqual((first / name).stat().st_mode & 0o777, 0o600)
            self.assertEqual(first.stat().st_mode & 0o777, 0o700)
            status = V.validate_dataset(first, spec, G.compact(protocol()), root)
            self.assertEqual(status['gold_review'], 'pending')
            self.assertFalse(status['final_locked'])
            self.assertEqual(result['manifest_sha256'], status['dataset_manifest_sha256'])
            self.assert_error('output_exists', lambda: G.generate_streams(spec, first))
        self.assertEqual(spec, before)

    def test_raw_hash_is_not_reconstructed_dictionary_hash(self):
        a = raw_spec()
        b = G.compact(fixture_spec())
        self.assertEqual(G.parse_raw(a), G.parse_raw(b))
        self.assertNotEqual(G.sha(a), G.sha(b))
        self.assertNotEqual(G.expand_spec(a)[1][3]['spec_sha256'], G.expand_spec(b)[1][3]['spec_sha256'])

    def test_raw_lexical_and_unicode_errors(self):
        cases = [(b'\xff', 'invalid_unicode'), (b'{"x":"\\ud800"}', 'invalid_unicode'),
                 (b'{"x":0,"x":1}', 'duplicate_key'), (b'{"x":1.0}', 'invalid_type'),
                 (b'{"x":1e0}', 'invalid_type'), (b'{"x":1e999}', 'invalid_value'),
                 (b'{"x":9223372036854775808}', 'invalid_value'), (b'NaN', 'invalid_json'),
                 (b'[' * 33 + b'0' + b']' * 33, 'input_limit')]
        for raw, code in cases:
            with self.subTest(raw=raw):
                self.assert_error(code, lambda raw=raw: G.parse_raw(raw))

    def test_unknown_fields_and_integer_bool_not_coerced(self):
        spec = fixture_spec()
        spec['private_unknown_marker'] = 'do not echo'
        error = self.assert_error('unknown_field', partial(G.expand_spec, raw_spec(spec)))
        self.assertNotIn('private_unknown_marker', str(error))
        self.assertNotIn('do not echo', str(error))
        spec = fixture_spec()
        spec['seed'] = True
        self.assert_error('invalid_type', partial(G.expand_spec, raw_spec(spec)))

    def test_complete_root_split_axes_and_stronger_mode(self):
        spec = fixture_spec()
        G.expand_spec(raw_spec(spec))
        spec['split_mode'] = 'entity_and_topic_disjoint'
        rows = G.expand_spec(raw_spec(spec))[1]
        self.assert_error('split_overlap', partial(V.validate_artifacts, *rows))
        spec = fixture_spec()
        spec['expansions'][1]['stream_seeds'] = [1]
        self.assert_error('split_overlap', lambda: V.validate_artifacts(*G.expand_spec(raw_spec(spec))[1]))

    def test_empty_axes_wrong_pair_and_unbound_slot_rejected(self):
        for kind in ('empty_fact', 'bad_pair', 'slot'):
            with self.subTest(kind=kind):
                spec = fixture_spec()
                if kind == 'empty_fact':
                    spec['partitions'][0]['fact_group_ids'] = []
                elif kind == 'bad_pair':
                    spec['partitions'][0]['entity_topic_pairs'][0]['entity_id'] = 'missing'
                else:
                    spec['expansions'][0]['slot_entity_bindings'][0]['slot_name'] = 'missing'
                with self.assertRaises(G.ExpError):
                    G.expand_spec(raw_spec(spec))

    def test_query_time_tie_is_explicit_prefix_not_all_ties(self):
        rows = copy.deepcopy(G.expand_spec(raw_spec())[1])
        events, queries, gold, manifest = rows
        query = next(q for q in queries if q['at_ns'] == 10)
        label = next(g for g in gold if g['query_id'] == query['query_id'])
        query['cutoff_plan_seq'] = label['cutoff_plan_seq'] = 1
        V.validate_artifacts(events, queries, gold, manifest)
        query['cutoff_plan_seq'] = label['cutoff_plan_seq'] = 3
        self.assert_error('schedule_mismatch', partial(V.validate_artifacts, *rows))

    def test_same_time_before_all_events_and_strict_past_omission(self):
        events = [e for e in G.expand_spec(raw_spec())[1][0] if e['stream_id'] == G.expand_spec(raw_spec())[1][0][0]['stream_id']]
        query = dict(G.expand_spec(raw_spec())[1][1][0])
        query.update(stream_id=events[0]['stream_id'], scope=events[0]['scope'], clock_domain=events[0]['clock_domain'], at_ns=10, cutoff_plan_seq=0)
        self.assertEqual(V._query_prefix(query, events), [])
        query['at_ns'] = 11
        self.assert_error('schedule_mismatch', partial(V._query_prefix, query, events))

    def test_causal_task_source_and_committed_pair_counterexamples(self):
        for kind in ('task', 'source', 'commit'):
            with self.subTest(kind=kind):
                rows = copy.deepcopy(G.expand_spec(raw_spec())[1])
                events = rows[0]
                if kind == 'task':
                    next(e for e in events if e['kind'] == 'outcome_observed')['task_id'] = 'different'
                elif kind == 'source':
                    event = next(e for e in events if e['kind'] == 'outcome_observed')
                    event['target'] = next(e['target'] for e in events if e['stream_id'] == event['stream_id'] and e['kind'] == 'material_available' and e['target']['revision'] == 2)
                else:
                    event = next(e for e in events if e['kind'] == 'correction_committed')
                    event['target'], event['related_refs'][0] = event['related_refs'][0], event['target']
                self.assert_error('causal_order', partial(V.validate_artifacts, *rows))

    def test_history_exact_asof_and_withdrawn_source_forbidden(self):
        rows = copy.deepcopy(G.expand_spec(raw_spec())[1])
        history = next(g for g in rows[2] if g['temporal_intent'] == 'historical')
        history['historical_as_of_ns'] = 5
        self.assert_error('gold_semantics', partial(V.validate_artifacts, *rows))
        rows = copy.deepcopy(G.expand_spec(raw_spec())[1])
        label = next(g for g in rows[2] if g['cohort'] == 'clear_negative')
        label.update(cohort='primary_answerable', required_refs=label['forbidden_refs'], forbidden_refs=[])
        self.assert_error('gold_semantics', partial(V.validate_artifacts, *rows))

    def test_alternative_forbidden_inner_empty_and_duplicate_set(self):
        for kind in ('forbidden', 'empty', 'duplicate'):
            with self.subTest(kind=kind):
                row = next(g for g in copy.deepcopy(G.expand_spec(raw_spec())[1][2]) if g['cohort'] == 'alternative_answers')
                if kind == 'forbidden':
                    row['forbidden_refs'] = row['alternative_ref_sets'][0]
                elif kind == 'empty':
                    row['alternative_ref_sets'][0] = []
                else:
                    row['alternative_ref_sets'].append(row['alternative_ref_sets'][0])
                with self.assertRaises(G.ExpError):
                    V._gold_sets(row)

    def test_missing_gold_or_failed_source_hash_does_not_pass(self):
        rows = copy.deepcopy(G.expand_spec(raw_spec())[1])
        rows[2].pop()
        self.assert_error('gold_incomplete', partial(V.validate_artifacts, *rows))
        rows = copy.deepcopy(G.expand_spec(raw_spec())[1])
        next(e for e in rows[0] if e['source_payload'])['source_payload']['text'] = 'changed'
        self.assert_error('hash_mismatch', partial(V.validate_artifacts, *rows))

    def test_seed_changes_content_or_time_and_overflow_rejects(self):
        spec = fixture_spec()
        for pool in spec['partitions']:
            pool['entity_slots'].append(dict(entity_id='other', display_text='Noah'))
            pool['entity_topic_pairs'].append(dict(entity_id='other', topic_id=pool['topic_slots'][0]['topic_id']))
        for request, pool in zip(spec['expansions'], spec['partitions']):
            request['allowed_entity_topic_pairs'] = pool['entity_topic_pairs']
            request['slot_entity_bindings'][0]['pool_entity_ids'].append('other')
        values = set()
        for seed in (0, 6):
            spec['seed'] = seed
            data = G.expand_spec(raw_spec(spec))[1]
            stream = G.opaque('stream', seed, 1, 'root_0', 'one', 'stream')
            values.add(next(e['source_payload']['text'] for e in data[0] if e['source_payload'] and e['stream_id'] == stream))
        self.assertGreater(len(values), 1)
        spec['time_regimes'][0]['base_ns'] = G.MAX_INT
        self.assert_error('invalid_value', partial(G.expand_spec, raw_spec(spec)))

    def test_final_locked_is_always_rejected(self):
        self.assert_error('final_lock_forbidden_in_draft', lambda: V.validate_protocol(G.compact(protocol()), 'final_locked'))
        value = protocol()
        value['ref_contract']['eligible_base'].append('B2')
        self.assert_error('invalid_value', lambda: V.validate_protocol(G.compact(value), 'draft'))

    def test_symlink_nooverwrite_and_partial_publish_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            link = root / 'broken'
            link.symlink_to(root / 'absent')
            self.assert_error('output_exists', lambda: G.generate_streams(raw_spec(), link))
            parent = root / 'parent'
            parent.symlink_to(root, target_is_directory=True)
            self.assert_error('unsafe_path', lambda: G.generate_streams(raw_spec(), parent / 'out'))
            helpers = G._fixture_helpers()
            with patch.object(G, '_fixture_helpers', return_value=helpers), patch.object(helpers, '_publish_file', side_effect=OSError('synthetic')):
                self.assert_error('io_error', lambda: G.generate_streams(raw_spec(), root / 'partial'))
            self.assertFalse((root / 'partial').exists())
            self.assertEqual(sorted(p.name for p in root.iterdir()), ['broken', 'parent'])

    def test_cli_end_to_end_and_fixed_errors(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec, proto, out = root / 'spec.json', root / 'protocol.json', root / 'out'
            spec.write_bytes(raw_spec())
            proto.write_bytes(G.compact(protocol()))
            command = [sys.executable, '-B', str(G.HERE / 'generate_streams.py'), '--spec', str(spec), '--out', str(out)]
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            command = [sys.executable, '-B', str(G.HERE / 'validate_splits.py'), '--dataset', str(out), '--spec', str(spec), '--protocol', str(proto), '--evidence-root', str(root), '--request-state', 'draft']
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            command[-1] = 'final_locked'
            result = subprocess.run(command, capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(json.loads(result.stdout)['code'], 'final_lock_forbidden_in_draft')
            result = subprocess.run(command + ['--spec', str(spec)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(json.loads(result.stdout)['code'], 'invalid_argv')
            self.assertEqual(result.stderr, '')

    def test_scope_uses_existing_wire_and_exact_component_hash_names(self):
        event = G.expand_spec(raw_spec())[1][0][0]
        self.assertEqual(set(event['scope']), {'workspace', 'project', 'user', 'agent'})
        for wire, component, prefix in [('workspace', 'workspace_id', 'w'), ('project', 'project_id', 'p'), ('user', 'user_id', 'u'), ('agent', 'agent_id', 'a')]:
            expected = prefix + hashlib.sha256(b'exp01-scope-v1\0' + component.encode() + b'\0' + event['stream_id'].encode()).hexdigest()[:62]
            self.assertEqual(event['scope'][wire], expected)
        G.IO.validate_typed('Scope', event['scope'])
        bad = dict(event['scope'])
        bad['workspace_id'] = bad.pop('workspace')
        with self.assertRaises(G.ExpError):
            G._typed('external:v1.Scope', bad, '$.scope')

    def review_fixture(self, root):
        spec_raw = raw_spec()
        out = root / 'out'
        G.generate_streams(spec_raw, out)
        data = {name: (out / name).read_bytes() for name in G.FILES}
        manifest = json.loads(data['split_manifest.json'])
        gold = [json.loads(line) for line in data['gold.jsonl'].splitlines()]
        def own_gold_hash(row):
            raw = json.dumps(row, ensure_ascii=False, sort_keys=True, separators=(',', ':')).encode('utf8')
            return hashlib.sha256(b'exp01-draft-v1\0GoldRow\0' + raw).hexdigest()
        record = dict(protocol='exp01-gold-review-v1', dataset_id=manifest['dataset_id'], dataset_version=manifest['dataset_version'],
                      label_version=manifest['label_version'], dataset_contract_sha256=manifest['contract_sha256'], generator_spec_sha256=manifest['spec_sha256'],
                      events_sha256=G.sha(data['events.jsonl']), queries_sha256=G.sha(data['queries.jsonl']), gold_sha256=G.sha(data['gold.jsonl']),
                      split_assignment_sha256=manifest['split_assignment_sha256'], generator_author_ids=manifest['generator_author_ids'],
                      label_author_ids=manifest['label_author_ids'], reviewer_id='synthetic_reviewer', reviewer_role='independent_gold_reviewer',
                      review_version='v1', items=[dict(query_id=g['query_id'], gold_row_sha256=own_gold_hash(g), judgment='confirmed_provisional', rule_id=g['label_rule_id']) for g in gold],
                      status='completed_provisional_review', disagreements=[], prior_review_sha256=None)
        return spec_raw, out, manifest, record

    def bind_review(self, root, manifest, record, out):
        review_raw = G.compact(record)
        (root / 'review.json').write_bytes(review_raw)
        reference = dict(reviewer_id=record['reviewer_id'], review_record=dict(path='review.json', sha256=G.sha(review_raw), size_bytes=len(review_raw)),
                         reviewed_gold_sha256=record['gold_sha256'], label_version=record['label_version'], status='record_supplied')
        manifest['review'] = reference
        manifest_raw = G.compact(manifest) + b'\n'
        (out / 'split_manifest.json').write_bytes(manifest_raw)
        value = protocol()
        value['independent_gold_review'] = copy.deepcopy(reference)
        value['dataset_reference'] = dict(path='split_manifest.json', sha256=G.sha(manifest_raw), size_bytes=len(manifest_raw))
        return G.compact(value)

    def test_actual_review_record_joins_and_only_review_regeneration_exception(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            spec, out, manifest, record = self.review_fixture(root)
            proto = self.bind_review(root, manifest, record, out)
            result = V.validate_dataset(out, spec, proto, root)
            self.assertEqual(result['gold_review'], 'record_joins_verified_not_authority')
            self.assertEqual(result['materialization'], 'not_run')
            manifest['entries'][0]['template_version'] = 'changed'
            (out / 'split_manifest.json').write_bytes(G.compact(manifest) + b'\n')
            with self.assertRaises(G.ExpError):
                V.validate_dataset(out, spec, proto, root)

    def test_review_wrong_author_rule_gold_version_disagreement_cannot_grant_acceptance(self):
        for kind in ('author', 'rule', 'gold_hash', 'label_version', 'query_coverage', 'unresolved'):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                spec, out, manifest, record = self.review_fixture(root)
                if kind == 'author':
                    record['reviewer_id'] = record['label_author_ids'][0]
                elif kind == 'rule':
                    record['items'][0]['rule_id'] = 'wrong_rule'
                elif kind == 'gold_hash':
                    record['items'][0]['gold_row_sha256'] = '0' * 64
                elif kind == 'label_version':
                    record['label_version'] = 'wrong_version'
                elif kind == 'query_coverage':
                    record['items'].pop()
                else:
                    record['disagreements'] = [dict(query_id=record['items'][0]['query_id'], code='temporal_meaning', resolution='open', adjudicator_id=None)]
                proto = self.bind_review(root, manifest, record, out)
                self.assert_error('review_unresolved', partial(V.validate_dataset, out, spec, proto, root))

    def test_pending_review_does_not_read_optional_review_file(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            out = root / 'out'
            G.generate_streams(raw_spec(), out)
            (root / 'review.json').symlink_to(root / 'absent')
            self.assertEqual(V.validate_dataset(out, raw_spec(), G.compact(protocol()), root)['gold_review'], 'pending')
            value = protocol()
            value['independent_gold_review']['reviewer_id'] = 'pretend'
            self.assert_error('review_unresolved', lambda: V.validate_dataset(out, raw_spec(), G.compact(value), root))

    def test_bad_data_rehash_cannot_bypass_deterministic_regeneration(self):
        with tempfile.TemporaryDirectory() as directory:
            root, spec = Path(directory), raw_spec()
            out = root / 'out'
            G.generate_streams(spec, out)
            rows = [json.loads(line) for line in (out / 'queries.jsonl').read_bytes().splitlines()]
            rows[0]['query_text'] = 'different visible query'
            raw = b''.join(G.compact(row) + b'\n' for row in rows)
            (out / 'queries.jsonl').write_bytes(raw)
            manifest = json.loads((out / 'split_manifest.json').read_bytes())
            manifest['files']['queries_sha256'] = G.sha(raw)
            (out / 'split_manifest.json').write_bytes(G.compact(manifest) + b'\n')
            self.assert_error('identity_conflict', lambda: V.validate_dataset(out, spec, G.compact(protocol()), root))

    def test_input_limit_is_bounded_before_large_file_parse(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'sparse.json'
            with path.open('wb') as stream:
                stream.truncate(G.MAX_BYTES + 1)
            self.assert_error('input_limit', lambda: G.bounded_read(path))
        self.assertEqual(G.parse_raw(b'[' * 32 + b'0' + b']' * 32), self.nested_array(32))

    def nested_array(self, count):
        value = 0
        for _ in range(count):
            value = [value]
        return value

    def test_lexical_depth_guard_ignores_braces_in_escaped_strings(self):
        value = {'text': '[' * 100 + '"\\]'}
        self.assertEqual(G.parse_raw(json.dumps(value).encode()), value)
        self.assert_error('invalid_json', lambda: G.parse_raw(b'{"x":"\\ud800",'))
        self.assert_error('invalid_unicode', lambda: G.parse_raw(b'{"x":"\\ud800","x":0}'))

    def test_exact_jsonl_encoding_rejects_CRLF_and_blank_rows(self):
        query = G.expand_spec(raw_spec())[1][1][0]
        for suffix in (b'\r\n', b'\n\n'):
            with self.subTest(suffix=suffix), self.assertRaises(G.ExpError):
                V._rows(G.compact(query) + suffix, 'QueryPlan')

    def test_publication_verifies_bytes_not_only_filename_presence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            helpers = G._fixture_helpers()
            original = helpers._write_bytes
            def corrupted(fd, name, data, owned):
                original(fd, name, data + b'corruption', owned)
            with patch.object(G, '_fixture_helpers', return_value=helpers), patch.object(helpers, '_write_bytes', side_effect=corrupted):
                self.assert_error('hash_mismatch', lambda: G.generate_streams(raw_spec(), root / 'out'))
            self.assertFalse((root / 'out').exists())

    def test_cli_missing_input_has_no_traceback_or_private_path(self):
        with tempfile.TemporaryDirectory() as directory:
            hidden = Path(directory) / 'private_marker_absent'
            result = subprocess.run([sys.executable, '-B', str(G.HERE / 'generate_streams.py'), '--spec', str(hidden), '--out', str(Path(directory) / 'out')], capture_output=True, text=True)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(json.loads(result.stdout), dict(protocol='exp01-error-v1', code='io_error', path='$'))
            self.assertEqual(result.stderr, '')
            self.assertNotIn(str(hidden), result.stdout)

    def test_dependency_initialization_error_is_fixed_in_both_CLIs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ('generate_streams.py', 'validate_splits.py'):
                (root / name).write_bytes((G.HERE / name).read_bytes())
            for name in ('generate_streams.py', 'validate_splits.py'):
                with self.subTest(name=name):
                    result = subprocess.run([sys.executable, '-B', str(root / name)], capture_output=True, text=True)
                    self.assertEqual(result.returncode, 2)
                    self.assertEqual(json.loads(result.stdout)['code'], 'dependency_mismatch')
                    self.assertEqual(result.stderr, '')

    def test_outer_importlib_loader_needs_no_sibling_sys_path(self):
        script = """import importlib.util,sys,unittest
from pathlib import Path
path=Path(sys.argv[1]);sys.path=[p for p in sys.path if str(path.parent) not in p]
sys.path.insert(0,str(path.parents[2]))
spec=importlib.util.spec_from_file_location('outer_exp01_probe',path)
module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
print(unittest.defaultTestLoader.loadTestsFromModule(module).countTestCases())
"""
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run([sys.executable, '-B', '-c', script, str(G.HERE / 'test_protocol.py')], cwd=directory, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertGreater(int(result.stdout.strip()), 0)

    def test_unselected_template_gold_rules_are_validated_before_selection(self):
        spec = fixture_spec()
        for number, family in enumerate(spec['template_families']):
            broken = copy.deepcopy(family['variants'][0])
            broken['variant_id'] = 'invalid_unselected'
            broken['gold_rules'][0]['required_symbols'] = []
            family['variants'].append(broken)
            root = spec['expansions'][number]['root_stream_symbol']
            seed = spec['expansions'][number]['stream_seeds'][0]
            selected = int.from_bytes(hashlib.sha256(b'exp01-expand-v1\x0011\0' + str(seed).encode() + b'\0' + root.encode() + b'\0one\0variant').digest(), 'big') % 2
            if selected == 1:
                family['variants'].reverse()
        self.assert_error('gold_semantics', partial(G.expand_spec, raw_spec(spec)))

    def test_sampled_time_regime_has_independent_hash_interval_oracle(self):
        spec = fixture_spec()
        regime = spec['time_regimes'][0]
        regime['kind'] = 'bounded_irregular'
        for step in regime['steps'][1:]:
            step.update(gap_min_ns=3, gap_max_ns=9)
        data = G.expand_spec(raw_spec(spec))[1]
        stream = G.opaque('stream', 11, 1, 'root_0', 'one', 'stream')
        rows = [e for e in data[0] if e['stream_id'] == stream]
        expected = [10]
        for anchor in ('b', 'c', 'd'):
            digest = hashlib.sha256(b'exp01-expand-v1\x0011\x001\0root_0\0one\0gap/' + anchor.encode()).digest()
            expected.append(expected[-1] + 3 + int.from_bytes(digest, 'big') % 7)
        self.assertEqual([rows[i]['available_at_ns'] for i in (0, 2, 4, 5)], expected)
        V.validate_artifacts(*data)

    def test_future_foreign_or_reobserved_withdrawn_source_cannot_be_positive_gold(self):
        for case in ('future', 'foreign', 'reobserved'):
            with self.subTest(case=case):
                rows = copy.deepcopy(G.expand_spec(raw_spec())[1])
                events, queries, labels, manifest = rows
                query = next(q for q in queries if q['at_ns'] == 10)
                label = next(g for g in labels if g['query_id'] == query['query_id'])
                if case == 'future':
                    label['required_refs'] = [next(e['target'] for e in events if e['stream_id'] == query['stream_id'] and e['kind'] == 'material_available' and e['target']['revision'] == 2)]
                elif case == 'foreign':
                    label['required_refs'] = [next(e['target'] for e in events if e['stream_id'] != query['stream_id'] and e['kind'] == 'material_available')]
                else:
                    label = next(g for g in labels if g['cohort'] == 'clear_negative')
                    prefix = [e for e in events if e['stream_id'] == label['stream_id']]
                    observed_again = copy.deepcopy(prefix[0])
                    observed_again.update(event_id='reobserved', plan_seq=8, available_at_ns=25)
                    prefix.append(observed_again)
                    label.update(cohort='primary_answerable', required_refs=label['forbidden_refs'], forbidden_refs=[])
                    self.assert_error('gold_semantics', lambda: V._gold_source_guard(label, prefix, {G.ref_key(e['target']): e['source_payload'] for e in prefix if e['source_payload']}))
                    continue
                self.assert_error('gold_semantics', partial(V.validate_artifacts, *rows))

    def test_unresolved_exact_sets_allow_reorder_without_normalizing_hashes(self):
        value = protocol()
        value['unresolved'].reverse()
        value['ref_contract']['unresolved'].reverse()
        literal = json.dumps(value['ref_contract'], ensure_ascii=False, sort_keys=True, separators=(',', ':')).encode('utf8')
        digest = hashlib.sha256(b'exp01-draft-v1\0REFContract\0' + literal).hexdigest()
        self.assertNotEqual(digest, value['ref_contract_sha256'])
        value['ref_contract_sha256'] = digest
        before = copy.deepcopy(value)
        V.validate_protocol(G.compact(value), 'draft')
        self.assertEqual(value, before)

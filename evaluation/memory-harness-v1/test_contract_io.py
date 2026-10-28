"""R2 regression tests: fixed schema, independent oracles, and file boundaries."""
import copy
import importlib.util
import json
import math
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

_SPEC = importlib.util.spec_from_file_location('arkism_contract_io_under_test', Path(__file__).with_name('contract_io.py'))
io = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(io)
_SCHEMA_BYTES = (Path(__file__).parent / 'contracts' / 'implementation_contract.json').read_bytes()
_SCHEMA = json.loads(_SCHEMA_BYTES)
CID = 'arkism-offline-b0-v1'
SCOPE = {'workspace': 'ws', 'project': 'proj', 'user': 'usr', 'agent': 'agt'}


def sample(name):
    result = {}
    for key, spec in _SCHEMA['objects'][name]['fields'].items():
        kind = spec['type']
        if kind.startswith('const_'):
            value = spec['value']
        elif kind == 'object':
            value = sample(spec['object'])
        elif kind == 'array_of_arrays':
            value = [[], [], []]
        elif kind == 'array':
            value = [0] if spec.get('min_items', 0) else []
        elif kind.startswith('nullable_'):
            value = None
        elif kind in ('int64', 'uint64'):
            value = spec.get('min', 0)
        elif kind == 'float64':
            value = float(spec.get('min', 0))
        elif kind == 'bool':
            value = False
        elif kind == 'enum':
            value = spec['values'][0]
        elif kind == 'sha256':
            value = '0' * 64
        elif kind == 'hex40':
            value = '0' * 40
        else:
            value = 'x'
        result[key] = value
    if name == 'Gold':
        result['relevant_refs'] = [sample('Ref')]
    elif name == 'Statistic':
        result['null_reason'] = 'no_denominator'
    elif name in ('InputManifest', 'EvaluationManifest'):
        names = (['candidates.json', 'implementation_contract.json', 'policy.json', 'probes.json']
                 if name == 'InputManifest' else ['gold.json', 'input_manifest.json', 'split_map.json'])
        result['files'] = [{'logical_path': n, 'sha256': '0' * 64, 'byte_length': 0} for n in names]
        if name == 'InputManifest':
            result['source_commit'] = '27bb41807ff48d47559ba2cfc30feb300caacecf'
    return result


def encoded(value):
    return json.dumps(value, ensure_ascii=False, allow_nan=False).encode('utf-8')


class ContractIOTests(unittest.TestCase):
    def assertCode(self, code, callback, path=None):
        with self.assertRaises(io.ContractError) as caught:
            callback()
        self.assertEqual(caught.exception.code, code)
        if path is not None:
            self.assertEqual(caught.exception.path, path)
        return caught.exception

    def test_27_types_150_fields_roundtrip(self):
        self.assertEqual(len(_SCHEMA['objects']), 27)
        self.assertEqual(sum(len(s['fields']) for s in _SCHEMA['objects'].values()), 150)
        for name in _SCHEMA['objects']:
            with self.subTest(name=name):
                value = sample(name)
                io.validate_typed(name, value)
                self.assertEqual(io.decode_typed(encoded(value), name), value)
                self.assertTrue(io.canonical_bytes(name, value).startswith(b'o'))

    def test_unknown_types_have_priority(self):
        for name in ('Event', 'State', 'Scope ', '', None):
            self.assertCode('unsupported_type', lambda: io.decode_typed(b'{', name), '$')
            self.assertCode('unsupported_type', lambda: io.validate_typed(name, {}), '$')

    def test_all_150_required_fields(self):
        for name, schema in _SCHEMA['objects'].items():
            for field in schema['fields']:
                value = sample(name)
                del value[field]
                with self.subTest(name=name, field=field):
                    self.assertCode('missing_field', lambda: io.decode_typed(encoded(value), name), '$.' + field)

    def test_unknown_field_never_leaks_key(self):
        value = dict(SCOPE, **{'secret-unknown-key': 'secret-value'})
        error = self.assertCode('unknown_field', lambda: io.decode_typed(encoded(value), 'Scope'), '$')
        self.assertNotIn('secret', str(error))
        self.assertEqual(error.detail, '')

    def test_recursive_shape_priority(self):
        raw = b'{"unknown-secret":1,"probes":[{"scope":{"unknown":1}}]}'
        self.assertCode('unknown_field', lambda: io.decode_typed(raw, 'ProbeFile'), '$')
        raw = b'{"unknown-secret":1,"probes":[{"scope":{"a":1,"a":2}}]}'
        self.assertCode('duplicate_key', lambda: io.decode_typed(raw, 'ProbeFile'), '$')
        value = sample('ProbeFile')
        value['probes'] = [sample('Probe')]
        del value['probes'][0]['scope']['agent']
        self.assertCode('missing_field', lambda: io.decode_typed(encoded(value), 'ProbeFile'), '$.probes[*].scope.agent')

    def test_duplicate_escaped_keys(self):
        self.assertCode('duplicate_key', lambda: io.decode_typed(b'{"agent":"a","\\u0061gent":"b"}', 'Scope'), '$')

    def test_json_syntax_precedes_shape(self):
        for raw in (b'{', b'{}{}', b'{"x":NaN}', b'{"x":Infinity}', b'{"x":-Infinity}', b'[] trailing', b'\xef\xbb\xbf{}'):
            self.assertCode('invalid_json', lambda: io.decode_typed(raw, 'Scope'))

    def test_deep_unknown_json_matches_go_parser_boundary(self):
        for depth in (996, 1500, 9999):
            for opening, closing in ((b'[', b']'), (b'{"y":', b'}')):
                raw = b'{"x":' + opening * depth + b'0' + closing * depth + b'}'
                with self.subTest(depth=depth, opening=opening):
                    self.assertCode('unknown_field', lambda: io.decode_typed(raw, 'Scope'), '$')
        for depth in (10000, 10001):
            raw = b'{"x":' + b'[' * depth + b'0' + b']' * depth + b'}'
            self.assertCode('invalid_json', lambda: io.decode_typed(raw, 'Scope'), '$')
        for payload, code in ((b'"\\ud800"', 'invalid_unicode'), (b'{"k":1,"k":2}', 'duplicate_key')):
            raw = b'{"x":' + b'[' * 1500 + payload + b']' * 1500 + b'}'
            self.assertCode(code, lambda: io.decode_typed(raw, 'Scope'), '$')

    def test_iterative_parser_matches_standard_json_grammar(self):
        valid = ['{}', '[]', '[{},[],true,false,null,0,-0,1.25,1e-5]',
                 '{"a":"\\uac00","b":"\\ud83d\\ude00","c":"\\\\\\\""}',
                 '  {"key" : [1, 2, 3]}\n']
        for text in valid:
            self.assertEqual(io._plain(io._parse_json(text)), json.loads(text))
        invalid = ['[1,]', '{"x":1,}', '{,"x":1}', '[,1]', '{1:2}',
                   '{"x" 1}', '{"x":}', '{"x"::1}', '{"x":1 "y":2}',
                   '[1 2]', '[}', '{]', '00', '01', '-01', '+1', '.1', '1.',
                   '1e', '1e+', '--1', 'True', 'undefined', 'null null',
                   'NaN', 'Infinity', '"\\x00"', '"\\u12"', '"line\nfeed"']
        base = json.dumps(SCOPE)
        # Removing each byte exercises punctuation, quoting, whitespace and
        # string boundaries without assuming every mutation is invalid JSON.
        variants = [base[:i] + base[i+1:] for i in range(len(base))]
        for text in invalid + variants:
            try:
                expected = json.loads(text)
            except ValueError:
                self.assertCode('invalid_json', lambda: io._parse_json(text))
            else:
                if text in ('NaN', 'Infinity'):
                    self.assertCode('invalid_json', lambda: io._parse_json(text))
                else:
                    self.assertEqual(io._plain(io._parse_json(text)), expected)

    def test_unicode_strict_everywhere(self):
        for raw in (b'{"x":"\xff"}', b'{"x":"\\ud800"}', b'{"\\udfff":"x"}'):
            self.assertCode('invalid_unicode', lambda: io.decode_typed(raw, 'Scope'), '$')
        self.assertCode('invalid_unicode', lambda: io.validate_typed('Scope', dict(SCOPE, agent='\ud800')))

    def test_utf8_surrogate_pair_is_decoded(self):
        # The scalar is valid Unicode; Scope's ASCII ID restriction then rejects it.
        self.assertCode('invalid_value', lambda: io.decode_typed(encoded(SCOPE).replace(b'"agt"', b'"\\ud83d\\ude00"'), 'Scope'), '$.agent')

    def test_int_lexical_forms_and_bool(self):
        base = encoded(sample('Ref'))
        for token in (b'1.0', b'1e0', b'true', b'null', b'"1"'):
            self.assertCode('invalid_type', lambda: io.decode_typed(base.replace(b'"revision": 1', b'"revision": ' + token), 'Ref'), '$.revision')
        for token in (b'0', b'-1', b'9223372036854775808', b'9' * 5000):
            self.assertCode('invalid_value', lambda: io.decode_typed(base.replace(b'"revision": 1', b'"revision": ' + token), 'Ref'), '$.revision')

    def test_exact_integer_boundaries(self):
        value = sample('Ref')
        value['revision'] = 9223372036854775807
        self.assertEqual(io.decode_typed(encoded(value), 'Ref')['revision'], 9223372036854775807)
        value = sample('InputManifest')
        value['seed'] = 18446744073709551615
        self.assertEqual(io.decode_typed(encoded(value), 'InputManifest')['seed'], 18446744073709551615)
        value['seed'] += 1
        self.assertCode('invalid_value', lambda: io.validate_typed('InputManifest', value), '$.seed')

    def test_float_lexical_and_finite(self):
        value = sample('Candidate')
        for token in (b'1e400', b'-1e400'):
            raw = encoded(value).replace(b'"trust": 0.0', b'"trust": ' + token)
            self.assertCode('invalid_value', lambda: io.decode_typed(raw, 'Candidate'), '$.trust')
        for number in (float('nan'), float('inf'), -float('inf'), -0.01, 1.01):
            value['trust'] = number
            self.assertCode('invalid_value', lambda: io.validate_typed('Candidate', value), '$.trust')
        value['trust'] = True
        self.assertCode('invalid_type', lambda: io.validate_typed('Candidate', value), '$.trust')
        value['trust'] = 1
        self.assertIs(type(io.decode_typed(encoded(value), 'Candidate')['trust']), float)

    def test_309_digit_integer_lexeme_can_be_finite_float64(self):
        value = sample('RankedCandidate')
        value['score'] = 10 ** 308
        decoded = io.decode_typed(encoded(value), 'RankedCandidate')
        self.assertEqual(decoded['score'], 1e308)
        self.assertIs(type(decoded['score']), float)
        # The largest finite binary64 is itself an integer with 309 digits.
        value['score'] = int(float.fromhex('0x1.fffffffffffffp+1023'))
        decoded = io.decode_typed(encoded(value), 'RankedCandidate')
        self.assertEqual(decoded['score'], float.fromhex('0x1.fffffffffffffp+1023'))
        value['score'] = 10 ** 309
        self.assertCode('invalid_value', lambda: io.decode_typed(encoded(value), 'RankedCandidate'), '$.score')

    def test_nested_arrays_nil_and_lengths(self):
        for lanes in (None, [[], []], [[], [], [], []], [None, [], []]):
            value = sample('CandidateSnapshot')
            value['lanes'] = lanes
            self.assertCode('invalid_type' if lanes is None or None in lanes else 'invalid_value', lambda: io.validate_typed('CandidateSnapshot', value))
        value = sample('RankedCandidate')
        for lanes in ([], [0, 0], [3], [True], [0, 1, 2, 0]):
            value['lanes'] = lanes
            self.assertCode('invalid_type' if lanes == [True] else 'invalid_value', lambda: io.validate_typed('RankedCandidate', value))

    def test_all_array_fields_reject_null(self):
        for name, schema in _SCHEMA['objects'].items():
            for field, spec in schema['fields'].items():
                if spec['type'].startswith('array'):
                    value = sample(name)
                    value[field] = None
                    with self.subTest(name=name, field=field):
                        self.assertCode('invalid_type', lambda: io.validate_typed(name, value), '$.' + field)

    def test_constants_id_hash_enum(self):
        for key, bad in (('id', 'Upper'), ('content_sha256', 'A' * 64), ('origin', 'other')):
            value = sample('Ref')
            value[key] = bad
            self.assertCode('invalid_value', lambda: io.validate_typed('Ref', value), '$.' + key)
        value = sample('Policy')
        value['contract_id'] = 'next-version'
        self.assertCode('unsupported_version', lambda: io.validate_typed('Policy', value), '$.contract_id')
        for key, bad in (('rank_offset', 61), ('exact_reason_weight', 1), ('context_unit', 'tokens')):
            value = sample('Policy')
            value[key] = bad
            self.assertCode('invalid_value', lambda: io.validate_typed('Policy', value), '$.' + key)

    def test_type_validation_does_not_mutate(self):
        value = sample('Candidate')
        value['trust'] = -0.0
        before = encoded(value)
        io.validate_typed('Candidate', value)
        io.canonical_bytes('Candidate', value)
        self.assertEqual(encoded(value), before)
        decoded = io.decode_typed(before, 'Candidate')
        self.assertEqual(math.copysign(1, decoded['trust']), 1)

    def test_independent_scope_canonical_golden(self):
        # GV-CAN-11, source golden SHA256:
        # 3ab88b141d399269478775bb2dd068a762d9444a43a46123a17dbe95c4c427c1
        expected = b'o4{s5:agents3:agts7:projects4:projs4:users3:usrs9:workspaces2:ws}'
        self.assertEqual(io.canonical_bytes('Scope', SCOPE), expected)
        self.assertEqual(io.canonical_sha256('Scope', SCOPE), '482d75fa9bd7a4686fffa00746432a8b4ad50a3329cb1ffa94948fd0858991c1')
        self.assertEqual(io.canonical_bytes('Scope', dict(reversed(list(SCOPE.items())))), expected)

    def test_independent_numeric_canonical(self):
        value = {'numerator': 1, 'denominator': 2, 'value': 0.5, 'null_reason': 'none'}
        self.assertEqual(io.canonical_bytes('Statistic', value), b'o4{s11:denominatori2;s11:null_reasons4:nones9:numeratord3ff0000000000000;s5:valued3fe0000000000000;}')
        a = sample('Candidate')
        b = copy.deepcopy(a)
        a['trust'] = -0.0
        self.assertEqual(io.canonical_bytes('Candidate', a), io.canonical_bytes('Candidate', b))
        self.assertNotEqual(io.sha256_bytes(encoded(SCOPE)), io.canonical_sha256('Scope', SCOPE))

    def test_nullable_distinct_from_absent_and_zero(self):
        null = sample('Statistic')
        zero = {'numerator': 0.0, 'denominator': 1, 'value': 0.0, 'null_reason': 'none'}
        self.assertNotEqual(io.canonical_bytes('Statistic', null), io.canonical_bytes('Statistic', zero))
        for bad in ({'numerator': 1.0, 'denominator': 0, 'value': None, 'null_reason': 'no_denominator'},
                    {'numerator': 1.0, 'denominator': 2, 'value': 0.6, 'null_reason': 'none'}):
            self.assertCode('invalid_value', lambda: io.validate_typed('Statistic', bad))

    def test_statistic_tolerance_adds_absolute_and_relative_terms(self):
        # golden_cases.md section 6: |actual-expected| <= 1e-12+1e-10*|expected|.
        # This valid delta exceeds max(abs_tol, rel_tol*expected), distinguishing
        # the specified additive bound from math.isclose's stricter maximum.
        value = {'numerator': 1.0, 'denominator': 2,
                 'value': 0.5 + 5.05e-11, 'null_reason': 'none'}
        io.validate_typed('Statistic', value)
        value['value'] = 0.5 + 5.2e-11
        self.assertCode('invalid_value', lambda: io.validate_typed('Statistic', value))

    def test_snapshot_conflicts_and_allowed_metadata(self):
        snapshot = sample('CandidateSnapshot')
        snapshot.update(as_of_ns=10, as_of_seq=2)
        candidate = sample('Candidate')
        snapshot['lanes'] = [[candidate], [], []]
        for key, bad in (('scope', SCOPE), ('observed_seq', 3), ('effective_at_ns', 11), ('updated_at_ns', 11)):
            altered = copy.deepcopy(snapshot)
            altered['lanes'][0][0][key] = bad
            self.assertCode('snapshot_conflict', lambda: io.validate_typed('CandidateSnapshot', altered))
        for key, bad in (('active', True), ('context_cost_units', 2), ('ref', dict(candidate['ref'], revision=2))):
            altered = copy.deepcopy(snapshot)
            other = copy.deepcopy(candidate)
            other[key] = bad
            altered['lanes'][1] = [other]
            self.assertCode('snapshot_conflict', lambda: io.validate_typed('CandidateSnapshot', altered))
        other = copy.deepcopy(candidate)
        other.update(trust=0.75, updated_at_ns=1, reasons=['vector'])
        snapshot['lanes'][1] = [other]
        io.validate_typed('CandidateSnapshot', snapshot)

    def test_local_gold_prediction_querymetric_rules(self):
        gold = sample('Gold')
        gold['relevant_refs'] = []
        self.assertCode('invalid_value', lambda: io.validate_typed('Gold', gold))
        prediction = sample('Prediction')
        prediction.update(status='timeout', error_code='none')
        self.assertCode('invalid_value', lambda: io.validate_typed('Prediction', prediction))
        query = sample('QueryMetric')
        query.update(status='timeout', recall_at_k=0.0, hit_any=0)
        io.validate_typed('QueryMetric', query)
        query['recall_at_k'] = None
        self.assertCode('invalid_value', lambda: io.validate_typed('QueryMetric', query))

    def test_fixed_hash_and_raw_hash_newline(self):
        self.assertEqual(io.sha256_bytes(_SCHEMA_BYTES), io.CONTRACT_SHA256)
        self.assertNotEqual(io.sha256_bytes(b'{}'), io.sha256_bytes(b'{}\n'))

    def fixture(self, root):
        policy = sample('Policy')
        probe = sample('Probe')
        snapshot = sample('CandidateSnapshot')
        probe.update(at_ns=10, cutoff_accepted_seq=2)
        snapshot.update(as_of_ns=10, as_of_seq=2)
        probes = {'contract_id': CID, 'probes': [probe]}
        candidates = {'contract_id': CID, 'snapshots': [snapshot]}
        gold = sample('Gold')
        gold.update(at_ns=10, cutoff_accepted_seq=2)
        objects = {'policy.json': policy, 'probes.json': probes, 'candidates.json': candidates,
                   'gold.json': {'contract_id': CID, 'labels': [gold]},
                   'split_map.json': {'contract_id': CID, 'streams': [sample('SplitEntry')]}}
        (root / 'implementation_contract.json').write_bytes(_SCHEMA_BYTES)
        for name, value in objects.items():
            (root / name).write_bytes(encoded(value))
        self.refresh(root)

    def refresh(self, root):
        for name, kind in (('input_manifest.json', 'InputManifest'), ('evaluation_manifest.json', 'EvaluationManifest')):
            manifest = sample(kind)
            for entry in manifest['files']:
                data = (root / entry['logical_path']).read_bytes()
                entry.update(sha256=io.sha256_bytes(data), byte_length=len(data))
            (root / name).write_bytes(encoded(manifest))

    def test_input_and_evaluation_exact_return_keys(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root = Path(directory)
            self.fixture(root)
            bundle = io.load_input_bundle(root / 'input_manifest.json')
            self.assertEqual(set(bundle), {'manifest', 'manifest_sha256', 'policy', 'policy_sha256', 'probes', 'candidates', 'contract_sha256'})
            evaluation = io.load_evaluation_bundle(root / 'evaluation_manifest.json')
            self.assertEqual(set(evaluation), {'input_bundle', 'manifest', 'manifest_sha256', 'gold', 'split_map'})
            self.assertEqual(evaluation['input_bundle'], bundle)

    def test_input_loader_never_opens_gold_or_split(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root = Path(directory)
            self.fixture(root)
            (root / 'gold.json').unlink()
            (root / 'split_map.json').unlink()
            with mock.patch.object(io, '_read_regular', wraps=io._read_regular) as reader:
                io.load_input_bundle(root / 'input_manifest.json')
            self.assertFalse({'gold.json', 'split_map.json', 'evaluation_manifest.json'} & {Path(c.args[0]).name for c in reader.call_args_list})

    def test_manifest_rejects_extra_roles_duplicates_order(self):
        for names in (['gold.json'], ['candidates.json'] * 4, list(reversed(['candidates.json', 'implementation_contract.json', 'policy.json', 'probes.json']))):
            manifest = sample('InputManifest')
            manifest['files'] = [{'logical_path': n, 'sha256': '0' * 64, 'byte_length': 0} for n in names]
            self.assertCode('invalid_value', lambda: io.validate_typed('InputManifest', manifest))
        for name in ('../policy.json', '/tmp/policy.json', 'sub/policy.json'):
            entry = sample('ArtifactEntry')
            entry['logical_path'] = name
            self.assertCode('invalid_value', lambda: io.validate_typed('ArtifactEntry', entry))

    def test_raw_byte_and_length_mismatch(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root = Path(directory)
            self.fixture(root)
            with (root / 'policy.json').open('ab') as stream:
                stream.write(b'\n')
            self.assertCode('hash_mismatch', lambda: io.load_input_bundle(root / 'input_manifest.json'))
            self.fixture_repair_policy(root)
            manifest = json.loads((root / 'input_manifest.json').read_bytes())
            manifest['files'][0]['byte_length'] += 1
            (root / 'input_manifest.json').write_bytes(encoded(manifest))
            self.assertCode('hash_mismatch', lambda: io.load_input_bundle(root / 'input_manifest.json'))

    def fixture_repair_policy(self, root):
        (root / 'policy.json').write_bytes(encoded(sample('Policy')))
        self.refresh(root)

    def test_contract_cannot_be_replaced_with_rehashed_bytes(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root = Path(directory)
            self.fixture(root)
            (root / 'implementation_contract.json').write_bytes(b'{}')
            self.refresh(root)
            self.assertCode('hash_mismatch', lambda: io.load_input_bundle(root / 'input_manifest.json'))

    def test_transitive_input_hash_and_evaluation_bindings(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root = Path(directory)
            self.fixture(root)
            (root / 'input_manifest.json').write_bytes((root / 'input_manifest.json').read_bytes() + b'\n')
            self.assertCode('hash_mismatch', lambda: io.load_evaluation_bundle(root / 'evaluation_manifest.json'))
            self.refresh(root)
            gold = json.loads((root / 'gold.json').read_bytes())
            gold['labels'][0]['at_ns'] += 1
            (root / 'gold.json').write_bytes(encoded(gold))
            self.refresh(root)
            self.assertCode('snapshot_binding', lambda: io.load_evaluation_bundle(root / 'evaluation_manifest.json'))

    def test_schedule_missing_or_orphan_snapshot(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root = Path(directory)
            self.fixture(root)
            candidates = {'contract_id': CID, 'snapshots': []}
            (root / 'candidates.json').write_bytes(encoded(candidates))
            self.refresh(root)
            self.assertCode('schedule_mismatch', lambda: io.load_input_bundle(root / 'input_manifest.json'))
            self.fixture(root)
            probes = {'contract_id': CID, 'probes': []}
            (root / 'probes.json').write_bytes(encoded(probes))
            self.refresh(root)
            self.assertCode('schedule_mismatch', lambda: io.load_input_bundle(root / 'input_manifest.json'))

    def test_input_type_validation_uses_fixed_argument_order(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root = Path(directory)
            self.fixture(root)
            policy = sample('Policy')
            policy['rank_offset'] = 'invalid'
            (root / 'policy.json').write_bytes(encoded(policy))
            probes = json.loads((root / 'probes.json').read_bytes())
            probes['probes'][0]['final_k'] = 'invalid'
            (root / 'probes.json').write_bytes(encoded(probes))
            self.refresh(root)
            self.assertCode('invalid_type', lambda: io.load_input_bundle(root / 'input_manifest.json'), '$.probes[*].final_k')

    def test_schedule_binding_mismatch(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root = Path(directory)
            self.fixture(root)
            probes = json.loads((root / 'probes.json').read_bytes())
            probes['probes'][0]['at_ns'] = 11
            (root / 'probes.json').write_bytes(encoded(probes))
            self.refresh(root)
            self.assertCode('snapshot_binding', lambda: io.load_input_bundle(root / 'input_manifest.json'))

    def test_missing_nonregular_and_symlink_files(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root = Path(directory)
            self.fixture(root)
            (root / 'policy.json').unlink()
            self.assertCode('io_error', lambda: io.load_input_bundle(root / 'input_manifest.json'))
            (root / 'policy.json').mkdir()
            self.assertCode('unsafe_path', lambda: io.load_input_bundle(root / 'input_manifest.json'))
            (root / 'policy.json').rmdir()
            (root / 'elsewhere').write_bytes(encoded(sample('Policy')))
            (root / 'policy.json').symlink_to('elsewhere')
            self.assertCode('unsafe_path', lambda: io.load_input_bundle(root / 'input_manifest.json'))
            alias = root / 'alias'
            alias.symlink_to(root, target_is_directory=True)
            self.assertCode('unsafe_path', lambda: io.load_input_bundle(alias / 'input_manifest.json'))

    def test_parent_traversal_is_rejected(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root = Path(directory)
            self.fixture(root)
            (root / 'sub').mkdir()
            self.assertCode('unsafe_path', lambda: io.load_input_bundle(root / 'sub' / '..' / 'input_manifest.json'))

    def test_nul_paths_are_contract_errors(self):
        self.assertCode('unsafe_path', lambda: io.load_input_bundle(Path('/tmp/invalid\x00path')))
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            self.assertCode('unsafe_path', lambda: io.write_json_new(Path(directory) / 'invalid\x00name', {}))

    def test_write_exclusive_sorted_utf8_lf(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            target = Path(directory) / 'result.json'
            io.write_json_new(target, {'z': '가', 'a': 1})
            expected = '{\n  "a": 1,\n  "z": "가"\n}\n'.encode('utf-8')
            self.assertEqual(target.read_bytes(), expected)
            self.assertCode('output_exists', lambda: io.write_json_new(target, {'a': 2}))
            self.assertEqual(target.read_bytes(), expected)

    def test_write_failure_leaves_no_final_or_temp(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            target = Path(directory) / 'result.json'
            for function in ('fsync', 'link'):
                with mock.patch.object(io.os, function, side_effect=OSError('injected')):
                    self.assertCode('io_error', lambda: io.write_json_new(target, {'a': 1}))
                self.assertEqual(list(Path(directory).iterdir()), [])
            self.assertCode('invalid_value', lambda: io.write_json_new(target, {'a': float('nan')}))
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_write_racing_existing_destination_is_preserved(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            target = Path(directory) / 'result.json'
            real_link = io.os.link
            def racing_link(source, destination, **kwargs):
                target.write_bytes(b'original')
                return real_link(source, destination, **kwargs)
            with mock.patch.object(io.os, 'link', side_effect=racing_link):
                self.assertCode('output_exists', lambda: io.write_json_new(target, {'a': 1}))
            self.assertEqual(target.read_bytes(), b'original')
            self.assertEqual([p.name for p in Path(directory).iterdir()], ['result.json'])

    def test_write_symlink_parent_rejected(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root = Path(directory)
            (root / 'real').mkdir()
            (root / 'alias').symlink_to('real', target_is_directory=True)
            self.assertCode('unsafe_path', lambda: io.write_json_new(root / 'alias' / 'result.json', {}))
            self.assertEqual(list((root / 'real').iterdir()), [])


if __name__ == '__main__':
    unittest.main()

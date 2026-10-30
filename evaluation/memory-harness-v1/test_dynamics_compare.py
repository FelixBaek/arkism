"""DYN05 compare tests: synthetic artifact bindings, never outcome/science proofs."""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).absolute().parent
spec = importlib.util.spec_from_file_location('_dyn05_compare_tests', HERE / 'dynamics_compare.py')
C = importlib.util.module_from_spec(spec)
spec.loader.exec_module(C)
REGISTRY_PATH = HERE.parents[1] / 'docs/research/memory-harness/policy_registry.json'


def raw(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(',', ':'), allow_nan=False).encode() + b'\n'


def sha(text):
    return hashlib.sha256(text.encode()).hexdigest()


def decision(entry):
    d = {'query_id': 'q', 'policy_id': entry['policy_id'], 'policy_version': entry['version'],
         'candidate_snapshot_sha256': sha('candidates'), 'prefix_sha256': sha('prefix_' + entry['policy_id']),
         'ranker_config_sha256': entry['ranker']['config_sha256'], 'ranks': [], 'selected': [],
         'context_cost_used': 0, 'context_budget': {'unit': 'fixture_units_v0', 'limit': 4},
         'status': 'ok', 'reason': 'none'}
    d['decision_sha256'] = C.semantic_sha('PolicyDecisionV2HashProjection', d)
    return d


def run_fixture(registry, index):
    entry = registry['entries'][index]
    cost = dict(event_inputs=0, unique_events=0, state_updates=0, duplicate_events=0, duplicate_evidence=0,
                feature_reads=0, failed_predictions=0, fallback_predictions=0, elapsed_wall_ns=None, provider_usd=None)
    r = {'protocol': 'dynamics-prediction-run-v2', 'run_id': 'job', 'policy_id': entry['policy_id'],
         'policy_version': entry['version'], 'input_manifest_sha256': sha('manifest'),
         'run_config_sha256': sha('config'), 'registry_sha256': registry['registry_sha256'],
         'predictions': [{'query_id': 'q', 'status': 'ok', 'decision': decision(entry), 'error': None, 'reason': 'none'}], 'cost': cost}
    r['run_sha256'] = C.semantic_sha('PredictionRunV2HashProjection', r)
    return r


def seal_run(r):
    if r['predictions'] and r['predictions'][0]['decision'] is not None:
        d = r['predictions'][0]['decision']
        d['decision_sha256'] = C.semantic_sha('PolicyDecisionV2HashProjection', d)
    r['run_sha256'] = C.semantic_sha('PredictionRunV2HashProjection', r)


class CompareTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.registry = json.loads(REGISTRY_PATH.read_bytes())
        self.registry_path = self.directory / 'registry.json'
        with self.registry_path.open('wb') as stream:
            stream.write(raw(self.registry))
        self.runs = [run_fixture(self.registry, 0), run_fixture(self.registry, 1)]
        self.paths = [self.directory / 'first.json', self.directory / 'second.json']
        self.save()

    def save(self):
        for path, run in zip(self.paths, self.runs):
            path.write_bytes(raw(run))

    def compare(self):
        self.save()
        return C.compare_runs(self.paths, self.registry_path)

    def error(self, code, call, *args):
        with self.assertRaises(C.ContractErrorV2) as caught:
            call(*args)
        self.assertEqual(code, caught.exception.code)
        self.assertTrue(C.known_path(caught.exception.path))

    def test_actual_registry_crosslanguage_literal(self):
        value = C.decode(REGISTRY_PATH.read_bytes(), 'PolicyRegistryV2')
        C._registry(value)
        self.assertEqual('17c58cf192536dc7f7fe7d3987cde09ea42265cb92982e3d811649def989b4f5', value['registry_sha256'])
        self.assertEqual(['B0', 'B1', 'B2', 'V3-P1-FD', 'V3-P1-IR', 'V3-P1-NOTIME', 'V3-P1-NOOUT'], [e['policy_id'] for e in value['entries']])

    def test_hand_literal_canonical(self):
        value = dict(workspace='w', project='p', user='u', agent='a')
        expected = b'o4{s5:agents1:as7:projects1:ps4:users1:us9:workspaces1:w}'
        self.assertEqual(expected, C.canonical('ScopeV2', value))
        self.assertEqual(hashlib.sha256(b'dynamics-contract-v2\0ScopeV2\0' + expected).hexdigest(), C.semantic_sha('ScopeV2', value))
        lane = b'o2{s4:kinds9:relevances6:weightd3ff0000000000000;}'
        self.assertEqual(lane, C.canonical('RankLane', {'kind': 'relevance', 'weight': 1}))

    def test_shared_RunID_and_derived_prefixes(self):
        result = self.compare()
        self.assertEqual(['job'], result['run_ids'])
        self.assertTrue(result['equal_conditions'])
        self.assertEqual(9, len(result['checks']))
        self.assertEqual(['B0', 'B1'], [s['policy_id'] for s in result['statuses']])

    def test_failed_only_unknown_denominators(self):
        for r in self.runs:
            r['predictions'] = [{'query_id': 'q', 'status': 'failed', 'decision': None,
                                 'error': {'code': 'missing_state', 'path': '$.states'}, 'reason': 'none'}]
            r['cost']['failed_predictions'] = 1
            seal_run(r)
        result = self.compare()
        self.assertFalse(result['equal_conditions'])
        by_name = {c['name']: c for c in result['checks']}
        for name in ('candidates', 'budget', 'source_snapshot'):
            self.assertEqual({'name': name, 'equal': False, 'reason': 'source_unknown'}, by_name[name])
        self.assertTrue(by_name['registry']['equal'])
        self.assertEqual([1, 1], [s['planned'] for s in result['statuses']])
        self.assertEqual([1, 1], [s['failed'] for s in result['statuses']])

    def test_missing_query_not_empty_success(self):
        self.runs[1]['predictions'] = []
        seal_run(self.runs[1])
        self.assertFalse(self.compare()['equal_conditions'])

    def test_commitment_mismatch_unknown(self):
        self.runs[1]['input_manifest_sha256'] = sha('other')
        seal_run(self.runs[1])
        result = self.compare()
        self.assertFalse(result['equal_conditions'])
        self.assertTrue(all(not c['equal'] for c in result['checks'] if c['name'] != 'registry'))

    def test_observed_candidate_budget_mismatch(self):
        for field, value in [('candidate_snapshot_sha256', sha('other')), ('context_budget', {'unit': 'fixture_units_v0', 'limit': 3})]:
            with self.subTest(field=field):
                r = copy.deepcopy(self.runs[1])
                self.runs[1]['predictions'][0]['decision'][field] = value
                seal_run(self.runs[1])
                self.assertFalse(self.compare()['equal_conditions'])
                self.runs[1] = r

    def test_duplicate_paths_hashes_and_objects(self):
        self.error('snapshot_conflict', C.compare_runs, [self.paths[0], self.paths[0]], self.registry_path)
        with self.paths[1].open('wb') as stream:
            stream.write(self.paths[0].read_bytes())
        self.error('snapshot_conflict', C.compare_runs, self.paths, self.registry_path)
        self.runs[1] = copy.deepcopy(self.runs[0])
        self.runs[1]['cost']['elapsed_wall_ns'] = 1
        seal_run(self.runs[1])
        self.error('snapshot_conflict', self.compare)

    def test_hash_tamper_and_wrong_association(self):
        self.runs[1]['run_sha256'] = sha('tampered')
        self.error('hash_mismatch', self.compare)
        seal_run(self.runs[1])
        self.runs[1]['predictions'][0]['decision']['ranker_config_sha256'] = self.registry['entries'][2]['ranker']['config_sha256']
        seal_run(self.runs[1])
        self.error('snapshot_binding', self.compare)

    def test_registry_association_and_ablation(self):
        self.registry['entries'][0]['ranker']['registry_sha256'] = sha('foreign')
        with self.registry_path.open('wb') as stream:
            stream.write(raw(self.registry))
        self.error('config_mismatch', C.compare_runs, self.paths, self.registry_path)

    def test_wrong_constant_returns_fixed_error(self):
        self.registry['entries'][0]['ranker']['outer_offset'] = 59
        with self.registry_path.open('wb') as stream:
            stream.write(raw(self.registry))
        self.error('invalid_value', C.compare_runs, self.paths, self.registry_path)

    def test_raw_JSON_boundaries(self):
        for data, code in [(b'{"x":1,"x":2}', 'duplicate_key'), (b'"\\ud800"', 'invalid_unicode'),
                           (b'\xff', 'invalid_unicode'), (b'[NaN]', 'invalid_json'), (b'[1,]', 'invalid_json')]:
            with self.subTest(data=data):
                self.error(code, C.parse, data)

    def test_native_integer_type_and_unknown_priority(self):
        r = copy.deepcopy(self.runs[0])
        r['cost']['event_inputs'] = 1.0
        self.error('invalid_type', C.decode, raw(r), 'PredictionRunV2')
        r['unknown'] = 'untrusted'
        del r['run_id']
        self.error('unknown_field', C.decode, raw(r), 'PredictionRunV2')

    def test_ordinary_bound_read_and_no_hidden_payload_IO(self):
        link = self.directory / 'link'
        link.symlink_to(self.paths[0])
        self.error('unsafe_path', C.read, link)
        allowed = {str(self.registry_path), *(str(p) for p in self.paths)}
        original = C.read
        visited = []
        def read_explicit(path):
            self.assertIn(str(path), allowed)
            visited.append(str(path))
            return original(path)
        with mock.patch.object(C, 'read', side_effect=read_explicit):
            C.compare_runs(self.paths, self.registry_path)
        self.assertEqual(3, len(visited))

    def test_inputs_remain_unchanged(self):
        before = [p.read_bytes() for p in [self.registry_path, *self.paths]]
        C.compare_runs(self.paths, self.registry_path)
        self.assertEqual(before, [p.read_bytes() for p in [self.registry_path, *self.paths]])

    def test_empty_successes_are_unknown(self):
        for run in self.runs:
            run['predictions'] = []
            seal_run(run)
        result = self.compare()
        self.assertFalse(result['equal_conditions'])
        self.assertEqual([0, 0], [s['planned'] for s in result['statuses']])
    def test_native_nineteen_error_identities_remain_failed(self):
        codes = ['invalid_json','invalid_unicode','duplicate_key','unknown_field','missing_field','invalid_type','invalid_value','unsupported_version','unsupported_type','hash_mismatch','snapshot_conflict','snapshot_binding','schedule_mismatch','invalid_state','invalid_time','invalid_half_life','unsafe_path','output_exists','io_error']
        for code in codes:
            with self.subTest(code=code):
                for run in self.runs:
                    run['predictions'] = [{'query_id':'q','status':'failed','decision':None,'error':{'code':code,'path':'$.scope'},'reason':'none'}]
                    run['cost']['failed_predictions'] = 1
                    seal_run(run)
                result = self.compare()
                self.assertFalse(result['equal_conditions'])
                self.assertEqual([1,1], [row['failed'] for row in result['statuses']])

    def test_mixed_job_ids_use_full_config_commitments(self):
        self.runs[1]['run_id'] = 'other_job'
        self.runs[1]['run_config_sha256'] = sha('different_full_config_with_other_runid')
        seal_run(self.runs[1])
        result = self.compare()
        self.assertEqual(['job','other_job'], result['run_ids'])
        self.assertFalse(result['equal_conditions'])


class CanonicalBoundaryTests(unittest.TestCase):
    def test_canonical_rejects_unknown_fields(self):
        value = dict(workspace='w', project='p', user='u', agent='a', unknown='forbidden')
        with self.assertRaises(C.ContractErrorV2) as caught:
            C.canonical('ScopeV2', value)
        self.assertEqual('unknown_field', caught.exception.code)


    def test_pinned_schema_matches_unchanged_contract_declarations(self):
        contract = json.loads(C.SCHEMA_PATH.read_bytes())
        types = {**contract['models'], **contract['closed_inline_records'], **contract['hash_projections']}
        self.assertEqual(C.MODELS, {name: types[name] for name in C.MODELS})
        self.assertEqual(C.ENUMS, {name: contract['enums'][name] for name in C.ENUMS})
        self.assertEqual(C.PRIMITIVES, {name: contract['primitives'][name] for name in C.PRIMITIVES})

    def test_pinned_schema_rejects_tampered_bytes(self):
        with mock.patch.object(C.IO, '_read_regular', return_value=b'{}'):
            with self.assertRaises(C.ContractErrorV2) as caught:
                C._schema()
        self.assertEqual(caught.exception.code, 'hash_mismatch')

    def test_pinned_schema_read_failure_is_fixed_error(self):
        with mock.patch.object(C.IO, '_read_regular', side_effect=OSError('private detail')):
            with self.assertRaises(C.ContractErrorV2) as caught:
                C._schema()
        self.assertEqual(caught.exception.code, 'io_error')
        self.assertNotIn('private detail', str(caught.exception))


if __name__ == '__main__':
    unittest.main()

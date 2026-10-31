"""Synthetic-fixture checks for the EXP-01 v2 final lock; no owner values are used here."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


def _load(alias, name):
    target = Path(__file__).with_name(name)
    cached = sys.modules.get(alias)
    if cached is not None:
        return cached
    spec = importlib.util.spec_from_file_location(alias, target)
    module = importlib.util.module_from_spec(spec)
    sys.modules[alias] = module
    spec.loader.exec_module(module)
    return module


L = _load('_arkism_exp01_protocol_lock_v2', 'protocol_lock_v2.py')
T = _load('_arkism_exp01_test_protocol', 'test_protocol.py')
G = L.G
KEYS = G.ENUMS['ResearchDecisionID']


def values(**overrides):
    # Synthetic decisions exercise types; they do not represent a research approval.
    decisions = {key: 'A specified synthetic decision for ' + key for key in KEYS}
    decisions.update(k=3, repetitions=2, candidate_count=5, delta_min='1/10',
                     REF_tie_tolerance='0.01', tuning_trials=0, tuning_compute_budget=0,
                     sample_size={'test_clusters': 8, 'validation_clusters': 4,
                                  'recompute_before_test_when_real_outcomes_exist': False},
                     token_budget={'max_output_tokens': 80},
                     total_budget={'usd_cap': '10', 'expected_usd': '3/2'},
                     strong_prior_comparators=['B0', 'B1', 'REF'],
                     REF_id_order=['B0', 'B1', 'V3-P1-FD', 'V3-P1-IR'])
    entries = {key: dict(value=decisions[key], provenance='owner_answered_20261031') for key in KEYS}
    entries.update(overrides)
    return dict(record='fixture', state='owner_decided_not_final_locked', values=entries)


def raw(value):
    return json.dumps(value, ensure_ascii=False, indent=1).encode('utf8')


def manifest_raw(label='labels_v2'):
    spec = T.fixture_spec()
    spec['label_version'] = label
    data, _ = G.expand_spec(T.raw_spec(spec))
    return data['split_manifest.json']


class LockV2(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.manifest = manifest_raw()
        cls.values = raw(values())
        cls.lock = L.lock_bytes(L.build_lock(cls.values, cls.manifest))

    def assert_error(self, code, call, path=None):
        with self.assertRaises(G.ExpError) as caught:
            call()
        self.assertEqual(caught.exception.code, code)
        if path is not None:
            self.assertEqual(caught.exception.path, path)

    def test_research_value_boundaries(self):
        cases = [('k', 0), ('k', True), ('repetitions', 0), ('candidate_count', 0),
                 ('delta_min', 'unresolved'), ('delta_min', '0'), ('delta_min', '1.01'),
                 ('delta_min', '1/0'), ('REF_tie_tolerance', '2'),
                 ('tuning_trials', True), ('tuning_compute_budget', '-1'),
                 ('sample_size', 0), ('sample_size', {}),
                 ('sample_size', {'test_clusters': 0, 'validation_clusters': 4}),
                 ('sample_size', {'test_clusters': 8, 'validation_clusters': 4,
                                  'recompute_before_test_when_real_outcomes_exist': True}),
                 ('token_budget', {'max_output_tokens': 0}),
                 ('total_budget', {'usd_cap': 10, 'expected_usd': 'recompute before run'}),
                 ('total_budget', {'usd_cap': 10, 'expected_usd': '11'}),
                 ('REF_id_order', ['B0', 'B1', 'B2', 'V3-P1-IR']),
                 ('REF_id_order', ['B0', 'B0', 'V3-P1-FD', 'V3-P1-IR']),
                 ('candidate_count', 4), ('strong_prior_comparators', ['B2']),
                 ('strong_prior_comparators', ['B0', 'B0'])]
        for key in KEYS:
            cases.extend((key, bad) for bad in ('', '  ', 'unresolved', 'pending', 'TBD', 'fixture_decision', None, [], {}, False))
        for key, value in cases:
            with self.subTest(key=key, value=value):
                bad = raw(values(**{key: dict(value=value, provenance='owner_answered_20261031')}))
                self.assert_error('research_unresolved', lambda: L.build_lock(bad, self.manifest), '$.values.' + key)

    def test_exact_numeric_strings_and_zero_tuning_are_valid(self):
        for number in ('0.1', '1/10', '1e-1'):
            doc = values(delta_min=dict(value=number, provenance='owner_answered_20261031'))
            self.assertEqual(L.build_lock(raw(doc), self.manifest)['research']['delta_min'], number)
        # Alternative descriptive decisions are allowed, including pending-case handling.
        alternative = values(failure_missingness_exclusion=dict(
            value='Exclude pending correction cases; preserve unknown grading failures',
            provenance='owner_answered_20261031'))
        L.build_lock(raw(alternative), self.manifest)
        # Closed boundary on tie tolerance, open boundary on minimum effect.
        for number in (0, '0', '1'):
            doc = values(REF_tie_tolerance=dict(value=number, provenance='owner_answered_20261031'))
            L.build_lock(raw(doc), self.manifest)

    def test_lock_binds_values_contracts_dataset_and_verifies(self):
        record = json.loads(self.lock)
        self.assertEqual(record['state'], 'final_locked')
        self.assertEqual(record['values_sha256'], G.sha(self.values))
        self.assertEqual(record['baseline_contract_sha256'], '04463d838f397772743c5740a346230eb8e9db60db8dddc824d52e1f083790ef')
        self.assertEqual(record['manifest_sha256'], G.sha(self.manifest))
        self.assertEqual(record['label_version'], 'labels_v2')
        self.assertEqual(set(record['provenance'].values()), {'owner_answered_20261031'})
        self.assertEqual(L.lock_bytes(L.build_lock(self.values, self.manifest)), self.lock)
        self.assertEqual(L.verify_lock(self.lock, self.values, self.manifest)['status'], 'valid_final_locked')

    def test_every_research_key_must_be_present_and_non_null(self):
        for key in KEYS:
            entries = values()['values']
            del entries[key]
            self.assert_error('research_unresolved', lambda: L.build_lock(raw(dict(values=entries)), self.manifest), '$.values.' + key)
        self.assert_error('research_unresolved', lambda: L.build_lock(raw(values(k=dict(value=None, provenance='owner_answered_20261031'))), self.manifest))
        self.assert_error('unknown_field', lambda: L.build_lock(raw(values(extra=dict(value=1, provenance='owner_answered_20261031'))), self.manifest))
        self.assert_error('missing_field', lambda: L.build_lock(b'{"record":"x"}', self.manifest))

    def test_non_owner_provenance_is_rejected(self):
        for provenance in ('proposal_r2_default_not_asked', 'owner_answered', '', None, 'OWNER_answered_20261031'):
            bad = values(REF_score=dict(value='x', provenance=provenance))
            self.assert_error('provenance_not_owner_decided', lambda: L.build_lock(raw(bad), self.manifest), '$.values.REF_score')
        self.assert_error('research_unresolved', lambda: L.build_lock(raw(values(k=dict(value=5))), self.manifest))

    def test_dataset_must_be_labels_v2_canonical_manifest(self):
        self.assert_error('identity_conflict', lambda: L.build_lock(self.values, manifest_raw('labels_v1')), '$.label_version')
        self.assert_error('invalid_value', lambda: L.build_lock(self.values, self.manifest.rstrip(b'\n')))

    def test_changed_inputs_or_lock_are_rejected(self):
        changed_values = raw(values(k=dict(value=6, provenance='owner_answered_20261031')))
        self.assert_error('hash_mismatch', lambda: L.verify_lock(self.lock, changed_values, self.manifest), '$.values_sha256')
        self.assert_error('hash_mismatch', lambda: L.verify_lock(self.lock, self.values + b' ', self.manifest), '$.values_sha256')
        record = json.loads(self.lock)
        record['state'] = 'draft'
        record['lock_sha256'] = G.sha(G.compact({k: v for k, v in record.items() if k != 'lock_sha256'}))
        self.assert_error('identity_conflict', lambda: L.verify_lock(L.lock_bytes(record), self.values, self.manifest), '$.state')
        record = json.loads(self.lock)
        record['note'] = 'x'
        self.assert_error('invalid_value', lambda: L.verify_lock(L.lock_bytes(record), self.values, self.manifest))
        self.assert_error('invalid_value', lambda: L.verify_lock(self.lock + b'\n', self.values, self.manifest))

    def test_no_state_change_after_final_locked(self):
        for state in ('draft', 'unlocked', ''):
            self.assert_error('final_lock_immutable', lambda: L.verify_lock(self.lock, self.values, self.manifest, state), '$.state')
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / 'lock.json'
            L.write_lock(json.loads(self.lock), out)
            self.assertEqual(out.read_bytes(), self.lock)
            self.assert_error('output_exists', lambda: L.write_lock(L.build_lock(self.values, self.manifest), out))
            self.assertEqual(out.read_bytes(), self.lock)
        self.assert_error('unsafe_path', lambda: L.write_lock(json.loads(self.lock), 'relative.json'))

    def test_mutation_every_single_byte_of_lock_or_values_is_rejected(self):
        for position in range(len(self.lock)):
            mutated = bytearray(self.lock)
            mutated[position] ^= 0x01
            with self.assertRaises(G.ExpError, msg=position):
                L.verify_lock(bytes(mutated), self.values, self.manifest)
        for position in range(0, len(self.values), 7):
            mutated = bytearray(self.values)
            mutated[position] ^= 0x01
            with self.assertRaises(G.ExpError, msg=position):
                L.verify_lock(self.lock, bytes(mutated), self.manifest)

    def test_rehashed_invalid_research_cannot_verify(self):
        doc = values(k=dict(value=0, provenance='owner_answered_20261031'))
        invalid_values = raw(doc)
        record = json.loads(self.lock)
        record['research']['k'] = 0
        record['values_sha256'] = G.sha(invalid_values)
        record['lock_sha256'] = G.sha(G.compact({k: v for k, v in record.items() if k != 'lock_sha256'}))
        self.assert_error('research_unresolved', lambda: L.verify_lock(
            L.lock_bytes(record), invalid_values, self.manifest), '$.values.k')

    def test_cli_invalid_values_never_create_output(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            bad = raw(values(k=dict(value=0, provenance='owner_answered_20261031')))
            (tmp / 'values.json').write_bytes(bad)
            (tmp / 'manifest.json').write_bytes(self.manifest)
            result = subprocess.run([sys.executable, '-B', str(Path(L.__file__)), 'lock',
                '--values', str(tmp / 'values.json'), '--manifest', str(tmp / 'manifest.json'),
                '--out', str(tmp / 'lock.json')], capture_output=True, text=True)
            self.assertEqual((result.returncode, json.loads(result.stdout)['code']), (2, 'research_unresolved'))
            self.assertFalse((tmp / 'lock.json').exists())

    def test_cli_lock_then_verify(self):
        script = str(Path(L.__file__))
        with tempfile.TemporaryDirectory() as tmp:
            tmp = Path(tmp)
            (tmp / 'values.json').write_bytes(self.values)
            (tmp / 'split_manifest.json').write_bytes(self.manifest)
            common = ['--values', str(tmp / 'values.json'), '--manifest', str(tmp / 'split_manifest.json')]
            run = lambda *args: subprocess.run([sys.executable, '-B', script, *args], capture_output=True, text=True)
            first = run('lock', *common, '--out', str(tmp / 'lock.json'))
            self.assertEqual(first.returncode, 0, first.stdout + first.stderr)
            again = run('lock', *common, '--out', str(tmp / 'lock.json'))
            self.assertEqual(json.loads(again.stdout)['code'], 'output_exists')
            ok = run('verify', '--lock', str(tmp / 'lock.json'), *common, '--request-state', 'final_locked')
            self.assertEqual(json.loads(ok.stdout)['status'], 'valid_final_locked', ok.stdout + ok.stderr)
            revert = run('verify', '--lock', str(tmp / 'lock.json'), *common, '--request-state', 'draft')
            self.assertEqual((revert.returncode, json.loads(revert.stdout)['code']), (2, 'final_lock_immutable'))


if __name__ == '__main__':
    unittest.main()

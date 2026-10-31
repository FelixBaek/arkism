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
    entries = {key: dict(value='fixture_' + key, provenance='owner_answered_20261031') for key in KEYS}
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

"""R3: deterministic generation, explicit labels, and failure-safe publication."""
import contextlib
import copy
import io as streams
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

# Discovery and command execution both place this directory on the import path.
import contract_io as contract_io
import fixture_gen as gen

CONTRACT = Path(__file__).parent / 'contracts' / 'implementation_contract.json'
SPEC = Path(__file__).with_name('fixtures_spec.json')
ROOT = Path(__file__).resolve().parents[2]
EXPECTED_FILES = {'implementation_contract.json', 'policy.json', 'probes.json',
                  'candidates.json', 'input_manifest.json', 'gold.json',
                  'split_map.json', 'evaluation_manifest.json'}


class FixtureTests(unittest.TestCase):
    def assertCode(self, code, callback):
        with self.assertRaises(contract_io.ContractError) as caught:
            callback()
        self.assertEqual(caught.exception.code, code)
        return caught.exception

    def generate(self, parent, name='fixture', seed=17):
        target = parent / name
        result = gen.generate_fixture(seed, target, CONTRACT)
        self.assertEqual(set(result), {'output_dir', 'input_manifest_sha256', 'evaluation_manifest_sha256'})
        self.assertEqual(result['output_dir'], str(target.absolute()))
        return target, result

    def rewrite(self, path, value):
        path.write_text(json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + '\n')

    def rehash(self, root):
        for name in ('input_manifest.json', 'evaluation_manifest.json'):
            manifest = json.loads((root / name).read_bytes())
            for entry in manifest['files']:
                data = (root / entry['logical_path']).read_bytes()
                entry['sha256'] = contract_io.sha256_bytes(data)
                entry['byte_length'] = len(data)
            self.rewrite(root / name, manifest)

    def test_exact_eight_files_and_raw_contract(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root, result = self.generate(Path(directory))
            self.assertEqual({p.name for p in root.iterdir()}, EXPECTED_FILES)
            self.assertEqual((root / 'implementation_contract.json').read_bytes(), CONTRACT.read_bytes())
            self.assertEqual(contract_io.sha256_bytes((root / 'implementation_contract.json').read_bytes()), contract_io.CONTRACT_SHA256)
            bundle = contract_io.load_evaluation_bundle(root / 'evaluation_manifest.json')
            self.assertEqual(bundle['manifest_sha256'], result['evaluation_manifest_sha256'])
            self.assertEqual(bundle['input_bundle']['manifest_sha256'], result['input_manifest_sha256'])
            self.assertEqual(bundle['input_bundle']['manifest']['dataset_role'], 'unit_fixture')
            self.assertEqual(bundle['input_bundle']['manifest']['source_commit'], '27bb41807ff48d47559ba2cfc30feb300caacecf')

    def test_seven_json_objects_validate(self):
        names = {'policy.json': 'Policy', 'probes.json': 'ProbeFile', 'candidates.json': 'CandidateFile',
                 'input_manifest.json': 'InputManifest', 'gold.json': 'GoldFile',
                 'split_map.json': 'SplitMap', 'evaluation_manifest.json': 'EvaluationManifest'}
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root, _ = self.generate(Path(directory))
            for name, kind in names.items():
                raw = (root / name).read_bytes()
                self.assertTrue(raw.endswith(b'\n'))
                value = contract_io.decode_typed(raw, kind)
                self.assertEqual(raw, (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + '\n').encode('utf-8'))

    def test_repeated_seed_is_byte_identical_for_all_outputs(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            a, _ = self.generate(parent, 'a')
            b, _ = self.generate(parent, 'b')
            for name in EXPECTED_FILES:
                self.assertEqual((a / name).read_bytes(), (b / name).read_bytes(), name)

    def test_seed_order_independent_golden(self):
        # GV-FILE-01; original golden_vectors SHA:
        # 3ab88b141d399269478775bb2dd068a762d9444a43a46123a17dbe95c4c427c1
        self.assertEqual(gen._ordered_ids(42, ['a', 'b', 'c']), ['a', 'c', 'b'])
        expected = {'a': '7127985993f10218bc374dc6fbd0984e10b0371c3fe4c3f2527693fd1756e4b3',
                    'b': 'c521a07b7af72e1e75196baa5e3abc110398b968201a11eb35557698d178f9a5',
                    'c': '9c492aa45155e418c37b06c606cb64f8e69efa8dc7da1dea8e5ae9454bac5087'}
        for symbol, digest in expected.items():
            self.assertEqual(contract_io.sha256_bytes(b'fixture-v0\n42\n' + symbol.encode('ascii')), digest)

    def test_seed_boundaries_and_different_seed(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            a, _ = self.generate(parent, 'a', 17)
            b, _ = self.generate(parent, 'b', 42)
            self.assertNotEqual((a / 'input_manifest.json').read_bytes(), (b / 'input_manifest.json').read_bytes())
            for seed in (0, 18446744073709551615):
                self.generate(parent, 'boundary-' + str(seed), seed)
            for seed in (True, 17.0, '17', None):
                self.assertCode('invalid_type', lambda: gen.generate_fixture(seed, parent / 'bad', CONTRACT))
            for seed in (-1, 18446744073709551616):
                self.assertCode('invalid_value', lambda: gen.generate_fixture(seed, parent / 'bad', CONTRACT))

    def test_schedule_gold_snapshot_full_binding(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root, _ = self.generate(Path(directory))
            bundle = contract_io.load_evaluation_bundle(root / 'evaluation_manifest.json')
            probes = bundle['input_bundle']['probes']['probes']
            snapshots = {row['snapshot_id']: row for row in bundle['input_bundle']['candidates']['snapshots']}
            labels = {row['query_id']: row for row in bundle['gold']['labels']}
            self.assertEqual(len(probes), 7)
            self.assertEqual(len(labels), 7)
            self.assertEqual(len(snapshots), 7)
            for probe in probes:
                snapshot = snapshots[probe['candidate_snapshot_id']]
                gold = labels[probe['query_id']]
                for field in ('stream_id', 'scope', 'at_ns', 'cutoff_accepted_seq'):
                    self.assertEqual(gold[field], probe[field])
                self.assertEqual(snapshot['as_of_ns'], probe['at_ns'])
                self.assertEqual(snapshot['as_of_seq'], probe['cutoff_accepted_seq'])
            self.assertEqual({row['label_status'] for row in labels.values()}, {'relevant', 'clear_negative', 'ambiguous'})

    def test_gold_and_oracles_do_not_enter_ranker_inputs(self):
        forbidden = {'gold', 'labels', 'label_status', 'relevant_refs', 'split', 'split_role',
                     'category', 'expected', 'rank', 'score_fractions', 'selected', 'oracle_id'}
        def keys(value):
            if isinstance(value, dict):
                yield from value.keys()
                for child in value.values():
                    yield from keys(child)
            elif isinstance(value, list):
                for child in value:
                    yield from keys(child)
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root, _ = self.generate(Path(directory))
            manifest = json.loads((root / 'input_manifest.json').read_bytes())
            self.assertEqual({r['logical_path'] for r in manifest['files']}, {'implementation_contract.json', 'policy.json', 'probes.json', 'candidates.json'})
            for name in ('policy.json', 'probes.json', 'candidates.json'):
                self.assertFalse(forbidden & set(keys(json.loads((root / name).read_bytes()))), name)
            # The frozen contract describes Gold but provides no runtime answers.
            original = contract_io.load_input_bundle(root / 'input_manifest.json')
            (root / 'gold.json').unlink()
            (root / 'split_map.json').unlink()
            self.assertEqual(contract_io.load_input_bundle(root / 'input_manifest.json'), original)

    def test_generation_does_not_read_expected_rank_or_scores(self):
        spec = json.loads(SPEC.read_bytes())
        baseline = gen._objects(17, spec)
        altered = copy.deepcopy(spec)
        for case in altered['cases']:
            case['expected'] = {'secret': 'must-not-be-read'}
            case['oracle_id'] = 'changed-development-label'
        self.assertEqual(gen._objects(17, altered), baseline)

    def test_fixed_independent_fixture_expectations(self):
        # These values are transcribed from hand oracles, not computed by Python RRF.
        cases = {case['query_id']: case for case in json.loads(SPEC.read_bytes())['cases']}
        self.assertEqual(cases['q01']['expected']['rank'], ['memory:a', 'memory:c', 'memory:b'])
        self.assertEqual(cases['q01']['expected']['score_fractions'], ['2/61', '1/62', '1/63'])
        self.assertEqual(cases['q02']['expected']['selected'], ['fixture_alt:a', 'memory:a'])
        self.assertEqual(cases['q03']['expected']['score_fractions'], ['1/62'])
        self.assertEqual(cases['q04']['expected']['selected'], ['memory:b', 'memory:c'])
        self.assertEqual(cases['q04']['expected']['used_context_units'], 3)
        self.assertEqual(cases['q05']['expected']['selected'], [])
        self.assertEqual(cases['q06']['gold']['label_status'], 'ambiguous')
        self.assertEqual(cases['q07']['gold']['label_status'], 'clear_negative')
        self.assertEqual(cases['q07']['expected']['selected'], ['memory:a'])

    def test_snapshot_semantic_negative_cases_bypass_hash_masking(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root, _ = self.generate(Path(directory))
            original = json.loads((root / 'candidates.json').read_bytes())
            for mutation in ('scope', 'revision', 'hash', 'future_seq', 'future_time', 'flag'):
                candidates = copy.deepcopy(original)
                snapshot = next(s for s in candidates['snapshots'] if s['snapshot_id'] == 'snap-q01')
                hit = snapshot['lanes'][0][0]
                if mutation == 'scope': hit['scope']['user'] = 'other'
                elif mutation == 'revision': hit['ref']['revision'] = 2
                elif mutation == 'hash': hit['ref']['content_sha256'] = 'f' * 64
                elif mutation == 'future_seq': hit['observed_seq'] = 4
                elif mutation == 'future_time': hit['effective_at_ns'] = 101
                elif mutation == 'flag': hit['approved'] = False
                with self.subTest(mutation=mutation):
                    self.assertCode('snapshot_conflict', lambda: contract_io.validate_typed('CandidateFile', candidates))

    def test_binding_negative_rehashes_isolated_manifests(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root, _ = self.generate(Path(directory))
            probes = json.loads((root / 'probes.json').read_bytes())
            probes['probes'][0]['at_ns'] += 1
            self.rewrite(root / 'probes.json', probes)
            self.rehash(root)
            self.assertCode('snapshot_binding', lambda: contract_io.load_input_bundle(root / 'input_manifest.json'))

    def test_gold_negative_rehashes_evaluation_manifest(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root, _ = self.generate(Path(directory))
            gold = json.loads((root / 'gold.json').read_bytes())
            gold['labels'][0]['cutoff_accepted_seq'] += 1
            self.rewrite(root / 'gold.json', gold)
            self.rehash(root)
            self.assertCode('snapshot_binding', lambda: contract_io.load_evaluation_bundle(root / 'evaluation_manifest.json'))

    def test_raw_lf_corruption_keeps_original_hashes(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            root, _ = self.generate(Path(directory))
            with (root / 'candidates.json').open('ab') as stream:
                stream.write(b'\n')
            self.assertCode('hash_mismatch', lambda: contract_io.load_input_bundle(root / 'input_manifest.json'))

    def test_existing_empty_nonempty_and_symlink_outputs_preserved(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            empty = parent / 'empty'
            empty.mkdir()
            self.assertCode('output_exists', lambda: gen.generate_fixture(17, empty, CONTRACT))
            self.assertEqual(list(empty.iterdir()), [])
            (empty / 'original').write_bytes(b'original')
            self.assertCode('output_exists', lambda: gen.generate_fixture(17, empty, CONTRACT))
            self.assertEqual((empty / 'original').read_bytes(), b'original')
            link = parent / 'link'
            link.symlink_to(empty, target_is_directory=True)
            self.assertCode('output_exists', lambda: gen.generate_fixture(17, link, CONTRACT))
            self.assertEqual({p.name for p in parent.iterdir()}, {'empty', 'link'})

    def test_parent_and_contract_paths_cannot_follow_symlinks(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            (parent / 'real').mkdir()
            (parent / 'alias').symlink_to('real', target_is_directory=True)
            self.assertCode('unsafe_path', lambda: gen.generate_fixture(17, parent / 'alias' / 'out', CONTRACT))
            link = parent / 'contract-link'
            link.symlink_to(CONTRACT.resolve())
            self.assertCode('unsafe_path', lambda: gen.generate_fixture(17, parent / 'out', link))
            bad = parent / 'wrong-contract'
            bad.write_bytes(b'{}')
            self.assertCode('hash_mismatch', lambda: gen.generate_fixture(17, parent / 'out', bad))
            self.assertFalse((parent / 'out').exists())
            self.assertFalse(any(p.name.startswith('.arkism-harness-') for p in parent.iterdir()))

    def test_staging_validates_before_destination_reservation(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            out = parent / 'out'
            original = contract_io.load_evaluation_bundle
            observed = []
            def loader(path):
                observed.append((Path(path).parent.name, out.exists()))
                return original(path)
            with mock.patch.object(contract_io, 'load_evaluation_bundle', side_effect=loader):
                self.generate(parent, 'out')
            self.assertTrue(observed[0][0].startswith('.arkism-harness-fixture-'))
            self.assertFalse(observed[0][1])
            self.assertTrue(observed[-1][1])

    def test_completed_data_published_before_manifests(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            calls = []
            original = gen._publish_file
            def publish(source, destination, name):
                calls.append(name)
                if name == 'input_manifest.json':
                    self.assertTrue(set(gen._FILES[:6]).issubset(set(os.listdir(destination))))
                if name == 'evaluation_manifest.json':
                    self.assertIn('input_manifest.json', os.listdir(destination))
                return original(source, destination, name)
            with mock.patch.object(gen, '_publish_file', side_effect=publish):
                self.generate(Path(directory))
            self.assertEqual(calls[-2:], ['input_manifest.json', 'evaluation_manifest.json'])

    def test_each_publication_failure_cleans_only_owned_outputs(self):
        for failure_index in range(8):
            with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
                parent = Path(directory)
                original = gen._publish_file
                count = 0
                def publish(source, destination, name):
                    nonlocal count
                    index = count
                    count += 1
                    if index == failure_index:
                        raise OSError('injected publication failure')
                    return original(source, destination, name)
                with mock.patch.object(gen, '_publish_file', side_effect=publish):
                    self.assertCode('io_error', lambda: gen.generate_fixture(17, parent / 'out', CONTRACT))
                self.assertEqual(list(parent.iterdir()), [], failure_index)

    def test_directory_open_failure_cleans_reserved_names(self):
        import errno
        for which in ('staging', 'output'):
            with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
                parent = Path(directory)
                original = gen.os.open
                failed = False
                def opened(name, flags, *args, **kwargs):
                    nonlocal failed
                    target = (name == 'out' if which == 'output' else str(name).startswith('.arkism-harness-fixture-'))
                    if target and flags & os.O_DIRECTORY and not failed:
                        failed = True
                        raise OSError(errno.EMFILE, 'injected descriptor exhaustion')
                    return original(name, flags, *args, **kwargs)
                with mock.patch.object(gen.os, 'open', side_effect=opened):
                    self.assertCode('io_error', lambda: gen.generate_fixture(17, parent / 'out', CONTRACT))
                self.assertTrue(failed)
                self.assertEqual(list(parent.iterdir()), [], which)

    def test_staging_cleanup_failure_does_not_leave_failed_final_output(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            original = gen._cleanup
            def cleanup(directory, name, descriptor, identity, owned):
                if name.startswith('.arkism-harness-fixture-'):
                    return False
                return original(directory, name, descriptor, identity, owned)
            with mock.patch.object(gen, '_cleanup', side_effect=cleanup):
                error = self.assertCode('io_error', lambda: gen.generate_fixture(17, parent / 'out', CONTRACT))
            self.assertFalse((parent / 'out').exists())
            self.assertIn('temporary artifacts', error.detail)
            self.assertEqual(len(list(parent.iterdir())), 1)
            self.assertTrue(next(parent.iterdir()).name.startswith('.arkism-harness-fixture-'))

    def test_competing_directory_after_staging_is_preserved(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            original = gen.os.mkdir
            def mkdir(name, *args, **kwargs):
                if name == 'out':
                    original(name, *args, **kwargs)
                    (parent / 'out' / 'competitor').write_bytes(b'preserve')
                return original(name, *args, **kwargs)
            with mock.patch.object(gen.os, 'mkdir', side_effect=mkdir):
                self.assertCode('output_exists', lambda: gen.generate_fixture(17, parent / 'out', CONTRACT))
            self.assertEqual((parent / 'out' / 'competitor').read_bytes(), b'preserve')
            self.assertEqual([p.name for p in parent.iterdir()], ['out'])

    def test_replaced_output_directory_is_not_deleted_on_failure(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            parent = Path(directory)
            def publish(source, destination, name):
                (parent / 'out').rename(parent / 'moved-owned')
                (parent / 'out').mkdir()
                (parent / 'out' / 'foreign').write_bytes(b'preserve')
                raise OSError('injected replacement')
            with mock.patch.object(gen, '_publish_file', side_effect=publish):
                error = self.assertCode('io_error', lambda: gen.generate_fixture(17, parent / 'out', CONTRACT))
            self.assertEqual((parent / 'out' / 'foreign').read_bytes(), b'preserve')
            self.assertIn('cleanup incomplete', error.detail)
            self.assertTrue((parent / 'moved-owned').is_dir())

    def test_cli_success_help_errors_and_no_raw_argument_leak(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            out = Path(directory) / 'fixture'
            args = ['--seed', '17', '--contract', str(CONTRACT), '--out', str(out)]
            stdout, stderr = streams.StringIO(), streams.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                self.assertEqual(gen.main(args), 0)
            self.assertEqual(json.loads(stdout.getvalue()), {'output_dir': str(out.absolute()), 'files': 8})
            self.assertEqual(stderr.getvalue(), '')
            for invalid in ([], args + ['--seed', '42'], args + ['--secret-token'], args + ['positional-secret'],
                            ['--seed', '1e0', '--contract', str(CONTRACT), '--out', str(out)]):
                stdout, stderr = streams.StringIO(), streams.StringIO()
                with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                    self.assertEqual(gen.main(invalid), 2)
                self.assertNotIn('secret', stderr.getvalue())
                self.assertEqual(set(json.loads(stderr.getvalue())), {'code', 'path', 'detail'})
            with contextlib.redirect_stdout(streams.StringIO()), contextlib.redirect_stderr(streams.StringIO()):
                self.assertEqual(gen.main(['--help']), 0)

    def test_existing_recall_dataset_and_freeze_unchanged(self):
        paths = [ROOT / 'evaluation/recall-quality-v1' / name for name in ('README.md', 'dataset.json', 'freeze.json')]
        before = {path: path.read_bytes() for path in paths}
        with tempfile.TemporaryDirectory(prefix='arkism-harness-') as directory:
            self.generate(Path(directory))
        for path, data in before.items():
            self.assertEqual(path.read_bytes(), data)


if __name__ == '__main__':
    unittest.main()

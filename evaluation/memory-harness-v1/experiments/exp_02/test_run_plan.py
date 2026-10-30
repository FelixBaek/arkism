"""Offline EXP02 contract tests; no native implementation or acceptance fabricated."""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).absolute().parent
spec = importlib.util.spec_from_file_location('_arkism_exp02_tests_source', HERE / 'build_run_plan.py')
B = importlib.util.module_from_spec(spec)
spec.loader.exec_module(B)
ROOT = HERE.parents[3]
REAL_ACCEPTANCE_SHA = B.ACCEPTANCE_SHA
REAL_DECISION_SHA = B.DECISION_SHA


def artifact(role='source'):
    return {'role': role, 'relative_path': 'fixed.json', 'raw_sha256': '0' * 64, 'size_bytes': 0}


def cost():
    return {'measurement_id': 'm', 'phase': 'tuning_fit', 'resource': 'cpu_time',
            'allocation': 'policy_incremental', 'policy_id': 'B2', 'value_decimal': '0.125',
            'unit': 'nanosecond', 'basis': 'observed', 'missing_reason': None,
            'source_artifact': artifact('cost_source'), 'price_artifact': None}


def policy_row(name='B0'):
    return {'policy_id': name, 'policy_version': 'v1', 'prediction_file': artifact('prediction'),
            'planned_rows': 5, 'recorded_rows': 5, 'successful_rows': 4, 'failed_rows': 1,
            'missing_rows': 0, 'fallback_rows': 0}


class ProtocolTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.matrix = json.loads((HERE / 'policy_matrix.json').read_bytes())
        self.controls = json.loads((HERE / 'controls.json').read_bytes())
        self.mp, self.cp = self.directory / 'matrix.json', self.directory / 'controls.json'
        self.evidence = self.directory / 'evidence'
        self.evidence.mkdir()
        # Explicit synthetic trust-fixture boundary: this is not real acceptance.
        raw = b'{"synthetic_unit_fixture":true}\n'
        synthetic_sha = hashlib.sha256(raw).hexdigest()
        for attribute in ('DECISION_SHA', 'ACCEPTANCE_SHA'):
            patch = mock.patch.object(B, attribute, synthetic_sha)
            patch.start()
            self.addCleanup(patch.stop)
        for field, name in (('REF_stage_decision', 'EXP-02-root-REF-stage-decision-001.json'),
                            ('acceptance', 'EXP-01-draft-local-acceptance.json')):
            (self.evidence / name).write_bytes(raw)
            ref = self.matrix['REF_stage_decision'] if field == 'REF_stage_decision' else self.matrix['exp01_draft_slot']['owner_acceptance']
            ref.update(raw_sha256=synthetic_sha, size_bytes=len(raw))
        self.save()

    def save(self):
        self.mp.write_bytes(B.compact(self.matrix))
        self.cp.write_bytes(B.compact(self.controls))

    def check_error(self, code, call, *args):
        with self.assertRaises(B.Exp02Error) as caught:
            call(*args)
        self.assertEqual(code, caught.exception.code)
        self.assertTrue(B.known_path(caught.exception.path), caught.exception.path)
        return caught.exception

    def validate(self):
        self.save()
        return B.validate_spec(self.mp, self.cp, ROOT, self.evidence)

    def test_actual_spec_pending(self):
        result = self.validate()
        self.assertEqual('spec_valid_pending_owner_bindings', result['status'])
        self.assertIsNone(result['plan'])
        self.assertEqual(0, result['processes_executed'])
        self.assertEqual(['REF_contract', 'operating_plan', 'DYN05_p1', 'required_seven_adapters',
                          'native_materialization', 'native_validation', 'gold_review', 'trusted_owner_review'], result['missing_gates'])

    def test_exact_pinned_EXP01_scope(self):
        self.assertIs(B.G, B.V.G)
        self.assertEqual('75fd80ea717a50ea2384d084ae077deda77ca75a915d28d009c96dae2e5f228e', hashlib.sha256(Path(B.G.__file__).read_bytes()).hexdigest())
        template = json.loads((HERE.with_name('exp_01') / 'protocol.template.json').read_bytes())
        self.assertEqual(['B0', 'B1', 'V3-P1-FD', 'V3-P1-IR'], template['ref_contract']['eligible_base'])
        self.assertEqual(['B2', 'V3-P1-NOTIME', 'V3-P1-NOOUT'], template['ref_contract']['excluded'])
        self.assertFalse(template['ref_contract']['execution_allowed'])
        self.assertTrue(all(v is None for v in template['research'].values()))
        raw = json.dumps(template['ref_contract'], ensure_ascii=False, sort_keys=True, separators=(',', ':')).encode()
        expected = hashlib.sha256(b'exp01-draft-v1\0REFContract\0' + raw).hexdigest()
        self.assertEqual('435819cc8d6ad7d769703a23c7a1f75130793db9b0b16962c33ea4ddadf098af', expected)

    def test_own_closed_fields(self):
        for field, value, code in [('unknown', True, 'unknown_field'), ('protocol', 'wrong', 'invalid_value')]:
            with self.subTest(field=field):
                row = copy.deepcopy(self.matrix)
                row[field] = value
                self.check_error(code, B.validate_typed, 'PolicyMatrix', row)
        row = copy.deepcopy(self.matrix)
        del row['scope']
        row['unknown'] = 0
        self.assertEqual('$.scope', self.check_error('missing_field', B.validate_typed, 'PolicyMatrix', row).path)

    def test_required_policy_order(self):
        for change in ('reverse', 'duplicate', 'omit'):
            with self.subTest(change=change):
                row = copy.deepcopy(self.matrix)
                if change == 'reverse':
                    row['policies'].reverse()
                elif change == 'duplicate':
                    row['policies'][1] = row['policies'][0]
                else:
                    row['policies'].pop()
                self.check_error('invalid_value', B._matrix, row)

    def test_policy_role_and_native_identity(self):
        for field, value in [('role', 'comparator'), ('contract_policy_key', 'B0')]:
            with self.subTest(field=field):
                row = copy.deepcopy(self.matrix)
                row['policies'][2][field] = value
                self.check_error('invalid_value', B._matrix, row)

    def test_no_fallback_or_unsupported_wire(self):
        for value in ('recorded_b0', 'B3'):
            with self.subTest(value=value):
                row = copy.deepcopy(self.matrix)
                row['policies'][0]['fallback' if value == 'recorded_b0' else 'policy_id'] = value
                self.check_error('invalid_value', B.validate_typed, 'PolicyMatrix', row)

    def test_unassigned_acceptance_not_authority(self):
        self.matrix['policies'][0]['implementation_acceptance'] = artifact('acceptance')
        self.check_error('external_contract_pending', self.validate)

    def test_control_exact_matrix(self):
        for index in (0, 1, 2, 4, 7, 8, 9):
            with self.subTest(index=index):
                row = copy.deepcopy(self.controls)
                row['controls'][index]['rule'] = 'disable_decay_only' if index else 'disable_outcome_jumps_only'
                self.check_error('invalid_value', B._controls, row)

    def test_regimes_not_collapsed(self):
        for field in ('comparison_families', 'signal_regimes'):
            with self.subTest(field=field):
                row = copy.deepcopy(self.controls)
                row[field].pop()
                self.check_error('invalid_value', B._controls, row)

    def test_private_decision_explicit_root_only(self):
        empty = self.directory / 'empty'
        empty.mkdir()
        self.check_error('io_error', B.validate_spec, self.mp, self.cp, ROOT, empty)
        self.check_error('unsafe_path', B.validate_spec, self.mp, self.cp, ROOT, Path('relative'))

    def test_acceptance_exact_bytes_not_status(self):
        p = self.evidence / 'EXP-01-draft-local-acceptance.json'
        p.write_text('{"status":"accepted","whole_card_accepted":true}')
        self.check_error('hash_mismatch', self.validate)

    def test_public_raw_and_REF_hash_distinct(self):
        self.matrix['exp01_REF_semantic_sha256'] = self.matrix['exp01_protocol_template']['raw_sha256']
        self.check_error('hash_mismatch', self.validate)

    def test_stale_public_contract_rejected(self):
        self.matrix['exp01_contract']['raw_sha256'] = '6c5353bcddec95613bf8901944840e1579b0853bb0606b79c49affc7fea55193'
        self.matrix['exp01_draft_slot']['expected_owner_schema']['schema'] = copy.deepcopy(self.matrix['exp01_contract'])
        self.matrix['exp01_REF_slot']['expected_owner_schema']['schema'] = copy.deepcopy(self.matrix['exp01_contract'])
        self.check_error('hash_mismatch', self.validate)

    def test_role_root_crossing_rejected(self):
        self.matrix['REF_stage_decision']['relative_path'] = '../EXP-02-root-REF-stage-decision-001.json'
        self.check_error('invalid_value', self.validate)

    def test_regular_nofollow(self):
        p = self.directory / 'link'
        p.symlink_to(self.mp)
        self.check_error('unsafe_path', B._read, p)

    def test_parser_invalid_inputs(self):
        for raw, code in [(b'{"x":1,"x":2}', 'duplicate_key'), (b'"\\ud800"', 'invalid_unicode'),
                          (b'\xff', 'invalid_unicode'), (b'[1e0]', 'invalid_type'),
                          (b'[NaN]', 'invalid_json'), (b'[9223372036854775808]', 'invalid_value'),
                          (b'[1,]', 'invalid_json'), (b'"\\u0000"', 'invalid_value')]:
            with self.subTest(raw=raw):
                self.check_error(code, B.parse_raw, raw)

    def test_signed_exit_preserved(self):
        self.assertEqual([-1, -9223372036854775808], B.parse_raw(b'[-1,-9223372036854775808]'))
        self.assertTrue(B._primitive('SignedInt64', -1))
        self.assertFalse(B._primitive('Count', -1))
        self.assertFalse(B._primitive('Count', True))
        self.assertEqual([True], B.parse_raw(b'[true]'))

    def test_depth_and_UTF8_limits(self):
        self.assertEqual('é' * 2048, B.parse_raw(json.dumps('é' * 2048, ensure_ascii=False).encode()))
        self.check_error('input_limit', B.parse_raw, json.dumps('é' * 2049, ensure_ascii=False).encode())
        B.parse_raw(b'[' * 32 + b'0' + b']' * 32)
        self.check_error('input_limit', B.parse_raw, b'[' * 33 + b'0' + b']' * 33)

    def test_array_limits(self):
        self.check_error('input_limit', B.parse_raw, b'[' + b'0,' * 100000 + b'0]')
        raw = json.dumps([[0] * 100000, [0] * 100000]).encode()
        self.check_error('input_limit', B.parse_raw, raw)

    def test_bounded_reader_sparse_file(self):
        p = self.directory / 'large'
        with p.open('wb') as stream:
            stream.truncate(67108865)
        self.check_error('input_limit', B._read, p)

    def test_external_error_table(self):
        for original, expected in [('invalid_json', 'invalid_json'), ('identity_conflict', 'external_schema_mismatch'),
                                   ('dependency_mismatch', 'dependency_mismatch'), ('not_enumerated', 'internal_error')]:
            with self.subTest(original=original):
                def bad():
                    raise B.G.ExpError(original, '/absolute-invalid-error-path')
                error = self.check_error(expected, B.external, bad)
                self.assertEqual('$', error.path)
        def unexpected():
            raise RuntimeError('private text')
        self.check_error('internal_error', B.external, unexpected)

    def test_plan_always_closed_without_owner_schemas(self):
        for stage in ('early_pilot', 'full_pilot', 'confirmatory'):
            with self.subTest(stage=stage):
                ready, operating = self.directory / 'ready', self.directory / 'operating'
                ready.write_text('{"accepted":true,"adapters":7}')
                operating.write_text('{"accepted":true,"REF":"B0"}')
                out = self.directory / stage
                self.check_error('external_contract_pending', B.build_run_plan, self.mp, self.cp,
                                 ready, operating, ROOT, self.evidence, stage, out)
                self.assertFalse(out.exists())

    def test_REF_selection_atomic(self):
        good = {'mode': 'validation_data_collection', 'selection_status': 'not_run_pending_selection', 'selection_record': None}
        B._ref_use(good, 'early_pilot')
        self.check_error('REF_unresolved', B._ref_use, good, 'confirmatory')
        good['selection_record'] = artifact('REF_selection')
        self.check_error('REF_unresolved', B._ref_use, good, 'early_pilot')
        selected = {'mode': 'uses_selected_REF', 'selection_status': 'selected_record_required', 'selection_record': artifact('REF_selection')}
        B._ref_use(selected, 'confirmatory')

    def test_cost_missing_observed_units(self):
        B._cost(cost())
        for field, value, code in [('unit', 'USD', 'cost_unit_mismatch'), ('source_artifact', None, 'cost_missing'),
                                   ('value_decimal', None, 'cost_missing'), ('policy_id', None, 'cost_missing')]:
            with self.subTest(field=field):
                row = cost()
                row[field] = value
                self.check_error(code, B._cost, row)
        row = cost()
        row.update(basis='missing', value_decimal=None, missing_reason='not_measured', source_artifact=None)
        B._cost(row)

    def test_exact_decimal_not_float(self):
        for value, valid in [('0.125', True), ('0', True), ('01', False), ('1e4', False), ('-1', False)]:
            with self.subTest(value=value):
                self.assertEqual(valid, B._primitive('UnsignedDecimal', value))

    def test_cli_spec_actual_subprocess(self):
        result = self.cli('spec', '--matrix', self.mp, '--controls', self.cp, '--input-root', ROOT, '--evidence-root', self.evidence)
        # The subprocess has genuine production trust pins; our synthetic evidence
        # must fail there. A separate author CLI run uses actual dispatched bytes.
        self.assertEqual(2, result.returncode, result.stdout)
        self.assertEqual('', result.stderr)
        self.assertEqual('hash_mismatch', json.loads(result.stdout)['code'])

    def cli(self, *args):
        return subprocess.run([sys.executable, '-B', str(HERE / 'build_run_plan.py'), *map(str, args)],
                              capture_output=True, text=True, cwd=self.directory, env={**os.environ, 'PYTHONDONTWRITEBYTECODE': '1'}, check=False)

    def test_cli_no_discovery_or_echo(self):
        for args in [('spec',), ('spec', '--evidence-root', '/private/sentinel'), ('wrong',)]:
            with self.subTest(args=args):
                result = self.cli(*args)
                self.assertEqual(2, result.returncode)
                self.assertEqual({'protocol': 'exp02-error-v1', 'code': 'invalid_argv', 'path': '$'}, json.loads(result.stdout))
                self.assertEqual('', result.stderr)
                self.assertNotIn('private', result.stdout)

    def test_outer_style_import_collection(self):
        script = '''import importlib.util,sys,unittest
sys.path[:]=[p for p in sys.path if p not in ('',%r)]
s=importlib.util.spec_from_file_location('outer_exp02',%r)
m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
print(unittest.defaultTestLoader.loadTestsFromModule(m).countTestCases())
''' % (str(HERE), str(HERE / 'test_run_plan.py'))
        result = subprocess.run([sys.executable, '-B', '-c', script], cwd=self.directory, capture_output=True, text=True, check=False)
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertGreater(int(result.stdout.strip()), 0)

    def test_no_process_calls_in_API(self):
        with mock.patch.object(subprocess, 'run', side_effect=AssertionError('unexpected process')):
            self.validate()

class EvidenceValuesTests(unittest.TestCase):
    def error(self, code, function, *args):
        with self.assertRaises(B.Exp02Error) as caught:
            function(*args)
        self.assertEqual(code, caught.exception.code)

    def row(self):
        return {'job_id': 'job', 'attempt_id': 'try1', 'status': 'completed_with_failures',
                'native_completion_manifest': artifact('completion_manifest'), 'process_exit': 0,
                'started_at_ns': 10, 'finished_at_ns': 20, 'execution_clock_domain': 'process_clock',
                'policy_rows': [policy_row(p) for p in B.ENUMS['RequiredPolicyID']],
                'cost_measures': [], 'deviations': []}

    def test_failure_denominators_and_completion(self):
        row = self.row()
        B.validate_index_values({'rows': [row]})
        for field, value in [('recorded_rows', 4), ('planned_rows', 6), ('missing_rows', 1), ('fallback_rows', 1)]:
            with self.subTest(field=field):
                changed = copy.deepcopy(row)
                changed['policy_rows'][0][field] = value
                self.error('failed_row_mismatch', B.validate_index_values, {'rows': [changed]})
        row['status'] = 'completed'
        self.error('failed_row_mismatch', B.validate_index_values, {'rows': [row]})

    def test_exit_zero_is_not_all_success(self):
        row = self.row()
        self.assertEqual(0, row['process_exit'])
        self.assertEqual(7, sum(p['failed_rows'] for p in row['policy_rows']))
        B.validate_index_values({'rows': [row]})
        row['policy_rows'].pop()
        self.error('failed_row_mismatch', B.validate_index_values, {'rows': [row]})

    def test_overflow_not_subtracted_away(self):
        row = self.row()
        row['policy_rows'][0].update(successful_rows=9223372036854775807, failed_rows=1)
        self.error('failed_row_mismatch', B.validate_index_values, {'rows': [row]})

    def test_not_run_remains_unknown(self):
        row = self.row()
        row.update(status='not_run', policy_rows=[], native_completion_manifest=None,
                   process_exit=None, started_at_ns=None, finished_at_ns=None, execution_clock_domain=None)
        B.validate_index_values({'rows': [row]})
        row['process_exit'] = 0
        self.error('failed_row_mismatch', B.validate_index_values, {'rows': [row]})

    def test_retry_attempts_not_overwritten(self):
        row = self.row()
        again = copy.deepcopy(row)
        again['attempt_id'] = 'try2'
        B.validate_index_values({'rows': [row, again]})
        again['attempt_id'] = row['attempt_id']
        self.error('failed_row_mismatch', B.validate_index_values, {'rows': [row, again]})

    def test_missing_cost_and_shared_double_count(self):
        row = self.row()
        measure = cost()
        measure.update(basis='missing', value_decimal=None, missing_reason='not_measured', source_artifact=None)
        row['cost_measures'] = [measure]
        self.error('cost_missing', B.validate_index_values, {'rows': [row]})
        row['deviations'] = [{'code': 'cost_missing', 'evidence': None}]
        B.validate_index_values({'rows': [row]})
        again = copy.deepcopy(row)
        again['attempt_id'] = 'try2'
        self.error('cost_missing', B.validate_index_values, {'rows': [row, again]})

    def test_concrete_native_budget_not_fabricated(self):
        # These required references cannot be substituted by zero numeric defaults.
        fields = B.MODELS['CommonConditions']['fields']
        for field in ('budget_rule', 'reader_generator_rule', 'encoder_revision_rule', 'eligibility_rule'):
            self.assertEqual('ArtifactRef', fields[field])
        self.assertEqual('literal:owner_operating_plan_required', B.MODELS['PolicySpec']['fields']['parameter_source'])

    def test_scope_wire_alias_rejected(self):
        valid = {'workspace': 'w', 'project': 'p', 'user': 'u', 'agent': 'a'}
        B._typed('external:v1.Scope', valid, '$.scope')
        bad = dict(valid)
        bad['workspace_id'] = bad.pop('workspace')
        with self.assertRaises(B.Exp02Error):
            B._typed('external:v1.Scope', bad, '$.scope')

    def test_raw_hash_actual_whitespace(self):
        self.assertEqual({'a': 1}, B.parse_raw(b'{ "a" : 1 }'))
        self.assertNotEqual(B.digest(b'{ "a" : 1 }'), B.digest(b'{"a":1}'))
        self.assertEqual(b'{"a":1}\n', B.compact({'a': 1}))

    def test_plan_index_schedule_raw_and_input_joins(self):
        plan_raw = b'{"synthetic_plan":true}\n'
        job = {'job_id': 'job', 'bindings': {'synthetic': True}, 'native_argv': ['not-executed'],
               'common': {'source_manifest': artifact(), 'expected_schedule': [0] * 5}}
        row = self.row()
        row.update(plan_raw_sha256=B.digest(plan_raw), input_bindings=job['bindings'],
                   source_manifest=job['common']['source_manifest'], actual_argv=['not-executed'])
        index = {'plan': {'role': 'plan', 'raw_sha256': B.digest(plan_raw), 'size_bytes': len(plan_raw)}, 'rows': [row]}
        B.validate_index_plan(index, {'jobs': [job]}, plan_raw)
        row['policy_rows'][0].update(planned_rows=6)
        self.error('schedule_mismatch', B.validate_index_plan, index, {'jobs': [job]}, plan_raw)
        row['policy_rows'][0].update(planned_rows=5)
        row['input_bindings'] = {'synthetic': False}
        self.error('common_condition_mismatch', B.validate_index_plan, index, {'jobs': [job]}, plan_raw)


class BootstrapTests(unittest.TestCase):
    def test_direct_codec_error_is_translated(self):
        def bad():
            raise B.G.IO.ContractError('invalid_json', '/absolute-invalid-error-path')
        with self.assertRaises(B.Exp02Error) as caught:
            B.external(bad, path='$.matrix')
        self.assertEqual(('invalid_json', '$.matrix'), (caught.exception.code, caught.exception.path))

    def test_dependency_failure_CLI_is_redacted(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'build_run_plan.py'
            target.write_bytes((HERE / 'build_run_plan.py').read_bytes())
            result = subprocess.run([sys.executable, '-B', str(target), 'spec'], capture_output=True, text=True, check=False)
            self.assertEqual(2, result.returncode)
            self.assertEqual('', result.stderr)
            self.assertEqual({'protocol': 'exp02-error-v1', 'code': 'dependency_mismatch', 'path': '$'}, json.loads(result.stdout))

    def test_cli_duplicate_flags_rejected(self):
        with self.assertRaises(B.Exp02Error) as caught:
            B._arguments(['spec', '--matrix', 'x', '--matrix', 'y', '--input-root', 'z', '--evidence-root', 'w'])
        self.assertEqual('invalid_argv', caught.exception.code)

    def test_immutable_registry_is_not_extended(self):
        self.assertNotIn('PolicyMatrix', B.G.IO._TYPES)
        self.assertNotIn('RunIndex', B.G.IO._OBJECTS)
        self.assertEqual(24, len(B.MODELS))
        self.assertEqual('planning_dispatch', next(k for k in B.MODELS['RunPlan']['fields'] if k.endswith('_dispatch')))


if __name__ == '__main__':
    unittest.main()

"""EXP-02.spec: closed comparison contracts; no policy execution or approval oracle."""
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import stat
import sys

HERE = Path(__file__).absolute().parent
BASE = HERE.parents[1]
MAX_BYTES, MAX_ROWS, MAX_ARRAY, MAX_INT = 67108864, 100000, 200000, 9223372036854775807
G_SHA = '75fd80ea717a50ea2384d084ae077deda77ca75a915d28d009c96dae2e5f228e'
V_SHA = 'ae679cabcb5a0e5ac5a20849c46fdca5fb604e1d9d1b271bf9dd9b5ddf094c82'
EXP01_SHA = 'a1296cea0119cdb11b4f6f2c0c1f6aeafe3e7f33ee4d48b3bef1aeda70c92a1a'
TEMPLATE_SHA = '3c1d63f004fc84b835006f4bc0f60a9597996b5ad2f03e1c7663d97504af2650'
REF_SHA = '435819cc8d6ad7d769703a23c7a1f75130793db9b0b16962c33ea4ddadf098af'
DECISION_SHA = '563d4f945de01fde525e4dbf44413d00ba03225dc32d02c89ec78b3a8de8fc2c'
ACCEPTANCE_SHA = 'b23d97cf2486758781892895c7261a57868fe035692a3748220bdded140cfcbf'
BASELINE_SHA = '04463d838f397772743c5740a346230eb8e9db60db8dddc824d52e1f083790ef'
DYNAMICS_SHA = '952d663b76254e15a3a65ab09dcd580504bc4c2814ce4903a8883ece1ca8f0da'

class Exp02Error(Exception):
    def __init__(self, code, path='$'):
        self.code, self.path = code, path
        super().__init__(code + ': ' + path)


def fail(code, path='$'):
    raise Exp02Error(code, path) from None


def require(condition, code='invalid_value', path='$'):
    if not condition:
        fail(code, path)


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def _bootstrap_bytes(path, expected):
    # Exact repository dependencies, not caller-selectable plugins.
    try:
        require(path.is_absolute() and not path.is_symlink(), 'dependency_mismatch')
        with path.open('rb') as stream:
            require(stat.S_ISREG(os.fstat(stream.fileno()).st_mode), 'dependency_mismatch')
            raw = stream.read(MAX_BYTES + 1)
        require(digest(raw) == expected and len(raw) <= MAX_BYTES, 'dependency_mismatch')
    except OSError:
        fail('dependency_mismatch')


def _dependencies():
    directory = HERE.with_name('exp_01')
    gp, vp = directory / 'generate_streams.py', directory / 'validate_splits.py'
    _bootstrap_bytes(gp, G_SHA)
    _bootstrap_bytes(vp, V_SHA)
    try:
        alias = '_arkism_exp02_pinned_exp01_validator'
        spec = importlib.util.spec_from_file_location(alias, vp)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        generator = module.G
        require(Path(module.__file__).absolute() == vp, 'dependency_mismatch')
        require(Path(generator.__file__).absolute() == gp, 'dependency_mismatch')
        _bootstrap_bytes(gp, G_SHA)
        _bootstrap_bytes(vp, V_SHA)
        return module, generator
    except Exp02Error:
        raise
    except Exception:
        fail('dependency_mismatch')


try:
    V, G = _dependencies()
except Exception:
    if __name__ == '__main__':
        print('{"protocol":"exp02-error-v1","code":"dependency_mismatch","path":"$"}')
        raise SystemExit(2) from None
    raise

MODELS = {'ArtifactRef': {'additional_fields': False,
                 'all_fields_required': True,
                 'fields': {'role': 'ArtifactRole',
                            'relative_path': 'SafeRelativePath',
                            'raw_sha256': 'SHA',
                            'size_bytes': 'Count'}},
 'SemanticBinding': {'additional_fields': False,
                     'all_fields_required': True,
                     'fields': {'type_name': 'ClosedNativeTypeName', 'semantic_sha256': 'SHA'}},
 'OwnerContractSlot': {'additional_fields': False,
                       'all_fields_required': True,
                       'fields': {'owner_task': 'OwnerTask',
                                  'role': 'OwnerRole',
                                  'binding': 'ArtifactRef?',
                                  'expected_owner_schema': 'OwnerSchemaBinding?',
                                  'owner_acceptance': 'ArtifactRef?'}},
 'AdapterBinding': {'additional_fields': False,
                    'all_fields_required': True,
                    'fields': {'implementation_sources': 'ArtifactRef[]',
                               'policy_version': 'ID',
                               'ranker_parameters_sha256': 'SHA',
                               'native_dynamics_config_sha256': 'SHA?',
                               'native_registry_raw': 'ArtifactRef',
                               'native_registry_semantic': 'SemanticBinding'}},
 'PolicySpec': {'additional_fields': False,
                'all_fields_required': True,
                'fields': {'policy_id': 'RequiredPolicyID',
                           'contract_policy_key': 'RequiredPolicyID',
                           'adapter_binding': 'AdapterBinding?',
                           'implementation_acceptance': 'ArtifactRef?',
                           'parameter_source': 'literal:owner_operating_plan_required',
                           'fallback': 'literal:error',
                           'role': 'PolicyRole'}},
 'OptionalPolicySpec': {'additional_fields': False,
                        'all_fields_required': True,
                        'fields': {'policy_id': 'OptionalPolicyID',
                                   'selection': 'OptionalSelection',
                                   'support': 'literal:unsupported_by_fixed_952d',
                                   'future_contract': 'ArtifactRef?'}},
 'PolicyMatrix': {'additional_fields': False,
                  'all_fields_required': True,
                  'fields': {'protocol': 'literal:exp02-policy-matrix-v1',
                             'scope': 'literal:specification_only',
                             'required_policy_order': 'RequiredPolicyID[]',
                             'policies': 'PolicySpec[]',
                             'optional_policies': 'OptionalPolicySpec[]',
                             'exp01_draft_slot': 'OwnerContractSlot',
                             'exp01_REF_slot': 'OwnerContractSlot',
                             'minimum_DYN05_acceptance': 'ArtifactRef?',
                             'dynamics_contract_sha256': 'SHA',
                             'baseline_contract_sha256': 'SHA',
                             'feature_contract_binding': 'ArtifactRef?',
                             'evaluation_adapter_slot': 'OwnerContractSlot',
                             'exp01_contract': 'ArtifactRef',
                             'exp01_protocol_template': 'ArtifactRef',
                             'exp01_REF_semantic_sha256': 'SHA',
                             'REF_stage_decision': 'ArtifactRef'}},
 'ControlSpec': {'additional_fields': False,
                 'all_fields_required': True,
                 'fields': {'control_id': 'ControlID',
                            'requirement': 'ControlRequirement',
                            'native_policy_id': 'RequiredPolicyID?',
                            'rule': 'ControlRule',
                            'condition_binding': 'ArtifactRef?',
                            'support_acceptance': 'ArtifactRef?'}},
 'Controls': {'additional_fields': False,
              'all_fields_required': True,
              'fields': {'protocol': 'literal:exp02-controls-v1',
                         'scope': 'literal:specification_only',
                         'controls': 'ControlSpec[]',
                         'comparison_families': 'ComparisonFamily[]',
                         'signal_regimes': 'SignalRegime[]',
                         'denominator_rule': 'literal:planned_scheduled_rows_preserve_failure_unknown_notrun',
                         'cost_rule': 'literal:separate_units_observed_shared_incremental_and_missing',
                         'operating_plan': 'ArtifactRef?'}},
 'InputBindings': {'additional_fields': False,
                   'all_fields_required': True,
                   'fields': {'matrix': 'ArtifactRef',
                              'controls': 'ArtifactRef',
                              'exp01_draft': 'ArtifactRef',
                              'REF_contract': 'ArtifactRef',
                              'operating_plan': 'ArtifactRef',
                              'readiness': 'ArtifactRef',
                              'registry_raw': 'ArtifactRef',
                              'registry_semantic': 'SemanticBinding',
                              'native_config_raw': 'ArtifactRef',
                              'native_config_semantic': 'SemanticBinding',
                              'materialization_manifest': 'ArtifactRef',
                              'native_input_manifest': 'ArtifactRef',
                              'native_validation_evidence': 'ArtifactRef',
                              'DYN05_acceptance': 'ArtifactRef',
                              'gold_review_evidence': 'ArtifactRef',
                              'evaluator_adapter_acceptance': 'ArtifactRef?',
                              'REF_use': 'REFUse'}},
 'CommonConditions': {'additional_fields': False,
                      'all_fields_required': True,
                      'fields': {'candidate_file': 'ArtifactRef',
                                 'events_file': 'ArtifactRef',
                                 'features_file': 'ArtifactRef',
                                 'probe_file': 'ArtifactRef',
                                 'source_manifest': 'ArtifactRef',
                                 'clock_domain': 'ID',
                                 'eligibility_rule': 'ArtifactRef',
                                 'budget_rule': 'ArtifactRef',
                                 'encoder_revision_rule': 'ArtifactRef',
                                 'reader_generator_rule': 'ArtifactRef',
                                 'seed_schedule_rule': 'ArtifactRef',
                                 'expected_schedule': 'ScheduleRow[]'}},
 'ScheduleRow': {'additional_fields': False,
                 'all_fields_required': True,
                 'fields': {'stream_id': 'ID',
                            'query_id': 'ID',
                            'scope': 'external:v1.Scope',
                            'at_ns': 'NS',
                            'cutoff_accepted_seq': 'Seq0',
                            'candidate_snapshot_id': 'ID',
                            'candidate_snapshot_sha256': 'SHA'}},
 'PlanJob': {'additional_fields': False,
             'all_fields_required': True,
             'fields': {'job_id': 'ID',
                        'condition_id': 'ID',
                        'comparison_family': 'ComparisonFamily',
                        'signal_regime': 'SignalRegime',
                        'bindings': 'InputBindings',
                        'common': 'CommonConditions',
                        'policy_order': 'RequiredPolicyID[]',
                        'native_argv': 'String[]',
                        'output_relative_dir': 'SafeRelativePath',
                        'claim_control_ids': 'ControlID[]'}},
 'RunPlan': {'additional_fields': False,
             'all_fields_required': True,
             'fields': {'protocol': 'literal:exp02-run-plan-v1',
                        'stage': 'RunnableStage',
                        'experiment_family': 'ID',
                        'verification_scope': 'literal:offline_synthetic',
                        'jobs': 'PlanJob[]',
                        'pending_research_decisions': 'String[]',
                        'claim_scope': 'literal:engineering_pilot_or_separately_locked_confirmatory',
                        'protocol_lock': 'ArtifactRef?',
                        'planning_dispatch': 'ArtifactRef'}},
 'BuildResult': {'additional_fields': False,
                 'all_fields_required': True,
                 'fields': {'protocol': 'literal:exp02-build-result-v1',
                            'status': 'BuildStatus',
                            'plan': 'ArtifactRef?',
                            'missing_gates': 'GateID[]',
                            'verification_scope': 'literal:specification_only',
                            'processes_executed': 'literal:0'}},
 'RunPolicyRow': {'additional_fields': False,
                  'all_fields_required': True,
                  'fields': {'policy_id': 'RequiredPolicyID',
                             'policy_version': 'ID',
                             'prediction_file': 'ArtifactRef',
                             'planned_rows': 'Count',
                             'recorded_rows': 'Count',
                             'successful_rows': 'Count',
                             'failed_rows': 'Count',
                             'missing_rows': 'Count',
                             'fallback_rows': 'Count'}},
 'CostMeasure': {'additional_fields': False,
                 'all_fields_required': True,
                 'fields': {'measurement_id': 'ID',
                            'phase': 'CostPhase',
                            'resource': 'CostResource',
                            'allocation': 'CostAllocation',
                            'policy_id': 'RequiredPolicyID?',
                            'value_decimal': 'UnsignedDecimal?',
                            'unit': 'CostUnit',
                            'basis': 'CostBasis',
                            'missing_reason': 'MissingCostReason?',
                            'source_artifact': 'ArtifactRef?',
                            'price_artifact': 'ArtifactRef?'}},
 'RunIndexRow': {'additional_fields': False,
                 'all_fields_required': True,
                 'fields': {'job_id': 'ID',
                            'attempt_id': 'ID',
                            'plan_raw_sha256': 'SHA',
                            'execution_dispatch': 'ArtifactRef',
                            'input_bindings': 'InputBindings',
                            'actual_argv': 'String[]',
                            'source_manifest': 'ArtifactRef',
                            'started_at_ns': 'NS?',
                            'finished_at_ns': 'NS?',
                            'process_exit': 'SignedInt64?',
                            'status': 'ExecutionStatus',
                            'native_completion_manifest': 'ArtifactRef?',
                            'native_stdout_status': 'ArtifactRef?',
                            'raw_log': 'ArtifactRef?',
                            'policy_rows': 'RunPolicyRow[]',
                            'cost_measures': 'CostMeasure[]',
                            'deviations': 'Deviation[]',
                            'execution_clock_domain': 'ID?'}},
 'Deviation': {'additional_fields': False,
               'all_fields_required': True,
               'fields': {'code': 'DeviationCode', 'evidence': 'ArtifactRef?'}},
 'RunIndex': {'additional_fields': False,
              'all_fields_required': True,
              'fields': {'protocol': 'literal:exp02-run-index-v1',
                         'plan': 'ArtifactRef',
                         'rows': 'RunIndexRow[]',
                         'analysis_status': 'literal:not_scored_by_EXP02',
                         'evaluation_adapter': 'OwnerContractSlot'}},
 'Exp02Error': {'additional_fields': False,
                'all_fields_required': True,
                'fields': {'protocol': 'literal:exp02-error-v1',
                           'code': 'ErrorCode',
                           'path': 'KnownSchemaPath'}},
 'Readiness': {'additional_fields': False,
               'all_fields_required': True,
               'fields': {'protocol': 'literal:exp02-readiness-v1',
                          'matrix': 'ArtifactRef',
                          'controls': 'ArtifactRef',
                          'EXP01_acceptance': 'ArtifactRef',
                          'DYN05_acceptance': 'ArtifactRef',
                          'native_materialization_acceptance': 'ArtifactRef',
                          'native_validation_evidence': 'ArtifactRef',
                          'REF_contract': 'ArtifactRef',
                          'operating_plan': 'ArtifactRef',
                          'protocol_lock': 'ArtifactRef?',
                          'trusted_review_manifest': 'ArtifactRef',
                          'REF_use': 'REFUse'}},
 'REFUse': {'additional_fields': False,
            'all_fields_required': True,
            'fields': {'mode': 'REFUseMode',
                       'selection_status': 'REFSelectionStatus',
                       'selection_record': 'ArtifactRef?'}},
 'OwnerSchemaBinding': {'additional_fields': False,
                        'all_fields_required': True,
                        'fields': {'protocol_id': 'String', 'schema': 'ArtifactRef'}}}
ENUMS = {'RequiredPolicyID': ['B0', 'B1', 'B2', 'V3-P1-FD', 'V3-P1-IR', 'V3-P1-NOTIME', 'V3-P1-NOOUT'],
 'OptionalPolicyID': ['B3', 'V3-P1-AD', 'B4', 'B5', 'B6'],
 'OptionalSelection': ['unselected', 'requested_future_contract'],
 'PolicyRole': ['comparator', 'treatment', 'ablation'],
 'OwnerTask': ['EXP-01.draft', 'EXP-01.final_lock', 'DYN-05.p1', 'EXP-02', 'EXP-03', 'root'],
 'OwnerRole': ['accepted_draft', 'REF_contract', 'final_lock', 'v2_evaluation_adapter'],
 'ComparisonFamily': ['fixed_candidate_mechanism', 'native_full_system'],
 'SignalRegime': ['synthetic_all_candidate_oracle', 'exposure_conditioned_observed', 'actual_api_accepted'],
 'RunnableStage': ['early_pilot', 'full_pilot', 'confirmatory'],
 'BuildStatus': ['spec_valid_pending_owner_bindings', 'executable_plan_built_not_executed'],
 'ExecutionStatus': ['not_run',
                     'completed',
                     'completed_with_failures',
                     'input_rejected',
                     'timeout',
                     'internal_error',
                     'publication_error',
                     'stdout_transport_error'],
 'ControlID': ['time_removal',
               'outcome_removal',
               'unknown_duplicate_checks',
               'delay_lifecycle',
               'elapsed_interference',
               'oracle_exposed_actual',
               'original_current',
               'exposure_replacement',
               'matched_write',
               'equal_tau_tag_resource'],
 'ControlRequirement': ['claim_conditional_unsupported',
                        'required_core',
                        'required_engineering',
                        'required_specification'],
 'ControlRule': ['disable_decay_only',
                 'disable_outcome_jumps_only',
                 'keep_unknown_and_unique_event_denominators',
                 'same_external_stream_with_arrival_lifecycle_conditions',
                 'time_only_and_event_density_only_controls',
                 'separate_information_and_observation_regimes',
                 'same_stream_original_target_vs_current_target',
                 'future_explicit_signal_policy_contract_required',
                 'validation_threshold_or_prefix_quota_no_future_selection',
                 'future_fastslow_capture_contract_required'],
 'CostPhase': ['ingest',
               'embedding',
               'importance',
               'retrieval',
               'reader_generator',
               'judge',
               'evidence_validation',
               'tuning_fit',
               'state_read_update',
               'repair_rebuild',
               'storage',
               'retry',
               'whole_run'],
 'CostResource': ['calls',
                  'input_tokens',
                  'output_tokens',
                  'wall_time',
                  'cpu_time',
                  'storage_bytes',
                  'peak_memory_bytes',
                  'actual_currency',
                  'synthetic_units'],
 'CostAllocation': ['shared_preprocessing', 'policy_incremental', 'whole_run_nonadditive'],
 'CostBasis': ['observed', 'estimated', 'missing'],
 'MissingCostReason': ['not_measured', 'source_unavailable', 'not_applicable'],
 'CostUnit': ['count', 'nanosecond', 'byte', 'USD', 'fixture_units_v0'],
 'DeviationCode': ['not_run',
                   'input_binding_changed',
                   'source_changed',
                   'argv_changed',
                   'timeout',
                   'missing_prediction',
                   'native_failed_row',
                   'publication_failed',
                   'stdout_failed_after_publication',
                   'cost_missing',
                   'unsupported_condition'],
 'ClosedNativeTypeName': ['PolicyRegistryV2', 'DynamicsRunConfigV2'],
 'GateID': ['EXP01_draft',
            'REF_contract',
            'REF_selection',
            'operating_plan',
            'DYN05_p1',
            'required_seven_adapters',
            'native_materialization',
            'native_validation',
            'gold_review',
            'trusted_owner_review',
            'final_protocol_lock',
            'SYS06_B',
            'EXP03_evaluation_adapter'],
 'ArtifactRole': ['matrix',
                  'controls',
                  'owner_contract',
                  'acceptance',
                  'source',
                  'registry',
                  'config',
                  'native_input',
                  'native_materialization',
                  'native_validation',
                  'operating_plan',
                  'readiness',
                  'REF_selection',
                  'eligibility_rule',
                  'budget_rule',
                  'encoder_rule',
                  'reader_rule',
                  'seed_rule',
                  'events',
                  'candidates',
                  'features',
                  'probes',
                  'completion_manifest',
                  'prediction',
                  'stdout_status',
                  'raw_log',
                  'cost_source',
                  'price_table',
                  'dispatch',
                  'plan',
                  'review_manifest',
                  'evaluation_adapter',
                  'owner_schema'],
 'ErrorCode': ['invalid_argv',
               'unsafe_path',
               'input_limit',
               'invalid_json',
               'invalid_unicode',
               'duplicate_key',
               'missing_field',
               'unknown_field',
               'invalid_type',
               'invalid_value',
               'hash_mismatch',
               'external_contract_pending',
               'external_schema_mismatch',
               'registry_mismatch',
               'configuration_mismatch',
               'common_condition_mismatch',
               'ablation_mismatch',
               'unsupported_policy',
               'unsupported_control',
               'unsupported_family',
               'REF_unresolved',
               'readiness_unverified',
               'native_materialization_pending',
               'native_validation_pending',
               'operating_plan_unresolved',
               'final_lock_required',
               'schedule_mismatch',
               'failed_row_mismatch',
               'cost_unit_mismatch',
               'cost_missing',
               'output_exists',
               'io_error',
               'internal_error',
               'dependency_mismatch'],
 'REFUseMode': ['validation_data_collection', 'uses_selected_REF'],
 'REFSelectionStatus': ['not_run_pending_selection', 'selected_record_required']}

# Single error conversion table for accepted dependencies. Unlisted is internal.
ERROR_MAP = {code: code for code in (
    'invalid_json', 'invalid_unicode', 'duplicate_key', 'invalid_type',
    'invalid_value', 'input_limit', 'missing_field', 'unknown_field',
    'unsafe_path', 'io_error', 'output_exists', 'hash_mismatch')}
ERROR_MAP.update(resource_limit='input_limit', dependency_mismatch='dependency_mismatch',
                 identity_conflict='external_schema_mismatch',
                 final_lock_forbidden_in_draft='external_schema_mismatch',
                 review_unresolved='external_schema_mismatch')


def external(call, *args, path='$'):
    try:
        return call(*args)
    except (G.ExpError, G.IO.ContractError) as error:
        code = ERROR_MAP.get(error.code, 'internal_error')
        fail(code, '$' if code in ('internal_error', 'dependency_mismatch') else path)
    except Exp02Error:
        raise
    except Exception:
        fail('internal_error')


def _raw_limits(tree):
    pending, total = [tree], 0
    while pending:
        value = pending.pop()
        if isinstance(value, G.IO._Pairs):
            for key, child in value:
                pending.extend((key, child))
        elif type(value) is list:
            require(len(value) <= MAX_ROWS, 'input_limit')
            total += len(value)
            require(total <= MAX_ARRAY, 'input_limit')
            pending.extend(value)
        elif isinstance(value, G.IO._OverflowInteger):
            fail('invalid_value')
        elif type(value) is int:
            require(-MAX_INT - 1 <= value <= MAX_INT)
        elif type(value) is float:
            fail('invalid_type' if math.isfinite(value) else 'invalid_value')
        elif type(value) is str:
            require(len(value.encode('utf8')) <= 4096, 'input_limit')
            require('\0' not in value)


def parse_raw(raw, path='$'):
    require(type(raw) is bytes, 'invalid_type', path)
    require(len(raw) <= MAX_BYTES, 'input_limit', path)
    try:
        text = raw.decode('utf8')
    except UnicodeError:
        fail('invalid_unicode', path)
    external(G._depth_guard, text, path=path)
    tree = external(G.IO._parse_json, text, path=path)
    external(G.IO._unicode, tree, path=path)
    external(G.IO._duplicates, tree, path=path)
    try:
        _raw_limits(tree)
    except Exp02Error as error:
        fail(error.code, path)
    return external(G.IO._plain, tree, path=path)


def compact(value):
    return external(G.compact, value) + b'\n'


def _relative(value):
    return (type(value) is str and bool(value) and '\\' not in value and
            all(part not in ('', '.', '..') for part in value.split('/')) and
            not value.startswith('/') and '\0' not in value)


def _primitive(kind, value):
    if kind.startswith('literal:'):
        literal = kind[8:]
        expected = 0 if literal == '0' else literal
        return type(value) is type(expected) and value == expected
    if kind == 'String':
        return type(value) is str and '\0' not in value and len(value.encode('utf8')) <= 4096
    if kind == 'ID':
        return type(value) is str and re.fullmatch('[a-z][a-z0-9_-]{0,63}', value, re.ASCII) is not None
    if kind == 'SHA':
        return type(value) is str and re.fullmatch('[0-9a-f]{64}', value, re.ASCII) is not None
    if kind in ('Count', 'NS', 'Seq0', 'SignedInt64'):
        low = -MAX_INT - 1 if kind == 'SignedInt64' else 0
        return type(value) is int and low <= value <= MAX_INT
    if kind == 'SafeRelativePath':
        return _relative(value)
    if kind == 'UnsignedDecimal':
        return type(value) is str and re.fullmatch(r'(0|[1-9][0-9]*)(\.[0-9]+)?', value, re.ASCII) is not None
    if kind == 'KnownSchemaPath':
        return known_path(value)
    fail('internal_error')


def known_path(value):
    if value == '$':
        return True
    if type(value) is not str or not value.startswith('$'):
        return False
    tokens = re.findall(r'\.[a-zA-Z][a-zA-Z0-9_]*|\[\*\]', value[1:])
    if ''.join(tokens) != value[1:]:
        return False
    states = set(MODELS)
    for token in tokens:
        following = set()
        for state in states:
            if token == '[*]' and state.endswith('[]'):
                following.add(state[:-2])
            elif state in MODELS and token.startswith('.'):
                field = MODELS[state]['fields'].get(token[1:])
                if field:
                    following.add(field.rstrip('?'))
        states = following
    return bool(states)


def _typed(kind, value, path):
    if kind.endswith('?'):
        if value is None:
            return
        kind = kind[:-1]
    if kind.endswith('[]'):
        require(type(value) is list, 'invalid_type', path)
        for item in value:
            _typed(kind[:-2], item, path + '[*]')
    elif kind in MODELS:
        require(type(value) is dict, 'invalid_type', path)
        fields = MODELS[kind]['fields']
        for name in fields:
            require(name in value, 'missing_field', path + '.' + name)
        require(not set(value).difference(fields), 'unknown_field', path)
        for name, child in fields.items():
            _typed(child, value[name], path + '.' + name)
    elif kind in ENUMS:
        require(type(value) is str, 'invalid_type', path)
        require(value in ENUMS[kind], 'invalid_value', path)
    elif kind == 'external:v1.Scope':
        external(G.IO.validate_typed, 'Scope', value, path=path)
    else:
        require(_primitive(kind, value), 'invalid_value', path)


def validate_typed(kind, value):
    require(kind in MODELS, 'invalid_type')
    # Compact then strict raw parsing applies limits to caller-owned values too.
    raw = compact(value)
    parse_raw(raw)
    _typed(kind, value, '$')


def _read(path, slot='$'):
    return external(G.bounded_read, path, path=slot)


def _directory(path):
    return external(G.checked_directory, path)


def read_reference(reference, root, role, slot):
    require(type(reference) is dict, 'external_contract_pending', slot)
    require(reference['role'] == role, 'external_schema_mismatch', slot)
    require(_relative(reference['relative_path']), 'unsafe_path', slot)
    raw = _read(Path(root) / reference['relative_path'], slot)
    require(len(raw) == reference['size_bytes'] and digest(raw) == reference['raw_sha256'], 'hash_mismatch', slot)
    return raw


def _fixed_reference(reference, root, role, relative, expected, slot):
    require(reference['relative_path'] == relative, 'external_schema_mismatch', slot)
    require(reference['raw_sha256'] == expected, 'hash_mismatch', slot)
    return read_reference(reference, root, role, slot)


def _owner_slots(matrix):
    for field in ('exp01_draft_slot', 'exp01_REF_slot'):
        slot = matrix[field]
        require(slot['owner_task'] == 'EXP-01.draft', 'external_schema_mismatch', '$.' + field)
        role = 'accepted_draft' if field == 'exp01_draft_slot' else 'REF_contract'
        require(slot['role'] == role, 'external_schema_mismatch', '$.' + field)
        require(slot['binding'] == matrix['exp01_protocol_template'], 'external_schema_mismatch', '$.' + field)
        schema = slot['expected_owner_schema']
        require(schema is not None and schema['protocol_id'] == 'exp01-protocol-draft-v1' and
                schema['schema'] == matrix['exp01_contract'], 'external_schema_mismatch', '$.' + field)
    slot = matrix['evaluation_adapter_slot']
    require(slot['owner_task'] == 'EXP-03' and slot['role'] == 'v2_evaluation_adapter', 'external_schema_mismatch', '$.evaluation_adapter_slot')


def _matrix(matrix):
    order = ENUMS['RequiredPolicyID']
    require(matrix['required_policy_order'] == order)
    require([p['policy_id'] for p in matrix['policies']] == order)
    require([p['policy_id'] for p in matrix['optional_policies']] == ENUMS['OptionalPolicyID'])
    require(matrix['baseline_contract_sha256'] == BASELINE_SHA and matrix['dynamics_contract_sha256'] == DYNAMICS_SHA, 'hash_mismatch')
    for policy in matrix['policies']:
        require(policy['policy_id'] == policy['contract_policy_key'])
        expected = 'treatment' if policy['policy_id'] == 'B2' else 'ablation' if policy['policy_id'] in ('V3-P1-NOTIME', 'V3-P1-NOOUT') else 'comparator'
        require(policy['role'] == expected)
        # Current specification has no assigned adapter/evidence schema to authenticate.
        require(policy['adapter_binding'] is None and policy['implementation_acceptance'] is None,
                'external_contract_pending', '$.policies')
    require(matrix['minimum_DYN05_acceptance'] is None and matrix['feature_contract_binding'] is None,
            'external_contract_pending', '$.minimum_DYN05_acceptance')
    require(matrix['evaluation_adapter_slot']['binding'] is None and
            matrix['evaluation_adapter_slot']['owner_acceptance'] is None and
            matrix['evaluation_adapter_slot']['expected_owner_schema'] is None,
            'external_contract_pending', '$.evaluation_adapter_slot')
    _owner_slots(matrix)


CONTROL_ROWS = (
    ('time_removal', 'required_core', 'V3-P1-NOTIME', 'disable_decay_only'),
    ('outcome_removal', 'required_core', 'V3-P1-NOOUT', 'disable_outcome_jumps_only'),
    ('unknown_duplicate_checks', 'required_engineering', None, 'keep_unknown_and_unique_event_denominators'),
    ('delay_lifecycle', 'required_specification', None, 'same_external_stream_with_arrival_lifecycle_conditions'),
    ('elapsed_interference', 'required_specification', None, 'time_only_and_event_density_only_controls'),
    ('oracle_exposed_actual', 'required_specification', None, 'separate_information_and_observation_regimes'),
    ('original_current', 'required_specification', None, 'same_stream_original_target_vs_current_target'),
    ('exposure_replacement', 'claim_conditional_unsupported', None, 'future_explicit_signal_policy_contract_required'),
    ('matched_write', 'claim_conditional_unsupported', None, 'validation_threshold_or_prefix_quota_no_future_selection'),
    ('equal_tau_tag_resource', 'claim_conditional_unsupported', None, 'future_fastslow_capture_contract_required'))


def _controls(controls):
    require(len(controls['controls']) == len(CONTROL_ROWS))
    for control, expected in zip(controls['controls'], CONTROL_ROWS):
        require(tuple(control[k] for k in ('control_id', 'requirement', 'native_policy_id', 'rule')) == expected)
        require(control['condition_binding'] is None and control['support_acceptance'] is None,
                'external_contract_pending', '$.controls')
    require(controls['comparison_families'] == ENUMS['ComparisonFamily'])
    require(controls['signal_regimes'] == ENUMS['SignalRegime'])
    require(controls['operating_plan'] is None, 'external_contract_pending', '$.operating_plan')


def _exp01_bindings(matrix, input_root, evidence_root):
    directory = 'evaluation/memory-harness-v1/experiments/exp_01/'
    _fixed_reference(matrix['exp01_contract'], input_root, 'owner_schema', directory + 'dataset_contract.json', EXP01_SHA, '$.exp01_contract')
    raw = _fixed_reference(matrix['exp01_protocol_template'], input_root, 'owner_contract', directory + 'protocol.template.json', TEMPLATE_SHA, '$.exp01_protocol_template')
    require(matrix['exp01_REF_semantic_sha256'] == REF_SHA, 'hash_mismatch', '$.exp01_REF_semantic_sha256')
    external(V.validate_protocol, raw, 'draft', path='$.exp01_protocol_template')
    protocol = external(G.decode_typed, raw, 'ProtocolDraft', path='$.exp01_protocol_template')
    require(protocol['ref_contract_sha256'] == REF_SHA, 'hash_mismatch', '$.exp01_REF_semantic_sha256')
    _fixed_reference(matrix['REF_stage_decision'], evidence_root, 'owner_contract',
                     'EXP-02-root-REF-stage-decision-001.json', DECISION_SHA, '$.REF_stage_decision')
    _fixed_reference(matrix['exp01_draft_slot']['owner_acceptance'], evidence_root, 'acceptance',
                     'EXP-01-draft-local-acceptance.json', ACCEPTANCE_SHA, '$.exp01_draft_slot')
    # REF semantic ownership is included in accepted draft, not a manufactured separate acceptance.
    require(matrix['exp01_REF_slot']['owner_acceptance'] is None, 'external_contract_pending', '$.exp01_REF_slot')


def _fixed_contracts(input_root, matrix):
    bindings = (
        ('docs/research/memory-harness/dynamics_policy_contract.json', 'dynamics_contract_sha256'),
        ('evaluation/memory-harness-v1/contracts/implementation_contract.json', 'baseline_contract_sha256'))
    for relative, field in bindings:
        raw = _read(Path(input_root) / relative, '$.' + field)
        require(digest(raw) == matrix[field], 'hash_mismatch', '$.' + field)


SPEC_MISSING = ['REF_contract', 'operating_plan', 'DYN05_p1', 'required_seven_adapters',
                'native_materialization', 'native_validation', 'gold_review', 'trusted_owner_review']


def validate_spec(matrix_path, controls_path, input_root, evidence_root):
    _directory(input_root)
    _directory(evidence_root)
    matrix = parse_raw(_read(matrix_path, '$.matrix'), '$.matrix')
    validate_typed('PolicyMatrix', matrix)
    _matrix(matrix)
    _fixed_contracts(input_root, matrix)
    controls = parse_raw(_read(controls_path, '$.controls'), '$.controls')
    validate_typed('Controls', controls)
    _controls(controls)
    _exp01_bindings(matrix, input_root, evidence_root)
    return {'protocol': 'exp02-build-result-v1', 'status': 'spec_valid_pending_owner_bindings',
            'plan': None, 'missing_gates': list(SPEC_MISSING),
            'verification_scope': 'specification_only', 'processes_executed': 0}


def build_run_plan(matrix_path, controls_path, readiness_path, operating_plan_path,
                   input_root, evidence_root, requested_stage, output_dir):
    require(requested_stage in ENUMS['RunnableStage'], 'invalid_value', '$.stage')
    validate_spec(matrix_path, controls_path, input_root, evidence_root)
    # Current owner schemas have not been assigned. Read explicit files safely, but
    # do not invent a compatible schema or grant from arbitrary caller metadata.
    _read(readiness_path, '$.readiness')
    _read(operating_plan_path, '$.operating_plan')
    require(Path(output_dir).is_absolute(), 'unsafe_path')
    fail('external_contract_pending', '$.readiness')


def validate_run_index(index_path, plan_path, input_root, evidence_root, run_output_root):
    for root in (input_root, evidence_root, run_output_root):
        _directory(root)
    index = parse_raw(_read(index_path))
    validate_typed('RunIndex', index)
    plan_raw = _read(plan_path)
    plan = parse_raw(plan_raw)
    validate_typed('RunPlan', plan)
    validate_plan_values(plan)
    validate_index_values(index)
    validate_index_plan(index, plan, plan_raw)
    # Actual evidence schemas/trust/native loader are still separate owners.
    fail('external_contract_pending', '$.evaluation_adapter')


def validate_plan_values(plan):
    require(bool(plan['jobs']), 'schedule_mismatch', '$.jobs')
    require(len({job['job_id'] for job in plan['jobs']}) == len(plan['jobs']), 'schedule_mismatch', '$.jobs')
    for job in plan['jobs']:
        require(job['policy_order'] == ENUMS['RequiredPolicyID'], 'registry_mismatch', '$.jobs[*].policy_order')
        require(job['comparison_family'] == 'fixed_candidate_mechanism', 'unsupported_family', '$.jobs[*].comparison_family')
        require(job['signal_regime'] == 'synthetic_all_candidate_oracle', 'unsupported_family', '$.jobs[*].signal_regime')
        require(not set(job['claim_control_ids']).difference(ENUMS['ControlID']), 'unsupported_control')
        require(not set(job['claim_control_ids']).intersection(('exposure_replacement', 'matched_write', 'equal_tau_tag_resource')), 'unsupported_control')
        schedule = job['common']['expected_schedule']
        require(bool(schedule) and len(schedule) <= MAX_INT // 7, 'schedule_mismatch')
        require(len({(r['stream_id'], r['query_id']) for r in schedule}) == len(schedule), 'schedule_mismatch')
        use = job['bindings']['REF_use']
        _ref_use(use, plan['stage'])
        # Native validator owns actual same candidate/evidence/encoder/reader/final-k
        # and concrete budget verification; ArtifactRef equality alone cannot prove it.


def _ref_use(use, stage):
    if use['mode'] == 'validation_data_collection':
        require(stage == 'early_pilot' and use['selection_status'] == 'not_run_pending_selection' and
                use['selection_record'] is None, 'REF_unresolved')
    else:
        require(use['selection_status'] == 'selected_record_required' and use['selection_record'] is not None, 'REF_unresolved')


COST_UNITS = {'calls': 'count', 'input_tokens': 'count', 'output_tokens': 'count',
              'wall_time': 'nanosecond', 'cpu_time': 'nanosecond', 'storage_bytes': 'byte',
              'peak_memory_bytes': 'byte', 'actual_currency': 'USD', 'synthetic_units': 'fixture_units_v0'}


def _cost(cost):
    require(COST_UNITS[cost['resource']] == cost['unit'], 'cost_unit_mismatch')
    if cost['basis'] == 'missing':
        require(cost['value_decimal'] is None and cost['missing_reason'] is not None, 'cost_missing')
    else:
        require(cost['value_decimal'] is not None and cost['missing_reason'] is None, 'cost_missing')
        require(cost['basis'] != 'observed' or cost['source_artifact'] is not None, 'cost_missing')
    require(cost['basis'] == 'missing' or cost['resource'] != 'actual_currency' or cost['price_artifact'] is not None, 'cost_missing')
    require(cost['allocation'] != 'policy_incremental' or cost['policy_id'] is not None, 'cost_missing')
    require(cost['allocation'] != 'shared_preprocessing' or cost['policy_id'] is None, 'cost_missing')


def validate_index_values(index):
    seen, measurements = set(), set()
    for row in index['rows']:
        key = (row['job_id'], row['attempt_id'])
        require(key not in seen, 'failed_row_mismatch')
        seen.add(key)
        require(row['started_at_ns'] is None or row['finished_at_ns'] is None or
                row['started_at_ns'] <= row['finished_at_ns'], 'failed_row_mismatch')
        if row['status'] == 'not_run':
            require(not row['policy_rows'] and row['native_completion_manifest'] is None and
                    all(row[n] is None for n in ('process_exit', 'started_at_ns', 'finished_at_ns', 'execution_clock_domain')), 'failed_row_mismatch')
        if row['status'] in ('completed', 'completed_with_failures'):
            require(row['native_completion_manifest'] is not None and row['process_exit'] == 0, 'failed_row_mismatch')
            require([p['policy_id'] for p in row['policy_rows']] == ENUMS['RequiredPolicyID'], 'failed_row_mismatch')
            failed = sum(p['failed_rows'] for p in row['policy_rows'])
            require((failed > 0) == (row['status'] == 'completed_with_failures'), 'failed_row_mismatch')
        for policy in row['policy_rows']:
            require(policy['successful_rows'] <= MAX_INT - policy['failed_rows'], 'failed_row_mismatch')
            require(policy['successful_rows'] + policy['failed_rows'] == policy['recorded_rows'] and
                    policy['recorded_rows'] <= policy['planned_rows'] and
                    policy['planned_rows'] - policy['recorded_rows'] == policy['missing_rows'] and
                    policy['fallback_rows'] == 0, 'failed_row_mismatch')
            if row['status'] in ('completed', 'completed_with_failures'):
                require(policy['missing_rows'] == 0, 'failed_row_mismatch')
        require(len({c['measurement_id'] for c in row['cost_measures']}) == len(row['cost_measures']), 'cost_missing')
        for cost in row['cost_measures']:
            require(cost['measurement_id'] not in measurements, 'cost_missing')
            measurements.add(cost['measurement_id'])
            _cost(cost)
        if any(c['basis'] == 'missing' for c in row['cost_measures']):
            require(any(d['code'] == 'cost_missing' for d in row['deviations']), 'cost_missing')


def validate_index_plan(index, plan, plan_raw):
    require(index['plan']['role'] == 'plan' and index['plan']['raw_sha256'] == digest(plan_raw) and
            index['plan']['size_bytes'] == len(plan_raw), 'hash_mismatch', '$.plan')
    jobs = {job['job_id']: job for job in plan['jobs']}
    require({row['job_id'] for row in index['rows']} == set(jobs), 'schedule_mismatch')
    for row in index['rows']:
        job = jobs[row['job_id']]
        require(row['plan_raw_sha256'] == digest(plan_raw), 'hash_mismatch')
        require(row['input_bindings'] == job['bindings'] and row['source_manifest'] == job['common']['source_manifest'],
                'common_condition_mismatch')
        if row['status'] != 'not_run':
            require(row['actual_argv'] == job['native_argv'], 'common_condition_mismatch')
        for policy in row['policy_rows']:
            require(policy['planned_rows'] == len(job['common']['expected_schedule']), 'schedule_mismatch')


FLAGS = {'spec': ('matrix', 'controls', 'input-root', 'evidence-root'),
         'plan': ('matrix', 'controls', 'readiness', 'operating-plan', 'input-root', 'evidence-root', 'stage', 'out'),
         'verify-index': ('index', 'plan', 'input-root', 'evidence-root', 'run-output-root')}


def _arguments(argv):
    require(bool(argv) and argv[0] in FLAGS, 'invalid_argv')
    mode, values, rest = argv[0], {}, argv[1:]
    require(len(rest) == 2 * len(FLAGS[mode]), 'invalid_argv')
    for position in range(0, len(rest), 2):
        flag, value = rest[position:position + 2]
        require(flag.startswith('--') and flag[2:] in FLAGS[mode] and flag[2:] not in values, 'invalid_argv')
        require(bool(value) and not value.startswith('--'), 'invalid_argv')
        values[flag[2:]] = value
    return mode, values


def main(argv):
    try:
        mode, args = _arguments(argv)
        if mode == 'spec':
            result = validate_spec(args['matrix'], args['controls'], args['input-root'], args['evidence-root'])
        elif mode == 'plan':
            result = build_run_plan(args['matrix'], args['controls'], args['readiness'], args['operating-plan'],
                                    args['input-root'], args['evidence-root'], args['stage'], args['out'])
        else:
            validate_run_index(args['index'], args['plan'], args['input-root'], args['evidence-root'], args['run-output-root'])
            fail('internal_error')
        sys.stdout.buffer.write(compact(result))
        sys.stdout.buffer.flush()
        return 0
    except Exp02Error as error:
        code, path = error.code, error.path
    except Exception:
        code, path = 'internal_error', '$'
    try:
        sys.stdout.buffer.write(compact({'protocol': 'exp02-error-v1', 'code': code, 'path': path}))
        sys.stdout.buffer.flush()
    except Exception:
        return 1
    return 1 if code in ('io_error', 'internal_error') else 2


if __name__ == '__main__':
    raise SystemExit(main(sys.argv[1:]))

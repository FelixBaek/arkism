"""DYN-05.p1 declared-binding comparison; no gold, scoring or REF selection.

The nine checks have the evidence scope fixed by native parent b7598e03.
Only explicitly supplied run/registry artifacts and pinned codec dependencies
are read. Commitment equality does not reconstruct source bodies or authority.
"""
import hashlib
import importlib.util
import math
import os
from pathlib import Path
import re
import stat
import struct

HERE = Path(__file__).absolute().parent
MAX_BYTES, MAX_ARRAY, MAX_ROWS, MAX_INT = 67108864, 200000, 100000, 9223372036854775807
CODEC_SHA = 'd575bf82f5d1088883fddf5191e97a60a2260cc46a37051af9a89cdd5b7aa74e'


class ContractErrorV2(Exception):
    def __init__(self, code, path='$'):
        self.code, self.path = code, path
        super().__init__(code + ': ' + path)


def fail(code, path='$'):
    raise ContractErrorV2(code, path) from None


def require(condition, code='invalid_value', path='$'):
    if not condition:
        fail(code, path)


def _codec():
    path = HERE / 'contract_io.py'
    try:
        raw = path.read_bytes()
        require(hashlib.sha256(raw).hexdigest() == CODEC_SHA, 'hash_mismatch')
        spec = importlib.util.spec_from_file_location('_dyn05_fixed_baseline_codec', path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module
    except ContractErrorV2:
        raise
    except Exception:
        fail('io_error')


IO = _codec()

MODELS = {'ContractErrorV2': {'additional_fields': False,
                     'fields': {'code': {'nullable': False,
                                         'ref': 'V2ErrorCode',
                                         'required': True,
                                         'type': 'ref'},
                                'path': {'nullable': False,
                                         'ref': 'SchemaPath',
                                         'required': True,
                                         'type': 'ref'}},
                     'kind': 'closed_object',
                     'required': ['code', 'path']},
 'DynamicsConfigV2': {'additional_fields': False,
                      'fields': {'atol': {'nullable': False,
                                          'ref': 'Tolerance',
                                          'required': True,
                                          'type': 'ref'},
                                 'clock': {'const': 'accepted_time',
                                           'nullable': False,
                                           'required': True,
                                           'type': 'string'},
                                 'clock_domain': {'nullable': False,
                                                  'ref': 'LegacyID',
                                                  'required': True,
                                                  'type': 'ref'},
                                 'config_sha256': {'nullable': False,
                                                   'ref': 'SHA',
                                                   'required': True,
                                                   'type': 'ref'},
                                 'decay_enabled': {'nullable': False,
                                                   'ref': 'Bool',
                                                   'required': True,
                                                   'type': 'ref'},
                                 'eta_negative': {'nullable': False,
                                                  'ref': 'UnitFloat',
                                                  'required': True,
                                                  'type': 'ref'},
                                 'eta_positive': {'nullable': False,
                                                  'ref': 'UnitFloat',
                                                  'required': True,
                                                  'type': 'ref'},
                                 'evidence_rule_sha256': {'nullable': False,
                                                          'ref': 'SHA',
                                                          'required': True,
                                                          'type': 'ref'},
                                 'initial': {'nullable': False,
                                             'ref': 'UnitFloat',
                                             'required': True,
                                             'type': 'ref'},
                                 'mode': {'const': 'scalar',
                                          'nullable': False,
                                          'required': True,
                                          'type': 'string'},
                                 'negative_policy': {'const': 'only_memory_attributable_independent',
                                                     'nullable': False,
                                                     'required': True,
                                                     'type': 'string'},
                                 'outcome_jump_enabled': {'nullable': False,
                                                          'ref': 'Bool',
                                                          'required': True,
                                                          'type': 'ref'},
                                 'protocol': {'const': 'dynamics-config-v2',
                                              'nullable': False,
                                              'required': True,
                                              'type': 'string'},
                                 'rtol': {'nullable': False,
                                          'ref': 'Tolerance',
                                          'required': True,
                                          'type': 'ref'},
                                 'tau_ns': {'nullable': False,
                                            'ref': 'PositiveNS',
                                            'required': True,
                                            'type': 'ref'},
                                 'unit_strength': {'nullable': False,
                                                   'ref': 'UnitFloat',
                                                   'required': True,
                                                   'type': 'ref'},
                                 'version': {'nullable': False,
                                             'ref': 'LegacyID',
                                             'required': True,
                                             'type': 'ref'}},
                      'kind': 'closed_object',
                      'required': ['protocol',
                                   'version',
                                   'mode',
                                   'clock',
                                   'clock_domain',
                                   'initial',
                                   'tau_ns',
                                   'eta_positive',
                                   'eta_negative',
                                   'unit_strength',
                                   'negative_policy',
                                   'decay_enabled',
                                   'outcome_jump_enabled',
                                   'atol',
                                   'rtol',
                                   'evidence_rule_sha256',
                                   'config_sha256']},
 'PolicyDecisionV2': {'additional_fields': False,
                      'fields': {'candidate_snapshot_sha256': {'nullable': False,
                                                               'ref': 'SHA',
                                                               'required': True,
                                                               'type': 'ref'},
                                 'context_budget': {'nullable': False,
                                                    'ref': 'LegacyContextBudget',
                                                    'required': True,
                                                    'type': 'ref'},
                                 'context_cost_used': {'nullable': False,
                                                       'ref': 'Count',
                                                       'required': True,
                                                       'type': 'ref'},
                                 'decision_sha256': {'nullable': False,
                                                     'ref': 'SHA',
                                                     'required': True,
                                                     'type': 'ref'},
                                 'policy_id': {'nullable': False,
                                               'ref': 'PolicyID',
                                               'required': True,
                                               'type': 'ref'},
                                 'policy_version': {'nullable': False,
                                                    'ref': 'LegacyID',
                                                    'required': True,
                                                    'type': 'ref'},
                                 'prefix_sha256': {'nullable': False,
                                                   'ref': 'SHA',
                                                   'required': True,
                                                   'type': 'ref'},
                                 'query_id': {'nullable': False,
                                              'ref': 'LegacyID',
                                              'required': True,
                                              'type': 'ref'},
                                 'ranker_config_sha256': {'nullable': False,
                                                          'ref': 'SHA',
                                                          'required': True,
                                                          'type': 'ref'},
                                 'ranks': {'items': {'nullable': False, 'ref': 'RankRow', 'type': 'ref'},
                                           'max_items': 'selected ResourceLimits.max_array_items with '
                                                        'field-specific limits applied additionally',
                                           'nullable': False,
                                           'required': True,
                                           'type': 'array'},
                                 'reason': {'nullable': False,
                                            'ref': 'Reason',
                                            'required': True,
                                            'type': 'ref'},
                                 'selected': {'items': {'nullable': False, 'ref': 'RefV2', 'type': 'ref'},
                                              'max_items': 'selected ResourceLimits.max_array_items with '
                                                           'field-specific limits applied additionally',
                                              'nullable': False,
                                              'required': True,
                                              'type': 'array'},
                                 'status': {'enum': ['ok', 'fallback'],
                                            'nullable': False,
                                            'required': True,
                                            'type': 'string'}},
                      'kind': 'closed_object',
                      'required': ['query_id',
                                   'policy_id',
                                   'policy_version',
                                   'candidate_snapshot_sha256',
                                   'prefix_sha256',
                                   'ranker_config_sha256',
                                   'ranks',
                                   'selected',
                                   'context_cost_used',
                                   'context_budget',
                                   'status',
                                   'reason',
                                   'decision_sha256']},
 'PolicyRegistryV2': {'additional_fields': False,
                      'fields': {'entries': {'items': {'nullable': False,
                                                       'ref': 'PolicyEntry',
                                                       'type': 'ref'},
                                             'max_items': 'selected ResourceLimits.max_array_items with '
                                                          'field-specific limits applied additionally',
                                             'nullable': False,
                                             'required': True,
                                             'type': 'array'},
                                 'protocol': {'const': 'dynamics-policy-registry-v2',
                                              'nullable': False,
                                              'required': True,
                                              'type': 'string'},
                                 'registry_sha256': {'nullable': False,
                                                     'ref': 'SHA',
                                                     'required': True,
                                                     'type': 'ref'},
                                 'resource_limits': {'nullable': False,
                                                     'ref': 'ResourceLimits',
                                                     'required': True,
                                                     'type': 'ref'}},
                      'kind': 'closed_object',
                      'required': ['protocol', 'entries', 'resource_limits', 'registry_sha256']},
 'PredictionRunV2': {'additional_fields': False,
                     'fields': {'cost': {'nullable': False,
                                         'ref': 'OfflineCost',
                                         'required': True,
                                         'type': 'ref'},
                                'input_manifest_sha256': {'nullable': False,
                                                          'ref': 'SHA',
                                                          'required': True,
                                                          'type': 'ref'},
                                'policy_id': {'nullable': False,
                                              'ref': 'PolicyID',
                                              'required': True,
                                              'type': 'ref'},
                                'policy_version': {'nullable': False,
                                                   'ref': 'LegacyID',
                                                   'required': True,
                                                   'type': 'ref'},
                                'predictions': {'items': {'nullable': False,
                                                          'ref': 'PredictionV2',
                                                          'type': 'ref'},
                                                'max_items': 'selected ResourceLimits.max_array_items with '
                                                             'field-specific limits applied additionally',
                                                'nullable': False,
                                                'required': True,
                                                'type': 'array'},
                                'protocol': {'const': 'dynamics-prediction-run-v2',
                                             'nullable': False,
                                             'required': True,
                                             'type': 'string'},
                                'registry_sha256': {'nullable': False,
                                                    'ref': 'SHA',
                                                    'required': True,
                                                    'type': 'ref'},
                                'run_config_sha256': {'nullable': False,
                                                      'ref': 'SHA',
                                                      'required': True,
                                                      'type': 'ref'},
                                'run_id': {'nullable': False,
                                           'ref': 'LegacyID',
                                           'required': True,
                                           'type': 'ref'},
                                'run_sha256': {'nullable': False,
                                               'ref': 'SHA',
                                               'required': True,
                                               'type': 'ref'}},
                     'kind': 'closed_object',
                     'required': ['protocol',
                                  'run_id',
                                  'policy_id',
                                  'policy_version',
                                  'input_manifest_sha256',
                                  'run_config_sha256',
                                  'registry_sha256',
                                  'predictions',
                                  'cost',
                                  'run_sha256']},
 'PredictionV2': {'additional_fields': False,
                  'fields': {'decision': {'nullable': True,
                                          'ref': 'PolicyDecisionV2',
                                          'required': True,
                                          'type': 'ref'},
                             'error': {'nullable': True,
                                       'ref': 'ContractErrorV2',
                                       'required': True,
                                       'type': 'ref'},
                             'query_id': {'nullable': False,
                                          'ref': 'LegacyID',
                                          'required': True,
                                          'type': 'ref'},
                             'reason': {'nullable': False, 'ref': 'Reason', 'required': True, 'type': 'ref'},
                             'status': {'enum': ['ok', 'fallback', 'failed'],
                                        'nullable': False,
                                        'required': True,
                                        'type': 'string'}},
                  'kind': 'closed_object',
                  'required': ['query_id', 'status', 'decision', 'error', 'reason']},
 'RankerConfigV2': {'additional_fields': False,
                    'fields': {'ablation': {'enum': ['none', 'disable_decay', 'disable_outcome_jumps'],
                                            'nullable': False,
                                            'required': True,
                                            'type': 'string'},
                               'atol': {'nullable': False,
                                        'ref': 'Tolerance',
                                        'required': True,
                                        'type': 'ref'},
                               'base_equal_rank': {'const': 'exact_score_dense',
                                                   'nullable': False,
                                                   'required': True,
                                                   'type': 'string'},
                               'config_sha256': {'nullable': False,
                                                 'ref': 'SHA',
                                                 'required': True,
                                                 'type': 'ref'},
                               'dynamics_config_sha256': {'nullable': True,
                                                          'ref': 'SHA',
                                                          'required': True,
                                                          'type': 'ref'},
                               'fallback': {'const': 'error',
                                            'nullable': False,
                                            'required': True,
                                            'type': 'string'},
                               'feature_contract_sha256': {'nullable': True,
                                                           'ref': 'SHA',
                                                           'required': True,
                                                           'type': 'ref'},
                               'feature_equal_rank': {'const': 'exact_value_dense',
                                                      'nullable': False,
                                                      'required': True,
                                                      'type': 'string'},
                               'final_tie': {'const': 'trust_updated_key',
                                             'nullable': False,
                                             'required': True,
                                             'type': 'string'},
                               'inherited_b2_config_sha256': {'hash_domain': 'selected B2 '
                                                                             'RankerParametersProjection '
                                                                             'digest '
                                                                             '(RankerConfigV2.config_sha256)',
                                                              'nullable': True,
                                                              'ref': 'SHA',
                                                              'required': True,
                                                              'type': 'ref'},
                               'lanes': {'items': {'nullable': False, 'ref': 'RankLane', 'type': 'ref'},
                                         'max_items': 'selected ResourceLimits.max_array_items with '
                                                      'field-specific limits applied additionally',
                                         'nullable': False,
                                         'required': True,
                                         'type': 'array'},
                               'missing_feature': {'const': 'error',
                                                   'nullable': False,
                                                   'required': True,
                                                   'type': 'string'},
                               'missing_state': {'const': 'error',
                                                 'nullable': False,
                                                 'required': True,
                                                 'type': 'string'},
                               'outer_offset': {'const': 60,
                                                'nullable': False,
                                                'required': True,
                                                'type': 'int64'},
                               'policy_id': {'nullable': False,
                                             'ref': 'PolicyID',
                                             'required': True,
                                             'type': 'ref'},
                               'policy_version': {'nullable': False,
                                                  'ref': 'LegacyID',
                                                  'required': True,
                                                  'type': 'ref'},
                               'recency_anchor': {'const': 'updated_at_ns',
                                                  'nullable': False,
                                                  'required': True,
                                                  'type': 'string'},
                               'recency_tau_ns': {'nullable': False,
                                                  'ref': 'PositiveNS',
                                                  'required': True,
                                                  'type': 'ref'},
                               'registry_sha256': {'nullable': False,
                                                   'ref': 'SHA',
                                                   'required': True,
                                                   'type': 'ref'},
                               'rtol': {'nullable': False,
                                        'ref': 'Tolerance',
                                        'required': True,
                                        'type': 'ref'}},
                    'kind': 'closed_object',
                    'required': ['policy_id',
                                 'policy_version',
                                 'registry_sha256',
                                 'dynamics_config_sha256',
                                 'feature_contract_sha256',
                                 'outer_offset',
                                 'lanes',
                                 'missing_state',
                                 'missing_feature',
                                 'recency_anchor',
                                 'recency_tau_ns',
                                 'base_equal_rank',
                                 'feature_equal_rank',
                                 'final_tie',
                                 'fallback',
                                 'atol',
                                 'rtol',
                                 'inherited_b2_config_sha256',
                                 'ablation',
                                 'config_sha256']},
 'RefV2': {'additional_fields': False,
           'fields': {'collection': {'const': 'memories',
                                     'nullable': False,
                                     'required': True,
                                     'type': 'string'},
                      'content_sha256': {'nullable': False, 'ref': 'SHA', 'required': True, 'type': 'ref'},
                      'id': {'nullable': False, 'ref': 'LegacyID', 'required': True, 'type': 'ref'},
                      'origin': {'enum': ['memory', 'fixture_alt'],
                                 'nullable': False,
                                 'required': True,
                                 'type': 'string'},
                      'revision': {'nullable': False, 'ref': 'Seq', 'required': True, 'type': 'ref'}},
           'kind': 'closed_object',
           'required': ['origin', 'collection', 'id', 'revision', 'content_sha256']},
 'ScopeV2': {'additional_fields': False,
             'fields': {'agent': {'nullable': False, 'ref': 'LegacyID', 'required': True, 'type': 'ref'},
                        'project': {'nullable': False, 'ref': 'LegacyID', 'required': True, 'type': 'ref'},
                        'user': {'nullable': False, 'ref': 'LegacyID', 'required': True, 'type': 'ref'},
                        'workspace': {'nullable': False, 'ref': 'LegacyID', 'required': True, 'type': 'ref'}},
             'kind': 'closed_object',
             'required': ['workspace', 'project', 'user', 'agent']},
 'AuxRank': {'additional_fields': False,
             'fields': {'dense_rank': {'nullable': False, 'ref': 'Seq', 'required': True, 'type': 'ref'},
                        'kind': {'enum': ['recency', 'state', 'importance'],
                                 'nullable': False,
                                 'required': True,
                                 'type': 'string'},
                        'value': {'nullable': False, 'ref': 'UnitFloat', 'required': True, 'type': 'ref'}},
             'kind': 'closed_object',
             'required': ['kind', 'value', 'dense_rank']},
 'CompareResult': {'additional_fields': False,
                   'fields': {'checks': {'items': {'nullable': False,
                                                   'ref': 'ComparisonCheck',
                                                   'type': 'ref'},
                                         'max_items': 'selected ResourceLimits.max_array_items with '
                                                      'field-specific limits applied additionally',
                                         'nullable': False,
                                         'required': True,
                                         'type': 'array'},
                              'equal_conditions': {'nullable': False,
                                                   'ref': 'Bool',
                                                   'required': True,
                                                   'type': 'ref'},
                              'protocol': {'const': 'dynamics-compare-v2',
                                           'nullable': False,
                                           'required': True,
                                           'type': 'string'},
                              'registry_sha256': {'nullable': False,
                                                  'ref': 'SHA',
                                                  'required': True,
                                                  'type': 'ref'},
                              'run_ids': {'items': {'nullable': False, 'ref': 'LegacyID', 'type': 'ref'},
                                          'max_items': 'selected ResourceLimits.max_array_items with '
                                                       'field-specific limits applied additionally',
                                          'nullable': False,
                                          'required': True,
                                          'type': 'array'},
                              'statuses': {'items': {'nullable': False,
                                                     'ref': 'ComparedStatus',
                                                     'type': 'ref'},
                                           'max_items': 'selected ResourceLimits.max_array_items with '
                                                        'field-specific limits applied additionally',
                                           'nullable': False,
                                           'required': True,
                                           'type': 'array'}},
                   'kind': 'closed_object',
                   'required': ['protocol',
                                'run_ids',
                                'registry_sha256',
                                'equal_conditions',
                                'checks',
                                'statuses']},
 'ComparedStatus': {'additional_fields': False,
                    'fields': {'failed': {'nullable': False, 'ref': 'Count', 'required': True, 'type': 'ref'},
                               'fallback': {'nullable': False,
                                            'ref': 'Count',
                                            'required': True,
                                            'type': 'ref'},
                               'ok': {'nullable': False, 'ref': 'Count', 'required': True, 'type': 'ref'},
                               'planned': {'nullable': False,
                                           'ref': 'Count',
                                           'required': True,
                                           'type': 'ref'},
                               'policy_id': {'nullable': False,
                                             'ref': 'PolicyID',
                                             'required': True,
                                             'type': 'ref'},
                               'run_id': {'nullable': False,
                                          'ref': 'LegacyID',
                                          'required': True,
                                          'type': 'ref'}},
                    'kind': 'closed_object',
                    'required': ['run_id', 'policy_id', 'planned', 'ok', 'failed', 'fallback']},
 'ComparisonCheck': {'additional_fields': False,
                     'fields': {'equal': {'nullable': False, 'ref': 'Bool', 'required': True, 'type': 'ref'},
                                'name': {'enum': ['candidates',
                                                  'events',
                                                  'scope',
                                                  'clock',
                                                  'cutoff',
                                                  'budget',
                                                  'registry',
                                                  'features',
                                                  'source_snapshot'],
                                         'nullable': False,
                                         'required': True,
                                         'type': 'string'},
                                'reason': {'nullable': False,
                                           'ref': 'Reason',
                                           'required': True,
                                           'type': 'ref'}},
                     'kind': 'closed_object',
                     'required': ['name', 'equal', 'reason']},
 'OfflineCost': {'additional_fields': False,
                 'fields': {'duplicate_events': {'nullable': False,
                                                 'ref': 'Count',
                                                 'required': True,
                                                 'type': 'ref'},
                            'duplicate_evidence': {'nullable': False,
                                                   'ref': 'Count',
                                                   'required': True,
                                                   'type': 'ref'},
                            'elapsed_wall_ns': {'nullable': True,
                                                'ref': 'NS',
                                                'required': True,
                                                'type': 'ref'},
                            'event_inputs': {'nullable': False,
                                             'ref': 'Count',
                                             'required': True,
                                             'type': 'ref'},
                            'failed_predictions': {'nullable': False,
                                                   'ref': 'Count',
                                                   'required': True,
                                                   'type': 'ref'},
                            'fallback_predictions': {'nullable': False,
                                                     'ref': 'Count',
                                                     'required': True,
                                                     'type': 'ref'},
                            'feature_reads': {'nullable': False,
                                              'ref': 'Count',
                                              'required': True,
                                              'type': 'ref'},
                            'provider_usd': {'nullable': True, 'required': True, 'type': 'null'},
                            'state_updates': {'nullable': False,
                                              'ref': 'Count',
                                              'required': True,
                                              'type': 'ref'},
                            'unique_events': {'nullable': False,
                                              'ref': 'Count',
                                              'required': True,
                                              'type': 'ref'}},
                 'kind': 'closed_object',
                 'required': ['event_inputs',
                              'unique_events',
                              'state_updates',
                              'duplicate_events',
                              'duplicate_evidence',
                              'feature_reads',
                              'failed_predictions',
                              'fallback_predictions',
                              'elapsed_wall_ns',
                              'provider_usd']},
 'PolicyEntry': {'additional_fields': False,
                 'fields': {'allowed_roles': {'items': {'nullable': False, 'ref': 'InputRole', 'type': 'ref'},
                                              'max_items': 'selected ResourceLimits.max_array_items with '
                                                           'field-specific limits applied additionally',
                                              'nullable': False,
                                              'required': True,
                                              'type': 'array'},
                            'dynamics': {'nullable': True,
                                         'ref': 'DynamicsConfigV2',
                                         'required': True,
                                         'type': 'ref'},
                            'implemented_mode': {'enum': ['none', 'scalar'],
                                                 'nullable': False,
                                                 'required': True,
                                                 'type': 'string'},
                            'parameter_provenance': {'const': 'engineering_regression_only',
                                                     'nullable': False,
                                                     'required': True,
                                                     'type': 'string'},
                            'policy_id': {'nullable': False,
                                          'ref': 'PolicyID',
                                          'required': True,
                                          'type': 'ref'},
                            'ranker': {'nullable': False,
                                       'ref': 'RankerConfigV2',
                                       'required': True,
                                       'type': 'ref'},
                            'rule_sha256': {'nullable': False, 'ref': 'SHA', 'required': True, 'type': 'ref'},
                            'uses_elapsed_decay': {'nullable': False,
                                                   'ref': 'Bool',
                                                   'required': True,
                                                   'type': 'ref'},
                            'uses_outcomes': {'nullable': False,
                                              'ref': 'Bool',
                                              'required': True,
                                              'type': 'ref'},
                            'version': {'nullable': False,
                                        'ref': 'LegacyID',
                                        'required': True,
                                        'type': 'ref'}},
                 'kind': 'closed_object',
                 'required': ['policy_id',
                              'version',
                              'implemented_mode',
                              'uses_outcomes',
                              'uses_elapsed_decay',
                              'allowed_roles',
                              'dynamics',
                              'ranker',
                              'rule_sha256',
                              'parameter_provenance']},
 'RankLane': {'additional_fields': False,
              'fields': {'kind': {'enum': ['relevance', 'recency', 'state', 'importance'],
                                  'nullable': False,
                                  'required': True,
                                  'type': 'string'},
                         'weight': {'nullable': False, 'ref': 'UnitFloat', 'required': True, 'type': 'ref'}},
              'kind': 'closed_object',
              'required': ['kind', 'weight']},
 'RankRow': {'additional_fields': False,
             'fields': {'auxiliary': {'items': {'nullable': False, 'ref': 'AuxRank', 'type': 'ref'},
                                      'max_items': 'selected ResourceLimits.max_array_items with '
                                                   'field-specific limits applied additionally',
                                      'nullable': False,
                                      'required': True,
                                      'type': 'array'},
                        'base_dense_rank': {'nullable': False, 'ref': 'Seq', 'required': True, 'type': 'ref'},
                        'base_rrf_score': {'nullable': False,
                                           'ref': 'NonnegativeFloat',
                                           'required': True,
                                           'type': 'ref'},
                        'final_ordinal': {'nullable': False, 'ref': 'Seq', 'required': True, 'type': 'ref'},
                        'fusion_score': {'nullable': False,
                                         'ref': 'NonnegativeFloat',
                                         'required': True,
                                         'type': 'ref'},
                        'ref': {'nullable': False, 'ref': 'RefV2', 'required': True, 'type': 'ref'}},
             'kind': 'closed_object',
             'required': ['ref',
                          'base_rrf_score',
                          'base_dense_rank',
                          'auxiliary',
                          'fusion_score',
                          'final_ordinal']},
 'ResourceLimits': {'additional_fields': False,
                    'fields': {'max_array_items': {'nullable': False,
                                                   'ref': 'PositiveInt64',
                                                   'required': True,
                                                   'type': 'ref'},
                               'max_events': {'nullable': False,
                                              'ref': 'PositiveInt64',
                                              'required': True,
                                              'type': 'ref'},
                               'max_features': {'nullable': False,
                                                'ref': 'PositiveInt64',
                                                'required': True,
                                                'type': 'ref'},
                               'max_file_bytes': {'nullable': False,
                                                  'ref': 'PositiveInt64',
                                                  'required': True,
                                                  'type': 'ref'},
                               'max_json_depth': {'nullable': False,
                                                  'ref': 'PositiveInt64',
                                                  'required': True,
                                                  'type': 'ref'},
                               'max_probes': {'nullable': False,
                                              'ref': 'PositiveInt64',
                                              'required': True,
                                              'type': 'ref'},
                               'max_receipts': {'nullable': False,
                                                'ref': 'PositiveInt64',
                                                'required': True,
                                                'type': 'ref'},
                               'max_states': {'nullable': False,
                                              'ref': 'PositiveInt64',
                                              'required': True,
                                              'type': 'ref'},
                               'max_string_bytes': {'nullable': False,
                                                    'ref': 'PositiveInt64',
                                                    'required': True,
                                                    'type': 'ref'}},
                    'kind': 'closed_object',
                    'required': ['max_file_bytes',
                                 'max_events',
                                 'max_states',
                                 'max_receipts',
                                 'max_probes',
                                 'max_features',
                                 'max_array_items',
                                 'max_string_bytes',
                                 'max_json_depth']},
 'DynamicsConfigV2HashProjection': {'additional_fields': False,
                                    'excluded_fields': ['config_sha256'],
                                    'fields': {'atol': {'nullable': False,
                                                        'ref': 'Tolerance',
                                                        'required': True,
                                                        'type': 'ref'},
                                               'clock': {'const': 'accepted_time',
                                                         'nullable': False,
                                                         'required': True,
                                                         'type': 'string'},
                                               'clock_domain': {'nullable': False,
                                                                'ref': 'LegacyID',
                                                                'required': True,
                                                                'type': 'ref'},
                                               'decay_enabled': {'nullable': False,
                                                                 'ref': 'Bool',
                                                                 'required': True,
                                                                 'type': 'ref'},
                                               'eta_negative': {'nullable': False,
                                                                'ref': 'UnitFloat',
                                                                'required': True,
                                                                'type': 'ref'},
                                               'eta_positive': {'nullable': False,
                                                                'ref': 'UnitFloat',
                                                                'required': True,
                                                                'type': 'ref'},
                                               'evidence_rule_sha256': {'nullable': False,
                                                                        'ref': 'SHA',
                                                                        'required': True,
                                                                        'type': 'ref'},
                                               'initial': {'nullable': False,
                                                           'ref': 'UnitFloat',
                                                           'required': True,
                                                           'type': 'ref'},
                                               'mode': {'const': 'scalar',
                                                        'nullable': False,
                                                        'required': True,
                                                        'type': 'string'},
                                               'negative_policy': {'const': 'only_memory_attributable_independent',
                                                                   'nullable': False,
                                                                   'required': True,
                                                                   'type': 'string'},
                                               'outcome_jump_enabled': {'nullable': False,
                                                                        'ref': 'Bool',
                                                                        'required': True,
                                                                        'type': 'ref'},
                                               'protocol': {'const': 'dynamics-config-v2',
                                                            'nullable': False,
                                                            'required': True,
                                                            'type': 'string'},
                                               'rtol': {'nullable': False,
                                                        'ref': 'Tolerance',
                                                        'required': True,
                                                        'type': 'ref'},
                                               'tau_ns': {'nullable': False,
                                                          'ref': 'PositiveNS',
                                                          'required': True,
                                                          'type': 'ref'},
                                               'unit_strength': {'nullable': False,
                                                                 'ref': 'UnitFloat',
                                                                 'required': True,
                                                                 'type': 'ref'},
                                               'version': {'nullable': False,
                                                           'ref': 'LegacyID',
                                                           'required': True,
                                                           'type': 'ref'}},
                                    'hash_type_name': 'DynamicsConfigV2HashProjection',
                                    'kind': 'closed_typed_projection',
                                    'meaning': '원selfhashfield하나만제외; nestedhash들은포함. '
                                               'explicitfieldmap이encoding source',
                                    'required': ['protocol',
                                                 'version',
                                                 'mode',
                                                 'clock',
                                                 'clock_domain',
                                                 'initial',
                                                 'tau_ns',
                                                 'eta_positive',
                                                 'eta_negative',
                                                 'unit_strength',
                                                 'negative_policy',
                                                 'decay_enabled',
                                                 'outcome_jump_enabled',
                                                 'atol',
                                                 'rtol',
                                                 'evidence_rule_sha256'],
                                    'source_type': 'DynamicsConfigV2'},
 'PolicyDecisionV2HashProjection': {'additional_fields': False,
                                    'excluded_fields': ['decision_sha256'],
                                    'fields': {'candidate_snapshot_sha256': {'nullable': False,
                                                                             'ref': 'SHA',
                                                                             'required': True,
                                                                             'type': 'ref'},
                                               'context_budget': {'nullable': False,
                                                                  'ref': 'LegacyContextBudget',
                                                                  'required': True,
                                                                  'type': 'ref'},
                                               'context_cost_used': {'nullable': False,
                                                                     'ref': 'Count',
                                                                     'required': True,
                                                                     'type': 'ref'},
                                               'policy_id': {'nullable': False,
                                                             'ref': 'PolicyID',
                                                             'required': True,
                                                             'type': 'ref'},
                                               'policy_version': {'nullable': False,
                                                                  'ref': 'LegacyID',
                                                                  'required': True,
                                                                  'type': 'ref'},
                                               'prefix_sha256': {'nullable': False,
                                                                 'ref': 'SHA',
                                                                 'required': True,
                                                                 'type': 'ref'},
                                               'query_id': {'nullable': False,
                                                            'ref': 'LegacyID',
                                                            'required': True,
                                                            'type': 'ref'},
                                               'ranker_config_sha256': {'nullable': False,
                                                                        'ref': 'SHA',
                                                                        'required': True,
                                                                        'type': 'ref'},
                                               'ranks': {'items': {'nullable': False,
                                                                   'ref': 'RankRow',
                                                                   'type': 'ref'},
                                                         'max_items': 'selected '
                                                                      'ResourceLimits.max_array_items with '
                                                                      'field-specific limits applied '
                                                                      'additionally',
                                                         'nullable': False,
                                                         'required': True,
                                                         'type': 'array'},
                                               'reason': {'nullable': False,
                                                          'ref': 'Reason',
                                                          'required': True,
                                                          'type': 'ref'},
                                               'selected': {'items': {'nullable': False,
                                                                      'ref': 'RefV2',
                                                                      'type': 'ref'},
                                                            'max_items': 'selected '
                                                                         'ResourceLimits.max_array_items '
                                                                         'with field-specific limits applied '
                                                                         'additionally',
                                                            'nullable': False,
                                                            'required': True,
                                                            'type': 'array'},
                                               'status': {'enum': ['ok', 'fallback'],
                                                          'nullable': False,
                                                          'required': True,
                                                          'type': 'string'}},
                                    'hash_type_name': 'PolicyDecisionV2HashProjection',
                                    'kind': 'closed_typed_projection',
                                    'meaning': '원selfhashfield하나만제외; nestedhash들은포함. '
                                               'explicitfieldmap이encoding source',
                                    'required': ['query_id',
                                                 'policy_id',
                                                 'policy_version',
                                                 'candidate_snapshot_sha256',
                                                 'prefix_sha256',
                                                 'ranker_config_sha256',
                                                 'ranks',
                                                 'selected',
                                                 'context_cost_used',
                                                 'context_budget',
                                                 'status',
                                                 'reason'],
                                    'source_type': 'PolicyDecisionV2'},
 'PolicyRegistryGraphProjectionV2': {'additional_fields': False,
                                     'excluded_fields': ['registry_sha256',
                                                         'entries[*].ranker.registry_sha256'],
                                     'fields': {'entries': {'items': {'nullable': False,
                                                                      'ref': 'RegistryPolicyEntryProjectionV2',
                                                                      'type': 'projection_ref'},
                                                            'max_items': 'selected '
                                                                         'ResourceLimits.max_array_items '
                                                                         'with field-specific limits applied '
                                                                         'additionally',
                                                            'nullable': False,
                                                            'required': True,
                                                            'type': 'array'},
                                                'protocol': {'const': 'dynamics-policy-registry-v2',
                                                             'nullable': False,
                                                             'required': True,
                                                             'type': 'string'},
                                                'resource_limits': {'nullable': False,
                                                                    'ref': 'ResourceLimits',
                                                                    'required': True,
                                                                    'type': 'ref'}},
                                     'hash_type_name': 'PolicyRegistryGraphProjectionV2',
                                     'kind': 'closed_typed_projection',
                                     'meaning': 'root U12 graph projection: fullnestedconfigsha 포함; registry '
                                                'associations만제외',
                                     'required': ['protocol', 'entries', 'resource_limits'],
                                     'source_type': 'PolicyRegistryV2'},
 'PredictionRunV2HashProjection': {'additional_fields': False,
                                   'excluded_fields': ['run_sha256'],
                                   'fields': {'cost': {'nullable': False,
                                                       'ref': 'OfflineCost',
                                                       'required': True,
                                                       'type': 'ref'},
                                              'input_manifest_sha256': {'nullable': False,
                                                                        'ref': 'SHA',
                                                                        'required': True,
                                                                        'type': 'ref'},
                                              'policy_id': {'nullable': False,
                                                            'ref': 'PolicyID',
                                                            'required': True,
                                                            'type': 'ref'},
                                              'policy_version': {'nullable': False,
                                                                 'ref': 'LegacyID',
                                                                 'required': True,
                                                                 'type': 'ref'},
                                              'predictions': {'items': {'nullable': False,
                                                                        'ref': 'PredictionV2',
                                                                        'type': 'ref'},
                                                              'max_items': 'selected '
                                                                           'ResourceLimits.max_array_items '
                                                                           'with field-specific limits '
                                                                           'applied additionally',
                                                              'nullable': False,
                                                              'required': True,
                                                              'type': 'array'},
                                              'protocol': {'const': 'dynamics-prediction-run-v2',
                                                           'nullable': False,
                                                           'required': True,
                                                           'type': 'string'},
                                              'registry_sha256': {'nullable': False,
                                                                  'ref': 'SHA',
                                                                  'required': True,
                                                                  'type': 'ref'},
                                              'run_config_sha256': {'nullable': False,
                                                                    'ref': 'SHA',
                                                                    'required': True,
                                                                    'type': 'ref'},
                                              'run_id': {'nullable': False,
                                                         'ref': 'LegacyID',
                                                         'required': True,
                                                         'type': 'ref'}},
                                   'hash_type_name': 'PredictionRunV2HashProjection',
                                   'kind': 'closed_typed_projection',
                                   'meaning': '원selfhashfield하나만제외; nestedhash들은포함. '
                                              'explicitfieldmap이encoding source',
                                   'required': ['protocol',
                                                'run_id',
                                                'policy_id',
                                                'policy_version',
                                                'input_manifest_sha256',
                                                'run_config_sha256',
                                                'registry_sha256',
                                                'predictions',
                                                'cost'],
                                   'source_type': 'PredictionRunV2'},
 'RankerConfigParametersProjectionV2': {'additional_fields': False,
                                        'excluded_fields': ['config_sha256', 'registry_sha256'],
                                        'fields': {'ablation': {'enum': ['none',
                                                                         'disable_decay',
                                                                         'disable_outcome_jumps'],
                                                                'nullable': False,
                                                                'required': True,
                                                                'type': 'string'},
                                                   'atol': {'nullable': False,
                                                            'ref': 'Tolerance',
                                                            'required': True,
                                                            'type': 'ref'},
                                                   'base_equal_rank': {'const': 'exact_score_dense',
                                                                       'nullable': False,
                                                                       'required': True,
                                                                       'type': 'string'},
                                                   'dynamics_config_sha256': {'nullable': True,
                                                                              'ref': 'SHA',
                                                                              'required': True,
                                                                              'type': 'ref'},
                                                   'fallback': {'const': 'error',
                                                                'nullable': False,
                                                                'required': True,
                                                                'type': 'string'},
                                                   'feature_contract_sha256': {'nullable': True,
                                                                               'ref': 'SHA',
                                                                               'required': True,
                                                                               'type': 'ref'},
                                                   'feature_equal_rank': {'const': 'exact_value_dense',
                                                                          'nullable': False,
                                                                          'required': True,
                                                                          'type': 'string'},
                                                   'final_tie': {'const': 'trust_updated_key',
                                                                 'nullable': False,
                                                                 'required': True,
                                                                 'type': 'string'},
                                                   'inherited_b2_config_sha256': {'nullable': True,
                                                                                  'ref': 'SHA',
                                                                                  'required': True,
                                                                                  'type': 'ref'},
                                                   'lanes': {'items': {'nullable': False,
                                                                       'ref': 'RankLane',
                                                                       'type': 'ref'},
                                                             'max_items': 'selected '
                                                                          'ResourceLimits.max_array_items '
                                                                          'with field-specific limits '
                                                                          'applied additionally',
                                                             'nullable': False,
                                                             'required': True,
                                                             'type': 'array'},
                                                   'missing_feature': {'const': 'error',
                                                                       'nullable': False,
                                                                       'required': True,
                                                                       'type': 'string'},
                                                   'missing_state': {'const': 'error',
                                                                     'nullable': False,
                                                                     'required': True,
                                                                     'type': 'string'},
                                                   'outer_offset': {'const': 60,
                                                                    'nullable': False,
                                                                    'required': True,
                                                                    'type': 'int64'},
                                                   'policy_id': {'nullable': False,
                                                                 'ref': 'PolicyID',
                                                                 'required': True,
                                                                 'type': 'ref'},
                                                   'policy_version': {'nullable': False,
                                                                      'ref': 'LegacyID',
                                                                      'required': True,
                                                                      'type': 'ref'},
                                                   'recency_anchor': {'const': 'updated_at_ns',
                                                                      'nullable': False,
                                                                      'required': True,
                                                                      'type': 'string'},
                                                   'recency_tau_ns': {'nullable': False,
                                                                      'ref': 'PositiveNS',
                                                                      'required': True,
                                                                      'type': 'ref'},
                                                   'rtol': {'nullable': False,
                                                            'ref': 'Tolerance',
                                                            'required': True,
                                                            'type': 'ref'}},
                                        'hash_type_name': 'RankerConfigParametersProjectionV2',
                                        'kind': 'closed_typed_projection',
                                        'meaning': 'root U12 selected parameters-only hash; '
                                                   'registryassociation불포함',
                                        'required': ['policy_id',
                                                     'policy_version',
                                                     'dynamics_config_sha256',
                                                     'feature_contract_sha256',
                                                     'outer_offset',
                                                     'lanes',
                                                     'missing_state',
                                                     'missing_feature',
                                                     'recency_anchor',
                                                     'recency_tau_ns',
                                                     'base_equal_rank',
                                                     'feature_equal_rank',
                                                     'final_tie',
                                                     'fallback',
                                                     'atol',
                                                     'rtol',
                                                     'inherited_b2_config_sha256',
                                                     'ablation'],
                                        'source_type': 'RankerConfigV2'},
 'RegistryBoundRankerProjectionV2': {'additional_fields': False,
                                     'excluded_fields': ['registry_sha256'],
                                     'fields': {'ablation': {'enum': ['none',
                                                                      'disable_decay',
                                                                      'disable_outcome_jumps'],
                                                             'nullable': False,
                                                             'required': True,
                                                             'type': 'string'},
                                                'atol': {'nullable': False,
                                                         'ref': 'Tolerance',
                                                         'required': True,
                                                         'type': 'ref'},
                                                'base_equal_rank': {'const': 'exact_score_dense',
                                                                    'nullable': False,
                                                                    'required': True,
                                                                    'type': 'string'},
                                                'config_sha256': {'nullable': False,
                                                                  'ref': 'SHA',
                                                                  'required': True,
                                                                  'type': 'ref'},
                                                'dynamics_config_sha256': {'nullable': True,
                                                                           'ref': 'SHA',
                                                                           'required': True,
                                                                           'type': 'ref'},
                                                'fallback': {'const': 'error',
                                                             'nullable': False,
                                                             'required': True,
                                                             'type': 'string'},
                                                'feature_contract_sha256': {'nullable': True,
                                                                            'ref': 'SHA',
                                                                            'required': True,
                                                                            'type': 'ref'},
                                                'feature_equal_rank': {'const': 'exact_value_dense',
                                                                       'nullable': False,
                                                                       'required': True,
                                                                       'type': 'string'},
                                                'final_tie': {'const': 'trust_updated_key',
                                                              'nullable': False,
                                                              'required': True,
                                                              'type': 'string'},
                                                'inherited_b2_config_sha256': {'nullable': True,
                                                                               'ref': 'SHA',
                                                                               'required': True,
                                                                               'type': 'ref'},
                                                'lanes': {'items': {'nullable': False,
                                                                    'ref': 'RankLane',
                                                                    'type': 'ref'},
                                                          'max_items': 'selected '
                                                                       'ResourceLimits.max_array_items with '
                                                                       'field-specific limits applied '
                                                                       'additionally',
                                                          'nullable': False,
                                                          'required': True,
                                                          'type': 'array'},
                                                'missing_feature': {'const': 'error',
                                                                    'nullable': False,
                                                                    'required': True,
                                                                    'type': 'string'},
                                                'missing_state': {'const': 'error',
                                                                  'nullable': False,
                                                                  'required': True,
                                                                  'type': 'string'},
                                                'outer_offset': {'const': 60,
                                                                 'nullable': False,
                                                                 'required': True,
                                                                 'type': 'int64'},
                                                'policy_id': {'nullable': False,
                                                              'ref': 'PolicyID',
                                                              'required': True,
                                                              'type': 'ref'},
                                                'policy_version': {'nullable': False,
                                                                   'ref': 'LegacyID',
                                                                   'required': True,
                                                                   'type': 'ref'},
                                                'recency_anchor': {'const': 'updated_at_ns',
                                                                   'nullable': False,
                                                                   'required': True,
                                                                   'type': 'string'},
                                                'recency_tau_ns': {'nullable': False,
                                                                   'ref': 'PositiveNS',
                                                                   'required': True,
                                                                   'type': 'ref'},
                                                'rtol': {'nullable': False,
                                                         'ref': 'Tolerance',
                                                         'required': True,
                                                         'type': 'ref'}},
                                     'hash_type_name': 'RegistryBoundRankerProjectionV2',
                                     'kind': 'closed_typed_projection',
                                     'meaning': 'RegistryGraph안의ranker; 이미검증된config_sha256포함',
                                     'required': ['policy_id',
                                                  'policy_version',
                                                  'dynamics_config_sha256',
                                                  'feature_contract_sha256',
                                                  'outer_offset',
                                                  'lanes',
                                                  'missing_state',
                                                  'missing_feature',
                                                  'recency_anchor',
                                                  'recency_tau_ns',
                                                  'base_equal_rank',
                                                  'feature_equal_rank',
                                                  'final_tie',
                                                  'fallback',
                                                  'atol',
                                                  'rtol',
                                                  'inherited_b2_config_sha256',
                                                  'ablation',
                                                  'config_sha256'],
                                     'source_type': 'RankerConfigV2'},
 'RegistryPolicyEntryProjectionV2': {'additional_fields': False,
                                     'excluded_fields': [],
                                     'fields': {'allowed_roles': {'items': {'nullable': False,
                                                                            'ref': 'InputRole',
                                                                            'type': 'ref'},
                                                                  'max_items': 'selected '
                                                                               'ResourceLimits.max_array_items '
                                                                               'with field-specific limits '
                                                                               'applied additionally',
                                                                  'nullable': False,
                                                                  'required': True,
                                                                  'type': 'array'},
                                                'dynamics': {'nullable': True,
                                                             'ref': 'DynamicsConfigV2',
                                                             'required': True,
                                                             'type': 'ref'},
                                                'implemented_mode': {'enum': ['none', 'scalar'],
                                                                     'nullable': False,
                                                                     'required': True,
                                                                     'type': 'string'},
                                                'parameter_provenance': {'const': 'engineering_regression_only',
                                                                         'nullable': False,
                                                                         'required': True,
                                                                         'type': 'string'},
                                                'policy_id': {'nullable': False,
                                                              'ref': 'PolicyID',
                                                              'required': True,
                                                              'type': 'ref'},
                                                'ranker': {'nullable': False,
                                                           'ref': 'RegistryBoundRankerProjectionV2',
                                                           'required': True,
                                                           'type': 'projection_ref'},
                                                'rule_sha256': {'nullable': False,
                                                                'ref': 'SHA',
                                                                'required': True,
                                                                'type': 'ref'},
                                                'uses_elapsed_decay': {'nullable': False,
                                                                       'ref': 'Bool',
                                                                       'required': True,
                                                                       'type': 'ref'},
                                                'uses_outcomes': {'nullable': False,
                                                                  'ref': 'Bool',
                                                                  'required': True,
                                                                  'type': 'ref'},
                                                'version': {'nullable': False,
                                                            'ref': 'LegacyID',
                                                            'required': True,
                                                            'type': 'ref'}},
                                     'hash_type_name': 'RegistryPolicyEntryProjectionV2',
                                     'kind': 'closed_typed_projection',
                                     'meaning': 'ranker field만 RegistryBoundRankerProjectionV2로typed대체',
                                     'required': ['policy_id',
                                                  'version',
                                                  'implemented_mode',
                                                  'uses_outcomes',
                                                  'uses_elapsed_decay',
                                                  'allowed_roles',
                                                  'dynamics',
                                                  'ranker',
                                                  'rule_sha256',
                                                  'parameter_provenance'],
                                     'source_type': 'PolicyEntry'}}
ENUMS = {'AttemptDisposition': ['applied', 'non_learning', 'invalidated', 'duplicate_event', 'duplicate_evidence'],
 'Attribution': ['memory', 'tool', 'retrieval', 'evaluator', 'unattributable', 'not_applicable'],
 'Dimension': ['personal_compliance', 'work_task'],
 'ErrorCode': ['invalid_json',
               'invalid_unicode',
               'duplicate_key',
               'unknown_field',
               'missing_field',
               'invalid_type',
               'invalid_value',
               'unsupported_protocol',
               'unsupported_mode',
               'unsupported_policy',
               'unsupported_restore',
               'scope_mismatch',
               'stream_mismatch',
               'clock_domain_mismatch',
               'revision_mismatch',
               'source_unavailable',
               'task_attribution_unknown',
               'evidence_unqualified',
               'event_conflict',
               'evidence_conflict',
               'time_regression',
               'sequence_regression',
               'future_event',
               'stale_target',
               'invalidated_target',
               'prefix_mismatch',
               'config_mismatch',
               'hash_mismatch',
               'missing_state',
               'missing_feature',
               'duplicate_feature',
               'input_role_forbidden',
               'resource_limit',
               'io_error',
               'pending_activation',
               'unsupported_fallback',
               'revision_regression',
               'target_mismatch',
               'incomplete_prefix',
               'unsupported_version',
               'unsupported_type',
               'snapshot_conflict',
               'snapshot_binding',
               'schedule_mismatch',
               'unsafe_path',
               'output_exists',
               'invalid_state',
               'invalid_time',
               'invalid_half_life'],
 'EventKind': ['activate', 'outcome', 'exposure', 'invalidate'],
 'FallbackMode': ['error'],
 'InputRole': ['events', 'probes', 'candidates', 'snapshot', 'registry', 'features'],
 'Outcome': ['passed', 'failed', 'unknown', 'not_applicable'],
 'OutputRole': ['prediction_run', 'completion_manifest'],
 'OutputStatus': ['completed', 'completed_with_failures'],
 'PolicyID': ['B0', 'B1', 'B2', 'V3-P1-FD', 'V3-P1-IR', 'V3-P1-NOTIME', 'V3-P1-NOOUT'],
 'Reason': ['none',
            'activation',
            'passed_independent',
            'failed_attributable',
            'failed_unattributable',
            'unknown_outcome',
            'exposure_only',
            'invalidated',
            'duplicate_event',
            'duplicate_evidence',
            'cold_start_missing',
            'feature_missing',
            'stale_revision',
            'source_deleted',
            'source_revoked',
            'source_unknown',
            'task_unknown',
            'fallback_b0',
            'unsupported'],
 'ReceiptDisposition': ['applied', 'non_learning', 'invalidated'],
 'V2ErrorCode': ['invalid_json',
                 'invalid_unicode',
                 'duplicate_key',
                 'unknown_field',
                 'missing_field',
                 'invalid_type',
                 'invalid_value',
                 'unsupported_protocol',
                 'unsupported_mode',
                 'unsupported_policy',
                 'unsupported_restore',
                 'scope_mismatch',
                 'stream_mismatch',
                 'clock_domain_mismatch',
                 'revision_mismatch',
                 'source_unavailable',
                 'task_attribution_unknown',
                 'evidence_unqualified',
                 'event_conflict',
                 'evidence_conflict',
                 'time_regression',
                 'sequence_regression',
                 'future_event',
                 'stale_target',
                 'invalidated_target',
                 'prefix_mismatch',
                 'config_mismatch',
                 'hash_mismatch',
                 'missing_state',
                 'missing_feature',
                 'duplicate_feature',
                 'input_role_forbidden',
                 'resource_limit',
                 'io_error',
                 'pending_activation',
                 'unsupported_fallback',
                 'revision_regression',
                 'target_mismatch',
                 'incomplete_prefix',
                 'unsupported_version',
                 'unsupported_type',
                 'snapshot_conflict',
                 'snapshot_binding',
                 'schedule_mismatch',
                 'unsafe_path',
                 'output_exists',
                 'invalid_state',
                 'invalid_time',
                 'invalid_half_life']}
PRIMITIVES = {'Bool': {'type': 'bool'},
 'Count': {'maximum': 9223372036854775807, 'minimum': 0, 'type': 'int64'},
 'FinalK': {'maximum': 64, 'minimum': 0, 'reuse': 'v1 Probe final_k domain', 'type': 'int64'},
 'LegacyContextBudget': {'fields': {'limit': {'maximum': 1000000, 'minimum': 0, 'type': 'int64'},
                                    'unit': {'const': 'fixture_units_v0'}},
                         'target': 'memoryharness.ContextBudget',
                         'type': 'alias'},
 'LegacyID': {'pattern': '^[a-z][a-z0-9_-]{0,63}$',
              'reuse': 'v1 ID exact regex; policy IDs are separate enum, not LegacyID',
              'type': 'string'},
 'NS': {'json_lexical': 'integer only; bool/float/exponent not accepted',
        'maximum': 9223372036854775807,
        'minimum': 0,
        'type': 'int64'},
 'NonnegativeFloat': {'bool_allowed': False,
                      'finite': True,
                      'maximum': 1.7976931348623157e+308,
                      'minimum': 0,
                      'type': 'float64'},
 'PositiveInt64': {'json_lexical': 'integer only',
                   'maximum': 9223372036854775807,
                   'minimum': 1,
                   'type': 'int64',
                   'unit': 'field-specific byte/item/depth count; no time meaning'},
 'PositiveNS': {'json_lexical': 'integer only',
                'maximum': 9223372036854775807,
                'minimum': 1,
                'note': '정수 범위 alias; ResourceLimits의 바이트/개수 필드는 ns단위가 아니라 해당 field unit 사용',
                'type': 'int64'},
 'SHA': {'pattern': '^[0-9a-f]{64}$', 'type': 'string'},
 'SafePath': {'rule': 'relative POSIX path; reject absolute/empty/dot/dotdot/backslash/NUL/symlink escape; '
                      'no secret-bearing segments; UTF8 path bytes<=4096',
              'type': 'string'},
 'SchemaPath': {'rule': 'known schema field path rooted at $; wildcard[*] indexes; unknown raw '
                        'key/body/value not emitted',
                'type': 'string'},
 'Seq': {'maximum': 9223372036854775807, 'minimum': 1, 'type': 'int64'},
 'Seq0': {'maximum': 9223372036854775807, 'minimum': 0, 'type': 'int64'},
 'Tolerance': {'finite': True, 'maximum': 1e-06, 'minimum': 0, 'type': 'float64'},
 'UnitFloat': {'bool_allowed': False, 'finite': True, 'maximum': 1, 'minimum': 0, 'type': 'float64'}}
SELF_HASHES = {'DynamicInputManifestV2': {'field': 'manifest_sha256', 'projection': 'DynamicInputManifestV2HashProjection'}, 'DynamicsConfigV2': {'field': 'config_sha256', 'projection': 'DynamicsConfigV2HashProjection'}, 'DynamicsRunConfigV2': {'field': 'config_sha256', 'projection': 'DynamicsRunConfigV2HashProjection'}, 'EvidenceBinding': {'field': 'binding_sha256', 'projection': 'EvidenceBindingHashProjection'}, 'ImportanceFeatureV1': {'field': 'feature_sha256', 'projection': 'ImportanceFeatureV1HashProjection'}, 'LifecycleFence': {'field': 'fence_sha256', 'projection': 'LifecycleFenceHashProjection'}, 'P1InputManifestV1': {'field': 'manifest_sha256', 'projection': 'P1InputManifestV1HashProjection'}, 'PolicyDecisionV2': {'field': 'decision_sha256', 'projection': 'PolicyDecisionV2HashProjection'}, 'PredictionRunV2': {'field': 'run_sha256', 'projection': 'PredictionRunV2HashProjection'}, 'ReceiptIndexV2': {'field': 'index_sha256', 'projection': 'ReceiptIndexV2HashProjection'}, 'ReceiptV2': {'field': 'receipt_sha256', 'projection': 'ReceiptV2HashProjection'}, 'SnapshotV2': {'field': 'snapshot_sha256', 'projection': 'SnapshotV2HashProjection'}, 'StateV2': {'field': 'state_sha256', 'projection': 'StateV2HashProjection'}, 'TransitionV2': {'field': 'transition_sha256', 'projection': 'TransitionV2HashProjection'}}

NATIVE_PATH_FIELDS = ('ablation', 'accepted_at_ns', 'accepted_seq', 'activated_at_ns', 'activation_accepted_seq', 'activation_event_id', 'activation_payload_sha256', 'active', 'after_state_revision', 'agent', 'allowed_roles', 'applied_count', 'approved', 'arrived_at_ns', 'as_of_ns', 'as_of_seq', 'at_ns', 'atol', 'attempt_disposition', 'attribution', 'authority', 'auxiliary', 'available_accepted_seq', 'available_at_ns', 'base_dense_rank', 'base_dynamic_manifest_sha256', 'base_equal_rank', 'base_rrf_score', 'baseline_bundle_sha256', 'before_state_revision', 'before_state_sha256', 'binding_sha256', 'candidate', 'candidate_snapshot_id', 'candidate_snapshot_sha256', 'checker_sha256', 'checks', 'claim_sha256', 'clock', 'clock_domain', 'code', 'collection', 'config_sha256', 'configs', 'content_sha256', 'context_budget', 'context_cost_units', 'context_cost_used', 'context_unit', 'contract_id', 'contract_sha256', 'cost', 'cursor', 'cutoff_accepted_seq', 'dataset_id', 'decay_enabled', 'decision', 'decision_sha256', 'deleted', 'dense_rank', 'dimension', 'disposition', 'duplicate_event_count', 'duplicate_events', 'duplicate_evidence', 'duplicate_evidence_count', 'duplicate_of_receipt_sha256', 'dynamics', 'dynamics_config_sha256', 'effective_at_ns', 'elapsed_wall_ns', 'eligibility_order', 'eligible_unique_count', 'entries', 'epoch', 'equal', 'equal_conditions', 'error', 'eta_negative', 'eta_positive', 'event_id', 'event_inputs', 'events', 'evidence', 'evidence_consumption_sha256', 'evidence_consumptions', 'evidence_refs', 'evidence_rule_sha256', 'exact_reason_weight', 'failed', 'failed_predictions', 'failed_rows', 'fallback', 'fallback_predictions', 'feature_contract_sha256', 'feature_equal_rank', 'feature_file', 'feature_id', 'feature_name', 'feature_reads', 'feature_sha256', 'feature_version', 'features', 'fence_sha256', 'files', 'final_k', 'final_ordinal', 'final_tie', 'first_event_id', 'first_receipt_sha256', 'fusion_score', 'generator_config_sha256', 'id', 'identity_sha256', 'implemented_mode', 'independence', 'index_sha256', 'inherited_b2_config_sha256', 'initial', 'input_count', 'input_manifest_sha256', 'input_refs', 'key', 'kind', 'lanes', 'last_applied_at_ns', 'last_applied_seq', 'lifecycle_fence', 'manifest_sha256', 'max_array_items', 'max_events', 'max_features', 'max_file_bytes', 'max_json_depth', 'max_probes', 'max_receipts', 'max_states', 'max_string_bytes', 'minimum_accepted_seq', 'missing_feature', 'missing_reason', 'missing_state', 'mode', 'name', 'namespace', 'negative_policy', 'non_learning_count', 'observed_at_ns', 'observed_seq', 'ok', 'origin', 'other_weight', 'outcome', 'outcome_jump_enabled', 'outer_offset', 'outputs', 'parameter_provenance', 'path', 'payload_sha256', 'planned', 'planned_rows', 'policy_contract_sha256', 'policy_id', 'policy_ids', 'policy_version', 'predictions', 'prefix_sha256', 'previous_prefix_sha256', 'probes', 'producer_version', 'project', 'protocol', 'provider_usd', 'qualification', 'query_id', 'rank_offset', 'ranker', 'ranker_config_sha256', 'ranks', 'raw_sha256', 'reason', 'reasons', 'receipt', 'receipt_sha256', 'receipts', 'recency_anchor', 'recency_tau_ns', 'ref', 'refs', 'registry', 'registry_sha256', 'rejected_count', 'relative_path', 'resource_limits', 'revision', 'role', 'rtol', 'rule_sha256', 'run_config_raw_sha256', 'run_config_sha256', 'run_id', 'run_ids', 'run_ref', 'run_sha256', 'scope', 'score', 'selected', 'size_bytes', 'snapshot', 'snapshot_cursor', 'snapshot_id', 'snapshot_sha256', 'snapshots', 'source', 'source_refs', 'source_state_revision', 'source_status', 'source_verified', 'state', 'state_revision', 'state_sha256', 'state_updates', 'states', 'status', 'statuses', 'stream_id', 'strength', 'target', 'task', 'task_ref', 'task_sha256', 'tau_ns', 'transition_sha256', 'transport_attempt_id', 'trial_ref', 'trust', 'unique_events', 'unit', 'unit_strength', 'updated_at_ns', 'used_context_units', 'user', 'uses_elapsed_decay', 'uses_outcomes', 'value', 'version', 'weight', 'workspace')


def external(call, *args):
    try:
        return call(*args)
    except IO.ContractError as error:
        code = error.code if error.code in ENUMS['V2ErrorCode'] else 'io_error'
        fail(code, error.path if known_path(error.path) else '$')
    except ContractErrorV2:
        raise
    except Exception:
        fail('io_error')


def known_path(value):
    if type(value) is not str or not re.fullmatch(r'\$(?:\.[a-z][a-z0-9_]*|\[\*\])*', value, re.ASCII):
        return False
    return all(token in NATIVE_PATH_FIELDS for token in re.findall(r'\.([a-z][a-z0-9_]*)', value))


def _depth(text):
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
        elif char in '[{':
            depth += 1
            require(depth <= 32, 'resource_limit')
        elif char in ']}':
            depth -= 1


def parse(raw):
    require(type(raw) is bytes, 'invalid_type')
    require(len(raw) <= MAX_BYTES, 'resource_limit')
    try:
        text = raw.decode('utf8')
    except UnicodeError:
        fail('invalid_unicode')
    tree = external(IO._parse_json, text)
    external(IO._unicode, tree)
    _depth(text)
    external(IO._duplicates, tree)
    pending = [tree]
    while pending:
        value = pending.pop()
        if isinstance(value, IO._Pairs):
            for key, item in value:
                pending.extend((key, item))
        elif type(value) is list:
            require(len(value) <= MAX_ARRAY, 'resource_limit')
            pending.extend(value)
        elif isinstance(value, IO._OverflowInteger):
            fail('invalid_value')
        elif type(value) is str:
            require(len(value.encode('utf8')) <= 4096, 'resource_limit')
        elif type(value) is float:
            require(math.isfinite(value))
    return external(IO._plain, tree)


def read(path):
    path = external(IO._safe_absolute, path)
    parent = external(IO._directory_fd, path.parent)
    try:
        before = os.stat(path.name, dir_fd=parent, follow_symlinks=False)
        require(stat.S_ISREG(before.st_mode), 'unsafe_path')
        fd = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=parent)
        with os.fdopen(fd, 'rb') as stream:
            info = os.fstat(stream.fileno())
            require(stat.S_ISREG(info.st_mode) and (info.st_dev, info.st_ino) == (before.st_dev, before.st_ino), 'unsafe_path')
            raw = stream.read(MAX_BYTES + 1)
        require(len(raw) <= MAX_BYTES, 'resource_limit')
        return raw
    except OSError:
        fail('io_error')
    finally:
        os.close(parent)


def _value_code(field):
    if field.get('ref') == 'PolicyID':
        return 'unsupported_policy'
    if field.get('const') == 'error':
        return 'unsupported_fallback'
    if field.get('const') == 'scalar':
        return 'unsupported_mode'
    constant = field.get('const')
    if type(constant) is str and constant.startswith(('dynamics-', 'p1-')):
        return 'unsupported_protocol'
    return 'invalid_value'


def _primitive(kind, value, path):
    if kind in ENUMS:
        require(type(value) is str, 'invalid_type', path)
        require(value in ENUMS[kind], 'unsupported_policy' if kind == 'PolicyID' else 'invalid_value', path)
        return
    if kind == 'LegacyContextBudget':
        external(IO.validate_typed, 'ContextBudget', value)
        return
    if kind == 'SchemaPath':
        require(known_path(value), 'invalid_value', path)
        return
    if kind in ('SHA', 'LegacyID', 'SafePath'):
        require(type(value) is str, 'invalid_type', path)
        patterns = {'SHA': r'[0-9a-f]{64}', 'LegacyID': r'[a-z][a-z0-9_-]{0,63}'}
        if kind in patterns:
            require(re.fullmatch(patterns[kind], value, re.ASCII) is not None, 'invalid_value', path)
        else:
            require(bool(value) and not value.startswith('/') and '\\' not in value and '\0' not in value and all(p not in ('', '.', '..') for p in value.split('/')), 'unsafe_path', path)
        return
    rule = PRIMITIVES[kind]
    if rule['type'] == 'bool':
        require(type(value) is bool, 'invalid_type', path)
        return
    if rule['type'] == 'int64':
        require(type(value) is int, 'invalid_type', path)
    else:
        require(type(value) in (int, float), 'invalid_type', path)
        require(math.isfinite(value), 'invalid_value', path)
    require(rule['minimum'] <= value <= rule['maximum'], 'invalid_value', path)


def _field(field, value, path):
    if value is None:
        require(field.get('nullable', False), 'invalid_type', path)
        return
    kind = field['type']
    if kind in ('ref', 'projection_ref'):
        _typed(field['ref'], value, path)
    elif kind == 'array':
        require(type(value) is list, 'invalid_type', path)
        cap = field.get('max_items', MAX_ARRAY)
        require(len(value) <= (cap if type(cap) is int else MAX_ARRAY), 'resource_limit', path)
        for item in value:
            _field(field['items'], item, path + '[*]')
    elif kind == 'string':
        require(type(value) is str, 'invalid_type', path)
    elif kind == 'int64':
        require(type(value) is int, 'invalid_type', path)
    else:
        fail('invalid_type', path)
    if 'const' in field:
        require(value == field['const'], _value_code(field), path)
    if 'enum' in field:
        require(value in field['enum'], 'unsupported_mode' if path.endswith(('.mode', '.implemented_mode')) else 'invalid_value', path)


def _shape(kind, value, unknown, path='$'):
    if kind not in MODELS or type(value) is not dict:
        return
    fields = MODELS[kind]['fields']
    if unknown:
        require(not set(value).difference(fields), 'unknown_field', path)
    else:
        for key in MODELS[kind]['required']:
            require(key in value, 'missing_field', path + '.' + key)
    for key, field in fields.items():
        if key not in value or value[key] is None:
            continue
        if field['type'] in ('ref', 'projection_ref'):
            _shape(field['ref'], value[key], unknown, path + '.' + key)
        elif field['type'] == 'array' and type(value[key]) is list:
            for item in value[key]:
                _shape(field['items'].get('ref', ''), item, unknown, path + '.' + key + '[*]')


def _typed(kind, value, path='$'):
    if kind not in MODELS:
        return _primitive(kind, value, path)
    require(type(value) is dict, 'invalid_type', path)
    fields = MODELS[kind]['fields']
    for key in MODELS[kind]['required']:
        _field(fields[key], value[key], path + '.' + key)


def _string(value):
    raw = value.encode('utf8')
    return b's' + str(len(raw)).encode('ascii') + b':' + raw


def _canonical_field(field, value):
    if value is None:
        return b'n'
    kind = field['type']
    if kind in ('ref', 'projection_ref'):
        return _encode(field['ref'], value)
    if kind == 'array':
        return b'a' + str(len(value)).encode('ascii') + b'[' + b''.join(_canonical_field(field['items'], item) for item in value) + b']'
    if kind == 'int64':
        return b'i' + str(value).encode('ascii') + b';'
    return _string(value)


def _encode(kind, value):
    if value is None:
        return b'n'
    if kind in MODELS:
        fields = MODELS[kind]['fields']
        return b'o' + str(len(fields)).encode('ascii') + b'{' + b''.join(_string(key) + _canonical_field(fields[key], value[key]) for key in sorted(fields)) + b'}'
    if kind == 'LegacyContextBudget':
        return b'o2{' + _string('limit') + b'i' + str(value['limit']).encode() + b';' + _string('unit') + _string(value['unit']) + b'}'
    if kind in ENUMS or kind in ('SHA', 'LegacyID', 'SchemaPath', 'SafePath'):
        return _string(value)
    rule = PRIMITIVES[kind]
    if rule['type'] == 'bool':
        return b't' if value else b'f'
    if rule['type'] == 'int64':
        return b'i' + str(value).encode('ascii') + b';'
    return b'd' + struct.pack('>d', 0.0 if value == 0 else float(value)).hex().encode() + b';'


def canonical(kind, value):
    require(kind in MODELS or kind in PRIMITIVES or kind in ENUMS, 'unsupported_type')
    _shape(kind, value, True)
    _shape(kind, value, False)
    _typed(kind, value)
    _hashes(kind, value)
    return _encode(kind, value)


def _projection(kind, value):
    model = MODELS[kind]
    source = model.get('source_type')
    allowed = MODELS[source]['fields'] if source in MODELS else model['fields']
    require(not set(value).difference(allowed), 'unknown_field')
    projected = {}
    for key, field in model['fields'].items():
        require(key in value, 'missing_field', '$.' + key)
        child = value[key]
        if child is not None and field['type'] == 'projection_ref':
            child = _projection(field['ref'], child)
        elif child is not None and field['type'] == 'array' and field['items']['type'] == 'projection_ref':
            child = [_projection(field['items']['ref'], item) for item in child]
        projected[key] = child
    return projected


def semantic_sha(kind, value):
    require(kind in MODELS, 'unsupported_type')
    if MODELS[kind].get('kind') == 'closed_typed_projection':
        value = _projection(kind, value)
    encoded = canonical(kind, value)
    return hashlib.sha256(b'dynamics-contract-v2\0' + kind.encode('ascii') + b'\0' + encoded).hexdigest()


def _hashes(kind, value):
    if kind not in MODELS or value is None:
        return
    for key, field in MODELS[kind]['fields'].items():
        child = value[key]
        if field['type'] in ('ref', 'projection_ref'):
            _hashes(field['ref'], child)
        elif field['type'] == 'array':
            for item in child:
                _hashes(field['items'].get('ref', ''), item)
    if kind in SELF_HASHES:
        binding = SELF_HASHES[kind]
        require(value[binding['field']] == semantic_sha(binding['projection'], value), 'hash_mismatch', '$.' + binding['field'])
    if kind in ('RankerConfigV2', 'RegistryBoundRankerProjectionV2'):
        require(value['config_sha256'] == semantic_sha('RankerConfigParametersProjectionV2', value), 'hash_mismatch', '$.config_sha256')
    if kind == 'PolicyRegistryV2':
        require(value['registry_sha256'] == semantic_sha('PolicyRegistryGraphProjectionV2', value), 'hash_mismatch', '$.registry_sha256')


def decode(raw, kind):
    value = parse(raw)
    require(kind in ('PolicyRegistryV2', 'PredictionRunV2'), 'unsupported_type')
    _shape(kind, value, True)
    _shape(kind, value, False)
    _typed(kind, value)
    _hashes(kind, value)
    return value


def _ranker(ranker):
    policy = ranker['policy_id']
    shapes = {'B0': [], 'B1': ['relevance', 'recency'], 'V3-P1-IR': ['relevance', 'importance', 'recency']}
    shape = shapes.get(policy, ['relevance', 'state'])
    require([lane['kind'] for lane in ranker['lanes']] == shape, 'config_mismatch')
    require(policy == 'B0' or any(lane['weight'] > 0 for lane in ranker['lanes']), 'config_mismatch')
    stateful = policy in ('B2', 'V3-P1-FD', 'V3-P1-NOTIME', 'V3-P1-NOOUT')
    require((ranker['dynamics_config_sha256'] is not None) == stateful, 'config_mismatch')
    require((ranker['feature_contract_sha256'] is not None) == (policy == 'V3-P1-IR'), 'config_mismatch')
    if ranker['feature_contract_sha256'] is not None:
        require(ranker['feature_contract_sha256'] == 'b6e965bbfecb4a0f20365d11709ff168043293fe1c5b848b06bc886e95f5f0d8', 'hash_mismatch')
    ablation = 'disable_decay' if policy == 'V3-P1-NOTIME' else 'disable_outcome_jumps' if policy == 'V3-P1-NOOUT' else 'none'
    require(ranker['ablation'] == ablation and (ranker['inherited_b2_config_sha256'] is None) == (ablation == 'none'), 'config_mismatch')


def _registry(registry):
    require([e['policy_id'] for e in registry['entries']] == ENUMS['PolicyID'], 'config_mismatch')
    limits = dict(max_array_items=200000, max_events=100000, max_features=100000, max_file_bytes=67108864,
                  max_json_depth=32, max_probes=100000, max_receipts=200000, max_states=100000, max_string_bytes=4096)
    require(registry['resource_limits'] == limits, 'resource_limit')
    by_id = {entry['policy_id']: entry for entry in registry['entries']}
    for entry in registry['entries']:
        r, dynamics = entry['ranker'], entry['dynamics']
        _ranker(r)
        require(r['registry_sha256'] == registry['registry_sha256'] and entry['policy_id'] == r['policy_id'] and entry['version'] == r['policy_version'], 'config_mismatch')
        state = r['dynamics_config_sha256'] is not None
        require((dynamics is not None) == state and entry['implemented_mode'] == ('scalar' if state else 'none'), 'config_mismatch')
        if dynamics is not None:
            require(dynamics['config_sha256'] == r['dynamics_config_sha256'], 'hash_mismatch')
            decay = entry['policy_id'] != 'V3-P1-NOTIME'
            jumps = entry['policy_id'] not in ('V3-P1-FD', 'V3-P1-NOOUT')
            require(dynamics['decay_enabled'] == decay and dynamics['outcome_jump_enabled'] == jumps and
                    entry['uses_elapsed_decay'] == decay and entry['uses_outcomes'] == jumps, 'config_mismatch')
    _ablations(by_id)


def _ablations(entries):
    base = entries['B2']
    for policy, flag in [('V3-P1-NOTIME', 'decay_enabled'), ('V3-P1-NOOUT', 'outcome_jump_enabled')]:
        entry = entries[policy]
        require(entry['ranker']['inherited_b2_config_sha256'] == base['ranker']['config_sha256'], 'config_mismatch')
        ignored = {'config_sha256', flag}
        require({k: v for k, v in entry['dynamics'].items() if k not in ignored} ==
                {k: v for k, v in base['dynamics'].items() if k not in ignored}, 'config_mismatch')
        ignored = {'policy_id', 'policy_version', 'dynamics_config_sha256', 'inherited_b2_config_sha256', 'ablation', 'config_sha256', 'registry_sha256'}
        require({k: v for k, v in entry['ranker'].items() if k not in ignored} ==
                {k: v for k, v in base['ranker'].items() if k not in ignored}, 'config_mismatch')


def _run(run, registry):
    require(run['registry_sha256'] == registry['registry_sha256'], 'hash_mismatch')
    entry = next(e for e in registry['entries'] if e['policy_id'] == run['policy_id'])
    require(run['policy_version'] == entry['version'], 'config_mismatch')
    seen, failed = set(), 0
    for prediction in run['predictions']:
        require(prediction['query_id'] not in seen, 'schedule_mismatch')
        seen.add(prediction['query_id'])
        if prediction['status'] == 'failed':
            require(prediction['decision'] is None and prediction['error'] is not None, 'invalid_state')
            failed += 1
        else:
            require(prediction['status'] != 'fallback', 'unsupported_fallback')
            decision = prediction['decision']
            require(decision is not None and prediction['error'] is None, 'invalid_state')
            require(decision['status'] != 'fallback', 'unsupported_fallback')
            require(decision['query_id'] == prediction['query_id'] and decision['policy_id'] == run['policy_id'] and
                    decision['policy_version'] == run['policy_version'] and decision['ranker_config_sha256'] == entry['ranker']['config_sha256'], 'snapshot_binding')
            _decision(decision)
    require(failed == run['cost']['failed_predictions'] and run['cost']['fallback_predictions'] == 0, 'schedule_mismatch')


def _decision(decision):
    require(decision['context_cost_used'] <= decision['context_budget']['limit'], 'invalid_value')
    refs = set()
    for ordinal, row in enumerate(decision['ranks'], 1):
        key = canonical('RefV2', row['ref'])
        require(key not in refs and row['final_ordinal'] == ordinal, 'snapshot_conflict')
        refs.add(key)
    selected = [canonical('RefV2', ref) for ref in decision['selected']]
    require(len(set(selected)) == len(selected) and set(selected).issubset(refs), 'snapshot_binding')


def compare_runs(run_paths, registry_path):
    require(type(run_paths) is list and len(run_paths) >= 2, 'invalid_value')
    require(len({str(path) for path in run_paths}) == len(run_paths), 'snapshot_conflict')
    registry = decode(read(registry_path), 'PolicyRegistryV2')
    _registry(registry)
    runs, hashes, identities = [], set(), set()
    for path in run_paths:
        raw = read(path)
        raw_hash = hashlib.sha256(raw).hexdigest()
        require(raw_hash not in hashes, 'snapshot_conflict')
        hashes.add(raw_hash)
        run = decode(raw, 'PredictionRunV2')
        _run(run, registry)
        identity = (run['run_id'], run['policy_id'])
        require(identity not in identities, 'snapshot_conflict')
        identities.add(identity)
        runs.append(run)
    return _compare(runs, registry['registry_sha256'])


def _compare(runs, registry_sha):
    commitments = {(r['input_manifest_sha256'], r['run_config_sha256']) for r in runs}
    shared = len(commitments) == 1
    predictions = [{p['query_id']: p for p in run['predictions']} for run in runs]
    query_sets = [set(rows) for rows in predictions]
    observed = bool(query_sets[0]) and all(keys == query_sets[0] for keys in query_sets)
    observed = observed and all(p['status'] == 'ok' for rows in predictions for p in rows.values())
    candidates, budget = observed and shared, observed and shared
    if observed:
        for query in query_sets[0]:
            decisions = [rows[query]['decision'] for rows in predictions]
            candidates = candidates and len({d['candidate_snapshot_sha256'] for d in decisions}) == 1
            budget = budget and all(d['context_budget'] == decisions[0]['context_budget'] for d in decisions)
    source = observed and shared and candidates
    values = {'registry': True, 'candidates': candidates, 'budget': budget, 'source_snapshot': source,
              'events': shared, 'features': shared, 'scope': shared, 'clock': shared, 'cutoff': shared}
    order = ['candidates', 'events', 'scope', 'clock', 'cutoff', 'budget', 'registry', 'features', 'source_snapshot']
    checks = [{'name': name, 'equal': bool(values[name]), 'reason': 'none' if values[name] else 'source_unknown'} for name in order]
    statuses = []
    for run in runs:
        rows = run['predictions']
        statuses.append({'run_id': run['run_id'], 'policy_id': run['policy_id'], 'planned': len(rows),
                         'ok': sum(p['status'] == 'ok' for p in rows), 'failed': sum(p['status'] == 'failed' for p in rows),
                         'fallback': sum(p['status'] == 'fallback' for p in rows)})
    return {'protocol': 'dynamics-compare-v2', 'run_ids': sorted({r['run_id'] for r in runs}),
            'registry_sha256': registry_sha, 'equal_conditions': all(c['equal'] for c in checks), 'checks': checks, 'statuses': statuses}

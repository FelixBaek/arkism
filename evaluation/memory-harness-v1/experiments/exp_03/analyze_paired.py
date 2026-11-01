"""Read-only paired-history analysis; separate from all frozen v1 DTOs.

A sealed computational plan is not scientific approval. No model, database,
source body, discovery or environment-selected dataset is used here.
"""
import argparse
from collections import Counter, defaultdict
from decimal import Decimal, localcontext
from datetime import date
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import random
import re
import stat
import sys

HERE = Path(__file__).resolve().parent
CONTRACT_RAW = (HERE / 'analysis_contract.json').read_bytes()
CONTRACT = json.loads(CONTRACT_RAW)
MAX_BYTES = CONTRACT['maximum_input_bytes']
STATUSES = set(CONTRACT['statuses'])
ID = re.compile(r'[A-Za-z0-9_.:/-]{1,128}\Z', re.ASCII)
SHA = re.compile(r'[0-9a-f]{64}\Z', re.ASCII)
DECIMAL = re.compile(r'(?:0|[1-9][0-9]{0,17})(?:\.[0-9]{1,12})?\Z', re.ASCII)


class AnalysisError(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def require(condition, code):
    if not condition:
        raise AnalysisError(code)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=True,
                      allow_nan=False).encode()


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def digest(value):
    return sha(canonical(value))


def fields(value, expected):
    require(type(value) is dict and set(value) == set(expected.split()), 'invalid_fields')


def identifier(value):
    require(type(value) is str and ID.fullmatch(value) is not None, 'invalid_id')
    return value


def hash_value(value):
    require(type(value) is str and SHA.fullmatch(value) is not None, 'invalid_hash')
    return value


def count(value, maximum=100000):
    require(type(value) is int and 0 <= value <= maximum, 'invalid_count')
    return value


def decimal(value):
    require(type(value) is str and DECIMAL.fullmatch(value) is not None, 'invalid_decimal')
    return Decimal(value)


def fraction(value):
    result = float(decimal(value))
    require(0 <= result <= 1, 'invalid_fraction')
    return result


def _load_ref_contract():
    path = HERE.parents[1] / 'contract_io.py'
    spec = importlib.util.spec_from_file_location('_exp03_frozen_ref_contract', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


REF_CONTRACT = _load_ref_contract()


def ref_key(value):
    try:
        REF_CONTRACT.validate_typed('Ref', value)
    except REF_CONTRACT.ContractError:
        raise AnalysisError('invalid_ref') from None
    return tuple(value[key] for key in ('origin', 'collection', 'id', 'revision', 'content_sha256'))


def ref_set(values):
    require(type(values) is list, 'invalid_refs')
    keys = [ref_key(value) for value in values]
    require(len(keys) == len(set(keys)), 'duplicate_returned_or_gold_ref')
    return set(keys)


PLAN_FIELDS = '''protocol state contract_sha256 plan_sha256 stage experiment_family population primary_metric
request_modes policies replications minimum_valid_replications cluster_unit bootstrap contrasts delta_min
equivalence_margin max_missing_fraction max_missing_gap truncation_rule alternative_set_rule grader_sha256
interim_looks tuning_trials analysis_changed_after_results cost_requirements'''


def validate_plan(plan):
    fields(plan, PLAN_FIELDS)
    require(plan['protocol'] == 'exp03-analysis-plan-v1' and
            plan['state'] == 'sealed_computational_plan', 'analysis_plan_unsealed')
    require(plan['contract_sha256'] == sha(CONTRACT_RAW), 'contract_mismatch')
    body = {key: value for key, value in plan.items() if key != 'plan_sha256'}
    require(hash_value(plan['plan_sha256']) == digest(body), 'plan_hash_mismatch')
    identifier(plan['experiment_family'])
    require(plan['stage'] in ('synthetic', 'exploratory', 'confirmatory'), 'invalid_stage')
    require(plan['population'] in ('synthetic', 'isolated_service', 'observed_service'), 'invalid_population')
    require(plan['stage'] != 'synthetic' or plan['population'] == 'synthetic', 'population_mismatch')
    require(plan['primary_metric'] in ('recall_at_k', 'provided_grader_accuracy'), 'invalid_metric')
    require(type(plan['policies']) is list and 2 <= len(plan['policies']) <= 32, 'invalid_policies')
    require(len({identifier(p) for p in plan['policies']}) == len(plan['policies']), 'duplicate_policy')
    require(type(plan['request_modes']) is list and bool(plan['request_modes']) and
            len(set(plan['request_modes'])) == len(plan['request_modes']) and
            set(plan['request_modes']) <= {'current', 'as_of'}, 'invalid_request_modes')
    require(1 <= count(plan['minimum_valid_replications']) <= count(plan['replications']) <= 1000,
            'invalid_replications')
    require(plan['cluster_unit'] in ('history', 'preassigned_cluster'), 'invalid_cluster_unit')
    require(plan['truncation_rule'] in ('score_zero', 'exclude'), 'invalid_truncation_rule')
    require(plan['alternative_set_rule'] == 'maximum_recall_over_alternatives', 'unsupported_gold_rule')
    require(0 < fraction(plan['delta_min']) <= 1, 'invalid_delta')
    for key in ('max_missing_fraction', 'max_missing_gap'):
        fraction(plan[key])
    if plan['equivalence_margin'] is not None:
        require(fraction(plan['equivalence_margin']) > 0, 'invalid_equivalence_margin')
    if plan['primary_metric'] == 'provided_grader_accuracy':
        hash_value(plan['grader_sha256'])
    else:
        require(plan['grader_sha256'] is None, 'unexpected_grader')
    count(plan['interim_looks'])
    count(plan['tuning_trials'])
    require(type(plan['analysis_changed_after_results']) is bool, 'invalid_change_flag')
    _validate_bootstrap(plan['bootstrap'])
    _validate_contrasts(plan)
    _validate_cost_requirements(plan['cost_requirements'])


def _validate_bootstrap(value):
    fields(value, 'seed replicates confidence percentile')
    count(value['seed'], 2**63 - 1)
    require(100 <= count(value['replicates'], 10000) <= 10000, 'invalid_bootstrap_replicates')
    require(0 < fraction(value['confidence']) < 1, 'invalid_confidence')
    require(value['percentile'] in ('linear', 'nearest_rank'), 'invalid_percentile')


def _validate_contrasts(plan):
    contrasts = plan['contrasts']
    require(type(contrasts) is list and 1 <= len(contrasts) <= 20, 'invalid_contrasts')
    ids, primary = set(), 0
    for contrast in contrasts:
        fields(contrast, 'id treatment control role')
        identifier(contrast['id'])
        require(contrast['id'] not in ids, 'duplicate_contrast')
        ids.add(contrast['id'])
        require(contrast['role'] in ('primary', 'secondary'), 'invalid_contrast_role')
        primary += contrast['role'] == 'primary'
        require(contrast['treatment'] != contrast['control'] and
                {contrast['treatment'], contrast['control']} <= set(plan['policies']), 'invalid_contrast_policies')
    require(primary == 1, 'one_primary_contrast_required')


def _validate_cost_requirements(values):
    require(type(values) is list and bool(values), 'cost_requirements_missing')
    keys = []
    for value in values:
        fields(value, 'phase resource allocation')
        require(value['phase'] in CONTRACT['cost_phases'] and value['resource'] in CONTRACT['cost_units'] and
                value['allocation'] in ('policy_incremental', 'shared_preprocessing', 'whole_run_nonadditive'),
                'invalid_cost_requirement')
        keys.append(tuple(value[key] for key in ('phase', 'resource', 'allocation')))
    require(len(keys) == len(set(keys)), 'duplicate_cost_requirement')


def job_id(stream, query, policy, repetition):
    return digest([stream, query, policy, repetition])


def validate_schedule(plan, schedule):
    fields(schedule, 'protocol analysis_plan_sha256 queries')
    require(schedule['protocol'] == 'exp03-schedule-v1' and
            schedule['analysis_plan_sha256'] == plan['plan_sha256'], 'schedule_binding_mismatch')
    require(type(schedule['queries']) is list and 0 < len(schedule['queries']) <= CONTRACT['maximum_rows'],
            'invalid_schedule')
    queries, streams = {}, {}
    for query in schedule['queries']:
        fields(query, 'stream_id cluster_id query_id scope kind request_mode gold_sets budget input_sha256')
        for key in ('stream_id', 'cluster_id', 'query_id'):
            identifier(query[key])
        key = (query['stream_id'], query['query_id'])
        require(key not in queries, 'duplicate_query')
        queries[key] = query
        require(streams.setdefault(query['stream_id'], query['cluster_id']) == query['cluster_id'],
                'history_cluster_changed')
        require(plan['cluster_unit'] != 'history' or query['cluster_id'] == query['stream_id'], 'cluster_unit_mismatch')
        require(query['kind'] in ('answerable', 'clear_negative', 'ambiguous') and
                query['request_mode'] in ('current', 'as_of'), 'invalid_query_kind')
        try:
            REF_CONTRACT.validate_typed('Scope', query['scope'])
        except REF_CONTRACT.ContractError:
            raise AnalysisError('invalid_scope') from None
        fields(query['budget'], 'final_k max_input_tokens max_output_tokens')
        count(query['budget']['final_k'], 2**63 - 1)
        for name in ('max_input_tokens', 'max_output_tokens'):
            if query['budget'][name] is not None:
                count(query['budget'][name], 2**63 - 1)
        hash_value(query['input_sha256'])
        require(type(query['gold_sets']) is list, 'invalid_gold')
        sets = [ref_set(value) for value in query['gold_sets']]
        if query['kind'] == 'answerable':
            require(bool(sets) and all(sets), 'empty_answerable_gold')
        else:
            require(not sets, 'nonanswerable_gold')
    require(len(queries)*len(plan['policies'])*plan['replications'] <= CONTRACT['maximum_rows'], 'schedule_too_large')
    jobs = {(s, q, p, r): job_id(s, q, p, r) for s, q in queries
            for p in plan['policies'] for r in range(plan['replications'])}
    require(len(jobs) <= CONTRACT['maximum_rows'], 'schedule_too_large')
    return queries, streams, jobs


def validate_observations(plan, schedule, queries, jobs, observations):
    fields(observations, 'protocol schedule_sha256 selection_stage rows')
    require(observations['protocol'] == 'exp03-observations-v1' and
            observations['schedule_sha256'] == digest(schedule), 'results_binding_mismatch')
    require(observations['selection_stage'] == 'post_packing_final_fence', 'wrong_selection_stage')
    require(type(observations['rows']) is list and len(observations['rows']) <= len(jobs), 'invalid_results')
    indexed = {}
    for row in observations['rows']:
        fields(row, 'stream_id query_id policy_id repetition input_sha256 gold_sha256 budget_sha256 final_refs status correct fallback grader_sha256')
        key = (row['stream_id'], row['query_id'], row['policy_id'], row['repetition'])
        require(key in jobs and key not in indexed and type(row['repetition']) is int, 'result_identity_mismatch')
        query = queries[key[:2]]
        require(row['input_sha256'] == query['input_sha256'] and row['gold_sha256'] == digest(query['gold_sets']) and
                row['budget_sha256'] == digest(query['budget']), 'gold_input_or_budget_mismatch')
        require(row['status'] in STATUSES and type(row['fallback']) is bool, 'invalid_result_status')
        selected = ref_set(row['final_refs'])
        require(len(selected) <= query['budget']['final_k'], 'final_budget_exceeded')
        require(row['correct'] is None or type(row['correct']) is bool, 'invalid_correctness')
        require(row['grader_sha256'] == plan['grader_sha256'], 'grader_mismatch')
        if plan['primary_metric'] == 'provided_grader_accuracy' and row['status'] == 'success':
            require(type(row['correct']) is bool, 'success_missing_grade')
        indexed[key] = row
    return indexed


def score(query, row, metric, truncation_rule):
    if row is None:
        return None
    if row['status'] == 'truncated' and truncation_rule == 'score_zero':
        return 0.0
    if row['status'] != 'success':
        return None
    if metric == 'provided_grader_accuracy':
        return float(row['correct'])
    if query['kind'] != 'answerable':
        return None
    selected = ref_set(row['final_refs'])
    return max(len(selected & relevant) / len(relevant) for relevant in map(ref_set, query['gold_sets']))


def _percentile(values, q, method):
    ordered = sorted(values)
    if method == 'nearest_rank':
        return ordered[max(0, math.ceil(q * len(ordered)) - 1)]
    x = q * (len(ordered) - 1)
    low, high = math.floor(x), math.ceil(x)
    return ordered[low] + (ordered[high] - ordered[low]) * (x - low)


def cluster_bootstrap(histories, clusters, config, contrast_id):
    grouped = defaultdict(list)
    for history, difference in histories.items():
        grouped[clusters[history]].append(difference)
    ids = sorted(grouped)
    require(len(histories) * config['replicates'] <= CONTRACT['maximum_bootstrap_work'], 'bootstrap_work_limit')
    if len(ids) < 2:
        return None
    rng = random.Random(str(config['seed']) + '|' + contrast_id)
    draws = []
    for _ in range(config['replicates']):
        selected = [grouped[rng.choice(ids)] for _ in ids]
        values = [v for group in selected for v in group]
        draws.append(math.fsum(values) / len(values))
    tail = (1 - fraction(config['confidence'])) / 2
    return [_percentile(draws, tail, config['percentile']),
            _percentile(draws, 1 - tail, config['percentile'])]


def _beta_fraction(a, b, x):
    # Continued fraction of the regularized incomplete beta, with bounded work.
    small = 1e-300
    c, d = 1.0, 1.0 - (a + b) * x / (a + 1)
    d = 1 / (d if abs(d) > small else small)
    result = d
    for m in range(1, 401):
        for coefficient in (m * (b - m) * x / ((a + 2*m - 1) * (a + 2*m)),
                            -(a + m) * (a + b + m) * x / ((a + 2*m) * (a + 2*m + 1))):
            d = 1 + coefficient * d
            c = 1 + coefficient / c
            d = 1 / (d if abs(d) > small else small)
            c = c if abs(c) > small else small
            change = c * d
            result *= change
        if abs(change - 1) < 3e-14:
            return result
    raise AnalysisError('statistical_convergence_failure')


def t_two_sided_p(t, df):
    require(df >= 1 and math.isfinite(t), 'invalid_t_input')
    x, a, b = df / (df + t*t), df / 2, 0.5
    if x >= 1:
        return 1.0
    if x <= 0:
        return 0.0
    factor = math.exp(math.lgamma(a+b)-math.lgamma(a)-math.lgamma(b) + a*math.log(x) + b*math.log1p(-x))
    if x < (a+1)/(a+b+2):
        result = factor * _beta_fraction(a, b, x) / a
    else:
        result = 1 - factor * _beta_fraction(b, a, 1-x) / b
    return max(0.0, min(1.0, result))


def t_critical(confidence, df):
    target = 1 - confidence
    low, high = 0.0, 1.0
    while t_two_sided_p(high, df) > target:
        high *= 2
        require(high < 1e12, 'statistical_convergence_failure')
    for _ in range(100):
        mid = (low + high) / 2
        if t_two_sided_p(mid, df) > target:
            low = mid
        else:
            high = mid
    return (low + high) / 2


def cluster_t(histories, clusters, confidence):
    n = len(histories)
    if not n:
        return None
    mean = math.fsum(histories.values()) / n
    residuals = defaultdict(float)
    for history in sorted(histories):
        residuals[clusters[history]] += histories[history] - mean
    size = len(residuals)
    if size < 2:
        return None
    # Equal history weights; whole-cluster residual sums preserve that estimand.
    se = math.sqrt(size / (size - 1) * math.fsum(x*x for x in residuals.values()) / (n*n))
    if se == 0:
        return {'estimate': mean, 'se': 0.0, 'df': size-1, 'p': 1.0 if mean == 0 else 0.0,
                'ci': [mean, mean], 'degenerate': True}
    critical = t_critical(confidence, size - 1)
    return {'estimate': mean, 'se': se, 'df': size-1, 'p': t_two_sided_p(mean/se, size-1),
            'ci': [mean-critical*se, mean+critical*se], 'degenerate': False}


def holm(p_values):
    ordered = sorted((1.0 if value is None else value, name) for name, value in p_values.items())
    adjusted, prior = {}, 0.0
    for rank, (p, name) in enumerate(ordered):
        prior = min(1.0, max(prior, (len(ordered)-rank)*p))
        adjusted[name] = None if p_values[name] is None else prior
    return adjusted


def cost_summary(plan, schedule, jobs, ledger):
    fields(ledger, 'protocol schedule_sha256 entries')
    require(ledger['protocol'] == 'exp03-cost-ledger-v1' and ledger['schedule_sha256'] == digest(schedule),
            'cost_binding_mismatch')
    require(type(ledger['entries']) is list and len(ledger['entries']) <= CONTRACT['maximum_rows'], 'invalid_costs')
    seen, coverage, groups = set(), set(), {}
    job_policies = {job: key[2] for key, job in jobs.items()}
    all_jobs = set(job_policies)
    for entry in ledger['entries']:
        fields(entry, 'measurement_id job_id policy_id phase resource allocation value unit basis missing_reason source_sha256 price_source')
        identifier(entry['measurement_id'])
        require(entry['measurement_id'] not in seen, 'duplicate_cost_measurement')
        seen.add(entry['measurement_id'])
        _validate_cost_entry(entry, plan, job_policies)
        key = (entry['policy_id'], entry['phase'], entry['resource'], entry['allocation'])
        coverage.add((entry['job_id'], entry['phase'], entry['resource'], entry['allocation']))
        group = groups.setdefault(key, {'observed': Decimal(0), 'estimated': Decimal(0), 'missing': 0,
                                        'estimated_count': 0, 'measurements': 0, 'individual': []})
        group['measurements'] += 1
        if entry['allocation'] == 'whole_run_nonadditive' or entry['resource'] == 'peak_memory_bytes':
            group['individual'].append({name: entry[name] for name in
                ('measurement_id', 'job_id', 'basis', 'value', 'missing_reason', 'source_sha256', 'price_source')})
        if entry['basis'] == 'missing':
            group['missing'] += 1
        else:
            with localcontext() as context:
                context.prec = 50
                group[entry['basis']] += decimal(entry['value'])
            group['estimated_count'] += entry['basis'] == 'estimated'
    for requirement in plan['cost_requirements']:
        expected = all_jobs if requirement['allocation'] == 'policy_incremental' else {None}
        for job in expected:
            require((job, requirement['phase'], requirement['resource'], requirement['allocation']) in coverage,
                    'missing_cost_measurement')
    table = []
    for key in sorted(groups, key=lambda k: tuple('' if x is None else x for x in k)):
        table.append(_cost_table_row(key, groups[key]))
    return table


def _cost_table_row(key, value):
    single_or_additive = not value['individual'] or len(value['individual']) == 1
    row = dict(zip(('policy_id', 'phase', 'resource', 'allocation'), key), unit=CONTRACT['cost_units'][key[2]],
               aggregation='individual_measurements' if value['individual'] else 'sum_of_measurements',
               observed_subtotal=str(value['observed']) if single_or_additive else None,
               estimated_subtotal=str(value['estimated']) if single_or_additive else None,
               missing_measurements=value['missing'], measurements=value['measurements'],
               complete_observed_total=str(value['observed']) if single_or_additive and not value['missing']
               and not value['estimated_count'] else None)
    if value['individual']:
        row['individual_measurements'] = sorted(value['individual'], key=lambda item: item['measurement_id'])
    return row


def _validate_cost_entry(entry, plan, job_policies):
    require(entry['phase'] in CONTRACT['cost_phases'] and entry['resource'] in CONTRACT['cost_units'], 'invalid_cost_kind')
    require(entry['unit'] == CONTRACT['cost_units'][entry['resource']], 'cost_unit_mismatch')
    require(entry['allocation'] in ('policy_incremental', 'shared_preprocessing', 'whole_run_nonadditive'),
            'invalid_cost_allocation')
    require(entry['basis'] in ('observed', 'estimated', 'missing'), 'invalid_cost_basis')
    if entry['allocation'] == 'policy_incremental':
        require(entry['job_id'] in job_policies and entry['policy_id'] in plan['policies'], 'invalid_cost_job')
        require(job_policies[entry['job_id']] == entry['policy_id'], 'cost_policy_mismatch')
    else:
        require(entry['job_id'] is None and entry['policy_id'] is None, 'invalid_shared_cost')
    if entry['basis'] == 'missing':
        require(entry['value'] is None and entry['missing_reason'] in ('not_measured', 'source_unavailable', 'not_applicable'),
                'missing_cost_reason')
        require(entry['source_sha256'] is None, 'unexpected_missing_source')
    else:
        decimal(entry['value'])
        require(entry['missing_reason'] is None, 'unexpected_missing_cost_reason')
        hash_value(entry['source_sha256'])
    if entry['resource'] == 'actual_currency' and entry['basis'] != 'missing':
        fields(entry['price_source'], 'sha256 as_of')
        hash_value(entry['price_source']['sha256'])
        require(type(entry['price_source']['as_of']) is str and
                re.fullmatch(r'[0-9]{4}-[0-9]{2}-[0-9]{2}', entry['price_source']['as_of']) is not None, 'price_time_missing')
        try:
            date.fromisoformat(entry['price_source']['as_of'])
        except ValueError:
            raise AnalysisError('invalid_price_date') from None
    else:
        require(entry['price_source'] is None, 'unexpected_price_source')


def _eligible(query, plan):
    return query['request_mode'] in plan['request_modes'] and query['kind'] != 'ambiguous' and (
        plan['primary_metric'] != 'recall_at_k' or query['kind'] == 'answerable')


def arm_reconciliation(plan, queries, jobs, indexed):
    result = {}
    for policy in plan['policies']:
        statuses, strata = Counter(), Counter()
        recorded = 0
        diagnostic = Counter()
        for key in jobs:
            if key[2] != policy:
                continue
            query, row = queries[key[:2]], indexed.get(key)
            recorded += row is not None
            status = row['status'] if row else 'missing'
            statuses[status] += 1
            strata[query['request_mode'] + ':' + query['kind']] += 1
            diagnostic['fallbacks'] += bool(row and row['fallback'])
            if query['kind'] == 'clear_negative':
                diagnostic['negative_scheduled'] += 1
                if status == 'success':
                    diagnostic['negative_successes'] += 1
                    diagnostic['negative_false_returns'] += bool(row['final_refs'])
                else:
                    diagnostic['negative_failures'] += 1
            if query['kind'] == 'answerable' and status == 'success':
                diagnostic['answerable_observed'] += 1
                diagnostic['answerable_hit_any'] += score(query, row, 'recall_at_k', 'exclude') > 0
        result[policy] = {'planned': sum(statuses.values()), 'recorded': recorded,
                          'status_counts': dict(statuses), 'strata': dict(strata), **dict(diagnostic)}
    return result


def _paired_histories(plan, queries, indexed, contrast, truncation):
    grouped, bounds = defaultdict(list), defaultdict(list)
    missing, total, absent = [0, 0], 0, 0
    treatment, control = contrast['treatment'], contrast['control']
    for key, query in sorted(queries.items()):
        if not _eligible(query, plan):
            continue
        paired, limits = [], []
        for repetition in range(plan['replications']):
            rows = [indexed.get((*key, p, repetition)) for p in (treatment, control)]
            scores = [score(query, row, plan['primary_metric'], truncation) for row in rows]
            total += 1
            absent += any(row is None or row['status'] in ('not_run', 'missing') for row in rows)
            for arm, value in enumerate(scores):
                missing[arm] += value is None
            if None not in scores:
                paired.append(scores)
            t, c = scores
            limits.append(((0 if t is None else t)-(1 if c is None else c),
                           (1 if t is None else t)-(0 if c is None else c)))
        if len(paired) >= plan['minimum_valid_replications']:
            grouped[key[0]].append(tuple(math.fsum(v[i] for v in paired)/len(paired) for i in (0, 1)))
        bounds[key[0]].append(tuple(math.fsum(v[i] for v in limits)/len(limits) for i in (0, 1)))
    histories = {}
    for stream, values in sorted(grouped.items()):
        t, c = (math.fsum(value[i] for value in values)/len(values) for i in (0, 1))
        histories[stream] = {'treatment': t, 'control': c, 'difference': t-c, 'paired_queries': len(values)}
    bound_values = [tuple(math.fsum(v[i] for v in values)/len(values) for i in (0, 1))
                    for _, values in sorted(bounds.items())]
    uncertainty = [math.fsum(v[i] for v in bound_values)/len(bound_values) for i in (0, 1)] if bound_values else None
    return histories, {'eligible_repetitions_per_arm': total, 'missing_by_arm': missing,
                       'missing_fractions': [value/total for value in missing] if total else [None, None],
                       'absent_or_not_run_pairs': absent, 'bounds': uncertainty}


def analyze_contrast(plan, queries, streams, indexed, contrast):
    histories, missing = _paired_histories(plan, queries, indexed, contrast, plan['truncation_rule'])
    differences = {key: value['difference'] for key, value in histories.items()}
    clusters = len({streams[stream] for stream in differences})
    ci = cluster_bootstrap(differences, streams, plan['bootstrap'], contrast['id']) if differences else None
    t = cluster_t(differences, streams, fraction(plan['bootstrap']['confidence']))
    rates = missing['missing_fractions']
    status = 'computable'
    if missing['absent_or_not_run_pairs']:
        status = 'unreconciled_or_unexecuted_observations'
    elif any(value is None for query in queries.values() if _eligible(query, plan) for value in query['budget'].values()):
        status = 'budget_unobserved'
    elif clusters < 2:
        status = 'insufficient_independent_clusters'
    elif max(rates) > fraction(plan['max_missing_fraction']) or abs(rates[0]-rates[1]) > fraction(plan['max_missing_gap']):
        status = 'missingness_limit'
    sensitivity, _ = _paired_histories(plan, queries, indexed, contrast, 'exclude')
    paired_queries = sum(value['paired_queries'] for value in histories.values())
    return {'id': contrast['id'], 'role': contrast['role'], 'treatment': contrast['treatment'], 'control': contrast['control'],
            'histories': histories, 'independent_clusters': clusters, 'history_count': len(histories),
            'treatment_history_macro': math.fsum(v['treatment'] for v in histories.values())/len(histories) if histories else None,
            'control_history_macro': math.fsum(v['control'] for v in histories.values())/len(histories) if histories else None,
            'difference': math.fsum(differences.values())/len(differences) if differences else None,
            'query_weighted_difference_diagnostic': math.fsum(v['difference']*v['paired_queries'] for v in histories.values())/paired_queries
            if paired_queries else None,
            'bootstrap_ci': ci, 'cluster_t': t, 'inference_status': status, 'missingness': missing,
            'truncation_exclusion_sensitivity': {'history_count': len(sensitivity),
                'difference': math.fsum(v['difference'] for v in sensitivity.values())/len(sensitivity) if sensitivity else None}}


def _verdict(plan, row, family_complete):
    if row['inference_status'] != 'computable' or row['role'] == 'secondary' and not family_complete:
        return 'undetermined'
    ci, p = row['bootstrap_ci'], row['adjusted_p']
    alpha = 1 - fraction(plan['bootstrap']['confidence'])
    if ci[0] > fraction(plan['delta_min']) and p is not None and p <= alpha:
        return 'practical_improvement_supported'
    if ci[0] > 0 and p is not None and p <= alpha:
        return 'directional_improvement_only'
    margin = plan['equivalence_margin']
    if margin is not None and -fraction(margin) < ci[0] <= ci[1] < fraction(margin):
        return 'equivalence_interval_contained'
    if ci[1] < fraction(plan['delta_min']):
        return 'practical_gain_not_supported_not_equivalence'
    return 'undetermined'


def analyze(plan, schedule, observations, ledger):
    validate_plan(plan)
    # Final accepted run-index projection is a separate, still required handoff.
    # A self-declared stage or hash is never enough to promote an exploratory run.
    require(plan['stage'] != 'confirmatory', 'confirmatory_execution_evidence_required')
    return _analyze_validated_inputs(plan, schedule, observations, ledger)


def _analyze_validated_inputs(plan, schedule, observations, ledger):
    """Internal computation after the caller has validated execution handoff."""
    validate_plan(plan)
    queries, streams, jobs = validate_schedule(plan, schedule)
    require(len(streams)*plan['bootstrap']['replicates']*len(plan['contrasts']) <=
            CONTRACT['maximum_bootstrap_work'], 'bootstrap_work_limit')
    indexed = validate_observations(plan, schedule, queries, jobs, observations)
    costs = cost_summary(plan, schedule, jobs, ledger)
    rows = [analyze_contrast(plan, queries, streams, indexed, contrast) for contrast in plan['contrasts']]
    secondary = {row['id']: row['cluster_t']['p'] if row['cluster_t'] and row['inference_status'] == 'computable' else None
                 for row in rows if row['role'] == 'secondary'}
    adjusted, family_complete = holm(secondary), all(value is not None for value in secondary.values())
    for row in rows:
        row['adjusted_p'] = adjusted[row['id']] if row['role'] == 'secondary' else (
            row['cluster_t']['p'] if row['cluster_t'] else None)
        row['verdict'] = _verdict(plan, row, family_complete)
    return {'protocol': 'exp03-paired-analysis-v1', 'stage': plan['stage'], 'population': plan['population'],
            'experiment_family': plan['experiment_family'], 'primary_metric': plan['primary_metric'],
            'claim_scope': 'provided_inputs_only_not_product_adoption_or_causal_effect',
            'final_card_acceptance': False, 'analysis_plan_sha256': plan['plan_sha256'],
            'schedule_sha256': digest(schedule), 'observations_sha256': digest(observations), 'ledger_sha256': digest(ledger),
            'arms': arm_reconciliation(plan, queries, jobs, indexed), 'contrasts': rows, 'costs': costs,
            'multiple_comparisons': {'primary': 'unadjusted', 'secondary': 'Holm',
                                    'secondary_family_size': len(secondary), 'family_complete': family_complete},
            'analysis_changes': {'after_results': plan['analysis_changed_after_results'],
                                 'interim_looks': plan['interim_looks'], 'tuning_trials': plan['tuning_trials']},
            'row_table_sha256': digest(rows), 'cost_table_sha256': digest(costs)}


def _pairs(pairs):
    output = {}
    for key, value in pairs:
        require(key not in output, 'duplicate_json_key')
        output[key] = value
    return output


def _invalid_number(_):
    raise AnalysisError('invalid_json_number')


def parse(raw):
    require(len(raw) <= MAX_BYTES, 'input_too_large')
    try:
        value = json.loads(raw.decode('utf-8'), object_pairs_hook=_pairs,
                           parse_float=_invalid_number, parse_constant=_invalid_number)
    except (ValueError, UnicodeError, RecursionError):
        raise AnalysisError('invalid_json') from None
    stack, nodes = [(value, 0)], 0
    while stack:
        current, depth = stack.pop()
        nodes += 1
        require(depth <= 32 and nodes <= 2000000, 'input_structure_limit')
        if type(current) is str:
            require(len(current) <= 4096 and all(not 0xd800 <= ord(c) <= 0xdfff for c in current), 'invalid_json_string')
        if type(current) is dict:
            stack.extend((v, depth+1) for pair in current.items() for v in pair)
        elif type(current) is list:
            stack.extend((v, depth+1) for v in current)
        elif type(current) is int:
            require(-(2**63) <= current < 2**63, 'integer_out_of_range')
    return value


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise AnalysisError('invalid_arguments')


class Once(argparse.Action):
    def __call__(self, parser, namespace, value, option_string=None):
        require(getattr(namespace, self.dest) is None, 'duplicate_argument')
        setattr(namespace, self.dest, value)


def read_regular(path):
    fd = os.open(path, os.O_RDONLY | getattr(os, 'O_NONBLOCK', 0) | getattr(os, 'O_NOFOLLOW', 0))
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_size <= MAX_BYTES, 'invalid_input_file')
        return stream.read(MAX_BYTES+1)


def main(argv=None):
    parser = Parser(allow_abbrev=False)
    for name in ('plan', 'schedule', 'observations', 'ledger', 'out'):
        parser.add_argument('--'+name, required=True, action=Once)
    for name in ('attribution-views', 'attribution-labels'):
        parser.add_argument('--'+name, action=Once)
    try:
        args = parser.parse_args(argv)
        raw = {}
        for name in ('plan', 'schedule', 'observations', 'ledger'):
            raw[name] = read_regular(getattr(args, name))
        inputs = tuple(parse(raw[name]) for name in ('plan', 'schedule', 'observations', 'ledger'))
        result = analyze(*inputs)
        require(bool(args.attribution_views) == bool(args.attribution_labels), 'incomplete_attribution_arguments')
        if args.attribution_views:
            for name in ('attribution_views', 'attribution_labels'):
                raw[name] = read_regular(getattr(args, name))
            spec = importlib.util.spec_from_file_location('_exp03_cli_attribution', HERE / 'attribution_views.py')
            views = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(views)
            # The view module loads the same source under its own module name.
            # Keep its typed errors within this CLI's closed error vocabulary.
            try:
                result['attribution_sensitivity'] = views.analyze_views(
                    *inputs, parse(raw['attribution_views']), parse(raw['attribution_labels']))
            except views.A.AnalysisError as error:
                raise AnalysisError(error.code) from None
        result['provenance'] = {'raw_input_sha256': {name: sha(value) for name, value in raw.items()},
                                'analyzer_source_sha256': sha(Path(__file__).read_bytes()),
                                'frozen_ref_validator_sha256': sha(Path(REF_CONTRACT.__file__).read_bytes()),
                                'command': ['analyze_paired.py', '--plan', '<file>', '--schedule', '<file>',
                                            '--observations', '<file>', '--ledger', '<file>', '--out', '<new-file>']}
        if args.attribution_views:
            result['provenance']['attribution_source_sha256'] = sha((HERE / 'attribution_views.py').read_bytes())
            result['provenance']['command'].extend(['--attribution-views', '<file>', '--attribution-labels', '<file>'])
        output = canonical(result) + b'\n'
        fd = os.open(args.out, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'wb') as target:
            target.write(output)
        print(json.dumps({'protocol': result['protocol'], 'output_sha256': sha(output), 'status': 'computed_pending_independent_review'}))
        return 0
    except AnalysisError as error:
        code = error.code
    except OSError:
        code = 'file_io_error'
    except Exception:
        code = 'internal_error'
    print(json.dumps({'protocol': 'exp03-error-v1', 'code': code, 'path': '$'}))
    return 2


if __name__ == '__main__':
    raise SystemExit(main())

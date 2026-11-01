"""Paired evaluation views over one frozen execution, never policy re-execution.

View definitions and labels are explicit, separately hashed inputs. No default
attribution rule, scientific acceptance or label is inferred by this module.
"""
import copy
import importlib.util
import math
from pathlib import Path
import random


def _analyzer():
    spec = importlib.util.spec_from_file_location('_exp03_view_analyzer', Path(__file__).with_name('analyze_paired.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


A = _analyzer()


def execution_projection(observations):
    """Exclude evaluation labels, retaining all execution identities and effects."""
    return [{key: value for key, value in row.items() if key not in
             ('correct', 'grader_sha256', 'gold_sha256')} for row in observations['rows']]


def _definitions(plan, schedule, observations, ledger, definition):
    A.fields(definition, 'protocol state analysis_plan_sha256 schedule_sha256 observations_sha256 ledger_sha256 '
             'reference_view views labels_timing definition_sha256')
    A.require(definition['protocol'] == 'exp03-attribution-views-v1' and
              definition['state'] == 'sealed_computational_views', 'view_definition_unsealed')
    for key, expected in [('analysis_plan_sha256', plan['plan_sha256']), ('schedule_sha256', A.digest(schedule)),
                          ('observations_sha256', A.digest(observations)), ('ledger_sha256', A.digest(ledger))]:
        A.require(definition[key] == expected, 'view_source_mismatch')
    A.require(definition['definition_sha256'] == A.digest({k: v for k, v in definition.items()
                                                         if k != 'definition_sha256'}), 'view_definition_hash_mismatch')
    A.require(definition['labels_timing'] in ('before_results', 'after_results'), 'invalid_view_timing')
    views = definition['views']
    A.require(type(views) is list and 2 <= len(views) <= 8, 'invalid_view_count')
    ids = set()
    for view in views:
        A.fields(view, 'id criterion_sha256 evaluator_sha256')
        A.identifier(view['id'])
        A.hash_value(view['criterion_sha256'])
        A.hash_value(view['evaluator_sha256'])
        A.require(view['id'] not in ids, 'duplicate_view')
        ids.add(view['id'])
    A.require(definition['reference_view'] in ids, 'unknown_reference_view')
    return views


def _labels(definition, labels, queries, jobs, indexed, metric):
    A.fields(labels, 'protocol definition_sha256 views')
    A.require(labels['protocol'] == 'exp03-attribution-labels-v1' and
              labels['definition_sha256'] == definition['definition_sha256'], 'view_labels_binding_mismatch')
    A.require(type(labels['views']) is list and len(labels['views']) == len(definition['views']),
              'view_labels_coverage_mismatch')
    seen, output = set(), {}
    for item in labels['views']:
        A.fields(item, 'id source_sha256 queries grades')
        A.require(item['id'] in {v['id'] for v in definition['views']} and item['id'] not in seen,
                  'view_labels_coverage_mismatch')
        seen.add(item['id'])
        A.hash_value(item['source_sha256'])
        A.require(type(item['queries']) is list and len(item['queries']) == len(queries), 'view_query_coverage_mismatch')
        qlabels = {}
        for query in item['queries']:
            A.fields(query, 'stream_id query_id available kind gold_sets')
            key = (query['stream_id'], query['query_id'])
            A.require(key in queries and key not in qlabels and type(query['available']) is bool,
                      'view_query_coverage_mismatch')
            if query['available']:
                A.require(query['kind'] in ('answerable', 'clear_negative', 'ambiguous'), 'invalid_query_kind')
                A.require(type(query['gold_sets']) is list, 'invalid_gold_sets')
                for alternative in query['gold_sets']:
                    A.require(bool(A.ref_set(alternative)), 'empty_gold_alternative')
                A.require((query['kind'] == 'answerable') == bool(query['gold_sets']), 'invalid_gold_sets')
                if metric == 'provided_grader_accuracy':
                    A.require(query['kind'] == queries[key]['kind'] and query['gold_sets'] == queries[key]['gold_sets'],
                              'accuracy_view_cannot_change_query_population')
            else:
                A.require(query['kind'] is None and query['gold_sets'] is None, 'unavailable_label_has_value')
            qlabels[key] = query
        A.require(type(item['grades']) is list and len(item['grades']) == len(jobs), 'view_grade_coverage_mismatch')
        grades = {}
        for grade in item['grades']:
            A.fields(grade, 'stream_id query_id policy_id repetition correct')
            key = (grade['stream_id'], grade['query_id'], grade['policy_id'], grade['repetition'])
            A.require(type(grade['repetition']) is int and key in jobs and key not in grades,
                      'view_grade_coverage_mismatch')
            A.require(grade['correct'] is None or type(grade['correct']) is bool, 'invalid_correctness')
            row = indexed.get(key)
            if metric == 'recall_at_k' or row is None or row['status'] != 'success':
                A.require(grade['correct'] is None, 'grade_without_successful_execution')
            grades[key] = grade['correct']
        output[item['id']] = (item, qlabels, grades)
    return output


def analyze_views(plan, schedule, observations, ledger, definition, labels):
    # Public inputs alone cannot establish confirmatory execution evidence.
    return _analyze_views(plan,schedule,observations,ledger,definition,labels,A.analyze)


def _analyze_views(plan, schedule, observations, ledger, definition, labels, evaluate):
    """Keep the full schedule, even when a view has unavailable labels.

    An incomplete view is reported as uncomputable, never a favorable selected
    complete-case subset. Bootstrap settings, cluster IDs and all costs stay fixed.
    """
    base = evaluate(plan, schedule, observations, ledger)
    queries, _, jobs = A.validate_schedule(plan, schedule)
    indexed = A.validate_observations(plan, schedule, queries, jobs, observations)
    definitions = _definitions(plan, schedule, observations, ledger, definition)
    A.require(len(definitions) * len(queries) * plan['replications'] <= A.CONTRACT['maximum_rows'],
              'view_work_limit')
    histories = len({q['stream_id'] for q in queries.values()})
    A.require(((2*len(definitions)+1)*len(plan['contrasts'])+len(definitions)*len(plan['policies'])**2)*
              histories*plan['bootstrap']['replicates'] <=
              A.CONTRACT['maximum_bootstrap_work'], 'view_work_limit')
    all_labels = _labels(definition, labels, queries, jobs, indexed, plan['primary_metric'])
    original_execution = A.digest(execution_projection(observations))
    results = []
    for view in definitions:
        item, qlabels, grades = all_labels[view['id']]
        missing_queries = sum(not q['available'] for q in qlabels.values())
        missing_grades = sum(row['status'] == 'success' and grades[key] is None for key, row in indexed.items()
                             ) if plan['primary_metric'] == 'provided_grader_accuracy' else 0
        result = dict(id=view['id'], criterion_sha256=view['criterion_sha256'],
                      evaluator_sha256=view['evaluator_sha256'], label_source_sha256=item['source_sha256'],
                      planned_rows=len(jobs), recorded_execution_rows=len(indexed),
                      unavailable_query_labels=missing_queries, unavailable_success_grades=missing_grades,
                      execution_sha256=original_execution, cost_table_sha256=base['cost_table_sha256'])
        if missing_queries or missing_grades:
            result.update(status='uncomputable_missing_labels', analysis=None)
        else:
            result.update(status='computed', analysis=_project_view(plan, schedule, observations, ledger,
                                                                   definition, view, qlabels, grades,evaluate))
            A.require(result['analysis']['cost_table_sha256'] == base['cost_table_sha256'], 'view_cost_changed')
        results.append(result)
    reference = next(r for r in results if r['id'] == definition['reference_view'])
    comparisons = []
    for result in results:
        if result is reference:
            continue
        comparisons.append(_comparison(plan, schedule, all_labels, reference, result))
    return dict(protocol='exp03-attribution-sensitivity-v1', definition_sha256=definition['definition_sha256'],
                labels_sha256=A.digest(labels), frozen_execution_sha256=original_execution,
                frozen_observations_sha256=A.digest(observations), frozen_ledger_sha256=A.digest(ledger),
                stage=plan['stage'], population=plan['population'],
                exploratory=plan['stage'] != 'confirmatory' or plan['analysis_changed_after_results'] or
                definition['labels_timing'] == 'after_results',
                claim_scope='same_frozen_trace_evaluation_sensitivity_not_closed_loop_or_causal_evidence',
                cross_view_inference='paired_history_change_ci_no_additional_hypothesis_test',
                reference_view=definition['reference_view'], views=results, comparisons=comparisons)


def _project_view(plan, schedule, observations, ledger, definition, view, queries, grades, evaluate):
    vp, vs, vo, vl = copy.deepcopy((plan, schedule, observations, ledger))
    vp['analysis_changed_after_results'] |= definition['labels_timing'] == 'after_results'
    if vp['primary_metric'] == 'provided_grader_accuracy':
        vp['grader_sha256'] = view['evaluator_sha256']
    vp['plan_sha256'] = A.digest({k: v for k, v in vp.items() if k != 'plan_sha256'})
    vs['analysis_plan_sha256'] = vp['plan_sha256']
    for query in vs['queries']:
        label = queries[(query['stream_id'], query['query_id'])]
        query.update(kind=label['kind'], gold_sets=copy.deepcopy(label['gold_sets']))
    for row in vo['rows']:
        key = (row['stream_id'], row['query_id'], row['policy_id'], row['repetition'])
        row.update(correct=grades[key], grader_sha256=vp['grader_sha256'],
                   gold_sha256=A.digest(queries[key[:2]]['gold_sets']))
    vo['schedule_sha256'] = vl['schedule_sha256'] = A.digest(vs)
    A.require(execution_projection(vo) == execution_projection(observations), 'view_execution_changed')
    result = evaluate(vp, vs, vo, vl)
    result['policy_rank_uncertainty'] = _rank_uncertainty(vp, vs, vo, view['id'])
    return result


def _rank_uncertainty(plan, schedule, observations, view_id):
    """Joint all-policy ranking on every eligible query, with exact ties retained.

    No success-only subset or extra hypotheses: bootstrap order frequencies are
    descriptive resampling frequencies, never posterior probabilities/p-values.
    """
    queries, clusters, jobs = A.validate_schedule(plan, schedule)
    indexed = A.validate_observations(plan, schedule, queries, jobs, observations)
    eligible = [q for q in queries.values() if A._eligible(q, plan)]
    result = dict(status='undetermined', eligible_queries=len(eligible), invalid_queries=0,
                  rule='all_policies_common_valid_repetitions_then_query_then_equal_history',
                  interpretation='bootstrap_order_frequency_not_posterior_probability_or_hypothesis_test',
                  policy_rank_ranges=[], pair_orders=[])
    history_entries = {}
    for query in eligible:
        values = {p: [] for p in plan['policies']}
        for repetition in range(plan['replications']):
            current = {p: A.score(query, indexed.get((query['stream_id'], query['query_id'], p, repetition)),
                                  plan['primary_metric'], plan['truncation_rule']) for p in plan['policies']}
            if all(v is not None for v in current.values()):
                for p, value in current.items():
                    values[p].append(value)
        if len(next(iter(values.values()))) < plan['minimum_valid_replications']:
            result['invalid_queries'] += 1
        else:
            history_entries.setdefault(query['stream_id'], []).append({
                p: math.fsum(values[p])/len(values[p]) for p in plan['policies']})
    if result['invalid_queries'] or not eligible:
        result['status'] = 'uncomputable_incomplete_joint_population'
        return result
    if any(v is None for q in eligible for v in q['budget'].values()):
        result['status'] = 'uncomputable_budget_unknown'
        return result
    histories = {}
    for stream, entries in sorted(history_entries.items()):
        histories[stream] = {p: math.fsum(v[p] for v in entries)/len(entries) for p in plan['policies']}
    grouped = {}
    for stream in histories:
        grouped.setdefault(clusters[stream], []).append(stream)
    ids = sorted(grouped)
    result.update(history_count=len(histories), independent_clusters=len(ids))
    if len(ids) < 2:
        result['status'] = 'insufficient_independent_clusters'
        return result
    policies = plan['policies']
    rng = random.Random(str(plan['bootstrap']['seed'])+'|attribution-rank/'+view_id)
    ranks = {p: ([], []) for p in policies}
    pairs = {(p, q): [0, 0, 0] for i, p in enumerate(policies) for q in policies[i+1:]}
    repeats = plan['bootstrap']['replicates']
    for _ in range(repeats):
        selected = [s for _ in ids for s in grouped[rng.choice(ids)]]
        values = {p: math.fsum(histories[s][p] for s in selected)/len(selected) for p in policies}
        for p in policies:
            ranks[p][0].append(1+sum(v > values[p] for v in values.values()))
            ranks[p][1].append(sum(v >= values[p] for v in values.values()))
        for (p, q), counts in pairs.items():
            counts[0 if values[p] > values[q] else 1 if values[p] < values[q] else 2] += 1
    tail = (1-A.fraction(plan['bootstrap']['confidence']))/2
    result['policy_rank_ranges'] = [dict(policy_id=p, lower_best_rank=A._percentile(ranks[p][0], tail,
        plan['bootstrap']['percentile']), upper_worst_rank=A._percentile(ranks[p][1], 1-tail,
        plan['bootstrap']['percentile'])) for p in policies]
    result['pair_orders'] = [dict(first=p, second=q, first_above=counts[0]/repeats,
                                second_above=counts[1]/repeats, tied=counts[2]/repeats) for (p, q), counts in pairs.items()]
    result.update(status='computed_descriptive', bootstrap_replicates=repeats,
                  tie_rule='retain_best_to_worst_tied_rank_no_lexical_break')
    return result


def _comparison(plan, schedule, labels, reference, result):
    output = dict(reference_view=reference['id'], view=result['id'], contrasts=[])
    if reference['analysis'] is None or result['analysis'] is None:
        output['status'] = 'uncomputable_missing_labels'
        return output
    # A change of answerable/negative/ambiguous populations is not a paired
    # evaluation difference. Retain each view's own tables, withhold the change.
    original_queries = labels[reference['id']][1]
    new_queries = labels[result['id']][1]
    changed = sum(original_queries[key]['kind'] != new_queries[key]['kind'] for key in original_queries)
    output['changed_query_kinds'] = changed
    if changed:
        output['status'] = 'uncomputable_changed_query_population'
        return output
    output['status'] = 'computed_descriptive'
    clusters = {q['stream_id']: q['cluster_id'] for q in schedule['queries']}
    for old, new in zip(reference['analysis']['contrasts'], result['analysis']['contrasts']):
        A.require(old['id'] == new['id'], 'view_contrast_changed')
        a, b = old['difference'], new['difference']
        A.require(set(old['histories']) == set(new['histories']), 'view_pair_population_changed')
        changes = {s: new['histories'][s]['difference']-old['histories'][s]['difference'] for s in old['histories']}
        ci = A.cluster_bootstrap(changes, clusters, plan['bootstrap'],
                                 'view-change/'+reference['id']+'/'+result['id']+'/'+old['id']) if changes else None
        output['contrasts'].append(dict(id=old['id'], reference_difference=a, view_difference=b,
                                       difference_change=None if a is None or b is None else b-a,
                                       reference_bootstrap_ci=old['bootstrap_ci'], view_bootstrap_ci=new['bootstrap_ci'],
                                       paired_change_bootstrap_ci=ci,
                                       paired_change_independent_clusters=len({clusters[s] for s in changes}),
                                       paired_change_histories=len(changes),
                                       inference_status='computable' if all(row['inference_status'] == 'computable'
                                                                         for row in (old, new)) else 'underlying_view_unresolved',
                                       reference_order=_order(old), view_order=_order(new)))
    return output


def _order(row):
    """Uncertainty of the declared policy contrast; no invented total ranking."""
    ci = row['bootstrap_ci']
    if row['inference_status'] != 'computable' or ci is None:
        return 'undetermined'
    if ci[0] > 0:
        return 'treatment_above_control'
    if ci[1] < 0:
        return 'control_above_treatment'
    return 'order_unresolved'

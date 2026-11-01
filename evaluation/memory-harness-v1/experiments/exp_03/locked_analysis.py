"""Bind an immutable EXP01 lock to typed analysis and the actual final-test rows.

This is a computational preflight, not scientific approval, evidence of timing,
permission to execute, or a replacement for the native execution handoff.
"""
from fractions import Fraction
import importlib.util
import json
import os
from pathlib import Path

HERE = Path(__file__).resolve().parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


A = load('_exp03_locked_analysis', HERE / 'analyze_paired.py')
L = load('_exp03_exp01_final_protocol', HERE.with_name('exp_01') / 'protocol_lock_v2.py')
V = load('_exp03_final_split_validation', HERE.with_name('exp_01') / 'validate_splits.py')
G = L.G
NAMES = ('lock', 'values', 'manifest', 'events', 'queries', 'gold', 'analysis_plan', 'schedule', 'cluster_assignments', 'analysis_source')


def source(call, *args):
    try:
        return call(*args)
    except (G.ExpError, G.IO.ContractError) as error:
        raise A.AnalysisError('source_' + error.code) from None


def numeric(value):
    # EXP01 has already validated exact decimal/fraction/exponent bounds.
    return Fraction(value)


def bind(raw, analysis_timing):
    A.require(type(raw) is dict and set(raw) == set(NAMES) and all(type(v) is bytes for v in raw.values()),
              'invalid_binding_inputs')
    A.require(sum(map(len, raw.values())) <= 268435456, 'binding_input_total_limit')
    verified = source(L.verify_lock, raw['lock'], raw['values'], raw['manifest'])
    lock = source(G.parse_raw, raw['lock'])
    manifest = source(G.decode_typed, raw['manifest'], 'SplitManifest')
    for name in ('events', 'queries', 'gold'):
        A.require(manifest['files'][name + '_sha256'] == A.sha(raw[name]), 'locked_dataset_hash_mismatch')
    rows = {name: source(V._rows, raw[name], kind) for name, kind in
            [('events', 'PlanEvent'), ('queries', 'QueryPlan'), ('gold', 'GoldRow')]}
    source(V.validate_artifacts, rows['events'], rows['queries'], rows['gold'], manifest)
    plan, schedule = A.parse(raw['analysis_plan']), A.parse(raw['schedule'])
    A.validate_plan(plan)
    queries, streams, _ = A.validate_schedule(plan, schedule)
    A.require(plan['stage'] in ('exploratory', 'confirmatory'), 'locked_analysis_stage_required')
    A.require(analysis_timing in ('before_results', 'after_results'), 'invalid_analysis_timing')
    A.require(analysis_timing != 'after_results' or plan['analysis_changed_after_results'], 'posthoc_analysis_change_required')
    A.require(plan['stage'] != 'confirmatory' or (analysis_timing == 'before_results' and
              not plan['analysis_changed_after_results']), 'posthoc_confirmatory_forbidden')
    analysis_source = A.parse(raw['analysis_source'])
    A.fields(analysis_source, 'protocol queries_raw_sha256 gold_raw_sha256')
    A.require(analysis_source == dict(protocol='exp03-analysis-source-v1',queries_raw_sha256=A.sha(raw['queries']),
              gold_raw_sha256=A.sha(raw['gold'])), 'locked_analysis_source_mismatch')
    research = lock['research']
    A.require(numeric(plan['delta_min']) == numeric(research['delta_min']), 'locked_delta_mismatch')
    A.require(plan['replications'] == research['repetitions'], 'locked_replications_mismatch')
    A.require(plan['tuning_trials'] == research['tuning_trials'], 'locked_tuning_mismatch')
    A.require(len(set(plan['policies']) - {'REF'}) <= research['candidate_count'], 'locked_candidate_count_mismatch')
    test_entries = {e['stream_id']: e for e in manifest['entries'] if e['split'] == 'final_test'}
    A.require(set(streams) == set(test_entries), 'final_test_stream_coverage_mismatch')
    A.require(len(set(streams.values())) == research['sample_size']['test_clusters'], 'locked_test_cluster_count_mismatch')
    validation_entries = [e for e in manifest['entries'] if e['split'] == 'validation']
    assignments = A.parse(raw['cluster_assignments'])
    A.fields(assignments, 'protocol manifest_raw_sha256 assignments')
    A.require(assignments['protocol'] == 'exp03-final-cluster-assignments-v1' and
              assignments['manifest_raw_sha256'] == A.sha(raw['manifest']) and
              type(assignments['assignments']) is list, 'invalid_cluster_assignments')
    assigned = {}
    for row in assignments['assignments']:
        A.fields(row, 'stream_id cluster_id')
        A.identifier(row['stream_id'])
        A.identifier(row['cluster_id'])
        A.require(row['stream_id'] not in assigned, 'duplicate_cluster_assignment')
        assigned[row['stream_id']] = row['cluster_id']
    A.require(set(assigned) == {e['stream_id'] for e in manifest['entries']}, 'cluster_assignment_coverage_mismatch')
    split_by_cluster = {}
    for entry in manifest['entries']:
        cluster = assigned[entry['stream_id']]
        A.require(split_by_cluster.setdefault(cluster, entry['split']) == entry['split'], 'cluster_assignment_split_overlap')
    A.require(all(assigned[s] == cluster for s,cluster in streams.items()), 'locked_cluster_assignment_mismatch')
    if plan['cluster_unit'] == 'history':
        A.require(all(s == cluster for s,cluster in assigned.items()), 'locked_cluster_assignment_mismatch')
    # The partition is provided and hash-bound, never inferred from outcomes or
    # substituted with a convenient root/session/topic grouping by this tool.
    validation_clusters = {assigned[e['stream_id']] for e in validation_entries}
    A.require(len(validation_clusters) == research['sample_size']['validation_clusters'], 'locked_validation_cluster_count_mismatch')
    source_queries = {(q['stream_id'], q['query_id']): q for q in rows['queries']
                      if q['stream_id'] in test_entries and ('current' if q['request_mode'] == 'current' else 'as_of') in plan['request_modes']}
    A.require(set(queries) == set(source_queries), 'final_test_query_coverage_mismatch')
    gold = {(g['stream_id'], g['query_id']): g for g in rows['gold']}
    alternatives = 0
    for key, query in queries.items():
        original, label = source_queries[key], gold[key]
        A.require(query['scope'] == original['scope'] and query['request_mode'] == ('current' if original['request_mode'] == 'current' else 'as_of'),
                  'locked_query_scope_mismatch')
        kind = ('answerable' if label['cohort'] == 'primary_answerable' else
                'clear_negative' if label['cohort'] == 'clear_negative' else 'ambiguous')
        expected_gold = [label['required_refs']] if kind == 'answerable' else []
        A.require(query['kind'] == kind and query['gold_sets'] == expected_gold, 'locked_gold_mismatch')
        alternatives += label['cohort'] == 'alternative_answers'
        A.require(query['input_sha256'] == A.sha(raw['analysis_source']), 'locked_analysis_source_mismatch')
        budget = query['budget']
        A.require(budget['final_k'] == research['k'], 'locked_final_k_mismatch')
        for name in ('max_input_tokens', 'max_output_tokens'):
            if name in research['token_budget']:
                A.require(budget[name] == research['token_budget'][name], 'locked_token_budget_mismatch')
        if 'max_total_tokens' in research['token_budget']:
            A.require(budget['max_input_tokens'] is not None and budget['max_output_tokens'] is not None and
                      budget['max_input_tokens'] + budget['max_output_tokens'] <= research['token_budget']['max_total_tokens'],
                      'locked_token_budget_mismatch')
    result = dict(protocol='exp03-locked-analysis-binding-v1', status='computational_inputs_matched',
                  experiment_family=plan['experiment_family'], analysis_plan_sha256=plan['plan_sha256'],
                  schedule_sha256=A.digest(schedule), protocol_lock_sha256=verified['lock_sha256'],
                  raw_input_sha256={name: A.sha(value) for name, value in raw.items()},
                  source_dataset=dict(dataset_id=manifest['dataset_id'], dataset_version=manifest['dataset_version'],
                                      label_version=manifest['label_version'], split_assignment_sha256=manifest['split_assignment_sha256']),
                  counts=dict(test_histories=len(streams), test_clusters=len(set(streams.values())),
                              validation_clusters=len(validation_clusters), queries=len(queries), alternative_queries=alternatives),
                  analysis_timing=analysis_timing, timing_authority='caller_declaration_not_attested', cluster_assignment_timing='provided_not_attested',
                  human_semantic_review_required=True, native_execution_reconciliation_required=True,
                  verification_scope='dataset_and_numeric_plan_binding_not_semantic_research_approval',
                  independent_gold_review_status=manifest['review']['status'],
                  scientific_approval_verified=False, execution_authorized=False, full_card_acceptance=False)
    result['binding_sha256'] = A.digest(result)
    return result


def projection_rule_sha256(projection):
    # Run outcomes and grader judgments become available after execution. The
    # predeclared rule binds the verifier, policy, schedule and ordered slots,
    # without pretending the future result bytes existed before the run.
    return A.digest({k:v for k,v in projection.items() if k not in ('run_index_raw_sha256','grader_artifact')})


def review_final_inputs(raw, gold_review_raw, owner_review_raw, trusted_owner_review_sha256, run_plan_raw, projection):
    """Verify externally pinned review declarations, not a self-approved payload."""
    A.hash_value(trusted_owner_review_sha256)
    A.require(A.sha(owner_review_raw) == trusted_owner_review_sha256, 'trusted_owner_review_mismatch')
    binding = bind(raw, 'before_results')
    plan = A.parse(raw['analysis_plan'])
    A.require(plan['stage'] == 'confirmatory' and not plan['analysis_changed_after_results'], 'confirmatory_plan_required')
    A.require(projection['selection_rule']=='first_completed_else_last_attempted','primary_attempt_selection_policy_mismatch')
    manifest = source(G.decode_typed,raw['manifest'],'SplitManifest')
    gold = source(V._rows,raw['gold'],'GoldRow')
    reviewed = source(G.decode_typed,gold_review_raw,'ReviewRecord')
    # Review is a separate sidecar. Never rewrite an original pending manifest
    # merely to make its review state look completed.
    reference = dict(reviewer_id=reviewed['reviewer_id'],reviewed_gold_sha256=A.sha(raw['gold']))
    data = {name+'.jsonl':raw[name] for name in ('events','queries','gold')}
    source(V._review_record,reviewed,reference,manifest,gold,data)
    owner = A.parse(owner_review_raw)
    A.fields(owner,'protocol reviewer_id role decision reviewed_at analysis_plan_sha256 schedule_sha256 locked_binding_sha256 '
             'run_plan_raw_sha256 projection_rule_sha256 independent_gold_review_raw_sha256 review_scope')
    A.require(owner['protocol'] == 'exp03-owner-analysis-review-v1' and owner['role'] == 'research_owner' and
              owner['decision'] == 'accepted_mapping' and
              owner['review_scope'] == 'semantic_analysis_mapping_not_execution_or_product_authority', 'owner_review_scope_mismatch')
    A.identifier(owner['reviewer_id'])
    try:
        A.require(type(owner['reviewed_at']) is str and A.date.fromisoformat(owner['reviewed_at']).isoformat() == owner['reviewed_at'],
                  'owner_review_date_invalid')
    except ValueError:
        raise A.AnalysisError('owner_review_date_invalid') from None
    expected = dict(analysis_plan_sha256=plan['plan_sha256'],schedule_sha256=A.digest(A.parse(raw['schedule'])),
                    locked_binding_sha256=binding['binding_sha256'],run_plan_raw_sha256=A.sha(run_plan_raw),
                    projection_rule_sha256=projection_rule_sha256(projection),independent_gold_review_raw_sha256=A.sha(gold_review_raw))
    A.require(all(owner[k] == v for k,v in expected.items()), 'owner_review_binding_mismatch')
    return dict(protocol='exp03-final-input-review-binding-v1',computational_binding=binding,
                owner_review_raw_sha256=trusted_owner_review_sha256,gold_review_raw_sha256=A.sha(gold_review_raw),
                owner_review_status='externally_pinned_declaration_not_authenticated_person',
                independent_gold_review_status='original_record_joins_verified_not_label_truth',
                projection_rule_sha256=expected['projection_rule_sha256'],
                scientific_approval_verified=False,execution_authorized=False,product_authority_verified=False)


def verify(record, raw):
    A.require(type(record) is dict and 'analysis_timing' in record, 'invalid_binding_record')
    expected = bind(raw, record['analysis_timing'])
    A.require(A.canonical(record) == A.canonical(expected), 'locked_analysis_binding_mismatch')
    return expected


def main(argv=None):
    parser = A.Parser(allow_abbrev=False)
    for name in NAMES:
        parser.add_argument('--' + name.replace('_', '-'), required=True, action=A.Once)
    parser.add_argument('--analysis-timing', required=True, action=A.Once)
    parser.add_argument('--out', required=True, action=A.Once)
    parser.add_argument('--verify-binding', action=A.Once)
    try:
        args = parser.parse_args(argv)
        raw = {name: source(G.bounded_read, Path(getattr(args, name))) for name in NAMES}
        result = bind(raw, args.analysis_timing)
        if args.verify_binding is not None:
            record = A.parse(source(G.bounded_read, Path(args.verify_binding)))
            A.require(record.get('analysis_timing') == args.analysis_timing, 'locked_analysis_binding_mismatch')
            result = verify(record, raw)
        A.require(all(source(G.bounded_read, Path(getattr(args, name))) == data for name, data in raw.items()), 'binding_input_changed')
        output = Path(args.out)
        A.require(output.is_absolute() and '..' not in output.parts and output.name not in ('', '.', '..'), 'unsafe_output_path')
        parent = source(G.IO._directory_fd, source(G.checked_directory, output.parent))
        identity = None
        try:
            fd = os.open(output.name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent)
            with os.fdopen(fd, 'wb') as stream:
                info = os.fstat(stream.fileno())
                identity = (info.st_dev, info.st_ino)
                stream.write(A.canonical(result) + b'\n')
        except BaseException:
            if identity is not None:
                current = os.stat(output.name, dir_fd=parent, follow_symlinks=False)
                if (current.st_dev, current.st_ino) == identity:
                    os.unlink(output.name, dir_fd=parent)
            raise
        finally:
            os.close(parent)
        print(json.dumps(dict(protocol=result['protocol'], status=result['status'], binding_sha256=result['binding_sha256'],
                              execution_authorized=False)))
        return 0
    except A.AnalysisError as error:
        code = error.code
    except OSError:
        code = 'file_io_error'
    except Exception:
        code = 'internal_error'
    print(json.dumps(dict(protocol='exp03-locked-analysis-error-v1', code=code, path='$')))
    return 2


if __name__ == '__main__':
    raise SystemExit(main())

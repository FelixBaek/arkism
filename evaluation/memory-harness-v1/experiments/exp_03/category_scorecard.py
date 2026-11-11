"""Per-category binary accuracy diagnostic over EXP03 primary observations.

Never selects attempts, grades answers, authenticates holdout status or grants
scientific acceptance. Native projection remains responsible for the execution
ledger, earliest completed attempt and provenance/owner-review verification.
"""
import importlib.util
from pathlib import Path
import sys

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location('_category_analysis', HERE / 'analyze_paired.py')
A = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(A)


def category_map(raw, trusted_sha256, schedule, queries):
    A.require(A.sha(raw) == A.hash_value(trusted_sha256), 'category_map_pin_mismatch')
    mapping = A.parse(raw)
    A.fields(mapping, 'protocol schedule_sha256 categories assignments')
    A.require(mapping['protocol'] == 'exp03-category-map-v1' and
              mapping['schedule_sha256'] == A.digest(schedule), 'category_schedule_mismatch')
    categories = mapping['categories']
    A.require(type(categories) is list and 0 < len(categories) <= 100, 'invalid_categories')
    A.require(len({A.identifier(c) for c in categories}) == len(categories), 'duplicate_category')
    A.require(type(mapping['assignments']) is list and len(mapping['assignments']) == len(queries),
              'category_coverage_mismatch')
    assigned = {}
    for row in mapping['assignments']:
        A.fields(row, 'stream_id query_id category')
        key = (A.identifier(row['stream_id']), A.identifier(row['query_id']))
        A.require(key in queries and key not in assigned and row['category'] in categories,
                  'category_identity_mismatch')
        assigned[key] = row['category']
    A.require(set(assigned.values()) == set(categories), 'empty_category')
    return categories, assigned


def report(plan, schedule, observations, mapping_raw, trusted_mapping_sha256):
    A.validate_plan(plan)
    A.require(plan['primary_metric'] == 'provided_grader_accuracy', 'binary_accuracy_required')
    queries, _, jobs = A.validate_schedule(plan, schedule)
    indexed = A.validate_observations(plan, schedule, queries, jobs, observations)
    categories, assigned = category_map(mapping_raw, trusted_mapping_sha256, schedule, queries)
    arms = {}
    for policy in plan['policies']:
        counts = {c: dict(scheduled=0, correct=0, status_counts={}) for c in categories}
        for key in jobs:
            if key[2] != policy:
                continue
            item, row = counts[assigned[key[:2]]], indexed.get(key)
            status = row['status'] if row else 'missing'
            item['scheduled'] += 1
            item['correct'] += int(bool(row and status == 'success' and row['correct']))
            item['status_counts'][status] = item['status_counts'].get(status, 0) + 1
        for item in counts.values():
            n, correct = item['scheduled'], item['correct']
            item.update(accuracy=correct / n, required_correct=(9*n+9)//10,
                        numerical_threshold_met=10*correct >= 9*n)
            item['additional_correct_needed'] = max(0, item['required_correct']-correct)
        arms[policy] = dict(categories=counts, all_categories_numerically_at_least_90=
                            all(c['numerical_threshold_met'] for c in counts.values()))
    return dict(protocol='exp03-category-scorecard-v1', arms=arms,
                status='computational_diagnostic_only', scientific_acceptance=False,
                holdout_independence_verified=False, native_attempt_selection_verified=False,
                category_mapping_timing='externally_pinned_not_attested',
                threshold='9/10', denominator='all_scheduled_queries_and_replications',
                category_map_raw_sha256=A.sha(mapping_raw), schedule_sha256=A.digest(schedule),
                analysis_plan_sha256=plan['plan_sha256'], observations_sha256=A.digest(observations),
                source_sha256={p.name: A.sha(p.read_bytes()) for p in
                               (Path(__file__), HERE / 'analyze_paired.py')})


def main(argv=None):
    parser = A.Parser(allow_abbrev=False)
    for name in ('plan', 'schedule', 'observations', 'category-map', 'trusted-category-map-sha256'):
        parser.add_argument('--'+name, required=True, action=A.Once)
    try:
        args = parser.parse_args(argv)
        result = report(*(A.parse(A.read_regular(getattr(args, name))) for name in
                          ('plan', 'schedule', 'observations')),
                        A.read_regular(args.category_map), args.trusted_category_map_sha256)
        print(A.canonical(result).decode())
        return 0
    except (A.AnalysisError, OSError):
        print('{"status":"invalid_inputs"}', file=sys.stderr)
        return 2


if __name__ == '__main__':
    raise SystemExit(main())

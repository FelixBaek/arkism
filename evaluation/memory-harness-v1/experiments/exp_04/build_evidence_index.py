"""EXP-04 file-only evidence index; prepares decisions, never approves them."""
import argparse
import csv
from datetime import date
from collections import defaultdict
from decimal import Decimal, localcontext
import importlib.util
import hashlib
import io
import json
import math
import os
from pathlib import Path
import re
import stat

SOURCE_BYTES=Path(__file__).read_bytes()
MAX_BYTES, MAX_ARTIFACTS, MAX_TOTAL_BYTES = 67108864, 256, 268435456
ID = re.compile(r'[A-Za-z0-9_.:-]{1,128}\Z', re.ASCII)
SHA = re.compile(r'[0-9a-f]{64}\Z', re.ASCII)
COMMIT = re.compile(r'[0-9a-f]{40}\Z', re.ASCII)
ROLES = {'protocol_lock', 'run_index', 'run_plan', 'raw_result', 'analysis', 'analysis_plan',
         'cost_ledger', 'baseline', 'ablation', 'failure_trace', 'boundary', 'system_acceptance',
         'review_record', 'normal_utility', 'strong_baseline', 'independence', 'bottleneck',
         'schedule', 'projection_plan', 'observations', 'native_verification', 'final_inputs', 'source_code', 'native_program', 'attribution_inputs'}
BOUNDARIES = ('authority', 'approval', 'revision', 'source', 'deletion', 'restore', 'scope', 'side_effect')
COVERAGE = set(BOUNDARIES) | {'normal_utility', 'full_cost', 'cost_budget', 'SYS06', 'protocol_lock',
                            'strong_nearest_baseline', 'B6', 'matched_write', 'time_confound', 'answer_quality'}
PRODUCT_GATES = set(BOUNDARIES) | {'normal_utility', 'full_cost', 'cost_budget', 'SYS06'}
STATUSES = {'success', 'truncated', 'retrieval_failure', 'packing_loss', 'fence_rejected', 'generator_failure',
            'checker_failure', 'api_error', 'timeout', 'not_run', 'missing'}


class EvidenceError(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def require(condition, code):
    if not condition:
        raise EvidenceError(code)


def fields(value, names):
    require(type(value) is dict and set(value) == set(names.split()), 'invalid_fields')


def ident(value):
    require(type(value) is str and ID.fullmatch(value) is not None, 'invalid_id')


def hashed(value):
    require(type(value) is str and SHA.fullmatch(value) is not None, 'invalid_hash')


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def canonical(value):
    return json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()


def _pairs(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'duplicate_json_key')
        result[key] = value
    return result


def parse(raw):
    require(len(raw) <= MAX_BYTES, 'input_too_large')
    try:
        value = json.loads(raw.decode('utf8'), object_pairs_hook=_pairs,
                           parse_constant=lambda _: (_ for _ in ()).throw(EvidenceError('nonfinite_number')))
    except (ValueError, UnicodeError, RecursionError):
        raise EvidenceError('invalid_json') from None
    pending, nodes = [(value, 0)], 0
    while pending:
        item, depth = pending.pop()
        nodes += 1
        require(depth <= 32 and nodes <= 2000000, 'structure_limit')
        if type(item) is dict:
            pending.extend((v, depth+1) for pair in item.items() for v in pair)
        elif type(item) is list:
            require(len(item) <= 100000, 'row_limit')
            pending.extend((v, depth+1) for v in item)
        elif type(item) is float:
            require(math.isfinite(item), 'nonfinite_number')
        elif type(item) is int:
            require(-(2**63) <= item < 2**63, 'integer_limit')
        elif type(item) is str:
            require((len(item) <= 4096 or len(item) <= 4102 and
                     re.fullmatch(r'(?:0|[1-9][0-9]*)(?:\.[0-9]+)?', item, re.ASCII)) and
                    all(not 0xd800 <= ord(c) <= 0xdfff for c in item), 'invalid_string')
    return value


def _parts(value, absolute=False):
    require(type(value) is str and '\\' not in value and '\x00' not in value and
            value.startswith('/') == absolute, 'unsafe_path')
    parts = value.split('/')[1:] if absolute else value.split('/')
    require(bool(parts) and all(p not in ('', '.', '..') and all(ord(c) >= 32 and ord(c) != 127 for c in p)
                               for p in parts), 'unsafe_path')
    return parts


def directory(path):
    parts = _parts(str(path), absolute=True)
    fd = os.open('/', os.O_RDONLY | os.O_DIRECTORY)
    try:
        for part in parts:
            next_fd = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = next_fd
        return fd
    except BaseException:
        os.close(fd)
        raise


def read_at(root_fd, relative):
    parts = _parts(relative)
    fd = os.dup(root_fd)
    try:
        for part in parts[:-1]:
            next_fd = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
            os.close(fd)
            fd = next_fd
        file_fd = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd)
        with os.fdopen(file_fd, 'rb') as stream:
            info = os.fstat(stream.fileno())
            require(stat.S_ISREG(info.st_mode) and info.st_size <= MAX_BYTES, 'invalid_artifact_file')
            raw = stream.read(MAX_BYTES+1)
            require(len(raw) <= MAX_BYTES, 'input_too_large')
            return raw
    finally:
        os.close(fd)


def read_absolute(path):
    parts = _parts(str(path), absolute=True)
    parent = '/'+'/'.join(parts[:-1]) if len(parts) > 1 else '/'
    fd = os.open('/', os.O_RDONLY | os.O_DIRECTORY) if parent == '/' else directory(parent)
    try:
        return read_at(fd, parts[-1])
    finally:
        os.close(fd)


def _date(value):
    require(type(value) is str, 'invalid_date')
    try:
        require(date.fromisoformat(value).isoformat() == value, 'invalid_date')
    except ValueError:
        raise EvidenceError('invalid_date') from None


def artifacts(package, root_fd):
    require(type(package['artifacts']) is list and len(package['artifacts']) <= MAX_ARTIFACTS, 'artifact_limit')
    indexed, raw_by_id, used_paths, total = {}, {}, set(), 0
    for artifact in package['artifacts']:
        fields(artifact, 'id role experiment_id source_commit relative_path raw_sha256 size_bytes review')
        ident(artifact['id'])
        ident(artifact['experiment_id'])
        require(artifact['id'] not in indexed and artifact['role'] in ROLES, 'artifact_identity_mismatch')
        require(type(artifact['source_commit']) is str and COMMIT.fullmatch(artifact['source_commit']), 'invalid_commit')
        hashed(artifact['raw_sha256'])
        require(type(artifact['size_bytes']) is int and 0 <= artifact['size_bytes'] <= MAX_BYTES, 'invalid_size')
        _parts(artifact['relative_path'])
        require(artifact['relative_path'] not in used_paths, 'duplicate_artifact_path')
        used_paths.add(artifact['relative_path'])
        total += artifact['size_bytes']
        require(total <= MAX_TOTAL_BYTES, 'artifact_total_limit')
        raw = read_at(root_fd, artifact['relative_path'])
        require(len(raw) == artifact['size_bytes'] and digest(raw) == artifact['raw_sha256'], 'artifact_hash_mismatch')
        review = artifact['review']
        fields(review, 'status reviewer_ref reviewed_at evidence_artifact_id')
        require(review['status'] in ('not_reviewed', 'accepted', 'rejected'), 'invalid_review')
        if review['status'] == 'not_reviewed':
            require(all(review[k] is None for k in ('reviewer_ref', 'reviewed_at', 'evidence_artifact_id')), 'invalid_review')
        else:
            ident(review['reviewer_ref'])
            _date(review['reviewed_at'])
            ident(review['evidence_artifact_id'])
        indexed[artifact['id']], raw_by_id[artifact['id']] = artifact, raw
    for artifact in indexed.values():
        ref = artifact['review']['evidence_artifact_id']
        if ref is not None:
            require(ref in indexed and indexed[ref]['role'] == 'review_record' and ref != artifact['id'], 'review_evidence_missing')
    return indexed, raw_by_id


def refs(values, indexed, role=None):
    require(type(values) is list and len(values) <= MAX_ARTIFACTS, 'invalid_evidence_refs')
    require(all(type(v) is str for v in values) and len(set(values)) == len(values), 'duplicate_evidence_ref')
    for value in values:
        require(value in indexed and (role is None or indexed[value]['role'] == role), 'evidence_role_mismatch')
    return values


def reviewed(values, indexed):
    # This is a recorded review declaration, not an authenticated approval oracle.
    return bool(values) and all(indexed[v]['review']['status'] == 'accepted' for v in values)


def _stat(value, low, high):
    require(type(value) in (int, float) and math.isfinite(value) and low <= value <= high, 'invalid_statistic')
    return value


def _count(value):
    require(type(value) is int and 0 <= value < 2**63, 'invalid_count')


def _analysis_reconciliation(analysis):
    """Only closed numeric summaries cross the source-body export boundary."""
    arms = analysis.get('arms')
    require(type(arms) is dict and len(arms) <= 32, 'invalid_arms')
    diagnostics = {'fallbacks', 'negative_scheduled', 'negative_successes', 'negative_false_returns',
                   'negative_failures', 'answerable_observed', 'answerable_hit_any'}
    for policy, arm in arms.items():
        ident(policy)
        require(type(arm) is dict and {'planned', 'recorded', 'status_counts', 'strata'} <= set(arm) and
                set(arm) <= diagnostics | {'planned', 'recorded', 'status_counts', 'strata'}, 'invalid_arms')
        for key in ('planned', 'recorded'):
            _count(arm[key])
        require(arm['recorded'] <= arm['planned'] and type(arm['status_counts']) is dict and
                set(arm['status_counts']) <= STATUSES, 'invalid_arm_counts')
        for value in arm['status_counts'].values():
            _count(value)
        require(sum(arm['status_counts'].values()) == arm['planned'], 'invalid_arm_counts')
        require(arm['recorded'] >= arm['planned']-arm['status_counts'].get('missing', 0), 'invalid_arm_counts')
        require(type(arm['strata']) is dict and set(arm['strata']) <= {m+':'+k for m in ('current', 'as_of')
            for k in ('answerable', 'clear_negative', 'ambiguous')}, 'invalid_strata')
        for value in arm['strata'].values():
            _count(value)
        require(sum(arm['strata'].values()) == arm['planned'], 'invalid_arm_counts')
        for key in diagnostics & set(arm):
            _count(arm[key])
            require(arm[key] <= arm['planned'], 'invalid_arm_counts')
        require(arm.get('fallbacks', 0) <= arm['recorded'] and
                arm.get('answerable_observed', 0) <= arm['status_counts'].get('success', 0) and
                arm.get('answerable_hit_any', 0) <= arm.get('answerable_observed', 0), 'invalid_arm_counts')
        negative = sum(v for k, v in arm['strata'].items() if k.endswith(':clear_negative'))
        require(arm.get('negative_scheduled', 0) == negative and
                arm.get('negative_successes', 0)+arm.get('negative_failures', 0) == negative and
                arm.get('negative_false_returns', 0) <= arm.get('negative_successes', 0) <= negative and
                arm.get('negative_successes', 0) <= arm['status_counts'].get('success', 0), 'invalid_arm_counts')
    changes = analysis.get('analysis_changes')
    fields(changes, 'after_results interim_looks tuning_trials')
    require(type(changes['after_results']) is bool, 'invalid_analysis_changes')
    _count(changes['interim_looks'])
    _count(changes['tuning_trials'])
    family = analysis.get('multiple_comparisons')
    fields(family, 'primary secondary secondary_family_size family_complete')
    require(family['primary'] == 'unadjusted' and family['secondary'] == 'Holm' and
            type(family['family_complete']) is bool, 'invalid_analysis_family')
    _count(family['secondary_family_size'])
    # Costs are not copied recursively: retain typed totals and the source table
    # hash. Individual measurements remain in their exact raw artifact, including
    # source/price hashes, rather than accepting arbitrary nested source content.
    costs = analysis.get('costs')
    require(type(costs) is list and len(costs) <= 100000, 'invalid_costs')
    totals = []
    for cost in costs:
        require(type(cost) is dict and {'policy_id', 'phase', 'resource', 'allocation', 'unit', 'aggregation',
                'observed_subtotal', 'estimated_subtotal', 'missing_measurements', 'measurements',
                'complete_observed_total'} <= set(cost), 'invalid_costs')
        for key in ('phase', 'resource', 'allocation', 'unit', 'aggregation'):
            ident(cost[key])
        if cost['policy_id'] is not None:
            ident(cost['policy_id'])
        for key in ('observed_subtotal', 'estimated_subtotal', 'complete_observed_total'):
            require(cost[key] is None or type(cost[key]) is str and
                    re.fullmatch(r'(?:0|[1-9][0-9]*)(?:\.[0-9]+)?', cost[key], re.ASCII), 'invalid_costs')
        for key in ('missing_measurements', 'measurements'):
            _count(cost[key])
        require(cost['missing_measurements'] <= cost['measurements'], 'invalid_costs')
        require(not cost['missing_measurements'] or cost['complete_observed_total'] is None, 'missing_cost_has_total')
        if cost['complete_observed_total'] is not None:
            require(cost['complete_observed_total'] == cost['observed_subtotal'] and
                    cost['estimated_subtotal'] is not None and Decimal(cost['estimated_subtotal']) == 0, 'invalid_complete_cost')
        totals.append({key: cost[key] for key in ('policy_id', 'phase', 'resource', 'allocation', 'unit', 'aggregation',
            'observed_subtotal', 'estimated_subtotal', 'missing_measurements', 'measurements', 'complete_observed_total')})
    require(analysis.get('cost_table_sha256') == digest(canonical(costs)), 'cost_table_hash_mismatch')
    return dict(arms=arms, cost_totals=totals, cost_table_sha256=analysis['cost_table_sha256'],
                analysis_changes=changes, multiple_comparisons=family)


def exact_sum(values):
    if not values:
        return '0'
    with localcontext() as context:
        context.prec = (max(len(v.split('.')[0]) for v in values)+
                        max(len(v.partition('.')[2]) for v in values)+len(str(len(values)))+1)
        return format(sum((Decimal(v) for v in values), Decimal(0)), 'f')


def native_cost_reconciliation(analysis, plan, groups, indexed, raw):
    """Recompute native batch costs from all recorded attempts, without allocation."""
    target = Path(__file__).resolve().parent.with_name('exp_02') / 'build_run_plan.py'
    spec = importlib.util.spec_from_file_location('_exp04_native_cost_exp02', target)
    source = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(source)
    declared = analysis['native_execution_costs']
    fields(declared, 'protocol run_index_sha256 records tables query_cost_allocation coverage_scope missing_declared_native_requirements')
    require(declared['protocol'] == 'exp03-native-execution-costs-v1', 'native_cost_protocol')
    # A copied result is insufficient: the separately listed, byte-pinned ledger
    # and a unique recorded index must agree with the embedded analysis value.
    ledgers = [parse(raw[e]) for e in groups['cost_artifact_ids']]
    require(sum(v == declared for v in ledgers) == 1, 'native_cost_ledger_binding')
    matches = [e for e in groups['run_artifact_ids'] if indexed[e]['raw_sha256'] == declared['run_index_sha256']]
    require(len(matches) == 1, 'native_cost_index_binding')
    try:
        index = source.parse_raw(raw[matches[0]])
        source.validate_typed('RunIndex', index)
        source.validate_index_values(index)
    except source.Exp02Error:
        raise EvidenceError('native_cost_invalid_index') from None
    require(type(plan) is dict and type(plan.get('cost_requirements')) is list and bool(plan['cost_requirements']),
            'native_cost_requirements_missing')
    requirements = plan['cost_requirements']
    keys = []
    for requirement in requirements:
        fields(requirement, 'phase resource allocation')
        require(all(type(requirement[k]) is str and requirement[k] in source.ENUMS[e] for k,e in
                    [('phase','CostPhase'),('resource','CostResource'),('allocation','CostAllocation')]), 'invalid_cost_requirement')
        keys.append(tuple(requirement[k] for k in ('phase','resource','allocation')))
    require(len(set(keys)) == len(keys), 'duplicate_cost_requirement')

    def artifact_bytes(ref, role):
        require(ref['role'] == role, 'native_cost_source_role')
        candidates = [raw[e] for e,a in indexed.items() if a['raw_sha256'] == ref['raw_sha256'] and
                      a['size_bytes'] == ref['size_bytes']]
        require(bool(candidates), 'native_cost_source_missing')
        return candidates[0]

    records, grouped = [], defaultdict(list)
    for row in index['rows']:
        for cost in row['cost_measures']:
            source_sha, price_sha, price_date = None, None, None
            if cost['source_artifact'] is not None:
                artifact_bytes(cost['source_artifact'], 'cost_source')
                source_sha = cost['source_artifact']['raw_sha256']
            if cost['price_artifact'] is not None:
                price_raw = artifact_bytes(cost['price_artifact'], 'price_table')
                price_sha = cost['price_artifact']['raw_sha256']
                # Match producer semantics: a price artifact can be present while
                # its date is unavailable. Its absence never becomes a new date.
                try:
                    value = parse(price_raw).get('as_of')
                    _date(value)
                    price_date = value
                except (EvidenceError, AttributeError):
                    pass
            record = {k:cost[k] for k in ('measurement_id','phase','resource','allocation','policy_id','value_decimal',
                                          'unit','basis','missing_reason')}
            record.update(job_id=row['job_id'],attempt_id=row['attempt_id'],source_sha256=source_sha,
                          price_source_sha256=price_sha,price_as_of=price_date)
            records.append(record)
            grouped[tuple(record[k] for k in ('policy_id','phase','resource','allocation','unit'))].append(record)
    tables = []
    for key, values in sorted(grouped.items(), key=lambda item:tuple('' if x is None else x for x in item[0])):
        nonadditive = key[3] == 'whole_run_nonadditive' or key[2] == 'peak_memory_bytes'
        totals = {b:exact_sum([v['value_decimal'] for v in values if v['basis'] == b]) for b in ('observed','estimated')}
        missing = sum(v['basis'] == 'missing' for v in values)
        estimates = sum(v['basis'] == 'estimated' for v in values)
        table = dict(zip(('policy_id','phase','resource','allocation','unit'),key),
                     granularity='native_execution_job_not_allocated_to_queries',measurements=len(values),
                     observed_subtotal=None if nonadditive else totals['observed'],
                     estimated_subtotal=None if nonadditive else totals['estimated'],missing_measurements=missing,
                     complete_observed_total=totals['observed'] if not(nonadditive or missing or estimates) else None,
                     price_dates_complete=all(v['resource'] != 'actual_currency' or v['basis'] == 'missing' or
                                              v['price_as_of'] is not None for v in values))
        if nonadditive:
            table['individual_measurements'] = values
        tables.append(table)
    found = {tuple(v[k] for k in ('job_id','attempt_id','policy_id','phase','resource','allocation')) for v in records}
    absent = []
    for row in index['rows']:
        for requirement in requirements:
            policies = source.ENUMS['RequiredPolicyID'] if requirement['allocation'] == 'policy_incremental' else [None]
            for policy in policies:
                key = (row['job_id'],row['attempt_id'],policy,*[requirement[k] for k in ('phase','resource','allocation')])
                if key not in found:
                    absent.append(dict(job_id=row['job_id'],attempt_id=row['attempt_id'],policy_id=policy,**requirement))
    expected = dict(protocol='exp03-native-execution-costs-v1',run_index_sha256=declared['run_index_sha256'],
                    records=records,tables=tables,query_cost_allocation='unknown_never_divided_or_imputed',
                    coverage_scope='recorded_measurements_only_not_unmeasured_resource_or_billing_assurance',
                    missing_declared_native_requirements=absent)
    require(canonical(declared) == canonical(expected), 'native_cost_reconciliation_mismatch')
    # Export only numeric tables and identifiers, never raw source bodies.
    return expected


def native_replays(context, package, indexed, raw, roots, native_cli,trusted_owner_reviews=None,trusted_attribution_inputs=None):
    """Freshly replay pinned native evidence; a saved success report is insufficient."""
    fields(context, 'protocol comparisons')
    require(context['protocol'] in ('exp04-native-replays-v1','exp04-native-replays-v2','exp04-native-replays-v3') and type(context['comparisons']) is list and
            0 < len(context['comparisons']) <= 128, 'invalid_native_replays')
    view_version=context['protocol']=='exp04-native-replays-v3'
    final_version=context['protocol'] in ('exp04-native-replays-v2','exp04-native-replays-v3')
    require(view_version or trusted_attribution_inputs is None,'unexpected_attribution_pins')
    require(final_version or trusted_owner_reviews is None,'unexpected_final_review_pins')
    require(type(roots) is dict and set(roots) == {'input','evidence','output'} and type(native_cli) is str,
            'native_replay_arguments_required')
    target = Path(__file__).resolve().parent.with_name('exp_03') / 'project_native.py'
    spec = importlib.util.spec_from_file_location('_exp04_live_native_projection', target)
    projector = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(projector)
    comparisons = {r['id']:r for r in package['comparisons']}
    result = {}
    roles = dict(schedule_artifact_id='schedule',projection_plan_artifact_id='projection_plan',
                 run_plan_artifact_id='run_plan',run_index_artifact_id='run_index',observations_artifact_id='observations',
                 query_cost_ledger_artifact_id='cost_ledger',native_cost_ledger_artifact_id='cost_ledger')
    expected_pins=set();expected_view_pins=set()
    for request in context['comparisons']:
        fields(request,'comparison_id '+' '.join(roles)+' native_report_artifact_ids'+
               (' final_inputs_artifact_id' if final_version else '')+
               (' attribution_inputs_artifact_id' if view_version else ''))
        ident(request['comparison_id'])
        if final_version and request['final_inputs_artifact_id'] is not None:expected_pins.add(request['comparison_id'])
        if view_version and request['attribution_inputs_artifact_id'] is not None:expected_view_pins.add(request['comparison_id'])
    view_pins={} if trusted_attribution_inputs is None else trusted_attribution_inputs
    require(type(view_pins) is dict and set(view_pins)==expected_view_pins,'attribution_pin_mapping_mismatch')
    for cid,value in view_pins.items():ident(cid);hashed(value)
    pins={} if trusted_owner_reviews is None else trusted_owner_reviews
    require(type(pins) is dict and set(pins)==expected_pins,'final_review_pin_mapping_mismatch')
    for cid,value in pins.items():ident(cid);hashed(value)
    for request in context['comparisons']:
        cid = request['comparison_id']
        ident(cid)
        require(cid in comparisons and cid not in result, 'native_comparison_identity')
        row = comparisons[cid]
        require(row['state'] != 'not_run' and row['analysis_artifact_id'] is not None and
                row['analysis_plan_artifact_id'] is not None, 'native_analysis_required')
        consumed = []
        for key,role in roles.items():
            consumed += refs([request[key]],indexed,role)
        reports = refs(request['native_report_artifact_ids'],indexed,'native_verification')
        consumed += reports
        consumed += refs([row['analysis_artifact_id']],indexed,'analysis')
        consumed += refs([row['analysis_plan_artifact_id']],indexed,'analysis_plan')
        require(request['run_index_artifact_id'] in row['run_artifact_ids'] and
                request['native_cost_ledger_artifact_id'] in row['cost_artifact_ids'] and
                request['query_cost_ledger_artifact_id'] in row['cost_artifact_ids'], 'native_comparison_binding')
        raw_inputs = dict(analysis_plan=raw[row['analysis_plan_artifact_id']],schedule=raw[request['schedule_artifact_id']],
            run_plan=raw[request['run_plan_artifact_id']],run_index=raw[request['run_index_artifact_id']],
            projection_plan=raw[request['projection_plan_artifact_id']])
        attribution_inputs=None
        if view_version and request['attribution_inputs_artifact_id'] is not None:
            attribution_id=request['attribution_inputs_artifact_id']
            consumed+=refs([attribution_id],indexed,'attribution_inputs')
            attribution_inputs=raw_inputs['attribution_inputs']=raw[attribution_id]
        final_inputs=None
        if final_version and request['final_inputs_artifact_id'] is not None:
            final_id=request['final_inputs_artifact_id']
            consumed+=refs([final_id],indexed,'final_inputs')
            raw_inputs['final_inputs']=raw[final_id]
            final_inputs=projector.A.parse(raw[final_id])
        if final_version:
            # Current code and executable bytes are explicit package evidence,
            # never executable paths obtained from an evidence record.
            code=[SOURCE_BYTES,projector.SOURCE_BYTES,*projector.DEPENDENCY_BYTES.values()]
            for data in code:
                matches=[e for e,a in indexed.items() if a['role']=='source_code' and a['raw_sha256']==digest(data) and a['size_bytes']==len(data)]
                require(bool(matches),'native_current_code_missing');consumed+=matches
            program=read_absolute(native_cli)
            matches=[e for e,a in indexed.items() if a['role']=='native_program' and a['raw_sha256']==digest(program) and a['size_bytes']==len(program)]
            require(bool(matches),'native_current_program_missing');consumed+=matches
            require(digest(program)==projector.A.parse(raw_inputs['projection_plan'])['native_cli_sha256'],
                    'native_current_program_binding_mismatch')
        try:
            fresh = projector.project(projector.A.parse(raw_inputs['analysis_plan']),projector.A.parse(raw_inputs['schedule']),
                raw_inputs['run_plan'],raw_inputs['run_index'],projector.A.parse(raw_inputs['projection_plan']),roots,native_cli,
                final_inputs,pins.get(cid),attribution_inputs=attribution_inputs,trusted_attribution_sha256=view_pins.get(cid))
        except projector.A.AnalysisError as error:
            raise EvidenceError('native_replay_'+error.code) from None
        expected = fresh['analysis']
        if final_version:
            require(digest(program)==fresh['analysis']['native_projection']['native_cli_sha256'] and
                    read_absolute(native_cli)==program,'native_current_program_binding_mismatch')
        recorded = parse(raw[row['analysis_artifact_id']])
        if 'provenance' in recorded:
            expected['provenance'] = dict(raw_input_sha256={name:digest(data) for name,data in raw_inputs.items()},
                projector_source_sha256=digest(projector.SOURCE_BYTES),
                dependency_source_sha256={path.name:digest(data) for path,data in projector.DEPENDENCY_BYTES.items()},
                command=['project_native.py','<explicit-file-and-root-arguments>'])
        require(canonical(recorded) == canonical(expected), 'native_analysis_replay_mismatch')
        for output,key in [('observations','observations_artifact_id'),('ledger','query_cost_ledger_artifact_id'),
                           ('native_execution_costs','native_cost_ledger_artifact_id')]:
            require(canonical(parse(raw[request[key]])) == canonical(fresh[output]), 'native_output_replay_mismatch')
        require(sorted(raw[e] for e in reports) == sorted(fresh.native_reports.values()), 'native_report_replay_mismatch')
        # Include every source actually consumed by the projector, even files that
        # live in another input/evidence/output root. Exact bytes identify them;
        # their contents are not copied into the report.
        for artifact in fresh['analysis']['native_projection']['consumed_artifacts']:
            matches = [e for e,a in indexed.items() if a['raw_sha256'] == artifact['raw_sha256'] and
                       a['size_bytes'] == artifact['size_bytes']]
            require(bool(matches), 'native_consumed_source_missing')
            consumed += matches
        if final_version:
            for data in fresh.extension_code.values():
                matches=[e for e,a in indexed.items() if a['role']=='source_code' and a['raw_sha256']==digest(data) and a['size_bytes']==len(data)]
                require(bool(matches),'native_current_code_missing');consumed+=matches
        # Go reads the full native manifest graph itself. Those reads include
        # candidates/probes/base-wrapper files that Python's Sources cache need
        # not read. Bind their actual fresh verifier identities as well.
        for report_raw in fresh.native_reports.values():
            report = projector.C.parse(report_raw)
            native_pins = report['inputs'] + [report['completion']] + [p['artifact'] for p in report['policies']]
            for pin in native_pins:
                matches = [e for e,a in indexed.items() if a['raw_sha256'] == pin['raw_sha256'] and
                           a['size_bytes'] == pin['size_bytes']]
                require(bool(matches), 'native_consumed_source_missing')
                consumed += matches
        result[cid] = dict(evidence_ids=sorted(set(consumed)),
            protocol='exp04-native-replay-reconciliation-v1',status='fresh_projection_matched',
            analysis_raw_sha256=indexed[row['analysis_artifact_id']]['raw_sha256'],
            run_index_raw_sha256=digest(raw_inputs['run_index']),native_cli_sha256=fresh['analysis']['native_projection']['native_cli_sha256'],
            scientific_lock_verified=fresh['analysis']['native_projection']['scientific_lock_verified'],
            product_authority_verified=False)
        if final_version:
            binding=fresh['analysis'].get('final_protocol_binding')
            descriptor=projector.A.parse(raw_inputs['projection_plan'])
            result[cid].update(protocol='exp04-native-replay-reconciliation-v2',
                computational_final_binding_verified=binding is not None,
                expected_owner_review_sha256=pins.get(cid),owner_authorization_authenticated=False,
                task_contract_bound=projector.has_task_contract(descriptor),
                task_contract_replayed=projector.has_task_contract(descriptor) and descriptor['grader_artifact'] is not None,
                extension_code_sources_bound=len(fresh.extension_code))
        if view_version:
            result[cid].update(protocol='exp04-native-replay-reconciliation-v3',
                attribution_sensitivity_replayed=attribution_inputs is not None,
                expected_attribution_inputs_sha256=view_pins.get(cid),
                attribution_label_truth_authenticated=False,attribution_timing_authenticated=False)
    return result


def comparison(row, package, indexed, raw, reconciliation=None):
    fields(row, 'id treatment control state analysis_artifact_id analysis_plan_artifact_id contrast_id '
           'run_artifact_ids raw_result_artifact_ids cost_artifact_ids baseline_artifact_ids ablation_artifact_ids failure_artifact_ids')
    for key in ('id', 'treatment', 'control'):
        ident(row[key])
    require(row['treatment'] != row['control'] and row['state'] in ('not_run', 'partial', 'completed'), 'invalid_comparison')
    groups = {}
    for key, role in [('run_artifact_ids', 'run_index'), ('raw_result_artifact_ids', 'raw_result'),
                      ('cost_artifact_ids', 'cost_ledger'), ('baseline_artifact_ids', 'baseline'),
                      ('ablation_artifact_ids', 'ablation'), ('failure_artifact_ids', 'failure_trace')]:
        groups[key] = refs(row[key], indexed, role)
    summary = dict(id=row['id'], treatment=row['treatment'], control=row['control'], state=row['state'],
                   missing=[], statistic=None, reported_verdict=None, review_status='not_reviewed', evidence_ids=[])
    for ids in groups.values():
        summary['evidence_ids'].extend(ids)
    if row['state'] == 'not_run':
        require(all(row[k] is None for k in ('analysis_artifact_id', 'analysis_plan_artifact_id', 'contrast_id')) and
                not any(groups.values()), 'not_run_has_results')
        summary['missing'] = ['not_run']
        return summary
    for field, role in [('analysis_artifact_id', 'analysis'), ('analysis_plan_artifact_id', 'analysis_plan')]:
        if row[field] is not None:
            refs([row[field]], indexed, role)
            summary['evidence_ids'].append(row[field])
        else:
            summary['missing'].append(field)
    for key in ('run_artifact_ids', 'raw_result_artifact_ids', 'cost_artifact_ids', 'baseline_artifact_ids', 'ablation_artifact_ids'):
        if not groups[key]:
            summary['missing'].append(key)
    if row['analysis_artifact_id'] is None:
        require(row['contrast_id'] is None, 'contrast_without_analysis')
        return summary
    ident(row['contrast_id'])
    analysis = parse(raw[row['analysis_artifact_id']])
    require(type(analysis) is dict and analysis.get('protocol') == 'exp03-paired-analysis-v1' and
            analysis.get('experiment_family') == package['experiment_family'] and
            analysis.get('population') == package['population'] and analysis.get('stage') == package['stage'], 'analysis_scope_mismatch')
    require(type(analysis.get('contrasts')) is list, 'invalid_analysis')
    contrasts = [v for v in analysis['contrasts'] if type(v) is dict and v.get('id') == row['contrast_id']]
    require(len(contrasts) == 1, 'contrast_identity_mismatch')
    contrast = contrasts[0]
    require(contrast.get('treatment') == row['treatment'] and contrast.get('control') == row['control'], 'contrast_policy_mismatch')
    if row['analysis_plan_artifact_id'] is not None:
        plan = parse(raw[row['analysis_plan_artifact_id']])
        require(type(plan) is dict and plan.get('protocol') == 'exp03-analysis-plan-v1' and
                plan.get('plan_sha256') == digest(canonical({k: v for k, v in plan.items() if k != 'plan_sha256'})) and
                analysis.get('analysis_plan_sha256') == plan['plan_sha256'], 'analysis_plan_binding_mismatch')
    require(analysis.get('primary_metric') in ('recall_at_k', 'provided_grader_accuracy'), 'invalid_metric')
    point, ci = contrast.get('difference'), contrast.get('bootstrap_ci')
    if point is not None:
        _stat(point, -1, 1)
    if ci is not None:
        require(type(ci) is list and len(ci) == 2, 'invalid_interval')
        require(_stat(ci[0], -1, 1) <= _stat(ci[1], -1, 1), 'invalid_interval')
    require(contrast.get('verdict') in ('practical_improvement_supported', 'directional_improvement_only',
            'equivalence_interval_contained', 'practical_gain_not_supported_not_equivalence', 'undetermined'), 'invalid_verdict')
    require(contrast.get('inference_status') in ('computable', 'unreconciled_or_unexecuted_observations', 'budget_unobserved',
            'insufficient_independent_clusters', 'missingness_limit'), 'invalid_inference_status')
    summary.update(primary_metric=analysis['primary_metric'], statistic=dict(difference=point, bootstrap_ci=ci),
                   reported_verdict=contrast['verdict'], inference_status=contrast['inference_status'],
                   population=analysis['population'], stage=analysis['stage'])
    if contrast.get('inference_status') != 'computable' or point is None or ci is None:
        summary['missing'].append('analysis_unresolved')
    summary.update(_analysis_reconciliation(analysis))
    require(row['treatment'] in summary['arms'] and row['control'] in summary['arms'], 'analysis_arm_missing')
    if any(a['status_counts'].get(s, 0) for a in summary['arms'].values() for s in STATUSES-{'success'}):
        if not row['failure_artifact_ids']:
            summary['missing'].append('failure_trace')
    if any(c['complete_observed_total'] is None for c in summary['cost_totals']):
        summary['missing'].append('cost_not_fully_observed')
    if 'native_execution_costs' in analysis:
        summary['native_execution_costs'] = native_cost_reconciliation(analysis, plan if row['analysis_plan_artifact_id'] else None,
                                                                   groups, indexed, raw)
        native = summary['native_execution_costs']
        if native['missing_declared_native_requirements'] or any(
                t['complete_observed_total'] is None or not t['price_dates_complete'] for t in native['tables']):
            summary['missing'].append('native_cost_not_fully_observed')
        summary['missing'].append('native_query_cost_allocation_unknown')
    # This bridge is not the still-required EXP03 native-artifact adapter.
    # Merely listing raw files cannot prove all planned native runs contributed.
    if reconciliation is None:
        summary['missing'].append('native_run_analysis_reconciliation_required')
    else:
        summary['native_reconciliation'] = {k:v for k,v in reconciliation.items() if k != 'evidence_ids'}
        summary['evidence_ids'] = sorted(set(summary['evidence_ids'] + reconciliation['evidence_ids']))
    if row['state'] != 'completed':
        summary['missing'].append('execution_partial')
    if not reviewed(summary['evidence_ids'], indexed):
        summary['missing'].append('independent_review')
    else:
        summary['review_status'] = 'declared_accepted_not_authenticated'
    return summary


def coverage_rows(package, indexed):
    require(type(package['coverage']) is list and len(package['coverage']) <= len(COVERAGE), 'invalid_coverage')
    output = {}
    for row in package['coverage']:
        fields(row, 'id status artifact_ids')
        require(row['id'] in COVERAGE and row['id'] not in output and
                row['status'] in ('pass', 'fail', 'unknown', 'not_run', 'unsupported'), 'invalid_coverage')
        ids = refs(row['artifact_ids'], indexed)
        require(row['status'] not in ('pass', 'fail') or bool(ids), 'coverage_without_evidence')
        required_role = 'boundary' if row['id'] in BOUNDARIES else {
            'normal_utility':'normal_utility', 'SYS06':'system_acceptance', 'protocol_lock':'protocol_lock',
            'strong_nearest_baseline':'strong_baseline', 'B6':'strong_baseline', 'matched_write':'ablation',
            'time_confound':'ablation', 'answer_quality':'analysis', 'full_cost':'cost_ledger', 'cost_budget':'cost_ledger'}[row['id']]
        refs(ids, indexed, required_role)
        output[row['id']] = dict(status=row['status'], evidence_ids=ids,
                                 reviewed=reviewed(ids, indexed))
    return {key: output.get(key, dict(status='not_run', evidence_ids=[], reviewed=False)) for key in sorted(COVERAGE)}


def claims(package, comparisons, indexed):
    require(type(package['claims']) is list and len(package['claims']) <= 256 and
            type(package['reuse_registry']) is list and len(package['reuse_registry']) <= 1000, 'claim_limit')
    reused = {}
    for row in package['reuse_registry']:
        fields(row, 'source_sha256 contribution_id paper_id')
        hashed(row['source_sha256'])
        ident(row['contribution_id'])
        ident(row['paper_id'])
        reused.setdefault((row['source_sha256'], row['contribution_id']), set()).add(row['paper_id'])
    result, ids = [], set()
    for claim in package['claims']:
        fields(claim, 'id comparison_id kind contribution_id paper_id')
        for key in ('id', 'comparison_id', 'contribution_id', 'paper_id'):
            ident(claim[key])
        require(claim['id'] not in ids and claim['comparison_id'] in comparisons and
                claim['kind'] in ('retrieval_quality', 'task_success', 'cost_efficacy'), 'invalid_claim')
        ids.add(claim['id'])
        comp = comparisons[claim['comparison_id']]
        limits = list(comp['missing'])
        if claim['kind'] == 'task_success' and comp.get('primary_metric') != 'provided_grader_accuracy':
            limits.append('retrieval_is_not_task_success')
        for eid in comp['evidence_ids']:
            if indexed[eid]['role'] in ('analysis', 'raw_result'):
                key = (indexed[eid]['raw_sha256'], claim['contribution_id'])
                reused.setdefault(key, set()).add(claim['paper_id'])
        result.append(dict(**claim, limitation_codes=limits, evidence_ids=comp['evidence_ids']))
    for claim in result:
        if any(len(reused[(indexed[e]['raw_sha256'], claim['contribution_id'])]) > 1
               for e in claim['evidence_ids'] if indexed[e]['role'] in ('analysis', 'raw_result')):
            claim['limitation_codes'].append('duplicate_contribution_across_papers')
        claim['status'] = 'insufficient_evidence' if claim['limitation_codes'] else 'ready_for_researcher_review'
    return result


def build(package, artifact_root, native_context=None, native_roots=None, native_cli=None,trusted_owner_reviews=None,trusted_attribution_inputs=None):
    fields(package, 'protocol experiment_family stage population artifacts comparisons coverage claims reuse_registry '
           'next_questions responsibility owner_decision')
    require(package['protocol'] == 'exp04-evidence-package-v1', 'invalid_protocol')
    ident(package['experiment_family'])
    require(package['stage'] in ('planned', 'synthetic', 'exploratory', 'confirmatory') and
            package['population'] in ('synthetic', 'isolated_service', 'observed_service'), 'invalid_scope')
    require(package['owner_decision'] is None, 'worker_cannot_set_owner_decision')
    responsibility = package['responsibility']
    fields(responsibility, 'research_owner_ref product_owner_ref requested_at unresolved_reason')
    for key in ('research_owner_ref', 'product_owner_ref', 'unresolved_reason'):
        if responsibility[key] is not None:
            ident(responsibility[key])
    if responsibility['requested_at'] is not None:
        _date(responsibility['requested_at'])
    root_fd = directory(artifact_root)
    try:
        indexed, raw = artifacts(package, root_fd)
    finally:
        os.close(root_fd)
    require(type(package['comparisons']) is list and len(package['comparisons']) <= 128, 'comparison_limit')
    require(native_context is not None or (trusted_owner_reviews is None and trusted_attribution_inputs is None),'native_replay_arguments_required')
    reconciliations = native_replays(native_context,package,indexed,raw,native_roots,native_cli,trusted_owner_reviews,trusted_attribution_inputs) if native_context is not None else {}
    comparisons = {}
    for row in package['comparisons']:
        value = comparison(row, package, indexed, raw, reconciliations.get(row['id']))
        require(value['id'] not in comparisons, 'duplicate_comparison')
        comparisons[value['id']] = value
    coverage = coverage_rows(package, indexed)
    claim_rows = claims(package, comparisons, indexed)
    failed_boundaries = sorted(k for k in BOUNDARIES if coverage[k]['status'] == 'fail')
    missing_product = sorted(k for k in PRODUCT_GATES if coverage[k]['status'] != 'pass' or not coverage[k]['reviewed'])
    missing_comparisons = [k for k, v in comparisons.items() if v['missing']]
    product_status = 'blocked_boundary_failure' if failed_boundaries else ('insufficient_evidence' if
                     missing_product or missing_comparisons or not comparisons else 'ready_for_owner_review')
    research_limits = sorted(k for k in ('B6', 'strong_nearest_baseline', 'matched_write', 'time_confound', 'answer_quality', 'protocol_lock')
                             if coverage[k]['status'] != 'pass' or not coverage[k]['reviewed'])
    if package['stage'] != 'confirmatory':
        research_limits.append('not_confirmatory')
    if package['population'] == 'synthetic':
        research_limits.append('synthetic_population_only')
    if any(c['status'] != 'ready_for_researcher_review' for c in claim_rows) or not claim_rows:
        research_limits.append('claim_evidence_incomplete')
    questions = []
    require(type(package['next_questions']) is list and len(package['next_questions']) <= 64, 'question_limit')
    for row in package['next_questions']:
        fields(row, 'id bottleneck_artifact_ids required_data_codes')
        ident(row['id'])
        refs(row['bottleneck_artifact_ids'], indexed, 'bottleneck')
        require(type(row['required_data_codes']) is list and bool(row['required_data_codes']), 'question_needs_data')
        for code in row['required_data_codes']:
            ident(code)
        evidence_status = ('absent' if not row['bottleneck_artifact_ids'] else 'review_rejected' if
                           any(indexed[e]['review']['status'] == 'rejected' for e in row['bottleneck_artifact_ids']) else
                           'declared_accepted_not_authenticated' if reviewed(row['bottleneck_artifact_ids'], indexed) else
                           'linked_unreviewed')
        questions.append(dict(**row, status='question_only_not_authorized_to_start',
                              bottleneck_evidence_status=evidence_status))
    # Recheck byte-pinned package artifacts after any live subprocess work.
    root_fd = directory(artifact_root)
    try:
        require(all(read_at(root_fd,a['relative_path']) == raw[e] for e,a in indexed.items()), 'artifact_changed_during_indexing')
    finally:
        os.close(root_fd)
    require(Path(__file__).read_bytes()==SOURCE_BYTES,'evidence_builder_code_changed')
    return dict(protocol='exp04-evidence-index-v1', experiment_family=package['experiment_family'], stage=package['stage'],
                population=package['population'], input_sha256=digest(canonical(package)), artifacts=list(indexed.values()),
                comparisons=list(comparisons.values()), coverage=coverage, claims=claim_rows, next_questions=questions,
                product=dict(status=product_status, missing_gates=missing_product, failed_boundaries=failed_boundaries,
                             incomplete_comparisons=missing_comparisons,
                             options=['keep_simple_baseline', 'simplify', 'gather_more_evidence', 'hold']+
                             (['consider_bounded_use'] if product_status == 'ready_for_owner_review' else []),
                             owner_decision=None),
                research=dict(status='insufficient_evidence' if research_limits else 'ready_for_researcher_review',
                              limitations=research_limits, owner_decision=None),
                responsibility=responsibility, review_authority='declarations_only_not_authenticated_approval',
                full_card_acceptance=False, deployment_authorized=False, paper_promotion_authorized=False)


def render(result):
    rows = io.StringIO(newline='')
    writer = csv.writer(rows)
    writer.writerow(['claim_id', 'comparison_id', 'kind', 'status', 'evidence_id', 'experiment_id', 'source_commit',
                     'file', 'raw_sha256', 'review_status', 'limitations'])
    indexed = {a['id']: a for a in result['artifacts']}
    for claim in result['claims']:
        for eid in claim['evidence_ids'] or [None]:
            a = indexed.get(eid, {})
            values = [claim['id'], claim['comparison_id'], claim['kind'], claim['status'], eid,
                             a.get('experiment_id'), a.get('source_commit'), a.get('relative_path'),
                             a.get('raw_sha256'), a.get('review', {}).get('status'), '|'.join(claim['limitation_codes'])]
            writer.writerow(["'"+v if type(v) is str and v.startswith(('=', '+', '-', '@', '\t', '\r')) else v for v in values])
    questions = ['# Next questions — no automatic implementation or research start', '',
                 'All decisions remain with the named research/product owners. Missing and not_run evidence is retained.', '']
    for question in result['next_questions']:
        questions += ['- '+question['id']+': bottleneck evidence '+question['bottleneck_evidence_status']+
                      '; required data: '+', '.join(question['required_data_codes'])+'.']
    if not result['next_questions']:
        questions.append('No observed bottleneck has been supplied. No next project is opened.')
    return {'evidence_index.json': canonical(result)+b'\n',
            'product_decision.json': canonical({k: result[k] for k in ('protocol', 'experiment_family', 'product', 'research',
                'responsibility', 'full_card_acceptance', 'deployment_authorized', 'paper_promotion_authorized')})+b'\n',
            'claim_evidence.csv': rows.getvalue().encode(), 'next_questions.md': ('\n'.join(questions)+'\n').encode()}


def write_new(out, files):
    parts = _parts(str(out), absolute=True)
    require(len(parts) > 1, 'unsafe_path')
    parent = directory('/'+'/'.join(parts[:-1]))
    target, written = None, []
    try:
        os.mkdir(parts[-1], 0o700, dir_fd=parent)
        target = os.open(parts[-1], os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
        for name, raw in files.items():
            fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=target)
            written.append(name)
            with os.fdopen(fd, 'wb') as stream:
                stream.write(raw)
    except BaseException:
        if target is not None:
            for name in written:
                os.unlink(name, dir_fd=target)
            current = os.stat(parts[-1], dir_fd=parent, follow_symlinks=False)
            opened = os.fstat(target)
            if (current.st_dev, current.st_ino) == (opened.st_dev, opened.st_ino):
                os.rmdir(parts[-1], dir_fd=parent)
        raise
    finally:
        if target is not None:
            os.close(target)
        os.close(parent)


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise EvidenceError('invalid_arguments')


class Once(argparse.Action):
    def __call__(self, parser, namespace, value, option_string=None):
        require(getattr(namespace, self.dest) is None, 'duplicate_argument')
        setattr(namespace, self.dest, value)


def main(argv=None):
    parser = Parser(allow_abbrev=False)
    for name in ('package', 'artifact-root', 'out'):
        parser.add_argument('--'+name, required=True, action=Once)
    for name in ('native-replays','native-input-root','native-evidence-root','native-output-root','native-cli','trusted-final-reviews','trusted-attribution-pins'):
        parser.add_argument('--'+name,action=Once)
    try:
        args = parser.parse_args(argv)
        options = [getattr(args,n) for n in ('native_replays','native_input_root','native_evidence_root','native_output_root','native_cli')]
        require(all(v is None for v in options) or all(v is not None for v in options), 'native_replay_arguments_required')
        context_raw = read_absolute(args.native_replays) if args.native_replays is not None else None
        review_raw=read_absolute(args.trusted_final_reviews) if args.trusted_final_reviews is not None else None
        review_pins=None
        if review_raw is not None:
            value=parse(review_raw);fields(value,'protocol comparisons')
            require(value['protocol']=='exp04-trusted-final-review-pins-v1','invalid_final_review_pins')
            review_pins=value['comparisons']
        attribution_raw=read_absolute(args.trusted_attribution_pins) if args.trusted_attribution_pins is not None else None
        attribution_pins=None
        if attribution_raw is not None:
            value=parse(attribution_raw);fields(value,'protocol comparisons')
            require(value['protocol']=='exp04-trusted-attribution-pins-v1','invalid_attribution_pins')
            attribution_pins=value['comparisons']
        roots = dict(input=args.native_input_root,evidence=args.native_evidence_root,output=args.native_output_root)
        raw = read_absolute(args.package)
        result = build(parse(raw), args.artifact_root,parse(context_raw) if context_raw is not None else None,roots,args.native_cli,review_pins,attribution_pins)
        require(read_absolute(args.package) == raw and (context_raw is None or read_absolute(args.native_replays) == context_raw),
                'package_changed_during_indexing')
        require(review_raw is None or read_absolute(args.trusted_final_reviews)==review_raw,'final_review_pins_changed')
        require(attribution_raw is None or read_absolute(args.trusted_attribution_pins)==attribution_raw,'attribution_pins_changed')
        result['provenance'] = dict(package_raw_sha256=digest(raw),
                                    builder_source_sha256=digest(SOURCE_BYTES),
                                    command=['build_evidence_index.py', '--package', '<file>', '--artifact-root', '<directory>',
                                             '--out', '<new-directory>'])
        if review_raw is not None:result['provenance']['trusted_final_reviews_raw_sha256']=digest(review_raw)
        if attribution_raw is not None:result['provenance']['trusted_attribution_pins_raw_sha256']=digest(attribution_raw)
        files = render(result)
        write_new(args.out, files)
        print(json.dumps(dict(protocol=result['protocol'], status='decision_preparation_only',
                              outputs={name: digest(data) for name, data in files.items()})))
        return 0
    except EvidenceError as error:
        code = error.code
    except OSError:
        code = 'file_io_error'
    except Exception:
        code = 'internal_error'
    print(json.dumps(dict(protocol='exp04-error-v1', code=code, path='$')))
    return 2


if __name__ == '__main__':
    raise SystemExit(main())

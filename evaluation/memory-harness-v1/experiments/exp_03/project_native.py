"""Formal EXP02 records -> analysis inputs using a live, explicitly pinned verifier.

No recorded argv is executed, no result/report grants authority, and native
batch costs are never divided into invented per-query measurements.
"""
from collections import defaultdict
from decimal import Decimal, localcontext
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

HERE = Path(__file__).resolve().parent


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


A = module('_exp03_projection_analysis', HERE / 'analyze_paired.py')
B = module('_exp03_projection_exp02', HERE.with_name('exp_02') / 'build_run_plan.py')
C = module('_exp03_projection_native_compare', HERE.parents[1] / 'dynamics_compare.py')
G = B.G
LOCKED = module('_exp03_projector_locked_analysis', HERE/'locked_analysis.py')
REF = module('_exp03_projector_REF_selection', HERE/'ref_selection.py')
ARCHIVE = module('_exp03_attempt_archive', HERE.with_name('exp_02')/'attempt_archive.py')
VIEWS = module('_exp03_native_attribution', HERE/'native_attribution.py')
require, fields, digest = A.require, A.fields, A.digest
MAX_TOTAL_BYTES = 268435456
COMPLETE = {'completed', 'completed_with_failures'}
SOURCE_BYTES = Path(__file__).read_bytes()
DEPENDENCY_BYTES = {Path(m.__file__):Path(m.__file__).read_bytes() for m in (A,B,C,G,LOCKED,LOCKED.L,LOCKED.V,REF,ARCHIVE,VIEWS,VIEWS.V)}


def has_task_contract(projection):
    return projection.get('protocol') in ('exp03-native-projection-plan-v2','exp03-native-projection-plan-v3') and projection.get('task_contract') is not None


def native_output(projection,job,row):
    if projection['protocol']!='exp03-native-projection-plan-v3':return job['output_relative_dir']
    archive=projection['output_archive']
    fields(archive,'protocol artifact_namespace')
    require(archive['protocol']=='exp02-native-attempt-archive-v1' and
            B._primitive('ID',archive['artifact_namespace']),'invalid_output_archive')
    return ARCHIVE.relative_output(archive['artifact_namespace'],job['job_id'],row['attempt_id'])


class ProjectedArtifacts(dict):
    def __init__(self, values, native_reports,extension_code=None):
        super().__init__(values)
        self.native_reports = native_reports
        self.extension_code={} if extension_code is None else extension_code

    def encoded_files(self):
        result = {name+'.json':A.canonical(value)+b'\n' for name,value in self.items()}
        result.update(self.native_reports)
        require(sum(map(len,result.values())) <= MAX_TOTAL_BYTES, 'output_total_limit')
        return result


def external(call, *args):
    try:
        return call(*args)
    except (G.ExpError, G.IO.ContractError, B.Exp02Error, C.ContractErrorV2) as error:
        raise A.AnalysisError('source_'+error.code) from None


class Sources:
    def __init__(self, roots):
        require(set(roots) == {'input', 'evidence', 'output'}, 'invalid_roots')
        self.roots = {k: external(G.checked_directory, v) for k, v in roots.items()}
        self.raw, self.identities, self.total = {}, {}, 0
        self.extension_code={}

    def read(self, ref, root, role):
        external(B.validate_typed, 'ArtifactRef', ref)
        require(ref['role'] == role and root in self.roots, 'artifact_role_mismatch')
        key = (root, ref['relative_path'])
        identity = (ref['raw_sha256'], ref['size_bytes'])
        if key not in self.raw:
            raw = external(G.bounded_read, self.roots[root] / ref['relative_path'])
            self.total += len(raw)
            require(self.total <= MAX_TOTAL_BYTES, 'source_total_limit')
            self.raw[key], self.identities[key] = raw, identity
        raw = self.raw[key]
        require(self.identities[key] == identity and len(raw) == ref['size_bytes'] and A.sha(raw) == ref['raw_sha256'],
                'artifact_hash_mismatch')
        return raw

    def path(self, ref, root, role):
        self.read(ref, root, role)
        return self.roots[root] / ref['relative_path']

    def recheck(self):
        for (root, relative), raw in self.raw.items():
            require(external(G.bounded_read, self.roots[root] / relative) == raw, 'source_changed_during_projection')
        require(all(path.read_bytes()==raw for path,raw in self.extension_code.items()),'extension_code_changed')


def validate_projection(projection, analysis, schedule, plan_raw, index_raw, plan, index, final_protocol=None):
    names=('protocol analysis_plan_sha256 schedule_sha256 run_plan_raw_sha256 run_index_raw_sha256 '
           'native_cli_sha256 verification_timeout_seconds selection_rule analysis_timing gold_artifact grader_artifact '
           'query_artifact cost_source_root price_source_root slots')
    if projection.get('protocol') in ('exp03-native-projection-plan-v2','exp03-native-projection-plan-v3'):names+=' task_contract'
    if projection.get('protocol')=='exp03-native-projection-plan-v3':names+=' output_archive'
    fields(projection,names)
    require(projection['protocol'] in ('exp03-native-projection-plan-v1','exp03-native-projection-plan-v2','exp03-native-projection-plan-v3'), 'invalid_projection_protocol')
    if projection['protocol']=='exp03-native-projection-plan-v2' or has_task_contract(projection):
        external(B.validate_typed,'ArtifactRef',projection['task_contract'])
        require(projection['task_contract']['role']=='owner_contract','artifact_role_mismatch')
    for key, expected in [('analysis_plan_sha256', analysis['plan_sha256']), ('schedule_sha256', digest(schedule)),
                          ('run_plan_raw_sha256', A.sha(plan_raw)), ('run_index_raw_sha256', A.sha(index_raw))]:
        require(projection[key] == expected, 'projection_binding_mismatch')
    A.hash_value(projection['native_cli_sha256'])
    require(type(projection['verification_timeout_seconds']) is int and
            1 <= projection['verification_timeout_seconds'] <= 86400, 'invalid_verification_budget')
    require(projection['selection_rule'] in ('first_completed_else_last_attempted', 'last_attempted'), 'selection_policy_required')
    require(projection['analysis_timing'] in ('before_results', 'after_results'), 'invalid_analysis_timing')
    require(projection['analysis_timing'] != 'before_results' or
            projection['selection_rule'] == 'first_completed_else_last_attempted', 'primary_attempt_selection_policy_mismatch')
    require(projection['analysis_timing'] != 'after_results' or analysis['analysis_changed_after_results'],
            'posthoc_projection_requires_exploratory_mark')
    if analysis['stage'] == 'confirmatory' or plan['stage'] == 'confirmatory':
        require(analysis['stage'] == plan['stage'] == 'confirmatory' and final_protocol is not None, 'final_lock_binding_required')
    else:
        require(final_protocol is None, 'unexpected_final_lock_binding')
    require(analysis['population'] == 'synthetic' and analysis['experiment_family'] == plan['experiment_family'], 'population_mismatch')
    require(projection['cost_source_root'] in ('input', 'evidence', 'output') and
            projection['price_source_root'] in ('input', 'evidence', 'output'), 'invalid_cost_roots')
    slots, jobs, by_job = {}, {j['job_id']: j for j in plan['jobs']}, defaultdict(list)
    for row in index['rows']:
        by_job[row['job_id']].append(row)
    require(sum(p['planned_rows'] for r in index['rows'] for p in r['policy_rows']) <= A.CONTRACT['maximum_rows'], 'native_replay_work_limit')
    require(type(projection['slots']) is list and len(projection['slots']) == len(jobs), 'projection_schedule_mismatch')
    require(len({j['condition_id'] for j in jobs.values()}) == 1, 'different_conditions_require_separate_analysis')
    for slot in projection['slots']:
        fields(slot, 'job_id repetition attempt_order')
        require(slot['job_id'] in jobs and slot['job_id'] not in slots and type(slot['repetition']) is int and
                0 <= slot['repetition'] < analysis['replications'], 'projection_schedule_mismatch')
        attempts = [r['attempt_id'] for r in by_job[slot['job_id']]]
        require(type(slot['attempt_order']) is list and len(slot['attempt_order']) == len(attempts) and
                len(set(slot['attempt_order'])) == len(attempts) and set(slot['attempt_order']) == set(attempts),
                'attempt_schedule_mismatch')
        slots[slot['job_id']] = slot
    if projection['protocol']=='exp03-native-projection-plan-v3':
        paths=[native_output(projection,jobs[row['job_id']],row) for row in index['rows']]
        require(len(paths)==len(set(paths)),'attempt_archive_collision')
        namespace=projection['output_archive']['artifact_namespace']
        require(all(j['output_relative_dir']==namespace+'/work/'+j['job_id']
                    for j in jobs.values()),'attempt_work_namespace_mismatch')
    return slots, by_job


def source_gold(projection, sources, queries):
    raw = sources.read(projection['gold_artifact'], 'input', 'source')
    rows = external(B.V._rows, raw, 'GoldRow')
    source_queries = external(B.V._rows, sources.read(projection['query_artifact'],'input','source'), 'QueryPlan')
    query_index = {}
    for query in source_queries:
        key = (query['stream_id'],query['query_id'])
        require(key not in query_index,'duplicate_source_query')
        require((query['requested_as_of_ns'] is None) if query['request_mode']=='current' else
                query['requested_as_of_ns'] is not None and query['requested_as_of_ns'] <= query['at_ns'], 'invalid_source_query_view')
        query_index[key] = query
    indexed = {}
    for row in rows:
        key = (row['stream_id'], row['query_id'])
        require(key not in indexed, 'duplicate_gold_query')
        require(key in query_index and all(row[k]==query_index[key][k] for k in
                ('stream_id','query_id','scope','clock_domain','at_ns','cutoff_plan_seq')) and
                row['temporal_intent']==query_index[key]['request_mode'] and
                row['historical_as_of_ns']==query_index[key]['requested_as_of_ns'], 'source_gold_query_identity_mismatch')
        external(G.answer_sets, row['required_refs'], row['alternative_ref_sets'], row['forbidden_refs'],
                 row['plausible_refs'], row['cohort'])
        indexed[key] = row
    require(set(indexed)==set(query_index), 'source_query_gold_coverage_mismatch')
    alternatives = []
    for key, query in queries.items():
        require(key in indexed, 'gold_query_missing')
        row = indexed[key]
        mode = 'current' if row['temporal_intent'] == 'current' else 'as_of'
        kind = {'primary_answerable':'answerable', 'clear_negative':'clear_negative',
                'ambiguous':'ambiguous', 'alternative_answers':'ambiguous'}[row['cohort']]
        gold_sets = [row['required_refs']] if kind == 'answerable' else []
        require(query['scope'] == row['scope'] and query['request_mode'] == mode and query['kind'] == kind and
                query['gold_sets'] == gold_sets, 'analysis_gold_or_scope_mismatch')
        if row['cohort'] == 'alternative_answers':
            alternatives.append(dict(stream_id=key[0], query_id=key[1], source_gold_sha256=G.semantic_sha('GoldRow', row),
                                     alternative_ref_sets=row['alternative_ref_sets'], primary_eligible=False))
    return indexed, query_index, alternatives


def budgets(job, sources, queries, gold, source_queries, projection):
    raw = sources.read(job['common']['budget_rule'], 'input', 'budget_rule')
    rule = A.parse(raw)
    fields(rule, 'protocol queries')
    require(rule['protocol'] == 'exp03-native-budget-rule-v1' and type(rule['queries']) is list, 'budget_rule_required')
    indexed = {}
    for row in rule['queries']:
        fields(row, 'stream_id query_id final_k max_input_tokens max_output_tokens context_budget')
        key = (row['stream_id'], row['query_id'])
        require(key in queries and key not in indexed, 'budget_query_mismatch')
        A.count(row['final_k'],2**63-1)
        for key_name in ('max_input_tokens','max_output_tokens'):
            if row[key_name] is not None:
                A.count(row[key_name],2**63-1)
        external(G.IO.validate_typed, 'ContextBudget', row['context_budget'])
        require({k:row[k] for k in ('final_k', 'max_input_tokens', 'max_output_tokens')} == queries[key]['budget'],
                'budget_binding_mismatch')
        indexed[key] = row
    expected = job['common']['expected_schedule']
    keys = {(q['stream_id'], q['query_id']) for q in expected}
    require(set(indexed) == keys, 'budget_query_mismatch')
    source = A.parse(sources.read(job['common']['source_manifest'], 'input', 'source'))
    fields(source,'protocol queries_raw_sha256 gold_raw_sha256')
    require(source['protocol']=='exp03-analysis-source-v1' and
            source['queries_raw_sha256']==projection['query_artifact']['raw_sha256'] and
            source['gold_raw_sha256']==projection['gold_artifact']['raw_sha256'], 'source_manifest_binding_mismatch')
    materialization = A.parse(sources.read(job['bindings']['materialization_manifest'],'input','native_materialization'))
    fields(materialization,'protocol source_manifest_raw_sha256 native_input_manifest_raw_sha256 queries')
    require(materialization['protocol'] in ('exp03-native-materialization-v1','exp03-native-materialization-v2') and
            materialization['source_manifest_raw_sha256']==job['common']['source_manifest']['raw_sha256'] and
            materialization['native_input_manifest_raw_sha256']==job['bindings']['native_input_manifest']['raw_sha256'] and
            type(materialization['queries']) is list, 'materialization_binding_mismatch')
    mappings = {}
    source_view_v2=materialization['protocol']=='exp03-native-materialization-v2'
    for mapping in materialization['queries']:
        names=('stream_id query_id source_query_sha256 source_gold_sha256 source_cutoff_plan_seq '
               'native_cutoff_accepted_seq requested_as_of_ns native_candidate_snapshot_sha256 clock_mapping')
        if source_view_v2:names+=' source_query_at_ns native_at_ns source_view_rule'
        fields(mapping,names)
        for name in ('stream_id','query_id'):
            A.identifier(mapping[name])
        for name in ('source_query_sha256','source_gold_sha256','native_candidate_snapshot_sha256'):
            A.hash_value(mapping[name])
        for name in ('source_cutoff_plan_seq','native_cutoff_accepted_seq'):
            A.count(mapping[name],2**63-1)
        if mapping['requested_as_of_ns'] is not None:
            A.count(mapping['requested_as_of_ns'],2**63-1)
        key = (mapping['stream_id'],mapping['query_id'])
        require(key in keys and key not in mappings,'materialization_query_mismatch')
        original, label = source_queries[key],gold[key]
        require(mapping['source_query_sha256']==G.semantic_sha('QueryPlan',original) and
                mapping['source_gold_sha256']==G.semantic_sha('GoldRow',label) and
                mapping['source_cutoff_plan_seq']==original['cutoff_plan_seq'] and
                mapping['requested_as_of_ns']==original['requested_as_of_ns'], 'materialization_query_mismatch')
        if source_view_v2:
            for name in ('source_query_at_ns','native_at_ns'):A.count(mapping[name],2**63-1)
            view_at=(original['at_ns'] if original['request_mode']=='current' else original['requested_as_of_ns'])
            require(mapping['source_query_at_ns']==original['at_ns'] and mapping['native_at_ns']==view_at and
                    mapping['source_view_rule']=='observed_fixture_view_with_current_withdrawal_fence',
                    'materialization_source_view_mismatch')
        mapping_clock = mapping['clock_mapping']
        require(mapping_clock in ('same_clock_identity_ns','renamed_synthetic_clock_identity_ns') and
                ((original['clock_domain']==job['common']['clock_domain']) == (mapping_clock=='same_clock_identity_ns')),
                'materialization_clock_mismatch')
        mappings[key] = mapping
    require(set(mappings)==keys,'materialization_query_mismatch')
    for row in expected:
        key = (row['stream_id'], row['query_id'])
        native_at=mappings[key]['native_at_ns'] if source_view_v2 else gold[key]['at_ns']
        require(key in queries and gold[key]['scope'] == row['scope'] and native_at == row['at_ns'] and
                queries[key]['input_sha256'] == job['common']['source_manifest']['raw_sha256'], 'source_query_binding_mismatch')
        require(mappings[key]['native_cutoff_accepted_seq']==row['cutoff_accepted_seq'] and
                mappings[key]['native_candidate_snapshot_sha256']==row['candidate_snapshot_sha256'], 'materialization_cutoff_mismatch')
    return indexed


class NativeVerifier:
    def __init__(self, executable, projection, deadline=None):
        self.original = Path(executable)
        self.projection = projection
        self.raw = external(G.bounded_read, self.original)
        require(A.sha(self.raw) == projection['native_cli_sha256'] and os.access(self.original, os.X_OK), 'native_executable_mismatch')
        own_deadline = time.monotonic()+projection['verification_timeout_seconds']
        self.deadline = min(deadline,own_deadline) if deadline is not None else own_deadline
        self.directory = tempfile.TemporaryDirectory(prefix='arkism-native-verifier-')
        self.executable = Path(self.directory.name) / ('verify.exe' if os.name == 'nt' else 'verify')
        try:
            self.executable.write_bytes(self.raw)
            self.executable.chmod(0o500)
        except BaseException:
            self.directory.cleanup()
            raise

    def close(self):
        self.directory.cleanup()

    def verify(self, job, row, sources, _before_archive=False):
        binding = job['bindings']
        manifest = sources.path(binding['native_input_manifest'], 'input', 'native_input')
        native = A.parse(sources.read(binding['native_input_manifest'], 'input', 'native_input'))
        flag = {'p1-input-manifest-v1':'--p1-manifest', 'dynamics-input-manifest-v2':'--manifest'}.get(native.get('protocol'))
        require(flag is not None, 'native_manifest_protocol_mismatch')
        completion = row['native_completion_manifest']
        require(completion is not None, 'completion_manifest_missing')
        output = sources.path(completion, 'output', 'completion_manifest').parent
        relative=job['output_relative_dir'] if _before_archive else native_output(self.projection,job,row)
        require(output == sources.roots['output'] / relative, 'native_output_binding_mismatch')
        args = [str(self.executable), 'verify', flag, str(manifest), '--config',
                str(sources.path(binding['native_config_raw'], 'input', 'config')), '--registry',
                str(sources.path(binding['registry_raw'], 'input', 'registry')), '--out', str(output)]
        env = {'PATH':os.defpath,'GOMAXPROCS':'4'}
        if 'HOME' in os.environ:
            env['HOME'] = os.environ['HOME']
        remaining = self.deadline-time.monotonic()
        require(remaining > 0, 'native_verification_timeout')
        try:
            with tempfile.TemporaryFile() as out, tempfile.TemporaryFile() as err:
                process = subprocess.run(args, env=env, stdout=out, stderr=err, timeout=remaining, check=False)
                require(process.returncode == 0, 'native_verification_failed')
                require(out.tell() <= A.MAX_BYTES and err.tell() == 0, 'native_verification_output_limit')
                out.seek(0)
                raw = out.read(A.MAX_BYTES+1)
        except subprocess.TimeoutExpired:
            raise A.AnalysisError('native_verification_timeout') from None
        require(external(G.bounded_read, self.original) == self.raw, 'native_executable_changed')
        report = external(C.parse, raw)
        return report, raw


def _identity(value, ref, role):
    fields(value, 'role raw_sha256 size_bytes')
    require(value['role'] == role and value['raw_sha256'] == ref['raw_sha256'] and value['size_bytes'] == ref['size_bytes'],
            'native_artifact_binding_mismatch')


def retain_native_graph(job, pins, sources):
    selected=job['bindings']['native_input_manifest']
    parent=Path(selected['relative_path']).parent
    raw=sources.read(selected,'input','native_input')
    manifest=A.parse(raw)
    def retain(file,role):
        ref=dict(role='source',relative_path=(parent/file['relative_path']).as_posix(),
                 raw_sha256=file['raw_sha256'],size_bytes=file['size_bytes'])
        require(role in pins,'native_input_missing')
        _identity(pins[role],ref,role)
        return sources.read(ref,'input','source')
    if manifest['protocol']=='p1-input-manifest-v1':
        require('base_manifest' in pins,'native_input_missing')
        base=dict(relative_path='dynamics-manifest.json',raw_sha256=pins['base_manifest']['raw_sha256'],
                  size_bytes=pins['base_manifest']['size_bytes'])
        retain(manifest['feature_file'],'p1_features')
        manifest=A.parse(retain(base,'base_manifest'))
    for file in manifest['files']:
        retain(file,'native_'+file['role'])


def verified_rows(report, job, row, sources, budget, queries):
    fields(report, 'protocol status scope contract_sha256 input_manifest_sha256 config_sha256 registry_sha256 inputs '
           'completion probes policies product_authority_verified scientific_lock_verified')
    require(report['protocol'] == 'exp03-native-verification-v1' and report['status'] == 'replay_matched' and
            report['scope'] == 'offline_synthetic_not_product_authority_or_scientific_approval' and
            report['contract_sha256'] == B.DYNAMICS_SHA and report['product_authority_verified'] is False and
            report['scientific_lock_verified'] is False, 'native_verification_scope_mismatch')
    require(type(report['inputs']) is list and len(report['inputs']) <= 16, 'invalid_native_inputs')
    pins = {}
    for pin in report['inputs']:
        fields(pin, 'role raw_sha256 size_bytes')
        A.hash_value(pin['raw_sha256'])
        A.count(pin['size_bytes'], A.MAX_BYTES)
        require(pin['role'] in {'selected_manifest','config','registry','base_manifest','p1_features'} |
                {'native_'+v for v in C.ENUMS['InputRole']} and pin['role'] not in pins, 'invalid_native_input_role')
        pins[pin['role']] = pin
    for role, ref in [('selected_manifest', job['bindings']['native_input_manifest']), ('config', job['bindings']['native_config_raw']),
                      ('registry', job['bindings']['registry_raw']), ('native_events', job['common']['events_file']),
                      ('native_probes', job['common']['probe_file']), ('native_candidates', job['common']['candidate_file'])]:
        require(role in pins, 'native_input_missing')
        _identity(pins[role], ref, role)
    feature = pins.get('p1_features', pins.get('native_features'))
    if feature is not None:
        _identity(feature, job['common']['features_file'], feature['role'])
    retain_native_graph(job,pins,sources)
    _identity(report['completion'], row['native_completion_manifest'], 'completion_manifest')
    require(job['bindings']['native_config_semantic'] == dict(type_name='DynamicsRunConfigV2', semantic_sha256=report['config_sha256']) and
            job['bindings']['registry_semantic'] == dict(type_name='PolicyRegistryV2', semantic_sha256=report['registry_sha256']),
            'native_semantic_binding_mismatch')
    expected = {(q['stream_id'],q['query_id']):q for q in job['common']['expected_schedule']}
    probes = report['probes']
    fields(probes, 'protocol stream_id clock_domain probes')
    require(probes['protocol'] == 'dynamics-probe-file-v2' and probes['clock_domain'] == job['common']['clock_domain'], 'native_probe_mismatch')
    found = set()
    for probe in probes['probes']:
        key = (probe['stream_id'], probe['query_id'])
        require(key in expected and (key, probe['policy_id']) not in found, 'native_probe_mismatch')
        found.add((key, probe['policy_id']))
        require(all(probe[k] == expected[key][k] for k in ('scope','at_ns','cutoff_accepted_seq','candidate_snapshot_id','candidate_snapshot_sha256')) and
                probe['final_k'] == budget[key]['final_k'] and probe['context_budget'] == budget[key]['context_budget'], 'native_probe_mismatch')
    require(found == {(q,p) for q in expected for p in job['policy_order']}, 'native_probe_mismatch')
    registry = external(C.decode, sources.read(job['bindings']['registry_raw'], 'input', 'registry'), 'PolicyRegistryV2')
    external(C._registry, registry)
    require(type(report['policies']) is list and [r['run']['policy_id'] for r in report['policies']] == job['policy_order'], 'native_policy_coverage_mismatch')
    indexed, output_refs = {}, []
    for actual, recorded in zip(report['policies'], row['policy_rows']):
        fields(actual, 'artifact run')
        ref = recorded['prediction_file']
        raw = sources.read(ref, 'output', 'prediction')
        _identity(actual['artifact'], ref, 'prediction_run')
        run = external(C.decode, raw, 'PredictionRunV2')
        external(C._run, run, registry)
        require(run == actual['run'] and run['policy_id'] == recorded['policy_id'] and run['policy_version'] == recorded['policy_version'] and
                run['input_manifest_sha256'] == report['input_manifest_sha256'] and run['run_config_sha256'] == report['config_sha256'],
                'native_prediction_binding_mismatch')
        predictions = run['predictions']
        require(len(predictions) == recorded['recorded_rows'] and sum(p['status']=='failed' for p in predictions) == recorded['failed_rows'],
                'native_denominator_mismatch')
        by_query = {q['query_id']:q for q in expected.values()}
        require(len(by_query) == len(expected) and {p['query_id'] for p in predictions} == set(by_query), 'native_query_coverage_mismatch')
        for prediction in predictions:
            query = by_query[prediction['query_id']]
            indexed[(query['stream_id'],query['query_id'],run['policy_id'])] = prediction
        output_refs.append(('prediction_run', 'predictions/'+run['policy_id']+'.json', ref['raw_sha256'], ref['size_bytes']))
    _stdout(row, sources, report, output_refs)
    return indexed


def _stdout(row, sources, report, output_refs):
    require(row['native_stdout_status'] is not None, 'native_stdout_evidence_missing')
    status = A.parse(sources.read(row['native_stdout_status'], 'output', 'stdout_status'))
    fields(status, 'status run_id outputs error')
    expected_status = 'completed_with_failures' if row['status']=='completed_with_failures' else 'completed'
    require(status['status'] == expected_status and status['error'] is None and
            all(p['run']['run_id'] == status['run_id'] for p in report['policies']), 'native_stdout_binding_mismatch')
    output_refs.append(('completion_manifest','manifest.json',report['completion']['raw_sha256'],report['completion']['size_bytes']))
    actual = []
    for ref in status['outputs']:
        fields(ref, 'role relative_path raw_sha256 size_bytes')
        actual.append(tuple(ref[k] for k in ('role','relative_path','raw_sha256','size_bytes')))
    require(len(actual) == len(set(actual)) and set(actual) == set(output_refs) and actual[-1][0] == 'completion_manifest',
            'native_stdout_binding_mismatch')


def _select(rows, order, rule):
    by_id = {r['attempt_id']:r for r in rows}
    ordered = [by_id[k] for k in order]
    attempted = [r for r in ordered if r['status'] != 'not_run']
    complete = [r for r in ordered if r['status'] in COMPLETE]
    if rule == 'first_completed_else_last_attempted' and complete:
        if len(complete)==1:
            return complete[0]
        clocks={r.get('execution_clock_domain') for r in complete}
        require(None not in clocks and len(clocks)==1 and all(type(r.get('finished_at_ns')) is int for r in complete),
                'completion_order_unverifiable')
        # Clock-comparable completion times choose the first published result;
        # the predeclared order resolves exact timestamp ties only.
        return min(complete,key=lambda r:r['finished_at_ns'])
    return (attempted or ordered)[-1]


def grades(projection, sources, analysis, schedule, index=None):
    adapter=None;task_raw=None
    if has_task_contract(projection):
        adapter=module('_exp03_literal_grader_replay',HERE/'task_adapter.py')
        sources.extension_code.update(adapter.DEPENDENCIES)
        sources.extension_code[Path(adapter.__file__)]=adapter.SOURCE_BYTES
        task_raw=sources.read(projection['task_contract'],'input','owner_contract')
        try:adapter.prepare_contract(task_raw,analysis,schedule,sources)
        except (adapter.A.AnalysisError,adapter.B.Exp02Error,adapter.G.ExpError,
                adapter.G.IO.ContractError,adapter.P.C.ContractErrorV2) as error:raise A.AnalysisError(error.code) from None
    ref = projection['grader_artifact']
    if ref is None:
        return {}
    require(analysis['primary_metric'] == 'provided_grader_accuracy', 'unexpected_grader_evidence')
    doc = A.parse(sources.read(ref, 'output', 'source'))
    fields(doc, 'protocol analysis_plan_sha256 schedule_sha256 rows')
    require(doc['protocol'] == 'exp03-grader-observations-v1' and doc['analysis_plan_sha256'] == analysis['plan_sha256'] and
            doc['schedule_sha256'] == digest(schedule) and type(doc['rows']) is list and len(doc['rows']) <= A.CONTRACT['maximum_rows'],
            'grader_evidence_mismatch')
    result = {}
    for grade in doc['rows']:
        fields(grade, 'job_id attempt_id stream_id query_id policy_id status correct fallback grader_sha256 final_refs_sha256')
        key = tuple(grade[k] for k in ('job_id','attempt_id','stream_id','query_id','policy_id'))
        for value in key:
            A.identifier(value)
        require(key not in result and grade['grader_sha256'] == analysis['grader_sha256'] and type(grade['fallback']) is bool and
                grade['status'] in {'success','truncated','generator_failure','checker_failure','api_error','timeout'}, 'invalid_grade')
        require((type(grade['correct']) is bool) if grade['status']=='success' else grade['correct'] is None, 'invalid_grade')
        A.hash_value(grade['final_refs_sha256'])
        result[key] = grade
    if adapter is not None:
        require(index is not None,'task_native_index_required')
        try:adapter.verify_grades(task_raw,analysis,schedule,index,result,sources)
        except (adapter.A.AnalysisError,adapter.B.Exp02Error,adapter.G.ExpError,
                adapter.G.IO.ContractError,adapter.P.C.ContractErrorV2) as error:raise A.AnalysisError(error.code) from None
    return result


def bind_task_dataset(projection,sources,raw):
    if not has_task_contract(projection):return
    task=A.parse(sources.read(projection['task_contract'],'input','owner_contract'))
    require(type(task.get('source_files')) is dict and set(task['source_files'])=={'events','queries','gold','manifest'},
            'task_locked_dataset_mismatch')
    require(all(sources.read(task['source_files'][name],'input','source')==raw[name]
                for name in ('events','queries','gold','manifest')),'task_locked_dataset_mismatch')


def observation(query, policy, repetition, selected, native, analysis, graded):
    status, refs, correct, fallback = 'not_run', [], None, False
    if selected['status'] in COMPLETE and native is not None:
        status = 'retrieval_failure' if native['status']=='failed' else 'success'
        refs = native['decision']['selected'] if native['decision'] is not None else []
        key = (selected['job_id'],selected['attempt_id'],query['stream_id'],query['query_id'],policy)
        grade = graded.get(key)
        if grade is not None:
            require(status == 'success' and grade['final_refs_sha256'] == digest(refs), 'grade_native_result_mismatch')
            status, correct, fallback = grade['status'], grade['correct'], grade['fallback']
        elif analysis['primary_metric'] == 'provided_grader_accuracy' and status == 'success':
            status = 'checker_failure'
    elif selected['status'] != 'not_run':
        status = 'timeout' if selected['status']=='timeout' else 'retrieval_failure'
    return dict(stream_id=query['stream_id'],query_id=query['query_id'],policy_id=policy,repetition=repetition,
                input_sha256=query['input_sha256'],gold_sha256=digest(query['gold_sets']),budget_sha256=digest(query['budget']),
                final_refs=refs,status=status,correct=correct,fallback=fallback,grader_sha256=analysis['grader_sha256'])


def observation_with_REF_alias(query, policy, repetition, selected, predictions, analysis, graded, selected_REF=None, executed_policies=()):
    native_policy=selected_REF if policy=='REF' and selected_REF is not None else policy
    chosen=selected if native_policy in executed_policies else dict(selected,status='not_run')
    row=observation(query,native_policy,repetition,chosen,predictions.get((query['stream_id'],query['query_id'],native_policy)),analysis,graded)
    row['policy_id']=policy
    return row


def query_cost_unknowns(analysis, schedule, jobs):
    entries = []
    for requirement in analysis['cost_requirements']:
        allocated = [(job,p) for (_,_,p,_),job in jobs.items()] if requirement['allocation']=='policy_incremental' else [(None,None)]
        for job, policy in allocated:
            entries.append(dict(measurement_id=digest([job,requirement]),job_id=job,policy_id=policy,**requirement,
                                value=None,unit=A.CONTRACT['cost_units'][requirement['resource']],basis='missing',
                                missing_reason='not_measured',source_sha256=None,price_source=None))
    return dict(protocol='exp03-cost-ledger-v1',schedule_sha256=digest(schedule),entries=entries)


def exact_total(values):
    if not values:
        return '0'
    integer_width = max(len(v.split('.')[0]) for v in values)
    scale = max(len(v.partition('.')[2]) for v in values)
    with localcontext() as context:
        context.prec = integer_width+scale+len(str(len(values)))+1
        return format(sum((Decimal(v) for v in values),Decimal(0)),'f')


def native_costs(index, projection, sources, analysis):
    records, groups = [], defaultdict(list)
    for row in index['rows']:
        for cost in row['cost_measures']:
            source_sha, price_sha, as_of = None, None, None
            if cost['source_artifact'] is not None:
                sources.read(cost['source_artifact'], projection['cost_source_root'], 'cost_source')
                source_sha = cost['source_artifact']['raw_sha256']
            if cost['price_artifact'] is not None:
                raw = sources.read(cost['price_artifact'], projection['price_source_root'], 'price_table')
                price_sha = cost['price_artifact']['raw_sha256']
                try:
                    value = A.parse(raw).get('as_of')
                    if type(value) is str and A.date.fromisoformat(value).isoformat() == value:
                        as_of = value
                except (A.AnalysisError, AttributeError, ValueError):
                    pass
            record = {k:cost[k] for k in ('measurement_id','phase','resource','allocation','policy_id','value_decimal','unit','basis','missing_reason')}
            record.update(job_id=row['job_id'],attempt_id=row['attempt_id'],source_sha256=source_sha,
                          price_source_sha256=price_sha,price_as_of=as_of)
            records.append(record)
            groups[(cost['policy_id'],cost['phase'],cost['resource'],cost['allocation'],cost['unit'])].append(record)
    tables = []
    for key, values in sorted(groups.items(), key=lambda item:tuple('' if x is None else x for x in item[0])):
        nonadditive = key[3]=='whole_run_nonadditive' or key[2]=='peak_memory_bytes'
        totals = {basis:exact_total([v['value_decimal'] for v in values if v['basis']==basis]) for basis in ('observed','estimated')}
        missing = sum(v['basis']=='missing' for v in values)
        estimates = sum(v['basis']=='estimated' for v in values)
        table = dict(zip(('policy_id','phase','resource','allocation','unit'),key),
                     granularity='native_execution_job_not_allocated_to_queries',measurements=len(values),
                     observed_subtotal=totals['observed'] if not nonadditive else None,
                     estimated_subtotal=totals['estimated'] if not nonadditive else None,missing_measurements=missing,
                     complete_observed_total=totals['observed'] if not(nonadditive or missing or estimates) else None,
                     price_dates_complete=all(v['resource']!='actual_currency' or v['basis']=='missing' or v['price_as_of'] is not None for v in values))
        if nonadditive:
            table['individual_measurements'] = values
        tables.append(table)
    found = {(v['job_id'],v['attempt_id'],v['policy_id'],v['phase'],v['resource'],v['allocation']) for v in records}
    missing_requirements = []
    for row in index['rows']:
        for requirement in analysis['cost_requirements']:
            policies = B.ENUMS['RequiredPolicyID'] if requirement['allocation']=='policy_incremental' else [None]
            for policy in policies:
                key = (row['job_id'],row['attempt_id'],policy,requirement['phase'],requirement['resource'],requirement['allocation'])
                if key not in found:
                    missing_requirements.append(dict(job_id=row['job_id'],attempt_id=row['attempt_id'],policy_id=policy,**requirement))
    return dict(protocol='exp03-native-execution-costs-v1',run_index_sha256=projection['run_index_raw_sha256'],
                records=records,tables=tables,query_cost_allocation='unknown_never_divided_or_imputed',
                coverage_scope='recorded_measurements_only_not_unmeasured_resource_or_billing_assurance',
                missing_declared_native_requirements=missing_requirements)


def verify_final_inputs(final_inputs, trusted_sha, analysis, schedule, plan, plan_raw, projection, sources):
    require((final_inputs is None) == (trusted_sha is None), 'final_review_arguments_required')
    if final_inputs is None:
        return None
    require(analysis['stage'] == plan['stage'] == 'confirmatory', 'unexpected_final_lock_binding')
    locked = LOCKED
    fields(final_inputs,'protocol files gold_review owner_review')
    require(final_inputs['protocol'] == 'exp03-final-inputs-v1' and type(final_inputs['files']) is dict and
            set(final_inputs['files']) == set(locked.NAMES), 'invalid_final_inputs')
    raw = {name:sources.read(ref,'input','owner_contract' if name=='lock' else 'source')
           for name,ref in final_inputs['files'].items()}
    bind_task_dataset(projection,sources,raw)
    require(A.parse(raw['analysis_plan']) == analysis and A.parse(raw['schedule']) == schedule, 'final_analysis_input_mismatch')
    require(plan['protocol_lock'] is not None and
            sources.read(plan['protocol_lock'],'input','owner_contract') == raw['lock'], 'final_protocol_lock_mismatch')
    require(sources.read(projection['query_artifact'],'input','source') == raw['queries'] and
            sources.read(projection['gold_artifact'],'input','source') == raw['gold'], 'final_dataset_projection_mismatch')
    ref_records = []
    for job in plan['jobs']:
        require(sources.read(job['common']['source_manifest'],'input','source') == raw['analysis_source'], 'final_source_manifest_mismatch')
        use = job['bindings']['REF_use']
        require(use['mode'] == 'uses_selected_REF' and use['selection_record'] is not None, 'final_REF_selection_required')
        selection_raw = sources.read(use['selection_record'],'evidence','REF_selection')
        ref_records.append(dict(raw_sha256=A.sha(selection_raw),size_bytes=len(selection_raw)))
    gold_review = sources.read(final_inputs['gold_review'],'evidence','acceptance')
    owner_review = sources.read(final_inputs['owner_review'],'evidence','owner_contract')
    try:
        reviewed = locked.review_final_inputs(raw,gold_review,owner_review,trusted_sha,plan_raw,projection)
    except locked.A.AnalysisError as error:
        raise A.AnalysisError(error.code) from None
    reviewed['REF_selection_records'] = ref_records
    reviewed['REF_selection_semantics_verified'] = False
    return reviewed


def selection_source(spec, sources, role):
    fields(spec,'root artifact')
    require(spec['root'] in ('input','evidence','output'), 'REF_source_root_mismatch')
    return sources.read(spec['artifact'],spec['root'],role)


def recompute_REF_selection(record, sources, roots, native_cli, check_expected=True, deadline=None):
    fields(record,'protocol files rule validation'+(' expected' if check_expected else ''))
    require(record['protocol']==('exp01-ref-selection-v2' if check_expected else 'exp01-ref-selection-request-v2'),
            'invalid_REF_selection_protocol')
    require(type(record['files']) is dict and set(record['files'])==set(LOCKED.NAMES),'invalid_REF_selection_inputs')
    raw={name:sources.read(ref,'input','owner_contract' if name=='lock' else 'source') for name,ref in record['files'].items()}
    rule=REF.parse(selection_source(record['rule'],sources,'owner_contract'))
    require(type(rule) is dict and type(rule.get('eligible_policies')) is list and
            all(type(p) is str for p in rule['eligible_policies']) and
            (set(B.ENUMS['RequiredPolicyID'])-{'B2'})<=set(rule['eligible_policies']), 'REF_required_candidate_coverage_mismatch')
    fields(record['validation'],'analysis_plan schedule run_plan run_index projection_plan')
    validation={name:selection_source(spec,sources,'plan' if name=='run_plan' else 'source') for name,spec in record['validation'].items()}
    valplan, valschedule, valprojection=(A.parse(validation[n]) for n in ('analysis_plan','schedule','projection_plan'))
    A.validate_plan(valplan)
    require(valplan['stage']=='exploratory' and 'REF' not in valplan['policies'], 'REF_validation_plan_required')
    require(valprojection['selection_rule']=='first_completed_else_last_attempted','primary_attempt_selection_policy_mismatch')
    require(sources.read(valprojection['query_artifact'],'input','source')==raw['queries'] and
            sources.read(valprojection['gold_artifact'],'input','source')==raw['gold'],'REF_validation_source_mismatch')
    bind_task_dataset(valprojection,sources,raw)
    try:
        LOCKED.bind(raw,'before_results')
        REF.validate_rule(rule,raw,valplan)
        REF.validation_population(raw,rule,valplan,valschedule)
    except (LOCKED.A.AnalysisError,REF.A.AnalysisError) as error:
        raise A.AnalysisError(error.code) from None
    # This nested projection can never be confirmatory, so recursion ends here.
    fresh=project(valplan,valschedule,validation['run_plan'],validation['run_index'],valprojection,roots,native_cli,
                  _verification_deadline=deadline)
    require(fresh['analysis']['native_projection']['native_cli_invoked'], 'REF_validation_native_replay_required')
    try:
        selected=REF.select(rule,raw,valplan,valschedule,fresh['observations'],external(B.parse_raw,validation['run_index']),
                            fresh['native_execution_costs'],validation['run_index'])
    except REF.A.AnalysisError as error:
        raise A.AnalysisError(error.code) from None
    expected=dict(protocol='exp01-ref-selection-v2',files=record['files'],rule=record['rule'],validation=record['validation'],expected=selected)
    if check_expected:
        require(A.canonical(record)==A.canonical(expected),'REF_selection_replay_mismatch')
    for artifact in fresh['analysis']['native_projection']['consumed_artifacts']:
        sources.read(dict(role='source',relative_path=artifact['relative_path'],raw_sha256=artifact['raw_sha256'],
                          size_bytes=artifact['size_bytes']),artifact['root'],'source')
    sources.extension_code.update(fresh.extension_code)
    return expected,fresh


def resolve_REF_selection(plan, final_inputs, sources, roots, native_cli, deadline):
    refs=[job['bindings']['REF_use']['selection_record'] for job in plan['jobs']]
    require(len({(r['raw_sha256'],r['size_bytes']) for r in refs})==1,'REF_selection_changed_across_test_jobs')
    raw=sources.read(refs[0],'evidence','REF_selection')
    record=REF.parse(raw)
    # Selection cannot substitute another dataset, frozen analysis namespace or
    # final lock. Compare actual input bytes, not caller-owned metadata strings.
    fields(record,'protocol files rule validation expected')
    require(type(record['files']) is dict and set(record['files'])==set(LOCKED.NAMES),'invalid_REF_selection_inputs')
    for name,ref in final_inputs['files'].items():
        role='owner_contract' if name=='lock' else 'source'
        require(sources.read(record['files'][name],'input',role)==sources.read(ref,'input',role),'REF_final_input_mismatch')
    expected,fresh=recompute_REF_selection(record,sources,roots,native_cli,True,deadline)
    choice=expected['expected']
    require(choice['status']=='selected' and choice['selected_policy']!='B2','REF_selection_undetermined')
    reports={'REF-validation-'+name:raw for name,raw in fresh.native_reports.items()}
    evidence=dict(selection_record_raw_sha256=A.sha(raw),selection=choice,
                  validation_analysis_sha256=A.digest(fresh['analysis']),
                  native_reports={name:A.sha(value) for name,value in reports.items()},
                  alias_scope='same_selected_native_execution_not_another_sample_or_paid_call')
    return choice['selected_policy'],evidence,reports


def analyze_native_inputs(analysis, schedule, observations, ledger, final_protocol, verifications):
    if final_protocol is None:
        return A.analyze(analysis,schedule,observations,ledger)
    require(bool(verifications), 'confirmatory_native_replay_required')
    require('REF' not in analysis['policies'] or final_protocol['REF_selection_semantics_verified'],
            'confirmatory_REF_selection_semantics_required')
    result = A._analyze_validated_inputs(analysis,schedule,observations,ledger)
    result['final_protocol_binding'] = final_protocol
    return result


def project(analysis, schedule, plan_raw, index_raw, projection, roots, native_cli, final_inputs=None, trusted_owner_review_sha256=None, _verification_deadline=None, *, attribution_inputs=None, trusted_attribution_sha256=None):
    A.validate_plan(analysis)
    queries, _, analysis_jobs = A.validate_schedule(analysis, schedule)
    plan, index = external(B.parse_raw, plan_raw), external(B.parse_raw, index_raw)
    external(B.validate_typed,'RunPlan',plan)
    external(B.validate_typed,'RunIndex',index)
    external(B.validate_plan_values,plan)
    external(B.validate_index_values,index)
    external(B.validate_index_plan,index,plan,plan_raw)
    sources = Sources(roots)
    final_protocol = verify_final_inputs(final_inputs,trusted_owner_review_sha256,analysis,schedule,plan,plan_raw,projection,sources)
    slots, by_job = validate_projection(projection,analysis,schedule,plan_raw,index_raw,plan,index,final_protocol)
    require((attribution_inputs is None)==(trusted_attribution_sha256 is None),'native_attribution_arguments_required')
    view_inputs=None
    if attribution_inputs is not None:
        try:view_inputs=VIEWS.prepare(attribution_inputs,trusted_attribution_sha256,sources,analysis,schedule,plan_raw,index_raw,projection)
        except VIEWS.A.AnalysisError as error:raise A.AnalysisError(error.code) from None
    deadline = time.monotonic()+projection['verification_timeout_seconds']
    if _verification_deadline is not None:
        deadline = min(deadline,_verification_deadline)
    REF_reports = {}
    if final_protocol is not None:
        selected_REF, REF_evidence, REF_reports = resolve_REF_selection(plan,final_inputs,sources,roots,native_cli,deadline)
        final_protocol.update(REF_selection_semantics_verified=True,REF_selected_policy=selected_REF,REF_validation=REF_evidence)
    sources.read(plan['planning_dispatch'],'evidence','dispatch')
    gold, source_queries, alternatives = source_gold(projection,sources,queries)
    graded = grades(projection,sources,analysis,schedule,index)
    grade_attempts = defaultdict(list)
    for key, grade in graded.items():
        grade_attempts[key[:2]].append((key,grade))
    observations = dict(protocol='exp03-observations-v1',schedule_sha256=digest(schedule),
                        selection_stage='post_packing_final_fence',rows=[])
    reconciled, covered, verifications, raw_reports = [], set(), [], {}
    condition_by_query = {}
    report_bytes_total = 0
    verifier = None
    try:
        for job in plan['jobs']:
            require(set(job['policy_order']) <= set(analysis['policies']), 'analysis_drops_native_policy')
            slot = slots[job['job_id']]
            budget = budgets(job,sources,queries,gold,source_queries,projection)
            for name,role in [('eligibility_rule','eligibility_rule'),('encoder_revision_rule','encoder_rule'),
                              ('reader_generator_rule','reader_rule'),('seed_schedule_rule','seed_rule'),
                              ('events_file','events'),('features_file','features')]:
                sources.read(job['common'][name],'input',role)
            for expected in job['common']['expected_schedule']:
                key = (expected['stream_id'],expected['query_id'])
                common = job['common']
                fingerprint = digest(dict(expected=expected,clock_domain=common['clock_domain'],budget=budget[key],
                    registry=job['bindings']['registry_semantic'],
                    frozen_trace={name:common[name]['raw_sha256'] for name in ('events_file','features_file')},
                    rules={name:common[name]['raw_sha256'] for name in ('eligibility_rule','encoder_revision_rule',
                                                                      'reader_generator_rule','seed_schedule_rule')}))
                require(condition_by_query.setdefault(key,fingerprint)==fingerprint,'paired_conditions_mismatch')
            rows = by_job[job['job_id']]
            selected = _select(rows,slot['attempt_order'],projection['selection_rule'])
            predictions = {}
            for row in rows:
                sources.read(row['execution_dispatch'],'evidence','dispatch')
                trace_hashes = {}
                for name,role in [('raw_log','raw_log'),('native_completion_manifest','completion_manifest'),
                                  ('native_stdout_status','stdout_status')]:
                    if row[name] is not None:
                        sources.read(row[name],'output',role)
                        trace_hashes[name] = row[name]['raw_sha256']
                verified = False
                if row['status'] in COMPLETE:
                    if verifier is None:
                        verifier = NativeVerifier(native_cli,projection,deadline)
                    report, report_raw = verifier.verify(job,row,sources)
                    current = verified_rows(report,job,row,sources,budget,queries)
                    for key, grade in grade_attempts[(job['job_id'],row['attempt_id'])]:
                        native = current.get(key[2:])
                        require(native is not None and native['status'] == 'ok' and
                                grade['final_refs_sha256'] == digest(native['decision']['selected']), 'grade_native_result_mismatch')
                    report_file = 'native-verification-'+digest([job['job_id'],row['attempt_id']])+'.json'
                    require(report_file not in raw_reports, 'verification_name_collision')
                    raw_reports[report_file] = report_raw
                    report_bytes_total += len(report_raw)
                    require(report_bytes_total <= MAX_TOTAL_BYTES, 'verification_report_total_limit')
                    verifications.append(dict(job_id=job['job_id'],attempt_id=row['attempt_id'],report_sha256=A.sha(report_raw),report_file=report_file,
                                              completion_sha256=report['completion']['raw_sha256']))
                    verified = True
                    if row['attempt_id']==selected['attempt_id']:
                        predictions = current
                else:
                    require(not grade_attempts[(job['job_id'],row['attempt_id'])], 'grade_without_verified_retrieval')
                reconciled.append(dict(job_id=row['job_id'],attempt_id=row['attempt_id'],status=row['status'],
                                       selected_for_analysis=row['attempt_id']==selected['attempt_id'],native_replay_verified=verified,
                                       execution_dispatch_sha256=row['execution_dispatch']['raw_sha256'],trace_artifact_sha256=trace_hashes))
            for expected in job['common']['expected_schedule']:
                key = (expected['stream_id'],expected['query_id'])
                logical = (*key,slot['repetition'])
                require(logical not in covered, 'duplicate_analysis_slot')
                covered.add(logical)
                for policy in analysis['policies']:
                    observations['rows'].append(observation_with_REF_alias(queries[key],policy,slot['repetition'],selected,
                        predictions,analysis,graded,final_protocol['REF_selected_policy'] if final_protocol is not None else None,
                        job['policy_order']))
        require(covered == {(*key,r) for key in queries for r in range(analysis['replications'])}, 'uncovered_analysis_schedule')
        require(set(grade_attempts) <= {(r['job_id'],r['attempt_id']) for r in reconciled}, 'unmatched_grader_attempt')
        ledger = query_cost_unknowns(analysis,schedule,analysis_jobs)
        execution_costs = native_costs(index,projection,sources,analysis)
        # Public standalone analysis still refuses confirmatory. This internal
        # path requires actual native replay and final input/review joins.
        result = analyze_native_inputs(analysis,schedule,observations,ledger,final_protocol,verifications)
        result['native_execution_costs'] = execution_costs
        by_query = defaultdict(list)
        for r in observations['rows']:
            by_query[(r['stream_id'],r['query_id'])].append(r)
        for diagnostic in alternatives:
            sets = [A.ref_set(refs) for refs in diagnostic['alternative_ref_sets']]
            diagnostic['observations'] = [dict(policy_id=r['policy_id'],repetition=r['repetition'],status=r['status'],
                recall=max(len(A.ref_set(r['final_refs']) & relevant)/len(relevant) for relevant in sets)
                if r['status']=='success' else None) for r in by_query[(diagnostic['stream_id'],diagnostic['query_id'])]]
        result['native_projection'] = dict(protocol='exp03-native-projection-evidence-v1',projection_plan_sha256=digest(projection),
            run_plan_raw_sha256=A.sha(plan_raw),run_index_raw_sha256=A.sha(index_raw),native_cli_sha256=projection['native_cli_sha256'],
            attempts=reconciled,verifications=verifications,label_status='provided_provisional_gold_not_scientific_acceptance',
            selection_rule=projection['selection_rule'],analysis_timing=projection['analysis_timing'],
            alternative_gold_diagnostics=alternatives,grading_scope='bound_provided_labels_not_checker_truth_or_product_authority',
            native_cli_invoked=bool(verifications),scientific_lock_verified=False,product_authority_verified=False)
        if view_inputs is not None:
            try:result['attribution_sensitivity']=VIEWS.analyze(view_inputs,analysis,schedule,observations,ledger,result,execution_costs)
            except VIEWS.A.AnalysisError as error:raise A.AnalysisError(error.code) from None
        result['native_projection']['consumed_artifacts'] = [dict(root=root,relative_path=relative,
            raw_sha256=A.sha(raw),size_bytes=len(raw)) for (root,relative),raw in sorted(sources.raw.items())]
        result['native_projection']['materialization_scope'] = 'source_query_view_binding_not_revalidation_of_materialization_algorithm'
        raw_reports.update(REF_reports)
        sources.recheck()
        require(Path(__file__).read_bytes() == SOURCE_BYTES and all(path.read_bytes() == raw for path,raw in DEPENDENCY_BYTES.items()),
                'projection_code_changed')
        if sources.extension_code:
            result['native_projection'].update(protocol='exp03-native-projection-evidence-v2',
                extension_code_sha256={p.relative_to(HERE.parents[3]).as_posix():A.sha(raw)
                                       for p,raw in sorted(sources.extension_code.items())})
        return ProjectedArtifacts(dict(analysis=result,observations=observations,ledger=ledger,native_execution_costs=execution_costs),raw_reports,
                                  sources.extension_code)
    finally:
        if verifier is not None:
            verifier.close()


def publish(files, output):
    output = Path(output)
    require(output.is_absolute() and output.name not in ('','.','..') and '..' not in output.parts, 'unsafe_output_path')
    helpers = G._fixture_helpers()
    parent = external(G.IO._directory_fd, external(G.checked_directory, output.parent))
    fd, identity, written, complete = None, None, {}, False
    try:
        fd, identity = external(G._reserve,parent,output.name,helpers)
        for name, raw in files.items():
            require(len(raw)<=A.MAX_BYTES, 'output_limit')
            external(helpers._write_bytes,fd,name,raw,written)
            external(G._verify_at,fd,name,raw,written[name],helpers)
        os.fsync(fd)
        require(helpers._identity(os.stat(output.name,dir_fd=parent,follow_symlinks=False))==identity, 'output_changed')
        complete = True
    finally:
        try:
            if identity is not None and not complete:
                require(helpers._cleanup(parent,output.name,fd,identity,written), 'output_cleanup_failed')
        finally:
            if fd is not None:
                os.close(fd)
            os.close(parent)


def main(argv=None):
    parser = A.Parser(allow_abbrev=False)
    for name in ('analysis-plan','schedule','run-plan','run-index','projection-plan','input-root','evidence-root','output-root','native-cli','out'):
        parser.add_argument('--'+name,required=True,action=A.Once)
    for name in ('final-inputs','trusted-owner-review-sha256','attribution-inputs','trusted-attribution-sha256'):
        parser.add_argument('--'+name,action=A.Once)
    try:
        args = parser.parse_args(argv)
        require(bool(args.final_inputs) == bool(args.trusted_owner_review_sha256), 'final_review_arguments_required')
        require(bool(args.attribution_inputs)==bool(args.trusted_attribution_sha256),'native_attribution_arguments_required')
        paths = {name:getattr(args,name) for name in ('analysis_plan','schedule','run_plan','run_index','projection_plan')}
        raw = {name:external(G.bounded_read,Path(path)) for name,path in paths.items()}
        if args.final_inputs:
            paths['final_inputs'] = args.final_inputs
            raw['final_inputs'] = external(G.bounded_read,Path(args.final_inputs))
        if args.attribution_inputs:
            paths['attribution_inputs']=args.attribution_inputs
            raw['attribution_inputs']=external(G.bounded_read,Path(args.attribution_inputs))
        result = project(A.parse(raw['analysis_plan']),A.parse(raw['schedule']),raw['run_plan'],raw['run_index'],
            A.parse(raw['projection_plan']),dict(input=args.input_root,evidence=args.evidence_root,output=args.output_root),args.native_cli,
            A.parse(raw['final_inputs']) if args.final_inputs else None,args.trusted_owner_review_sha256,
            attribution_inputs=raw.get('attribution_inputs'),trusted_attribution_sha256=args.trusted_attribution_sha256)
        result['analysis']['provenance'] = dict(raw_input_sha256={name:A.sha(data) for name,data in raw.items()},
            projector_source_sha256=A.sha(SOURCE_BYTES),dependency_source_sha256={path.name:A.sha(data) for path,data in DEPENDENCY_BYTES.items()},
            command=['project_native.py','<explicit-file-and-root-arguments>'])
        require(all(external(G.bounded_read,Path(paths[name])) == data for name,data in raw.items()), 'projection_input_changed')
        files = result.encoded_files()
        publish(files,args.out)
        print(json.dumps(dict(protocol='exp03-native-projection-result-v1',status='computed_not_scientific_approval',
                              outputs={name:A.sha(data) for name,data in files.items()})))
        return 0
    except A.AnalysisError as error:
        code = error.code
    except OSError:
        code = 'file_io_error'
    except Exception:
        code = 'internal_error'
    print(json.dumps(dict(protocol='exp03-projection-error-v1',code=code,path='$')))
    return 2


if __name__ == '__main__':
    raise SystemExit(main())

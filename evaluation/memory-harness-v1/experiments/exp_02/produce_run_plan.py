"""Compile original observed sources into typed native plans, never approvals.

The emitted index is not_run. A separate execution dispatch and recorder must
bind the reviewed plan bytes before any policy execution. No gold reaches the
source materializer or native ranking inputs.
"""
import copy
import importlib.util
import json
import os
from pathlib import Path


HERE=Path(__file__).resolve().parent


def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    result=importlib.util.module_from_spec(spec);spec.loader.exec_module(result)
    return result

M=load('_plan_source_materializer',HERE/'materialize_native.py')
P=load('_plan_native_projection',HERE.with_name('exp_03')/'project_native.py')
A,B,G=P.A,P.B,M.G


def require(value,code):
    if not value:raise A.AnalysisError(code)


def artifact(path,raw,role):
    return dict(role=role,relative_path=path,raw_sha256=A.sha(raw),size_bytes=len(raw))


OWNER_ROLES=dict(matrix=('input','matrix'),controls=('input','controls'),
    exp01_draft=('input','owner_contract'),REF_contract=('input','owner_contract'),
    operating_plan=('input','operating_plan'),readiness=('input','readiness'),
    native_validation_evidence=('evidence','native_validation'),DYN05_acceptance=('evidence','acceptance'),
    gold_review_evidence=('evidence','acceptance'),evaluator_adapter_acceptance=('evidence','acceptance'))
RULE_ROLES=dict(eligibility_rule='eligibility_rule',encoder_revision_rule='encoder_rule',
    reader_generator_rule='reader_rule',seed_schedule_rule='seed_rule')
SOURCE_NAMES=('events','queries','gold','manifest','catalog','registry','analysis_plan','schedule','analysis_source')


def production_namespace(request):
    value=request.get('production_namespace','produced')
    require(B._primitive('ID',value),'invalid_production_namespace')
    return value


class GeneratedSources:
    def __init__(self,sources,files):self.sources,self.files=sources,files

    def read(self,ref,root,role):
        raw=self.files.get(ref['relative_path']) if root=='input' else None
        if raw is None:return self.sources.read(ref,root,role)
        B.validate_typed('ArtifactRef',ref)
        require(ref['role']==role and ref['raw_sha256']==A.sha(raw) and ref['size_bytes']==len(raw),
                'generated_artifact_binding_mismatch')
        return raw


def validate_request(request,sources):
    names=('protocol stage experiment_family pending_research_decisions protocol_lock planning_dispatch '
           'owner_bindings rules files materialization_plan condition_id verification_timeout_seconds')
    if request.get('protocol') in ('exp02-native-plan-request-v2','exp02-native-plan-request-v3'):names+=' production_namespace task_contract'
    if request.get('protocol')=='exp02-native-plan-request-v3':names+=' attempt_order artifact_namespace'
    A.fields(request,names)
    require(request['protocol'] in ('exp02-native-plan-request-v1','exp02-native-plan-request-v2','exp02-native-plan-request-v3'),'invalid_production_request')
    namespace=production_namespace(request)
    if request['protocol']=='exp02-native-plan-request-v3':
        attempts=request['attempt_order']
        require(type(attempts) is list and 1<=len(attempts)<=16 and
                all(B._primitive('ID',a) for a in attempts) and len(set(attempts))==len(attempts),'invalid_attempt_schedule')
        require(B._primitive('ID',request['artifact_namespace']) and request['artifact_namespace']!=namespace,
                'invalid_record_namespace')
    require(request['stage'] in B.ENUMS['RunnableStage'],'invalid_production_stage')
    for name in ('experiment_family','condition_id'):A.identifier(request[name])
    require(type(request['pending_research_decisions']) is list and
            all(type(s) is str for s in request['pending_research_decisions']),'invalid_research_decisions')
    require(type(request['verification_timeout_seconds']) is int and
            1<=request['verification_timeout_seconds']<=86400,'invalid_verification_budget')
    A.fields(request['files'],' '.join(SOURCE_NAMES))
    raw={name:sources.read(ref,'input','registry' if name=='registry' else 'source') for name,ref in request['files'].items()}
    if request.get('task_contract') is not None:
        P.bind_task_dataset(dict(protocol='exp03-native-projection-plan-v2',task_contract=request['task_contract']),sources,raw)
    A.fields(request['owner_bindings'],' '.join(OWNER_ROLES)+' REF_use')
    for name,(root,role) in OWNER_ROLES.items():
        ref=request['owner_bindings'][name]
        if name=='evaluator_adapter_acceptance' and ref is None:continue
        sources.read(ref,root,role)
    B.validate_typed('REFUse',request['owner_bindings']['REF_use'])
    B._ref_use(request['owner_bindings']['REF_use'],request['stage'])
    selected=request['owner_bindings']['REF_use']['selection_record']
    if selected is not None:sources.read(selected,'evidence','REF_selection')
    sources.read(request['planning_dispatch'],'evidence','dispatch')
    A.fields(request['rules'],' '.join(RULE_ROLES))
    for name,role in RULE_ROLES.items():sources.read(request['rules'][name],'input',role)
    if request['protocol_lock'] is not None:sources.read(request['protocol_lock'],'input','owner_contract')
    require(request['stage']!='confirmatory' or request['protocol_lock'] is not None,'final_lock_binding_required')
    require(not any(root=='input' and (path==namespace or path.startswith(namespace+'/'))
                    for root,path in sources.raw),'source_generated_namespace_collision')
    return raw


def produce(request,roots,native_executable,native_sha256,native_hash):
    """Return fresh files relative to a new input root. Does not execute policies.

    Owner artifacts are hash-bound declarations here. This compiler does not
    authenticate their authors or convert them into scientific authorization.
    """
    sources=P.Sources(roots)
    raw=validate_request(request,sources)
    namespace=production_namespace(request)
    A.hash_value(native_sha256)
    program=Path(native_executable)
    require(program.is_absolute() and A.sha(G.bounded_read(program))==native_sha256,'native_executable_mismatch')
    analysis,schedule=(A.parse(raw[n]) for n in ('analysis_plan','schedule'))
    A.validate_plan(analysis)
    analysis_queries,_,_=A.validate_schedule(analysis,schedule)
    require(analysis['experiment_family']==request['experiment_family'],'population_mismatch')
    require(analysis['population']=='synthetic' and set(B.ENUMS['RequiredPolicyID'])<=set(analysis['policies']),
            'required_policy_coverage_mismatch')
    require((analysis['stage']=='confirmatory')==(request['stage']=='confirmatory'),'final_stage_mismatch')
    manifest=G.decode_typed(raw['manifest'],'SplitManifest')
    require(all(manifest['files'][name+'_sha256']==A.sha(raw[name]) for name in ('events','queries','gold')),
            'source_manifest_raw_hash_mismatch')
    events=M.V._rows(raw['events'],'PlanEvent')
    queries=M.V._rows(raw['queries'],'QueryPlan');gold=M.V._rows(raw['gold'],'GoldRow')
    M.V.validate_artifacts(events,queries,gold,manifest)
    original={(q['stream_id'],q['query_id']):q for q in queries}
    require(len(original)==len(queries) and set(analysis_queries)<=set(original),'source_query_coverage_mismatch')
    maximum=len(analysis_queries)*analysis['replications']*len(B.ENUMS['RequiredPolicyID'])
    attempts=request.get('attempt_order',['attempt_1'])
    require(maximum*len(attempts)<=A.CONTRACT['maximum_rows'],'native_replay_work_limit')
    files={};total_bytes=0
    def put(path,value,role):
        nonlocal total_bytes
        require(path not in files,'generated_path_collision')
        data=value if type(value) is bytes else A.canonical(value)+b'\n'
        require(len(data)<=A.MAX_BYTES,'generated_file_limit')
        total_bytes+=len(data)
        require(total_bytes<=P.MAX_TOTAL_BYTES,'generated_total_limit')
        files[path]=data
        return artifact(path,data,role)
    source_ref=put(namespace+'/source.json',raw['analysis_source'],'source')
    require(all(q['input_sha256']==source_ref['raw_sha256'] for q in analysis_queries.values()),'analysis_source_mismatch')
    merged=GeneratedSources(sources,files)
    source_projection=dict(query_artifact=request['files']['queries'],gold_artifact=request['files']['gold'])
    labels,source_queries,_=P.source_gold(source_projection,merged,analysis_queries)
    jobs=[];slots=[];rows=[]
    for key,query in analysis_queries.items():
        material_plan=copy.deepcopy(request['materialization_plan'])
        require(material_plan['final_k']==query['budget']['final_k'],'materialization_final_k_mismatch')
        bundle=M.materialize(raw['events'],G.compact(original[key]),raw['catalog'],raw['registry'],
            M.compact(material_plan),native_hash,native_sha256)
        stem=namespace+'/'+A.digest(list(key))
        native_refs={}
        for name,data in bundle.items():native_refs[name]=put(stem+'/'+name,data,'source')
        def native_ref(name,role):return dict(native_refs[name],role=role)
        cfg=M.C.parse(bundle['config.json']);report=A.parse(bundle['materialization-report.json'])
        probe=A.parse(bundle['probes.json'])['probes'][0]
        budget=put(stem+'/budget.json',dict(protocol='exp03-native-budget-rule-v1',queries=[dict(
            stream_id=key[0],query_id=key[1],**query['budget'],context_budget=probe['context_budget'])]),'budget_rule')
        mapping=dict(stream_id=key[0],query_id=key[1],source_query_sha256=report['source_query_sha256'],
            source_gold_sha256=G.semantic_sha('GoldRow',labels[key]),source_cutoff_plan_seq=report['source_cutoff_plan_seq'],
            native_cutoff_accepted_seq=report['native_cutoff_accepted_seq'],requested_as_of_ns=report['requested_as_of_ns'],
            native_candidate_snapshot_sha256=report['native_candidate_snapshot_sha256'],
            clock_mapping='same_clock_identity_ns' if original[key]['clock_domain']==report['native_clock_domain'] else
                'renamed_synthetic_clock_identity_ns',source_query_at_ns=report['source_query_at_ns'],native_at_ns=report['native_at_ns'],
            source_view_rule=report['historical_scope'])
        mapping_ref=put(stem+'/mapping.json',dict(protocol='exp03-native-materialization-v2',
            source_manifest_raw_sha256=source_ref['raw_sha256'],native_input_manifest_raw_sha256=native_refs['p1.json']['raw_sha256'],
            queries=[mapping]),'native_materialization')
        bindings=dict(copy.deepcopy(request['owner_bindings']),registry_raw=native_ref('registry.json','registry'),
            registry_semantic=dict(type_name='PolicyRegistryV2',semantic_sha256=cfg['registry_sha256']),
            native_config_raw=native_ref('config.json','config'),
            native_config_semantic=dict(type_name='DynamicsRunConfigV2',semantic_sha256=cfg['config_sha256']),
            materialization_manifest=mapping_ref,native_input_manifest=native_ref('p1.json','native_input'))
        expected={k:probe[k] for k in ('stream_id','query_id','scope','at_ns','cutoff_accepted_seq',
                                      'candidate_snapshot_id','candidate_snapshot_sha256')}
        common=dict(copy.deepcopy(request['rules']),candidate_file=native_ref('candidates.json','candidates'),
            events_file=native_ref('events.json','events'),features_file=native_ref('features.json','features'),
            probe_file=native_ref('probes.json','probes'),source_manifest=source_ref,
            clock_domain=report['native_clock_domain'],budget_rule=budget,expected_schedule=[expected])
        for repetition in range(analysis['replications']):
            job_id='j'+A.digest([key,repetition])[:40];output='native-runs/'+job_id
            if request['protocol']=='exp02-native-plan-request-v3':output=request['artifact_namespace']+'/work/'+job_id
            args=[str(program),'run','--p1-manifest',str(sources.roots['input']/native_refs['p1.json']['relative_path']),
                '--config',str(sources.roots['input']/native_refs['config.json']['relative_path']),
                '--registry',str(sources.roots['input']/native_refs['registry.json']['relative_path']),
                '--out',str(sources.roots['output']/output)]
            job=dict(job_id=job_id,condition_id=request['condition_id'],comparison_family='fixed_candidate_mechanism',
                signal_regime='synthetic_all_candidate_oracle',bindings=copy.deepcopy(bindings),common=copy.deepcopy(common),
                policy_order=list(B.ENUMS['RequiredPolicyID']),native_argv=args,output_relative_dir=output,claim_control_ids=[])
            P.budgets(job,merged,analysis_queries,labels,source_queries,source_projection)
            jobs.append(job);slots.append(dict(job_id=job_id,repetition=repetition,attempt_order=list(attempts)))
            for attempt in attempts:
                rows.append(dict(job_id=job_id,attempt_id=attempt,plan_raw_sha256='0'*64,
                    execution_dispatch=request['planning_dispatch'],input_bindings=copy.deepcopy(bindings),actual_argv=[],
                    source_manifest=source_ref,started_at_ns=None,finished_at_ns=None,process_exit=None,status='not_run',
                    native_completion_manifest=None,native_stdout_status=None,raw_log=None,policy_rows=[],cost_measures=[],
                    deviations=[],execution_clock_domain=None))
    plan=dict(protocol='exp02-run-plan-v1',stage=request['stage'],experiment_family=request['experiment_family'],
        verification_scope='offline_synthetic',jobs=jobs,pending_research_decisions=request['pending_research_decisions'],
        claim_scope='engineering_pilot_or_separately_locked_confirmatory',protocol_lock=request['protocol_lock'],
        planning_dispatch=request['planning_dispatch'])
    B.validate_typed('RunPlan',plan);B.validate_plan_values(plan)
    plan_ref=put(namespace+'/plan.json',B.compact(plan),'plan')
    for row in rows:row['plan_raw_sha256']=plan_ref['raw_sha256']
    index=dict(protocol='exp02-run-index-v1',plan=plan_ref,rows=rows,analysis_status='not_scored_by_EXP02',
        evaluation_adapter=dict(owner_task='EXP-03',role='v2_evaluation_adapter',binding=None,
            expected_owner_schema=None,owner_acceptance=None))
    B.validate_typed('RunIndex',index);B.validate_index_values(index);B.validate_index_plan(index,plan,files[namespace+'/plan.json'])
    index_ref=put(namespace+'/index.json',B.compact(index),'source')
    projection=dict(protocol='exp03-native-projection-plan-v1',analysis_plan_sha256=analysis['plan_sha256'],
        schedule_sha256=A.digest(schedule),run_plan_raw_sha256=plan_ref['raw_sha256'],run_index_raw_sha256=index_ref['raw_sha256'],
        native_cli_sha256=native_sha256,verification_timeout_seconds=request['verification_timeout_seconds'],
        selection_rule='first_completed_else_last_attempted',analysis_timing='before_results',
        gold_artifact=request['files']['gold'],query_artifact=request['files']['queries'],grader_artifact=None,
        cost_source_root='output',price_source_root='evidence',slots=slots)
    if request.get('task_contract') is not None:projection.update(protocol='exp03-native-projection-plan-v2',task_contract=request['task_contract'])
    if request['protocol']=='exp02-native-plan-request-v3':projection.update(protocol='exp03-native-projection-plan-v3',
        task_contract=request['task_contract'],output_archive=dict(protocol='exp02-native-attempt-archive-v1',
            artifact_namespace=request['artifact_namespace']))
    put(namespace+'/projection.json',projection,'source')
    sources.recheck()
    require(A.sha(G.bounded_read(program))==native_sha256,'native_executable_changed')
    require(sum(map(len,files.values()))<=P.MAX_TOTAL_BYTES,'generated_total_limit')
    put(namespace+'/production.json',dict(protocol='exp02-native-plan-production-v1',
        request_sha256=A.digest(request),source_artifacts=[dict(root=r,relative_path=p,raw_sha256=A.sha(v),size_bytes=len(v))
            for (r,p),v in sorted(sources.raw.items())],jobs=len(jobs),planned_policy_rows=maximum,
        planned_attempts=len(rows),planned_physical_policy_rows=maximum*len(attempts),
        policy_processes_executed=0,hashing_backend='explicit_pinned_go_hash_callback',
        hash_process_count='not_measured_by_compiler',execution_authorized=False,owner_declarations_authenticated=False,
        scientific_approval_verified=False,product_authority_verified=False),'source')
    return files


def publish(files,input_root,output,namespace='produced'):
    """Publish one fresh owned subtree; never follow or remove foreign entries."""
    root=G.checked_directory(input_root)
    require(B._primitive('ID',namespace) and Path(output)==root/namespace,'production_output_root_mismatch')
    helpers=G._fixture_helpers()
    parent=G.IO._directory_fd(root)
    owned=[];nodes={};complete=False
    try:
        fd,identity=G._reserve(parent,namespace,helpers)
        nodes[namespace]=(fd,{})
        owned.append((parent,namespace,fd,identity,nodes[namespace][1]))
        for path,raw in files.items():
            require(B._relative(path) and path.startswith(namespace+'/'),'unsafe_generated_path')
            parts=path.split('/');prefix=namespace
            for name in parts[1:-1]:
                next_prefix=prefix+'/'+name
                if next_prefix not in nodes:
                    child,identity=G._reserve(nodes[prefix][0],name,helpers)
                    nodes[next_prefix]=(child,{})
                    owned.append((nodes[prefix][0],name,child,identity,nodes[next_prefix][1]))
                prefix=next_prefix
            fd,written=nodes[prefix]
            helpers._write_bytes(fd,parts[-1],raw,written)
            G._verify_at(fd,parts[-1],raw,written[parts[-1]],helpers)
        for parent_fd,name,fd,identity,_ in reversed(owned):
            os.fsync(fd)
            require(helpers._identity(os.stat(name,dir_fd=parent_fd,follow_symlinks=False))==identity,'production_output_changed')
        complete=True
    finally:
        clean=True
        for parent_fd,name,fd,identity,written in reversed(owned):
            try:
                if not complete:clean=helpers._cleanup(parent_fd,name,fd,identity,written) and clean
            finally:os.close(fd)
        os.close(parent)
        require(complete or clean,'production_output_cleanup_failed')


def main(argv=None):
    parser=A.Parser(allow_abbrev=False)
    for name in ('request','input-root','evidence-root','output-root','native-cli','native-cli-sha256','out'):
        parser.add_argument('--'+name,required=True,action=A.Once)
    try:
        args=parser.parse_args(argv)
        raw=G.bounded_read(Path(args.request));request=A.parse(raw)
        roots=dict(input=args.input_root,evidence=args.evidence_root,output=args.output_root)
        with M.NativeHashes(args.native_cli,args.native_cli_sha256,60) as hashes:
            files=produce(request,roots,args.native_cli,args.native_cli_sha256,hashes.hash)
        require(G.bounded_read(Path(args.request))==raw,'production_request_changed')
        namespace=production_namespace(request)
        publish(files,args.input_root,args.out,namespace)
        print(json.dumps(dict(protocol='exp02-native-plan-produced-v1',status='built_not_executed_or_authorized',
            plan_sha256=A.sha(files[namespace+'/plan.json']),index_sha256=A.sha(files[namespace+'/index.json']),
            policy_processes_executed=0,hash_stream_processes_started=1,execution_authorized=False)))
        return 0
    except (A.AnalysisError,M.MaterializationError,B.Exp02Error,M.B.Exp02Error,G.ExpError,G.IO.ContractError) as error:
        code=error.code
    except OSError:code='file_io_error'
    except Exception:code='internal_error'
    print(json.dumps(dict(protocol='exp02-native-production-error-v1',code=code,path='$')))
    return 2


if __name__=='__main__':
    raise SystemExit(main())

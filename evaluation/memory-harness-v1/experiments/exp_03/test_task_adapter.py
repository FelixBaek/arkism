"""Explicit fixture tasks on actual source/native/recorder/REF selection paths."""
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

HERE=Path(__file__).resolve().parent


def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    result=importlib.util.module_from_spec(spec);spec.loader.exec_module(result)
    return result


T=load('_test_literal_task_adapter',HERE/'task_adapter.py')
R=load('_task_actual_recorder',HERE.with_name('exp_02')/'record_native.py')
F=load('_task_actual_source_fixture',HERE.with_name('exp_02')/'test_produce_run_plan.py')
L=load('_task_lock_fixture',HERE/'test_locked_analysis.py')
A=T.A


class AdapterTests(unittest.TestCase):
    def test_locked_task_dataset_cannot_substitute_resealed_source_bytes(self):
        with tempfile.TemporaryDirectory() as tmp:
            f=F.Fixture(tmp);sources=T.P.Sources(f.roots)
            refs={n:f.request['files'][n] for n in ('events','queries','gold','manifest')}
            ref=f.write('task-source-binding.json',A.canonical(dict(source_files=refs)),'owner_contract')
            projection=dict(protocol='exp03-native-projection-plan-v2',task_contract=ref)
            T.P.bind_task_dataset(projection,sources,f.raw)
            changed=dict(refs);changed['events']=f.write('different-events.json',f.raw['events']+b'\n','source')
            ref=f.write('task-alternate-binding.json',A.canonical(dict(source_files=changed)),'owner_contract')
            with self.assertRaises(T.P.A.AnalysisError) as caught:
                T.P.bind_task_dataset(dict(projection,task_contract=ref),sources,f.raw)
            self.assertEqual(caught.exception.code,'task_locked_dataset_mismatch')

    def test_reader_only_extracts_observed_body_and_checker_is_separate(self):
        reader=dict(rule='first_visible_body_literal_prefix',prefix='Mina alpha ')
        self.assertEqual(T.read_answer(['unrelated','Mina alpha old'],reader),'old')
        self.assertFalse(T.check_answer('old',dict(accepted_answers=['new'])))
        self.assertTrue(T.check_answer('old',dict(accepted_answers=['old'])))
        self.assertIsNone(T.read_answer([],reader))
        self.assertTrue(T.check_answer(None,dict(accepted_answers=[None])))

    def test_task_identity_binds_bytes_and_code(self):
        self.assertNotEqual(T.identity(b'{}'),T.identity(b'{} '))
        self.assertEqual(len(T.identity(b'{}')),64)

    def test_token_measurement_fills_only_unmeasured_stage(self):
        row=dict(job_id='job',attempt_id='attempt',cost_measures=[])
        ref=dict(role='cost_source',relative_path='stage/cost.json',raw_sha256='1'*64,size_bytes=1)
        T.measure_tokens(row,'B0',ref)
        self.assertEqual([c['value_decimal'] for c in row['cost_measures']],['0','0'])
        with self.assertRaises(A.AnalysisError) as caught:T.measure_tokens(row,'B0',ref)
        self.assertEqual(caught.exception.code,'reader_cost_already_recorded')

    def test_historical_body_never_uses_future_or_current_withdrawn_ref(self):
        with tempfile.TemporaryDirectory() as tmp:
            f=F.Fixture(tmp);events=T.M.V._rows(f.raw['events'],'PlanEvent');queries=T.M.V._rows(f.raw['queries'],'QueryPlan')
            q=next(q for q in queries if q['request_mode']=='historical')
            old=next(e['target'] for e in events if e['stream_id']==q['stream_id'] and e['kind']=='material_available' and e['target']['revision']==1)
            new=next(e['target'] for e in events if e['stream_id']==q['stream_id'] and e['kind']=='material_available' and e['target']['revision']==2)
            self.assertEqual(len(T.contexts(events,q,[old,new])),1)
            withdrawn=next(e for e in events if e['stream_id']==q['stream_id'] and e['kind']=='scope_changed')
            later=dict(q,at_ns=withdrawn['available_at_ns'],cutoff_plan_seq=withdrawn['plan_seq'])
            self.assertEqual(T.contexts(events,later,[old,new]),[])
            withdrawn['target']=copy.deepcopy(new)
            self.assertEqual(T.contexts(events,later,[old,new]),[])


def locked_fixture(f):
    raw=L.fixture()
    assert raw['events']==f.raw['events'] and raw['queries']==f.raw['queries'] and raw['gold']==f.raw['gold']
    values=json.loads(raw['values'])
    values['values']['candidate_count']['value']=7
    values['values']['REF_id_order']['value']=[p for p in R.B.ENUMS['RequiredPolicyID'] if p!='B2']
    raw['values']=A.canonical(values)
    raw['lock']=L.B.L.lock_bytes(L.B.L.build_lock(raw['values'],raw['manifest']))
    return raw


def stage_inputs(f,raw,split):
    originals=T.M.V._rows(raw['queries'],'QueryPlan');labels={g['query_id']:g for g in T.M.V._rows(raw['gold'],'GoldRow')}
    streams={e['stream_id'] for e in json.loads(raw['manifest'])['entries'] if e['split']==split}
    # An exhaustive, predeclared historical-mode positive fixture. The original
    # dataset and all current/late/privacy failure events remain unchanged.
    qs=[q for q in originals if q['stream_id'] in streams and q['request_mode']=='historical']
    task=dict(protocol='exp03-literal-synthetic-task-v1',scope='model_free_synthetic_fixture_not_study_quality',
        stage_namespace=split+'-tasks',source_files={n:f.request['files'][n] for n in ('events','queries','gold','manifest')},tasks=[])
    events=T.M.V._rows(raw['events'],'PlanEvent')
    for q in qs:
        expected=labels[q['query_id']]['required_refs'][0]
        body=next(e['source_payload']['text'] for e in events if e['kind']=='material_available' and e['target']==expected)
        prefix,answer=body.rsplit(' ',1)
        task['tasks'].append(dict(stream_id=q['stream_id'],query_id=q['query_id'],source_query_sha256=T.G.semantic_sha('QueryPlan',q),
            reader=dict(rule='first_visible_body_literal_prefix',prefix=prefix+' '),
            checker=dict(rule='exact_answer_or_explicit_abstention',accepted_answers=[answer])))
    task_raw=A.canonical(task);task_ref=f.write(split+'-task-contract.json',task_raw,'owner_contract')
    plan=A.parse(raw['analysis_plan']);plan.update(stage='exploratory' if split=='validation' else 'confirmatory',
        request_modes=['as_of'],primary_metric='provided_grader_accuracy',grader_sha256=T.identity(task_raw),
        policies=list(R.B.ENUMS['RequiredPolicyID'])+([] if split=='validation' else ['REF']),
        cost_requirements=[dict(phase='reader_generator',resource=r,allocation='policy_incremental') for r in ('input_tokens','output_tokens')])
    plan['contrasts'][0]['control']='B0' if split=='validation' else 'REF'
    plan['plan_sha256']=A.digest({k:v for k,v in plan.items() if k!='plan_sha256'})
    schedule=dict(protocol='exp03-schedule-v1',analysis_plan_sha256=plan['plan_sha256'],queries=[dict(
        stream_id=q['stream_id'],query_id=q['query_id'],scope=q['scope'],cluster_id=q['stream_id'],request_mode='as_of',
        kind='answerable',gold_sets=[labels[q['query_id']]['required_refs']],
        budget=dict(final_k=3,max_input_tokens=100,max_output_tokens=80),input_sha256=A.sha(raw['analysis_source'])) for q in qs])
    A.validate_plan(plan);A.validate_schedule(plan,schedule)
    request=copy.deepcopy(f.request)
    request.update(protocol='exp02-native-plan-request-v2',production_namespace=split+'-input',task_contract=task_ref,
        stage='early_pilot' if split=='validation' else 'confirmatory',experiment_family=plan['experiment_family'])
    request['files'].update(analysis_plan=f.write(split+'-analysis.json',A.canonical(plan),'source'),
                            schedule=f.write(split+'-schedule.json',A.canonical(schedule),'source'))
    return plan,schedule,request,task_raw


def execute_stage(f,request,analysis,schedule,native_cli,raw=None,final_inputs=None,owner_sha=None):
    ns=request['production_namespace'];sha=A.sha(Path(native_cli).read_bytes())
    with R.N.M.NativeHashes(native_cli,sha,60) as hashes:files=R.N.produce(request,f.roots,native_cli,sha,hashes.hash)
    R.N.publish(files,f.root,f.root/ns,ns)
    projection=A.parse(files[ns+'/projection.json']);index=R.B.parse_raw(files[ns+'/index.json'])
    dispatch=dict(protocol='exp02-native-execution-dispatch-v2',scope='offline_native_fixture_only',
        plan_raw_sha256=A.sha(files[ns+'/plan.json']),index_raw_sha256=A.sha(files[ns+'/index.json']),
        projection_sha256=A.digest(projection),native_cli_sha256=sha,artifact_namespace=request['production_namespace']+'-recorded',
        attempts=[dict(job_id=r['job_id'],attempt_id=r['attempt_id']) for r in index['rows']],timeout_ns=60000000000)
    if request['protocol']=='exp02-native-plan-request-v3':
        dispatch.update(protocol='exp02-native-execution-dispatch-v3',artifact_namespace=request['artifact_namespace'],
            attempts=[dict(job_id=r['job_id'],attempt_id=r['attempt_id'],timeout_ns=60000000000) for r in index['rows']])
    if raw is not None:
        # The prospective synthetic owner pin binds these exact plan/rules.
        gold,owner,_,_=L.reviewed_fixture(raw,files[ns+'/plan.json'],projection)
        final_inputs=dict(final_inputs,gold_review=f.write('final-gold-review.json',gold,'acceptance'),
                          owner_review=f.write('final-owner-review.json',owner,'owner_contract'))
        owner_sha=A.sha(owner)
    dispatch_raw=A.canonical(dispatch);ref=f.write(ns+'-execution-dispatch.json',dispatch_raw,'dispatch')
    recorded,projection,report=R.record(files[ns+'/plan.json'],files[ns+'/index.json'],projection,analysis,schedule,
        ref,A.sha(dispatch_raw),f.roots,native_cli,final_inputs,owner_sha)
    assert report['planned_native_run_processes_started']==len(index['rows']) and report['native_replays_verified']==len(index['rows']),report
    return files[ns+'/plan.json'],R.B.compact(recorded),projection,final_inputs,owner_sha


def task_stage(f,analysis,schedule,plan_raw,index_raw,projection,task_raw,native_cli,final_inputs=None,owner_sha=None):
    task=A.parse(task_raw);ns=task['stage_namespace']
    args=[sys.executable,'-B',str(HERE/'task_adapter.py'),'--native-cli',native_cli,
          '--trusted-task-sha256',A.sha(task_raw),'--out',str(f.root/ns)]
    inputs={'analysis-plan':A.canonical(analysis),'schedule':A.canonical(schedule),'run-plan':plan_raw,
            'run-index':index_raw,'projection-plan':A.canonical(projection),'task-contract':task_raw}
    if final_inputs is not None:
        inputs['final-inputs']=A.canonical(final_inputs);args+=['--trusted-owner-review-sha256',owner_sha]
    for name,raw in inputs.items():
        path=f.root/(ns+'-input-'+name+'.json');path.write_bytes(raw);args+=['--'+name,str(path)]
    for name,path in f.roots.items():args+=['--'+name+'-root',path]
    executed=subprocess.run(args,capture_output=True,text=True,timeout=60)
    assert executed.returncode==0,(executed.stdout,executed.stderr)
    assert json.loads(executed.stdout)['scientific_approval_verified'] is False
    files={p.name:p.read_bytes() for p in (f.root/ns).iterdir()}
    graded=T.P.project(analysis,schedule,plan_raw,files['index.json'],A.parse(files['projection.json']),f.roots,native_cli,final_inputs,owner_sha)
    assert all(r['correct'] is True for r in graded['observations']['rows']),graded['observations']
    assert len(files['grades.json'])>0
    return files,graded


def integration(root,native_cli,retries=False):
    f=F.Fixture(root);(f.root/'native-runs').mkdir();raw=locked_fixture(f)
    final_analysis,final_schedule,final_request,final_task=stage_inputs(f,raw,'final_test')
    if retries:final_request.update(protocol='exp02-native-plan-request-v3',attempt_order=['attempt_1','attempt_2'],
        artifact_namespace='final-retry-recorded')
    raw.update(analysis_plan=A.canonical(final_analysis),schedule=A.canonical(final_schedule))
    L.B.bind(raw,'before_results')
    # Real source files and fixed final inputs, not precomputed analysis rows.
    locked_refs={name:f.write('locked-'+name+'.json',data,'owner_contract' if name=='lock' else 'source') for name,data in raw.items()}
    val_analysis,val_schedule,val_request,val_task=stage_inputs(f,raw,'validation')
    if retries:val_request.update(protocol='exp02-native-plan-request-v3',attempt_order=['attempt_1','attempt_2'],
        artifact_namespace='validation-retry-recorded')
    val_plan,val_index,val_projection,_,_=execute_stage(f,val_request,val_analysis,val_schedule,native_cli)
    val_files,val_result=task_stage(f,val_analysis,val_schedule,val_plan,val_index,val_projection,val_task,native_cli)
    assert len(val_result['observations']['rows'])==7
    # Correctly resealed grader/cost JSON cannot replace deterministic replay.
    path=f.root/'validation-tasks/grades.json';good_grade=path.read_bytes()
    changed=A.parse(good_grade);changed['rows'][0]['correct']=False
    bad_grade=A.canonical(changed);path.write_bytes(bad_grade)
    p=A.parse(val_files['projection.json']);p['grader_artifact']=R.N.artifact('validation-tasks/grades.json',bad_grade,'source')
    try:T.P.project(val_analysis,val_schedule,val_plan,val_files['index.json'],p,f.roots,native_cli)
    except T.P.A.AnalysisError as issue:assert issue.code=='task_grade_replay_mismatch',issue.code
    else:raise AssertionError('resealed incorrect grade accepted')
    path.write_bytes(good_grade)
    bad_index=R.B.parse_raw(val_files['index.json'])
    cost=next(c for c in bad_index['rows'][0]['cost_measures'] if c['resource']=='input_tokens')
    path=f.root/cost['source_artifact']['relative_path'];good_cost=path.read_bytes()
    changed=A.parse(good_cost);changed['reader_calls']=True;bad_cost=A.canonical(changed);path.write_bytes(bad_cost)
    for c in bad_index['rows'][0]['cost_measures']:
        if c['source_artifact'] is not None and c['source_artifact']['relative_path']==cost['source_artifact']['relative_path']:
            c['source_artifact']=R.N.artifact(c['source_artifact']['relative_path'],bad_cost,'cost_source')
    p=A.parse(val_files['projection.json']);p['run_index_raw_sha256']=A.sha(R.B.compact(bad_index))
    try:T.P.project(val_analysis,val_schedule,val_plan,R.B.compact(bad_index),p,f.roots,native_cli)
    except T.P.A.AnalysisError as issue:assert issue.code=='task_cost_replay_mismatch',issue.code
    else:raise AssertionError('boolean counter accepted')
    path.write_bytes(good_cost)
    # A fully resealed actual origin index may not alter non-task metadata.
    cost_sources={}
    original_index=R.B.parse_raw(val_files['index.json'])
    for row in original_index['rows']:
        for c in row['cost_measures']:
            if c['resource'] in ('input_tokens','output_tokens'):
                cost_sources[c['source_artifact']['relative_path']]=(f.root/c['source_artifact']['relative_path']).read_bytes()
    origin_path=f.root/A.parse(next(iter(cost_sources.values())))['native_index_artifact']['relative_path']
    good_origin=origin_path.read_bytes();changed=R.B.parse_raw(good_origin)
    changed['rows'][0]['finished_at_ns']+=1;bad_origin=R.B.compact(changed);origin_path.write_bytes(bad_origin)
    bad_index=R.B.parse_raw(val_files['index.json']);new_sources={}
    for relative,good in cost_sources.items():
        value=A.parse(good);value['native_index_raw_sha256']=A.sha(bad_origin)
        value['native_index_artifact']=R.N.artifact(origin_path.relative_to(f.root).as_posix(),bad_origin,'source')
        data=A.canonical(value);(f.root/relative).write_bytes(data)
        new_sources[relative]=R.N.artifact(relative,data,'cost_source')
    for row in bad_index['rows']:
        for c in row['cost_measures']:
            if c['source_artifact'] is not None and c['source_artifact']['relative_path'] in new_sources:
                c['source_artifact']=new_sources[c['source_artifact']['relative_path']]
    p=A.parse(val_files['projection.json']);p['run_index_raw_sha256']=A.sha(R.B.compact(bad_index))
    try:T.P.project(val_analysis,val_schedule,val_plan,R.B.compact(bad_index),p,f.roots,native_cli)
    except T.P.A.AnalysisError as issue:assert issue.code=='task_origin_index_transformation_mismatch',issue.code
    else:raise AssertionError('unrelated origin execution metadata accepted')
    origin_path.write_bytes(good_origin)
    for relative,good in cost_sources.items():(f.root/relative).write_bytes(good)
    changed=A.parse(good_cost);changed['native_index_raw_sha256']='f'*64
    bad_cost=A.canonical(changed);path.write_bytes(bad_cost)
    bad_index=R.B.parse_raw(val_files['index.json'])
    for c in bad_index['rows'][0]['cost_measures']:
        if c['source_artifact'] is not None and c['source_artifact']['relative_path']==cost['source_artifact']['relative_path']:
            c['source_artifact']=R.N.artifact(c['source_artifact']['relative_path'],bad_cost,'cost_source')
    p=A.parse(val_files['projection.json']);p['run_index_raw_sha256']=A.sha(R.B.compact(bad_index))
    try:T.P.project(val_analysis,val_schedule,val_plan,R.B.compact(bad_index),p,f.roots,native_cli)
    except T.P.A.AnalysisError as issue:assert issue.code=='task_origin_index_mismatch',issue.code
    else:raise AssertionError('unrelated original index SHA accepted')
    path.write_bytes(good_cost)
    rule=dict(protocol='exp01-ref-selection-rule-v2',protocol_lock_raw_sha256=A.sha(raw['lock']),manifest_raw_sha256=A.sha(raw['manifest']),
        validation_analysis_plan_sha256=val_analysis['plan_sha256'],eligible_policies=[p for p in R.B.ENUMS['RequiredPolicyID'] if p!='B2'],
        request_modes=['as_of'],tie_tolerance='0.01',token_cost_phases=['reader_generator'],id_tie_break='listed_order',
        id_order=[p for p in R.B.ENUMS['RequiredPolicyID'] if p!='B2'])
    def spec(name,data,where='input',role='source'):
        return dict(root=where,artifact=f.write(name,data,role))
    selection_request=dict(protocol='exp01-ref-selection-request-v2',files=locked_refs,
        rule=spec('REF-rule.json',A.canonical(rule),role='owner_contract'),validation=dict(
            analysis_plan=spec('val-analysis-projection.json',A.canonical(val_analysis)),schedule=spec('val-schedule-projection.json',A.canonical(val_schedule)),
            run_plan=spec('val-native-plan.json',val_plan,role='plan'),run_index=spec('val-native-index.json',val_files['index.json']),
            projection_plan=spec('val-native-projection.json',val_files['projection.json'])))
    path=f.root/'REF-request.json';path.write_bytes(A.canonical(selection_request))
    args=[sys.executable,'-B',str(HERE/'ref_selection.py'),'--request',str(path),'--native-cli',native_cli,'--out',str(f.root/'REF-selection')]
    for name,path in f.roots.items():args+=['--'+name+'-root',path]
    selected=subprocess.run(args,capture_output=True,text=True,timeout=60)
    assert selected.returncode==0,(selected.stdout,selected.stderr)
    status=json.loads(selected.stdout);assert status['status']=='selected' and status['selected_policy']=='B0',status
    record=A.parse((f.root/'REF-selection/selection.json').read_bytes())
    assert record['expected']['observed_token_costs']=={p:'0' for p in rule['eligible_policies']}
    # B2 is a treatment; its native run and grade exist but never select it.
    assert record['expected']['selected_policy']!='B2'
    selection_raw=(f.root/'REF-selection/selection.json').read_bytes()
    final_request['protocol_lock']=locked_refs['lock']
    final_request['owner_bindings']['REF_use']=dict(mode='uses_selected_REF',selection_status='selected_record_required',
        selection_record=R.N.artifact('REF-selection/selection.json',selection_raw,'REF_selection'))
    final_inputs=dict(protocol='exp03-final-inputs-v1',files=locked_refs)
    final_plan,final_index,final_projection,final_inputs,owner_sha=execute_stage(f,final_request,final_analysis,final_schedule,
        native_cli,raw,final_inputs)
    final_files,final_result=task_stage(f,final_analysis,final_schedule,final_plan,final_index,final_projection,final_task,
        native_cli,final_inputs,owner_sha)
    observations=final_result['observations']['rows']
    assert len(observations)==8 and all(r['correct'] is True for r in observations)
    b0=next(r for r in observations if r['policy_id']=='B0');alias=next(r for r in observations if r['policy_id']=='REF')
    assert all(b0[k]==alias[k] for k in ('status','correct','final_refs','repetition'))
    binding=final_result['analysis']['final_protocol_binding']
    assert binding['REF_selection_semantics_verified'] is True and binding['REF_selected_policy']=='B0'
    assert binding['scientific_approval_verified'] is False and binding['execution_authorized'] is False
    assert len(final_result['analysis']['native_projection']['verifications'])==(2 if retries else 1)
    if retries:
        for result in (val_result,final_result):
            attempts=result['analysis']['native_projection']['attempts']
            assert len(attempts)==2 and [a['selected_for_analysis'] for a in attempts]==[True,False]
            assert {c['attempt_id'] for c in result['analysis']['native_execution_costs']['records']}=={'attempt_1','attempt_2'}
        assert len(A.parse(val_files['grades.json'])['rows'])==14
        assert len(A.parse(final_files['grades.json'])['rows'])==14
    assert final_result['analysis']['native_projection']['extension_code_sha256']
    assert final_result['analysis']['stage']=='confirmatory'
    assert raw['events']==f.raw['events']
    print('actual source→producer/recorder→literal-task CLI→graded7rows→REF CLI selects B0/6nonB2→final native/ownerpin→literal-task CLI→confirmatory8rows with REFalias; resealed wronggrade/cost rejected; unchanged fullsource; no model/study approval')
    if retries:print('v3 actual repeated native outputs→14grades/stage but7/8primary rows→REF→final: duplicate completions excluded, every attempt/cost retained')
    return dict(f=f,raw=raw,analysis=final_analysis,schedule=final_schedule,run_plan=final_plan,
                run_index=final_files['index.json'],projection=A.parse(final_files['projection.json']),
                final_inputs=final_inputs,trusted_owner_sha=owner_sha,result=final_result)


if __name__=='__main__':
    if len(sys.argv)==4 and sys.argv[1]=='--integration':integration(Path(sys.argv[2]),sys.argv[3])
    else:unittest.main()

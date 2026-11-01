"""Real source/compiler/native joins; owner declarations are synthetic fixtures."""
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE=Path(__file__).resolve().parent


def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    result=importlib.util.module_from_spec(spec);spec.loader.exec_module(result)
    return result


N=load('_test_native_plan_producer',HERE/'produce_run_plan.py')
T=load('_producer_locked_source_fixture',HERE.with_name('exp_03')/'test_locked_analysis.py')
MT=load('_producer_source_admissions_fixture',HERE/'test_materialize_native.py')
A=N.A


class Fixture:
    def __init__(self,root):
        self.root=Path(root);self.root.mkdir(exist_ok=True)
        self.roots={k:str(self.root) for k in ('input','evidence','output')}
        self.raw=T.fixture()
        plan=A.parse(self.raw['analysis_plan'])
        plan.update(stage='exploratory',policies=list(N.B.ENUMS['RequiredPolicyID']))
        plan['plan_sha256']=A.digest({k:v for k,v in plan.items() if k!='plan_sha256'})
        schedule=A.parse(self.raw['schedule']);schedule['analysis_plan_sha256']=plan['plan_sha256']
        self.raw.update(analysis_plan=A.canonical(plan),schedule=A.canonical(schedule))
        self.plan,self.schedule=plan,schedule
        events=N.M.V._rows(self.raw['events'],'PlanEvent')
        catalog=dict(protocol='exp02-synthetic-admission-v1',scope='offline_fixture_only_not_product_authority',
            events_raw_sha256=A.sha(self.raw['events']),admissions=[],outcomes=[])
        for e in events:
            if e['kind']!='material_available':continue
            activation=e['event_id']
            if e['target']['revision']>1:
                activation=next(c['event_id'] for c in events if c['stream_id']==e['stream_id'] and
                    c['kind']=='correction_committed' and e['target'] in c['related_refs'])
            catalog['admissions'].append(dict(ref=e['target'],scope=e['scope'],source_event_id=e['event_id'],
                activation_event_id=activation,admission='explicit_synthetic_fixture',trust='0.5',context_cost_units=1,
                effective_at_ns=e['source_payload']['created_at_ns'],effective_basis='explicit_synthetic_fixture',
                historical_body_available=True,importance='0.5',importance_rule_sha256=A.sha(b'explicit fixture importance')))
        self.raw['catalog']=A.canonical(catalog)
        self.raw['registry']=MT.Fixture().registry
        files={name:self.write('source_'+name+'.json',self.raw[name],'registry' if name=='registry' else 'source')
               for name in N.SOURCE_NAMES}
        stub=b'{"synthetic_metadata_not_acceptance":true}\n'
        owners={name:self.write('owner_'+name+'.json',stub,role) for name,(_,role) in N.OWNER_ROLES.items()}
        owners['REF_use']=dict(mode='validation_data_collection',selection_status='not_run_pending_selection',selection_record=None)
        self.request=dict(protocol='exp02-native-plan-request-v1',stage='early_pilot',experiment_family=plan['experiment_family'],
            pending_research_decisions=['synthetic owner declarations not human acceptance'],protocol_lock=None,
            planning_dispatch=self.write('planning_dispatch.json',stub,'dispatch'),owner_bindings=owners,
            rules={name:self.write(name+'.json',stub,role) for name,role in N.RULE_ROLES.items()},files=files,
            materialization_plan=MT.Fixture().plan,condition_id='synthetic_native_fixture',verification_timeout_seconds=60)
        self.request['materialization_plan']['final_k']=3

    def write(self,name,raw,role):
        (self.root/name).write_bytes(raw)
        return N.artifact(name,raw,role)


class ProducerTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.f=Fixture(self.temp.name)

    def validate(self):return N.validate_request(self.f.request,N.P.Sources(self.f.roots))

    def test_v2_phase_namespace_is_closed_and_v1_cannot_add_it(self):
        self.f.request['production_namespace']='phase'
        with self.assertRaises(A.AnalysisError) as caught:self.validate()
        self.assertEqual(caught.exception.code,'invalid_fields')
        self.f.request.update(protocol='exp02-native-plan-request-v2',task_contract=None)
        self.assertEqual(set(self.validate()),set(N.SOURCE_NAMES))
        self.f.request['production_namespace']='../escape'
        with self.assertRaises(A.AnalysisError) as caught:self.validate()
        self.assertEqual(caught.exception.code,'invalid_production_namespace')

    def test_phase_publication_preserves_prior_namespace(self):
        N.publish({'phase-one/file':b'one'},self.f.root,self.f.root/'phase-one','phase-one')
        N.publish({'phase-two/file':b'two'},self.f.root,self.f.root/'phase-two','phase-two')
        self.assertEqual((self.f.root/'phase-one/file').read_bytes(),b'one')
        self.assertEqual((self.f.root/'phase-two/file').read_bytes(),b'two')

    def test_bound_sources_do_not_make_owner_declarations_authenticated(self):
        raw=self.validate()
        self.assertEqual(set(raw),set(N.SOURCE_NAMES))
        self.assertIn(b'synthetic_metadata_not_acceptance',(self.f.root/'owner_readiness.json').read_bytes())

    def test_changed_owner_bytes_reject_before_native_hash_or_execution(self):
        (self.f.root/'owner_readiness.json').write_bytes(b'changed')
        with self.assertRaises(A.AnalysisError) as caught:self.validate()
        self.assertEqual(caught.exception.code,'artifact_hash_mismatch')

    def test_confirmatory_stage_requires_actual_lock_artifact(self):
        self.f.request['stage']='confirmatory'
        self.f.request['owner_bindings']['REF_use']=dict(mode='uses_selected_REF',selection_status='selected_record_required',
            selection_record=self.f.write('selected.json',b'{"synthetic_not_real_selection":true}','REF_selection'))
        with self.assertRaises(A.AnalysisError) as caught:self.validate()
        self.assertEqual(caught.exception.code,'final_lock_binding_required')

    def test_unknown_job_command_capability_is_not_an_input(self):
        self.f.request['command']=['secret_program']
        with self.assertRaises(A.AnalysisError) as caught:self.validate()
        self.assertEqual(caught.exception.code,'invalid_fields')

    def test_nested_publication_is_fresh_and_refuses_overwrite(self):
        files={'produced/p.json':b'plan','produced/bundle/native.json':b'native'}
        N.publish(files,self.f.root,self.f.root/'produced')
        self.assertEqual((self.f.root/'produced/bundle/native.json').read_bytes(),b'native')
        with self.assertRaises(FileExistsError):N.publish(files,self.f.root,self.f.root/'produced')
        self.assertEqual((self.f.root/'produced/p.json').read_bytes(),b'plan')

    def test_failed_publication_removes_only_own_tree(self):
        helpers=N.G._fixture_helpers();original=helpers._write_bytes
        def fail(directory,name,raw,owned):
            original(directory,name,raw,owned)
            raise OSError('synthetic injected write failure')
        with mock.patch.object(N.G,'_fixture_helpers',return_value=helpers),mock.patch.object(helpers,'_write_bytes',side_effect=fail):
            with self.assertRaises(OSError):N.publish({'produced/child/file':b'owned'},self.f.root,self.f.root/'produced')
        self.assertFalse((self.f.root/'produced').exists())

    def test_foreign_content_is_preserved_on_failed_publication(self):
        helpers=N.G._fixture_helpers();original=helpers._write_bytes
        def fail(directory,name,raw,owned):
            original(directory,name,raw,owned)
            (self.f.root/'produced'/'foreign').write_bytes(b'foreign')
            raise OSError('synthetic injected foreign entry')
        with mock.patch.object(N.G,'_fixture_helpers',return_value=helpers),mock.patch.object(helpers,'_write_bytes',side_effect=fail):
            with self.assertRaises(A.AnalysisError) as caught:
                N.publish({'produced/child/file':b'owned'},self.f.root,self.f.root/'produced')
        self.assertEqual(caught.exception.code,'production_output_cleanup_failed')
        self.assertEqual((self.f.root/'produced'/'foreign').read_bytes(),b'foreign')

    def test_publication_cannot_escape_fixed_produced_directory(self):
        with self.assertRaises(A.AnalysisError) as caught:
            N.publish({'produced/file':b'owned'},self.f.root,self.f.root/'elsewhere')
        self.assertEqual(caught.exception.code,'production_output_root_mismatch')
        self.assertFalse((self.f.root/'elsewhere').exists())

    def test_final_k_mismatch_rejects_before_hashing(self):
        self.f.request['materialization_plan']['final_k']=2
        program=Path(sys.executable).resolve();sha=A.sha(program.read_bytes())
        with self.assertRaises(A.AnalysisError) as caught:
            N.produce(self.f.request,self.f.roots,str(program),sha,lambda *args:self.fail('must not hash mismatched budget'))
        self.assertEqual(caught.exception.code,'materialization_final_k_mismatch')

    def test_owner_source_cannot_overlap_reserved_generated_namespace(self):
        (self.f.root/'produced').mkdir()
        raw=self.f.raw['analysis_source'];(self.f.root/'produced/source.json').write_bytes(raw)
        self.f.request['files']['analysis_source']=N.artifact('produced/source.json',raw,'source')
        with self.assertRaises(A.AnalysisError) as caught:self.validate()
        self.assertEqual(caught.exception.code,'source_generated_namespace_collision')

    def test_manifest_raw_source_hash_mismatch_rejects_before_hashing(self):
        original=json.loads(self.f.raw['manifest'])
        for name in ('events','queries','gold'):
            with self.subTest(name=name):
                manifest=copy.deepcopy(original);manifest['files'][name+'_sha256']='f'*64
                self.f.request['files']['manifest']=self.f.write('source_manifest.json',A.canonical(manifest),'source')
                program=Path(sys.executable).resolve();sha=A.sha(program.read_bytes())
                with self.assertRaises(A.AnalysisError) as caught:
                    N.produce(self.f.request,self.f.roots,str(program),sha,lambda *args:self.fail('must not hash inconsistent source'))
                self.assertEqual(caught.exception.code,'source_manifest_raw_hash_mismatch')

    def test_cli_rejects_extra_command_without_echoing_arguments(self):
        secret='PRIVATE_EXECUTION_ARGUMENT_SENTINEL'
        args=[sys.executable,'-B',str(HERE/'produce_run_plan.py')]
        for name in ('request','input-root','evidence-root','output-root','native-cli','native-cli-sha256','out'):
            args+=['--'+name,'/unused']
        args+=['--command',secret]
        result=subprocess.run(args,capture_output=True,text=True,timeout=30)
        self.assertEqual(result.returncode,2)
        self.assertEqual(json.loads(result.stdout)['code'],'invalid_arguments')
        self.assertNotIn(secret,result.stdout+result.stderr)
        self.assertEqual(result.stderr,'')


def integration(root,native_cli):
    f=Fixture(root);native_sha=A.sha(Path(native_cli).read_bytes())
    # Valid byte-distinct source manifest remains original, never normalized.
    f.raw['analysis_source']=b' '+f.raw['analysis_source']+b'\n'
    f.request['files']['analysis_source']=f.write('source_analysis_source.json',f.raw['analysis_source'],'source')
    for q in f.schedule['queries']:q['input_sha256']=A.sha(f.raw['analysis_source'])
    f.raw['schedule']=A.canonical(f.schedule)
    f.request['files']['schedule']=f.write('source_schedule.json',f.raw['schedule'],'source')
    with N.M.NativeHashes(native_cli,native_sha,60) as hashes:
        files=N.produce(f.request,f.roots,native_cli,native_sha,hashes.hash)
    assert files['produced/source.json']==f.raw['analysis_source']
    for name,raw in files.items():
        target=f.root/name;target.parent.mkdir(parents=True,exist_ok=True);target.write_bytes(raw)
    report=A.parse(files['produced/production.json'])
    assert report['policy_processes_executed']==0 and report['execution_authorized'] is False
    assert report['hash_process_count']=='not_measured_by_compiler'
    assert report['owner_declarations_authenticated'] is False
    plan=A.parse(files['produced/plan.json']);index=A.parse(files['produced/index.json'])
    projection=A.parse(files['produced/projection.json'])
    assert len(plan['jobs'])==len(f.schedule['queries'])==6
    assert all(row['status']=='not_run' and not row['policy_rows'] for row in index['rows'])
    result=N.P.project(f.plan,f.schedule,files['produced/plan.json'],files['produced/index.json'],projection,f.roots,native_cli)
    assert len(result['observations']['rows'])==42
    assert all(row['status']=='not_run' for row in result['observations']['rows'])
    # Compiler artifacts actually run through the original engine. A later
    # production recorder must record these outcomes, never forge this index.
    (f.root/'native-runs').mkdir()
    failures=0
    for job in plan['jobs']:
        run=subprocess.run(job['native_argv'],capture_output=True,text=True,timeout=30)
        assert run.returncode==0,(run.stdout,run.stderr)
        status=json.loads(run.stdout)
        assert status['status'] in ('completed','completed_with_failures'),status
        completion=json.loads((f.root/job['output_relative_dir']/'manifest.json').read_bytes())
        assert completion['planned_rows']==7
        failures+=completion['failed_rows']
        args=list(job['native_argv']);args[1]='verify'
        verified=subprocess.run(args,capture_output=True,text=True,timeout=30)
        assert verified.returncode==0,(verified.stdout,verified.stderr)
        assert json.loads(verified.stdout)['status']=='replay_matched'
    assert failures==4,failures
    # The original emitted index stays unexecuted, not retrospectively invented.
    assert all(row['status']=='not_run' for row in A.parse(files['produced/index.json'])['rows'])
    cli=Fixture(f.root/'cli');request=cli.root/'request.json';request.write_bytes(A.canonical(cli.request))
    args=[sys.executable,'-B',str(HERE/'produce_run_plan.py'),'--request',str(request),
          '--native-cli',native_cli,'--native-cli-sha256',native_sha,'--out',str(cli.root/'produced')]
    for name,path in cli.roots.items():args+=['--'+name+'-root',path]
    produced=subprocess.run(args,capture_output=True,text=True,timeout=60)
    assert produced.returncode==0,(produced.stdout,produced.stderr)
    assert json.loads(produced.stdout)['status']=='built_not_executed_or_authorized'
    assert json.loads(produced.stdout)['hash_stream_processes_started']==1
    assert json.loads(produced.stdout)['policy_processes_executed']==0
    repeated=subprocess.run(args,capture_output=True,text=True,timeout=60)
    assert repeated.returncode==2 and repeated.stderr=='',(repeated.stdout,repeated.stderr)
    cli_projection=A.parse((cli.root/'produced/projection.json').read_bytes())
    cli_result=N.P.project(cli.plan,cli.schedule,(cli.root/'produced/plan.json').read_bytes(),
        (cli.root/'produced/index.json').read_bytes(),cli_projection,cli.roots,native_cli)
    assert len(cli_result['observations']['rows'])==42 and all(r['status']=='not_run' for r in cli_result['observations']['rows'])
    print('original source -> typed producer6jobs/42rows -> original seven-policy run+verify;4late-outcome failures preserved; emitted index correctly not_run; no fake approval')


if __name__=='__main__':
    if len(sys.argv)==4 and sys.argv[1]=='--integration':integration(Path(sys.argv[2]),sys.argv[3])
    else:unittest.main()

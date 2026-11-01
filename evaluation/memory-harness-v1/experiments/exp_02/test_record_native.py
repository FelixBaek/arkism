"""Actual typed producer/recorder/native replay; no invented completion rows."""
import copy
import importlib.util
import json
from pathlib import Path
import sys
import subprocess
import tempfile
import unittest
from unittest import mock
from types import SimpleNamespace

HERE=Path(__file__).resolve().parent


def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    result=importlib.util.module_from_spec(spec);spec.loader.exec_module(result)
    return result


R=load('_test_actual_native_recorder',HERE/'record_native.py')
T=load('_recorder_actual_plan_fixture',HERE/'test_produce_run_plan.py')
A=R.A


class RecorderTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.root=Path(self.temp.name)

    def test_phase_journals_are_fresh_separate_and_bounded_to_root(self):
        first=R.Journal(self.root,'phase-one');first.put('attempt.raw',b'one');first.close()
        second=R.Journal(self.root,'phase-two');second.put('attempt.raw',b'two');second.close()
        self.assertEqual((self.root/'phase-one/attempt.raw').read_bytes(),b'one')
        with self.assertRaises(A.AnalysisError):R.Journal(self.root,'../escape')
        self.assertEqual((self.root/'phase-two/attempt.raw').read_bytes(),b'two')

    def test_durable_journal_records_immutable_fresh_bytes(self):
        journal=R.Journal(self.root)
        try:
            ref=journal.put('attempt.raw',b'actual fixture stdout','raw_log')
            self.assertEqual(ref['raw_sha256'],A.sha(b'actual fixture stdout'))
            with self.assertRaises(FileExistsError):journal.put('attempt.raw',b'overwrite','raw_log')
        finally:journal.close()
        self.assertEqual((self.root/'recording/attempt.raw').read_bytes(),b'actual fixture stdout')
        with self.assertRaises(FileExistsError):R.Journal(self.root)

    def test_journal_never_writes_outside_owned_directory(self):
        journal=R.Journal(self.root)
        try:
            with self.assertRaises(A.AnalysisError) as caught:journal.put('../outside',b'body')
            self.assertEqual(caught.exception.code,'unsafe_record_name')
        finally:journal.close()
        self.assertFalse((self.root/'outside').exists())

    def test_unmeasured_policy_tokens_are_missing_not_zero(self):
        row=dict(job_id='job',attempt_id='attempt')
        analysis=dict(cost_requirements=[dict(phase='reader_generator',resource='input_tokens',allocation='policy_incremental')])
        source=R.N.artifact('source.json',b'actual fixture measured duration','cost_source')
        costs=R.observed_costs(row,analysis,source,12)
        self.assertEqual(costs[0]['value_decimal'],'12')
        self.assertEqual(costs[0]['allocation'],'whole_run_nonadditive')
        self.assertEqual(len(costs),8)
        self.assertTrue(all(c['basis']=='missing' and c['value_decimal'] is None for c in costs[1:]))
        self.assertEqual(len({c['measurement_id'] for c in costs}),8)

    def test_cancel_kills_and_reaps_child_and_returns_partial_trace(self):
        process=mock.Mock();process.wait.side_effect=[KeyboardInterrupt,-9]
        with mock.patch.object(R.subprocess,'Popen',return_value=process):
            result=R.capture(['native','run'],SimpleNamespace(executable=Path('/explicit/fixture/image')),1)
        self.assertTrue(result['interrupted']);self.assertTrue(result['started'])
        self.assertEqual(result['exit'],-9)
        process.kill.assert_called_once();self.assertEqual(process.wait.call_count,2)
        self.assertLessEqual(result['begin'],result['finish'])

    def test_spawn_failure_never_becomes_started_or_free_observed_tokens(self):
        with mock.patch.object(R.subprocess,'Popen',side_effect=OSError('fixture start failure')):
            result=R.capture(['native','run'],SimpleNamespace(executable=Path('/explicit/fixture/image')),1)
        self.assertFalse(result['started']);self.assertIsNone(result['exit']);self.assertFalse(result['interrupted'])

    def test_bounded_transport_prefix_is_explicitly_truncated(self):
        def spawned(*args,**kwargs):
            kwargs['stdout'].write(b'x'*3000)
            process=mock.Mock();process.wait.return_value=0
            return process
        with mock.patch.object(R.A,'MAX_BYTES',8192),mock.patch.object(R.subprocess,'Popen',side_effect=spawned):
            result=R.capture(['native','run'],SimpleNamespace(executable=Path('/explicit/fixture/image')),1)
        self.assertTrue(result['oversized']);self.assertEqual(result['stdout_size'],3000)
        self.assertEqual(len(result['stdout']),2048)


def prepare(root,native_cli,one=False,retry_contract=None):
    f=T.Fixture(root);sha=A.sha(Path(native_cli).read_bytes())
    if retry_contract is not None:
        f.request.update(protocol='exp02-native-plan-request-v3',production_namespace='produced',task_contract=None,
            verification_timeout_seconds=180,**retry_contract)
    if one:
        f.schedule['queries']=f.schedule['queries'][:1]
        f.request['files']['schedule']=f.write('source_schedule.json',A.canonical(f.schedule),'source')
    with R.N.M.NativeHashes(native_cli,sha,60) as hashes:
        files=R.N.produce(f.request,f.roots,native_cli,sha,hashes.hash)
    for name,raw in files.items():
        path=f.root/name;path.parent.mkdir(parents=True,exist_ok=True);path.write_bytes(raw)
    (f.root/'native-runs').mkdir()
    return f,files


def dispatch(f,files,timeout_ns=60000000000):
    projection=A.parse(files['produced/projection.json']);index=A.parse(files['produced/index.json'])
    value=dict(protocol='exp02-native-execution-dispatch-v1',scope='offline_native_fixture_only',
        plan_raw_sha256=A.sha(files['produced/plan.json']),index_raw_sha256=A.sha(files['produced/index.json']),
        projection_sha256=A.digest(projection),native_cli_sha256=projection['native_cli_sha256'],
        attempts=[dict(job_id=r['job_id'],attempt_id=r['attempt_id']) for r in index['rows']],timeout_ns=timeout_ns)
    raw=A.canonical(value)
    ref=f.write('execution_dispatch.json',raw,'dispatch')
    return projection,ref,A.sha(raw)


def run(f,files,native_cli,timeout_ns=60000000000):
    projection,ref,sha=dispatch(f,files,timeout_ns)
    return R.record(files['produced/plan.json'],files['produced/index.json'],projection,f.plan,f.schedule,
        ref,sha,f.roots,native_cli)


def integration(root,native_cli):
    f,files=prepare(root/'success',native_cli)
    projection,ref,sha=dispatch(f,files)
    # Dispatch, argv and output tampering reject before a journal or policy run.
    for change,code in [('pin','trusted_execution_dispatch_mismatch'),('argv','execution_argv_mismatch')]:
        bad=dict(files);pin=sha
        if change=='pin':pin='0'*64
        else:
            plan=R.B.parse_raw(bad['produced/plan.json']);plan['jobs'][0]['native_argv'][1]='untrusted'
            bad['produced/plan.json']=R.B.compact(plan)
            index=R.B.parse_raw(bad['produced/index.json']);index['plan']=R.N.artifact('produced/plan.json',bad['produced/plan.json'],'plan')
            for row in index['rows']:row['plan_raw_sha256']=A.sha(bad['produced/plan.json'])
            bad['produced/index.json']=R.B.compact(index)
            (f.root/'produced/plan.json').write_bytes(bad['produced/plan.json'])
            changed=A.parse(bad['produced/projection.json'])
            changed.update(run_plan_raw_sha256=A.sha(bad['produced/plan.json']),run_index_raw_sha256=A.sha(bad['produced/index.json']))
            bad['produced/projection.json']=A.canonical(changed)
            p,rr,pin=dispatch(f,bad)
        if change=='pin':p,rr=projection,ref
        try:
            if change=='pin':
                with (mock.patch.object(R.P,'verify_final_inputs',side_effect=AssertionError('must reject dispatch before final review')),
                      mock.patch.object(R.P,'resolve_REF_selection',side_effect=AssertionError('must not launch REF replay'))):
                    R.record(bad['produced/plan.json'],bad['produced/index.json'],p,f.plan,f.schedule,rr,pin,f.roots,native_cli,
                        final_inputs={'untrusted_fixture':True},trusted_owner_sha='0'*64)
            else:R.record(bad['produced/plan.json'],bad['produced/index.json'],p,f.plan,f.schedule,rr,pin,f.roots,native_cli)
        except A.AnalysisError as error:assert error.code==code,(change,error.code)
        else:raise AssertionError(('unexpected dispatch',change))
        assert not (f.root/'recording').exists()
        (f.root/'produced/plan.json').write_bytes(files['produced/plan.json'])
    index,projection,report=run(f,files,native_cli)
    assert report['planned_native_run_processes_started']==6 and report['native_verification_calls']==6 and report['native_replays_verified']==6,report
    assert [r['status'] for r in index['rows']].count('completed_with_failures')==1
    assert all(r['status'] in R.P.COMPLETE for r in index['rows'])
    assert sum(p['failed_rows'] for r in index['rows'] for p in r['policy_rows'])==4
    assert sum(p['planned_rows'] for r in index['rows'] for p in r['policy_rows'])==42
    assert all(r['execution_dispatch']['raw_sha256']!=f.request['planning_dispatch']['raw_sha256'] for r in index['rows'])
    assert len({r['execution_clock_domain'] for r in index['rows']})==1
    assert all(r['started_at_ns']<=r['finished_at_ns'] for r in index['rows'])
    assert all(r['status']=='not_run' for r in R.B.parse_raw(files['produced/index.json'])['rows'])
    result=R.P.project(f.plan,f.schedule,files['produced/plan.json'],R.B.compact(index),projection,f.roots,native_cli)
    observations=result['observations']['rows']
    assert len(observations)==42 and sum(r['status']=='retrieval_failure' for r in observations)==4
    assert all(c['value_decimal'] is None for r in index['rows'] for c in r['cost_measures'] if c['basis']=='missing')
    assert len(result['analysis']['native_projection']['verifications'])==6
    assert report['scientific_approval_verified'] is False and report['product_authority_verified'] is False
    # Bounded actual timeout: the original helper is really dispatched/killed.
    timeout,timeout_files=prepare(root/'timeout',native_cli,one=True)
    timed,projection,report=run(timeout,timeout_files,native_cli,100000000)
    assert report['planned_native_run_processes_started']==1 and timed['rows'][0]['status']=='timeout',(report,timed)
    assert timed['rows'][0]['process_exit'] is not None
    assert (timeout.root/timed['rows'][0]['raw_log']['relative_path']).is_file()
    projected=R.P.project(timeout.plan,timeout.schedule,timeout_files['produced/plan.json'],R.B.compact(timed),
        projection,timeout.roots,native_cli)
    assert len(projected['observations']['rows'])==7 and all(r['status']=='timeout' for r in projected['observations']['rows'])
    # A separately dispatched, correctly raw-pinned but invalid native config
    # is really rejected by Go, and all seven scheduled failures remain.
    bad,bad_files=prepare(root/'input-rejected',native_cli,one=True)
    plan=R.B.parse_raw(bad_files['produced/plan.json']);index=R.B.parse_raw(bad_files['produced/index.json'])
    bindings=plan['jobs'][0]['bindings'];path=bad.root/bindings['native_config_raw']['relative_path']
    cfg=R.P.C.parse(path.read_bytes());cfg['config_sha256']='0'*64;data=R.N.M.compact(cfg);path.write_bytes(data)
    bindings['native_config_raw']=R.N.artifact(bindings['native_config_raw']['relative_path'],data,'config')
    bindings['native_config_semantic']['semantic_sha256']='0'*64
    bad_files['produced/plan.json']=R.B.compact(plan)
    (bad.root/'produced/plan.json').write_bytes(bad_files['produced/plan.json'])
    index['plan']=R.N.artifact('produced/plan.json',bad_files['produced/plan.json'],'plan')
    index['rows'][0].update(plan_raw_sha256=A.sha(bad_files['produced/plan.json']),input_bindings=copy.deepcopy(bindings))
    bad_files['produced/index.json']=R.B.compact(index)
    p=A.parse(bad_files['produced/projection.json'])
    p.update(run_plan_raw_sha256=A.sha(bad_files['produced/plan.json']),run_index_raw_sha256=A.sha(bad_files['produced/index.json']))
    bad_files['produced/projection.json']=A.canonical(p)
    rejected,p,report=run(bad,bad_files,native_cli)
    assert report['planned_native_run_processes_started']==1 and report['native_replays_verified']==0
    assert rejected['rows'][0]['status']=='input_rejected' and rejected['rows'][0]['process_exit']!=0
    projected=R.P.project(bad.plan,bad.schedule,bad_files['produced/plan.json'],R.B.compact(rejected),p,bad.roots,native_cli)
    assert len(projected['observations']['rows'])==7 and all(r['status']=='retrieval_failure' for r in projected['observations']['rows'])
    # Real public CLI records one complete run, with its separate dispatch pin.
    cli,cli_files=prepare(root/'cli',native_cli,one=True)
    _,_,sha=dispatch(cli,cli_files)
    args=[sys.executable,'-B',str(HERE/'record_native.py'),'--native-cli',native_cli,'--trusted-dispatch-sha256',sha]
    for name,path in [('run-plan','produced/plan.json'),('run-index','produced/index.json'),
                      ('projection-plan','produced/projection.json'),('analysis-plan','source_analysis_plan.json'),
                      ('schedule','source_schedule.json'),('dispatch','execution_dispatch.json')]:
        args+=['--'+name,str(cli.root/path)]
    for name,path in cli.roots.items():args+=['--'+name+'-root',path]
    recorded=subprocess.run(args,capture_output=True,text=True,timeout=60)
    assert recorded.returncode==0,(recorded.stdout,recorded.stderr)
    assert json.loads(recorded.stdout)['native_replays_verified']==1
    actual=R.B.parse_raw((cli.root/'recording/index.json').read_bytes())
    assert actual['rows'][0]['status']=='completed'
    rejected=subprocess.run(args,capture_output=True,text=True,timeout=60)
    assert rejected.returncode==2 and json.loads(rejected.stdout)['code']=='execution_output_exists'
    assert (cli.root/'recording/index.json').read_bytes()==R.B.compact(actual)
    print('actual source→typed plan→separate dispatch→recorder6runs/6freshverify→analysis42rows/4failed; actual timeout7rows preserved; original unexecuted index unchanged; no fake token costs/approval')


if __name__=='__main__':
    if len(sys.argv)==4 and sys.argv[1]=='--integration':integration(Path(sys.argv[2]),sys.argv[3])
    else:unittest.main()

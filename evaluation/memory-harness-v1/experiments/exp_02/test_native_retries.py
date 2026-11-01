"""Public producer/recorder/projector retry flow using actual native children."""
import copy
import importlib.util
from pathlib import Path
import os
import sys
import tempfile
import unittest
from unittest import mock

HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('_retry_recorder_fixture',HERE/'test_record_native.py')
T=importlib.util.module_from_spec(spec);spec.loader.exec_module(T)
R,A=T.R,T.A


class RetryArchiveTests(unittest.TestCase):
    def test_atomic_archive_preserves_existing_foreign_destination(self):
        with tempfile.TemporaryDirectory() as root:
            root=Path(root);(root/'source').mkdir();(root/'destination').mkdir()
            (root/'source/child').write_bytes(b'partial original output')
            (root/'destination/foreign').write_bytes(b'foreign destination')
            parent=R.G.IO._directory_fd(root)
            try:
                with self.assertRaises(FileExistsError):R.P.ARCHIVE.move_no_replace(parent,'source',parent,'destination')
            finally:os.close(parent)
            self.assertEqual((root/'source/child').read_bytes(),b'partial original output')
            self.assertEqual((root/'destination/foreign').read_bytes(),b'foreign destination')

    def test_unsupported_archive_has_no_replacing_fallback(self):
        with mock.patch.object(R.P.ARCHIVE,'_rename_function',return_value=None):
            with self.assertRaises(OSError):R.P.ARCHIVE.move_no_replace(-1,'source',-1,'destination')

    def test_partial_output_bytes_survive_archive_without_becoming_complete(self):
        with tempfile.TemporaryDirectory() as root:
            root=Path(root);journal=R.Journal(root,'retry');journal.reserve_work()
            try:
                output=root/'retry/work/job';output.mkdir();(output/'partial.raw').write_bytes(b'partial output')
                row=dict(job_id='job',attempt_id='attempt',status='timeout',native_completion_manifest=None,policy_rows=[])
                target=R.archive_output(dict(job_id='job',output_relative_dir='retry/work/job'),row,
                    R.P.Sources(dict(input=root,evidence=root,output=root)),journal)
                self.assertFalse(output.exists());self.assertEqual(row['status'],'timeout')
                self.assertIsNone(row['native_completion_manifest'])
                self.assertEqual((root/target/'partial.raw').read_bytes(),b'partial output')
            finally:journal.close()

    def test_invalid_predeclared_attempts_reject_before_native_hashing(self):
        for attempts in ([],['same','same'],['../escape'],[True],['a']*17):
            with self.subTest(attempts=attempts),tempfile.TemporaryDirectory() as root:
                f=T.T.Fixture(Path(root))
                f.request.update(protocol='exp02-native-plan-request-v3',production_namespace='produced',
                    task_contract=None,attempt_order=attempts,artifact_namespace='retry')
                program=Path(sys.executable).resolve()
                with self.assertRaises(A.AnalysisError) as caught:
                    R.N.produce(f.request,f.roots,str(program),A.sha(program.read_bytes()),
                        lambda *args:self.fail('invalid attempt schedule invoked hashing'))
                self.assertEqual(caught.exception.code,'invalid_attempt_schedule')


def integration(root,native_cli):
    f,files=T.prepare(root,native_cli,one=True,retry_contract=dict(
        attempt_order=['attempt_1','attempt_2','attempt_3'],artifact_namespace='retry-recording'))
    projection=A.parse(files['produced/projection.json']);index=A.parse(files['produced/index.json'])
    assert len(index['rows'])==3 and len(R.B.parse_raw(files['produced/plan.json'])['jobs'])==1
    dispatch=dict(protocol='exp02-native-execution-dispatch-v3',scope='offline_native_fixture_only',
        plan_raw_sha256=A.sha(files['produced/plan.json']),index_raw_sha256=A.sha(files['produced/index.json']),
        projection_sha256=A.digest(projection),native_cli_sha256=projection['native_cli_sha256'],
        artifact_namespace='retry-recording',timeout_ns=180000000000,
        attempts=[dict(job_id=r['job_id'],attempt_id=r['attempt_id'],timeout_ns=(100000000 if i==0 else 60000000000))
                  for i,r in enumerate(index['rows'])])
    raw=A.canonical(dispatch);ref=f.write('retry-dispatch.json',raw,'dispatch')
    for mutation,code in [('timeout','invalid_execution_timeout'),('namespace','attempt_archive_namespace_mismatch'),
                          ('order','execution_attempt_dispatch_mismatch')]:
        bad=copy.deepcopy(dispatch)
        if mutation=='timeout':bad['attempts'][0]['timeout_ns']=True
        elif mutation=='namespace':bad['artifact_namespace']='different'
        else:bad['attempts'].reverse()
        data=A.canonical(bad);bad_ref=f.write('bad-retry-dispatch.json',data,'dispatch')
        try:
            with mock.patch.object(R.subprocess,'Popen',side_effect=AssertionError('invalid dispatch launched child')):
                R.record(files['produced/plan.json'],files['produced/index.json'],projection,
                    f.plan,f.schedule,bad_ref,A.sha(data),f.roots,native_cli)
        except A.AnalysisError as issue:assert issue.code==code,(mutation,issue.code)
        else:raise AssertionError(('invalid retry dispatch accepted',mutation))
        assert not (f.root/'retry-recording').exists()
    recorded,updated,report=R.record(files['produced/plan.json'],files['produced/index.json'],projection,
        f.plan,f.schedule,ref,A.sha(raw),f.roots,native_cli)
    assert [r['status'] for r in recorded['rows']]==['timeout','completed','completed'],(report,
        [p.read_text() for p in (f.root/'retry-recording').glob('*-error.json')])
    assert report['planned_native_run_processes_started']==3 and report['native_replays_verified']==2,report
    assert all(r['actual_argv']==R.B.parse_raw(files['produced/plan.json'])['jobs'][0]['native_argv'] for r in recorded['rows'])
    assert len({r['native_completion_manifest']['relative_path'] for r in recorded['rows'][1:]})==2
    assert all(r['process_exit'] is not None and r['raw_log'] is not None and r['cost_measures'] for r in recorded['rows'])
    before={p.relative_to(f.root).as_posix():p.read_bytes() for p in (f.root/'retry-recording').rglob('*') if p.is_file()}
    actual=R.P.project(f.plan,f.schedule,files['produced/plan.json'],R.B.compact(recorded),updated,f.roots,native_cli)
    attempts=actual['analysis']['native_projection']['attempts']
    assert [r['selected_for_analysis'] for r in attempts]==[False,True,False],attempts
    assert len(actual['observations']['rows'])==7
    assert all(r['status']=='success' for r in actual['observations']['rows'])
    assert len(actual['analysis']['native_projection']['verifications'])==2
    # The earlier timeout remains in physical costs and failures; no retry
    # adds a logical query/repetition to the primary analysis.
    costs=actual['analysis']['native_execution_costs']
    assert len(recorded['rows'])==3 and any(r['status']=='timeout' for r in attempts)
    assert {c['attempt_id'] for c in costs['records']}=={'attempt_1','attempt_2','attempt_3'}
    wall=[c for c in costs['records'] if c['resource']=='wall_time']
    assert len(wall)==3
    assert [int(c['value_decimal']) for c in wall]==[r['finished_at_ns']-r['started_at_ns'] for r in recorded['rows']]
    assert len(costs['records'])==sum(len(r['cost_measures']) for r in recorded['rows'])
    assert all(c['value_decimal'] is None for c in costs['records'] if c['basis']=='missing')
    assert before=={p.relative_to(f.root).as_posix():p.read_bytes() for p in (f.root/'retry-recording').rglob('*') if p.is_file()}
    changed=copy.deepcopy(recorded)
    changed['rows'][1]['native_completion_manifest']=copy.deepcopy(changed['rows'][2]['native_completion_manifest'])
    bad=dict(updated,run_index_raw_sha256=A.sha(R.B.compact(changed)))
    try:R.P.project(f.plan,f.schedule,files['produced/plan.json'],R.B.compact(changed),bad,f.roots,native_cli)
    except A.AnalysisError as issue:assert issue.code=='native_output_binding_mismatch',issue.code
    else:raise AssertionError('another attempt archive accepted')
    # Reusing the explicit journal cannot alter any earlier attempt bytes.
    try:R.record(files['produced/plan.json'],files['produced/index.json'],projection,
        f.plan,f.schedule,ref,A.sha(raw),f.roots,native_cli)
    except A.AnalysisError as issue:assert issue.code=='execution_output_exists',issue.code
    else:raise AssertionError('existing retry journal accepted')
    assert before=={p.relative_to(f.root).as_posix():p.read_bytes() for p in (f.root/'retry-recording').rglob('*') if p.is_file()}
    print('actual retry recorder: timeout + two native completions, distinct immutable archives, first completion selected, all three attempts/costs retained; no product authority')


if __name__=='__main__':
    if len(sys.argv)==4 and sys.argv[1]=='--integration':integration(Path(sys.argv[2]),sys.argv[3])
    else:unittest.main()

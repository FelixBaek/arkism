"""R8 acceptance tests mock the standalone build/binary executor completely."""
import unittest
import acceptance

class AcceptanceInitialBoundaryTests(unittest.TestCase):
    def test_required_build_command_is_not_a_go_run_substitution(self):
        from pathlib import Path
        plan=acceptance._plan(Path('/repo'),Path('/work'))
        build=next(row for row in plan if row['check_id']=='baseline_build')
        self.assertEqual(build['argv'],['go','build','-o','/work/baseline','./cmd/memory-harness-baseline'])

import contextlib
import copy
import importlib.util
import io as text_io
import json
import os
from pathlib import Path
import subprocess
import tempfile
from unittest import mock

import contract_io as io
import fixture_gen
import metrics
import report

ROOT = Path(__file__).resolve().parents[2]
CONTRACT = ROOT/'evaluation/memory-harness-v1/contracts/implementation_contract.json'


def go_events():
    records=[]
    for name in ('TestCodecGolden','TestCanonicalGolden','TestDecayGolden','TestRRFGolden',
                 'TestPackGolden','TestRankAndPackGolden'):
        records.append({'Action':'pass','Package':'github.com/FelixBaek/arkism/internal/memoryharness','Test':name})
    records.append({'Action':'pass','Package':'github.com/FelixBaek/arkism/cmd/memory-harness-baseline','Test':'TestCLIGolden'})
    return ('\n'.join(json.dumps(row)for row in records)+'\n').encode()


def repository():
    dirty=[]
    return {'reference_commit':acceptance._REFERENCE,'observed_commit':'a'*40,'dirty_files':dirty,
            'dirty_files_sha256':io.sha256_bytes(b'[]')}


def cli_call(module,args):
    out,err=text_io.StringIO(),text_io.StringIO()
    with contextlib.redirect_stdout(out),contextlib.redirect_stderr(err):
        code=module.main(args)
    return subprocess.CompletedProcess([],code,out.getvalue().encode(),err.getvalue().encode())


class AcceptanceMockedPipelineTests(unittest.TestCase):
    """Fake standalone binary/build; real Python fixtures/metrics/report artifacts.

    Every call to the acceptance child executor is intercepted. Generated mock
    baseline artifacts never establish real standalone acceptance completion.
    """
    def setUp(self):
        self.temporary=tempfile.TemporaryDirectory(prefix='arkism-harness-acceptance-tests-')
        self.addCleanup(self.temporary.cleanup)
        self.parent=Path(self.temporary.name);self.work=self.parent/'work'
        self.calls=[];self.alter=None
        self.repo_patch=mock.patch.object(acceptance,'_repository',side_effect=lambda *args:repository())
        self.repo_patch.start();self.addCleanup(self.repo_patch.stop)
        self.executor=mock.patch.object(acceptance,'_execute',side_effect=self.execute)
        self.executor.start();self.addCleanup(self.executor.stop)

    def execute(self,argv,cwd,env):
        self.calls.append((list(argv),Path(cwd),dict(env)))
        if self.alter:
            changed=self.alter(argv)
            if changed is not None:return changed
        if argv[:3]==['go','test','-json']:
            return subprocess.CompletedProcess(argv,0,go_events(),b'')
        if argv[:3]==['go','build','-o']:
            Path(argv[3]).write_bytes(b'mocked standalone artifact; not an executable')
            return subprocess.CompletedProcess(argv,0,b'',b'')
        if '-m' in argv and 'unittest' in argv:
            return subprocess.CompletedProcess(argv,0,b'',b'test_contract (mocked.Test.test_contract) ... ok\n\nRan 1 test in 0.001s\n\nOK\n')
        if str(argv[0])==str(self.work/'baseline'):
            return self.baseline(argv)
        module={'fixture_gen.py':fixture_gen,'metrics.py':metrics,'report.py':report}.get(Path(argv[1]).name)
        if module is None:
            self.fail('unexpected command; never fall through to subprocess')
        return cli_call(module,argv[2:])

    def baseline(self,argv):
        if '-gold' in argv:
            return subprocess.CompletedProcess(argv,2,b'',b'{"code":"invalid_value","path":"$","detail":""}\n')
        try:
            bundle=io.load_input_bundle(Path(argv[argv.index('-manifest')+1]))
            rows=[]
            for probe in bundle['probes']['probes']:
                refs=[]
                # Fixed selected IDs, not generated from gold or a Python ranker.
                for key in acceptance._SELECTIONS[probe['query_id']]:
                    origin,symbol=key.split(':')
                    refs.append({'origin':origin,'collection':'memories','id':symbol,'revision':1,
                                 'content_sha256':io.sha256_bytes(symbol.encode())})
                rows.append({'query_id':probe['query_id'],'stream_id':probe['stream_id'],
                             'scope':dict(probe['scope']),'status':'success','selected_refs':refs,'error_code':'none'})
            value={'contract_id':'arkism-offline-b0-v1','run_id':argv[argv.index('-run-id')+1],
                   'policy_id':'b0-rrf-synthetic-v1','input_manifest_sha256':bundle['manifest_sha256'],
                   'policy_sha256':bundle['policy_sha256'],'predictions':rows}
            output=Path(argv[argv.index('-output')+1]);io.write_json_new(output,value)
            return subprocess.CompletedProcess(argv,0,json.dumps({'status':'success','output':str(output),
                                               'queries':len(rows)}).encode()+b'\n',b'')
        except io.ContractError as error:
            return subprocess.CompletedProcess(argv,2,b'',json.dumps({'code':error.code,'path':error.path,'detail':''}).encode()+b'\n')

    def run_mocked(self):
        return acceptance.run_acceptance(ROOT,self.work)

    def test_mocked_protocol_real_python_artifact_chain_and_original_argv(self):
        result=self.run_mocked();self.assertEqual(result['status'],'checks_passed',result)
        bundle=json.loads((self.work/'baseline_bundle.json').read_bytes())
        self.assertEqual(bundle['review_status'],'requires_independent_review')
        self.assertEqual(set(bundle),set(acceptance._contract(ROOT)['baseline_bundle_contract']['required_fields']))
        self.assertEqual(bundle['repository']['observed_commit'],'a'*40)
        self.assertEqual(len(result['checks']),14)
        self.assertEqual(len(bundle['artifacts']),30)
        build=next(row for row in result['checks']if row['check_id']=='baseline_build')
        binary=next(row for row in result['checks']if row['check_id']=='baseline')
        self.assertEqual(build['argv'],['go','build','-o',str(self.work/'baseline'),'./cmd/memory-harness-baseline'])
        self.assertEqual(binary['argv'][0],str(self.work/'baseline'))
        self.assertNotIn('run',build['argv'])
        for _,_,env in self.calls:
            self.assertEqual((env['GOTOOLCHAIN'],env['GOPROXY'],env['GOSUMDB']),('local','off','off'))
        self.assertFalse(any('acceptance.py' in str(arg)or'test_acceptance.py' in str(arg)or
                             'test_memory_harness.py' in str(arg)or'check_local.py' in str(arg)
                             for argv,_,_ in self.calls for arg in argv))
        for row in bundle['artifacts']:
            path=self.work/row['relative_path'];raw=path.read_bytes()
            self.assertEqual((row['sha256'],row['byte_length']),(io.sha256_bytes(raw),len(raw)))
            self.assertNotEqual(row['relative_path'],'baseline_bundle.json')
        for row in bundle['checks']:
            self.assertEqual(row['status'],'passed')
            self.assertEqual(row['evidence_sha256'],io.sha256_bytes((self.work/row['evidence_path']).read_bytes()))
        self.assertEqual([r['exit_code']for r in bundle['checks'][-4:]],[2]*4)
        summary=json.loads((self.work/'report/summary.json').read_bytes())
        self.assertEqual(summary['summary']['stream_macro_recall_at_k']['value'],1)
        self.assertEqual(summary['summary']['observed_negative_false_positive_rate']['value'],.5)

    def test_zero_python_test_count_stops_and_publishes_diagnosis_only(self):
        self.alter=lambda argv:subprocess.CompletedProcess(argv,0,b'',b'Ran 0 tests in 0.000s\n\nOK\n') if 'unittest'in argv else None
        result=self.run_mocked();self.assertEqual(result['status'],'checks_failed')
        self.assertTrue((self.work/'acceptance.json').exists());self.assertFalse((self.work/'baseline_bundle.json').exists())
        self.assertIsNone(result['baseline_bundle']);self.assertFalse(any(argv[:2]==['go','build']for argv,_,_ in self.calls))
        for row in result['checks'][2:]:
            self.assertEqual((row['status'],row['exit_code'],row['evidence_path'],row['evidence_sha256']),('not_run',None,None,None))

    def test_required_skip_is_not_zero_exit_success(self):
        self.alter=lambda argv:subprocess.CompletedProcess(argv,0,b'',b'test_x (mocked.test_x) ... skipped\nRan 1 test in 0.001s\nOK (skipped=1)\n') if 'unittest'in argv else None
        result=self.run_mocked();self.assertEqual(result['status'],'checks_failed');self.assertFalse((self.work/'baseline_bundle.json').exists())

    def test_go_zero_test_skip_failure_and_malformed_evidence_rejected(self):
        for name,raw in [('zero',b''),('skip',b'{"Action":"skip"}\n'),('fail',b'{"Action":"fail"}\n'),('malformed',b'not-json\n')]:
            with self.subTest(name=name):
                self.work=self.parent/name
                self.alter=lambda argv,raw=raw:subprocess.CompletedProcess(argv,0,raw,b'') if argv[:2]==['go','test']else None
                result=self.run_mocked();self.assertEqual(result['status'],'checks_failed');self.assertFalse((self.work/'baseline_bundle.json').exists())

    def test_nonzero_build_preserves_exit_log_and_no_completed_bundle(self):
        self.alter=lambda argv:subprocess.CompletedProcess(argv,7,b'',b'compiler failed') if argv[:2]==['go','build']else None
        result=self.run_mocked();build=next(row for row in result['checks']if row['check_id']=='baseline_build')
        self.assertEqual((build['exit_code'],build['status']),(7,'failed'))
        self.assertIsNotNone(build['evidence_sha256']);self.assertFalse((self.work/'baseline_bundle.json').exists())

    def test_unexpected_executor_error_sanitized_and_not_run(self):
        def fault(argv):
            raise OSError('secret')
        self.alter=fault;result=self.run_mocked()
        self.assertEqual(result['status'],'checks_failed');self.assertIn('io_error',result['errors'])
        self.assertNotIn('secret',(self.work/'acceptance.json').read_text())
        self.assertEqual(result['checks'][0]['exit_code'],None)

    def test_missing_final_report_artifact_suppresses_bundle(self):
        def alter(argv):
            if len(argv)>1 and Path(argv[1]).name=='report.py':
                result=cli_call(report,argv[2:]);(self.work/'report/queries.csv').unlink();return result
        self.alter=alter;result=self.run_mocked()
        self.assertEqual(result['status'],'checks_failed');self.assertFalse((self.work/'baseline_bundle.json').exists())
        self.assertTrue((self.work/'acceptance.json').exists())

    def test_wrong_report_hash_all_artifacts_present_gets_failed_bundle(self):
        def alter(argv):
            if len(argv)>1 and Path(argv[1]).name=='report.py':
                result=cli_call(report,argv[2:]);path=self.work/'report/summary.json'
                value=json.loads(path.read_bytes());value['metrics_sha256']='f'*64;path.write_text(json.dumps(value));return result
        self.alter=alter;result=self.run_mocked();self.assertEqual(result['status'],'checks_failed')
        bundle=json.loads((self.work/'baseline_bundle.json').read_bytes());self.assertEqual(bundle['status'],'checks_failed')
        self.assertTrue(all(row['sha256'] is not None for row in bundle['artifacts']))

    def test_wrong_ref_cannot_be_hidden_by_recomputed_metrics_and_report(self):
        def alter(argv):
            if len(argv)>1 and Path(argv[1]).name=='metrics.py' and 'negative' not in str(argv):
                path=self.work/'predictions.json';value=json.loads(path.read_bytes())
                value['predictions'][0]['selected_refs'][0]['revision']=2;path.write_text(json.dumps(value))
                return cli_call(metrics,argv[2:])
        self.alter=alter;result=self.run_mocked();self.assertEqual(result['status'],'checks_failed')
        self.assertEqual(json.loads((self.work/'baseline_bundle.json').read_bytes())['status'],'checks_failed')

    def test_negative_command_return_zero_is_a_failed_check(self):
        self.alter=lambda argv:subprocess.CompletedProcess(argv,0,b'{}',b'') if '-gold'in argv else None
        result=self.run_mocked();self.assertEqual(result['status'],'checks_failed')
        row=next(row for row in result['checks']if row['check_id']=='reject_gold_flag')
        self.assertEqual((row['exit_code'],row['status']),(0,'failed'))

    def test_outside_dirty_change_or_commit_change_fails_without_reading_it(self):
        before=repository();after=copy.deepcopy(before)
        after['dirty_files']=[{'path':'protected/secret','status':'??','sha256':None}]
        with mock.patch.object(acceptance,'_repository',side_effect=[before,after]):
            result=self.run_mocked()
        self.assertEqual(result['status'],'checks_failed')
        self.assertEqual(json.loads((self.work/'baseline_bundle.json').read_bytes())['repository']['dirty_files'][0]['sha256'],None)

    def test_existing_workdir_not_overwritten(self):
        self.work.mkdir();sentinel=self.work/'keep';sentinel.write_text('sentinel')
        with self.assertRaises(io.ContractError)as caught:self.run_mocked()
        self.assertEqual(caught.exception.code,'output_exists');self.assertEqual(sentinel.read_text(),'sentinel');self.assertEqual(self.calls,[])

    def test_input_contract_pin_failure_precedes_any_command(self):
        with mock.patch.object(acceptance,'_contract',side_effect=io.ContractError('hash_mismatch','$','')):
            with self.assertRaises(io.ContractError)as caught:self.run_mocked()
        self.assertEqual(caught.exception.code,'hash_mismatch');self.assertEqual(self.calls,[]);self.assertFalse(self.work.exists())


    def test_removed_or_changed_check_log_cannot_complete_bundle(self):
        def alter(argv):
            if len(argv)>1 and Path(argv[1]).name=='report.py':
                result=cli_call(report,argv[2:]);(self.work/'logs/go_tests.log').write_bytes(b'changed')
                return result
        self.alter=alter;result=self.run_mocked()
        self.assertEqual(result['status'],'checks_failed');self.assertFalse((self.work/'baseline_bundle.json').exists())
        self.assertIn('hash_mismatch',result['errors'])

    def test_observed_commit_change_fails(self):
        before=repository();after=copy.deepcopy(before);after['observed_commit']='b'*40
        with mock.patch.object(acceptance,'_repository',side_effect=[before,after]):
            result=self.run_mocked()
        self.assertEqual(result['status'],'checks_failed')
        self.assertEqual(json.loads((self.work/'baseline_bundle.json').read_bytes())['repository']['observed_commit'],'b'*40)


    def test_negative_broken_symlink_is_still_forbidden_publication(self):
        def alter(argv):
            if '-gold'in argv:
                output=Path(argv[argv.index('-output')+1]);output.symlink_to(self.work/'absent')
                return subprocess.CompletedProcess(argv,2,b'',b'{"code":"invalid_value","path":"$","detail":""}\n')
        self.alter=alter;result=self.run_mocked()
        self.assertEqual(result['status'],'checks_failed')
        self.assertEqual(next(row for row in result['checks']if row['check_id']=='reject_gold_flag')['status'],'failed')


    def test_changed_owned_source_hash_during_checks_is_not_accepted(self):
        before=repository();after=copy.deepcopy(before);after['dirty_files_sha256']='c'*64
        with mock.patch.object(acceptance,'_repository',side_effect=[before,after]):
            result=self.run_mocked()
        self.assertEqual(result['status'],'checks_failed')


    def test_final_bundle_write_failure_retracts_success_diagnosis(self):
        original_link=os.link
        def fault(source,destination,*args,**kwargs):
            if destination=='baseline_bundle.json':raise OSError(5,'synthetic write fault')
            return original_link(source,destination,*args,**kwargs)
        with mock.patch.object(os,'link',side_effect=fault):
            try:result=self.run_mocked()
            except io.ContractError:result=None
        diagnosis=json.loads((self.work/'acceptance.json').read_bytes())
        self.assertEqual(diagnosis['status'],'checks_failed')
        self.assertIsNone(diagnosis['baseline_bundle']);self.assertIn('io_error',diagnosis['errors'])
        self.assertFalse((self.work/'baseline_bundle.json').exists())
        self.assertEqual(result['status'],'checks_failed')
        self.assertFalse(any(p.name.startswith('.arkism')for p in self.work.iterdir()))

    def test_bundle_construction_failure_preserves_original_error(self):
        with mock.patch.object(acceptance,'_bundle',side_effect=io.ContractError('hash_mismatch','$','')):
            try:result=self.run_mocked()
            except io.ContractError:result=None
        diagnosis=json.loads((self.work/'acceptance.json').read_bytes())
        self.assertEqual(diagnosis['status'],'checks_failed');self.assertEqual(diagnosis['errors'],['hash_mismatch'])
        self.assertIsNone(diagnosis['baseline_bundle']);self.assertFalse((self.work/'baseline_bundle.json').exists())
        self.assertEqual(result['status'],'checks_failed')

    def test_failed_diagnosis_republication_leaves_no_owned_success_record(self):
        original_link=os.link;acceptance_links=0
        def fault(source,destination,*args,**kwargs):
            nonlocal acceptance_links
            if destination=='acceptance.json':
                acceptance_links+=1
                if acceptance_links>1:raise OSError(5,'diagnosis write fault')
            if destination=='baseline_bundle.json':raise OSError(5,'bundle write fault')
            return original_link(source,destination,*args,**kwargs)
        with mock.patch.object(os,'link',side_effect=fault):
            with self.assertRaises(io.ContractError)as caught:self.run_mocked()
        self.assertEqual(caught.exception.code,'io_error')
        self.assertIn('original error: io_error',caught.exception.detail)
        self.assertFalse((self.work/'acceptance.json').exists());self.assertFalse((self.work/'baseline_bundle.json').exists())
        self.assertFalse(any(p.name.startswith('.arkism')for p in self.work.iterdir()))

    def test_foreign_acceptance_replacement_is_preserved(self):
        target=self.parent/'foreign';target.write_text('foreign sentinel')
        def fault(*args):
            path=self.work/'acceptance.json';path.unlink();path.symlink_to(target)
            raise io.ContractError('hash_mismatch','$','')
        with mock.patch.object(acceptance,'_bundle',side_effect=fault):
            with self.assertRaises(io.ContractError)as caught:self.run_mocked()
        self.assertEqual(caught.exception.code,'io_error')
        self.assertIn('competing output preserved',caught.exception.detail)
        self.assertTrue((self.work/'acceptance.json').is_symlink());self.assertEqual(target.read_text(),'foreign sentinel')
        self.assertFalse((self.work/'baseline_bundle.json').exists())

    def test_foreign_bundle_collision_preserved_with_failed_diagnosis(self):
        original_link=os.link
        def fault(source,destination,*args,**kwargs):
            if destination=='baseline_bundle.json':
                (self.work/'baseline_bundle.json').write_bytes(b'foreign sentinel')
                raise FileExistsError(17,'foreign reservation')
            return original_link(source,destination,*args,**kwargs)
        with mock.patch.object(os,'link',side_effect=fault):
            try:result=self.run_mocked()
            except io.ContractError:result=None
        self.assertEqual((self.work/'baseline_bundle.json').read_bytes(),b'foreign sentinel')
        diagnosis=json.loads((self.work/'acceptance.json').read_bytes())
        self.assertEqual(diagnosis['status'],'checks_failed');self.assertIsNone(diagnosis['baseline_bundle'])
        self.assertIn('output_exists',diagnosis['errors']);self.assertEqual(result['status'],'checks_failed')


    def test_post_link_temporary_cleanup_fault_retracts_success_record(self):
        original_unlink=os.unlink;failed=False
        def fault(name,*args,**kwargs):
            nonlocal failed
            if str(name).startswith('.arkism-acceptance-')and not failed:
                failed=True;raise OSError(5,'temporary cleanup fault')
            return original_unlink(name,*args,**kwargs)
        with mock.patch.object(os,'unlink',side_effect=fault):result=self.run_mocked()
        self.assertEqual(result['status'],'checks_failed');self.assertIn('io_error',result['errors'])
        diagnosis=json.loads((self.work/'acceptance.json').read_bytes())
        self.assertEqual(diagnosis['status'],'checks_failed');self.assertIsNone(diagnosis['baseline_bundle'])
        self.assertFalse((self.work/'baseline_bundle.json').exists())
        self.assertFalse(any(p.name.startswith('.arkism')for p in self.work.iterdir()))


    def publication_fstat_fault(self, failures):
        original_open,original_fstat=os.open,os.fstat
        opened=[];remaining=failures
        def track(name,*args,**kwargs):
            descriptor=original_open(name,*args,**kwargs)
            if str(name).startswith('.arkism-acceptance-'):opened.append(descriptor)
            return descriptor
        def fault(descriptor):
            nonlocal remaining
            if descriptor in opened and (remaining is None or remaining>0):
                if remaining is not None:remaining-=1
                raise OSError(5,'metadata fault')
            return original_fstat(descriptor)
        outcome=error=None
        with mock.patch.object(os,'open',side_effect=track),mock.patch.object(os,'fstat',side_effect=fault):
            try:outcome=self.run_mocked()
            except io.ContractError as caught:error=caught
        leaked=[]
        for descriptor in set(opened):
            try:original_fstat(descriptor)
            except OSError:continue
            leaked.append(descriptor);os.close(descriptor) # Audit cleanup for the pre-fix RED only.
        return outcome,error,leaked

    def test_first_fstat_failure_recovers_identity_closes_fd_and_cleans_temp(self):
        result,error,leaked=self.publication_fstat_fault(1)
        self.assertEqual(leaked,[]);self.assertIsNone(error)
        self.assertEqual(result['status'],'checks_failed');self.assertIn('io_error',result['errors'])
        self.assertFalse((self.work/'baseline_bundle.json').exists())
        self.assertFalse(any(p.name.startswith('.arkism')for p in self.work.iterdir()))
        self.assertEqual(json.loads((self.work/'acceptance.json').read_bytes())['status'],'checks_failed')

    def test_unrecoverable_initial_identity_reports_retained_unknown_temp(self):
        result,error,leaked=self.publication_fstat_fault(2)
        self.assertEqual(leaked,[]);self.assertIsNone(result)
        self.assertEqual(error.code,'io_error');self.assertIn('inode identity unavailable',error.detail)
        self.assertFalse((self.work/'baseline_bundle.json').exists())
        self.assertEqual(len([p for p in self.work.iterdir()if p.name.startswith('.arkism')]),1)
        diagnosis=json.loads((self.work/'acceptance.json').read_bytes())
        self.assertEqual(diagnosis['status'],'checks_failed');self.assertIsNone(diagnosis['baseline_bundle'])
        self.assertIn('owned temporary cleanup incomplete: inode identity unavailable',diagnosis['limitations'])

    def test_persistent_fstat_failures_close_every_fd_and_report_cleanup_limit(self):
        result,error,leaked=self.publication_fstat_fault(None)
        self.assertEqual(leaked,[]);self.assertIsNone(result)
        self.assertEqual(error.code,'io_error');self.assertIn('inode identity unavailable',error.detail)
        self.assertFalse((self.work/'baseline_bundle.json').exists());self.assertFalse((self.work/'acceptance.json').exists())
        self.assertGreater(len([p for p in self.work.iterdir()if p.name.startswith('.arkism')]),0)


class AcceptanceRepositoryAndParserTests(unittest.TestCase):
    def test_repository_hashes_only_exact_allowlist_regular_files(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-repository-')as directory:
            root=Path(directory);(root/'allowed').write_bytes(b'owned')
            (root/'external').symlink_to('/does-not-exist')
            contract={'file_ownership':{'R1':['allowed','deleted','renamed']}}
            status=b' M allowed\0?? external\0 D deleted\0R  renamed\0old-name\0'
            read=io._read_regular
            opened=[]
            def guarded(path):
                opened.append(Path(path).name)
                if Path(path).name in ('external','old-name'):self.fail('outside contents read')
                return read(path)
            (root/'renamed').write_bytes(b'renamed')
            with mock.patch.object(acceptance,'_git',side_effect=[b'a'*40+b'\n',status]),mock.patch.object(io,'_read_regular',side_effect=guarded):
                result=acceptance._repository(root,contract)
            self.assertEqual(set(opened),{'allowed','renamed'})
            self.assertEqual([r['path']for r in result['dirty_files']],['allowed','deleted','external','renamed'])
            self.assertIsNone(result['dirty_files'][1]['sha256']);self.assertIsNone(result['dirty_files'][2]['sha256'])
            raw=json.dumps(result['dirty_files'],sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()
            self.assertEqual(result['dirty_files_sha256'],io.sha256_bytes(raw))

    def test_cli_help_usage_and_duplicate_never_invoke_acceptance(self):
        cases=[([],2),(['--repo-root','secret'],2),(['--unknown','secret'],2),
               (['--repo-root','a','--work-dir','b','--work-dir','c'],2),
               (['--repo-root','a','--work-dir','b','positional'],2),(['--help'],0)]
        with mock.patch.object(acceptance,'run_acceptance',side_effect=AssertionError('default execution forbidden'))as run:
            for args,want in cases:
                with self.subTest(args=args):
                    result=cli_call(acceptance,args);self.assertEqual(result.returncode,want)
                    if want==2:self.assertNotIn(b'secret',result.stderr);json.loads(result.stderr)
            run.assert_not_called()

    def test_cli_synthetic_failed_result_is_not_exit_zero(self):
        fake={'status':'checks_failed','work_dir':'/work','checks':[],'errors':['invalid_value']}
        with mock.patch.object(acceptance,'run_acceptance',return_value=fake):
            result=cli_call(acceptance,['--repo-root','/repo','--work-dir','/work'])
        self.assertEqual(result.returncode,2);self.assertEqual(json.loads(result.stderr)['code'],'invalid_value')

    def test_bundle_missing_artifact_never_fills_hash_null_or_zero(self):
        with tempfile.TemporaryDirectory(prefix='arkism-harness-bundle-')as directory:
            with self.assertRaises(io.ContractError):acceptance._bundle(Path(directory),repository(),[],'checks_failed')

    def test_go_missing_required_group_not_accepted(self):
        lines=[line for line in go_events().splitlines()if b'TestDecay'not in line]
        with self.assertRaises(io.ContractError):acceptance._go_counts(b'\n'.join(lines))

    def test_go_nested_count_is_distinct_from_leaf_count(self):
        extra={'Action':'pass','Package':'github.com/FelixBaek/arkism/internal/memoryharness','Test':'TestDecayGolden/subcase'}
        value=acceptance._go_counts(go_events()+json.dumps(extra).encode()+b'\n')
        self.assertEqual((value['nodes'],value['leaf']),(8,7))


class CollectorBoundaryTests(unittest.TestCase):
    def collector(self):
        spec=importlib.util.spec_from_file_location('arkism_collector_unit',ROOT/'evaluation/test_memory_harness.py')
        module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module

    def test_module_list_is_fixed_no_build_or_recursion(self):
        module=self.collector()
        self.assertEqual(module._MODULES,('test_contract_io.py','test_fixture_gen.py','test_metrics.py','test_report.py','test_acceptance.py'))

    def test_missing_or_zero_module_is_collection_failure(self):
        module=self.collector()
        with tempfile.TemporaryDirectory(prefix='arkism-harness-collector-')as directory:
            module._DIRECTORY=Path(directory);module._MODULES=('test_empty.py',)
            with self.assertRaises(AssertionError):module.load_tests(unittest.TestLoader(),None,None)
            (Path(directory)/'test_empty.py').write_text('import unittest\n')
            with self.assertRaises(AssertionError):module.load_tests(unittest.TestLoader(),None,None)

    def test_required_runtime_skip_becomes_failure(self):
        module=self.collector()
        class Skips(unittest.TestCase):
            def runTest(self):self.skipTest('required')
        result=unittest.TestResult();module._RequiredSuite([Skips()]).run(result)
        self.assertEqual((len(result.failures),len(result.skipped)),(1,0));self.assertFalse(result.wasSuccessful())

    def test_collection_import_failure_is_not_skipped(self):
        module=self.collector()
        with tempfile.TemporaryDirectory(prefix='arkism-harness-collector-')as directory:
            module._DIRECTORY=Path(directory);module._MODULES=('test_broken.py',)
            (Path(directory)/'test_broken.py').write_text('raise RuntimeError("broken")\n')
            with self.assertRaises(RuntimeError):module.load_tests(unittest.TestLoader(),None,None)

    def test_required_module_import_skip_is_failure(self):
        module=self.collector()
        with tempfile.TemporaryDirectory(prefix='arkism-harness-collector-')as directory:
            module._DIRECTORY=Path(directory);module._MODULES=('test_skipped.py',)
            (Path(directory)/'test_skipped.py').write_text('import unittest\nraise unittest.SkipTest("required")\n')
            with self.assertRaises(AssertionError):module.load_tests(unittest.TestLoader(),None,None)

    def test_expected_failure_is_not_a_required_pass(self):
        module=self.collector()
        class Expected(unittest.TestCase):
            @unittest.expectedFailure
            def runTest(self):self.fail('expected')
        result=unittest.TestResult();module._RequiredSuite([Expected()]).run(result)
        self.assertEqual(len(result.failures),1);self.assertEqual(len(result.expectedFailures),0)


    def test_import_skip_via_outer_loader_cannot_be_success(self):
        module=self.collector()
        with tempfile.TemporaryDirectory(prefix='arkism-harness-collector-')as directory:
            module._DIRECTORY=Path(directory);module._MODULES=('test_skipped.py',)
            (Path(directory)/'test_skipped.py').write_text('import unittest\nraise unittest.SkipTest("required")\n')
            result=unittest.TestResult();unittest.TestLoader().loadTestsFromModule(module).run(result)
            self.assertFalse(result.wasSuccessful());self.assertEqual(len(result.skipped),0)
            self.assertEqual(len(result.errors)+len(result.failures),1)

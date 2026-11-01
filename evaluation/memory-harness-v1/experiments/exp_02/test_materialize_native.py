"""Original source generator, explicit fixture admissions and real native tests."""
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


M=load('_source_materializer_under_test',HERE/'materialize_native.py')
T=load('_materializer_original_source_fixture',HERE.with_name('exp_01')/'test_protocol.py')
ROOT=HERE.parents[3]


class Fixture:
    def __init__(self,live_outcome=False):
        spec=T.fixture_spec()
        if live_outcome:
            for family in spec['template_families']:
                events=family['variants'][0]['events']
                outcome=events.pop(5)
                for name in ('available_anchor','source_anchor','arrived_anchor'):outcome[name]='b'
                events.insert(3,outcome)
        data,(_,queries,_,_)=T.G.expand_spec(T.raw_spec(spec))
        self.events_raw=data['events.jsonl']
        self.events=M.source(M.V._rows,self.events_raw,'PlanEvent')
        self.queries=queries
        stream=queries[0]['stream_id']
        self.stream=stream
        self.query=next(q for q in queries if q['stream_id']==stream and q['query_text'].endswith('request current'))
        self.catalog=dict(protocol='exp02-synthetic-admission-v1',scope='offline_fixture_only_not_product_authority',
                          events_raw_sha256=M.sha(self.events_raw),admissions=[],outcomes=[])
        for e in self.events:
            if e['kind']!='material_available':continue
            activation=e['event_id']
            if e['target']['revision']>1:
                activation=next(c['event_id'] for c in self.events if c['stream_id']==e['stream_id'] and
                    c['kind']=='correction_committed' and e['target'] in c['related_refs'])
            self.catalog['admissions'].append(dict(ref=e['target'],scope=e['scope'],source_event_id=e['event_id'],
                activation_event_id=activation,admission='explicit_synthetic_fixture',trust='0.5',context_cost_units=1,
                effective_at_ns=e['source_payload']['created_at_ns'],effective_basis='explicit_synthetic_fixture',
                historical_body_available=True,importance='0.5',importance_rule_sha256=M.sha(b'explicit fixture importance')))
        self.registry=(ROOT/'docs/research/memory-harness/policy_registry.json').read_bytes()
        self.plan=dict(protocol='exp02-native-materialization-plan-v1',dataset_id='synthetic_materialization',run_id='synthetic_run',
            baseline_bundle_sha256=M.sha(b'synthetic baseline identity fixture not actual acceptance'),final_k=2,
            context_budget=dict(unit='fixture_units_v0',limit=2),lane_rule='character_bigram_overlap_then_full_ref')

    def view(self):
        return M.prepare_source_view(self.events_raw,T.G.compact(self.query),M.compact(self.catalog),self.registry,M.compact(self.plan))

    def qualify_outcome(self):
        event=next(e for e in self.events if e['stream_id']==self.stream and e['kind']=='outcome_observed')
        registry=json.loads(self.registry);rule=next(e['dynamics']['evidence_rule_sha256'] for e in registry['entries'] if e['policy_id']=='B2')
        def ref(collection,name,body):return dict(namespace='fixture',collection=collection,id=name,revision=1,content_sha256=M.sha(body))
        taskref=ref('eval_tasks',event['task_id'],b'explicit independent fixture task definition')
        task=dict(task_ref=taskref,run_ref=ref('eval_runs','fixture_run',b'explicit fixture run'),
            trial_ref=ref('eval_trials','fixture_trial',b'explicit fixture trial'),checker_sha256=event['outcome']['source_rule_sha256'],
            rule_sha256=rule,qualification='synthetic_independent',task_sha256=taskref['content_sha256'])
        evidence=dict(refs=[ref('evaluations',event['outcome']['evidence_id'],b'explicit fixture outcome evidence')],
            target=event['target'],task_sha256=task['task_sha256'],dimension='work_task',rule_sha256=rule,
            independence='synthetic_independent',source_status='available')
        self.catalog['outcomes'].append(dict(source_event_id=event['event_id'],task=task,evidence_template=evidence,
            attribution='memory',strength='1'))

    def current(self,name):
        self.query=next(q for q in self.queries if q['stream_id']==self.stream and q['query_text'].endswith('request '+name))


class MaterializerTests(unittest.TestCase):
    def setUp(self):self.f=Fixture()

    def test_current_view_has_explicit_admission_and_no_native_source_text(self):
        view=self.f.view();snapshot=view['snapshot']
        self.assertEqual(len(snapshot['lanes'][0]),1)
        self.assertEqual(snapshot['lanes'][0][0]['ref']['revision'],2)
        self.assertTrue(snapshot['lanes'][0][0]['active'])
        self.assertEqual(len(view['registry']['entries']),7)
        for value in (view['events'],view['snapshot'],view['features']):
            self.assertNotIn(self.f.query['query_text'].encode(),M.compact(value))

    def test_no_admission_never_automatically_activates_source(self):
        self.f.catalog['admissions']=[]
        view=self.f.view()
        self.assertEqual(view['events'],[])
        self.assertEqual(view['snapshot']['lanes'],[[],[],[]])

    def test_pending_correction_keeps_old_revision(self):
        self.f.current('pending')
        row=self.f.view()['snapshot']['lanes'][0][0]
        self.assertEqual(row['ref']['revision'],1)
        self.assertTrue(row['active'])

    def test_historical_observed_view_uses_original_old_body_identity(self):
        self.f.current('history')
        view=self.f.view();snapshot=view['snapshot']
        self.assertEqual(snapshot['as_of_ns'],self.f.query['requested_as_of_ns'])
        self.assertEqual(snapshot['lanes'][0][0]['ref']['revision'],1)
        self.assertTrue(snapshot['lanes'][0][0]['active'])
        self.assertEqual(view['view_at'],self.f.query['requested_as_of_ns'])

    def test_current_withdrawal_still_fences_historical_source(self):
        self.f.current('history');self.f.query=copy.deepcopy(self.f.query)
        last=next(e for e in self.f.events if e['stream_id']==self.f.stream and e['kind']=='scope_changed')
        self.f.query.update(at_ns=last['available_at_ns'],cutoff_plan_seq=last['plan_seq'])
        row=self.f.view()['snapshot']['lanes'][0][0]
        self.assertTrue(row['deleted']);self.assertFalse(row['active'])

    def test_current_new_revision_withdrawal_fences_all_past_revisions(self):
        self.f.current('history');self.f.query=copy.deepcopy(self.f.query)
        replacement=next(e['target'] for e in self.f.events if e['stream_id']==self.f.stream and e['kind']=='material_available' and e['target']['revision']==2)
        last=next(e for e in self.f.events if e['stream_id']==self.f.stream and e['kind']=='scope_changed')
        last['target']=copy.deepcopy(replacement)
        self.f.events_raw=b''.join(T.G.compact(e)+b'\n' for e in self.f.events)
        self.f.catalog['events_raw_sha256']=M.sha(self.f.events_raw)
        self.f.query.update(at_ns=last['available_at_ns'],cutoff_plan_seq=last['plan_seq'])
        row=self.f.view()['snapshot']['lanes'][0][0]
        self.assertEqual(row['ref']['revision'],1)
        self.assertTrue(row['deleted']);self.assertFalse(row['active'])

    def test_historical_missing_body_does_not_substitute_new_revision(self):
        self.f.current('history')
        for r in self.f.catalog['admissions']:
            if r['scope']==self.f.query['scope']:r['historical_body_available']=False
        self.assertEqual(self.f.view()['snapshot']['lanes'],[[],[],[]])

    def test_future_source_changes_leave_native_view_unchanged(self):
        self.f.current('pending');before=self.f.view()
        future=copy.deepcopy(next(e for e in self.f.events if e['stream_id']==self.f.stream and e['kind']=='material_available'))
        future.update(event_id='future_fixture_source',plan_seq=999,available_at_ns=999,source_at_ns=999,arrived_at_ns=999)
        future['source_payload']['text']='unseen future body'
        future['target']['content_sha256']=M.sha((future['source_payload']['text']+'\n'+future['source_payload']['summary']).encode())
        self.f.events_raw+=T.G.compact(future)+b'\n';self.f.catalog['events_raw_sha256']=M.sha(self.f.events_raw)
        after=self.f.view()
        for name in ('events','features','snapshot','snapshot_sha','mapping'):self.assertEqual(before[name],after[name],name)

    def test_declared_outcome_never_auto_qualifies(self):
        self.f.current('after')
        event=next(e for e in self.f.view()['events'] if e['kind']=='outcome')
        self.assertEqual(event['outcome'],'passed')
        self.assertIsNone(event['task']);self.assertIsNone(event['evidence']);self.assertIsNone(event['strength'])
        self.assertEqual(event['attribution'],'unattributable')

    def test_qualification_requires_separate_explicit_fixture_record(self):
        self.f.current('after');self.f.qualify_outcome()
        event=next(e for e in self.f.view()['events'] if e['kind']=='outcome')
        self.assertEqual(event['task']['qualification'],'synthetic_independent')
        self.assertEqual(event['key']['target']['revision'],1)
        self.assertEqual(event['strength'],1)
        self.assertEqual(event['evidence']['target'],event['key']['target'])
        self.f.catalog['outcomes'][0]['task']['qualification']='unknown'
        with self.assertRaises(M.MaterializationError) as caught:self.f.view()
        self.assertEqual(caught.exception.code,'qualification_scope_mismatch')

    def test_wrong_admission_body_or_visibility_rejects(self):
        admission=next(r for r in self.f.catalog['admissions'] if r['scope']==self.f.query['scope'])
        admission['source_event_id']='unknown_source'
        with self.assertRaises(M.MaterializationError) as caught:self.f.view()
        self.assertEqual(caught.exception.code,'admission_source_not_visible')

    def test_unobserved_future_cutoff_is_rejected(self):
        self.f.query=copy.deepcopy(self.f.query);self.f.query['cutoff_plan_seq']=999
        with self.assertRaises(M.MaterializationError) as caught:self.f.view()
        self.assertEqual(caught.exception.code,'source_schedule_mismatch')

    def test_recorded_source_order_is_not_silently_sorted(self):
        rows=[e for e in self.f.events if e['stream_id']==self.f.stream]
        other=[e for e in self.f.events if e['stream_id']!=self.f.stream]
        rows[0],rows[1]=rows[1],rows[0]
        self.f.events_raw=b''.join(T.G.compact(e)+b'\n' for e in rows+other)
        self.f.catalog['events_raw_sha256']=M.sha(self.f.events_raw)
        with self.assertRaises(M.MaterializationError) as caught:self.f.view()
        self.assertEqual(caught.exception.code,'source_causal_order')

    def test_cli_forbids_gold_and_does_not_echo_sensitive_arguments(self):
        secret='/private/fixture-sensitive-gold.jsonl'
        args=[sys.executable,'-B',str(HERE/'materialize_native.py')]
        for name in ('events','query','catalog','registry','plan','native-cli','native-cli-sha256','out'):args+=['--'+name,'/unused']
        args+=['--gold',secret]
        proc=subprocess.run(args,capture_output=True,text=True,timeout=30)
        self.assertEqual(proc.returncode,2)
        self.assertEqual(json.loads(proc.stdout)['code'],'invalid_arguments')
        self.assertNotIn(secret,proc.stdout+proc.stderr)
        self.assertEqual(proc.stderr,'')


def integration(root,native_cli):
    f=Fixture();program_sha=M.sha(Path(native_cli).read_bytes())
    def source_raw():
        return dict(events=f.events_raw,query=T.G.compact(f.query),catalog=M.compact(f.catalog),registry=f.registry,plan=M.compact(f.plan))
    def execute(name,files,expected_revision,expected_errors=None):
        target=root/name;target.mkdir()
        for filename,raw in files.items():(target/filename).write_bytes(raw)
        args=[native_cli,'run','--p1-manifest',str(target/'p1.json'),'--config',str(target/'config.json'),
              '--registry',str(target/'registry.json'),'--out',str(target/'output')]
        proc=subprocess.run(args,capture_output=True,text=True,timeout=30)
        assert proc.returncode==0,(name,proc.stdout,proc.stderr)
        status=json.loads(proc.stdout)
        expected_errors={} if expected_errors is None else expected_errors
        assert status['status']==('completed_with_failures' if expected_errors else 'completed'),(name,status)
        rows=[]
        for policy in M.B.ENUMS['RequiredPolicyID']:
            run=json.loads((target/'output'/'predictions'/(policy+'.json')).read_bytes())
            assert len(run['predictions'])==1
            row=run['predictions'][0]
            if policy in expected_errors:
                assert row['status']=='failed' and row['error']==expected_errors[policy],(name,policy,row)
                assert row['decision'] is None,(name,policy,row)
            else:
                assert row['status']=='ok',(name,policy,row)
                refs=row['decision']['selected']
                assert [r['revision'] for r in refs]==([] if expected_revision is None else [expected_revision]),(name,policy,refs)
            rows.append(row)
        # Actual original native verification, not just an accepted self-seal.
        args[1]='verify'
        checked=subprocess.run(args,capture_output=True,text=True,timeout=30)
        assert checked.returncode==0,(name,checked.stdout,checked.stderr)
        assert json.loads(checked.stdout)['status']=='replay_matched'
        return rows
    with M.NativeHashes(native_cli,program_sha,60) as hashes:
        for name,revision in [('pending',1),('current',2),('history',1)]:
            f.current(name);raw=source_raw()
            files=M.materialize(*(raw[k] for k in ('events','query','catalog','registry','plan')),hashes.hash,hashes.raw_sha256)
            execute(name,files,revision)
            report=json.loads(files['materialization-report.json'])
            assert report['product_authority_verified'] is False
            if name=='current':assert report['native_cutoff_accepted_seq']!=report['source_cutoff_plan_seq']
            if name=='history':assert report['native_at_ns']==f.query['requested_as_of_ns']<f.query['at_ns']
        # A late outcome on an invalidated revision must remain a failed native
        # prediction in the denominator, never disappear or become a success.
        stale_errors={p:dict(code='invalidated_target',path='$.lifecycle_fence.entries')
                      for p in ('B2','V3-P1-FD','V3-P1-NOTIME','V3-P1-NOOUT')}
        f.current('after');raw=source_raw()
        execute('after_unqualified_outcome',M.materialize(*(raw[k] for k in ('events','query','catalog','registry','plan')),hashes.hash,hashes.raw_sha256),None,stale_errors)
        f.qualify_outcome();raw=source_raw()
        execute('after_explicit_stale_qualified_outcome',M.materialize(*(raw[k] for k in ('events','query','catalog','registry','plan')),hashes.hash,hashes.raw_sha256),None,stale_errors)
        f.current('history');f.query=copy.deepcopy(f.query)
        last=next(e for e in f.events if e['stream_id']==f.stream and e['kind']=='scope_changed')
        f.query.update(at_ns=last['available_at_ns'],cutoff_plan_seq=last['plan_seq']);raw=source_raw()
        execute('withdrawn_history',M.materialize(*(raw[k] for k in ('events','query','catalog','registry','plan')),hashes.hash,hashes.raw_sha256),None)
        # A current withdrawal attached to rev2 must also fence historical rev1.
        replacement=next(e['target'] for e in f.events if e['stream_id']==f.stream and e['kind']=='material_available' and e['target']['revision']==2)
        last['target']=copy.deepcopy(replacement)
        f.events_raw=b''.join(T.G.compact(e)+b'\n' for e in f.events)
        f.catalog['events_raw_sha256']=M.sha(f.events_raw);raw=source_raw()
        execute('withdrawn_new_revision_history',M.materialize(*(raw[k] for k in ('events','query','catalog','registry','plan')),hashes.hash,hashes.raw_sha256),None)
        f.current('current');f.catalog['admissions']=[];raw=source_raw()
        execute('no_admission',M.materialize(*(raw[k] for k in ('events','query','catalog','registry','plan')),hashes.hash,hashes.raw_sha256),None)
        for qualified in (False,True):
            f=Fixture(live_outcome=True);f.current('pending')
            if qualified:f.qualify_outcome()
            raw=source_raw()
            execute('live_outcome_'+str(qualified),M.materialize(*(raw[k] for k in ('events','query','catalog','registry','plan')),hashes.hash,hashes.raw_sha256),1)
    # The actual materializer CLI also owns and publishes a fresh bundle.
    f=Fixture();inputs=root/'cli-inputs';inputs.mkdir();raw=source_raw()
    args=[sys.executable,'-B',str(HERE/'materialize_native.py')]
    for name,value in raw.items():
        path=inputs/(name+'.json');path.write_bytes(value);args+=['--'+name,str(path)]
    target=root/'materializer-cli'
    args+=['--native-cli',native_cli,'--native-cli-sha256',program_sha,'--out',str(target)]
    proc=subprocess.run(args,capture_output=True,text=True,timeout=60)
    assert proc.returncode==0,(proc.stdout,proc.stderr)
    status=json.loads(proc.stdout)
    for name,sha in status['outputs'].items():assert M.sha((target/name).read_bytes())==sha
    assert subprocess.run(args,capture_output=True,text=True,timeout=60).returncode==2
    print('actual original source -> materializer/hash codec -> native seven policies/run+verify: pending/current/history/withdrawal/no-admission; explicit permissions only, no gold input or helper build')


if __name__=='__main__':
    if len(sys.argv)==4 and sys.argv[1]=='--integration':integration(Path(sys.argv[2]),sys.argv[3])
    else:unittest.main()

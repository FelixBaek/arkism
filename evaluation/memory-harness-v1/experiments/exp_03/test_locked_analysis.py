"""Real generator/lock/analysis joins, with synthetic owner values only."""
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


B = load('_locked_analysis_under_test', HERE / 'locked_analysis.py')
T = load('_locked_analysis_source_fixture', HERE.with_name('exp_01') / 'test_protocol_lock_v2.py')
H = load('_locked_analysis_plan_fixture', HERE / 'test_analysis.py')
A = B.A


def fixture():
    spec = T.T.fixture_spec()
    spec['label_version'] = 'labels_v2'
    data, (_, original, gold, manifest) = T.G.expand_spec(T.T.raw_spec(spec))
    values = T.values()
    decisions = dict(repetitions=1, delta_min='0.05', sample_size=dict(test_clusters=1,validation_clusters=1,
        recompute_before_test_when_real_outcomes_exist=False), token_budget=dict(max_input_tokens=100,max_output_tokens=80),k=3)
    for key,value in decisions.items():values['values'][key]['value'] = value
    values_raw = T.raw(values)
    raw = dict(values=values_raw,manifest=data['split_manifest.json'],events=data['events.jsonl'],
               queries=data['queries.jsonl'],gold=data['gold.jsonl'])
    raw['lock'] = B.L.lock_bytes(B.L.build_lock(values_raw,raw['manifest']))
    plan,_,_,_ = H.fixture()
    plan.update(stage='confirmatory',experiment_family='p1')
    plan['plan_sha256'] = A.digest({k:v for k,v in plan.items() if k != 'plan_sha256'})
    raw['analysis_source'] = A.canonical(dict(protocol='exp03-analysis-source-v1',queries_raw_sha256=A.sha(raw['queries']),
        gold_raw_sha256=A.sha(raw['gold'])))+b'\n'
    test_streams = {e['stream_id'] for e in manifest['entries'] if e['split']=='final_test'}
    labels = {g['query_id']:g for g in gold}
    schedule = dict(protocol='exp03-schedule-v1',analysis_plan_sha256=plan['plan_sha256'],queries=[])
    for q in original:
        if q['stream_id'] not in test_streams or q['request_mode'] != 'current':continue
        g = labels[q['query_id']]
        kind = 'answerable' if g['cohort']=='primary_answerable' else 'clear_negative' if g['cohort']=='clear_negative' else 'ambiguous'
        schedule['queries'].append(dict(stream_id=q['stream_id'],query_id=q['query_id'],cluster_id=q['stream_id'],scope=q['scope'],
            kind=kind,request_mode='current',gold_sets=[g['required_refs']] if kind=='answerable' else [],
            budget=dict(final_k=3,max_input_tokens=100,max_output_tokens=80),input_sha256=A.sha(raw['analysis_source'])))
    assignments = dict(protocol='exp03-final-cluster-assignments-v1',manifest_raw_sha256=A.sha(raw['manifest']),
        assignments=[dict(stream_id=e['stream_id'],cluster_id=e['stream_id']) for e in manifest['entries']])
    raw.update(analysis_plan=A.canonical(plan),schedule=A.canonical(schedule),cluster_assignments=A.canonical(assignments))
    return raw


def update(raw, name, fn):
    document = A.parse(raw[name]);fn(document)
    if name == 'analysis_plan':
        document['plan_sha256']=A.digest({k:v for k,v in document.items() if k!='plan_sha256'})
        schedule=A.parse(raw['schedule']);schedule['analysis_plan_sha256']=document['plan_sha256'];raw['schedule']=A.canonical(schedule)
    raw[name]=A.canonical(document)


def reviewed_fixture(raw, run_plan_raw=b'{"synthetic_unexecuted_run_plan":true}', projection=None):
    if projection is None:
        projection=dict(selection_rule='first_completed_else_last_attempted',analysis_timing='before_results',slots=[])
    manifest=json.loads(raw['manifest'])
    labels=B.source(B.V._rows,raw['gold'],'GoldRow')
    record=dict(protocol='exp01-gold-review-v1',dataset_id=manifest['dataset_id'],dataset_version=manifest['dataset_version'],
        label_version=manifest['label_version'],dataset_contract_sha256=manifest['contract_sha256'],generator_spec_sha256=manifest['spec_sha256'],
        events_sha256=A.sha(raw['events']),queries_sha256=A.sha(raw['queries']),gold_sha256=A.sha(raw['gold']),
        split_assignment_sha256=manifest['split_assignment_sha256'],generator_author_ids=manifest['generator_author_ids'],
        label_author_ids=manifest['label_author_ids'],reviewer_id='synthetic_independent_reviewer',reviewer_role='independent_gold_reviewer',
        review_version='synthetic_v1',items=[dict(query_id=g['query_id'],gold_row_sha256=B.G.semantic_sha('GoldRow',g),
            judgment='confirmed_provisional',rule_id=g['label_rule_id']) for g in labels],
        status='completed_provisional_review',disagreements=[],prior_review_sha256=None)
    gold_review=A.canonical(record)+b'\n'
    plan=A.parse(raw['analysis_plan'])
    binding=B.bind(raw,'before_results')
    owner=dict(protocol='exp03-owner-analysis-review-v1',reviewer_id='synthetic_research_owner',role='research_owner',decision='accepted_mapping',
        reviewed_at='2026-11-01',analysis_plan_sha256=plan['plan_sha256'],schedule_sha256=A.digest(A.parse(raw['schedule'])),
        locked_binding_sha256=binding['binding_sha256'],run_plan_raw_sha256=A.sha(run_plan_raw),
        projection_rule_sha256=B.projection_rule_sha256(projection),independent_gold_review_raw_sha256=A.sha(gold_review),
        review_scope='semantic_analysis_mapping_not_execution_or_product_authority')
    return gold_review,A.canonical(owner)+b'\n',projection,run_plan_raw


class LockedAnalysisTests(unittest.TestCase):
    def setUp(self):self.raw=fixture()

    def rejects(self,code):
        with self.assertRaises(A.AnalysisError) as caught:B.bind(self.raw,'before_results')
        self.assertEqual(caught.exception.code,code)

    def test_real_generated_lock_and_final_test_joins_are_deterministic(self):
        result=B.bind(self.raw,'before_results')
        self.assertEqual(result,B.verify(result,self.raw))
        # Source scenario: before/pending/current/alternative/ambiguous/after
        # are current queries; history is separately excluded by request_modes.
        self.assertEqual(result['counts'],dict(test_histories=1,test_clusters=1,validation_clusters=1,queries=6,alternative_queries=1))
        self.assertFalse(result['execution_authorized'])
        self.assertFalse(result['scientific_approval_verified'])
        self.assertTrue(result['native_execution_reconciliation_required'])
        self.assertTrue(result['human_semantic_review_required'])
        self.assertNotIn('Mina',json.dumps(result))

    def test_changed_source_bytes_do_not_reuse_lock(self):
        self.raw['values']+=b' '
        self.rejects('source_hash_mismatch')

    def test_each_numeric_analysis_decision_matches_lock(self):
        cases=[('delta_min','0.2','locked_delta_mismatch'),('replications',2,'locked_replications_mismatch'),
               ('tuning_trials',1,'locked_tuning_mismatch')]
        for key,value,code in cases:
            with self.subTest(key=key):
                self.raw=fixture();update(self.raw,'analysis_plan',lambda p:p.update({key:value}));self.rejects(code)

    def test_final_k_and_token_caps_cannot_drift(self):
        for key,value,code in [('final_k',4,'locked_final_k_mismatch'),('max_input_tokens',99,'locked_token_budget_mismatch'),
                               ('max_output_tokens',None,'locked_token_budget_mismatch')]:
            with self.subTest(key=key):
                self.raw=fixture();update(self.raw,'schedule',lambda s:s['queries'][0]['budget'].update({key:value}));self.rejects(code)

    def test_dropped_query_or_final_test_history_is_rejected(self):
        update(self.raw,'schedule',lambda s:s['queries'].pop())
        self.rejects('final_test_query_coverage_mismatch')

    def test_validation_query_cannot_replace_final_test(self):
        query=A.parse(self.raw['schedule'])['queries'][0]
        manifest=json.loads(self.raw['manifest'])
        validation=next(e['stream_id'] for e in manifest['entries'] if e['split']=='validation')
        update(self.raw,'schedule',lambda s:[q.update(stream_id=validation,cluster_id=validation) for q in s['queries']])
        self.rejects('final_test_stream_coverage_mismatch')

    def test_resealed_schedule_cannot_replace_gold(self):
        update(self.raw,'schedule',lambda s:s['queries'][0]['gold_sets'][0][0].update(id='forged'))
        self.rejects('locked_gold_mismatch')

    def test_unknown_input_source_is_rejected(self):
        update(self.raw,'schedule',lambda s:s['queries'][0].update(input_sha256='f'*64))
        self.rejects('locked_analysis_source_mismatch')

    def test_cluster_partition_requires_complete_original_assignments(self):
        update(self.raw,'cluster_assignments',lambda s:s['assignments'].pop())
        self.rejects('cluster_assignment_coverage_mismatch')

    def test_preassigned_cluster_names_preserved_without_invented_grouping(self):
        update(self.raw,'analysis_plan',lambda p:p.update(cluster_unit='preassigned_cluster'))
        mapping={r['stream_id']:'fixed_'+str(i) for i,r in enumerate(A.parse(self.raw['cluster_assignments'])['assignments'])}
        update(self.raw,'cluster_assignments',lambda s:[r.update(cluster_id=mapping[r['stream_id']]) for r in s['assignments']])
        update(self.raw,'schedule',lambda s:[r.update(cluster_id=mapping[r['stream_id']]) for r in s['queries']])
        result=B.bind(self.raw,'before_results')
        self.assertEqual(result['counts']['test_clusters'],1)
        update(self.raw,'schedule',lambda s:[r.update(cluster_id='different_partition') for r in s['queries']])
        self.rejects('locked_cluster_assignment_mismatch')

    def test_preassigned_clusters_cannot_cross_any_dataset_split(self):
        update(self.raw,'analysis_plan',lambda p:p.update(cluster_unit='preassigned_cluster'))
        update(self.raw,'cluster_assignments',lambda s:[r.update(cluster_id='same_cluster') for r in s['assignments']])
        update(self.raw,'schedule',lambda s:[r.update(cluster_id='same_cluster') for r in s['queries']])
        self.rejects('cluster_assignment_split_overlap')

    def test_each_raw_dataset_hash_is_bound_even_for_same_semantic_rows(self):
        for name in ('events','queries','gold'):
            with self.subTest(name=name):
                self.raw=fixture();self.raw[name]=self.raw[name].replace(b':',b': ',1);self.rejects('locked_dataset_hash_mismatch')

    def test_historical_mode_joins_original_history_without_new_labels(self):
        update(self.raw,'analysis_plan',lambda p:p.update(request_modes=['current','as_of']))
        originals=B.source(B.V._rows,self.raw['queries'],'QueryPlan')
        labels=B.source(B.V._rows,self.raw['gold'],'GoldRow')
        test_stream=A.parse(self.raw['schedule'])['queries'][0]['stream_id']
        original=next(q for q in originals if q['stream_id']==test_stream and q['request_mode']=='historical')
        label=next(g for g in labels if g['query_id']==original['query_id'])
        query=dict(stream_id=test_stream,query_id=original['query_id'],cluster_id=test_stream,scope=original['scope'],
            kind='answerable',request_mode='as_of',gold_sets=[label['required_refs']],
            budget=dict(final_k=3,max_input_tokens=100,max_output_tokens=80),input_sha256=A.sha(self.raw['analysis_source']))
        update(self.raw,'schedule',lambda s:s['queries'].append(query))
        self.assertEqual(B.bind(self.raw,'before_results')['counts']['queries'],7)

    def test_cluster_count_cannot_be_recomputed_by_changing_lock_values_only(self):
        values=json.loads(self.raw['values'])
        values['values']['sample_size']['value']['test_clusters']=2
        self.raw['values']=T.raw(values)
        self.raw['lock']=B.L.lock_bytes(B.L.build_lock(self.raw['values'],self.raw['manifest']))
        self.rejects('locked_test_cluster_count_mismatch')

    def test_posthoc_confirmatory_is_refused_but_exploratory_retained(self):
        with self.assertRaises(A.AnalysisError) as caught:B.bind(self.raw,'after_results')
        self.assertEqual(caught.exception.code,'posthoc_analysis_change_required')
        update(self.raw,'analysis_plan',lambda p:p.update(analysis_changed_after_results=True))
        self.rejects('posthoc_confirmatory_forbidden')
        update(self.raw,'analysis_plan',lambda p:p.update(stage='exploratory'))
        result=B.bind(self.raw,'after_results')
        self.assertEqual(result['analysis_timing'],'after_results')
        self.assertFalse(result['scientific_approval_verified'])

    def test_changed_binding_even_if_resealed_is_rejected(self):
        record=B.bind(self.raw,'before_results');record['counts']['queries']=0
        record['binding_sha256']=A.digest({k:v for k,v in record.items() if k!='binding_sha256'})
        with self.assertRaises(A.AnalysisError) as caught:B.verify(record,self.raw)
        self.assertEqual(caught.exception.code,'locked_analysis_binding_mismatch')

    def test_externally_pinned_review_binds_real_original_review_record(self):
        gold_review,owner,projection,run_plan=reviewed_fixture(self.raw)
        report=B.review_final_inputs(self.raw,gold_review,owner,A.sha(owner),run_plan,projection)
        self.assertEqual(report['owner_review_status'],'externally_pinned_declaration_not_authenticated_person')
        self.assertFalse(report['scientific_approval_verified'])
        self.assertFalse(report['execution_authorized'])
        self.assertEqual(report['computational_binding']['independent_gold_review_status'],'pending_independent_review')

    def test_payload_owner_hash_cannot_replace_external_expected_pin(self):
        gold,owner,projection,runplan=reviewed_fixture(self.raw)
        with self.assertRaises(A.AnalysisError) as caught:
            B.review_final_inputs(self.raw,gold,owner,'0'*64,runplan,projection)
        self.assertEqual(caught.exception.code,'trusted_owner_review_mismatch')

    def test_owner_review_cannot_rebind_changed_run_plan_or_selection_rule(self):
        gold,owner,projection,runplan=reviewed_fixture(self.raw)
        for changed_plan,changed_projection in [(runplan+b' ',projection),(runplan,dict(projection,selection_rule='last_attempted'))]:
            with self.subTest(plan=changed_plan),self.assertRaises(A.AnalysisError) as caught:
                B.review_final_inputs(self.raw,gold,owner,A.sha(owner),changed_plan,changed_projection)
            self.assertIn(caught.exception.code,('owner_review_binding_mismatch','primary_attempt_selection_policy_mismatch'))

    def test_gold_review_missing_item_or_author_self_review_rejected(self):
        gold,owner,projection,runplan=reviewed_fixture(self.raw)
        for change in (lambda r:r['items'].pop(),lambda r:r.update(reviewer_id=r['label_author_ids'][0])):
            with self.subTest(change=change),self.assertRaises(A.AnalysisError) as caught:
                record=json.loads(gold);change(record)
                B.review_final_inputs(self.raw,A.canonical(record),owner,A.sha(owner),runplan,projection)
            self.assertEqual(caught.exception.code,'source_review_unresolved')

    def test_future_index_and_grade_bytes_are_not_claimed_to_be_prelocked(self):
        gold,owner,projection,runplan=reviewed_fixture(self.raw)
        projection.update(run_index_raw_sha256='1'*64,grader_artifact=dict(post_execution_labels=True))
        report=B.review_final_inputs(self.raw,gold,owner,A.sha(owner),runplan,projection)
        self.assertFalse(report['scientific_approval_verified'])
        projection['slots']=[dict(job_id='changed',attempt_order=['chosen_after_results'])]
        with self.assertRaises(A.AnalysisError) as caught:
            B.review_final_inputs(self.raw,gold,owner,A.sha(owner),runplan,projection)
        self.assertEqual(caught.exception.code,'owner_review_binding_mismatch')

    def test_actual_source_roots_join_final_inputs_and_separate_review_pin(self):
        P=load('_locked_analysis_projector_component',HERE/'project_native.py')
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            roots={name:str(root/name) for name in ('input','evidence','output')}
            for path in roots.values():Path(path).mkdir()
            def write(name,raw,role,where='input'):
                path=Path(roots[where])/(name+'.json');path.write_bytes(raw)
                return dict(role=role,relative_path=path.name,raw_sha256=A.sha(raw),size_bytes=len(raw))
            refs={name:write(name,raw,'owner_contract' if name=='lock' else 'source') for name,raw in self.raw.items()}
            # This test exercises the final-input source component, not the
            # separate closed RunPlan/native execution validation in project().
            selection=write('selected_ref',b'{"synthetic_unverified_selection":true}\n','REF_selection','evidence')
            plan=dict(stage='confirmatory',protocol_lock=refs['lock'],jobs=[dict(common=dict(source_manifest=refs['analysis_source']),
                bindings=dict(REF_use=dict(mode='uses_selected_REF',selection_record=selection)))])
            runplan=A.canonical(plan)
            projection=dict(query_artifact=refs['queries'],gold_artifact=refs['gold'],analysis_timing='before_results',
                            selection_rule='first_completed_else_last_attempted',slots=[])
            gold,owner,_,_=reviewed_fixture(self.raw,runplan,projection)
            context=dict(protocol='exp03-final-inputs-v1',files=refs,
                         gold_review=write('gold_review',gold,'acceptance','evidence'),
                         owner_review=write('owner_review',owner,'owner_contract','evidence'))
            sources=P.Sources(roots)
            result=P.verify_final_inputs(context,A.sha(owner),A.parse(self.raw['analysis_plan']),A.parse(self.raw['schedule']),
                                         plan,runplan,projection,sources)
            sources.recheck()
            self.assertEqual(result['owner_review_raw_sha256'],A.sha(owner))
            self.assertGreaterEqual(len(sources.raw),len(self.raw)+2)
            self.assertFalse(result['execution_authorized'])
            self.assertFalse(result['REF_selection_semantics_verified'])
            self.assertEqual(result['REF_selection_records'][0]['raw_sha256'],selection['raw_sha256'])
            with self.assertRaises(P.A.AnalysisError) as caught:
                P.verify_final_inputs(context,'0'*64,A.parse(self.raw['analysis_plan']),A.parse(self.raw['schedule']),
                                      plan,runplan,projection,P.Sources(roots))
            self.assertEqual(caught.exception.code,'trusted_owner_review_mismatch')

    def test_current_owner_policy_refuses_last_attempt_primary_even_if_review_resealed(self):
        gold,owner,projection,runplan=reviewed_fixture(self.raw)
        projection['selection_rule']='last_attempted'
        record=json.loads(owner);record['projection_rule_sha256']=B.projection_rule_sha256(projection)
        owner=A.canonical(record)
        with self.assertRaises(A.AnalysisError) as caught:
            B.review_final_inputs(self.raw,gold,owner,A.sha(owner),runplan,projection)
        self.assertEqual(caught.exception.code,'primary_attempt_selection_policy_mismatch')

    def test_confirmatory_internal_path_requires_real_native_replay(self):
        P=load('_locked_analysis_projector_no_replay',HERE/'project_native.py')
        plan,schedule,observations,ledger=H.fixture()
        plan['stage']='confirmatory'
        H.reseal(plan,schedule,observations,ledger)
        with self.assertRaises(P.A.AnalysisError) as caught:
            P.analyze_native_inputs(plan,schedule,observations,ledger,dict(REF_selection_semantics_verified=False),[])
        self.assertEqual(caught.exception.code,'confirmatory_native_replay_required')

    def test_confirmatory_REF_cannot_use_unverified_selection_meaning(self):
        P=load('_locked_analysis_projector_ref_guard',HERE/'project_native.py')
        plan,schedule,observations,ledger=H.fixture()
        plan['stage']='confirmatory';plan['policies'].append('REF')
        H.reseal(plan,schedule,observations,ledger)
        with self.assertRaises(P.A.AnalysisError) as caught:
            P.analyze_native_inputs(plan,schedule,observations,ledger,dict(REF_selection_semantics_verified=False),
                                    [dict(synthetic_guard_fixture_not_native_proof=True)])
        self.assertEqual(caught.exception.code,'confirmatory_REF_selection_semantics_required')

    def test_native_cli_requires_external_pin_together_with_final_inputs(self):
        args=[sys.executable,'-B',str(HERE/'project_native.py')]
        for name in ('analysis-plan','schedule','run-plan','run-index','projection-plan','input-root','evidence-root',
                     'output-root','native-cli','out'):
            args+=['--'+name,'/unused']
        args+=['--final-inputs','/unused']
        proc=subprocess.run(args,capture_output=True,text=True,timeout=30)
        self.assertEqual(proc.returncode,2)
        self.assertEqual(json.loads(proc.stdout)['code'],'final_review_arguments_required')
        self.assertEqual(proc.stderr,'')

    def test_actual_cli_new_file_and_verify_and_overwrite_refusal(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);args=[sys.executable,'-B',str(HERE/'locked_analysis.py')]
            for name,value in self.raw.items():
                file=root/(name+'.json');file.write_bytes(value);args+=['--'+name.replace('_','-'),str(file)]
            args+=['--analysis-timing','before_results','--out',str(root/'binding.json')]
            proc=subprocess.run(args,capture_output=True,text=True,timeout=30)
            self.assertEqual(proc.returncode,0,proc.stdout+proc.stderr)
            output=(root/'binding.json').read_bytes()
            self.assertFalse(json.loads(output)['execution_authorized'])
            self.assertNotIn(str(root).encode(),output)
            self.assertNotIn(b'Mina',output)
            again=subprocess.run(args,capture_output=True,text=True,timeout=30)
            self.assertEqual(again.returncode,2)
            args[-1]=str(root/'verified.json');args+=['--verify-binding',str(root/'binding.json')]
            verified=subprocess.run(args,capture_output=True,text=True,timeout=30)
            self.assertEqual(verified.returncode,0,verified.stdout+verified.stderr)
            self.assertEqual(output,(root/'verified.json').read_bytes())


if __name__=='__main__':unittest.main()

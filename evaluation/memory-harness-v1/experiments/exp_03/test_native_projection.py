"""Formal record tests; --integration uses the real Go command/test executable."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent


def load(name, file):
    spec = importlib.util.spec_from_file_location(name,HERE/file)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


P = load('_projection_test_impl','project_native.py')
T = load('_projection_test_fixture','test_analysis.py')
A, B = P.A, P.B


class Fixture:
    def __init__(self, root, native_cli=None, native=False):
        if native_cli is None:
            path=Path(root)/'unit-no-verification'
            path.write_text('#!'+sys.executable+'\nraise SystemExit(97)\n')
            path.chmod(0o700)
            native_cli=str(path)
        self.root, self.native_cli = Path(root), native_cli
        self.roots = {k:str(self.root/('evidence' if k=='evidence' else '.')) for k in ('input','evidence','output')}
        (self.root/'evidence').mkdir(exist_ok=True)
        self.stub = self.write('stub.json',{'synthetic_metadata_not_acceptance':True})
        (self.root/'evidence'/'stub.json').write_bytes((self.root/'stub.json').read_bytes())
        scope = dict(workspace='w',project='p',user='u',agent='a')
        probe = dict(stream_id='s',query_id='q',scope=scope,at_ns=1,cutoff_accepted_seq=1,
                     candidate_snapshot_id='snapshot',candidate_snapshot_sha256='1'*64,final_k=2,
                     context_budget=dict(unit='fixture_units_v0',limit=4))
        if native:
            probe = json.loads((self.root/'probes.json').read_bytes())['probes'][0]
        self.probe = probe
        analysis, schedule, _, _ = T.fixture()
        analysis.update(policies=list(B.ENUMS['RequiredPolicyID']),analysis_changed_after_results=True)
        self.source = self.write('source.json',{'synthetic_source_binding':True})
        query = dict(stream_id=probe['stream_id'],query_id=probe['query_id'],scope=scope,cluster_id=probe['stream_id'],
                     request_mode='current',kind='answerable',gold_sets=[[T.ref('a'),T.ref('b')]],
                     budget=dict(final_k=2,max_input_tokens=None,max_output_tokens=None),input_sha256=self.source['raw_sha256'])
        analysis['plan_sha256'] = A.digest({k:v for k,v in analysis.items() if k!='plan_sha256'})
        schedule.update(analysis_plan_sha256=analysis['plan_sha256'],queries=[query])
        self.analysis,self.schedule = analysis,schedule
        gold = dict(stream_id=probe['stream_id'],query_id=probe['query_id'],scope=scope,clock_domain='c',at_ns=probe['at_ns'],
            cutoff_plan_seq=1,cohort='primary_answerable',temporal_intent='current',historical_as_of_ns=None,
            correction_phase='none',required_refs=query['gold_sets'][0],alternative_ref_sets=[],forbidden_refs=[],
            plausible_refs=[],cause_event_ids=[],label_version='fixture_v1',label_status='provisional',label_rule_id='fixture_rule')
        self.gold = self.write_raw('gold.jsonl',P.G.compact(gold)+b'\n')
        source_query = dict(stream_id=probe['stream_id'],query_id=probe['query_id'],scope=scope,clock_domain='c',
            at_ns=probe['at_ns'],cutoff_plan_seq=1,query_text='PRIVATE_QUERY_SENTINEL',request_mode='current',requested_as_of_ns=None)
        self.source_query,self.gold_row = source_query,gold
        self.queries = self.write_raw('queries.jsonl',P.G.compact(source_query)+b'\n')
        self.source = self.write('source.json',dict(protocol='exp03-analysis-source-v1',
            queries_raw_sha256=self.queries['raw_sha256'],gold_raw_sha256=self.gold['raw_sha256']))
        query['input_sha256']=self.source['raw_sha256']
        budget = dict(protocol='exp03-native-budget-rule-v1',queries=[dict(stream_id=query['stream_id'],query_id=query['query_id'],
            **query['budget'],context_budget=probe['context_budget'])])
        self.budget = self.write('budget.json',budget,'budget_rule')
        binding_roles = dict(matrix='matrix',controls='controls',exp01_draft='owner_contract',REF_contract='owner_contract',
            operating_plan='operating_plan',readiness='readiness',registry_raw='registry',native_config_raw='config',
            materialization_manifest='native_materialization',native_input_manifest='native_input',native_validation_evidence='native_validation',
            DYN05_acceptance='acceptance',gold_review_evidence='acceptance')
        bindings = {k:dict(self.stub,role=role) for k,role in binding_roles.items()}
        bindings.update(registry_semantic=dict(type_name='PolicyRegistryV2',semantic_sha256='1'*64),
            native_config_semantic=dict(type_name='DynamicsRunConfigV2',semantic_sha256='1'*64),
            evaluator_adapter_acceptance=None,REF_use=dict(mode='validation_data_collection',selection_status='not_run_pending_selection',selection_record=None))
        common = {k:dict(self.stub,role=role) for k,role in dict(candidate_file='candidates',events_file='events',
            features_file='features',probe_file='probes',eligibility_rule='eligibility_rule',encoder_revision_rule='encoder_rule',
            reader_generator_rule='reader_rule',seed_schedule_rule='seed_rule').items()}
        common.update(source_manifest=self.source,budget_rule=self.budget,clock_domain='c',
            expected_schedule=[{k:probe[k] for k in ('stream_id','query_id','scope','at_ns','cutoff_accepted_seq',
                                                   'candidate_snapshot_id','candidate_snapshot_sha256')}])
        if native:
            for k,file in [('registry_raw','registry.json'),('native_config_raw','config.json'),('native_input_manifest','p1.json')]:
                bindings[k] = self.ref(file,bindings[k]['role'])
            cfg = json.loads((self.root/'config.json').read_bytes())
            bindings['registry_semantic']['semantic_sha256'] = cfg['registry_sha256']
            bindings['native_config_semantic']['semantic_sha256'] = cfg['config_sha256']
            for k,file in [('candidate_file','candidates.json'),('events_file','events.json'),('features_file','features.json'),('probe_file','probes.json')]:
                common[k] = self.ref(file,common[k]['role'])
        materialization = dict(protocol='exp03-native-materialization-v1',source_manifest_raw_sha256=self.source['raw_sha256'],
            native_input_manifest_raw_sha256=bindings['native_input_manifest']['raw_sha256'],queries=[dict(
                stream_id=query['stream_id'],query_id=query['query_id'],source_query_sha256=P.G.semantic_sha('QueryPlan',source_query),
                source_gold_sha256=P.G.semantic_sha('GoldRow',gold),source_cutoff_plan_seq=source_query['cutoff_plan_seq'],
                native_cutoff_accepted_seq=probe['cutoff_accepted_seq'],requested_as_of_ns=None,
                native_candidate_snapshot_sha256=probe['candidate_snapshot_sha256'],clock_mapping='same_clock_identity_ns')])
        bindings['materialization_manifest']=self.write('materialization.json',materialization,'native_materialization')
        job = dict(job_id='job',condition_id='condition',comparison_family='fixed_candidate_mechanism',
            signal_regime='synthetic_all_candidate_oracle',bindings=bindings,common=common,policy_order=list(B.ENUMS['RequiredPolicyID']),
            native_argv=['recorded_metadata_never_executed'],output_relative_dir='output',claim_control_ids=[])
        self.plan = dict(protocol='exp02-run-plan-v1',stage='early_pilot',experiment_family=analysis['experiment_family'],
            verification_scope='offline_synthetic',jobs=[job],pending_research_decisions=['actual_gold_review_pending'],
            claim_scope='engineering_pilot_or_separately_locked_confirmatory',protocol_lock=None,planning_dispatch=dict(self.stub,role='dispatch'))
        self.row = dict(job_id='job',attempt_id='try1',plan_raw_sha256='',execution_dispatch=dict(self.stub,role='dispatch'),
            input_bindings=copy.deepcopy(bindings),actual_argv=[],source_manifest=self.source,started_at_ns=None,finished_at_ns=None,
            process_exit=None,status='not_run',native_completion_manifest=None,native_stdout_status=None,raw_log=None,
            policy_rows=[],cost_measures=[],deviations=[],execution_clock_domain=None)
        if native:
            completion = json.loads((self.root/'output/manifest.json').read_bytes())
            self.row.update(actual_argv=job['native_argv'],started_at_ns=1,finished_at_ns=2,process_exit=0,status=completion['status'],
                native_completion_manifest=self.ref('output/manifest.json','completion_manifest'),
                native_stdout_status=self.ref('stdout.json','stdout_status'),execution_clock_domain='c')
            for policy in job['policy_order']:
                ref = self.ref('output/predictions/'+policy+'.json','prediction')
                run = json.loads((self.root/ref['relative_path']).read_bytes())
                failed = run['cost']['failed_predictions']
                self.row['policy_rows'].append(dict(policy_id=policy,policy_version=run['policy_version'],prediction_file=ref,
                    planned_rows=1,recorded_rows=1,successful_rows=1-failed,failed_rows=failed,missing_rows=0,fallback_rows=0))
        self.index = dict(protocol='exp02-run-index-v1',plan={},rows=[self.row],analysis_status='not_scored_by_EXP02',
            evaluation_adapter=dict(owner_task='EXP-03',role='v2_evaluation_adapter',binding=None,expected_owner_schema=None,owner_acceptance=None))
        self.projection = dict(protocol='exp03-native-projection-plan-v1',analysis_plan_sha256=analysis['plan_sha256'],
            schedule_sha256=A.digest(schedule),run_plan_raw_sha256='',run_index_raw_sha256='',
            native_cli_sha256=A.sha(Path(native_cli).read_bytes()),verification_timeout_seconds=30,
            selection_rule='first_completed_else_last_attempted',analysis_timing='after_results',gold_artifact=self.gold,
            query_artifact=self.queries,grader_artifact=None,cost_source_root='output',price_source_root='evidence',
            slots=[dict(job_id='job',repetition=0,attempt_order=['try1'])])
        self.seal()

    def ref(self,path,role):
        raw=(self.root/path).read_bytes()
        return dict(role=role,relative_path=path,raw_sha256=A.sha(raw),size_bytes=len(raw))

    def write_raw(self,path,raw,role='source'):
        (self.root/path).write_bytes(raw)
        return self.ref(path,role)

    def write(self,path,value,role='source'):
        return self.write_raw(path,A.canonical(value)+b'\n',role)

    def seal(self):
        self.plan_raw=B.compact(self.plan)
        self.index['plan']=dict(role='plan',relative_path='plan.json',raw_sha256=A.sha(self.plan_raw),size_bytes=len(self.plan_raw))
        for row in self.index['rows']:
            row['plan_raw_sha256']=A.sha(self.plan_raw)
        self.index_raw=B.compact(self.index)
        self.projection.update(run_plan_raw_sha256=A.sha(self.plan_raw),run_index_raw_sha256=A.sha(self.index_raw))

    def project(self):
        self.seal()
        return P.project(self.analysis,self.schedule,self.plan_raw,self.index_raw,self.projection,self.roots,self.native_cli)

    def timeout(self,row=None):
        row=self.row if row is None else row
        row.update(status='timeout',started_at_ns=1,finished_at_ns=2,process_exit=124,execution_clock_domain='c',
                   actual_argv=self.plan['jobs'][0]['native_argv'])

    def cost(self,name,value,allocation='policy_incremental',basis='observed',resource='synthetic_units'):
        source=self.write(name+'.json',{'synthetic_quantity':value},'cost_source') if basis!='missing' else None
        return dict(measurement_id=name,phase='retrieval',resource=resource,allocation=allocation,
            policy_id='B2' if allocation=='policy_incremental' else None,value_decimal=value,unit=B.COST_UNITS[resource],
            basis=basis,missing_reason='not_measured' if basis=='missing' else None,source_artifact=source,price_artifact=None)

    def historical(self,clock='c'):
        self.source_query.update(request_mode='historical',requested_as_of_ns=self.probe['at_ns']-1,
                                 cutoff_plan_seq=7,clock_domain=clock)
        self.gold_row.update(temporal_intent='historical',historical_as_of_ns=self.probe['at_ns']-1,
                             cutoff_plan_seq=7,clock_domain=clock)
        self.queries=self.write_raw('queries.jsonl',P.G.compact(self.source_query)+b'\n')
        self.gold=self.write_raw('gold.jsonl',P.G.compact(self.gold_row)+b'\n')
        self.source=self.write('source.json',dict(protocol='exp03-analysis-source-v1',queries_raw_sha256=self.queries['raw_sha256'],
                                                 gold_raw_sha256=self.gold['raw_sha256']))
        self.schedule['queries'][0].update(request_mode='as_of',input_sha256=self.source['raw_sha256'])
        self.analysis['request_modes']=['as_of']
        self.analysis['plan_sha256']=A.digest({k:v for k,v in self.analysis.items() if k!='plan_sha256'})
        self.schedule['analysis_plan_sha256']=self.analysis['plan_sha256']
        job=self.plan['jobs'][0]
        job['common']['source_manifest']=self.source
        mapping=json.loads((self.root/'materialization.json').read_bytes())
        mapping['source_manifest_raw_sha256']=self.source['raw_sha256']
        mapping['queries'][0].update(source_query_sha256=P.G.semantic_sha('QueryPlan',self.source_query),
            source_gold_sha256=P.G.semantic_sha('GoldRow',self.gold_row),source_cutoff_plan_seq=7,
            requested_as_of_ns=self.probe['at_ns']-1,
            clock_mapping='same_clock_identity_ns' if clock=='c' else 'renamed_synthetic_clock_identity_ns')
        ref=self.write('materialization.json',mapping,'native_materialization')
        job['bindings']['materialization_manifest']=ref
        for row in self.index['rows']:
            row['source_manifest']=self.source
            row['input_bindings']['materialization_manifest']=ref
        self.projection.update(gold_artifact=self.gold,query_artifact=self.queries,analysis_plan_sha256=self.analysis['plan_sha256'],
                               schedule_sha256=A.digest(self.schedule))

    def second_replication(self):
        job=copy.deepcopy(self.plan['jobs'][0])
        job.update(job_id='job2',output_relative_dir='output2')
        self.plan['jobs'].append(job)
        row=copy.deepcopy(self.row)
        row.update(job_id='job2',input_bindings=copy.deepcopy(job['bindings']))
        self.index['rows'].append(row)
        self.analysis['replications']=2
        self.analysis['plan_sha256']=A.digest({k:v for k,v in self.analysis.items() if k!='plan_sha256'})
        self.schedule['analysis_plan_sha256']=self.analysis['plan_sha256']
        self.projection.update(analysis_plan_sha256=self.analysis['plan_sha256'],schedule_sha256=A.digest(self.schedule))
        self.projection['slots'].append(dict(job_id='job2',repetition=1,attempt_order=['try1']))
        return job,row


class ProjectionTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.f=Fixture(self.temp.name)

    def rejects(self,code):
        with self.assertRaises(A.AnalysisError) as caught:self.f.project()
        self.assertEqual(caught.exception.code,code)

    def test_not_run_preserves_all_seven_planned_rows(self):
        out=self.f.project()
        self.assertEqual(len(out['observations']['rows']),7)
        self.assertTrue(all(r['status']=='not_run' for r in out['observations']['rows']))
        self.assertTrue(all(v['planned']==1 for v in out['analysis']['arms'].values()))
        self.assertEqual(out['analysis']['contrasts'][0]['verdict'],'undetermined')
        self.assertEqual(out['analysis']['native_projection']['verifications'],[])

    def test_timeout_is_not_success_and_costs_are_not_imputed(self):
        self.f.timeout()
        out=self.f.project()
        self.assertTrue(all(r['status']=='timeout' for r in out['observations']['rows']))
        self.assertTrue(all(c['basis']=='missing' and c['value'] is None for c in out['ledger']['entries']))

    def test_all_retry_costs_retained_exactly_once_and_query_allocation_unknown(self):
        self.f.timeout()
        again=copy.deepcopy(self.f.row)
        again['attempt_id']='try2'
        self.f.row['cost_measures']=[self.f.cost('first','0.1')]
        again['cost_measures']=[self.f.cost('second','0.2')]
        self.f.index['rows'].append(again)
        self.f.projection['slots'][0]['attempt_order'].append('try2')
        result=self.f.project()
        costs=result['native_execution_costs']
        self.assertEqual(costs['tables'][0]['complete_observed_total'],'0.3')
        self.assertEqual(len(costs['records']),2)
        self.assertEqual(len(result['analysis']['native_projection']['attempts']),2)
        self.assertEqual(costs['query_cost_allocation'],'unknown_never_divided_or_imputed')
        self.assertTrue(all(r['basis']=='missing' for r in result['ledger']['entries']))

    def test_nonadditive_peaks_never_sum_across_attempts(self):
        self.f.timeout()
        self.f.row['cost_measures']=[self.f.cost('a','9','whole_run_nonadditive',resource='peak_memory_bytes'),
                                   self.f.cost('b','12','whole_run_nonadditive',resource='peak_memory_bytes')]
        table=self.f.project()['native_execution_costs']['tables'][0]
        self.assertIsNone(table['complete_observed_total'])
        self.assertEqual([r['value_decimal'] for r in table['individual_measurements']],['9','12'])

    def test_estimated_zero_never_becomes_observed_free(self):
        self.f.row['cost_measures']=[self.f.cost('estimate','0',basis='estimated')]
        table=self.f.project()['native_execution_costs']['tables'][0]
        self.assertIsNone(table['complete_observed_total'])

    def test_large_and_fine_decimal_sums_are_exact_not_rounded(self):
        self.f.row['cost_measures']=[self.f.cost('large','1'+'0'*50),self.f.cost('one','1')]
        table=self.f.project()['native_execution_costs']['tables'][0]
        self.assertEqual(table['complete_observed_total'],'1'+'0'*49+'1')
        self.f.row['cost_measures']=[self.f.cost('tiny','0.'+'0'*69+'1'),self.f.cost('whole','1')]
        table=self.f.project()['native_execution_costs']['tables'][0]
        self.assertEqual(table['complete_observed_total'],'1.'+'0'*69+'1')

    def test_empty_measurements_are_not_complete_total_cost_coverage(self):
        costs=self.f.project()['native_execution_costs']
        self.assertEqual(costs['tables'],[])
        self.assertEqual(len(costs['missing_declared_native_requirements']),7)
        self.assertIn('not_unmeasured',costs['coverage_scope'])

    def test_gold_clock_cutoff_and_asof_cannot_disagree_with_source_query(self):
        original=copy.deepcopy(self.f.gold_row)
        for change in [dict(clock_domain='unrelated_clock'),dict(cutoff_plan_seq=2),
                       dict(temporal_intent='historical',historical_as_of_ns=0)]:
            with self.subTest(change=change):
                gold=dict(original,**change)
                self.f.projection['gold_artifact']=self.f.write_raw('gold.jsonl',P.G.compact(gold)+b'\n')
                self.rejects('source_gold_query_identity_mismatch')

    def test_materialization_cannot_change_cutoff_with_a_resealed_file(self):
        value=json.loads((self.f.root/'materialization.json').read_bytes())
        value['queries'][0]['native_cutoff_accepted_seq']=99
        ref=self.f.write('materialization.json',value,'native_materialization')
        self.f.plan['jobs'][0]['bindings']['materialization_manifest']=ref
        self.f.row['input_bindings']['materialization_manifest']=ref
        self.rejects('materialization_cutoff_mismatch')

    def test_materialization_booleans_cannot_replace_integer_sequences(self):
        original=json.loads((self.f.root/'materialization.json').read_bytes())
        for name in ('source_cutoff_plan_seq','native_cutoff_accepted_seq','requested_as_of_ns'):
            with self.subTest(name=name):
                value=copy.deepcopy(original)
                value['queries'][0][name]=True
                ref=self.f.write('materialization.json',value,'native_materialization')
                self.f.plan['jobs'][0]['bindings']['materialization_manifest']=ref
                self.f.row['input_bindings']['materialization_manifest']=ref
                self.rejects('invalid_count')

    def test_historical_view_binds_distinct_planned_and_native_sequences(self):
        self.f.historical()
        out=self.f.project()
        self.assertEqual(self.f.source_query['cutoff_plan_seq'],7)
        self.assertEqual(self.f.probe['cutoff_accepted_seq'],1)
        self.assertTrue(all(r['status']=='not_run' for r in out['observations']['rows']))

    def source_view_v2(self):
        self.f.historical()
        job=self.f.plan['jobs'][0]
        mapping=json.loads((self.f.root/'materialization.json').read_bytes())
        mapping['protocol']='exp03-native-materialization-v2'
        at=self.f.source_query['requested_as_of_ns']
        mapping['queries'][0].update(source_query_at_ns=self.f.source_query['at_ns'],native_at_ns=at,
            source_view_rule='observed_fixture_view_with_current_withdrawal_fence')
        job['common']['expected_schedule'][0]['at_ns']=at
        return mapping

    def bind_source_view(self,mapping):
        ref=self.f.write('materialization.json',mapping,'native_materialization')
        self.f.plan['jobs'][0]['bindings']['materialization_manifest']=ref
        self.f.row['input_bindings']['materialization_manifest']=ref

    def test_v2_historical_native_time_is_requested_view_not_query_time(self):
        mapping=self.source_view_v2();self.bind_source_view(mapping)
        result=self.f.project()
        self.assertEqual(len(result['observations']['rows']),7)
        self.assertLess(mapping['queries'][0]['native_at_ns'],self.f.source_query['at_ns'])
        self.assertTrue(all(row['status']=='not_run' for row in result['observations']['rows']))

    def test_v2_cannot_substitute_current_time_or_bool_or_change_view_rule(self):
        original=self.source_view_v2()
        for change,code in [(dict(native_at_ns=self.f.source_query['at_ns']),'materialization_source_view_mismatch'),
                            (dict(source_query_at_ns=0),'materialization_source_view_mismatch'),
                            (dict(source_view_rule='latest_body_substitution'),'materialization_source_view_mismatch'),
                            (dict(native_at_ns=True),'invalid_count')]:
            with self.subTest(change=change):
                value=copy.deepcopy(original);value['queries'][0].update(change)
                self.bind_source_view(value);self.rejects(code)

    def test_synthetic_clock_rename_must_be_explicit(self):
        self.f.historical(clock='source_clock')
        self.f.project()
        value=json.loads((self.f.root/'materialization.json').read_bytes())
        value['queries'][0]['clock_mapping']='same_clock_identity_ns'
        ref=self.f.write('materialization.json',value,'native_materialization')
        self.f.plan['jobs'][0]['bindings']['materialization_manifest']=ref
        self.f.row['input_bindings']['materialization_manifest']=ref
        self.rejects('materialization_clock_mismatch')

    def test_repetitions_cannot_silently_change_named_policy_conditions(self):
        job,row=self.f.second_replication()
        job['bindings']['registry_semantic']['semantic_sha256']='2'*64
        row['input_bindings']=copy.deepcopy(job['bindings'])
        self.rejects('paired_conditions_mismatch')

    def test_frozen_events_and_importance_features_cannot_drift_between_repeats(self):
        for name,role in [('events_file','events'),('features_file','features')]:
            with self.subTest(name=name),tempfile.TemporaryDirectory() as root:
                fixture=Fixture(root)
                job,_=fixture.second_replication()
                job['common'][name]=fixture.write('changed.json',{'different_trace_or_importance':True},role)
                with self.assertRaises(A.AnalysisError) as caught:
                    fixture.project()
                self.assertEqual(caught.exception.code,'paired_conditions_mismatch')

    def test_duplicate_native_cost_measurement_rejected(self):
        self.f.row['cost_measures']=[self.f.cost('a','1')]*2
        self.rejects('source_cost_missing')

    def test_gold_or_scope_mismatch_rejected(self):
        self.f.schedule['queries'][0]['gold_sets']=[[T.ref('other')]]
        self.f.projection['schedule_sha256']=A.digest(self.f.schedule)
        self.rejects('analysis_gold_or_scope_mismatch')

    def test_budget_rule_bound_to_actual_bytes(self):
        (self.f.root/'budget.json').write_bytes(b'{}')
        self.rejects('artifact_hash_mismatch')

    def test_unknown_input_cap_is_not_replaced_by_zero(self):
        self.assertIsNone(self.f.schedule['queries'][0]['budget']['max_input_tokens'])
        out=self.f.project()
        self.assertEqual(out['analysis']['contrasts'][0]['verdict'],'undetermined')

    def test_missing_attempt_and_duplicate_analysis_slot_rejected(self):
        self.f.projection['slots'][0]['attempt_order']=[]
        self.rejects('attempt_schedule_mismatch')

    def test_posthoc_cannot_be_labelled_unchanged_plan(self):
        self.f.analysis['analysis_changed_after_results']=False
        self.f.analysis['plan_sha256']=A.digest({k:v for k,v in self.f.analysis.items() if k!='plan_sha256'})
        self.f.schedule['analysis_plan_sha256']=self.f.analysis['plan_sha256']
        self.f.projection.update(analysis_plan_sha256=self.f.analysis['plan_sha256'],schedule_sha256=A.digest(self.f.schedule))
        self.rejects('posthoc_projection_requires_exploratory_mark')

    def test_executable_is_pinned_and_never_taken_from_recorded_argv(self):
        self.f.projection['native_cli_sha256']='0'*64
        with self.assertRaises(A.AnalysisError) as caught:
            P.NativeVerifier(self.f.native_cli,self.f.projection)
        self.assertEqual(caught.exception.code,'native_executable_mismatch')

    def test_unrun_schedule_does_not_require_or_invoke_a_native_binary(self):
        self.f.native_cli='/not/installed/native-cli'
        result=self.f.project()
        self.assertFalse(result['analysis']['native_projection']['native_cli_invoked'])

    def test_first_completed_result_uses_same_clock_finish_order(self):
        rows=[dict(attempt_id='a',status='completed',execution_clock_domain='c',finished_at_ns=20),
              dict(attempt_id='b',status='completed_with_failures',execution_clock_domain='c',finished_at_ns=10),
              dict(attempt_id='c',status='timeout',execution_clock_domain='c',finished_at_ns=30)]
        self.assertEqual(P._select(rows,['a','b','c'],'first_completed_else_last_attempted')['attempt_id'],'b')
        rows[0]['finished_at_ns']=10
        self.assertEqual(P._select(rows,['a','b','c'],'first_completed_else_last_attempted')['attempt_id'],'a')
        rows[1]['execution_clock_domain']='different'
        with self.assertRaises(A.AnalysisError) as caught:P._select(rows,['a','b','c'],'first_completed_else_last_attempted')
        self.assertEqual(caught.exception.code,'completion_order_unverifiable')

    def test_select_rule_is_explicit_and_not_a_favorable_default(self):
        self.f.projection['selection_rule']=None
        self.rejects('selection_policy_required')
        rows=[dict(attempt_id='a',status='completed_with_failures'),dict(attempt_id='b',status='timeout')]
        self.assertEqual(P._select(rows,['a','b'],'first_completed_else_last_attempted')['attempt_id'],'a')
        self.assertEqual(P._select(rows,['a','b'],'last_attempted')['attempt_id'],'b')


def integration(root,native_cli):
    fixture=Fixture(root,native_cli,native=True)
    before={p.relative_to(root).as_posix():A.sha(p.read_bytes()) for p in Path(root).rglob('*') if p.is_file()}
    out=fixture.project()
    assert len(out['observations']['rows'])==7
    assert all(r['status']=='success' and {ref['id'] for ref in r['final_refs']}=={'a','b'} for r in out['observations']['rows'])
    assert out['analysis']['arms']['B0']['answerable_hit_any']==1
    assert out['analysis']['contrasts'][0]['control_history_macro']==1
    assert out['analysis']['contrasts'][0]['treatment_history_macro']==1
    assert out['analysis']['contrasts'][0]['verdict']=='undetermined' # One history, no invented input-token cap.
    assert out['analysis']['native_projection']['attempts'][0]['native_replay_verified'] is True
    retained={a['relative_path'] for a in out['analysis']['native_projection']['consumed_artifacts'] if a['root']=='input'}
    assert {'probes.json','candidates.json','dynamics-manifest.json'} <= retained

    after={p.relative_to(root).as_posix():A.sha(p.read_bytes()) for p in Path(root).rglob('*') if p.is_file()}
    assert before==after
    # The actual file CLI uses the same formal records and emits four new files.
    files={'analysis-plan':fixture.analysis,'schedule':fixture.schedule,'run-plan':fixture.plan,
           'run-index':fixture.index,'projection-plan':fixture.projection}
    args=[sys.executable,'-B',str(HERE/'project_native.py')]
    for name,value in files.items():
        path=Path(root)/(name+'.json')
        path.write_bytes(B.compact(value))
        args+=['--'+name,str(path)]
    for name,value in [('input-root',fixture.roots['input']),('evidence-root',fixture.roots['evidence']),
                       ('output-root',fixture.roots['output']),('native-cli',native_cli),('out',str(Path(root)/'analysis-output'))]:
        args+=['--'+name,value]
    process=subprocess.run(args,capture_output=True,text=True,timeout=40)
    assert process.returncode==0,(process.stdout,process.stderr)
    report=json.loads(process.stdout)
    for name,sha in report['outputs'].items():assert A.sha((Path(root)/'analysis-output'/name).read_bytes())==sha
    assert str(root) not in (Path(root)/'analysis-output'/'analysis.json').read_text()
    assert 'PRIVATE_QUERY_SENTINEL' not in (Path(root)/'analysis-output'/'analysis.json').read_text()
    fixture.historical()
    historical=fixture.project()
    assert all(r['status']=='success' for r in historical['observations']['rows'])
    assert historical['analysis']['contrasts'][0]['control_history_macro']==1
    assert fixture.source_query['cutoff_plan_seq']==7 and fixture.probe['cutoff_accepted_seq']==2
    assert historical['analysis']['native_projection']['scientific_lock_verified'] is False
    # A real completed native result followed by an actual-index timeout: both
    # explicit policies preserve attempts and costs, and select different quality.
    again=copy.deepcopy(fixture.row)
    again.update(attempt_id='try2',policy_rows=[],native_completion_manifest=None,native_stdout_status=None)
    fixture.timeout(again)
    fixture.row['cost_measures']=[fixture.cost('first_attempt_cost','0.1')]
    again['cost_measures']=[fixture.cost('retry_cost','0.2')]
    fixture.index['rows'].append(again)
    fixture.projection['slots'][0]['attempt_order'].append('try2')
    first=fixture.project()
    assert all(r['status']=='success' for r in first['observations']['rows'])
    assert first['native_execution_costs']['tables'][0]['complete_observed_total']=='0.3'
    fixture.projection['selection_rule']='last_attempted'
    last=fixture.project()
    assert all(r['status']=='timeout' for r in last['observations']['rows'])
    assert len(last['analysis']['native_projection']['attempts'])==2
    assert len(last['analysis']['native_projection']['verifications'])==1
    # Independent provided-grader fixture labels are bound, never inferred from
    # a retrieval success. Missing labels retain checker failures instead of Y0.
    fixture.projection['selection_rule']='first_completed_else_last_attempted'
    fixture.analysis.update(primary_metric='provided_grader_accuracy',grader_sha256='f'*64)
    fixture.analysis['plan_sha256']=A.digest({k:v for k,v in fixture.analysis.items() if k!='plan_sha256'})
    fixture.schedule['analysis_plan_sha256']=fixture.analysis['plan_sha256']
    fixture.projection.update(analysis_plan_sha256=fixture.analysis['plan_sha256'],schedule_sha256=A.digest(fixture.schedule))
    missing_grade=fixture.project()
    assert all(r['status']=='checker_failure' and r['correct'] is None for r in missing_grade['observations']['rows'])
    grades=[]
    for row in first['observations']['rows']:
        grades.append(dict(job_id='job',attempt_id='try1',stream_id=row['stream_id'],query_id=row['query_id'],policy_id=row['policy_id'],
            status='success',correct=row['policy_id']=='B2',fallback=False,grader_sha256='f'*64,final_refs_sha256=A.digest(row['final_refs'])))
    fixture.projection['grader_artifact']=fixture.write('grades.json',dict(protocol='exp03-grader-observations-v1',
        analysis_plan_sha256=fixture.analysis['plan_sha256'],schedule_sha256=A.digest(fixture.schedule),rows=grades))
    graded=fixture.project()
    assert graded['analysis']['contrasts'][0]['control_history_macro']==0
    assert graded['analysis']['contrasts'][0]['treatment_history_macro']==1
    assert graded['analysis']['contrasts'][0]['verdict']=='undetermined'
    print('formal native projection: 7 actual policies, 2 refs, current/historical, planseq7/native2, retry policies, provided/missing grades, exact CLI hashes; no scientific acceptance')


if __name__=='__main__':
    if len(sys.argv)==4 and sys.argv[1]=='--integration':integration(Path(sys.argv[2]),sys.argv[3])
    else:unittest.main()

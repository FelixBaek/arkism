"""Actual selection on original generated validation rows; numbers are synthetic."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
from unittest import mock
import subprocess
import sys
import unittest

HERE=Path(__file__).resolve().parent


def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    result=importlib.util.module_from_spec(spec);spec.loader.exec_module(result)
    return result


R=load('_test_REF_selector',HERE/'ref_selection.py')
T=load('_test_REF_locked_fixture',HERE/'test_locked_analysis.py')
N=load('_test_REF_native_fixture',HERE/'test_native_projection.py')
A=R.A


class REFSelectionTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name)
        self.raw=T.fixture()
        self.set_locked('REF_id_order','Lexicographic policy IDs under the explicit typed rule')
        self.plan=A.parse(self.raw['analysis_plan'])
        self.plan.update(stage='exploratory',primary_metric='provided_grader_accuracy',grader_sha256='f'*64,policies=['B0','B1','B2'])
        self.plan['plan_sha256']=A.digest({k:v for k,v in self.plan.items() if k!='plan_sha256'})
        original=T.B.source(T.B.V._rows,self.raw['queries'],'QueryPlan')
        gold=T.B.source(T.B.V._rows,self.raw['gold'],'GoldRow')
        manifest=json.loads(self.raw['manifest'])
        validation={e['stream_id'] for e in manifest['entries'] if e['split']=='validation'}
        labels={g['query_id']:g for g in gold}
        self.schedule=dict(protocol='exp03-schedule-v1',analysis_plan_sha256=self.plan['plan_sha256'],queries=[])
        for q in original:
            if q['stream_id'] not in validation or q['request_mode']!='current':continue
            g=labels[q['query_id']]
            kind={'primary_answerable':'answerable','clear_negative':'clear_negative','ambiguous':'ambiguous','alternative_answers':'ambiguous'}[g['cohort']]
            self.schedule['queries'].append(dict(stream_id=q['stream_id'],query_id=q['query_id'],cluster_id=q['stream_id'],scope=q['scope'],
                kind=kind,request_mode='current',gold_sets=[g['required_refs']] if kind=='answerable' else [],
                budget=dict(final_k=3,max_input_tokens=100,max_output_tokens=80),input_sha256=A.sha(self.raw['analysis_source'])))
        self.observations=dict(protocol='exp03-observations-v1',schedule_sha256=A.digest(self.schedule),selection_stage='post_packing_final_fence',rows=[])
        for q in self.schedule['queries']:
            for p in self.plan['policies']:
                self.observations['rows'].append(dict(stream_id=q['stream_id'],query_id=q['query_id'],policy_id=p,repetition=0,
                    input_sha256=q['input_sha256'],gold_sha256=A.digest(q['gold_sets']),budget_sha256=A.digest(q['budget']),
                    final_refs=[],status='success',correct=True,fallback=False,grader_sha256=self.plan['grader_sha256']))
        self.rule=dict(protocol='exp01-ref-selection-rule-v2',protocol_lock_raw_sha256=A.sha(self.raw['lock']),
            manifest_raw_sha256=A.sha(self.raw['manifest']),validation_analysis_plan_sha256=self.plan['plan_sha256'],
            eligible_policies=['B0','B1'],request_modes=['current'],tie_tolerance='0.01',token_cost_phases=['reader_generator'],
            id_tie_break='lexicographic_policy_id',id_order=None)
        self.native=N.Fixture(self.root)
        # Typed native-index metadata is a synthetic cost-component fixture.
        # Only the production builder's fresh Go replay can establish real origin.
        self.native.row.update(status='completed',process_exit=0,started_at_ns=1,finished_at_ns=2,execution_clock_domain='c',
            native_completion_manifest=dict(self.native.stub,role='completion_manifest'),policy_rows=[])
        for p in N.B.ENUMS['RequiredPolicyID']:
            self.native.row['policy_rows'].append(dict(policy_id=p,policy_version='v',prediction_file=dict(self.native.stub,role='prediction'),
                planned_rows=6,recorded_rows=6,successful_rows=6,failed_rows=0,missing_rows=0,fallback_rows=0))
        self.set_costs(10,12)

    def set_locked(self,key,value):
        values=json.loads(self.raw['values']);values['values'][key]['value']=value
        self.raw['values']=T.T.raw(values)
        self.raw['lock']=T.B.L.lock_bytes(T.B.L.build_lock(self.raw['values'],self.raw['manifest']))

    def set_costs(self,b0,b1,basis='observed'):
        self.native.row['cost_measures']=[]
        for p,total in [('B0',b0),('B1',b1)]:
            for resource,value in [('input_tokens',str(total)),('output_tokens','0')]:
                c=self.native.cost(p.lower()+'_'+resource,value,basis=basis,resource=resource)
                c.update(policy_id=p,phase='reader_generator')
                self.native.row['cost_measures'].append(c)

    def select(self):
        self.native.seal()
        costs=N.P.native_costs(self.native.index,self.native.projection,N.P.Sources(self.native.roots),self.plan)
        return R.select(self.rule,self.raw,self.plan,self.schedule,self.observations,self.native.index,costs,self.native.index_raw)

    def quality(self,p,correct_count):
        eligible={q['query_id'] for q in self.schedule['queries'] if A._eligible(q,self.plan)}
        for i,row in enumerate(r for r in self.observations['rows'] if r['policy_id']==p and r['query_id'] in eligible):
            row['correct']=i<correct_count

    def error(self,code):
        with self.assertRaises(R.A.AnalysisError) as caught:self.select()
        self.assertEqual(caught.exception.code,code)

    def test_quality_first_and_B2_never_selected(self):
        self.quality('B0',2);self.quality('B1',3)
        result=self.select()
        self.assertEqual(result['scores'],{'B0':'1/2','B1':'3/4'})
        self.assertEqual(result['selected_policy'],'B1')
        self.assertEqual(result['history_counts'],{'B0':1,'B1':1})
        self.assertFalse(result['execution_authorized'])

    def test_tied_quality_uses_full_observed_token_cost(self):
        self.assertEqual(self.select()['selected_policy'],'B0')
        self.set_costs(15,12)
        self.assertEqual(self.select()['selected_policy'],'B1')

    def test_exact_tolerance_boundary_is_inclusive(self):
        self.set_locked('REF_tie_tolerance','0.25')
        self.rule.update(protocol_lock_raw_sha256=A.sha(self.raw['lock']),tie_tolerance='0.25')
        self.quality('B0',2);self.quality('B1',3)
        self.assertEqual(self.select()['selected_policy'],'B0')
        self.set_locked('REF_tie_tolerance','0.249999999999')
        self.rule.update(protocol_lock_raw_sha256=A.sha(self.raw['lock']),tie_tolerance='0.249999999999')
        self.assertEqual(self.select()['selected_policy'],'B1')

    def test_unknown_or_estimated_tie_cost_is_not_free(self):
        self.native.row['cost_measures'].pop()
        result=self.select()
        self.assertIsNone(result['selected_policy'])
        self.assertIn('tie_token_cost_unknown',result['reasons'])
        self.set_costs(10,12,basis='estimated')
        self.assertEqual(self.select()['status'],'undetermined')

    def test_unique_quality_winner_retains_unknown_cost_without_fake_cost_win(self):
        self.quality('B0',2);self.quality('B1',3)
        self.native.row['cost_measures']=[]
        result=self.select()
        self.assertEqual(result['selected_policy'],'B1')
        self.assertEqual(result['observed_token_costs'],{'B0':None,'B1':None})

    def test_failed_retry_cost_is_included(self):
        retry=copy.deepcopy(self.native.row);retry['attempt_id']='try2';self.native.timeout(retry)
        retry['cost_measures']=[]
        for p,total in [('B0','100'),('B1','0')]:
            for resource,value in [('input_tokens',total),('output_tokens','0')]:
                c=self.native.cost('retry_'+p.lower()+'_'+resource,value,resource=resource)
                c.update(policy_id=p,phase='reader_generator');retry['cost_measures'].append(c)
        self.native.index['rows'].append(retry)
        result=self.select()
        self.assertEqual(result['observed_token_costs'],{'B0':'110','B1':'12'})
        self.assertEqual(result['selected_policy'],'B1')

    def test_exact_large_native_costs_keep_last_digit(self):
        self.set_costs('1'+'0'*50,'1'+'0'*49+'1')
        self.assertEqual(self.select()['selected_policy'],'B0')
        self.assertEqual(self.select()['observed_token_costs']['B1'],'1'+'0'*49+'1')

    def test_last_id_tie_break_is_explicit(self):
        self.set_costs(12,12)
        self.assertEqual(self.select()['selected_policy'],'B0')
        self.set_locked('REF_id_order',['B1','B0'])
        self.rule.update(protocol_lock_raw_sha256=A.sha(self.raw['lock']),id_tie_break='listed_order',id_order=['B1','B0'])
        self.assertEqual(self.select()['selected_policy'],'B1')

    def test_treatment_or_unknown_candidate_rejected(self):
        for candidates in (['B0','B2'],['B0','UNKNOWN']):
            self.rule['eligible_policies']=candidates
            self.error('REF_invalid_candidates')

    def test_final_test_rows_cannot_be_selection_data(self):
        self.schedule=A.parse(self.raw['schedule'])
        self.schedule['analysis_plan_sha256']=self.plan['plan_sha256']
        self.error('REF_validation_split_mismatch')

    def test_dropped_validation_query_is_not_silently_ignored(self):
        self.schedule['queries'].pop()
        self.error('REF_validation_query_coverage_mismatch')

    def test_missing_judgment_is_unknown_not_zero(self):
        row=self.observations['rows'][0];row.update(status='checker_failure',correct=None)
        result=self.select()
        self.assertEqual(result['status'],'undetermined')
        self.assertEqual(result['missing_by_policy']['B0'],1)
        self.assertIn('validation_missingness_limit',result['reasons'])

    def test_not_run_and_absent_rows_preserve_denominator(self):
        self.observations['rows'].pop(0)
        result=self.select()
        self.assertEqual(result['eligible_repetitions'],4)
        self.assertEqual(result['absent_or_unexecuted_repetitions'],1)
        self.assertEqual(result['status'],'undetermined')

    def test_forged_native_cost_record_cannot_replace_index_measurement(self):
        self.native.seal()
        costs=N.P.native_costs(self.native.index,self.native.projection,N.P.Sources(self.native.roots),self.plan)
        costs['records'][0]['value_decimal']='0'
        with self.assertRaises(R.A.AnalysisError) as caught:
            R.select(self.rule,self.raw,self.plan,self.schedule,self.observations,self.native.index,costs,self.native.index_raw)
        self.assertEqual(caught.exception.code,'REF_cost_record_mismatch')

    def test_validation_total_token_limit_matches_locked_final_budget(self):
        self.set_locked('token_budget',dict(max_output_tokens=80,max_total_tokens=180))
        self.rule['protocol_lock_raw_sha256']=A.sha(self.raw['lock'])
        self.assertEqual(self.select()['status'],'selected')
        self.schedule['queries'][0]['budget']['max_input_tokens']=101
        self.error('REF_validation_budget_mismatch')

    def test_all_recorded_token_phases_must_be_in_tie_accounting(self):
        for p,value in [('B0','1000'),('B1','0')]:
            for resource,amount in [('input_tokens',value),('output_tokens','0')]:
                cost=self.native.cost('other_'+p.lower()+'_'+resource,amount,resource=resource)
                cost.update(policy_id=p,phase='retrieval');self.native.row['cost_measures'].append(cost)
        self.error('REF_token_phase_coverage_mismatch')
        self.rule['token_cost_phases'].append('retrieval')
        result=self.select()
        self.assertEqual(result['observed_token_costs'],{'B0':'1010','B1':'12'})
        self.assertEqual(result['selected_policy'],'B1')

    def test_treatment_only_cost_is_preserved_but_not_assigned_to_comparators(self):
        cost=self.native.cost('treatment_tokens','999',resource='input_tokens')
        cost['phase']='importance';self.native.row['cost_measures'].append(cost)
        result=self.select()
        self.assertEqual(result['observed_token_costs'],{'B0':'10','B1':'12'})
        self.assertEqual(result['selected_policy'],'B0')

    def test_shared_token_cost_allocation_remains_unknown(self):
        cost=self.native.cost('shared_tokens','100',allocation='shared_preprocessing',resource='input_tokens')
        cost['phase']='reader_generator';self.native.row['cost_measures'].append(cost)
        result=self.select()
        self.assertEqual(result['observed_token_costs'],{'B0':None,'B1':None})
        self.assertIn('tie_token_cost_unknown',result['reasons'])

    def test_locked_explicit_id_order_cannot_be_replaced_with_lexical(self):
        self.set_locked('REF_id_order',['B1','B0'])
        self.rule['protocol_lock_raw_sha256']=A.sha(self.raw['lock'])
        self.error('REF_locked_id_order_mismatch')

    def test_REF_alias_reuses_actual_selected_prediction_and_grade(self):
        P=N.P
        query=self.schedule['queries'][0]
        refs=query['gold_sets'][0]
        selected=dict(job_id='job',attempt_id='try1',status='completed')
        prediction=dict(status='ok',decision=dict(selected=refs))
        predictions={(query['stream_id'],query['query_id'],'B0'):prediction}
        grade=dict(status='success',correct=False,fallback=True,final_refs_sha256=A.digest(refs))
        graded={('job','try1',query['stream_id'],query['query_id'],'B0'):grade}
        baseline=P.observation(query,'B0',0,selected,prediction,self.plan,graded)
        alias=P.observation_with_REF_alias(query,'REF',0,selected,predictions,self.plan,graded,'B0',['B0','B2'])
        self.assertEqual(alias,dict(baseline,policy_id='REF'))
        self.assertFalse(alias['correct'])
        self.assertTrue(alias['fallback'])
        self.assertEqual(alias['final_refs'],refs)

    def test_nested_verifier_deadline_cannot_extend_either_budget(self):
        with mock.patch.object(N.P.time,'monotonic',return_value=10):
            verifier=N.P.NativeVerifier(self.native.native_cli,self.native.projection,50)
            self.addCleanup(verifier.close)
            self.assertEqual(verifier.deadline,40)
            verifier2=N.P.NativeVerifier(self.native.native_cli,self.native.projection,15)
            self.addCleanup(verifier2.close)
            self.assertEqual(verifier2.deadline,15)

    def test_actual_selection_cli_rejects_bad_request_before_native_execution(self):
        request=self.root/'selection-request.json';request.write_text('{}')
        out=self.root/'output'
        proc=subprocess.run([sys.executable,'-B',str(HERE/'ref_selection.py'),'--request',str(request),
            '--input-root',str(self.root),'--evidence-root',str(self.root),'--output-root',str(self.root),
            '--native-cli',self.native.native_cli,'--out',str(out)],capture_output=True,text=True,timeout=30)
        self.assertEqual(proc.returncode,2)
        self.assertEqual(json.loads(proc.stdout)['code'],'invalid_fields')
        self.assertEqual(proc.stderr,'')
        self.assertFalse(out.exists())

    def test_parse_bounds_extended_exact_totals_and_duplicate_keys(self):
        value='1'+'0'*4096
        self.assertEqual(R.parse(A.canonical(dict(total=value)))['total'],value)
        for raw in (b'{"x":1,"x":2}',b'{"x":NaN}',A.canonical(dict(total='x'*4097))):
            with self.assertRaises(R.A.AnalysisError):R.parse(raw)


if __name__=='__main__':unittest.main()

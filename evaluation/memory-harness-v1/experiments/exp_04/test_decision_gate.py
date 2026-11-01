"""Actual artifact/CLI evidence indexing; synthetic examples are not results."""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location('_exp04_evidence_tests', HERE / 'build_evidence_index.py')
B = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(B)


def template():
    return json.loads((HERE / 'decision_template.json').read_bytes())


class EvidenceIndexTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.package = template()

    def artifact(self, name, role, value, review=False):
        raw = B.canonical(value)+b'\n'
        path = self.root / (name+'.json')
        path.write_bytes(raw)
        a = dict(id=name, role=role, experiment_id='synthetic_fixture', source_commit='1'*40,
                 relative_path=path.name, raw_sha256=hashlib.sha256(raw).hexdigest(), size_bytes=len(raw),
                 review=dict(status='not_reviewed', reviewer_ref=None, reviewed_at=None, evidence_artifact_id=None))
        if review:
            a['review'] = dict(status='accepted', reviewer_ref='fixture_reviewer', reviewed_at='2026-11-01',
                               evidence_artifact_id='review')
        self.package['artifacts'].append(a)
        return a

    def populated(self, reviewed=False):
        self.package['stage'] = 'synthetic'
        if reviewed:
            self.artifact('review', 'review_record', {'synthetic_review_fixture': True})
        plan = dict(protocol='exp03-analysis-plan-v1', stage='synthetic', delta_min='0.05')
        plan['plan_sha256'] = B.digest(B.canonical(plan))
        costs = [dict(policy_id=p, phase='retrieval', resource='calls', allocation='policy_incremental', unit='count',
                      aggregation='sum_of_measurements', observed_subtotal='4', estimated_subtotal='0',
                      missing_measurements=0, measurements=4, complete_observed_total='4') for p in ('B0', 'B2')]
        analysis = dict(protocol='exp03-paired-analysis-v1', experiment_family='p1', population='synthetic', stage='synthetic',
                        primary_metric='recall_at_k', analysis_plan_sha256=plan['plan_sha256'],
                        contrasts=[dict(id='primary', treatment='B2', control='B0', difference=0.1, bootstrap_ci=[-0.1, 0.3],
                        verdict='practical_gain_not_supported_not_equivalence', inference_status='computable')],
                        arms={p: dict(planned=4, recorded=4, status_counts=dict(success=3, timeout=1),
                                      strata={'current:answerable':4}, fallbacks=1) for p in ('B0', 'B2')},
                        costs=costs, cost_table_sha256=B.digest(B.canonical(costs)),
                        analysis_changes=dict(after_results=False, interim_looks=0, tuning_trials=0),
                        multiple_comparisons=dict(primary='unadjusted', secondary='Holm', secondary_family_size=0,
                                                  family_complete=True))
        self.analysis = analysis
        self.artifact('plan', 'analysis_plan', plan, reviewed)
        self.artifact('analysis', 'analysis', analysis, reviewed)
        for name, role in [('runs','run_index'), ('raw','raw_result'), ('cost','cost_ledger'),
                           ('base','baseline'), ('ablation','ablation'), ('failure','failure_trace')]:
            self.artifact(name, role, {'synthetic_fixture':name}, reviewed)
        self.package['comparisons'] = [dict(id='comparison', treatment='B2', control='B0', state='completed',
            analysis_artifact_id='analysis', analysis_plan_artifact_id='plan', contrast_id='primary',
            run_artifact_ids=['runs'], raw_result_artifact_ids=['raw'], cost_artifact_ids=['cost'],
            baseline_artifact_ids=['base'], ablation_artifact_ids=['ablation'], failure_artifact_ids=['failure'])]
        self.package['claims'] = [dict(id='claim', comparison_id='comparison', kind='retrieval_quality',
                                      contribution_id='retrieval', paper_id='P1')]

    def save_analysis(self):
        raw = B.canonical(self.analysis)+b'\n'
        (self.root/'analysis.json').write_bytes(raw)
        a = next(a for a in self.package['artifacts'] if a['id'] == 'analysis')
        a.update(raw_sha256=B.digest(raw), size_bytes=len(raw))

    def build(self):
        return B.build(self.package, str(self.root))

    def rejects(self, code):
        with self.assertRaises(B.EvidenceError) as caught:
            self.build()
        self.assertEqual(caught.exception.code, code)

    def native_cost_fixture(self, values=('0.1', '0.2'), resource='synthetic_units', allocation='shared_preprocessing', total='0.3'):
        self.populated()
        source = self.artifact('measurement', 'raw_result', {'PRIVATE_COST_BODY': True})
        ref = dict(role='cost_source',relative_path='measurement.json',raw_sha256=source['raw_sha256'],size_bytes=source['size_bytes'])
        stub = dict(ref,role='source')
        bindings = {k:stub for k in ('matrix','controls','exp01_draft','REF_contract','operating_plan','readiness','registry_raw',
            'native_config_raw','materialization_manifest','native_input_manifest','native_validation_evidence','DYN05_acceptance','gold_review_evidence')}
        bindings.update(registry_semantic=dict(type_name='PolicyRegistryV2',semantic_sha256='1'*64),
                        native_config_semantic=dict(type_name='DynamicsRunConfigV2',semantic_sha256='1'*64),
                        evaluator_adapter_acceptance=None,REF_use=dict(mode='validation_data_collection',
                            selection_status='not_run_pending_selection',selection_record=None))
        rows, records = [], []
        for i,value in enumerate(values):
            attempt = 'try'+str(i)
            measure = dict(measurement_id='m'+str(i),phase='retry',resource=resource,allocation=allocation,
                           policy_id=None,value_decimal=value,unit='byte' if resource=='peak_memory_bytes' else 'fixture_units_v0',
                           basis='observed',missing_reason=None,source_artifact=ref,price_artifact=None)
            rows.append(dict(job_id='job',attempt_id=attempt,plan_raw_sha256='1'*64,execution_dispatch=dict(stub,role='dispatch'),
                input_bindings=bindings,actual_argv=[],source_manifest=stub,started_at_ns=1,finished_at_ns=2,process_exit=1,
                status='timeout',native_completion_manifest=None,native_stdout_status=None,raw_log=None,policy_rows=[],
                cost_measures=[measure],deviations=[dict(code='timeout',evidence=None)],execution_clock_domain='c'))
            records.append(dict((k,v) for k,v in measure.items() if k not in ('source_artifact','price_artifact')))
            records[-1].update(job_id='job',attempt_id=attempt,source_sha256=ref['raw_sha256'],price_source_sha256=None,price_as_of=None)
        index = dict(protocol='exp02-run-index-v1',plan=dict(stub,role='plan'),rows=rows,analysis_status='not_scored_by_EXP02',
                     evaluation_adapter=dict(owner_task='EXP-03',role='v2_evaluation_adapter',binding=None,
                                             expected_owner_schema=None,owner_acceptance=None))
        self.write_artifact('runs', index)
        # Expected totals below are independent hand values, not the consumer's
        # summation helper. All attempts failed; their charges still count.
        nonadditive = resource=='peak_memory_bytes' or allocation=='whole_run_nonadditive'
        table = dict(policy_id=None,phase='retry',resource=resource,allocation=allocation,unit=records[0]['unit'],
            granularity='native_execution_job_not_allocated_to_queries',measurements=2,observed_subtotal=None if nonadditive else total,
            estimated_subtotal=None if nonadditive else '0',missing_measurements=0,complete_observed_total=None if nonadditive else total,
            price_dates_complete=True)
        if nonadditive:
            table['individual_measurements'] = records
        declared = dict(protocol='exp03-native-execution-costs-v1',run_index_sha256=B.digest((self.root/'runs.json').read_bytes()),
                        records=records,tables=[table],query_cost_allocation='unknown_never_divided_or_imputed',
                        coverage_scope='recorded_measurements_only_not_unmeasured_resource_or_billing_assurance',
                        missing_declared_native_requirements=[])
        self.analysis['native_execution_costs'] = declared
        plan = json.loads((self.root/'plan.json').read_bytes())
        plan.pop('plan_sha256')
        plan['cost_requirements'] = [dict(phase='retry',resource=resource,allocation=allocation)]
        plan['plan_sha256'] = B.digest(B.canonical(plan))
        self.analysis['analysis_plan_sha256'] = plan['plan_sha256']
        self.write_artifact('plan',plan)
        self.save_native_costs()
        return declared,index

    def write_artifact(self, name, value):
        raw = B.canonical(value)+b'\n'
        (self.root/(name+'.json')).write_bytes(raw)
        a = next(a for a in self.package['artifacts'] if a['id']==name)
        a.update(raw_sha256=B.digest(raw),size_bytes=len(raw))

    def save_native_costs(self):
        self.save_analysis()
        self.write_artifact('cost',self.analysis['native_execution_costs'])

    def test_native_failed_attempt_costs_reconciled_without_query_allocation(self):
        self.native_cost_fixture()
        row = self.build()['comparisons'][0]
        self.assertEqual(row['native_execution_costs']['tables'][0]['observed_subtotal'],'0.3')
        self.assertEqual(len(row['native_execution_costs']['records']),2)
        self.assertIn('native_query_cost_allocation_unknown',row['missing'])
        self.assertIn('native_run_analysis_reconciliation_required',row['missing'])
        self.assertNotIn('PRIVATE_COST_BODY',json.dumps(row))

    def test_native_forged_totals_rejected_even_when_ledger_agrees(self):
        declared,_ = self.native_cost_fixture()
        declared['tables'][0]['observed_subtotal']='0'
        declared['tables'][0]['complete_observed_total']='0'
        self.save_native_costs()
        self.rejects('native_cost_reconciliation_mismatch')

    def test_native_failed_retry_omitted_from_report_rejected(self):
        declared,_ = self.native_cost_fixture()
        declared['records'].pop()
        self.save_native_costs()
        self.rejects('native_cost_reconciliation_mismatch')

    def test_native_measurement_source_required_and_hash_bound(self):
        self.native_cost_fixture()
        self.package['artifacts']=[a for a in self.package['artifacts'] if a['id']!='measurement']
        self.rejects('native_cost_source_missing')

    def test_native_peak_costs_not_added(self):
        declared,_ = self.native_cost_fixture(resource='peak_memory_bytes')
        row = self.build()['comparisons'][0]
        self.assertIsNone(row['native_execution_costs']['tables'][0]['observed_subtotal'])
        self.assertEqual(len(row['native_execution_costs']['tables'][0]['individual_measurements']),2)
        self.assertIn('native_cost_not_fully_observed',row['missing'])

    def test_native_ledger_and_index_are_required(self):
        self.native_cost_fixture()
        self.write_artifact('cost',{'unrelated':True})
        self.rejects('native_cost_ledger_binding')
        self.save_native_costs()
        self.analysis['native_execution_costs']['run_index_sha256']='f'*64
        self.save_native_costs()
        self.rejects('native_cost_index_binding')

    def test_native_unreported_required_measurement_cannot_disappear(self):
        declared,_ = self.native_cost_fixture()
        plan=json.loads((self.root/'plan.json').read_bytes())
        plan.pop('plan_sha256')
        plan['cost_requirements'].append(dict(phase='whole_run',resource='calls',allocation='shared_preprocessing'))
        plan['plan_sha256']=B.digest(B.canonical(plan))
        self.write_artifact('plan',plan)
        self.analysis['analysis_plan_sha256']=plan['plan_sha256']
        self.save_native_costs()
        self.rejects('native_cost_reconciliation_mismatch')
        declared['missing_declared_native_requirements']=[dict(job_id='job',attempt_id='try'+str(i),policy_id=None,
            phase='whole_run',resource='calls',allocation='shared_preprocessing') for i in range(2)]
        self.save_native_costs()
        self.assertIn('native_cost_not_fully_observed',self.build()['comparisons'][0]['missing'])

    def test_native_aggregate_preserves_more_than_fifty_digits(self):
        expected='1'+'0'*49+'1'
        self.native_cost_fixture(values=('1'+'0'*50,'1'),total=expected)
        self.assertEqual(self.build()['comparisons'][0]['native_execution_costs']['tables'][0]['observed_subtotal'],expected)

    def test_native_consumer_actual_cli_keeps_failed_attempt_charges(self):
        self.native_cost_fixture()
        package_path=self.root/'package.json'
        package_path.write_bytes(B.canonical(self.package))
        out=self.root/'out'
        proc=subprocess.run([sys.executable,'-B',str(HERE/'build_evidence_index.py'),'--package',str(package_path),
            '--artifact-root',str(self.root),'--out',str(out)],capture_output=True,text=True,timeout=30)
        self.assertEqual(proc.returncode,0,proc.stdout+proc.stderr)
        status=json.loads(proc.stdout)
        result=json.loads((out/'evidence_index.json').read_bytes())
        row=result['comparisons'][0]
        self.assertEqual(row['native_execution_costs']['tables'][0]['observed_subtotal'],'0.3')
        self.assertIn('native_query_cost_allocation_unknown',row['missing'])
        self.assertIsNone(result['product']['owner_decision'])
        for filename,sha in status['outputs'].items():
            data=(out/filename).read_bytes()
            self.assertEqual(B.digest(data),sha)
            self.assertNotIn(b'PRIVATE_COST_BODY',data)
            self.assertNotIn(str(self.root).encode(),data)

    def test_exact_cost_sums_and_analysis_totals_keep_large_scale(self):
        self.assertEqual(B.exact_sum(['1'+'0'*50,'1']),'1'+'0'*49+'1')
        self.assertEqual(B.exact_sum(['1','0.'+'0'*69+'1']),'1.'+'0'*69+'1')
        self.populated()
        for cost in self.analysis['costs']:
            cost.update(observed_subtotal='1.'+'0'*69+'1',complete_observed_total='1.'+'0'*69+'1')
        self.analysis['cost_table_sha256']=B.digest(B.canonical(self.analysis['costs']))
        self.save_analysis()
        self.assertEqual(self.build()['comparisons'][0]['cost_totals'][0]['observed_subtotal'],'1.'+'0'*69+'1')

    def test_native_context_requires_complete_explicit_cli_arguments(self):
        package_path=self.root/'package.json';package_path.write_bytes(B.canonical(self.package))
        proc=subprocess.run([sys.executable,'-B',str(HERE/'build_evidence_index.py'),'--package',str(package_path),
            '--artifact-root',str(self.root),'--out',str(self.root/'out'),'--native-cli','/unused'],
            capture_output=True,text=True,timeout=30)
        self.assertEqual(proc.returncode,2)
        self.assertEqual(json.loads(proc.stdout)['code'],'native_replay_arguments_required')
        self.assertFalse((self.root/'out').exists())

    def test_native_context_unknown_comparison_rejected_before_execution(self):
        context=dict(protocol='exp04-native-replays-v1',comparisons=[dict(comparison_id='absent',
            schedule_artifact_id='s',projection_plan_artifact_id='p',run_plan_artifact_id='rp',run_index_artifact_id='ri',
            observations_artifact_id='o',query_cost_ledger_artifact_id='q',native_cost_ledger_artifact_id='n',native_report_artifact_ids=[])])
        with mock.patch.object(subprocess,'Popen',side_effect=AssertionError('must not execute')):
            with self.assertRaises(B.EvidenceError) as caught:
                B.build(self.package,str(self.root),context,dict(input=str(self.root),evidence=str(self.root),output=str(self.root)),'/unused')
        self.assertEqual(caught.exception.code,'native_comparison_identity')

    def test_native_aggregate_decimal_width_growth_is_bounded(self):
        text='1'+'0'*4096
        self.assertEqual(B.parse(B.canonical(dict(total=text)))['total'],text)
        for value in ('x'*4097,'1'*4103):
            with self.assertRaises(B.EvidenceError):B.parse(B.canonical(dict(total=value)))

    def test_no_results_produces_missing_gates_and_owner_template(self):
        result = self.build()
        self.assertEqual(result['product']['status'], 'insufficient_evidence')
        self.assertEqual(result['research']['status'], 'insufficient_evidence')
        self.assertEqual(result['comparisons'], [])
        self.assertTrue(set(B.BOUNDARIES) <= set(result['product']['missing_gates']))
        self.assertIsNone(result['product']['owner_decision'])
        self.assertFalse(result['full_card_acceptance'])
        self.assertFalse(result['deployment_authorized'])
        self.assertFalse(result['paper_promotion_authorized'])

    def test_actual_bytes_match_and_failed_rows_stay_visible(self):
        self.populated()
        result = self.build()
        row = result['comparisons'][0]
        self.assertEqual(row['statistic'], dict(difference=0.1, bootstrap_ci=[-0.1, 0.3]))
        self.assertEqual(row['arms']['B2']['status_counts'], dict(success=3, timeout=1))
        self.assertEqual(row['arms']['B2']['fallbacks'], 1)
        self.assertEqual(row['cost_totals'][0]['complete_observed_total'], '4')
        self.assertIn('native_run_analysis_reconciliation_required', row['missing'])
        self.assertEqual(result['claims'][0]['status'], 'insufficient_evidence')
        self.assertNotIn('equivalence_interval_contained', json.dumps(result))

    def test_review_declarations_never_approve_adoption(self):
        self.populated(reviewed=True)
        result = self.build()
        self.assertEqual(result['comparisons'][0]['review_status'], 'declared_accepted_not_authenticated')
        self.assertIsNone(result['product']['owner_decision'])
        self.assertIn('native_run_analysis_reconciliation_required', result['claims'][0]['limitation_codes'])

    def test_corrupt_raw_hash_rejected(self):
        self.populated()
        (self.root/'raw.json').write_bytes(b'corrupted')
        self.rejects('artifact_hash_mismatch')

    def test_size_and_same_json_different_bytes_not_interchangeable(self):
        self.populated()
        p = self.root/'raw.json'
        p.write_bytes(json.dumps(json.loads(p.read_bytes()), indent=2).encode())
        self.rejects('artifact_hash_mismatch')

    def test_owner_decision_cannot_be_filled_by_worker(self):
        self.package['owner_decision'] = 'adopt'
        self.rejects('worker_cannot_set_owner_decision')

    def test_boundary_failure_blocks_even_with_positive_quality(self):
        self.populated()
        self.analysis['contrasts'][0].update(difference=0.2, bootstrap_ci=[0.1, 0.3], verdict='practical_improvement_supported')
        self.save_analysis()
        self.artifact('boundary', 'boundary', {'synthetic_failure':True})
        self.package['coverage'] = [dict(id='deletion', status='fail', artifact_ids=['boundary'])]
        result = self.build()
        self.assertEqual(result['product']['status'], 'blocked_boundary_failure')
        self.assertEqual(result['product']['failed_boundaries'], ['deletion'])
        self.assertNotIn('consider_bounded_use', result['product']['options'])

    def test_not_run_comparison_never_gets_empty_success(self):
        self.package['comparisons'] = [dict(id='future', treatment='B4', control='B0', state='not_run',
            analysis_artifact_id=None, analysis_plan_artifact_id=None, contrast_id=None, run_artifact_ids=[],
            raw_result_artifact_ids=[], cost_artifact_ids=[], baseline_artifact_ids=[], ablation_artifact_ids=[], failure_artifact_ids=[])]
        result = self.build()
        self.assertEqual(result['comparisons'][0]['state'], 'not_run')
        self.assertIsNone(result['comparisons'][0]['statistic'])
        self.assertEqual(result['comparisons'][0]['missing'], ['not_run'])

    def test_retrieval_claim_cannot_be_promoted_to_task_success(self):
        self.populated()
        self.package['claims'][0]['kind'] = 'task_success'
        self.assertIn('retrieval_is_not_task_success', self.build()['claims'][0]['limitation_codes'])

    def test_scope_or_policy_mismatch_rejected(self):
        self.populated()
        self.package['population'] = 'observed_service'
        self.rejects('analysis_scope_mismatch')
        self.package['population'] = 'synthetic'
        self.package['comparisons'][0]['treatment'] = 'B1'
        self.rejects('contrast_policy_mismatch')

    def test_cost_hash_and_missing_charge_not_free(self):
        self.populated()
        self.analysis['costs'][0]['missing_measurements'] = 1
        self.analysis['costs'][0]['complete_observed_total'] = None
        self.save_analysis()
        self.rejects('cost_table_hash_mismatch')
        self.analysis['cost_table_sha256'] = B.digest(B.canonical(self.analysis['costs']))
        self.save_analysis()
        self.assertIn('cost_not_fully_observed', self.build()['comparisons'][0]['missing'])

    def test_negative_counts_cannot_claim_false_return_without_negative_queries(self):
        self.populated()
        self.analysis['arms']['B2'].update(negative_scheduled=0, negative_successes=0, negative_false_returns=4)
        self.save_analysis()
        self.rejects('invalid_arm_counts')

    def test_negative_false_returns_need_success_and_stratum_binding(self):
        self.populated()
        arm = self.analysis['arms']['B2']
        arm.update(negative_scheduled=2, negative_successes=1, negative_failures=1, negative_false_returns=1,
                   strata={'current:answerable':2, 'as_of:clear_negative':2})
        self.save_analysis()
        self.assertEqual(self.build()['comparisons'][0]['arms']['B2']['negative_false_returns'], 1)
        arm['negative_false_returns'] = 2
        self.save_analysis()
        self.rejects('invalid_arm_counts')
        arm['negative_false_returns'] = 1
        arm['negative_failures'] = 0
        self.save_analysis()
        self.rejects('invalid_arm_counts')

    def test_raw_body_injected_into_arm_summary_rejected(self):
        self.populated()
        self.analysis['arms']['B2']['prompt'] = 'PRIVATE_SENTINEL'
        self.save_analysis()
        self.rejects('invalid_arms')

    def test_success_without_recorded_execution_is_rejected(self):
        self.populated()
        self.analysis['arms']['B2']['recorded'] = 0
        self.save_analysis()
        self.rejects('invalid_arm_counts')

    def test_explicit_missing_can_be_recorded_or_absent(self):
        self.populated()
        self.analysis['arms']['B2'].update(recorded=3, status_counts=dict(success=3, missing=1))
        self.save_analysis()
        self.assertEqual(self.build()['comparisons'][0]['arms']['B2']['recorded'], 3)
        self.analysis['arms']['B2']['recorded'] = 4
        self.save_analysis()
        self.assertEqual(self.build()['comparisons'][0]['arms']['B2']['recorded'], 4)

    def test_outside_body_not_copied_to_result(self):
        self.populated()
        self.analysis['unused_source_body'] = 'PRIVATE_SENTINEL'
        self.save_analysis()
        self.assertNotIn('PRIVATE_SENTINEL', json.dumps(self.build()))

    def test_duplicate_contribution_across_papers_is_flagged(self):
        self.populated()
        again = copy.deepcopy(self.package['claims'][0])
        again.update(id='claim2', paper_id='P2')
        self.package['claims'].append(again)
        result = self.build()
        self.assertTrue(all('duplicate_contribution_across_papers' in c['limitation_codes'] for c in result['claims']))

    def test_previously_used_contribution_is_also_flagged(self):
        self.populated()
        a = next(a for a in self.package['artifacts'] if a['id']=='analysis')
        self.package['reuse_registry'] = [dict(source_sha256=a['raw_sha256'], contribution_id='retrieval', paper_id='P3')]
        self.assertIn('duplicate_contribution_across_papers', self.build()['claims'][0]['limitation_codes'])

    def test_review_cannot_point_to_self_or_wrong_role(self):
        self.populated(reviewed=True)
        self.package['artifacts'][1]['review']['evidence_artifact_id'] = 'plan'
        self.rejects('review_evidence_missing')

    def test_wrong_evidence_role_cannot_satisfy_system_gate(self):
        self.populated()
        self.package['coverage'] = [dict(id='SYS06', status='pass', artifact_ids=['raw'])]
        self.rejects('evidence_role_mismatch')

    def test_next_question_requires_new_data_and_never_starts_project(self):
        self.package['next_questions'] = [dict(id='P3-candidate-coverage', bottleneck_artifact_ids=[], required_data_codes=['candidate_omission_trace'])]
        q = self.build()['next_questions'][0]
        self.assertEqual(q['bottleneck_evidence_status'], 'absent')
        self.assertEqual(q['status'], 'question_only_not_authorized_to_start')

    def test_unreviewed_bottleneck_is_not_exported_as_observed(self):
        self.artifact('bottleneck','bottleneck', {'synthetic_example':True})
        self.package['next_questions'] = [dict(id='P3', bottleneck_artifact_ids=['bottleneck'], required_data_codes=['new_trace'])]
        result = self.build()
        self.assertEqual(result['next_questions'][0]['bottleneck_evidence_status'], 'linked_unreviewed')
        self.assertNotIn('observed bottleneck', B.render(result)['next_questions.md'].decode())

    def test_failed_output_is_rolled_back_without_deleting_existing_directory(self):
        target = self.root/'output'
        original = os.open
        def fail_on_csv(path, *args, **kwargs):
            if path == 'claim_evidence.csv':
                raise PermissionError('synthetic_write_fault')
            return original(path, *args, **kwargs)
        with mock.patch.object(B.os,'open',side_effect=fail_on_csv), self.assertRaises(PermissionError):
            B.write_new(str(target), B.render(self.build()))
        self.assertFalse(target.exists())
        target.mkdir()
        (target/'keep').write_text('keep')
        with self.assertRaises(FileExistsError):
            B.write_new(str(target), B.render(self.build()))
        self.assertEqual((target/'keep').read_text(),'keep')

    def test_artifact_symlink_parent_and_traversal_rejected(self):
        self.populated()
        a = next(a for a in self.package['artifacts'] if a['id']=='raw')
        (self.root/'link').symlink_to(self.root, target_is_directory=True)
        a['relative_path'] = 'link/raw.json'
        with self.assertRaises(OSError):
            self.build()
        a['relative_path'] = '../raw.json'
        self.rejects('unsafe_path')

    def test_json_duplicate_nan_and_infinity_rejected(self):
        for raw in (b'{"x":1,"x":2}', b'{"x":NaN}', b'{"x":1e9999}'):
            with self.subTest(raw=raw), self.assertRaises(B.EvidenceError):
                B.parse(raw)

    def test_cli_generates_four_files_with_exact_hashes_and_no_input_path(self):
        self.populated()
        package_path = self.root/'package.json'
        package_path.write_bytes(B.canonical(self.package))
        out = self.root/'out'
        args = [sys.executable,'-B',str(HERE/'build_evidence_index.py'),'--package',str(package_path),
                '--artifact-root',str(self.root),'--out',str(out)]
        proc = subprocess.run(args, capture_output=True, text=True, timeout=30)
        self.assertEqual(proc.returncode,0,proc.stdout+proc.stderr)
        status = json.loads(proc.stdout)
        self.assertEqual(set(status['outputs']), {'evidence_index.json','product_decision.json','claim_evidence.csv','next_questions.md'})
        for filename, sha in status['outputs'].items():
            data = (out/filename).read_bytes()
            self.assertEqual(B.digest(data), sha)
            self.assertNotIn(str(self.root).encode(), data)
            self.assertEqual(os.stat(out/filename).st_mode & 0o777, 0o600)
        self.assertIn('source_commit', (out/'claim_evidence.csv').read_text())
        self.assertIsNone(json.loads((out/'product_decision.json').read_bytes())['product']['owner_decision'])
        again = subprocess.run(args,capture_output=True,text=True,timeout=30)
        self.assertEqual(again.returncode,2)

    def test_cli_failure_does_not_emit_output_or_sensitive_path(self):
        package_path = self.root/'package.json'
        self.package['artifacts'] = [dict(id='incomplete')]
        package_path.write_bytes(B.canonical(self.package))
        out = self.root/'out'
        proc = subprocess.run([sys.executable,'-B',str(HERE/'build_evidence_index.py'),'--package',str(package_path),
            '--artifact-root',str(self.root),'--out',str(out)],capture_output=True,text=True,timeout=30)
        self.assertEqual(proc.returncode,2)
        self.assertFalse(out.exists())
        self.assertNotIn(str(self.root),proc.stdout+proc.stderr)


def integration(root, native_cli):
    spec=importlib.util.spec_from_file_location('_exp04_actual_native_fixture',HERE.with_name('exp_03')/'test_native_projection.py')
    producer=importlib.util.module_from_spec(spec)
    spec.loader.exec_module(producer)
    fixture=producer.Fixture(root,native_cli,native=True)
    projected=fixture.project()
    native_raw={B.digest(p.read_bytes()):p.read_bytes() for p in root.rglob('*') if p.is_file()}
    artifact_root=root/'index-inputs'
    artifact_root.mkdir()
    package=template()
    package.update(stage=projected['analysis']['stage'],population=projected['analysis']['population'],
                   experiment_family=projected['analysis']['experiment_family'])
    def add(name, role, raw):
        (artifact_root/(name+'.json')).write_bytes(raw)
        package['artifacts'].append(dict(id=name,role=role,experiment_id='synthetic_native_fixture',source_commit='1'*40,
            relative_path=name+'.json',raw_sha256=B.digest(raw),size_bytes=len(raw),
            review=dict(status='not_reviewed',reviewer_ref=None,reviewed_at=None,evidence_artifact_id=None)))
        return name
    def data(name,role,value):
        return add(name,role,B.canonical(value)+b'\n')
    data('analysis','analysis',projected['analysis'])
    data('plan','analysis_plan',fixture.analysis)
    data('schedule','schedule',fixture.schedule)
    data('projection','projection_plan',fixture.projection)
    add('runplan','run_plan',fixture.plan_raw)
    add('runs','run_index',fixture.index_raw)
    data('observations','observations',projected['observations'])
    data('querycost','cost_ledger',projected['ledger'])
    data('nativecost','cost_ledger',projected['native_execution_costs'])
    report_ids=[add('report'+str(i),'native_verification',raw) for i,raw in enumerate(projected.native_reports.values())]
    for i,a in enumerate(projected['analysis']['native_projection']['consumed_artifacts']):
        add('source'+str(i),'raw_result',(Path(fixture.roots[a['root']])/a['relative_path']).read_bytes())
    # A native verifier may read more files than the Python projector's cache,
    # notably probes/candidates and a P1 base manifest. Include them explicitly.
    known={a['raw_sha256'] for a in package['artifacts']}
    for report_raw in projected.native_reports.values():
        report=json.loads(report_raw)
        for pin in report['inputs']+[report['completion']]+[p['artifact'] for p in report['policies']]:
            if pin['raw_sha256'] not in known:
                add('nativeinput'+str(len(known)),'raw_result',native_raw[pin['raw_sha256']])
                known.add(pin['raw_sha256'])
    package['comparisons']=[dict(id='comparison',treatment='B2',control='B0',state='completed',analysis_artifact_id='analysis',
        analysis_plan_artifact_id='plan',contrast_id=projected['analysis']['contrasts'][0]['id'],run_artifact_ids=['runs'],
        raw_result_artifact_ids=[a['id'] for a in package['artifacts'] if a['role']=='raw_result'],
        cost_artifact_ids=['querycost','nativecost'],baseline_artifact_ids=[],ablation_artifact_ids=[],failure_artifact_ids=[])]
    request=dict(comparison_id='comparison',schedule_artifact_id='schedule',projection_plan_artifact_id='projection',
        run_plan_artifact_id='runplan',run_index_artifact_id='runs',observations_artifact_id='observations',
        query_cost_ledger_artifact_id='querycost',native_cost_ledger_artifact_id='nativecost',native_report_artifact_ids=report_ids)
    context=dict(protocol='exp04-native-replays-v1',comparisons=[request])
    result=B.build(package,str(artifact_root),context,fixture.roots,native_cli)
    row=result['comparisons'][0]
    assert row['native_reconciliation']['status']=='fresh_projection_matched'
    assert 'native_run_analysis_reconciliation_required' not in row['missing']
    assert 'analysis_unresolved' in row['missing'] and 'native_query_cost_allocation_unknown' in row['missing']
    assert result['product']['owner_decision'] is None and result['full_card_acceptance'] is False
    assert row['native_reconciliation']['scientific_lock_verified'] is False
    # A correctly resealed forged analysis still cannot match a fresh live replay.
    altered=copy.deepcopy(projected['analysis'])
    altered['contrasts'][0]['difference']=0.9
    forged=B.canonical(altered)+b'\n'
    (artifact_root/'analysis.json').write_bytes(forged)
    a=next(a for a in package['artifacts'] if a['id']=='analysis')
    a.update(raw_sha256=B.digest(forged),size_bytes=len(forged))
    try:
        B.build(package,str(artifact_root),context,fixture.roots,native_cli)
    except B.EvidenceError as error:
        assert error.code=='native_analysis_replay_mismatch',error.code
    else:
        raise AssertionError('forged result accepted')
    original=B.canonical(projected['analysis'])+b'\n'
    (artifact_root/'analysis.json').write_bytes(original)
    a.update(raw_sha256=B.digest(original),size_bytes=len(original))
    def expect(code):
        try:
            B.build(package,str(artifact_root),context,fixture.roots,native_cli)
        except B.EvidenceError as error:
            assert error.code==code,(error.code,code)
        else:
            raise AssertionError('tampered evidence accepted: '+code)
    observations=copy.deepcopy(projected['observations'])
    observations['rows'][0]['final_refs']=[]
    modified=B.canonical(observations)+b'\n'
    observed=next(a for a in package['artifacts'] if a['id']=='observations')
    saved=dict(observed)
    (artifact_root/'observations.json').write_bytes(modified)
    observed.update(raw_sha256=B.digest(modified),size_bytes=len(modified))
    expect('native_output_replay_mismatch')
    observed.update(saved)
    (artifact_root/'observations.json').write_bytes(B.canonical(projected['observations'])+b'\n')
    saved_ids=list(request['native_report_artifact_ids'])
    request['native_report_artifact_ids']=[]
    expect('native_report_replay_mismatch')
    request['native_report_artifact_ids']=saved_ids
    # Remove the actual native probe source from the package while leaving the
    # source-root file intact. Fresh execution succeeds, source closure must fail.
    probe_hash=fixture.ref('probes.json','probes')['raw_sha256']
    saved_artifacts=list(package['artifacts'])
    package['artifacts']=[a for a in package['artifacts'] if a['raw_sha256']!=probe_hash]
    package['comparisons'][0]['raw_result_artifact_ids']=[a['id'] for a in package['artifacts'] if a['role']=='raw_result']
    expect('native_consumed_source_missing')
    package['artifacts']=saved_artifacts
    package['comparisons'][0]['raw_result_artifact_ids']=[a['id'] for a in saved_artifacts if a['role']=='raw_result']
    actual_report=json.loads(next(iter(projected.native_reports.values())))
    base=next(pin for pin in actual_report['inputs'] if pin['role']=='base_manifest')
    package['artifacts']=[a for a in package['artifacts'] if a['raw_sha256']!=base['raw_sha256']]
    package['comparisons'][0]['raw_result_artifact_ids']=[a['id'] for a in package['artifacts'] if a['role']=='raw_result']
    expect('native_consumed_source_missing')
    package['artifacts']=saved_artifacts
    package['comparisons'][0]['raw_result_artifact_ids']=[a['id'] for a in saved_artifacts if a['role']=='raw_result']
    # Actual EXP04 CLI invokes the same native verifier test executable and emits
    # byte-pinned four-file decisions, with unknown quality/cost retained.
    package_path=root/'evidence-package.json';package_path.write_bytes(B.canonical(package))
    context_path=root/'replays.json';context_path.write_bytes(B.canonical(context))
    out=root/'evidence-index-output'
    args=[sys.executable,'-B',str(HERE/'build_evidence_index.py'),'--package',str(package_path),
          '--artifact-root',str(artifact_root),'--out',str(out),'--native-replays',str(context_path),'--native-cli',native_cli]
    for k,v in fixture.roots.items():args+=['--native-'+k+'-root',v]
    proc=subprocess.run(args,capture_output=True,text=True,timeout=60)
    assert proc.returncode==0,(proc.stdout,proc.stderr)
    status=json.loads(proc.stdout)
    for name,sha in status['outputs'].items():
        raw=(out/name).read_bytes()
        assert B.digest(raw)==sha
        assert b'PRIVATE_QUERY_SENTINEL' not in raw
        assert str(root).encode() not in raw
    indexed=json.loads((out/'evidence_index.json').read_bytes())
    assert indexed['comparisons'][0]['native_reconciliation']['status']=='fresh_projection_matched'
    assert indexed['product']['owner_decision'] is None
    print('actual native replay -> analysis -> EXP04 API/CLI: 7 policies, original refs, forged contrast rejected; unknown quality/cost and human decisions preserved')


if __name__ == '__main__':
    if len(sys.argv)==4 and sys.argv[1]=='--integration':
        integration(Path(sys.argv[2]),sys.argv[3])
    else:
        unittest.main()

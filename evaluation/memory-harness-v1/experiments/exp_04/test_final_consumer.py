"""Real producer/recorder/task/REF/final output consumed by live EXP04 replay."""
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


B=load('_test_final_evidence_consumer',HERE/'build_evidence_index.py')
T=load('_test_actual_final_task_pipeline',HERE.with_name('exp_03')/'test_task_adapter.py')
P=T.T.P


class FinalConsumerTests(unittest.TestCase):
    def test_extra_final_pin_cannot_be_ignored_without_native_context(self):
        with tempfile.TemporaryDirectory() as root:
            package=json.loads((HERE/'decision_template.json').read_bytes())
            with self.assertRaises(B.EvidenceError) as caught:B.build(package,root,trusted_owner_reviews={'comparison':'f'*64})
            self.assertEqual(caught.exception.code,'native_replay_arguments_required')

    def test_native_v1_cannot_silently_accept_final_pins(self):
        with self.assertRaises(B.EvidenceError) as caught:
            B.native_replays(dict(protocol='exp04-native-replays-v1',comparisons=[{}]),{}, {}, {},{},'/unused',{})
        self.assertEqual(caught.exception.code,'unexpected_final_review_pins')

    def test_extra_view_pin_cannot_be_ignored_without_native_context(self):
        with tempfile.TemporaryDirectory() as root:
            package=json.loads((HERE/'decision_template.json').read_bytes())
            with self.assertRaises(B.EvidenceError) as caught:
                B.build(package,root,trusted_attribution_inputs={'comparison':'f'*64})
            self.assertEqual(caught.exception.code,'native_replay_arguments_required')

    def test_legacy_context_cannot_silently_ignore_view_pins(self):
        for protocol in ('exp04-native-replays-v1','exp04-native-replays-v2'):
            with self.subTest(protocol=protocol),self.assertRaises(B.EvidenceError) as caught:
                B.native_replays(dict(protocol=protocol,comparisons=[{}]),{}, {}, {},{},'/unused',None,{})
            self.assertEqual(caught.exception.code,'unexpected_attribution_pins')


def integration(root,native_cli,retries=False,attribution=False):
    case=T.integration(root/'actual',native_cli,retries=retries);f=case['f'];fresh=case['result']
    attribution_raw=None
    if attribution:
        V=load('_actual_native_attribution_fixture',HERE.with_name('exp_03')/'test_native_attribution.py')
        fresh,attribution_raw=V.actual(case,root,native_cli)
    artifact_root=root/'package-artifacts';artifact_root.mkdir()
    package=json.loads((HERE/'decision_template.json').read_bytes())
    package.update(stage='confirmatory',population='synthetic',experiment_family=case['analysis']['experiment_family'])
    package['responsibility']['unresolved_reason']='synthetic_owner_decisions_pending'
    def add(name,role,raw):
        path=name+'.bin';(artifact_root/path).write_bytes(raw)
        package['artifacts'].append(dict(id=name,role=role,experiment_id='synthetic_final_pipeline',source_commit='1'*40,
            relative_path=path,raw_sha256=B.digest(raw),size_bytes=len(raw),
            review=dict(status='not_reviewed',reviewer_ref=None,reviewed_at=None,evidence_artifact_id=None)))
        return name
    def data(name,role,value):return add(name,role,B.canonical(value)+(b'' if attribution else b'\n'))
    data('analysis','analysis',fresh['analysis']);data('analysis-plan','analysis_plan',case['analysis'])
    data('schedule','schedule',case['schedule']);data('projection','projection_plan',case['projection'])
    add('native-plan','run_plan',case['run_plan']);add('index','run_index',case['run_index'])
    data('observations','observations',fresh['observations']);data('query-cost','cost_ledger',fresh['ledger'])
    data('native-cost','cost_ledger',fresh['native_execution_costs']);data('final-inputs','final_inputs',case['final_inputs'])
    reports=[add('report'+str(i),'native_verification',raw) for i,raw in enumerate(fresh.native_reports.values())]
    for i,artifact in enumerate(fresh['analysis']['native_projection']['consumed_artifacts']):
        raw=(Path(f.roots[artifact['root']])/artifact['relative_path']).read_bytes()
        add('source'+str(i),'raw_result',raw)
    code={B.SOURCE_BYTES,P.SOURCE_BYTES,*P.DEPENDENCY_BYTES.values(),*fresh.extension_code.values()}
    for i,raw in enumerate(sorted(code)):add('code'+str(i),'source_code',raw)
    add('program','native_program',Path(native_cli).read_bytes())
    package['comparisons']=[dict(id='comparison',treatment='B2',control='REF',state='completed',analysis_artifact_id='analysis',
        analysis_plan_artifact_id='analysis-plan',contrast_id=case['analysis']['contrasts'][0]['id'],run_artifact_ids=['index'],
        raw_result_artifact_ids=[a['id'] for a in package['artifacts'] if a['role']=='raw_result'],
        cost_artifact_ids=['query-cost','native-cost'],baseline_artifact_ids=[],ablation_artifact_ids=[],failure_artifact_ids=[])]
    request=dict(comparison_id='comparison',schedule_artifact_id='schedule',projection_plan_artifact_id='projection',
        run_plan_artifact_id='native-plan',run_index_artifact_id='index',observations_artifact_id='observations',
        query_cost_ledger_artifact_id='query-cost',native_cost_ledger_artifact_id='native-cost',native_report_artifact_ids=reports,
        final_inputs_artifact_id='final-inputs')
    context=dict(protocol='exp04-native-replays-v2',comparisons=[request]);pins={'comparison':case['trusted_owner_sha']}
    view_pins=None
    if attribution:
        context['protocol']='exp04-native-replays-v3'
        request['attribution_inputs_artifact_id']=add('attribution-inputs','attribution_inputs',attribution_raw)
        view_pins={'comparison':B.digest(attribution_raw)}
    def build(p=package,c=context,pin=pins,vpin=view_pins):return B.build(p,str(artifact_root),c,f.roots,native_cli,pin,vpin)
    result=build();replay=result['comparisons'][0]['native_reconciliation']
    assert replay['protocol']==('exp04-native-replay-reconciliation-v3' if attribution else 'exp04-native-replay-reconciliation-v2') and replay['status']=='fresh_projection_matched'
    assert replay['computational_final_binding_verified'] is True and replay['task_contract_replayed'] is True
    assert replay['extension_code_sources_bound']>0 and replay['expected_owner_review_sha256']==case['trusted_owner_sha']
    assert replay['scientific_lock_verified'] is False and replay['owner_authorization_authenticated'] is False
    if attribution:
        assert replay['attribution_sensitivity_replayed'] is True
        assert replay['expected_attribution_inputs_sha256']==B.digest(attribution_raw)
        assert replay['attribution_label_truth_authenticated'] is False
        with mock.patch.object(subprocess,'run',side_effect=AssertionError('wrong view pin must not execute')):
            for vp,code in [({},'attribution_pin_mapping_mismatch'),({'comparison':'0'*64},'native_replay_trusted_native_attribution_mismatch')]:
                try:build(vpin=vp)
                except B.EvidenceError as error:assert error.code==code,(error.code,code)
                else:raise AssertionError('wrong view pin accepted')
    assert result['full_card_acceptance'] is False and result['product']['owner_decision'] is None
    assert result['research']['owner_decision'] is None and result['deployment_authorized'] is False
    def expect(code,p=package,pin=pins):
        try:build(p=p,pin=pin)
        except B.EvidenceError as error:assert error.code==code,(code,error.code)
        else:raise AssertionError(('forged final evidence accepted',code))
    with mock.patch.object(subprocess,'run',side_effect=AssertionError('native invocation before pin rejection')):
        expect('final_review_pin_mapping_mismatch',pin={})
        expect('native_replay_trusted_owner_review_mismatch',pin={'comparison':'0'*64})
    # Simulate image A at the consumer read while the explicit native path and
    # projection refer to B. The mismatch must reject before any helper run.
    program=next(a for a in package['artifacts'] if a['id']=='program');saved=dict(program)
    path=artifact_root/program['relative_path'];original_program=path.read_bytes();other_image=b'explicit nonexecuted fixture image A'
    path.write_bytes(other_image);program.update(raw_sha256=B.digest(other_image),size_bytes=len(other_image))
    real_read=B.read_absolute
    with (mock.patch.object(B,'read_absolute',side_effect=lambda p:other_image if p==native_cli else real_read(p)),
          mock.patch.object(subprocess,'run',side_effect=AssertionError('unbound image must not execute'))):
        expect('native_current_program_binding_mismatch')
    program.update(saved);path.write_bytes(original_program)
    def omit(role,sha=None):
        p=copy.deepcopy(package)
        p['artifacts']=[a for a in p['artifacts'] if not(a['role']==role and (sha is None or a['raw_sha256']==sha))]
        p['comparisons'][0]['raw_result_artifact_ids']=[a['id'] for a in p['artifacts'] if a['role']=='raw_result']
        return p
    if attribution:
        expect('native_current_code_missing',p=omit('source_code',B.digest(P.VIEWS.SOURCE_BYTES)))
        rule=B.parse(attribution_raw)['view_sources'][0]['criterion']
        rule_sha=B.digest((Path(f.roots[rule['root']])/rule['artifact']['relative_path']).read_bytes())
        expect('native_consumed_source_missing',p=omit('raw_result',rule_sha))
        evidence=next(a for a in package['artifacts'] if a['id']=='analysis');saved_analysis=dict(evidence)
        analysis_path=artifact_root/evidence['relative_path'];original_analysis=analysis_path.read_bytes()
        forged=json.loads(original_analysis);forged['attribution_sensitivity']['additional_policy_executions']=1
        forged_raw=B.canonical(forged);analysis_path.write_bytes(forged_raw)
        evidence.update(raw_sha256=B.digest(forged_raw),size_bytes=len(forged_raw))
        expect('native_analysis_replay_mismatch')
        evidence.update(saved_analysis);analysis_path.write_bytes(original_analysis)
    expect('native_current_program_missing',p=omit('native_program'))
    expect('native_current_code_missing',p=omit('source_code',B.digest(P.SOURCE_BYTES)))
    expect('native_current_code_missing',p=omit('source_code',B.digest(T.T.SOURCE_BYTES)))
    origin_path=f.root/'final_test-tasks/native-index-before-task.json'
    origin_sha=B.digest(origin_path.read_bytes())
    expect('native_consumed_source_missing',p=omit('raw_result',origin_sha))
    # Correctly resealed results still have to match current original replay.
    artifact=next(a for a in package['artifacts'] if a['id']=='observations');saved=dict(artifact)
    path=artifact_root/artifact['relative_path'];original=path.read_bytes();value=json.loads(original)
    value['rows'][0]['correct']=False;changed=B.canonical(value);path.write_bytes(changed)
    artifact.update(raw_sha256=B.digest(changed),size_bytes=len(changed))
    expect('native_output_replay_mismatch')
    artifact.update(saved);path.write_bytes(original)
    # Actual CLI uses a separate expected-review file, not a package trust claim.
    args=[sys.executable,'-B',str(HERE/'build_evidence_index.py'),'--artifact-root',str(artifact_root),
          '--native-cli',native_cli,'--out',str(root/'decision-output')]
    for name,value in [('package',package),('native-replays',context),('trusted-final-reviews',
            dict(protocol='exp04-trusted-final-review-pins-v1',comparisons=pins))]:
        path=root/(name+'.json');path.write_bytes(B.canonical(value));args+=['--'+name,str(path)]
    if attribution:
        path=root/'trusted-attribution-pins.json';path.write_bytes(B.canonical(dict(protocol='exp04-trusted-attribution-pins-v1',comparisons=view_pins)))
        args+=['--trusted-attribution-pins',str(path)]
    for name,path in f.roots.items():args+=['--native-'+name+'-root',path]
    executed=subprocess.run(args,capture_output=True,text=True,timeout=60)
    assert executed.returncode==0,(executed.stdout,executed.stderr)
    index=json.loads((root/'decision-output/evidence_index.json').read_bytes())
    assert index['comparisons'][0]['native_reconciliation']['computational_final_binding_verified'] is True
    assert index['full_card_acceptance'] is False and index['paper_promotion_authorized'] is False
    print('actual original final task/REF pipeline→EXP04 API+CLI fresh replays/code+image+source/cost/index graph; independent wrongpin/omittedcode/program/origin/forgedobservation rejected; research/product decisions remain null')


if __name__=='__main__':
    if len(sys.argv)==4 and sys.argv[1]=='--integration':integration(Path(sys.argv[2]),sys.argv[3])
    else:unittest.main()

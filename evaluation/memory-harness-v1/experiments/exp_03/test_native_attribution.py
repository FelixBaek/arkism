"""Native-bound views keep the public confirmatory rejection and source trace."""
import copy
import importlib.util
from pathlib import Path
import tempfile
import json
import subprocess
import sys
import unittest

HERE=Path(__file__).resolve().parent


def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    value=importlib.util.module_from_spec(spec);spec.loader.exec_module(value)
    return value


T=load('_native_view_source_fixture',HERE/'test_native_projection.py')
C=load('_native_view_old_view_fixture',HERE/'test_attribution_views.py')
P,A=T.P,T.A


def inputs(case,write,prefix='native-view',labels_timing='after_results',missing_view=False):
    """Explicit synthetic view sources, never an inferred truth or reviewer."""
    plan,schedule=case['analysis'],case['schedule'];fresh=case['result']
    def artifact(name,value):
        raw=A.canonical(value)+b'\n'
        return dict(root='input',artifact=write(prefix+'-'+name+'.json',raw,'source')),raw
    definition=dict(protocol='exp03-attribution-views-v1',state='sealed_computational_views',
        analysis_plan_sha256=plan['plan_sha256'],schedule_sha256=A.digest(schedule),
        observations_sha256=A.digest(fresh['observations']),ledger_sha256=A.digest(fresh['ledger']),
        reference_view='original',views=[],labels_timing=labels_timing,definition_sha256='')
    labels=dict(protocol='exp03-attribution-labels-v1',definition_sha256='',views=[]);sources=[]
    for ident in ('original','same_labels'):
        q=[dict(stream_id=v['stream_id'],query_id=v['query_id'],available=True,kind=v['kind'],
            gold_sets=copy.deepcopy(v['gold_sets'])) for v in schedule['queries']]
        if missing_view and ident=='same_labels':q[0].update(available=False,kind=None,gold_sets=None)
        grades=[{k:r[k] for k in ('stream_id','query_id','policy_id','repetition','correct')}
                for r in fresh['observations']['rows']]
        criterion,craw=artifact(ident+'-criterion',dict(protocol='synthetic-view-rule-v1',view_id=ident,
            rule='provided_same_source_labels_not_new_checker_execution'))
        evaluator,eraw=artifact(ident+'-evaluator',dict(protocol='synthetic-view-evaluator-v1',view_id=ident,
            rule='provided_grades_sensitivity_only_not_task_truth_authority'))
        label_source,lraw=artifact(ident+'-source',dict(protocol='exp03-attribution-label-source-v1',
            view_id=ident,queries=q,grades=grades))
        definition['views'].append(dict(id=ident,criterion_sha256=A.sha(craw),evaluator_sha256=A.sha(eraw)))
        labels['views'].append(dict(id=ident,source_sha256=A.sha(lraw),queries=q,grades=grades))
        sources.append(dict(id=ident,criterion=criterion,evaluator=evaluator,label_source=label_source))
    definition['definition_sha256']=A.digest({k:v for k,v in definition.items() if k!='definition_sha256'})
    labels['definition_sha256']=definition['definition_sha256']
    d,_=artifact('definition',definition);l,_=artifact('labels',labels)
    request=dict(protocol='exp03-native-attribution-inputs-v1',scope='same_frozen_native_trace_only',
        analysis_plan_sha256=plan['plan_sha256'],schedule_sha256=A.digest(schedule),
        run_plan_raw_sha256=A.sha(case['run_plan']),run_index_raw_sha256=A.sha(case['run_index']),
        projection_plan_sha256=A.digest(case['projection']),definition=d,labels=l,view_sources=sources)
    return A.canonical(request)+b'\n'


def actual(case,root,native_cli):
    """Actual native final projection API and CLI, preserving primary trace."""
    f=case['f'];original=case['result'];raw=inputs(case,f.write,labels_timing='before_results')
    def project(data=raw,pin=None):
        return P.project(case['analysis'],case['schedule'],case['run_plan'],case['run_index'],case['projection'],
            f.roots,native_cli,case['final_inputs'],case['trusted_owner_sha'],
            attribution_inputs=data,trusted_attribution_sha256=A.sha(data) if pin is None else pin)
    missing_raw=inputs(case,f.write,prefix='native-view-missing',labels_timing='before_results',missing_view=True)
    incomplete=project(missing_raw)['analysis']['attribution_sensitivity']
    incomplete_view=next(v for v in incomplete['views']['views'] if v['id']=='same_labels')
    assert incomplete_view['status']=='uncomputable_missing_labels' and incomplete_view['analysis'] is None
    assert incomplete_view['planned_rows']==len(case['result']['observations']['rows'])
    assert incomplete_view['unavailable_query_labels']==1
    fresh=project();view=fresh['analysis']['attribution_sensitivity']
    primary=copy.deepcopy(fresh['analysis']);del primary['attribution_sensitivity']
    # New source reads are additionally disclosed; all original primary results,
    # REF choice, costs and execution/verifier facts must remain identical.
    primary['native_projection']['consumed_artifacts']=original['analysis']['native_projection']['consumed_artifacts']
    assert primary==original['analysis']
    for name in ('observations','ledger','native_execution_costs'):assert fresh[name]==original[name]
    assert fresh.native_reports==original.native_reports
    assert view['source_final_computational_binding_verified'] is True
    assert view['source_primary_and_REF_unchanged'] is True and view['additional_policy_executions']==0
    assert view['additional_model_or_provider_calls']==0 and view['scientific_acceptance_verified'] is False
    assert view['labels_timing_authenticated'] is False
    assert len(view['views']['views'])==2 and view['views']['exploratory'] is True
    assert view['views']['source_stage']=='confirmatory' and view['views']['stage']=='exploratory'
    for item in view['views']['views']:
        assert item['analysis'] is not None and item['analysis']['derived_evaluation_only'] is True
        assert item['analysis']['scientific_acceptance_verified'] is False
        assert item['analysis']['stage']=='exploratory' and item['analysis']['source_stage']=='confirmatory'
    # Expected input pin/source binding must reject before even native verification.
    from unittest import mock
    with mock.patch.object(subprocess,'run',side_effect=AssertionError('view input rejection must precede helper')):
        for data,pin,code in [(raw,'0'*64,'trusted_native_attribution_mismatch'),
                (A.canonical(dict(A.parse(raw),run_index_raw_sha256='0'*64)),None,'native_attribution_source_mismatch')]:
            try:project(data,pin)
            except A.AnalysisError as error:assert error.code==code,(error.code,code)
            else:raise AssertionError('unbound view input accepted')
    args=[sys.executable,'-B',str(HERE/'project_native.py'),'--native-cli',native_cli,
          '--out',str(root/'attribution-projection'),'--trusted-owner-review-sha256',case['trusted_owner_sha'],
          '--trusted-attribution-sha256',A.sha(raw)]
    values=dict(analysis_plan=A.canonical(case['analysis']),schedule=A.canonical(case['schedule']),
        run_plan=case['run_plan'],run_index=case['run_index'],projection_plan=A.canonical(case['projection']),
        final_inputs=A.canonical(case['final_inputs']),attribution_inputs=raw)
    for name,value in values.items():
        path=root/('view-cli-'+name+'.json');path.write_bytes(value);args+=['--'+name.replace('_','-'),str(path)]
    for name,path in f.roots.items():args+=['--'+name+'-root',path]
    done=subprocess.run(args,capture_output=True,text=True,timeout=60)
    assert done.returncode==0,(done.stdout,done.stderr)
    # Actual CLI reports include file/code provenance. EXP04 must freshly
    # reproduce that exact branch too, not just the API return value.
    reported=json.loads((root/'attribution-projection/analysis.json').read_bytes())
    check=copy.deepcopy(reported);del check['provenance'];assert check==fresh['analysis']
    fresh['analysis']=reported
    return fresh,raw


class NativeAttributionTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.f=T.Fixture(Path(self.temp.name))

    def case(self):
        f=self.f
        return dict(analysis=f.analysis,schedule=f.schedule,run_plan=f.plan_raw,run_index=f.index_raw,
            projection=f.projection,result=f.project())

    def test_public_view_entry_cannot_self_declare_confirmatory(self):
        args=list(C.fixture());args[0]['stage']='confirmatory'
        C.T.reseal(*args[:4]);d=args[4]
        d.update(analysis_plan_sha256=args[0]['plan_sha256'],schedule_sha256=A.digest(args[1]),
            observations_sha256=A.digest(args[2]),ledger_sha256=A.digest(args[3]))
        C.seal(d,args[5])
        with self.assertRaises(C.A.AnalysisError) as caught:C.V.analyze_views(*args)
        self.assertEqual(caught.exception.code,'confirmatory_execution_evidence_required')

    def test_pinned_view_inputs_cannot_promote_an_unrun_native_index(self):
        case=self.case();raw=inputs(case,self.f.write_raw)
        with self.assertRaises(A.AnalysisError) as caught:
            P.project(self.f.analysis,self.f.schedule,self.f.plan_raw,self.f.index_raw,self.f.projection,
                self.f.roots,self.f.native_cli,attribution_inputs=raw,trusted_attribution_sha256=A.sha(raw))
        self.assertEqual(caught.exception.code,'native_attribution_execution_evidence_required')

    def test_external_pin_and_current_index_reject_before_helper(self):
        case=self.case();raw=inputs(case,self.f.write_raw)
        from unittest import mock
        with mock.patch.object(subprocess,'run',side_effect=AssertionError('preflight must not invoke native')):
            for data,pin,code in [(raw,'0'*64,'trusted_native_attribution_mismatch'),
                    (A.canonical(dict(A.parse(raw),run_index_raw_sha256='0'*64)),None,'native_attribution_source_mismatch')]:
                with self.assertRaises(A.AnalysisError) as caught:
                    P.project(self.f.analysis,self.f.schedule,self.f.plan_raw,self.f.index_raw,self.f.projection,
                        self.f.roots,self.f.native_cli,attribution_inputs=data,
                        trusted_attribution_sha256=A.sha(data) if pin is None else pin)
                self.assertEqual(caught.exception.code,code)

    def test_resealed_criterion_ref_still_requires_original_rule_bytes(self):
        case=self.case();request=A.parse(inputs(case,self.f.write_raw))
        request['view_sources'][0]['criterion']['artifact']=self.f.write_raw('alternate-rule.json',b'{}','source')
        raw=A.canonical(request)
        from unittest import mock
        with mock.patch.object(subprocess,'run',side_effect=AssertionError('rule mismatch must precede helper')):
            with self.assertRaises(A.AnalysisError) as caught:
                P.project(self.f.analysis,self.f.schedule,self.f.plan_raw,self.f.index_raw,self.f.projection,
                    self.f.roots,self.f.native_cli,attribution_inputs=raw,trusted_attribution_sha256=A.sha(raw))
        self.assertEqual(caught.exception.code,'native_attribution_rule_binding_mismatch')

    def test_declared_before_results_never_promotes_derived_view_stage(self):
        # Controlled unit composition only; the actual integration above also
        # supplies before_results through real final/native proof and both CLIs.
        for timing in ('before_results','after_results'):
            args=list(C.fixture());args[4]['labels_timing']=timing;C.seal(args[4],args[5])
            primary=C.A.analyze(*args[:4]);primary['native_projection']={'verifications':['unit_only_not_native_proof']}
            primary['final_protocol_binding']={'unit_only':True}
            primary_before=copy.deepcopy(primary)
            result=P.VIEWS.analyze(dict(definition=args[4],labels=args[5],request_raw_sha256='1'*64),
                *args[:4],primary,{})
            self.assertTrue(result['views']['exploratory']);self.assertEqual(result['views']['stage'],'exploratory')
            self.assertFalse(result['labels_timing_authenticated'])
            for view in result['views']['views']:
                if view['analysis'] is not None:
                    self.assertTrue(view['analysis']['exploratory'])
                    self.assertEqual(view['analysis']['stage'],'exploratory')
            self.assertEqual(primary,primary_before)


if __name__=='__main__':unittest.main()

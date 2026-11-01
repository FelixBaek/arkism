"""Source-bound attribution sensitivity after fresh native projection only.

This internal composition never authenticates a person, supplied evaluation
labels or their timing. Public attribution_views/analyze guards remain intact.
"""
import importlib.util
from pathlib import Path

HERE=Path(__file__).resolve().parent
spec=importlib.util.spec_from_file_location('_native_bound_attribution_views',HERE/'attribution_views.py')
V=importlib.util.module_from_spec(spec);spec.loader.exec_module(V)
A=V.A
SOURCE_BYTES=Path(__file__).read_bytes()
SCOPE='same_frozen_native_trace_only'


def _source(reference,sources):
    A.fields(reference,'root artifact')
    A.require(reference['root'] in ('input','evidence','output'),'invalid_attribution_source_root')
    return sources.read(reference['artifact'],reference['root'],'source')


def prepare(raw,trusted_sha,sources,plan,schedule,plan_raw,index_raw,projection):
    """Reject pins/bindings/rule-source mismatches before native helper work."""
    A.require(type(raw) is bytes,'invalid_native_attribution_inputs')
    A.hash_value(trusted_sha)
    A.require(A.sha(raw)==trusted_sha,'trusted_native_attribution_mismatch')
    request=A.parse(raw)
    A.fields(request,'protocol scope analysis_plan_sha256 schedule_sha256 run_plan_raw_sha256 '
        'run_index_raw_sha256 projection_plan_sha256 definition labels view_sources')
    A.require(request['protocol']=='exp03-native-attribution-inputs-v1' and request['scope']==SCOPE,
        'invalid_native_attribution_inputs')
    for name,expected in [('analysis_plan_sha256',plan['plan_sha256']),('schedule_sha256',A.digest(schedule)),
            ('run_plan_raw_sha256',A.sha(plan_raw)),('run_index_raw_sha256',A.sha(index_raw)),
            ('projection_plan_sha256',A.digest(projection))]:
        A.require(request[name]==expected,'native_attribution_source_mismatch')
    definition=A.parse(_source(request['definition'],sources));labels=A.parse(_source(request['labels'],sources))
    A.fields(definition,'protocol state analysis_plan_sha256 schedule_sha256 observations_sha256 ledger_sha256 '
        'reference_view views labels_timing definition_sha256')
    A.require(definition['protocol']=='exp03-attribution-views-v1' and definition['state']=='sealed_computational_views' and
        definition['definition_sha256']==A.digest({k:v for k,v in definition.items() if k!='definition_sha256'}),
        'native_attribution_definition_mismatch')
    A.require(definition['analysis_plan_sha256']==plan['plan_sha256'] and
        definition['schedule_sha256']==A.digest(schedule),'native_attribution_source_mismatch')
    A.require(type(definition['views']) is list and 2<=len(definition['views'])<=8,'invalid_view_count')
    A.fields(labels,'protocol definition_sha256 views')
    A.require(labels['protocol']=='exp03-attribution-labels-v1' and
        labels['definition_sha256']==definition['definition_sha256'] and type(labels['views']) is list and
        len(labels['views'])==len(definition['views']),'view_labels_binding_mismatch')
    ids=set()
    for view in definition['views']:
        A.fields(view,'id criterion_sha256 evaluator_sha256');A.identifier(view['id'])
        A.hash_value(view['criterion_sha256']);A.hash_value(view['evaluator_sha256'])
        A.require(view['id'] not in ids,'duplicate_view');ids.add(view['id'])
    A.require(type(request['view_sources']) is list and len(request['view_sources'])==len(ids),
        'native_attribution_source_coverage')
    seen=set()
    for source in request['view_sources']:
        A.fields(source,'id criterion evaluator label_source')
        A.require(source['id'] in ids and source['id'] not in seen,'native_attribution_source_coverage');seen.add(source['id'])
        view=next(v for v in definition['views'] if v['id']==source['id'])
        criterion=_source(source['criterion'],sources);evaluator=_source(source['evaluator'],sources)
        A.require(A.sha(criterion)==view['criterion_sha256'] and A.sha(evaluator)==view['evaluator_sha256'],
            'native_attribution_rule_binding_mismatch')
        item=[v for v in labels['views'] if type(v) is dict and v.get('id')==source['id']]
        A.require(len(item)==1,'view_labels_coverage_mismatch');item=item[0]
        label_raw=_source(source['label_source'],sources);declared=A.parse(label_raw)
        A.fields(declared,'protocol view_id queries grades')
        A.require(declared['protocol']=='exp03-attribution-label-source-v1' and declared['view_id']==source['id'] and
            A.sha(label_raw)==item.get('source_sha256') and declared['queries']==item.get('queries') and
            declared['grades']==item.get('grades'),'native_attribution_label_binding_mismatch')
    return dict(request=request,request_raw_sha256=A.sha(raw),definition=definition,labels=labels)


def analyze(prepared,plan,schedule,observations,ledger,primary,execution_costs):
    """Internal caller has just performed actual native/lock/REF verification.

    The source primary stays unchanged. Derived view computations are never
    new confirmatory claims, a new execution, or a replacement REF selection.
    """
    native=primary.get('native_projection')
    A.require(type(native) is dict and bool(native.get('verifications')),
        'native_attribution_execution_evidence_required')
    if plan['stage']=='confirmatory':
        A.require(primary.get('final_protocol_binding') is not None,'native_attribution_final_binding_required')
    before=A.digest([plan,schedule,observations,ledger,primary,execution_costs])
    sensitivity=V._analyze_views(plan,schedule,observations,ledger,prepared['definition'],prepared['labels'],
        A._analyze_validated_inputs)
    # A separately pinned label declaration proves bytes, never registration
    # before results. This branch is always derived exploratory sensitivity.
    sensitivity.update(source_stage=sensitivity['stage'],stage='exploratory',exploratory=True)
    for view in sensitivity['views']:
        if view['analysis'] is not None:
            view['analysis'].update(source_stage=view['analysis']['stage'],stage='exploratory',exploratory=True)
            view['analysis']['derived_evaluation_only']=True
            view['analysis']['scientific_acceptance_verified']=False
    A.require(before==A.digest([plan,schedule,observations,ledger,primary,execution_costs]),
        'native_attribution_source_mutated')
    A.require(Path(__file__).read_bytes()==SOURCE_BYTES,'native_attribution_code_changed')
    return dict(protocol='exp03-native-attribution-sensitivity-v1',scope=SCOPE,
        source_request_raw_sha256=prepared['request_raw_sha256'],
        source_analysis_plan_sha256=plan['plan_sha256'],source_observations_sha256=A.digest(observations),
        source_query_cost_ledger_sha256=A.digest(ledger),source_native_cost_ledger_sha256=A.digest(execution_costs),
        source_primary_row_table_sha256=primary['row_table_sha256'],
        source_native_verifications=native['verifications'],
        source_final_computational_binding_verified=primary.get('final_protocol_binding') is not None,
        source_primary_and_REF_unchanged=True,additional_policy_executions=0,additional_model_or_provider_calls=0,
        label_scope='provided_source_bound_labels_not_new_checker_execution_or_truth_authentication',
        labels_timing_authenticated=False,scientific_acceptance_verified=False,product_authority_verified=False,
        human_authorization_authenticated=False,views=sensitivity)

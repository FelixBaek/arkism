"""Validation-only REF selection; exact scores and explicit unknown token costs."""
from collections import defaultdict
from fractions import Fraction
import importlib.util
import json
from pathlib import Path
import sys

HERE = Path(__file__).resolve().parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name,path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


A = load('_ref_selection_analysis',HERE/'analyze_paired.py')
L = load('_ref_selection_lock',HERE/'locked_analysis.py')
B = load('_ref_selection_index',HERE.with_name('exp_02')/'build_run_plan.py')
ELIGIBLE = {'B0','B1','V3-P1-FD','V3-P1-IR','V3-P1-NOTIME','V3-P1-NOOUT','B6'}


def source(call,*args):
    try:
        return call(*args)
    except (L.A.AnalysisError,B.Exp02Error) as error:
        raise A.AnalysisError(error.code) from None


def exact(value):
    return str(value.numerator) if value.denominator == 1 else str(value.numerator)+'/'+str(value.denominator)


def validate_rule(rule, raw, plan):
    A.fields(rule,'protocol protocol_lock_raw_sha256 manifest_raw_sha256 validation_analysis_plan_sha256 eligible_policies '
             'request_modes tie_tolerance token_cost_phases id_tie_break id_order')
    A.require(rule['protocol']=='exp01-ref-selection-rule-v2' and rule['protocol_lock_raw_sha256']==A.sha(raw['lock']) and
              rule['manifest_raw_sha256']==A.sha(raw['manifest']) and
              rule['validation_analysis_plan_sha256']==plan['plan_sha256'], 'REF_rule_binding_mismatch')
    policies=rule['eligible_policies']
    A.require(type(policies) is list and len(policies)>=2 and all(type(p) is str and p in ELIGIBLE for p in policies) and
              len(set(policies))==len(policies) and set(policies)<=set(plan['policies']), 'REF_invalid_candidates')
    research=source(L.source,L.G.parse_raw,raw['lock'])['research']
    A.decimal(rule['tie_tolerance'])
    A.require(Fraction(rule['tie_tolerance'])==Fraction(research['REF_tie_tolerance']) and
              0<=Fraction(rule['tie_tolerance'])<=1, 'REF_tolerance_mismatch')
    A.require(len(set(policies)|{'B2'})<=research['candidate_count'], 'REF_candidate_count_mismatch')
    A.require(type(rule['request_modes']) is list and rule['request_modes']==plan['request_modes'] and
              bool(rule['request_modes']), 'REF_request_mode_mismatch')
    A.require(type(rule['token_cost_phases']) is list and bool(rule['token_cost_phases']) and
              len(set(rule['token_cost_phases']))==len(rule['token_cost_phases']) and
              all(type(p) is str and p in A.CONTRACT['cost_phases'] for p in rule['token_cost_phases']), 'REF_invalid_cost_phases')
    A.require(rule['id_tie_break'] in ('lexicographic_policy_id','listed_order'), 'REF_id_rule_required')
    locked_order=research['REF_id_order']
    if type(locked_order) is list:
        A.require(set(policies)<=set(locked_order) and rule['id_tie_break']=='listed_order' and
                  rule['id_order']==[p for p in locked_order if p in policies], 'REF_locked_id_order_mismatch')
    if rule['id_tie_break']=='listed_order':
        A.require(type(rule['id_order']) is list and len(rule['id_order'])==len(policies) and
                  set(rule['id_order'])==set(policies), 'REF_invalid_id_order')
    else:
        A.require(rule['id_order'] is None, 'REF_invalid_id_order')
    return policies


def validation_population(raw, rule, plan, schedule):
    A.validate_plan(plan)
    A.require(plan['stage']=='exploratory' and plan['primary_metric']=='provided_grader_accuracy', 'REF_validation_plan_required')
    queries,streams,jobs=A.validate_schedule(plan,schedule)
    manifest=source(L.source,L.G.decode_typed,raw['manifest'],'SplitManifest')
    expected_streams={e['stream_id'] for e in manifest['entries'] if e['split']=='validation'}
    A.require(set(streams)==expected_streams, 'REF_validation_split_mismatch')
    originals=source(L.source,L.V._rows,raw['queries'],'QueryPlan')
    expected={(q['stream_id'],q['query_id']):q for q in originals if q['stream_id'] in expected_streams and
              ('current' if q['request_mode']=='current' else 'as_of') in rule['request_modes']}
    A.require(set(queries)==set(expected), 'REF_validation_query_coverage_mismatch')
    gold={(g['stream_id'],g['query_id']):g for g in source(L.source,L.V._rows,raw['gold'],'GoldRow')}
    assignments=A.parse(raw['cluster_assignments'])
    assigned={r['stream_id']:r['cluster_id'] for r in assignments['assignments']}
    research=source(L.source,L.G.parse_raw,raw['lock'])['research']
    A.require(plan['replications']==research['repetitions'] and plan['tuning_trials']<=research['tuning_trials'], 'REF_validation_budget_mismatch')
    for key,q in queries.items():
        g=gold[key]
        kind={'primary_answerable':'answerable','clear_negative':'clear_negative','ambiguous':'ambiguous','alternative_answers':'ambiguous'}[g['cohort']]
        A.require(q['scope']==expected[key]['scope'] and
                  q['request_mode']==('current' if expected[key]['request_mode']=='current' else 'as_of') and q['kind']==kind and
                  q['gold_sets']==([g['required_refs']] if kind=='answerable' else []) and
                  q['input_sha256']==A.sha(raw['analysis_source']) and
                  q['cluster_id']==assigned[q['stream_id']], 'REF_validation_source_mismatch')
        A.require(q['budget']['final_k']==research['k'] and all(q['budget'][k]==v for k,v in research['token_budget'].items()
                  if k in ('max_input_tokens','max_output_tokens')), 'REF_validation_budget_mismatch')
        if 'max_total_tokens' in research['token_budget']:
            A.require(q['budget']['max_input_tokens'] is not None and q['budget']['max_output_tokens'] is not None and
                      q['budget']['max_input_tokens']+q['budget']['max_output_tokens']<=research['token_budget']['max_total_tokens'],
                      'REF_validation_budget_mismatch')
    return queries,streams,jobs


def observed_tokens(index, costs, policies, phases):
    # Native batch costs are compared as complete recorded executions, not
    # divided into fabricated per-query allocations. Failed retries still count.
    rows={(r['job_id'],r['attempt_id']) for r in index['rows']}
    A.require(costs['protocol']=='exp03-native-execution-costs-v1', 'REF_native_cost_protocol')
    records=costs['records']
    A.require(type(records) is list and len(records)<=A.CONTRACT['maximum_rows'], 'REF_cost_work_limit')
    A.require(len(rows)*len(policies)*len(phases)*2<=A.CONTRACT['maximum_bootstrap_work'], 'REF_cost_work_limit')
    grouped=defaultdict(list)
    measures={}
    for r in index['rows']:
        for c in r['cost_measures']:
            measures[(r['job_id'],r['attempt_id'],c['measurement_id'])]=c
    seen=set()
    for r in records:
        A.fields(r,'measurement_id phase resource allocation policy_id value_decimal unit basis missing_reason job_id attempt_id '
                 'source_sha256 price_source_sha256 price_as_of')
        identity=(r['job_id'],r['attempt_id'],r['measurement_id'])
        A.require(identity in measures and identity not in seen, 'REF_cost_record_mismatch')
        seen.add(identity)
        c=measures[identity]
        fields=('measurement_id','phase','resource','allocation','policy_id','value_decimal','unit','basis','missing_reason')
        A.require(all(r[k]==c[k] for k in fields) and
                  r['source_sha256']==(c['source_artifact']['raw_sha256'] if c['source_artifact'] else None) and
                  r['price_source_sha256']==(c['price_artifact']['raw_sha256'] if c['price_artifact'] else None), 'REF_cost_record_mismatch')
        grouped[(r['job_id'],r['attempt_id'],r['policy_id'],r['phase'],r['resource'],r['allocation'])].append(r)
    A.require(seen==set(measures), 'REF_cost_record_mismatch')
    token_records=[r for r in records if r['resource'] in ('input_tokens','output_tokens') and r['policy_id'] in (*policies,None)]
    A.require(all(r['phase'] in phases for r in token_records),'REF_token_phase_coverage_mismatch')
    totals={}
    for policy in policies:
        values=[]
        # Shared/nonadditive measurements cannot be assigned to one policy or
        # added on top of potentially overlapping incremental measurements.
        valid=not any(r['allocation']!='policy_incremental' and r['policy_id'] in (None,policy) for r in token_records)
        valid=valid and not any(r['resource'] in ('input_tokens','output_tokens') and r['policy_id'] in (None,policy)
                               for r in costs['missing_declared_native_requirements'])
        for job,attempt in sorted(rows):
            for phase in phases:
                for resource in ('input_tokens','output_tokens'):
                    measures=grouped[(job,attempt,policy,phase,resource,'policy_incremental')]
                    if not measures or any(r['basis']!='observed' or r['source_sha256'] is None or r['unit']!='count' for r in measures):
                        valid=False;continue
                    for r in measures:
                        A.require(type(r['value_decimal']) is str and len(r['value_decimal'])<=4096 and
                                  B._primitive('UnsignedDecimal',r['value_decimal']), 'REF_invalid_token_count')
                        value=Fraction(r['value_decimal'])
                        A.require(value>=0 and value.denominator==1, 'REF_invalid_token_count')
                        values.append(value)
        totals[policy]=sum(values,Fraction(0)) if valid and rows else None
    return totals


def select(rule, raw, plan, schedule, observations, index, costs, index_raw=None):
    # Full final lock/dataset/partition validation precedes any validation-only
    # selection, preventing final-test questions or post-hoc grouping as inputs.
    source(L.bind,raw,'before_results')
    A.validate_plan(plan)
    policies=validate_rule(rule,raw,plan)
    queries,streams,jobs=validation_population(raw,rule,plan,schedule)
    indexed=A.validate_observations(plan,schedule,queries,jobs,observations)
    missing={p:0 for p in policies};planned=0;absent=0;unpaired=0
    histories={p:defaultdict(list) for p in policies}
    for key,q in sorted(queries.items()):
        if not A._eligible(q,plan):continue
        scores={p:[] for p in policies}
        for rep in range(plan['replications']):
            values={p:A.score(q,indexed.get((*key,p,rep)),plan['primary_metric'],plan['truncation_rule']) for p in policies}
            planned+=1
            for p,v in values.items():missing[p]+=v is None
            absent+=any(indexed.get((*key,p,rep)) is None or indexed[(*key,p,rep)]['status'] in ('missing','not_run') for p in policies)
            if all(v is not None for v in values.values()):
                for p,v in values.items():scores[p].append(Fraction(int(v)))
        if len(scores[policies[0]])<plan['minimum_valid_replications']:
            unpaired+=1;continue
        for p in policies:histories[p][key[0]].append(sum(scores[p],Fraction(0))/len(scores[p]))
    scores={p:(sum((sum(q,Fraction(0))/len(q) for q in histories[p].values()),Fraction(0))/len(histories[p])
               if histories[p] else None) for p in policies}
    missing_rates={p:Fraction(missing[p],planned) if planned else Fraction(1) for p in policies}
    reasons=[]
    if not planned or absent or unpaired or any(v is None for v in scores.values()):reasons.append('validation_incomplete')
    if max(missing_rates.values())>Fraction(plan['max_missing_fraction']) or max(missing_rates.values())-min(missing_rates.values())>Fraction(plan['max_missing_gap']):
        reasons.append('validation_missingness_limit')
    try:
        B.validate_typed('RunIndex',index)
        B.validate_index_values(index)
    except B.Exp02Error as error:
        raise A.AnalysisError('REF_source_'+error.code) from None
    index_raw=B.compact(index) if index_raw is None else index_raw
    A.require(B.parse_raw(index_raw)==index and costs['run_index_sha256']==A.sha(index_raw), 'REF_cost_index_binding_mismatch')
    A.require(all(r['phase'] in rule['token_cost_phases'] for r in plan['cost_requirements']
                  if r['resource'] in ('input_tokens','output_tokens')),'REF_token_phase_coverage_mismatch')
    token_costs=observed_tokens(index,costs,policies,rule['token_cost_phases'])
    tied=[];winner=None
    if not reasons:
        best=max(scores.values())
        tied=sorted(p for p in policies if best-scores[p]<=Fraction(rule['tie_tolerance']))
        if len(tied)>1:
            if any(token_costs[p] is None for p in tied):reasons.append('tie_token_cost_unknown')
            else:
                minimum=min(token_costs[p] for p in tied)
                tied=[p for p in tied if token_costs[p]==minimum]
        if not reasons:
            order=sorted(policies) if rule['id_tie_break']=='lexicographic_policy_id' else rule['id_order']
            winner=next(p for p in order if p in tied)
    result=dict(protocol='exp01-ref-selection-result-v2',status='selected' if winner else 'undetermined',selected_policy=winner,
        rule_sha256=A.digest(rule),analysis_plan_sha256=plan['plan_sha256'],schedule_sha256=A.digest(schedule),
        observations_sha256=A.digest(observations),native_costs_sha256=A.digest(costs),
        scope='provided_validation_observations_and_recorded_native_costs_not_label_truth_or_authority',
        scores={p:exact(v) if v is not None else None for p,v in scores.items()},
        observed_token_costs={p:exact(v) if v is not None else None for p,v in token_costs.items()},
        retained_ties=tied,missing_by_policy=missing,eligible_repetitions=planned,unpaired_queries=unpaired,
        absent_or_unexecuted_repetitions=absent,reasons=reasons,
        history_counts={p:len(histories[p]) for p in policies},full_card_acceptance=False,execution_authorized=False)
    result['selection_sha256']=A.digest(result)
    return result


def parse(raw):
    # A native batch total can grow by six digits beyond one 4096-digit source
    # measurement. Retain exact bounded totals without widening frozen codecs.
    A.require(len(raw)<=A.MAX_BYTES,'input_limit')
    try:
        value=json.loads(raw.decode('utf8'),object_pairs_hook=A._pairs,parse_float=A._invalid_number,parse_constant=A._invalid_number)
    except (ValueError,UnicodeError,RecursionError):
        raise A.AnalysisError('invalid_json') from None
    pending=[(value,0)];nodes=0
    while pending:
        item,depth=pending.pop();nodes+=1
        A.require(depth<=32 and nodes<=2000000,'input_limit')
        if type(item) is dict:pending.extend((v,depth+1) for pair in item.items() for v in pair)
        elif type(item) is list:
            A.require(len(item)<=100000,'input_limit');pending.extend((v,depth+1) for v in item)
        elif type(item) is str:
            A.require((len(item)<=4096 or len(item)<=4102 and B._primitive('UnsignedDecimal',item)) and
                      all(not 0xd800<=ord(c)<=0xdfff for c in item),'invalid_json_string')
        elif type(item) is int:A.require(-(2**63)<=item<2**63,'integer_out_of_range')
    return value


def main(argv=None):
    parser=A.Parser(allow_abbrev=False)
    for name in ('request','input-root','evidence-root','output-root','native-cli','out'):
        parser.add_argument('--'+name,required=True,action=A.Once)
    try:
        args=parser.parse_args(argv)
        P=load('_ref_selection_native_projector',HERE/'project_native.py')
        try:
            raw=P.external(P.G.bounded_read,Path(args.request))
            sources=P.Sources(dict(input=args.input_root,evidence=args.evidence_root,output=args.output_root))
            result,_=P.recompute_REF_selection(parse(raw),sources,sources.roots,args.native_cli,False)
            A.require(P.external(P.G.bounded_read,Path(args.request))==raw,'REF_request_changed')
            sources.recheck()
            P.publish({'selection.json':A.canonical(result)+b'\n'},args.out)
        except P.A.AnalysisError as error:
            raise A.AnalysisError(error.code) from None
        print(json.dumps(dict(protocol='exp01-ref-selection-written-v2',status=result['expected']['status'],
                              selected_policy=result['expected']['selected_policy'],execution_authorized=False)))
        return 0
    except A.AnalysisError as error:code=error.code
    except OSError:code='file_io_error'
    except Exception:code='internal_error'
    print(json.dumps(dict(protocol='exp01-ref-selection-error-v2',code=code,path='$')))
    return 2


if __name__=='__main__':raise SystemExit(main())

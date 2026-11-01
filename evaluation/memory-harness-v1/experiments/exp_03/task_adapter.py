"""Explicit model-free synthetic reader/checker on freshly verified retrieval.

The reader sees only selected visible source bodies and its literal prefix.
Expected answers are passed solely to the checker. This is an explicit fixture
task, never an LLM outcome, independent human assessment, or study acceptance.
"""
import importlib.util
import copy
import json
from pathlib import Path
import time

HERE=Path(__file__).resolve().parent


def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    result=importlib.util.module_from_spec(spec);spec.loader.exec_module(result)
    return result


P=load('_task_native_projection',HERE/'project_native.py')
M=load('_task_observed_source_view',HERE.with_name('exp_02')/'materialize_native.py')
A,B,G=P.A,P.B,P.G
SOURCE_BYTES=Path(__file__).read_bytes()
DEPENDENCIES={Path(m.__file__):Path(m.__file__).read_bytes() for m in (P,M,A,B,G,P.C,M.G,M.V)}


def identity(task_raw):
    return A.digest(dict(adapter_source_sha256=A.sha(SOURCE_BYTES),task_contract_raw_sha256=A.sha(task_raw),
        dependencies={p.relative_to(HERE.parents[3]).as_posix():A.sha(raw) for p,raw in sorted(DEPENDENCIES.items())}))


def source_data(task,sources):
    A.fields(task,'protocol scope source_files tasks stage_namespace')
    A.require(B._primitive('ID',task['stage_namespace']),'invalid_task_namespace')
    A.require(task['protocol']=='exp03-literal-synthetic-task-v1' and
              task['scope']=='model_free_synthetic_fixture_not_study_quality','invalid_task_scope')
    A.fields(task['source_files'],'events queries gold manifest')
    raw={n:sources.read(ref,'input','source') for n,ref in task['source_files'].items()}
    manifest=P.external(G.decode_typed,raw['manifest'],'SplitManifest')
    A.require(all(manifest['files'][n+'_sha256']==A.sha(raw[n]) for n in ('events','queries','gold')),
              'task_source_raw_hash_mismatch')
    events,queries,gold=(P.external(P.B.V._rows,raw[n],kind) for n,kind in
                        [('events','PlanEvent'),('queries','QueryPlan'),('gold','GoldRow')])
    P.external(P.B.V.validate_artifacts,events,queries,gold,manifest)
    return events,{(q['stream_id'],q['query_id']):q for q in queries}


def validate_tasks(task,originals,queries):
    A.require(type(task['tasks']) is list and len(task['tasks'])<=A.CONTRACT['maximum_rows'],'task_work_limit')
    indexed={}
    for row in task['tasks']:
        A.fields(row,'stream_id query_id source_query_sha256 reader checker')
        for name in ('stream_id','query_id'):A.identifier(row[name])
        A.hash_value(row['source_query_sha256'])
        key=(row['stream_id'],row['query_id'])
        A.require(key in queries and key in originals and key not in indexed,'task_query_coverage_mismatch')
        A.require(row['source_query_sha256']==G.semantic_sha('QueryPlan',originals[key]),'task_query_binding_mismatch')
        A.fields(row['reader'],'rule prefix');A.fields(row['checker'],'rule accepted_answers')
        A.require(row['reader']['rule']=='first_visible_body_literal_prefix' and type(row['reader']['prefix']) is str and
                  0<len(row['reader']['prefix'].encode('utf8'))<=4096,'unsupported_reader_rule')
        answers=row['checker']['accepted_answers']
        A.require(row['checker']['rule']=='exact_answer_or_explicit_abstention' and type(answers) is list and
                  0<len(answers)<=100 and all(v is None or type(v) is str and len(v.encode('utf8'))<=4096 for v in answers),
                  'unsupported_checker_rule')
        indexed[key]=row
    A.require(set(indexed)==set(queries),'task_query_coverage_mismatch')
    return indexed


class ContextIndex:
    """Lookup validated source rows by exact ref, recorded cutoff and view time.

    Source visibility is not approval. Only independently replayed selected
    references reach the reader. No future/current revision substitutes a body.
    """
    def __init__(self,events):
        self.bodies={};self.withdrawals={}
        for event in events:
            stream=event['stream_id'];target=event['target']
            if event['kind']=='material_available':
                key=(stream,M.refkey(target))
                value=(event['plan_seq'],event['available_at_ns'],event['source_payload']['text'])
                if key not in self.bodies or value[0]<self.bodies[key][0]:self.bodies[key]=value
            elif event['kind'] in ('material_deleted','scope_changed'):
                key=(stream,M.refkey(target)[:3])
                self.withdrawals[key]=min(self.withdrawals.get(key,event['plan_seq']),event['plan_seq'])

    def get(self,query,selected):
        at=query['at_ns'] if query['request_mode']=='current' else query['requested_as_of_ns']
        bodies=[]
        for ref in selected:
            key=M.refkey(ref);source=self.bodies.get((query['stream_id'],key))
            withdrawn=self.withdrawals.get((query['stream_id'],key[:3]))
            if (source is not None and source[0]<=query['cutoff_plan_seq'] and source[1]<=at and
                    (withdrawn is None or withdrawn>query['cutoff_plan_seq'])):bodies.append(source[2])
        return bodies


def contexts(events,query,selected):return ContextIndex(events).get(query,selected)


def read_answer(bodies,reader):
    """No checker, expected answers, gold, or query text is accepted here."""
    for body in bodies:
        if body.startswith(reader['prefix']):return body[len(reader['prefix']):]
    return None


def check_answer(answer,checker):return answer in checker['accepted_answers']


def prepare_contract(task_raw,analysis,schedule,sources):
    A.require(analysis['primary_metric']=='provided_grader_accuracy' and analysis['grader_sha256']==identity(task_raw),
              'task_grader_identity_mismatch')
    task=A.parse(task_raw);events,originals=source_data(task,sources)
    queries,_,_=A.validate_schedule(analysis,schedule)
    return task,events,originals,queries,validate_tasks(task,originals,queries)


def verify_grades(task_raw,analysis,schedule,index,graded,sources):
    """Recompute the literal task from pinned predictions; Go replay follows.

    This performs no policy subprocess, reader plugin, model or paid call.
    The parent projector independently verifies every completed native attempt.
    """
    task,events,originals,_,tasks=prepare_contract(task_raw,analysis,schedule,sources)
    context_index=ContextIndex(events)
    plan=B.parse_raw(sources.read(index['plan'],'input','plan'));jobs={j['job_id']:j for j in plan['jobs']}
    expected_keys=set();origins={};token_sources={}
    for row in index['rows']:
        if row['status'] not in P.COMPLETE:continue
        expected={q['query_id']:(q['stream_id'],q['query_id']) for q in jobs[row['job_id']]['common']['expected_schedule']}
        for policy in row['policy_rows']:
            run=P.C.decode(sources.read(policy['prediction_file'],'output','prediction'),'PredictionRunV2')
            called=0
            for prediction in run['predictions']:
                if prediction['status']!='ok':continue
                key=expected.get(prediction['query_id'])
                A.require(key in tasks,'task_query_coverage_mismatch')
                logical=(row['job_id'],row['attempt_id'],*key,policy['policy_id'])
                A.require(logical in graded,'task_grade_coverage_mismatch');expected_keys.add(logical)
                answer=read_answer(context_index.get(originals[key],prediction['decision']['selected']),tasks[key]['reader'])
                grade=graded[logical]
                A.require(grade['status']=='success' and grade['fallback'] is False and
                          grade['correct'] is check_answer(answer,tasks[key]['checker']) and
                          grade['final_refs_sha256']==A.digest(prediction['decision']['selected']), 'task_grade_replay_mismatch')
                called+=1
            for resource in ('input_tokens','output_tokens'):
                costs=[c for c in row['cost_measures'] if c['policy_id']==policy['policy_id'] and
                       c['phase']=='reader_generator' and c['resource']==resource and c['allocation']=='policy_incremental']
                A.require(len(costs)==1,'task_cost_coverage_mismatch');cost=costs[0]
                A.require(cost['basis']=='observed' and cost['value_decimal']=='0' and cost['unit']=='count' and
                          cost['source_artifact'] is not None,'task_cost_replay_mismatch')
                source=A.parse(sources.read(cost['source_artifact'],'output','cost_source'))
                A.fields(source,'protocol scope grader_sha256 task_contract_raw_sha256 native_index_raw_sha256 native_index_artifact job_id attempt_id '
                         'policy_id reader_calls checker_calls model_invocations input_tokens output_tokens elapsed_local_ns token_scope')
                A.hash_value(source['native_index_raw_sha256']);A.count(source['elapsed_local_ns'],2**63-1)
                origin_raw=sources.read(source['native_index_artifact'],'output','source')
                A.require(A.sha(origin_raw)==source['native_index_raw_sha256'],'task_origin_index_mismatch')
                origins[source['native_index_raw_sha256']]=origin_raw
                token_sources[(row['job_id'],row['attempt_id'],policy['policy_id'])]=cost['source_artifact']
                A.require(source.get('protocol')=='exp03-literal-reader-cost-source-v1' and
                          source.get('scope')==task['scope'] and source.get('grader_sha256')==analysis['grader_sha256'] and
                          source.get('task_contract_raw_sha256')==A.sha(task_raw) and
                          source['token_scope']=='model_tokens_only_not_source_characters_or_real_provider_cost' and
                          all(source.get(k)==row[k] for k in ('job_id','attempt_id')) and
                          source.get('policy_id')==policy['policy_id'] and all(type(source[k]) is int and source[k]==called
                          for k in ('reader_calls','checker_calls')) and all(type(source.get(k)) is int and source[k]==0
                          for k in ('model_invocations','input_tokens','output_tokens')),'task_cost_replay_mismatch')
    A.require(set(graded)==expected_keys,'task_grade_coverage_mismatch')
    A.require(len(origins)==1,'task_origin_index_mismatch')
    original=B.parse_raw(next(iter(origins.values())))
    B.validate_typed('RunIndex',original);B.validate_index_values(original)
    raw_plan=sources.read(original['plan'],'input','plan')
    B.validate_index_plan(original,B.parse_raw(raw_plan),raw_plan)
    transformed=copy.deepcopy(original)
    for row in transformed['rows']:
        if row['status'] not in P.COMPLETE:continue
        for policy in row['policy_rows']:
            key=(row['job_id'],row['attempt_id'],policy['policy_id'])
            A.require(key in token_sources,'task_origin_index_mismatch')
            measure_tokens(row,policy['policy_id'],token_sources[key])
    A.require(transformed==index,'task_origin_index_transformation_mismatch')


def measure_tokens(row,policy,source_ref):
    for resource in ('input_tokens','output_tokens'):
        same=[c for c in row['cost_measures'] if c['phase']=='reader_generator' and c['resource']==resource and
              c['policy_id']==policy and c['allocation']=='policy_incremental']
        A.require(len(same)<=1 and all(c['basis']=='missing' and c['value_decimal'] is None and
                  c['source_artifact'] is None and c['price_artifact'] is None for c in same),'reader_cost_already_recorded')
        cost=(same[0] if same else dict(measurement_id='t'+A.digest([row['job_id'],row['attempt_id'],policy,resource])[:40],
            phase='reader_generator',resource=resource,allocation='policy_incremental',policy_id=policy,unit='count'))
        cost.update(value_decimal='0',basis='observed',missing_reason=None,source_artifact=source_ref,price_artifact=None)
        if not same:row['cost_measures'].append(cost)


def adapt(analysis,schedule,plan_raw,index_raw,projection,roots,native_cli,task_raw,trusted_task_sha,
          final_inputs=None,trusted_owner_sha=None):
    A.hash_value(trusted_task_sha)
    A.require(A.sha(task_raw)==trusted_task_sha,'trusted_task_contract_mismatch')
    A.require(projection['grader_artifact'] is None,'task_grades_already_recorded')
    sources=P.Sources(roots)
    task,events,originals,queries,tasks=prepare_contract(task_raw,analysis,schedule,sources)
    A.require(P.has_task_contract(projection) and
              sources.read(projection['task_contract'],'input','owner_contract')==task_raw,'task_projection_contract_mismatch')
    A.require(sources.read(task['source_files']['queries'],'input','source')==
              sources.read(projection['query_artifact'],'input','source') and
              sources.read(task['source_files']['gold'],'input','source')==sources.read(projection['gold_artifact'],'input','source'),
              'task_projection_dataset_mismatch')
    # Real original Go replay establishes origin. Stored success flags cannot
    # supply the selected references or authorize a grade.
    fresh=P.project(analysis,schedule,plan_raw,index_raw,projection,roots,native_cli,final_inputs,trusted_owner_sha)
    index=B.parse_raw(index_raw);jobs={j['job_id']:j for j in B.parse_raw(plan_raw)['jobs']}
    report_map={r['job_id']+'\0'+r['attempt_id']:fresh.native_reports[r['report_file']]
                for r in fresh['analysis']['native_projection']['verifications']}
    A.require(bool(report_map),'task_native_replay_required')
    files={};grades=[];outcomes=[];total=0;context_index=ContextIndex(events)
    def put(name,value,role='source'):
        nonlocal total
        A.require(name not in files,'task_output_collision')
        raw=value if type(value) is bytes else A.canonical(value)+b'\n'
        A.require(len(raw)<=A.MAX_BYTES,'task_output_limit');files[name]=raw
        total+=len(raw);A.require(total<=P.MAX_TOTAL_BYTES,'task_output_total_limit')
        return dict(role=role,relative_path=task['stage_namespace']+'/'+name,raw_sha256=A.sha(raw),size_bytes=len(raw))
    origin_ref=put('native-index-before-task.json',index_raw)
    for row in index['rows']:
        if row['status'] not in P.COMPLETE:continue
        native=P.C.parse(report_map[row['job_id']+'\0'+row['attempt_id']])
        expected={q['query_id']:q for q in jobs[row['job_id']]['common']['expected_schedule']}
        for item in native['policies']:
            policy=item['run']['policy_id'];reader_calls=0;checker_calls=0;elapsed=0
            for prediction in item['run']['predictions']:
                if prediction['status']!='ok':continue
                q=expected[prediction['query_id']];key=(q['stream_id'],q['query_id']);spec=tasks[key]
                started=time.monotonic_ns()
                answer=read_answer(context_index.get(originals[key],prediction['decision']['selected']),spec['reader'])
                reader_calls+=1;correct=check_answer(answer,spec['checker']);checker_calls+=1
                elapsed+=time.monotonic_ns()-started
                grades.append(dict(job_id=row['job_id'],attempt_id=row['attempt_id'],stream_id=key[0],query_id=key[1],
                    policy_id=policy,status='success',correct=correct,fallback=False,grader_sha256=analysis['grader_sha256'],
                    final_refs_sha256=A.digest(prediction['decision']['selected'])))
                outcomes.append(dict(job_id=row['job_id'],attempt_id=row['attempt_id'],query_id=key[1],policy_id=policy,
                    answer_sha256=None if answer is None else A.sha(answer.encode('utf8')),abstained=answer is None,correct=correct))
            cost=put('cost-'+A.digest([row['job_id'],row['attempt_id'],policy])+'.json',dict(
                protocol='exp03-literal-reader-cost-source-v1',scope=task['scope'],grader_sha256=analysis['grader_sha256'],
                task_contract_raw_sha256=trusted_task_sha,native_index_raw_sha256=A.sha(index_raw),native_index_artifact=origin_ref,job_id=row['job_id'],
                attempt_id=row['attempt_id'],policy_id=policy,reader_calls=reader_calls,checker_calls=checker_calls,
                model_invocations=0,input_tokens=0,output_tokens=0,elapsed_local_ns=elapsed,
                token_scope='model_tokens_only_not_source_characters_or_real_provider_cost'),'cost_source')
            measure_tokens(row,policy,cost)
    B.validate_typed('RunIndex',index);B.validate_index_values(index);B.validate_index_plan(index,B.parse_raw(plan_raw),plan_raw)
    grade_ref=put('grades.json',dict(protocol='exp03-grader-observations-v1',analysis_plan_sha256=analysis['plan_sha256'],
        schedule_sha256=A.digest(schedule),rows=grades))
    put('index.json',B.compact(index))
    updated=dict(projection,run_index_raw_sha256=A.sha(files['index.json']),grader_artifact=grade_ref)
    put('projection.json',updated)
    for name,raw in fresh.native_reports.items():put('pregrade-'+name,raw)
    put('stage.json',dict(protocol='exp03-literal-task-stage-v1',scope=task['scope'],task_contract_raw_sha256=trusted_task_sha,
        grader_sha256=analysis['grader_sha256'],original_index_raw_sha256=A.sha(index_raw),outcomes=outcomes,
        execution_authorized=False,scientific_approval_verified=False,real_provider_quality_verified=False,
        complete_native_replay_reports=len(report_map),source_artifacts=[dict(root=r,relative_path=p,raw_sha256=A.sha(v),size_bytes=len(v))
            for (r,p),v in sorted(sources.raw.items())]))
    sources.recheck()
    A.require(Path(__file__).read_bytes()==SOURCE_BYTES and all(p.read_bytes()==raw for p,raw in DEPENDENCIES.items()),'task_code_changed')
    return files


def main(argv=None):
    parser=A.Parser(allow_abbrev=False)
    for name in ('analysis-plan','schedule','run-plan','run-index','projection-plan','task-contract','trusted-task-sha256',
                 'input-root','evidence-root','output-root','native-cli','out'):
        parser.add_argument('--'+name,required=True,action=A.Once)
    for name in ('final-inputs','trusted-owner-review-sha256'):parser.add_argument('--'+name,action=A.Once)
    try:
        args=parser.parse_args(argv)
        A.require(bool(args.final_inputs)==bool(args.trusted_owner_review_sha256),'final_review_arguments_required')
        names=('analysis_plan','schedule','run_plan','run_index','projection_plan','task_contract')
        paths={n:Path(getattr(args,n)) for n in names}
        if args.final_inputs:paths['final_inputs']=Path(args.final_inputs)
        raw={n:G.bounded_read(p) for n,p in paths.items()}
        roots=dict(input=args.input_root,evidence=args.evidence_root,output=args.output_root)
        task=A.parse(raw['task_contract'])
        A.require(Path(args.out)==G.checked_directory(args.output_root)/task['stage_namespace'],'task_output_root_mismatch')
        files=adapt(A.parse(raw['analysis_plan']),A.parse(raw['schedule']),raw['run_plan'],raw['run_index'],A.parse(raw['projection_plan']),
            roots,args.native_cli,raw['task_contract'],args.trusted_task_sha256,
            A.parse(raw['final_inputs']) if args.final_inputs else None,args.trusted_owner_review_sha256)
        A.require(all(G.bounded_read(paths[n])==v for n,v in raw.items()),'task_inputs_changed')
        P.publish(files,args.out)
        print(json.dumps(dict(protocol='exp03-task-stage-written-v1',scope='model_free_synthetic_fixture_not_study_quality',
            grader_sha256=identity(raw['task_contract']),scientific_approval_verified=False)))
        return 0
    except (A.AnalysisError,B.Exp02Error,G.ExpError,G.IO.ContractError,P.C.ContractErrorV2) as error:code=error.code
    except OSError:code='file_io_error'
    except Exception:code='internal_error'
    print(json.dumps(dict(protocol='exp03-task-stage-error-v1',code=code,path='$')))
    return 2


if __name__=='__main__':
    raise SystemExit(main())

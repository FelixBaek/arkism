"""Record explicitly dispatched offline native attempts and fresh replay proof.

The original argv is passed unchanged; executable= selects a byte-pinned private
image snapshot. Neither declarations nor computational review authenticate a
person, scientific approval, product execution, or paid-provider permission.
"""
import copy
import importlib.util
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import time

HERE=Path(__file__).resolve().parent


def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    result=importlib.util.module_from_spec(spec);spec.loader.exec_module(result)
    return result


N=load('_recorder_plan_producer',HERE/'produce_run_plan.py')
P,A,B,G=N.P,N.A,N.B,N.G
require=A.require


class Journal:
    """Keep durable attempt evidence, including on failure. Never erase it."""
    def __init__(self,root,namespace='recording'):
        require(B._primitive('ID',namespace),'invalid_record_namespace')
        self.namespace=namespace
        self.root=G.checked_directory(root);self.helpers=G._fixture_helpers()
        self.parent=G.IO._directory_fd(self.root);self.fd=None;self.work=None;self.owned={};self.total=0
        try:self.fd,self.identity=G._reserve(self.parent,namespace,self.helpers)
        except BaseException:
            os.close(self.parent);raise

    def put(self,name,raw,role='source'):
        require(type(name) is str and name==os.path.basename(name) and name not in ('','.','..'),'unsafe_record_name')
        data=raw if type(raw) is bytes else A.canonical(raw)+b'\n'
        self.total+=len(data)
        require(len(data)<=A.MAX_BYTES and self.total<=P.MAX_TOTAL_BYTES,'record_output_limit')
        require(self.helpers._identity(os.stat(self.namespace,dir_fd=self.parent,follow_symlinks=False))==self.identity,
                'record_output_changed')
        self.helpers._write_bytes(self.fd,name,data,self.owned)
        G._verify_at(self.fd,name,data,self.owned[name],self.helpers)
        os.fsync(self.fd)
        return N.artifact(self.namespace+'/'+name,data,role)

    def close(self):
        if self.work is not None:os.close(self.work)
        os.close(self.fd);os.close(self.parent)

    def reserve_work(self):
        self.work,self.work_identity=G._reserve(self.fd,'work',self.helpers)


def read_native_graph(job,sources):
    selected=job['bindings']['native_input_manifest']
    parent=Path(selected['relative_path']).parent
    manifest=P.C.parse(sources.read(selected,'input','native_input'))
    def read_file(file):
        require(B._relative(file['relative_path']),'unsafe_native_input_path')
        return sources.read(dict(file,relative_path=(parent/file['relative_path']).as_posix(),role='source'),'input','source')
    if manifest.get('protocol')=='p1-input-manifest-v1':
        read_file(manifest['feature_file'])
        path=parent/'dynamics-manifest.json'
        raw=G.bounded_read(sources.roots['input']/path)
        manifest=P.C.parse(read_file(dict(relative_path='dynamics-manifest.json',raw_sha256=A.sha(raw),size_bytes=len(raw))))
    require(manifest.get('protocol')=='dynamics-input-manifest-v2','native_manifest_protocol_mismatch')
    for file in manifest['files']:read_file(file)


def fixed_argv(job,sources,native_cli):
    b=job['bindings']
    native=P.C.parse(sources.read(b['native_input_manifest'],'input','native_input'))
    flag={'p1-input-manifest-v1':'--p1-manifest','dynamics-input-manifest-v2':'--manifest'}.get(native.get('protocol'))
    require(flag is not None,'native_manifest_protocol_mismatch')
    return [str(native_cli),'run',flag,str(sources.path(b['native_input_manifest'],'input','native_input')),
        '--config',str(sources.path(b['native_config_raw'],'input','config')),
        '--registry',str(sources.path(b['registry_raw'],'input','registry')),
        '--out',str(sources.roots['output']/job['output_relative_dir'])]


def preflight(plan_raw,index_raw,projection,analysis,schedule,dispatch_ref,trusted_sha,roots,native_cli,
              final_inputs,owner_sha):
    A.validate_plan(analysis)
    plan,index=B.parse_raw(plan_raw),B.parse_raw(index_raw)
    B.validate_typed('RunPlan',plan);B.validate_plan_values(plan)
    B.validate_typed('RunIndex',index);B.validate_index_values(index);B.validate_index_plan(index,plan,plan_raw)
    sources=P.Sources(roots)
    require(sources.read(index['plan'],'input','plan')==plan_raw,'execution_plan_source_mismatch')
    require(all(row['status']=='not_run' for row in index['rows']),'recorder_unexecuted_index_required')
    retries=projection.get('protocol')=='exp03-native-projection-plan-v3'
    require(retries or len(index['rows'])==len(plan['jobs']),'recorder_one_attempt_per_job_required')
    dispatch_raw=sources.read(dispatch_ref,'evidence','dispatch');A.hash_value(trusted_sha)
    require(A.sha(dispatch_raw)==trusted_sha,'trusted_execution_dispatch_mismatch')
    dispatch=A.parse(dispatch_raw)
    names='protocol scope plan_raw_sha256 index_raw_sha256 projection_sha256 native_cli_sha256 attempts timeout_ns'
    if dispatch.get('protocol') in ('exp02-native-execution-dispatch-v2','exp02-native-execution-dispatch-v3'):names+=' artifact_namespace'
    A.fields(dispatch,names)
    require(dispatch['protocol'] in ('exp02-native-execution-dispatch-v1','exp02-native-execution-dispatch-v2','exp02-native-execution-dispatch-v3') and
            dispatch['scope']=='offline_native_fixture_only','invalid_execution_dispatch')
    require(B._primitive('ID',dispatch.get('artifact_namespace','recording')),'invalid_record_namespace')
    require(dispatch['plan_raw_sha256']==A.sha(plan_raw) and dispatch['index_raw_sha256']==A.sha(index_raw) and
            dispatch['projection_sha256']==A.digest(projection) and
            dispatch['native_cli_sha256']==projection['native_cli_sha256'],'execution_dispatch_binding_mismatch')
    require(type(dispatch['timeout_ns']) is int and 1<=dispatch['timeout_ns']<=86400000000000,'invalid_execution_timeout')
    expected=[dict(job_id=row['job_id'],attempt_id=row['attempt_id']) for row in index['rows']]
    if retries:
        require(dispatch['protocol']=='exp02-native-execution-dispatch-v3','retry_dispatch_required')
        require(P.ARCHIVE.supported(),'atomic_attempt_archive_unsupported')
        require(type(dispatch['attempts']) is list and len(dispatch['attempts'])==len(expected),'execution_attempt_dispatch_mismatch')
        for declared,pair in zip(dispatch['attempts'],expected):
            A.fields(declared,'job_id attempt_id timeout_ns')
            require({k:declared[k] for k in pair}==pair,'execution_attempt_dispatch_mismatch')
            require(type(declared['timeout_ns']) is int and 1<=declared['timeout_ns']<=dispatch['timeout_ns'],
                    'invalid_execution_timeout')
        require(dispatch['artifact_namespace']==projection['output_archive']['artifact_namespace'],'attempt_archive_namespace_mismatch')
    else:
        require(dispatch['protocol']!='exp02-native-execution-dispatch-v3','unexpected_retry_dispatch')
        require(dispatch['attempts']==expected,'execution_attempt_dispatch_mismatch')
    final=P.verify_final_inputs(final_inputs,owner_sha,analysis,schedule,plan,plan_raw,projection,sources)
    slots,_=P.validate_projection(projection,analysis,schedule,plan_raw,index_raw,plan,index,final)
    P.grades(projection,sources,analysis,schedule,index)
    queries,_,_=A.validate_schedule(analysis,schedule)
    labels,originals,_=P.source_gold(projection,sources,queries)
    sources.read(plan['planning_dispatch'],'evidence','dispatch')
    budgets={}
    for job in plan['jobs']:
        require(job['native_argv']==fixed_argv(job,sources,native_cli),'execution_argv_mismatch')
        require(slots[job['job_id']]['attempt_order']==[r['attempt_id'] for r in index['rows'] if r['job_id']==job['job_id']],
                'execution_attempt_dispatch_mismatch')
        for name,(root,role) in N.OWNER_ROLES.items():
            ref=job['bindings'][name]
            if ref is not None:sources.read(ref,root,role)
        for name,role in N.RULE_ROLES.items():sources.read(job['common'][name],'input',role)
        budgets[job['job_id']]=P.budgets(job,sources,queries,labels,originals,projection)
        for name,role in [('candidate_file','candidates'),('events_file','events'),('features_file','features'),('probe_file','probes')]:
            sources.read(job['common'][name],'input',role)
        read_native_graph(job,sources)
        output=sources.roots['output']/job['output_relative_dir']
        if retries:
            require(not (sources.roots['output']/dispatch['artifact_namespace']).exists() and
                    not (sources.roots['output']/dispatch['artifact_namespace']).is_symlink(),'execution_output_exists')
        else:
            G.checked_directory(output.parent)
            require(not output.exists() and not output.is_symlink(),'execution_output_exists')
    sources.recheck()
    return plan,index,sources,queries,budgets,dispatch,final


def archive_output(job,row,sources,journal):
    """Preserve an output from the recorder's exclusively reserved work root.

    Called before this row is persisted. Earlier checkpoints are never edited.
    Cached source references are relocated with the identical raw bytes.
    """
    original=job['output_relative_dir']
    target=P.ARCHIVE.relative_output(journal.namespace,job['job_id'],row['attempt_id'])
    output=sources.roots['output']/original
    require(original==journal.namespace+'/work/'+job['job_id'] and journal.work is not None,
            'attempt_work_namespace_mismatch')
    require(journal.helpers._identity(os.stat('work',dir_fd=journal.fd,follow_symlinks=False))==journal.work_identity,
            'attempt_work_directory_changed')
    parent=journal.work
    descriptor=None
    try:
        descriptor=os.open(output.name,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=parent)
        identity=journal.helpers._identity(os.fstat(descriptor))
        require(stat.S_ISDIR(os.fstat(descriptor).st_mode),'unsafe_attempt_output')
        require(journal.helpers._identity(os.stat(journal.namespace,dir_fd=journal.parent,follow_symlinks=False))==journal.identity,
                'record_output_changed')
        # All registered native bytes must still match immediately before
        # the atomic move. A replaced directory must never be reused.
        sources.recheck()
        require(journal.helpers._identity(os.stat(output.name,dir_fd=parent,follow_symlinks=False))==identity,
                'attempt_output_changed')
        name=Path(target).name
        P.ARCHIVE.move_no_replace(parent,output.name,journal.fd,name)
        require(journal.helpers._identity(os.stat(name,dir_fd=journal.fd,follow_symlinks=False))==identity,
                'attempt_output_changed')
        os.fsync(parent);os.fsync(journal.fd)
    finally:
        if descriptor is not None:os.close(descriptor)
    for key in list(sources.raw):
        root,path=key
        if root=='output' and path.startswith(original+'/'):
            renamed=(root,target+path[len(original):])
            require(renamed not in sources.raw,'attempt_archive_collision')
            sources.raw[renamed]=sources.raw.pop(key);sources.identities[renamed]=sources.identities.pop(key)
    refs=[ref for ref in [row['native_completion_manifest'],*(p['prediction_file'] for p in row['policy_rows'])] if ref is not None]
    for ref in refs:
        require(ref['relative_path'].startswith(original+'/'),'native_output_binding_mismatch')
        ref['relative_path']=target+ref['relative_path'][len(original):]
    sources.recheck()
    return target


def observed_costs(row,analysis,source_ref,wall_ns):
    costs=[dict(measurement_id='c'+A.digest([row['job_id'],row['attempt_id'],'wall'])[:40],
        phase='whole_run',resource='wall_time',allocation='whole_run_nonadditive',policy_id=None,
        value_decimal=str(wall_ns),unit='nanosecond',basis='observed',missing_reason=None,
        source_artifact=source_ref,price_artifact=None)]
    for requirement in analysis['cost_requirements']:
        if requirement==dict(phase='whole_run',resource='wall_time',allocation='whole_run_nonadditive'):continue
        for policy in B.ENUMS['RequiredPolicyID'] if requirement['allocation']=='policy_incremental' else [None]:
            costs.append(dict(measurement_id='c'+A.digest([row['job_id'],row['attempt_id'],requirement,policy])[:40],
                **requirement,policy_id=policy,value_decimal=None,unit=B.COST_UNITS[requirement['resource']],
                basis='missing',missing_reason='not_measured',source_artifact=None,price_artifact=None))
    return costs


def capture(args,verifier,remaining):
    env={'PATH':os.defpath,'GOMAXPROCS':'4'}
    if 'HOME' in os.environ:env['HOME']=os.environ['HOME']
    with tempfile.TemporaryFile() as stdout,tempfile.TemporaryFile() as stderr:
        begin=time.monotonic_ns();started=False;timed_out=False;interrupted=False;exit_code=None
        try:
            process=subprocess.Popen(args,executable=str(verifier.executable),stdin=subprocess.DEVNULL,
                stdout=stdout,stderr=stderr,env=env)
            started=True
            try:exit_code=process.wait(timeout=remaining)
            except subprocess.TimeoutExpired:
                timed_out=True;process.kill();exit_code=process.wait()
            except BaseException as issue:
                process.kill();exit_code=process.wait()
                if isinstance(issue,KeyboardInterrupt):interrupted=True
                else:raise
        except OSError:pass
        finish=time.monotonic_ns()
        stdout_size,stderr_size=stdout.tell(),stderr.tell()
        cap=(A.MAX_BYTES-4096)//2
        oversized=stdout_size>cap or stderr_size>cap
        stdout.seek(0);stderr.seek(0)
        out=stdout.read(cap);err=stderr.read(cap)
    return dict(begin=begin,finish=finish,started=started,timeout=timed_out,interrupted=interrupted,exit=exit_code,
                stdout=out,stderr=err,stdout_size=stdout_size,stderr_size=stderr_size,oversized=oversized)


def complete_row(job,row,sources):
    status=A.parse(sources.read(row['native_stdout_status'],'output','stdout_status'))
    require(status.get('status') in P.COMPLETE,'native_execution_not_completed')
    row['status']=status['status']
    output=job['output_relative_dir']
    def output_ref(path,role):
        raw=G.bounded_read(sources.roots['output']/path)
        ref=N.artifact(path,raw,role);sources.read(ref,'output',role)
        return ref,raw
    row['native_completion_manifest'],_=output_ref(output+'/manifest.json','completion_manifest')
    registry=P.C.decode(sources.read(job['bindings']['registry_raw'],'input','registry'),'PolicyRegistryV2')
    for policy in job['policy_order']:
        ref,raw=output_ref(output+'/predictions/'+policy+'.json','prediction')
        run=P.C.decode(raw,'PredictionRunV2');P.C._run(run,registry)
        failed=sum(v['status']=='failed' for v in run['predictions']);recorded=len(run['predictions'])
        planned=len(job['common']['expected_schedule'])
        require(recorded<=planned,'native_denominator_mismatch')
        row['policy_rows'].append(dict(policy_id=policy,policy_version=run['policy_version'],prediction_file=ref,
            planned_rows=planned,recorded_rows=recorded,successful_rows=recorded-failed,failed_rows=failed,
            missing_rows=planned-recorded,fallback_rows=0))
    return status


def record(plan_raw,index_raw,projection,analysis,schedule,dispatch_ref,trusted_dispatch_sha,roots,native_cli,
           final_inputs=None,trusted_owner_sha=None):
    plan,index,sources,queries,budgets,dispatch,final=preflight(plan_raw,index_raw,projection,analysis,schedule,
        dispatch_ref,trusted_dispatch_sha,roots,native_cli,final_inputs,trusted_owner_sha)
    deadline=time.monotonic()+dispatch['timeout_ns']/1000000000
    verifier=P.NativeVerifier(native_cli,projection,deadline)
    journal=None
    try:
        journal=Journal(sources.roots['output'],dispatch.get('artifact_namespace','recording'))
        if projection['protocol']=='exp03-native-projection-plan-v3':journal.reserve_work()
        clock='process_'+os.urandom(20).hex();started=0;verified=0;verification_calls=0;error=None
        journal.put('started.json',dict(protocol='exp02-native-recording-started-v1',plan_raw_sha256=A.sha(plan_raw),
            original_unexecuted_index_sha256=A.sha(index_raw),
            dispatch_sha256=trusted_dispatch_sha,clock_domain=clock,scope='offline_native_fixture_only',
            execution_image_sha256=A.sha(verifier.raw),argv_image_binding='unchanged_plan_argv_with_private_pinned_executable_image'))
        REF_phase=dict(status='not_required',successful_replay_reports=0,native_process_count='not_measured_here')
        if final is not None:
            journal.put('REF-validation-started.json',dict(protocol='exp02-native-REF-validation-started-v1',
                dispatch_sha256=trusted_dispatch_sha,attempt_process_count='not_measured_here'))
            try:
                _,evidence,reports=P.resolve_REF_selection(plan,final_inputs,sources,roots,native_cli,verifier.deadline)
                for name,raw in reports.items():journal.put('REF-'+A.digest(name)+'.json',raw)
                journal.put('REF-validation-evidence.json',evidence)
                REF_phase.update(status='replay_verified',successful_replay_reports=len(reports))
            except (A.AnalysisError,P.C.ContractErrorV2,B.Exp02Error,G.ExpError,G.IO.ContractError,OSError) as issue:
                error='REF_validation_failed';REF_phase.update(status='failed',code=getattr(issue,'code','file_io_error'))
                journal.put('REF-validation-error.json',REF_phase)
        jobs={j['job_id']:j for j in plan['jobs']}
        retries=projection['protocol']=='exp03-native-projection-plan-v3'
        for i,row in enumerate(index['rows']):
            if error is not None or time.monotonic()>=verifier.deadline:break
            try:
                sources.recheck()
                require(G.bounded_read(verifier.original)==verifier.raw,'native_executable_changed')
            except (A.AnalysisError,G.ExpError):
                error='source_changed';break
            job=jobs[row['job_id']]
            output=sources.roots['output']/job['output_relative_dir']
            if output.exists() or output.is_symlink():
                error='execution_output_conflict';break
            row['execution_dispatch']=dispatch_ref
            row['actual_argv']=list(job['native_argv']);row['execution_clock_domain']=clock
            prefix='a'+A.digest([row['job_id'],row['attempt_id']])[:40]
            journal.put(prefix+'-dispatch.json',dict(protocol='exp02-native-attempt-dispatching-v1',
                state='dispatching_not_a_start_receipt',job_id=row['job_id'],attempt_id=row['attempt_id'],
                execution_dispatch_sha256=trusted_dispatch_sha,argv_sha256=A.digest(row['actual_argv']),
                clock_domain=clock,at_ns=time.monotonic_ns()))
            remaining=verifier.deadline-time.monotonic()
            if retries:remaining=min(remaining,dispatch['attempts'][i]['timeout_ns']/1000000000)
            result=capture(row['actual_argv'],verifier,remaining)
            started+=result['started'];row.update(started_at_ns=result['begin'],finished_at_ns=result['finish'],process_exit=result['exit'])
            rawlog=(b'ARKISM_NATIVE_STDIO_V1\n'+str(len(result['stdout'])).encode()+b'\n'+result['stdout']+
                    str(len(result['stderr'])).encode()+b'\n'+result['stderr'])
            row['raw_log']=journal.put(prefix+'-stdio.raw',rawlog,'raw_log')
            row['native_stdout_status']=journal.put(prefix+'-stdout.json',result['stdout'],'stdout_status')
            source=journal.put(prefix+'-cost.json',dict(protocol='exp02-native-attempt-timing-v1',clock_domain=clock,
                started_at_ns=result['begin'],finished_at_ns=result['finish'],elapsed_ns=result['finish']-result['begin'],
                policy_process_started=result['started'],process_exit=result['exit'],timed_out=result['timeout'],
                interrupted=result['interrupted'],
                raw_log=row['raw_log'],transport_truncated=result['oversized'],stdout_size_bytes=result['stdout_size'],
                stderr_size_bytes=result['stderr_size'],execution_image_sha256=A.sha(verifier.raw)), 'cost_source')
            row['cost_measures']=observed_costs(row,analysis,source,result['finish']-result['begin'])
            if any(c['basis']=='missing' for c in row['cost_measures']):row['deviations'].append(dict(code='cost_missing',evidence=source))
            row['status']='internal_error'
            if result['interrupted']:
                error='interrupted';row['deviations'].append(dict(code='unsupported_condition',evidence=row['raw_log']))
            elif result['timeout']:
                row['status']='timeout';row['deviations'].append(dict(code='timeout',evidence=row['raw_log']))
            elif not result['started']:
                row['deviations'].append(dict(code='unsupported_condition',evidence=row['raw_log']))
            elif result['oversized'] or result['stderr'] or result['exit']!=0:
                row['status']='input_rejected' if result['exit'] else 'stdout_transport_error'
            elif result['started']:
                try:
                    complete_row(job,row,sources)
                    verification_calls+=1
                    report,raw=verifier.verify(job,row,sources,_before_archive=retries)
                    P.verified_rows(report,job,row,sources,budgets[row['job_id']],queries)
                    if retries:
                        archive_output(job,row,sources,journal)
                        verification_calls+=1
                        report,raw=verifier.verify(job,row,sources)
                        P.verified_rows(report,job,row,sources,budgets[row['job_id']],queries)
                    verified+=1
                    journal.put(prefix+'-verification.json',raw,'source')
                    if row['status']=='completed_with_failures':row['deviations'].append(dict(code='native_failed_row',evidence=row['native_completion_manifest']))
                except (A.AnalysisError,P.C.ContractErrorV2,B.Exp02Error,G.ExpError,G.IO.ContractError,OSError) as issue:
                    detail=journal.put(prefix+'-verification-error.json',dict(protocol='exp02-native-verification-failure-v1',
                        code=getattr(issue,'code','file_io_error')))
                    row['status']='internal_error';row['deviations'].append(dict(code='input_binding_changed',evidence=detail))
            if retries and row['status'] not in P.COMPLETE and output.exists() and not output.is_symlink():
                # Partial files are from this reserved work directory, after
                # the child has been killed/reaped. Preserve them verbatim;
                # their existence never makes this failed attempt complete.
                try:
                    target=archive_output(job,row,sources,journal)
                    retained=journal.put(prefix+'-failed-publication.json',dict(protocol='exp02-native-failed-output-v1',
                        job_id=row['job_id'],attempt_id=row['attempt_id'],relative_directory=target,
                        original_status=row['status'],output_completeness='not_asserted',
                        quality_accepted=False))
                    row['deviations'].append(dict(code='unsupported_condition',evidence=retained))
                except (A.AnalysisError,P.C.ContractErrorV2,B.Exp02Error,G.ExpError,G.IO.ContractError,OSError) as issue:
                    journal.put(prefix+'-failed-publication-error.json',dict(protocol='exp02-native-retention-error-v1',
                        code=getattr(issue,'code','file_io_error')))
                    error=error or 'execution_output_conflict'
            if retries and (output.exists() or output.is_symlink()):
                # Unverified/partial publication is preserved in place. It
                # cannot be overwritten, promoted or silently discarded to
                # make a later declared retry appear successful.
                error=error or 'execution_output_conflict'
            journal.put('checkpoint_'+str(i)+'.json',dict(protocol='exp02-native-attempt-checkpoint-v1',
                original_index_raw_sha256=A.sha(index_raw),plan_raw_sha256=A.sha(plan_raw),row=row))
        try:
            sources.recheck();require(G.bounded_read(verifier.original)==verifier.raw,'native_executable_changed')
        except (A.AnalysisError,G.ExpError):
            error='source_changed'
            for row in index['rows']:
                if row['status'] in P.COMPLETE:
                    row['status']='internal_error';row['deviations'].append(dict(code='source_changed',evidence=None))
        B.validate_typed('RunIndex',index);B.validate_index_values(index);B.validate_index_plan(index,plan,plan_raw)
        raw_index=B.compact(index);index_ref=journal.put('index.json',raw_index)
        updated=dict(projection,run_index_raw_sha256=A.sha(raw_index))
        journal.put('projection.json',updated)
        report=dict(protocol='exp02-native-recording-result-v1',scope='offline_native_fixture_only',
            index=index_ref,planned_native_run_processes_started=started,native_verification_calls=verification_calls,
            native_replays_verified=verified,REF_validation_phase=REF_phase,
            attempts=[dict(job_id=r['job_id'],attempt_id=r['attempt_id'],status=r['status']) for r in index['rows']],
            source_error=error,product_authority_verified=False,scientific_approval_verified=False,
            human_authorization_authenticated=False,query_cost_allocation='unknown_not_divided',
            version_scope='immutable_predeclared_attempt_archives_original_wire' if retries else 'one_declared_attempt_per_job_original_wire')
        journal.put('result.json',report)
        return index,updated,report
    finally:
        if journal is not None:journal.close()
        verifier.close()


def main(argv=None):
    parser=A.Parser(allow_abbrev=False)
    for name in ('run-plan','run-index','projection-plan','analysis-plan','schedule','dispatch','trusted-dispatch-sha256',
                 'input-root','evidence-root','output-root','native-cli'):
        parser.add_argument('--'+name,required=True,action=A.Once)
    for name in ('final-inputs','trusted-owner-review-sha256'):parser.add_argument('--'+name,action=A.Once)
    try:
        args=parser.parse_args(argv)
        require(bool(args.final_inputs)==bool(args.trusted_owner_review_sha256),'final_review_arguments_required')
        names=('run_plan','run_index','projection_plan','analysis_plan','schedule','dispatch')
        paths={name:Path(getattr(args,name)) for name in names}
        if args.final_inputs:paths['final_inputs']=Path(args.final_inputs)
        raw={name:G.bounded_read(path) for name,path in paths.items()}
        roots=dict(input=args.input_root,evidence=args.evidence_root,output=args.output_root)
        evidence=G.checked_directory(args.evidence_root)
        dispatch_ref=N.artifact(paths['dispatch'].relative_to(evidence).as_posix(),raw['dispatch'],'dispatch')
        _,_,report=record(raw['run_plan'],raw['run_index'],A.parse(raw['projection_plan']),A.parse(raw['analysis_plan']),
            A.parse(raw['schedule']),dispatch_ref,args.trusted_dispatch_sha256,roots,args.native_cli,
            A.parse(raw['final_inputs']) if args.final_inputs else None,args.trusted_owner_review_sha256)
        require(all(G.bounded_read(paths[name])==data for name,data in raw.items()),'record_request_changed')
        print(json.dumps(report));return 130 if report['source_error']=='interrupted' else 0
    except (A.AnalysisError,B.Exp02Error,G.ExpError,G.IO.ContractError,P.C.ContractErrorV2) as error:code=error.code
    except OSError:code='file_io_error'
    except Exception:code='internal_error'
    print(json.dumps(dict(protocol='exp02-native-recording-error-v1',code=code,path='$')))
    return 2


if __name__=='__main__':
    raise SystemExit(main())

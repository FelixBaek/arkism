"""Label-free EXP01 observed source views -> explicit synthetic native bundles.

Source visibility never grants product authority or independent qualification.
Admissions, synthetic feature values and optional qualifications are separate
explicit inputs. No gold, split labels or future query is accepted as input.
"""
import argparse
import copy
import importlib.util
import json
import os
import queue
import subprocess
import tempfile
import threading
import time
from pathlib import Path

HERE=Path(__file__).resolve().parent


def load(name,path):
    spec=importlib.util.spec_from_file_location(name,path)
    result=importlib.util.module_from_spec(spec);spec.loader.exec_module(result)
    return result


B=load('_materialize_exp02',HERE/'build_run_plan.py')
C=load('_materialize_native_codec',HERE.parents[1]/'dynamics_compare.py')
G,V=B.G,B.V
MAX_BYTES=67108864


class MaterializationError(Exception):
    def __init__(self,code):self.code=code;super().__init__(code)


def require(condition,code):
    if not condition:raise MaterializationError(code)


def source(call,*args):
    try:return call(*args)
    except (B.Exp02Error,G.ExpError,G.IO.ContractError,C.ContractErrorV2) as error:
        raise MaterializationError('source_'+error.code) from None


def fields(value,names):
    require(type(value) is dict and set(value)==set(names.split()),'invalid_fields')


def sha(raw):return B.digest(raw)


def compact(value):return json.dumps(value,sort_keys=True,separators=(',',':'),ensure_ascii=True,allow_nan=False).encode()+b'\n'


def seal(kind,value,field,native_hash):
    result=copy.deepcopy(value)
    result[field]=native_hash(kind,result)
    return result


def refkey(ref):return tuple(ref[k] for k in ('origin','collection','id','revision','content_sha256'))


def ident(prefix,value):return prefix+sha(compact(value))[:40]


def feature_number(value):
    require(type(value) is str and len(value)<=32 and B._primitive('UnsignedDecimal',value),'invalid_feature_value')
    from decimal import Decimal
    parsed=Decimal(value)
    require(0<=parsed<=1,'invalid_feature_value')
    return float(parsed)


def validate_catalog(catalog,events_raw,query):
    fields(catalog,'protocol scope events_raw_sha256 admissions outcomes')
    require(catalog['protocol']=='exp02-synthetic-admission-v1' and catalog['scope']=='offline_fixture_only_not_product_authority' and
            catalog['events_raw_sha256']==sha(events_raw),'catalog_binding_mismatch')
    require(type(catalog['admissions']) is list and type(catalog['outcomes']) is list and
            len(catalog['admissions'])<=100000 and len(catalog['outcomes'])<=100000,'catalog_limit')
    admissions=[];seen=set()
    for row in catalog['admissions']:
        fields(row,'ref scope source_event_id activation_event_id admission trust context_cost_units effective_at_ns effective_basis '
                   'historical_body_available importance importance_rule_sha256')
        source(G.IO.validate_typed,'Ref',row['ref']);source(G.IO.validate_typed,'Scope',row['scope'])
        require(row['admission']=='explicit_synthetic_fixture' and type(row['historical_body_available']) is bool,'admission_required')
        source(G._typed,'ID',row['source_event_id'],'$');source(G._typed,'ID',row['activation_event_id'],'$')
        source(G._typed,'NS',row['effective_at_ns'],'$');source(G._typed,'Count',row['context_cost_units'],'$')
        require(row['effective_basis'] in ('source','explicit_synthetic_fixture'),'invalid_effective_basis')
        feature_number(row['trust'])
        if row['importance'] is not None:feature_number(row['importance'])
        source(G._typed,'SHA',row['importance_rule_sha256'],'$')
        key=(json.dumps(row['scope'],sort_keys=True),refkey(row['ref']))
        require(key not in seen,'duplicate_admission');seen.add(key)
        if row['scope']==query['scope']:admissions.append(row)
    outcomes={}
    for row in catalog['outcomes']:
        fields(row,'source_event_id task evidence_template attribution strength')
        source(G._typed,'ID',row['source_event_id'],'$')
        require(row['source_event_id'] not in outcomes,'duplicate_qualification')
        fields(row['task'],'task_ref run_ref trial_ref checker_sha256 rule_sha256 qualification task_sha256')
        # The authoritative Go codec validates the nested native TaskBinding
        # when sealing the event. This catalog preflight never fabricates it.
        fields(row['evidence_template'],'refs target task_sha256 dimension rule_sha256 independence source_status')
        require(row['attribution'] in ('memory','tool','retrieval','evaluator','unattributable'),'invalid_attribution')
        if row['strength'] is not None:feature_number(row['strength'])
        outcomes[row['source_event_id']]=row
    return admissions,outcomes


def prepare_source_view(events_raw,query_raw,catalog_raw,registry_raw,plan_raw):
    inputs=(events_raw,query_raw,catalog_raw,registry_raw,plan_raw)
    require(all(type(v) is bytes and len(v)<=MAX_BYTES for v in inputs) and sum(map(len,inputs))<=268435456,'input_limit')
    query=source(G.decode_typed,query_raw,'QueryPlan')
    all_events=source(V._rows,events_raw,'PlanEvent')
    catalog=source(B.parse_raw,catalog_raw);plan=source(B.parse_raw,plan_raw)
    fields(plan,'protocol dataset_id run_id baseline_bundle_sha256 final_k context_budget lane_rule')
    require(plan['protocol']=='exp02-native-materialization-plan-v1' and
            plan['lane_rule']=='character_bigram_overlap_then_full_ref','invalid_materialization_plan')
    for name in ('dataset_id','run_id'):source(G._typed,'ID',plan[name],'$')
    source(G._typed,'SHA',plan['baseline_bundle_sha256'],'$');source(G._typed,'Count',plan['final_k'],'$')
    source(G.IO.validate_typed,'ContextBudget',plan['context_budget'])
    registry=source(C.decode,registry_raw,'PolicyRegistryV2');source(C._registry,registry)
    require([e['policy_id'] for e in registry['entries']]==B.ENUMS['RequiredPolicyID'],'required_policy_mismatch')
    dynamic=next(e['dynamics'] for e in registry['entries'] if e['policy_id']=='B2')
    clock=dynamic['clock_domain']
    require(all(e['dynamics'] is None or e['dynamics']['clock_domain']==clock for e in registry['entries']),'native_clock_mismatch')
    stream_events=[e for e in all_events if e['stream_id']==query['stream_id']]
    require(bool(stream_events),'source_stream_missing')
    # Preserve recorded plan order. Do not sort a malformed stream into an
    # apparently valid transcript or infer an unobserved future cutoff.
    current=source(V._query_prefix,query,stream_events)
    require(len({e['event_id'] for e in current})==len(current) and len({e['plan_seq'] for e in current})==len(current),'duplicate_source_event')
    require(all(e['clock_domain']==query['clock_domain'] for e in current),'source_clock_mismatch')
    require(all(a['available_at_ns']<=b['available_at_ns'] for a,b in zip(current,current[1:])),'source_time_reversal')
    for e in current:source(V._event_shape,e)
    if current:source(V._stream_events,current)
    view_at=query['at_ns'] if query['request_mode']=='current' else query['requested_as_of_ns']
    require(type(view_at) is int and 0<=view_at<=query['at_ns'],'invalid_requested_view')
    # Current withdrawals remain fences even for an older observed source view.
    withdrawn={refkey(e['target'])[:3] for e in current if e['kind'] in ('material_deleted','scope_changed')}
    visible=[e for e in current if e['available_at_ns']<=view_at]
    admissions,qualifications=validate_catalog(catalog,events_raw,query)
    by_activation={};by_ref={}
    for row in admissions:
        by_activation.setdefault(row['activation_event_id'],[]).append(row)
        by_ref[refkey(row['ref'])]=row
    native=[];active={};active_identity={};known={};source_records={};features=[];mapping=[]
    def emit(e,kind,target,suffix,reason):
        seq=len(native)+1
        value=dict(protocol='dynamics-event-v2',stream_id=query['stream_id'],event_id=ident('e',[e['event_id'],suffix]),
            transport_attempt_id=ident('d',[e['event_id'],suffix]),
            key=dict(scope=query['scope'],target=target,dimension='work_task',contract_sha256=B.DYNAMICS_SHA,config_sha256=dynamic['config_sha256']),
            kind=kind,clock_domain=clock,observed_at_ns=e['source_at_ns'],arrived_at_ns=e['arrived_at_ns'],
            accepted_at_ns=e['available_at_ns'],accepted_seq=seq,task=None,evidence=None,outcome='not_applicable',
            attribution='not_applicable',strength=None,reason=reason,payload_sha256='0'*64)
        if kind=='outcome':
            outcome=e['outcome'];require(outcome is not None,'missing_source_outcome')
            value.update(outcome=outcome['judgment'],attribution='unattributable',reason='unknown_outcome')
            qualified=qualifications.get(e['event_id'])
            if qualified is not None:
                task=qualified['task'];evidence=copy.deepcopy(qualified['evidence_template'])
                require(task['task_ref']['id']==e['task_id'] and task['checker_sha256']==outcome['source_rule_sha256'] and
                        task['rule_sha256']==dynamic['evidence_rule_sha256'],'qualification_source_mismatch')
                evidence.update(available_accepted_seq=seq,binding_sha256='0'*64)
                require(task['qualification']=='synthetic_independent' and evidence['independence']=='synthetic_independent' and
                        evidence['source_status']=='available' and evidence['dimension']=='work_task','qualification_scope_mismatch')
                require(evidence['target']==target and evidence['task_sha256']==task['task_sha256'] and
                        evidence['rule_sha256']==task['rule_sha256'],'qualification_source_mismatch')
                value.update(task=task,evidence=evidence,
                    attribution=qualified['attribution'],strength=None if qualified['strength'] is None else feature_number(qualified['strength']))
                value['reason']=('passed_independent' if outcome['judgment']=='passed' else 'failed_attributable' if
                    outcome['judgment']=='failed' and qualified['attribution']=='memory' else 'failed_unattributable' if
                    outcome['judgment']=='failed' else 'unknown_outcome')
        native.append(value)
        return seq
    for e in visible:
        start=len(native)
        key=refkey(e['target']) if e['target'] is not None else None
        if e['kind']=='material_available':
            require(e['source_payload'] is not None and e['target'] is not None,'missing_source_payload')
            payload=e['source_payload']
            require(sha((payload['text']+'\n'+payload['summary']).encode())==e['target']['content_sha256'],'source_content_hash_mismatch')
            if key in source_records:require(source_records[key]['source_payload']==payload,'source_payload_conflict')
            source_records[key]=e
        if e['kind'] in ('correction_committed','material_deleted','scope_changed') and key in known:
            emit(e,'invalidate',e['target'],'invalidate','source_revoked' if e['kind']=='scope_changed' else 'source_deleted')
            active[key]=False
            active_identity.pop(key[:3],None)
        for admission in sorted(by_activation.get(e['event_id'],[]),key=lambda r:refkey(r['ref'])):
            k=refkey(admission['ref'])
            require(k in source_records and source_records[k]['event_id']==admission['source_event_id'],'admission_source_not_visible')
            require((e['kind']=='material_available' and e['target']==admission['ref']) or
                    (e['kind']=='correction_committed' and admission['ref'] in e['related_refs']),'admission_event_mismatch')
            require(k not in known,'duplicate_native_activation')
            require(k[:3] not in active_identity,'native_revision_overlap')
            payload=source_records[k]['source_payload']
            require((payload['effective_at_ns'] is None and admission['effective_basis']=='explicit_synthetic_fixture') or
                    (admission['effective_basis']=='source' and admission['effective_at_ns']==payload['effective_at_ns']), 'effective_source_mismatch')
            seq=emit(e,'activate',admission['ref'],'activate_'+admission['ref']['id']+'_'+str(admission['ref']['revision']),'activation')
            known[k]=dict(admission=admission,payload=payload,activation_seq=seq);active[k]=True;active_identity[k[:3]]=k
            if admission['importance'] is not None:
                ref=admission['ref'];src=dict(namespace='fixture',collection='sources',id=ref['id'],revision=ref['revision'],content_sha256=ref['content_sha256'])
                feature=dict(feature_id=ident('f',ref),scope=query['scope'],target=ref,source=src,clock_domain=clock,
                    observed_at_ns=e['available_at_ns'],available_at_ns=e['available_at_ns'],available_accepted_seq=seq,
                    feature_name='importance',feature_version='explicit_fixture_v1',rule_sha256=admission['importance_rule_sha256'],
                    generator_config_sha256=sha(compact(admission)),producer_version='exp02_source_native_v1',input_refs=[src],
                    status='present',value=feature_number(admission['importance']),unit='dimensionless',missing_reason=None,feature_sha256='0'*64)
                features.append(feature)
        if e['kind'] in ('outcome_observed','exposure_observed') and key in known:
            emit(e,'outcome' if e['kind']=='outcome_observed' else 'exposure',e['target'],e['kind'],'exposure_only')
        mapping.append(dict(source_event_id=e['event_id'],source_plan_seq=e['plan_seq'],native_sequences=list(range(start+1,len(native)+1))))
    chosen={}
    for k,data in known.items():
        admission,payload=data['admission'],data['payload']
        if query['request_mode']=='historical' and not admission['historical_body_available']:continue
        eligible=active[k] and k[:3] not in withdrawn
        candidate=dict(scope=query['scope'],ref=admission['ref'],reasons=[],trust=feature_number(admission['trust']),
            updated_at_ns=payload['updated_at_ns'],observed_seq=data['activation_seq'],effective_at_ns=admission['effective_at_ns'],
            context_cost_units=admission['context_cost_units'],approved=True,active=eligible,deleted=k[:3] in withdrawn,
            source_verified=True,authority='data_only_untrusted')
        identity=k[:3]
        if identity not in chosen or (candidate['active'],candidate['ref']['revision'])>(chosen[identity]['active'],chosen[identity]['ref']['revision']):
            chosen[identity]=candidate
    def bigrams(text):return {text[i:i+2] for i in range(len(text)-1) if ' ' not in text[i:i+2]}
    query_terms=bigrams(query['query_text'])
    def overlap(candidate):return len(query_terms&bigrams(known[refkey(candidate['ref'])]['payload']['text']))
    lane=sorted(chosen.values(),key=lambda c:(-overlap(c),source(C.canonical,'RefV2',c['ref'])))
    for c in lane:
        if overlap(c):c['reasons']=['text_search']
    snapshot=dict(snapshot_id=query['query_id'],stream_id=query['stream_id'],scope=query['scope'],as_of_ns=view_at,
                  as_of_seq=len(native),lanes=[lane,[],[]])
    require(len(native)<=100000 and len(features)<=100000,'native_frame_resource_limit')
    source(G.IO.validate_typed,'CandidateSnapshot',snapshot)
    snapshot_sha=source(G.IO.canonical_sha256,'CandidateSnapshot',snapshot)
    return dict(query=query,plan=plan,registry=registry,registry_raw=registry_raw,dynamic=dynamic,clock=clock,view_at=view_at,
                events=native,features=features,snapshot=snapshot,snapshot_sha=snapshot_sha,mapping=mapping,
                source_inputs_sha256=dict(events=sha(events_raw),query=sha(query_raw),catalog=sha(catalog_raw),registry=sha(registry_raw),plan=sha(plan_raw)))


def materialize(events_raw,query_raw,catalog_raw,registry_raw,plan_raw,native_hash,native_program_sha256):
    source(G._typed,'SHA',native_program_sha256,'$')
    view=prepare_source_view(events_raw,query_raw,catalog_raw,registry_raw,plan_raw)
    query,plan,registry,dynamic=(view[k] for k in ('query','plan','registry','dynamic'))
    clock,view_at,snapshot,snapshot_sha=(view[k] for k in ('clock','view_at','snapshot','snapshot_sha'))
    native=[]
    for original in view['events']:
        event=copy.deepcopy(original)
        if event['evidence'] is not None:
            event['evidence']=seal('EvidenceBindingHashProjection',event['evidence'],'binding_sha256',native_hash)
        native.append(seal('EventLogicalProjectionV2',event,'payload_sha256',native_hash))
    features=[seal('ImportanceFeatureV1HashProjection',feature,'feature_sha256',native_hash) for feature in view['features']]
    mapping=view['mapping']
    probes=[]
    for entry in registry['entries']:
        cfgsha=entry['dynamics']['config_sha256'] if entry['dynamics'] is not None else dynamic['config_sha256']
        prefix=native_hash('StreamInitialPrefixProjectionV2',dict(stream_id=query['stream_id'],clock_domain=clock,
            scope=query['scope'],dimension='work_task',config_sha256=cfgsha))
        for original in native:
            event=copy.deepcopy(original);event['key']['config_sha256']=cfgsha
            event=seal('EventLogicalProjectionV2',event,'payload_sha256',native_hash)
            prefix=native_hash('StreamAcceptedStepProjectionV2',dict(previous_prefix_sha256=prefix,stream_id=query['stream_id'],
                clock_domain=clock,accepted_seq=event['accepted_seq'],accepted_at_ns=event['accepted_at_ns'],payload_sha256=event['payload_sha256']))
        probes.append(dict(protocol='dynamics-probe-v2',query_id=query['query_id'],stream_id=query['stream_id'],scope=query['scope'],
            dimension='work_task',at_ns=view_at,clock_domain=clock,cutoff_accepted_seq=len(native),candidate_snapshot_id=snapshot['snapshot_id'],
            candidate_snapshot_sha256=snapshot_sha,prefix_sha256=prefix,final_k=plan['final_k'],context_budget=plan['context_budget'],
            policy_id=entry['policy_id'],policy_version=entry['version']))
    features.sort(key=lambda f:source(C.canonical,'RefV2',f['target']))
    files={'events.json':compact(dict(protocol='dynamics-event-file-v2',stream_id=query['stream_id'],clock_domain=clock,
                                    config_sha256=dynamic['config_sha256'],events=native)),
        'candidates.json':compact(dict(contract_id='arkism-offline-b0-v1',snapshots=[snapshot])),
        'probes.json':compact(dict(protocol='dynamics-probe-file-v2',stream_id=query['stream_id'],clock_domain=clock,probes=probes)),
        'features.json':compact(dict(protocol='p1-feature-file-v1',feature_contract_sha256='b6e965bbfecb4a0f20365d11709ff168043293fe1c5b848b06bc886e95f5f0d8',features=features)),
        'registry.json':registry_raw}
    def role(role,name):return dict(role=role,relative_path=name,raw_sha256=sha(files[name]),size_bytes=len(files[name]))
    manifest=dict(protocol='dynamics-input-manifest-v2',dataset_id=plan['dataset_id'],stream_id=query['stream_id'],clock_domain=clock,
        baseline_bundle_sha256=plan['baseline_bundle_sha256'],policy_contract_sha256=B.DYNAMICS_SHA,
        files=[role(r,n) for r,n in [('events','events.json'),('probes','probes.json'),('candidates','candidates.json'),('registry','registry.json')]],manifest_sha256='0'*64)
    manifest=seal('DynamicInputManifestV2HashProjection',manifest,'manifest_sha256',native_hash);files['dynamics-manifest.json']=compact(manifest)
    wrapper=dict(protocol='p1-input-manifest-v1',base_dynamic_manifest_sha256=manifest['manifest_sha256'],
        feature_contract_sha256='b6e965bbfecb4a0f20365d11709ff168043293fe1c5b848b06bc886e95f5f0d8',feature_file=role('features','features.json'),manifest_sha256='0'*64)
    wrapper=seal('P1InputManifestV1HashProjection',wrapper,'manifest_sha256',native_hash);files['p1.json']=compact(wrapper)
    cfg=dict(protocol='dynamics-run-config-v2',run_id=plan['run_id'],input_manifest_sha256=wrapper['manifest_sha256'],
        registry_sha256=registry['registry_sha256'],policy_ids=[e['policy_id'] for e in registry['entries']],
        configs=[dict(policy_id=e['policy_id'],ranker=e['ranker'],dynamics=e['dynamics']) for e in registry['entries']],
        resource_limits=registry['resource_limits'],registry=registry,config_sha256='0'*64)
    files['config.json']=compact(seal('DynamicsRunConfigV2HashProjection',cfg,'config_sha256',native_hash))
    report=dict(protocol='exp02-source-native-materialization-v1',scope='explicit_synthetic_fixture_not_product_permission',
        source_query_sha256=source(G.semantic_sha,'QueryPlan',query),source_cutoff_plan_seq=query['cutoff_plan_seq'],
        source_query_at_ns=query['at_ns'],requested_as_of_ns=query['requested_as_of_ns'],native_at_ns=view_at,native_clock_domain=clock,
        native_cutoff_accepted_seq=len(native),native_candidate_snapshot_sha256=snapshot_sha,source_to_native=mapping,
        source_inputs_sha256=view['source_inputs_sha256'],native_hash_program_sha256=native_program_sha256,
        source_visibility_grants_authority=False,declared_outcome_auto_qualified=False,product_authority_verified=False,
        baseline_bundle_binding='explicit_digest_not_owner_attestation',historical_scope='observed_fixture_view_with_current_withdrawal_fence')
    files['materialization-report.json']=compact(report)
    require(all(len(raw)<=MAX_BYTES for raw in files.values()),'output_limit')
    return files


class NativeHashes:
    """One pinned offline Go process; authoritative codec, bounded replies/time."""
    def __init__(self,executable,expected_sha256,timeout=30):
        source(G._typed,'SHA',expected_sha256,'$')
        self.original=Path(executable);self.raw=source(G.bounded_read,self.original)
        require(sha(self.raw)==expected_sha256 and os.access(self.original,os.X_OK),'native_hash_program_mismatch')
        require(type(timeout) is int and 1<=timeout<=60,'invalid_native_hash_timeout')
        schema_raw=source(G.bounded_read,C.SCHEMA_PATH)
        require(sha(schema_raw)==B.DYNAMICS_SHA,'native_schema_mismatch')
        schema=json.loads(schema_raw)
        self.projections=schema['hash_projections'];self.models={**schema['models'],**schema['closed_inline_records']}
        self.raw_sha256=expected_sha256;self.requests=0
        self.directory=tempfile.TemporaryDirectory(prefix='arkism-native-hash-')
        target=Path(self.directory.name)/('native.exe' if os.name=='nt' else 'native')
        target.write_bytes(self.raw);target.chmod(0o500)
        env={'PATH':os.defpath,'GOMAXPROCS':'4'}
        if 'HOME' in os.environ:env['HOME']=os.environ['HOME']
        self.process=None
        try:
            self.process=subprocess.Popen([str(target),'hash-stream'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,env=env)
            self.deadline=time.monotonic()+timeout
            self.stop=threading.Event();self.replies=queue.Queue(maxsize=1)
            self.reader=threading.Thread(target=self._read,daemon=True);self.reader.start()
            self.watchdog=threading.Timer(timeout,self._kill);self.watchdog.daemon=True;self.watchdog.start()
        except BaseException:
            self._kill()
            if self.process is not None:self.process.wait(timeout=2)
            self.directory.cleanup();raise

    def _kill(self):
        if self.process is not None and self.process.poll() is None:
            self.process.kill()

    def _read(self):
        while not self.stop.is_set():
            try:reply=self.process.stdout.readline(4097)
            except (OSError,ValueError):reply=b''
            if len(reply)>4096 or not reply:reply=None
            while not self.stop.is_set():
                try:self.replies.put(reply,timeout=0.1);break
                except queue.Full:continue
            if reply is None:break

    def hash(self,kind,value):
        self.requests+=1
        require(self.requests<=1000000 and kind in self.projections,'native_hash_type_or_work_limit')
        model=self.projections[kind];allowed=self.models.get(model.get('source_type'),model)['fields']
        require(type(value) is dict and set(value)<=set(allowed) and set(model['fields'])<=set(value),'invalid_hash_projection')
        payload={k:value[k] for k in model['fields']}
        remaining=self.deadline-time.monotonic();require(remaining>0,'native_hash_timeout')
        try:
            self.process.stdin.write(compact(dict(type_name=kind,payload=payload)));self.process.stdin.flush()
            reply=self.replies.get(timeout=remaining)
        except (OSError,ValueError,queue.Empty):
            raise MaterializationError('native_hash_failed') from None
        require(reply is not None,'native_hash_failed')
        response=source(B.parse_raw,reply)
        require(type(response) is dict and response.get('status')=='hashed','native_hash_rejected')
        fields(response,'protocol status sha256')
        require(response['protocol']=='exp02-native-hash-v1','native_hash_reply_mismatch')
        source(G._typed,'SHA',response['sha256'],'$')
        return response['sha256']

    def close(self):
        try:
            try:self.process.stdin.close()
            except (OSError,ValueError):pass
            try:self.process.wait(timeout=2)
            except subprocess.TimeoutExpired:self._kill();self.process.wait(timeout=2)
            require(self.process.returncode==0,'native_hash_exit_failed')
            require(source(G.bounded_read,self.original)==self.raw,'native_hash_program_changed')
        finally:
            self.watchdog.cancel();self.stop.set();self.process.stdout.close();self.reader.join(timeout=1)
            self.directory.cleanup()

    def __enter__(self):return self
    def __exit__(self,kind,value,traceback):
        if kind is not None:
            self._kill()
            try:self.close()
            except (MaterializationError,OSError):pass
        else:self.close()


class Parser(argparse.ArgumentParser):
    def error(self,message):raise MaterializationError('invalid_arguments')


class Once(argparse.Action):
    def __call__(self,parser,namespace,value,option_string=None):
        require(getattr(namespace,self.dest) is None,'duplicate_argument')
        setattr(namespace,self.dest,value)


def main(argv=None):
    parser=Parser(allow_abbrev=False)
    for name in ('events','query','catalog','registry','plan','native-cli','native-cli-sha256','out'):parser.add_argument('--'+name,required=True,action=Once)
    try:
        args=parser.parse_args(argv)
        paths={n:Path(getattr(args,n)) for n in ('events','query','catalog','registry','plan')}
        raw={n:source(G.bounded_read,path) for n,path in paths.items()}
        with NativeHashes(args.native_cli,args.native_cli_sha256) as hashes:
            files=materialize(*(raw[n] for n in ('events','query','catalog','registry','plan')),hashes.hash,hashes.raw_sha256)
        require(all(source(G.bounded_read,paths[n])==data for n,data in raw.items()),'input_changed')
        P=load('_materializer_safe_publish',HERE.with_name('exp_03')/'project_native.py')
        try:P.publish(files,args.out)
        except P.A.AnalysisError as error:raise MaterializationError(error.code) from None
        print(json.dumps(dict(protocol='exp02-materialization-written-v1',outputs={n:sha(v) for n,v in files.items()},product_authority_verified=False)))
        return 0
    except MaterializationError as error:code=error.code
    except OSError:code='file_io_error'
    except Exception:code='internal_error'
    print(json.dumps(dict(protocol='exp02-materialization-error-v1',code=code,path='$')))
    return 2


if __name__=='__main__':raise SystemExit(main())

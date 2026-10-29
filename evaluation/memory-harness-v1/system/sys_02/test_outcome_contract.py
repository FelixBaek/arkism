"""SYS-02 A: offline conversion specification and synthetic tests, never native auth.

The metadata-only native plan is deliberately separate from synthetic DYN mapping.
No database, checker process, environment, credential, provider or network access.
"""
import copy
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import re
import struct
import unittest

HERE=Path(__file__).resolve().parent
ROOT=HERE.parents[3]
CONTRACT_SHA256='0746994f05df55800a3fbbebddb3acb6b737518aa1fd93f764a0456eba236fd3'
_CONTRACT_RAW=(HERE/'outcome_contract.json').read_bytes()
if hashlib.sha256(_CONTRACT_RAW).hexdigest()!=CONTRACT_SHA256:raise RuntimeError('fixed SYS02 contract mismatch')
CONTRACT=json.loads(_CONTRACT_RAW)
MAX_BYTES=4194304
MAX_INT=(1<<63)-1
class OutcomeContractError(Exception):
    def __init__(self,code):self.code=code;super().__init__(code)
def need(ok,code='invalid_value'):
    if not ok:raise OutcomeContractError(code)
def sha(raw):return hashlib.sha256(raw).hexdigest()
def raw(value):return (json.dumps(value,sort_keys=True,ensure_ascii=False,separators=(',',':'),allow_nan=False)+'\n').encode()
def audit_hash(domain,value):return sha(b'sys-outcome-conversion-v1\0'+domain.encode()+b'\0'+raw(value))
def _load_dependencies():
    for relative,digest in CONTRACT['dependency_pins'].items():
        need(sha((ROOT/relative).read_bytes())==digest,'dependency_mismatch')
    spec=importlib.util.spec_from_file_location('_sys02_pinned_sys01',ROOT/'evaluation/memory-harness-v1/system/sys_01/test_shadow_contract.py')
    module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
    dyn=json.loads((ROOT/'docs/research/memory-harness/dynamics_policy_contract.json').read_bytes())
    return module,dyn
SYS,DYN=_load_dependencies()
MODELS=copy.deepcopy(CONTRACT['models'])
DYN_MODELS={**DYN['models'],**DYN['closed_inline_records'],**DYN['hash_projections']}

def _pairs(items):
    out={}
    for key,value in items:
        need(key not in out,'duplicate_key');out[key]=value
    return out

def _int(token):
    need(len(token.lstrip('-'))<=19,'invalid_value');return int(token)
def _constant(_):raise OutcomeContractError('invalid_json')
def _bounded(value,depth=0):
    need(depth<=32,'resource_limit')
    if type(value) is str:
        try:encoded=value.encode('utf8')
        except UnicodeEncodeError:raise OutcomeContractError('invalid_unicode') from None
        need(len(encoded)<=4096,'resource_limit')
    elif type(value) is dict:
        need(len(value)<=64,'resource_limit')
        for key,child in value.items():need(type(key) is str,'invalid_type');_bounded(key,depth+1);_bounded(child,depth+1)
    elif type(value) is list:
        need(len(value)<=64,'resource_limit')
        for child in value:_bounded(child,depth+1)
    elif type(value) is float:need(math.isfinite(value),'invalid_value')
    elif value is not None:need(type(value) in (int,bool),'invalid_type')

def decode(data):
    need(type(data) is bytes,'invalid_type');need(len(data)<=MAX_BYTES,'resource_limit')
    try:value=json.loads(data.decode('utf8'),object_pairs_hook=_pairs,parse_int=_int,parse_constant=_constant)
    except UnicodeDecodeError:raise OutcomeContractError('invalid_unicode') from None
    except (ValueError,RecursionError):raise OutcomeContractError('invalid_json') from None
    _bounded(value);validate('Observation',value);return value

def _closed(fields,value):
    need(type(value) is dict,'invalid_type');need(not(set(value)-set(fields)),'unknown_field');need(not(set(fields)-set(value)),'missing_field')
def _primitive(name,value):
    if name in ('NativeEventID','NativePrincipal','NativeLabel'):
        need(type(value) is str,'invalid_type')
        if name=='NativeEventID':need(re.fullmatch(r'[A-Za-z0-9._-]{1,128}',value,re.ASCII) is not None)
        else:need(len(value.encode('utf8'))<=256 and (bool(value) or name=='NativeLabel'))
    elif name in ('ID','SyntheticID','SHA','LegacyID'):
        need(type(value) is str,'invalid_type')
        pattern=r'[0-9a-f]{64}' if name=='SHA' else r'[a-z][a-z0-9_-]{0,63}'
        need(re.fullmatch(pattern,value,re.ASCII) is not None)
        if name=='SyntheticID':need(value.startswith('synthetic_'),'namespace_mismatch')
    elif name in ('NS','PositiveInt64','Seq','Seq0','Count'):
        need(type(value) is int,'invalid_type');need((1 if name in ('PositiveInt64','Seq') else 0)<=value<=MAX_INT)
    elif name in ('UnitFloat','NonnegativeFloat'):
        need(type(value) in (int,float),'invalid_type');need(math.isfinite(value) and 0<=value<=(1 if name=='UnitFloat' else 1.7976931348623157e308))
    elif name=='Bool':need(type(value) is bool,'invalid_type')
    else:raise OutcomeContractError('unsupported_type')

def validate(spec,value):
    if isinstance(spec,dict):
        if 'nullable' in spec:
            if value is None:return
            return validate(spec['nullable'],value)
        if 'enum' in spec:need(type(value) is str,'invalid_type');need(value in spec['enum']);return
        if 'items' in spec:
            need(type(value) is list,'invalid_type');need(spec['min_items']<=len(value)<=spec['max_items'],'resource_limit')
            for child in value:validate(spec['items'],child)
            return
    need(type(spec) is str,'unsupported_type')
    if spec.startswith('SYS:'):
        name=spec[4:];need(name in ('Scope','CanonicalRecordRef','NativeClock'),'unsupported_type')
        try:SYS.validate_typed(name,value)
        except SYS.SYSContractError as e:raise OutcomeContractError(e.code if e.code in CONTRACT['error_codes'] else 'invalid_value') from None
    elif spec.startswith('DYN:'):dyn_validate(spec[4:],value)
    elif spec in MODELS:
        fields=MODELS[spec]['fields'];_closed(fields,value)
        for key,rule in fields.items():validate(rule,value[key])
    else:_primitive(spec,value)

def dyn_rule(rule,value):
    if value is None:need(rule.get('nullable',False),'invalid_type');return
    if rule['type']=='ref':return dyn_validate(rule['ref'],value)
    if rule['type']=='array':
        need(type(value) is list,'invalid_type');need(len(value)<=64,'resource_limit')
        for child in value:dyn_rule(rule['items'],child)
    elif rule['type']=='string':
        need(type(value) is str,'invalid_type')
        if 'const' in rule:need(value==rule['const'])
        if 'enum' in rule:need(value in rule['enum'])
    else:raise OutcomeContractError('unsupported_type')
def dyn_validate(name,value):
    if name in DYN_MODELS:
        fields=DYN_MODELS[name]['fields'];_closed(fields,value)
        for key,rule in fields.items():dyn_rule(rule,value[key])
    elif name in DYN['enums']:need(type(value) is str,'invalid_type');need(value in DYN['enums'][name])
    else:_primitive(name,value)

def dyn_canonical(name,value):
    """Explicit frozen type map, including int vs binary64 distinction."""
    dyn_validate(name,value)
    if name in DYN_MODELS:
        fields=DYN_MODELS[name]['fields'];out=b'o'+str(len(fields)).encode()+b'{'
        for key in sorted(fields):out+=_string(key)+_dyn_rule_bytes(fields[key],value[key])
        return out+b'}'
    if name in DYN['enums'] or name in ('LegacyID','SHA'):return _string(value)
    if name in ('UnitFloat','NonnegativeFloat'):return b'd'+struct.pack('>d',0.0 if value==0 else float(value)).hex().encode()+b';'
    if name=='Bool':return b't' if value else b'f'
    return b'i'+str(value).encode()+b';'
def _string(v):b=v.encode();return b's'+str(len(b)).encode()+b':'+b
def _dyn_rule_bytes(rule,value):
    if value is None:return b'n'
    if rule['type']=='ref':return dyn_canonical(rule['ref'],value)
    if rule['type']=='array':return b'a'+str(len(value)).encode()+b'['+b''.join(_dyn_rule_bytes(rule['items'],x) for x in value)+b']'
    return _string(value)
def dyn_hash(name,value):return sha(b'dynamics-contract-v2\0'+name.encode()+b'\0'+dyn_canonical(name,value))
def projection(name,value):return {k:copy.deepcopy(value[k]) for k in DYN_MODELS[name]['fields']}

def _ref(ref,collection):need(ref['collection']==collection,'invalid_value')
def _live(row):need(row['lifecycle']=='live','source_unavailable');need(row['ref']==row['current_ref'],'revision_mismatch')
def _native_plan(o):
    n=o['native'];need(n is not None and o['dyn'] is None,'namespace_mismatch')
    q=n['request'];a=n['acceptance'];who=n['evaluator'];target=n['target_record'];trace=n['trace_record'];evidence=n['evidence_records']
    _ref(q['target'],'memories');_ref(q['trace'],'execution_traces')
    need(who['role']=='service' and who['permission_observation']=='declared_granted','evaluator_denied')
    for row in [target,trace]+evidence:need(row['scope']==n['scope'],'scope_mismatch');_live(row)
    need(q['target']==target['ref'] and q['trace']==trace['ref'],'revision_mismatch')
    need(who['principal_id'] not in (target['created_by'],target['approved_by'],trace['created_by']),'self_rating')
    need(trace['run_task_type']!='crew_run','crew_trace_excluded');_ref(trace['run_ref'],'agent_runs')
    need(trace['run_ref']==trace['run_current_ref'],'revision_mismatch')
    need(trace['run_lifecycle']=='live','source_unavailable');need(trace['run_terminal'] and trace['run_finished'],'trace_invalid')
    refs=[r['ref'] for r in evidence];need(refs==q['evidence'],'evidence_invalid')
    need(len({r['id'] for r in refs})==len(refs),'evidence_invalid')
    exposed=False
    for row in evidence:
        _ref(row['ref'],'sources');need(row['ref'] in trace['source_refs'] and row['trace_ref']==trace['ref'],'evidence_invalid')
        need(row['created_by']!=who['principal_id'],'self_rating')
        exposed=exposed or q['target'] in row['retrieved_refs']
    need(exposed and o['server_return']=='observed_true','target_not_returned')
    c=n['checker'];need(c['host_config_sha256']==q['checker_sha256']==c['host_bytes_sha256'],'checker_mismatch')
    need(c['basis']=='synthetic_hash_comparison','checker_unverified')
    observed=o['task_success'] in ('observed_true','observed_false')
    need((q['passed'] is not None)==observed,'acceptance_binding')
    if observed:need(q['passed'] is (o['task_success']=='observed_true'),'acceptance_binding')
    need(a['processing_seq'] is None,'native_sequence_unavailable')
    if a['state']=='accepted':
        need(observed and a['evaluation_ref'] is not None,'acceptance_binding');_ref(a['evaluation_ref'],'evaluations')
        need(a['server_created_at']['state']!='absent','acceptance_binding')
    else:need(a['evaluation_ref'] is None and a['server_created_at']['state'] in ('absent','not_recorded'),'acceptance_binding')
    oc=n['observation_clock'];ac=a['server_created_at']
    if oc['state']=='known' and ac['state']=='known':need(oc['unix_ns']<=ac['unix_ns'],'future_observation')
    api={'scope':n['scope'],'evaluator_principal':who['principal_id'],'event_id':q['event_id']}
    request={'scope':n['scope'],**q}
    return {'status':'native_metadata_plan_only','native_acceptance':'not_authenticated_by_A','declared_acceptance_state':a['state'],'evaluation_ref':copy.deepcopy(a['evaluation_ref']),'server_created_at':copy.deepcopy(ac),'observation_clock':copy.deepcopy(oc),'processing_seq':None,'api_audit_identity_sha256':audit_hash('api_identity',api),'request_audit_sha256':audit_hash('request',request),'logical_evidence_identity_sha256':None,'dyn_event':None,'jump_eligible':False,'actual_use':o['actual_use'],'task_success':o['task_success'],'context_delivery':o['context_delivery'],'server_return':o['server_return'],'unresolved':copy.deepcopy(CONTRACT['authority']['native_unresolved'])}

def _synthetic_event(o):
    b=o['dyn'];need(b is not None and o['native'] is None,'namespace_mismatch')
    need(b['key']['contract_sha256']==CONTRACT['dependency_pins']['docs/research/memory-harness/dynamics_policy_contract.json'],'hash_mismatch')
    task=b['task'];e=b['evidence'];key=b['key']
    need(task['task_ref']['collection']=='eval_tasks' and task['run_ref']['collection']=='eval_runs' and task['trial_ref']['collection']=='eval_trials','evidence_invalid')
    need(task['task_sha256']==task['task_ref']['content_sha256']==e['task_sha256'],'hash_mismatch')
    need(e['target']==key['target'] and e['dimension']==key['dimension'],'evidence_invalid')
    need(task['rule_sha256']==e['rule_sha256'],'hash_mismatch')
    need(e['binding_sha256']==dyn_hash('EvidenceBindingHashProjection',projection('EvidenceBindingHashProjection',e)),'hash_mismatch')
    encoded=[dyn_canonical('EvidenceRef',r) for r in e['refs']];need(encoded==sorted(set(encoded)),'evidence_invalid')
    if e['independence']=='synthetic_independent' and e['source_status']=='available':need(len(e['refs'])>0,'evidence_invalid')
    if b['observed_at_ns'] is not None:need(b['observed_at_ns']<=b['accepted_at_ns'],'future_observation')
    # First acceptance only; retries are checked by the synthetic stream index after ID lookup.
    state=o['task_success']
    if state in ('pending','unknown'):return {'status':'unobserved_no_event','dyn_event':None,'jump_eligible':False,'logical_evidence_identity_sha256':None,'claim_sha256':None}
    need(o['server_return']=='observed_true','target_not_returned')
    outcome='passed' if state=='observed_true' else 'failed'
    qualified=task['qualification']=='synthetic_independent' and e['independence']=='synthetic_independent' and e['source_status']=='available' and e['available_accepted_seq']<=b['accepted_seq'] and len(e['refs'])>0 and (outcome=='passed' or b['attribution']=='memory')
    if qualified:need(b['strength'] is not None)
    event={'protocol':'dynamics-event-v2','stream_id':b['stream_id'],'event_id':b['event_id'],'transport_attempt_id':b['transport_attempt_id'],'key':copy.deepcopy(key),'kind':'outcome','clock_domain':b['clock_domain'],'observed_at_ns':b['observed_at_ns'],'arrived_at_ns':b['arrived_at_ns'],'accepted_at_ns':b['accepted_at_ns'],'accepted_seq':b['accepted_seq'],'task':copy.deepcopy(task),'evidence':copy.deepcopy(e),'outcome':outcome,'attribution':b['attribution'],'strength':b['strength'],'reason':'passed_independent' if outcome=='passed' else ('failed_attributable' if b['attribution']=='memory' else 'failed_unattributable'),'payload_sha256':'0'*64}
    event['payload_sha256']=dyn_hash('EventLogicalProjectionV2',projection('EventLogicalProjectionV2',event));dyn_validate('EventV2',event)
    identity={'scope':key['scope'],'target':key['target'],'dimension':key['dimension'],'task_ref':task['task_ref'],'run_ref':task['run_ref'],'trial_ref':task['trial_ref'],'checker_sha256':task['checker_sha256'],'evidence_refs':e['refs']}
    return {'status':'synthetic_event_only','dyn_event':event,'jump_eligible':qualified,'logical_evidence_identity_sha256':dyn_hash('EvidenceIdentityProjectionV2',identity) if qualified else None,'claim_sha256':dyn_hash('EvidenceClaimProjectionV2',projection('EvidenceClaimProjectionV2',event)) if qualified else None}

def convert(data):
    o=decode(data);result=_native_plan(o) if o['channel']=='native_metadata' else _synthetic_event(o)
    result.update({'verification_scope':'synthetic_model','execution_authority':'not_granted','durability':'not_implemented'})
    return result

class SyntheticIndex:
    """Test-only in-memory retry/consumption model. Not a durable product adapter."""
    def __init__(self):self.events={};self.evidence={};self.last={}
    def accept(self,data):
        result=convert(data);e=result['dyn_event']
        if e is None:return result
        key=(e['stream_id'],e['event_id']);old=self.events.get(key)
        if old is not None:
            need(old['dyn_event']['payload_sha256']==e['payload_sha256'],'event_conflict')
            # Original acceptance coordinates cannot change, while transport arrival can be later.
            need((old['dyn_event']['accepted_at_ns'],old['dyn_event']['accepted_seq'])==(e['accepted_at_ns'],e['accepted_seq']),'event_conflict')
            return {**copy.deepcopy(old),'status':'duplicate_event'}
        prev=self.last.get(e['stream_id'])
        if prev:need(e['accepted_at_ns']>=prev[0],'time_regression');need(e['accepted_seq']>prev[1],'sequence_regression')
        if e['arrived_at_ns'] is not None:need(e['arrived_at_ns']<=e['accepted_at_ns'],'future_observation')
        identity=result['logical_evidence_identity_sha256']
        if identity is not None and identity in self.evidence:
            need(self.evidence[identity]==result['claim_sha256'],'evidence_conflict');result={**result,'status':'duplicate_evidence','jump_eligible':False}
        # Commit only after every rejection check. No partial index mutations on failures.
        if identity is not None:self.evidence[identity]=result['claim_sha256']
        self.events[key]=copy.deepcopy(result);self.last[e['stream_id']]=(e['accepted_at_ns'],e['accepted_seq'])
        return copy.deepcopy(result)

class NativeAuditIndex:
    """Declared fixture retry audit, NOT authenticated native acceptance service."""
    def __init__(self):self.events={}
    def inspect(self,data):
        result=convert(data);need(result['status']=='native_metadata_plan_only','namespace_mismatch')
        if result['declared_acceptance_state']!='accepted':return result
        key=result['api_audit_identity_sha256'];old=self.events.get(key)
        if old is not None:
            need(old['request_audit_sha256']==result['request_audit_sha256'],'event_conflict')
            need(old['evaluation_ref']==result['evaluation_ref'] and old['server_created_at']==result['server_created_at'],'acceptance_binding')
            return {**copy.deepcopy(old),'status':'duplicate_api_fixture'}
        self.events[key]=copy.deepcopy(result);return copy.deepcopy(result)


def _cases():
    value=json.loads((HERE/'outcome_cases.json').read_bytes())
    _closed(['protocol','verification_scope','cases','literal_oracle'],value)
    need(value['protocol']=='sys-outcome-cases-v1' and value['verification_scope']=='synthetic_model')
    ids=set()
    for row in value['cases']:
        _closed(['id','input','expected'],row);validate('ID',row['id']);need(row['id'] not in ids);ids.add(row['id'])
        _closed(['error','status','jump_eligible'],row['expected'])
        need(type(row['expected']['jump_eligible']) is bool,'invalid_type')
        need(row['expected']['error'] is None or row['expected']['error'] in CONTRACT['error_codes'])
    return value

class OutcomeContractTests(unittest.TestCase):
    def setUp(self):self.cases=_cases();self.byid={r['id']:r for r in self.cases['cases']}
    def native(self):return copy.deepcopy(self.byid['native_accepted_metadata_only']['input'])
    def synthetic(self):return copy.deepcopy(self.byid['synthetic_pass_only']['input'])
    def assertCode(self,code,fn):
        with self.assertRaises(OutcomeContractError) as error:fn()
        self.assertEqual(error.exception.code,code)
    def test_checked_in63_inputs_literal_expected_outcomes(self):
        self.assertEqual(len(self.cases['cases']),63)
        for row in self.cases['cases']:
            with self.subTest(id=row['id']):
                e=row['expected']
                if e['error']:self.assertCode(e['error'],lambda:convert(raw(row['input'])))
                else:
                    got=convert(raw(row['input']));self.assertEqual((got['status'],got['jump_eligible']),(e['status'],e['jump_eligible']))
                    self.assertEqual(got['verification_scope'],'synthetic_model');self.assertEqual(got['execution_authority'],'not_granted')
    def test_synthetic_full_event_and_three_hashes_literal_oracle(self):
        r=convert(raw(self.synthetic()));oracle=self.cases['literal_oracle']
        self.assertEqual(r['dyn_event'],oracle['synthetic_pass_event'])
        self.assertEqual(r['logical_evidence_identity_sha256'],oracle['logical_identity_sha256']);self.assertEqual(r['claim_sha256'],oracle['claim_sha256'])
        self.assertEqual(r['dyn_event']['evidence']['binding_sha256'],oracle['binding_sha256'])
    def test_native_plan_never_authenticates_or_generates_dyn(self):
        r=convert(raw(self.native()));self.assertEqual(r['native_acceptance'],'not_authenticated_by_A')
        self.assertIsNone(r['dyn_event']);self.assertIsNone(r['processing_seq']);self.assertIsNone(r['logical_evidence_identity_sha256']);self.assertFalse(r['jump_eligible'])
        self.assertEqual(len(r['unresolved']),4)
    def test_return_delivery_use_and_success_independent_observations(self):
        value=self.native()
        for delivery in ['unknown','pending','observed_false','observed_true']:
            for use in ['unknown','pending','observed_false','observed_true']:
                with self.subTest(delivery=delivery,use=use):
                    value['context_delivery']=delivery;value['actual_use']=use;r=convert(raw(value))
                    self.assertEqual((r['context_delivery'],r['actual_use'],r['task_success']),(delivery,use,'observed_true'))
                    self.assertIsNone(r['dyn_event'])
    def test_all_nativeclock_unknown_states_preserved_without_zero_substitute(self):
        for state in ['native_zero','outside_int64_ns','not_recorded']:
            with self.subTest(state=state):
                r=convert(raw(self.byid['native_clock_'+state]['input']))
                self.assertEqual(r['server_created_at'],{'state':state,'unix_ns':None});self.assertIsNone(r['processing_seq'])
    def test_nativeclock_known_int64_limits_are_exact(self):
        for n in [-(1<<63),(1<<63)-1]:
            with self.subTest(n=n):
                o=self.native();o['native']['acceptance']['server_created_at']['unix_ns']=n;o['native']['observation_clock']['unix_ns']=n
                self.assertEqual(convert(raw(o))['server_created_at']['unix_ns'],n)
    def test_native_api_retry_keeps_original_server_created_time(self):
        index=NativeAuditIndex();o=self.native();first=index.inspect(raw(o));again=index.inspect(raw(o))
        self.assertEqual(again['status'],'duplicate_api_fixture');self.assertEqual(again['server_created_at'],first['server_created_at'])
        o['native']['acceptance']['server_created_at']['unix_ns']=200
        self.assertCode('acceptance_binding',lambda:index.inspect(raw(o)))
    def test_api_same_id_changed_payload_conflicts_distinct_id_not_logical_consumption(self):
        index=NativeAuditIndex();o=self.native();first=index.inspect(raw(o))
        o['native']['request']['passed']=False;o['task_success']='observed_false';self.assertCode('event_conflict',lambda:index.inspect(raw(o)))
        o['native']['request']['event_id']='other';second=index.inspect(raw(o));self.assertNotEqual(first['api_audit_identity_sha256'],second['api_audit_identity_sha256'])
        self.assertIsNone(second['logical_evidence_identity_sha256']);self.assertIsNone(second['dyn_event'])
    def test_api_retry_rechecks_current_revision_before_returning_old(self):
        index=NativeAuditIndex();o=self.native();index.inspect(raw(o));o['native']['target_record']['current_ref']['revision']=2
        self.assertCode('revision_mismatch',lambda:index.inspect(raw(o)));self.assertEqual(len(index.events),1)
    def test_dyn_retry_can_arrive_later_without_changing_first_acceptance(self):
        index=SyntheticIndex();o=self.synthetic();first=index.accept(raw(o));o['dyn']['arrived_at_ns']=999;o['dyn']['transport_attempt_id']='synthetic_retry'
        again=index.accept(raw(o));self.assertEqual(again['status'],'duplicate_event');self.assertEqual(again['dyn_event'],first['dyn_event'])
        o['dyn']['accepted_at_ns']=1000;self.assertCode('event_conflict',lambda:index.accept(raw(o)))
    def test_new_event_same_evidence_new_accepted_coordinates_consumes_once(self):
        index=SyntheticIndex();o=self.synthetic();first=index.accept(raw(o));o['dyn'].update(event_id='synthetic_repacked',transport_attempt_id='synthetic_repacked_attempt',arrived_at_ns=150,accepted_at_ns=200,accepted_seq=3)
        second=index.accept(raw(o));self.assertEqual(second['status'],'duplicate_evidence');self.assertFalse(second['jump_eligible'])
        self.assertEqual(first['logical_evidence_identity_sha256'],second['logical_evidence_identity_sha256']);self.assertEqual(first['claim_sha256'],second['claim_sha256'])
        self.assertEqual(second['dyn_event']['observed_at_ns'],90);self.assertEqual(second['dyn_event']['evidence']['available_accepted_seq'],1);self.assertEqual(len(index.evidence),1)
    def test_changed_observed_claim_same_identity_conflicts(self):
        index=SyntheticIndex();o=self.synthetic();index.accept(raw(o));before=copy.deepcopy(index.__dict__)
        o['dyn'].update(event_id='synthetic_other',accepted_at_ns=200,accepted_seq=3,observed_at_ns=91)
        self.assertCode('evidence_conflict',lambda:index.accept(raw(o)));self.assertEqual(index.__dict__,before)
    def test_unknown_pending_never_create_or_consume_outcome(self):
        index=SyntheticIndex()
        for state in ['unknown','pending']:
            with self.subTest(state=state):
                o=self.synthetic();o['task_success']=state;r=index.accept(raw(o));self.assertIsNone(r['dyn_event']);self.assertFalse(r['jump_eligible'])
        self.assertEqual(index.events,{});self.assertEqual(index.evidence,{})
    def test_observed_negative_not_automatically_memory_blame(self):
        for attr in ['unattributable','tool','retrieval','evaluator']:
            with self.subTest(attribution=attr):
                index=SyntheticIndex();o=self.synthetic();o['task_success']='observed_false';o['dyn']['attribution']=attr;r=index.accept(raw(o))
                self.assertFalse(r['jump_eligible']);self.assertIsNone(r['logical_evidence_identity_sha256']);self.assertEqual(index.evidence,{})
    def test_equal_accepted_times_order_by_sequence_not_timestamp_sort(self):
        index=SyntheticIndex();o=self.synthetic();index.accept(raw(o));o['dyn'].update(event_id='synthetic_second',accepted_seq=3)
        self.assertEqual(index.accept(raw(o))['status'],'duplicate_evidence')
        o['dyn']['event_id']='synthetic_third';self.assertCode('sequence_regression',lambda:index.accept(raw(o)))
    def test_new_acceptance_time_or_sequence_regression_rejected(self):
        for field,value,error in [('accepted_seq',1,'sequence_regression'),('accepted_at_ns',99,'time_regression')]:
            with self.subTest(field=field):
                index=SyntheticIndex();o=self.synthetic();index.accept(raw(o));o['dyn'].update(event_id='synthetic_other');o['dyn'][field]=value
                self.assertCode(error,lambda:index.accept(raw(o)))
    def test_first_arrival_future_rejected_retry_accepted(self):
        index=SyntheticIndex();o=self.synthetic();o['dyn']['arrived_at_ns']=101
        self.assertCode('future_observation',lambda:index.accept(raw(o)));self.assertEqual(index.events,{})
    def test_native_absent_task_trial_never_fabricated_or_relabelled(self):
        o=self.native();o['dyn']=self.synthetic()['dyn'];self.assertCode('namespace_mismatch',lambda:convert(raw(o)))
        o=self.synthetic();o['dyn']['task']['qualification']='native_independent';self.assertCode('invalid_value',lambda:convert(raw(o)))
    def test_input_immutability_and_independent_results(self):
        o=self.synthetic();before=copy.deepcopy(o);a=convert(raw(o));b=convert(raw(o));a['dyn_event']['task']['task_ref']['id']='mutated'
        self.assertEqual(o,before);self.assertNotEqual(a,b);self.assertEqual(convert(raw(o)),b)
    def test_no_sys01_or_dyn_model_registry_mutation(self):
        before=copy.deepcopy(SYS._MODELS);models=copy.deepcopy(DYN_MODELS)
        convert(raw(self.native()));convert(raw(self.synthetic()));self.assertEqual(SYS._MODELS,before);self.assertEqual(DYN_MODELS,models)
    def test_raw_parser_rejects_duplicate_bom_unicode_and_nonfinite(self):
        for value,code in [(b'{"x":1,"x":2}','duplicate_key'),(b'\xef\xbb\xbf{}','invalid_json'),(b'\xff','invalid_unicode'),(b'"\\ud800"','invalid_unicode'),(b'NaN','invalid_json'),(b'Infinity','invalid_json')]:
            with self.subTest(code=code,raw_length=len(value)):self.assertCode(code,lambda:decode(value))
    def test_schema_unknown_missing_type_numeric_limits(self):
        o=self.native();o['verified']=True;self.assertCode('unknown_field',lambda:convert(raw(o)))
        del o['verified'];del o['protocol'];self.assertCode('missing_field',lambda:convert(raw(o)))
        for value in [True,1.0,1e20,-1,1<<63]:
            with self.subTest(type=type(value).__name__):
                o=self.synthetic();o['dyn']['accepted_seq']=value
                self.assertCode('invalid_type' if type(value) is not int else 'invalid_value',lambda:convert(raw(o)))
    def test_parser_bounds_depth_bytes_strings_and_arrays(self):
        for value in [b' '* (MAX_BYTES+1),raw('x'*4097),b'['*34+b'0'+b']'*34,raw([0]*65)]:
            with self.subTest(length=len(value)):self.assertCode('resource_limit',lambda:decode(value))
    def test_native_source_facts_do_not_claim_product_acceptance(self):
        r=convert(raw(self.native()));self.assertNotIn('verified',r);self.assertEqual(r['durability'],'not_implemented')
        self.assertEqual(CONTRACT['mapping']['native'].count('no EventV2'),1)
    def test_strength_zero_half_one_and_negative_zero_match_fixed_dyn(self):
        for token in ['0','0.5','1','-0','-0.0','1e0']:
            with self.subTest(token=token):
                data=raw(self.synthetic()).replace(b'"strength":0.5',b'"strength":'+token.encode())
                result=convert(data);self.assertTrue(result['jump_eligible'])
                zero=copy.deepcopy(result['dyn_event']);zero['strength']=0.0
                if float(token)==0:self.assertEqual(dyn_canonical('EventV2',result['dyn_event']),dyn_canonical('EventV2',zero))
        for token in ['-0.01','1.01','NaN','Infinity','1e309']:
            with self.subTest(invalid=token):
                data=raw(self.synthetic()).replace(b'"strength":0.5',b'"strength":'+token.encode())
                with self.assertRaises(OutcomeContractError):convert(data)
    def test_failed_qualified_null_strength_rejects_without_default(self):
        o=self.synthetic();o['task_success']='observed_false';o['dyn']['attribution']='memory';o['dyn']['strength']=None
        self.assertCode('invalid_value',lambda:convert(raw(o)))
    def test_api_retry_deleted_target_refused(self):
        index=NativeAuditIndex();o=self.native();index.inspect(raw(o));o['native']['target_record']['lifecycle']='deleted'
        self.assertCode('source_unavailable',lambda:index.inspect(raw(o)))
    def test_synthetic_independent_available_requires_nonempty_evidence(self):
        o=self.synthetic();o['dyn']['evidence']['refs']=[]
        e=o['dyn']['evidence'];e['binding_sha256']=dyn_hash('EvidenceBindingHashProjection',projection('EvidenceBindingHashProjection',e))
        self.assertCode('evidence_invalid',lambda:convert(raw(o)))

    def test_native_event_id_and_noncrew_task_type_preserve_product_domain(self):
        o=self.native();o['native']['request']['event_id']='Upper.Dashed-Event_01';o['native']['trace_record']['run_task_type']='other_noncrew_task'
        o['native']['evaluator']['principal_id']='service:evaluator@example';self.assertEqual(convert(raw(o))['status'],'native_metadata_plan_only')
        o['native']['request']['event_id']='X'*128;self.assertEqual(convert(raw(o))['status'],'native_metadata_plan_only')
        o['native']['request']['event_id']='X'*129;self.assertCode('invalid_value',lambda:convert(raw(o)))

    def test_nested_raw_payloads_and_self_auth_flags_are_rejected(self):
        for path in [('native',),('native','target_record'),('native','trace_record'),('native','checker'),('native','evidence_records',0),('native','acceptance')]:
            with self.subTest(depth=len(path)):
                o=self.native();node=o
                for k in path:node=node[k]
                node['raw_secret']='RAW_BODY_CANARY';self.assertCode('unknown_field',lambda:convert(raw(o)))
        o=self.native();o['native']['evaluator']['principal_id']='CANARY_PRINCIPAL';result=convert(raw(o));self.assertNotIn('CANARY',json.dumps(result))
    def test_field_specific_float_and_integer_clock_rules(self):
        for token in ['1.0','1e0','true']:
            with self.subTest(token=token):
                data=raw(self.synthetic()).replace(b'"accepted_seq":2',b'"accepted_seq":'+token.encode())
                self.assertCode('invalid_type',lambda:convert(data))
        data=raw(self.synthetic()).replace(b'"observed_at_ns":90',b'"observed_at_ns":-0')
        self.assertEqual(convert(data)['dyn_event']['observed_at_ns'],0)
    def test_missing_and_duplicate_evidence_cannot_form_event(self):
        o=self.native();o['native']['request']['evidence']*=2;o['native']['evidence_records']*=2
        self.assertCode('evidence_invalid',lambda:convert(raw(o)))
        o=self.synthetic();e=o['dyn']['evidence'];e['refs']*=2;e['binding_sha256']=dyn_hash('EvidenceBindingHashProjection',projection('EvidenceBindingHashProjection',e))
        self.assertCode('evidence_invalid',lambda:convert(raw(o)))
    def test_scope_ref_clock_reuse_is_byte_exact_and_deps_stay_pinned(self):
        for relative,want in CONTRACT['dependency_pins'].items():
            with self.subTest(path=relative):self.assertEqual(sha((ROOT/relative).read_bytes()),want)
        self.assertEqual(SYS.semantic_sha256('Scope',self.native()['native']['scope']),SYS.semantic_sha256('Scope',copy.deepcopy(self.native()['native']['scope'])))
    def test_explicit_rejected_acceptance_plan_has_no_event(self):
        o=self.byid['native_pending']['input'];o=copy.deepcopy(o);o['native']['acceptance']['state']='rejected'
        r=convert(raw(o));self.assertEqual(r['declared_acceptance_state'],'rejected');self.assertIsNone(r['dyn_event']);self.assertFalse(r['jump_eligible'])

    def test_future_evidence_seq_preserves_valid_literal_event_without_learning(self):
        oracle=self.cases['literal_oracle'];o=copy.deepcopy(oracle['future_evidence_input'])
        result=convert(raw(o));self.assertEqual(result['dyn_event'],oracle['future_evidence_valid_event'])
        self.assertFalse(result['jump_eligible']);self.assertIsNone(result['logical_evidence_identity_sha256']);self.assertIsNone(result['claim_sha256'])
        index=SyntheticIndex();first=index.accept(raw(o));self.assertEqual(first['dyn_event'],oracle['future_evidence_valid_event'])
        self.assertEqual(len(index.events),1);self.assertEqual(index.evidence,{})
        # Same evidence only becomes eligible when its declared earliest availability has arrived.
        o['dyn'].update(event_id='synthetic_later',accepted_seq=3,accepted_at_ns=200,arrived_at_ns=150)
        later=index.accept(raw(o));self.assertTrue(later['jump_eligible']);self.assertEqual(len(index.evidence),1)
        self.assertEqual(later['dyn_event']['evidence']['available_accepted_seq'],3)
        o['dyn'].update(event_id='synthetic_later_duplicate',accepted_seq=4,accepted_at_ns=300,arrived_at_ns=250)
        duplicate=index.accept(raw(o));self.assertEqual(duplicate['status'],'duplicate_evidence');self.assertFalse(duplicate['jump_eligible'])
        self.assertEqual(duplicate['claim_sha256'],later['claim_sha256']);self.assertEqual(len(index.evidence),1)
        self.assertEqual(duplicate['dyn_event']['observed_at_ns'],90);self.assertEqual(duplicate['dyn_event']['evidence']['available_accepted_seq'],3)
        unqualified=copy.deepcopy(oracle['future_evidence_input']);unqualified['dyn']['strength']=None
        nonlearning=convert(raw(unqualified));self.assertIsNotNone(nonlearning['dyn_event']);self.assertFalse(nonlearning['jump_eligible'])
    def test_future_evidence_does_not_relax_other_rejection_boundaries(self):
        original=self.cases['literal_oracle']['future_evidence_input']
        for field,value,error in [('observed_at_ns',101,'future_observation'),('accepted_seq',True,'invalid_type')]:
            with self.subTest(field=field):
                o=copy.deepcopy(original);o['dyn'][field]=value;self.assertCode(error,lambda:convert(raw(o)))
        o=copy.deepcopy(original);o['dyn']['evidence']['binding_sha256']='0'*64;self.assertCode('hash_mismatch',lambda:convert(raw(o)))
        o=copy.deepcopy(original);o['dyn']['arrived_at_ns']=101;index=SyntheticIndex()
        self.assertCode('future_observation',lambda:index.accept(raw(o)));self.assertEqual(index.events,{})
        o=copy.deepcopy(original);o['dyn']['evidence']['source_status']='invalid_state'
        self.assertCode('invalid_value',lambda:convert(raw(o)))
        o=copy.deepcopy(original);o['dyn']['key']['target']['id']='other_target'
        self.assertCode('evidence_invalid',lambda:convert(raw(o)))
    def test_retry_historical_receipt_qualification_never_mutates_index(self):
        index=SyntheticIndex();o=self.synthetic();first=index.accept(raw(o));before=copy.deepcopy(index.__dict__)
        o['dyn'].update(transport_attempt_id='synthetic_late_retry',arrived_at_ns=500)
        receipt=index.accept(raw(o));self.assertEqual(receipt['status'],'duplicate_event')
        self.assertTrue(first['jump_eligible']);self.assertTrue(receipt['jump_eligible'])
        self.assertEqual(index.__dict__,before);self.assertEqual(receipt['dyn_event'],first['dyn_event'])

    def test_required_suite_rejects_zero_skips_expected_failures(self):
        with self.assertRaises(AssertionError):RequiredSuite([]).run(unittest.TestResult())
        class Skip(unittest.TestCase):
            def runTest(self):self.skipTest('required')
        class Expected(unittest.TestCase):
            @unittest.expectedFailure
            def runTest(self):self.fail('required')
        for case in [Skip(),Expected()]:
            r=unittest.TestResult();RequiredSuite([case]).run(r);self.assertFalse(r.wasSuccessful());self.assertFalse(r.skipped);self.assertFalse(r.expectedFailures)

class RequiredResult:
    def __init__(self,result):self.result=result
    def __getattr__(self,key):return getattr(self.result,key)
    def addSkip(self,test,why):self.result.addFailure(test,(AssertionError,AssertionError('required skip'),None))
    def addExpectedFailure(self,test,error):self.result.addFailure(test,(AssertionError,AssertionError('required expected failure'),None))
class RequiredSuite(unittest.TestSuite):
    def run(self,result,debug=False):
        if not self.countTestCases():raise AssertionError('required empty suite')
        return super().run(RequiredResult(result),debug)
def load_tests(loader,standard,pattern):return RequiredSuite(loader.loadTestsFromTestCase(OutcomeContractTests))
if __name__=='__main__':unittest.main(verbosity=2)

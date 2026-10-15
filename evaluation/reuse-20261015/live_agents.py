#!/usr/bin/env python3
"""Opt-in real-provider smoke. Supply private state from TestCrossAgentHTTPPostgresReuse.
Requires explicit human approval of FACTS before --human-approved-facts is passed.
Uses only isolated fixture credentials; never connects to the user's Arkism profile.
"""
import argparse,json,os,pathlib,shutil,subprocess,tempfile,time,urllib.request,urllib.parse
FACTS=[('codex','claude','labels: trim surrounding whitespace but preserve letter case.'),('claude','codex','cache: zero TTL means do not cache; positive TTL stays unchanged.')]
p=argparse.ArgumentParser();p.add_argument('--state',required=True);p.add_argument('--arkism',required=True);p.add_argument('--output',required=True);p.add_argument('--human-approved-facts',action='store_true');a=p.parse_args()
if not a.human_approved_facts:p.error('explicit human approval of FACTS is required')
s=json.loads(pathlib.Path(a.state).read_text());out=pathlib.Path(a.output);out.mkdir(parents=True,exist_ok=True)
if urllib.parse.urlparse(s['url']).hostname not in ('127.0.0.1','localhost'):raise ValueError('loopback fixture only')
real_home=pathlib.Path.home();rows=[]
def api(method,path,token,body=None):
 req=urllib.request.Request(s['url']+path,data=json.dumps(body).encode() if body is not None else None,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'},method=method)
 with urllib.request.urlopen(req,timeout=30) as r:return json.load(r)
def run_agent(kind,role,prompt,label):
 with tempfile.TemporaryDirectory(prefix='arkism-agent-') as tmp:
  h=pathlib.Path(tmp);token=h/'fixture.token';token.write_text(s['agent_'+role+'_token']);token.chmod(0o600)
  me={'ARKISM_URL':s['url'],'ARKISM_TOKEN_FILE':str(token),'ARKISM_WORKSPACE':s['scope']['workspace_id'],'ARKISM_PROJECT':s['scope']['project_id'],'ARKISM_USER':s['scope'].get('user_id',''),'ARKISM_AGENT':s['scope'].get('agent_id',''),'ARKISM_RUNTIME':'codex' if kind=='codex' else 'claude_code','ARKISM_SKILL_CACHE':str(h/'cache'),'HOME':tmp}
  env={k:v for k,v in os.environ.items() if k in ('PATH','LANG','TERM')};env.update(HOME=tmp)
  server={'command':a.arkism,'args':['skill-router'],'env':me}
  if kind=='codex':
   ch=h/'.codex';ch.mkdir();shutil.copy2(real_home/'.codex/auth.json',ch/'auth.json');env['CODEX_HOME']=str(ch)
   cfg='model = "gpt-6-astra"\nmodel_reasoning_effort = "low"\n[mcp_servers.arkism]\ncommand = '+json.dumps(a.arkism)+'\nargs = ["skill-router"]\n[mcp_servers.arkism.env]\n'+''.join(k+' = '+json.dumps(v)+'\n' for k,v in me.items())
   cfg += '\n[mcp_servers.arkism.tools.remember]\napproval_mode = \"approve\"\n[mcp_servers.arkism.tools.recall]\napproval_mode = \"approve\"\n'
   (ch/'config.toml').write_text(cfg)
   cmd=[shutil.which('codex'),'exec','--skip-git-repo-check','--ephemeral','--json','-C',tmp,'-s','read-only','-']
  else:
   ch=h/'.claude';ch.mkdir();shutil.copy2(real_home/'.claude/.credentials.json',ch/'.credentials.json');env['CLAUDE_CONFIG_DIR']=str(ch)
   cfg=h/'mcp.json';cfg.write_text(json.dumps({'mcpServers':{'arkism':server}}))
   cmd=[shutil.which('claude'),'-p','--model','claude-opus-5-5','--effort','low','--output-format','stream-json','--verbose','--setting-sources','','--settings','{"disableAllHooks":true,"autoMemoryEnabled":false}','--strict-mcp-config','--mcp-config',str(cfg),'--tools','','--allowedTools','mcp__arkism__remember','mcp__arkism__recall','--no-session-persistence']
  start=time.monotonic()
  try:r=subprocess.run(cmd,input=prompt,text=True,capture_output=True,cwd=h,env=env,timeout=180)
  except subprocess.TimeoutExpired:
   rows.append({'label':label,'agent':kind,'timeout':True,'seconds':time.monotonic()-start});raise
  events=[]
  for line in r.stdout.splitlines():
   try:events.append(json.loads(line))
   except ValueError:pass
  # Private transient paths and generated session IDs are not evidence needed here.
  public=[]
  for e in events:
   if e.get('type')=='thread.started':continue
   if e.get('type')=='system':
    if e.get('subtype')!='init':continue
    e={k:v for k,v in e.items() if k in ('type','subtype','model','tools','mcp_servers','claude_code_version','skills','plugins')}
   for key in ('uuid','session_id','parent_tool_use_id'):e.pop(key,None)
   if isinstance(e.get('message'),dict):
    e['message']={k:v for k,v in e['message'].items() if k in ('role','content','usage','model')}
    if isinstance(e['message'].get('content'),list):e['message']['content']=[b for b in e['message']['content'] if not isinstance(b,dict) or b.get('type') not in ('thinking','redacted_thinking')]
   public.append(e)
  safe=(''.join(json.dumps(e,ensure_ascii=False)+'\n' for e in public)).replace(tmp,'<isolated-home>')
  for key in ('agent_a_token','agent_b_token','synthetic_owner_token'):safe=safe.replace(s[key],'<fixture-token>')
  (out/(label+'.jsonl')).write_text(safe)
  row={'label':label,'agent':kind,'exit_code':r.returncode,'seconds':time.monotonic()-start,'provider_cost_usd':None,'events_path':label+'.jsonl','timeout':False}
  for e in events:
   if e.get('type')=='turn.completed':row['usage']=e.get('usage')
   if e.get('type')=='result':row['usage']=e.get('usage');row['list_price_cost_usd']=e.get('total_cost_usd');row['result']=e.get('result')
  rows.append(row)
  if r.returncode:raise RuntimeError(label+' failed: '+r.stderr[-300:])
  return safe
try:
 for i,(writer,reader,fact) in enumerate(FACTS):
  tag=f'direction-{i+1}'
  run_agent(writer,'a','Use only the Arkism remember MCP tool once to propose this exact synthetic project fact as kind decision: '+fact+' Do not approve or change its wording. Return the candidate ID.',tag+'-writer')
  qs=urllib.parse.urlencode(s['scope']);candidates=api('GET','/v1/memory_candidates?'+qs+'&status=candidate&limit=100',s['synthetic_owner_token'])['items']
  matches=[c for c in candidates if c.get('text')==fact]
  if len(matches)!=1:raise AssertionError('one exact candidate required')
  candidate=matches[0]
  search={'scope':s['scope'],'query':fact.split(':')[0],'modes':['exact','fts'],'budget_tokens':2000,'token_encoding':'cl100k_base'}
  before=api('POST','/v1/search',s['agent_b_token'],search)
  if any(x.get('text')==fact for x in before['items']):raise AssertionError('unapproved publication')
  approved=api('POST','/v1/memory_candidates/'+candidate['id']+'/review',s['synthetic_owner_token'],{'scope':s['scope'],'expected_revision':candidate['revision'],'action':'approve'})
  retrieved=api('POST','/v1/search',s['agent_b_token'],search)
  assert any(x['id']==approved['id'] for x in retrieved['items'])
  task=('Apply the recalled rule to the label "  MiXeD  ". Return JSON with transformed_label and source_id.' if i==0 else 'Apply the recalled rule to TTLs 0 and 15. Return JSON with cache_zero (boolean), positive_ttl (number), and source_id.')
  result=run_agent(reader,'b','Use only the Arkism recall MCP tool to read memory ID '+approved['id']+'. '+task+' Do not invent a missing rule.',tag+'-reader')
  # Assertions inspect actual tool invocation plus task output, not model self-reported success.
  used_tool=('mcp_tool_call' in result or 'tool_use' in result) and 'recall' in result
  sources=[x['source_id'] for x in approved.get('sources',[])]
  rows.append({'label':tag+'-validation','writer':writer,'reader':reader,'human_approval':'explicit user reply approving these exact facts before execution','fact':fact,'before_items':len(before['items']),'memory_id':approved['id'],'source_ids':sources,'actual_recall_tool_observed':used_tool,'retrieval':retrieved,'task_result_requires_independent_review':True})
finally:
 (out/'runs.json').write_text(json.dumps(rows,indent=2))

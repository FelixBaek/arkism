#!/usr/bin/env python3
"""Opt-in real-provider smoke. Supply private state from TestCrossAgentHTTPPostgresReuse.
Requires explicit human approval of FACTS before --human-approved-facts is passed.
Uses only isolated fixture credentials; never connects to the user's Arkism profile.
"""
import argparse,json,os,pathlib,re,shutil,subprocess,tempfile,time,urllib.request,urllib.parse
FACTS=[('codex','claude','labels: trim surrounding whitespace but preserve letter case.'),('claude','codex','cache: zero TTL means do not cache; positive TTL stays unchanged.')]
ARTIFACT_ROOT = pathlib.Path(__file__).resolve().parent
PRIVATE_ROOT = pathlib.Path.home() / 'arkism-reuse-evidence-20261015'

def confined_path(raw):
 path = pathlib.Path(raw).resolve()
 if path.is_relative_to(ARTIFACT_ROOT):
  return path
 PRIVATE_ROOT.mkdir(mode=0o700, exist_ok=True)
 info = PRIVATE_ROOT.lstat()
 if PRIVATE_ROOT.is_symlink() or info.st_uid != os.getuid() or info.st_mode & 0o077:
  raise ValueError('private evidence root must be owned by the current user with mode 0700')
 if not path.is_relative_to(PRIVATE_ROOT.resolve()):
  raise ValueError('path must be inside this evaluation directory or the private evidence root')
 return path

def artifact_path(directory, name):
 if re.fullmatch(r'[A-Za-z0-9_-]+\.(json|jsonl|csv)', name) is None:
  raise ValueError('invalid artifact filename')
 path = (directory / name).resolve()
 if path.parent != directory.resolve():
  raise ValueError('artifact symlink escapes output directory')
 return path

def parse_args():
 parser=argparse.ArgumentParser()
 parser.add_argument('--state',required=True)
 parser.add_argument('--arkism',required=True)
 parser.add_argument('--output',required=True)
 parser.add_argument('--human-approved-facts',action='store_true')
 args=parser.parse_args()
 if not args.human_approved_facts:
  parser.error('explicit human approval of FACTS is required')
 args.state=confined_path(args.state)
 args.output=confined_path(args.output)
 executable=confined_path(args.arkism)
 if not executable.is_file() or not os.access(executable,os.X_OK):
  parser.error('Arkism must be an executable fixture build inside an allowed directory')
 args.arkism=str(executable)
 return args

class NoRedirect(urllib.request.HTTPRedirectHandler):
 def redirect_request(self, req, fp, code, msg, headers, newurl):
  raise ValueError('fixture API redirects are prohibited')

def fixture_url(value):
 parsed=urllib.parse.urlsplit(value)
 if parsed.scheme!='http' or parsed.hostname not in ('127.0.0.1','localhost') or parsed.username or parsed.password or parsed.path not in ('','/') or parsed.query or parsed.fragment:
  raise ValueError('plain loopback fixture origin required')
 return urllib.parse.urlunsplit(('http',parsed.netloc,'','',''))

def api(method,path,token,body=None):
 if not path.startswith('/v1/') or '..' in path or '#' in path:
  raise ValueError('invalid fixture API path')
 req=urllib.request.Request(s['url']+path,data=json.dumps(body).encode() if body is not None else None,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'},method=method)
 with urllib.request.build_opener(NoRedirect).open(req,timeout=30) as r:return json.load(r)
def agent_command(kind, h, tmp, me, env):
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
 return cmd

def parse_events(stdout):
 events=[]
 for line in stdout.splitlines():
  try:events.append(json.loads(line))
  except ValueError:pass
 return events

def public_event(event):
 if event.get('type') in ('thread.started','rate_limit_event'):return None
 if event.get('type')=='system':
  if event.get('subtype')!='init':return None
  return {key:value for key,value in event.items() if key in ('type','subtype','model','tools','mcp_servers','claude_code_version','skills','plugins')}
 result={key:value for key,value in event.items() if key not in ('uuid','session_id','parent_tool_use_id')}
 if isinstance(result.get('message'),dict):
  message={key:value for key,value in result['message'].items() if key in ('role','content','usage','model')}
  if isinstance(message.get('content'),list):
   message['content']=[block for block in message['content'] if not isinstance(block,dict) or block.get('type') not in ('thinking','redacted_thinking')]
  result['message']=message
 return result

def public_events(events, tmp):
 public=[value for event in events if (value:=public_event(event)) is not None]
 safe=(''.join(json.dumps(event,ensure_ascii=False)+'\n' for event in public)).replace(tmp,'<isolated-home>')
 for key in ('agent_a_token','agent_b_token','synthetic_owner_token'):
  safe=safe.replace(s[key],'<fixture-token>')
 return safe

def result_row(label,kind,result,seconds,events):
 row={'label':label,'agent':kind,'exit_code':result.returncode,'seconds':seconds,'provider_cost_usd':None,'events_path':label+'.jsonl','timeout':False}
 for event in events:
  if event.get('type')=='turn.completed':row['usage']=event.get('usage')
  if event.get('type')=='result':
   row['usage']=event.get('usage')
   row['list_price_cost_usd']=event.get('total_cost_usd')
   row['result']=event.get('result')
 return row

def run_agent(kind,role,prompt,label):
 destination=artifact_path(out,label+'.jsonl')
 if kind not in ('codex','claude') or role not in ('a','b'):
  raise ValueError('invalid fixture agent')
 with tempfile.TemporaryDirectory(prefix='arkism-agent-') as tmp:
  h=pathlib.Path(tmp);token=h/'fixture.token';token.write_text(s['agent_'+role+'_token']);token.chmod(0o600)
  me={'ARKISM_URL':s['url'],'ARKISM_TOKEN_FILE':str(token),'ARKISM_WORKSPACE':s['scope']['workspace_id'],'ARKISM_PROJECT':s['scope']['project_id'],'ARKISM_USER':s['scope'].get('user_id',''),'ARKISM_AGENT':s['scope'].get('agent_id',''),'ARKISM_RUNTIME':'codex' if kind=='codex' else 'claude_code','ARKISM_SKILL_CACHE':str(h/'cache'),'HOME':tmp}
  env={key:value for key,value in os.environ.items() if key in ('PATH','LANG','TERM')};env.update(HOME=tmp)
  cmd=agent_command(kind,h,tmp,me,env)
  start=time.monotonic()
  try:result=subprocess.run(cmd,input=prompt,text=True,capture_output=True,cwd=h,env=env,timeout=180)
  except subprocess.TimeoutExpired:
   rows.append({'label':label,'agent':kind,'timeout':True,'seconds':time.monotonic()-start});raise
  events=parse_events(result.stdout)
  safe=public_events(events,tmp)
  destination.write_text(safe)
  rows.append(result_row(label,kind,result,time.monotonic()-start,events))
  if result.returncode:raise RuntimeError(label+' failed: '+result.stderr[-300:])
  return safe

def run_direction(i, writer, reader, fact):
 tag=f'direction-{i+1}'
 run_agent(writer,'a','Use only the Arkism remember MCP tool once to propose this exact synthetic project fact as kind decision: '+fact+' Do not approve or change its wording. Return the candidate ID.',tag+'-writer')
 qs=urllib.parse.urlencode(s['scope']);candidates=api('GET','/v1/memory_candidates?'+qs+'&status=candidate&limit=100',s['synthetic_owner_token'])['items']
 matches=[c for c in candidates if c.get('text')==fact]
 if len(matches)!=1:raise AssertionError('one exact candidate required')
 candidate=matches[0]
 if re.fullmatch(r'[A-Za-z0-9_-]{1,256}',candidate['id']) is None:
  raise ValueError('invalid candidate identifier')
 search={'scope':s['scope'],'query':fact.split(':')[0],'modes':['exact','fts'],'budget_tokens':2000,'token_encoding':'cl100k_base'}
 before=api('POST','/v1/search',s['agent_b_token'],search)
 if any(x.get('text')==fact for x in before['items']):raise AssertionError('unapproved publication')
 approved=api('POST','/v1/memory_candidates/'+urllib.parse.quote(candidate['id'],safe='')+'/review',s['synthetic_owner_token'],{'scope':s['scope'],'expected_revision':candidate['revision'],'action':'approve'})
 retrieved=api('POST','/v1/search',s['agent_b_token'],search)
 assert any(x['id']==approved['id'] for x in retrieved['items'])
 task=('Apply the recalled rule to the label "  MiXeD  ". Return JSON with transformed_label and source_id.' if i==0 else 'Apply the recalled rule to TTLs 0 and 15. Return JSON with cache_zero (boolean), positive_ttl (number), and source_id.')
 result=run_agent(reader,'b','Use only the Arkism recall MCP tool to read memory ID '+approved['id']+'. '+task+' Do not invent a missing rule.',tag+'-reader')
 # Assertions inspect actual tool invocation plus task output, not model self-reported success.
 used_tool=('mcp_tool_call' in result or 'tool_use' in result) and 'recall' in result
 sources=[x['source_id'] for x in approved.get('sources',[])]
 rows.append({'label':tag+'-validation','writer':writer,'reader':reader,'human_approval':'explicit user reply approving these exact facts before execution','fact':fact,'before_items':len(before['items']),'memory_id':approved['id'],'source_ids':sources,'actual_recall_tool_observed':used_tool,'retrieval':retrieved,'task_result_requires_independent_review':True})

def main():
 global a,s,out,real_home,rows
 a=parse_args()
 s=json.loads(a.state.read_text())
 s['url']=fixture_url(s['url'])
 out=a.output;out.mkdir(parents=True,exist_ok=True)
 real_home=pathlib.Path.home();rows=[]
 try:
  for i,(writer,reader,fact) in enumerate(FACTS):
   run_direction(i,writer,reader,fact)
 finally:
  artifact_path(out,'runs.json').write_text(json.dumps(rows,indent=2))

if __name__=='__main__':
 main()

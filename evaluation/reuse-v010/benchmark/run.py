#!/usr/bin/env python3
"""Frozen synthetic code benchmark; no production memories/settings loaded.
Requires codex auth; auth bytes are copied to a private temporary profile and removed.
The model sees task + selected fact, never evaluator checks.
"""
import argparse, csv, hashlib, json, os, pathlib, random, shutil, statistics, subprocess, tempfile, time
ROOT = pathlib.Path(__file__).resolve().parent
BASELINE = '9379ee5f493716e1c80027c0beb4fd12283100cf'
MODEL = 'gpt-6-astra'
TASKS = [
 {'id':'billing','scenario':'past_rounding_error','fact':'Amounts are integer cents; partial-cent charges round down.', 'task':'Implement charge(cents, numerator, denominator) returning a nonnegative integer charge. Inputs are nonnegative integers and denominator is positive. Follow the project billing policy.', 'starter':'def charge(cents, numerator, denominator):\n    raise NotImplementedError\n', 'checks':'assert charge(105,1,2)==52\nassert charge(7,1,2)==3\nassert charge(100,1,2)==50\nassert charge(0,3,2)==0'},
 {'id':'roster','scenario':'persistent_project_rule','fact':'Jobs are permitted Monday–Thursday; Friday–Sunday are blackout days.', 'task':'Implement allowed(weekday) returning whether a job may run. weekday is an integer 0 (Monday) through 6 (Sunday). Follow the project scheduling policy.', 'starter':'def allowed(weekday):\n    raise NotImplementedError\n','checks':'assert [allowed(i) for i in range(7)] == [True,True,True,True,False,False,False]'},
 {'id':'labels','scenario':'past_normalization_error','fact':'Trim surrounding whitespace but preserve letter case.', 'task':'Implement normalize(label) for a string label according to the project normalization policy.', 'starter':'def normalize(label):\n    raise NotImplementedError\n','checks':'assert normalize("  AbC  ")=="AbC"\nassert normalize("\\n MiXeD\\t")=="MiXeD"\nassert normalize("a b")=="a b"\nassert normalize("")==""'},
 {'id':'cache','scenario':'conditional_zero_ttl','fact':'Zero TTL means do not cache; positive TTL stays unchanged.', 'task':'Implement cache_ttl(ttl) for a nonnegative integer TTL. Return None to bypass caching or an integer expiration TTL according to the project policy.', 'starter':'def cache_ttl(ttl):\n    raise NotImplementedError\n','checks':'assert cache_ttl(0) is None\nassert cache_ttl(1)==1\nassert cache_ttl(120)==120'},
 {'id':'handoff','scenario':'agent_handoff_retry','fact':'Retrying an event with an already-recorded event_id must leave the ledger unchanged; new events append in arrival order.', 'task':'Implement apply_event(ledger, event), returning a list of event dicts. Each event has event_id and value. This function is called by a different worker after handoff. Follow the project retry policy.', 'starter':'def apply_event(ledger, event):\n    raise NotImplementedError\n','checks':'a=[{"event_id":"a","value":1}]\nassert apply_event(a,{"event_id":"a","value":999}) == [{"event_id":"a","value":1}]\nb=[{"event_id":"z","value":2}]\nassert apply_event(b,{"event_id":"a","value":3}) == [{"event_id":"z","value":2},{"event_id":"a","value":3}]\nassert apply_event([],{"event_id":"a","value":3}) == [{"event_id":"a","value":3}]'}
]
def evaluate(code, checks, path):
    # Model output executes only in an isolated unprivileged Docker container.
    # No auth, network, repository, or evaluator files are mounted into it.
    p = subprocess.run(['docker','run','--rm','--network','none','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--user','65534:65534','--memory','128m','--cpus','1','-i','python:3.12-alpine','python','-I','-'],input=code+'\n'+checks,text=True,capture_output=True,timeout=20)
    return p.returncode == 0, p.stderr[-2000:]
def run_one(task, condition, rep, ordinal, out, auth, exporter, approval):
    run_id=f'{task["id"]}-{condition}-{rep}'
    run_dir=out/run_id; run_dir.mkdir(exist_ok=True)
    record=dict(experiment_id='v010-synthetic-code-20261015',commit=BASELINE,fixture_sha256=hashlib.sha256(json.dumps(TASKS,sort_keys=True).encode()).hexdigest(),task_id=task['id'],scenario=task['scenario'],condition=condition,direction='host-retrieval-to-codex',replicate=rep,order=ordinal,model=MODEL,reasoning_effort='low',temperature=None,seed=None,success=None,timeout=False,rule_violations=None,rework_count=0,input_tokens=None,output_tokens=None,cached_input_tokens=None,provider_cost_usd=None,setup_seconds=None,maintenance_seconds=None,review_seconds=None,retrieval_ms=None,task_seconds=None,manual_interventions=0,approval_basis=approval if condition=='arkism' else None,evidence_path=str(run_dir.name))
    fact=''
    if condition=='static': fact=task['fact']
    if condition=='arkism':
        if not exporter: raise RuntimeError('Arkism requires real HTTP exporter; no simulation fallback')
        env=os.environ.copy();env.update(ARKISM_BENCH_FACT=task['fact'],ARKISM_BENCH_OUTPUT=str((run_dir/'retrieval.json').resolve()))
        p=subprocess.run([exporter,'-test.run=^TestBenchmarkHTTPFactExport$','-test.v'],env=env,text=True,capture_output=True,timeout=90)
        (run_dir/'retrieval.log').write_text(p.stdout+p.stderr)
        if p.returncode: raise RuntimeError('HTTP exporter failed; see retrieval.log')
        retrieved=json.loads((run_dir/'retrieval.json').read_text())
        fact=retrieved['text'];record['retrieval_ms']=retrieved['retrieval_ms'];record['setup_seconds']=retrieved['machine_setup_seconds']
        record['returned_rule_ids']=retrieved['returned_rule_ids'];record['source_ids']=retrieved['source_ids']
        if fact!=task['fact']: raise RuntimeError('static and Arkism information differ')
    prompt='Write Python code completing the supplied module. Return JSON only, with one string key "code" containing the whole module. Use no tools. Do not ask questions; make the best implementation from the available information.\nTask: '+task['task']+'\nStarting module:\n'+task['starter']+'\nProject context:\n'+(fact or '(No additional project context.)')
    (run_dir/'prompt.txt').write_text(prompt)
    with tempfile.TemporaryDirectory(prefix='arkism-model-') as tmp:
        home=pathlib.Path(tmp); profile=home/'profile';profile.mkdir();shutil.copyfile(auth,profile/'auth.json');(profile/'auth.json').chmod(0o600)
        env={k:v for k,v in os.environ.items() if not (k.startswith(('CODEX','CLAUDE','ANTHROPIC','OPENAI','ARKISM','MEMORY')) or k=='POSTGRES_TEST_DSN')}
        env.update(HOME=str(home),CODEX_HOME=str(profile));env['PATH']=os.environ['PATH']
        cmd=['codex','exec','--ignore-user-config','--ignore-rules','--ephemeral','--skip-git-repo-check','--json','-m',MODEL,'-c','model_reasoning_effort="low"','-c','project_doc_max_bytes=0','-c','model_max_output_tokens=2048','-C',str(home),'-']
        t=time.monotonic()
        try:
            p=subprocess.run(cmd,input=prompt,env=env,cwd=home,text=True,capture_output=True,timeout=90)
            record['task_seconds']=time.monotonic()-t
            # CLI output contains synthetic prompt/output and usage only. Drop thread identifiers.
            events=[];answer=''
            for line in p.stdout.splitlines():
                try: event=json.loads(line)
                except json.JSONDecodeError: continue
                if event.get('type')=='thread.started': continue
                events.append(event)
                if event.get('type')=='turn.completed':
                    for key in ['input_tokens','output_tokens','cached_input_tokens']: record[key]=event.get('usage',{}).get(key)
                if event.get('type')=='item.completed' and event.get('item',{}).get('type')=='agent_message': answer=event['item']['text']
            (run_dir/'events.json').write_text(json.dumps(events,indent=2))
            (run_dir/'cli-status.json').write_text(json.dumps({'returncode':p.returncode,'stderr':p.stderr.replace(str(home),'<isolated-profile>')},indent=2))
            if p.returncode != 0:
                record.update(success=False,rule_violations=None,cli_returncode=p.returncode)
                answer=''
            if answer.startswith('```'): answer='\n'.join(answer.splitlines()[1:-1])
            try:
                code=json.loads(answer)['code'];(run_dir/'answer.py').write_text(code)
                try:
                    success,error=evaluate(code,task['checks'],run_dir)
                except subprocess.TimeoutExpired:
                    success,error=False,'judge exceeded 20-second limit'
                    record['judge_timeout']=True
                record['success']=success;record['rule_violations']=int(not success)
                (run_dir/'judge.json').write_text(json.dumps({'success':success,'stderr':error},indent=2))
            except (ValueError,KeyError,TypeError) as exc:
                record.update(success=False,rule_violations=1,parse_error=type(exc).__name__)
        except subprocess.TimeoutExpired:
            record.update(timeout=True,success=False,task_seconds=time.monotonic()-t)
    (run_dir/'result.json').write_text(json.dumps(record,indent=2))
    print(json.dumps({k:record[k] for k in ['task_id','condition','replicate','success','task_seconds']}),flush=True)
    return record

def main():
    p=argparse.ArgumentParser();p.add_argument('--conditions',default='none,static,arkism');p.add_argument('--out',type=pathlib.Path,required=True);p.add_argument('--auth',default=str(pathlib.Path.home()/'.codex/auth.json'));p.add_argument('--exporter');p.add_argument('--approval',choices=['synthetic-role-fixture','explicit-human-approved'],default='synthetic-role-fixture');p.add_argument('--repetitions',type=int,default=3);a=p.parse_args()
    a.out.mkdir(parents=True,exist_ok=True)
    schedule=[(t,c,r) for t in TASKS for c in ['none','static','arkism'] for r in range(1,a.repetitions+1)];random.Random(20261015).shuffle(schedule)
    (a.out/'frozen-spec.json').write_text(json.dumps({'baseline':BASELINE,'tasks':TASKS,'schedule':[(t['id'],c,r) for t,c,r in schedule],'limitations':['host retrieves context; model does not call MCP','no real agent A capture','synthetic short code tasks','equal configured budgets, different observed input token counts'],'improvement_threshold':'At least 3 additional successes over static in 15 paired attempts, without extra rule violations; exploratory signal only, not general productivity proof.'},indent=2))
    for i,(t,c,r) in enumerate(schedule):
        if c not in a.conditions.split(','):continue
        if (a.out/f'{t["id"]}-{c}-{r}'/'result.json').exists():continue
        run_one(t,c,r,i,a.out,a.auth,a.exporter,a.approval)
    rows=[json.loads(f.read_text()) for f in a.out.glob('*/result.json')];rows.sort(key=lambda r:r['order'])
    (a.out/'results.json').write_text(json.dumps(rows,indent=2))
    fields=sorted(set().union(*(r.keys() for r in rows)))
    with (a.out/'results.csv').open('w') as f:
        w=csv.DictWriter(f,fields,lineterminator="\n");w.writeheader();w.writerows(rows)
    summary={}
    for c in ['none','static','arkism']:
        rr=[r for r in rows if r['condition']==c];ts=[r['task_seconds'] for r in rr if r['task_seconds'] is not None]
        summary[c]={'runs':len(rr),'successes':sum(r['success'] is True for r in rr),'rule_violations':sum(r['rule_violations'] or 0 for r in rr),'timeouts':sum(r['timeout'] for r in rr),'seconds_mean':statistics.mean(ts) if ts else None,'seconds_stdev':statistics.stdev(ts) if len(ts)>1 else None,'seconds_min':min(ts) if ts else None,'seconds_max':max(ts) if ts else None,'input_tokens':sum(r['input_tokens'] for r in rr) if rr and all(r['input_tokens'] is not None for r in rr) else None,'output_tokens':sum(r['output_tokens'] for r in rr) if rr and all(r['output_tokens'] is not None for r in rr) else None,'input_token_observations':sum(r['input_tokens'] is not None for r in rr),'output_token_observations':sum(r['output_tokens'] is not None for r in rr),'actual_cost_usd':None}
    (a.out/'summary.json').write_text(json.dumps(summary,indent=2));print(json.dumps(summary))
if __name__=='__main__': main()

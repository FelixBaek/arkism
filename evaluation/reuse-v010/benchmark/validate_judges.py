#!/usr/bin/env python3
"""Validate each hidden checker against one correct module and one policy mutant.
These are evaluator-only fixtures, never sent to the model or Arkism.
"""
import json
from run import ROOT,TASKS,evaluate
pairs={
'billing':('def charge(c,n,d): return c*n//d','def charge(c,n,d): return round(c*n/d)'),
'roster':('def allowed(d): return d<4','def allowed(d): return d<5'),
'labels':('def normalize(s): return s.strip()','def normalize(s): return s.strip().lower()'),
'cache':('def cache_ttl(t): return t or None','def cache_ttl(t): return t'),
'handoff':('def apply_event(ledger,event): return ledger if any(e["event_id"]==event["event_id"] for e in ledger) else ledger+[event]','def apply_event(ledger,event): return ledger+[event]')}
rows=[]
for t in TASKS:
 for kind,code in zip(['reference','mutant'],pairs[t['id']]):
  passed,error=evaluate(code,t['checks'],ROOT)
  assert passed==(kind=='reference'),(t['id'],kind,error)
  rows.append({'task_id':t['id'],'kind':kind,'judge_passed':passed,'expected_judge_passed':kind=='reference','code':code,'stderr':error})
(ROOT/'evaluator-validation.json').write_text(json.dumps(rows,indent=2))
print('5 reference modules accepted; 5 policy mutants rejected')

#!/usr/bin/env python3
"""Independent deterministic scoring of live agent final answers and MCP events."""
import csv,json,pathlib,sys
root=pathlib.Path(sys.argv[1]);runs=json.loads((root/'runs.json').read_text());scores=[]
for direction in (1,2):
 label=f'direction-{direction}';validation=next(r for r in runs if r['label']==label+'-validation')
 events=[json.loads(l) for l in (root/(label+'-reader.jsonl')).read_text().splitlines()]
 final='';called=False;recall_ids=set()
 def valid_recall(body):
  if isinstance(body,str):
   try:body=json.loads(body)
   except ValueError:return False
  return isinstance(body,dict) and any(m.get('id')==validation['memory_id'] and m.get('text')==validation['fact'] for m in body.get('memories',[]))
 for e in events:
  if e.get('type')=='result':final=e.get('result','')
  item=e.get('item',{})
  if item.get('type')=='agent_message':final=item.get('text','')
  if item.get('type')=='mcp_tool_call' and item.get('tool')=='recall' and item.get('status')=='completed' and not item.get('error'):
   called=called or valid_recall((item.get('result') or {}).get('structured_content'))
  message=e.get('message',{})
  for block in (message.get('content',[]) if isinstance(message,dict) else []):
   if not isinstance(block,dict):continue
   if block.get('type')=='tool_use' and block.get('name','').endswith('__recall'):recall_ids.add(block['id'])
   if block.get('type')=='tool_result' and block.get('tool_use_id') in recall_ids and not block.get('is_error'):called=called or valid_recall(block.get('content'))
 try:answer=json.JSONDecoder().raw_decode(final[final.index('{'):])[0]
 except (ValueError,TypeError):answer={}
 correct=answer.get('transformed_label')=='MiXeD' if direction==1 else answer.get('cache_zero') is False and answer.get('positive_ttl')==15
 scores.append({'direction':validation['writer']+' -> '+validation['reader'],'actual_recall':called,'task_correct':correct,'source_id_correct':answer.get('source_id') in validation['source_ids'],'preapproval_search_count':validation['before_items'],'answer':answer,'evidence':label+'-reader.jsonl'})
(root/'scores.json').write_text(json.dumps(scores,indent=2)+'\n')
with (root/'scores.csv').open('w',newline='') as f:
 w=csv.DictWriter(f,fieldnames=scores[0],lineterminator="\n");w.writeheader();w.writerows(scores)
print(json.dumps(scores,indent=2))

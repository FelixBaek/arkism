#!/usr/bin/env python3
"""Retain compressed Go JSONL evidence and machine-readable check outcomes."""
import argparse,collections,gzip,hashlib,json,pathlib
p=argparse.ArgumentParser();p.add_argument('source');p.add_argument('destination');p.add_argument('--label',required=True);p.add_argument('--commit',required=True);a=p.parse_args()
src=pathlib.Path(a.source);dest=pathlib.Path(a.destination);dest.mkdir(parents=True,exist_ok=True);raw=src.read_bytes();(dest/(a.label+'.jsonl.gz')).write_bytes(gzip.compress(raw,mtime=0))
counts=collections.Counter();top=collections.Counter();skips=[];failures=[];packages=[]
for line in raw.splitlines():
 try:e=json.loads(line)
 except ValueError:continue
 if e.get('Test') and e['Action'] in ('pass','fail','skip'):
  counts[e['Action']]+=1
  if '/' not in e['Test']:top[e['Action']]+=1
  if e['Action']=='skip':skips.append({'package':e['Package'],'test':e['Test']})
 if e['Action']=='fail':failures.append(e)
 if not e.get('Test') and e['Action'] in ('pass','fail','skip'):packages.append({'package':e['Package'],'result':e['Action'],'seconds':e.get('Elapsed')})
r={'label':a.label,'product_commit':a.commit,'raw_sha256':hashlib.sha256(raw).hexdigest(),'counts_including_subtests':dict(counts),'top_level_counts':dict(top),'skips':skips,'failures':failures,'packages':packages,'command':'go test -race -tags=integration -json -count=1 ./...','environment':'disposable loopback pgvector Postgres 17; no model providers'}
(dest/(a.label+'-summary.json')).write_text(json.dumps(r,indent=2)+'\n');print(json.dumps({'label':a.label,'top_level':dict(top),'all':dict(counts),'packages':len(packages)}))

#!/usr/bin/env python3
"""Summarize the frozen experiment, retaining every run and failed example."""
import json,pathlib,statistics
root=pathlib.Path(__file__).resolve().parent
rows=json.loads((root/'results/results.json').read_text())
def avg(xs): return f'{statistics.mean(xs):.3f}' if xs else 'N/A'
lines=['# 실제 모델 비교 결과','', '기준 구현: `9379ee5f493716e1c80027c0beb4fd12283100cf` (v0.1.0). 테스트 도구만 이후 추가했다.','', '같은 Codex 모델로 새 세션 45회를 실행했다. Arkism 조건은 **호스트가 실제 HTTP로 검색한 승인 기억을 모델에 전달**했다. 모델의 자율 MCP 검색, 실제 에이전트 A의 수집, 스킬 사용은 이 실험에 포함하지 않았다.','', '|조건|성공|규칙 검사 실패|시간 평균±표준편차(초)|범위(초)|입력 토큰|출력 토큰|','|---|---:|---:|---:|---:|---:|---:|']
for c,label in [('none','기억 없음'),('static','정적 문서'),('arkism','Arkism')]:
 rr=[r for r in rows if r['condition']==c];ts=[r['task_seconds'] for r in rr];lines.append(f"|{label}|{sum(r['success'] for r in rr)}/{len(rr)}|{sum(r['rule_violations'] or 0 for r in rr)}|{avg(ts)} ± {statistics.stdev(ts):.3f}|{min(ts):.3f}–{max(ts):.3f}|{sum(r['input_tokens'] or 0 for r in rr):,}|{sum(r['output_tokens'] or 0 for r in rr):,}|")
lines+=['','시간은 CLI 시작과 추론을 포함하며, 검색·DB 준비·독립 판정 실행은 제외한다. 실제 청구 비용과 사람의 작성·검토·승인 시간은 미측정이다. 재작업 0회는 한 번만 답하게 한 실험 설계의 결과다.','', '|과제|기억 없음|정적 문서|Arkism|','|---|---:|---:|---:|']
for t in ['billing','roster','labels','cache','handoff']:
 vals=[]
 for c in ['none','static','arkism']:
  rr=[r for r in rows if r['condition']==c and r['task_id']==t];vals.append(f"{sum(r['success'] for r in rr)}/{len(rr)}")
 lines.append('|'+t+'|'+'|'.join(vals)+'|')
a=[r for r in rows if r['condition']=='arkism'];lat=[r['retrieval_ms'] for r in a];setup=[r['setup_seconds'] for r in a]
lines+=['',f'검색 {len(a)}회: 평균 {avg(lat)}ms, 표준편차 {statistics.stdev(lat):.3f}ms, 범위 {min(lat):.3f}–{max(lat):.3f}ms. DB 생성·수집·승인 요청 등 기계 준비 평균 {avg(setup)}초. 각 검색은 새 DB에서 승인 전 0건, 승인 후 정확한 기억 1건과 출처 1건을 확인했다. 임베딩·유사도 검색은 사용하지 않았다.','', '정적 문서보다 나은 효과를 판단할 사전 기준은 15회에서 성공 최소 3회 추가였다. 두 조건 모두 15/15로 이 기준을 충족하지 못했다. 이 실험에서는 정보 제공 효과는 관측했지만, 정적 문서 대비 Arkism의 생산성·비용 우월성은 입증하지 못했다. 성공률 차이가 있더라도 실제 저장소 작업이나 장기 업무로 일반화할 수 없다.','', '실행 순서·프롬프트·시작 코드·독립 판정 기준: [frozen-spec.json](results/frozen-spec.json). 모든 실행 수치: [CSV](results/results.csv), [JSON](results/results.json). 환경·설정·초기 fixture 수정·지원되지 않는 출력 상한 설정은 [README](README.md)에 기록했다.','', '## 실패 사례','']
for r in rows:
 if not r['success']:
  p=r['evidence_path'];lines.append(f'- `{p}`: [생성 코드](results/{p}/answer.py), [판정 로그](results/{p}/judge.json), [모델 이벤트](results/{p}/events.json).')
lines+=['','## 실행별 결과','', '|순서|과제|조건|반복|성공|초|입력|출력|검색 ms|','|---:|---|---|---:|---|---:|---:|---:|---:|']
for r in rows:
 lines.append(f"|{r['order']+1}|{r['task_id']}|{r['condition']}|{r['replicate']}|{'성공' if r['success'] else '실패'}|{r['task_seconds']:.3f}|{r['input_tokens']}|{r['output_tokens']}|{r['retrieval_ms'] if r['retrieval_ms'] is not None else '미측정'}|")
(root/'REPORT.ko.md').write_text('\n'.join(lines)+'\n')

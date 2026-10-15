#!/usr/bin/env python3
"""Summarize the frozen experiment, retaining every run and failed example."""
import json,pathlib,statistics
root=pathlib.Path(__file__).resolve().parent
def avg(xs): return f'{statistics.mean(xs):.3f}' if xs else 'N/A'
def number(value, decimals=3):
 return '미측정' if value is None else f'{value:.{decimals}f}'

def spread(values):
 return f'{statistics.stdev(values):.3f}' if len(values)>1 else 'N/A'

def span(values):
 return f'{min(values):.3f}–{max(values):.3f}' if values else 'N/A'

def measured_total(rows,field):
 observed=[r.get(field) for r in rows if r.get(field) is not None]
 coverage=f'{len(observed)}/{len(rows)} 관측'
 if not rows or len(observed)!=len(rows):
  return f'미측정 ({coverage})'
 return f'{sum(observed):,} ({coverage})'

def condition_line(rows,label):
 times=[r['task_seconds'] for r in rows if r['task_seconds'] is not None]
 return (f"|{label}|{sum(r['success'] is True for r in rows)}/{len(rows)}|"
         f"{measured_total(rows,'rule_violations')}|{avg(times)} ± {spread(times)}|{span(times)}|"
         f"{measured_total(rows,'input_tokens')}|{measured_total(rows,'output_tokens')}|")


def task_rows(rows):
 lines=[]
 for t in ['billing','roster','labels','cache','handoff']:
  vals=[]
  for c in ['none','static','arkism']:
   rr=[r for r in rows if r['condition']==c and r['task_id']==t];vals.append(f"{sum(r['success'] is True for r in rr)}/{len(rr)}")
  lines.append('|'+t+'|'+'|'.join(vals)+'|')
 return lines


def failure_rows(rows):
 lines=[]
 for r in rows:
  if not r['success']:
   p=r['evidence_path'];lines.append(f'- `{p}`: [생성 코드](results/{p}/answer.py), [판정 로그](results/{p}/judge.json), [모델 이벤트](results/{p}/events.json).')
 return lines


def run_rows(rows):
 lines=[]
 for r in rows:
  lines.append(f"|{r['order']+1}|{r['task_id']}|{r['condition']}|{r['replicate']}|{'성공' if r['success'] else '실패'}|{number(r['task_seconds'])}|{number(r['input_tokens'],0)}|{number(r['output_tokens'],0)}|{r['retrieval_ms'] if r['retrieval_ms'] is not None else '미측정'}|")
 return lines


def render_report(rows):
 lines=['# 실제 모델 비교 결과','', '기준 구현: `9379ee5f493716e1c80027c0beb4fd12283100cf` (v0.1.0). 테스트 도구만 이후 추가했다.','', '같은 Codex 모델로 새 세션 45회를 실행했다. Arkism 조건은 **호스트가 실제 HTTP로 검색한 승인 기억을 모델에 전달**했다. 모델의 자율 MCP 검색, 실제 에이전트 A의 수집, 스킬 사용은 이 실험에 포함하지 않았다.','', '|조건|성공|규칙 검사 실패|시간 평균±표준편차(초)|범위(초)|입력 토큰|출력 토큰|','|---|---:|---:|---:|---:|---:|---:|']
 for c,label in [('none','기억 없음'),('static','정적 문서'),('arkism','Arkism')]:
  rr=[r for r in rows if r['condition']==c];lines.append(condition_line(rr,label))
 lines+=['','시간은 CLI 시작과 추론을 포함하며, 검색·DB 준비·독립 판정 실행은 제외한다. 실제 청구 비용과 사람의 작성·검토·승인 시간은 미측정이다. 재작업 0회는 한 번만 답하게 한 실험 설계의 결과다.','', '|과제|기억 없음|정적 문서|Arkism|','|---|---:|---:|---:|']
 lines.extend(task_rows(rows))
 a=[r for r in rows if r['condition']=='arkism'];lat=[r['retrieval_ms'] for r in a if r['retrieval_ms'] is not None];setup=[r['setup_seconds'] for r in a if r['setup_seconds'] is not None]
 lines+=['',f'검색 {len(a)}회: 평균 {avg(lat)}ms, 표준편차 {spread(lat)}ms, 범위 {span(lat)}ms. DB 생성·수집·승인 요청 등 기계 준비 평균 {avg(setup)}초. 각 검색은 새 DB에서 승인 전 0건, 승인 후 정확한 기억 1건과 출처 1건을 확인했다. 임베딩·유사도 검색은 사용하지 않았다.','', '정적 문서보다 나은 효과를 판단할 사전 기준은 15회에서 성공 최소 3회 추가였다. 두 조건 모두 15/15로 이 기준을 충족하지 못했다. 이 실험에서는 정보 제공 효과는 관측했지만, 정적 문서 대비 Arkism의 생산성·비용 우월성은 입증하지 못했다. 성공률 차이가 있더라도 실제 저장소 작업이나 장기 업무로 일반화할 수 없다.','', '실행 순서·프롬프트·시작 코드·독립 판정 기준: [frozen-spec.json](results/frozen-spec.json). 모든 실행 수치: [CSV](results/results.csv), [JSON](results/results.json). 환경·설정·초기 fixture 수정·지원되지 않는 출력 상한 설정은 [README](README.md)에 기록했다.','', '## 실패 사례','']
 lines.extend(failure_rows(rows))
 lines+=['','## 실행별 결과','', '|순서|과제|조건|반복|성공|초|입력|출력|검색 ms|','|---:|---|---|---:|---|---:|---:|---:|---:|']
 lines.extend(run_rows(rows))
 return '\n'.join(lines) + '\n'


def main():
 rows=json.loads((root/'results/results.json').read_text())
 with (root / 'REPORT.ko.md').open('w') as report:
  report.write(render_report(rows))


if __name__ == '__main__':
 main()

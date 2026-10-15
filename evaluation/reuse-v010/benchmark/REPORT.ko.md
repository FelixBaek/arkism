# 실제 모델 비교 결과

기준 구현: `9379ee5f493716e1c80027c0beb4fd12283100cf` (v0.1.0). 테스트 도구만 이후 추가했다.

같은 Codex 모델로 새 세션 45회를 실행했다. Arkism 조건은 **호스트가 실제 HTTP로 검색한 승인 기억을 모델에 전달**했다. 모델의 자율 MCP 검색, 실제 에이전트 A의 수집, 스킬 사용은 이 실험에 포함하지 않았다.

|조건|성공|규칙 검사 실패|시간 평균±표준편차(초)|범위(초)|입력 토큰|출력 토큰|
|---|---:|---:|---:|---:|---:|---:|
|기억 없음|5/15|10|10.882 ± 2.365|7.813–15.795|213,288|2,074|
|정적 문서|15/15|0|7.023 ± 1.615|5.076–10.027|213,408|645|
|Arkism|15/15|0|7.073 ± 1.132|5.915–9.056|213,396|592|

시간은 CLI 시작과 추론을 포함하며, 검색·DB 준비·독립 판정 실행은 제외한다. 실제 청구 비용과 사람의 작성·검토·승인 시간은 미측정이다. 재작업 0회는 한 번만 답하게 한 실험 설계의 결과다.

|과제|기억 없음|정적 문서|Arkism|
|---|---:|---:|---:|
|billing|0/3|3/3|3/3|
|roster|0/3|3/3|3/3|
|labels|0/3|3/3|3/3|
|cache|3/3|3/3|3/3|
|handoff|2/3|3/3|3/3|

검색 15회: 평균 12.800ms, 표준편차 2.100ms, 범위 10.345–17.911ms. DB 생성·수집·승인 요청 등 기계 준비 평균 0.266초. 각 검색은 새 DB에서 승인 전 0건, 승인 후 정확한 기억 1건과 출처 1건을 확인했다. 임베딩·유사도 검색은 사용하지 않았다.

정적 문서보다 나은 효과를 판단할 사전 기준은 15회에서 성공 최소 3회 추가였다. 두 조건 모두 15/15로 이 기준을 충족하지 못했다. 이 실험에서는 정보 제공 효과는 관측했지만, 정적 문서 대비 Arkism의 생산성·비용 우월성은 입증하지 못했다. 성공률 차이가 있더라도 실제 저장소 작업이나 장기 업무로 일반화할 수 없다.

실행 순서·프롬프트·시작 코드·독립 판정 기준: [frozen-spec.json](results/frozen-spec.json). 모든 실행 수치: [CSV](results/results.csv), [JSON](results/results.json). 환경·설정·초기 fixture 수정·지원되지 않는 출력 상한 설정은 [README](README.md)에 기록했다.

## 실패 사례

- `roster-none-1`: [생성 코드](results/roster-none-1/answer.py), [판정 로그](results/roster-none-1/judge.json), [모델 이벤트](results/roster-none-1/events.json).
- `roster-none-2`: [생성 코드](results/roster-none-2/answer.py), [판정 로그](results/roster-none-2/judge.json), [모델 이벤트](results/roster-none-2/events.json).
- `labels-none-2`: [생성 코드](results/labels-none-2/answer.py), [판정 로그](results/labels-none-2/judge.json), [모델 이벤트](results/labels-none-2/events.json).
- `labels-none-1`: [생성 코드](results/labels-none-1/answer.py), [판정 로그](results/labels-none-1/judge.json), [모델 이벤트](results/labels-none-1/events.json).
- `labels-none-3`: [생성 코드](results/labels-none-3/answer.py), [판정 로그](results/labels-none-3/judge.json), [모델 이벤트](results/labels-none-3/events.json).
- `roster-none-3`: [생성 코드](results/roster-none-3/answer.py), [판정 로그](results/roster-none-3/judge.json), [모델 이벤트](results/roster-none-3/events.json).
- `handoff-none-3`: [생성 코드](results/handoff-none-3/answer.py), [판정 로그](results/handoff-none-3/judge.json), [모델 이벤트](results/handoff-none-3/events.json).
- `billing-none-2`: [생성 코드](results/billing-none-2/answer.py), [판정 로그](results/billing-none-2/judge.json), [모델 이벤트](results/billing-none-2/events.json).
- `billing-none-3`: [생성 코드](results/billing-none-3/answer.py), [판정 로그](results/billing-none-3/judge.json), [모델 이벤트](results/billing-none-3/events.json).
- `billing-none-1`: [생성 코드](results/billing-none-1/answer.py), [판정 로그](results/billing-none-1/judge.json), [모델 이벤트](results/billing-none-1/events.json).

## 실행별 결과

|순서|과제|조건|반복|성공|초|입력|출력|검색 ms|
|---:|---|---|---:|---|---:|---:|---:|---:|
|1|roster|none|1|실패|12.754|14217|168|미측정|
|2|roster|none|2|실패|9.167|14217|82|미측정|
|3|labels|arkism|1|성공|8.412|14201|20|12.345|
|4|billing|static|1|성공|5.993|14226|30|미측정|
|5|billing|static|2|성공|6.786|14229|30|미측정|
|6|roster|arkism|2|성공|6.289|14229|25|12.546|
|7|handoff|arkism|3|성공|7.858|14254|73|16.991|
|8|labels|static|3|성공|6.261|14207|20|미측정|
|9|billing|arkism|1|성공|6.188|14229|30|12.356|
|10|labels|none|2|실패|12.445|14201|146|미측정|
|11|labels|none|1|실패|11.109|14201|93|미측정|
|12|cache|static|3|성공|5.635|14229|28|미측정|
|13|labels|arkism|3|성공|6.195|14198|20|12.312|
|14|labels|none|3|실패|10.394|14204|126|미측정|
|15|roster|arkism|1|성공|6.616|14226|25|10.475|
|16|handoff|static|2|성공|10.027|14251|116|미측정|
|17|handoff|none|1|성공|14.725|14232|241|미측정|
|18|handoff|none|2|성공|12.870|14235|202|미측정|
|19|labels|static|2|성공|6.796|14207|20|미측정|
|20|cache|none|3|성공|9.537|14219|119|미측정|
|21|roster|none|3|실패|8.552|14220|77|미측정|
|22|handoff|static|1|성공|9.885|14248|108|미측정|
|23|billing|arkism|3|성공|8.311|14226|30|13.511|
|24|handoff|static|3|성공|9.557|14248|112|미측정|
|25|handoff|arkism|1|성공|8.898|14251|110|17.911|
|26|billing|static|3|성공|6.049|14226|30|미측정|
|27|roster|arkism|3|성공|5.918|14229|25|12.608|
|28|billing|arkism|2|성공|6.602|14232|30|12.344|
|29|handoff|none|3|실패|15.795|14229|279|미측정|
|30|cache|arkism|3|성공|6.537|14223|28|11.299|
|31|labels|static|1|성공|5.897|14204|20|미측정|
|32|cache|static|2|성공|6.796|14223|28|미측정|
|33|roster|static|3|성공|6.475|14229|25|미측정|
|34|cache|arkism|2|성공|7.290|14223|28|13.092|
|35|labels|arkism|2|성공|5.915|14201|20|12.752|
|36|cache|none|1|성공|8.518|14222|82|미측정|
|37|cache|arkism|1|성공|6.009|14229|28|11.12|
|38|handoff|arkism|2|성공|9.056|14245|100|10.345|
|39|billing|none|2|실패|9.749|14220|125|미측정|
|40|billing|none|3|실패|9.253|14229|122|미측정|
|41|billing|none|1|실패|10.553|14226|119|미측정|
|42|cache|static|1|성공|8.247|14223|28|미측정|
|43|cache|none|2|성공|7.813|14216|93|미측정|
|44|roster|static|1|성공|5.076|14229|25|미측정|
|45|roster|static|2|성공|5.865|14229|25|미측정|

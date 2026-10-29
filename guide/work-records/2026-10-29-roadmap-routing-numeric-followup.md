# Preserve finite binary64 values at the JSON boundary

Task: RTE-01 (#155)
Research: F parser correctness; no empirical result
Design: Existing closed routing-v2 schema with field-specific typed validation
Relation: faithful
Deviation: Implementation correction only. The schema, baseline v1 and earlier accepted snapshots remain unchanged.

The common JSON parser rejected decimal integer tokens longer than100 characters
before it knew their destination type. A finite309-digit integer is a valid
binary64 cost value, so the parser rejected an input permitted by its schema.
The bounded tokenizer now retains up to310 characters for typed validation.
Integer fields still require exact int64; overflow, nonfinite values, negative
cost and bool coercion remain rejected.

The author suite passed38 tests. Independent review replayed the earlier139
tests and added13 methods, for152 methods/157 leaf checks with no failures or
skips. An independent struct.pack oracle verified finite309/maxfloat canonical
values; overflow310, huge int64,5000-digit tokens,1e309, NaN, bool and negative
zero boundaries were checked. The earlier rejected input was reproduced from
preserved predecessor bytes before testing the correction.

This is a new implementation revision after8c14279d. The earlier36/139 results,
accepted hashes and logs are preserved rather than reassigned to the new source.
Only three artifacts changed; the other eight, schema and baseline remain pinned.

| Private logical artifact | SHA256 |
|---|---|
| validator.py | e536c3682f584ff6663456faf9ed8a4f3aca41523f0add45972d7d0004765fde |
| test_validator.py | 776389f1ba10d3b801eb8d25278767b343af4ea975f5807fe0f1a1cc1d80d407 |
| integrity.json | 73cf4405fad6ff75ce37b4d8675fb1ed7898ef8ef50edc7ec6fe413d944f64d6 |

No product API/runtime, external model, ledger, effect, build or deployment ran.
The well-formed-record versus authentication/randomization limits remain intact.

## 한국어 작업 기록

작업 ID: RTE-01 (#155)
변경 이유: 공통 숫자 파서의 자릿수 제한이 허용된 finite float64를 거부하는 오류를 고친다.
실행한 검증: 소유38·독립152methods/157leaf, 이전 입력 실패 재현과 새 값 canonical 검산·나머지8아티팩트 불변 확인 통과.
미실행: 제품/API 통합, 실제 제공자·장부·효과 호출, 빌드·배포.
현재 한계: 파서 구현 후속 보정이며 새로운 연구 결과나 실행 인증이 아니다.
커밋 상태: 이전 수용 기록을 보존하고 새3SHA·검증·소비 관계를 후속 단계 PR로 연결한다.

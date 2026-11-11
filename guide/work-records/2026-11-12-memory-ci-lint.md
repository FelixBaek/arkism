# Memory evaluation CI follow-up

Task: MEMORY-QUALITY-90. Research: none.

The CI cognitive-complexity check differs from the repository cyclomatic-debt
gate. The former rejected nine new functions, including nested table-driven
tests, and also rejected three local variables shadowing a builtin name.
Extracted focused verification and test-case helpers without removing scenarios,
assertions, authorization checks or transaction fences. Renamed the shadowing
locals. No lint suppression or threshold change was introduced.

The actual cognitive checker reports no function above its limit of twenty in
the affected files. Focused race and actual PostgreSQL tests passed after the
refactors: source/date selection 2.152 seconds, memory selection 2.040 seconds,
and mental-model selection 2.328 seconds. Whitespace validation passed. The
complete CI lint workflow will validate the combined final change again.

The initial offline attempt to invoke the pinned full lint tool could not resolve
module deprecation metadata with network lookup disabled; it was not a lint pass.

한국어: CI의 인지 복잡도와 기존 순환 복잡도 검사는 서로 다른 기준이다.
검사 제외나 기준 완화 없이 함수와 테스트 사례를 나누고 기본 함수 이름을 가리는
변수를 바꿨다. 수정 후 해당 인지 복잡도 검사와 실제 PostgreSQL 회귀검사는
통과했다. 전체 CI 재검증 결과는 별도로 확인한다.

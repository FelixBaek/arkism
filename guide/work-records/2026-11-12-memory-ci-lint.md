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

## Broader memory regression and pinned full lint

The shared record-to-hit fingerprint change justified running the entire
`internal/memory` race suite against disposable PostgreSQL child databases,
beyond the earlier focused Reflect checks. The first invocation was not a pass:
it reached the default ten-minute timeout after 601.276 seconds, and ten top-level
restore tests (including twenty-four failing subtests) lacked the explicitly
required PostgreSQL dump/restore binary directory. Those setup failures and the
timeout log are retained.

A continuation selected only unfinished tests, the setup-failed tests and the
previously skipped backup roundtrip. It supplied the existing PostgreSQL tools
and an explicit thirty-minute timeout; already-passing top-level tests were not
rerun. The continuation passed in 144.593 seconds. Combining terminal results by
unique test name gives 2,208 top-level tests and 5,046 subtests passed, with no
unresolved failed tests. Eighteen top-level tests and seven subtests remained
skipped. These require opt-in research/export/scale runs, a separately owned host
receipt fixture, or an MCP subprocess binary; a subprocess-only helper also skips
when invoked as a standalone test. The existing API daemon is not the required
MCP CLI and was not substituted for it.

Twenty-one large-archive tests carry explicit `!race` build constraints and were
not executed by this race run. They are exclusions, not passes. The three mixed
tool-envelope tests were added after the original regression binary was compiled;
they are covered separately by the successful focused loop checks and the
4.736-second Reflect race run recorded in
[the mixed-envelope work record](2026-11-12-reflect-mixed-tool-envelope.md).
Their results are not added to the broader counts above.

`go vet ./internal/memory` passed for Linux, Windows and Darwin using Go 1.27.4.
The pinned golangci-lint v2.14.0 binary, itself built with Go 1.27.4, subsequently
ran `golangci-lint run ./...` over the full working tree and reported `0 issues.`
with exit status zero. This successful invocation included the mixed-envelope
changes before their commit. Its private output and exit-status file are retained;
the earlier unresolved/offline invocation is not counted as evidence of success.

After every test owner confirmed completion, the owned PostgreSQL test cluster
was stopped and its data directory removed. Test and lint logs remain available.
The evaluation database was not changed by these regression checks. No production
binary build or deployment was performed for this verification follow-up.

한국어: 전체 메모리 race 검사는 첫 실행의 시간 초과와 PostgreSQL 도구 경로 누락을
보존한 뒤 미완료·설정 실패 항목만 이어서 검사했다. 최종적으로 최상위 2,208개와
하위 5,046개가 통과했고 미해결 실패는 없다. 최상위 18개·하위 7개 건너뛰기와
`!race` 대용량 검사 21개 제외는 통과로 계산하지 않았다. 나중에 추가된 혼합 도구
응답 검사 3개는 별도 성공 기록을 따른다. 세 운영체제 vet와 고정 버전 전체 lint가
통과했으며, 전용 PostgreSQL 검사 환경은 로그를 남기고 정리했다.

## Final cross-platform test-package correction

Repeating vet on candidate 75a2e407 caught a newer test-package portability
failure: two mixed-envelope service-loop tests referenced the Linux-only fixture
from an untagged file. Linux vet succeeded, but Windows and Darwin vet rejected
the undefined helper; the corresponding Windows CI failure was also observed.
The fixture-dependent tests now use the same Linux constraint as the existing
service fixtures. The pure parser test was moved to a separate portable file and
uses a literal tool envelope, so it remains checked on all platforms. Production
code and the evaluation binary behavior are unchanged.

Go 1.27.4 vet passed again on Linux, Windows and Darwin after this correction.
The platform fix does not turn earlier failed runs into passes; their logs remain
preserved separately. No test assertions were removed.

The three mixed-envelope tests also passed under Linux race instrumentation
(1.258 seconds) after the split.

# Memory follow-ups / 기억 추가 개선

Task: L1–L5, R1–R8, F1–F5, A1–A4 from the memory full audit (22 items).
Research: none. Synthetic measurements do not establish production quality.
Why: implement the additional candidates without changing legacy defaults or touching other worktrees.
Verified: all 20 implementable candidates passed the final integrated memory suite; the fixed recall comparison also passed with unchanged covered evidence. R6/F5 measurement infrastructure is implemented, with data-dependent application decisions held.
Limits: no publication, deployment, live provider calls or production access. Data-dependent research remains held.

제목: 추가 개선 후보 22개 구현
이유: 전체 기억 점검에서 발견한 동작·확장성·검증 누락을 개선한다.
검증: 구현 가능한 20개 항목의 통합 메모리 검사가 통과했다. 고정된 질문 453개의 실제 검색 906회도 완료했으며 기존 근거 회수율을 유지했다.
한계: 실제 운영 자료가 필요한 효과 판단은 보류하며, 기본 동작과 기존 안전 경계를 유지한다.
연구: 해당 없음.

| ID | Change / 개선 | State / 상태 |
| --- | --- | --- |
| L1 | Cumulative extraction budget / 파일 전체 누적 추출 예산 | Implemented; final memory integration passed |
| L2 | Ambiguous typed entity endpoints / 모호한 동명이인 연결 거부 | Implemented; final memory integration passed |
| L3 | Durable summary scheduling / 승인 후 요약 예약 복구·실행 중 대기 | Implemented; final memory integration passed |
| L4 | Directive maintenance and history / 비활성 규칙 용량 정리·전체 관리 이력 | Implemented; final memory integration passed |
| L5 | Complete stale cascade traversal / 오래된 요약 전체 순회 | Implemented; final memory integration passed |
| R1 | Temporal candidate starvation / 날짜에 맞는 기억 누락 수정 | Implemented; focused PG regression passed |
| R2 | Stable graph fan-out / 그래프 조회 순서 고정 | Implemented; four branch regressions passed |
| R3 | NFC tokens and resumable backfill / 문자 정규화·기존 인덱스 갱신 | Implemented; 511-row backfill checked |
| R4 | Explicit multiple time windows / 명시한 복수 기간 비교 조회 | Implemented; HTTP final context checked |
| R5 | Finite ranking weights / NaN·무한 가중치 거부 | Implemented; prior failure reproduced |
| R6 | Vector scale evidence / 벡터 규모별 측정 | Synthetic measurement implemented; production ANN decision held for data |
| R7 | Consumer and UI options / MCP·CLI·UI 문맥 선택지 | Implemented; service, host and browser checks passed |
| R8 | Explicit conversation order / 대화 수집 순번·누락 구간 보존 | Implemented; final memory integration passed |
| F1 | Independent first evidence / 같은 작업의 중복 평가 제외 | Implemented; PG policy checks passed |
| F2 | Public signal batch reads / 가시성 확인 후 평가 일괄 조회 | Implemented; visibility and malformed-result checks passed |
| F3 | Policy settings and explanation / 평가 정책 설정·표본·가중치 표시 | Implemented; HTTP and keyboard checks passed |
| F4 | SDK composed request fields / SDK 요청 타입 누락 수정 | Implemented; positive/negative compile checks passed |
| F5 | Metadata revision classification / 메타데이터 변경 원인 기록 | Classification implemented; snapshot reuse/operational frequency held for data |
| A1 | Complete discovery and durable purge / 삭제 영향 전체 탐색·제거 재개 | Implemented; 1,008 sources and 70 dependency edges checked |
| A2 | Bounded retention / 감사·모델 추적 보존기간 배치 정리 | Implemented; interrupted cleanup and collection isolation checked |
| A3 | Authorized component health / 운영자용 구성요소 상태 진단 | Implemented; authorization and PG degradation checked |
| A4 | Missing HTTP contracts / 누락 관리 API 14개 응답 검사 | Implemented; strict 279-case validation passed before later integration |

Details / 세부 구현·한계:
[retrieval](2026-11-07-memory-retrieval-followups.md),
[feedback and consumer UI](2026-11-07-memory-feedback-followups.md),
[lifecycle](2026-11-07-memory-followups-lifecycle.md).

R6 and F5 do not claim production deployment or quality improvement. The measurable
infrastructure is present, while the two data-dependent application decisions stay held.
R6·F5는 측정·분류 장치를 구현했으나 실제 자료가 필요한 적용 결정은 계속 보류한다.

## Operations evidence / 운영 검증

A1: complete discovery streams 1,000-row cursor pages in the existing serializable
request transaction, then quarantines the full impact atomically. The legacy
bounded discovery API still fails closed. Canonical purge commits 100 sources or
records and the durable offset together. A new runtime receipt resets the offsets
for a genuine re-purge. Tests cover 1,008 sources, interruption/new service resume,
70 dependency edges, scope isolation, cancellation, and refusal to claim completion
while child erasures remain. Discovery itself restarts after rollback; its complete
ID manifest still consumes memory and must fit the request deadline. No partial
manifest is accepted as complete. Existing holds, runtime receipts, child-artifact
and backup verification remain required.

A2: retention deletes at most 1,000 rows per committed statement, with a fixed
cutoff. Remaining eligible rows are the resume marker. Preview stays read-only;
token-bound/non-owner callers remain denied. Tests cover interruption, 1,003 old
rows, a distinct collection and re-entry with the same cutoff.

A3: GET /metrics/health requires human/service ops:metrics authorization and returns
only fixed component codes for database, schema, search, write gate and blobs.
Public /readyz remains opaque. Real PG missing-index and closed-connection tests,
anonymous/agent rejection and identifier-leak checks passed. This does not probe
model providers or establish production availability.

A4: the 14 previously missing management endpoints now have actual successful and
anonymous-denial HTTP fixtures, including wiki mutations, extension data and document
history/chunks. Strict offline OpenAPI validation passed: 434 operations and 279
real response/request cases. No provider calls were made.

Verified: scoped memory tests matching Privacy, RetentionBatch, ExpireAudit,
ComponentHealth, OpenAPIHTTPResponses and SchemaMigration passed (16.956 seconds)
against disposable PostgreSQL. This is scoped validation, not the final integrated run.

검증: 대량 삭제·깊은 의존성·재개·격리·법적 보류 관련 회귀 검사와 운영 API 검사가 통과했다.
한계: 탐색 중 중단은 전체 롤백 후 재시도한다. 접수된 삭제의 실제 제거 단계는 저장된 위치에서
재개한다. 운영 환경, 실제 모델, 대규모 운영 부하에서의 성능까지 검증한 것은 아니다.


Independent review also found that schema ownership alone does not prove global
row visibility under FORCE RLS. A real PostgreSQL regression reproduced the old
preview falsely returning zero without error despite an old audit row. Retention
now separately requires superuser, BYPASSRLS, or the explicitly privileged
arkism_init role; ordinary owners are denied before counting or deleting. General
migration-owner eligibility is unchanged. The first fixture lacked policy-function
EXECUTE and returned unavailable; granting ordinary application policy access
reproduced the actual false-zero case before the fix.

검증 보완: 일반 스키마 소유자의 RLS 부분 조회가 0건 완료로 잘못 보고되는 상황을 실제 DB에서
재현했다. 보존기간 작업에만 전역 조회 권한을 추가 확인하고, 마이그레이션 권한은 바꾸지 않았다.


The first integrated run exposed two issues: FileSourceInput lacked the new
conversation_order field shared with CreateInput, and the existing extension
restart test charged a cold process start against its 300ms fault deadline.
The schema is synchronized. The extension test still demands an actual transport
deadline and closed/open behavior at 300ms, then separately verifies a real
restarted process/handshake and healthy request under a test-only 5s bound.
Production timeouts are unchanged. Twenty repeated cases passed (20.202s), as did
the related extension suite (3.103s). These failures remain in the first-run log;
a clean full integration run is required after the fixes.

Strict integrated HTTP validation now covers 435 operations and 284 real cases,
including both-period final context and the new policy explanation endpoint.
SDK tests passed all eight cases with an installed TypeScript compiler, including
positive/negative request-type checks. The initial SDK run skipped that compiler
check before TSC_BIN was configured; it is not counted as the final result.
The UI regression used the five existing insight/settings/views/page-views/list
files (all pass). An attempted nonexistent memory-evaluation.test.js is not
counted; evaluation behavior is covered by the real files and browser components.

검증 보완: 전체 검사에서 발견한 입력 스키마 누락과 시간 제한에 민감한 재시작 시험을 수정했다.
원래 실패 차단은 유지하고 제품 timeout은 바꾸지 않았다. 현재 HTTP 284개 응답·요청과
SDK 8개 검사가 통과했고, 최종 전체 검사 결과는 아래에 추가한다.


## Final integrated verification / 최종 통합 검증

- Final memory package: PASS in 362.749 seconds; 2,001 top-level functions passed,
  6,928 including subtests passed, zero failures. Seventeen top-level tests (24
  including subtests) skipped because explicit fixture roots, pinned research
  inputs/locks, subprocess transports, live handoff opt-in or export/copy inputs
  were unavailable. The optional R6 scale test was run separately, as recorded
  in the retrieval work record; a skipped test is not counted as a suite pass.
- Final HTTP corpus: 435 documented operations, 284 real successful/denial
  responses and request bodies validated, with strict management coverage.
  All 102 management DTO mappings match the source; SDK regeneration is current.
- SDK: eight tests passed with TypeScript positive/negative no-emit checks and
  local Python HTTP transport. Checker unit tests: eight passed.
- Host: integrated recall/MCP/CLI checks passed (0.024s); actual capture order
  transmission checks passed (0.016s). UI: five existing regression files passed;
  local browser keyboard, error/retry and packet rendering evidence is recorded
  in the feedback work record. Browser transport replies were synthetic, separate
  from actual server/DB functional tests. Better Design cannot inspect these JS
  inputs; that unsupported invocation is not a design-validation pass.
- The first full run failed two tests (schema synchronization and extension
  restart timing); both were corrected and the clean full run above is the final
  result. The first log remains available as failure evidence.
- All worker commits were verified patch-equivalent in integration. Three clean
  worker worktrees were removed; the prior integration and other worktrees were
  preserved. The disposable test PostgreSQL cluster was stopped and removed.

검증: 메모리 최상위 검사 2,001개, 하위 검사 포함 6,928개가 통과했고 실패는 없다.
17개 제외는 통과에 포함하지 않았다. 실제 HTTP 284개와 SDK·호스트·UI 검사를 별도로
기록했으며, 브라우저 합성 응답 검사를 전체 서비스 E2E 또는 모델 품질 검증으로 부풀리지 않는다.


## Fixed recall comparison / 고정 질문 실제 기억검색 비교

The isolated offline harness completed 906 HTTP searches for 453 fixed questions
at limits 10 and 50; 451 questions had scorable evidence. Compared with the
immediately preceding memory-improvements run, the covered evidence sets were
identical for every question at combined@10, memories@10 and combined@50.
Combined Recall@10 stayed 0.8565410199556541 (85.65%); memories-only Recall@10
stayed 0.7472653362897265; combined Recall@50 stayed 0.9369469960933375.
There were zero degraded results. The model-call trap recorded zero calls and
non-embedding model usage remained 0 → 0. This demonstrates preservation of the
fixed retrieval baseline, not improved model answers or production latency.

The harness used integration b05f7cd7; subsequent commits only changed tests,
contracts and documentation, with no production Go changes. The harness exited
successfully. Its temporary containers, listeners, runtime directory and compiled
binary were cleaned up; the fixed inputs, comparison outputs and other runs were
preserved. The auxiliary log-status parser could not classify HTTP status fields
(906 unknown), so it is not used as evidence that all responses were HTTP 200.
The completed harness results, evidence comparison and non-degraded responses
are the functional retrieval evidence.

검증: 고정 질문 453개에 실제 검색 906회를 실행했다. 채점 가능한 451개 질문에서
상위 10개 근거 회수율 85.65%를 유지했고, 질문별로 회수한 근거도 이전 결과와 같았다.
모델 호출과 검색 성능 저하 표시는 모두 0건이다. 실제 답변 생성 품질이나 운영 부하에서의
속도 향상을 입증한 것으로 해석하지 않는다. 격리된 검산 환경은 정리했다.

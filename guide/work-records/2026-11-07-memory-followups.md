# Memory follow-ups / 기억 추가 개선

Task: L1–L5, R1–R8, F1–F5, A1–A4 from the memory full audit (22 items).
Research: none. Synthetic measurements do not establish production quality.
Why: implement the additional candidates without changing legacy defaults or touching other worktrees.
Verified: implementation and isolated functional verification are in progress; this document is not a completion claim.
Limits: no publication, deployment, live provider calls or production access. Data-dependent research remains held.

제목: 추가 개선 후보 22개 구현
이유: 전체 기억 점검에서 발견한 동작·확장성·검증 누락을 개선한다.
검증: 영역별 구현 및 임시 DB 기능 검증 진행 중. 완료 후 실제 결과로 갱신한다.
한계: 실제 운영 자료가 필요한 효과 판단은 보류하며, 기본 동작과 기존 안전 경계를 유지한다.
연구: 해당 없음.

| IDs | Area | Status |
| --- | --- | --- |
| L1–L5, R8 | Budgets, entity resolution, durable jobs, lifecycle, conversation order | In progress |
| R1–R6 | Temporal retrieval, graph order, NFC, time windows, finite weights, vector measurement | In progress |
| F1–F5, R7 | Independent evidence, batching, policy UI, SDK, revision measurement, consumer options | In progress |
| A1 | Complete privacy discovery and bounded resumable purge | Implemented; 1,008 sources and 70 dependency edges tested |
| A2 | Bounded audit/model-trace retention with fixed cutoff | Implemented; isolated PG tests pass |
| A3 | Authenticated component diagnosis; opaque public readiness | Implemented; auth and actual PG degradation tests pass |
| A4 | 14 missing HTTP success and anonymous-denial fixtures | Implemented; HTTP tests pass; strict schema validation passes (279 real responses) |

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

# Queue integration with memory maintenance

Task: GOAL5 integration
Research: none
Why: The ordered queue change and memory-quality branch both allocated migration 0007. Their combined state must retain conversation neighbors, normalized search tokens and the durable maintenance gate while preserving queue selection, token isolation and existing job kinds.

The queue change from 8263ece2 is integrated after the memory-quality migrations as **0010_job_queue_indexes.sql**. Registry and upgrade fixtures now exercise 9→10. Migrations 0001–0009 and the frozen pre-migration schema fixture remain byte-identical to the memory-quality parent; the queue SQL itself is byte-identical to its original migration 0007. The approved-entity outbox job kind remains supported in actual claims and the plan/concurrency fixtures. The historical [GOAL5 record](2026-11-08-goal5-ordered-job-claims.md) describes the separate original implementation and its measurements, not a rerun of this integrated state.

## Integrated verification

One scoped race run passed **79 top-level tests and 56 subtests in 44.514 seconds**, with zero failures, skips or race reports. It includes the original GOAL5 selection, queue/schema/privacy tests, maintenance and recovery tests, approved-entity outbox and reindex tests. Three additional real-database integration tests establish:

- An active maintenance gate rejects owner and restricted-token claims, including an empty erasure-only lane, without changing any durable job row or payload. The same restricted connection retains its original sort setting. After cancellation, it can claim an approved-entity job, erasure-only selection remains restricted, and a valid foreign-project job remains inaccessible.
- Both public caller transaction wrappers retain their isolation and sort preference after a claim. Maintenance waits for each already-admitted claim transaction to finish; the test observes the actual blocking backend and verifies the claimed job commits once. New claims then fail while maintenance is active.
- A claim arriving while the actual maintenance operation owns the gate row waits for its commit and subsequently observes the active gate. The test uses a controlled database barrier and observed lock dependencies, and verifies that no job was mutated.

The initial focused integration run also passed four top-level tests and two subtests in 0.673 seconds. Clean initialization, the populated 9→10 upgrade and repeated initialization pass. The existing actual-connection tests cover restricted token/RLS access, forced-generic-plan prewarming, restored on/off settings, unchanged caller isolation, cancellations, eligibility and concurrent rescheduling. Independent review found no blocking correctness issue.

Integrated race-run server execution measurements (milliseconds):

| Query/fixture | Before | After | After page accesses |
|---|---:|---:|---:|
| Claim, 52,000 jobs including 50,000 unrelated kinds | 33.971 | 0.235 | 118 |
| Same claim, forced generic plan under claim preference | — | 0.915 | 118 |
| Claim, 50,000 jobs of the requested kind | — | 0.714 | 118 |
| Same requested-kind fixture, forced generic plan | — | 0.391 | 118 |
| Privacy advancement, empty matching kind | 75.044 | 0.073 | 3 |
| Backup waiting, empty matching kind | 0.035 | 0.214 | 3 |
| Extraction waiting, empty matching kind | 0.025 | 0.054 | 3 |
| 50,000 future jobs and one unscheduled ready job | — | 0.111 | 14 |

The large claim plans have no Sort or Bitmap nodes. Empty kind polls may sort zero rows; the three-page bound shows they avoid the unrelated backlog. The two-second test ceiling is only a CI hang regression bound. Small empty backup/extraction timings increased in this run; no universal latency improvement is claimed. Restricted-token access across 1,000 earlier inaccessible jobs remained correct but cost 3,500 pages and 6.011 ms. A 50,000-unexpired-lease fixture read eight pages in 0.102 ms, without establishing a bound for arbitrary lease distributions.

The 4-worker and 12-worker fixtures each claimed 240 distinct jobs with unique lease tokens and one attempt/revision increment. They observed 196.60 and 297.58 claims per second (1.514 times). These race-instrumented observations include shared-host contention and are not exclusive-host or linear-scaling guarantees. No extra ordinary performance run was needed to establish the integration's correctness.

## Boundaries and operation

The public transaction wrapper checks and holds the durable shared gate before invoking ClaimJob. The claim's read-committed default, unnamed query execution and temporary sort preference do not bypass that gate. Existing caller transactions preserve their isolation. The gate drains **database transactions**, not an entire external worker operation after its claim has committed. Raw owner SQL and explicitly internal maintenance transactions remain privileged bypasses; these tests do not claim to fence arbitrary administrative SQL.

Migration 0010 uses ordinary CREATE INDEX inside the migration transaction, so applying it can make job writes wait. Three additional indexes increase write/storage work. RLS filtering, dead entries awaiting vacuum and varied running-lease priorities can increase scanning. Ordering remains snapshot priority with locked rows skipped and changed selected run times rejected for retry; commit-time global FIFO is not promised.

All tests were limited to the memory package, with a 6 GiB scope and a 900-second outer timeout. The isolated UTF8 database had a 2 GiB server cap and peaked at 697.1 MiB. No product build, deployment, production access, remote request or push occurred. The independent review's optional suggestion to explicitly join goroutines on failing test exits was not added: those exits already cancel contexts, release barriers and close fixture pools; successful paths join their result channels. This is a test-cleanup hardening opportunity, not an observed product defect.

Private artifacts are retained under `/tmp/arkism-memory-quality-queue-20261108/`: the exact test pattern, complete JSON test log, immutable-file hashes and raw before/after plans. Cleanup found zero test databases and roles, stopped the owned server successfully and removed only its data/socket directories. Original memory-quality and GOAL5 worktrees were not modified by this integration.

제목: 기억 유지보수 게이트와 정렬 작업 대기열 통합
이유: 중복된 마이그레이션 번호를 0010으로 분리하고 기존 9개 원문과 승인 엔티티 작업 종류를 보존하면서 실제 유지보수·권한 경계를 검증한다.
검증: 통합 race 검사 79개와 하위 검사 56개가 44.514초에 통과했고 실패·생략은 0개다. 실제 DB에서 유지보수 활성 중 claim 차단, 진행 중 호출자 트랜잭션 배출, 유지보수 커밋 대기, 권한 분리, 설정·격리 수준 복원, 초기 설치와 9→10 업그레이드를 확인했다. 52,000건 claim은 33.971→0.235ms였으며 페이지 접근은 118개였다. 전용 DB만 종료하고 데이터·소켓만 정리했다.
한계: 유지보수 게이트는 DB 트랜잭션 종료까지 기다리며 claim 이후 외부 작업 전체를 기다리지는 않는다. 관리용 원시 SQL 우회, 권한 필터에 따른 추가 스캔, 인덱스 생성 중 쓰기 대기와 공유 호스트 성능 변동을 남긴다. 제품 빌드·push는 하지 않았다.

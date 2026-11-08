# GOAL5: ordered job claims and bounded kind polling

Historical record: the measurements below describe the original GOAL5 branch at commit 8263ece2, where the queue migration was 0007 and the upgrade was 6→7. In the integrated memory-quality branch, the same queue index SQL is migration 0010, after unchanged migrations 0001–0009. See [the integration record](2026-11-08-memory-quality-queue-integration.md) for the combined maintenance/recovery/token checks and 9→10 upgrade evidence.

Task: GOAL5
Research: none
Why: The previous claim combined waiting jobs and expired leases in one OR query, then sorted a large result. Unrelated job kinds increased every worker's work. Privacy advancement also inspected the general queue when its own kind was empty.

Changes: Migration 0007 adds per-kind waiting/run-order and running/run-order indexes, plus a kind/update-order index shared by privacy and extraction polling. The claim merges due, unscheduled and expired-lease streams for each supported kind. A parameterized lateral lookup locks only the selected actual row, rechecks eligibility and skips locked or already leased candidates. Priority remains global `run_at ASC NULLS LAST,id`; lease expiry is eligibility, not priority. There is no branch candidate limit or speculative lock on losing heads.

The planner can cost each sorted branch as a complete retrieval and choose a bitmap scan plus sort, even with these indexes. Claim selection therefore temporarily disables the sort preference and uses an unnamed parameterized execution, avoiding any previously cached generic plan. The original setting is restored after rows close, before payload mutation, and on error. Existing `off` settings and caller-owned transactions are preserved; cancellation uses a bounded independent restoration context. An aborted transaction still requires rollback.

Only standalone claims use read-committed transactions. Lease exclusivity, revision/attempt increments and corrupt-record quarantine remain protected by the selected row lock. Existing caller transactions keep their isolation. If a concurrent reschedule changes the selected row's `run_at`, the claim returns the existing transient retry signal before mutation: standalone execution retries with a fresh snapshot, and caller-owned transactions receive the signal. Ordering describes the candidate snapshot with locked rows skipped; it is not a commit-time global FIFO promise for concurrent inserts or reschedules.

## Verification

- The final scoped race suite passed 55 top-level tests and 38 subtests in 24.080 seconds, with zero failures, skips or race reports. It covers queue leases, crash rollback, corruption quarantine, erasure-only claims, future retries, NULL ordering, locked heads, losing-head availability, restricted tokens, recovery gates, stale revisions, migrations and privacy/extraction behavior.
- The real snapshot-race tests cover a concurrently committed lease, future rescheduling, due rescheduling and an unscheduled transition. The latter two observe the changed times, verify the typed retry signal and verify that a fresh claim chooses the correct next job. The connection from that signal to automatic retry within one standalone call was independently reviewed; that complete single-call timing was not directly injected in the test.
- A connection first caches and executes a generic plan with sorting enabled six times. Subsequent actual claims leave that cached statement's execution count unchanged and preserve both original on/off settings after successful and empty claims. Cancellation restoration is idempotent and leaves the caller transaction usable.
- Fresh initialization, an upgrade from migration 6 with an existing claimable job, and repeated initialization pass. Migrations 0001–0006 and the frozen pre-migration schema fixture are byte-identical to the starting commit.
- All test commands used the memory package, a 6 GiB test scope and a 900-second outer timeout. Only a disposable loopback database was used; its final server cap was 2 GiB. No product build, deployment, remote request, production access or push occurred.

Final race-run server plan measurements, in milliseconds:

| Fixture/query | Before | After | After page accesses |
|---|---:|---:|---:|
| 52,000 jobs, including 50,000 unrelated kinds: claim | 21.213 | 0.436 | 110 |
| Same claim, forced generic plan under the claim preference | — | 0.308 | 110 |
| 50,000 jobs of the claimed kind | — | 0.396 | 110 |
| Same claimed-kind fixture, forced generic plan | — | 0.259 | 110 |
| Privacy advancement, no matching jobs | 25.680 | 0.080 | 3 |
| Backup waiting, no matching jobs | 0.130 | 0.044 | 3 |
| Extraction waiting, no matching jobs | 0.055 | 0.099 | 3 |
| 50,000 future jobs plus one unscheduled ready job | — | 0.105 | 14 |

The large claim plans have no Sort or Bitmap nodes. Empty polling plans may sort zero rows after a kind-index lookup; their measured page bound demonstrates that they do not inspect the unrelated backlog. The test's two-second ceiling is a CI hang regression bound, not the millisecond performance claim. Planning time and complete claim transactions cost more than server execution alone.

Final ordinary-run measurements: the final two large-queue tests passed once in 7.411 seconds. The 52,000-job claim improved from 16.140 to 0.313 ms (110 pages), its forced generic plan took 0.432 ms, and the 50,000-target-kind claim took 0.445 ms (generic 0.239 ms). Privacy advancement improved from 20.807 to 0.044 ms; backup waiting was 0.046→0.060 ms and extraction waiting 0.026→0.033 ms. Both empty polls read only three pages. The final ordinary 4-worker and 12-worker runs measured 538.97 and 855.14 claims/second (1.587 times), with 240 distinct jobs each. These are observations from one controlled fixture per run, not a statistical throughput guarantee.

Concurrency: each 4-worker and 12-worker measurement starts with the same 2,000 ready jobs and 50,000 unrelated jobs, vacuumed equally, with 12 connections warmed. Each claims 240 jobs. The final race run observed 283.09 versus 251.61 claims/second (0.889 times); an earlier ordinary run observed 343.83 versus 454.22 (1.321 times). Every measured claim had a unique job and lease token with exactly one attempt/revision increment. These shared-host measurements competed with separately scheduled CPU-intensive analysis; linear scaling is not established.

## Failures and remaining limits

Initial development checks caught a test-variable typo and full-sort plans in several candidate query shapes. Those versions were rejected. An initial 768 MiB disposable database cap caused an OOM during a 50,000-row fixture update, followed by connection failures; the final server uses 2 GiB and fixtures are seeded directly in their intended states. The failure logs are retained with the successful final evidence.

The result establishes bounded access for the tested pending-queue distributions, not constant latency for every possible queue. Many same-kind running leases with varied priorities, dead index entries awaiting vacuum, or many inaccessible rows can increase work. The restricted-token fixture correctly skips 1,000 earlier foreign-project jobs, but still reads 3,500 pages in 4.389 ms; it is evidence of isolation, not a constant-cost RLS guarantee. The all-unexpired running fixture reads eight pages in 0.064 ms, which does not prove the same bound for arbitrary lease/run-time distributions. Full service throughput and exclusive-host scaling remain unmeasured.

Migration 0007 creates ordinary indexes inside the existing migration transaction. Applying it can make job writes wait during index construction. Three additional indexes also add write/storage maintenance. The old migrations and old general index remain unchanged.

Private diagnostic artifacts: `/tmp/arkism-goal5-20261108/` contains exact test-selection patterns, command logs and raw before/after plans; `final-normal/` keeps the final ordinary plans separately. The independent review found no blocking correctness issue after the cancellation, connection-limit and concurrent-reschedule corrections. Cleanup confirmed zero test databases and roles, stopped the owned server successfully, and removed only its data/socket directories. The final server peak was 831.6 MiB under its 2 GiB cap; diagnostic logs and plans remain available.

제목: 작업 대기열을 종류별 정렬 인덱스로 조회
이유: 큰 대기열에서 비대상 작업까지 읽고 정렬하던 비용과 빈 개인정보 작업 조회 비용을 줄인다.
검증: 최종 race 검사 55개와 하위 검사 38개가 24.080초에 통과했다. 실제 임시 DB의 52,000건 조회는 21.213→0.436ms, 페이지 접근은 10,401→110개였다. 대상 종류만 50,000건이어도 0.396ms였고, 개인정보 진행 조회는 25.680→0.080ms였다. 동시 작업 480개의 ID·임대 토큰 중복은 없었다. 만료 임대, 미래 재시도, NULL 순서, 손상 격리, 삭제 전용 모드, 권한 분리, 복구 차단, 설정 복원과 마이그레이션을 검증했다. 과거 마이그레이션 6개와 고정 스키마 원문은 바꾸지 않았다. 마지막 일반 실행 2개도 7.411초에 통과했으며 조회는 16.140→0.313ms, 4→12-worker 처리량은 538.97→855.14건/초였다.
한계: race 측정의 12-worker 처리량은 4-worker보다 낮았으며 CPU 경쟁 환경에서 선형 증가는 입증하지 못했다. 권한 필터·미만료 임대 분포·미정리 인덱스 항목에 따른 추가 스캔 가능성이 남는다. 동시 예약 변경은 시간 차이를 검출해 재시도하며 커밋 시점 전체 FIFO를 보장한다고 주장하지 않는다. 인덱스 생성 중 쓰기가 대기할 수 있다. 초기 테스트 오류와 768 MiB DB 제한의 OOM도 기록했고 최종 DB 제한은 2 GiB였다. 제품 빌드·배포·운영 DB 접근·push는 하지 않았다.

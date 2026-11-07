# Bounded lifecycle work / 대량 기억 관리 처리량 제한

Task: memory-quality item 5, large deletion and stale derived-record maintenance.
Research: none
Why: avoid loading unrelated derived records during replacement and keep one durable purge advance within a shared item budget.
Verified: focused PostgreSQL and service checks are recorded below.
Limits: partial item 5. Discovery/quarantine and stale invalidation remain atomic; this change does not implement durable discovery or multi-transaction stale invalidation.

제목: 대량 기억 관리 처리량 제한
이유: 관련 없는 파생 기록을 읽지 않고 삭제 진행 단계의 처리량 상한을 지킨다.
검증: 아래 실제 DB·서비스 검사 결과를 기록한다.
한계: 삭제 영향 탐색과 오래된 기록 무효화는 여전히 단일 트랜잭션이다. 중단 재개 가능한 탐색을 완료했다고 표시하지 않는다.
연구: 해당 없음

## Changes

- PostgreSQL stale-cascade paging now selects only rows whose digest members,
  skill proposal relations or observation source memories name the replaced
  memory. The existing decoded-document checks, audit writes and transaction
  atomicity remain. Page traversal is ordered by immutable ID while status changes.
  Other repositories retain the complete fail-closed fallback.
- Source deletion and work-record erasure now share a total 100-item budget in
  each durable purge advance. Previously the source/record boundary could process
  the last source chunk plus another 100 records in one transaction. Both offsets
  still commit with the changes and resume on a fresh service; a page is never
  recorded as completed before its transaction commits.

## Verification and limits

The selective scan regression populates 205 records in each of four collections
plus foreign-scope distractors. It visits exactly 103 dependent records across a
page boundary while changing their status; all 102 unrelated records survive.
The existing actual 1,008-source deletion test now checks that every resumed
advance respects the combined 100-item limit, including the source/record
boundary. It also preserves foreign-scope records and refuses completion while
child erasures remain.

The initial test command used a `/postgres` database name and was rejected before
DB access by the disposable-database guard. The first actual DB scan fixture
mistakenly reused a globally unique ID for a foreign-scope distractor; the fixture
was corrected to use a distinct ID. Neither is recorded as a successful check.

Existing PrivacyFence only checks already discovered impact IDs. The recovery
write gate blocks writes, not reads. Neither proves unvisited dependent records
are unreadable during asynchronous discovery. A future durable frontier needs
complete read/write admission fences, checkpointed provenance work, hold handling,
concurrent insertion/revision fencing, and atomic final quarantine activation.
Breaking the current transaction into pages without those contracts would expose
stale/private data and is intentionally not implemented. Selective SQL pages also
do not guarantee indexed lookup or bound total transaction duration for many real
dependents. Production latency and live workloads were not measured.

Focused verification passed: scoped memory tests matching StaleDependentPages,
PrivacyLargeSubject, Supersede and StaleCascade completed successfully in 11.380s
against isolated PostgreSQL, with a 6 GiB systemd scope and 900-second deadline.
검증: 실제 DB의 선택 순회·대량 삭제 재개·기억 교체 관련 검사가 11.380초에 통과했다.

The adjacent scoped privacy, digest, observation, supersede/stale and source-erasure
regression selection also passed; see the integration validation record for the
full-package result. No product build, model call, publication or operational data
access was performed.

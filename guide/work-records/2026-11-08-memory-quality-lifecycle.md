# Bounded lifecycle work / 대량 기억 관리 처리량 제한

Task: memory-quality item 5, large deletion and stale derived-record maintenance.
Research: none
Why: avoid loading unrelated derived records during replacement and keep one durable purge advance within a shared item budget.
Verified: focused PostgreSQL and service checks are recorded below.
Limits: default operations remain atomic; explicit owner maintenance supports resumable source_event/trace/subject discovery and stale invalidation. Targeted work_checkpoint deletion retains the atomic API. Mixed-version/raw SQL clients must be stopped; total manifest size remains unbounded.

제목: 대량 기억 관리 처리량 제한
이유: 관련 없는 파생 기록을 읽지 않고 삭제 진행 단계의 처리량 상한을 지킨다.
검증: 아래 실제 DB·서비스 검사 결과를 기록한다.
한계: 기본 동작은 원자성을 유지하고 명시적 점검 모드에서만 재개한다. 구버전·직접 SQL 클라이언트 중지가 전제이며 전체 영향 목록 크기 비용은 남는다.
연구: 해당 없음

## Initial bounded improvements

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

## Opt-in durable global maintenance / 명시적 전체 서비스 점검 모드

The follow-up implementation adds a durable owner-only maintenance gate in
migration 0009. Normal PostgreSQL repository transactions hold a shared gate row
lock until completion. Maintenance takes the exclusive lock, drains earlier
admitted transactions and then persists its operation. Every subsequent normal
repository read/write through the upgraded client fails closed, including unbound/superuser application views.
Direct recovery, migration and baseline entrypoints also check the gate; explicitly
read-only snapshots hold a matching advisory lease on their existing connection.
This is **temporary unavailability of the entire service**, not a per-user pause.
No deployment is enabled by this change; the default gate is open.

The administrative entrypoints require an unbound privileged database connection.
Begin additionally uses a real PostgreSQL human principal and the ordinary privacy
or supersede authorization/content checks in the same transaction. Ordinary API
and agent tokens cannot unlock maintenance. Status does not increment the revision;
advance, hold/release and safe cancellation use expected-revision comparisons.

- Privacy supports `subject`, `source_event` and `trace`. The existing default
  API remains atomic. Explicit maintenance walks immutable keyset pages of at most
  100 selected source/trace/relation/consultation rows, repeats the exact provenance
  closure until stable, and discovers record/work/archive dependencies using the
  existing typed predicates. Quarantine pages and their cursor commit together.
  The ordinary privacy job is published and access reopened only after every
  discovered item has been quarantined. Runtime purge, retention, backups and
  child-erasure verification still use the existing saga; they are not declared
  complete by maintenance. Targeted `work_checkpoint` deletion remains supported
  by the original atomic API, not this maintenance entrypoint.
- Stale maintenance performs the authorized memory replacement under the closed
  gate, captures the exact old/new revision mark, and invalidates digests, skill
  proposals, observations and support edges in committed pages. Unvisited stale
  bodies cannot be served while the gate is closed. Reopening happens only after
  the last phase; approved user summaries request regeneration as before.
- A fresh process reads the persisted operation and continues. Holding maintenance
  stops progress without opening access. Existing scoped legal holds cause begin
  to fail before the gate closes. Stale/missing target revisions are also rejected
  before admission. Privacy discovery may be cancelled before quarantine begins;
  no canonical row has changed at that point. Cancellation does not remove legal
  holds. Stale operations and any operation that started quarantine cannot be
  abandoned through this cancellation API.

Read linearization is at the guarded database transaction. A response already
materialized before maintenance was admitted, or a previously authorized external
model/runtime call, is not retroactively recalled. The gate is not a firewall
against a database administrator issuing raw SQL outside Arkism. Direct raw owner
connections are the administrative trust boundary; application repository access
still checks the gate even when its SQL role is privileged.

The accumulated impact manifest is persisted as JSON in the maintenance record.
Its memory footprint and rewrite cost therefore grow with total impact. Keyset
paging bounds selected items per step, not every internal SQL operation or the
fan-out of dependency invalidation. Work/archive joins and a large fixed point can
still require substantial total work. No constant-time or production-throughput
claim is made.

추가 구현: 전체 서비스 접근을 잠시 차단하는 명시적 점검 모드에서 원본·실행 추적·사용자
삭제 영향 탐색과 오래된 파생 기억 정리를 중단 후 재개할 수 있다. 진행 위치와 변경은 함께
커밋하며, 새 프로세스에서도 이어간다. 실제 삭제 완료·백업 만료·모델 품질을 의미하지 않는다.
대상 work_checkpoint 삭제는 기존 원자적 API를 유지하고, 전체 영향 목록의 JSON 크기 비용은 남는다.


### Deployment prerequisite / 실행 전제

The gate is enforced by the upgraded Arkism repository, not by arbitrary database
clients. Before opt-in maintenance, all application clients must be upgraded to
this guard-aware version or stopped; old already-running binaries and raw SQL
administrator clients must not read/write during the operation. A schema version
alone cannot establish that prerequisite, and session application names are not
trustworthy proof of client behavior. The explicit CLI acknowledgement includes
this operational prerequisite. The implementation does not auto-stop old clients,
change DB roles or claim to fence every process with database credentials.

Operator entrypoint: `cmd/memory-maintenance`; see
[CLI contract](2026-11-08-memory-maintenance-cli.md). Advance never initializes
schema or bypasses the gate through normal HTTP credentials. All target/status
outputs remain owner-only and omit record bodies.

Verification: full selected maintenance/RLS/recovery/baseline/schema checks passed
in 49.014s. Final review corrected lock copying by constructing the minimal service
instead of copying mutex-bearing Service; cancellation now explicitly lists only
known read-only phases; cancelled advisory-lock acquisition closes the connection
rather than unlocking an uncertain acquisition. The reviewed maintenance tests
passed in 9.074s and scoped `go vet ./internal/memory` passed.

The earlier migration fixture failures used current guarded repository calls on
pre-migration schemas; historical seeding/readback now uses explicit fixture-only
owner transactions. The production missing-guard behavior stays fail closed.
An earlier subprocess regression attempted compilation during new source-file
creation and failed; only the subsequent stable snapshot runs count as validation.
The subprocess-only resume helper is skipped in an ordinary package run and is
executed by its parent regression; its skip is not counted as an independent pass.

A separate OS process also resumed the real PostgreSQL operation without Init and
completed publication: PASS, 1.617s. The first subprocess fixture connected to the
original admin database because pgx Config.ConnString retains its original DSN
when Database is later overridden; the test now constructs the child DSN from the
actual isolated connection fields. It also rejects non-loopback/non-test targets.
검증 보완: 별도 프로세스가 저장된 진행 위치를 읽고 삭제 격리 작업을 끝내는 실제 DB 검사가
1.617초에 통과했다. 앞선 잘못된 테스트 DB 연결 실패는 통과로 집계하지 않았다.

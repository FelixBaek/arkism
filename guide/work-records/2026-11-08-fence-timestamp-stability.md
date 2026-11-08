# Preserve indexed state during read fences / 읽기 fence의 시간값 보존

Task: MQ-R2-FENCE.
Why: a logical read must not change the indexed timestamps that order later
searches or determine scheduling boundaries.
Research: none; correctness repair found during the evidence-selector's database acceptance checks.

## Cause and change / 원인과 수정

The storage codec encodes record dates in milliseconds. `Postgres.Insert` and
`Replace` pass the original time to SQL timestamp columns, which can retain
microseconds. Normal service creation uses `time.Now().UTC()`, so the discrepancy
is possible in ordinary usage, not just constructed fixtures.

A fence reads and decodes the payload, increments its concurrency counters and
performs a real write. Previously that write also copied the decoded timestamps
back into `updated_at`, `run_at` and `lease_until`. With otherwise tied full-text
matches, fencing the first result could reduce its indexed `updated_at` by a
fraction of a millisecond and move it behind the next result. The logical revision
and returned content had not changed.

The fence-only write now changes only the two counter keys in the original
payload map and writes that document to `payload` and `doc`. It preserves unknown
payload fields, indexed metadata and their existing precision. The row lock and
actual write remain, with an exact revision condition, so the concurrency fence
still serializes against mutations. Job claim/finish use the existing state-write
branch, including their revision increment, lease, attempt and scheduling updates.
No schema, migration, timestamp normalization or stored-data rewrite is introduced.

## Verification / 검증

Before the fix, the new actual-PostgreSQL regressions failed as intended in all
four source/search/relation/run fence cases: indexed timestamps ending in
`.123456`, `.123567` and `.123678` became `.123`. The full-text test also failed:
`[precision-a, precision-b]` became `[precision-b, precision-a]` after a read fence.
The pre-fix run took 0.324 seconds and skipped no tests. It used fresh disposable
child databases, not production data.

The final scoped race run passed 7 top-level tests and 13 subtests, with zero
failures/skips, in 3.443 seconds. The new regression
checks all three SQL timestamps plus status/revision/lease metadata, unknown
payload fields and payload/doc parity, both counters, repeated reads and stale
revision rejection. Existing core, job claim/finish, committed-lease competition,
restricted-token/recovery and archive-usage fence tests exercise the unchanged
paths. Scoped `go vet ./internal/memory` and the diff whitespace check also passed.

Commands use the already installed matching Go toolchain with
`GOTOOLCHAIN=local`, `GOPROXY=off`, `GOMAXPROCS=2` and an explicitly disposable
loopback `POSTGRES_TEST_DSN`. Each database test creates and removes its own
child database. Test commands run under
`systemd-run --user --scope -q -p MemoryMax=6G timeout 900`.

```sh
go test ./internal/memory -run '^TestPostgresReadFence(PreservesStorageTimestamps|KeepsFTSOrder)$' -count=1 -timeout=850s -json
go test -race ./internal/memory -run '^(TestPostgresReadFence.*|TestPostgresCoreIntegration|TestPostgresJobClaimPriorityAndEligibility|TestPostgresJobClaimRechecksCommittedLease|TestPostgresJobClaimRestrictedTokenAndRecoveryGate|TestPostgresSessionArchiveUsageFence)$' -count=1 -timeout=850s -json
go vet ./internal/memory
git diff --check
```

The final specification/code read and independent standards review found no
blocking issue. The reviewed `postgres.go` SHA-256 is
`037e518c41fbba529e0ee3e8355c3a8d076d1c4989c73a6621228ba88b779662`.
That review is a code inspection, separate from the database test results.

## Limits / 범위와 한계

This repairs read-induced ordering changes; it does not normalize historical SQL
and payload timestamps or change legitimate job/content writes. No retrieval
quality percentage, model improvement or production performance gain is inferred
from this test. Product builds, model calls, deployment and publication are outside
this change. Separate HTTP acceptance evidence is recorded with the selector work.

한국어: 읽기 동시성 검사가 저장된 시간값의 소수점 정밀도를 바꾸어 검색 순서를
뒤집던 문제다. 실제 행잠금과 카운터 쓰기는 유지하면서, 읽기 fence가 SQL 시간·
상태를 덮어쓰지 않도록 수정했다. 기존 데이터나 job 갱신 정책은 변경하지 않는다.

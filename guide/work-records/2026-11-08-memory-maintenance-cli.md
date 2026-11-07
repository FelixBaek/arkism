# Explicit memory maintenance command / 명시적 기억 유지보수 명령

Task: memory quality follow-up 5, operator interface for durable maintenance.
Research: none
Why: a durable global maintenance gate needs a bounded, restartable operator path
that is usable while ordinary application requests are fenced.
Verified: scoped CLI package tests passed (0.430s), compiled against the maintenance engine API snapshot; no database maintenance was executed.
Limits: no production execution, deployment or release build. The command relies
on the database maintenance engine for owner permission, human authorization,
transactional cursors, source fences, legal holds and completion.

제목: 중단 후 재개 가능한 유지보수 운영 명령
이유: 정상 서비스가 차단된 유지보수 상태에서도 운영자가 진행 상황을 확인하고 재개할 수 있다.
검증: 아래 검사 기록을 따른다.
한계: 실제 운영 유지보수를 실행하지 않았다. DB 엔진의 권한·보류·완료 검사를 우회하지 않는다.
연구: 해당 없음

`cmd/memory-maintenance` uses the existing `MEMORY_DATABASE_URL` administrative
connection. It never initializes or migrates schemas. The begin commands also
require an authorized human token through `MEMORY_MAINTENANCE_TOKEN` or
`--token-file` (regular non-symlink file with no group/other access). Two token
sources are rejected rather than silently choosing one. Tokens and connection
strings are never accepted as command-line flags or printed in failures.

Commands:

- `status`: read the current revision, active/held state and progress without
  incrementing the revision.
- `begin-privacy --request FILE --allow-global-downtime`: submit a local
  `PrivacyDeletionRequest` JSON file. The durable engine supports
  `target_type: subject`, `source_event` and `trace`; `work_checkpoint` retains
  the existing API. Subject IDs must match the requested user scope, as checked
  by the service.
- `begin-supersede --memory-id ID --request FILE --allow-global-downtime`:
  submit the existing `SupersedeInput` JSON, including its expected memory revision.
- `advance --expected-revision N`: commit one bounded maintenance step using the
  last observed maintenance revision. A retry must inspect status after an
  uncertain response; it does not assume that a timed-out step failed to commit.
- `hold --expected-revision N` and `release --expected-revision N`: pause or
  resume the maintenance job. Release does not reopen normal service or release
  any legal hold. Successful engine completion reopens the global gate after mutation.
- `cancel --expected-revision N`: cancel privacy discovery only before any
  quarantine or mutation. The engine rejects cancellation after that boundary
  and rejects stale-cascade cancellation. This is not a general gate-unlock.

The explicit begin flag acknowledges that ALL normal service access is fenced,
not merely requests in the affected scope. There is no force-unlock, unrestricted abort,
implicit continue loop or implicit schema update. Every invocation is bounded by
`--timeout` (30 seconds by default; 1 second through 15 minutes accepted).
Request JSON is local, size-bounded, and rejects unknown fields or trailing JSON.
Error output uses fixed codes rather than raw database, file or parser messages.

검증 범위: 명시적 서비스 중단 동의 누락, 잘못된 revision, 중복 플래그, 잘못된 JSON,
공개 권한 토큰 파일·심볼릭 링크·토큰 입력 충돌은 거부한다. 연결 전 검증과 출력 비밀값
차단은 가짜 backend로 확인한다. DB 유지보수 동작 자체는 별도 엔진의 실제 DB 시험으로
검증하며 CLI 가짜 backend 검사를 전체 DB 실행으로 표시하지 않는다.


Final verification: `go test ./cmd/memory-maintenance` with a Go overlay of the
lifecycle agent's stable source snapshot passed in 0.430s. This compiled the real
PostgreSQL adapter and tested parsing, token/request-file checks, dispatch, timeout,
expected-revision forwarding and sanitized output using a fake backend. It did
not connect to an operational database or run maintenance. The snapshot is a
compile-time integration check; the root integration runs the combined committed
source and the engine's separate real-database tests. Parser-only tests also
passed; a first temporary-package attempt was refused by Go's internal-import
boundary and was replaced by an in-module test fixture that was then removed.

검증 결과: lifecycle API 소스 snapshot을 Go overlay로 연결해 실제 CLI adapter까지
컴파일한 패키지 검사가 0.430초에 통과했다. 명령 실행 backend는 시험용이므로 실제 DB
유지보수 실행으로 계산하지 않는다. 제품 배포용 빌드·운영 접근은 하지 않았다.

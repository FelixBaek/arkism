# accept SYS-05 with the comparison recorded as not possible

Task: SYS-05 (#152)
Research: Records the card result; the same-condition sync/async comparison is not possible under the locked A-v2 rules, so no superiority conclusion is made.
Design: The owner decided on 2026-10-31 to accept "comparison not possible" as the result instead of locking a new comparison contract. Acceptance used the card's own command at the head plus a re-run of the SYS-05 tests outside its pattern, then an independent review against the five B criteria.
Relation: adapted
Deviation: The locked comparison test is not re-run at the head: its lock pins the fixture's 10 s lease, which the later lease bound (lease shorter than the 5 s queue wait) replaced with 4 s, so the test refuses the config by design. Its passing run stays valid only at the lock commit; re-locking needs an owner decision.
Why: The remaining SYS-05 gates could not move without changing the locked contract; recording the result closes the card without a superiority claim.
Verified: At 5cdc4c4e on an own disposable PostgreSQL: `go test -race ./internal/memory -run '^(TestDynamicsWorker|TestDynamicsLoad|TestWorkOnceAdvancesPrivacy)' -count=1` passed 48 with no failure or skip. `go test -race ./internal/memory -run '^(TestDynamicsMeasurement|TestDynamicsSnapshotRefresh|TestDynamicsHTTPAtomicAdmission|TestDynamicsOutcome)'` passed 139 and failed only TestDynamicsMeasurementLockedSyncAsyncPG on the lock config check. `go test -race ./cmd/memoryd -run '^TestDaemonDynamics'` passed 43. The PostgreSQL was cleaned with no leftover databases, roles or backends. An independent review returned accept with notes.
Limits: Synthetic N=1-2 scenarios; no percentile assertion; RSS is the whole test process. service_execution_authenticated stays false. The feature is off by default. No product build, deploy or merge was done, and remote CI for this head was still running when recorded.

## 한국어 작업 기록

제목: 비교 불가를 결과로 적고 SYS-05 수용
이유: 남은 SYS-05 관문은 잠긴 계약을 바꾸지 않고는 움직일 수 없다. 2026-10-31 소유자 결정에 따라 '같은 조건 비교 불가'를 결과로 적고 우월성 주장 없이 카드를 닫는다.
검증: 5cdc4c4e에서 전용 임시 PostgreSQL로 카드 수용 명령을 race로 돌려 48 통과·0 실패·0 skip이었다. 카드 패턴 밖 시험은 internal/memory 139 통과, cmd/memoryd 43 통과였다. 잠긴 비교 시험 1개만 실패했는데, 잠금 뒤 lease 한도 수정(10초→4초)으로 설정이 바뀌어 잠금이 이를 거부한 것이며 설계대로다. PG 정리 결과 남은 DB·역할·연결은 0이었다. 독립 검토 결과는 조건부 수용이었다.
한계: 합성 시나리오 N=1~2이고 분위수 단언은 없다. RSS는 시험 프로세스 전체 값이다. service_execution_authenticated는 false 그대로다. 잠긴 비교 결과는 잠금 커밋에서만 유효하며, 다시 잠그려면 소유자 결정이 필요하다. 기능 기본값은 꺼짐이다. 제품 빌드·배포·병합은 하지 않았다. 기록 시점에 이 커밋의 원격 CI는 아직 실행 중이었다.

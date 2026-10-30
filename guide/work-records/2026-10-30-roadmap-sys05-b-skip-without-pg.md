# skip two dynamics parent tests when their subtests produced no report

Task: SYS-05.B (#152)
Research: Engineering test fix; no measurement.
Design: The worker crash under a full queue and the starvation rounds tests collect a report from two subtests and then read both reports. Without PostgreSQL the subtests skip, the reports are nil, and the parent panicked on the type assertion, aborting the whole package. The parent now skips when a report is missing; a subtest that failed has already failed the parent, so no failure is hidden.
Relation: adapted
Deviation: None.
Why: A package run without PostgreSQL must skip the PostgreSQL tests, not panic and stop every later test.
Verified: Without PostgreSQL, go test -run 'WorkOnce|Worker|Privacy|Embed|Dynamics|Measurement|Native' on internal/memory: 361 passed, 177 skipped, none failed; before the fix the same run panicked in the crash test.
Limits: Full check_local and unrestricted go test ./... were not run. The PostgreSQL runs of the two tests are covered by their earlier records and the next dynamics run.

## 한국어 작업 기록

제목: 하위 시험 보고가 없으면 동적 부모 시험 두 개를 건너뜀
이유: PG 없이 패키지를 돌리면 PG 시험은 건너뛰어야 한다. 그런데 가득 찬 대기열 crash 시험과 starvation 라운드 시험의 부모가 nil 보고를 단언하다 panic해 이후 시험이 모두 멈췄다. 이제 보고가 없으면 건너뛴다. 하위 시험이 실패했다면 부모도 이미 실패하므로 실패를 숨기지 않는다.
검증: PG 없이 internal/memory의 관련 시험(-run 'WorkOnce|Worker|Privacy|Embed|Dynamics|Measurement|Native')을 돌려 361 통과, 177 건너뜀, 실패 0. 수정 전에는 같은 실행이 crash 시험에서 panic했다.
한계: 전체 check_local과 무제한 go test는 실행하지 않았다. 두 시험의 PG 실행은 이전 기록과 다음 동적 실행이 다룬다.

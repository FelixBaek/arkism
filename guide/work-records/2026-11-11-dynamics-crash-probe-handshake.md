# Start the replacement worker before measuring a live lease

Task: SYS-05.B (#152), PR #551 CI follow-up
Research: none
Why: The crash test checked that a lease was live when the first worker died, then started another process and assumed the lease was still live when that process drained. Process startup and race-detector exit delay could consume the two-second lease. A legal claim after expiry was therefore reported as stealing a live lease.
Changed: A replacement worker now opens its PostgreSQL connection and reports ready before the test enqueues its entry. After SIGKILL, the parent signals that worker through stdin and reads the operation's start and finish timestamps before waiting for process exit. Both timestamps must fall between the kill and the original lease deadline before unchanged queue/token/event state can establish the live-lease assertion. Missing, reversed or expired timing evidence fails the test. The existing lease lengths, queue-wait limits, expired-lease recovery, full-queue behavior and publication checks remain unchanged; no production code or CI retry rules changed.
Verified: The two affected PostgreSQL crash tests passed with `-race`: two top-level tests and five subtests, no skips, 26.812 seconds of test-process time. All four early probes returned timestamp evidence before lease expiry; the smallest observed remaining interval was about 1.87 seconds. The child databases cleaned up without errors. The full CI log was also reconciled: code-graph, long archive metadata and work-writer tests passed the existing isolated rerun; only the dynamics crash group remained failed in that historical job.
Limits: These targeted tests used a disposable local PostgreSQL instance and synthetic data. No model, release build, full suite or new CI run was executed for this record. The original CI log did not retain the replacement worker's claim timestamp, so it cannot by itself prove a product lease violation. Severe scheduling delay can still miss the original deadline; the test then reports missing live-lease timing evidence instead of accepting it or relaxing the bound.

## 한국어 기록

작업자가 죽은 순간 lease가 유효해도, 새 프로세스를 시작한 뒤에는 이미 만료될 수 있었다. 이제 대체 작업자를 대기열 입력 전에 준비시키고, SIGKILL 뒤 신호를 보내 실제 drain 구간을 측정한다. 시작·완료 시각이 기존 lease 안에 있었다는 증거가 있어야 상태 불변 검사를 통과한다. 시간 증거가 없거나 만료됐다면 실패로 남긴다.

실제 임시 PostgreSQL에서 관련 crash 검사 2개·하위 사례 5개가 race로 통과했다. lease·대기시간·복구조건은 늘리지 않았고 제품 코드는 변경하지 않았다. 과거 CI의 다른 세 실패 표시는 기존 단독 재실행에서 통과한 로그이며, 최종 실패는 dynamics 한 그룹이었다. 전체 CI가 통과했다는 뜻은 아니다.

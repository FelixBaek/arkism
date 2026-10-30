# lock and run a small same-plan sync/async comparison

Task: SYS-05.B (#152)
Research: Locked small functional comparison with synthetic fixtures in an isolated database; descriptive only, no performance, quality or scientific claim.
Design: A lock file written before the run pins the pilot report hash, the eight-request plan, two balanced mode orders, the 64-attempt budget and the exact runtime configuration hash per mode, plus the selection rule and the analysis rule. The new locked test refuses a lock that differs from the coded plan, budget or configuration, builds a fresh database per mode run, keeps every request in the denominator and writes the raw report. A separate script recomputes the per-run figures from the raw report and checks the lock hash.
Relation: adapted
Deviation: The A-v2 projection and repair-window witness from the design are not produced, so comparison_ready stays false. The locked plan reuses the pilot plan unchanged, including one trace with distinct EventIDs (one learning plus duplicates).
Why: The pilot showed both modes collect every ledger and cost field; the design requires a fresh locked run, not reuse of pilot data, before any comparison is described.
Verified: The lock, test source and pilot report hashes matched before the run. TestDynamicsMeasurementLockedSyncAsyncPG ran exactly once against an own disposable PostgreSQL and passed. In both orders sync applied all five updates in-request with no queue-full refusal, and async admitted four, refused the fifth as dynamics_queue_full and drained; each run kept 8/8 requests, one 409 changed retry and exactly one consumed learning. Recomputed ranges: update response sync 71.8-113.8 ms, async 8.4-61.4 ms; received to applied witness sync 86.2-127.4 ms, async 272.7-394.6 ms; received to visible witness sync 164.0-648.9 ms, async 345.5-549.9 ms. Stored ledger payload sync 37864-37865 bytes, async 32487-32490 bytes; state 3599-3601 bytes in all runs. golangci-lint v2.14.0 reported 0 issues. The disposable database was stopped with no leftover test databases or roles.
Limits: Descriptive only: N=2 per mode, one process, no mean, percentile or significance. Async applied delay mostly reflects that the test runs the worker only after all requests are sent, not a real worker cadence. No restart, burst, sustained overload, cold/warm, delete race or repair window; receipt_bytes and replay_bytes stay null; RSS is a shared test process. Full check_local and unrestricted go test ./... were not run. SYS-05 stays in_progress; 25/63 done, 24 required remaining.

## 한국어 작업 기록

제목: 작은 동기·비동기 같은 계획 비교를 잠그고 실행
이유: 파일럿에서 두 방식 모두 원장과 비용을 모을 수 있음을 확인했다. 설계는 비교를 말하기 전에 파일럿 자료를 다시 쓰지 말고 잠근 새 실행을 요구한다.
검증: 실행 전 잠금·시험 코드·파일럿 결과 해시가 일치했다. 잠금 비교 시험을 자체 임시 PG에서 정확히 한 번 실행해 통과했다. 두 순서 모두 동기는 5건을 요청 안에서 반영했고, 비동기는 4건을 받고 5번째를 대기열 가득으로 거절한 뒤 비웠다. 각 실행에서 요청 8/8을 모두 남겼고 바꾼 재전송 409와 학습 소비 1건이 같았다. 다시 계산한 범위: 응답 동기 71.8~113.8ms·비동기 8.4~61.4ms, 반영 확인까지 동기 86.2~127.4ms·비동기 272.7~394.6ms. lint 0건. 임시 PG는 남은 시험 DB 없이 정지했다.
한계: 기술 통계만: 방식마다 2회, 한 프로세스, 평균·백분위·유의성 없음. 비동기 반영 지연은 시험이 요청을 다 보낸 뒤 작업자를 돌린 영향이 크다. 재시작·폭주·지속 과부하·cold/warm·삭제 경쟁·repair 구간 없음, A-v2 투영 없음, receipt·replay 비용 null. 전체 check_local·무제한 go test 미실행. 전체 25개 완료·필수 24개 남음 유지.

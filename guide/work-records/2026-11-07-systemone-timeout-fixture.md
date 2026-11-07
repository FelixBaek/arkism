# Synchronize the in-flight deadline fixture / 호출 중 deadline 시험 동기화

Task: memory improvement integration regression, TestSystemOneFailuresRecordOnce/timeout
Research: none
Why: The old 30 ms caller deadline began before input checks, authorization, usage reservation and connection setup. Under load it could correctly expire before any HTTP handler ran, contradicting the fixture's requirement of exactly one dispatched request. Its 80 ms server sleep did not synchronize dispatch and expiry.
Verified: With only a temporary 50 ms authorization delay added to the old fixture, the failure reproduced as attempts=0 (0.176s). The delay was removed. After the fix the complete failure table passed a fixed 30 repetitions in 1.632s. git diff --check passed. Race-check outcome is recorded below.
Limits: Test fixture change only. No product timeout, request retry, ledger implementation or expected count changed. This is a deterministic in-flight deadline/failure-recording test, not a wall-clock latency test. The separate offline transport timeout fixture remains unchanged.
제목: 실제 HTTP 도착 이후 deadline 만료를 발생시키도록 시험 동기화
이유: 30ms가 승인·원장예약·연결 전 과정에 적용되어 부하 시 정상적으로 HTTP 호출 전에 만료될 수 있었다. 서버 도착을 전제로 한 호출 1회 검사가 그 상황과 충돌했다.
검증: 기존 시험에 승인 단계 50ms 지연만 임시 주입하여 attempts=0을 재현했고 주입은 제거했다. 수정 후 실패 유형 전체를 고정 30회 실행하여 1.632초에 통과했다. 공백 검사도 통과했다.
한계: 제품 코드는 바꾸지 않았다. 시간 측정 성능시험이 아니라 호출 도착 후 deadline 실패와 원장 기록을 확인한다. 기존 단일 호출·단일 비용/라우팅 실패 기록 검사를 유지했다.

The test-only context exposes DeadlineExceeded when its barrier closes. The real local HTTP handler consumes the request body, closes that barrier and waits for request cancellation. This preserves a real transport abort and the caller context's deadline error without making scheduler speed part of success. A five-second watchdog only prevents a stuck fixture from waiting forever; firing it is an explicit test failure. Cleanup stops the watchdog and expires the context before closing the server. The failure table still requires one HTTP call, one usage row, one routing row where applicable, finalized failed usage, and a recorded routing error.

No product build, live model call, operating database, remote service or publication was used. The isolated local HTTP test server is the only network boundary exercised.

At this commit, the focused race run is still running; no race pass is claimed. The parent integration run is also pending.

Follow-up verification: `go test -race ./internal/memory -run '^TestSystemOneFailuresRecordOnce$/timeout$' -count=30`, under the same 6 GiB scope and 900-second command limit, passed in 1.364s with no race report. The preceding pending statement describes the implementation commit's state. The bilingual work-record checker also passed for that commit; the parent whole-package integration result remains separate.

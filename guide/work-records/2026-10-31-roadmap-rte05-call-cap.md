# RTE-05 one call cap for every routed step

Task: RTE-05 (#155)
Research: none
Design: runRouted keeps at most 3 fallbacks after the routed pick, the same number the server's routingFallbacks sends, so a routed step makes at most 4 attempts whatever a decision lists. The cap sits before the first attempt, so the effect_unknown and budget_spent stops, the tried list and the call reports are unchanged for any decision the server sends today.
Relation: extends
Why: The RTE-05 card asks for one shared tool, call and time limit for every policy. Time was shared already; the attempt count was bounded only by the candidate list, which crew code did not cap.
Verified: `TestFallbackChainCapsTheCallCount` checks 0, 1, 3, 4 and 6 listed fallbacks give 1, 2, 4, 4 and 4 attempts with the same tried list and reports. Three mutations of the cap (removed, one more, one fewer) each failed it. `go test -race` for internal/host/crew, internal/host, internal/host/gateway and internal/crewrun passed, as did go vet, gofmt on the changed files and the complexity gate. An independent model review accepted; its notes on boundary cases and the stale tryRoutes comment were applied.
Limits: Extra fallbacks are dropped without a signal. The client constant is not tied to the server constant by a test. There is still no shared tool count (the node result does not expose one) and no concurrency reservation or settlement. The full local check was not run. No build, deploy or merge.

## 한국어 작업 기록

제목: 라우팅 단계의 호출 횟수에 공통 상한
이유: RTE-05 카드는 모든 정책에 공통인 도구·호출·시간 한도를 요구한다. 시간은 이미 나눠 썼지만 시도 횟수는 crew가 상한을 두지 않는 후보 목록으로만 묶였다.
검증: 대체 목록 0·1·3·4·6개가 시도 1·2·4·4·4번이 되고 tried와 보고가 같음을 새 시험이 확인했다. 상한 변이 3건(제거·하나 더·하나 덜)을 모두 잡았다. crew·host·gateway·crewrun race 시험, go vet, 바뀐 파일 gofmt, 복잡도 검사가 통과했다. 독립 모델 검토가 수용했고 경계 사례·tryRoutes 주석 지적을 반영했다.
한계: 넘친 대체 모델은 표시 없이 버린다. 클라이언트 상수와 서버 상수는 시험으로 묶지 않았다. 공통 도구 횟수 한도와 동시성 예약·정산은 여전히 없다. 전체 로컬 검사는 돌리지 않았다. 빌드·배포·병합 없음.

# measure privacy, embedding and native work across worker rounds

Task: SYS-05.B (#152)
Research: Engineering fairness measurement on an own disposable PostgreSQL in one small synthetic scenario; not a load comparison.
Design: The setup of the existing one-round fairness test moves into a shared helper. A new test adds three more queued native updates (four in all) and four more stale embedding jobs (six in all) next to the existing privacy deletion job, then runs the real WorkOnce until nothing is eligible, at most ten rounds. Before and after each round it reads what is eligible: the native queue count, open due embedding jobs, and for privacy the product's own NextPrivacyAdvance selection. A kind with eligible work that did not progress in a round is counted as stalled. A control run keeps the same backlog but turns the native round off. Coverage is reported per kind in the A-v2 background shape (eligible, stalled rounds, starvation observed, last progress time).
Relation: adapted
Deviation: None.
Why: The card requires privacy and embedding starvation checks next to the native queue. The earlier test covered one round only.
Verified: Own disposable PostgreSQL, plain and -race, with the other functional dynamics tests (9 passed each). With the native round: the four native updates applied in two rounds (head 4, none withheld), the six embedding jobs finished in six rounds, privacy advanced in both rounds where it was selected (purging_derived to verifying to partial), and no kind stalled. Privacy then stayed partial because its runtime purge was not reported in this fixture (runtime_purged false); the control run without the native round ended in the same phase with the same report, so the native work did not change privacy's path. An independent review found the first version's privacy witness vacuous and the cause of partial unexplained; the control run, the report fields, the applied and not-withheld queue checks and an error check on every round were added. golangci-lint v2.14.0: 0 issues; the complexity tool reported nothing new.
Limits: Not accepted. One scope, a backlog of four, six and one, and privacy eligible for two rounds only. The opposite direction is weak by construction: WorkOnce runs the native round in a deferred step every round, so the test does not exercise a slow or failing embedding, a failed restore check or lock contention. Embedding uses the stale-target path with no provider. The progress check counts a smaller open count, so an embedding retry would also count. Full check_local and unrestricted go test ./... were not run.

## 한국어 작업 기록

제목: 작업자 라운드마다 privacy·embedding·native 작업이 서로 굶지 않는지 측정
이유: 카드는 native 대기열 옆에서 privacy·embedding starvation 검사를 요구한다. 이전 시험은 한 라운드만 봤다.
검증: 자체 임시 PG에서 일반·race 모두 통과했다(다른 기능 시험과 함께 각 9건).
- native 갱신 4건은 2라운드에 모두 적용됐다(보류 없음). embedding 6건은 6라운드에 끝났다. privacy는 선택된 두 라운드에서 모두 진행했다. 멈춘 종류는 없었다.
- privacy는 이 fixture에서 runtime 정리가 보고되지 않아(runtime_purged false) partial에 머물렀다. native 라운드를 끈 대조 실행도 같은 단계와 같은 보고로 끝났다. 따라서 native 작업이 privacy 경로를 바꾸지 않았다.
- 독립 검토가 첫 버전의 privacy 증인이 비어 있고 partial 원인이 설명되지 않았다고 지적했다. 대조 실행, 보고 필드, 적용·보류 단언, 라운드 오류 검사를 추가했다. lint 0건, 복잡도 추가 없음.
한계: 미수용 상태다. 스코프 1개, 작업량 4·6·1, privacy가 선택된 라운드는 2번뿐이다. 반대 방향은 구조상 약하다. WorkOnce가 native 라운드를 매번 defer로 돌리기 때문에 느리거나 실패하는 embedding, 복원 검사 실패, 잠금 경합은 시험하지 않았다. embedding은 provider 없는 stale 대상 경로다. 진행 판정은 열린 작업 수 감소라서 embedding retry도 진행으로 센다. 전체 check_local과 무제한 go test는 실행하지 않았다.

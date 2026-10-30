# keep native dynamics moving behind a hung embedding

Task: SYS-05.B (#152)
Research: Engineering functional test on an own disposable PostgreSQL; not a load comparison.
Design: WorkOnce runs the native dynamics step last, under the iteration context. An embedding whose provider hangs until that context's deadline left the native step an expired context, so a due native queue made no progress in any round with a due embedding. When the iteration ended by its deadline, the native step now runs on a context without that deadline; it still bounds itself by its substep and finish timeouts. A cancellation before the deadline (shutdown) still stops it. Two PG tests use an embedder that waits for its context: with overdue embedding jobs claimed first and a 1 s deadline, every round must reach the embedder, end in DeadlineExceeded and still advance the native head without a hold; a cancellation during the hang must leave the head unchanged.
Relation: adapted
Deviation: None.
Why: The independent acceptance review asked for reverse starvation under slow embedding; the test found that a hung embedding starved native work for the whole round.
Verified: Before the fix the slow-embedding test failed in round 1 with the head unchanged at 0 and DeadlineExceeded. After it, the head went 0 to 2 to 3 over two deadline-bound rounds, and the cancellation test kept the head unchanged. The dynamics PG set of 46 tests passed under plain and -race on an own disposable PostgreSQL; golangci-lint 0 issues; complexity tool nothing new. An independent review accepted the change and asked for a comment on the bound, an error check instead of a timing check, a longer deadline and a cancellation test; all four were added and the run repeated.
Limits: Not accepted. A cancellation that arrives after the deadline waits for the substep and finish bounds (one plus two seconds in the fixture). Lock contention as a starvation source is not covered. One worker, one scope, in-process; no separate-process run. Full check_local and unrestricted go test ./... were not run.

## 한국어 작업 기록

제목: 멈춘 임베딩 뒤에서도 동적 대기열이 진행되도록 함
이유: 독립 수용 검토가 느린 임베딩에서의 역방향 starvation 시험을 요구했다. 시험해 보니 멈춘 임베딩이 작업 시간을 다 쓰면 그 라운드에서 동적 작업이 전혀 진행되지 않았다.
검증:
- 수정 전: 느린 임베딩 시험이 1라운드에서 실패했다(head 0 그대로, 마감 초과).
- 수정 후: 마감까지 간 두 라운드에서 head가 0→2→3으로 진행했다. 취소 시험에서는 head가 바뀌지 않았다.
- 자체 임시 PG에서 동적 시험 46건이 일반·race 모두 통과했다. lint 0건, 복잡도 추가 없음.
- 독립 검토가 수용했고, 권고 4건(상한 주석, 시간 대신 오류 종류 검사, 마감 연장, 취소 시험)을 모두 반영해 다시 돌렸다.
한계: 미수용. 마감 뒤에 온 취소는 substep·finish 상한(시험 설정 1초+2초)까지 기다린다. lock 경합에 의한 starvation은 다루지 않았다. 작업자 1개·scope 1개·같은 프로세스이고 분리 프로세스 실행은 없다. 전체 check_local과 무제한 go test는 실행하지 않았다.

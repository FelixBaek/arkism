# hold a dynamics queue at its limit through sustained overload

Task: SYS-05.B (#152)
Research: Engineering functional test on an own disposable PostgreSQL; not a load comparison or a throughput measurement.
Design: Eight rounds each offer three distinct updates and then run one worker round, which drains at most two, so the queue (limit four) stays full. Each offer must be accepted durably or refused as queue full. After the rounds every refused update is retried on a drained queue and must be a new admission; then every entry must be done and map, ordinal by ordinal, to the update admitted at that position, with non-decreasing apply times.
Relation: adapted
Deviation: None.
Why: The independent acceptance review asked for sustained overload; the earlier burst offered one batch at once.
Verified: Plain and -race on an own disposable PostgreSQL, both passed. Of 24 offers, 18 were accepted and 6 refused; the queue was at its limit in 7 of 8 rounds, never above it, with no hold and the oldest wait 0.19 to 0.46 s. Evaluation and capture rows grew only by the accepted count, each refused update retried as one new queue entry, and all 24 entries ended done in admission order. An independent review found weak order checks, a count-only witness, a single retry and a fixed throughput assumption; all were fixed and the run was repeated. golangci-lint 0 issues; complexity tool nothing new.
Limits: Not accepted. One worker, one scope and one process; depth is sampled once per round, not between offers. The age limit uses the wall clock, so a much slower host could reach the 5 s wait limit. N=1. Reverse starvation under slow embedding or lock contention and a separate-process run remain. Full check_local and unrestricted go test ./... were not run.

## 한국어 작업 기록

제목: 지속 과부하에서 동적 대기열을 한도로 유지하는 시험
이유: 독립 수용 검토가 지속 과부하 시험을 요구했다. 이전 burst는 한 번에 한 묶음만 넣었다.
검증: 자체 임시 PG에서 일반·race 모두 통과했다.
- 8라운드 동안 24건을 넣어 18건 수락, 6건은 queue full로 거절됐다. 8라운드 중 7라운드에서 대기열이 한도(4)였고, 한도를 넘거나 보류된 적은 없다. 가장 오래 기다린 시간은 0.19~0.46초다.
- evaluation·capture 행은 수락 수만큼만 늘었다. 거절된 6건은 다시 넣으면 각각 새 항목 1개로 접수됐다. 24건 모두 수락 순서대로 끝났다.
- 독립 검토 지적(순서 검사 약함, 개수만 확인, 재시도 1건, 처리량 가정 고정)을 모두 고치고 다시 돌렸다. lint 0건, 복잡도 추가 없음.
한계: 미수용. 작업자 1개·scope 1개·프로세스 1개다. 깊이는 라운드마다 한 번만 봤다. 대기 나이는 벽시계 기준이라 훨씬 느린 호스트에서는 5초 한도에 닿을 수 있다. 1회 실행이다. 느린 임베딩·lock 경합에서의 역방향 starvation과 분리 프로세스 실행은 남았다. 전체 check_local과 무제한 go test는 실행하지 않았다.

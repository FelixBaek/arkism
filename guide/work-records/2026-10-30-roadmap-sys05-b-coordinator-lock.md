# keep other background work moving under a held dynamics lock

Task: SYS-05.B (#152)
Research: Engineering functional test on an own disposable PostgreSQL; not a load comparison.
Design: A second transaction holds the dynamics coordinator lock while one worker round runs with a 10 s deadline. The native claim waits on that lock, so the native step must give up on its own 1 s substep bound with ErrUnavailable, leave the head, count and hold unchanged, and finish within substep plus finish timeout plus one second. In the same round the embedding backlog and the privacy phase must both advance. After the lock is released the native backlog must drain with head equal to tail and no hold.
Relation: adapted
Deviation: None.
Why: The independent acceptance review asked for starvation under lock contention; the earlier test covered a hung embedding only.
Verified: Plain and -race on an own disposable PostgreSQL, both passed, together with the other dynamics PG tests (47 in all). The locked round took 1.01 s and returned dependency_unavailable; due embedding work went from 2 to 1 and privacy moved from purging_derived to verifying, while the native head stayed at 0 with no hold; after release the backlog drained. An independent review asked for a non-empty queue, both kinds advancing, an error check, protection against a holder that fails before locking, and a deadline separated from the native bound; all were added and the run repeated. golangci-lint 0 issues; complexity tool nothing new.
Limits: Not accepted. The test does not read the lease token or attempts directly after the timed-out claim; the drain to head equal to tail is the evidence that no entry was lost. Only the coordinator lock is held; locks on other rows and admission under the lock are not covered. One worker, one scope, in-process. Full check_local and unrestricted go test ./... were not run.

## 한국어 작업 기록

제목: 동적 잠금이 잡혀 있어도 다른 백그라운드 작업이 진행되는지 시험
이유: 독립 수용 검토가 lock 경합에서의 starvation 시험을 요구했다. 앞 시험은 멈춘 임베딩만 다뤘다.
검증:
- 자체 임시 PG에서 다른 동적 PG 시험과 함께 47건이 일반·race 모두 통과했다.
- 잠긴 라운드는 1.01초 걸렸고 dependency_unavailable을 냈다.
- 그동안 임베딩은 2→1로 줄었고 privacy는 purging_derived에서 verifying으로 넘어갔다. 동적 head는 0 그대로였고 보류는 없었다.
- 잠금을 푼 뒤 남은 작업이 모두 처리됐다.
- 독립 검토 권고(빈 대기열 방지, 두 종류 모두 진행, 오류 종류 단언, 잠금 전 실패 시 멈춤 방지, 마감과 동적 한도 분리)를 모두 반영해 다시 돌렸다. lint 0건, 복잡도 추가 없음.
한계: 미수용 상태다.
- 시간 초과된 claim 뒤 lease·attempts를 직접 읽지 않았다. 항목 유실이 없다는 근거는 head=tail까지 처리된 것뿐이다.
- 코디네이터 잠금만 잡았다. 다른 행의 잠금과 잠금 중 접수는 다루지 않았다.
- 작업자 1개·scope 1개·같은 프로세스에서만 돌렸다.
- 전체 check_local과 무제한 go test는 실행하지 않았다.

# kill a worker process while it holds a dynamics lease

Task: SYS-05.B (#152)
Research: Engineering fault test with separate worker test processes on an own disposable PostgreSQL; not a load comparison or performance claim.
Design: The existing worker subprocess helper (a re-exec of the test binary with the worker token and runtime config) gains two roles. A claiming process takes the lease on the one queued update, reports it and waits; the parent kills it with SIGKILL. A draining process runs one ordinary worker round and reports its result. The parent checks the database between steps. Two lease lengths are run: 2 s, inside the 5 s queue wait limit, and 10 s, beyond it. A third case kills the process inside its apply transaction: a test-only transaction wrapper runs the whole apply body, then reports and blocks before commit.
Relation: adapted
Deviation: None.
Why: The card requires crash conditions for the queue. Earlier restart evidence swapped the runtime inside one process and expired the lease by editing the row; this run kills a real process that holds a real lease.
Verified: Own disposable PostgreSQL, plain and -race, together with the existing snapshot refresh tests (31 passed in each). In every case the dead lease stayed in the database and a new process started before its end applied nothing. With a 2 s lease, a new process after the end took a new lease (generation 1 to 2), applied the update once (one native event; entry done after two attempts) and published generation 1, and a separate read-only refresh changed no row. With a 10 s lease the entry had waited longer than the 5 s limit, so the new process ended it as failed with reason queue_wait_exhausted after one attempt, applied nothing and withheld publication; an explicit repair released the hold and a later round applied nothing. Killed inside the apply transaction, after all its writes and before commit, the process left no event and the entry still queued; after the 2 s lease a new process applied the update once (entry done after two attempts) and published generation 1. An independent review found no false claim; its findings were applied: entry status, reason and attempts are now asserted, the publication is asserted, the claiming process's stderr is kept, the 1 s lease was widened to 2 s, and "fenced" was corrected to a new lease after expiry. golangci-lint v2.14.0: 0 issues; the complexity tool reported nothing new.
Limits: Not accepted. Configuration validation accepts a lease longer than the queue wait limit; with such a setting every worker crash fails the update closed instead of retrying it. After that, the worker round reports "dynamics runtime binding required", the same error as a withheld queue, and skips its publish step. A dead worker's late write is not tested here (the stale-claim fence is covered by the in-process restart test), and one native event row is the only apply witness. The 2 s case needs the three processes to finish inside the 5 s wait limit, so a very slow host could fail it. One queued update and N=1 per case; the in-apply case covers the point just before commit only. No crash of the API process and no crash under load. Full check_local and unrestricted go test ./... were not run.

## 한국어 작업 기록

제목: lease를 쥔 작업자 프로세스를 강제로 종료하는 시험
이유: 카드는 대기열의 crash 조건을 요구한다. 이전 재시작 증거는 한 프로세스 안에서 런타임을 바꾸고 lease를 행 수정으로 만료시켰다. 이번에는 실제 lease를 쥔 실제 프로세스를 SIGKILL로 죽인다.
검증: 자체 임시 PG에서 일반·race 모두 통과했다(기존 스냅숏 갱신 시험과 함께 각 31건).
- 모든 경우에 죽은 프로세스의 lease가 DB에 남았고, 만료 전에 띄운 새 프로세스는 아무것도 적용하지 않았다.
- lease 2초(대기 한도 5초 안): 만료 뒤 새 프로세스가 새 lease를 잡고(세대 1→2), 갱신을 한 번만 적용한 뒤 게시했다(세대 1). 항목은 시도 2회 뒤 done이다. 별도 읽기 전용 갱신은 DB를 바꾸지 않았다.
- lease 10초(한도 밖): 갱신이 이미 5초 한도를 넘겨 시도 1회 뒤 failed(queue_wait_exhausted)로 끝났다. 적용 0건, 게시는 보류됐다. 명시 repair가 보류를 풀었고, 그 뒤에도 적용은 없었다.
- 독립 검토에서 거짓 주장은 없었다. 지적한 항목 행 상태·게시 단언 추가, stderr 보존, lease 2초 확대, "fence" 표현 수정을 반영했다.
- 적용 트랜잭션 안에서 모든 쓰기를 마치고 commit 직전에 죽인 경우: event가 남지 않았고 항목은 대기열에 그대로였다. lease 2초 뒤 새 프로세스가 한 번 적용하고 게시했다(세대 1).
- lint 0건, 복잡도 추가 없음.
한계: 미수용 상태다. 설정 검사는 lease가 대기 한도보다 긴 값을 받아들인다. 그런 설정에서는 작업자가 죽을 때마다 갱신이 재시도되지 않고 실패로 닫힌다. 그때 작업자 라운드 오류가 보류된 대기열과 같은 "dynamics runtime binding required"로 나오고, 그 라운드는 게시 단계를 건너뛴다. 죽은 작업자의 늦은 쓰기는 이번에 시험하지 않았다(오래된 claim 차단은 기존 프로세스 안 재시작 시험이 다룬다). 적용 증거는 native event 행 수 하나다. 2초 경우는 프로세스 셋이 5초 안에 끝나야 해서 아주 느린 호스트에서는 실패할 수 있다. 갱신 1건, 경우마다 1회다. 적용 중 crash는 commit 직전 한 지점만 시험했다. API 프로세스 crash와 부하 중 crash는 하지 않았다. 전체 check_local과 무제한 go test는 실행하지 않았다.

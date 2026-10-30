# release a withheld dynamics publication and keep the lease inside the wait limit

Task: SYS-05.B (#152)
Research: Engineering fix from an independent acceptance review; isolated tests on an own disposable PostgreSQL; no load comparison.
Design: A one-shot daemon mode, `-dynamics-repair`, uses the worker token and both pinned dynamics configs, calls the existing verify-then-republish repair for every configured scope and prints one JSON line per scope; a conflict (no hold, a live lease or a pending purge) is reported as not released and any other failure stops the run. The dynamics config now rejects a lease that is not shorter than the queue wait limit. The shared fixtures use a 4 s lease; the crash case that used a 10 s lease now drains 1.5 s after a 4 s lease, past the 5 s wait limit.
Relation: adapted
Deviation: None.
Why: The independent review accepted criteria 1 to 3 and set conditions: the repair had no caller outside tests, so one terminal failure withheld a scope for good; a lease longer than the wait limit made every crashed claim exhaust its entry; and the card check regex did not select the measurement, HTTP admission and refresh tests.
Verified: Own disposable PostgreSQL, -race, internal/memory and cmd/memoryd with the card regex widened to measurement, HTTP admission, snapshot refresh and daemon dynamics tests: 53 top-level tests, 136 passed and 1 skipped. The skip is the locked sync/async comparison, which runs only with a reviewed lock file and was not run. A worker killed holding a 4 s lease and drained after the wait limit fails its entry as queue_wait_exhausted and withholds; repair releases it with nothing applied. The repair mode is refused without both configs or without the worker token, and a fake repairer showed one line per scope and a stop on denial. A config with a lease equal to the wait limit is refused. golangci-lint 0 issues; complexity tool nothing new.
Limits: Not accepted. The repair mode is not run as a process against a database here; its round is tested with a fake and the repair itself by the existing PG tests. A lease shorter than the wait limit narrows but does not remove exhaustion after a crash, and "runtime binding required" still names that exhaustion. The same-condition sync/async comparison stays not ready, so no superiority conclusion is made. Reverse starvation, sustained overload and separate-process execution remain. Full check_local and unrestricted go test ./... were not run.

## 한국어 작업 기록

제목: 보류된 동적 게시를 해제하는 명령 추가와 lease를 대기 한도 안으로 제한
이유: 독립 수용 검토가 기준 ①~③은 충족, 나머지는 조건부로 판정했다. 해제 함수는 시험 밖에서 부를 길이 없어서, 작업이 한 번 실패로 끝나면 그 scope가 계속 막혔다. lease가 대기 한도보다 길면 crash 한 번에 항목이 소진됐다. 카드 정규식은 계측·HTTP 접수·refresh 시험을 고르지 않았다.
검증: 자체 임시 PG에서 -race로 internal/memory와 cmd/memoryd를 넓힌 정규식으로 돌렸다. 최상위 53개, 136건 통과, 1건 skip이다. skip은 검토된 잠금 파일이 있어야 도는 잠긴 동기/비동기 비교이며 실행하지 않았다.
- 4초 lease를 쥔 작업자를 죽이고 대기 한도 뒤에 drain하면 항목이 queue_wait_exhausted로 끝나고 보류된다. 해제 명령의 함수가 적용 없이 보류를 푼다.
- 해제 모드는 설정 두 개나 작업자 토큰이 없으면 거절된다. 가짜 repairer로 scope마다 한 줄 보고와 권한 거부 시 중단을 확인했다.
- lease가 대기 한도와 같은 설정은 거절된다. lint 0건, 복잡도 추가 없음.
한계: 미수용. 해제 모드를 DB에 대한 실제 프로세스로는 돌리지 않았다(라운드는 가짜로, 해제 자체는 기존 PG 시험으로 확인). lease 제한은 crash 뒤 소진을 줄일 뿐 없애지 않으며, 오류 이름은 여전히 "runtime binding required"다. 같은 조건 비교는 준비되지 않아 우월성 결론은 없다. 역방향 starvation·지속 과부하·분리 프로세스 실행은 남았다. 전체 check_local과 무제한 go test는 실행하지 않았다.

# record why the remaining SYS-05 B gates need an owner decision

Task: SYS-05.B (#152)
Research: Documentation of the measurement contract's fixed outputs; no new measurement.
Design: The remaining B gates were checked against the locked A summarizer instead of being attempted. summarize_load.py writes service_execution_authenticated as false in both of its outputs (lines 357 and 399) for every observation origin, so moving the projected run's worker into a separate process cannot change that field; the summarizer is part of the locked A contract and stays unchanged. A separate worker process is already exercised functionally by the snapshot refresh test, which runs the worker in a child test process and serves the published generation to a read-only API process over HTTP. The same-condition comparison stays not ready because the v2 rules give sync, which keeps serving its previous snapshot during a purge, no repair interval. Lifting either gate needs an owner decision on the comparison contract, not more harness code.
Relation: adapted
Deviation: None.
Why: The last records listed a separate-process measurement as remaining; this records that it cannot move the locked result, so it is not repeated.
Verified: Read summarize_load.py at lines 357 and 399: service_execution_authenticated is the constant False. Read TestDynamicsSnapshotRefreshSeparateWorkerHTTPPG and its child helper: the worker runs in a child test process and the parent reads through a read-only token and HTTP. Documentation only; no code changed.
Limits: No test was run for this record. The owner decision on the A-v2 comparison rule and on what would count as authenticated service execution is not made here. SYS-05 is not accepted.

## 한국어 작업 기록

제목: 남은 SYS-05 B 관문에 소유자 결정이 필요한 이유 기록
이유: 지난 기록은 분리 프로세스 측정을 남은 일로 적었다. 하지만 그 측정으로는 잠긴 결과가 바뀌지 않으므로, 같은 일을 되풀이하지 않도록 기록한다.
검증: summarize_load.py 357·399행에서 service_execution_authenticated가 상수 False임을 읽어 확인했다. 스냅숏 갱신 시험에서는 작업자가 자식 프로세스로 돌고, 부모는 읽기 전용 토큰과 HTTP로 게시본을 받는 것을 읽어 확인했다. 문서만 바꿨고 코드는 바꾸지 않았다.
한계: 이 기록을 위해 시험을 돌리지 않았다. A-v2 비교 규칙과 '인증된 서비스 실행'의 기준에 대한 소유자 결정은 여기서 내리지 않는다. SYS-05는 미수용이다.

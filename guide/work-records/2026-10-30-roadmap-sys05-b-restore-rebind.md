# rebind a restored dynamics queue to its new storage

Task: SYS-05.B (#152)
Research: Engineering integration with synthetic fixtures and an actual pg_dump/pg_restore in an isolated database; no load, quality or scientific claim.
Design: An offline operator step reuses the native recovery pattern. The current original is read inside a paused, serializable transaction; the restored copy must be open, still under general restore review and report a different storage identity. The whole ledger (coordinator, every terminal entry, active entries with their captures) must match byte for byte before the coordinator and active entries are rebound with a new lease generation. No hold is cleared.
Relation: adapted
Deviation: The draft ran the rebind inside the paused restored transaction; the existing write gate refuses that write, so the restored copy must be open while the general review still holds publication. The test was split into helpers to meet the complexity limit.
Why: Without a rebind a restored copy keeps the original storage identity in its queue and stays permanently refused; it was safe but not recoverable.
Verified: Against an own disposable PostgreSQL with actual dump/restore: before the rebind the clone refuses claims; self, wrong-generation, diverged-ledger, paused and unreviewed attempts are refused and leave the clone unchanged; a matching clone rebinds once, a second attempt is refused, the original is untouched and the general hold remains; a lease copied by the dump is rejected; while held the worker publishes nothing; after the existing review releases the hold the queue drains and each rebound capture carries a native receipt. Removing the ledger comparison makes the diverged case fail. Adjacent dynamics and recovery tests passed under -race (87 pass). golangci-lint v2.14.0 reported 0 issues. Selected packages internal/memory and internal/memory/dynamicsv2: 6042 passed; under -race 5988 passed; no failures; 21 pre-existing skips in each run.
Limits: Not accepted. One scope per call; terminal history above 4096 entries is refused rather than paged. The restore review release is the existing manual marker removal. No CLI or HTTP entry point, Windows, separate-process rebind or long-run test. Full check_local and unrestricted go test ./... were not run. SYS-05 stays in_progress; 25/63 done, 24 required remaining.

## 한국어 작업 기록

제목: 복원한 동적 대기열을 새 저장소에 다시 연결
이유: 지금까지 복원본 대기열은 원본 저장소 신원을 그대로 갖고 있어 영구히 거부됐다. 안전하지만 복구할 수 없었다.
검증: 자체 임시 PG에서 실제 dump/restore로 확인. 재연결 전 복원본은 작업을 거부한다. 자기 자신·틀린 세대·이력 불일치·정지 상태·검토 없음은 모두 거부되고 복원본은 그대로다. 일치하는 복원본은 한 번만 재연결되고, 원본은 그대로이며 일반 보류는 남는다. dump에 복사된 lease는 거부된다. 보류 중에는 게시가 없고, 기존 검토가 보류를 풀면 대기열이 비워지며 각 capture에 native 영수증이 남는다. 이력 비교를 빼면 불일치 사례가 실패한다. 인접 시험 race 87건 통과, lint 0건. 선택 패키지 internal/memory·dynamicsv2 전체: 정상 6042건·race 5988건 통과(기존 skip 21건), 실패 0건.
한계: 미수용. 한 번에 범위 하나, 종료 이력 4096건 초과는 거부한다. 보류 해제는 기존 수동 절차다. CLI·HTTP 진입점·Windows·별도 프로세스 재연결·장시간 시험은 없다. 전체 check_local·무제한 go test 미실행. 전체 25개 완료·필수 24개 남음 유지.

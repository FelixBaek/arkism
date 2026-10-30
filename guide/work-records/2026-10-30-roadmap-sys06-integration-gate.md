# SYS-06 stage A: flag matrix, contract index and integration report validator

Task: SYS-06 (#152)
Research: none
Design: Offline gate under evaluation/memory-harness-v1/system/sys_06. A 4×8 flag matrix, a SHA-256 index of the SYS-01~05 contract files, and a validator returning needs_fix, hold, synthetic_pass or accepted_local_integration.
Relation: faithful
Deviation: Stage A only. Product wiring (stage B) is not started: it needs SYS-05 acceptance, an accepted DYN policy and a Story allowlist.
Why: Before any dynamic policy reaches the product, a report must show that off and shadow keep the baseline, that faults fall back with a recorded reason without skipping deletion, permission or revision checks, and that synthetic results are not reported as integration.
Verified: 13 validator tests passed, including a good report and each rejection. Mutations removing the blocking, synthetic-claim, fallback, skip and drift checks each made a test fail. The harness collector now runs 564 tests (551 before plus 13) and passed.
Review: An independent review rejected the first draft with seven reproduced defects (crash on non-string case keys, SYS-05 review state ignored, required checks not matched, product diff outside the allowlist, loose types, unchecked hidden fallback count, no scope/approval/source recheck). All seven were fixed; the reviewer's scripts now hold or reject, 19 tests pass and eight mutations, one per fix, each fail a test against a passing control. The report is read from standard input instead of a path argument. The harness collector now runs 570 tests and passed.
Limits: The validator judges reports and runs nothing; a well-formed report does not prove its observations. No product code, flag or config change. Whole SYS-06 is not accepted. Full check_local was not run.

## 한국어 작업 기록

제목: SYS-06 A단계 — 기능 스위치 행렬·계약 색인·통합 보고 검증기
이유: 동적 정책을 제품에 붙이기 전에, 꺼짐·그림자 모드가 기존 결과를 지키는지 확인해야 한다. 고장 때 이유를 남기고 기존 결과로 돌아가는지, 삭제·권한·revision 검사를 빼먹지 않는지도 봐야 한다. 모의 결과를 통합 완료로 보고하지 못하게 막는 것도 목적이다.
검토: 독립 검토가 결함 7건(비문자열 칸 이름에서 죽음, SYS-05 검토 상태 무시, 필수 명령 미대조, 허용 밖 파일, 느슨한 타입, 숨은 대체 횟수, scope·approval·source 재확인 없음)으로 반려했다. 모두 고쳤고 검토자 스크립트는 이제 보류·거부된다. 시험 19건 성공, 수정별 변이 8개 모두 잡힘(대조군 정상). 보고서는 경로 대신 표준 입력으로 읽는다. 하네스 수집기 570건 성공.
검증: 검증기 시험 13건 성공. 막힘·모의 주장·대체 경로·skip·계약 변경 검사를 하나씩 빼면 시험이 실패했다. 하네스 수집기는 564건(기존 551+13)이 모두 성공했다.
한계: 검증기는 보고서를 판정할 뿐 제품을 실행하지 않는다. 제품 코드·설정은 바꾸지 않았다. SYS-06 전체는 B단계가 남았다. 전체 check_local은 실행하지 않았다.

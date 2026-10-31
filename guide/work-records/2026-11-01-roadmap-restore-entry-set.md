# Verify the actual restored queue entry set before rebinding

Task: SYS-05 recovery, PR526 independent review
Research: Engineering validation of offline recovery; no experiment or effect claim.
Design: In the existing recovery transaction, read the bounded actual entry set and require exact cardinality, canonical SQL/payload IDs, unique ordinals and valid metadata before hashing the expected ledger.
Relation: adapted
Deviation: No recovery authority, hold, storage identity, digest or native-state requirement is relaxed. Existing coordinator bounds limit the extra scan to 4597 rows.
Why: Actual dump/restore with an additional terminal entry beyond Tail incorrectly reconciled successfully and could collide with the next admission.
Verified: The original actual-restore failure is preserved. The corrected actual probe passes. Selected recovery normal and race suites each passed 11 top-level tests with 40 pass events, zero failures and zero skips. The two added restore/entry-set tests use genuine restricted LOGIN; unchanged regression tests retain their original role fixtures. A separate reviewer used a fresh disposable database for the original extra-row probe and five additional metadata boundaries; all passed. These overlapping checks are not added together. Test databases, roles and clients were zero before both private clusters were stopped and removed.
Limits: Selected recovery regression and independent boundary review, not a full repository CI or load result. No operating-data access, provider call, product build or deployment. Rows outside the physical scope or with all entry identifiers erased remain outside the existing scoped ledger contract.

제목: 복원 큐의 실제 항목 집합을 검증한 뒤 저장소 연결
이유: Tail 뒤에 추가된 완료 항목을 놓쳐 복원 검사가 성공하고 다음 등록이 충돌하는 오류를 실제 복원으로 재현했다.
검증: 수정 후 실제 복원과 기존 회귀 검사가 통과했다. 일반·race 각각 상위 11개, pass 이벤트40개, 실패·스킵0이다. 신규 복원·항목 집합 시험2개는 실제 제한 LOGIN을 사용하고 기존 회귀 시험은 원래 역할 방식을 유지한다. 독립 검토의 추가 메타데이터 경계5개도 통과했다. 중복 검사는 합산하지 않으며 임시 DB·역할·연결 정리를 확인했다.
한계: 선택된 복원 검사 결과이며 전체 CI·부하·운영 검증은 아니다. 기존 범위·권한·보류·식별자 검사를 유지한다. 제품 빌드·배포 없음.

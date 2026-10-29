# Validate synthetic shadow comparison contracts

Task: SYS-01.spec, R8 collector followup (#152)
Research: P1 engineering foundation; synthetic observations only
Design: Keep immutable candidate/clock/outcome observations, per-reference writes and ordered calls explicit. Preserve the native baseline and its five loss stages without invoking product Search.
Relation: adapted
Deviation: Stage A is a typed synthetic comparator and declared-fixture oracle. Stage B requires separately implemented actual product integration; no actual native witness is authenticated by its label.

The contract covers same-candidate comparison, complete result projection including authority/degraded ordering, unknown observations, current-mode witnesses, postpack/final material joins and native scope rules. It distinguishes declared measurements from unknown physical effects and never equates unknown with zero. Forty typed models and forty-six synthetic cases preserve history/session/source-deletion boundaries.

Verified: author76 methods/116 leaf; independent original17/23 and fresh9/24, each with zero failures/errors/skips. A separate actual chr(133) control case passed1 method/2 leaf. Source four pins match independent before/after records. Root verified every reported artifact SHA. Original I01 paired-clock binding, I02 postpack/committed output binding and I03 native scope guards have preserved pre-fix source and actual failure logs. The first independent pre_final==selected_prepack oracle was wrong and was corrected without redefining native behavior. A later claimed control-character fixture replacement did not happen; the erroneous report remains preserved and a separately pinned followup supplies the missing actual input and evidence.

The R8-owned outer collector preserves its fixed five baseline modules and adds an explicit separate system-module list with the existing mandatory no-skip/no-empty/import guards. A first combined list failed the baseline fixed-list invariant; that failure and source were archived. The corrected outer discovery ran266 tests successfully. Independent collector review confirms all SYS76 IDs execute exactly once in outer266; four independent guard methods/ten leaf also pass with zero skips. Final combined check_local session64935 exited0: Python488 (173+266+49), Go complexity/vet/full-race and UI532 passed. Final four source pins were verified unchanged after the run. Publication audit of all eight staged files passed with only the exact previously authorized fixed-contract exception.

Limits: Stage A synthetic functional validation only. Actual product Search, Go adapter, isolated PostgreSQL integration and operational permissions/physical effects are not accepted. Whole SYS-01 remains in_progress; count19/63 unchanged. No model/provider/human/operational DB/product build/deploy/Claude edits. Fixed baseline contract, types, registry and original gold data remain unchanged.

## 한국어 작업 기록

작업 ID: SYS-01.spec, R8 후속 (#152)
이유: 같은 후보·시각·권한 관측을 고정해 비교하고, 알 수 없는 값과 실제 관측을 구분한다. 제품 검색은 호출하지 않는다.
검증: 작성자76방법116세부, 독립17/23·9/24, 실제 제어문자1/2 각각 통과·skip0. 수정 전 실패·잘못된 검사 준비/보고를 분리 보존했다. 기본 수집기266개 통과. 수집기 독립 검토에서 SYS76개 각각 한 번 실행과 경계4방법10세부 통과를 확인했다. 최종 전체검사64935 종료0: Python488·Go복잡도/vet/전체race·UI532 통과, 최종4파일 해시 불변이다. 공개 검사도 허용된 고정 계약 예외 외 지적0이다.
한계: 합성 A만 검증했다. 실제 제품 연결 B·Go/PG·권한/물리 효과·제품 빌드/배포는 미검증이다. 전체 카드는 진행 중이며19/63을 유지한다.

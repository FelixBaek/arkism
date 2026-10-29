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

## Sonar negative-fixture followup

Task: SYS-01.spec
Why: The remote security gate flagged a publicly writable directory literal used only as negative error-path data. The test never accesses that path. Replace that literal with another absolute invalid error path while preserving all four negative cases and their exact rejection expectation; no policy, contract, case JSON, checker or suppression changes.
Verified: Original source and failing remote log/API findings are preserved. Followup outer discovery266 and full check73256 passed. The earlier accepted-source results above remain historical evidence for their original bytes.
Limits: No actual filesystem vulnerability was observed. Other reported maintainability suggestions are not claimed resolved. Product B remains unimplemented, count19/63 unchanged.

이유: 실제 경로 접근이 없는 오류 경로 거절 자료의 /tmp 문자열에 원격 보안 검사가 반응했다. 다른 절대 경로 문자열로 바꾸고 네 음성 사례·거절 기대값을 유지한다.
검증: 수정 전 소스·원격 실패·API 근거 보존, 후속 outer266·전체73256 통과. 앞선 수용본 검증 기록은 원 bytes의 이력으로 유지한다.
한계: 실제 파일 접근 취약점은 관측되지 않았다. 나머지 유지보수 제안 해결이나 제품 B 완료를 주장하지 않는다.

## Exact empty-digest scanner followup

Task: SYS-01.spec
Why: The remote secret scan reported67 generic-api-key matches in race_cases.json; every value is the public SHA256 of empty input used by synthetic UsageReceiptProjection.project_key_sha256. Preserve fixture bytes and add a match-only allowance for that exact field and exact empty digest. Other fields and every different digest remain scanned; no whole-file exclusion is added.
Verified: Reproduced67 original scanner findings locally with redacted reports and proved all67 values equal SHA256(empty). Original private generator derives H(empty). Actual positive controls verify different high-entropy values in both the fixture path and other paths, plus a different key field, still fail scanning. Exact known non-secret field/value is allowed regardless of path. The first draft incorrectly combined a global path criterion with condition=AND; a positive control caught broad suppression in installed8.28.0. That uncommitted draft and failed preparation logs were archived, and the path criterion was removed. Final combined check6222 exited0: Python488, Go complexity/vet/fullrace, UI532. Independent scanner review accepted this exact narrow block after12 before/after control runs and unchanged19 source pins. Local git --all additionally finds8 historical matches outside this stage; they remain unresolved, distinct from clean working-directory scanning.
Limits: This is an exact known-public digest allowance, not approval of arbitrary hashes or fixture files. Original Sonar literal followup passed local whole checks73256 and independent76/116/outer266, but the final combined configuration check6222 passed; new remote gates remain pending. Original acceptance bytes/reports remain historical and unchanged.

이유: 비밀 탐지67건은 모두 합성 영수증의 빈 입력 SHA256이었다. 자료를 바꾸지 않고 정확한 필드·정확한 공개 digest만 허용한다. 파일 전체 예외는 두지 않는다.
검증: 실제67건 재현·원생성식·값 일치 확인, 다른 값/필드 적발 대조를 수행했다. 첫 global AND 초안의 넓은 예외를 대조 검사에서 발견해 공개 전 폐기했다. 최종 전체6222 종료0(Python488·Go복잡도/vet/race·UI532). 독립 scanner 검토에서12대조 기대일치와19파일 불변을 확인했으며 로컬 전체git이력의기존8건은미해결로구분한다.
한계: 임의 hash나 실제 key를 허용하지 않는다. 기존 수용 이력은 보존하며 새 원격 검사는 미확인이다.

The8 local-history hits are retained by other local/backup refs, not ancestors of this branch. HEAD-ancestry scan passed0 as a separate diagnostic, never a replacement for allrefsFAIL8. The earlier remote job stopped at the67 directory hits before its history command; a fresh remote result is still required.
로컬 이력8건은 다른 local/backup refs에만 남은 비조상 이력이다. HEAD범위0건은 전체이력8실패를 대체하지 않으며, 원격 최초 작업은 directory67건에서 중단했으므로 새 원격 결과를 기다린다.

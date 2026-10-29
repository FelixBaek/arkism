# Preserve participant Brier and group-specific action and time contrasts

Task: PSY-03.simulation (#157)
Research: Candidate B synthetic analysis preparation; no human or observed native truth
Design: Session2 primary states receive equal weight within participant, then participants receive equal weight within group; preserve B2 minus B1. Separate response/oracle, participant/task/attempt, withdrawal, action failure and valid completion-time denominators. Report group/state flows and descriptive action/time contrasts.
Relation: adapted
Deviation: The v7/v8 Brier target supersedes the old Likert2x2 primary analysis. Explicit development assumptions support hand checks and candidate sensitivity only; scientific analysis and sample decisions remain unselected.

Why: Check analysis before collecting data. Independent counterexamples showed that swapping the two groups' unnecessary recorrections or completion times left the entire analysis unchanged. Preserve group/state observations and descriptive B2 minus B1 while retaining the primary calculation and existing exclusion/unknown boundaries.
Verified: Author45 methods/107 leaf passed; the original39 methods/100 leaf passed separately. Original independent counterexamples changed from2 actual failures to2 passes after the fix; source and original logs were preserved. Literal group recorrection contrasts are minus1/plus1 and completion-time contrasts plus2000/minus2000ms. Additional Brier endpoint contrasts plus1/minus1 and the existing zero-effect check passed. Four of30 source pins changed intentionally; the other26, including all20 accepted predecessor files, stayed unchanged. Fresh independent follow-up30 methods/63 leaf (original16/47, original counterexamples2/2 and new literal12/14), author45/107 and original39/100 separately passed without failures/errors/skips. Actual output preserved both directional contrasts, observed-person weighting versus task pooling, missing/withdrawn/diagnostic/action/time boundaries and null comparisons. Root verified all30 current source pins and27 independent artifacts. Synthetic simulation substage is accepted.
Limits: All rows are explicitly synthetic/manual-expected-only. Actual native Y and human responses are zero runs, not observed zero outcomes. Scientific inclusion/missingness/CI/time-analysis/sample parameters remain null; no noninferiority, causal effect, scale validity, power guarantee or equivalence claim follows. Pilot readiness stays false with missing native/B0/researcher/institutional conditions listed. No product UI, database/model, recruitment, build/deployment or Claude changes. Simulation substage only is accepted; whole card remains in_progress and21/63 is unchanged.

## 한국어 작업 기록

이유: 집단별 행동·완료시간 차이가 전체 합계에 묻히는 실제 정보 손실을 수정했다. 상태·개인 가중과 결측 분모를 함께 검산한다.
검증: 작성자45메서드·107세부 검사, 기존39메서드·100세부 재생 통과. 독립 원 반례2건을 보존하고 수정 후 통과를 확인했다. 기존20파일은 불변이다. 독립30메서드·63세부 검사와 작성자 최신/기존 재생도 각각 통과했다. 현재30핀과 독립 증거27파일을 확인해 합성 구현만 수용했다.
한계: 실제 사람 자료나 native 정답은 없다. 과학적 분석 선택과 연구 승인은 미정이며 pilot 준비 완료로 표시하지 않는다.

Public-record validation: check_local55678 exit0, Python567 (173 tools,345 evaluation,49 integrations), Go complexity/vet/full race and UI532 passed. This clean66f parent plus records excludes the unfinished DYN05 worktree and its known numerical-rule failure. Publication audit passed with only the previously authorized fixed-contract exception. Initial sandbox socket errors were preserved and the identical suite reran with test-local sockets allowed.
공개기록검증: 전체55678 종료0(Python567·Go복잡도/vet/race·UI532), 공개검사 통과. 별도DYN05 미완료/1FAIL과 구분한다. 최초 샌드박스 소켓 차단 로그는 보존했다.

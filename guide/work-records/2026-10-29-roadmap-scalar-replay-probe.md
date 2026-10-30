# Replay accepted scalar prefixes and compute read-only views

Task: DYN-03.scalar (#152)
Research: P1 synthetic engineering foundation; no effectiveness study
Design: Reuse ApplyEvent, ResetRevision, scalarReceiptHistory/scalarStateHistory and scalarDecay; retain the original typed schema and validation order. Prefix cutoff is inclusive accepted_seq; no automatic event sorting.
Relation: adapted
Deviation: Only complete synthetic accepted-prefix checkpoints. No compaction, pruning, authenticated restore, fast/slow support or full historical numeric authenticity from a resealed checkpoint.

ReplayPrefix supports suffix or full retransmitted history, emits deeply owned exact-cutoff snapshots, retains every activated full key and classifies attempted delivery rows. Only actual activation initializes an explicitly catalog-current full key. New valid future headers stop before body evaluation; a known original ID still undergoes full retry/conflict validation even if its coordinates are forged into the future. Invalid minimal headers reject explicitly. ReadAt returns a view without writes to state, receipts or cursor and uses integer time subtraction before existing R4 decay.

A separate reviewed implementation-semantics supplement binds these boundaries without changing the original contract. Future-time read is extrapolation, not proof of absent later events. Exact declared lifecycle status governs eligibility; a higher revision alone does not exclude an older declared active key.

Verified: Author followup normal/race25methods/95nodes/78leaf, zero failures/skips, vet and pinned lint0issues, complexity with no new >20 debt. Root verified original author004 and followup artifact/source hashes and raw JSON counts. Fresh independent original22methods/30leaf had2actual failures: nil events accepted as known empty, and visited nonactivation hash alias reported missing_state rather than revision_mismatch. Before-fix sources and normal/race failure logs were preserved. Author corrected both under the original all_arrays and alias rules; fresh independent normal/race25methods/46nodes/39leaf passed with zero failures/skips. Root verified every pinned artifact, raw counts and twenty source pins; all stayed unchanged after integrating the separate SYS stage. Final combined check_local40568 exited0: Python488 (173+266+49), Go complexity/vet/fullrace and UI532 passed. All twenty pins remain unchanged after the run; staged publication audit and directory secret scan passed.

Initial author source defects in fixed DTO enum/error names and overly broad unsupported-namespace mapping were corrected after actual failures. Preparation errors were kept distinct: a qualified-receipt mutation with stale consumption hash in a test, an error-priority fixture targeting a later field, short policy labels that failed to disable flags, and independent helper-name collisions/unused local causing compile failure with zero collected tests. The first policy-flag PASS did not prove disabled-mode coverage; the corrected real-ID/flag assertions ran before author004 acceptance review.

Limits: DYN-03.scalar is accepted in the verified synthetic scope. Whole DYN-03 stays in_progress; conditional fast/slow remains unexecuted and unsupported. Declared checkpoint hashes cannot discover omitted operational lifecycle events or authenticate every prior numeric value without original events/trusted parent. Existing ApplyEvent clones/hashes a growing ledger, so no total linear-time or throughput claim is made. No product Search/DB/API/provider/model/human/build/deploy/Claude changes; whole-card count remains19/63.

## 한국어 작업 기록

작업 ID: DYN-03.scalar (#152)
이유: 기존 상태 전이를 재사용해 확정 사건 순서의 특정 지점까지 재생하고, 조회가 상태를 바꾸지 않도록 한다.
검증: 작성자 후속 일반/race25방법78세부·vet/lint/복잡도 통과. 독립 원검사에서 발견한 nil배열·alias오류우선순위2건의 실제 실패와 원소스를 보존 후 수정했다. 독립 재검증 일반/race25방법39세부 통과, root가 원로그·모든 artifact 해시·20개 source pin 및 SYS선행 통합 후 불변을 확인했다. 최종 전체40568 종료0(Python488·Go복잡도/vet/race·UI532), 20핀 불변·공개/현재디렉터리 비밀 검사 통과.
한계: scalar 하위 단계만 수용하며 fast/slow·삭제 안전 복원·과거 수치 진위·실제 효능은 미검증이다. 제품 빌드/배포·Claude 작업 변경 없이 전체19/63을 유지한다.

## Optional capture disposition

Task: DYN-04
Decision: held / not_selected now. The v8 card requires a research question and scalar contrast plan; v4 requires accepted fast/slow and B3 pilot comparisons. Current scalar acceptance does not satisfy those prerequisites, and no accepted pilot establishes capture necessity. No capture code, successful stub or invented coefficient is added. Reopen only with a recorded need, comparison plan and original optional gates; real outcome integration additionally requires SYS-02/SYS-03. This is a planning hold, not evidence that capture is ineffective or an implemented-card completion.

선택 DYN-04는 현재 보류한다. scalar 대비 필요성과 원 fast/slow·B3 비교 근거가 없으며, 원 조건을 충족하면 재검토한다. 구현 완료나 효과 없음으로 바꾸지 않고 더미 코드도 추가하지 않는다. 전체19/63은 유지한다.

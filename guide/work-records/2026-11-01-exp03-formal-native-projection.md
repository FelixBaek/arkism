# EXP-03 formal native run projection

This initial implementation record describes its original snapshot. The policy update below supersedes the original pending-owner-choice statements; historical test counts and other limits are not current integrated-tree acceptance.

Task: EXP-03 (#153)
Research: Paired analysis evidence plumbing; no new empirical/user-effect or scientific acceptance claim.
Design: Project the accepted closed EXP02 RunPlan/RunIndex into analysis inputs through a live caller-pinned native verifier executable snapshot. Bind original typed QueryPlan/GoldRow, source manifest, explicit materialization mapping, native probe/snapshot/cutoff, scopes, budgets and every job/attempt/repetition. Preserve exact raw verification reports and all attempt costs. Never execute recorded argv, fabricate missing grades or divide native batch charges into query costs. New-output publication reuses accepted safe artifact primitives without changing frozen codecs/DTOs.
Relation: partial
Deviation: New computational sidecars preserve all frozen v1/native payloads. Producer materialization mapping is bound, not independently reimplemented or authenticated. Provided grades are source-bound labels, not proof of actual checker truth, live reader delivery or participant Y. Native batch measurements remain separate from explicitly unknown query allocations. Retry selection has no default and awaits owner choice for real data; tests use labeled synthetic choices only. Final-lock trust and downstream EXP04 consumption remain engineering work, not insufficient-data holds.
Why: The actual native replay verifier alone could not reconcile a formal plan/index, original question/view, failures and costs into the analyzer. Directly trusting a saved success report, dropping failed attempts, guessing temporal ordinal equivalence or dividing batch charges would not satisfy the original roadmap requirements.
Verified: Current focused EXP03 suite passes 76 tests; collected evaluation passes 673 tests. The native package passes 20 top-level tests with race detection, including an actual Go→Python API/CLI→snapshotted Go verifier loop. That loop processes seven policies/two real native fixture refs in current and historical modes, explicitly mapping source plan sequence7 to native accepted sequence2, verifies all generated file hashes and no source/query-body leakage, retains both completed/timeout attempts under both retry choices, and distinguishes missing grades from supplied synthetic grader labels. Quality remains undetermined for insufficient independent data/unobserved token budget. Pinned native-command lint passed before the final source-only projector refinements; current full-tree verification is being prepared, not claimed passed. Work-record and roadmap checks are separate structural evidence.
Limits: No whole-card or roadmap completion. The formal EXP02 execution builder, final-lock/trust integration, actual independent outcomes/labels, responsible scientific acceptance and downstream EXP04 native-closure/cost integration remain. No owner policy or approval is invented. No standalone verifier/product binary build, operating DB, deployment, paid provider/model call, automatic commit or PR. Go test executable reuse is test execution; its actual command code is exercised, not a prewritten fake JSON report. A later full check must cover this current source, not reuse the older pre-projector snapshot's pass.

Independent review fixes: Reviewers reproduced Decimal rounding at 10^50+1 and mismatched Gold clocks/view cutoffs. Exact input-derived precision now retains both that integer and 1+10^-70 without rounding. Typed original question/gold joins and an explicit materialization record bind clock, requested-as-of, planned/native ordinals and native candidate view. Boolean values cannot masquerade as integer cutoffs. A further review found that policy registry equality alone did not freeze event/importance inputs; repeated queries now hash-check and retain those exact sources, with separate conditions required for changed traces. All initial failing tests/reports remain privately preserved. Tests also retain missing native cost requirements so an empty ledger cannot become complete/free-cost evidence.

제목: 정식 실행 원장과 분석기를 실제 native 검증으로 연결
작업 ID: EXP-03 (#153)
이유: 원장·원래 질문/정답·시점·예산·실패·비용을 실제 재실행 검증에 결속하고 저장된 성공 선언만으로 분석하지 않는다.
실행한 검증: 집중76개·평가673개 통과. 실제 Go/Python CLI 상호호출과 native package20개 top-level race 검사 통과. 현재/과거 시점·서로 다른planned/native순번·재시도 두규칙·누락grade/제공fixturegrade·원본보고서 hash·본문/절대경로 미노출을 확인했다. 독립 검토가 찾은 합계 반올림·시점결속·boolean순번·반복입력 drift 문제를 고치고 회귀를 추가했다.
미실행: 현 소스 전체 check_local, final-lock 권위 결속, 실제 연구 결과/제품 권한 검증, 제품 빌드·배포·운영 DB·모델 호출.
커밋 상태: 별도 EXP03 작업 트리의 미커밋 구현이다.
한계: 양쪽 재시도 규칙을 구현했지만 실제 자료의 선택 정책은 사용자 답변 대기다. 변환 알고리즘 자체의 독립 검증·최종 잠금·실제 자료 수용·EXP04 소비 연결을 완료로 세지 않는다.

## Owner retry policy update

Task: EXP-03 (#153)
Research: Primary attempt selection; no empirical acceptance claim.
Design: The explicit owner reply selects first_completed_else_last_attempted for primary analysis. Comparable completion timestamps determine the first completed result; predeclared order breaks exact ties. Incomparable clocks are rejected. All failed attempts and every attempt cost remain in the ledger; subsequent completed duplicates do not add primary observations. With no completion, retain the last attempted failure without inventing success. The separate last_attempted option is not the primary policy.
Relation: partial
Deviation: None; this records the user decision already enforced by the primary projection gate.
Why: Retry selection must follow the chosen policy rather than favoring later successful or higher-quality runs.
Verified: Re-ran python3 evaluation/memory-harness-v1/experiments/exp_03/test_native_projection.py; exit 0, 26 tests passed. Coverage includes finish-time ordering, timestamp ties, incomparable clocks, explicit selection policy and preservation of retry costs.
Limits: This is a policy confirmation and regression check, not new card completion. The recorder still accepts one declared attempt per job; immutable multi-attempt native output storage remains implementation work. No product build, deployment, paid call or operating database access.

제목: 재시도 주 분석은 먼저 완료된 결과로 확정
이유: 사용자의 명시적 선택을 주 분석 정책에 적용하고 이전의 답변 대기 기록을 정정한다.
검증: 기존 선택 정책·시간 동률·다른 시계 거부·재시도 비용 보존 회귀 26개를 재실행해 통과했다.
한계: 문서·회귀 확인을 개발 완료로 세지 않는다. 실행 기록기의 작업당 다중 시도 출력 보존은 아직 구현해야 한다. 제품 빌드·배포·유료 호출·운영 DB 접근은 하지 않았다.

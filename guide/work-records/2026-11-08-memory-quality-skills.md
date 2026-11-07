# Holdout validation for skill promotion / 스킬 승격의 미사용 과제 검증

Task: memory quality follow-up 4, stronger validation of memory-derived skills.
Research: none
Why: a higher aggregate score can hide a failed task category, and an aggregate
report alone cannot show which previously declared holdout tasks actually ran.
Verified: final scoped results are recorded below.
Limits: opt-in policy; declared hashes do not prove semantic independence or full
disclosure of development exposure. Synthetic execution checks are not evidence
of live-model quality, generalization or a production success rate.

제목: 스킬 승격에 개발 과제와 분리된 검증 과제 적용
이유: 평균 점수로 특정 분야 실패를 숨기거나 실행 근거 없이 승격하는 것을 막는다.
검증: 아래 실제 실행 결과를 따른다.
한계: 과제 해시는 선언된 동일성을 검증한다. 표현만 다른 동일 문제나 누락된 개발 이력을
자동 판별하지 않는다. 실자료 검증과 실제 모델 효과는 보류한다.
연구: 해당 없음

## Implementation / 구현

- Optional `SkillComparisonPlan.holdout` is part of the existing immutable plan
  hash. It declares `development_tasks` (1–128 SHA-256 identities), `tasks`
  (1–62 exact quality-case hashes and domain categories), `min_category_score`
  (0.9–1), and `min_category_cases` (1–62). Every declared category must meet its
  sample-count bound and pass-rate threshold. Existing plans retain their behavior.
- Task hashes are SHA-256 of the existing comparison runner's canonical full
  case JSON. Domain categories in the manifest are independent of the existing
  quality/regression/safety case roles. Each holdout requires baseline/candidate
  pairs; regression and safety checks remain mandatory under the existing contract.
- Development/holdout overlap, duplicated task identity, unknown quality tasks,
  missing pairs and relabelling a holdout as safety/regression fail closed.
  Development tasks are excluded even when submitted as regression or safety.
  There is no averaging across domains to hide a weak category.
- CLI preflight checks declared leakage/coverage before invoking adapters; it
  also computes the domain gate in the final result. Server recording independently
  recomputes coverage and gates within the existing atomic evidence transaction.
- Approval re-reads the exact source-bound comparison result and recomputes the
  measurements and domain pass rates. Ordinary aggregate evaluations without the
  independent service/pipeline comparison source cannot satisfy this policy.
  Existing source validity, privacy, revision, evaluator independence, regression,
  safety, cost, time, approval and rollback restrictions remain in force.

## Evidence and limits / 검증 근거와 한계

Tests cover disjoint manifests, inadequate category samples, thresholds below 90%,
missing/extra/duplicated/relabelled cases, a 90% overall result concealing a 0%
category, rejected aggregate-only approval, actual recorded-case approval and
atomic rejection of missing evidence. Approval is exercised through the service
with both the in-memory repository and disposable PostgreSQL. CLI tests cover
preflight overlap rejection and category gating alongside its existing evaluation
regressions. No representative new held-out dataset or model-quality result is
invented. This adds enforceable declared-evidence contracts, not a claim that all
skills now succeed at least 90% of the time.

Final verification: scoped memory holdout/comparison/proposal regressions passed
against disposable PostgreSQL (16.029s). Scoped CLI holdout and comparison tests,
including an actual local adapter subprocess, passed (2.293s). `git diff --check`
passed. API/SDK schema regeneration belongs to integration.

The first database attempt was refused because the database name did not satisfy
the disposable-test guard. After correcting the name, a test fixture incorrectly
tried to rewrite an immutable comparison plan at the same revision; PostgreSQL
correctly rejected it. The fixture now declares holdout in the real proposal
input. The final run above passed; earlier failures are not counted as passes.

검증 결과: 실제 임시 PostgreSQL을 포함한 관련 메모리 검사는 16.029초에 통과했다.
CLI 검사는 로컬 adapter 프로세스 실행과 개발 문제 유출 사전 차단을 포함해
2.293초에 통과했다. 모델 호출은 하지 않았다. API·SDK 동기화는 통합 단계에서 수행한다.

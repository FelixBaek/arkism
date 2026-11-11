# Per-category accuracy without hiding incomplete outcomes

Task: MEMORY-QUALITY-90, category evaluation follow-up.
Research: computational reporting extension; no new empirical accuracy result.
Design: reuse EXP03's sealed analysis plan, schedule and primary-observation
validation instead of implementing a second attempt ledger or grader.
Relation: adapted from the existing EXP03 analysis inputs.
Deviation: this diagnostic reports all scheduled binary accuracy by category,
including missing/failed/truncated outcomes as non-correct. It does not change
EXP03's research estimand, exclusion rules or acceptance contract.

The overall mean can exceed 90% while a smaller category fails completely.
`category_scorecard.py` now reports exact integer numerators and denominators,
ceil(0.9*n) required correct, and the additional correct outcomes needed for each
category and policy. Repeated executions are supplied only as the repetitions
already declared in the schedule; they are not additional independent questions.
Every scheduled query must have exactly one category assignment. Missing or
empty categories, duplicate primary observations, changed graders and mismatched
source/gold/budget bindings are rejected. Missing and errored observations stay
in their category denominator, even if their row claims a correct grade.

The category map is pinned by a separately supplied raw SHA256 and binds the
schedule. That pin is not proof of when it was made or who approved it. Category
selection must be fixed before outcomes are inspected. The original native
projector remains responsible for selecting the earliest completed attempt from
comparable clocks, retaining failed attempts and all costs. This report neither
selects attempts nor claims it independently replayed native execution.

Even an all-correct report has `scientific_acceptance=false` and
`holdout_independence_verified=false`. Historical permissive grades and reused
development questions cannot become accepted holdout evidence through this tool.
Formal evidence still requires the existing native projection, frozen dataset,
model/configuration, grader and independent review. No raw conversations, gold
answers or historical per-question artifacts are added to the public repository.

## Verification

Seven offline tests passed, including actual CLI subprocess execution. They cover an aggregate90% result hiding a0%
category; missing and timed-out outcomes retained in the denominator; duplicate
answer rejection; map-pin mutation and empty-category rejection; absent grades;
and grader/input/gold/budget binding failures. A numerically perfect input still
cannot claim scientific acceptance. The existing native-projection suite also passed26
tests, including earliest completed result selection with comparable clocks.
`git diff --check` passed.

```sh
python3 -m unittest discover -s evaluation/memory-harness-v1/experiments/exp_03 -p test_category_scorecard.py -v
```

Limits: this is a reporting correction, not a retrieval improvement, live-model
run, product build or measured90% achievement. It supplies no confidence bound
or independent-sample guarantee. Category truth and holdout independence remain
external scientific requirements. CLI success means valid diagnostic inputs,
not that numerical thresholds or scientific acceptance passed.

한국어: 전체 평균만으로 약한 분야를 숨기지 않도록 분야별 정확도를 계산한다.
누락·실패도 분모에 남기고, 중복 정답이나 변경된 채점 근거는 거부한다.
기존 첫 완료 결과 선택 원장을 재사용하며 새 원장을 만들지 않는다.
오프라인 검사7개가 통과했지만, 실제90% 달성이나 독립 평가 완료를 뜻하지 않는다.

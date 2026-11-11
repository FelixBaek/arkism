# Evidence-grounded answer quality

Task: MEMORY-QUALITY-90.
Research: development changes; no new acceptance score.

The previous answer prompt instructed the model to choose an inference whenever
any record concerned the topic, preferred the newest record for every conflict,
and enabled complete-list guidance only when optional passages were present.
These policies could encourage unsupported personal conclusions, replace a
historical event with a different later event, or omit evidence in ordinary
memory-only answers.

The answer instructions now require discriminating evidence for an inference,
explicit uncertainty otherwise, preservation of date precision, and all supported
items for lists/comparisons in every answer mode. A newer record overrides a
conflict only when it represents a real update or correction. Original source
admission, transfer authorization, deletion fences and retrieval budgets remain.
Prompt versions advance to reflect-v5 (ordinary) and reflect-v6 (passages), so
future evaluations can distinguish the changed behavior from historical results.

These are prompt-policy changes, not proof that a language model follows every
instruction. Historical benchmark totals remain diagnostic only. No benchmark
answer, example-specific alias or grading threshold is embedded in the prompt.
Actual answer quality, false abstention and category-level regressions must be
measured with a fixed model and evaluation protocol before a90% claim.

## Verification

The focused reflect race suite passed42 top-level tests with zero failures or skips (3.191s). After independent review qualified passage precedence so older transcripts cannot override later explicit corrections, the passage race selection passed again (1.328s). Earlier runs captured in-progress regression fixtures and failed; those failures are retained and are not counted as successes. At that verification stage, no server build or model evaluation had run. Subsequent isolated evaluation builds and actual results are recorded in [the live evaluation record](2026-11-12-memory-live-evaluation.md); the per-category target remains unmet.

Related work records cover temporal metadata (7 top-level tests and2 subtests), query-source prioritization (10 tests), and the category scorecard (7 new tests and26 existing projection tests). These selections overlap and are not summed into a unique-test total.

## 한국어

관련된 기억이 있다는 이유만으로 추론을 강요하거나, 나중에 기록됐다는 이유만으로
과거 사건을 덮어쓰던 지침을 보완했다. 목록·비교 질문의 근거 수집 규칙은 원문
조각 기능이 꺼져 있어도 적용한다. 코드 검사 통과와 실제 답변 정확도는 구분하며,
새 평가 전에는 분야별90% 달성을 주장하지 않는다.

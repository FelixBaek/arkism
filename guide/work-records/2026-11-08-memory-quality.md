# Memory quality follow-through / 기억 품질 추가 고도화

Task: MQ1 multi-fact retrieval, MQ2 query expression coverage, MQ3 task-level outcomes,
MQ4 development/holdout separation, MQ5 bounded resumable lifecycle maintenance.
Research: none
Why: improve the five follow-up areas while preserving original evidence, privacy,
legacy defaults and honest category-level acceptance.
Verified: initial actual PostgreSQL retrieval selection passed (1.373s); five
category-gate checker tests passed. Post-review regressions and frozen retrieval
comparison are pending. The earlier fake repository did not implement token-based
full-text matching; the new opt-in functional test uses the real database.
Limits: no publication, production deployment, paid model calls or answer-quality
claim. Only the previously authorized isolated offline recall harness is compiled.

제목: 추가 개선 5개 및 분야별 기억검색 품질 기준
이유: 검색 누락·중복 평가·승격 검증·대량 유지보수의 남은 한계를 개선한다.
검증: 실제 DB 검색 집중 검사와 분야별 품질 판정기 검사를 실행했다. 통합 결과는 아래에 갱신한다.
한계: 90%는 고정 질문의 근거 회수율 목표이며 답변 정확도나 모든 실제 업무의 성공률이 아니다.
연구: 해당 없음

## Frozen acceptance / 고정 완료 기준

The existing frozen harness asks 453 questions with 451 scorable evidence sets;
limits are ten memory hits and five conversation entries, with the original
character budgets and provenance mapping unchanged. Category macro evidence
Recall@10 is the primary metric. Every category must reach at least 0.90 without
regressing a stronger baseline and without degraded retrieval. Missing questions,
changed question text/categories/gold evidence or enlarged hit budgets invalidate
the comparison. Limit50 remains diagnostic, never substituted for the primary.
The existing mapping approximates memory provenance through text overlap; this is
a development comparison, not an unseen holdout or model-answer accuracy test.

| Category | Baseline Recall@10 | Acceptance |
| --- | ---: | ---: |
| multi-hop | 0.5955 | at least 0.90 |
| temporal | 0.9109 | at least baseline |
| open-domain | 0.6146 | at least 0.90 |
| single-hop | 0.9707 | at least baseline |

`tools/check_recall_category_gate.py` binds the comparison to the same question
identities and gold evidence, emits a fingerprint, reports each category, and
returns failure if any category misses its gate. Empty-gold rows remain unscored;
they are never converted into successful answers.

## MQ1 / MQ2 implementation

- Conversation excerpts select a contiguous verbatim window of complete turns
  relevant to the query instead of invariably clipping the first600 characters.
  Existing per-entry and whole-lane character budgets remain. Private fragments
  are redacted before scoring. Full source versions are preserved and fenced in
  consumed context. A shortened/middle excerpt cannot be stitched to a neighboring
  batch across omitted turns that could contain a correction.
- Optional `recall_quality.local_expand` removes question scaffolding and searches
  predicate/explicit conjunction fragments, at most four, using only words already
  present in the query. It makes no language-model generation calls. Authorized
  local query embeddings and normal visibility/source checks still apply. Original
  and complementary lanes are fused; both memory and conversation candidates can
  benefit. This setting is off by default; `expand` retains its separate model
  contract. Explicit model expansion takes precedence when both are selected.
- History, related-record lookups and frozen context views do not expand. Failures
  preserve the original side-lane results and mark optional expansion degraded.
  Simple canonical queries are not redundantly expanded. The feature does not
  invent synonyms, fact names or dates and cannot promise semantic completeness.
- Independent review caught two issues before acceptance: optional side queries
  dropping baseline results on failure, and connecting middle excerpts across
  omitted turns. Fixes include focused regressions; their final run is pending.

The new retrieval setting is configured through the existing space API. No new
UI screen is added, and no existing space is enabled automatically.

## MQ3 / MQ4 / MQ5 integration

See the task-specific work records after integration. The implementation status
and any unmet acceptance gate will be updated from actual test results, never
inferred from code completion.

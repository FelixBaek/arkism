# Memory quality follow-through / 기억 품질 추가 고도화

Task: MQ1 multi-fact retrieval, MQ2 query expression coverage, MQ3 task-level outcomes,
MQ4 development/holdout separation, MQ5 bounded resumable lifecycle maintenance.
Research: none
Why: improve the five follow-up areas while preserving original evidence, privacy,
legacy defaults and honest category-level acceptance.
Verified: integrated PostgreSQL memory suite passed with2029 top-level tests and18
explicit skips (726.896s); later retrieval corrections passed their combined
selection (4.405s). Eleven additional real HTTP host/receipt tests passed (19.986s).
API contract validation covered284 real HTTP cases; SDK tests and scoped vet passed.
The first frozen quality comparison failed; the final corrected comparison is pending.
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

- Opt-in conversation excerpts select a contiguous verbatim window of complete turns
  relevant to the query instead of invariably clipping the first600 characters.
  Existing per-entry and whole-lane character budgets remain. Private fragments
  are redacted before scoring. Full source versions are preserved and fenced in
  consumed context. A shortened/middle excerpt cannot be stitched to a neighboring
  batch across omitted turns that could contain a correction.
- Optional `recall_quality.local_expand` removes question scaffolding and searches
  shared predicates for explicit coordinated names, at most four, using only words already
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
  omitted turns. Fixes include focused regressions; the focused actual database and delivery run passed (0.874s).

The new retrieval setting is configured through the existing space API. No new
UI screen is added, and no existing space is enabled automatically.

## MQ3 / MQ4 / MQ5 integration

- MQ3: [task-level feedback](2026-11-08-memory-quality-feedback.md) is integrated.
  Explicit producer task keys group independent executions; only the first
  eligible completed judged result contributes to the main analysis. Raw attempts
  and costs remain. The real DB fixture retained7 raw outcomes and selected5
  independent outcomes,4 passing. Keys cannot prove semantic task identity.
- MQ4: [holdout promotion](2026-11-08-memory-quality-skills.md) is integrated.
  Optional declared holdout tasks must be disjoint from development hashes and
  complete in both baseline/candidate evidence, with a per-category floor of at
  least90%. Admission and approval revalidate the source evidence. Declared hashes
  cannot prove that undeclared exposure never occurred; no new live holdout model
  experiment or production skill promotion is claimed.
- MQ5: [resumable maintenance](2026-11-08-memory-quality-lifecycle.md) and the
  [operator CLI](2026-11-08-memory-maintenance-cli.md) are integrated. Durable100-row
  steps, hold/release, revision checks, safe pre-quarantine cancellation and actual
  subprocess resumption are tested. This is an explicit global outage with an
  upgraded-client prerequisite. Targeted work-checkpoint deletion remains atomic;
  total manifest memory/rewrite cost remains proportional to impact size.

MQ1/MQ2 implementation is present, but the category-level retrieval acceptance is
not complete. Implementation and unit-test completion never substitute for the
frozen evidence-recall target.


Independent review also found native serving could replace the expanded result
with candidates frozen before expansion. Local expansion now triggers the
existing unsupported-context baseline fallback. The first combined regression
used a history-enabled helper (where expansion intentionally does not run); the
fixture was corrected to normal recall at a fixed query clock so recency is identical. API contracts and SDK now include local
expansion, producer task keys and holdout plans. Eight SDK tests passed, including
actual TypeScript no-emit checks and local Python transport.


Further independent review found keyword coverage discount applied only to the
original memory query. Expanded and side-lane queries now apply the same per-query
coverage discount so a common-name-only match cannot acquire full repeated votes.
Explicit coordinated names retain their common predicate in each local fragment;
anchorless predicates are not added. These are general rules, not question/answer
lookup tables. The first frozen run is retained as a development attempt; a new
comparison is required after these changes.

The native fallback/expansion/context regression selection passed (1.305s) after
using a fixed query clock. The excerpt window search was then made linear in
line count times query-term count, avoiding repeated window scans on many empty
turns; the long-input regression and integration rerun remain pending.

In opt-in local expansion, exact-keyword votes also use query-concept coverage
for compound questions. A common name alone no longer receives the full2x vote
of a complete topical match. Deliberate whole-query keywords, single-concept
lookups, unrecognized identifiers and legacy mode retain their existing exact
behavior. Each fragment uses its own query, preserving complementary subject
evidence. The combined expansion/conversation/context/fusion/lexical regression
selection passed (1.830s), including the linear long-input excerpt case.

## Integrated maintenance review

The durable maintenance engine and operator CLI are integrated. Privacy discovery
supports subject, source-event and trace targets; targeted work-checkpoint
requests retain the existing atomic API. Each scan/mutation step is bounded,
while the complete impact manifest still requires memory and JSON rewriting
proportional to its total size. This is an explicit global maintenance outage,
not an online replacement for ordinary APIs. All clients must be upgraded or
stopped; direct administrator SQL is outside the application guard. Migration
nine installs an idle gate and does not activate maintenance.

The first combined PostgreSQL run found an authorization regression: a
non-owner without schema USAGE could not execute the new gate function, changing
retention's explicit owner denial into an unavailable error. The preflight now
reads only role metadata through the pool; all record reads and deletes retain
the transaction gate. A paused-maintenance regression checks dry-run and deleting
retention calls remain blocked. The failing test now closes its pool through
cleanup even when an assertion fails. The initial failure is retained in the
local test log. A previous invocation used the wrong DB environment-variable
name and therefore cannot be counted as PostgreSQL validation.

Independent review checked normal transactions, native/recovery raw-pool paths,
read-only snapshot leases and CLI resume without initialization. It found no
additional gate bypass or lock cycle. Maintenance rollback cleanup now has a
five-second deadline so a cancelled command does not wait without a bound for
cleanup. Integrated reruns and final frozen retrieval measurements remain pending.

The focused maintenance/retention PostgreSQL rerun passed (15.178s), including
blocked owner retention and rejection of roles without schema access. Final
whole-package rerun will include the subsequent bounded rollback cleanup.

## Frozen development comparison 1 — rejected

Run `memory-quality-local-20261108` completed all 906 searches (453 questions,
451 scored). It used the initial retrieval snapshot, identified by harness
revision `cdb400bd +dirty`, before the subsequent coverage/anchor/context fixes.
All HTTP searches succeeded; degraded results0, model trap calls0 and
non-embedding provider-usage rows0→0. These are execution checks, not quality
acceptance. The frozen-question fingerprint was
`f6a6492fa38c0fece24811e68d6f2cb91759748ea0c987162293738c090052ab`.

| Category | Baseline | First experiment |
| --- | ---: | ---: |
| multi-hop |59.55%|59.35%|
| temporal |91.09%|87.84%|
| open-domain |61.46%|62.24%|
| single-hop |97.07%|96.65%|
| overall (diagnostic aggregate) |85.65%|84.82%|

The category checker returned1 (failed acceptance). This attempt is retained as
negative evidence; no category was counted as accepted. The revised integrated
snapshot `fb129389` is being measured separately with identical questions,
evidence mapping and limits. No installed space is enabled by this experiment.

The first full PostgreSQL package invocation also hit Go's default10-minute
package timeout after1928 top-level passes,17 skips and the already-corrected
retention test failure. It is not a passing full suite. The final-source rerun
uses a14-minute Go package timeout inside the existing15-minute outer cap.
The general non-PostgreSQL invocation completed2058 top-level passes and334 skips;
its skipped DB checks are not counted as database evidence. Scoped static vet
and102 management-operation DTO mappings passed. Three clean worker worktrees
were removed only after confirming every worker patch was integrated.

## Equivalent lexical evidence correction

Independent review identified that scaffolding removal produces the same
PostgreSQL FTS concepts/order as the original question. Fusion nevertheless
counted each rewritten query as independent evidence. In local-expansion mode,
FTS hits now carry a private canonical query-term signature. Fusion preserves
the union after relevance checks and credits each hit/signature at its strongest
surviving rank once. Distinct predicates, exact-keyword lanes and vector lanes
retain separate credit. Non-opt-in fusion retains its existing behavior. This
also covers derived-memory and conversation FTS; no positional lane assumption
is used and no candidate is removed solely because a wording is equivalent.

Two synthetic regressions fail against the prior source overlay (0.055s).
The corrected local-expansion/conversation/fusion/diversity/lexical selection,
including real PostgreSQL conversation retrieval, passes (1.864s). The separate
excerpt eligibility correction is documented in its own record.

The second run `memory-quality-integrated-20261108` was deliberately stopped
when these confirmed defects made it a superseded candidate. Its partial rows
and an `aborted_not_accepted` marker are retained; it is not a completed or
passing quality comparison. A new full frozen run is required after integration.


## Actual host transport verification

After the full PostgreSQL suite,11 opt-in host/receipt tests were run with their
own temporary host state, synthetic host identity and disposable database.
External model execution was disabled. The first invocation omitted the fixture
origin and failed identity checks. With the origin set, the old fixture's text
held only one topic from a three-topic prompt, so the pre-existing relevance gate
correctly returned no memories. Independent review confirmed this behavior in
pre-task code as well. The synthetic source now contains the topics its receipt
prompt asks for. The view-transport fixtures now ask for their two actual synthetic
topic families, omitting unrelated framing words; exact-reference, revision,
correction, withdrawal, new-reader and no-fallback assertions remain unchanged.
These are functional fixture corrections, not changes to frozen benchmark
questions, gold evidence, retrieval budgets or production relevance rules.

The final11 host/receipt tests passed in19.986s. They cover actual HTTP source
reads, context exclusions, retained-path delivery, correction/withdrawal, native
active-view transport, lost replies, transaction failure and durable publication.
They establish transport behavior, not model understanding or task success.
The initial fixture failures remain in separate logs. The full suite's remaining
opt-in research/export/live-model/scale/copy tests are not claimed as executed;
the subprocess helper runs only through its parent test. The latest retrieval
code also passed scoped static vet. No product release or production maintenance
was run.

## Default behavior boundary

The new conversation-window selection and side-lane/expanded-query weighting are
now all scoped to `recall_quality.local_expand=true`. With the option off, the
existing prefix selection and side-lane/model-expansion weighting are retained.
Privacy redaction still happens before clipping, unavailable side-lane candidates
remain filtered before limits, and excerpts are never stitched across omitted
turns. These correctness safeguards do not depend on opting into retrieval tuning.

The combined actual PostgreSQL search/conversation/context/opt-in selection passed
(6.039s). After avoiding redundant prefix work on the enabled branch, the on/off
and legacy-model-expansion regressions passed (0.391s); scoped vet also passed.
The frozen run uses snapshot `5e2b2379` with local expansion enabled. Subsequent
changes are functional fixtures/docs and these disabled-option safeguards; the
measured enabled-path algorithms and budgets are unchanged. This is not a separate
full-corpus quality measurement of the disabled/default path.

The earlier evidence-dependent ANN-index and snapshot-reuse research holds remain
in place. Neither a passing functional suite nor partial retrieval scores remove
those holds or satisfy the90% per-category acceptance gate.

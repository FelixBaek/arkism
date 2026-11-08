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
Completed frozen quality comparisons failed acceptance. The latest intent-preserving
comparison completed all906 searches normally; results are below. MQ1/MQ2 remain
incomplete. Its focused checks passed61 tests without skips; updated scoped vet passed.
Limits: no publication, production deployment, paid model calls or answer-quality
claim. Only the previously authorized isolated offline recall harness is compiled.

제목: 추가 개선 5개 및 분야별 기억검색 품질 기준
이유: 검색 누락·중복 평가·승격 검증·대량 유지보수의 남은 한계를 개선한다.
검증: 실제 DB 검색 집중 검사와 분야별 품질 판정기 검사를 실행했다. 통합 결과는 아래에 갱신한다.
한계: 90%는 고정 질문의 근거 회수율 목표이며 답변 정확도나 모든 실제 업무의 성공률이 아니다.
연구: 해당 없음

## Latest completed comparison / 최근 완료 비교

MQ3/MQ4/MQ5는 구현·기능 검증을 마쳤다. MQ1/MQ2의 모든 분야90% 목표는 미달이다.
최근 질문 의도 보존 비교는453문항·906회 실제 검색을 정상 완료했다.
검색 오류·`degraded` 표시·생성 모델 호출은 모두0이다.

| 질문 분야 | 기존 근거 회수율 | 최근 후보 | 판정 |
| --- | ---: | ---: | --- |
| 여러 기억 연결 |59.55%|60.38%|90% 미달|
| 날짜·시간 |91.09%|90.33%|기존 대비 하락|
| 개방형 |61.46%|63.28%|90% 미달|
| 단일 기억 |97.07%|97.49%|통과|

추가 로컬 모델도453문항 전체 검산을 마쳤다. 개방형 성적이 같은 후보의
벡터 기준60.42%에서58.33%로 하락해 채택하지 않았다. 이 수치는 오프라인
순위 진단이며 위 실제 검색 표를 대체하지 않는다. 기본값은 계속 `false`이고,
전체90% 목표는 미달이다. 아래에는 실패한 과거 결과도 보존한다.

별도 요청한 GOAL5 큐 개선은 [유지보수 게이트와 통합 검증](2026-11-08-memory-quality-queue-integration.md)을 마쳤다.
통합 race79개와 하위56개, 최종 정적 검사가 통과했고 제품 빌드·push는 하지 않았다.

## Earlier completed comparison / 이전 완료 비교

**전체 요청 미완료: MQ3/MQ4/MQ5는 구현·기능 검증했고, MQ1/MQ2의 분야별90% 검색 품질 목표는 달성하지 못했다.**
새 검색 옵션은 기본값 `false`이며 생산 환경에 적용하지 않았다. 자료 부족으로
보류한 연구 항목은 그대로 보류한다.

| 질문 분야 | 기존 근거 회수율 | 최종 후보 | 판정 |
| --- | ---: | ---: | --- |
| 여러 기억 연결 |59.55%|60.66%|90% 미달|
| 날짜·시간 |91.09%|89.56%|90% 미달·기존 대비 하락|
| 개방형 |61.46%|61.46%|90% 미달|
| 단일 기억 |97.07%|96.65%|90% 이상이지만 기존 대비 하락|
| 전체 참고 평균 |85.65%|85.37%|분야별 완료 기준을 대신하지 않음|

The final frozen candidate `memory-quality-final-20261108` produced453 complete
question rows,451 scored questions and906 successful HTTP searches. Degraded
results0; model-trap lines0; independently queried final non-embedding usage0.
The fingerprint matches the original baseline. The strict category checker
returned1: **not accepted**, including the single-hop baseline regression.
The small multi-hop gain does not offset regressions in other categories.

The evaluator wrote all rows and `summary.json` but its stdout pipe closed during
final flushing (`BrokenPipeError`), so the runner did not produce its normal
summary/footer or complete cleanup. This was not reported as a successful runner
exit. Results were rechecked directly from the complete rows and server log;
model usage was read from the isolated DB before removal. A recovered
`postcheck.json` records that distinction. Tests do not establish answer accuracy,
semantic task success or an unseen-data generalization claim.

Remaining retrieval work must improve multi-hop/open-domain evidence coverage,
recover temporal and single-hop regressions, and then pass the unchanged full
category gate. No claims of90% completion, production enablement, publication,
merge or issue closure are made. Temporary test clusters, completed worker trees,
and the owned benchmark services/binaries were removed; result rows and logs
remain for review.

## Continued retrieval work / 검색 개선 계속 진행

The failed comparison above is not task completion. A new development iteration
separates candidate retrieval from final selection. In the opt-in path, ordinary
memory lanes now retrieve a fixed50 candidates and retain up to150 fused rows
before recency/diversity selection when no partial reranker/decision filter is
active. Conversation retrieval retains50 candidates
before selecting five authorized, redacted excerpts. The caller's ten-memory,
five-conversation and character budgets are unchanged; default-off, history,
relationship and frozen-context contracts remain unchanged.

The conversation selector compares the text actually returned to the question,
with the original fusion score as the primary signal. Exact repeated passages
of at least six words receive a small penalty. Negation, numbers and qualifications
are preserved when deciding whether passages repeat. Vector-only matches are not
discarded for lacking a lexical match. All sources are re-read and authorized
before selection; this increases opt-in read/auth work.

Read-only diagnostics found all80 evidence references missing from the previous
limit50 run in active, embedded conversation sources. None was missing solely
because a returned source's excerpt was cut. All624 conversation sources in this
fixture already fit600 characters, so it cannot demonstrate the benefit of long
source windowing. Separately, ordinary semantic-diversity selection penalized
distinct activities and worsened all four categories; that approach was rejected.
The server's vector-distance cap is unset, so relative vector pruning is not the
cause of this run's failures. Pure vector ranking also regressed stronger lexical
cases and was not adopted.

The candidate-width-only and combined candidate-width/conversation-selection
comparisons use the unchanged evaluator. Focused Go checks passed (0.104s); neither those checks nor
partial development scores establish the90% acceptance target. The first new
PostgreSQL check failed during schema initialization on a temporary SQL_ASCII
cluster; it is retained as an environment failure. The same focused selection
then passed on a new UTF8 cluster:53 top-level tests, zero skips,1.996s.
Independent review found that enlarging the fused tail while an existing reranker
only scores its head can promote unscored results above evaluated ones. Those
reranker/prune/dedup combinations now retain their original fusion budget.
A regression actually runs the local reranker against fifty candidates and
checks that its unscored tail cannot displace the evaluated head; the combined
reranker-boundary selection passed (0.351s). Existing partial-rerank policies
were not redesigned or represented as newly validated full-pool reranking.
No production enablement or90% claim is made.

The candidate-width-only comparison has now completed normally:453 questions,
906 successful HTTP searches, zero degraded results, zero generation-trap hits
and zero final non-embedding usage. Its fixed category gate failed: multi-hop
61.53%, temporal87.84%, open-domain63.02%, single-hop97.07%. The first and third
categories improved over the original baseline and single-hop tied it, but
the temporal regression prevents adoption. Observed concurrent-run latency was
p50 6.686s and p95 11.289s; these are development-host observations, not a
production performance claim. Its owned services were stopped after postchecks.

The combined width/conversation-selection run also completed normally with all
906 searches, no degraded results and no generation/non-embedding calls. Results
were60.97% multi-hop,87.84% temporal,63.02% open-domain and97.07% single-hop; its
strict gate failed. The selection change lost one additional fraction of a
multi-hop evidence set relative to width alone and was not accepted as a quality
gain. Its observed concurrent-run p50/p95 were7.025s/11.094s. The owned runtime
was cleaned up after the runner's normal postchecks and an independent log check.

The next isolated comparison changed query expansion only: ordinary questions
no longer generate a second vector query by deleting question scaffolding.
FTS already normalizes those words; deleting them from a vector input can lose
answer type, conditions or negation. Only an explicit adjacent name conjunction
is separated, with the original question words, conditions, tense, negation,
punctuation and remaining whitespace preserved. This is an intent-preservation
change; its measured result below does not meet acceptance. Original queries still run, and
all candidate/output/character budgets remain unchanged. Regression checks
verify one original embedding for ordinary/default-off searches and two extra
embeddings only for an explicit pair. The new PostgreSQL/related selection
passed61 top-level tests without skips (1.345s). The full comparison completed
normally on a freshly restored isolated server:453 questions,906 successful
searches, zero degraded results, zero generation-trap hits and zero non-embedding
usage before/after. Source hashes match the committed implementation. Results
were60.38% multi-hop,90.33% temporal,63.28% open-domain and97.49% single-hop
(overall86.03%). The strict gate still fails: multi-hop and open-domain remain
below90%, and temporal remains below its91.09% baseline. Observed concurrent-run
p50/p95 were4.937s/8.238s. The owned runtime was stopped and its four ports
confirmed closed after independent log/source/row checks. These development-host
latencies are not a controlled production performance comparison.

A separate local FP32 cross-encoder shadow experiment scored all453 questions
against fixed vector-only pools of50 memories and50 conversation sources:
45,300 pairs, no input truncation or fallback,949.56s, about1.14GiB peak RSS.
Final10+5 and existing character budgets were retained. Compared with the same
vector-only selection, multi-hop moved60.24% to62.52%, temporal91.48% to92.62%,
open-domain60.42% to55.73%, and single-hop95.82% to97.49%. The open-domain loss
rejects this model as a solution; its86.38% overall development average is not
an acceptance result. This diagnostic did not execute the service's HTTP
authorization and provenance-revalidation path and does not replace that test.

A second cached local cross-encoder runtime was tested with external networking
disabled. Its first configuration rejected an invalid token budget; the next
8192-token warmup exceeded the container's4GiB limit. A bounded512-token runtime
then started, with client preflight ensuring no actual input needed truncation.
The pilot was stopped for runtime cost after58 completed pairs across15 requests
(one additional request was in flight): the early32-pair throughput projected
roughly24 hours for the full comparison, beyond the one-hour diagnostic budget.
No full questions or gold-quality evaluation completed for this pilot. This is
a runtime-speed rejection in this environment, not evidence of model quality.
The owned container and isolated network were removed; existing caches remain.

The second model was also checked through its pinned, inspected local CPU
implementation with the existing cached weights. No model weights or private
text were transmitted. An FP32 eager batch-one pilot completed300 pairs without
errors:15.37–17.77s per100-pair question, projecting2.04 hours before reserve.
A one-off batch-eight/four-thread pilot preserved all lane rankings (maximum
raw-score difference0.0000012) but slowed to32.89–37.75s per question, so that
variant was rejected. Neither pilot was evaluated against gold evidence.
The three-shard comparison completed all453 questions using the original
batch-one path, a shared6GiB memory limit and a600% CPU quota. The first attempt
stopped at its original deadline after3852.98s with277 complete questions and
143 additional logged pairs from incomplete questions. Its timeout failure
records were preserved. A continuation completed only the remaining176 questions
in2220.69s. A separate preparation failure created no workers and performed zero
inference; its record is also preserved. The combined result retains the first
completed result for every question and verifies all input/result hashes, order,
uniqueness and complete100-pair journals before gold evaluation.

There are45,300 unique scored pairs and45,443 logged forwards including the143
discarded partial pairs. All906 final lanes were scored without fallback or
truncation; no OOM or memory-limit event occurred. The original300 identity pairs
match the pilot exactly. Continuation processes verify pinned files and dependency
versions and inherit that original validation; they did not repeat raw-score
identity inference. Both inference scopes exited and their owned processes were
removed. The two scoring attempts used101.23 minutes of wall time in total,
excluding preparation and the gap between attempts. Observed per-question scoring
latency was39.05s p50 and57.74s p95 on the shared host, not service HTTP latency.

The fixed offline result was64.88% multi-hop,98.18% temporal,58.33% open-domain,
97.49% single-hop and88.12% overall. The same vector-only baseline matches the
previous shadow experiment exactly. Its open-domain60.42% falls to58.33%, so
this model was rejected despite temporal gains. Every category stays within the
independently computed50+50 candidate-inclusion bound. This remains a development
diagnostic: it did not execute the actual service's authorization/provenance path,
does not replace the full HTTP comparison, and does not meet the all-category90%
target. No model adoption or category-specific policy tuning was performed.

A read-only candidate-ceiling audit independently rebuilt the vector rankings
from existing cached query and candidate vectors, without model, embedding,
database or external calls. It reproduced all453 questions,906 lane orders and
45,300 candidate ID/text hashes in1.584s with one CPU thread and about290MiB RSS.
The100+100 candidate lists were frozen from vectors before reading gold evidence.
Their union-coverage ceilings are98.7455% multi-hop,100% temporal,95.3125%
open-domain and99.5816% single-hop. The existing50+50 open-domain ceiling is
88.28125% across32 scored questions, so reranking that pool alone cannot meet90%.
With100+100, fully covered open-domain questions increase25 to28 and covered
references58/68 to63/68. These are oracle candidate-inclusion bounds under the frozen harness mapping,
including its approximate memory-provenance mapping, not achieved ranking quality: final10+5 selection and the6000-character memory budget remain
required. The then-running model experiment's candidates, protocol and scores were
not changed or evaluated by this audit. Its50+50 pool remains a ranking diagnostic,
not a viable all-category acceptance candidate on its own.

A separate cross-lane audit did not justify broad duplicate suppression.
Only66 of2,265 baseline conversation entries (2.91%) had all their mapped turns
already represented in selected memories; no question had all five entries in
that condition. Mapped-turn overlap is not proof of repeated facts because the
harness uses an approximate token-overlap memory mapping and one turn can contain
additional information. Stored-body equality, direct source-ID equality and
cross-lane cosine similarity at least0.92 were all absent. Conversation entries
provided additional gold evidence for70 questions. Removing entries from sessions
already represented by memories lost evidence for50 questions in a deletion-only,
no-refill sensitivity check; this is not a measured replacement-selection policy.
Referenced source originals are absent from the export, and original returned
response bodies were not retained, so complete provenance and returned-text
redundancy could not be established. No suppression change was adopted; the
then-running model scores and protocol were not read or changed by this diagnostic.

Post-review validation of the committed candidate and partial-rerank guard passed
58 top-level PostgreSQL/related checks with no skips (4.128s), scoped vet and work
record validation. Further retrieval work remains; none of these passed
engineering checks establishes the still-unmet category target.

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

MQ1/MQ2 implementation is present, but the final category-level retrieval acceptance
failed and those quality objectives remain incomplete. Implementation and unit-test completion never substitute for the
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

The opt-in eligibility guard also excludes history, relation lookups and frozen
context views from the new exact/duplicate-FTS credits and side-lane weighting.
This matches the documented expansion boundary. The final combined real-DB
retrieval, context, option and legacy-expansion regression passed (5.569s).
The plain enabled recall path used by the frozen comparison remains unchanged.

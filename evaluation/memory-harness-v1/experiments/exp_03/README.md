# EXP-03 paired-history analyzer — implementation in progress

The analyzer reads explicitly supplied files and writes a new JSON comparison
table. It does not call a model, query a database, find datasets automatically,
change the memory engine, or approve an experiment. No product UI is added.

Implemented computations:

- Match the complete planned policy × query × repetition schedule. Missing
  observations, failures, truncation and fallback remain in the denominators.
- Validate exact Ref identities using the unchanged frozen v1 Ref validator.
  Duplicate refs, changed input/gold/budget bindings and pre-packing selections
  are rejected. Alternative gold sets are scored separately, never unioned.
- Average valid paired repetitions within each query, then queries within each
  history, then histories equally. The query-weighted diagnostic is separate.
  Repetitions are never independent histories or extra clusters.
- Bootstrap whole preassigned clusters. A cluster containing several histories
  retains them together; the point estimate still weights histories equally.
- Calculate a t approximation from cluster residual sums for that same
  history-macro estimand, with cluster count minus one degrees of freedom.
  This equals the ordinary paired t calculation for one history per cluster.
  Unequal cluster sizes use a cluster-robust approximation and require statistical
  review; this is not an exact small-cluster guarantee.
- Keep one predeclared primary contrast and apply Holm to the declared secondary
  family. An incomplete secondary family cannot produce a positive verdict.
- Report missingness limits, paired exclusions, [0,1] bounds as sensitivity
  bounds (not imputed observations), and truncation-exclusion sensitivity.
- Separate observed, estimated and missing costs by policy, phase, resource,
  unit and allocation. Shared and whole-run nonadditive measurements are
  separate rows, not charges to add again. A missing or estimated charge never
  becomes a complete observed total, including an estimated value of zero.
  Multiple nonadditive or peak-memory observations retain their individual
  measurement IDs and values; the analyzer does not invent a max/mean or sum.
- Bind input bytes, source bytes and generated table data with hashes. Error
  messages and provenance do not copy input paths, prompts or source bodies.

## Explicit inputs

All four protocols are new sidecars. The frozen baseline, existing experiment
templates, gold and accepted codecs are unchanged.

`exp03-analysis-plan-v1` is a **computational** seal. Its hash covers every plan
field except `plan_sha256`. It explicitly selects population, policies, primary
metric, current/as-of query modes, history/cluster mapping, repetitions and
minimum valid repetitions, primary/secondary contrasts, bootstrap seed/count/
confidence/percentile definition, practical difference, optional equivalence
margin, missingness limits, truncation rule, grader hash, cost requirements,
interim looks and tuning trials. No research values are defaulted or inferred
from prose. Synthetic example choices do not constitute owner decisions.

`exp03-schedule-v1` binds that seal and lists each query's history, cluster,
scope, kind, request mode, alternative gold sets, budget and input hash. Token
caps can be explicitly unknown (`null`); they are not replaced with zero or a
made-up value. An unobserved budget withholds an inference verdict.

`exp03-observations-v1` binds the schedule hash and requires
`selection_stage=post_packing_final_fence`. Each row binds policy/query/history/
repetition, input/gold/budget hashes and the final refs, execution status,
optional supplied grader correctness, fallback flag and grader hash. A failed
checker/API/timeout is not a correctness observation. The fixed planned
truncation rule can score truncation as zero for the primary calculation while
its exclusion sensitivity remains separate.

`exp03-cost-ledger-v1` binds the schedule hash. Each measurement has a unique ID,
job/policy binding (or an explicitly shared/nonadditive allocation), phase,
resource/unit, observed/estimated/missing basis, exact decimal value or missing
reason, source hash and a dated price source for currency. The plan's required
cost measurements must exist, including explicit unknowns for failed jobs.
Cost coverage is coverage of declared requirements, not proof that unmeasured
resources are free.

Exact field checks are in `analyze_paired.py`; its tests build complete small
inputs from `hand_examples.json`. The hand-calculated quality/CI/t constants
are stored separately from the implementation functions.

## Run

```sh
python3 -B -m unittest discover \
  -s evaluation/memory-harness-v1/experiments/exp_03 -p 'test_*.py' -v

python3 -B evaluation/memory-harness-v1/experiments/exp_03/analyze_paired.py \
  --plan PLAN.json --schedule SCHEDULE.json \
  --observations OBSERVATIONS.json --ledger LEDGER.json --out NEW_ANALYSIS.json
```

The CLI requires every flag once, refuses abbreviation and overwriting output,
and reads bounded regular files. `evaluation/test_memory_harness.py` also
collects the tests in the required evaluation suite.

## Attribution-view sensitivity on the same frozen trace

Optional `--attribution-views VIEWS.json --attribution-labels LABELS.json` must
be supplied together. These are separate sidecars, not additions to frozen DTOs.
The views file (`exp03-attribution-views-v1`) seals the original analysis-plan,
schedule, observations and ledger hashes, an explicit reference view, two to
eight view IDs and criterion/evaluator hashes, and declared label timing. The
labels file (`exp03-attribution-labels-v1`) binds that seal and supplies every
planned query and policy/repetition identity for every view. Missing labels
remain explicit unknowns. Criterion hashes and declared timing are computational
bindings, not proof of preregistration, truthful attribution or owner acceptance.

For recall, the caller supplies each view's query kind and alternative gold sets.
For supplied grader accuracy, query populations/gold stay fixed and the caller
supplies correctness labels. Failed or absent executions cannot acquire a success
label. A missing required label makes the whole view uncomputable, rather than
selecting a favorable subset. Input hashes, scopes, returned refs, execution
status, fallback, budget, history/cluster IDs and all cost measurements remain
unchanged. No policy is rerun and no update trajectory is simulated.

Each complete view produces the original paired contrasts and uncertainty.
If query kinds/populations match, the change of each contrast is also bootstrapped
jointly within the same preassigned clusters, preserving history pairs. If kinds
change, each view keeps its own table but the cross-view paired change is withheld.
No additional cross-view hypothesis test or automatic policy adoption is created.

All-policy rank uncertainty uses common valid repetitions across all policies,
then query means, equal-history means and whole-cluster resampling. An eligible
query with insufficient common repetitions withholds the entire rank calculation;
unknown budgets and insufficient clusters also withhold it. Exact ties retain the
best-to-worst tied rank rather than choosing a lexical winner. Rank percentile
ranges and bootstrap order frequencies are descriptive, not posterior policy
probabilities or a new confirmatory test family. Original exploratory stages and
post-result label definitions remain marked exploratory.

The implementation and tests are `attribution_views.py` and
`test_attribution_views.py`. Independent examples cover a recall difference change
of -0.125 with joint interval [-0.25, 0], an accuracy order reversal with constant
change -2, and tied policies retaining rank range [1, 2]. These are synthetic hand
examples, not measured product performance.

## Native execution verifier

The producer CLI now has a read-only `verify` command with the same four flags
as `run`: select exactly one of `--manifest` / `--p1-manifest`, plus `--config`,
`--registry` and `--out` (the existing result directory). Unlike `run`, verification
never publishes or changes files. It emits a separate
`exp03-native-verification-v1` report, not a change to any frozen native DTO.

The verifier reuses the accepted Go strict codec, original bundle loader and
actual `RunPolicies` / `RunPoliciesP1` implementation. It reruns the deterministic
offline policy and compares every recorded prediction, decision, failure and
cost field with recomputation. Correctly resealing a wrong selected-ref result
does not pass. Raw input, base-wrapper, config, registry, completion and prediction
identities remain distinct from typed semantic hashes. Input graph pins, a closing
reload and recorded-result recheck detect changes during the read. Source bodies
and absolute input paths are not exported. The report is limited to 64 MiB.

The shared reader now opens Unix files nonblocking with a no-follow leaf flag,
then preserves its opened-type and inode checks. A FIFO swapped after a regular
file check cannot block before type validation. Non-Unix behavior retains the
existing guarded reader; Windows/macOS test executables were cross-compiled,
which does not establish runtime validation on those platforms.

Go tests exercise the actual command path in process, including a real two-memory
lookup/packing cost, P1 and plain modes, P1 feature/base tampering, correctly
resealed wrong selections, failed rows, raw corruption, output failure, symlink
rejection and an actual Unix FIFO. No standalone helper or product binary was
built or installed. The verifier is producer-side DYN05 code consumed by EXP03;
its single-writer handoff and paths are recorded explicitly.

This report verifies a supplied **offline synthetic** execution. Its product
authority and scientific-lock flags are false. A saved self-declared report is
not a trusted executor. The formal adapter must invoke/bind the actual verifier
and reconcile all EXP02 planned jobs, attempts, gold, scopes, budgets and costs.

## Formal EXP02 projection

`project_native.py` reads the actual closed EXP02 `RunPlan` and `RunIndex`, an
EXP03 analysis plan/schedule and an explicit `exp03-native-projection-plan-v1`.
All ten CLI arguments are required: `--analysis-plan`, `--schedule`, `--run-plan`,
`--run-index`, `--projection-plan`, `--input-root`, `--evidence-root`,
`--output-root`, `--native-cli`, and `--out` (a new directory).

The projection plan binds the raw plan/index, analysis seal/schedule, verifier
executable hash, shared verification deadline, source GoldRow/QueryPlan artifacts,
cost evidence roots and every job/repetition/ordered-attempt slot. Retry choice
is required explicitly: `first_completed_else_last_attempted` or `last_attempted`.
There is no default. Owner choice for real runs is pending; synthetic tests cover
both. Post-result projection requires the original analysis plan's exploratory
change marker. This computational seal is not proof of preregistration.

The original EXP01 QueryPlan and GoldRow identities must agree on clock, scope,
time, planned cutoff and requested historical-as-of time. A separate
`exp03-analysis-source-v1` commits both raw files. Each job's
`exp03-native-materialization-v1` binds that source, native input and the exact
source query/gold semantic hashes to native cutoff and candidate-view hashes.
It explicitly maps planned sequence to native accepted sequence; they are not
assumed equal. Clocks either match or declare an identity-ns synthetic rename.
This verifies producer mapping provenance, not an independent implementation or
approval of the materialization algorithm. Alternative-answer gold stays outside
the primary population and receives separate recall diagnostics.

The budget-rule artifact is `exp03-native-budget-rule-v1`: every planned query
supplies final-k, nullable input/output token caps and the native context budget.
Fixture units are never converted to made-up tokens. Repetitions of the same
query retain the same registry, candidate view, budget, rules and frozen
event/importance-file hashes. Unrelated probe batching does not need identical
whole-file bytes. Planning/execution dispatches and supplied failure traces are
hash-checked; arbitrary recorded argv is never executed.

For completed attempts the caller-selected regular executable is hash-pinned,
copied to a private executable snapshot and called through the actual `verify`
command. No saved success report is accepted in its place. The original report
bytes are retained as separately hashed JSON files. Native probes, predictions,
completion and stdout identities/counts are reconciled. All planned slots,
including unrun/failed attempts and extra unexecuted policies, remain represented.
Entirely unrun inputs need no installed verifier. Source/code/input rechecks and
new-output-only publication prevent silently mixing changing snapshots.

Supplied grader evidence uses `exp03-grader-observations-v1`, bound to plan,
schedule, exact attempt/policy/query/final-refs and grader hash. It does not prove
checker truth or actual model input delivery. Without a success grade, native
retrieval success becomes a checker failure for the supplied-accuracy metric,
never a guessed correctness label. Retrieval recall remains a separate metric.

Every recorded native attempt cost is retained in
`exp03-native-execution-costs-v1`, with exact decimal totals, observed/estimated/
missing separation, nonadditive individual measurements and source/price hashes.
Missing declared measurements are listed. An empty ledger is not evidence that
unmeasured resources are free. Native batch costs are not divided among queries:
the v1 query-cost ledger explicitly retains unknown allocations. Costs from
failed/redundant attempts remain even when another attempt supplies primary
quality. The output contains analysis, observations, query ledger, native cost
ledger and original native verification reports; each file is hashed.

## Required remaining work — not a data hold

This implementation is **not full-card acceptance**. The formal pilot projection
and actual native replay connection now exist; they do not open EXP02's still
closed execution-builder/owner-acceptance gates. Final protocol/owner-value and
trusted final-lock binding remain engineering work. Confirmatory analysis is
still explicitly refused until that path is implemented and the genuine final
lock/execution prerequisites are satisfied. A self-declared seal/stage/hash is
insufficient. Downstream EXP04 must consume the new verified source closure and
native batch-cost coverage rather than its current unconditional missing gate.

Formal protocol/owner-value binding and the native source/attribution-label
evidence adapter remain required. Attribution-view computations are implemented
for explicit sidecar labels, but do not certify the labels or their source traces.
The separate current/as-of query-mode strata are not attribution views. The actual scientific
lock and result interpretation require the retained owner decisions, sufficient
actual outcome data and researcher review; they are separate from code work.

A directionally positive interval is not automatically a practical improvement.
An interval containing zero is not equivalence. An explicitly supplied
equivalence margin permits the descriptive `equivalence_interval_contained`
criterion; it does not authorize an equivalence claim, secondary-family
equivalence testing, product adoption or a causal/user-experience conclusion.
Small cluster samples and degenerate intervals are not general guarantees.

Numerical reference checks: [NIST t critical values](https://www.itl.nist.gov/div898/handbook/eda/section3/eda3672.htm),
[R's t distribution definition](https://www.stat.ethz.ch/R-manual/R-devel/library/stats/html/TDist.html).

Actual participant recruitment, collection, live provider calls, operating DB
access, product builds, deployment and scientific approval are outside this
analyzer execution.

`locked_analysis.py` adds a file-only `exp03-locked-analysis-binding-v1` preflight. Supply `--lock`, `--values`, `--manifest`, `--events`, `--queries`, `--gold`, `--analysis-plan`, `--schedule`, `--cluster-assignments`, `--analysis-source`, `--analysis-timing` (`before_results` or `after_results`) and `--out` (a new absolute file). Optional `--verify-binding` recomputes an existing binding against all exact source bytes. Cluster input protocol `exp03-final-cluster-assignments-v1` has `manifest_raw_sha256` and `assignments` of `{stream_id, cluster_id}` for every original manifest stream. No cluster may span dataset splits. Assignment and timing are supplied declarations, not attestations. The analysis-source file is the same `exp03-analysis-source-v1` queries/gold raw-hash pair consumed by the native projector. The preflight checks all original final-test questions in selected request modes, including diagnostic/alternative questions; it does not silently flatten alternative gold into the primary cohort. Scientific approval and execution permission remain false. Confirmatory projector/analyzer integration and human semantic/label review still remain; a computational binding alone is insufficient.

The formal native projector has optional paired `--final-inputs FILE --trusted-owner-review-sha256 SHA` arguments. A final-input document uses `exp03-final-inputs-v1`, `files` containing ArtifactRefs for all locked-analysis input names, and `gold_review` / `owner_review` ArtifactRefs. Lock uses `owner_contract`, other data files use `source` under the input root; independent gold review uses `acceptance` and owner declaration uses `owner_contract` under the evidence root. The expected owner-review hash is supplied separately, never derived from its payload. The review schema `exp03-owner-analysis-review-v1` binds computational binding, analysis/schedule/run-plan/projection-rule and independent GoldReviewRecord hashes. Result-index and post-execution grade artifact fields are excluded from the predeclared rule hash. Reviewer/person identity and timing remain declarations. Actual native replay is mandatory before the projector invokes internal confirmatory computation; the public standalone analyzer cannot do so. REF selection records are hash-read but the REF selector/verifier and derived alias remain required engineering: registered REF analyses reject until that implementation exists. The full final-source/materializer/native/confirmatory end-to-end case is still unexecuted. This paired-input code must not be reported as whole EXP03 or scientific acceptance.

REF selection implementation: `ref_selection.py --request FILE --input-root DIR --evidence-root DIR --output-root DIR --native-cli PROGRAM --out NEW_DIR` builds a new `selection.json`. The request protocol is `exp01-ref-selection-request-v2`, with `files` (the locked-analysis ArtifactRefs), a `rule` root/artifact pair and `validation` root/artifact pairs for `analysis_plan`, `schedule`, `run_plan`, `run_index`, `projection_plan`. Analysis/schedule/index/projection use role `source`, run plan uses `plan`, the rule uses `owner_contract`. The result is `exp01-ref-selection-v2` with `expected`; final job bindings use the existing `REF_selection` role under the evidence root. The production path requires all six current non-B2 comparator candidates, replays native validation before recomputing exact macro scores/ties and retains the complete native input graph and report bytes. Rule fields are `protocol` (`exp01-ref-selection-rule-v2`), `protocol_lock_raw_sha256`, `manifest_raw_sha256`, `validation_analysis_plan_sha256`, `eligible_policies`, `request_modes`, `tie_tolerance`, `token_cost_phases`, `id_tie_break`, `id_order`. All recorded/required candidate or shared token phases must be covered; unknown allocations never become free totals. Treatment-only costs remain in the original ledger and are not charged to comparator candidates. The derived REF row shares its selected native execution and grade; it is not a new sample or model call. Actual source-materializer→native validation→REF CLI positive and complete final E2E remain required tests, not proved by the component cases.

The user selected first completed result for prospective primary analysis. Final and REF validation inputs enforce that policy; multiple completions need one comparable clock and choose earliest completion time, breaking exact ties by predeclared attempt order. No successful completion means retained failed-attempt metadata, not invented successful quality. All attempt costs remain recorded.

## 명시적 로컬 합성 작업과 실제 REF/최종 연결

`task_adapter.py`는 `exp03-literal-synthetic-task-v1` 명세를 받는다. scope는 `model_free_synthetic_fixture_not_study_quality`, 필수 항목은 `stage_namespace`, 원래 events/queries/gold/manifest ArtifactRefs인 `source_files`, 질문별 `tasks`다. 각 작업은 stream/query ID와 원래 질문 semantic SHA, reader의 `first_visible_body_literal_prefix`/prefix, checker의 `exact_answer_or_explicit_abstention`/accepted_answers를 명시한다. reader에 정답은 전달하지 않는다. 이는 명시적 합성 task이며 LLM 성과나 실제 연구 정답의 독립성을 대신하지 않는다.

새 `exp03-native-projection-plan-v2`는 미리 정한 task_contract ArtifactRef를 추가한다. 기존 v1 provided-grader/고정 DTO는 유지한다. grader identity는 adapter 코드·의존 코드·task 원문을 결속하며 잠금/최종 검토에 들어간다. task의 events/manifest까지 producer 및 final/REF 입력의 실제 원본과 같아야 한다. 실제 Go 재검산 후에만 채점하고, projection도 선택된 실제 본문으로 채점·토큰 출처를 다시 계산한다. 동적 코드 원본도 중첩 REF를 지나 최종 재확인하고 v2 provenance에 남긴다.

실행 인자는 `--analysis-plan --schedule --run-plan --run-index --projection-plan --task-contract --trusted-task-sha256 --input-root --evidence-root --output-root --native-cli --out`이며 확인실험 코드 경로는 `--final-inputs --trusted-owner-review-sha256`도 받는다. out은 output-root의 명세된 stage_namespace 새 디렉터리다. 모델 호출 없는 이 reader/checker의 모델 토큰만0으로 측정한다. 원래 pre-task index 원문을 보존하고, 허용된 해당 단계 비용 변경 이외의 원장 변경은 거부한다. 본문·정답 원문은 결과 보고서에 복사하지 않는다.

실제 합성 기술 검사에서는 원자료→native 원장→task CLI→REF CLI→최종 native/별도 owner pin→task CLI→확인실험 분석을 실행한다. 원자료 전체 사건은 유지하며 선택 모드의 질문은 전부 포함한다. REF는 기존 B0 결과의 alias이므로 추가 실행/독립 표본으로 세지 않는다. 한 과거모드 합성 성공은 실제 연구 효과·사람 인증·전체 카드 수용이 아니다. 기존 current/늦은 결과/철회 음성 검사는 그대로 남는다.


### Native-bound sensitivity

`project_native.py` optionally accepts both `--attribution-inputs` and `--trusted-attribution-sha256`. The latter is an independently supplied SHA of the exact envelope bytes. Protocol `exp03-native-attribution-inputs-v1` binds the source plan/schedule/native plan/index/projection and explicit root+ArtifactRef inputs for the sealed view definition, labels, and each view's criterion/evaluator/label source. Artifact roles are `source`; all source files are bounded and hash-read before helper work. Label-source protocol `exp03-attribution-label-source-v1` contains exact `view_id`, `queries` and `grades` matching the supplied view labels. No truth rule is inferred or executed by this adapter.

Fresh native execution evidence is mandatory; confirmatory sources also retain the existing full final/REF binding. The public standalone view analyzer continues to reject confirmatory self-declarations. New sensitivity is always exploratory, including a caller's `before_results` declaration, because label timing is not authenticated. Source primary, REF and cost/denominator remain unchanged. Missing labels withhold the whole affected view. The output's `attribution_sensitivity` is computational evidence only; no additional policy execution or provider call occurs.

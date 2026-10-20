# Memory reuse benchmark v2 preregistration draft

**Status: DRAFT.** This document proposes a text contract for a future study.
Decisions below not marked DECIDED remain pending; no v2 protocol has been preregistered or
frozen, and no v2 runner, schema or validator is supplied by this document.
Publishing it does not authorize experiments, model calls or human recruitment.
The scope is Arkism's own memory delivery and reuse in a coding workflow.

The [evaluation guide](../../guide/memory-evaluation.md) supplies the existing
measurement principles. The [v0.1.0 benchmark](../reuse-v010/benchmark/README.md)
remains a historical archive: its five tasks, three conditions, three repetitions
and 45 observations do not determine v2's sample, settings or held-out tasks.
Its fixtures, frozen spec, observations and approval basis remain unchanged.

## 1. Decisions required before registration

Each pending item needs a value, rationale and responsible person. Keep draft
status until decisions, artifacts and analysis are reviewed before outcomes.

| Decision | Status |
| --- | --- |
| Primary question, comparison and outcome; secondary comparisons | PENDING |
| Task families, scenarios, held-out split and independent sample size | PENDING |
| Repetitions, adapter directions, OS coverage and paired blocks | PENDING |
| Randomization algorithm, order, seed and counterbalancing | PENDING |
| Product, harness, fixture, adapter, judge and environment versions | PENDING |
| Model/provider identifiers, supported settings and embedding configuration | PENDING |
| Approved corpus, instruction revisions and changes/removal schedule | PENDING |
| Whole-task context/token/output/tool/repair/time budgets | PENDING |
| Currency, actual-charge evidence and whole-experiment spending/time limits | PENDING |
| Executor, real human reviewers, independent judges and analysis owners | PENDING |
| Meaningful effect, any noninferiority margin and uncertainty method | PENDING |
| Retry, exclusion, stopping, amendment and resumption rules | PENDING |
| Public/private evidence boundary ([evaluation guide](../../guide/memory-evaluation.md#2-freeze-the-experiment-before-collecting-results)) | DECIDED |
| Evidence retention and review consent | PENDING |

Record unavailable provider settings as unknown and unsupported caps as unenforced.
Historical settings and exploratory thresholds are not defaults for these decisions.

## 2. R: delivery of the same approved knowledge

| Condition | Treatment and required evidence |
| --- | --- |
| A: maintained static instructions | Load current approved facts through the adapter's native project instruction files. Record loaded bytes/revisions and human preparation and maintenance time. |
| B: automatic injection | Use the product's actual selection and injection path over the same approved corpus. Record delivered context, omissions, irrelevant or duplicate selections, budget cuts and selection reasons. |
| C: injection plus autonomous search | Add declared memory tools to B. The agent chooses whether and how to search. Record requests, successful or failed responses, returned evidence and context delivery. |

Within each paired block, hold the approved corpus, update opportunities, model,
starting repository, ordinary instructions, non-memory permissions, network
allowlist and whole-task budgets equal. Keep A maintained when rules change or
sources are removed; do not intentionally give it obsolete instructions.
C's additional memory-tool permission is an intentional treatment difference
and must appear explicitly in the condition permission table.

The equality concerns available approved facts. Do not force identical delivered
bytes by selecting the right rule for B or C on the host: missing or incorrect
selection is part of the treatment. A byte-identical delivery ablation may be
reported separately. Record any facts duplicated in static files and injection.

All tool results and extra model calls count toward the same whole-task budgets.
C attempts without a search, with an empty result or with a tool failure remain
in C. A tool request alone does not establish successful retrieval, application
of a rule, correct citation or improvement in the task outcome.

## 3. E and S: separate study axes

**E: capture to reuse.** Start from declared synthetic source material and follow
actual capture, candidate generation, real human review and fresh-agent reuse.
Record missed or incorrect candidates, pending reviews, rejection, correction,
abandonment and repeated review. Measure A's source reading and document upkeep
from the same starting material and update opportunities.

Link source/candidate IDs, revisions and hashes to a pseudonymous reviewer,
the reviewed revision, recorded decision reference, API actor role and subsequent
active-memory reuse. A scripted human-role request is not a person's decision.
Previously approved facts do not authorize new facts. E includes acquisition
and review failures; report it separately from R's approved-corpus comparison.

**S: skill execution.** Declare a separate static/manual comparison, skill
selection, approved version/body hash, execution permissions and sandbox,
execution receipt and task outcome. Finding a skill, executing it successfully
and benefiting from it require separate observations. Do not add executable
skills to R without registering that additional treatment.

## 4. Fixtures, isolation and independent judgment

Use new synthetic repositories and a separate v2 namespace and output location.
Keep development, pilot and held-out sets separated by repository/rule/task
family, including paraphrases. Do not tune extraction, search or thresholds on
held-out outcomes or relabel the historical tasks as a new held-out set.

Include declared scenarios for persistent rules, exceptions, changed rules,
removed evidence, irrelevant scopes, misleading context, duplicate instructions,
longer work and multiple repositories. Their counts and weighting remain pending.
Use clean checkouts, fresh sessions and isolated product profiles/databases per
declared cell; prevent cross-condition transcript, output and memory reuse.

Pair by protocol, product, fixture, model, adapter/direction, family, task and
replicate; randomize condition order using the registered schedule. Repetitions
within a family do not become independent task families. Order randomization
does not fix provider sampling or eliminate provider-cache confounding.

Prepare hidden executable checks, reference solutions, policy mutants and error
cases before execution. Keep answers and checks out of prompts and memory.
Use condition-blind judgment where possible; distinguish task correctness from
stale/forbidden rules, scope failures and source-citation correctness.
Contract tests are prerequisites; service tests alone do not certify adapters.

## 5. Protocol binding and every attempt

A future manifest must bind product/build, harness, adapter, judge, fixture and
approved-corpus versions/hashes, model settings, permissions, budgets, split,
planned cells and order, analysis, human evidence and amendments. Record its
reviewed version/hash and registration time before evaluation outcomes exist.

Define a cell separately from an attempt. Record an attempt before its first
effect, preserving phase, timestamps, protocol/cell identity, prompt/tool-event
references, exit status and partial artifacts. Retain setup failures, timeouts,
invalid answers, repair attempts, interrupted runs and policy blocks. A retry
gets a new attempt ID; it does not overwrite a failed attempt or add an
independent sample. Predeclare which attempt determines the cell-level outcome.

Resumption must validate the complete requested protocol and planned cell,
folder and attempt identities before writes, authorization, retrieval, judging
or model calls. Use the validated observation bytes for later analysis. These
are proposed v2 requirements; append-only and crash/concurrency recovery need
implementation and verification rather than a label in this document.

The existing [identity/resume gates](../reuse-v010/benchmark/run.py) bind the
historical experiment's identities and legacy spec. Its
[completion audit](../reuse-v010/benchmark/completion_audit.py) counts planned
result rows, including failed, timed-out and missing-outcome rows. Row coverage
does not establish valid outcomes, complete attempts, usage, billing, human
review or whole-experiment completion. Preserve those distinct denominators.

## 6. Costs, time and human effort

Record provider usage per request/turn, including failed calls and tool loops.
Cached input is a subset of input, not an additional input total. Distinguish
injected/tool context from observed provider usage. Incomplete usage requires
coverage counts and unknown totals instead of filling missing values with zero.

Keep actual provider-billed charges separate from tokens and price estimates.
Require currency, attempt/request mapping and private receipt references;
partial coverage cannot become a complete condition total. Subscription charges
without attributable evidence remain unknown/null. Preserve failure/retry costs.
The [measurement guide](../reuse-v010/benchmark/MEASUREMENT.ko.md) explains these
principles; its [auditor](../reuse-v010/benchmark/measurement_audit.py) currently
assumes a single completed usage event per observation.

Measure setup, capture/extraction, queue wait, static maintenance, injection,
retrieval/tool rounds, inference, repair, judging and whole-workflow elapsed
time as defined spans. The historical task timer excludes retrieval, setup and
review, so it cannot be relabeled as total workflow time.

The [review timer](../reuse-v010/benchmark/review_timer.py) records operator
start/stop wall-clock intervals, including interruptions. Active attention
remains unknown/null unless a separate method is registered. Preserve open,
rejected, corrected and abandoned reviews. Its existing condition names and
Linux storage do not supply v2 run/phase/document mappings or cross-OS support.

## 7. Analysis, stopping and evidence limits

Report scheduled, started, recorded-outcome, usage, billing and review coverage
separately, with missing reasons. Preserve failures in the declared denominator.
Predeclare infrastructure-failure treatment, retries and exclusions; retain any
excluded originals and their reasons. Never select the best retry afterward.

Predeclare the primary effect to estimate, paired/family uncertainty method, secondary
comparisons and thresholds. Report independent families separately from repeats.
Retain negative or inconclusive evidence; missing superiority is not equivalence.

Stop for privacy/approval/scope violations, hidden-check leakage, sandbox escape
or undeclared tool/model/network activity. Register numerical resource limits
and resumption authority before execution. Preserve stopped attempts and version
any amendment; do not stop early because interim results look favorable.

Publish only synthetic, reviewed artifacts and redacted evidence references.
Keep credentials, personal transcripts, reviewer identities and billing ledgers private.
Confidential competitive research is excluded from public study artifacts.
This draft involved no experiment or candidate review. Treat retrieval, service,
human-acquisition, skill and productivity claims separately; missing cost/review evidence cannot establish savings.

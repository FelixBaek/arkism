# Memory reuse benchmark v2 preregistration draft

**Status: DECISIONS SETTLED, NOT REGISTERED.** Every row of section 1 now has a value
(section 1.1, 2026-10-27, #365). Registration still needs the manifest of section 5 with its
hash recorded before any outcome exists; no v2 runner, schema or validator is supplied by
this document, and no v2 run has happened.
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
| Primary question, comparison and outcome; secondary comparisons | DECIDED (1.1 D1) |
| Task families, scenarios, held-out split and independent sample size | DECIDED (1.1 D2) |
| Repetitions, adapter directions, OS coverage and paired blocks | DECIDED (1.1 D3) |
| Randomization algorithm, order, seed and counterbalancing | DECIDED (1.1 D4) |
| Product, harness, fixture, adapter, judge and environment versions | DECIDED (1.1 D5) |
| Model/provider identifiers, supported settings and embedding configuration | DECIDED (1.1 D6) |
| Approved corpus, instruction revisions and changes/removal schedule | DECIDED (1.1 D7) |
| Whole-task context/token/output/tool/repair/time budgets | DECIDED (1.1 D8) |
| Currency, actual-charge evidence and whole-experiment spending/time limits | DECIDED (1.1 D9) |
| Executor, real human reviewers, independent judges and analysis owners | DECIDED (1.1 D10) |
| Meaningful effect, any noninferiority margin and uncertainty method | DECIDED (1.1 D11) |
| Retry, exclusion, stopping, amendment and resumption rules | DECIDED (1.1 D12) |
| Public/private evidence boundary ([evaluation guide](../../guide/memory-evaluation.md#2-freeze-the-experiment-before-collecting-results)) | DECIDED |
| Evidence retention and review consent | DECIDED (1.1 D13) |

Record unavailable provider settings as unknown and unsupported caps as unenforced.
Historical settings and exploratory thresholds are not defaults for these decisions.

### 1.1 Decisions (2026-10-27, owner: FelixBaek)

Each value's rationale cites only what this repository records. Where the repository
records nothing, the value is a choice and says so. Changing a value after
registration is an amendment under D12.

**D1 Primary question.** Does automatic injection (B) match or beat maintained static
instructions (A) on task correctness? Outcome: the cell-level pass rate of the hidden
executable checks (one pass/fail per attempt, as the v0.1.0 judge counted it). Comparison:
B − A, paired by block. Secondary, in this order: C − B on the same outcome; rule-violation
count per attempt; delivered context bytes per attempt; whole-workflow elapsed time.
Rationale: v0.1.0 reported static 15/15 and Arkism 15/15 against none 5/15
([report](../reuse-v010/benchmark/REPORT.ko.md)), so B versus A is the open question,
not memory versus none.

**D2 Families, split, sample.** The nine declared scenarios of section 4 are the task
families: persistent rule, exception, changed rule, removed evidence, irrelevant scope,
misleading context, duplicate instructions, longer work, multiple repositories (the
first five are the guide's minimum set, [guide §2](../../guide/memory-evaluation.md)).
Development set: 2 tasks per family, published with the fixtures. Held-out set: 2 new
tasks per family, kept under a `held-out` directory outside the repository
(`tools/check_public_push.py` rejects that name). Independent sample: 18 held-out tasks,
each one paired block. Development tasks are for pilots and tuning only and are never
pooled with the held-out result.

**D3 Repetitions, adapters, OS.** 3 repetitions per cell, as v0.1.0 ran
([benchmark README](../reuse-v010/benchmark/README.md)). Adapters: Claude Code and
Codex CLI, both directions, because those are the two hosts the capture hooks and the
2026-10-15 live run cover ([live-agents](../reuse-20261015/live-agents.md)). OS: Linux
only; the self-hosted runner is Linux and no adapter test exists for Windows or macOS.
Paired block = one held-out task × one adapter; each block holds all three conditions.
Planned attempts: 18 tasks × 2 adapters × 3 conditions × 3 repetitions = 324.

**D4 Randomization.** The full attempt list is shuffled once with Python's
`random.Random(seed).shuffle`, the v0.1.0 mechanism (`run.py`). The seed is the first
eight hex digits of the registered manifest's SHA-256, read as an integer, so nobody can
pick it. Condition order inside a block is whatever the shuffle gives; no further
counterbalancing. The shuffle fixes order only, not provider sampling.

**D5 Versions.** Product: the `main` commit the manifest names (full SHA). Harness: the
v2 runner's commit (same repository, so the same SHA). Fixtures: new synthetic
repositories, each pinned by commit in the manifest. Adapters: the Claude Code and Codex
CLI version strings printed at registration. Judge: hidden executable checks run in an
unprivileged, network-disabled Docker container as in v0.1.0, image pinned by digest.
Environment: the self-hosted Linux runner (`arkism-self` in `checks.yml`), `uname -a`
and Docker version recorded.

**D6 Models and embedding.** Coding-agent models: whatever the two CLIs report in their
own event logs; requested settings recorded, unavailable ones recorded as unknown.
Arkism's own harness roles: the preset in `config/harness-models.json`
(`arkism-runtime-v3-haiku-budget`: Sonnet for the consolidator, Haiku where the quality
gate allows). Embedding: the local TEI model and revision frozen in
`tools/evaluate_semantic_recall.py` (`microsoft/harrier-oss-v1-0.6b`,
`f9b9dc8d367d443f2479d27aa5d8d2850c0774ee`, 1024 dimensions, cutoff 0.38). Recall
budget: the host default in `internal/host/recall_budget.go`
(`user=2:1500,procedural=3:1500,lesson=2:1500,decision=2:1500`).

**D7 Corpus and schedule.** One approved synthetic fact per family (nine facts), approved
by the owner in writing before registration, as the five v0.1.0 facts were. Revision
schedule: the "changed rule" fact changes once, after the first repetition of its block;
the "removed evidence" fact's source is deleted after the first repetition; every other
fact stays fixed. A's static files are updated at the same moments by the executor.

**D8 Budgets.** Per attempt: 90 seconds wall clock (v0.1.0), output cap 2048 tokens where
the CLI supports it (unenforced otherwise, recorded), context budget = the recall budget
of D6 plus the adapter's own default, at most 10 memory-tool calls in C, 0 repair rounds
(as v0.1.0; a repair round would be a new treatment). Tool calls and extra model calls
count inside the same 90 seconds.

**D9 Money and time.** Currency USD, the unit `config/harness-quality.json` already
records. Evidence: the provider's billing export for the run window, kept private, with
request ids mapped to attempt ids; attempts without a matching line are reported as
unknown cost, never zero. Limits (choices, since the repository records no larger spend
than the probes' 0.25 USD): 100 USD provider charges and 30 operator hours for the whole
experiment, pilots included; reaching either stops the run under D12.

**D10 People.** Executor, human reviewer, analysis owner: FelixBaek, the repository's
only maintainer (the ruleset needs no second approval). Independent judge: none human;
the executable checks of D5 are the judge. This is a single-person study and the report
says so; reviewer agreement cannot be measured.

**D11 Effect and uncertainty.** Meaningful effect: B − A ≥ +3 passes out of 18 blocks
(the v0.1.0 pre-set criterion, three more successes out of fifteen, scaled to the new
denominator). Noninferiority margin for B against A: −1 pass out of 18. Uncertainty:
paired bootstrap over blocks, 10,000 resamples, 95 % percentile interval, reported with
the raw block table. No p-values.

**D12 Retry, exclusion, stopping, amendment, resumption.** An attempt that fails before
the model's first token (setup, sandbox, network) is retried once under a new attempt id;
the first attempt with a recorded outcome decides the cell. No other retries. Exclusions:
none after the fact; an attempt the executor stops for a section 7 violation is kept as
"stopped" and its block is reported without that cell. Stopping: any section 7 trigger or a
D9 limit stops the whole experiment. Amendments: a new numbered version of this file with
its hash; anything changed after the first outcome is marked post-hoc in the report.
Resumption: only the executor, only from a validated manifest, per section 5.

**D13 Retention and consent.** Private evidence (transcripts, usage, billing lines,
review timer intervals) is kept for 12 months under the owner-only storage rules of
`evaluation/reuse-v010/benchmark/private_storage.py` and then deleted; public artifacts
stay in the repository. Consent: the only person observed is the owner, who registers
this document; no other reviewer or participant is recruited.

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

# v0.1.0 real-model, host-assisted memory benchmark

Baseline: `9379ee5f493716e1c80027c0beb4fd12283100cf` (v0.1.0).
All benchmark code is evaluation scaffolding added after that release; the
production implementation under test is unchanged.

## What this actually runs

45 independent Codex CLI sessions: 5 synthetic Python tasks × 3 conditions ×
3 repetitions, ordered with `random.Random(20261015).shuffle` over the complete
45-run list. Model `gpt-6-astra`, reasoning `low`, no model tools requested,
90-second deadline, same starting module and task per paired condition. The
requested output cap of 2048 is not supported by this CLI (recorded warnings);
the cap is therefore unmeasured, with the same default in all conditions.
Provider temperature and seed are unavailable.

- `none`: ordinary task and starter; no historical project rule.
- `static`: the maintained project rule delivered as explicit static text.
- `arkism`: client A creates a source/candidate through real HTTP into a fresh
  Postgres database; a separate human-role token applies the user's explicit
  approval of these five facts; B searches through real HTTP with a distinct
  token. The host gives the returned text to the fresh Codex session. Source IDs,
  rule IDs, labels and full returned context are saved separately.

The user explicitly approved the five synthetic facts in the evaluation chat
before any model benchmark run. The machine submits this recorded decision.
No automatic human approval is claimed. Human writing/review time was not
instrumented and remains null. `setup_seconds` measures machine database setup,
HTTP collection and approval requests, not human preparation.

This is **actual Codex inference using retrieved memory, with host-assisted
retrieval**. It is not an actual agent A capture, autonomous Codex MCP lookup,
Claude↔Codex test, skill-use benchmark, or production desktop integration.
The benchmark uses an administrative Postgres connection; authorization is real
but RLS is not asserted here. The independent API suite covers those boundaries.

Static and Arkism model-visible rule text is byte-identical; only its delivery
source differs. This controls information content but deliberately omits Arkism
metadata from the model prompt. Thus it cannot measure advantages of source
inspection, retrieval choice, multi-rule ranking, or additional tool overhead.
The static baseline is an explicit document block, not native AGENTS.md loading.

The evaluator checks are frozen before execution and never passed to the model
or inserted in memory. They run generated Python in a fresh unprivileged,
network-disabled, read-only Docker container with no host mounts or credentials.
Rule violations are failed executable policy checks, counted once per run;
they are not a separate human rubric. A parse failure is also a failed run.
The task is code generation from a supplied module, not editing a checked-out
repository; repository-wide rule compliance and build integration are not
measured. There are no repair rounds, so rework count is zero by design, not evidence that
production work requires no rework. The no-memory condition cannot know arbitrary project policies; its failures
show missing information, not a defect in the model. Short tasks are exploratory and selected to
make arbitrary project facts matter. None measures long-repository productivity.

## Isolation and observation

Each session has a new temporary HOME and CODEX_HOME, containing only a private
copy of the existing Codex auth file. User config, hooks, skills, instructions,
production Arkism connectors and prior conversations are not copied. Codex uses
`--ignore-user-config --ignore-rules --ephemeral`, with project instruction
loading disabled. The model is instructed to use no tools. Check `events.json`
for any unexpected tool activity before interpreting the result.

Every C run uses its own database through `isolatedPostgres`; cleanup drops it.
The benchmark owns a separate loopback Docker Postgres server. No production
memory, code or sessions are read. Client A is a scripted authenticated HTTP
client, not a second language model. All fixtures and published model outputs
are synthetic. Fresh session IDs are omitted from saved model events.

Provider-reported input/output/cache tokens are retained. Provider prefix caching
was not disabled; fresh conversation isolation does not imply a cold provider
cache. Cached token counts are available per run, and shuffled condition order
reduces but does not eliminate timing/cache confounding. Provider actual cost,
human preparation/review/maintenance duration, temperature, seed and unsupported
fields are null. Task duration includes CLI startup/inference but excludes judge
execution and C retrieval/setup; C retrieval latency and machine setup are
separate. The fixed seed determines order, not model sampling. Model context
budgets/settings are equal, but observed input lengths vary because A lacks the
rule and provider behavior can differ. We do not pad with fabricated data.

## Reproduction

Prerequisites: Go, Python 3, Docker, authenticated Codex CLI supporting the flags
below. The observed adapters were Codex CLI 0.162.1 and Claude CLI 2.1.299;
Claude was not used in this benchmark. Existing auth is used only inside the
private profile. Never publish auth files or raw production settings.

```sh
git checkout 9379ee5f493716e1c80027c0beb4fd12283100cf
# Apply the evaluation-only files from the PR to this clean checkout.
docker pull python:3.12-alpine
docker run -d --name arkism-reuse-bench-test \
  -e POSTGRES_PASSWORD=synthetic-benchmark-only -e POSTGRES_DB=arkism_bench \
  -p 127.0.0.1::5432 pgvector/pgvector:pg17
docker port arkism-reuse-bench-test 5432
install -d -m 700 "$HOME/.local/state/arkism-benchmark"
go test -c ./internal/memory -o "$HOME/.local/state/arkism-benchmark/exporter.test"
# Replace PORT with the loopback port printed above. Password is disposable.
POSTGRES_TEST_DSN='postgres://postgres:synthetic-benchmark-only@127.0.0.1:PORT/arkism_bench?sslmode=disable' \
  python3 evaluation/reuse-v010/benchmark/run.py \
  --out "$HOME/.local/state/arkism-benchmark/results" \
  --exporter "$HOME/.local/state/arkism-benchmark/exporter.test" --approval explicit-human-approved
# After completion:
docker rm -f arkism-reuse-bench-test
rm "$HOME/.local/state/arkism-benchmark/exporter.test"
```

The exporter only accepts the exact five user-approved synthetic facts. Reusing
this harness with other facts requires a new human review; do not relabel a
synthetic role as real human approval. Results are incremental and resumable;
the reproduction output is restricted to `$HOME/.local/state/arkism-benchmark/results` (or the published results directory). Move your own prior reproduction results aside before starting a new experiment. Authentication failure or HTTP
fixture failure aborts rather than substituting simulated data.

`results/frozen-spec.json` includes task facts, starting code, hidden checks and
predeclared improvement threshold. Do not show that whole file to an agent;
`run.py` exposes only the task/starter and condition-appropriate fact. Per-run
prompts, generated modules, raw model events, judge results, retrieval responses,
CSV/JSON rows and aggregate summaries are in `results/`.

Initial setup interruption: the exporter tried to create a user-authored source
with an agent token and correctly received HTTP 403. The fixture was corrected
to collect an untrusted source before human review. The first two completed
model runs were retained; no model answer/evaluator was changed. The same
shuffled schedule resumed at the blocked C run. This is a harness correction,
not a product defect. `setup-interruption.log` preserves the failed attempt.

Evaluator sanity check: `python3 evaluation/reuse-v010/benchmark/validate_judges.py`
accepted five reference modules and rejected five policy mutants; see
`evaluator-validation.json`. These are evaluator checks, not additional model runs.

For byte-identical containers, use the `judge_repo_digest` and
`postgres_repo_digest` values in `environment.json`; the tags above resolved to
those digests in this run. Provider-side model updates cannot be pinned beyond
the requested model identifier.

An independent agent reviewed the harness during execution: no hidden evaluator
checks were present in prompts, static/C content matched, all observed CLI exits
were zero, and no tool-use events appeared. Three robustness changes were added
for future failed runs: a nonzero CLI exit cannot pass; judge timeout is distinct
from model timeout; incomplete token observations produce a null aggregate plus
coverage counts. The running model process had already loaded its original
harness, so these changes did not alter prompts, task ordering, checks or
observed outcomes. Completed-run exit/timeout/token coverage is audited afterward.

After data collection, the harness was split into helpers and its CLI output/exporter paths were restricted for static-analysis review. The original seed-generated 45-run permutation is now replayed explicitly. All saved prompts, ordering, observed results and summaries were checked unchanged; these maintenance changes did not trigger another model run.


## Offline result integrity

Before aggregation or measurement auditing, observations must have unique
`(task, condition, replicate)` cells and a consistent `experiment_id`, `commit`,
`fixture_sha256` and `model`. Duplicate cells and mixed experiment identities are
rejected before replacing aggregate outputs. Cell and execution-identity fields
must be present with valid, nonblank values; missing or malformed identity is a
validation error. Identical prompts alone do not make observations from different
experiments comparable. The auditor validates the complete result set before
reading token or prompt evidence, and uses the same result and event bytes for
observation checks and evidence hashes.

This validation also applies to partial result sets, while a partial audit
remains supported. Passing it does not prove that every planned run completed,
or that billing and human review time were measured. Those are separate
requirements for a future benchmark protocol.

Before resuming, the runner also checks every saved result, including unselected
conditions, against the current experiment, baseline commit, task-fixture hash
and model. Each result must belong to its exact scheduled folder. It validates
the complete legacy frozen spec before any run or output-file write and leaves
matching spec bytes intact. Results without their spec are rejected. A matching
partial result set remains resumable: only missing selected cells run, and the
validated rows are reused for aggregation. Key order and whitespace in the spec
may differ; JSON value types must match. No model or experiment fields are
retroactively inserted into legacy specs.

This resume gate does not freeze a complete v2 protocol, recover attempts
without results, or provide concurrent-writer or global artifact transactions.
Adapter/judge/environment provenance and a complete protocol manifest remain
separate work.

The offline regression rechecks the archived 45 unique observations and their
15 static/Arkism prompt pairs without new model calls. The original recorded
results remain unchanged. Synthetic duplicates and one-field identity changes
exercise the rejected paths; they do not establish an error in the archived
measurements.

## Planned result-row coverage

Inspect scheduled, recorded and missing cells, including counts per condition:

```sh
python3 -B evaluation/reuse-v010/benchmark/completion_audit.py \
  --out evaluation/reuse-v010/benchmark/results --require-complete
```

This reads the existing frozen spec and reuses the current identity, folder and
ordinal gates. It writes no result or aggregate files. Without
`--require-complete`, valid partial coverage is reported with exit 0; with it,
missing result rows return exit 1. Invalid inputs return exit 2 before a JSON
report. A matching plan with no rows reports 0/45 and `coverage_complete: false`;
a missing plan is rejected rather than created.

`coverage_complete` means all planned result rows are recorded, including failed,
timed-out and missing-outcome rows accepted by the identity gates. It does not
validate outcomes, usage, attempts, provenance, billing, human review or the
whole experiment. Existing partial measurement auditing remains unchanged.

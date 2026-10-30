# Roadmap file ownership

The [ownership manifest](ownership.json) is the editing and handoff boundary for
issues [#150–158](https://github.com/FelixBaek/arkism/issues/150). It contains 63
development cards. Paper-writing cards are outside that implementation count;
an external review prerequisite is retained where later acceptance needs it.

The manifest records editing boundaries and current local progress. A
`verified` card has local acceptance evidence linked from the [progress
record](progress.md); it does not imply PR merge, product integration or research
acceptance. The preparation checker validates ownership and the pinned source,
not the truth of a status field. Private hypotheses, participant material and
source ledgers remain outside the public checkout. R0 owns the public progress
record and work-record directory so documentation has one continuing writer.

## Before assigning work

1. Record the current source commit and check existing code and active Claude
   work. An old proposed filename does not establish that a feature is absent.
2. Resolve each logical root in a private dispatch record. `repo` is the current
   checkout. The four external-private roots have no public home-directory
   binding. Their resolved directories must be ordinary, mutually disjoint and
   outside the repository; a binding cannot change while work holds its files.
3. Assign one agent to an explicit list of files. Record task ID, source commit,
   root bindings and accepted predecessor evidence. Resolve aliases before
   checking physical path overlap. Two agents cannot edit one file just because
   their cards or root aliases differ.
4. Give the worker its `owns` paths. `consumes` is read-only. `shared_updates`
   requests an edit from the named shared-file owner; it is not another write
   permission. An unlisted file requires an ownership update before editing.

The integration owner serializes changes to existing product files and their
tests. It rechecks current code, approval, scope, revision and deletion behavior
before applying a card's request. This plan never grants permission to modify a
different Claude checkout, publish, deploy, call a paid model or start a study.

## First baseline handoff

The R1–R8 `owns` lists match the fixed contract's `file_ownership` exactly.

| Owner | Sole editing boundary | Read-only consumers |
|---|---|---|
| R1 | Go types, strict codec, canonical encoding, their tests, fixed contract destination | R2–R8 and later adapters |
| R2 | Python contract I/O and its tests | R3, R6, R8 |
| R3 | Fixture generator, fixture specification and tests | R7, R8 |
| R4 | Pure decay function and tests | DYN-02 |
| R5 | RRF reference, context packing and tests | R7 and dynamic replay |
| R6 | Metric calculation and tests | R8 |
| R7 | File-only baseline CLI and tests | R8 |
| R8 | Reports, acceptance entry point, collection bridge and handoff | DYN, SYS and EXP |

R1 owns shared Go declarations for the lifetime of the baseline. A later card
must not quietly add fields to `types.go` or adjust canonical encoding. The
contract ID and SHA-256 remain fixed. If publishing the original bytes conflicts
with the repository's public-content rules, record the conflict and keep the
original private; do not redact it, change its hash, or claim it was published.

The exact immutable asset was subsequently authorized for publication by the
user and published in [PR417](https://github.com/FelixBaek/arkism/pull/417), with
its digest unchanged. That authorization covers this one asset only.

The destination remains declared in the manifest so a publication conflict does
not silently transfer ownership or create a second contract version. Preparation
can verify a private source against the pinned digest without declaring the
repository copy or R1 complete.

## Later shared files

- DYN-01 owns base dynamics types and validation. DYN-04 owns the separate
  capture-extension types. DYN-05 owns its registry, policy, CLI and comparison
  files; its core and selected extension edits happen in sequence.
- ADAPT-02 owns base adaptation types. ADAPT-03 adds separate recovery types and
  cannot edit the base type file as a second writer.
- SYS and RTE requests touching product search, outcome, routing, daemon or
  budget files go through `INTEGRATION`. CRW's shared engine/node/meeting changes
  use the same editor. The manifest lists the actual shared files, including
  existing tests, rather than assigning overlapping broad package globs.
- New research files use non-overlapping directories or exact filenames.
  Synthetic fixtures and analysis outputs remain separate from product state.

## Dependencies and acceptance

`entry_gates` identifies where a card can begin; `completion_gates` identifies
its full acceptance boundary. The gate graph retains specification/integration,
draft/final-lock, scalar/extension and independent-review steps. For example,
DYN-02 can start at its scalar step without claiming whole-card acceptance.

Card-level `conditional` marks 14 optional cards, including DYN-04. The earlier
issue-level audit counted 13 and is corrected by the source plan here. Fast/slow
replay and capture-policy substages also require recorded selection decisions;
they must not be made mandatory just because their parent card exists.
The full pilot's `requires_if_selected` edges retain additional predecessors
for selected capture or frozen learned baselines. A skipped option needs a
recorded hold/reject decision, not an invented passing test.

PSY-04 enters and finishes at `PSY-04.preparation` for the current scope. Only
its readiness, preregistration and recruitment/data-plan drafts are assigned
now. Collection runbooks, data validators and actual analysis/result records
are listed under `future_owns`, not current `owns`; they require the separate
`PSY-04.execution_authorized` gate. PSY-05 can finish a conditional design
package without inventing actual participant results. The original later
collection/result gates remain visible as future acceptance boundaries.
Existing titles and older fixture shapes do not override this preparation-only
scope. Real collection, external spending and publication remain separate
decisions.

Dependency satisfaction requires the promised artifact and its actual checks,
not the card's name in a log. A substage pass, green general CI or a worker's
completion statement does not establish full-card, product or research-effect
acceptance. Continue recording actual commands, failures, unknowns and remaining
limits as implementation progresses.

CRW-02 first freezes and independently reviews its owned helper API at
`CRW-02.modules`. The INTEGRATION coordinator then assigns disjoint exact-file handoffs to connect the observer
to calls, existing FLOW reservations, meeting turns, trace delivery and host
usage transport. This substage resolves the former circular interpretation
of waiting for full CRW-02 acceptance before its own wiring. The CRW-02
completion gate still requires the integrated card criteria. Host producer
and corresponding test paths are explicitly registered as shared files;
each file has one writer at a time. Further edits wait for its completed handoff,
and the coordinator reviews the combined diff. Frozen private CRW-01 artifacts retain
their accepted bytes; a private dispatch overlay records this later ownership
revision. No external-call or build authorization is granted by this change.

`CRW-01.execution_refs_v2` is a separate preparation extension owned by R0.
Its five private artifact paths preserve the accepted crew v1 registry and
consumer pins. The extension must distinguish actual workflow, meeting and
assist record collections, require explicit verified roots, and keep effect
approval bound to crew_runs. Full CRW-02 acceptance and meeting/host wiring
also require this gate; v1 helper review does not satisfy it.

CRW-03 freezes its receipt/decision helper at `CRW-03.modules` before handing
off existing meeting/store paths to INTEGRATION. This prevents waiting for full
card acceptance before its own wiring. Observe-only receipt delivery and an
explicit current-use gate remain distinct; original contracts and server schemas
are not widened. The full card still requires its integrated acceptance checks.

SYS-05 A owns the offline workload generator, summary validator, contract and
regression corpus. Its reviewed default-collector registration is an exact-file
R8 handoff; SYS-05 consumes that file and does not become its writer. A preparation
and registration do not satisfy SYS-05.integration. Actual service/daemon,
restore, queue fairness and same-condition performance evidence remain required.
The SYS-05 status is in_progress; whole-card completion counts remain unchanged.
The SYS-05 B candidate owns the bounded dynamics worker, snapshot refresh and the
daemon's paired dynamics flags. It reuses the existing worker token and read:jobs
grants; native Search shadow stays off unless separately configured.

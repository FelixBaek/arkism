# Capture evidence audit (2026-10-15)

This audit compares metadata logs with the deployed Go session processor. No
conversation text, model output, private documents, or session identifiers are
included.

## What the reported 575 means

At 2026-10-14T14:37:47.309Z, selected pending-event counts were:

| Error | Log occurrences |
| --- | ---: |
| checkpoint_string_invalid | 69 |
| evidence_missing | 308 |
| tool_provenance_mismatch | 198 |
| Total selected errors | 575 |

These are retry-inclusive log occurrences, not 575 distinct rejected memories.
The selected events span 31 distinct logged session prefixes; truncated session
prefixes are not reliable unique-job identifiers. Other pending errors and batch
waiting events are excluded from this subtotal.

Reproduce this cutoff using metadata only:

    python3 tools/capture_metrics_audit.py /path/to/metrics.jsonl --until 2026-10-14T14:37:47.309Z

## Findings and limits

The active timer invokes the Go processor at deployed revision
0e5532b18f23ebb086990b3fe128fae679349d69. A separately running gateway had an
older executable revision; inspecting a source checkout alone is insufficient
to identify the running code.

Existing runtime changes already normalize checkpoint strings, downgrade
unobserved passed/failed checks, park invalid batches and children, and record
missing-evidence field diagnostics. They should not be reimplemented.

An evidence_missing result means empty source_refs. A wrong quote, revision, or
source reference is a different validation failure. The ten available
field-level missing-evidence diagnostics all identify check:not_run. These
diagnostics explain that observed subset, not the earlier 308 events: historical
raw model responses were not retained.

The remaining reproducible defect is a contract mismatch: the generation schema
permits empty source_refs while the validator requires evidence for state,
checks (including unknown/not_run), and candidates. The supplied patch requires
at least one reference and tells the model to omit unsupported checks or
candidates; empty checks/candidates lists remain valid. Strict quote, revision,
and role validation remains intact. Early minimum-length failures preserve
content-free missing-evidence diagnostics.

The archive contract intentionally exposes user/assistant dialogue. A general
tool-result ingestion path is not currently implemented. The 198 tool-provenance
events alone do not prove that an ingestor accidentally dropped tool output:
incorrectly labeled or mixed references can also fail. Adding a synthetic tool
line without authenticated tool records would not repair provenance.

## Runtime patch, not a public feature migration

patches/session-citation-contract.patch targets the deployed revision above.
The public base e1eb8fec79121e8b79b347a2734fc0152b06c9bc does not contain the
session processor. This PR publishes a reviewable source patch and audit tools;
it does not import the whole private feature branch or enable it on public main.

The prompt contract becomes harness-session-v3. Existing v2 quality evidence
cannot qualify the new prompt, so routing falls back to Sonnet until new quality
measurements qualify cheaper categories. This may increase cost. Deterministic
tests establish contract behavior, not model acceptance-rate improvement.
There was no paid model replay, production deployment, job replay, or automatic
memory approval during this audit.

The latest log tail is dominated by archive availability and parent-batch
waiting, which require a separate investigation. This patch does not claim to
resolve that backlog.

## Publishing hygiene

Use guide/public-publishing.md and tools/check_public_push.py from a clean
public checkout. Deleting private files in a later commit does not sanitize
earlier commits. Existing private worktrees must remain isolated until their
owners coordinate any history migration or deletion.

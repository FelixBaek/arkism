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

## Citation contract on main

The session processor and its citation contract are now part of main
(internal/host/capture/session_processor.go, covered by
session_citation_contract_test.go). The earlier source patch is removed.

The citation contract introduced harness-session-v3; the current durable-memory
and quoted-condition contract is harness-session-v4. Existing v2/v3 quality evidence
cannot qualify the new prompt, so routing falls back to Sonnet until new quality
measurements qualify cheaper categories. This may increase cost. Deterministic
tests establish contract behavior, not model acceptance-rate improvement.
The initial audit did not deploy the change or replay models. Subsequent
authorized runtime validation is described below; no automatic memory approval
was introduced.

The latest log tail is dominated by archive availability and parent-batch
waiting, which require a separate investigation. This patch does not claim to
resolve that backlog.

## Publishing hygiene

Use guide/public-publishing.md and tools/check_public_push.py from a clean
public checkout. Deleting private files in a later commit does not sanitize
earlier commits. Existing private worktrees must remain isolated until their
owners coordinate any history migration or deletion.

## Runtime validation and diagnostic correction

The regression tests now isolate capture state. Earlier test execution emitted
synthetic missing-evidence diagnostics into the live metrics file. Exclude those
test-generated records from operational comparisons using the private exclusion
manifest; retain the raw metrics. Their absence in later runs is not evidence
of lower production failure rates. The ten earlier field diagnostics predate
those tests and remain valid evidence for their limited observed subset.

After the correction, the full local check passed with an external state
sentinel unchanged and no increase in live missing-evidence diagnostics.
Authorized deployment and limited real-source probes were also performed.
Three matched replay pairs validated in both versions. An additional full
two-record window validated after the change, while its baseline CLI failed
before a classified validation result was available. These observations do not
establish a rejection-rate reduction. Longer operational comparisons must keep
test events separate and account for archive and parent-batch waiting.

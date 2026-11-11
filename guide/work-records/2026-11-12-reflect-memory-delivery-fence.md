# Reflect memory delivery fence

Work ID: MEMORY-QUALITY-90; related source-expansion delivery safety work.

## Reason and behavior

A memory deleted while a Reflect model request was running could still appear in the returned answer. The existing post-model check covered retained source snapshots, not ordinary memory records. A regression with a memory's own observation timestamp reproduced the stale answer when the new guard was disabled through a test-only Go overlay.

Memory and observation hits now retain an internal hash of their original admitted record before search or tool text truncation. Reflect retains the first snapshot and checks it before generation and after each answer, and before returning a prompt. Checks reject changed scope, revision, content, classification, erased/quarantined status or eligibility. They repeat scoped context/transfer authorization and use existing transactional search fences for the record and its source dependencies. Provenance normalization is consistent between admission and verification; the internal memory reference fence counter is excluded. A later read cannot rebind old prompt text to a new record.

## Validation

- Reproduction with the delivery guard disabled: failed as expected, returning the deleted memory answer without an error.
- Focused memory regressions: passed for unchanged control, deletion, erasure, quarantine, revision changes, same-revision text changes, scope changes, revoked source, context/transfer denial, truncated hits and first-snapshot preservation.
- Combined Reflect and disposable PostgreSQL memory/source-expansion regression run passed, including the final missing-admission-hash regression (1.168 seconds).

## Limits

This guard covers ordinary memories and observations. Synthetic source hits have the separate source snapshot checks. Synthetic mental-model hits are not newly covered by this change. Tests use controlled authorization/model doubles and do not establish production RLS behavior or answer accuracy. Record changes unrelated to prompt text may conservatively reject an in-flight answer because the full admitted record is checked. No production deployment was performed.

# Native delivery and lifecycle boundaries

The local service implementation connects explicit context selection, actual reader evidence and native-state recovery. It reuses existing Search, host packing, canonical traces, reference-only receipt spooling, source permissions, transactions and deletion ledgers. Native features remain off by default; no UI or daemon enablement is added here.

## One invocation and persistent changes

`HookWithReceiptChainView` accepts an explicit in-process context selector together with a caller-provided token encoding, item limit and token budget. It calls the existing BuildContext API and preserves the selector through bounded retries. A nil selector keeps the previous Search request and retry behavior. Invalid selectors never fall back to an unmasked Search request, including when evidence collection is absent, closed or has a mismatched identity.

The selector filters only the fresh invocation's exact scope/reference/revision, including resolved pins and the fresh history projection. It does not erase a retained session path or change another request. Persistent correction and withdrawal still use existing human review and deletion. A retained reference is recorded as a prior path rather than promoted to a new current inclusion or read.

## Evidence and durable publication

The chain separates server return, final host packing, successful transport, optional exact reader acknowledgement, actual memory GET and observed reader output. A reference excluded by final packing has no current-reader inclusion receipt. Short writes and absent or mismatched acknowledgements remain failures or unknown observations. The canonical audit record being stored and read back is separate from reading the memory itself.

Publication reuses canonical trace/source creation and exact readback before the existing reference-only spool. Scope/reader/cycle/page identities are stable across retry. A different payload for the same identity conflicts; a lost reply can recover without creating a second accepted page. Concurrent publication, partial multi-page failure and a deleted canonical readback are tested without restoring deleted data.

These are delivery observations. Actual model use, task success and causal contribution remain unknown. The existing hidden-path inventory and bounded local checks do not prove that every future context path has been enumerated.

## Revision, deletion and recovery

A dependency revision change immediately withholds dependent native state and creates an idempotent bounded purge job in the same transaction. Delayed physical cleanup cannot make stale state serve again. External correction may therefore create a legitimate purge even when Search itself has no write side effect.

Recovery keeps the original six reconciliation fields unchanged and uses separate native snapshot/restore sidecars. Readback witnesses bind the original scoped dependencies and revisions. A changed restored row can be reconciled only against the authoritative current deletion ledger, completed purge and matching current scrubbed state, with old serving data absent. Missing evidence, correction-only cases and an already-scrubbed backup whose original dependency cannot be reconstructed remain held or explicitly unsupported.

Readback and reconciliation require an explicit privileged database role. Ordinary callers and a database owner without superuser or bypass privileges cannot supply their own approval. Captured digest fields are diagnostic within this privileged metadata trust boundary; they are not signatures or independently authenticated metadata. Native reconciliation does not clear the existing general restore-review marker.

Native admission/read callbacks and the Search native-state publication transaction check the existing recovery gate. A hold committed before that transaction cannot publish native values. Baseline behavior is compared at the same request boundary: a request already in progress and a new request started after the hold are different cases. This does not claim to drain every reader or revoke already returned bytes.

## Validation scope

The latest verified public-main candidate ran387 methods1374 leaf cases in each of normal and race modes, without failure or skip. It includes actual isolated PostgreSQL/HTTP, existing worker/privacy regressions and complete physical-row comparison around one shared external mutation. The active-native/view/reader/recovery tests are included in that single invocation and use an actual restricted LOGIN. Separate independent coupled checks and the genuine LOGIN reader replay retain their own counts. Changed-public-main Go tests39 and UI source tests591 also passed. Full-module vet, explicit all-module changed-Go lint and the unchanged complexity policy passed.

Original source/gold fixtures, the fixed baseline contract, failed experiments and mistaken reviewer assumptions are preserved with separate followups. No full check_local or unrestricted all-Go suite is claimed: legacy standalone product-build helpers were not executed. The candidate incorporates the verified public main commit f86b95f and retains its inherited changes exactly; the API combines both sides without semantic conflicts. This remains local integration evidence, not deployment or a scientific performance result. The bounded worker/load and mode/fallback integration cards remain separate work.

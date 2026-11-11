# Reflect mental-model delivery fence

Work ID: MEMORY-QUALITY-90; related ordinary-memory and expanded-source delivery fences.

## Reason and behavior

A real Reflect-loop regression reproduced a late deletion leak: the mental-model tool admitted an approved summary, the next model call deleted its cited memory, and Reflect returned the copied summary without an error. Existing mental-model view filtering and stored-summary erasure did not revalidate an already admitted synthetic hit.

Reflect now retains the first admitted mental-model record and each cited record, checks their complete normalized hashes, and refuses later rebinding. Citation revisions must match at admission. Before generation and after each answer, it rereads the existing mental-model view, applies withheld-citation checks and transfer authorization, and transactionally fences the model, cited records and source dependencies. Deleted, erased, quarantined, unapproved or changed citations fail closed even when the model's own revision has not changed. The same checks cover both mental-model tool variants. A shared evidence-boundary helper keeps the existing source and ordinary-memory checks together.

## Validation

- Before the fix, the late-citation-deletion regression failed as expected: the answer was returned with no error.
- Focused Reflect and disposable PostgreSQL regressions passed, including unchanged controls, deletion, erasure, quarantine, same-revision text mutation, revision/approval changes, source revocation, transfer denial, model deletion/change/read denial and first-citation snapshot preservation.
- Go complexity gate passed after extracting the initial evidence preparation and common boundary helpers.
- Race-enabled Reflect and PostgreSQL regressions passed (`go test -race ./internal/memory -run 'TestReflect|TestPostgresReflect' -count=1`, 7.204 seconds), using the owned disposable database.

## Lint follow-up

The CI cognitive-complexity check required smaller validation and test helpers. The refactor preserves the authorization, transaction and mutation checks, and renames a test variable that shadowed the built-in `copy`. Focused `gocognit -over 20` checks pass for both mental-model files. The race-enabled mental-model and disposable PostgreSQL regression run passed after extraction (2.328 seconds).

## Limits

Controlled model and authorization doubles establish the tested race behavior; no external model requests were made. These checks do not establish answer accuracy or production RLS behavior. Full-record changes may conservatively reject an in-flight answer. Existing read fences define the validated transaction boundary; this change does not create a lock held across external model execution or network response delivery. No production deployment was performed.

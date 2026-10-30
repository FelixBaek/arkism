# SYS-06 feature flag, fallback and isolation: integration plan and report validator

Task: SYS-06 (#152)
Research: none (engineering acceptance gate; no product result)
Design: Stage A only. A flag matrix (off, shadow, dynamic, emergency_fallback × normal and seven fault/race conditions), an index of the SYS-01~05 contract files with their hashes, and a validator that judges an integration report.
Relation: faithful
Deviation: none for stage A. Stage B (actual product wiring) waits for SYS-05 acceptance, an accepted DYN policy and an owner-assigned Story allowlist.

## Files

- `flag_matrix.json`: expected path per mode and condition. Default `off`; an unknown mode is a startup error.
- `system_contract.json`: SYS-01~05 contract and case files with SHA-256 and review state. A changed file holds any report until it is rechecked.
- `integration_plan.json`: stages, prerequisites, required checks, states and stop conditions.
- `validate_integration.py < REPORT.json`: reads the report from standard input and prints `{status, reasons}`; exit 0 only for `synthetic_pass` or `accepted_local_integration`.

## What the validator enforces

- off and shadow equal the baseline with no receipt or reinforcement change.
- A fault in dynamic mode falls back to the baseline with the expected reason and a count; errors are not hidden.
- Every cell rechecks scope, approval, source, permission, deletion, revision, correction and the final fence; no deleted or superseded memory is revived; no rollback to an older snapshot. A product diff names each path once.
- A cell without an expected fallback must report `fallback_count` 0; booleans and counts are typed strictly (1 is not true, false is not 0).
- Blocking normal work (`returned: none`) is not a safe pass.
- Every matrix cell is present once; unknown cells are rejected.
- Missing code/config/fixture hashes, independent review, clean commands (exit 0, no failure, no skip, at least one test), Story link or product diff hold the report.
- A local integration claim also needs every SYS-01~05 index entry `accepted` (SYS-05 is still `in_progress`, so it holds today), every stage B `required_checks` command run clean verbatim, and product diff paths inside stage B `candidate_files`.
- Synthetic evidence can reach `synthetic_pass` but never `accepted_local_integration`; `production` is never accepted here.

## Reproduce

```sh
python3 -m unittest discover -s evaluation/memory-harness-v1/system/sys_06 -p 'test_*.py'
```

## Limits

Hashes are checked for shape, not bound to files; Story ids are checked for shape only; the evidence label is self-declared. The validator checks a report; it does not run the product, and a well-formed report is not proof that its observations are true. No product code, flag or configuration was changed. SYS-06 as a whole needs stage B.

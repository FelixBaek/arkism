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
- `validate_integration.py REPORT.json`: prints `{status, reasons}`; exit 0 only for `synthetic_pass` or `accepted_local_integration`.

## What the validator enforces

- off and shadow equal the baseline with no receipt or reinforcement change.
- A fault in dynamic mode falls back to the baseline with the expected reason and a count; errors are not hidden.
- Every cell rechecks permission, deletion, revision and the final fence; no deleted or superseded memory is revived; no rollback to an older snapshot.
- Blocking normal work (`returned: none`) is not a safe pass.
- Every matrix cell is present once; unknown cells are rejected.
- Missing code/config/fixture hashes, independent review, clean commands (exit 0, no failure, no skip, at least one test), Story link or product diff hold the report.
- Synthetic evidence can reach `synthetic_pass` but never `accepted_local_integration`; `production` is never accepted here.

## Reproduce

```sh
python3 -m unittest discover -s evaluation/memory-harness-v1/system/sys_06 -p 'test_*.py'
```

## Limits

The validator checks a report; it does not run the product, and a well-formed report is not proof that its observations are true. No product code, flag or configuration was changed. SYS-06 as a whole needs stage B.

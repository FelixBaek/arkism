# Offline B0 harness handoff

R1–R7 define an isolated synthetic baseline: exact DTO validation and canonical encoding, reproducible fixtures, a scalar decay reference, fixed ordinal RRF, whole-item fixture packing, scheduled-query metrics, and a Go CLI. R8 adds report export, an acceptance runner, and test collection. These components do not modify product retrieval or authorization.

The contract is `arkism-offline-b0-v1`; its original bytes have SHA256 `04463d838f397772743c5740a346230eb8e9db60db8dddc824d52e1f083790ef`. Contract bytes and independent golden expectations remain unchanged. R8 reuses the accepted contract, fixture, metric and safe file interfaces; it does not add another ranking or schema engine.

## Verification boundary

The current task prohibits standalone builds. **The default `run_acceptance(repo_root, work_dir)` has not been executed.** Its original `go build -o <work_dir>/baseline ./cmd/memory-harness-baseline` command and the separate binary invocation are implemented for a later authorized run. Neither `go run` nor mocked commands replace those required checks. Full standalone acceptance remains **not run**.

`test_acceptance.py` intercepts every acceptance child command. Its build output is an explicitly fake, non-executable artifact; fixture generation, Python metrics and report generation use real temporary files. Those tests verify scheduling, argument vectors, evidence hashes and failure handling. They do not establish that a standalone binary was built or executed. Separate permitted Go tests exercise the CLI's `run()` function, which is a different verification scope.

The following commands run unit checks and compile Go tests; they do not invoke the acceptance runner:

```sh
python3 -m unittest discover -s evaluation/memory-harness-v1 -p test_report.py -v
python3 -m unittest discover -s evaluation/memory-harness-v1 -p test_acceptance.py -v
python3 -m unittest discover -s evaluation -p test_memory_harness.py -v
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test ./internal/memoryharness ./cmd/memory-harness-baseline -count=1
```

Record the actual command, exit code, test names/counts and evidence hashes for each run. Repeated executions are not additional samples or unique tests. The acceptance runner deliberately excludes its own tests, the collector and full checkout checks from its child list, preventing recursion. Required zero-test suites, skipped tests, malformed evidence and missing required hashes cannot pass.

## Reports and evidence

`make_report(metric_run, metrics_sha256, output_dir)` returns the exact `ReportSummary` and writes only `queries.csv`, `streams.csv`, `summary.csv`, and `summary.json`. CSV columns and the six summary rows follow the fixed contract. UTF-8 without BOM, RFC4180 quoting, CRLF rows, `.17g` floats, empty null cells, numeric zeros and failed query rows remain distinct. `fixture_units_v0` is a synthetic cost unit; it is not tokenizer usage or the product host's text budget.

A future authorized acceptance run requires a fresh work directory. It records the build and binary execution as separate checks, then verifies raw contract/policy/input/evaluation/prediction/metric/report hashes, typed outputs, fixed full-reference selections and CSV contents. Negative checks cover corrupted input hashes, another run's bindings, missing queries and a forbidden gold argument. The raw byte hashes bind the actual files; typed canonical hashes compare logical values and do not replace raw provenance.

`acceptance.json` records failures and commands that did not run. If any required artifact or hash is missing, no `baseline_bundle.json` is published and no placeholder hash is invented. A `checks_failed` bundle is possible when all required artifacts and evidence exist but a check fails. `checks_passed` requires every required check to run and pass. Its review status remains `requires_independent_review` until a separate owner review; the worker does not promote itself to accepted, published or product integrated.

The final two records use exclusive publication with inode ownership captured from the new temporary file descriptor before linking. If bundle construction or publication fails, only matching owned records are retracted and a failed diagnosis with no bundle reference is published. If that diagnosis cannot be published, the runner returns an I/O error and retracts its own success records; competing files and symlinks remain untouched. A fixed error detail reports incomplete cleanup or a preserved competing output. Descriptor lifetime is established before metadata lookup. One retry may recover inode identity for cleanup; if identity remains unavailable, descriptors close, the unknown temporary is preserved, and the fixed cleanup limitation is reported rather than guessing ownership.

The bundle records the observed Git commit and sorted dirty path metadata. Only files in the exact R1–R8 ownership list have their bytes hashed. Deleted and unrelated dirty paths use a null hash; unrelated contents, including symlink targets, are never opened. Preexisting unrelated work is preserved. Newly observed changes outside the ownership boundary, a changed observed commit, or changed owned source hashes during execution cause failure. No staging or reset occurs.

## Remaining work

An independently reviewed, authorized standalone build and binary acceptance run is still required. Local unit checks and `run()` integration do not discharge that gate. Full checkout checks and independent review remain the integrator's responsibility.

B0 measures fixed synthetic retrieval only. It provides no new holdout result, significance or improvement claim, full service equivalence, DB deletion result, deployment validation, dynamic-state stability, or actual token budget validation. Future DYN/EXP/SYS work needs its own versioned contracts, comparable candidates and budgets, provenance, failure accounting and explicit research or product acceptance. Candidate, returned and packed stages must remain distinct; no extra fields are added to frozen v1 DTOs to anticipate those extensions.

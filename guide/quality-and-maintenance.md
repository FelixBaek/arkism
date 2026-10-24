# Quality checks and maintainer setup

## Metrics with visible limits

Rust and JavaScript `S3776` cognitive-complexity findings are no longer
suppressed by language-wide exceptions. Go `S3776` stays suppressed by the
`gocx` exception until a confirming analysis shows the existing Go debt is no
longer new code. Sonar coverage remains excluded because
coverage has its own explicit gate; a zero-smell result must not be presented
as proof of low complexity or complete testing. Live Sonar results require a
new analysis and are not established by changing this configuration. Review the
new-code definition in SonarCloud itself; the app version label in the workflow
does not configure that definition. With `Previous version`, a changed
`sonar.projectVersion` starts a new period; a fixed date or other definition
needs its own SonarCloud setting change.

`go run ./tools/complexity --report /tmp/go-complexity.json` reports cyclomatic
complexity for production Go declarations and closures. It is **not** Sonar's
cognitive metric. The initial `efb51f0` source inventory contains 5,756 functions
and closures; 382 exceed 20. The JSON baseline records those values explicitly.
New functions above 20 and increases above their frozen legacy values fail CI.
All measured values remain in the report, with the worst 20 in the job log.
Do not regenerate the baseline to make a failed change pass. Source moves must
retain the old debt rather than silently obtaining a fresh allowance.

Coverage has three independent controls: the existing three core files each
need 90%; `config/package-coverage.json` freezes measured floors for the memory
and workflow packages (83.5% and 79.5%, initialized from observed 83.78%
and 79.56% on this change); Codecov changed Go/UI lines require 80%. Rust is tested
separately and is not represented by the Go/UI coverage uploads. Package
coverage and Linux Go/UI upload errors fail `Coverage`; native Windows upload
errors fail the Windows Checks job. A repository ruleset must require the
corresponding statuses for them to prevent merging.

The Windows Checks job uploads native installer coverage as `go_windows`;
it covers `internal/installtxn` and does not carry forward an older result.
Codecov statuses and comments wait for all three reports for the commit:
Linux Go, UI and native Windows. Local native test results are separate evidence
from a successful remote upload or patch status. The 80% patch target is unchanged.

## What the repository actually enforces

On 2026-10-21, authenticated API reads found active ruleset `main-protect`
(`25018792`) on the default branch, no bypass actors, PRs required and 11 status
checks. The classic branch-protection endpoint returns 404 because protection
uses a ruleset. It would be incorrect to infer no protection from that endpoint.
The active rule did **not** require `go-windows`, `evaluation`, `coverage` or
`codecov/patch`. Independent approval count was zero.

[`.github/rulesets/main.json`](../.github/rulesets/main.json) preserves the
observed policy and proposes those four missing checks plus the new
`ui-browser-windows` regression job. That Windows job checks both source and
production UI with synthetic APIs; it does not install the product. It is a reviewable
configuration file, not evidence of a server-side update. A maintainer may apply
it after checking the exact check names and Codecov availability:

```bash
gh api --method PUT repos/FelixBaek/arkism/rulesets/25018792 \
  --input .github/rulesets/main.json
gh api repos/FelixBaek/arkism/rules/branches/main
```

The release verifier separately requires the latest exact-commit main runs for
Checks, SonarCloud, Coverage and default-setup CodeQL. CodeQL's inventory path is
`dynamic/github-code-scanning/codeql` and its event is `dynamic`, not `push`.
The verifier resolves its active workflow ID, checks the complete bounded API
inventory and rejects missing/disabled/ambiguous evidence. It does not change
repository rules or guarantee that an independent person reviewed the code.

## CI setup and runner recovery

Own trusted branches use the self-hosted `arkism-self` label. Fork and Dependabot
PRs use GitHub-hosted Ubuntu; Windows uses hosted runners. Set repository
variable `ARKISM_USE_HOSTED_RUNNERS=true` to route Linux jobs to hosted Ubuntu
when the self-hosted runner is unavailable. This is an explicit operational
fallback; a queued self-hosted job cannot switch runners midway through a run.

SonarCloud follows the same rule. Default-setup CodeQL stays on hosted runners:
on `arkism-self` its jobs were cut off mid-analysis.

For a replacement self-hosted runner, install Git, `unzip` (the Sonar scanner
download needs it), the Go version from `go.mod`, a C compiler for `-race`, Python 3, Node 22, Rust stable, PostgreSQL with pgvector,
and the WebKit/GTK development packages named in `checks.yml`. Run it under a
dedicated non-root account with an owned build cache. `tools/ci_postgres.sh`
creates a throwaway loopback database in `RUNNER_TEMP`; it must never point at
the production database. Ensure temporary storage has enough capacity for
parallel database and process tests. No production credentials are needed.

On the repository side, configure SonarCloud's organization/project matching
`sonar-project.properties`, disable automatic analysis, and provide `SONAR_TOKEN`
to the analysis workflow. Configure `CODECOV_TOKEN` for Coverage and the Windows
Checks coverage uploads, and
confirm the patch status reports on a real PR. Do not paste secret values into
issues or setup notes. Windows signing secrets belong only in the protected
environment described in [update security](security-operations.md).

The pinned gitleaks job checks both the checkout and full fetched Git history.
Dependabot keeps routine version PRs disabled, while explicitly grouping
security updates. These policies do not substitute for vulnerability review,
dependency upgrades or independent maintainers. CODEOWNERS currently names the
single existing maintainer; recruiting another reviewer remains human work.

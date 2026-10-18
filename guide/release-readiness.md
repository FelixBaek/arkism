# Public release readiness

The current milestone is approval-policy consistency, public installation, reuse between coding agents, and measured usefulness. Team expansion is outside this milestone.

## Packaging preflight

Changes to the release workflow, installer, Unix build script or Tauri shell trigger the Release workflow on pull requests. Windows, macOS and Linux build independently and upload test installers as Actions artifacts. These builds use version `0.1.0`; they do not create tags, publish a release or update the public update feed. The existing platform build scripts run their built-in runtime smoke checks.

Download artifacts only from the expected repository, pull request and commit. Record that commit alongside results. A successful packaging build is not evidence that memory quality or approval policy is correct.

### Installed Windows runtime check

The GitHub-hosted Windows packaging job also opts into
`build-windows.ps1 -InstalledSmoke`. It installs the same final NSIS file with
`/S /NoAutoRun` into a fresh owned temporary directory and checks the installed
payload against the final staging files. The explicit `/NoAutoRun` option skips
silent launch; the default silent update restart and interactive finish-page
checkbox retain their existing behavior. An inert-payload fixture checks both
silent launch options without starting the product GUI.

The installed `Arkism.exe --runtime-check` must start its own local database,
reuse its local endpoint, back up, erase and restore it. The check requires
successful original and restored capabilities, a nonempty backup and successful
expiry. It uses fresh test data, disabled embedding, a fixed judge and a child
environment without provider or signing credentials. It does not make a model
request. Stage-only execution is recorded separately.

This mode refuses local or self-hosted runners and refuses existing product
registrations, shortcuts, tasks, autostart entries or affected agent settings.
It never starts the normal GUI or registers a scheduled task. Cleanup removes
only its owned files and verified test-created registrations and shortcuts;
it does not run the product uninstaller or disconnect agents. Inspection and
cleanup errors fail the check and retain the original failure.

The separate `windows-installed-check` Actions artifact contains only check
summaries and source/payload hashes. Runtime tokens, database backups and child
profiles are excluded, and this evidence artifact is excluded from release
assets. A successful job establishes fresh installed-runtime operation for that
commit. Existing-data upgrades, migration from older releases, normal uninstall,
agent connection, interactive UI and operating-system signing trust still need
their own acceptance checks.

## Runtime download inputs

The Windows runtime recipe pins SHA-256 for both ZIP inputs before extraction.
The existing versions and URLs are retained. These pins were observed from the
exact official HTTPS distribution bytes on 2026-10-18; an independent
publisher-signed or publisher-published checksum was not verified.

| Input | Exact artifact | SHA-256 |
| --- | --- | --- |
| PostgreSQL 17.6-1 | [Windows x64 binaries ZIP](https://get.enterprisedb.com/postgresql/postgresql-17.6-1-windows-x64-binaries.zip) | `d378882abd001a186735acd6f6ba716bca6ccd192e800412d4fd15ed25376b3e` |
| pgvector 0.8.1 | [Source ZIP](https://github.com/pgvector/pgvector/archive/refs/tags/v0.8.1.zip) | `61182a6afd6fb94c0e7740c037a7d3c36a8cab1cc581ae5bd9b7b2cfd789bdfc` |

A failed or mismatched download stops before that archive is extracted, native
build commands run, or the runtime is staged. Unix checksum diagnostics also
stop on mismatch. Updating a pin requires reviewing the exact new artifact;
changing versions or using the digest of a different archive is insufficient.
GitHub can change generated ZIP compression bytes, so a regenerated archive may
require a reviewed pin update even when its source tree is unchanged.

Windows cache reuse requires a completion marker for the current recipe, target,
versions, URLs and pins, plus the required runtime files. The marker is written
after a successful build and required-file checks. A legacy cache containing
only `vector.dll` cannot bypass the new download verification. Changes to the
runtime build recipe must update its fingerprint identifier. This records the
verified inputs and build completion; it does not authenticate every cached
output or protect a cache that a local writer can modify.

Normalized cache and staging overlaps are rejected before cleanup. Existing
path components are inspected from the filesystem root: reparse points,
unexpected file/directory types and inspection errors stop preparation. Only a
genuinely missing tail below inspected directories is allowed. Checks repeat
before cleanup, after the build and before staging changes. Use ordinary
directory paths; this does not prove every DOS-device alias or prevent a
concurrent path replacement.

A new staging copy is checked before replacing the previous output. Copy or
publication failures preserve or restore that output; a failed restoration
retains the recovery backup and reports its location.
If temporary-copy cleanup also fails, the reported error retains the original
failure and any recovery path alongside the cleanup failure.

The offline PowerShell regression uses inert ZIP fixtures and fake download,
extraction and native build boundaries. Windows CI runs it with Windows
PowerShell; it does not install or execute PostgreSQL from the fixture.
Packaging preflight still checks the real bundled runtime. Source pins, cache
completion, OS code signing and signed updater manifests provide separate
evidence. Runtime preparation alone does not establish installed-app behavior;
the separate hosted installed-runtime check covers the narrower case above.

## Before a public tag

- Land and verify the approval-policy changes, including captured user statements and background extraction. AI classification must not activate a memory by itself.
- Require the database integration and boundary checks selected by maintainers in branch protection. A workflow file alone does not configure required checks.
- Build installers from the exact release candidate commit on all supported platforms.
- On clean profiles, verify installation, agent connection, candidate creation, human approval, recall through a second agent, correction/revocation, backup and restore, and uninstall. Preserve existing agent configuration and check that uninstallation restores it.
- Record platform, agent versions, commit, steps and outcomes. Do not substitute preview data or unit-test results for an installed-app check.

## Draft and publication

Every tag release requires the latest `checks.yml` and `sonarcloud.yml` main-push
runs for the exact source commit to succeed. Pull-request results, another
commit, another repository, a non-main branch, and older successful runs do not
satisfy this gate. Missing or running checks are awaited for up to 30 minutes;
failed, cancelled, skipped, incomplete, or unavailable results block packaging.
The gate checks every API page and rechecks the latest run attempts immediately
before creating the release. If quality is rerun and no longer succeeds while
packages build, those packages cannot be released by that run.

Queries use authenticated, read-only HTTPS requests to `api.github.com` with
redirects disabled. Credentials come from the workflow token or the existing
local GitHub CLI login; credential values and provider errors are never printed.
Requests share a deadline, and inventories over 10 pages or oversized responses
are blocked rather than partially trusted.

Pull-request and normal Release manual builds still create disposable workflow
artifacts without creating tags or releases. The retained initial-publication
workflow requires an explicit manual dispatch and also passes the shared gate;
editing its file cannot automatically publish. Its caller grants read access to
quality runs, and existing published tags/assets are never replaced by a retry.
The release gate does not install branch protection or sign an artifact. The
installed-runtime check does not prove existing-profile upgrade behavior.

After the release candidate passes, create a new `vX.Y.Z` tag on that verified commit. The tag workflow builds the packages and creates a draft release. Inspect its assets and checksums before publishing. Do not move an existing tag or replace published assets. No tag or public release is created by a pull request preflight.

When publishing, update the English and Korean README installation sections to remove the preparation notice. State which artifacts are signed and which require operating-system confirmation. Do not claim all-local processing when configured extraction sends conversation excerpts to a model provider.

## Evidence still required

Installer artifacts, human approval enforcement, and successful cross-agent delivery are separate acceptance criteria. Controlled task evaluations and actual user review time are needed before claiming productivity improvement. Keep those measurements separate from build/test status.

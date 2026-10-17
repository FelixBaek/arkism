# Public release readiness

The current milestone is approval-policy consistency, public installation, reuse between coding agents, and measured usefulness. Team expansion is outside this milestone.

## Packaging preflight

Changes to the release workflow, installer, Unix build script or Tauri shell trigger the Release workflow on pull requests. Windows, macOS and Linux build independently and upload test installers as Actions artifacts. These builds use version `0.1.0`; they do not create tags, publish a release or update the public update feed. The existing platform build scripts run their built-in runtime smoke checks.

Download artifacts only from the expected repository, pull request and commit. Record that commit alongside results. A successful packaging build is not evidence that memory quality or approval policy is correct.

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

Pull-request and manual builds still create disposable workflow artifacts without
creating tags or releases. The one-time initial publication also passes the
shared release gate. These checks do not install branch protection, sign an
artifact, or prove clean-profile installation and upgrade behavior.

After the release candidate passes, create a new `vX.Y.Z` tag on that verified commit. The tag workflow builds the packages and creates a draft release. Inspect its assets and checksums before publishing. Do not move an existing tag or replace published assets. No tag or public release is created by a pull request preflight.

When publishing, update the English and Korean README installation sections to remove the preparation notice. State which artifacts are signed and which require operating-system confirmation. Do not claim all-local processing when configured extraction sends conversation excerpts to a model provider.

## Evidence still required

Installer artifacts, human approval enforcement, and successful cross-agent delivery are separate acceptance criteria. Controlled task evaluations and actual user review time are needed before claiming productivity improvement. Keep those measurements separate from build/test status.

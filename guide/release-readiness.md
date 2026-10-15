# First release readiness

The current milestone is approval-policy consistency, public installation, reuse between coding agents, and measured usefulness. Team expansion is outside this milestone.

## Packaging preflight

Changes to the release workflow, installer, Unix build script or Tauri shell trigger the Release workflow on pull requests. Windows, macOS and Linux build independently and upload test installers as Actions artifacts. These builds use version `0.1.0`; they do not create tags, publish a release or update the public update feed. The existing platform build scripts run their built-in runtime smoke checks.

Download artifacts only from the expected repository, pull request and commit. Record that commit alongside results. A successful packaging build is not evidence that memory quality or approval policy is correct.

## Before the first public tag

- Land and verify the approval-policy changes, including captured user statements and background extraction. AI classification must not activate a memory by itself.
- Require the database integration and boundary checks selected by maintainers in branch protection. A workflow file alone does not configure required checks.
- Build installers from the exact release candidate commit on all supported platforms.
- On clean profiles, verify installation, agent connection, candidate creation, human approval, recall through a second agent, correction/revocation, backup and restore, and uninstall. Preserve existing agent configuration and check that uninstallation restores it.
- Record platform, agent versions, commit, steps and outcomes. Do not substitute preview data or unit-test results for an installed-app check.

## Draft and publication

After the release candidate passes, create `v0.1.0` on that verified commit. The tag workflow builds the packages and creates a draft release. Inspect its assets and checksums before publishing. No tag or public release is created by a pull request preflight.

When publishing, update the English and Korean README installation sections to remove the preparation notice. State which artifacts are signed and which require operating-system confirmation. Do not claim all-local processing when configured extraction sends conversation excerpts to a model provider.

## Evidence still required

Installer artifacts, human approval enforcement, and successful cross-agent delivery are separate acceptance criteria. Controlled task evaluations and actual user review time are needed before claiming productivity improvement. Keep those measurements separate from build/test status.

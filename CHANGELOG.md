# Changelog

Published entries summarize the linked release notes. Unreleased items describe
source changes only; they do not claim that a signed installer is available.

## Unreleased

- Give the agents of the built-in desktop Arkism their own `agent` token in
  place of the owner token, so approvals, deletions and other owner-only calls
  stay with the person; existing installs get it on their next start.
- Start a session with at most 10 memory lines: this repository's rules first, then
  memories close to the first question or the task contract, and one line counting
  the rest (`ARKISM_SESSION_INDEX_LINES`, `ARKISM_SESSION_INDEX_CHARS`).
- Name every memory-map point, keep line and label size steady when zoomed,
  show loading and error states, and read memories past the first page.
- Hide the window buttons while a popup is open; fix expert creation and
  opening, false "unsaved changes" prompts in teams and automations, and report
  a taken $name/@name as such (API `409 handle_taken`).
- Pick the AI tools default model per provider; run GLM models in Claude Code
  against Z.ai without a wrapper; skip Codex composer-suggestion prompts.
- Show review editing, kind filters, full lists and dreaming proposals beyond
  the first loaded page in the memory windows.
- Add per-release compression, hook latency and token evaluation metrics.
- Fail a routed group call over to its fallback on a 5xx (529 included) or a
  timeout before any response, and return the pick's own error when none answers.

## [0.1.3](https://github.com/FelixBaek/arkism/releases/tag/v0.1.3) — 2026-10-22

- Bound local HTTP hosts, requests and provider lifetimes; distinguish missing
  credentials (401) from permission denials (403), and limit login attempts.
- Quarantine malformed jobs without losing payload evidence; recover worker
  panics, jitter retries, batch skill parents and bound default database pools.
- Coordinate final recall commits without weakening Serializable validation.
- Add opt-in scoped candidate expiry, transient notification retries, private
  diagnostics, log levels and bounded Windows desktop log snapshots.
- Pin patched Go and Rust toolchains; retain minisign as the sole update format.

- Page candidates, conflicts, skills and trash on demand, 50 at a time; retain
  retry state and refresh only pages already opened, including sparse pages.
- Virtualize long management lists with measured row heights and keyboard/focus
  preservation; create grouped candidate rows only when expanded, 50 at a time.
- Separate background refresh planning/publication and candidate presentation;
  index candidate grouping and yield during large skill similarity comparisons.
- Add Windows source/bundle browser regression checks and document which filters
  and counts describe loaded rows rather than the entire server catalog.

- Load memory pages on demand, search the full server list, preserve retry state,
  and discard replies after a connection/scope change.
- Document all 271 management HTTP registrations, including 60 missing contracts;
  reject route/DTO drift and validate 170 real handler responses with their requests.
- Split app/workflow UI ownership into ES modules, bundle Windows assets with
  pinned esbuild, and load the English dictionary only when selected.

- Authenticate Windows update manifests and installers with an independently
  pinned minisign key; require HTTPS throughout downloads and redirects.
- Prepare Windows-only Syft SPDX SBOM and build provenance, sign the checksum
  inventory, and refuse release creation after verification failure.
- Restore Go/Rust/JavaScript cognitive-complexity findings; publish a measured
  Go complexity report and prevent new complexity debt above a frozen baseline.
- Require exact-commit coverage and CodeQL evidence before a public release.
- Align both READMEs with current menus, Windows-only distribution and capture
  privacy behavior; add contributor templates and a proposed required-check list.

See [Windows release security](guide/security-operations.md) for the signing and release checks.

## [0.1.2](https://github.com/FelixBaek/arkism/releases/tag/v0.1.2) — 2026-10-20

- Verify runtime download hashes and updater download limits; retain the
  installer identity and restore the runtime when installer launch fails.
- Exercise installed-runtime and uninstall behavior on disposable Windows jobs.
- Preserve captured user evidence, memory conditions and case-sensitive groups.
- Revise Crew, automation, memory-map and settings screens. Windows is the
  distributed desktop target.

## [0.1.1](https://github.com/FelixBaek/arkism/releases/tag/v0.1.1) — 2026-10-18

See the published release notes for the original change list and Windows assets.

## [0.1.0](https://github.com/FelixBaek/arkism/releases/tag/v0.1.0) — 2026-10-15

Initial alpha: shared memory and skills for coding agents, with captured claims
kept as candidates until a person reviews them. Local storage can still involve
external model requests when extraction is configured.

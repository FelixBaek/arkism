# Development

## Layout

| Path | What it is |
|---|---|
| `cmd/memoryd` | API server and embedding worker (one binary, mode chosen by flags) |
| `cmd/memory-principal` | Registers tokens and permissions in Postgres |
| `cmd/arkism` | Host tools as subcommands: agent hooks, capture, skill sync, model gateway, Discord, backups, PC client |
| `cmd/arkism-crew` | Workflow runner (`serve`, `run CREW_ID`, `hook-url WORKFLOW_ID`) |
| `internal/memory` | Memory, skill, routing, session and privacy logic behind the API |
| `internal/host` | The `arkism` subcommands |
| `internal/crewrun` | Workflow and meeting engine |
| `desktop/src-tauri` | Desktop app shell (Rust, Tauri): built-in mode, SSH connection, backups |
| `desktop/ui` | Desktop app screens (plain JavaScript, no build step) |
| `api/openapi.yaml` | HTTP API description |

## Requirements

| Tool | Version and use |
|---|---|
| Go | The version in `go.mod`. Tests use `-race`, so a C compiler (gcc) is also needed |
| PostgreSQL | Postgres 17 with pgvector, only for integration tests and server mode. The `vector` extension must live in the `extensions` schema |
| Docker | Only for `tools/check_postgres_local.sh` |
| Node.js | 22, for the desktop UI tests |
| Python | 3, for the UI preview |
| Rust (stable) | Only to build or test the desktop app |

## Checks

Local checks, with no credentials:

```bash
go vet ./...
go test -race ./...
node --test desktop/ui/*.test.js
cd desktop/src-tauri && cargo test --locked
```

Core boundary tests must keep at least 90% statement coverage in `tei.go`, `candidate_quota.go` and `context_budget.go`:

```bash
go test -coverprofile=/tmp/arkism-core.out -run 'TestTEI|TestCandidate|TestContext|TestSearchBudget' ./internal/memory
python3 tools/check_core_coverage.py /tmp/arkism-core.out
```

Postgres integration tests start a throwaway Postgres in Docker, run every test against it, and remove it:

```bash
bash tools/check_postgres_local.sh
```

GitHub Actions Checks runs the Go race suite with a disposable pgvector Postgres database and `-tags=integration` on every pull request. It also enforces the core boundary coverage threshold, runs UI and Tauri tests, and builds the Go commands and executes the Windows skill bundle capture test on a Windows runner. Keep both `go` and `go-windows` required in branch protection; a workflow file alone does not configure merge protection.

To run the same tagged Go suite against your own disposable loopback database:

```bash
POSTGRES_TEST_DSN='postgres://arkism@127.0.0.1:5432/arkism_ci?sslmode=disable' \
  go test -race -tags=integration -count=1 ./...
```

The database user must be able to create disposable test databases. The local Docker helper additionally checks restricted database roles and schema ownership; these extra checks are not part of the pgvector CI service job.

Before publishing, maintainers run `ARKISM_PUBLIC_DENYLIST=<their own denylist file> tools/check_public.sh`, which fails if any tracked file matches one of its private regexes.

## Skill improvement generation

New memory extraction uses `capture-extract-v5` and `harness-session-v4`.
One-off permissions, budgets and assignment corrections belong in session state;
durable candidates retain the named project and every condition. Session
user-statement context fields must share one verbatim cited user quote. Inferred
scope diagnostics belong in state uncertainties, not in quoted condition fields.
This literal check prevents invented metadata wording; it does not prove semantic
completeness. Older prepared requests retain their original delivery contract.
An older session quality gate does not enable the new prompt's fast model.

`POST /v1/skills/generate` reuses verified lessons and the pinned baseline. The
configured chat and Anthropic adapters request localized changes to an existing
skill instead of asking the model to rewrite the full document. Each of up to
eight replacements names a literal `old_text`, its `new_text`, and a zero-based
`lesson_index` into the supplied verified lessons. These are internal provider
fields; the HTTP request and canonical `SkillDraft` format are unchanged.

Every anchor must occur exactly once in the original baseline. Missing,
ambiguous, overlapping, chained, empty, and ineffective changes are rejected.
Replacements are composed against original byte locations, preserving all
untouched text and line endings. New skills continue to use full content.
The resulting full document still passes the existing capability, size, source,
comparison, and approval checks. A patch identifies a proposed change; it does
not establish its semantic correctness or performance benefit.

Candidate generation leaves the active baseline unchanged. The Anthropic
prepared-result path stores the composed document, so a retry after candidate
storage failure can recover it without repeating the provider call. Provider
prompt versions distinguish this format from older whole-document generation.

## UI preview

The desktop screens can run in a browser against made-up data, without a real Arkism:

```bash
python3 -I desktop/ui/testdata/preview/serve.py 8795
# open http://127.0.0.1:8795/index.html
```

`desktop/ui/testdata/preview/shot.mjs` takes screenshots of it with Playwright.

## Building the desktop app

- Windows: `desktop/installer/build-windows.ps1 -Version X.Y.Z`
- macOS and Linux: `desktop/build-unix.sh --version X.Y.Z`. It builds the built-in Postgres and pgvector from source, so it needs a C compiler, bison and flex, and on Linux the WebKitGTK build packages.

Values compiled into the app:

| Name | Use | Default |
|---|---|---|
| `ARKISM_DEFAULT_SERVER` | First server address built into the app | `http://127.0.0.1:8091` |
| `ARKISM_DEFAULT_WORKSPACE`, `ARKISM_DEFAULT_PROJECT` | Scope a server connection opens first | `default` |
| `ARKISM_UPDATE_FEED` | HTTPS update feed the app checks | update check off |
| `ARKISM_SERVER_BIN` | `arkism` path tried first on a server | `/srv/arkism/bin/arkism` |

## Releases

Pushing a `vX.Y.Z` tag runs [`.github/workflows/release.yml`](../.github/workflows/release.yml). It builds the Windows installer, the macOS app and the Linux AppImage and `.deb`, checks that each starts its built-in Arkism and can back it up and restore it, and uploads them with `latest.json` as a **draft release**. A maintainer publishes it. Signing happens only when the signing secrets exist (see the workflow header).

The app checks the update feed for new versions: Windows installs the update in the app, macOS and Linux download the new file.

## Memory conditions in retrieval

Full-text retrieval includes descriptive applicability, uncertainty and valid-from
phrases alongside the body, summary and keywords. Bounded condition phrases
precede body tokens so long records cannot exhaust the index budget before
the conditions are included. The same Korean normalizer is
used for new writes, replacements and bounded index backfill. Domain, evidence
and semantic-kind labels do not become generic search terms. Descriptive dates
do not change lifecycle dates or make an unapproved memory eligible for recall.

The HTTP/PostgreSQL regression captures a tool observation, requires human
approval, then retrieves it using only condition terms. It checks returned
context, scope, unrelated queries and deletion. The schema upgrade regression
checks old indexes are refreshed without changing canonical records, and that
paused recovery remains paused. See [database migrations](database-migrations.md)
for upgrade and rollback. These deterministic tests do not measure extraction
completeness or semantic retrieval accuracy on live conversations.

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

New memory extraction uses `capture-extract-v5` and `harness-session-v6`.
One-off permissions, budgets and assignment corrections belong in session state;
durable candidates retain the named project and every condition. Session
user-statement context fields must be supported by verbatim quotes from the same
original user message. Separate messages cannot be combined to justify a condition.
Host-created fragments may share that origin only at the same revision, including
authorized prior evidence; absent or malformed fragment metadata keeps them separate.
Inferred scope diagnostics belong in state uncertainties, not in quoted condition fields.
This literal check prevents invented metadata wording; it does not prove semantic
completeness. Older prepared requests retain their original delivery contract.
An older session quality gate does not enable the new prompt's fast model.
Inherited scope stays in the statement, with exact original citations. Context
fields copy the actual subject or condition phrase; they do not concatenate a
name and a subject from different clauses, replace the scope with an exception,
or invent a translated name. Previously prepared v4/v5 context still requires
literal evidence.

The full-text lane omits English question scaffolding from query terms while
preserving negation, quantities and temporal operators. Queries with at least
three distinct topic concepts need two distinct matches, including descriptive
memory context. This check runs before the candidate limit and also applies to
rows awaiting backfill. Short lookups remain permissive; exact keyword,
semantic and relation retrieval keep their independent contracts. These rules
reduce lexical noise; they are not a semantic relevance guarantee.
Korean backup/copy and restore/recovery wording is expanded as a single concept
per pair. The alternative spellings cannot count as two independent matches.

On Windows, state-file publications to the same destination may wait up to two
seconds for their turn. Transient replacement retries retain their separate
500 ms budget. These bound additional waits and attempts, not kernel or scheduler
latency; neither bound is a promise of absolute write completion time.

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

The release workflow builds only the Windows app.

Values compiled into the app:

| Name | Use | Default |
|---|---|---|
| `ARKISM_DEFAULT_SERVER` | First server address built into the app | `http://127.0.0.1:8091` |
| `ARKISM_DEFAULT_WORKSPACE`, `ARKISM_DEFAULT_PROJECT` | Scope a server connection opens first | `default` |
| `ARKISM_UPDATE_FEED` | HTTPS update feed the app checks | update check off |
| `ARKISM_SERVER_BIN` | `arkism` path tried first on a server | `/srv/arkism/bin/arkism` |

## Releases

Pushing a `vX.Y.Z` tag runs [`.github/workflows/release.yml`](../.github/workflows/release.yml). It builds only the Windows installer, checks that it starts its built-in Arkism and can back it up and restore it, and uploads it with `latest.json` as a **draft release**. A maintainer publishes it. Signing happens only when the signing secrets exist (see the workflow header).

The Windows app checks the update feed and installs new versions in the app.

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

### Memory storage and management evidence

Storage/management is separate from recall ranking. Raw session archives, capture
transport copies, work checkpoints, pending candidates and active memories retain
their existing independent lifecycles. A successful citation validator checks literal
support; it does not establish semantic completeness or an actual human decision.

The pending review screen groups similar proposals for display only. Four-part scope,
kind, reach, repository, applicability, exceptions, effective date and polarity
must agree; legacy proposals without applicability only group by normalized exact text.
Each proposal keeps its own revision, sources and review action. A repeated statement
never approves a proposal or raises trust. Expanding a group checks at most32 proposals,
two requests at a time. The evidence endpoint reads at most64 original message references
and counts distinct canonical user turns only where the candidate's literal citation
occurs. Conversation-window membership alone does not count. Expired, reconstructed,
unreadable, inferred and unmatched evidence stays unknown. Candidate source tracing
also provides the existing privacy-fenced original-message reader before approval.

New explicit review decisions store a server receipt id, authenticated actor role and
channel. These identify a human-role account request, not physical human presence.
Historical role labels, trust1, user-speech tags or old `person` flags are not upgraded
into this evidence. Existing memories and trust values are preserved. Recall exposes
`approval_origin` as provenance without changing relevance or usage ranking.

Human review reminders (`review_due_at`) are independent of expiry. Scheduling/clearing
uses the current revision, source checks and an atomic audit, and refuses a pending
correction. An overdue reminder appears in the review list; it never approves, deletes,
expires or renews original archives. Correction-pending warnings reuse the existing
conflict guard. Server delivery and answer-text overlap are displayed separately from
actual answer use; a zero overlap is not proof of uselessness.

Capture copy expiry leaves bodyless, mode0600 local receipts for seven days, capped at256.
They record copy removal and whether extraction was prepared; successful delivery stays
unverified. Raw paths, utterances, session ids and model responses are excluded. Failed
receipt writes do not extend raw-copy retention. Session extraction records bodyless
withholding reason/window diagnostics only while collection policy permits persistence.
`capture stats` reports expired transport copies, receipt failures and validation holds.
Neither these counters nor successful literal checks claim complete collection or
semantic extraction coverage.

Deletion displays distinguish request acceptance, server stage reports and unverified
external copies. The desktop keeps at most32 bodyless receipt ids per server/scope for
seven days; it reads status on demand. The optional archive audit has a25-row/2second
bound, exact scope and human authorization. A full page is partial; errors/timeouts
return unknown without a residue count. It never attests external backups or client
caches. Restore receipt-ledger and publication-review gates remain in force.

The approval-origin review list surfaces existing active memories whose new explicit
review receipt is missing. Listing them never changes their active status or trust;
the historical attribution remains unproven until a separately authorized review flow.

Desktop runtime launches omit inherited Windows shell pseudo variables (for example
`=C:` drive-directory state) from their explicit application environment. Programs
and working directories are already absolute. Ordinary inherited variables, command
overrides/removals and replay snapshots remain intact. Explicit malformed names still
reach the existing native rejection boundary; the native validator is unchanged.

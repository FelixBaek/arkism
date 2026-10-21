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
| `desktop/ui` | Desktop app source (ES module entry, screen modules and legacy adapters) |
| `desktop/frontend` | Pinned esbuild tools; build dependencies never enter Tauri assets |
| `api/openapi.yaml` | HTTP API description |

## Requirements

| Tool | Version and use |
|---|---|
| Go | The version in `go.mod`. Tests use `-race`, so a C compiler (gcc) is also needed |
| PostgreSQL | Postgres 17 with pgvector, only for integration tests and server mode. The `vector` extension must live in the `extensions` schema |
| Docker | Only for `tools/check_postgres_local.sh` |
| Node.js | 22 with npm, for UI tests and the Windows frontend bundle |
| Python | 3, for the UI preview |
| Rust (stable) | Only to build or test the desktop app |

## Checks

Local checks, with no credentials (install the pinned minisign verifier for the signing regression suite first):

```bash
bash tools/install-minisign.sh /tmp/arkism-test-tools
export MINISIGN=/tmp/arkism-test-tools/minisign
python3 tools/check_local.py
```

Individual checks:

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

Memory extraction and retrieval contracts: [design notes](design-notes/memory-contracts.md).

## UI preview

The desktop screens can run in a browser against made-up data, without a real Arkism:

```bash
python3 -I desktop/ui/testdata/preview/serve.py 8795
# open http://127.0.0.1:8795/index.html
```

`desktop/ui/testdata/preview/shot.mjs` takes screenshots of it with Playwright.
The regression harness uses the locked `playwright-core` in `desktop/frontend`:

```bash
npm ci --prefix desktop/frontend --no-audit --no-fund
npm exec --prefix desktop/frontend -- playwright-core install chromium
npm run test:browser --prefix desktop/frontend
```

An existing compatible Chromium can be selected with `CHROMIUM_PATH`. The harness
blocks external requests and uses synthetic native/API fixtures. Set
`ARKISM_UI_DIRECTORY` to `desktop/ui-dist` to check the actual Windows bundle,
and `ARKISM_UI_SCREENSHOTS` to an owned evidence folder.
`ARKISM_LIST_EVIDENCE` selects the list screenshots and request report folder.
The browser suite includes candidate/conflict/skill/trash next-page and retry
flows, real-screen keyboard/typing checks, and a 10,000-row virtualizer fixture.
The `ui-browser-windows` job runs source and production assets on Windows and
compares seven rendered screens. Its remote result must be checked separately.
See [list loading and virtualization](list-performance.md) for exact limits. The structure harness
uses fixed data/time and reduced motion so `compare-ui-snapshots.cjs BEFORE AFTER`
can require identical labels, selection and rendered PNGs. A browser fixture is
separate from a running Windows product or its native WebView.

## Building the desktop app

- Windows: `desktop/installer/build-windows.ps1 -Version X.Y.Z`

The release workflow builds only the Windows app. Both Windows packaging entry
points install the locked tools in `desktop/frontend`, build `desktop/ui-dist`,
validate its source hashes/assets, and pass that folder as Tauri's frontendDist.
Direct `cargo` tests retain the source UI configuration; they do not prove the
production bundle was built.

```bash
npm ci --prefix desktop/frontend --no-audit --no-fund
node desktop/frontend/build.cjs
node desktop/frontend/check.cjs
node --test desktop/ui/*.test.js
```

`desktop/ui/index.html` uses the source `entry.mjs` for browser development.
Production uses one esbuild module graph with a separate lazy English chunk.
Keep `desktop/ui-dist` and `desktop/frontend/node_modules` in local Git excludes.
See [UI ownership and validation](ui-structure-improvement-plan.md).

Values compiled into the app:

| Name | Use | Default |
|---|---|---|
| `ARKISM_DEFAULT_SERVER` | First server address built into the app | `http://127.0.0.1:8091` |
| `ARKISM_DEFAULT_WORKSPACE`, `ARKISM_DEFAULT_PROJECT` | Scope a server connection opens first | `default` |
| `ARKISM_UPDATE_FEED` | HTTPS update feed the app checks | update check off |
| `ARKISM_UPDATE_PUBLIC_KEY` | Base64 of the complete minisign public-key file compiled into the Windows app | updates refused without a valid key |
| `ARKISM_UPDATE_CHANNEL` | `stable` or `beta` signed manifest | `stable` |
| `ARKISM_SERVER_BIN` | `arkism` path tried first on a server | `/srv/arkism/bin/arkism` |

Coverage floors, measured complexity, repository rules and runner setup:
[quality and maintenance](quality-and-maintenance.md).

## Releases

Pushing a `vX.Y.Z` tag runs [`.github/workflows/release.yml`](../.github/workflows/release.yml). It builds only the Windows installer, checks that it starts its built-in Arkism and can back it up and restore it, and uploads it with `latest.json` as a **draft release**. A maintainer publishes it. Minisign update authentication, provenance and the signed checksum inventory are mandatory for public releases. Authenticode remains optional. See [Windows update security](security-operations.md).

The Windows app checks the update feed and installs new versions in the app.

## API contract checks

The OpenAPI document remains JSON-compatible YAML. Its pinned official 3.1
structure schema is vendored under `api/schema`; routine checks need no schema
network access. In a disposable validation environment:

```bash
python -m pip install -r tools/requirements-api-contract.txt
python tools/check_openapi.py
python -m unittest discover -s tools -p test_check_openapi.py
```

The Windows CI job runs the actual Go handlers and exports their responses:

```powershell
$env:ARKISM_OPENAPI_CASES = Join-Path $env:TEMP 'arkism-api-cases.json'
go test -count=1 -run '^TestMemoryList|^TestHTTPRoutes|^TestOpenAPI' ./internal/memory
python tools/check_openapi.py --cases $env:ARKISM_OPENAPI_CASES
```

Actual response-schema coverage includes the memory-list pagination/search cases
and all 60 management operations mapped in `api/management-contracts.json`.
The corpus contains successful requests/responses, anonymous access denial and
unknown-body-field rejection for every management POST (170 HTTP responses in
the current fixture). Query values and successful request bodies are validated,
not just response examples. In-memory storage, a local notification fixture and
a deterministic model stand in for dependencies; this is not a live provider or
PostgreSQL integration test.

All 271 production registrations must match the spec in both directions. There
is no undocumented-route allowlist. The reviewed manifest owns summaries, query
semantics and required request fields. Go AST extraction owns DTO JSON names,
types, nullability and omitempty, including the record envelope. Regenerate,
review the diff, and check for source drift:

```bash
python tools/update_api_contracts.py
python tools/update_api_contracts.py --check
```

The generator fails on unknown DTO syntax. It uses only the local Go standard
library; it does not start the service or download schemas. Runtime checks still
need the actual handlers: source shapes alone cannot prove field semantics or
endpoint authorization. Gateway extension descriptions belong to another
process and are excluded from management-route coverage.

### Management lists and daemon structure

Full-scope management search uses the four documented `POST .../query` routes;
see [list-performance.md](list-performance.md) for filters, cursors and cost limits.
`GET /v1/usage/sessions` now supports global ordering and cursor continuation.
The DTO manifest covers 65 management operations; OpenAPI covers 275 routes.
`tools/update_api_contracts.py --types <exported-AST.json>` regenerates/checks
contracts using existing parser metadata without compiling an application.
The default invocation still runs the Go AST exporter.

`internal/memory/http.go` owns middleware, deadlines, response handling and the
small dynamic route tables. `http_core_*.go` owns the existing domain handlers;
route registration still goes through the common authorization/payload policy.
`cmd/memoryd/main.go` owns startup sequencing. `daemon_options.go` resolves one
explicit execution mode, `daemon_environment.go` validates environment and
Postgres settings, `daemon_maintenance.go` owns offline operations,
`daemon_service.go` composes service dependencies, and `daemon_runtime.go` owns
HTTP/worker lifecycles. Flags remain compatible, but combining exclusive modes
now fails before opening the database. Recovery permits only API/worker modes.
The frozen complexity baseline is unchanged; existing large functions elsewhere
remain visible technical debt, not a claim of complete server simplification.

### CI와 제품 빌드 실행

일반 PR/main CI는 Go/Rust 테스트 대상과 불활성 fixture를 컴파일하고 원본 UI를
검사한다. 제품 실행파일·설치파일·프런트엔드 배포 번들은 생성하지 않는다.
배포 번들 및 원본/번들 비교는 Checks 수동 실행의 `product_build_checks=true`를
명시했을 때만 실행한다. Release의 Windows 설치파일 생성도 PR에서는 건너뛰며
승인된 태그·수동 릴리스 실행에서만 수행한다. 따라서 PR의 CI 성공만으로
설치파일·업그레이드·배포 번들 검증까지 완료했다고 판단하지 않는다.

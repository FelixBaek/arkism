# Server mode

Most people only need the desktop app in built-in mode. Run a server when several PCs, or several people, should share one Arkism.

A server is the same Arkism API (`memoryd`) and worker, backed by Postgres with pgvector. The desktop app reaches it over SSH.

## Connect the desktop app over SSH

The app connects with one SSH address and nothing else. It runs `arkism connect-info` on the server over SSH, reads the API address, owner token and scope, and forwards the API port to the PC through the same SSH login. The forward restarts on its own if it drops.

On the server:

1. Build and install the `arkism` tool:

   ```bash
   go build -o arkism ./cmd/arkism
   sudo install -d /srv/arkism/bin /srv/arkism/config
   sudo install -m 0755 arkism /srv/arkism/bin/arkism
   ```

2. Save the owner token where your SSH user can read it, and nobody else:

   ```bash
   printf '%s\n' 'OWNER-TOKEN' | sudo tee /srv/arkism/config/owner.token >/dev/null
   sudo chown "$USER" /srv/arkism/config/owner.token
   sudo chmod 0600 /srv/arkism/config/owner.token
   ```

3. Check it: `/srv/arkism/bin/arkism connect-info` prints the connection details as JSON.

The defaults are the API at `http://127.0.0.1:8091` and the token at `/srv/arkism/config/owner.token`. If yours differ, set `ARKISM_URL` and `ARKISM_TOKEN_FILE` for the SSH user. The app looks for `arkism` in `/srv/arkism/bin` and on `PATH`; the connection form also has a field for another path.

On the PC:

1. Make sure `ssh me@my-server` works from a terminal on this PC. With an SSH key or agent there is no password prompt. A password works too: type it in the app, and after the login works it is kept in the OS credential store (Windows Credential Manager, macOS Keychain, Linux Secret Service), never in a file. Windows uses the built-in OpenSSH client.
2. In the app, open **Settings › 연결 (Connection)**, type `me@my-server` (or a `~/.ssh/config` alias, optionally with `:port`) and connect. The port, key file and password are under **자세히 (Details)**.
3. A server the app has not seen before shows its host key fingerprint first. Approve it only when it matches the one your server admin gave you; a password is sent only to an approved host.

### Keep the server at the app's version

`GET /healthz` (no token) reports `version`, `api_level` and `features`. When the server is older than the app, the app shows one banner and turns off only the features that server lacks.

The banner's **서버 업데이트** button installs the server bundle with the app's own version from the GitHub release (`arkism-server-<version>-linux-<amd64|arm64>.tar.gz`). The PC checks the bundle's signature with the same key it uses for app updates, then sends it over the same SSH login. On the server it:

1. finds the install folder (`/srv/arkism/bin`, the form's path, or `arkism` on `PATH`) and the running systemd services that start programs from it,
2. backs up `arkism`, `arkism-crew`, `memoryd` and `memory-principal` to `.backup-<time>` in that folder,
3. swaps in the new files, restarts those services and waits for `/healthz` to report the new version,
4. restores the backup and restarts again if the new version does not come up.

The SSH user needs to be root, or have passwordless sudo (`sudo -n`) for the copy and `systemctl restart`. If something is missing (write access, sudo, a systemd service), the app stops before changing anything and says what is needed. Database migrations are not run: if the new `memoryd` needs one, the update rolls back; run `memoryd --init-db` with the migration credentials, then update again.

### Open the screens in a browser

Without the desktop app, `memoryd` can serve the same screens to a browser on your private network. It is off by default.

1. Build the UI once: `npm ci --prefix desktop/frontend --no-audit --no-fund && node desktop/frontend/build.cjs` (output: `desktop/ui-dist`).
2. Copy that folder to the server and set `ARKISM_WEB_UI_DIR=/srv/arkism/web-ui` for `memoryd`, then restart it. `GET /` now redirects to `/app/`. `memoryd` refuses to start if the folder is not a build (it has no `.arkism-frontend-output` file), so the source folder with its tests is never served.
3. Make `memoryd` reachable from your network (`--listen`, or a reverse proxy with TLS). Do not expose it to the internet.
4. Open `http://my-server:8091/app/` and paste an Arkism access token. The browser keeps it only for that tab (session storage) and sends it as a Bearer header; `/v1` authorization does not change.

SSH connection, the built-in Arkism, backups and agent setup stay in the desktop app; the browser shows a short note there instead.

## Install by hand

1. Prepare Postgres 17 with pgvector and two roles: `arkism_init` owns the schema, `arkism_app` is used by the API and the worker. The `vector` extension must live in the `extensions` schema. The exact SQL is in [`tools/check_postgres_local.sh`](../tools/check_postgres_local.sh) and [`tools/roles.sql`](../tools/roles.sql).
2. Create and check the schema with the `arkism_init` connection:

   ```bash
   export MEMORY_DATABASE_URL='postgres://arkism_init:PASSWORD@127.0.0.1:5432/arkism?sslmode=disable'
   go run ./cmd/memoryd --init-db
   go run ./cmd/memoryd --check-indexes
   ```

3. Register an owner token and a worker token, still with the `arkism_init` connection. The token is read only from `MEMORY_PRINCIPAL_TOKEN` and is never printed. `'*'` grants every action in that project; list actions (for example `read:memories`) to narrow it.

   ```bash
   install -d -m 700 "$HOME/.local/share/arkism-admin"
   go run ./cmd/memory-principal --generate-token-file "$HOME/.local/share/arkism-admin/owner-token"
   go run ./cmd/memory-principal --generate-token-file "$HOME/.local/share/arkism-admin/worker-token"
   MEMORY_PRINCIPAL_TOKEN="$(cat "$HOME/.local/share/arkism-admin/owner-token")" go run ./cmd/memory-principal \
     --principal management:owner --role human --organization default --project default --actions '*'
   MEMORY_PRINCIPAL_TOKEN="$(cat "$HOME/.local/share/arkism-admin/worker-token")" go run ./cmd/memory-principal \
     --principal worker --role service --organization default --project default --actions '*'
   ```

   Tokens expire within 90 days. Existing grants do not extend that lifetime.
   See [token generation, rotation and upgrade](principal-tokens.md) for the
   Windows commands and the legacy-token transition period.

4. Put the settings in an env file of your own, with `MEMORY_DATABASE_URL` set to the `arkism_app` connection, and start the API and the worker. `memoryd` does not read the file by itself.

   ```bash
   set -a; . ./arkism.env; set +a
   go run ./cmd/memoryd             # API, default 127.0.0.1:8091
   go run ./cmd/memoryd --worker    # embedding and index worker (needs MEMORY_EMBEDDER)
   ```

`/healthz` says the process is alive. `/readyz` says the schema, extensions and indexes are ready. Indexes are never created at startup.

## Use Supabase Cloud as the database

Arkism only needs Postgres with pgvector, so a Supabase project works as a managed database (backups and upgrades are done by Supabase). Arkism does not use Supabase Auth, Storage or Realtime.

1. Create a project. Supabase already keeps extensions in the `extensions` schema; enable `vector` there (Database › Extensions).
2. In the SQL editor, run [`tools/roles.sql`](../tools/roles.sql) with two changes: the database is `postgres` (not `arkism`), and put the two passwords in place of `:'init_pw'` and `:'app_pw'`.
3. Use the **direct connection** or the **session pooler** (port 5432). The transaction pooler (port 6543) does not keep a session, which memoryd needs. Add `sslmode=require`:
   ```sh
   export MEMORY_DATABASE_URL='postgres://arkism_init.PROJECT_REF:PASSWORD@aws-0-REGION.pooler.supabase.com:5432/postgres?sslmode=require'
   go run ./cmd/memoryd --init-db
   ```
4. Run the API and worker with the same URL but the `arkism_app` role.

## `memoryd` flags

Choose exactly one execution mode per invocation. API is the default; worker,
initialization, reindex, snapshots and offline maintenance are mutually exclusive.
Combining them is rejected before connecting to the database. `--recovery` is
allowed only with the API or worker mode. Existing individual flags and defaults
remain supported.


| Flag | What it does |
|---|---|
| (none) | Run the HTTP API (`--listen`, default `127.0.0.1:8091`) |
| `--worker` | Run the embedding and index worker instead |
| `--init-db` | Apply pending [versioned migrations](database-migrations.md) with the migration owner |
| `--check-indexes` | Check migration history, schema, search indexes and row-level security |
| `--semantic-index create\|drop` | Create or drop the optional pgvector index (`MEMORY_SEMANTIC_RETRIEVAL=1`) |
| `--reindex` | Queue embedding jobs for records the current embedder has not indexed, then exit |
| `--collect-blobs` | Remove files nothing points to |
| `--adopt-restored-blobs`, `--recovery` | Restore steps, see [backup-restore.md](backup-restore.md) |

## Configuration

A minimal env file, with fake values:

```sh
MEMORY_STORE=postgres
MEMORY_DATABASE_URL=postgres://arkism_app:change-me@127.0.0.1:5432/arkism?sslmode=disable
MEMORY_EMBEDDER=none                  # none | tei | embed_texts | embed_passage | ollama | gemini; the worker needs one
MEMORY_LOCAL_STANDALONE=true          # API listens on loopback only
MEMORY_WORKER_TOKEN=example-worker-token
```

Keep the file out of Git. All settings:

| Name | Meaning |
|---|---|
| `MEMORY_DATABASE_URL` | Postgres connection (required). API and worker use the `arkism_app` role |
| `MEMORY_EMBEDDER` | `none`, `tei`, `embed_texts`, `embed_passage`, `ollama` or `gemini`. Required for the worker (see [Model providers](#model-providers)) |
| `ARKISM_MODEL_SIDECAR` | Optional local model server memoryd starts and stops itself, as a JSON argv array (for example `["/opt/models/server","--port","8089"]`). See [Local models](#local-models) |
| `MEMORY_LOCAL_STANDALONE` | `true` for single-owner mode. The API then listens on loopback only |
| `MEMORY_WORKER_TOKEN` | Service token for the worker and readiness checks |
| `MEMORY_CANDIDATE_DAILY_LIMIT` | Daily cap (1–100000) for AI-written memory candidates. Unset means none are accepted |
| `MEMORY_CAPTURE_MERGE_SIMILARITY` | Cosine similarity (0–1, default 0.92) at which a captured claim gets a merge proposal toward the closest approved memory instead of a plain review entry. Needs an embedder; a person still decides |
| `MEMORY_BLOB_DIRECTORY` | Folder for attached files. With object storage on, it keeps only the lock and upload staging |
| `MEMORY_BLOB_S3_ENDPOINT` | Optional S3-compatible object storage URL (https, or http on loopback). Set it with `_BUCKET`, `_REGION`, `_ACCESS_KEY_ID`, `_SECRET_ACCESS_KEY`, and optionally `_PREFIX` and `_SESSION_TOKEN` (all `MEMORY_BLOB_S3_*`) to keep attached files there. Deletion and erasure delete the objects. Backups list the stored objects (digest and size) without copying them; restore checks they are there |
| `MEMORY_BLOB_S3_ADDRESSING` | `path` (default) puts the bucket in the URL path; `virtual` puts it in the host name (`https://<bucket>.<endpoint host>/`), for providers that only serve that form. Other providers' S3-interoperable endpoints work with their endpoint URL, the region name they document (for example `auto`), and their HMAC access keys |
| `MEMORY_BLOB_SHAREDKEY_ENDPOINT` | Optional object storage that speaks the Shared Key blob protocol, instead of S3: the account's blob service URL (https). Set it with `_ACCOUNT`, `_CONTAINER`, `_KEY` (the base64 account key) and optionally `_PREFIX` (all `MEMORY_BLOB_SHAREDKEY_*`). Set this or `MEMORY_BLOB_S3_*`, not both |
| `MEMORY_DOCUMENT_CONVERTER` | Optional absolute path of a converter run as `<path> <file>` that prints text, for the extensions listed in `MEMORY_DOCUMENT_CONVERTER_EXTENSIONS` (for example `.epub,.rtf,.odt`). `.doc`, `.xls` and `.ppt` have built-in readers; listed there, the converter reads the files those readers cannot. It runs without a shell, with an empty environment, in its own temporary folder (removed afterwards) and process group (stopped as a whole on timeout), for at most 60 seconds, with 4 MiB of output. Other unsupported formats get 415 |
| `MEMORY_FILE_VISION` | `1` when the server model (`SKILL_LLM_*` with `ARKISM_WIKI=on`) takes image input: uploaded png/jpg/webp images and scanned PDFs (JPEG page images, up to 8 pages) are read by it for memory extraction. Off by default; without it these uploads get 415 |
| `MEMORY_TRANSCRIBE_ENDPOINT` | Optional speech-to-text URL (https, or http on loopback) that takes multipart `file` + `model` and answers `{"text": ...}`, the common `/audio/transcriptions` protocol. Set `MEMORY_TRANSCRIBE_MODEL` and, if needed, `MEMORY_TRANSCRIBE_API_KEY`. Uploaded mp3/wav/m4a/ogg/webm/flac files (at most 4 MiB) are then transcribed for memory extraction; without it they get 415 |
| `MEMORY_SESSION_ARCHIVE_KEY` | 32-byte hex key that encrypts archived session transcripts |

Values the `arkism` tools read:

| Name | Default | Meaning |
|---|---|---|
| `ARKISM_URL` | `http://127.0.0.1:8091` | Arkism API address |
| `ARKISM_TOKEN_FILE` | `/srv/arkism/config/owner.token` | Token file the tools use |
| `ARKISM_WORKSPACE`, `ARKISM_PROJECT`, `ARKISM_USER` | `default`, `default`, `management:owner` | Scope the tools read and write |
| `ARKISM_SKILLS_ROOT` | `~/skills` | Shared skill folder (`SKILL.md` files) |
| `ARKISM_DATA_DIR` | `~/.local/share/arkism` | Skill catalog data. Keep it out of Git |
| `ARKISM_GIT_ORG` | none | GitHub user or organization the desktop TOOLS › 저장소 screen shows. The app runs `arkism git` over SSH, which does not load a systemd `EnvironmentFile`, so set it in the SSH user's shell (for example an `export` at the top of `~/.bashrc`). Unset, the screen reports `git_org_unset` |
| `ARKISM_SESSION_PROCESSOR` | unset (off) | `1` switches the capture hooks to session-processor mode: finished sessions are summarized by the harness models and memory candidates are proposed automatically. Needs `ARKISM_HARNESS_TOKEN_FILE` |
| `ARKISM_HARNESS_TOKEN_FILE` | none | Service token the session processor captures with, so automated capture never runs as the owner |
| `ARKISM_MEMORY_OUTCOME_SPOOL` | unset (off) | Directory for memory outcome receipts. See [Memory outcome spool](#memory-outcome-spool) |

### Memory outcome spool

With `ARKISM_SESSION_PROCESSOR=1` and `ARKISM_MEMORY_OUTCOME_SPOOL=/srv/arkism/state/outcome-spool`
set for the capture hooks (and for `arkism-crew serve`, whose finished runs become traces the same way),
every recall writes the exact memory versions it delivered to a per-session ledger under the capture state
directory. Once a session has been idle for 30 minutes the next `arkism capture reflect` turns the ledger
into execution traces on the server and drops a receipt into the spool. The ledger and the receipts hold
references only (memory id, revision, scope), never memory text or transcript lines, so the spool adds no
new personal data to disk.

The spool stays off by default for two reasons that are about the install, not privacy:

- Nothing drains it on its own. `arkism capture outcomes --spool DIR --config FILE --evaluator-token-file F --producer-token-file G`
  consumes the receipts once per call and needs a second service credential plus a checker configuration;
  the repository ships no unit or timer for it. A spool nobody drains only grows.
- It is a side effect of the session processor, which is itself opt-in because it sends session text to
  the harness models.

Turn it on when you run the processor and want outcome evidence: add the two variables above to
`/srv/arkism/config/arkism.env` (the file the hook wrapper loads), create the directory with mode `0700`
for the hook user, and schedule `arkism capture outcomes` (a systemd timer or cron entry, daily is enough)
with the same `--spool` directory. `arkism capture reflect` reports `session_trace_recorded`,
`session_trace_pending` and `session_trace_dropped` in its metrics so you can see the ledger moving.

## Upgrade to the memory review features

This release adds observations, topic summaries, rules, answers from memory,
names, causal links, file extraction, webhooks, space bundles and the memory
wiki. What a person approves and how is described in
[reviewing model-written content](model-written-review.md).

1. **Migration.** Back up, stop the API and worker, and run the new binary's
   `memoryd --init-db` as `arkism_init` ([database migrations](database-migrations.md)).
   It applies `0006_named_entities`.
2. **Server model.** Answers (`/v1/reflect`), observation runs, topic summary
   refreshes, causal link proposals, file extraction, text extraction preview
   and model name extraction call the server model. Configure it on the API:

   | Name | Meaning |
   |---|---|
   | `SKILL_LLM_ENDPOINT_URL`, `SKILL_LLM_API_KEY`, `SKILL_LLM_MODEL` | Chat model the server calls |
   | `SKILL_LLM_PROTOCOL` | Empty for the common chat-completions protocol, `messages` for the messages protocol, `responses` for the responses protocol (see [Model providers](#model-providers)). Any other value stops startup |
   | `SKILL_LLM_BASE_URL` | Base URL instead of `SKILL_LLM_ENDPOINT_URL` (for example `https://api.example.com/v1`); the protocol's path (`/chat/completions`, `/messages`, `/responses`) is added. Setting both stops startup |
   | `SKILL_LLM_PRESET` | `local`, `runner`, `proxy` or `gateway`: a known kind of server, so no endpoint is needed (see [Server model presets](#server-model-presets)). Unset by default |
   | `ARKISM_WIKI` | `on` turns the server model on for the features above. Off by default |
   | `ARKISM_LLM_TRACE_CONTENT` | `1` keeps redacted prompt text in model call traces. Off by default |

   Without it these routes answer `503 model_not_configured`; everything else
   keeps working. Calls share one daily cap and are recorded in the usage ledger.
3. **Optional settings.**

   | Name | Meaning |
   |---|---|
   | `MEMORY_WEBHOOK_KEY` | 32-byte hex key that seals webhook signing secrets. Required to create webhooks; an invalid value stops startup |
   | `ARKISM_WEBHOOK_ALLOW_PRIVATE` | `1` lets webhooks reach loopback and private addresses (development only). Link-local and metadata addresses stay refused |
   | `ARKISM_RERANK_URL`, `ARKISM_RERANK_TIMEOUT` | Base URL of a `POST /rerank` service and its timeout (Go duration, at most `1m`). On error or timeout recall keeps the fused order |
   | `ARKISM_RERANK_MODEL`, `ARKISM_RERANK_API_KEY` | Model name and bearer key of a hosted rerank API. Setting the model switches to the hosted request shape (see [Model providers](#model-providers)) |
   | `ARKISM_RERANK_PROTOCOL` | Empty, `passages` or `records`: two other hosted rerank shapes. With either, `ARKISM_RERANK_URL` is the full endpoint and `ARKISM_RERANK_MODEL` is required. Any other value stops startup |
   | `ARKISM_RERANK_API_KEY_FILE` | File holding the rerank bearer key, read on every call instead of `ARKISM_RERANK_API_KEY`, so a short-lived access token can be rotated on disk without a restart |
   | `ARKISM_RERANK_LOCAL` | `1` without `ARKISM_RERANK_URL` turns on an in-process lexical (BM25) reranker. It sends nothing off the server, so it does not need `external:transfer`; it is weaker than a cross-encoder. Off by default, so an upgrade does not change recall order. Calls are filed in the usage ledger as provider `local`, model `lexical` |
   | `ARKISM_OTLP_ENDPOINT`, `ARKISM_OTLP_SERVICE_NAME`, `ARKISM_OTLP_SAMPLE_RATIO` | Request tracing (see [Tracing](#tracing)). Off by default |
   | `ARKISM_OTLP_METRICS_ENDPOINT`, `ARKISM_OTLP_METRICS_INTERVAL` | Metrics export (see [Metrics](#metrics)). Off by default |
   | `ARKISM_PROMETHEUS`, `ARKISM_PROMETHEUS_TOKEN` | `GET /metrics/prometheus` (see [Metrics](#metrics)). Off by default |
   | `ARKISM_LLM_TRACE_OPERATIONS`, `ARKISM_LLM_TRACE_EXCLUDE_OPERATIONS` | Which model call operations get a trace row (see [Model call traces](#model-call-traces)). Empty records all |
   | `ARKISM_LLM_TRACE_RETENTION_DAYS` | Days model call traces are kept by `memoryd --audit-expiry execute` (default `30`) |
   | `ARKISM_RECALL_GRAPH` | `1` adds the relations-graph recall lane. Off by default |
   | `ARKISM_SPACE_DEFAULT_TEMPLATE` | Path to a space template bundle (`POST /v1/spaces/{workspace}/{project}/export` with `template: true`). Every space inherits the settings it leaves empty from the template's config, the template's defense rules apply to every space, and `POST .../import` with `default_template: true` copies its directives and mental model definitions into a space without settings. An unreadable file stops startup |
   | `ARKISM_SPACE_TEMPLATE_AUTO` | `1` applies the default template's directives and mental model definitions to a space by itself: when a person saves the space's first settings (their settings win), or on a person's first stored memory in a space without settings (the template's settings are stored too). The definitions arrive switched off (directives inactive, mental models on manual refresh) until a person turns them on in that space. Each space gets it once per server run; agent writes never apply it, since the definitions are human-only. Off by default. `GET /v1/spaces/template-schema?workspace_id=…&project_id=…` (`read:space_configs`) returns the JSON schema of a template bundle line |
   | `MEMORY_AUTO_DERIVE` | `on` lets the worker run observation, causal link and model name proposals by itself after approvals. Off by default, so an upgrade does not start spending the daily model calls. Needs the server model; results stay proposals for a person. This is the server default: a space's `auto_derive` setting (`PUT/PATCH /v1/spaces/{workspace}/{project}/config`, or the default template) overrides each field |
   | `MEMORY_AUTO_DERIVE_THRESHOLD`, `MEMORY_AUTO_DERIVE_INTERVAL` | Approvals that start a run at once (1-1000, default `10`) and the longest wait after the first counted approval (Go duration, `1m` to `720h`, default `6h`) |

4. **Permissions.** Principals registered with `--actions '*'` (the examples
   above and the desktop app) need nothing. If you list actions, add these:

   | Principal | Actions to add |
   |---|---|
   | Owner (person) | `read:directives`, `write:directives`, `read:observations`, `write:observations`, `read:mental_models`, `write:mental_models`, `read:entities`, `write:entities`, `read:memory_relations`, `write:memory_relations`, `read:wiki_pages`, `write:wiki_pages`, `read:space_configs`, `write:space_configs`, `write:webhooks`, `read:jobs`, `transition:jobs`, `delete:jobs`, `ops:metrics`, `read:audit_events`, `generate:memory_candidates` |
   | Agent (MCP router) | `search:memories`, `generate:reflect`, `read:directives`, `read:observations`, `read:mental_models`, `read:entities`, `read:memory_relations`, `read:memory_candidates`, `read:wiki_pages`, and for the proposal tools `write:directives`, `write:mental_models`, `write:maintenance_proposals`, `read:jobs`, `transition:jobs`, `read:knowledge_sources`, `read:knowledge_documents`; `add_document` also needs `write:memory_candidates`, `generate:memory_candidates` and `external:transfer` |
   | Service that runs model jobs | the job's write action (`write:observations`, `write:mental_models`, `write:memory_relations`, `write:entities`, `generate:entities`, `generate:wiki_pages`) plus `search:memories` and `context:read` |
   | Worker (`MEMORY_WORKER_TOKEN`), only with `MEMORY_AUTO_DERIVE=on` | `write:observations`, `write:mental_models`, `write:memory_relations`, `write:entities`, `generate:entities`, `search:memories`, `context:read`, `external:transfer`. Without them a run ends as `denied` |

   Anything that sends memory text to the server model or out of the server
   also needs `external:transfer`. The audit log names the destination:
   `external:wiki-llm` for server model calls and `external:space_bundle` for a
   space export. These are destinations, not actions you grant.
5. **Delta refresh of topic summaries (optional).** A mental model with
   `refresh.strategy: "delta"` sends the server model only the approved summary,
   the records it cites and the recalled records changed since the approval,
   instead of the whole recall. Every record sent passes the transfer check again;
   if a cited record no longer stands or may not leave the server, the refresh
   falls back to a full rewrite and the approved text is not sent. With nothing
   new the refresh answers `404`. The draft still waits for a person. Existing
   models keep `full`.
6. **Images, scanned PDFs and audio in file extraction (optional).**
   `MEMORY_FILE_VISION=1` and `MEMORY_TRANSCRIBE_*` are off by default because
   each file is a model call. The raw file leaves the server only after a
   `transfer:model_input` decision for destination `external:wiki-llm`
   (images) or `external:transcription` (audio); grant it with
   `external:transfer` like other model input. The calls count against the
   daily model call cap and appear in usage as `wiki_file_vision` and
   `wiki_file_transcribe`. What the model returns goes through the same
   privacy filters and becomes review candidates only. A `dry_run` upload
   never calls these models; it answers with the skip reason `dry_run_media`.
7. **File extraction jobs.** Cancel a running extraction with
   `POST /v1/operations/{id}/cancel`. Operator retry is refused; the uploader
   resumes a failed job with `POST /v1/files/memories/jobs/{id}/run`.
8. **Object storage for attached files (optional).** With `MEMORY_BLOB_S3_*`
   or `MEMORY_BLOB_SHAREDKEY_*` set, attached files live in the bucket, not in
   `MEMORY_BLOB_DIRECTORY`. The server backup does not copy the bucket; with the
   same settings in its environment it lists each object's digest and size, and
   `memoryd --adopt-restored-blobs` now runs with object storage on: it checks
   every listed object is present at its size and uploads the files the bundle
   carried. Back the bucket up yourself
   (bucket versioning or a scheduled copy) at least as often as the database
   backup, and restore it to the same time as the database. Switch storage
   only on an empty blob folder: blob garbage collection stops with an error
   while digest files are still in the folder.
9. **Model rerank (optional, off by default).** A search or context build
   that sends `"rerank": "model"`, or any search in a space whose config sets
   `rerank_model: true`, lets the server model reorder the top 20 results.
   `"rerank": false` still turns every reranker off. Each call spends the
   server model under operation `recall_rerank` (usage ledger and
   `GET /v1/llm-requests`), at most 200 calls per project a day. The caller
   needs `generate:recall_rerank`, and only memories cleared for
   `external:wiki-llm` (classified, not private or restricted, sources
   classified) are
   sent. When anything fails the order stays as it was, and
   `explain.model_rerank` names why. Search, context build and the MCP
   `recall` tool now accept the `rerank` string `"model"` as well as a boolean.
10. **Time words in more languages.** Recall now reads time words ("yesterday",
   "3 months ago", "fin octobre", "Ekim'de") from per-language tables, plus
   ISO weeks (`2026-W44`) and a year alone (`2025년`). Numeric dates with dots
   or dashes are read day first: `03-04-2026` is 3 April, never March 4.
   Dates with slashes are not read, since both orders are common there.
11. **Agent proposal tools.** Agents can now propose directives, mental
    models and invalidations, list and cancel their own jobs, and read
    documents. What an agent creates stays `proposed` until a person saves or
    decides it; open proposals are capped at 20 per scope and kind. Agent file
    uploads (`add_document`) spend the daily model calls, so they are off
    until a person lists `add_document` in the space's `mcp_tools`
    (`PATCH /v1/spaces/{workspace}/{project}/config`). Space bundle import is
    now refused for non-human principals.
12. **Entity names, labels and graph filters.** Extraction now compares a new
    name with the space's entities by trigram similarity. With the new space
    setting `entity_resolution` at its default `propose`, a similar name still
    becomes its own entity and carries `similar` merge proposals that only
    people see (`GET /v1/entities?similar=true`); merge with
    `POST /v1/entities/{id}/merge` or close them with `PATCH` `dismiss_similar`.
    `similar` also links names that differ only in case, punctuation or word
    order; `exact` turns the comparison off. No model is called for this.
    `entity_labels` gives a space a classification schema (groups of
    `value`, `multi-values`, `text` or `multi-text` labels); labels become
    entities of type `label` named `key:value`, and groups with `tag: true`
    copy stated labels into the candidate's tags. Captures and candidates
    accept `entities` [{name, type}] that are applied as written when the
    memory is approved. `entities_allow_free_form: false` keeps only labels,
    and `entity_free_types: true` allows types outside the built-in list; both
    are off the defaults only when set. The entity graph accepts `tags`,
    `tags_match`, `tag_groups` and `min_count`. No migration is needed.

## Model providers

The server reaches models through a few plain HTTP protocols, so most hosted
APIs and self-hosted inference servers work without new code:

| Need | Setting | Protocol |
|---|---|---|
| Chat (server model) | `SKILL_LLM_ENDPOINT_URL`, `SKILL_LLM_API_KEY`, `SKILL_LLM_MODEL` | the common `/chat/completions` protocol |
| Chat (server model) | the same, plus `SKILL_LLM_PROTOCOL=messages` | Messages protocol `POST {url}` `{model, max_tokens, system, messages}` with the key in `x-api-key` and the protocol version header `2023-06-01` → `{content: [{type, text}], stop_reason, usage}` |
| Chat (server model) | the same, plus `SKILL_LLM_PROTOCOL=responses` | Responses protocol `POST {url}` `{model, instructions, input: [{role, content: [input_text, input_image]}], max_output_tokens, store: false}` (JSON answers add `text.format: json_object`; no temperature, which reasoning models refuse) → `{status, output: [{type: message, content: [{type: output_text, text}]}], usage}`. `status: incomplete` counts as cut off |
| Embeddings | `MEMORY_EMBEDDER=tei`, `TEI_ENDPOINT_URL` ending in `/embed` | Text Embeddings Inference `POST /embed` |
| Embeddings | `MEMORY_EMBEDDER=tei`, `TEI_ENDPOINT_URL` ending in `/embeddings` (for example `https://api.example.com/v1/embeddings`), `TEI_API_KEY` | `POST .../embeddings` `{model, input}` → `{data: [{index, embedding}], usage}` |
| Embeddings | `MEMORY_EMBEDDER=embed_texts`, `TEI_ENDPOINT_URL` (the full URL, for example `https://api.example.com/v2/embed`), `TEI_MODEL`, `TEI_API_KEY` | `POST {url}` `{model, texts, input_type: search_query or search_document, embedding_types: [float]}` (`output_dimension: 1024` with `TEI_SEND_DIMENSIONS=1`) → `{embeddings: {float: [[...]]}, meta: {billed_units: {input_tokens}}}` |
| Embeddings | `MEMORY_EMBEDDER=embed_passage`, `TEI_ENDPOINT_URL` (for example `https://api.example.com/v1/embeddings`), `TEI_MODEL`, `TEI_API_KEY` | The `.../embeddings` protocol plus `input_type: query or passage` and `truncate: END` (over-long text is cut instead of refused) |
| Embeddings | `MEMORY_EMBEDDER=ollama` or `gemini` | Their own embedding routes |
| Rerank | `ARKISM_RERANK_URL` | Text Embeddings Inference `POST /rerank` `{query, texts}` |
| Rerank | `ARKISM_RERANK_URL` (for example `https://api.example.com/v1`), `ARKISM_RERANK_MODEL`, `ARKISM_RERANK_API_KEY` | Hosted rerank `POST {url}/rerank` `{model, query, documents, top_n}` → `{results` or `data: [{index, relevance_score}]}` |
| Rerank | `ARKISM_RERANK_PROTOCOL=passages`, `ARKISM_RERANK_URL` (full URL, for example `https://api.example.com/v1/retrieval/<model>/reranking`), `ARKISM_RERANK_MODEL`, key | `POST {url}` `{model, query: {text}, passages: [{text}], truncate: END}` → `{rankings: [{index, logit}]}`; each logit becomes a 0–1 score through a sigmoid |
| Rerank | `ARKISM_RERANK_PROTOCOL=records`, `ARKISM_RERANK_URL` (full URL of the ranking config's `:rank` method), `ARKISM_RERANK_MODEL`, `ARKISM_RERANK_API_KEY_FILE` | `POST {url}` `{model, query, records: [{id, content}], topN}` → `{records: [{id, score}]}`. These services take a short-lived access token: keep it fresh in the key file (for example from a timer) |
| Rerank | `ARKISM_RERANK_LOCAL=1`, no URL | In-process lexical BM25 over the hit texts, best text scaled to 1 |

Notes:

- Vectors must have 1024 dimensions. For a model that shortens its output on
  request, set `TEI_SEND_DIMENSIONS=1` to send `dimensions: 1024`; leave it off
  for servers that reject the field.
- `TEI_QUERY_PREFIX` and `TEI_DOCUMENT_PREFIX` stay required (empty is allowed)
  for `tei`. `embed_texts` and `embed_passage` send the purpose as
  `input_type`, so their prefixes default to empty.
- A proxy gateway that fronts many hosted models serves the
  same `.../embeddings` and `.../rerank` routes: point `TEI_ENDPOINT_URL` and
  `ARKISM_RERANK_URL` at it like any other host.
- Remote endpoints must use https. A loopback endpoint (`localhost`, `127.0.0.1`,
  `::1`) or a literal private address (`10.x`, `172.16-31.x`, `192.168.x`,
  `fd00::/8`) keeps text on your own machines; any other endpoint, including a
  host name that resolves to a private address, needs the caller's
  `external:transfer` grant, like `MEMORY_EMBEDDER=gemini`.
- **Upgrade note.** Before this release every `TEI_ENDPOINT_URL` counted as local.
  Knowledge search and knowledge indexing through an https `TEI_ENDPOINT_URL` on
  a public address or a host name now need `external:transfer` on the caller's
  token. Loopback and literal private-address endpoints are unchanged. Either
  point `TEI_ENDPOINT_URL` at the private address, or grant `external:transfer`.
- Each protocol has its own vector space, so switching endpoint or protocol
  needs a reindex (`memoryd --reindex`).
- Every chat, embedding and rerank call is filed in the usage ledger and in the
  model call traces (`GET /v1/llm-requests`) under its ledger operation (for
  example `embedding_query`, `embedding_document`, `rerank`), with tokens when
  the provider reports them. **Upgrade note.** Embedding traces used to be filed
  as `embed`; filters on that name need the ledger names. The
  rerank protocols are filed as provider `rerank_passages` and `rerank_records`.
- **Upgrade note.** The new protocols, presets and the sidecar are all off
  until set; existing settings behave as before. A set but unknown
  `SKILL_LLM_PRESET` or `ARKISM_RERANK_PROTOCOL` now stops startup.

### Server model presets

`SKILL_LLM_PRESET` names a kind of server, so only the model (and a key where
one is needed) has to be set. `SKILL_LLM_BASE_URL` overrides the preset's
address; `SKILL_LLM_PROTOCOL` still picks the protocol and its path.

| Preset | Address | Key | Default model |
|---|---|---|---|
| `local` | `http://127.0.0.1:8080/v1` (a single-model inference server on this machine) | none | `local` |
| `runner` | `http://127.0.0.1:11434/v1` (a local runner that serves many models) | none | set `SKILL_LLM_MODEL` |
| `proxy` | `http://127.0.0.1:4000/v1` (a chat-completions proxy gateway in front of many hosted models) | `SKILL_LLM_API_KEY` if the proxy wants one | set `SKILL_LLM_MODEL` |
| `gateway` | this host's model gateway, `http://` + `ARKISM_GATEWAY_LISTEN` (default `127.0.0.1:8099`) + `/v1` | the gateway's local token, sent as `x-arkism-gateway-token`: `SKILL_LLM_API_KEY`, else `ARKISM_GATEWAY_TOKEN`, else the token file (`ARKISM_GATEWAY_TOKEN_FILE`) | `group/auto` |

A preset without a key is accepted only on a loopback address, and the
connection is checked again after name resolution, so a key-free call never
leaves the machine even if a name resolves elsewhere. The `gateway` preset
likewise refuses a base URL that is not loopback: its token is this host's own. Hosted APIs that speak one of the three
protocols need no preset: set `SKILL_LLM_BASE_URL` (or the full
`SKILL_LLM_ENDPOINT_URL`), `SKILL_LLM_API_KEY` and `SKILL_LLM_MODEL`.

The `gateway` preset lets the server model use the subscription accounts
signed in on this host (the same accounts agents use through the gateway),
with the gateway's failover, limits and usage records. It works when memoryd
runs on the same machine as the gateway; a container needs host networking
or a loopback forward, since the gateway listens on loopback only. Model names
are the gateway's (`provider/model` or `group/<name>`). Subscription logins
the gateway does not have (other editors' or vendors' device logins) are not
offered: each needs its own sign-in flow, token refresh and terms check in the
gateway first, and the server would only reach them through it.

### Local models

Neural embedding models and cross-encoders do not run inside memoryd: that
needs a native inference runtime and model weights, which would tie every
build and install to them. Run a local inference server instead, either
yourself or as a sidecar memoryd manages:

| Name | Meaning |
|---|---|
| `ARKISM_MODEL_SIDECAR` | JSON argv array of the server to start, for example `["/opt/models/server","--model","/opt/models/embed.gguf","--embedding","--port","8089"]`. Started without a shell |
| `ARKISM_MODEL_SIDECAR_READY_URL` | Loopback `http` URL that answers 200 once the server is ready (for example `http://127.0.0.1:8089/health`). Required with the sidecar |
| `ARKISM_MODEL_SIDECAR_WAIT` | How long startup waits for it (Go duration, default `2m`, at most `30m`) |
| `ARKISM_MODEL_SIDECAR_ENV` | Comma-separated names of extra environment variables the sidecar may see. It otherwise gets only `PATH`, `HOME`, `TMPDIR`, `LANG` (and the Windows equivalents), never the server's secrets: a name starting with `ARKISM_`, `MEMORY_`, `SKILL_` or `TEI_`, or containing `KEY`, `TOKEN`, `SECRET`, `PASS`, `AUTH`, `DATABASE` or the like, stops startup |

memoryd starts the command only when the ready URL does not answer yet (so the
API server and the worker can share one environment file), stops it on exit,
and refuses to start if it exits or is not ready in time. A sidecar that dies
later is not restarted; calls to it fail (recall keeps the fused order) until
memoryd restarts. Point the providers at it:

- embeddings: `MEMORY_EMBEDDER=tei` with `TEI_ENDPOINT_URL=http://127.0.0.1:8089/embed`,
  or `.../v1/embeddings` for servers that speak that protocol;
- rerank: `ARKISM_RERANK_URL=http://127.0.0.1:8089` (`/rerank`), or the hosted
  shape with `ARKISM_RERANK_MODEL` for servers that answer `{model, query, documents}`;
- chat: `SKILL_LLM_PRESET=local` with `SKILL_LLM_BASE_URL=http://127.0.0.1:8089/v1`.

Loopback endpoints keep text on the machine, so they need no `external:transfer`.

## Tracing

Set `ARKISM_OTLP_ENDPOINT` to an OTLP/HTTP traces URL (for example
`https://collector.example.com:4318/v1/traces`; plain http only on loopback) and
the API exports spans as OTLP JSON. Off when unset; nothing is recorded then.

- One span per request, named by its route. A valid W3C `traceparent` header on
  the request continues the caller's trace.
- Child spans for each search lane (`search.lane.exact|fts|vector`) and model call
  (`model.complete`, `model.embed`, `model.rerank`). Requests to a loopback or
  private-address model server carry `traceparent`, so a traced inference server
  joins the same trace; a hosted provider never receives it.
- Spans hold routes, ids, status and fixed error codes only: no memory text,
  queries or tokens. Spans are batched every 2 seconds; when the collector is
  slow, spans are dropped rather than slowing requests.
- `ARKISM_OTLP_SAMPLE_RATIO` (0 to 1, default 1) keeps that share of new traces.
  A caller's `traceparent` decides for its own trace: flags `00` export nothing.
- `GET /metrics` reports `tracing.exported_spans`, `dropped_spans`, `failed_spans`
  and `sample_ratio` while tracing is on. Export never fails a request; stop
  waits at most about 10 seconds for the collector.
- `ARKISM_OTLP_SERVICE_NAME` sets `service.name` (default `arkism-memoryd`).
- Background jobs (wiki and file extraction) open a `job.*` span; the model
  calls of the job are its children.
- Model call spans carry `gen_ai.operation.name`, `gen_ai.provider.name`,
  `gen_ai.request.model`, `gen_ai.response.model` and
  `gen_ai.usage.input_tokens` / `output_tokens`; never prompts or answers.

## Model call traces

Every model call (chat steps, answers, file and media extraction, embeddings,
rerank, the model picker, skill drafts, skill routing and analysis) goes
through one ledger call that also writes a trace row (`GET /v1/llm-requests`).
Each row holds the trace id, span id and parent span id (the job's trace when
the call ran inside a job, so rows of one job group together even with tracing
off), the job id, the source document it worked on, and up to 64 ids of the
memories it read and created.

- `ARKISM_LLM_TRACE_OPERATIONS` (comma list) records only those operations;
  `ARKISM_LLM_TRACE_EXCLUDE_OPERATIONS` skips them. A trailing `*` matches a
  prefix (`embedding_*`). The usage ledger still counts every call.
- Erasing a source deletes its trace rows in the same transaction.
- Rows older than `ARKISM_LLM_TRACE_RETENTION_DAYS` (default 30) are deleted by
  `memoryd --audit-expiry execute` (`preview` counts them first).

## Metrics

`GET /metrics` (`ops:metrics`) stays the scoped JSON view. Two opt-in exports
add process-wide counters (requests, model calls with token histograms per
operation, rerank, tracing, database pool, goroutines, heap, GC, CPU, RSS and
open files). They hold no memory text and no scope data.

- `ARKISM_PROMETHEUS=1` serves `GET /metrics/prometheus` in Prometheus text
  format. With `ARKISM_PROMETHEUS_TOKEN` set, scrapers send it as
  `Authorization: Bearer …`; without a token only loopback peers are served.
  Behind a reverse proxy every peer looks like loopback, so set a token there.
  Off: `404`; refused: `403`.
- `ARKISM_OTLP_METRICS_ENDPOINT` (an OTLP/HTTP metrics URL, for example
  `https://collector.example.com:4318/v1/metrics`; plain http only on loopback)
  pushes the same counters as OTLP JSON every `ARKISM_OTLP_METRICS_INTERVAL`
  (Go duration, default `30s`, at least `5s`), and once more on stop.

## Audit events

`GET /v1/audit-events` lists a scope's audit events and
`GET /v1/audit-events/{id}` reads one (both human `read:audit_events`). Each
event carries redacted request metadata: `transport` (`mcp` when an agent or
service token sends `X-Arkism-Transport: mcp`, as the router tools do; `http`
for every other request, including a person's token that sends the header;
`system` for background work, never taken from a request; the label is the
router's claim, not proof), `method`, the route pattern (never the
raw URL or query) and `duration_ms`. Request and response bodies are never
captured.

A space can narrow its optional events with an `audit` block in its config
(`PUT/PATCH /v1/spaces/{workspace}/{project}/config`):
`{"audit": {"enabled": false}}` drops them, and `{"audit": {"actions":
["read:memories", "search:*"]}}` keeps only the listed ones (at most 64; a
trailing `*` matches a prefix). `{"audit": {}}` records everything again, the
default. Only a person can change the block. Optional events are routine
reads, previews, searches and observations that change nothing stored. These
are always recorded, whatever the block says (the event's `mandatory` field is
`true`):

- denials and policy decisions;
- deletes, erasures, restores, exports, imports, transfers and handoffs,
  privacy requests and holds, settings and policy changes, writes, creates,
  updates, reviews, proposals, job and budget bookkeeping, blocked-content
  inspections and maintenance;
- raw-content reads (session archives and their messages, sources, artifacts
  and source-derived rows);
- any action added later, until it is listed as optional.

If the space settings cannot be read, the event is recorded.

## Host tools and systemd

Run `arkism` with no arguments to list its subcommands (capture hooks, skill sync, gateway, crew runner, Discord, backups and more). `arkism-crew` runs workflows (`serve`, `run CREW_ID`, `hook-url WORKFLOW_ID`).

```bash
sudo install -d /srv/arkism/bin
go build -o arkism ./cmd/arkism && sudo install -m 0755 arkism /srv/arkism/bin/arkism
go build -o arkism-crew ./cmd/arkism-crew && sudo install -m 0755 arkism-crew /srv/arkism/bin/arkism-crew
```

The binary must be executable by uid 65532, because the backup job runs part of it as that user.

To keep these running, write a systemd unit for each long-running command (`memoryd`, `memoryd --worker`, `arkism-crew serve`, `arkism discord run`). Keep binaries in `/srv/arkism/bin` and settings and tokens in `/srv/arkism/config`, and load machine-specific values with `EnvironmentFile=/srv/arkism/config/arkism.env`. The repository does not ship unit files.

Backups for a server: [backup-restore.md](backup-restore.md). Discord bots: [discord.md](discord.md).

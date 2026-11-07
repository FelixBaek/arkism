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
| `--space-repair preview\|execute --space workspace/project` | Repair one space's derived state, then print a JSON report: observations whose cited memories are gone or retired (execute marks them stale, as a live delete would), and bundle text older exports left on job rows (execute clears it). Run `preview` first; it changes nothing |
| `--space-rename preview\|execute --from workspace/project --to workspace/project2` | Move one space to a new project id of the same workspace in one transaction, then print a JSON report of what moved: records (scope columns, documents, and ids derived from the scope, recomputed with every reference to them), graph, traces, code index, memberships, permissions and decision counts. The target must be empty and no job of the space may hold a lease. Stop the server first. Run `preview` first; it counts the same rows and changes nothing. Stored files are keyed by digest and do not move. Privacy deletion requests move with their id, request hash and report reference, and graph relations follow their evidence id. Knowledge sources and bindings, source receipts and consultations keep their ids, and other idempotent writes keep their request hash, because the row does not keep the key or request body the value was hashed from; reads by id still work, but repeating an old request under the new name is not recognized as a repeat |
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
| `MEMORY_CAPTURE_GATE_BUDGET` | How long one capture request may wait on the embedder for that merge check, over all its claims (Go duration, up to 40s, default 15s). Claims past the budget stay ordinary candidates, so a busy embedder no longer turns a capture into a request timeout; their check is queued (job kind `capture_merge_gate`, one per candidate revision) and the worker runs it later with its own credentials, opening the same proposal a person decides. A candidate decided before then is skipped. A worker registered with narrowed actions needs `search:memories`, `context:read` and `external:transfer` for it; without them the queued check ends with a warning in the log and the candidate stays on plain review |
| `MEMORY_BLOB_DIRECTORY` | Folder for attached files. With object storage on, it keeps only the lock and upload staging |
| `MEMORY_BLOB_S3_ENDPOINT` | Optional S3-compatible object storage URL (https, or http on loopback). Set it with `_BUCKET`, `_REGION`, `_ACCESS_KEY_ID`, `_SECRET_ACCESS_KEY`, and optionally `_PREFIX` and `_SESSION_TOKEN` (all `MEMORY_BLOB_S3_*`) to keep attached files there. Deletion and erasure delete the objects. Backups list the stored objects (digest and size) without copying them; restore checks they are there |
| `MEMORY_BLOB_S3_ADDRESSING` | `path` (default) puts the bucket in the URL path; `virtual` puts it in the host name (`https://<bucket>.<endpoint host>/`), for providers that only serve that form. Other providers' S3-interoperable endpoints work with their endpoint URL, the region name they document (for example `auto`), and their HMAC access keys |
| `MEMORY_BLOB_SHAREDKEY_ENDPOINT` | Optional object storage that speaks the Shared Key blob protocol, instead of S3: the account's blob service URL (https). Set it with `_ACCOUNT`, `_CONTAINER`, `_KEY` (the base64 account key) and optionally `_PREFIX` (all `MEMORY_BLOB_SHAREDKEY_*`). Set only one object storage |
| `MEMORY_BLOB_SERVICE_ACCOUNT_ENDPOINT` | Optional object storage that speaks the JSON object API with OAuth service-account keys, instead of S3 HMAC keys: the API base URL (https). Set it with `_BUCKET`, `_KEY_FILE` (path of the service-account key file the provider issues; its token address must be https), `_SCOPE` (the read-write storage scope the provider documents) and optionally `_PREFIX` (all `MEMORY_BLOB_SERVICE_ACCOUNT_*`). The server signs a one-hour assertion with the key, trades it for an access token and reuses it until a minute before it expires. Set only one object storage |
| `MEMORY_DOCUMENT_CONVERTER` | Optional absolute path of a converter run as `<path> <file>` that prints text, for the extensions listed in `MEMORY_DOCUMENT_CONVERTER_EXTENSIONS` (for example `.epub,.rtf,.odt`). `.doc`, `.xls` and `.ppt` have built-in readers; listed there, the converter reads the files those readers cannot. It runs without a shell, with an empty environment, in its own temporary folder (removed afterwards) and process group (stopped as a whole on timeout), for at most 60 seconds, with 4 MiB of output. Other unsupported formats get 415 |
| `MEMORY_FILE_VISION` | `1` when the server model (`SKILL_LLM_*` with `ARKISM_WIKI=on`) takes image input: uploaded png/jpg/webp images, image blocks, and scanned PDFs (JPEG or 8-bit RGB/gray Flate page images, up to `MEMORY_FILE_VISION_MAX_PAGES` pages, default 8, at most 200) are read by it for memory extraction and knowledge uploads. Off by default; without it these uploads get 415 |
| `MEMORY_EXTRACT_BATCH` | `1` lets queued extraction jobs of more than 2 chunks send them to the server model as one provider batch (messages protocol only: `{endpoint}/batches`). Submitting reserves one usage-ledger row per chunk under the daily call cap (two batches cannot both pass it). The job waits as `retry` with `batch_pending` and is checked every minute for up to a day; after a restart the worker resumes waiting batches at start and then hourly with `MEMORY_WORKER_TOKEN` (it needs `write:memory_candidates` and `read:memory_candidates` in the job's space; without them the job waits for its owner), and the owner's next `GET` of the job (or its run route) also resumes the checks. Cancelling the job closes its reserved rows. Each answer settles its reserved row and becomes review candidates only; turning the setting off drops a waiting batch and the job goes on per chunk. Off by default |
| `MEMORY_FILE_MAX_BYTES` | Largest uploaded file for memory extraction and knowledge uploads, in bytes (default 100 MiB, the same as one whole default batch; at most 200 MiB). The JSON request may be that much in base64 plus 64 KiB, and the server holds such a request and its decoded file in memory at once, so set it lower on a small server. The multipart batch route reads one file at a time |
| `MEMORY_FILE_BATCH_MAX_FILES`, `MEMORY_FILE_BATCH_MAX_BYTES` | Limits of one `POST /v1/files/memories/batch` request: files (default 10, at most 100) and their bytes together (default 100 MiB, at most 1 GiB). Files are read one at a time, so memory stays near one file |
| `MEMORY_FILE_PARSERS`, `MEMORY_FILE_PARSER_ALLOWLIST` | Comma-separated reader order used when a request names none (default `builtin,converter,vision,transcribe` plus configured external readers), and the readers a request may name in `parsers` (default all configured). A reader that cannot read a file hands it to the next; a limit, a refusal or the model budget stops the chain |
| `MEMORY_FILE_PARSER_JOB_URL` | Optional external reading service (reader `upload_job`) using the upload-and-poll protocol: `POST {url}/upload` (multipart `file`), `GET {url}/{id}` until done, `GET {url}/{id}?expand=markdown_full`. Key in `MEMORY_FILE_PARSER_JOB_API_KEY` |
| `MEMORY_FILE_PARSER_PRESIGNED_URL` | Optional external reading service (reader `presigned_job`) using the presigned-upload protocol under `{url}/org/{MEMORY_FILE_PARSER_PRESIGNED_ORG}`; token in `MEMORY_FILE_PARSER_PRESIGNED_TOKEN`. Each file needs a `transfer:model_input` decision for destination `external:file_parser`; the file name never leaves the server, a file holding a secret or a private part is never sent, and the upload address the service returns may not be a private address unless the service URL itself is on a private network |
| `MEMORY_FILE_BUNDLE_MAX_BYTES` | Largest attachment a space bundle carries (default `MEMORY_FILE_MAX_BYTES`, at most 200 MiB). Larger attachments are left out and counted as `too_large` in the bundle's `skipped` list |
| `MEMORY_FILE_KEEP_ORIGINALS` | `1` keeps each file uploaded for memory extraction as an attachment of its evidence (needs `MEMORY_BLOB_DIRECTORY` or object storage). Recall lists them with `include: ["attachments"]`, reflect with `include: {"attachments": true}`; download is `GET /v1/sources/{id}/artifacts/{artifact_id}`. Off by default. Files from agents and files whose text had private or secret parts removed are not kept |
| `MEMORY_TRANSCRIBE_ENDPOINT` | Optional speech-to-text URL (https, or http on loopback) that takes multipart `file` + `model` and answers `{"text": ...}`, the common `/audio/transcriptions` protocol. Set `MEMORY_TRANSCRIBE_MODEL` and, if needed, `MEMORY_TRANSCRIBE_API_KEY`. Uploaded mp3/wav/m4a/ogg/webm/flac files (at most `MEMORY_FILE_MAX_BYTES`) are then transcribed for memory extraction; without it they get 415 |
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
   | `ARKISM_AUDIT_BODIES` | `1` keeps the first 4 KiB of each JSON request body, redacted, on its audit events, and the response status and first 4 KiB of a JSON response in a `response:<action>` follow-up event (see [Audit events](#audit-events)). Off by default |

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
   | `ARKISM_RERANK_LOCAL` | `1` without `ARKISM_RERANK_URL` turns on an in-process lexical (BM25) reranker; with it, the lexical reranker becomes the last stage of the fallback chain (upgrade note: before, a URL made this setting ignored). It sends nothing off the server, so it does not need `external:transfer`; it is weaker than a cross-encoder. Off by default, so an upgrade does not change recall order. Calls are filed in the usage ledger as provider `local`, model `lexical` |
   | `ARKISM_RERANK_FALLBACK_URL` (with `_MODEL`, `_PROTOCOL`, `_API_KEY`, `_API_KEY_FILE`) | A second rerank service, tried when the first fails or times out; it takes the same settings as the first and shares `ARKISM_RERANK_TIMEOUT`. Needs `ARKISM_RERANK_URL`. Unset by default. Each stage checks `external:transfer` on its own (a local stage needs none); when every stage fails, recall keeps the fused order. `explain` lists every stage tried with its outcome in `rerank_chain` |
   | `ARKISM_OTLP_ENDPOINT`, `ARKISM_OTLP_SERVICE_NAME`, `ARKISM_OTLP_SAMPLE_RATIO` | Request tracing (see [Tracing](#tracing)). Off by default |
   | `ARKISM_OTLP_METRICS_ENDPOINT`, `ARKISM_OTLP_METRICS_INTERVAL` | Metrics export (see [Metrics](#metrics)). Off by default |
   | `ARKISM_PROMETHEUS`, `ARKISM_PROMETHEUS_TOKEN` | `GET /metrics/prometheus` (see [Metrics](#metrics)). Off by default |
   | `ARKISM_LLM_TRACE_OPERATIONS`, `ARKISM_LLM_TRACE_EXCLUDE_OPERATIONS` | Which model call operations get a trace row (see [Model call traces](#model-call-traces)). Empty records all |
   | `ARKISM_LLM_TRACE_RETENTION_DAYS` | Days model call traces are kept by `memoryd --audit-expiry execute` (default `30`) |
   | `ARKISM_RECALL_GRAPH` | `1` adds the relations-graph recall lane. Off by default |
   | `ARKISM_RECALL_LANE_WEIGHTS` | Server default fusion weight per recall lane, e.g. `graph=2,temporal=4` (lanes `exact keyword vector relation temporal graph causal semantic`, weights 0.25-8; entries it cannot read are skipped). A space's `recall_lane_weights` and a request's `lane_weights` lay over it. Unset keeps every lane at 1, so recall order does not change |
   | `ARKISM_SPACE_DEFAULT_TEMPLATE` | Path to a space template bundle (`POST /v1/spaces/{workspace}/{project}/export` with `template: true`). Every space inherits the settings it leaves empty from the template's config, the template's defense rules apply to every space, and `POST .../import` with `default_template: true` copies its directives and mental model definitions into a space without settings. An unreadable file stops startup |
   | `ARKISM_SPACE_TEMPLATE_AUTO` | `1` applies the default template's directives and mental model definitions to a space by itself: when a person saves the space's first settings (their settings win), or on a person's first stored memory in a space without settings (the template's settings are stored too). The definitions arrive switched off (directives inactive, mental models on manual refresh) until a person turns them on in that space. Each space gets it once per server run; agent writes never apply it, since the definitions are human-only. Off by default. `GET /v1/spaces/template-schema?workspace_id=…&project_id=…` (`read:space_configs`) returns the JSON schema of a template bundle line |
   | `MEMORY_AUTO_DERIVE` | `on` lets the worker run observation, causal link and model name proposals by itself after approvals. Off by default, so an upgrade does not start spending the daily model calls. Needs the server model; results stay proposals for a person. This is the server default: a space's `auto_derive` setting (`PUT/PATCH /v1/spaces/{workspace}/{project}/config`, or the default template) overrides each field |
   | `ARKISM_EXTENSIONS_FILE` | Path to a JSON file of out-of-process extensions (see item 12). Unset means none. An unreadable file, a bad entry or an extension that does not answer `describe` stops startup |
   | `ARKISM_MCP_TOOLS` | Comma-separated MCP tool names the server allows in every space (a tool must also pass the space's `mcp_tools`). Empty allows every tool. Shown as `server_mcp_tools` in `GET /v1/spaces/{workspace}/{project}/config/effective`. A bad name stops startup |
   | `ARKISM_EXT_DATA_MAX_KEYS`, `ARKISM_EXT_DATA_VALUE_BYTES` | Per-space limits of each extension's data store: keys (default 200, at most 5000) and one JSON value's size (default 4096 bytes, 64 to 262144). Keys × value size may not pass 16 MiB. A bad value stops startup |
   | `MEMORY_AUTO_DERIVE_THRESHOLD`, `MEMORY_AUTO_DERIVE_INTERVAL` | Approvals that start a run at once (1-1000, default `10`) and the longest wait after the first counted approval (Go duration, `1m` to `720h`, default `6h`) |
   | `MEMORY_DERIVE_SCHEDULE` | `on` lets the worker run mental-model triggers (`daily`, `cron`, `full_refresh_cron`, `on_change`, `after_consolidation`) and a space's `consolidation_schedule` without anyone calling consolidate. One job per scope waits for the earliest due time; a model's `min_interval` (or the space's `mental_model_defaults.min_interval`) folds a burst of triggers into one refresh, and an attempt that wrote nothing waits an hour. Off by default, so an upgrade does not start spending the daily model calls. Results stay drafts and proposals for a person |

4. **Permissions.** Principals registered with `--actions '*'` (the examples
   above and the desktop app) need nothing. If you list actions, add these:

   | Principal | Actions to add |
   |---|---|
   | Owner (person) | `read:directives`, `write:directives`, `read:observations`, `write:observations`, `read:mental_models`, `write:mental_models`, `read:entities`, `write:entities`, `read:memory_relations`, `write:memory_relations`, `read:wiki_pages`, `write:wiki_pages`, `read:space_configs`, `write:space_configs`, `write:webhooks`, `read:jobs`, `transition:jobs`, `delete:jobs`, `ops:metrics`, `read:audit_events`, `generate:memory_candidates`, `delete:spaces` and `delete:memories` (deleting or clearing a space) |
   | Agent (MCP router) | `search:memories`, `generate:reflect`, `read:directives`, `read:observations`, `read:mental_models`, `read:entities`, `read:memory_relations`, `read:memory_candidates`, `read:wiki_pages`, and for the proposal tools `write:directives`, `write:mental_models`, `write:maintenance_proposals`, `read:jobs`, `transition:jobs`, `read:knowledge_sources`, `read:knowledge_documents`; `add_document` also needs `write:memory_candidates`, `generate:memory_candidates` and `external:transfer` |
   | Service that runs model jobs | the job's write action (`write:observations`, `write:mental_models`, `write:memory_relations`, `write:entities`, `generate:entities`, `generate:wiki_pages`) plus `search:memories` and `context:read` |
   | Worker (`MEMORY_WORKER_TOKEN`), only with `MEMORY_AUTO_DERIVE=on` or `MEMORY_DERIVE_SCHEDULE=on` | `write:observations`, `write:mental_models`, `write:memory_relations`, `write:entities`, `generate:entities`, `search:memories`, `read:memories`, `context:read`, `external:transfer`. Without them a run ends as `denied`; a scheduled run names the missing action (job outcome `denied:<action>`, and a warning in the worker log) and waits an hour |
   | Worker (`MEMORY_WORKER_TOKEN`), only with `MEMORY_EXTRACT_BATCH=1` | `write:memory_candidates`, `read:memory_candidates` in every space whose extraction batches it should resume after a restart. Candidates it reads from a batch are filed under the worker's name and still wait for a person's review |

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
   Knowledge uploads now read images and audio the same way, and an upload may
   send text with images in place (`blocks`). External reading services
   (`MEMORY_FILE_PARSER_*`) are off until set, and each file sent to one needs
   a `transfer:model_input` decision for `external:file_parser`. The per-file
   limit (`MEMORY_FILE_MAX_BYTES`) and the scanned-PDF page bound
   (`MEMORY_FILE_VISION_MAX_PAGES`) keep their old defaults.
7. **File extraction jobs and documents.** Cancel a running extraction with
   `POST /v1/operations/{id}/cancel`. Operator retry is refused; the uploader
   resumes a failed job with `POST /v1/files/memories/jobs/{id}/run`.
   `GET /v1/files/memories/documents` lists extraction documents.
   `PATCH /v1/files/memories/documents/{id}` (people only) edits a document's
   tags and passes the edit to its waiting candidates and, one audited
   revision each, to the memories its approved candidates became. Observation
   scopes (the space's `consolidation` block) are cut from memory tags, so the
   edit also moves those memories to the matching scopes on the next run.
   `POST /v1/files/memories/documents/{id}/reprocess` (the uploader) reads a
   finished document again from its stored evidence; it spends model calls
   like an upload and only creates candidates.
   `DELETE /v1/files/memories/documents/{id}` (people only) removes a document
   and its upload jobs, rejects its waiting candidates (audited) and leaves
   approved memories and their evidence in place. An agent's document list
   shows only the documents it uploaded.
   The list takes `from` and `to` (RFC 3339) to keep documents first added
   in that period; `time_field=updated` bounds when a document last changed
   instead (a new upload or reprocess of it, a text or tag edit). `GET /v1/files/memories/documents/{id}/text` (people only)
   shows a document's text; `PUT` on the same path (the uploader) replaces it
   and extracts only the chunks that changed, spending model calls like an
   upload and only creating candidates. `POST /v1/files/memories/documents/export`
   (people with the space export permission) zips up to 200 picked documents
   (8 MiB at most) and `POST /v1/files/memories/documents/import` restores such
   a zip without a model call. A space export takes `document_ids` to carry
   only some documents, and an import's `document_conflict` (zip import:
   `conflict`) says what a document of the same name does: `replace` (the
   default, as before), `skip`, or `new_id` (keep both).
8. **Object storage for attached files (optional).** With `MEMORY_BLOB_S3_*`,
   `MEMORY_BLOB_SHAREDKEY_*` or `MEMORY_BLOB_SERVICE_ACCOUNT_*` set, attached files live in the bucket, not in
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
11. **Recall tuning (all optional; defaults keep the old order).**
    - `lane_weights` on a search, the space setting `recall_lane_weights` and
      `ARKISM_RECALL_LANE_WEIGHTS` weigh lanes in fusion; `explain.lane_weights`
      shows the weights used.
    - `temporal_window {from, to}` ranks memories in that span higher instead of
      time words from the query. It never filters.
    - `min_scores {semantic, keyword, reranker, final}` set per-stage floors;
      `rerank_options {prune, dedup}` let the model rerank drop off-topic or
      repeated snippets in the same model call (no extra call), and the
      space's recall decision engine (`decision_models.recall`, opt-in) do the
      same in its own call.
      `explain.dropped` counts what each stage removed.
    - The space lane `lanes.semantic` (off by default) adds memories whose
      stored embedding is a near neighbour of a top hit.
    - Tag filters: `tag_groups` tests now nest `and`/`or`/`not` (5 levels, 64
      tests in all), a leaf with `resolve: "fuzzy"` also matches the space's
      tags spelled alike, and reflect accepts `tags_match` and `tag_groups`.
    - `include` gains `source_facts` (an observation's source memories, default
      4096 tokens), and `include_options` sets token budgets: `sources` allows
      longer excerpts, `entities` adds approved summaries and observations
      (needs `read:observations`).
    - Time words for about 380 more CLDR locales (month names with a day or
      year, yesterday/last week words, "N days ago", last/this weekday) are read
      after the hand-tuned languages, which still decide first. A month name a
      hand-tuned language also uses now reads there too when it names the same
      month ("enero 15, 2026" in Tagalog). Month names with a case ending
      and numbers with a suffix read too when a year or a suffixed day is next
      to them ("2026ko urtarrilaren 15a", "2026. gada 15. janvārī").
    - Slash dates: `15/01/2026` and `01/15/2026` read the only way they can;
      `03/04/2026` reads in the CLDR short date order of the request's
      `Accept-Language` (en-GB day first, en-US month first), else of the
      query's language when all its CLDR locales agree (German, French), and
      is not read at all otherwise (English or Spanish without a region).
    - "Last weekend" and "this weekend" (Saturday and Sunday of the previous
      or current Monday-first week) in every hand-tuned language. CLDR has no
      weekend term, so the CLDR-only locales do not read weekend words.
12. **Agent proposal tools.** Agents can now propose directives, mental
    models and invalidations, list and cancel their own jobs, and read
    documents. What an agent creates stays `proposed` until a person saves or
    decides it; open proposals are capped at 20 per scope and kind. Agent file
    uploads (`add_document`) spend the daily model calls, so they are off
    until a person lists `add_document` in the space's `mcp_tools`
    (`PATCH /v1/spaces/{workspace}/{project}/config`). Space bundle import is
    now refused for non-human principals.
13. **Entity names, labels and graph filters.** Extraction now compares a new
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
14. **Text ingest and extraction settings.** `POST /v1/memories/ingest` takes
    1-16 text items (context, timestamp, tags, metadata, entities, strategy,
    `document_id`, `update_mode` replace or append) and turns them into review
    candidates through the same extraction as file uploads; `async: true`
    queues every item as a job. Each item reports its own `status` (`failed`
    with `error`, and `job_id` when its text was stored); a failed item never
    undoes earlier ones. Items may send ordered `blocks` (text and png/jpeg/webp
    images) instead of `content`; images are read by the server model only with
    `MEMORY_FILE_VISION=1`. Agents may call it only where the space lists
    `add_document` in `mcp_tools`, as for uploads. New space settings, all
    inherited and off or unchanged by default: `extraction_mode: "chunks"`
    (each chunk of at most 1000 characters becomes a candidate, no model
    call), `extraction_mission` (sent apart from `extraction_instructions`),
    `extraction_chunk_size` (500-12000, default 6000), named
    `extraction_strategies` with `extraction_default_strategy`, and
    `default_timezone` (relative dates of extracted text are now read in it,
    the server's zone when unset). `extract_causal: true` is opt-in because
    the model returns more: causes between claims of one chunk become causal
    link candidates, reviewable after a person approved both claims. Extracted
    claims now show `fact_type` (`experience` for kind `episodic`, else
    `world`). `POST /v1/extract/preview` accepts `strategy`, `overrides`,
    `context`, `timestamp`, and `prompt_only` (renders the prompt, no model
    call); `text` is optional with `prompt_only`. Capture claims and the MCP
    `remember` tool accept `tags` (no `:`), `metadata` (stored as
    `meta:<key>=<value>`) and `entities` (names stated on the candidate, linked
    on approval). Jobs queued before this upgrade run with the space's current
    settings.
15. **Extensions without rebuilding (optional, off by default).**
    `ARKISM_EXTENSIONS_FILE` lists programs as
    `{"extensions":[{"name":"house","command":["/opt/house/bin/ext"],"env":{},"timeout":"2s","fail":"closed"},{"name":"corp","url":"https://ext.example.com/rpc","token_file":"/etc/arkism/corp.token"}]}`.
    A `command` (absolute path) is started once and spoken to over
    stdin/stdout, one JSON-RPC 2.0 message per line, with only `PATH` and the
    listed `env` (a name containing KEY, TOKEN, SECRET, PASS, AUTH, DATABASE or
    CREDENTIAL is refused unless `secret_env` also lists it). It runs in its
    own process group, which is stopped when a call fails or times out and
    when the daemon shuts down; answers over 1 MiB are refused. A `url` gets one JSON-RPC
    POST per call (https, or loopback http with `"allow_private": true`) with
    the bearer token read from `token_file`. Arkism calls `describe` at start,
    then `operation` before and after capture, recall, reflect, consolidate and
    mental model refresh and update (`before` can refuse, `after` only
    observes; results are sent only with `"send_results": true`), `tenant` for
    each authorized action (answers cached 30 seconds), `route` and `tool`.
    An extension whose `describe` answers `"llm_request": true` is asked
    `llm_request` before every server-side model call with `{scope, operation,
    kind, provider, model, prompt_chars, prompt_sha256, rewritable}` (never the
    prompt) and answers `{allow, model?}`: a refusal means the call is not made
    and the feature takes its fallback; a `model` moves a chat call (`rewritable`
    true: kinds complete, image, analyze, draft, route) to that model on the
    same endpoint and is ignored for other kinds. `"model_call": true` hears
    `model_call` after each call with `{call, result}` (ok, error code, model,
    input, output and cached tokens, latency); it only observes. Both run in
    the caller's request or job, so keep them fast.
    Each call has `timeout` (default 2s, at most 30s); on failure `fail:"closed"`
    (the default) refuses and `fail:"open"` lets operations and tenant checks
    pass. Declared routes are served at
    `GET|POST /v1/extensions/{name}/routes/{path}` (POST body `{scope, input}`)
    and tools at `GET /v1/extension-tools` and
    `POST /v1/extension-tools/call`, named `ext_<name>_<tool>` in the MCP
    router. Arkism checks the token, the scope and the action and roles each
    route or tool declares (default action for tools `search:memories`,
    default roles human, agent and service); a route or tool nobody may see
    answers `403` before `404`. Extension tools also pass both tool allowlists.
    An extension failure answers `503 dependency_unavailable`. Nothing an
    extension answers approves a memory, and its answers are returned as data
    (`authority: data_only_untrusted`; route answers carry the header
    `X-Arkism-Authority: data_only_untrusted`). Instead of their own tables,
    extensions get a small store per space in the existing records table
    (collection `ext_data`, so backup, restore, row security and space
    deletion cover it): at most 200 keys per extension and space, JSON values
    up to 4 KiB, secrets and private spans refused. `ARKISM_EXT_DATA_MAX_KEYS`
    (1–5000) and `ARKISM_EXT_DATA_VALUE_BYTES` (64 B–256 KiB) raise these;
    keys × value size may not pass 16 MiB, because a data call carries every
    entry of the space, and memoryd refuses to start on a bad value. The
    change request limit grows with the value size (4 × value + 16 KiB, at
    least 64 KiB). A route or tool that
    declares `"data": true` receives the entries as `data` and may answer
    `data` changes (`[{"op":"put","key","value"},{"op":"delete","key"}]`),
    applied with the caller's `read:ext_data` and `write:ext_data`. People and
    services read and change entries at `GET /v1/extensions/{name}/data`
    (`?cursor`, `limit`), `GET /v1/extensions/{name}/data/{key}` and
    `POST /v1/extensions/{name}/data` (`{scope, changes}`, all or none, optional
    `expected_revision`); deleting a key is how its data is erased. Principals
    with listed actions need `read:ext_data` and `write:ext_data` for these.
16. **MCP tool allowlists now fail closed.** The router reads the space's
    `mcp_tools` and the server's `ARKISM_MCP_TOOLS` and keeps them for a minute.
    Before this release a router that could not read the space settings
    offered every tool; now it offers none until it can (it asks again after
    10 seconds). `ARKISM_MCP_TOOLS_ON_ERROR=open` in the router's environment
    keeps the old behaviour. The settings are read with `search:memories`, which the router already holds.
17. **More agent read and proposal tools.** `browse_memory` now lists
    `memories` (active, paged, with `query`), `documents` (extraction documents,
    `GET /v1/files/memories/documents`, `read:memory_candidates`; an agent
    sees only the documents whose last upload it started), `spaces`,
    `tags` and `stats`. `GET /v1/stats` now asks `read:memories`, and agents
    may read it without `ops:metrics`; people and services still need
    `ops:metrics` as well, so principals with listed actions need
    `read:memories` added. The new `propose_change` tool files a maintenance
    proposal of type `change` for knowledge folder and page create, edit and
    delete, document delete, mental model refresh, clear and delete, directive
    delete, a space settings change, or clearing the space's memories. The
    target must exist at the revision the agent saw, secrets and private spans
    are refused, and only a person accepts or dismisses it. Deciding changes
    nothing: the person makes the change through its own screen or route.
18. **Space bundles stream.** Export downloads are now format version 2:
    the manifest line, the body, then a trailer line `{"end": {sha256,
    sections, skipped}}`. They stream fresh from the database, with no 8 MB or
    5,000 record cap, so a record erased after the export is left out instead
    of failing the download with 409. Older servers cannot import version 2
    bundles; this server still imports version 1. `POST
    /v1/spaces/{workspace}/{project}/import` also takes the bundle itself as an
    `application/x-ndjson` body (up to 1 GiB, spooled to a temp file and checked
    before anything is written), with `import_as` and `as_template=true` in the
    query. Export and clone take `parts` (`config`, `data`, `observations`,
    `history`, `knowledge`, `attachments`, `documents`; default `config` and
    `data`, as before). Observations arrive as
    memory candidates. History (audit rows and model-call traces without their
    text) is only for people; an uploaded bundle's history is never written,
    and only a clone inside the server copies it. `as_template` takes the
    settings and definitions of any bundle into a space without settings.
    The `knowledge` part carries knowledge files as their stored text and the
    `attachments` part carries source attachments of any format (base64, up
    to `MEMORY_FILE_BUNDLE_MAX_BYTES`, default `MEMORY_FILE_MAX_BYTES`; a file
    over 2 MiB goes as consecutive pieces that import joins again), both from
    the space's user and agent sub-scopes. The `documents` part carries each
    extracted document with its chunks' text read back from its evidence;
    import stores the chunks as new evidence and records the document as done
    under the importing person without calling a model (reprocess it to
    extract again). Every line left out is counted in `manifest.skipped` with
    its reason (`too_large`, `private`, `not_live`, `incomplete_file`, ...). Only a person
    gets them, and only their own: knowledge files the caller supplied,
    private attachments the caller created; restricted attachments never
    leave. Import and clone restore each file into the same sub-scope of the
    target, knowledge as a draft source, and importing again adds nothing.
    Old bundle text on export job rows is cleared on the next download
    or by `memoryd --space-repair execute`.
19. **Webhooks.** A webhook can set its `method` (POST, PUT or PATCH),
    `timeout_seconds` (up to 30), and extra `headers` and `query_params` (16
    each). Their values are sealed with the webhook key and never shown again:
    reads return only `header_names` and `query_param_names`. The outbound
    address check is unchanged. New events: `consolidation.completed`,
    `ingest.completed` (knowledge and file ingest) and `defense.triggered`
    (a space defense rule redacted or blocked a write). Existing webhooks keep
    their events; subscribe to the new ones to receive them. Changing a
    webhook's URL to another scheme, host or port drops its sealed headers and
    query values unless the same update sends them again.
20. **Wiki folders and query pages.** `PATCH /v1/wiki/folders` renames or
    moves a folder with everything in it, `DELETE /v1/wiki/folders` deletes an
    empty folder (or, with `recursive=true`, its pages too), and `DELETE
    /v1/wiki/pages/{id}` deletes one page. All three are for people only, and
    moving or deleting a folder that holds query pages also needs the
    mental model write permission.
    Mental models take a `folder` and show in the wiki tree as query pages;
    an update with `no_folder: true` takes one out of the tree again. A
    query page refreshes only on the refresh mode a person chose, and every
    refresh is a draft a person approves; nothing new spends model budget by
    itself.
21. **Tags and stats.** `GET /v1/tags` takes `q` (`*` wildcards), `source`
    (`memories` or `mental_models`), `limit` and `offset`, and returns
    `total`. Stats count links by kind, and the timeseries takes `time_field`
    (`created`, `occurred` or `mentioned`).
22. **Space rename.** `memoryd --space-rename preview|execute --from
    workspace/project --to workspace/project2` moves a space to a new project
    id offline (see the command table). Nothing changes until an administrator
    runs it. A person can also rename from the Spaces screen
    (`POST /v1/spaces/{workspace}/{project}/rename`, preview first); that runs
    the same move under the person's own database permissions, so memberships,
    permissions and decision counts, which the app role may not update, stay
    under the old name and are listed in `skipped`. Use the command for those.
24. **Space delete.** A person (never an agent) can delete a space with
    everything in it (`DELETE /v1/spaces/{workspace}/{project}?confirm=<project>`,
    or several at once with `POST /v1/spaces/delete`), or clear only its
    memories (`DELETE /v1/spaces/{workspace}/{project}/memories`). The typed
    confirm must repeat the project id. A space with a running job or a privacy
    deletion on legal hold is refused. Accepting an agent's `space_delete` or
    `memories_clear` proposal with the same confirm now runs it. Grant
    `delete:spaces` to owners who list actions.
23. **Unnamed keys.** The secret check now also catches long random tokens
    no named rule knows (32 characters or more, mixed letters and digits),
    and lets a reference through (`token: ~/.config/x/token`, `$API_KEY`,
    `<your-key>`, `***`) while a value after the same marks is still caught.
    Hashes, commit ids, uuids, record ids and image data are not flagged.
    Inspection reports say `101-ingest-v2`. Stored records are not inspected
    again on upgrade, but checks made when old text is read again (archive
    search, merge proposals, webhook content, wiki sources, transfers and
    exports) now withhold text that holds such a token, and importing an old
    bundle refuses those items. A webhook address that carries a key (a
    token in its path) is now accepted: it is sealed like the headers, and
    reads show it with the key as `[REDACTED]`.
25. **Bigger uploaded files by default.** `MEMORY_FILE_MAX_BYTES` now
    defaults to 100 MiB (was 4 MiB), so one file may be as large as a whole
    default batch; space bundles carry attachments up to the same size unless
    `MEMORY_FILE_BUNDLE_MAX_BYTES` says otherwise. A JSON upload of that size
    is held in memory with its decoded file, so a small server should set
    `MEMORY_FILE_MAX_BYTES=4194304` to keep the old limit. The desktop app no
    longer stops files above 4 MB itself; the server's limit decides.

### Extraction labels and opt-ins

Extracted claims may now carry a `rule` (`always`, `never`), a `decision_status` (`proposed`, `confirmed`, `dropped`), a `temporary` flag and `relations` between the entities they name. They only mark the candidate. A person's approval acts on them: a rule memory also becomes an active directive, which SessionStart shows first; stated relations become knowledge graph edges; a temporary request expires after 7 days (the extractor's flag, or "this time only" wording in a preference or procedure; a lesson, decision or event that only describes one time keeps no expiry). The review screen marks a rule candidate, starts it as "drop" in batch review, and marks a temporary one with its expiry. A recalled procedure that names a file of the current repository that no longer exists gets an invalidation proposal with reason `path_missing`, for a person to decide; paths outside the repository (another machine's or home folder), and relative paths of a memory not bound to this project, are only flagged.

Space settings (`PUT/PATCH /v1/spaces/{workspace}/{project}/config`, or the default template) for extraction; every default except `capture_density` leaves an upgrade's behaviour as it was:

- `memory_language`: `auto` (default, the language the person wrote in) or a language tag such as `ko` or `en`. The capture hook, lesson extraction and server-side file extraction write memory texts in it; evidence quotes stay in the source's own words.
- `work_state_candidates` (default off, `null` inherits): a fresh work checkpoint or handoff save also proposes its decisions (`decision_status: proposed`) and one current-state line as decision candidates. They go through the normal capture gate and stay candidates until a person reviews them. It makes no model call, but it stays opt-in: turned on it adds review work and extra candidate rows to every checkpoint and handoff save.
- `recall_session_archives` (default off, `null` inherits): search (purpose `answer` or none) adds up to 3 raw past session messages as a separate `sessions` lane. That text is not reviewed memory; a principal without session archive access gets no lane. It stays opt-in because it puts unreviewed text in front of agents.
- `capture_mode` (`user`, the default, or `assistant_results`) and `capture_density` (`durable`, `balanced`, the default, `dense`, or `conversation`) shape the capture hook. `user` with `durable` keeps only the user's own lasting statements, from turns with a memory cue. `assistant_results` (opt-in) also reads the agent's last reply of the turn (its last 2000 characters) and keeps facts the agent confirmed (a test or command outcome, a value read from a tool) and facts another person is quoted as saying. Those candidates carry basis `assistant_result` (an inference when the quote is not literally in that text), are never low risk, and stay candidates until a person approves them; the agent's plans, guesses and promises are not kept. `balanced` and `dense` also keep specific facts about people, events, dates and numbers. `assistant_results`, `balanced` and `dense` read every turn instead of cued turns only, so they spend a capture model call per batch where `user`/`durable` spent none. Session imports and the session processor keep their own prompts. `conversation` (opt-in) is for spaces that hold conversations between people (or people and an assistant) rather than instructions to a coding agent. Its rules replace the coding rules instead of adding to them: every concrete fact any speaker states (events with absolute dates resolved against the session date, things people own or make, names, numbers, places, preferences, reasons and feelings, plans) becomes its own candidate, written about the named person. A long batch is read in windows of 10 turns that overlap by 2, so nothing past the first claims is cut, and each window is one model call: this profile spends noticeably more model calls than `dense`. It also keeps each batch's turns, dated and speaker-labelled, as sources of kind `conversation` (privacy markers wait for the batch, secrets are redacted, at most 600 characters a source); a recall that includes sources (`include: ["sources"]` or the space's `recall_quality.include_chunks`) returns up to 5 matching turns in a separate `conversation` list, never mixed with memories, only to callers that may read sources and never to an external destination. With an embedder running, those turns are embedded so the vector lane finds them. Extracted claims stay candidates for a person to review, as in every profile.
  Upgrade note: the server default for `capture_density` changed from `durable` to `balanced`. A space (or default template) that never set it now captures every turn, not only cued ones, so each session makes more capture model calls (about one per captured batch) and produces more candidates to review. Approval rules do not change: every candidate still waits for a person. A space that stored `durable` keeps it. To go back, set it per space (`PATCH /v1/spaces/{workspace}/{project}/config` with `{"capture_density": "durable"}`, or 「얼마나 촘촘히 뽑을지」 → 「오래 쓸 것만」 in the app's space settings), or put `"capture_density": "durable"` in the default template so every space inherits it.
- `decision_models` (`{task: off|clef|jev}`, every task `off` by default; a sent map replaces the stored one): a small typed decision engine of the server (the skill-routing engines, configured by the server's environment, never by the space) settles easy cases of a task before the server model is asked. The tasks:
  - `consolidation`: a note group that an existing observation already states, or passing chatter, is dropped from the run (listed in `rejected` with code `decision_restates` or `decision_chatter`) without an observation call.
  - `mental_model_refresh`: before a mental model's summary is written, recalled records that do not bear on its title and query are left out of the prompt; when none bear on it, the refresh ends with `content_preserved_no_new_facts` and no summary call.
  - `mental_model_delta`: a delta refresh (`refresh.strategy` `delta`) asks, for each record that changed after the approval, whether it could change the approved text; only those go to the edit call, and none means no edit call (`content_preserved_no_new_facts`). The approved text is only sent when every record it cites passes the transfer check for the engine.
  - `reflect`: the records the first search found that do not bear on the question are dropped before the answer, and when the engine says the rest already answers it, the first model call is the last one (no tool rounds). `prompt_only` never asks the engine.
  - `recall`: a search's results are checked as its `rerank_options` ask (a request's own, else the space's `recall_quality.rerank_options`): `prune` drops results that do not bear on the query, `dedup` drops results that only repeat one ranked above them (the top result is never a repeat). It runs before the model rerank, in one decision call per search; without `prune` or `dedup` it asks nothing. Private results and a query that may not leave the host are never sent; `explain.dropped` counts `decision_prune` and `decision_dedup`.

  One decision call per task run asks about at most 40 records; records past that, records that may not leave, and every record of a failed call stay, so anything the engine does not settle goes to the model as before. The result is still a draft or an answer a person reads; the engine never writes. Each decision call is recorded as a model call (operation `wiki_decision_<task>`) and counts against the server model's daily limit, and every record passes the external transfer check first.

The approval-driven automatic derivation (`auto_derive`, server default `MEMORY_AUTO_DERIVE`, off) gains a `summaries` pass: when a person approves a memory that names a person (a person entity or a relation to one), that person is queued and, after the observation, causal and entity passes, the run drafts a refreshed summary for at most 16 queued people. Each draft is a model call and waits for a person to approve it like a drafted summary from the entity screen; a person with a draft already waiting is skipped.

### Reflect, consolidation and mental-model settings

Three space settings blocks (`PUT/PATCH /v1/spaces/{workspace}/{project}/config`, or the default template) are empty by default, so an upgrade keeps today's behaviour. Each block is inherited whole; sending an empty block clears it.

- `reflect_default_options`: `budget` (`low`, `mid`, `high`), `fact_types`, `exclude_mental_models` and `tools` (token caps per reflect tool) for requests that leave them unset. `tag_scoped_directives: true` (opt-in) applies a tagged directive only to a reflect whose tags share one of its tags; a request's `apply_all_directives` turns that off.
- `mental_model_defaults`: the `trigger` a mental model created without a refresh mode takes, and a space-wide `min_interval`.
- `recall_budget`: how many records reflect and mental-model refreshes recall for each budget level when the call sets no limit. `function: fixed` (the default) takes `fixed_low`/`fixed_mid`/`fixed_high` (0-50; 0 keeps the shipped count: low 5 and high 20 for reflect, 10/20/40 for mental models); `adaptive` takes `round(max_tokens x adaptive_<level>)` (ratios 0-1) clamped to `min`..`max` (0-50; defaults 1 and 50). Reflect reads at most 20. Unset keeps today's behaviour.
- `consolidation`: `observations_mission`, `default_scope_mode` (`shared`, `combined`, `per_tag`, `all_combinations`) or `default_scopes`, `strategies` (tag-glob rules, each with its own mission, cap and source token budget; the first claiming strategy wins), `scope_limits`, `max_observations_per_scope`, `batch_size`, `parallel` (1-4) and `max_memories_per_round` (0-100000, 0 = no cap: a run takes no further group once its groups hold that many memories; the group that reaches it still runs, the stop is listed in `rejected` as `round_limit`). More scopes mean more model calls per consolidation run, all counted against the daily limit; one run or preview builds at most 1000 scopes.

Three extraction settings (plain fields, inherited one by one; the first two also go in `extraction_strategies`) keep today's behaviour unless set:

- `extraction_batch_size` (0-1000, 0 = all chunks): with `MEMORY_EXTRACT_BATCH=1`, how many chunks one provider batch sends; the next batch goes out when the answers of the last one are in.
- `extraction_images_per_chunk` (0-64, 0 = no cap): a chunk holding that many image readings ends there and the next image starts a new chunk.
- `store_document_text` (default `true`, `null` inherits): `false` keeps only each chunk's locator as evidence and does not keep the uploaded original. Candidates are still extracted, but the document's text can no longer be shown, reprocessed or exported (409 or skipped `not_live`); editing the text with new text still works.

The server cannot grant its own worker these actions (principals are registered with the schema owner's connection). A worker registered with `--actions '*'` needs nothing; a narrowed one is registered again with the scheduler's actions, for example:

```sh
MEMORY_PRINCIPAL_TOKEN="$(cat "$HOME/.local/share/arkism-admin/worker-token")" go run ./cmd/memory-principal \
  --principal worker --role service --organization default --project default \
  --actions 'write:observations,write:mental_models,search:memories,read:memories,context:read,external:transfer'
```

Parallel consolidation (`parallel` above 1) reserves each call against the daily model call limit before it starts, so a run never passes it.

Routes added with them, none of which calls a model: `POST /v1/mental-models/{id}/clear` (people only), `POST /v1/mental-models/cron-preview`, `GET /v1/observations/scopes`, `POST /v1/observations/strategies/preview`, `DELETE /v1/observations` (people only; bulk, audited; rows keep their history with status `deleted`) and `POST /v1/prompts/preview`.

### Recall quality settings

The `recall_quality` space block (inherited whole, like the blocks above) changes what memory search returns. Upgrading changes four things by default; the rest is opt-in.

- `relevance_gate` (default **on**; `false` turns it off): an exact-keyword or keyword-search hit stays only when its text, summary and keywords share the query's topic words (one, or two once the query has three or more) or the vector lane found it too. A keyword equal to the whole query always passes. When every hit is removed the result is empty with `empty_reason: below_relevance_floor`. On the fixed no-answer set in `internal/memory/testdata/recall_no_answer_v1.json` this cut off-topic results from 14 of 14 no-answer queries to 2 of 14 with no answer lost (11 of 11). Korean question words (언제, 어디, 누가, 무엇, 왜, 어떻게, …) now count as question scaffolding, as English ones already did.
- Keyword coverage in fusion (always on, no setting): in memory recall a keyword-search hit's lane credit is multiplied by the share of the query's topic concepts it holds, so a hit that matches only a word much of the space shares (a person's or project's name) no longer ties a semantic match of the whole question, while a hit holding every concept counts as before. Upgrade note: result order changes in every space. On the three-conversation store at limit 10, evidence recall rose from 0.8288 to 0.8433 with every category higher (multi-step 0.5633 to 0.5833, time 0.9013 to 0.9109, open 0.5469 to 0.5885, single-step 0.9435 to 0.9540) and no model calls.
- `conflict_pending` (default **attach**): a memory a correction is waiting on comes back with `pending_correction` (the correction's text only for callers with `read:memory_candidates`). `exclude` leaves such memories out until a person decides.
- `provisional` (default **on** since this release; `false` turns it off): pending candidates that match the query come back in a separate `provisional` list (at most 3) with a `provisional_notice`, only to callers with `read:memory_candidates` (people and owner tokens; an agent without that scope never sees them). They are never mixed into `items`, never sent to an external destination (`destination: external:…`), never part of an export or a frozen context view. Each passes the same read check as `items`, and with a context budget they are packed into `context_data` after the approved items, within what the budget leaves. Candidates are found by the recall's own exact and keyword lanes over the candidate table, no longer by a scan of the 100 newest candidates.
- `provisional_vectors` (default off; spends embedding calls on candidates a person may reject): new candidates are queued for embedding so the vector lane finds them too. Approving a candidate unchanged hands its vector to the memory, so an approved fact is still embedded once. Candidates made before it was turned on are not back-filled.
- `auto_close_on_correction` (default off): when a person approves a correction and keeps the old memory (`keep_both`), the old memory gets a `valid_to_proposal`; a person accepts it by setting `expires_at` to that time (`POST /v1/memories/{id}/expiry` now accepts that one past time). With this on, the approval ends the old memory at once.

- `diversity` (default **on** since this release; `false` keeps the old plain score order): each recall lane reads twice the request's limit (at most 50), and memory recall then picks its `limit` rows one at a time, lowering each candidate's score by half of how far its highest embedding similarity to a row already picked sits into the near-duplicate band (0 below the capture merge bar of 0.92 cosine, 1 at identical). A memory that rewords one already returned gives way to the next distinct fact, while memories that only share a topic (every memory about one person) keep their score order; rows without a stored vector in the query's space are not penalised, scores keep their real values (so score floors still apply) and the count never exceeds `limit`. Upgrade note: result order changes for every space that does not set it. It is on by default because extraction stores the same fact several times when a capture's merge check is skipped or misses, and those copies filled the head of a limited result: on a fixed three-conversation recall test at limit 10, answers went from 362 to 366 of 453 with the same store and no extra model calls: multi-step questions 59 to 63 of 93, single-step questions unchanged at 219 of 239, time questions 63 to 64 of 87 and open questions 21 to 20 of 34. The gain is small: two runs with diversity off on separately built stores scored 364 and 362, so read it as a modest lift on questions that need several facts, not a general one. The first row of a result is never moved (the best match is always picked first); a fact worded almost like a higher one can drop out of a full result. Since the near-duplicate band replaced the earlier penalty on any similarity, the same three-conversation store scores higher on evidence recall at limit 10 (the share of a question's evidence turns the returned memories and conversation turns cover, no model calls): 0.8433 to 0.8466 overall with every category equal or higher; the earlier penalty had pushed same-topic evidence out of the result. To go back, `PATCH /v1/spaces/{workspace}/{project}/config` with `{"recall_quality": {"diversity": false}}` (the block is inherited whole, so send the other fields you keep with it).

- `expand` (default **off**; opt-in because it spends one server-model call per memory recall): before fusion the server model (`ARKISM_WIKI=on` with `SKILL_LLM_*`) is asked once for up to 4 short sub-queries that together find the facts a question needs (one per person, thing or event it involves; one per item for a list question). Each sub-query runs through the same lanes, checks, floors and relevance gate as the question (judged against its own wording), and its lanes join the question's fusion, so rerank, recency and `diversity` pick the `limit` rows from one pool; the result never holds more than `limit`. A search or context build can also send `expand: true` (or `false` to skip it in a space that has it on), and the MCP `recall` tool accepts `expand`. Only the query is sent, never one with a private marker or a secret; the caller needs `generate:recall_expand`; calls are recorded under operation `recall_expand` (usage ledger and `GET /v1/llm-requests`), capped at 500 per project a day and bounded by a 10 second timeout; the sub-queries' embeddings are one extra embedding call. Any refusal or failure runs plain recall, and `explain.expand` names the outcome (`explain.expand_queries` lists the sub-queries). Upgrade note: nothing changes until a space or request turns it on.

- Recall defaults a request leaves unset: `rerank_options` `{prune, dedup}` for the model rerank, `prefer_observations`, `include_chunks` (adds `include: ["sources"]`) and `chunks_max_tokens` (1-16384 token budget for those excerpts, `include_options.sources.max_tokens`). A request that sends its own value keeps it; `prefer_observations: false` now turns a space default off.

Notices the server writes (`provisional_notice`, `pending_correction.notice`, the capture hook's `risk_reasons` and review reasons) follow the space's `memory_language`; with `auto` they follow the request's `Accept-Language`, else the script of the query (Korean, Japanese, Chinese, else English). Built-in notice texts exist for ko, en, ja, zh, es, de and fr; other languages get English.

`POST /v1/memories/at` takes an optional `valid_at` next to `as_of`: `as_of` is record time (what was known then), `valid_at` keeps only memories in effect at that time. The `recall_at` MCP tool passes it through.

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

With a stored Google API key (API mode), `ARKISM_GOOGLE_CONTEXT_CACHE=1`
on the gateway stores a long stable prompt prefix (system instruction,
tools and tool config, at least 4 KiB) once upstream as cached content and
points later requests at it, so those input tokens are billed at the
provider's cached rate. An entry lives 55 minutes and is recreated 5 minutes
before that; a refused create (prefix below the model's minimum) falls back
to the full request and is not retried for 5 minutes; a request the provider
rejects drops the entry. Cached tokens arrive as `cached_tokens` in the chat
usage, so they show in `arkism_llm_cached_input_tokens` and the usage
ledger. **Upgrade note.** Off by default: each create is a billed request
and cached content has a storage charge. The login mode has no cache resource
and is never cached.

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
add process-wide counters (requests and requests in flight, model calls with
input, output, cached input and reasoning (thinking) token histograms per
operation, rerank, recall lane and file extraction stage durations, finished
background jobs by kind and outcome with their run time
(`arkism_operations_total`, `arkism_operation_duration_seconds`), documents
handed to memory extraction by source (`arkism_documents_ingested_total`:
text, file, knowledge, reprocess), extension hook wall time by hook and
outcome (`arkism_extension_hook_duration_seconds`: ok or refused), rerank
calls, failures and timeouts for every stage of the fallback chain (labels
`stage`, 0 first, and `provider`; `GET /metrics` lists the same in
`rerank_stages`), job queue backlog, running,
failed and oldest-waiting gauges by job kind, tracing, database pool,
goroutines, heap, GC, CPU, RSS and open files). They hold no memory text and
no scope data. **Upgrade note.** The rerank counters used to be one
unlabelled series for the first stage only; they now carry `stage` and
`provider` labels, so a query on the old series needs `stage="0"`. The queue gauges are counted with the server's own store
binding on each scrape (2 s limit; `arkism_job_queue_stats_up` is 0 when the
count failed); cached input and reasoning tokens appear only when the provider
reports them.

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
raw URL or query) and `duration_ms`. Bodies are not captured by default.
With `ARKISM_AUDIT_BODIES=1` an event written during a JSON request also keeps
`request_body`: the first 4 KiB the handler read, with private spans and
secret-looking text removed. Only `GET /v1/audit-events/{id}` returns it, not
the list. Uploads and other media types are never kept. Events are written
before the answer exists, so the answer goes in a follow-up: once the
response is sent, a request that wrote events also writes one
`response:<action>` event per audited scope (at most 8), with the same
`request_id`, `target` set to that scope's first event of the request,
`response_status` and `response_body` (the first 4 KiB of a JSON response,
redacted the same way; other media types keep only the status). Model call
traces keep model answers with `ARKISM_LLM_TRACE_CONTENT=1`. A kept body is part of
the audit history: deleting a source does not erase it, it expires with
`memoryd --audit-expiry` and travels in a space export that includes history.

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

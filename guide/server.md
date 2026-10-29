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
MEMORY_EMBEDDER=none                  # none | tei | gemini; the worker needs one
MEMORY_LOCAL_STANDALONE=true          # API listens on loopback only
MEMORY_WORKER_TOKEN=example-worker-token
```

Keep the file out of Git. All settings:

| Name | Meaning |
|---|---|
| `MEMORY_DATABASE_URL` | Postgres connection (required). API and worker use the `arkism_app` role |
| `MEMORY_EMBEDDER` | `none`, `tei`, `ollama` or `gemini`. Required for the worker |
| `MEMORY_LOCAL_STANDALONE` | `true` for single-owner mode. The API then listens on loopback only |
| `MEMORY_WORKER_TOKEN` | Service token for the worker and readiness checks |
| `MEMORY_CANDIDATE_DAILY_LIMIT` | Daily cap (1–100000) for AI-written memory candidates. Unset means none are accepted |
| `MEMORY_CAPTURE_MERGE_SIMILARITY` | Cosine similarity (0–1, default 0.92) at which a captured claim gets a merge proposal toward the closest approved memory instead of a plain review entry. Needs an embedder; a person still decides |
| `MEMORY_BLOB_DIRECTORY` | Folder for attached files |
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

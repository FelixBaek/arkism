# Server mode

Most people only need the desktop app in built-in mode. Run a server when several PCs, or several people, should share one Arkism.

A server is the same Arkism API (`memoryd`) and worker, backed by Postgres with pgvector. The desktop app reaches it over SSH.

## Start with Docker

```bash
./deploy/local/start.sh          # Windows: .\deploy\local\start.ps1
```

The script:

- builds the Arkism image,
- creates `deploy/local/.env` with random passwords and tokens (keep it private),
- starts Postgres, creates the schema, then starts the API and the worker,
- prints the API address (`http://127.0.0.1:8091`) and the **owner token**.

Running it again keeps `.env` and the data and only starts what is stopped. Stop it with:

```bash
docker compose -f deploy/local/compose.yaml down    # data is kept
```

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

1. Make sure `ssh me@my-server` works without a password prompt (an SSH key or agent). The app never asks for or sees a password. Windows uses the built-in OpenSSH client.
2. In the app, open **Settings › 연결 (Connection)**, type `me@my-server` (or a `~/.ssh/config` alias, optionally with `:port`) and connect.

A new host key is accepted on first use, like answering "yes" once in `ssh`.

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
   MEMORY_PRINCIPAL_TOKEN='owner-secret' go run ./cmd/memory-principal \
     --principal management:owner --role human --organization default --project default --actions '*'
   MEMORY_PRINCIPAL_TOKEN='worker-secret' go run ./cmd/memory-principal \
     --principal worker --role service --organization default --project default --actions '*'
   ```

4. Copy [`.env.example`](../.env.example) to `.env`, fill it in with the `arkism_app` connection, and start the API and the worker. `memoryd` does not read `.env` by itself.

   ```bash
   set -a; . ./.env; set +a
   go run ./cmd/memoryd             # API, default 127.0.0.1:8091
   go run ./cmd/memoryd --worker    # embedding and index worker (needs MEMORY_EMBEDDER)
   ```

`/healthz` says the process is alive. `/readyz` says the schema, extensions and indexes are ready. Indexes are never created at startup.

To run in containers, build the image from `Dockerfile` and use `compose.yaml` (add `compose.files.yaml` for file storage). Compose needs `MEMORY_ENV`, `MEMORY_IMAGE`, `MEMORY_API_ENV_FILE` and `MEMORY_WORKER_ENV_FILE`; `compose.files.yaml` also needs `MEMORY_BLOB_HOST_ROOT`.

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

| Flag | What it does |
|---|---|
| (none) | Run the HTTP API (`--listen`, default `127.0.0.1:8091`) |
| `--worker` | Run the embedding and index worker instead |
| `--init-db` | Create the Postgres schema and indexes |
| `--check-indexes` | Check the schema, search indexes and row-level security |
| `--semantic-index create\|drop` | Create or drop the optional pgvector index (`MEMORY_SEMANTIC_RETRIEVAL=1`) |
| `--reindex` | Queue embedding jobs for records the current embedder has not indexed, then exit |
| `--collect-blobs` | Remove files nothing points to |
| `--adopt-restored-blobs`, `--recovery` | Restore steps, see [backup-restore.md](backup-restore.md) |

## Configuration

`memoryd` settings, with fake values, are in [`.env.example`](../.env.example).

| Name | Meaning |
|---|---|
| `MEMORY_DATABASE_URL` | Postgres connection (required). API and worker use the `arkism_app` role |
| `MEMORY_EMBEDDER` | `none`, `tei`, `ollama` or `gemini`. Required for the worker |
| `MEMORY_LOCAL_STANDALONE` | `true` for single-owner mode. The API then listens on loopback only |
| `MEMORY_WORKER_TOKEN` | Service token for the worker and readiness checks |
| `MEMORY_CANDIDATE_DAILY_LIMIT` | Daily cap (1–100000) for AI-written memory candidates. Unset means none are accepted |
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

## Host tools and systemd

Run `arkism` with no arguments to list its subcommands (capture hooks, skill sync, gateway, crew runner, Discord, backups and more). `arkism-crew` runs workflows (`serve`, `run CREW_ID`, `hook-url WORKFLOW_ID`).

```bash
sudo install -d /srv/arkism/bin
go build -o arkism ./cmd/arkism && sudo install -m 0755 arkism /srv/arkism/bin/arkism
go build -o arkism-crew ./cmd/arkism-crew && sudo install -m 0755 arkism-crew /srv/arkism/bin/arkism-crew
```

The binary must be executable by uid 65532, because the backup job runs part of it as that user.

The units in [`deploy/systemd/`](../deploy/systemd) expect binaries in `/srv/arkism/bin`, scripts in `/srv/arkism/tools` (copy `tools/*.sh` there), and settings and tokens in `/srv/arkism/config`. Change paths, `HOME`, `User=` and ports for your machine, then copy the units to `/etc/systemd/system`. Machine-specific values go in `/srv/arkism/config/arkism.env` (see the last section of `.env.example`).

Backups for a server: [backup-restore.md](backup-restore.md). Discord bots: [discord.md](discord.md).

#!/usr/bin/env bash
# Postgres for CI tests: a throwaway local cluster with pgvector in RUNNER_TEMP, on a random loopback port.
# No docker (the self-hosted runner has none), so no service container. The cluster dies with the job.
# Writes POSTGRES_TEST_* to GITHUB_ENV. No password or secret is involved: trust auth on loopback only.
set -euo pipefail
if [[ "${RUNNER_ENVIRONMENT:-}" == github-hosted ]]; then
  # GitHub-hosted Ubuntu ships PostgreSQL without pgvector; the self-hosted runner has both preinstalled.
  major=$(find /usr/lib/postgresql -mindepth 1 -maxdepth 1 -printf '%f\n' | sort -V | tail -1)
  sudo apt-get update -q >/dev/null
  sudo apt-get install -y -q "postgresql-${major}-pgvector" >/dev/null
fi
bin=$(find /usr/lib/postgresql -mindepth 2 -maxdepth 2 -name bin | sort -V | tail -1)
[[ -x "$bin/initdb" ]] || { echo "::error::No local Postgres on this runner (install postgresql and pgvector)" >&2; exit 1; }
data="$RUNNER_TEMP/pgdata"
port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
"$bin/initdb" -D "$data" -U arkism --auth=trust -E UTF8 >/dev/null
"$bin/pg_ctl" -D "$data" -l "$RUNNER_TEMP/postgres.log" -w \
  -o "-c listen_addresses=127.0.0.1 -p $port -k $RUNNER_TEMP" start >/dev/null
"$bin/createdb" -h 127.0.0.1 -p "$port" -U arkism arkism_ci
{
  echo "POSTGRES_TEST_DSN=postgres://arkism@127.0.0.1:${port}/arkism_ci?sslmode=disable"
  echo "POSTGRES_TEST_PG_BINDIR=$bin"
} >> "$GITHUB_ENV"

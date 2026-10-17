#!/usr/bin/env bash
# Postgres for CI tests. GitHub-hosted jobs use the service container; the self-hosted runner has no
# docker, so it starts a throwaway local cluster in RUNNER_TEMP (killed with the job's process group).
# Writes POSTGRES_TEST_* to GITHUB_ENV. No password or secret is involved: trust auth on loopback only.
set -euo pipefail
if [ -n "${SERVICE_PORT:-}" ]; then
  echo "POSTGRES_TEST_DSN=postgres://arkism@127.0.0.1:${SERVICE_PORT}/arkism_ci?sslmode=disable" >> "$GITHUB_ENV"
  if [ -n "${SERVICE_CONTAINER:-}" ]; then echo "POSTGRES_TEST_CONTAINER=${SERVICE_CONTAINER}" >> "$GITHUB_ENV"; fi
  exit 0
fi
bin=$(find /usr/lib/postgresql -mindepth 2 -maxdepth 2 -name bin | sort -V | tail -1)
[ -x "$bin/initdb" ] || { echo "::error::No local Postgres on this runner (install postgresql and pgvector)"; exit 1; }
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

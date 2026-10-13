#!/bin/bash
# Run the LoCoMo harness against a fresh built-in runtime (Linux/macOS stage), then stop it.
#
#   bench/locomo/run-runtime.sh STAGE DATA "conv-26 conv-30" -- <harness command...>
#
# STAGE holds runtime/ (memoryd, memory-principal, pgsql); DATA is a data dir made with
# `arkism-desktop --runtime-check DATA` (runtime.json, owner.token, pg). The third argument lists
# the LoCoMo sample ids; each gets its own project "locomo-<sample>" in workspace "default", granted
# to the owner and worker tokens, so conversations never share memories. The harness command gets
# BENCH_URL and BENCH_TOKEN_FILE in its environment. Run as the data dir's owner (Postgres refuses root).
set -euo pipefail
T="$1"; D="$2"; SAMPLES="$3"; shift 3
[ "${1:-}" = "--" ] && shift
R=$T/runtime; B=$R/pgsql/bin
j() { python3 -c "import json,sys;print(json.load(open(sys.argv[1]))[sys.argv[2]])" "$D/runtime.json" "$1"; }
PGP=$(j pg_port); API=$(j api_port)
PG_STARTED=0; SP=; WP=
cleanup() {
	[ -n "$SP" ] && kill "$SP" 2>/dev/null || true
	[ -n "$WP" ] && kill "$WP" 2>/dev/null || true
	wait 2>/dev/null || true
	[ "$PG_STARTED" = 1 ] && "$B/pg_ctl" -D "$D/pg" -m fast -w stop >/dev/null || true
}
trap cleanup EXIT
mkdir -p "$D/logs"
"$B/pg_ctl" -D "$D/pg" -l "$D/logs/postgres.log" -o "-p $PGP -c listen_addresses=127.0.0.1" -w start >/dev/null
PG_STARTED=1
INIT_URL="postgres://arkism_init:$(j init)@127.0.0.1:$PGP/arkism?sslmode=disable"
for s in $SAMPLES; do
	MEMORY_DATABASE_URL="$INIT_URL" MEMORY_PRINCIPAL_TOKEN="$(j owner)" "$R/memory-principal" --principal management:owner --role human --organization default --project "locomo-$s" --actions '*'
	MEMORY_DATABASE_URL="$INIT_URL" MEMORY_PRINCIPAL_TOKEN="$(j worker)" "$R/memory-principal" --principal worker --role service --organization default --project "locomo-$s" --actions '*'
done
# The desktop caps new candidates at 50 per project per day (an abuse guard, not a quality
# decision); a benchmark ingests weeks of conversation in minutes, so the cap is raised.
export MEMORY_STORE=postgres MEMORY_EMBEDDER="${MEMORY_EMBEDDER:-none}" MEMORY_LOCAL_STANDALONE=true
export MEMORY_CANDIDATE_DAILY_LIMIT="${MEMORY_CANDIDATE_DAILY_LIMIT:-5000}"
export MEMORY_DATABASE_URL="postgres://arkism_app:$(j app)@127.0.0.1:$PGP/arkism?sslmode=disable"
export MEMORY_WORKER_TOKEN="$(j worker)" MEMORY_SESSION_ARCHIVE_KEY="$(j archive_key)"
"$R/memoryd" --listen "127.0.0.1:$API" >>"$D/logs/server.log" 2>&1 & SP=$!
"$R/memoryd" --worker >>"$D/logs/worker.log" 2>&1 & WP=$!
unset MEMORY_DATABASE_URL MEMORY_WORKER_TOKEN MEMORY_SESSION_ARCHIVE_KEY
for _ in $(seq 1 100); do curl -sf "http://127.0.0.1:$API/readyz" >/dev/null && break; sleep 0.2; done
curl -sf "http://127.0.0.1:$API/readyz" >/dev/null || { echo "server not ready; see $D/logs/server.log" >&2; exit 1; }
BENCH_URL="http://127.0.0.1:$API" BENCH_TOKEN_FILE="$D/owner.token" "$@"

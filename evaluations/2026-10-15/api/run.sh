#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
output_dir=${1:-evaluations/2026-10-15/api/rerun}
mkdir -p "$output_dir"
run_tmp=$(mktemp -d)
container="arkism-reuse-eval-$(date +%s)-$$"
cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf "$run_tmp"
}
trap cleanup EXIT
# Hex is URL-safe; a fresh credential exists only for this disposable run.
eval_db_password=$(openssl rand -hex 24)
# Dedicated disposable instance; never inspect or modify an existing server.
POSTGRES_PASSWORD="$eval_db_password" docker run -d --name "$container" \
  -e POSTGRES_PASSWORD -e POSTGRES_DB=arkism_eval \
  -p 127.0.0.1::5432 supabase/postgres:17.6.1.136 >/dev/null
for attempt in $(seq 1 60); do
  if docker exec "$container" pg_isready -U postgres -d arkism_eval >/dev/null 2>&1; then break; fi
  sleep 1
done
# Supabase's default postgres role cannot SET ROLE into the test's restricted
# non-superuser role. Only this disposable instance's setup principal is raised.
docker exec "$container" psql -U supabase_admin -d arkism_eval -c 'ALTER ROLE postgres SUPERUSER' >/dev/null
port=$(docker port "$container" 5432 | cut -d: -f2)
go build -o "$run_tmp/arkism" ./cmd/arkism
POSTGRES_TEST_DSN="postgres://postgres:${eval_db_password}@127.0.0.1:$port/arkism_eval?sslmode=disable" \
  ARKISM_EVAL_MCP_BINARY="$run_tmp/arkism" \
  go test -race -json ./internal/memory -run '^TestCrossAgentHTTPPostgresReuse$' -count=3 > "$output_dir/results.jsonl"

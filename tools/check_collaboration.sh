#!/usr/bin/env bash
# Isolated transport/lifecycle tests. External agent CLIs are deterministic stubs.
set -euo pipefail
cd "$(dirname "$0")/.."
run_tmp=$(mktemp -d)
container=""
cleanup() {
  if [[ -n "$container" ]]; then docker rm -fv "$container" >/dev/null 2>&1 || true; fi
  rm -rf "$run_tmp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
test_password=$(openssl rand -hex 24)
container=$(POSTGRES_PASSWORD="$test_password" docker run -d \
  --label dev.arkism.disposable-check=1 -e POSTGRES_PASSWORD \
  -e POSTGRES_DB=arkism_collaboration -p 127.0.0.1::5432 \
  supabase/postgres:17.6.1.136@sha256:5a4314708484bec672de2c09653a5c01fb1c84a998564ac231b0325e2238ed5b)
ready=0
for _ in {1..45}; do
  if docker exec "$container" pg_isready -U postgres -d arkism_collaboration >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
if [[ "$ready" != 1 ]]; then echo 'Disposable PostgreSQL did not start' >&2; exit 1; fi
docker exec "$container" psql -U supabase_admin -d arkism_collaboration \
  -c 'ALTER ROLE postgres SUPERUSER' >/dev/null
port=$(docker port "$container" 5432/tcp)
port=${port##*:}
export POSTGRES_TEST_DSN="postgres://postgres:${test_password}@127.0.0.1:${port}/arkism_collaboration?sslmode=disable"
export POSTGRES_TEST_CONTAINER="$container"
export ARKISM_EVAL_MCP_BINARY="$run_tmp/arkism"
go build -o "$ARKISM_EVAL_MCP_BINARY" ./cmd/arkism
go test -race ./internal/host/crew ./internal/host/skills ./internal/host/capture ./internal/host \
  -run 'Test(Handoff|Collaboration|Recent)' -count=1
go test -race ./internal/memory \
  -run 'Test(PostgresAgentHandoff|CollaborationHTTPPostgres|PostgresKnowledgeGraph|PostgresRecentWork|KnowledgeGraph|RoleRetrieval|RecentWork|RecentSessions|HandoffTransfer|PostgresHandoffTransfer|PostgresWorkArchivePrivacy)' -count=1

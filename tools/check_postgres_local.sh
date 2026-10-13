#!/usr/bin/env bash
# Run the complete check against a disposable Supabase Postgres distribution.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${POSTGRES_TEST_IMAGE:-supabase/postgres:17.6.1.136@sha256:5a4314708484bec672de2c09653a5c01fb1c84a998564ac231b0325e2238ed5b}"
container_id=""
cleanup() {
  if [ -n "$container_id" ]; then docker rm -f "$container_id" >/dev/null; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

admin_password="$(openssl rand -hex 24)"
init_password="$(openssl rand -hex 24)"
app_password="$(openssl rand -hex 24)"
container_id="$(docker run -d --memory 1g --cpus 2 --label dev.arkism.disposable-check=1 \
  -e POSTGRES_PASSWORD="$admin_password" -e POSTGRES_DB=arkism_verify \
  -p 127.0.0.1::5432 "$image")"

ready=0
for _ in {1..45}; do
  if docker exec "$container_id" pg_isready -U postgres -d arkism_verify -h 127.0.0.1 >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  echo 'isolated Supabase Postgres did not start' >&2
  exit 1
fi

host_port="$(docker port "$container_id" 5432/tcp)"
host_port="${host_port##*:}"
docker exec -e PGPASSWORD="$admin_password" -e INIT_PASSWORD="$init_password" -e APP_PASSWORD="$app_password" \
  "$container_id" /bin/bash -ec 'psql --no-psqlrc -v ON_ERROR_STOP=1 -h 127.0.0.1 -U supabase_admin -d arkism_verify <<SQL
CREATE ROLE arkism_init LOGIN PASSWORD '\''$INIT_PASSWORD'\'' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE arkism_app LOGIN PASSWORD '\''$APP_PASSWORD'\'' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
REVOKE ALL ON DATABASE arkism_verify FROM PUBLIC;
GRANT CONNECT ON DATABASE arkism_verify TO arkism_init, arkism_app;
GRANT CREATE ON DATABASE arkism_verify TO arkism_init;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA IF NOT EXISTS extensions AUTHORIZATION supabase_admin;
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA extensions;
CREATE SCHEMA IF NOT EXISTS arkism AUTHORIZATION arkism_init;
ALTER SCHEMA arkism OWNER TO arkism_init;
REVOKE ALL ON SCHEMA arkism FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA arkism TO arkism_init;
GRANT USAGE ON SCHEMA extensions TO arkism_init;
GRANT USAGE ON SCHEMA arkism, extensions TO arkism_app;
ALTER DEFAULT PRIVILEGES FOR ROLE arkism_init IN SCHEMA arkism GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO arkism_app;
ALTER DEFAULT PRIVILEGES FOR ROLE arkism_init IN SCHEMA arkism GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO arkism_app;
ALTER ROLE arkism_init IN DATABASE arkism_verify SET search_path=arkism,extensions,pg_catalog;
ALTER ROLE arkism_app IN DATABASE arkism_verify SET search_path=arkism,extensions,pg_catalog;
SQL'

init_url="postgres://arkism_init:${init_password}@127.0.0.1:${host_port}/arkism_verify?sslmode=disable"
app_url="postgres://arkism_app:${app_password}@127.0.0.1:${host_port}/arkism_verify?sslmode=disable"
admin_url="postgres://supabase_admin:${admin_password}@127.0.0.1:${host_port}/arkism_verify?sslmode=disable"

cd "$repo_root"
MEMORY_STORE=postgres MEMORY_DATABASE_URL="$init_url" go run ./cmd/memoryd --init-db
MEMORY_STORE=postgres MEMORY_DATABASE_URL="$app_url" go run ./cmd/memoryd --check-indexes
docker exec -e PGPASSWORD="$app_password" "$container_id" \
  psql --no-psqlrc -v ON_ERROR_STOP=1 -h 127.0.0.1 -U arkism_app -d arkism_verify -Atqc \
  "SELECT current_schema(), extnamespace::regnamespace::text FROM pg_extension WHERE extname='vector'" |
  grep -qx 'arkism|extensions'

export POSTGRES_TEST_DSN="$admin_url"
export POSTGRES_TEST_CONTAINER="$container_id"
go vet ./...
go test -race ./...

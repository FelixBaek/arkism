#!/usr/bin/env bash
# Start Arkism on this PC: Postgres, the API and the worker (Docker).
# The first run creates .env with random passwords and tokens and prints the owner token once more at the end.
# Run it again any time: it keeps .env and the data, and only starts what is stopped.
set -euo pipefail
cd "$(dirname "$0")"
command -v docker >/dev/null || { echo "Docker is required: https://docs.docker.com/get-docker/" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "Docker Compose v2 is required" >&2; exit 1; }

if [ ! -f .env ]; then
  umask 077
  rand() { od -An -tx1 -N32 /dev/urandom | tr -d ' \n'; }
  {
    echo "ARKISM_DB_ADMIN_PASSWORD=$(rand)"
    echo "ARKISM_INIT_PASSWORD=$(rand)"
    echo "ARKISM_APP_PASSWORD=$(rand)"
    echo "ARKISM_OWNER_TOKEN=$(rand)"
    echo "ARKISM_WORKER_TOKEN=$(rand)"
    echo "ARKISM_API_PORT=8091"
    echo "ARKISM_DB_PORT=55491"
  } > .env
  echo "Created deploy/local/.env (keep it private)."
fi
set -a; . ./.env; set +a

echo "Building the Arkism image…"
docker compose --profile setup build setup
echo "Starting Postgres…"
docker compose up -d --wait db
docker compose exec -T -e PGPASSWORD="$ARKISM_DB_ADMIN_PASSWORD" db \
  psql --no-psqlrc -q -v ON_ERROR_STOP=1 -h 127.0.0.1 -U supabase_admin -d arkism \
  -v init_pw="$ARKISM_INIT_PASSWORD" -v app_pw="$ARKISM_APP_PASSWORD" < roles.sql
echo "Creating the schema…"
docker compose --profile setup run --rm setup
register() { # token principal role
  MEMORY_PRINCIPAL_TOKEN="$1" docker compose --profile setup run --rm -e MEMORY_PRINCIPAL_TOKEN \
    --entrypoint /memory-principal setup \
    --principal "$2" --role "$3" --organization default --project default --actions '*'
}
register "$ARKISM_OWNER_TOKEN" management:owner human
register "$ARKISM_WORKER_TOKEN" worker service
echo "Starting the API and the worker…"
docker compose up -d api worker

url="http://127.0.0.1:${ARKISM_API_PORT:-8091}"
for _ in $(seq 60); do
  curl -fsS "$url/readyz" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS "$url/readyz" >/dev/null || { echo "The API did not become ready. See: docker compose -f deploy/local/compose.yaml logs api" >&2; exit 1; }

ssh_user="$(id -un 2>/dev/null || echo me)"
ssh_host="$(hostname 2>/dev/null || uname -n 2>/dev/null || echo my-server)"
url_note=""
[ "$url" = "http://127.0.0.1:8091" ] || url_note="
     The API is not on the default port: set ARKISM_URL=$url for the SSH user."
cat <<MSG

Arkism is running.
  Server address : $url
  Owner token    : $ARKISM_OWNER_TOKEN (also in deploy/local/.env)

Next, so the desktop app can connect with one SSH address (see docs/server.md):
  1. Install the arkism tool (from the repository root):
       go build -o arkism ./cmd/arkism
       sudo install -d /srv/arkism/bin /srv/arkism/config
       sudo install -m 0755 arkism /srv/arkism/bin/arkism
  2. Save the owner token for your SSH user only:
       sed -n 's/^ARKISM_OWNER_TOKEN=//p' deploy/local/.env | sudo tee /srv/arkism/config/owner.token >/dev/null
       sudo chown "\$USER" /srv/arkism/config/owner.token
       sudo chmod 0600 /srv/arkism/config/owner.token
  3. Check it: /srv/arkism/bin/arkism connect-info   (prints the connection details as JSON)${url_note}
  4. On the PC, make sure 'ssh ${ssh_user}@${ssh_host}' works without a password prompt, then in the app open
     Settings › 연결 (Connection), type ${ssh_user}@${ssh_host} (or your ~/.ssh/config alias) and connect.

Stop: docker compose -f deploy/local/compose.yaml down (data is kept).
MSG

#!/bin/sh
# Nightly production backup: pg_dump -Fc of the dedicated Supabase Postgres
# plus the blob root, bundled by `arkism backup-bundle` and CMS-encrypted by
# `arkism backup-archive`. Writers pause only while pinning a DB snapshot and
# copying blobs; the long dump and encryption run after writers resume.
set -eu

root="${ARKISM_ROOT:-/srv/arkism}"
db_container="${MEMORY_PG_CONTAINER:-arkism-local-supabase-db-db-1}"
arkism="${ARKISM_BIN:-$root/bin/arkism}"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
plain="$root/backup-work/database-$stamp.pgdump"
output="$root/backups/arkism-prod-pg-$stamp.cms"
snapshot_root="$root/backup-work/blobs-$stamp"

exec 9>"${ARKISM_BACKUP_LOCK:-/run/lock/arkism-backup.lock}"
flock -n 9 || exit 0
"$arkism" backup-retention --directory "$root/backups" --execute

cleanup() {
    rm -f "$plain"
    if [ -d "$snapshot_root" ]; then
        rm -rf -- "$snapshot_root"
    fi
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

# Preflight remains before the helper, including on failed backup attempts.
[ "$(docker inspect -f '{{.State.Running}}' "$db_container")" = true ]
umask 077
"$arkism" backup-snapshot --container "$db_container" \
    --blob-root "$root/blobs-pg/prod" --output "$plain" --snapshot-root "$snapshot_root"

"$arkism" backup-archive encrypt \
    --cert "${ARKISM_BACKUP_CERT:?set ARKISM_BACKUP_CERT to the backup recipient certificate}" \
    --output "$output" -- \
    setpriv --reuid=65532 --regid=65532 --clear-groups \
    "$arkism" backup-bundle create --writers-stopped \
    --database-archive "$plain" --blob-root "$snapshot_root"
chmod 600 "$output"

sha256sum "$output"

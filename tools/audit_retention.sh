#!/bin/sh
# Audit events past MEMORY_AUDIT_RETENTION_DAYS (default 30): preview by default,
# `execute` deletes. Runs memoryd from the deployed image as the schema owner
# (config/init.url), the only role that sees every scope's rows.
# Usage: tools/audit_retention.sh [preview|execute]
set -eu
root="${ARKISM_ROOT:-/srv/arkism}"
mode="${1:-preview}"
umask 077
env_file=$(mktemp -p "$root/config" .audit-env.XXXXXX)
trap 'shred -u "$env_file" 2>/dev/null || rm -f "$env_file"' EXIT
printf 'MEMORY_STORE=postgres\nMEMORY_DATABASE_URL=%s\nMEMORY_AUDIT_RETENTION_DAYS=%s\n' \
    "$(tr -d '\n' < "$root/config/init.url")" "${MEMORY_AUDIT_RETENTION_DAYS:-30}" > "$env_file"
docker run --rm --network host --env-file "$env_file" "$(cat "$root/config/image")" -audit-expiry "$mode"

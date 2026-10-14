#!/usr/bin/env bash
# Prints the newest version the update feed has ever offered (latest.json or any installer in it),
# or nothing for an empty feed. build-windows.sh and publish-update.sh check against it.
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# Per-builder values (not in git): see desktop/README.md. Explicit environment wins over the file.
if [[ -f "$DIR/local.env" ]]; then
  while IFS='=' read -r key value; do
    case "$key" in ARKISM_[A-Z_]*) [[ -n "${!key:-}" ]] || export "$key=$value" ;; *) ;; esac
  done < "$DIR/local.env"
fi
FEED="${ARKISM_FEED_DIR:-/srv/arkism-updates}"
{
  sed -n 's/.*"version":"\([0-9.]*\)".*/\1/p' "$FEED/latest.json" 2>/dev/null || true
  { ls "$FEED" 2>/dev/null || true; } | sed -n 's/^Arkism-\([0-9]*\.[0-9]*\.[0-9]*\)-setup\.exe$/\1/p'
} | sort -V | tail -1

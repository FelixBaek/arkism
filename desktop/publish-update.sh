#!/usr/bin/env bash
# Publish a built installer to the update feed the app checks (Settings > 앱 업데이트).
#
#   desktop/publish-update.sh 0.5.0
#
# The feed folder is ARKISM_FEED_DIR (default /srv/arkism-updates), served over HTTPS at the
# address compiled into the app as ARKISM_UPDATE_FEED.
# The installer is copied first and latest.json last, so the app never sees a
# version whose file is missing.
set -euo pipefail
VERSION="${1:-}"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "usage: $0 x.y.z" >&2; exit 2; }
DIR="$(cd "$(dirname "$0")" && pwd)"
# Per-builder values (not in git): see README.md. Explicit environment wins over the file.
if [[ -f "$DIR/local.env" ]]; then
  while IFS='=' read -r key value; do
    case "$key" in ARKISM_[A-Z_]*) [[ -n "${!key:-}" ]] || export "$key=$value" ;; *) ;; esac
  done < "$DIR/local.env"
fi
FEED="${ARKISM_FEED_DIR:-/srv/arkism-updates}"
FILE="Arkism-$VERSION-setup.exe"
[[ -f "$DIR/dist/$FILE" ]] || { echo "missing $DIR/dist/$FILE (run build-windows.sh)" >&2; exit 1; }
mkdir -p "$FEED"
# One publish at a time: the check below and the latest.json write happen under the same lock.
exec 9>"$FEED/.publish.lock"
flock 9
# Other sessions publish too: never move the feed back. The newest is whatever latest.json or any
# installer already in the feed says, and the new version must be above it.
NEWEST="$("$DIR/feed-newest.sh")"
if [[ -n "$NEWEST" ]] && { [[ "$NEWEST" = "$VERSION" ]] || [[ "$(printf '%s\n%s\n' "$NEWEST" "$VERSION" | sort -V | tail -1)" != "$VERSION" ]]; }; then
  echo "feed already has $NEWEST: publish a newer version than that" >&2; exit 1
fi
cp "$DIR/dist/$FILE" "$FEED/$FILE"
SUM="$(sha256sum "$FEED/$FILE" | cut -d' ' -f1)"
printf '{"version":"%s","file":"%s","sha256":"%s"}\n' "$VERSION" "$FILE" "$SUM" > "$FEED/latest.json.tmp"
mv "$FEED/latest.json.tmp" "$FEED/latest.json"
echo "published $VERSION ($SUM)"

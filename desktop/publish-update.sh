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
RELEASE="${ARKISM_RELEASE_DIR:-$DIR/dist}"
CHANNEL="${ARKISM_UPDATE_CHANNEL:-stable}"
: "${ARKISM_UPDATE_FEED:?Set the HTTPS URL compiled into the Windows app}"
: "${ARKISM_UPDATE_PUBLIC_KEY:?Set the independently trusted Tauri-encoded minisign public key}"
# This host receives only a pre-signed bundle. It never needs a private key.
python3 "$DIR/../tools/release_artifacts.py" verify --directory "$RELEASE" \
  --version "$VERSION" --feed "$ARKISM_UPDATE_FEED" --channel "$CHANNEL"
mkdir -p "$FEED"
exec 9>"$FEED/.publish.lock"
flock 9
NEWEST="$("$DIR/feed-newest.sh")"
if [[ -n "$NEWEST" ]] && { [[ "$NEWEST" = "$VERSION" ]] || [[ "$(printf '%s\n%s\n' "$NEWEST" "$VERSION" | sort -V | tail -1)" != "$VERSION" ]]; }; then
  echo "feed already has $NEWEST: publish a newer version than that" >&2; exit 1
fi
stage="$(mktemp -d "$FEED/.publish.XXXXXX")"
trap 'rm -rf -- "$stage"' EXIT
cp "$RELEASE"/* "$stage/"
# Verify the staged bytes again before exposing any new feed assets.
python3 "$DIR/../tools/release_artifacts.py" verify --directory "$stage" \
  --version "$VERSION" --feed "$ARKISM_UPDATE_FEED" --channel "$CHANNEL"
manifest=latest.json
[[ "$CHANNEL" != beta ]] || manifest=latest-beta.json
for file in "$stage"/*; do
  name="${file##*/}"
  [[ "$name" == "$manifest" || "$name" == "$manifest.minisig" ]] && continue
  mv -- "$file" "$FEED/$name"
done
# Readers overlapping these two renames fail signature verification and retry;
# they cannot install unauthenticated bytes. The manifest is always last.
mv -- "$stage/$manifest.minisig" "$FEED/$manifest.minisig"
mv -- "$stage/$manifest" "$FEED/$manifest"
echo "published verified Windows release $VERSION"

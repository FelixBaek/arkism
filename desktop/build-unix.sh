#!/usr/bin/env bash
# Build the Arkism app for macOS (.app + .dmg) or Linux (.AppImage + .deb) on that system.
# Used by .github/workflows/release.yml; works the same on a Mac or Linux PC.
#
#   desktop/build-unix.sh --version 0.3.0
#
# Needs Go, Rust, Node (npx), a C compiler, bison and flex (for the built-in Postgres), and on
# Linux the WebKitGTK build packages. Values compiled into the app come from the environment:
# ARKISM_UPDATE_FEED (the release download address), and optionally ARKISM_DEFAULT_SERVER.
# macOS signing and notarization happen only when APPLE_CERTIFICATE and friends are set
# (see the workflow); otherwise the app is signed ad hoc.
#
# Output: desktop/dist/ (the bundles and their .sha256).
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
VERSION=""
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
done
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "--version x.y.z is required" >&2; exit 2; }
case "$(uname -s)" in
  Darwin) BUNDLES="app,dmg" ;;
  Linux) BUNDLES="appimage,deb" ;;
  *) echo "build-unix.sh is for macOS and Linux; Windows uses installer/build-windows.ps1" >&2; exit 2 ;;
esac
STAGE="$DIR/stage"
CACHE="${ARKISM_BUILD_CACHE:-$DIR/.build-cache}"
rm -rf "$STAGE"
mkdir -p "$STAGE/runtime/integration" "$DIR/dist"

# The built-in Arkism (server programs and database roles) and the agent client, which the app
# finds in its resources under runtime/ (runtime.rs: find_programs, find_agent_client).
export CGO_ENABLED=0
go build -C "$DIR/.." -trimpath -o "$STAGE/runtime/memoryd" ./cmd/memoryd
go build -C "$DIR/.." -trimpath -o "$STAGE/runtime/memory-principal" ./cmd/memory-principal
go build -C "$DIR/.." -trimpath -o "$STAGE/runtime/integration/arkism" ./cmd/arkism
cp "$DIR/../tools/roles.sql" "$STAGE/runtime/"
"$DIR/installer/prepare-runtime.sh" "$CACHE" "$STAGE"

# Bundle settings for this build only; tauri.conf.json keeps bundling off for the Windows NSIS path.
CONFIG="$(mktemp)"; trap 'rm -f "$CONFIG"' EXIT
SIGN="${APPLE_SIGNING_IDENTITY:--}" # "-" signs ad hoc
cat > "$CONFIG" <<JSON
{
  "version": "$VERSION",
  "bundle": {
    "active": true,
    "icon": ["icons/icon.png"],
    "resources": { "../stage/runtime/": "runtime/" },
    "category": "DeveloperTool",
    "shortDescription": "Shared memory and skills for coding agents",
    "macOS": { "minimumSystemVersion": "11.0", "signingIdentity": "$SIGN" },
    "linux": { "deb": { "depends": [] } }
  }
}
JSON
export RUSTFLAGS="${RUSTFLAGS:-} --remap-path-prefix=$HOME=~"
( cd "$DIR/src-tauri" && npx --yes @tauri-apps/cli@2.12.1 build --ci --bundles "$BUNDLES" --config "$CONFIG" -- --locked )

# The built app must start its built-in Arkism, back it up, erase and restore it (no window).
cp "$DIR/src-tauri/target/release/arkism-desktop" "$STAGE/"
CHECK="$(mktemp -d)"
"$STAGE/arkism-desktop" --runtime-check "$CHECK/data" || { cat "$CHECK/data/check.json" "$CHECK"/data/logs/*.log 2>/dev/null | tail -40; exit 1; }
cat "$CHECK/data/check.json"; echo
rm -rf "$CHECK"

OUT="$DIR/src-tauri/target/release/bundle"
made=()
shopt -s nullglob
for f in "$OUT"/dmg/*.dmg "$OUT"/appimage/*.AppImage "$OUT"/deb/*.deb; do
  cp "$f" "$DIR/dist/"
  made+=("$(basename "$f")")
done
if [ "$(uname -s)" = Darwin ]; then
  # The .app as a zip as well, for copying by hand.
  zip="Arkism-$VERSION-macos-$(uname -m).app.zip"
  ( cd "$OUT/macos" && ditto -c -k --keepParent Arkism.app "$DIR/dist/$zip" )
  made+=("$zip")
fi
[ ${#made[@]} -gt 0 ] || { echo "no bundle was made" >&2; exit 1; }
cd "$DIR/dist"
for f in "${made[@]}"; do
  if command -v sha256sum >/dev/null; then sha256sum "$f" > "$f.sha256"; else shasum -a 256 "$f" > "$f.sha256"; fi
  echo "$f"
done

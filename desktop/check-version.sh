#!/usr/bin/env bash
# Accepts a local build version only as the latest published GitHub release or <release>-test.N.
# build-windows.sh (local builds; releases build from their tag in CI) calls it: a hand-picked number above the release made test builds
# look newer than anything published.
set -euo pipefail
VERSION="${1:-}"
[[ "$VERSION" =~ ^([0-9]+\.[0-9]+\.[0-9]+)(-test\.[1-9][0-9]{0,3})?$ ]] || { echo "version x.y.z or x.y.z-test.N is required" >&2; exit 2; }
BASE="${BASH_REMATCH[1]}"
REPO="${ARKISM_RELEASE_REPO:-FelixBaek/arkism}"
LATEST="$(curl -fsSL --proto =https --proto-redir =https --max-time 20 "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/^ *"tag_name": *"v\([0-9.]*\)".*/\1/p')" || true
[[ -n "$LATEST" ]] || { echo "could not read the latest release of $REPO" >&2; exit 2; }
[[ "$BASE" = "$LATEST" ]] || { echo "the latest release is $LATEST: build $LATEST-test.N, not $VERSION" >&2; exit 2; }

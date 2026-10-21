#!/usr/bin/env bash
# Build the Arkism Windows installer on a Windows build PC (native MSVC + NSIS) over SSH.
#
#   desktop/build-windows.sh --version 0.1.0 [--host <build-pc>] [--relock]
#
# desktop/local.env (not in git) supplies the build PC and the values compiled into the app:
#   ARKISM_BUILD_HOST, ARKISM_BUILD_DIR (default C:/arkism-build), ARKISM_MAKENSIS (default makensis),
#   ARKISM_DEFAULT_SERVER, ARKISM_UPDATE_FEED, ARKISM_SSH_HOST, ARKISM_SERVER_BIN.
#
# Output: desktop/dist/Arkism-<version>-setup.exe (+ .sha256). The first build
# writes Cargo.lock back here; later builds use it with --locked. --relock
# rebuilds the lock after Cargo.toml changed and writes it back too.
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
# Per-builder values (not in git): see README.md. Explicit environment wins over the file.
if [[ -f "$DIR/local.env" ]]; then
  while IFS='=' read -r key value; do
    case "$key" in ARKISM_[A-Z_]*) [[ -n "${!key:-}" ]] || export "$key=$value" ;; *) ;; esac
  done < "$DIR/local.env"
fi
VERSION="" HOST="${ARKISM_BUILD_HOST:-}" RELOCK=0
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --host) HOST="$2"; shift 2 ;;
    --relock) RELOCK=1; shift ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
done
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "--version x.y.z is required" >&2; exit 2; }
[[ -n "$HOST" ]] || { echo "--host <build-pc> (or ARKISM_BUILD_HOST in desktop/local.env) is required" >&2; exit 2; }
BUILD_DIR="${ARKISM_BUILD_DIR:-C:/arkism-build}"
MAKENSIS="${ARKISM_MAKENSIS:-makensis}"
# Values compiled into the app (option_env! in src-tauri); unset ones keep the public defaults.
BUILD_ENV=""
for name in ARKISM_DEFAULT_SERVER ARKISM_DEFAULT_WORKSPACE ARKISM_DEFAULT_PROJECT ARKISM_UPDATE_FEED ARKISM_UPDATE_PUBLIC_KEY ARKISM_UPDATE_CHANNEL ARKISM_SSH_HOST ARKISM_SERVER_BIN; do
  value="${!name:-}"
  # Unset here means unset in the app: a variable left in the build PC's own environment must not leak in.
  if [[ -z "$value" ]]; then BUILD_ENV+="Remove-Item Env:$name -ErrorAction SilentlyContinue"$'\n'; continue; fi
  [[ "$value" != *"'"* ]] || { echo "$name must not contain a quote" >&2; exit 2; }
  BUILD_ENV+="\$env:$name = '$value'"$'\n'
done
# Check the feed first: another session may have shipped a newer version (see feed-newest.sh).
NEWEST="$("$(dirname "$0")/feed-newest.sh")"
if [[ -n "$NEWEST" ]] && { [[ "$NEWEST" = "$VERSION" ]] || [[ "$(printf '%s\n%s\n' "$NEWEST" "$VERSION" | sort -V | tail -1)" != "$VERSION" ]]; }; then
  echo "the feed already has $NEWEST: build a newer version than that" >&2; exit 2
fi
REMOTE="$BUILD_DIR/$VERSION-$(date -u +%Y%m%dT%H%M%SZ)-$$"
quiet() { grep -a -v -i 'post-quantum\|store now\|pq.html' || true; }
# The remote step's exit status, not the filter's, decides success.
run() { "$@" 2>&1 | quiet; [[ "${PIPESTATUS[0]}" -eq 0 ]] || { echo "== FAILED: $1 ${*: -1}" >&2; exit 1; }; }
LOCKED=""; [[ -f "$DIR/src-tauri/Cargo.lock" ]] && [[ "$RELOCK" = 0 ]] && LOCKED="--locked"

# The remote builder receives the production assets, never build dependencies.
npm ci --prefix "$DIR/frontend" --no-audit --no-fund
node "$DIR/frontend/build.cjs"
node "$DIR/frontend/check.cjs"

echo "== ship to $HOST:$REMOTE"
ssh -o BatchMode=yes "$HOST" "powershell -NoProfile -Command \"New-Item -ItemType Directory '$REMOTE/stage' | Out-Null\"" 2>&1 | quiet
( cd "$DIR" && tar cf - --exclude=src-tauri/target --exclude=dist ui ui-dist src-tauri installer ) | ssh -o BatchMode=yes "$HOST" "tar xf - -C $REMOTE" 2>&1 | quiet

WORK="$(mktemp -d)"; trap 'rm -rf -- "$WORK"' EXIT
# The client embeds the Windows task helpers; both packaging paths ship the same payload.
GOOS=windows GOARCH=amd64 go build -C "$DIR/.." -o "$WORK/arkism.exe" ./cmd/arkism
run ssh -o BatchMode=yes "$HOST" "powershell -NoProfile -Command \"New-Item -ItemType Directory '$REMOTE/stage/integration' | Out-Null\""
run scp -q -o BatchMode=yes "$WORK/arkism.exe" "$HOST:$REMOTE/stage/integration/"

# Built-in Arkism: the server programs (built here) and the database roles. The build PC adds
# Postgres and pgvector (installer/prepare-runtime.ps1) because pgvector needs its C compiler.
GOOS=windows GOARCH=amd64 go build -C "$DIR/.." -o "$WORK/memoryd.exe" ./cmd/memoryd
GOOS=windows GOARCH=amd64 go build -C "$DIR/.." -o "$WORK/memory-principal.exe" ./cmd/memory-principal
GOOS=windows GOARCH=amd64 go build -C "$DIR/.." -ldflags=-H=windowsgui -o "$WORK/arkism-installer.exe" ./cmd/arkism-installer
run ssh -o BatchMode=yes "$HOST" "powershell -NoProfile -Command \"New-Item -ItemType Directory '$REMOTE/stage/runtime' | Out-Null\""
run scp -q -o BatchMode=yes "$WORK/memoryd.exe" "$WORK/memory-principal.exe" "$WORK/arkism-installer.exe" "$DIR/../tools/roles.sql" "$HOST:$REMOTE/stage/runtime/"

cat > "$WORK/remote.ps1" <<PS1
\$ErrorActionPreference = 'Stop'
\$env:TAURI_CONFIG = '{"version":"$VERSION","build":{"frontendDist":"../ui-dist"}}'
\$env:CARGO_TARGET_DIR = '$BUILD_DIR/target'
# Keep the build PC's paths (user name, folders) out of the binary's panic locations.
\$env:RUSTFLAGS = "--remap-path-prefix=\$(\$env:USERPROFILE)=~ --remap-path-prefix=$BUILD_DIR=build --remap-path-prefix=${BUILD_DIR//\//\\}=build"
$BUILD_ENV# The rustup proxy in .cargo/bin may be broken; use the toolchain directly.
\$env:PATH = \$env:USERPROFILE + '/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin;' + \$env:PATH
& '$REMOTE/installer/prepare-runtime.ps1' -Cache '$BUILD_DIR/cache' -Stage '$REMOTE/stage'
Set-Location '$REMOTE/src-tauri'
\$ErrorActionPreference = 'Continue'
cmd /c "cargo build --release $LOCKED > ..\\cargo.log 2>&1"
\$code = \$LASTEXITCODE
Get-Content ../cargo.log -Tail 8
if (\$code -ne 0) { throw 'cargo build failed' }
cmd /c "cargo test --release $LOCKED > ..\\test.log 2>&1"
\$code = \$LASTEXITCODE
Get-Content ../test.log -Tail 4
if (\$code -ne 0) { throw 'cargo test failed' }
\$ErrorActionPreference = 'Stop'
Copy-Item '$BUILD_DIR/target/release/arkism-desktop.exe' '$REMOTE/stage/Arkism.exe' -Force
. '$REMOTE/installer/package-windows.ps1'
\$package = Invoke-WindowsInstallerPackage -Version '$VERSION' -Stage '$REMOTE/stage' -Makensis '$MAKENSIS' -Output '$REMOTE/Arkism-$VERSION-setup.exe'
PS1
echo "== cargo build + installer on $HOST"
scp -q -o BatchMode=yes "$WORK/remote.ps1" "$HOST:$REMOTE/remote.ps1" 2>/dev/null
run ssh -o BatchMode=yes "$HOST" "powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $REMOTE/remote.ps1"
mkdir -p "$DIR/dist"
OUT="$DIR/dist/Arkism-$VERSION-setup.exe"
scp -q -o BatchMode=yes "$HOST:$REMOTE/Arkism-$VERSION-setup.exe" "$OUT" 2>/dev/null
[[ -n "$LOCKED" ]] || scp -q -o BatchMode=yes "$HOST:$REMOTE/src-tauri/Cargo.lock" "$DIR/src-tauri/Cargo.lock" 2>/dev/null
sha256sum "$OUT" | tee "$OUT.sha256"
# What changed since the last tag, grouped (연구·앱·엔진·빌드·문서) in Korean and English, beside the installer.
if prev="$(git -C "$DIR" describe --tags --abbrev=0 HEAD^ 2>/dev/null)"; then # from HEAD^: a tag on HEAD is this release
  python3 "$DIR/../tools/release_notes.py" --range "$prev..HEAD" --title "Arkism $VERSION" > "$OUT.notes.md" && echo "== notes: $OUT.notes.md"
fi

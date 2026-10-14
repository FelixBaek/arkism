#!/usr/bin/env bash
# Built-in Arkism for the macOS and Linux bundles: Postgres with pgvector, built from source so the
# bundle needs nothing from the user's system beyond libc (and zlib, which every system has).
# The Windows installer uses prepare-runtime.ps1 instead.
#
#   desktop/installer/prepare-runtime.sh <cache-dir> <stage-dir>
#
# Prepared once per version in <cache-dir>, then copied to <stage-dir>/runtime/pgsql with the
# same layout as Windows: bin/ (server and setup tools), lib/, share/. The programs find lib/ and
# share/ relative to themselves, so the folder works wherever the bundle puts it.
set -euo pipefail
CACHE="${1:?cache dir}" STAGE="${2:?stage dir}"
PG_VERSION=17.6
VECTOR_VERSION=0.8.1
# Pinned sources: a changed download fails the build instead of shipping something else.
PG_SHA256=e0630a3600aea27511715563259ec2111cd5f4353a4b040e0be827f94cd7a8b0
VECTOR_SHA256=a9094dfb85ccdde3cbb295f1086d4c71a20db1d26bf1d6c39f07a7d164033eb4
OS="$(uname -s)"
JOBS="$( (command -v nproc >/dev/null && nproc) || sysctl -n hw.ncpu 2>/dev/null || echo 4)"
DIR="$CACHE/postgres-$PG_VERSION-pgvector-$VECTOR_VERSION-$OS-$(uname -m)"
PG="$DIR/pgsql"

sha256() { local file="$1"; if command -v sha256sum >/dev/null; then sha256sum "$file" | cut -d' ' -f1; else shasum -a 256 "$file" | cut -d' ' -f1; fi; }
fetch() {
  local url="$1" file="$2" want="$3"
  curl -fsSL --retry 3 -o "$file" "$url"
  local got; got="$(sha256 "$file")"
  if [[ "$got" != "$want" ]]; then
    if [[ -n "${ARKISM_PRINT_SHA:-}" ]]; then echo "sha256 $file = $got" >&2; else echo "checksum mismatch for $url: $got" >&2; exit 1; fi
  fi
}

if [[ ! -f "$PG/lib/vector.so" ]] && [[ ! -f "$PG/lib/vector.dylib" ]]; then
  rm -rf "$DIR"; mkdir -p "$DIR/src"
  fetch "https://ftp.postgresql.org/pub/source/v$PG_VERSION/postgresql-$PG_VERSION.tar.bz2" "$DIR/src/pg.tar.bz2" "$PG_SHA256"
  fetch "https://github.com/pgvector/pgvector/archive/refs/tags/v$VECTOR_VERSION.tar.gz" "$DIR/src/vector.tar.gz" "$VECTOR_SHA256"
  tar xjf "$DIR/src/pg.tar.bz2" -C "$DIR/src"
  tar xzf "$DIR/src/vector.tar.gz" -C "$DIR/src"
  # Programs find libpq beside them: $ORIGIN on Linux, @loader_path on macOS (set after install).
  if [[ "$OS" = Linux ]]; then export LDFLAGS="-Wl,-rpath,'\$\$ORIGIN/../lib'"; fi
  # Only what a loopback database needs: no ICU, readline, OpenSSL, Kerberos or LDAP, so there is
  # no system library to be missing. Passwords still use SCRAM (built in without OpenSSL).
  ( cd "$DIR/src/postgresql-$PG_VERSION" &&
    ./configure --prefix="$PG" --disable-rpath --without-icu --without-readline --with-zlib \
      --without-openssl --without-gssapi --without-ldap --without-libxml --without-lz4 --without-zstd >"$DIR/configure.log" &&
    make -s -j"$JOBS" >"$DIR/make.log" && make -s install >>"$DIR/make.log" )
  ( cd "$DIR/src/pgvector-$VECTOR_VERSION" &&
    make -s PG_CONFIG="$PG/bin/pg_config" OPTFLAGS="" >"$DIR/vector.log" && make -s PG_CONFIG="$PG/bin/pg_config" install >>"$DIR/vector.log" )
  if [[ "$OS" = Darwin ]]; then
    # Point every program at the libpq beside it instead of the build path.
    for f in "$PG"/bin/*; do
      for dep in $(otool -L "$f" | awk 'NR>1 {print $1}' | grep "^$PG/lib/" || true); do
        install_name_tool -change "$dep" "@loader_path/../lib/$(basename "$dep")" "$f"
      done
    done
    for f in "$PG"/lib/*.dylib; do install_name_tool -id "@loader_path/../lib/$(basename "$f")" "$f" 2>/dev/null || true; done
  fi
  # Only what the app runs (the same tools as on Windows), the libraries and share/.
  keep="postgres pg_ctl initdb psql pg_dump pg_restore"
  for f in "$PG"/bin/*; do case " $keep " in *" $(basename "$f") "*) ;; *) rm -f "$f" ;; esac; done
  rm -rf "$PG/include" "$PG/lib/pgxs" "$PG/lib/pkgconfig" "$PG"/lib/*.a "$PG/share/doc" "$PG/share/man" "$DIR/src"
fi
mkdir -p "$STAGE/runtime"
rm -rf "$STAGE/runtime/pgsql"
cp -R "$PG" "$STAGE/runtime/pgsql"
echo "built-in runtime staged: $(du -sm "$STAGE/runtime" | cut -f1) MB"

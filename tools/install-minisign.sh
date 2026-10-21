#!/usr/bin/env bash
# CI signing/test host only; this does not build or distribute a Linux app.
# Pin observed official 0.12 release bytes. Recheck the publisher when updating.
set -euo pipefail
[[ "$(uname -sm)" == 'Linux x86_64' ]] || { echo 'Expected Linux x86_64 signing host' >&2; exit 1; }
dest="${1:?usage: bash tools/install-minisign.sh DESTINATION}"
mkdir -p "$dest"
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fLsS \
  https://github.com/jedisct1/minisign/releases/download/0.12/minisign-0.12-linux.tar.gz -o "$work/minisign.tar.gz"
echo '9a599b48ba6eb7b1e80f12f36b94ceca7c00b7a5173c95c3efc88d9822957e73  '"$work/minisign.tar.gz" | sha256sum -c -
tar xOf "$work/minisign.tar.gz" minisign-linux/x86_64/minisign > "$dest/minisign"
chmod 755 "$dest/minisign"

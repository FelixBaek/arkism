# Arkism server update, run over SSH by the desktop app (server_update.rs).
# stdin: arkism-server-<version>-linux-<arch>.tar.gz, already signature-checked on the PC.
# $1: the version to install, $2: the remote arkism path from the connection form (optional).
# stdout: "step <name>" lines and a final "ok <version>"; stderr: one error code line.
# Order: find what blocks the update (write, restart, service) before changing anything,
# back up, replace by rename, restart, check /healthz, and roll back when the check fails.
set -u
want=$1
hint=${2:-}
files="arkism arkism-crew memoryd memory-principal"
fail() { printf '%s\n' "$1" >&2; exit 3; }
step() { printf 'step %s\n' "$1"; }

dir=
for b in "$hint" /srv/arkism/bin/arkism /srv/arkism/bin/ark "$(command -v arkism 2>/dev/null)"; do
  if [ -n "$b" ] && [ -x "$b" ]; then dir=$(cd "$(dirname "$b")" && pwd -P); break; fi
done
[ -n "$dir" ] || fail arkism_not_found
step "found $dir"

# Root needs nothing; anyone else writes the folder themselves or uses passwordless sudo.
as=
if [ "$(id -u)" != 0 ]; then
  if sudo -n true >/dev/null 2>&1; then as="sudo -n"; elif [ ! -w "$dir" ]; then fail "needs_write $dir"; fi
fi

# The running systemd services whose command line starts a program in this folder.
units=
for u in $(systemctl list-units --type=service --state=running --no-legend --plain 2>/dev/null | awk '{print $1}'); do
  case "$(systemctl show -p ExecStart --value "$u" 2>/dev/null)" in *"$dir/"*) units="$units $u";; esac
done
[ -n "$units" ] || fail "no_service $dir"
if [ "$(id -u)" != 0 ] && [ -z "$as" ]; then fail "needs_sudo_restart:$units"; fi
step "services$units"

tmp=$($as mktemp -d "$dir/.update-XXXXXX") || fail "needs_write $dir"
cleanup() { $as rm -rf "$tmp"; }
trap cleanup EXIT
$as tar -xzf - -C "$tmp" || fail bad_archive
[ "$(ls -A "$tmp" | sort | tr '\n' ' ')" = "arkism arkism-crew memory-principal memoryd " ] || fail bad_archive
for f in $files; do [ -f "$tmp/$f" ] && [ ! -L "$tmp/$f" ] || fail bad_archive; done
$as chmod 0755 $(for f in $files; do printf '%s ' "$tmp/$f"; done)
[ "$("$tmp/arkism" version 2>/dev/null)" = "$want" ] || fail "bad_archive version"
# The checker stays outside the swapped files so it also runs after a rollback to an older build.
$as cp "$tmp/arkism" "$tmp/.check" || fail "needs_write $dir"

backup="$dir/.backup-$(date +%Y%m%d-%H%M%S)"
$as mkdir "$backup" || fail "needs_write $dir"
for f in $files; do
  if [ -e "$dir/$f" ]; then $as cp -p "$dir/$f" "$backup/$f" || fail "needs_write $dir"; fi
done
step "backup $backup"

for f in $files; do $as mv -f "$tmp/$f" "$dir/$f" || break; done
step replace
restart() { for u in $units; do $as systemctl restart "$u" || return 1; done; }
restart
step restart

if "$tmp/.check" server-check --want "$want" --wait 60s >/dev/null; then
  step check
  printf 'ok %s\n' "$want"
  exit 0
fi

# Roll back: the backup copies return, new files without a predecessor leave.
for f in $files; do
  if [ -e "$backup/$f" ]; then $as cp -p "$backup/$f" "$dir/.rollback-$f" && $as mv -f "$dir/.rollback-$f" "$dir/$f"; else $as rm -f "$dir/$f"; fi
done
step rollback
if restart && "$tmp/.check" server-check --want "" --wait 60s >/dev/null; then
  fail health_failed_rolled_back
fi
fail "rollback_failed $backup"

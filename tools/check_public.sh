#!/usr/bin/env bash
# Fail when files that would go public match a maintainer's private denylist.
#
#   ARKISM_PUBLIC_DENYLIST=/path/to/denylist.txt tools/check_public.sh [DIR]
#
# The denylist holds one Perl-style regex per line (case-insensitive); empty lines
# and lines starting with # are ignored. It is never part of the repository.
# DIR defaults to this repository; in a git checkout only tracked files are read,
# otherwise (an exported copy) every file under DIR. LICENSE is skipped because
# it names the copyright holder on purpose.
# Exit 0 = clean, 1 = hits (printed as file:line:text), 2 = usage error.
set -euo pipefail

list="${ARKISM_PUBLIC_DENYLIST:-}"
if [[ -z "$list" ]] || [[ ! -r "$list" ]]; then
	echo "check_public: set ARKISM_PUBLIC_DENYLIST to a readable denylist file" >&2
	exit 2
fi
dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$dir"

pat=$(command grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$list" | sed 's/.*/(?:&)/' | paste -sd'|' -)
if [[ -z "$pat" ]]; then
	echo "check_public: $list has no patterns" >&2
	exit 2
fi
rc=0
printf 'x\n' | command grep -qiP -e "$pat" || rc=$?
if [[ "$rc" -gt 1 ]]; then
	echo "check_public: $list has an invalid pattern" >&2
	exit 2
fi

if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
	files() { git ls-files -z; }
else
	files() { find . -type f -not -path './.git/*' -print0; }
fi

# grep exits 1 when a batch has no hit; any worse status (2+) aborts xargs with 124.
rc=0
hits=$(files | command grep -zv -e '^\(\./\)\?LICENSE$' |
	xargs -0 -r sh -c 'grep -nHIiP -e "$0" -- "$@"; s=$?; [ "$s" -le 1 ] || exit 255' "$pat") || rc=$?
if [[ "$rc" -ne 0 ]] && [[ "$rc" -ne 123 ]]; then
	echo "check_public: grep failed ($rc)" >&2
	exit 2
fi
if [[ -n "$hits" ]]; then
	echo "check_public: private text found:"
	echo "$hits"
	exit 1
fi
echo "check_public: clean"

#!/bin/sh
set -eu

base="${TMPDIR:-/tmp}/ternal-backup-test-$$"
src="$base/src"
out="$base/out"
mkdir -p "$src/nested" "$out"
printf 'ok\n' >"$src/nested/file.txt"

cleanup() {
	rm -rf "$base"
}
trap cleanup EXIT INT TERM HUP

archive=$(sh "$(dirname "$0")/local-backup.sh" "$src" "$out")

test -f "$archive"
case "$archive" in
	"$out"/*.tar.gz) ;;
	*) echo "archive outside output dir: $archive" >&2; exit 1 ;;
esac
tar -tzf "$archive" | grep -q 'nested/file.txt'

# Red check: restrictive permissions under a permissive umask.
mode() {
	# GNU stat first: on Linux, `stat -f` succeeds and prints filesystem
	# information, so the BSD form must never be tried first.
	if stat -c '%a' "$1" >/dev/null 2>&1; then
		stat -c '%a' "$1"
	else
		stat -f '%Lp' "$1"
	fi
}
if [ "$(mode "$archive")" != "600" ]; then
	echo "archive mode is $(mode "$archive"), want 600" >&2; exit 1
fi
if [ "$(mode "$out")" != "700" ]; then
	echo "output dir mode is $(mode "$out"), want 700" >&2; exit 1
fi

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

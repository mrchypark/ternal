#!/bin/sh
set -eu

src=${1:-${RHIZA_DATA_DIR:-ternal-rhiza}}
out=${2:-backups}

if [ ! -d "$src" ]; then
	echo "backup source directory not found: $src" >&2
	exit 1
fi

mkdir -p "$out"
base=$(basename "$src")
test -n "$base" || base=rhiza
archive="$out/ternal-$base-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"
tmp="$archive.$$"

cleanup() {
	rm -f "$tmp"
}
trap cleanup EXIT INT TERM HUP

tar -czf "$tmp" -C "$src" .
mv "$tmp" "$archive"
trap - EXIT INT TERM HUP
printf '%s\n' "$archive"

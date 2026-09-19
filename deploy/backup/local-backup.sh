#!/bin/sh
set -eu

# Backups contain key material: never let a permissive umask leak them.
umask 077

src=${1:-${TERNAL_DATA_DIR:-ternal-data}}
out=${2:-backups}

if [ ! -d "$src" ]; then
	echo "backup source directory not found: $src" >&2
	exit 1
fi

mkdir -p "$out"
chmod 0700 "$out"
base=$(basename "$src")
test -n "$base" || base=data
archive="$out/ternal-$base-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"
tmp="$archive.$$"

cleanup() {
	rm -f "$tmp"
}
trap cleanup EXIT INT TERM HUP

tar -czf "$tmp" -C "$src" .
chmod 0600 "$tmp"
mv "$tmp" "$archive"
trap - EXIT INT TERM HUP
printf '%s\n' "$archive"

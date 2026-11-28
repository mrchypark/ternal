#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
out="$root/internal/web/static"

mkdir -p "$out"
cp "$root/frontend/src/assets/brand/ternal-logo-640.png" "$out/ternal-logo-640.png"
cp "$root/frontend/src/assets/brand/ternal-icon-256.png" "$out/ternal-icon-256.png"

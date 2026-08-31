#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
out="$root/internal/web/static"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM

case "$(uname -s):$(uname -m)" in
	Darwin:arm64) tailwind_asset=tailwindcss-macos-arm64; tailwind_sha=cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d ;;
	Darwin:x86_64) tailwind_asset=tailwindcss-macos-x64; tailwind_sha=7922e0953f2110c05976e3bf58f14e643d90427575e766b7d433f5f80cbee7e1 ;;
	Linux:aarch64) tailwind_asset=tailwindcss-linux-arm64; tailwind_sha=55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195 ;;
	Linux:x86_64) tailwind_asset=tailwindcss-linux-x64; tailwind_sha=dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a ;;
	*) echo "unsupported Tailwind build platform: $(uname -s) $(uname -m)" >&2; exit 1 ;;
esac

mkdir -p "$out"
curl --fail --silent --show-error --location \
	"https://github.com/tailwindlabs/tailwindcss/releases/download/v4.3.3/$tailwind_asset" \
	-o "$work/tailwindcss"
printf '%s  %s\n' "$tailwind_sha" "$work/tailwindcss" | shasum -a 256 -c -
chmod +x "$work/tailwindcss"

curl --fail --silent --show-error --location \
	https://unpkg.com/htmx.org@4.0.0/dist/htmx.min.js \
	-o "$work/htmx.min.js"
printf '%s  %s\n' e484d9171a9db30a39c8f16e3d709d4137f3211c659f8e6125816635033d593f "$work/htmx.min.js" | shasum -a 256 -c -

"$work/tailwindcss" -i "$root/frontend/src/input.css" -o "$out/app.css" --minify
cp "$work/htmx.min.js" "$out/htmx.min.js"

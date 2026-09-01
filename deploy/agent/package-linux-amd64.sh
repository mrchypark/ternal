#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
# shellcheck source=/dev/null
. "$script_dir/pigeons-build.env"
platform=${1:-linux-amd64}
case "$platform" in linux-amd64 | linux-arm64) ;; *) echo "unsupported agent platform: $platform" >&2; exit 1 ;; esac
agent_bin=${TERNAL_AGENT_BIN:-dist/bin/ternal-agent}
package_dir=${TERNAL_AGENT_PACKAGE_DIR:-dist/ternal-agent-$platform}
archive=${TERNAL_AGENT_ARCHIVE:-dist/ternal-agent-$platform.tar.gz}
pigeons_bin=${TERNAL_TRANSPORT_BIN:-}
need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1" >&2; exit 127; }; }
case "$package_dir" in "" | /) echo "refusing unsafe package dir: $package_dir" >&2; exit 1 ;; esac
test -x "$agent_bin" || { echo "missing executable ternal-agent: $agent_bin" >&2; exit 1; }
need gzip
need tar
if [ -z "$pigeons_bin" ]; then pigeons_bin=dist/pigeons-$platform; sh "$script_dir/build-pigeons-native.sh" "$platform"; fi
test -x "$pigeons_bin" || { echo "missing executable patched pigeons: $pigeons_bin" >&2; exit 1; }
tar_tmp=$archive.tmp
rm -rf "$package_dir" "$archive" "$tar_tmp"
mkdir -p "$package_dir" "$(dirname "$archive")"
cp "$agent_bin" "$package_dir/ternal-agent"
cp "$pigeons_bin" "$package_dir/pigeons"
cp "$script_dir/pigeons-LICENSE" "$package_dir/LICENSE.pigeons"
chmod 755 "$package_dir/ternal-agent" "$package_dir/pigeons"
printf '%s\n' \
	"Ternal agent $platform bundle" \
	'' \
	'Files:' \
	'- ternal-agent' \
	'- pigeons' \
	'- LICENSE.pigeons (upstream MIT license)' \
	'' \
	"Bundled pigeons: upstream $PIGEONS_VERSION ($PIGEONS_COMMIT), MIT licensed; Ternal compatibility patch" \
	'Ternal patch divergence: persistent client endpoint identity, explicit direct/relay routes,' \
	'  extra-relay composition, and redacted transport diagnostics for SSH ProxyCommand use.' \
	'Enable redacted transport diagnostics without touching ProxyCommand stdout:' \
	'  export PIGEONS_TRANSPORT_DIAGNOSTICS=stderr' \
	'  # or set it to an append-only JSONL file path' \
	'' \
	'Print the persisted local client endpoint ID:' \
	'  ./pigeons endpoint-id [--key-dir <DIR>]' \
	'' \
	'Run:' \
	'  TERNAL_API_URL=https://<ternal-host> TERNAL_DEVICE_KEY_FILE=./device.key ./ternal-agent run' \
	> "$package_dir/README.txt"
parent=$(dirname "$package_dir") base=$(basename "$package_dir")
find "$package_dir" -exec touch -t 200001010000.00 {} +
if tar --version 2>/dev/null | grep -q 'GNU tar'; then
	tar --format=ustar --sort=name --owner=0 --group=0 --numeric-owner \
		-C "$parent" -cf "$tar_tmp" "$base"
else
	tar --format=ustar --uid 0 --gid 0 --uname root --gname root \
		-C "$parent" -cf "$tar_tmp" "$base"
fi
gzip -n <"$tar_tmp" >"$archive"
rm -f "$tar_tmp"
printf 'packaged\t%s\n' "$archive"

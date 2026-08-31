#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
work=$(mktemp -d)

cleanup() {
	rm -rf "$work"
}
trap cleanup EXIT INT TERM HUP

mkdir -p "$work/bin"
printf '#!/bin/sh\necho ternal-agent fake\n' >"$work/bin/ternal-agent"
printf '#!/bin/sh\necho pigeons fake\n' >"$work/pigeons"
chmod +x "$work/bin/ternal-agent" "$work/pigeons"

TERNAL_AGENT_BIN="$work/bin/ternal-agent" \
TERNAL_PIGEONS_BIN="$work/pigeons" \
TERNAL_AGENT_PACKAGE_DIR="$work/dist/ternal-agent-linux-amd64" \
TERNAL_AGENT_ARCHIVE="$work/dist/ternal-agent-linux-amd64.tar.gz" \
	sh "$repo_dir/deploy/agent/package-linux-amd64.sh"

test -x "$work/dist/ternal-agent-linux-amd64/ternal-agent"
test -x "$work/dist/ternal-agent-linux-amd64/pigeons"
test -s "$work/dist/ternal-agent-linux-amd64/LICENSE.pigeons"
test -s "$work/dist/ternal-agent-linux-amd64.tar.gz"
tar -tzf "$work/dist/ternal-agent-linux-amd64.tar.gz" | grep '^ternal-agent-linux-amd64/pigeons$' >/dev/null
tar -tzf "$work/dist/ternal-agent-linux-amd64.tar.gz" | grep '^ternal-agent-linux-amd64/LICENSE.pigeons$' >/dev/null
tar -tzf "$work/dist/ternal-agent-linux-amd64.tar.gz" | grep '^ternal-agent-linux-amd64/ternal-agent$' >/dev/null
grep 'upstream 0.1.1 (0ad18072f77a3ce64c093cab2686a3e99d73c944), MIT licensed' "$work/dist/ternal-agent-linux-amd64/README.txt" >/dev/null
grep 'MIT License' "$work/dist/ternal-agent-linux-amd64/LICENSE.pigeons" >/dev/null

cp "$work/dist/ternal-agent-linux-amd64.tar.gz" "$work/first.tar.gz"
TERNAL_AGENT_BIN="$work/bin/ternal-agent" \
TERNAL_PIGEONS_BIN="$work/pigeons" \
TERNAL_AGENT_PACKAGE_DIR="$work/dist/ternal-agent-linux-amd64" \
TERNAL_AGENT_ARCHIVE="$work/dist/ternal-agent-linux-amd64.tar.gz" \
	sh "$repo_dir/deploy/agent/package-linux-amd64.sh" >/dev/null
cmp "$work/first.tar.gz" "$work/dist/ternal-agent-linux-amd64.tar.gz"

printf 'agent linux amd64 package test passed\n'

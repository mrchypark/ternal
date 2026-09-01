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

for platform in linux-amd64 linux-arm64; do
	package="$work/dist/ternal-agent-$platform"
	archive="$package.tar.gz"
	TERNAL_AGENT_BIN="$work/bin/ternal-agent" TERNAL_TRANSPORT_BIN="$work/pigeons" \
		TERNAL_AGENT_PACKAGE_DIR="$package" TERNAL_AGENT_ARCHIVE="$archive" \
		sh "$repo_dir/deploy/agent/package-linux-amd64.sh" "$platform"

	test -x "$package/ternal-agent"
	test -x "$package/pigeons"
	test -s "$package/LICENSE.pigeons"
	test -s "$archive"
	tar -tzf "$archive" | grep "^ternal-agent-$platform/pigeons$" >/dev/null
	tar -tzf "$archive" | grep "^ternal-agent-$platform/LICENSE.pigeons$" >/dev/null
	tar -tzf "$archive" | grep "^ternal-agent-$platform/ternal-agent$" >/dev/null
	grep "Ternal agent $platform bundle" "$package/README.txt" >/dev/null
	grep 'upstream 0.1.1 (0ad18072f77a3ce64c093cab2686a3e99d73c944), MIT licensed' "$package/README.txt" >/dev/null
	grep 'MIT License' "$package/LICENSE.pigeons" >/dev/null

	cp "$archive" "$work/first-$platform.tar.gz"
	TERNAL_AGENT_BIN="$work/bin/ternal-agent" TERNAL_TRANSPORT_BIN="$work/pigeons" \
		TERNAL_AGENT_PACKAGE_DIR="$package" TERNAL_AGENT_ARCHIVE="$archive" \
		sh "$repo_dir/deploy/agent/package-linux-amd64.sh" "$platform" >/dev/null
	cmp "$work/first-$platform.tar.gz" "$archive"
done

if sh "$repo_dir/deploy/agent/package-linux-amd64.sh" linux-ppc64le >/dev/null 2>&1; then
	echo 'unsupported agent platform accepted' >&2
	exit 1
fi

printf 'agent Linux package test passed\n'

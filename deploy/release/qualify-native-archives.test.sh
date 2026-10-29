#!/bin/sh
# Tiny fake-byte orchestration test, not native artifact qualification.
set -eu
script=$(CDPATH='' cd "$(dirname "$0")" && pwd)/qualify-native-archives.sh
sh -n "$script"
case "$(uname -s):$(uname -m)" in
	Linux:x86_64|Linux:amd64) platform=linux-amd64 ;;
	Linux:aarch64|Linux:arm64) platform=linux-arm64 ;;
	Darwin:x86_64) platform=macos-amd64 ;;
	Darwin:arm64) platform=macos-arm64 ;;
	*) echo 'unsupported qualification self-test host' >&2; exit 2 ;;
esac
umask 077
work=$(mktemp -d)
trap 'find "$work" -depth -delete' EXIT INT TERM HUP
mkdir "$work/source" "$work/qualifier-tmp"

fixture() {
	mode=$1
	cli=$work/source/ternalctl-$platform
	agent=$work/source/ternal-agent-$platform
	mkdir -p "$cli" "$agent"
	# shellcheck disable=SC2016 # These variables belong to the fake executable.
	printf '%s\n' '#!/bin/sh' "mode=$mode" 'case "$1" in' \
		'whoami) [ "$mode" != accept-session ] || exit 0; echo "not logged in" >&2; exit 1 ;;' \
		'known-host-key) if [ "$2" != "$4" ]; then echo "fingerprint mismatch" >&2; exit 1; fi; printf "* %s %s\n" "$5" "$6" ;;' \
		'*) exit 2 ;;' 'esac' >"$cli/ternalctl"
	# shellcheck disable=SC2016
	printf '%s\n' '#!/bin/sh' "mode=$mode" \
		'[ "$1" = endpoint-id ] && [ "$2" = --key-dir ] || exit 2' \
		'if [ "$mode" = unstable ] && [ -e "$3/identity" ]; then printf "%064d\n" 2; exit 0; fi' \
		'mkdir -p "$3"; printf private-fixture >"$3/identity"; printf "%064d\n" 1' >"$cli/pigeons"
	cp "$cli/pigeons" "$agent/pigeons"
	# shellcheck disable=SC2016
	printf '%s\n' '#!/bin/sh' "mode=$mode" \
		'[ "$1" = device-keygen ] || exit 2' \
		'if [ -e "$2" ] && [ "$mode" != overwrite ]; then echo "device key already exists" >&2; exit 1; fi' \
		'printf private-fixture >"$2"; chmod 600 "$2"; printf "device_public_key\tpublic-fixture\n"' >"$agent/ternal-agent"
	for bundle in "$cli" "$agent"; do
		printf 'fixture\n' >"$bundle/LICENSE.pigeons"
		printf 'fixture\n' >"$bundle/README.txt"
		chmod 755 "$bundle/pigeons"
	done
	chmod 755 "$cli/ternalctl" "$agent/ternal-agent"
	tar -czf "$work/cli archive.tar.gz" -C "$work/source" "ternalctl-$platform"
	tar -czf "$work/agent archive.tar.gz" -C "$work/source" "ternal-agent-$platform"
}
probe() {
	if [ "${platform#linux-}" != "$platform" ]; then
		TMPDIR="$work/qualifier-tmp" sh "$script" "$platform" "$work/cli archive.tar.gz" "$work/agent archive.tar.gz"
	else
		TMPDIR="$work/qualifier-tmp" sh "$script" "$platform" "$work/cli archive.tar.gz"
	fi
}
assert_clean() {
	[ "$(find "$work/qualifier-tmp" -print | wc -l | tr -d ' ')" = 1 ] || { echo 'qualifier retained owned temporary state' >&2; exit 1; }
	if grep -Eq 'private-fixture|public-fixture' "$work/result.stdout" "$work/result.stderr"; then
		echo 'qualifier logged fixture key material' >&2; exit 1
	fi
}
expect_failure() {
	if probe >"$work/result.stdout" 2>"$work/result.stderr"; then echo "unsafe fake fixture passed: $1" >&2; exit 1; fi
	grep -F "$2" "$work/result.stderr" >/dev/null
	assert_clean
}
fixture normal
probe >"$work/result.stdout" 2>"$work/result.stderr"
grep -F "qualified-native-archives platform=$platform" "$work/result.stdout" >/dev/null
assert_clean
fixture accept-session
expect_failure accept-session 'candidate CLI accepted a missing session'
fixture unstable
expect_failure unstable 'candidate transport did not preserve its endpoint ID'
if [ "${platform#linux-}" != "$platform" ]; then
	fixture overwrite
	expect_failure overwrite 'candidate agent overwrote an existing device key'
fi
fixture normal
printf 'preserve\n' >"$work/sentinel"
rm "$work/source/ternalctl-$platform/ternalctl"
ln -s "$work/sentinel" "$work/source/ternalctl-$platform/ternalctl"
tar -czf "$work/cli archive.tar.gz" -C "$work/source" "ternalctl-$platform"
expect_failure link 'candidate archive contains a link or special file'
[ "$(cat "$work/sentinel")" = preserve ]
echo 'Native archive qualifier fake orchestration and refusal checks passed'

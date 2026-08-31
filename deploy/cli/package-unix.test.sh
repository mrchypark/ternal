#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
work=$(mktemp -d)
cli_bin=${TERNALCTL_TEST_BIN:-$repo_dir/dist/bin/ternalctl}
pigeons_test_bin=${TERNAL_PIGEONS_TEST_BIN:-}

cleanup() {
	rm -rf "$work"
}
trap cleanup EXIT INT TERM HUP

case "$(uname -s):$(uname -m)" in
	Linux:x86_64 | Linux:amd64) host_platform=linux-amd64 ;;
	Linux:aarch64 | Linux:arm64) host_platform=linux-arm64 ;;
	Darwin:x86_64) host_platform=macos-amd64 ;;
	Darwin:arm64) host_platform=macos-arm64 ;;
	*)
		echo "unsupported native package test host: $(uname -s) $(uname -m)" >&2
		exit 1
		;;
esac
platform=${TERNALCTL_TEST_PLATFORM:-$host_platform}
if [ "$platform" != "$host_platform" ]; then
	echo "native package test platform mismatch: requested $platform, host is $host_platform" >&2
	exit 1
fi
package_script=${TERNALCTL_TEST_PACKAGE_SCRIPT:-$repo_dir/deploy/cli/package-unix.sh}
bundle=ternalctl-$platform

mkdir -p "$work/bin" "$work/home"
test -x "$cli_bin" || {
	echo "missing executable test ternalctl: $cli_bin" >&2
	exit 1
}
if [ -z "$pigeons_test_bin" ]; then
	pigeons_test_bin="$work/bin/pigeons"
	pigeons_proof=fake
	# shellcheck disable=SC2016 # Variables expand when the generated fake runs.
	printf '%s\n' \
		'#!/bin/sh' \
		'set -eu' \
		'printf "%s\\n" "$*" >> "${TERNALCTL_TEST_PIGEONS_LOG:?}"' \
		'if [ "$1" = endpoint-id ]; then' \
		'  printf "%s\\n" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
		'fi' \
		>"$pigeons_test_bin"
	chmod +x "$pigeons_test_bin"
else
	pigeons_proof=real
	test -x "$pigeons_test_bin" || {
		echo "missing executable test pigeons: $pigeons_test_bin" >&2
		exit 1
	}
fi

TERNALCTL_BIN="$cli_bin" \
TERNAL_PIGEONS_BIN="$pigeons_test_bin" \
TERNALCTL_PACKAGE_DIR="$work/dist/$bundle" \
TERNALCTL_ARCHIVE="$work/dist/$bundle.tar.gz" \
	sh "$package_script" "$platform"

test -x "$work/dist/$bundle/ternalctl"
test -x "$work/dist/$bundle/pigeons"
test -s "$work/dist/$bundle/LICENSE.pigeons"
test -s "$work/dist/$bundle.tar.gz"
tar -tzf "$work/dist/$bundle.tar.gz" | grep "^$bundle/ternalctl$" >/dev/null
tar -tzf "$work/dist/$bundle.tar.gz" | grep "^$bundle/pigeons$" >/dev/null
tar -tzf "$work/dist/$bundle.tar.gz" | grep "^$bundle/LICENSE.pigeons$" >/dev/null
tar -tzf "$work/dist/$bundle.tar.gz" | grep "^$bundle/README.txt$" >/dev/null
grep "Supported platform:" "$work/dist/$bundle/README.txt" >/dev/null
grep 'TERNAL_PIGEONS_BIN' "$work/dist/$bundle/README.txt" >/dev/null
grep 'ssh-keygen' "$work/dist/$bundle/README.txt" >/dev/null
case "$platform" in
	linux-*)
		grep -F '  sudo install -m 755 ternalctl pigeons /usr/local/bin/' "$work/dist/$bundle/README.txt" >/dev/null
		grep -F 'glibc 2.35 or newer' "$work/dist/$bundle/README.txt" >/dev/null
		grep -F 'musl-only systems are not supported' "$work/dist/$bundle/README.txt" >/dev/null
		;;
	macos-*)
		grep -F '  sudo mkdir -p /usr/local/bin && sudo install -m 755 ternalctl pigeons /usr/local/bin/' "$work/dist/$bundle/README.txt" >/dev/null
		if grep -E 'glibc|musl' "$work/dist/$bundle/README.txt" >/dev/null; then
			echo 'Linux runtime requirements leaked into macOS README' >&2
			exit 1
		fi
		;;
esac

cp "$work/dist/$bundle.tar.gz" "$work/first.tar.gz"
TERNALCTL_BIN="$cli_bin" \
TERNAL_PIGEONS_BIN="$pigeons_test_bin" \
TERNALCTL_PACKAGE_DIR="$work/dist/$bundle" \
TERNALCTL_ARCHIVE="$work/dist/$bundle.tar.gz" \
	sh "$package_script" "$platform" >/dev/null
cmp "$work/first.tar.gz" "$work/dist/$bundle.tar.gz"

mkdir -p "$work/extracted"
tar -xzf "$work/dist/$bundle.tar.gz" -C "$work/extracted"
if (
	unset TERNAL_PIGEONS_BIN
	HOME="$work/home" \
	TERNALCTL_TEST_PIGEONS_LOG="$work/pigeons.log" \
	TERNAL_API_URL=http://127.0.0.1:1 \
	TERNAL_USER=package-test \
	TERNAL_GROUPS=package-test \
		"$work/extracted/$bundle/ternalctl" proxy host-1 \
		aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:22
) >"$work/ternalctl.out" 2>&1; then
	echo 'expected loopback API request to fail' >&2
	exit 1
fi
grep 'not logged in' "$work/ternalctl.out" >/dev/null
if [ -e "$work/pigeons.log" ]; then
	echo 'pigeons ran before local session validation' >&2
	exit 1
fi

case "$platform" in
	linux-amd64) mismatch=linux-arm64 ;;
	*) mismatch=linux-amd64 ;;
esac
if TERNALCTL_BIN="$cli_bin" TERNAL_PIGEONS_BIN="$pigeons_test_bin" \
	sh "$repo_dir/deploy/cli/package-unix.sh" "$mismatch" >"$work/mismatch.out" 2>&1; then
	echo 'expected mismatched native package request to fail' >&2
	exit 1
fi
grep 'native package platform mismatch' "$work/mismatch.out" >/dev/null

expect_safety_rejection() {
	name=$1
	unsafe_package_dir=$2
	unsafe_archive=$3
	unsafe_cli_bin=$4
	unsafe_pigeons_bin=$5
	run_dir=$6
	if (
		cd "$run_dir"
		TERNALCTL_BIN="$unsafe_cli_bin" \
		TERNAL_PIGEONS_BIN="$unsafe_pigeons_bin" \
		TERNALCTL_PACKAGE_DIR="$unsafe_package_dir" \
		TERNALCTL_ARCHIVE="$unsafe_archive" \
			sh "$repo_dir/deploy/cli/package-unix.sh" "$platform"
	) >"$work/$name.out" 2>&1; then
		echo "expected unsafe $name package request to fail" >&2
		exit 1
	fi
}

safety="$work/safety sentinel"
current="$safety/current/$bundle"
ancestor="$safety/ancestor/$bundle"
cli_package="$safety/package-cli/$bundle"
pigeons_package="$safety/package-pigeons/$bundle"
archive_package="$safety/package-archive/$bundle"
mkdir -p "$current" "$ancestor/cwd" "$cli_package" "$pigeons_package" "$archive_package"
printf 'keep\n' >"$current/sentinel"
expect_safety_rejection current "$current" "$safety/current.tar.gz" \
	"$cli_bin" "$pigeons_test_bin" "$current"
grep '^keep$' "$current/sentinel" >/dev/null

printf 'keep\n' >"$ancestor/sentinel"
expect_safety_rejection ancestor "$ancestor" "$safety/ancestor.tar.gz" \
	"$cli_bin" "$pigeons_test_bin" "$ancestor/cwd"
grep '^keep$' "$ancestor/sentinel" >/dev/null

printf '#!/bin/sh\nexit 0\n' >"$cli_package/cli-input"
chmod +x "$cli_package/cli-input"
printf 'keep\n' >"$cli_package/sentinel"
expect_safety_rejection cli-inside "$cli_package" "$safety/cli-inside.tar.gz" \
	"$cli_package/cli-input" "$pigeons_test_bin" "$repo_dir"
grep '^keep$' "$cli_package/sentinel" >/dev/null

printf '#!/bin/sh\nexit 0\n' >"$pigeons_package/pigeons-input"
chmod +x "$pigeons_package/pigeons-input"
printf 'keep\n' >"$pigeons_package/sentinel"
expect_safety_rejection pigeons-inside "$pigeons_package" "$safety/pigeons-inside.tar.gz" \
	"$cli_bin" "$pigeons_package/pigeons-input" "$repo_dir"
grep '^keep$' "$pigeons_package/sentinel" >/dev/null

printf 'keep\n' >"$archive_package/sentinel"
expect_safety_rejection archive-inside "$archive_package" "$archive_package/output.tar.gz" \
	"$cli_bin" "$pigeons_test_bin" "$repo_dir"
grep '^keep$' "$archive_package/sentinel" >/dev/null

cli_archive="$safety/cli-archive-sentinel.tar.gz"
printf 'cli-input-keep\n' >"$cli_archive"
chmod +x "$cli_archive"
expect_safety_rejection archive-is-cli "$safety/archive-is-cli/$bundle" "$cli_archive" \
	"$cli_archive" "$pigeons_test_bin" "$repo_dir"
grep '^cli-input-keep$' "$cli_archive" >/dev/null

pigeons_archive="$safety/pigeons-archive-sentinel.tar.gz"
printf '#!/bin/sh\nprintf "pigeons-input-keep\\n"\n' >"$pigeons_archive"
chmod +x "$pigeons_archive"
expect_safety_rejection archive-is-pigeons "$safety/archive-is-pigeons/$bundle" "$pigeons_archive" \
	"$cli_bin" "$pigeons_archive" "$repo_dir"
grep 'pigeons-input-keep' "$pigeons_archive" >/dev/null

mkdir -p "$safety/tmp"
printf 'keep\n' >"$safety/tmp/sentinel"
expect_safety_rejection arbitrary-tmp "$safety/tmp" "$safety/arbitrary-tmp.tar.gz" \
	"$cli_bin" "$pigeons_test_bin" "$repo_dir"
grep '^keep$' "$safety/tmp/sentinel" >/dev/null

directory_archive="$safety/directory-archive.tar.gz"
mkdir -p "$directory_archive"
printf 'keep\n' >"$directory_archive/sentinel"
expect_safety_rejection directory-archive "$safety/directory-archive/$bundle" "$directory_archive" \
	"$cli_bin" "$pigeons_test_bin" "$repo_dir"
grep '^keep$' "$directory_archive/sentinel" >/dev/null

extension_package="$safety/archive-extension/$bundle"
mkdir -p "$extension_package"
printf 'keep\n' >"$extension_package/sentinel"
expect_safety_rejection archive-extension "$extension_package" "$safety/archive.zip" \
	"$cli_bin" "$pigeons_test_bin" "$repo_dir"
grep '^keep$' "$extension_package/sentinel" >/dev/null

expect_safety_rejection empty-package "" "$safety/empty-package.tar.gz" \
	"$cli_bin" "$pigeons_test_bin" "$repo_dir"
expect_safety_rejection root-package / "$safety/root-package.tar.gz" \
	"$cli_bin" "$pigeons_test_bin" "$repo_dir"

printf 'cli %s package test passed\n' "$platform"

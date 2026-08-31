#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
# shellcheck source=/dev/null
. "$script_dir/pigeons-build.env"

case "$(uname -s):$(uname -m)" in
	Linux:x86_64 | Linux:amd64) host_platform=linux-amd64 ;;
	Linux:aarch64 | Linux:arm64) host_platform=linux-arm64 ;;
	Darwin:x86_64) host_platform=macos-amd64 ;;
	Darwin:arm64) host_platform=macos-arm64 ;;
	*) echo "unsupported native build host: $(uname -s) $(uname -m)" >&2; exit 1 ;;
esac

if [ "$#" -gt 1 ]; then
	echo "usage: $0 [linux-amd64|linux-arm64|macos-amd64|macos-arm64]" >&2
	exit 2
fi
platform=${1:-${TERNAL_PLATFORM:-$host_platform}}
case "$platform" in linux-amd64 | linux-arm64 | macos-amd64 | macos-arm64) ;; *) echo "unsupported native build platform: $platform" >&2; exit 1 ;; esac
if [ "$platform" != "$host_platform" ]; then echo "native build platform mismatch: requested $platform, host is $host_platform" >&2; exit 1; fi

output=${TERNAL_PIGEONS_OUTPUT:-dist/pigeons-$platform}
target_dir=${TERNAL_PIGEONS_TARGET_DIR:-target/pigeons-$platform}
source_url="https://codeload.github.com/n0-computer/pigeons/tar.gz/$PIGEONS_COMMIT"
patch_file="$script_dir/pigeons-$PIGEONS_VERSION-ternal.patch"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM HUP

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1" >&2; exit 127; }; }
verify_sha256() {
	expected=$1 file=$2
	if command -v sha256sum >/dev/null 2>&1; then printf '%s  %s\n' "$expected" "$file" | sha256sum -c - >/dev/null
	else need shasum; printf '%s  %s\n' "$expected" "$file" | shasum -a 256 -c - >/dev/null; fi
}
need cargo; need curl; need patch; need tar
archive="$work/pigeons.tar.gz"
curl -fsSL -o "$archive" "$source_url"
verify_sha256 "$PIGEONS_SOURCE_SHA256" "$archive"
tar -xzf "$archive" -C "$work"
source_dir="$work/pigeons-$PIGEONS_COMMIT"
test -d "$source_dir" || { echo "source archive did not contain expected directory: $source_dir" >&2; exit 1; }
verify_sha256 "$PIGEONS_CARGO_LOCK_SHA256" "$source_dir/Cargo.lock"
patch -s -d "$source_dir" -p1 < "$patch_file"
cargo fmt --all --manifest-path "$source_dir/Cargo.toml" -- --check
CARGO_TARGET_DIR="$target_dir" cargo test --locked --manifest-path "$source_dir/Cargo.toml"
CARGO_TARGET_DIR="$target_dir" cargo build --locked --release --manifest-path "$source_dir/Cargo.toml"
mkdir -p "$(dirname "$output")"
cp "$target_dir/release/pigeons" "$output"
chmod 755 "$output"
printf 'built\t%s\tplatform=%s\tpigeons=%s\tcommit=%s\n' "$output" "$platform" "$PIGEONS_VERSION" "$PIGEONS_COMMIT"

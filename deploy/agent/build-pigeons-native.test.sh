#!/bin/sh
set -eu
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM HUP
mkdir -p "$work/bin"
printf '%s\n' '#!/bin/sh' 'case "$1" in -s) printf "%s\\n" "${FAKE_UNAME_S:?}" ;; -m) printf "%s\\n" "${FAKE_UNAME_M:?}" ;; *) exit 2 ;; esac' > "$work/bin/uname"
printf '%s\n' '#!/bin/sh' 'printf "reached\\n" >> "${FAKE_CURL_LOG:?}"' 'exit 99' > "$work/bin/curl"
chmod +x "$work/bin/uname" "$work/bin/curl"
check_mapping() { os=$1 arch=$2 platform=$3 log="$work/$platform.log"; if PATH="$work/bin:$PATH" FAKE_UNAME_S="$os" FAKE_UNAME_M="$arch" FAKE_CURL_LOG="$log" sh "$script_dir/build-pigeons-native.sh" "$platform" >"$work/output" 2>&1; then echo "expected fake curl to stop $platform build" >&2; exit 1; fi; grep '^reached$' "$log" >/dev/null; }
check_mapping Linux x86_64 linux-amd64
check_mapping Linux aarch64 linux-arm64
check_mapping Darwin x86_64 macos-amd64
check_mapping Darwin arm64 macos-arm64
if PATH="$work/bin:$PATH" FAKE_UNAME_S=FreeBSD FAKE_UNAME_M=amd64 FAKE_CURL_LOG="$work/unknown.log" sh "$script_dir/build-pigeons-native.sh" >"$work/unknown.out" 2>&1; then exit 1; fi
grep 'unsupported native build host' "$work/unknown.out" >/dev/null
if PATH="$work/bin:$PATH" FAKE_UNAME_S=Darwin FAKE_UNAME_M=arm64 FAKE_CURL_LOG="$work/mismatch.log" sh "$script_dir/build-pigeons-native.sh" linux-amd64 >"$work/mismatch.out" 2>&1; then exit 1; fi
grep 'native build platform mismatch' "$work/mismatch.out" >/dev/null
printf 'native pigeons platform mapping test passed\n'

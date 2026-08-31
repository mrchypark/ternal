#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
# shellcheck source=/dev/null
. "$script_dir/../agent/pigeons-build.env"

case "$(uname -s):$(uname -m)" in
	Linux:x86_64 | Linux:amd64) host_platform=linux-amd64 ;;
	Linux:aarch64 | Linux:arm64) host_platform=linux-arm64 ;;
	Darwin:x86_64) host_platform=macos-amd64 ;;
	Darwin:arm64) host_platform=macos-arm64 ;;
	*)
		echo "unsupported native package host: $(uname -s) $(uname -m)" >&2
		exit 1
		;;
esac

if [ "$#" -gt 1 ]; then
	echo "usage: $0 [linux-amd64|linux-arm64|macos-amd64|macos-arm64]" >&2
	exit 2
fi
platform=${1:-${TERNAL_PLATFORM:-$host_platform}}
case "$platform" in
	linux-amd64)
		os_label=Linux
		arch_label='amd64 (x86_64)'
		install_command='  sudo install -m 755 ternalctl pigeons /usr/local/bin/'
		runtime_requirements='Runtime requirements: glibc 2.35 or newer; musl-only systems are not supported; ssh and ssh-keygen from OpenSSH must be available on PATH.'
		;;
	linux-arm64)
		os_label=Linux
		arch_label='arm64 (aarch64)'
		install_command='  sudo install -m 755 ternalctl pigeons /usr/local/bin/'
		runtime_requirements='Runtime requirements: glibc 2.35 or newer; musl-only systems are not supported; ssh and ssh-keygen from OpenSSH must be available on PATH.'
		;;
	macos-amd64)
		os_label=macOS
		arch_label='amd64 (x86_64)'
		install_command='  sudo mkdir -p /usr/local/bin && sudo install -m 755 ternalctl pigeons /usr/local/bin/'
		runtime_requirements='Runtime requirements: ssh and ssh-keygen from OpenSSH must be available on PATH.'
		;;
	macos-arm64)
		os_label=macOS
		arch_label='arm64 (Apple silicon)'
		install_command='  sudo mkdir -p /usr/local/bin && sudo install -m 755 ternalctl pigeons /usr/local/bin/'
		runtime_requirements='Runtime requirements: ssh and ssh-keygen from OpenSSH must be available on PATH.'
		;;
	*)
		echo "unsupported native package platform: $platform" >&2
		exit 1
		;;
esac
if [ "$platform" != "$host_platform" ]; then
	echo "native package platform mismatch: requested $platform, host is $host_platform" >&2
	exit 1
fi
expected_package_name=ternalctl-$platform

cli_bin=${TERNALCTL_BIN:-dist/bin/ternalctl}
package_dir=${TERNALCTL_PACKAGE_DIR-dist/ternalctl-$platform}
archive=${TERNALCTL_ARCHIVE-dist/ternalctl-$platform.tar.gz}
pigeons_bin=${TERNAL_TRANSPORT_BIN:-}

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 127
	fi
}

normalize_path() (
	set -f
	old_ifs=$IFS
	IFS=/
	# shellcheck disable=SC2086 # Intentional split into path components.
	set -- $1
	IFS=$old_ifs
	result=/
	for component do
		case "$component" in
		"" | .) ;;
		..)
			if [ "$result" != / ]; then
				result=${result%/*}
				[ -n "$result" ] || result=/
			fi
			;;
		*)
			if [ "$result" = / ]; then
				result=/$component
			else
				result=$result/$component
			fi
			;;
		esac
	done
	printf '%s\n' "$result"
)

absolute_path() {
	case "$1" in
	/*) path=$1 ;;
	*) path=$PWD/$1 ;;
	esac
	path=$(normalize_path "$path")
	if [ -d "$path" ]; then
		(CDPATH='' cd -P "$path" && pwd)
		return
	fi
	dir=$(dirname "$path")
	tail=$(basename "$path")
	while [ ! -d "$dir" ]; do
		tail=$(basename "$dir")/$tail
		dir=$(dirname "$dir")
	done
	dir=$(CDPATH='' cd -P "$dir" && pwd)
	normalize_path "$dir/$tail"
}

path_is_within() {
	[ "$1" = "$2" ] && return 0
	case "$1" in
	"$2"/*) return 0 ;;
	esac
	return 1
}

case "$package_dir" in
	"" | /)
		echo "refusing unsafe package dir: $package_dir" >&2
		exit 1
		;;
esac
case "$archive" in
	"" | /)
		echo "refusing unsafe archive: $archive" >&2
		exit 1
		;;
esac

if [ -z "$pigeons_bin" ]; then
	pigeons_bin=dist/pigeons-$platform
	build_pigeons=true
else
	build_pigeons=false
fi

cli_bin=$(absolute_path "$cli_bin")
pigeons_bin=$(absolute_path "$pigeons_bin")
package_dir=$(absolute_path "$package_dir")
archive=$(absolute_path "$archive")
cwd=$(pwd -P)

if [ "$package_dir" = / ] || path_is_within "$cwd" "$package_dir"; then
	echo "refusing package dir that contains the working directory: $package_dir" >&2
	exit 1
fi
if [ "$(basename "$package_dir")" != "$expected_package_name" ]; then
	echo "package dir must be named $expected_package_name: $package_dir" >&2
	exit 1
fi
case "$archive" in
	*.tar.gz) ;;
	*)
		echo "Ternal CLI Unix archive must use .tar.gz: $archive" >&2
		exit 1
		;;
esac
if [ -d "$archive" ]; then
	echo "archive must not be a directory: $archive" >&2
	exit 1
fi
if path_is_within "$cli_bin" "$package_dir" || path_is_within "$pigeons_bin" "$package_dir"; then
	echo "input binaries must be outside the package dir: $package_dir" >&2
	exit 1
fi
if path_is_within "$archive" "$package_dir"; then
	echo "archive must be outside the package dir: $archive" >&2
	exit 1
fi
if [ "$archive" = "$cli_bin" ] || [ "$archive" = "$pigeons_bin" ]; then
	echo "archive must not overwrite an input binary: $archive" >&2
	exit 1
fi

test -x "$cli_bin" || {
	echo "missing executable ternalctl: $cli_bin" >&2
	exit 1
}

need gzip
need tar

if [ "$build_pigeons" = true ]; then
	TERNAL_TRANSPORT_OUTPUT="$pigeons_bin" sh "$script_dir/../agent/build-pigeons-native.sh" "$platform"
fi

test -x "$pigeons_bin" || {
	echo "missing executable patched pigeons: $pigeons_bin" >&2
	exit 1
}

tar_tmp=$archive.tmp
rm -rf "$package_dir" "$archive" "$tar_tmp"
mkdir -p "$package_dir" "$(dirname "$archive")"

cp "$cli_bin" "$package_dir/ternalctl"
cp "$pigeons_bin" "$package_dir/pigeons"
cp "$script_dir/../agent/pigeons-LICENSE" "$package_dir/LICENSE.pigeons"
chmod 755 "$package_dir/ternalctl" "$package_dir/pigeons"

printf '%s\n' \
	"Ternal CLI $os_label bundle for $arch_label" \
	'' \
	"Supported platform: $os_label on $arch_label." \
	'This is a native platform build, not a static or universal binary.' \
	"$runtime_requirements" \
	'' \
	'Files:' \
	'- ternalctl' \
	'- pigeons' \
	'- LICENSE.pigeons (upstream MIT license)' \
	'' \
	"Bundled pigeons: upstream $PIGEONS_VERSION ($PIGEONS_COMMIT), Ternal transport diagnostics patch" \
	'' \
	'Install both files together:' \
	"$install_command" \
	'' \
	'pigeons lookup order:' \
	'1. TERNAL_TRANSPORT_BIN' \
	'2. executable pigeons beside the resolved ternalctl executable target' \
	'3. pigeons on PATH' \
	'Symlink installs must keep both files beside the symlink target, or use TERNAL_TRANSPORT_BIN/PATH.' \
	'' \
	'Run:' \
	'  TERNAL_API_URL=https://<ternal-host> ./ternalctl login' \
	>"$package_dir/README.txt"

parent=$(dirname "$package_dir")
base=$(basename "$package_dir")
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

printf 'packaged\t%s\tplatform=%s\n' "$archive" "$platform"

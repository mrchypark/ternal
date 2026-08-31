#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
TERNALCTL_TEST_PLATFORM=linux-amd64
TERNALCTL_TEST_PACKAGE_SCRIPT="$script_dir/package-linux-amd64.sh"
export TERNALCTL_TEST_PLATFORM TERNALCTL_TEST_PACKAGE_SCRIPT
exec sh "$script_dir/package-unix.test.sh"

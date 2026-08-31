#!/bin/sh
set -eu

relay_url=${TERNAL_RELAY_URL:-}
if [ -z "$relay_url" ]; then
	echo "TERNAL_RELAY_URL is required" >&2
	exit 1
fi
relay_url=${relay_url%/}
case "$relay_url" in
	https://*) ;;
	*)
		if [ "${TERNAL_ALLOW_INSECURE_RELAY_URL:-}" != 1 ]; then
			echo "production relay URL must use https" >&2
			exit 1
		fi
		;;
esac

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
export TERNAL_RELAY_URL="$relay_url"
export TERNAL_TRANSPORT_MATRIX_STATES=relay-only
TERNAL_TRANSPORT_DRIVER=${TERNAL_TRANSPORT_DRIVER:-"$script_dir/drivers/linux-netns-pigeons.sh"}
export TERNAL_TRANSPORT_DRIVER
exec sh "$script_dir/transport-matrix.sh"

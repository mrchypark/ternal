#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
driver="$script_dir/test-fixtures/observable-transport-driver.sh"

TERNAL_TRANSPORT_MATRIX_ALLOW_NON_LINUX=1 \
	TERNAL_TRANSPORT_ALLOW_FIXTURE=1 \
	TERNAL_TRANSPORT_DRIVER="$driver" \
	sh "$script_dir/transport-matrix.sh"

status=0
TERNAL_TRANSPORT_MATRIX_ALLOW_NON_LINUX=1 \
	TERNAL_TRANSPORT_ALLOW_FIXTURE=1 \
	TERNAL_FIXTURE_PATH_OBSERVABLE=0 \
	TERNAL_TRANSPORT_DRIVER="$driver" \
	sh "$script_dir/transport-matrix.sh" >/dev/null 2>&1 || status=$?
if [ "$status" -ne 77 ]; then
	echo "unobservable transport must return SKIP status 77, got $status" >&2
	exit 1
fi

status=0
TERNAL_TRANSPORT_MATRIX_ALLOW_NON_LINUX=1 \
	TERNAL_TRANSPORT_DRIVER="$driver" \
	sh "$script_dir/transport-matrix.sh" >/dev/null 2>&1 || status=$?
if [ "$status" -ne 77 ]; then
	echo "fixture transport must require explicit opt-in, got $status" >&2
	exit 1
fi

status=0
TERNAL_TRANSPORT_MATRIX_ALLOW_NON_LINUX=1 \
	TERNAL_FIXTURE_CLAIM_PRODUCTION=1 \
	TERNAL_TRANSPORT_DRIVER="$driver" \
	sh "$script_dir/transport-matrix.sh" >/dev/null 2>&1 || status=$?
if [ "$status" -eq 0 ] || [ "$status" -eq 77 ]; then
	echo "fixture driver claiming production evidence must fail, got $status" >&2
	exit 1
fi

printf 'transport matrix harness self-test passed\n'

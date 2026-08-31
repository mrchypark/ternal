#!/bin/sh
set -eu

case "${1:-}" in
	capabilities)
		if [ "${TERNAL_FIXTURE_CLAIM_PRODUCTION:-}" = 1 ]; then
			printf '%s\n' '{"driver":"observable-fixture-v1","path_observable":true,"evidence":"production","states":["relay-only","direct-only","both-blocked","recovery"]}'
		elif [ "${TERNAL_FIXTURE_PATH_OBSERVABLE:-1}" = 1 ]; then
			printf '%s\n' '{"driver":"observable-fixture-v1","path_observable":true,"evidence":"fixture","states":["relay-only","direct-only","both-blocked","recovery"]}'
		else
			printf '%s\n' '{"driver":"observable-fixture-v1","path_observable":false,"evidence":"fixture","reason":"fixture path observation disabled","states":["relay-only","direct-only","both-blocked","recovery"]}'
		fi
		;;
	prepare|cleanup)
		:
		;;
	endpoint-id)
		printf '%s\n' fixture-stable-endpoint
		;;
	apply)
		case "${2:-}" in
			relay-only|direct-only|both-blocked|recovery) ;;
			*) exit 1 ;;
		esac
		;;
	probe)
		case "${2:-}" in
			relay-only|recovery)
				printf '%s\n' '{"connected":true,"path":"relay","endpoint_id":"fixture-stable-endpoint"}'
				;;
			direct-only)
				printf '%s\n' '{"connected":true,"path":"direct","endpoint_id":"fixture-stable-endpoint"}'
				;;
			both-blocked)
				printf '%s\n' '{"connected":false,"path":"none","endpoint_id":"fixture-stable-endpoint"}'
				;;
			*) exit 1 ;;
		esac
		;;
	*)
		echo "unsupported fixture driver operation: ${1:-}" >&2
		exit 1
		;;
esac

#!/bin/sh
set -eu

skip() {
	printf 'SKIP: %s\n' "$1" >&2
	exit 77
}

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 127
	fi
}

need jq

if [ "$(uname -s)" != Linux ] && [ "${TERNAL_TRANSPORT_MATRIX_ALLOW_NON_LINUX:-}" != 1 ]; then
	skip "transport matrix requires a Linux runner or vind Linux workload"
fi

driver=${TERNAL_TRANSPORT_DRIVER:-${1:-}}
[ -n "$driver" ] || skip "TERNAL_TRANSPORT_DRIVER is required; pigeons cannot observe direct versus relay paths without the patched diagnostics contract"
[ -x "$driver" ] || {
	echo "transport driver is not executable: $driver" >&2
	exit 1
}

work=${TERNAL_TRANSPORT_MATRIX_WORK:-}
remove_work=0
if [ -z "$work" ]; then
	work=$(mktemp -d)
	remove_work=1
else
	mkdir -p "$work"
fi
export TERNAL_TRANSPORT_MATRIX_WORK="$work"

prepared=0
cleanup() {
	code=$?
	if [ "$prepared" -eq 1 ]; then
		"$driver" cleanup >/dev/null 2>&1 || true
	fi
	if [ "$remove_work" -eq 1 ]; then
		rm -rf "$work"
	fi
	trap - EXIT INT TERM HUP
	exit "$code"
}
trap cleanup EXIT INT TERM HUP

capabilities=$($driver capabilities)
printf '%s' "$capabilities" | jq -e 'type == "object" and (.driver | type == "string") and (.path_observable | type == "boolean") and (.states | type == "array") and (.evidence == "production" or .evidence == "fixture")' >/dev/null || {
	echo "transport driver returned invalid capabilities JSON" >&2
	exit 1
}
evidence=$(printf '%s' "$capabilities" | jq -r '.evidence')
if [ "$evidence" = fixture ] && [ "${TERNAL_TRANSPORT_ALLOW_FIXTURE:-}" != 1 ]; then
	skip "fixture drivers are self-test only and cannot provide production transport evidence"
fi
if [ "$evidence" = production ] && [ "$(printf '%s' "$capabilities" | jq -r '.driver')" != linux-netns-pigeons-v1 ]; then
	echo "production evidence is accepted only from linux-netns-pigeons-v1" >&2
	exit 1
fi
if [ "$(printf '%s' "$capabilities" | jq -r '.path_observable')" != true ]; then
	reason=$(printf '%s' "$capabilities" | jq -r '.reason // "transport driver cannot independently observe direct versus relay paths"')
	skip "$reason"
fi

matrix_states=${TERNAL_TRANSPORT_MATRIX_STATES:-"relay-only direct-only both-blocked recovery"}
for state in $matrix_states; do
	case "$state" in
		relay-only|direct-only|both-blocked|recovery) ;;
		*)
			echo "unsupported requested matrix state: $state" >&2
			exit 1
			;;
	esac
	printf '%s' "$capabilities" | jq -e --arg state "$state" '.states | index($state) != null' >/dev/null || {
		echo "transport driver does not support required state: $state" >&2
		exit 1
	}
done

prepared=1
"$driver" prepare
initial_endpoint=$($driver endpoint-id)
[ -n "$initial_endpoint" ] || {
	echo "transport driver returned an empty endpoint ID" >&2
	exit 1
}

assert_probe() {
	state=$1
	expected_connected=$2
	expected_path=$3
	result_file="$work/$state.json"

	"$driver" apply "$state"
	"$driver" probe "$state" >"$result_file"
	jq -e 'type == "object" and (.connected | type == "boolean") and (.path | type == "string") and (.endpoint_id | type == "string")' "$result_file" >/dev/null || {
		echo "transport driver returned invalid probe JSON for $state" >&2
		exit 1
	}
	if [ "$evidence" = production ]; then
		jq -e '(.network_state_verified | type == "boolean") and (.ssh_banner | type == "boolean") and (.diagnostics_jsonl | type == "boolean")' "$result_file" >/dev/null || {
			echo "$state: production probe omitted required network/banner/diagnostics evidence" >&2
			exit 1
		}
		jq -e '.network_state_verified == true' "$result_file" >/dev/null || {
			echo "$state: iptables state was not verified" >&2
			exit 1
		}
		if [ "$expected_connected" = true ]; then
			jq -e '.ssh_banner == true and .diagnostics_jsonl == true' "$result_file" >/dev/null || {
				echo "$state: connected production probe requires both SSH banner and diagnostics JSONL" >&2
				exit 1
			}
		else
			jq -e '.ssh_banner == false' "$result_file" >/dev/null || {
				echo "$state: blocked production probe unexpectedly observed an SSH banner" >&2
				exit 1
			}
		fi
	fi

	connected=$(jq -r '.connected' "$result_file")
	path=$(jq -r '.path' "$result_file")
	endpoint=$(jq -r '.endpoint_id' "$result_file")
	if [ "$connected" != "$expected_connected" ]; then
		echo "$state: expected connected=$expected_connected, observed connected=$connected" >&2
		exit 1
	fi
	if [ "$path" = unknown ] || [ "$path" != "$expected_path" ]; then
		echo "$state: expected observable path=$expected_path, observed path=$path" >&2
		exit 1
	fi
	if [ "$endpoint" != "$initial_endpoint" ]; then
		echo "$state: endpoint identity changed during transport transition" >&2
		exit 1
	fi
	printf 'PASS: state=%s connected=%s path=%s\n' "$state" "$connected" "$path"
}

for state in $matrix_states; do
	case "$state" in
		relay-only) assert_probe relay-only true relay ;;
		direct-only) assert_probe direct-only true direct ;;
		both-blocked) assert_probe both-blocked false none ;;
		recovery) assert_probe recovery true relay ;;
	esac
done

printf 'transport matrix passed: evidence=%s endpoint identity remained stable across all states\n' "$evidence"

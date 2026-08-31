#!/bin/sh
set -eu

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 127
	fi
}

need go
need curl
need jq
need tar

script_dir=$(CDPATH='' cd "$(dirname "$0")" && pwd)
repo_dir=$(CDPATH='' cd "$script_dir/../.." && pwd)
work=$(mktemp -d)
source_dir="$work/source-rhiza"
restore_dir="$work/restored-rhiza"
backup_dir="$work/backups"
api_log="$work/ternal-api.log"
api_pid=""

port_base=$((24000 + ($$ % 1000) * 3))
http_port=${TERNAL_BACKUP_TEST_HTTP_PORT:-$port_base}
api_url="http://127.0.0.1:$http_port"
host_name="backup-restore-$$"
endpoint_id=$(printf '%064d' "$$")

cleanup() {
	status=$?
	trap - EXIT INT TERM HUP
	if [ -n "$api_pid" ] && kill -0 "$api_pid" >/dev/null 2>&1; then
		kill "$api_pid" >/dev/null 2>&1 || true
		wait "$api_pid" >/dev/null 2>&1 || true
	fi
	if [ "$status" -ne 0 ] && [ -f "$api_log" ]; then
		echo "ternal-api log:" >&2
		sed -n '1,200p' "$api_log" >&2 || true
	fi
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT INT TERM HUP

start_api() {
	data_dir=$1
	: >"$api_log"
	TERNAL_BIND="127.0.0.1:$http_port" \
	TERNAL_DEV_HEADERS=1 \
	RHIZA_DATA_DIR="$data_dir" \
	RHIZA_ADMIN_TOKEN=ternal-backup-test-rhiza-admin-token-minimum-32-chars \
	TERNAL_REQUIRE_RHIZA_ADMIN_TOKEN=1 \
	TERNAL_PIGEONS_RELAY_ACCESS_TOKEN=ternal-backup-test-relay-token-minimum-32-chars \
	TERNAL_SESSION_KEY=ternal-backup-test-session-key-minimum-32-chars \
	"$work/ternal-api" >"$api_log" 2>&1 &
	api_pid=$!

	i=0
	while ! curl -fsS "$api_url/health" >/dev/null 2>&1; do
		if ! kill -0 "$api_pid" >/dev/null 2>&1; then
			echo "ternal-api exited before becoming healthy" >&2
			exit 1
		fi
		i=$((i + 1))
		if [ "$i" -ge 120 ]; then
			echo "ternal-api did not become healthy at $api_url" >&2
			exit 1
		fi
		sleep 0.25
	done
}

stop_api() {
	kill "$api_pid"
	wait "$api_pid" || true
	api_pid=""
}

admin_request() {
	method=$1
	path=$2
	body=${3:-}
	if [ -n "$body" ]; then
		curl -fsS -X "$method" "$api_url$path" \
			-H 'x-ternal-user: backup-test-admin' \
			-H 'x-ternal-groups: ternal-admins' \
			-H 'x-csrf-token: dev-csrf' \
			-H 'content-type: application/json' \
			-d "$body"
	else
		curl -fsS -X "$method" "$api_url$path" \
			-H 'x-ternal-user: backup-test-admin' \
			-H 'x-ternal-groups: ternal-admins'
	fi
}

cd "$repo_dir"
go build -trimpath -o "$work/ternal-api" ./cmd/ternal-api
mkdir -p "$backup_dir" "$restore_dir"

start_api "$source_dir"
host_json=$(jq -n --arg name "$host_name" --arg endpoint_id "$endpoint_id" '{name:$name, endpoint_id:$endpoint_id, ssh_user:"ops", tags:{ops:"backup-restore"}, ssh_port:22, status:"online", owner:"ops-test", last_seen:null}')
host_id=$(admin_request POST /hosts "$host_json" | jq -er '.id')
admin_request GET "/hosts/$host_id" | jq -e --arg name "$host_name" '.name == $name' >/dev/null
stop_api

archive=$(sh "$script_dir/local-backup.sh" "$source_dir" "$backup_dir")
tar -xzf "$archive" -C "$restore_dir"

start_api "$restore_dir"
admin_request GET "/hosts/$host_id" | jq -e --arg name "$host_name" '.name == $name and .tags.ops == "backup-restore"' >/dev/null
stop_api

printf 'real Rhiza backup restore/readback passed\n'
printf 'host_id=%s\n' "$host_id"

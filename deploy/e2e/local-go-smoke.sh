#!/bin/sh
set -eu

for command in curl go jq; do
	command -v "$command" >/dev/null 2>&1 || {
		echo "missing required command: $command" >&2
		exit 127
	}
done

repo=$(CDPATH='' cd "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d)
port=${TERNAL_LOCAL_SMOKE_PORT:-$((23000 + ($$ % 1000)))}
base="http://127.0.0.1:$port"
pid=""

cleanup() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
	if [ "$status" -ne 0 ]; then
		sed -n '1,160p' "$work/api.log" >&2 || true
	fi
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT HUP INT TERM

cd "$repo"
go build -trimpath -o "$work/ternal-api" ./cmd/ternal-api
TERNAL_BIND="127.0.0.1:$port" \
	TERNAL_DEV_HEADERS=1 \
	TERNAL_SESSION_KEY=local-smoke-session-key-at-least-32-bytes \
	TERNAL_RELAY_ACCESS_TOKEN=local-smoke-relay-token-at-least-32-bytes \
	TERNAL_DATA_DIR="$work/data" \
	"$work/ternal-api" >"$work/api.log" 2>&1 &
pid=$!

i=0
until curl -fsS "$base/health" >/dev/null 2>&1; do
	i=$((i + 1))
	[ "$i" -lt 80 ] || { echo 'API did not become healthy' >&2; exit 1; }
	sleep 0.1
done

headers="-H X-Ternal-User:smoke-admin -H X-Ternal-Groups:ternal-admins"
curl -fsS "$base/" | grep -q '/assets/htmx.min.js'
curl -fsS "$base/assets/htmx.min.js" | grep -q 'htmx'
host=$(jq -nc --arg endpoint "$(printf '%064d' 1)" '{name:"go-smoke",endpoint_id:$endpoint,ssh_user:"ops",ssh_port:22,tags:{environment:"local"}}')
# shellcheck disable=SC2086
created=$(curl -fsS -X POST "$base/hosts/" $headers -H 'X-CSRF-Token: dev-csrf' -H 'Content-Type: application/json' -d "$host")
host_id=$(printf '%s' "$created" | jq -er .id)
# shellcheck disable=SC2086
curl -fsS "$base/hosts/$host_id" $headers | jq -e '.name == "go-smoke" and .ssh_user == "ops"' >/dev/null
# shellcheck disable=SC2086
curl -fsS -X DELETE "$base/hosts/$host_id" $headers -H 'X-CSRF-Token: dev-csrf' | jq -e '.status == "ok"' >/dev/null

printf 'Go API, gomponents portal, htmx asset, and authenticated CRUD smoke passed\n'

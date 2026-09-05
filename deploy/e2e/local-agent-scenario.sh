#!/bin/sh
set -eu

for command in awk curl jq ssh-keygen; do
	command -v "$command" >/dev/null 2>&1 || {
		echo "missing required command: $command" >&2
		exit 127
	}
done

api_url=${TERNAL_API_URL%/}
agent=${TERNAL_AGENT_BIN:-}
transport=${TERNAL_TRANSPORT_BIN:-}
state=${TERNAL_E2E_STATE_DIR:-}
relay_url=${TERNAL_E2E_RELAY_URL:-}
direct_address=${TERNAL_E2E_DIRECT_ADDRESS:-}
[ -n "$agent" ] && [ -x "$agent" ] || { echo 'TERNAL_AGENT_BIN must name the verified public ternal-agent binary' >&2; exit 1; }
[ -n "$transport" ] && [ -x "$transport" ] || { echo 'TERNAL_TRANSPORT_BIN must name the verified public pigeons binary' >&2; exit 1; }
[ -n "$state" ] && [ -d "$state" ] || { echo 'TERNAL_E2E_STATE_DIR must exist' >&2; exit 1; }
[ -n "$relay_url" ] || { echo 'TERNAL_E2E_RELAY_URL is required' >&2; exit 1; }
[ -n "$direct_address" ] || { echo 'TERNAL_E2E_DIRECT_ADDRESS is required' >&2; exit 1; }
[ -s "$state/user.json" ] || { echo 'local-user-scenario.sh must run first' >&2; exit 1; }

require_env() {
	eval "value=\${$1:-}"
	[ -n "$value" ] || { echo "$1 is required" >&2; exit 1; }
	case $value in *[[:space:]]*) echo "$1 contains whitespace" >&2; exit 1 ;; esac
}

write_headers() {
	path=$1
	cookie=$2
	csrf=$3
	case $cookie in *=*) ;; *) cookie="ternal_session=$cookie" ;; esac
	printf 'cookie: %s\nX-CSRF-Token: %s\n' "$cookie" "$csrf" >"$path"
}

request() {
	method=$1
	path=$2
	headers=$3
	body=${4:-}
	output=$5
	if [ -n "$body" ]; then
		printf '%s' "$body" | curl -sS -o "$output" -w '%{http_code}' -X "$method" "$api_url$path" \
			-H "@$headers" -H 'Content-Type: application/json' --data-binary @-
	else
		curl -sS -o "$output" -w '%{http_code}' -X "$method" "$api_url$path" -H "@$headers"
	fi
}

require_env TERNAL_ADMIN_SESSION_COOKIE
require_env TERNAL_ADMIN_CSRF_TOKEN
require_env TERNAL_USER_SESSION_COOKIE
require_env TERNAL_USER_CSRF_TOKEN
require_env TERNAL_OTHER_USER_SESSION_COOKIE
require_env TERNAL_E2E_USER_GROUP
require_env TERNAL_E2E_OTHER_USER_GROUP

umask 077
work=$(mktemp -d)
agent_pid=''
cleanup() {
	code=$?
	trap - EXIT INT TERM HUP
	if [ -n "$agent_pid" ] && kill -0 "$agent_pid" 2>/dev/null; then
		kill "$agent_pid" 2>/dev/null || true
		wait "$agent_pid" 2>/dev/null || true
	fi
	find "$work" -depth -delete
	exit "$code"
}
trap cleanup EXIT INT TERM HUP

write_headers "$work/admin.headers" "$TERNAL_ADMIN_SESSION_COOKIE" "$TERNAL_ADMIN_CSRF_TOKEN"
write_headers "$work/user.headers" "$TERNAL_USER_SESSION_COOKIE" "$TERNAL_USER_CSRF_TOKEN"
write_headers "$work/other.headers" "$TERNAL_OTHER_USER_SESSION_COOKIE" ''

expires_at=$(( $(date +%s) + 900 ))
batch_name="ied-greenfield-$(basename "$work")"
batch_request=$(jq -nc --arg name "$batch_name" --argjson expires_at "$expires_at" \
	'{name:$name,serial_prefix:"IED",expires_at:$expires_at,max_devices:1}')
status=$(request POST /manufacturing/batches "$work/admin.headers" "$batch_request" "$work/batch.json")
[ "$status" = 201 ] || { echo "manufacturing batch creation returned HTTP $status" >&2; exit 1; }
jq -er '.token' "$work/batch.json" >"$work/manufacturing-token"
batch_id=$(jq -er '.item.id' "$work/batch.json")

host_key="$state/ssh-host-key"
ssh-keygen -q -t ed25519 -N '' -f "$host_key"
fingerprint=$(ssh-keygen -E sha256 -lf "$host_key.pub" | awk '{print $2}')

agent_home="$state/agent-home"
config_home="$state/agent-config"
mkdir -p "$agent_home" "$config_home"
run_agent() (
	HOME="$agent_home" XDG_CONFIG_HOME="$config_home" \
	TERNAL_API_URL="$api_url" TERNAL_TRANSPORT_BIN="$transport" \
	TERNAL_DEVICE_KEY_FILE="$state/device.key" TERNAL_DEVICE_IDENTITY_FILE="$state/device-identity.json" \
	TERNAL_AGENT_STATUS_FILE="$state/agent-status.json" TERNAL_AGENT_AUTHORIZED_KEYS_PATH="$state/authorized_keys" \
	TERNAL_AGENT_SSH_USER=ops TERNAL_AGENT_SSH_PORT=22 \
	TERNAL_AGENT_RELAY_URLS="$relay_url" TERNAL_DIRECT_ADDRESSES="$direct_address" \
	TERNAL_AGENT_HEARTBEAT_SECONDS=1 TERNAL_AGENT_RESTART_BACKOFF_SECONDS=1 \
		"$agent" "$@"
)

run_agent device-keygen >"$work/device-keygen.out"
first_endpoint=$(HOME="$agent_home" XDG_CONFIG_HOME="$config_home" "$transport" endpoint-id --roost | tr '[:upper:]' '[:lower:]')
second_endpoint=$(HOME="$agent_home" XDG_CONFIG_HOME="$config_home" "$transport" endpoint-id --roost | tr '[:upper:]' '[:lower:]')
[ "$first_endpoint" = "$second_endpoint" ]
printf '%s' "$first_endpoint" | grep -Eq '^[[:xdigit:]]{64}$'

enrollment=$(TERNAL_MANUFACTURING_TOKEN_FILE="$work/manufacturing-token" run_agent enroll "$fingerprint")
device_id=$(printf '%s\n' "$enrollment" | awk -F '\t' '$1 == "device" {print $2}')
host_id=$(printf '%s\n' "$enrollment" | awk -F '\t' '$1 == "device" {print $3}')
serial=$(printf '%s\n' "$enrollment" | awk -F '\t' '$1 == "device" {print $4}')
[ -n "$device_id" ] && [ -n "$host_id" ] && [ "$serial" = IED-000001 ]
if TERNAL_MANUFACTURING_TOKEN_FILE="$work/manufacturing-token" run_agent enroll "$fingerprint" >/dev/null 2>&1; then
	echo 'closed manufacturing batch accepted a second enrollment' >&2
	exit 1
fi

run_agent heartbeat
status=$(request GET /manufacturing/batches "$work/admin.headers" '' "$work/batches.json")
[ "$status" = 200 ]
jq -e --arg id "$batch_id" 'any(.[]; .id == $id and .state == "closed" and .used_count == 1)' "$work/batches.json" >/dev/null
status=$(request GET /devices/ "$work/admin.headers" '' "$work/devices.json")
[ "$status" = 200 ]
jq -e --arg id "$device_id" --arg host "$host_id" --arg endpoint "$first_endpoint" --arg serial "$serial" --arg fingerprint "$fingerprint" '
	any(.[]; .id == $id and .host_id == $host and .endpoint_id == $endpoint
		and .serial_number == $serial and .ssh_host_key_fingerprint == $fingerprint and .last_seen_at != null)
' "$work/devices.json" >/dev/null

status=$(request GET "/hosts/$host_id" "$work/admin.headers" '' "$work/host.json")
[ "$status" = 200 ]
host_update=$(jq '.tags={scenario:"ied"}' "$work/host.json")
status=$(request PUT "/hosts/$host_id" "$work/admin.headers" "$host_update" "$work/host-update.json")
[ "$status" = 200 ]

policy_request=$(jq -nc --arg group "$TERNAL_E2E_USER_GROUP" \
	'{name:"ied-user",principal:("groups="+$group),host_selector:"tag:scenario=ied",ssh_users:["ops"]}')
status=$(request POST /policies/ "$work/admin.headers" "$policy_request" "$work/policy.json")
[ "$status" = 201 ]
policy_id=$(jq -er .id "$work/policy.json")
expired=$(( $(date +%s) - 1 ))
expired_request=$(jq -nc --arg group "$TERNAL_E2E_OTHER_USER_GROUP" --argjson expires "$expired" \
	'{name:"ied-expired",principal:("groups="+$group),host_selector:"tag:scenario=ied",ssh_users:["ops"],expires_at:$expires}')
status=$(request POST /policies/ "$work/admin.headers" "$expired_request" "$work/expired-policy.json")
[ "$status" = 201 ]
expired_policy_id=$(jq -er .id "$work/expired-policy.json")

status=$(request GET /hosts/ "$work/user.headers" '' "$work/user-hosts.json")
[ "$status" = 200 ]
jq -e --arg id "$host_id" 'any(.[]; .id == $id) and all(.[]; has("endpoint_id") | not)' "$work/user-hosts.json" >/dev/null
status=$(request GET /hosts/ "$work/other.headers" '' "$work/other-hosts.json")
[ "$status" = 200 ]
jq -e --arg id "$host_id" 'all(.[]; .id != $id)' "$work/other-hosts.json" >/dev/null

bad_access=$(jq -nc --arg host "$host_id" '{host_id:$host,ssh_user:"root"}')
status=$(request POST /access/ssh "$work/user.headers" "$bad_access" "$work/bad-access.json")
[ "$status" = 404 ] || { echo "unauthorized SSH user returned HTTP $status" >&2; exit 1; }
access_request=$(jq -nc --arg host "$host_id" '{host_id:$host,ssh_user:"ops"}')
status=$(request POST /access/ssh "$work/user.headers" "$access_request" "$work/access.json")
[ "$status" = 200 ] || { echo "authorized SSH request returned HTTP $status" >&2; exit 1; }
jq -e '
	.program == "ssh"
	and any(.args[]; . == "StrictHostKeyChecking=yes")
	and any(.args[]; startswith("KnownHostsCommand=ternalctl known-host-key SHA256:"))
	and any(.args[]; startswith("ProxyCommand=ternalctl proxy "))
' "$work/access.json" >/dev/null

run_agent sync-authorized-keys
cmp -s "$state/user-key.pub" "$state/authorized_keys"
generation=$(jq -er .generation "$state/authorized_keys.ternal-state")
[ "$generation" = 1 ]
run_agent sync-authorized-keys
[ "$(jq -er .generation "$state/authorized_keys.ternal-state")" = 1 ]
status=$(request GET /access/grants "$work/user.headers" '' "$work/grants.json")
[ "$status" = 200 ]
jq -e --arg host "$host_id" 'any(.[]; .host_id == $host and .key_installed == true)' "$work/grants.json" >/dev/null

client_endpoint=$(HOME="$agent_home" XDG_CONFIG_HOME="$config_home" "$transport" endpoint-id | tr '[:upper:]' '[:lower:]')
grant_request=$(jq -nc --arg host "$host_id" --arg endpoint "$client_endpoint" '{host_id:$host,client_endpoint_id:$endpoint,ttl:300}')
status=$(request POST /access/relay-grants "$work/user.headers" "$grant_request" "$work/relay-grant.json")
[ "$status" = 201 ]
jq -e '.expires_at - .created_at == 300' "$work/relay-grant.json" >/dev/null

timestamp=$(date +%s)
bad_heartbeat=$(jq -nc --arg serial "$serial" --arg endpoint "$first_endpoint" --arg fingerprint "$fingerprint" --argjson timestamp "$timestamp" \
	'{serial:$serial,endpoint_id:$endpoint,ssh_host_key_fingerprint:$fingerprint,timestamp:$timestamp,service_status:"healthy",signature:"invalid"}')
status=$(request POST /agents/heartbeat "$work/admin.headers" "$bad_heartbeat" "$work/bad-heartbeat.json")
[ "$status" = 401 ]
stale_heartbeat=$(printf '%s' "$bad_heartbeat" | jq --argjson timestamp "$((timestamp - 600))" '.timestamp=$timestamp')
status=$(request POST /agents/heartbeat "$work/admin.headers" "$stale_heartbeat" "$work/stale-heartbeat.json")
[ "$status" = 401 ]

run_agent run >"$work/agent-run.log" 2>&1 &
agent_pid=$!
i=0
until [ -s "$state/agent-status.json" ] && jq -e '.child == "running"' "$state/agent-status.json" >/dev/null 2>&1; do
	i=$((i + 1)); [ "$i" -lt 30 ] || { echo 'agent supervisor did not start transport' >&2; exit 1; }; sleep 1
done
status=$(request DELETE "/devices/$device_id" "$work/admin.headers" '' "$work/revoke.json")
[ "$status" = 200 ]
i=0
while kill -0 "$agent_pid" 2>/dev/null; do
	i=$((i + 1)); [ "$i" -lt 30 ] || { echo 'revoked agent did not stop transport' >&2; exit 1; }; sleep 1
done
wait "$agent_pid" 2>/dev/null || true
agent_pid=''
jq -e '.service == "revoked" and .child == "stopped"' "$state/agent-status.json" >/dev/null
if run_agent heartbeat >/dev/null 2>&1; then
	echo 'revoked device heartbeat succeeded' >&2
	exit 1
fi
status=$(request GET "/access/discovery/$host_id" "$work/user.headers" '' "$work/revoked-discovery.json")
[ "$status" = 404 ]
status=$(request POST /access/ssh "$work/user.headers" "$access_request" "$work/revoked-access.json")
[ "$status" = 404 ]

status=$(request DELETE "/policies/$policy_id" "$work/admin.headers" '' "$work/delete-policy.json")
[ "$status" = 200 ]
status=$(request DELETE "/policies/$expired_policy_id" "$work/admin.headers" '' "$work/delete-expired-policy.json")
[ "$status" = 200 ]

jq -n --arg device_id "$device_id" --arg host_id "$host_id" --arg serial "$serial" \
	--arg endpoint_id "$first_endpoint" --arg fingerprint "$fingerprint" \
	'{device_id:$device_id,host_id:$host_id,serial:$serial,endpoint_id:$endpoint_id,ssh_host_key_fingerprint:$fingerprint,state:"revoked"}' \
	>"$state/device.json"

printf 'manufacturing limit, persistent identity, signed heartbeat denial, policy visibility, authorized_keys ACK, 300-second grant, and revocation passed\n'

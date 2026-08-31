#!/bin/sh
set -eu

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 127
	fi
}

need awk
need curl
need docker
need nc
need sed
need ssh
need ssh-keygen

timeout_bin=$(command -v timeout || command -v gtimeout || true)
if [ -z "$timeout_bin" ]; then
	echo "missing required command: timeout or gtimeout" >&2
	exit 127
fi

sshd_bin=${SSHD_BIN:-}
if [ -z "$sshd_bin" ]; then
	sshd_bin=$(command -v sshd || true)
fi
if [ -z "$sshd_bin" ] && [ -x /usr/sbin/sshd ]; then
	sshd_bin=/usr/sbin/sshd
fi
if [ -z "$sshd_bin" ] || [ ! -x "$sshd_bin" ]; then
	echo "missing required command: sshd" >&2
	exit 127
fi

api_url=${TERNAL_API_URL:-}
managed_relay=0
csrf_token=${TERNAL_CSRF_TOKEN:-}
cli_dev_headers=1

if [ -n "$api_url" ]; then
	need jq
	api_url=$(printf '%s' "$api_url" | sed 's#/$##')
	if [ -n "${TERNAL_SESSION_COOKIE:-}" ] && [ -z "$csrf_token" ]; then
		echo "TERNAL_CSRF_TOKEN is required when TERNAL_SESSION_COOKIE is set" >&2
		exit 1
	fi
	if [ -n "${TERNAL_SESSION_COOKIE:-}" ]; then
		cli_dev_headers=0
	fi
	if [ -z "${TERNAL_SESSION_COOKIE:-}" ] && [ -z "$csrf_token" ]; then
		csrf_token=dev-csrf
	fi
fi
if [ -n "$api_url" ] && [ -n "${TERNAL_RELAY_ACCESS_TOKEN:-}" ]; then
	managed_relay=1
	need jq
fi

work=$(mktemp -d)
relay_name="ternal-pigeons-relay-smoke-$$"
pigeons_pid=""
sshd_pid=""
host_id=""
device_id=""
policy_id=""
ssh_key_id=""
grant_callback_verified=0
banner_file=""

assert_patched_route_guard() {
	patch=${TERNAL_TRANSPORT_PATCH_FILE:-deploy/agent/pigeons-0.1.1-ternal.patch}
	grep -F 'custom relay routes require a remote relay or direct address' "$patch" >/dev/null || {
		echo "patched pigeons route guard is missing: $patch" >&2
		exit 1
	}
}

api_request() {
	method=$1
	path=$2
	body=${3:-}
	auth_headers="$work/api-headers"
	: >"$auth_headers"
	if [ -n "${TERNAL_SESSION_COOKIE:-}" ]; then
		case "$TERNAL_SESSION_COOKIE" in
			*=*) cookie=$TERNAL_SESSION_COOKIE ;;
			*) cookie="ternal_session=$TERNAL_SESSION_COOKIE" ;;
		esac
		printf 'cookie: %s\n' "$cookie" >>"$auth_headers"
		if [ -n "$csrf_token" ]; then
			printf 'x-csrf-token: %s\n' "$csrf_token" >>"$auth_headers"
		fi
	else
		{
			printf 'x-ternal-user: %s\n' "${TERNAL_USER:-local-admin}"
			printf 'x-ternal-groups: %s\n' "${TERNAL_GROUPS:-ternal-admins}"
			printf 'x-ternal-claims: %s\n' "${TERNAL_CLAIMS:-role=ssh-admin}"
			[ -z "$csrf_token" ] || printf 'x-csrf-token: %s\n' "$csrf_token"
		} >>"$auth_headers"
	fi
	if [ -n "$body" ]; then
		curl -fsS -X "$method" "$api_url$path" \
			-H "@$auth_headers" \
			-H 'content-type: application/json' \
			-d "$body"
	else
		curl -fsS -X "$method" "$api_url$path" \
			-H "@$auth_headers"
	fi
}

cleanup() {
	if [ -n "$api_url" ]; then
		[ -n "$ssh_key_id" ] && api_request DELETE "/ssh-keys/$ssh_key_id" >/dev/null 2>&1 || true
		[ -n "$policy_id" ] && api_request DELETE "/policies/$policy_id" >/dev/null 2>&1 || true
		[ -n "$device_id" ] && api_request DELETE "/devices/$device_id" >/dev/null 2>&1 || true
		[ -n "$host_id" ] && api_request DELETE "/hosts/$host_id" >/dev/null 2>&1 || true
	fi
	[ -n "$pigeons_pid" ] && kill "$pigeons_pid" >/dev/null 2>&1 || true
	[ -n "$sshd_pid" ] && kill "$sshd_pid" >/dev/null 2>&1 || true
	docker rm -f "$relay_name" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT INT TERM HUP

relay_port=${TERNAL_SMOKE_RELAY_PORT:-3340}
if nc -z 127.0.0.1 "$relay_port" >/dev/null 2>&1; then
	if [ -n "$api_url" ]; then
		echo "relay port $relay_port is already in use; choose a free TERNAL_SMOKE_RELAY_PORT and start the API with matching TERNAL_AGENT_RELAY_URLS" >&2
		exit 1
	fi
	relay_port=$((relay_port + 1))
fi
if [ "$managed_relay" -eq 1 ]; then
	relay_access_url=${TERNAL_SMOKE_RELAY_ACCESS_URL:-$api_url/internal/iroh-relay/access}
	case "$relay_access_url" in
		http://127.0.0.1:*|http://localhost:*)
			relay_access_url=$(printf '%s' "$relay_access_url" | sed -e 's#127.0.0.1#host.docker.internal#' -e 's#localhost#host.docker.internal#')
			;;
	esac
	cat >"$work/relay.toml" <<EOF
enable_relay = true
http_bind_addr = "[::]:3340"
enable_quic_addr_discovery = false
access.http.url = "$relay_access_url"
EOF
	docker create --name "$relay_name" \
		--add-host host.docker.internal:host-gateway \
		-p "127.0.0.1:${relay_port}:3340" \
		-e IROH_RELAY_HTTP_BEARER_TOKEN="$TERNAL_RELAY_ACCESS_TOKEN" \
		n0computer/iroh-relay:v0.96.1 --config-path /relay.toml >/dev/null
	docker cp "$work/relay.toml" "$relay_name:/relay.toml"
	docker start "$relay_name" >/dev/null
else
	docker run -d --name "$relay_name" \
		-p "127.0.0.1:${relay_port}:3340" \
		n0computer/iroh-relay:v0.96.1 --dev >/dev/null
fi

i=0
while [ "$i" -lt 80 ]; do
	nc -z 127.0.0.1 "$relay_port" >/dev/null 2>&1 && break
	i=$((i + 1))
	sleep 0.25
done
if ! nc -z 127.0.0.1 "$relay_port" >/dev/null 2>&1; then
	echo "iroh-relay did not listen" >&2
	docker logs "$relay_name" >&2 || true
	exit 1
fi
relay_url="http://127.0.0.1:${relay_port}"

bin=${TERNAL_TRANSPORT_BIN:?set TERNAL_TRANSPORT_BIN to the patched transport binary}
test -x "$bin" || { echo "pigeons binary is not executable: $bin" >&2; exit 1; }

ssh_port=${TERNAL_SMOKE_SSH_PORT:-40226}
while nc -z 127.0.0.1 "$ssh_port" >/dev/null 2>&1; do
	ssh_port=$((ssh_port + 1))
done
ssh-keygen -q -t ed25519 -N '' -f "$work/ssh_host_ed25519_key"
ssh-keygen -q -t ed25519 -N '' -f "$work/ssh_client_ed25519_key"
ssh_host_fingerprint=$(ssh-keygen -E sha256 -lf "$work/ssh_host_ed25519_key.pub" | awk '{print $2}')
cp "$work/ssh_client_ed25519_key.pub" "$work/authorized_keys"
chmod 600 "$work/authorized_keys"
cat >"$work/sshd_config" <<EOF
Port $ssh_port
ListenAddress 127.0.0.1
HostKey $work/ssh_host_ed25519_key
PidFile $work/sshd.pid
AuthorizedKeysFile $work/authorized_keys
StrictModes no
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
UsePAM no
LogLevel ERROR
EOF

"$sshd_bin" -t -f "$work/sshd_config"
"$sshd_bin" -D -e -f "$work/sshd_config" >"$work/sshd.log" 2>&1 &
sshd_pid=$!

i=0
while [ "$i" -lt 40 ]; do
	nc -z 127.0.0.1 "$ssh_port" >/dev/null 2>&1 && break
	i=$((i + 1))
	sleep 0.25
done
if ! nc -z 127.0.0.1 "$ssh_port" >/dev/null 2>&1; then
	echo "temporary sshd did not listen" >&2
	cat "$work/sshd.log" >&2
	exit 1
fi

mkdir -p "$work/home"
HOME="$work/home" "$bin" roost --ssh-port "$ssh_port" \
	--relay-url "$relay_url" >"$work/pigeons-roost.log" 2>&1 &
pigeons_pid=$!

endpoint=""
i=0
while [ "$i" -lt 160 ]; do
	endpoint=$(sed -nE 's/^roost is running! id: ([[:xdigit:]]{64})$/\1/p' "$work/pigeons-roost.log" | head -n 1)
	[ -n "$endpoint" ] && break
	if ! kill -0 "$pigeons_pid" >/dev/null 2>&1; then
		echo "pigeons roost exited early" >&2
		cat "$work/pigeons-roost.log" >&2
		docker logs "$relay_name" >&2 || true
		exit 1
	fi
	i=$((i + 1))
	sleep 0.25
done
if [ -z "$endpoint" ]; then
	echo "endpoint id not found" >&2
	cat "$work/pigeons-roost.log" >&2
	exit 1
fi

if [ "$managed_relay" -eq 0 ]; then
	sleep 3
	i=0
	while [ "$i" -lt 8 ]; do
		"$timeout_bin" 30 "$bin" fly --stdio "$endpoint" --relay-url "$relay_url" \
			</dev/null >"$work/proxy.out" 2>"$work/proxy.err" || true
		head -n 1 "$work/proxy.out" | grep '^SSH-' >/dev/null 2>&1 && break
		i=$((i + 1))
		sleep 2
	done
	if ! head -n 1 "$work/proxy.out" | grep '^SSH-' >/dev/null 2>&1; then
		echo "SSH banner not observed through pigeons fly with local relay" >&2
		exit 1
	fi
	banner_file="$work/proxy.out"
fi

if [ "$managed_relay" -eq 0 ] && [ -n "${TERNAL_SMOKE_POST_HEALTHY_HOOK:-}" ]; then
	TERNAL_SMOKE_HOOK=1 \
	TERNAL_SMOKE_WORK=$work \
	TERNAL_SMOKE_RELAY_NAME=$relay_name \
	TERNAL_SMOKE_RELAY_URL=$relay_url \
	TERNAL_SMOKE_ENDPOINT=$endpoint \
	TERNAL_TRANSPORT_BIN=$bin \
	TERNAL_SMOKE_TIMEOUT_BIN=$timeout_bin \
		sh "$TERNAL_SMOKE_POST_HEALTHY_HOOK"
fi

if [ -n "$api_url" ]; then
	api_request GET /health >/dev/null
	host_name="smoke-$$"
	ssh_user=${TERNAL_SMOKE_SSH_USER:-$(id -un)}
	policy_group=${TERNAL_SMOKE_GROUP:-ternal-admins}

	agent_bin=${TERNAL_SMOKE_AGENT_BIN:-}
	if [ -z "$agent_bin" ] && [ -x dist/bin/ternal-agent ]; then
		agent_bin=dist/bin/ternal-agent
	fi
	if [ -z "$agent_bin" ] || [ ! -x "$agent_bin" ]; then
		echo "API relay smoke requires TERNAL_SMOKE_AGENT_BIN or dist/bin/ternal-agent" >&2
		exit 1
	fi

	api_request POST /manufacturing/tokens '{}' | jq -er '.token' >"$work/manufacturing-token"
	chmod 600 "$work/manufacturing-token"
	run_agent() {
		HOME="$work/home" \
		TERNAL_API_URL="$api_url" \
		TERNAL_TRANSPORT_BIN="$bin" \
		TERNAL_DEVICE_KEY_FILE="$work/device.key" \
		TERNAL_DEVICE_IDENTITY_FILE="$work/device-identity.json" \
		TERNAL_AGENT_SSH_USER="$ssh_user" \
		TERNAL_AGENT_SSH_PORT="$ssh_port" \
		TERNAL_AGENT_RELAY_URLS="$relay_url" \
			"$agent_bin" "$@"
	}
	run_agent device-keygen >/dev/null
	enrollment=$(TERNAL_MANUFACTURING_TOKEN_FILE="$work/manufacturing-token" run_agent enroll "$ssh_host_fingerprint" "$host_name")
	device_id=$(printf '%s\n' "$enrollment" | awk -F '\t' '$1 == "device" {print $2}')
	host_id=$(printf '%s\n' "$enrollment" | awk -F '\t' '$1 == "device" {print $3}')
	[ -n "$device_id" ] && [ -n "$host_id" ] || {
		echo "device enrollment did not return device and host IDs" >&2
		exit 1
	}
	run_agent heartbeat >/dev/null

	ssh_key_response=$(api_request POST /ssh-keys "$(jq -n --rawfile public_key "$work/ssh_client_ed25519_key.pub" '{public_key:$public_key}')")
	ssh_key_id=$(printf '%s' "$ssh_key_response" | jq -er '.id')

	policy_json=$(jq -n \
		--arg name "$host_name" \
		--arg group "$policy_group" \
		--arg host_selector "$host_name" \
		--arg ssh_user "$ssh_user" \
		'{name:$name, principal:$group, host_selector:$host_selector, ssh_users:[$ssh_user], expires_at:null}')
	policy_response=$(api_request POST /policies "$policy_json")
	policy_id=$(printf '%s' "$policy_response" | jq -r '.id')

	access_response=$(api_request POST /access/ssh "$(jq -n --arg host_id "$host_id" --arg ssh_user "$ssh_user" '{host_id:$host_id, ssh_user:$ssh_user}')")
	issued_proxy=$(printf '%s' "$access_response" | jq -er '.args[] | select(startswith("ProxyCommand="))')
	expected_proxy="ProxyCommand=ternalctl proxy $host_id %h:%p --relay-url $relay_url"
	if [ "$issued_proxy" != "$expected_proxy" ]; then
		echo "Ternal issued unexpected ProxyCommand" >&2
		echo "start the API with TERNAL_AGENT_RELAY_URLS=$relay_url" >&2
		echo "expected: $expected_proxy" >&2
		echo "actual:   $issued_proxy" >&2
		exit 1
	fi
	printf 'ternal grant-aware command issuance smoke passed\n'

	ternalctl_bin=${TERNAL_SMOKE_TERNALCTL_BIN:-}
	if [ -z "$ternalctl_bin" ] && [ -x dist/bin/ternalctl ]; then
		ternalctl_bin=dist/bin/ternalctl
	fi
	if [ -z "$ternalctl_bin" ]; then
		ternalctl_bin=$(command -v ternalctl || true)
	fi
	if [ "$managed_relay" -eq 1 ] && [ -z "$ternalctl_bin" ]; then
		echo "managed relay smoke requires TERNAL_SMOKE_TERNALCTL_BIN or ternalctl on PATH" >&2
		exit 1
	fi

	if [ -n "$ternalctl_bin" ] && [ "$managed_relay" -eq 1 ]; then
		assert_patched_route_guard
		client_key_dir="$work/client-key-dir"
		mkdir -p "$client_key_dir"
		cat >"$work/pigeons-wrapper" <<EOF
#!/bin/sh
case "\${1:-}" in
	endpoint-id)
		exec '$bin' endpoint-id --key-dir '$client_key_dir'
		;;
	fly)
		shift
		exec '$bin' fly --key-dir '$client_key_dir' "\$@"
		;;
	*)
		exit 64
		;;
esac
EOF
		chmod +x "$work/pigeons-wrapper"

		set +e
		TERNAL_API_URL="$api_url" \
		TERNAL_DEV_HEADERS="$cli_dev_headers" \
		HOME="$work/home" \
		XDG_CONFIG_HOME="$work/config" \
		TERNAL_SESSION_COOKIE="${TERNAL_SESSION_COOKIE:-}" \
		TERNAL_CSRF_TOKEN="$csrf_token" \
		TERNAL_USER="${TERNAL_USER:-local-admin}" \
		TERNAL_GROUPS="${TERNAL_GROUPS:-ternal-admins}" \
		TERNAL_CLAIMS="${TERNAL_CLAIMS:-role=ssh-admin}" \
		TERNAL_TRANSPORT_BIN="$work/pigeons-wrapper" \
			"$timeout_bin" 30 "$ternalctl_bin" proxy "$host_id" "$endpoint:$ssh_port" --relay-url "$relay_url" \
			</dev/null >"$work/ternalctl-proxy.out" 2>"$work/ternalctl-proxy.err"
		proxy_status=$?
		set -e
		if ! head -n 1 "$work/ternalctl-proxy.out" | grep '^SSH-' >/dev/null 2>&1; then
			echo "ternalctl proxy did not reach the SSH banner after grant issuance (status $proxy_status)" >&2
			cat "$work/ternalctl-proxy.err" >&2
			exit 1
		fi
		banner_file="$work/ternalctl-proxy.out"

		proxy_command="$ternalctl_bin proxy $host_id %h:%p --relay-url $relay_url"
		known_hosts_command="$ternalctl_bin known-host-key $ssh_host_fingerprint %I %f %t %K"
		wrong_fingerprint=SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
		if [ "$wrong_fingerprint" = "$ssh_host_fingerprint" ]; then
			wrong_fingerprint=SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB
		fi
		wrong_known_hosts_command="$ternalctl_bin known-host-key $wrong_fingerprint %I %f %t %K"

		set +e
		TERNAL_API_URL="$api_url" \
		TERNAL_DEV_HEADERS="$cli_dev_headers" \
		HOME="$work/home" \
		XDG_CONFIG_HOME="$work/config" \
		TERNAL_SESSION_COOKIE="${TERNAL_SESSION_COOKIE:-}" \
		TERNAL_CSRF_TOKEN="$csrf_token" \
		TERNAL_USER="${TERNAL_USER:-local-admin}" \
		TERNAL_GROUPS="${TERNAL_GROUPS:-ternal-admins}" \
		TERNAL_CLAIMS="${TERNAL_CLAIMS:-role=ssh-admin}" \
		TERNAL_TRANSPORT_BIN="$work/pigeons-wrapper" \
			"$timeout_bin" 30 ssh -F /dev/null -p "$ssh_port" \
			-o BatchMode=yes \
			-o IdentitiesOnly=yes \
			-o UserKnownHostsFile=none \
			-o GlobalKnownHostsFile=none \
			-o StrictHostKeyChecking=yes \
			-o CheckHostIP=no \
			-o UpdateHostKeys=no \
			-o "ProxyCommand=$proxy_command" \
			-o "KnownHostsCommand=$wrong_known_hosts_command" \
			-i "$work/ssh_client_ed25519_key" \
			"$ssh_user@$endpoint" true \
			>"$work/strict-host-reject.out" 2>"$work/strict-host-reject.err"
		wrong_host_status=$?
		set -e
		if [ "$wrong_host_status" -eq 0 ]; then
			echo "SSH session unexpectedly accepted the wrong pinned host fingerprint" >&2
			exit 1
		fi
		if ! grep -F 'host key rejected: fingerprint mismatch' "$work/strict-host-reject.err" >/dev/null; then
			echo "wrong-fingerprint SSH attempt failed without Ternal's pin-mismatch evidence" >&2
			cat "$work/strict-host-reject.err" >&2
			exit 1
		fi

		TERNAL_API_URL="$api_url" \
		TERNAL_DEV_HEADERS="$cli_dev_headers" \
		HOME="$work/home" \
		XDG_CONFIG_HOME="$work/config" \
		TERNAL_SESSION_COOKIE="${TERNAL_SESSION_COOKIE:-}" \
		TERNAL_CSRF_TOKEN="$csrf_token" \
		TERNAL_USER="${TERNAL_USER:-local-admin}" \
		TERNAL_GROUPS="${TERNAL_GROUPS:-ternal-admins}" \
		TERNAL_CLAIMS="${TERNAL_CLAIMS:-role=ssh-admin}" \
		TERNAL_TRANSPORT_BIN="$work/pigeons-wrapper" \
			"$timeout_bin" 30 ssh -F /dev/null -p "$ssh_port" \
			-o BatchMode=yes \
			-o IdentitiesOnly=yes \
			-o UserKnownHostsFile=none \
			-o GlobalKnownHostsFile=none \
			-o StrictHostKeyChecking=yes \
			-o CheckHostIP=no \
			-o UpdateHostKeys=no \
			-o "ProxyCommand=$proxy_command" \
			-o "KnownHostsCommand=$known_hosts_command" \
			-i "$work/ssh_client_ed25519_key" \
			"$ssh_user@$endpoint" "printf 'ternal-managed-session\\n'" \
			>"$work/strict-host-accept.out" 2>"$work/strict-host-accept.err"
		if ! grep -Fx 'ternal-managed-session' "$work/strict-host-accept.out" >/dev/null; then
			echo "strictly pinned SSH session did not return its sentinel" >&2
			cat "$work/strict-host-accept.err" >&2
			exit 1
		fi
		printf 'strict SSH host-key rejection/acceptance and managed-relay session smoke passed\n'

		denied_key_dir="$work/denied-client-key-dir"
		mkdir -p "$denied_key_dir"
		denied_endpoint=$($bin endpoint-id --key-dir "$denied_key_dir")
		printf '%s\n' "$denied_endpoint" | grep -E '^[[:xdigit:]]{64}$' >/dev/null || {
			echo "pigeons did not emit a denied-client endpoint ID" >&2
			exit 1
		}
		if "$timeout_bin" 10 "$bin" fly --stdio --key-dir "$denied_key_dir" "$endpoint" --relay-url "$relay_url" \
			</dev/null >"$work/denied-proxy.out" 2>"$work/denied-proxy.err" && \
			head -n 1 "$work/denied-proxy.out" | grep '^SSH-' >/dev/null 2>&1; then
			echo "managed relay admitted an ungranted client endpoint: $denied_endpoint" >&2
			exit 1
		fi
		grant_callback_verified=1
		printf 'managed relay admission smoke passed: real client grant reached SSH banner and ungranted client was denied\n'
	else
		printf 'managed relay admission smoke skipped: set TERNAL_SMOKE_TERNALCTL_BIN and TERNAL_RELAY_ACCESS_TOKEN\n'
	fi
fi

	printf 'pigeons local-relay tunnel smoke passed\n'
printf 'relay_url=%s\n' "$relay_url"
printf 'endpoint=%s\n' "$endpoint"
printf 'server_ssh_port=%s\n' "$ssh_port"
printf 'banner=%s\n' "$(head -n 1 "$banner_file")"
printf 'grant_callback_verified=%s\n' "$grant_callback_verified"

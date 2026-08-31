#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
parser="$script_dir/../parse-transport-jsonl.sh"
work=${TERNAL_TRANSPORT_MATRIX_WORK:-}
bin=${TERNAL_PIGEONS_BIN:-dist/pigeons-linux-amd64}

capability_skip() {
	if [ -n "${TERNAL_RELAY_URL:-}" ]; then
		states='["relay-only"]'
	else
		states='["relay-only","direct-only","both-blocked","recovery"]'
	fi
	jq -nc --arg reason "$1" --argjson states "$states" '{driver:"linux-netns-pigeons-v1",path_observable:false,evidence:"production",reason:$reason,states:$states}'
	exit 0
}

capabilities() {
	[ "$(uname -s)" = Linux ] || capability_skip "linux network namespaces are unavailable on this runner"
	[ "$(id -u)" -eq 0 ] || capability_skip "linux transport driver requires root or an equivalent privileged vind workload"
	[ -x "$bin" ] || capability_skip "patched pigeons binary not found at $bin"
	for command in ip iptables iptables-save jq nc sed ss ssh-keygen; do
		command -v "$command" >/dev/null 2>&1 || capability_skip "missing required Linux transport command: $command"
	done
	sshd=${SSHD_BIN:-$(command -v sshd || true)}
	[ -n "$sshd" ] || [ -x /usr/sbin/sshd ] || capability_skip "sshd is required for the transport fixture"
	timeout_bin=$(command -v timeout || true)
	[ -n "$timeout_bin" ] || capability_skip "GNU timeout is required for the Linux transport fixture"
	if [ -n "${TERNAL_RELAY_URL:-}" ]; then
		for command in curl getent sysctl; do
			command -v "$command" >/dev/null 2>&1 || capability_skip "missing required external-relay command: $command"
		done
		registration_hook=${TERNAL_TRANSPORT_REGISTER_ENDPOINT_CMD:-}
		[ -n "$registration_hook" ] && [ -x "$registration_hook" ] || capability_skip "TERNAL_TRANSPORT_REGISTER_ENDPOINT_CMD and endpoint-registration credentials are required for the protected production relay"
		printf '%s\n' '{"driver":"linux-netns-pigeons-v1","path_observable":true,"evidence":"production","states":["relay-only"]}'
		exit 0
	fi
	command -v docker >/dev/null 2>&1 || capability_skip "Docker is unavailable to run the pinned local iroh relay"
	docker info >/dev/null 2>&1 || capability_skip "Docker is unavailable to run the pinned local iroh relay"
	help=$($bin fly --help 2>&1 || true)
	printf '%s' "$help" | grep -- '--direct-address' >/dev/null 2>&1 || capability_skip "patched pigeons does not expose repeated --direct-address"
	printf '%s' "$help" | grep -- '--transport-diagnostics' >/dev/null 2>&1 || capability_skip "patched pigeons does not expose transport diagnostics"
	printf '%s\n' '{"driver":"linux-netns-pigeons-v1","path_observable":true,"evidence":"production","states":["relay-only","direct-only","both-blocked","recovery"]}'
}

state_file() {
	printf '%s/%s\n' "$work" "$1"
}

read_state() {
	file=$(state_file "$1")
	[ -f "$file" ] || return 1
	sed -n '1p' "$file"
}

write_state() {
	printf '%s\n' "$2" >"$(state_file "$1")"
}

cleanup() {
	[ -n "$work" ] || return 0
	server_pid=$(read_state server-pid 2>/dev/null || true)
	sshd_pid=$(read_state sshd-pid 2>/dev/null || true)
	relay_name=$(read_state relay-name 2>/dev/null || true)
	netns=$(read_state netns 2>/dev/null || true)
	host_if=$(read_state host-if 2>/dev/null || true)
	subnet=$(read_state external-subnet 2>/dev/null || true)
	marker=$(read_state firewall-marker 2>/dev/null || true)
	ip_forward_before=$(read_state ip-forward-before 2>/dev/null || true)
	registration_hook=${TERNAL_TRANSPORT_REGISTER_ENDPOINT_CMD:-}
	registration_started=$(read_state registration-started 2>/dev/null || true)
	[ "$registration_started" = 1 ] && [ -n "$registration_hook" ] && [ -x "$registration_hook" ] && "$registration_hook" cleanup >/dev/null 2>&1 || true
	[ -n "$server_pid" ] && kill "$server_pid" >/dev/null 2>&1 || true
	[ -n "$sshd_pid" ] && kill "$sshd_pid" >/dev/null 2>&1 || true
	[ -n "$relay_name" ] && docker rm -f "$relay_name" >/dev/null 2>&1 || true
	if [ -n "$subnet" ] && [ -n "$marker" ] && [ -n "$host_if" ]; then
		iptables -t nat -D POSTROUTING -s "$subnet" -m comment --comment "$marker" -j MASQUERADE >/dev/null 2>&1 || true
		iptables -D FORWARD -i "$host_if" -m comment --comment "$marker" -j ACCEPT >/dev/null 2>&1 || true
		iptables -D FORWARD -o "$host_if" -m conntrack --ctstate ESTABLISHED,RELATED -m comment --comment "$marker" -j ACCEPT >/dev/null 2>&1 || true
	fi
	[ -n "$netns" ] && ip netns delete "$netns" >/dev/null 2>&1 || true
	[ -n "$netns" ] && rm -rf "/etc/netns/$netns" >/dev/null 2>&1 || true
	[ -n "$host_if" ] && ip link delete "$host_if" >/dev/null 2>&1 || true
	[ "$ip_forward_before" = 0 ] && sysctl -q -w net.ipv4.ip_forward=0 >/dev/null 2>&1 || true
}

setup_external_egress() {
	netns=$1
	host_if=$2
	host_ip=$3
	subnet=$4
	relay_url=$5
	marker="ternal-transport-$netns"
	write_state external-subnet "$subnet"
	write_state firewall-marker "$marker"
	write_state ip-forward-before "$(cat /proc/sys/net/ipv4/ip_forward)"
	sysctl -q -w net.ipv4.ip_forward=1 >/dev/null
	ip netns exec "$netns" ip route add default via "$host_ip"
	iptables -t nat -A POSTROUTING -s "$subnet" -m comment --comment "$marker" -j MASQUERADE
	iptables -A FORWARD -i "$host_if" -m comment --comment "$marker" -j ACCEPT
	iptables -A FORWARD -o "$host_if" -m conntrack --ctstate ESTABLISHED,RELATED -m comment --comment "$marker" -j ACCEPT

	authority=${relay_url#*://}
	authority=${authority%%/*}
	case "$authority" in
		\[*)
		echo "ingress relay IPv6 authorities are not supported by this netns fixture" >&2
			exit 1
			;;
	esac
	relay_host=${authority%%:*}
	relay_ip=$(getent ahostsv4 "$relay_host" | awk 'NR == 1 { print $1 }')
	[ -n "$relay_ip" ] || {
		echo "could not resolve ingress relay host: $relay_host" >&2
		exit 1
	}
	mkdir -p "/etc/netns/$netns"
	cp /etc/hosts "/etc/netns/$netns/hosts"
	printf '%s %s\n' "$relay_ip" "$relay_host" >>"/etc/netns/$netns/hosts"
	ip netns exec "$netns" curl -fsS --max-time "${TERNAL_RELAY_PING_TIMEOUT:-10}" "${relay_url%/}/ping" >/dev/null
}

wait_tcp() {
	host=$1
	port=$2
	i=0
	while [ "$i" -lt 80 ]; do
		nc -z -w 1 "$host" "$port" >/dev/null 2>&1 && return 0
		i=$((i + 1))
		sleep 0.25
	done
	return 1
}

prepare() {
	[ -n "$work" ] || {
		echo "TERNAL_TRANSPORT_MATRIX_WORK is required" >&2
		exit 1
	}
	cleanup

	suffix=$(( $$ % 100000 ))
	netns="tmn$suffix"
	host_if="tmh$suffix"
	peer_if="tmp$suffix"
	octet=$((($$ % 200) + 20))
	host_ip="10.203.$octet.1"
	client_ip="10.203.$octet.2"
	subnet="10.203.$octet.0/30"
	write_state netns "$netns"
	write_state host-if "$host_if"
	write_state host-ip "$host_ip"

	ip netns add "$netns"
	ip link add "$host_if" type veth peer name "$peer_if"
	ip link set "$peer_if" netns "$netns"
	ip addr add "$host_ip/30" dev "$host_if"
	ip link set "$host_if" up
	ip netns exec "$netns" ip link set lo up
	ip netns exec "$netns" ip addr add "$client_ip/30" dev "$peer_if"
	ip netns exec "$netns" ip link set "$peer_if" up

	if [ -n "${TERNAL_RELAY_URL:-}" ]; then
		relay_url=${TERNAL_RELAY_URL%/}
		write_state relay-port 0
		setup_external_egress "$netns" "$host_if" "$host_ip" "$subnet" "$relay_url"
	else
		relay_port=${TERNAL_TRANSPORT_RELAY_PORT:-3340}
		while nc -z 127.0.0.1 "$relay_port" >/dev/null 2>&1; do relay_port=$((relay_port + 1)); done
		relay_name="ternal-transport-relay-$suffix"
		write_state relay-name "$relay_name"
		write_state relay-port "$relay_port"
		docker run -d --name "$relay_name" -p "0.0.0.0:$relay_port:3340" \
			n0computer/iroh-relay:v0.96.1 --dev >/dev/null
		wait_tcp "$host_ip" "$relay_port" || {
			docker logs "$relay_name" >&2 || true
			echo "relay did not become reachable from the isolated client network" >&2
			exit 1
		}
		relay_url="http://$host_ip:$relay_port"
	fi
	write_state relay-url "$relay_url"

	sshd=${SSHD_BIN:-$(command -v sshd || true)}
	[ -n "$sshd" ] || sshd=/usr/sbin/sshd
	ssh_port=${TERNAL_TRANSPORT_SSH_PORT:-40226}
	while nc -z 127.0.0.1 "$ssh_port" >/dev/null 2>&1; do ssh_port=$((ssh_port + 1)); done
	ssh-keygen -q -t ed25519 -N '' -f "$work/ssh_host_ed25519_key"
	cat >"$work/sshd_config" <<EOF
Port $ssh_port
ListenAddress 127.0.0.1
HostKey $work/ssh_host_ed25519_key
PidFile $work/sshd.pid
AuthorizedKeysFile none
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
UsePAM no
LogLevel ERROR
EOF
	"$sshd" -t -f "$work/sshd_config"
	"$sshd" -D -e -f "$work/sshd_config" >"$work/sshd.log" 2>&1 &
	sshd_pid=$!
	write_state sshd-pid "$sshd_pid"
	wait_tcp 127.0.0.1 "$ssh_port" || {
		cat "$work/sshd.log" >&2
		exit 1
	}

	mkdir -p "$work/server-home" "$work/client-home"
	HOME="$work/server-home" "$bin" roost --ssh-port "$ssh_port" \
		--relay-url "$relay_url" >"$work/pigeons-roost.log" 2>&1 &
	server_pid=$!
	write_state server-pid "$server_pid"

	endpoint=""
	i=0
	while [ "$i" -lt 160 ]; do
		endpoint=$(sed -nE 's/^roost is running! id: ([[:xdigit:]]{64})$/\1/p' "$work/pigeons-roost.log" | head -n 1)
		[ -n "$endpoint" ] && break
		kill -0 "$server_pid" >/dev/null 2>&1 || {
			cat "$work/pigeons-roost.log" >&2
			exit 1
		}
		i=$((i + 1))
		sleep 0.25
	done
	[ -n "$endpoint" ] || {
		echo "endpoint ID was not emitted by pigeons roost" >&2
		exit 1
	}
	write_state endpoint-id "$endpoint"
	if [ -n "${TERNAL_RELAY_URL:-}" ]; then
		registration_hook=${TERNAL_TRANSPORT_REGISTER_ENDPOINT_CMD:?}
		write_state registration-started 1
		"$registration_hook" register server "$endpoint"
		HOME="$work/client-home" "$bin" roost --ssh-port "$ssh_port" \
			--relay-url "$relay_url" >"$work/client-identity.log" 2>&1 &
		client_identity_pid=$!
		client_endpoint=""
		i=0
		while [ "$i" -lt 80 ]; do
			client_endpoint=$(sed -nE 's/^roost is running! id: ([[:xdigit:]]{64})$/\1/p' "$work/client-identity.log" | head -n 1)
			[ -n "$client_endpoint" ] && break
			kill -0 "$client_identity_pid" >/dev/null 2>&1 || break
			i=$((i + 1))
			sleep 0.25
		done
		kill "$client_identity_pid" >/dev/null 2>&1 || true
		wait "$client_identity_pid" >/dev/null 2>&1 || true
		[ -n "$client_endpoint" ] || {
			cat "$work/client-identity.log" >&2
			echo "could not create persistent client endpoint identity" >&2
			exit 1
		}
		"$registration_hook" register client "$client_endpoint"
		write_state client-endpoint-id "$client_endpoint"

		kill "$server_pid" >/dev/null 2>&1 || true
		wait "$server_pid" >/dev/null 2>&1 || true
		HOME="$work/server-home" "$bin" roost --ssh-port "$ssh_port" \
		--relay-url "$relay_url" >"$work/pigeons-roost-restarted.log" 2>&1 &
		server_pid=$!
		write_state server-pid "$server_pid"
		restarted_endpoint=""
		i=0
		while [ "$i" -lt 80 ]; do
			restarted_endpoint=$(sed -nE 's/^roost is running! id: ([[:xdigit:]]{64})$/\1/p' "$work/pigeons-roost-restarted.log" | head -n 1)
			[ -n "$restarted_endpoint" ] && break
			kill -0 "$server_pid" >/dev/null 2>&1 || break
			i=$((i + 1))
			sleep 0.25
		done
		if [ "$restarted_endpoint" != "$endpoint" ]; then
			cat "$work/pigeons-roost-restarted.log" >&2
			echo "server endpoint identity changed or failed after registration" >&2
			exit 1
		fi
		sleep "${TERNAL_TRANSPORT_REGISTRATION_SETTLE_SECONDS:-3}"
	fi

	udp_port=""
	i=0
	while [ "$i" -lt 40 ]; do
		udp_port=$(ss -Hlunp | awk -v marker="pid=$server_pid," '
			index($0, marker) {
				addr = $4
				sub(/^.*:/, "", addr)
				if (addr ~ /^[0-9]+$/) { print addr; exit }
			}
		')
		[ -n "$udp_port" ] && break
		i=$((i + 1))
		sleep 0.25
	done
	[ -n "$udp_port" ] || {
		echo "could not discover the iroh server UDP socket with ss" >&2
		exit 1
	}
	write_state direct-address "$host_ip:$udp_port"
}

apply_state() {
	state=$1
	if [ -n "${TERNAL_RELAY_URL:-}" ] && [ "$state" != relay-only ]; then
		echo "production ingress mode supports only the relay-only data-path proof" >&2
		exit 1
	fi
	netns=$(read_state netns)
	host_ip=$(read_state host-ip)
	relay_port=$(read_state relay-port)
	ip netns exec "$netns" iptables -F OUTPUT
	ip netns exec "$netns" iptables -A OUTPUT -o lo -j ACCEPT
	case "$state" in
		relay-only|recovery)
			ip netns exec "$netns" iptables -A OUTPUT -p udp -j REJECT
			;;
		direct-only)
			ip netns exec "$netns" iptables -A OUTPUT -p tcp -d "$host_ip" --dport "$relay_port" -j REJECT
			;;
		both-blocked)
			ip netns exec "$netns" iptables -A OUTPUT -p udp -j REJECT
			ip netns exec "$netns" iptables -A OUTPUT -p tcp -d "$host_ip" --dport "$relay_port" -j REJECT
			;;
		*)
			echo "unsupported transport state: $state" >&2
			exit 1
			;;
	esac
	verify_firewall_state "$state"
	write_state applied-state "$state"
}

verify_firewall_state() {
	state=$1
	netns=$(read_state netns)
	relay_port=$(read_state relay-port)
	rules=$(ip netns exec "$netns" iptables-save -t filter)
	udp_blocked=false
	tcp_blocked=false
	printf '%s\n' "$rules" | grep -- '-A OUTPUT -p udp -j REJECT' >/dev/null 2>&1 && udp_blocked=true
	printf '%s\n' "$rules" | grep -- "--dport $relay_port -j REJECT" >/dev/null 2>&1 && tcp_blocked=true
	case "$state" in
		relay-only|recovery) [ "$udp_blocked:$tcp_blocked" = true:false ] ;;
		direct-only) [ "$udp_blocked:$tcp_blocked" = false:true ] ;;
		both-blocked) [ "$udp_blocked:$tcp_blocked" = true:true ] ;;
		*) return 1 ;;
	esac
}

run_proxy() {
	state=$1
	endpoint=$(read_state endpoint-id)
	relay_url=$(read_state relay-url)
	direct_address=$(read_state direct-address)
	netns=$(read_state netns)
	out="$work/$state.proxy.out"
	err="$work/$state.proxy.err"
	timeout_bin=$(command -v timeout)

	set -- "$bin" fly --stdio "$endpoint" --relay-url "$relay_url" --direct-address "$direct_address"
	if [ -n "${TERNAL_TRANSPORT_EXTRA_DIRECT_ADDRESSES:-}" ]; then
		printf '%s\n' "$TERNAL_TRANSPORT_EXTRA_DIRECT_ADDRESSES" >"$work/extra-direct-addresses"
		while IFS= read -r address; do
			[ -n "$address" ] && set -- "$@" --direct-address "$address"
		done <"$work/extra-direct-addresses"
	fi
	proxy_status=0
	ip netns exec "$netns" env \
		HOME="$work/client-home" \
		PIGEONS_TRANSPORT_DIAGNOSTICS=stderr \
		"$timeout_bin" "${TERNAL_TRANSPORT_PROBE_TIMEOUT:-15}" "$@" \
		</dev/null >"$out" 2>"$err" || proxy_status=$?
	printf '%s\n' "$proxy_status" >"$work/$state.proxy.status"
}

probe() {
	state=$1
	if [ "$(read_state applied-state)" != "$state" ] || ! verify_firewall_state "$state"; then
		echo "$state: applied iptables state does not match requested state" >&2
		exit 1
	fi
	run_proxy "$state"
	endpoint=$(read_state endpoint-id)
	out="$work/$state.proxy.out"
	err="$work/$state.proxy.err"
	if head -n 1 "$out" | grep '^SSH-' >/dev/null 2>&1; then
		connected=true
		ssh_banner=true
		path=$(sh "$parser" "$err")
	else
		connected=false
		ssh_banner=false
		path=none
	fi
	diagnostics_jsonl=false
	jq -Rsc '[split("\n")[] | fromjson? | select(.schema == "pigeons.transport.v1" and .event == "transport_changed" and (.transport == "direct" or .transport == "relay" or .transport == "unknown"))] | length > 0' "$err" | grep '^true$' >/dev/null 2>&1 && diagnostics_jsonl=true
	jq -nc \
		--argjson connected "$connected" \
		--argjson ssh_banner "$ssh_banner" \
		--argjson diagnostics_jsonl "$diagnostics_jsonl" \
		--arg path "$path" \
		--arg endpoint_id "$endpoint" \
		'{connected:$connected,path:$path,endpoint_id:$endpoint_id,network_state_verified:true,ssh_banner:$ssh_banner,diagnostics_jsonl:$diagnostics_jsonl}'
}

case "${1:-}" in
	capabilities) capabilities ;;
	prepare) prepare ;;
	endpoint-id) read_state endpoint-id ;;
	apply) apply_state "${2:-}" ;;
	probe) probe "${2:-}" ;;
	cleanup) cleanup ;;
	*)
		echo "usage: $0 capabilities|prepare|endpoint-id|apply STATE|probe STATE|cleanup" >&2
		exit 2
		;;
esac

#!/bin/sh
set -eu

fail() {
	echo "Rauthy device grant preflight failed: $1" >&2
	exit 2
}

print_safe_oauth_error() {
	error=$(jq -r '
		if (.error | type == "string")
			and (.error | test("^[a-z][a-z0-9_]{0,63}$"))
		then .error else empty end
	' "$1" 2>/dev/null || true)
	[ -z "$error" ] || printf 'oauth_error=%s\n' "$error" >&2
}

is_local_http_url() {
	local_rest=${1#http://}
	local_authority=${local_rest%%/*}
	case $local_authority in
		localhost | localhost:* | *.localhost | *.localhost:* | 127.0.0.1 | 127.0.0.1:* | '[::1]' | '[::1]':*) return 0 ;;
		*) return 1 ;;
	esac
}

validate_discovered_endpoint() {
	endpoint_label=$1
	endpoint_url=$2
	case $endpoint_url in
		https://?*) ;;
		http://?*)
			is_local_http_url "$endpoint_url" ||
				fail "$endpoint_label may use http only for a loopback or *.localhost URL"
			;;
		*) fail "$endpoint_label must use http or https" ;;
	esac
	if [ "$issuer_scheme" = https ]; then
		case $endpoint_url in
			https://?*) ;;
			*) fail "$endpoint_label must use https for an https issuer" ;;
		esac
	fi
}

for command in curl jq python3 sleep; do
	command -v "$command" >/dev/null 2>&1 || fail "missing required command: $command"
done

issuer=${RAUTHY_ISSUER:-}
client_id=${RAUTHY_CLIENT_ID:-ternal}
client_secret=${RAUTHY_CLIENT_SECRET:-}

[ -n "$issuer" ] || fail 'RAUTHY_ISSUER is required'
[ -n "$client_secret" ] || fail 'RAUTHY_CLIENT_SECRET is required'
case $issuer in
	https://?*) issuer_scheme=https ;;
	http://?*)
		issuer_scheme=http
		is_local_http_url "$issuer" ||
			fail 'an http RAUTHY_ISSUER must use loopback or a *.localhost host'
		;;
	*) fail 'RAUTHY_ISSUER must use http or https' ;;
esac
issuer_authority=${issuer#*://}
issuer_authority=${issuer_authority%%/*}
case $issuer_authority in *'@'*) fail 'RAUTHY_ISSUER must not contain userinfo' ;; esac
case $client_id in '' | *[!a-zA-Z0-9._~-]*) fail 'RAUTHY_CLIENT_ID contains unsupported form characters' ;; esac
case $client_secret in '' | *[!a-zA-Z0-9]*) fail 'RAUTHY_CLIENT_SECRET must be alphanumeric for Rauthy' ;; esac

umask 077
work=$(mktemp -d)
cleanup() {
	rm -rf "$work"
}
trap cleanup EXIT INT TERM HUP

discovery_url=${issuer%/}/.well-known/openid-configuration
curl -fsS --connect-timeout 5 --max-time 15 "$discovery_url" >"$work/discovery.json" ||
	fail "could not fetch discovery from $discovery_url"

python3 - "$issuer" "$work/discovery.json" <<'PY'
import json
import sys
from urllib.parse import urlsplit

expected_issuer = sys.argv[1]
issuer = urlsplit(expected_issuer)
if issuer.username or issuer.password:
    raise SystemExit("RAUTHY_ISSUER must not contain userinfo")
def origin(url):
    port = url.port
    if port is None:
        port = 443 if url.scheme == "https" else 80 if url.scheme == "http" else None
    return (url.scheme, url.hostname, port)
issuer_origin = origin(issuer)
with open(sys.argv[2], encoding="utf-8") as handle:
    discovery = json.load(handle)
if discovery.get("issuer") != expected_issuer:
    raise SystemExit("OIDC discovery issuer does not match RAUTHY_ISSUER")
for key in ("device_authorization_endpoint", "token_endpoint"):
    endpoint = urlsplit(discovery.get(key, ""))
    if endpoint.username or endpoint.password or origin(endpoint) != issuer_origin:
        raise SystemExit(f"OIDC discovery {key} origin does not match RAUTHY_ISSUER")
PY

if ! jq -e '
	(.device_authorization_endpoint | type == "string" and length > 0)
	and (.token_endpoint | type == "string" and length > 0)
	and (.grant_types_supported | index("urn:ietf:params:oauth:grant-type:device_code") != null)
' "$work/discovery.json" >/dev/null; then
	fail 'discovery does not advertise the OAuth device grant and endpoints'
fi

device_endpoint=$(jq -r '.device_authorization_endpoint' "$work/discovery.json")
token_endpoint=$(jq -r '.token_endpoint' "$work/discovery.json")
validate_discovered_endpoint device_authorization_endpoint "$device_endpoint"
validate_discovered_endpoint token_endpoint "$token_endpoint"

# Keep the confidential client secret out of process arguments and temporary files.
device_form="client_id=$client_id&client_secret=$client_secret&scope=openid%20groups"
device_status=$(printf '%s' "$device_form" | curl -sS --connect-timeout 5 --max-time 15 \
	-o "$work/device.json" -w '%{http_code}' -X POST "$device_endpoint" \
	-H 'content-type: application/x-www-form-urlencoded' --data-binary @-) ||
	fail 'device authorization request failed'
unset device_form

if [ "$device_status" != 200 ] || ! jq -e '
	(.device_code | type == "string" and length > 0)
	and (.user_code | type == "string" and length > 0)
	and (.verification_uri | type == "string" and (startswith("http://") or startswith("https://")))
	and (.expires_in | type == "number" and . > 0)
	and (.interval == null or (.interval | type == "number" and . >= 1 and . <= 60))
' "$work/device.json" >/dev/null; then
	echo "device endpoint returned HTTP $device_status" >&2
	print_safe_oauth_error "$work/device.json"
	fail 'client is not enabled for a valid device authorization response'
fi

device_code=$(jq -r '.device_code' "$work/device.json")
case $device_code in '' | *[!a-zA-Z0-9._~-]*) fail 'device endpoint returned an unsupported device_code' ;; esac
interval=$(jq -r '.interval // 5' "$work/device.json")

# No browser approves this code. After the advertised interval, a valid grant
# must therefore reach the token endpoint and return authorization_pending.
sleep "$((interval + 1))"
token_form="client_id=$client_id&client_secret=$client_secret&device_code=$device_code&grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code"
token_status=$(printf '%s' "$token_form" | curl -sS --connect-timeout 5 --max-time 15 \
	-o "$work/token.json" -w '%{http_code}' -X POST "$token_endpoint" \
	-H 'content-type: application/x-www-form-urlencoded' --data-binary @-) ||
	fail 'device token polling request failed'
unset token_form device_code client_secret

if [ "$token_status" != 400 ] || ! jq -e '.error == "authorization_pending"' "$work/token.json" >/dev/null; then
	echo "token endpoint returned HTTP $token_status" >&2
	print_safe_oauth_error "$work/token.json"
	fail 'device grant did not return authorization_pending before browser approval'
fi

printf 'Rauthy device grant preflight passed\n'
printf 'issuer=%s\n' "$issuer"
printf 'client_id=%s\n' "$client_id"
printf 'device_start=ok token_poll=authorization_pending\n'

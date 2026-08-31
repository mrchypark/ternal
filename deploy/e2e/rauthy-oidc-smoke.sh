#!/bin/sh
set -eu

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 127
	fi
}

need curl
need grep
need jq
need python3
need sed

api_url=$(printf '%s' "${TERNAL_API_URL:-http://127.0.0.1:3000}" | sed 's#/$##')
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM HUP

config=$(curl -fsS "$api_url/auth/oidc-config")
issuer=$(printf '%s' "$config" | jq -r '.issuer')
client_id=$(printf '%s' "$config" | jq -r '.client_id')
redirect_url=$(printf '%s' "$config" | jq -r '.redirect_url')
dev_headers=$(printf '%s' "$config" | jq -r '.dev_headers')
issuer_base=$(printf '%s' "$issuer" | sed 's#/*$##')
issuer_authority=${issuer#*://}
issuer_authority=${issuer_authority%%/*}
case $issuer_authority in
	*'@'*)
		echo 'configured OIDC issuer must not contain userinfo' >&2
		exit 1
		;;
esac

if [ "$dev_headers" = "true" ] && [ "${TERNAL_E2E_ALLOW_DEV_HEADERS:-0}" != "1" ]; then
	echo "Ternal dev-header auth is enabled; restart without TERNAL_DEV_HEADERS for a Rauthy OIDC smoke" >&2
	exit 1
fi

curl -fsS "$issuer_base/.well-known/openid-configuration" >"$work/discovery.json"
jq -e --arg issuer "$issuer" '.issuer == $issuer' "$work/discovery.json" >/dev/null
authorization_endpoint=$(jq -r '.authorization_endpoint // empty' "$work/discovery.json")
jq -e '.token_endpoint and .jwks_uri' "$work/discovery.json" >/dev/null
python3 - "$issuer" "$work/discovery.json" <<'PY'
import json
import sys
from urllib.parse import urlsplit

issuer = urlsplit(sys.argv[1])
if issuer.username or issuer.password:
    raise SystemExit("configured OIDC issuer must not contain userinfo")
def origin(url):
    port = url.port
    if port is None:
        port = 443 if url.scheme == "https" else 80 if url.scheme == "http" else None
    return (url.scheme, url.hostname, port)
issuer_origin = origin(issuer)
with open(sys.argv[2], encoding="utf-8") as handle:
    discovery = json.load(handle)
for key in (
    "authorization_endpoint",
    "token_endpoint",
    "jwks_uri",
    "device_authorization_endpoint",
):
    value = discovery.get(key)
    if value is None:
        continue
    endpoint = urlsplit(value)
    if endpoint.username or endpoint.password or origin(endpoint) != issuer_origin:
        raise SystemExit(f"OIDC discovery {key} origin does not match configured issuer")
PY

if [ -z "$authorization_endpoint" ]; then
	echo "OIDC discovery is missing authorization_endpoint" >&2
	exit 1
fi

status=$(curl -sS -o /dev/null -D "$work/login.headers" -w '%{http_code}' "$api_url/auth/login")
if [ "$status" != "302" ]; then
	echo "expected /auth/login to return 302, got $status" >&2
	exit 1
fi

location=$(sed -n 's/^[Ll]ocation:[[:space:]]*//p' "$work/login.headers" | tr -d '\r' | tail -n 1)
case "$location" in
	"$authorization_endpoint"*) ;;
	*)
		echo "/auth/login did not redirect to Rauthy authorization_endpoint" >&2
		echo "expected prefix: $authorization_endpoint" >&2
		echo "actual:          $location" >&2
		exit 1
		;;
esac

encoded_client=$(jq -nr --arg value "$client_id" '$value|@uri')
encoded_redirect=$(jq -nr --arg value "$redirect_url" '$value|@uri')

has_query() {
	case "$location" in
		*"?$1&"* | *"?$1" | *"&$1&"* | *"&$1") return 0 ;;
		*) return 1 ;;
	esac
}

has_query 'response_type=code'
has_query "client_id=$encoded_client"
has_query "redirect_uri=$encoded_redirect"
if ! has_query 'scope=openid+groups' && ! has_query 'scope=openid%20groups'; then
	echo "/auth/login redirect is missing openid groups scope" >&2
	exit 1
fi
printf '%s' "$location" | grep -Eq '[?&]state=[^&]+'
printf '%s' "$location" | grep -Eq '[?&]nonce=[^&]+'
if printf '%s' "$location" | grep -qi 'client_secret'; then
	echo "/auth/login leaked client_secret in redirect URL" >&2
	exit 1
fi
grep -i '^set-cookie:[[:space:]]*ternal_oidc_state=' "$work/login.headers" >/dev/null

printf 'rauthy oidc smoke passed\n'
printf 'issuer=%s\n' "$issuer"
printf 'client_id=%s\n' "$client_id"
printf 'redirect_url=%s\n' "$redirect_url"

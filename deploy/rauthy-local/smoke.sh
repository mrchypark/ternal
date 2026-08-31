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
need sed

if docker compose version >/dev/null 2>&1; then
	compose() { docker compose "$@"; }
elif command -v docker-compose >/dev/null 2>&1; then
	compose() { docker-compose "$@"; }
else
	echo "Docker Compose is required" >&2
	exit 127
fi

here=$(cd "$(dirname "$0")" && pwd)
compose_file=$here/compose.yaml
project=${TERNAL_OIDC_SMOKE_PROJECT:-ternal-oidc-smoke}
api_url=http://127.0.0.1:3000
issuer=http://rauthy.localhost:18080/auth/v1/
issuer_base=${issuer%/}
work=$(mktemp -d)
ternal_pid=

cleanup() {
	if [ -n "$ternal_pid" ]; then
		kill "$ternal_pid" >/dev/null 2>&1 || true
		wait "$ternal_pid" >/dev/null 2>&1 || true
	fi
	compose -p "$project" -f "$compose_file" down -v --remove-orphans >/dev/null 2>&1 || true
	rm -rf "$work"
}
on_signal() {
	trap - EXIT
	cleanup
	exit 130
}
trap cleanup EXIT
trap on_signal INT TERM HUP

compose -f "$compose_file" config >/dev/null

jq -e '
	.[0].id == "ternal"
	and (.[0].redirect_uris | index("http://127.0.0.1:3000/auth/callback") != null)
	and (.[0].flows_enabled | index("authorization_code") != null)
	and (.[0].flows_enabled | index("urn:ietf:params:oauth:grant-type:device_code") != null)
	and (.[0].scopes | index("openid") != null)
	and (.[0].scopes | index("groups") != null)
' "$here/bootstrap/clients.json" >/dev/null
jq -e 'map(.name) | index("ternal-admins") != null and index("platform") != null' \
	"$here/bootstrap/groups.json" >/dev/null
jq -e '
	any(.[]; .email == "ternal-admin@example.com" and (.groups | index("ternal-admins") != null))
	and any(.[]; .email == "alice@example.com" and (.groups | index("ternal-admins") == null))
' "$here/bootstrap/users.json" >/dev/null
jq -e '
	.client_id == "ternal"
	and .light.action == [147, 100, 22]
	and .dark.action == [138, 40, 51]
	and .border_radius == "6px"
' "$here/branding/theme.json" >/dev/null

compose -p "$project" -f "$compose_file" down -v --remove-orphans >/dev/null 2>&1 || true
compose -p "$project" -f "$compose_file" up -d rauthy

wait_for_json() {
	url=$1
	output=$2
	label=$3
	count=0
	while [ "$count" -lt 120 ]; do
		if curl -fsS --connect-timeout 2 --max-time 5 "$url" >"$output" 2>/dev/null \
			&& jq -e . "$output" >/dev/null 2>&1; then
			return 0
		fi
		if [ -n "$ternal_pid" ] && ! kill -0 "$ternal_pid" >/dev/null 2>&1; then
			echo "Ternal exited while waiting for $label" >&2
			cat "$work/ternal.log" >&2
			return 1
		fi
		count=$((count + 1))
		sleep 1
	done
	echo "timed out waiting for $label at $url" >&2
	compose -p "$project" -f "$compose_file" ps >&2 || true
	compose -p "$project" -f "$compose_file" logs --no-color >&2 || true
	return 1
}

wait_for_json "$issuer_base/.well-known/openid-configuration" "$work/discovery.json" "Rauthy discovery"

# smoke.sh starts only Rauthy above, so run the same one-shot branding service
# used by a manual Compose startup and wait for its successful exit explicitly.
compose -p "$project" -f "$compose_file" up --no-deps --force-recreate \
	--abort-on-container-exit --exit-code-from rauthy-branding rauthy-branding

curl -fsS --connect-timeout 2 --max-time 10 \
	"$issuer_base/theme/ternal/0" >"$work/theme.css"
grep -E -- '--action:[[:space:]]*147 100 22;' "$work/theme.css" >/dev/null
grep -E -- '--border-radius:[[:space:]]*6px;' "$work/theme.css" >/dev/null
curl -fsS --connect-timeout 2 --max-time 10 -D "$work/logo.headers" \
	-o "$work/logo.png" "$issuer_base/clients/ternal/logo"
# Rauthy normalizes uploaded PNG/JPEG client logos to WebP.
grep -i '^content-type:[[:space:]]*image/webp' "$work/logo.headers" >/dev/null
test -s "$work/logo.png"

TERNAL_OIDC_ISSUER=$issuer \
TERNAL_OIDC_CLIENT_ID=ternal \
TERNAL_OIDC_CLIENT_SECRET=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
	sh "$here/../e2e/rauthy-device-grant-preflight.sh"

repo=$(cd "$here/../.." && pwd)
ternal_bin=$work/ternal-api
if [ ! -x "$ternal_bin" ]; then
	need go
	(cd "$repo" && go build -trimpath -o "$ternal_bin" ./cmd/ternal-api)
fi

env \
	TERNAL_BIND=127.0.0.1:3000 \
	TERNAL_DATA_DIR="$work/data" \
	TERNAL_DATA_ADMIN_TOKEN=ternal-local-data-admin-token-minimum-32-characters \
	TERNAL_REQUIRE_DATA_ADMIN_TOKEN=1 \
	TERNAL_RELAY_ACCESS_TOKEN=ternal-local-relay-access-token-minimum-32-characters \
	TERNAL_OIDC_ISSUER="$issuer" \
	TERNAL_OIDC_CLIENT_ID=ternal \
	TERNAL_OIDC_CLIENT_SECRET=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
	TERNAL_OIDC_REDIRECT_URL=http://127.0.0.1:3000/auth/callback \
	TERNAL_OIDC_ADMIN_GROUP=ternal-admins \
	TERNAL_OIDC_GROUPS_CLAIM=groups \
	TERNAL_SESSION_KEY=ternal-local-session-key-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
	TERNAL_SESSION_TTL_SECONDS=60 \
	TERNAL_DEV_HEADERS=0 \
	"$ternal_bin" >"$work/ternal.log" 2>&1 &
ternal_pid=$!

wait_for_json "$api_url/auth/oidc-config" "$work/oidc-config.json" "Ternal OIDC config"

ternal_device_start_status=$(curl -sS --connect-timeout 2 --max-time 30 \
	-o "$work/ternal-device-start.json" -w '%{http_code}' -X POST \
	"$api_url/auth/device/start")
if [ "$ternal_device_start_status" != 200 ] || ! jq -e '
	(.device_code | type == "string" and length > 0)
	and (.user_code | type == "string" and length > 0)
	and (.verification_uri | type == "string" and length > 0)
	and (.expires_in | type == "number" and . > 0)
	and (.interval == null or (.interval | type == "number" and . >= 1 and . <= 60))
' "$work/ternal-device-start.json" >/dev/null; then
	echo "Ternal device start returned an invalid HTTP $ternal_device_start_status response" >&2
	exit 1
fi
ternal_device_interval=$(jq -r '.interval // 5' "$work/ternal-device-start.json")
sleep "$((ternal_device_interval + 1))"
ternal_device_token_status=$(jq -c '{device_code:.device_code}' \
	"$work/ternal-device-start.json" | curl -sS --connect-timeout 2 --max-time 30 \
	-o "$work/ternal-device-token.json" -w '%{http_code}' -X POST \
	-H 'content-type: application/json' --data-binary @- \
	"$api_url/auth/device/token")
if [ "$ternal_device_token_status" != 202 ] ||
	! jq -e '.status == "authorization_pending"' "$work/ternal-device-token.json" >/dev/null; then
	echo "Ternal device token poll returned unexpected HTTP $ternal_device_token_status" >&2
	jq -r '
		.error
		| select(. == "OIDC device token validation setup failed"
			or . == "OIDC device token exchange failed")
	' "$work/ternal-device-token.json" >&2 2>/dev/null || true
	exit 1
fi

jq -e --arg issuer "$issuer" '
	.issuer == $issuer
	and (.authorization_endpoint | startswith("http://rauthy.localhost:18080/"))
	and (.token_endpoint | startswith("http://rauthy.localhost:18080/"))
	and (.jwks_uri | startswith("http://rauthy.localhost:18080/"))
' "$work/discovery.json" >/dev/null

if ! jq -e --arg issuer "$issuer" '
	.issuer == $issuer
	and .client_id == "ternal"
	and .redirect_url == "http://127.0.0.1:3000/auth/callback"
	and (.session_ttl_seconds > 0 and .session_ttl_seconds <= 3600)
	and .dev_headers == false
' "$work/oidc-config.json" >/dev/null; then
	echo "unexpected Ternal OIDC configuration:" >&2
	cat "$work/oidc-config.json" >&2
	exit 1
fi

curl -fsS --connect-timeout 2 --max-time 10 "$api_url/auth/session" >"$work/session.json"
jq -e '
	.authenticated == false
	and .mode == "anonymous"
	and .dev_headers == false
	and .session_cookie == "ternal_session"
' "$work/session.json" >/dev/null

status=$(curl -sS --connect-timeout 2 --max-time 10 -o /dev/null \
	-D "$work/login.headers" -w '%{http_code}' "$api_url/auth/login")
if [ "$status" != "302" ]; then
	echo "expected Ternal /auth/login HTTP 302, got $status" >&2
	exit 1
fi

location=$(sed -n 's/^[Ll]ocation:[[:space:]]*//p' "$work/login.headers" | tr -d '\r' | tail -n 1)
authorization_endpoint=$(jq -r '.authorization_endpoint' "$work/discovery.json")
case "$location" in
	"$authorization_endpoint"*) ;;
	*) echo "Ternal login did not redirect to Rauthy authorization endpoint: $location" >&2; exit 1 ;;
esac

printf '%s' "$location" | grep -Eq '[?&]response_type=code(&|$)'
printf '%s' "$location" | grep -Eq '[?&]client_id=ternal(&|$)'
printf '%s' "$location" | grep -Eq '[?&]state=[^&]+'
printf '%s' "$location" | grep -Eq '[?&]nonce=[^&]+'
if printf '%s' "$location" | grep -qi 'client_secret'; then
	echo "Ternal leaked the OIDC client secret in the authorization URL" >&2
	exit 1
fi
grep -i '^set-cookie:[[:space:]]*ternal_oidc_state=' "$work/login.headers" >/dev/null

auth_status=$(curl -sS --connect-timeout 2 --max-time 10 \
	-o "$work/authorize.html" -w '%{http_code}' "$location")
case "$auth_status" in
	200 | 302) ;;
	*) echo "Rauthy rejected the bootstrapped client authorization request with HTTP $auth_status" >&2; exit 1 ;;
esac
if grep -Eqi 'invalid[_ -]?client|redirect[_ -]?uri.*invalid' "$work/authorize.html"; then
	echo "Rauthy authorization response reports an invalid client or redirect URI" >&2
	exit 1
fi

printf 'local Rauthy OIDC and device-grant smoke passed\n'
printf 'image=%s\n' 'ghcr.io/sebadob/rauthy:0.35.2 (digest pinned in compose.yaml)'
printf 'issuer=%s\n' "$issuer"
printf 'ternal_device_flow=start-ok,poll-authorization_pending\n'
printf 'ternal_auth_mode=anonymous (dev headers disabled; signed-session callback prerequisites valid)\n'

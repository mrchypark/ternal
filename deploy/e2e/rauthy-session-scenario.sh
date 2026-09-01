#!/bin/sh
set -eu

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 127
	fi
}

need curl
need jq
need sed

api_url=$(printf '%s' "${TERNAL_API_URL:-http://127.0.0.1:3000}" | sed 's#/$##')
umask 077
work=$(mktemp -d)
trap 'find "$work" -depth -delete' EXIT INT TERM HUP
mkdir "$work/state"
export TERNAL_E2E_STATE_DIR="$work/state"

require_env() {
	name=$1
	eval "value=\${$name:-}"
	if [ -z "$value" ]; then
		echo "$name is required" >&2
		exit 1
	fi
}

write_headers() {
	path=$1
	cookie=$2
	csrf=${3:-}
	case $cookie$csrf in *[[:space:]]*) echo 'session header value contains whitespace' >&2; exit 1 ;; esac
	case $cookie in *=*) ;; *) cookie="ternal_session=$cookie" ;; esac
	printf 'cookie: %s\n' "$cookie" >"$path"
	[ -z "$csrf" ] || printf 'X-CSRF-Token: %s\n' "$csrf" >>"$path"
}

session_json() {
	curl -fsS "$api_url/auth/session" -H "@$1"
}

require_env TERNAL_ADMIN_SESSION_COOKIE
require_env TERNAL_ADMIN_CSRF_TOKEN
require_env TERNAL_USER_SESSION_COOKIE
require_env TERNAL_USER_CSRF_TOKEN
require_env TERNAL_OTHER_USER_SESSION_COOKIE
require_env TERNAL_OTHER_USER_CSRF_TOKEN

write_headers "$work/admin.headers" "$TERNAL_ADMIN_SESSION_COOKIE" "$TERNAL_ADMIN_CSRF_TOKEN"
write_headers "$work/user.headers" "$TERNAL_USER_SESSION_COOKIE" "$TERNAL_USER_CSRF_TOKEN"
write_headers "$work/other.headers" "$TERNAL_OTHER_USER_SESSION_COOKIE" "$TERNAL_OTHER_USER_CSRF_TOKEN"
admin_session=$(session_json "$work/admin.headers")
user_session=$(session_json "$work/user.headers")
other_user_session=$(session_json "$work/other.headers")
user_subject=${TERNAL_E2E_USER_SUBJECT:-$(printf '%s' "$user_session" | jq -r '.user.sub // empty')}
user_group=${TERNAL_E2E_USER_GROUP:-$(printf '%s' "$user_session" | jq -r '.user.groups[0] // empty')}
other_user_subject=$(printf '%s' "$other_user_session" | jq -r '.user.sub // empty')
other_user_group=${TERNAL_E2E_OTHER_USER_GROUP:-$(printf '%s' "$other_user_session" | jq -r '.user.groups[0] // empty')}

if [ -z "$user_subject" ]; then
	echo "user session subject is required" >&2
	exit 1
fi
if [ -z "$user_group" ]; then
	echo "TERNAL_E2E_USER_GROUP is required when the user session has no groups" >&2
	exit 1
fi
[ -n "$other_user_subject" ] && [ "$other_user_subject" != "$user_subject" ] || {
	echo 'other user session must have a distinct subject' >&2
	exit 1
}
[ -n "$other_user_group" ] || { echo 'other user session must have at least one group' >&2; exit 1; }
export TERNAL_E2E_USER_SUBJECT="$user_subject"
export TERNAL_E2E_USER_GROUP="$user_group"
export TERNAL_E2E_OTHER_USER_GROUP="$other_user_group"

printf '%s' "$admin_session" | jq -e '
	.authenticated == true and .is_admin == true
	and (.user.sub | type == "string" and length > 0)
' >/dev/null
printf '%s' "$admin_session" | jq -e --arg csrf "$TERNAL_ADMIN_CSRF_TOKEN" '.csrf_token == $csrf' >/dev/null

printf '%s' "$user_session" | jq -e --arg group "$TERNAL_E2E_USER_GROUP" '
	.authenticated == true and .is_admin == false
	and (.user.groups // [] | index($group) != null)
' >/dev/null
printf '%s' "$user_session" | jq -e --arg csrf "$TERNAL_USER_CSRF_TOKEN" '.csrf_token == $csrf' >/dev/null
printf '%s' "$other_user_session" | jq -e --arg csrf "$TERNAL_OTHER_USER_CSRF_TOKEN" '
	.authenticated == true and .is_admin == false and .csrf_token == $csrf
' >/dev/null

curl -fsS "$api_url/auth/oidc-config" | jq -e '.dev_headers == false' >/dev/null

if [ -n "${TERNAL_E2E_ADMIN_SUBJECT:-}" ]; then
	printf '%s' "$admin_session" | jq -e --arg subject "$TERNAL_E2E_ADMIN_SUBJECT" '.user.sub == $subject' >/dev/null
fi
if [ -n "${TERNAL_E2E_USER_SUBJECT:-}" ]; then
	printf '%s' "$user_session" | jq -e --arg subject "$TERNAL_E2E_USER_SUBJECT" '.user.sub == $subject' >/dev/null
fi

sh deploy/e2e/local-portal-smoke.sh
sh deploy/e2e/local-user-scenario.sh
sh deploy/e2e/local-cli-scenario.sh
sh deploy/e2e/local-agent-scenario.sh

if [ "${TERNAL_E2E_TRANSPORT_SMOKE:-0}" = "1" ]; then
	TERNAL_API_URL="$api_url" \
	TERNAL_SESSION_COOKIE="$TERNAL_ADMIN_SESSION_COOKIE" \
	TERNAL_CSRF_TOKEN="$TERNAL_ADMIN_CSRF_TOKEN" \
	sh deploy/pigeons-smoke/local-relay-smoke.sh
fi

if [ -s "$TERNAL_E2E_STATE_DIR/user.json" ]; then
	key_id=$(jq -er .key_id "$TERNAL_E2E_STATE_DIR/user.json")
	status=$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE "$api_url/ssh-keys/$key_id" -H "@$work/user.headers")
	[ "$status" = 200 ] || { echo "E2E user key cleanup returned HTTP $status" >&2; exit 1; }
fi

logout_and_reject_replay() {
	label=$1
	headers=$2
	status=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$api_url/auth/logout" -H "@$headers")
	[ "$status" = 200 ] || { echo "$label logout returned HTTP $status" >&2; exit 1; }
	curl -fsS "$api_url/auth/session" -H "@$headers" | jq -e '.authenticated == false' >/dev/null
}
logout_and_reject_replay other-user "$work/other.headers"
logout_and_reject_replay user "$work/user.headers"
logout_and_reject_replay admin "$work/admin.headers"

printf 'rauthy session scenario passed\n'
printf 'admin_subject=%s\n' "$(printf '%s' "$admin_session" | jq -r '.user.sub')"
printf 'user_subject=%s\n' "$(printf '%s' "$user_session" | jq -r '.user.sub')"
printf 'user_group=%s\n' "$TERNAL_E2E_USER_GROUP"

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

require_env() {
	name=$1
	eval "value=\${$name:-}"
	if [ -z "$value" ]; then
		echo "$name is required" >&2
		exit 1
	fi
}

cookie_header() {
	case "$1" in
		*=*) printf '%s' "$1" ;;
		*) printf 'ternal_session=%s' "$1" ;;
	esac
}

session_json() {
	curl -fsS "$api_url/auth/session" -H "cookie: $(cookie_header "$1")"
}

require_env TERNAL_ADMIN_SESSION_COOKIE
require_env TERNAL_ADMIN_CSRF_TOKEN
require_env TERNAL_USER_SESSION_COOKIE
require_env TERNAL_USER_CSRF_TOKEN

admin_session=$(session_json "$TERNAL_ADMIN_SESSION_COOKIE")
user_session=$(session_json "$TERNAL_USER_SESSION_COOKIE")
user_subject=${TERNAL_E2E_USER_SUBJECT:-$(printf '%s' "$user_session" | jq -r '.subject // empty')}
user_group=${TERNAL_E2E_USER_GROUP:-$(printf '%s' "$user_session" | jq -r '.groups[0] // empty')}

if [ -z "$user_subject" ]; then
	echo "user session subject is required" >&2
	exit 1
fi
if [ -z "$user_group" ]; then
	echo "TERNAL_E2E_USER_GROUP is required when the user session has no groups" >&2
	exit 1
fi
export TERNAL_E2E_USER_SUBJECT=$user_subject
export TERNAL_E2E_USER_GROUP=$user_group

printf '%s' "$admin_session" | jq -e '
	.authenticated == true and .mode == "session" and .admin == true and .dev_headers == false
' >/dev/null
printf '%s' "$admin_session" | jq -e --arg csrf "$TERNAL_ADMIN_CSRF_TOKEN" '.csrf_token == $csrf' >/dev/null

printf '%s' "$user_session" | jq -e --arg group "$TERNAL_E2E_USER_GROUP" '
	.authenticated == true and .mode == "session" and .admin == false and .dev_headers == false
	and (.groups // [] | index($group) != null)
' >/dev/null
printf '%s' "$user_session" | jq -e --arg csrf "$TERNAL_USER_CSRF_TOKEN" '.csrf_token == $csrf' >/dev/null

if [ -n "${TERNAL_E2E_ADMIN_SUBJECT:-}" ]; then
	printf '%s' "$admin_session" | jq -e --arg subject "$TERNAL_E2E_ADMIN_SUBJECT" '.subject == $subject' >/dev/null
fi
if [ -n "${TERNAL_E2E_USER_SUBJECT:-}" ]; then
	printf '%s' "$user_session" | jq -e --arg subject "$TERNAL_E2E_USER_SUBJECT" '.subject == $subject' >/dev/null
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

printf 'rauthy session scenario passed\n'
printf 'admin_subject=%s\n' "$(printf '%s' "$admin_session" | jq -r '.subject')"
printf 'user_subject=%s\n' "$(printf '%s' "$user_session" | jq -r '.subject')"
printf 'user_group=%s\n' "$TERNAL_E2E_USER_GROUP"

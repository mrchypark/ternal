#!/bin/sh
set -eu

for command in curl jq ssh-keygen; do
	command -v "$command" >/dev/null 2>&1 || {
		echo "missing required command: $command" >&2
		exit 127
	}
done

api_url=${TERNAL_API_URL%/}
state=${TERNAL_E2E_STATE_DIR:-}
[ -n "$state" ] || { echo 'TERNAL_E2E_STATE_DIR is required' >&2; exit 1; }
[ -d "$state" ] || { echo 'TERNAL_E2E_STATE_DIR must exist' >&2; exit 1; }

require_env() {
	eval "value=\${$1:-}"
	[ -n "$value" ] || { echo "$1 is required" >&2; exit 1; }
	case $value in *[[:space:]]*) echo "$1 contains whitespace" >&2; exit 1 ;; esac
}

write_headers() {
	path=$1
	cookie=$2
	csrf=${3:-}
	case $cookie in *=*) ;; *) cookie="ternal_session=$cookie" ;; esac
	printf 'cookie: %s\n' "$cookie" >"$path"
	[ -z "$csrf" ] || printf 'X-CSRF-Token: %s\n' "$csrf" >>"$path"
}

require_env TERNAL_USER_SESSION_COOKIE
require_env TERNAL_USER_CSRF_TOKEN
require_env TERNAL_OTHER_USER_SESSION_COOKIE

umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM HUP
write_headers "$work/user.headers" "$TERNAL_USER_SESSION_COOKIE" "$TERNAL_USER_CSRF_TOKEN"
write_headers "$work/other.headers" "$TERNAL_OTHER_USER_SESSION_COOKIE"

key_path="$state/user-key"
ssh-keygen -q -t ed25519 -N '' -C 'ternal-e2e-user' -f "$key_path"
public_key=$(cat "$key_path.pub")
request=$(jq -nc --arg public_key "$public_key" '{public_key:$public_key}')
status=$(printf '%s' "$request" | curl -sS -o "$work/created.json" -w '%{http_code}' \
	-X POST "$api_url/ssh-keys/" -H "@$work/user.headers" -H 'Content-Type: application/json' --data-binary @-)
[ "$status" = 201 ] || { echo "SSH key creation returned HTTP $status" >&2; exit 1; }

key_id=$(jq -er '.id' "$work/created.json")
jq -e --arg public_key "$public_key" '
	.public_key == $public_key and (.fingerprint | startswith("SHA256:")) and (.user_id | length > 0)
' "$work/created.json" >/dev/null

status=$(printf '%s' '{"public_key":"not an ssh key"}' | curl -sS -o /dev/null -w '%{http_code}' \
	-X POST "$api_url/ssh-keys/" -H "@$work/user.headers" -H 'Content-Type: application/json' --data-binary @-)
[ "$status" = 400 ] || { echo "invalid SSH key returned HTTP $status" >&2; exit 1; }

curl -fsS "$api_url/ssh-keys/" -H "@$work/user.headers" | jq -e --arg id "$key_id" 'any(.[]; .id == $id)' >/dev/null
curl -fsS "$api_url/ssh-keys/" -H "@$work/other.headers" | jq -e --arg id "$key_id" 'all(.[]; .id != $id)' >/dev/null
status=$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE "$api_url/ssh-keys/$key_id" -H "@$work/other.headers")
[ "$status" = 404 ] || { echo "other user key deletion returned HTTP $status" >&2; exit 1; }

jq -n --arg key_id "$key_id" --arg public_key "$public_key" \
	'{key_id:$key_id,public_key:$public_key}' >"$state/user.json"

printf 'SSH key canonicalization, invalid-key rejection, owner listing, and cross-user isolation passed\n'

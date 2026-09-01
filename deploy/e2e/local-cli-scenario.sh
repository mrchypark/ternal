#!/bin/sh
set -eu

for command in curl jq ssh-keygen; do
	command -v "$command" >/dev/null 2>&1 || {
		echo "missing required command: $command" >&2
		exit 127
	}
done

api_url=${TERNAL_API_URL%/}
cli=${TERNAL_CLI_BIN:-}
state=${TERNAL_E2E_STATE_DIR:-}
[ -n "$cli" ] && [ -x "$cli" ] || { echo 'TERNAL_CLI_BIN must name the verified public ternalctl binary' >&2; exit 1; }
[ -n "$state" ] && [ -d "$state" ] || { echo 'TERNAL_E2E_STATE_DIR must exist' >&2; exit 1; }

umask 077
config="$state/cli-config"
mkdir -p "$config"
session="$config/ternal/session.json"
key="$state/cli-key"

run_cli() (
	unset TERNAL_SESSION_COOKIE TERNAL_CSRF_TOKEN TERNAL_DEV_HEADERS TERNAL_USER TERNAL_GROUPS TERNAL_CLAIMS
	XDG_CONFIG_HOME="$config" TERNAL_API_URL="$api_url" "$cli" "$@"
)

[ ! -e "$session" ] || { echo 'CLI session path was not greenfield' >&2; exit 1; }
run_cli login
[ -f "$session" ] || { echo 'ternalctl login did not create a local session' >&2; exit 1; }

jq -e '
	(keys | sort) == ["cookie","csrf_token","expires_at"]
	and (.cookie | type == "string" and length > 0)
	and (.csrf_token | type == "string" and length > 0)
	and (.expires_at | type == "number" and . > now)
	and ([paths(scalars) as $p | $p | map(tostring) | join("_") | ascii_downcase
	      | select(test("(^|_)(access|refresh|id|device)_?token($|_)"))] | length == 0)
' "$session" >/dev/null

mode=$(stat -f '%Lp' "$session" 2>/dev/null || stat -c '%a' "$session")
[ "$mode" = 600 ] || { echo "CLI session mode is $mode, want 600" >&2; exit 1; }
run_cli whoami | grep -q '^User: '
if run_cli hosts | grep -Eq '(^|[[:space:]])[[:xdigit:]]{64}($|[[:space:]])'; then
	echo 'ternalctl hosts exposed a transport endpoint ID' >&2
	exit 1
fi

ssh-keygen -q -t ed25519 -N '' -C 'ternal-e2e-cli' -f "$key"
run_cli submit-key "$key.pub" | grep -q 'Key submitted successfully'

cookie=$(jq -er .cookie "$session")
csrf=$(jq -er .csrf_token "$session")
case $cookie$csrf in *[!A-Za-z0-9._=-]*) echo 'CLI session contains an unsafe header value' >&2; exit 1 ;; esac
printf 'cookie: ternal_session=%s\nX-CSRF-Token: %s\n' "$cookie" "$csrf" >"$state/cli.headers"
public_key=$(cat "$key.pub")
key_id=$(curl -fsS "$api_url/ssh-keys/" -H "@$state/cli.headers" | jq -er --arg public_key "$public_key" '.[] | select(.public_key == $public_key) | .id')
status=$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE "$api_url/ssh-keys/$key_id" -H "@$state/cli.headers")
[ "$status" = 200 ] || { echo "CLI-created key cleanup returned HTTP $status" >&2; exit 1; }

run_cli logout | grep -q 'Logged out.'
[ ! -e "$session" ] || { echo 'ternalctl logout retained its local session' >&2; exit 1; }
printf 'cookie: ternal_session=%s\n' "$cookie" >"$state/cli-replay.headers"
curl -fsS "$api_url/auth/session" -H "@$state/cli-replay.headers" | jq -e '.authenticated == false' >/dev/null
if run_cli whoami >/dev/null 2>&1; then
	echo 'terminal session remained usable after ternalctl logout' >&2
	exit 1
fi
find "$state" -depth -name 'cli.headers' -delete
find "$state" -depth -name 'cli-replay.headers' -delete

printf 'CLI device login, local-only session schema, key submission, endpoint redaction, and server-side logout passed\n'

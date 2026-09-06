#!/bin/sh
set -eu

for command in curl go nc openssl tr; do
	command -v "$command" >/dev/null 2>&1 || {
		echo "missing required command: $command" >&2
		exit 127
	}
done

repo=$(CDPATH='' cd "$(dirname "$0")/../.." && pwd)
goauthy_root=${GOAUTHY_ROOT:-/Users/cypark/Documents/project/goauthy}
[ -f "$goauthy_root/go.mod" ] || {
	echo "GOAUTHY_ROOT must reference a GoAuthy source checkout" >&2
	exit 2
}

work=$(mktemp -d)
umask 077
port=${TERNAL_GOAUTHY_E2E_PORT:-$((24000 + ($$ % 1000)))}
redirect_port=$((port + 1000))
issuer="http://127.0.0.1:$port"
redirect="http://127.0.0.1:$redirect_port/auth/callback"
pid=

cleanup() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		kill -TERM "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
	if [ "$status" -ne 0 ]; then
		sed -n '1,160p' "$work/goauthy.log" >&2 2>/dev/null || true
	fi
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT HUP INT TERM

for candidate in "$port" "$redirect_port"; do
	if nc -z 127.0.0.1 "$candidate" >/dev/null 2>&1; then
		echo "local integration port $candidate is already in use" >&2
		exit 2
	fi
done

mkdir -p "$work/master-keys" "$work/data"
openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n' >"$work/master-keys/e2e-1"
openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n' >"$work/oauth-hmac"
client_secret=$(openssl rand -hex 32)
browser_password="Ternal-$(openssl rand -hex 12)-A1"
printf '%s\n' "$client_secret" >"$work/client-secret"
printf '%s\n' "$browser_password" | (cd "$goauthy_root" && go run ./cmd/goauthy-password) >"$work/password-phc"
(cd "$goauthy_root" && go build -trimpath -o "$work/goauthy" ./cmd/goauthy)

GOAUTHY_LISTEN_ADDR="127.0.0.1:$port" \
	GOAUTHY_ISSUER="$issuer" \
	GOAUTHY_RHIZA_PROFILE=standalone \
	GOAUTHY_CLUSTER_ID=ternal-consumer-e2e \
	GOAUTHY_NODE_ID=ternal-consumer-e2e-0 \
	GOAUTHY_DATA_DIR="$work/data" \
	GOAUTHY_MASTER_KEY_DIR="$work/master-keys" \
	GOAUTHY_ACTIVE_MASTER_KEY_ID=e2e-1 \
	GOAUTHY_OAUTH_HMAC_SECRET_FILE="$work/oauth-hmac" \
	GOAUTHY_BOOTSTRAP_CLIENT_ID=ternal \
	GOAUTHY_BOOTSTRAP_CLIENT_SECRET_FILE="$work/client-secret" \
	GOAUTHY_BOOTSTRAP_REDIRECT_URI="$redirect" \
	GOAUTHY_BOOTSTRAP_USER=admin@ternal.e2e \
	GOAUTHY_BOOTSTRAP_USER_SUBJECT=ternal-e2e-admin \
	GOAUTHY_BOOTSTRAP_USER_PASSWORD_PHC_FILE="$work/password-phc" \
	GOAUTHY_BOOTSTRAP_USER_ROLES='["administrator"]' \
	GOAUTHY_BOOTSTRAP_USER_GROUPS='["ternal-admins","operators"]' \
	"$work/goauthy" >"$work/goauthy.log" 2>&1 &
pid=$!

curl --fail --silent --show-error --retry 300 --retry-connrefused --retry-delay 0 --retry-max-time 30 "$issuer/readyz" >/dev/null

cd "$repo"
TERNAL_GOAUTHY_E2E=1 \
	TERNAL_GOAUTHY_E2E_URL="$issuer" \
	TERNAL_GOAUTHY_E2E_USERNAME=admin@ternal.e2e \
	TERNAL_GOAUTHY_E2E_PASSWORD="$browser_password" \
	TERNAL_GOAUTHY_E2E_CLIENT_SECRET="$client_secret" \
	TERNAL_GOAUTHY_E2E_REDIRECT_URL="$redirect" \
	go test -count=1 ./internal/auth -run '^TestGoAuthyAuthorizationCodeConsumer$'

printf 'GoAuthy standalone and Ternal authorization-code consumer integration passed\n'

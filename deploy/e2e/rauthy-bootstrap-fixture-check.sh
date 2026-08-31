#!/bin/sh
set -eu

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 127
	fi
}

need jq

dir=${TERNAL_RAUTHY_BOOTSTRAP_DIR:-deploy/rauthy-local/bootstrap}

jq -e '
	.[0].id == "ternal"
	and .[0].secret.Plain == "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	and (.[0].secret.Plain | length) >= 64
	and (.[0].redirect_uris | index("http://127.0.0.1:3000/auth/callback") != null)
	and (.[0].flows_enabled | index("authorization_code") != null)
	and (.[0].flows_enabled | index("urn:ietf:params:oauth:grant-type:device_code") != null)
	and (.[0].flows_enabled | index("device_code") == null)
	and (.[0].scopes | index("openid") != null)
	and (.[0].scopes | index("groups") != null)
	and (.[0].default_scopes | index("openid") != null)
	and (.[0].default_scopes | index("groups") != null)
	and .[0].id_token_alg == "RS256"
' "$dir/clients.json" >/dev/null

jq -e '
	(map(.name) | index("ternal-admins") != null and index("platform") != null)
' "$dir/groups.json" >/dev/null

jq -e '
	any(.[]; .email == "ternal-admin@example.com"
		and (.groups | index("ternal-admins") != null)
		and (.groups | index("platform") != null)
		and (.roles | index("admin") != null)
		and .enabled == true
		and .email_verified == true)
	and any(.[]; .email == "alice@example.com"
		and (.groups | index("platform") != null)
		and (.groups | index("ternal-admins") == null)
		and .enabled == true
		and .email_verified == true)
' "$dir/users.json" >/dev/null

printf 'rauthy bootstrap fixture check passed (empty-database fixture only)\n'
printf 'bootstrap_dir=%s\n' "$dir"
printf 'live_device_grant=not_checked (run deploy/e2e/rauthy-device-grant-preflight.sh)\n'

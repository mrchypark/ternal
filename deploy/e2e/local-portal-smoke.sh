#!/bin/sh
set -eu

for command in curl grep sed; do
	command -v "$command" >/dev/null 2>&1 || {
		echo "missing required command: $command" >&2
		exit 127
	}
done

api_url=$(printf '%s' "${TERNAL_API_URL:-http://127.0.0.1:3000}" | sed 's#/$##')
umask 077
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM HUP

require_env() {
	eval "value=\${$1:-}"
	[ -n "$value" ] || { echo "$1 is required" >&2; exit 1; }
	case $value in *[[:space:]]*) echo "$1 contains whitespace" >&2; exit 1 ;; esac
}

write_headers() {
	path=$1
	cookie=$2
	case $cookie in *=*) ;; *) cookie="ternal_session=$cookie" ;; esac
	printf 'cookie: %s\n' "$cookie" >"$path"
}

fetch() {
	role=$1
	view=$2
	headers=$3
	mode=$4
	body="$work/$role-$view-$mode.html"
	if [ "$mode" = fragment ]; then
		status=$(curl -sS -o "$body" -w '%{http_code}' "$api_url/?view=$view" -H "@$headers" -H 'HX-Request: true')
	else
		status=$(curl -sS -o "$body" -w '%{http_code}' "$api_url/?view=$view" -H "@$headers")
	fi
	[ "$status" = 200 ] || { echo "$role $view $mode returned HTTP $status" >&2; exit 1; }
	if [ "$mode" = fragment ]; then
		grep -q '^<section id="workspace"' "$body"
		if grep -qi '<!doctype\|<script src=' "$body"; then
			echo "$role $view returned a document shell for an htmx request" >&2
			exit 1
		fi
	else
		grep -qi '<!doctype html>' "$body"
		grep -q 'href="#workspace"' "$body"
		grep -q 'name="viewport"' "$body"
		grep -q 'src="/assets/htmx.min.js"' "$body"
	fi
}

require_env TERNAL_ADMIN_SESSION_COOKIE
require_env TERNAL_USER_SESSION_COOKIE
write_headers "$work/admin.headers" "$TERNAL_ADMIN_SESSION_COOKIE"
write_headers "$work/user.headers" "$TERNAL_USER_SESSION_COOKIE"

for view in hosts keys policies access audit; do
	fetch admin "$view" "$work/admin.headers" page
	fetch admin "$view" "$work/admin.headers" fragment
done

for view in hosts keys access; do
	fetch user "$view" "$work/user.headers" page
	fetch user "$view" "$work/user.headers" fragment
done

for view in policies audit; do
	status=$(curl -sS -o /dev/null -w '%{http_code}' "$api_url/?view=$view" -H "@$work/user.headers")
	[ "$status" = 403 ] || { echo "regular user $view returned HTTP $status, want 403" >&2; exit 1; }
done

if grep -q 'view=policies\|view=audit' "$work/user-hosts-page.html"; then
	echo 'regular user navigation exposed administrator views' >&2
	exit 1
fi
grep -q 'view=policies' "$work/admin-hosts-page.html"
grep -q 'view=audit' "$work/admin-hosts-page.html"

printf 'authenticated portal pages, htmx fragments, navigation roles, and admin-view denial passed\n'

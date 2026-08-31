#!/bin/sh
set -eu

need() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "missing required command: $1" >&2
		exit 127
	}
}

need helm

chart=deploy/helm/ternal
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ternal-helm-render.XXXXXX")
trap 'find "$tmp" -depth -delete' EXIT INT TERM HUP

helm lint "$chart" \
	--set image.tag=render-check \
	--set-string secrets.rauthyClientSecret=test-rauthy-secret \
	--set-string secrets.sessionKey=0123456789abcdef0123456789abcdef \
	--set-string secrets.rhizaAdminToken=0123456789abcdef0123456789abcdef \
	--set-string secrets.pigeonsRelayAccessToken=0123456789abcdef0123456789abcdef

helm template ternal "$chart" \
	--set image.tag=render-check \
	--set-string rauthy.issuer=https://auth.ternal.example.invalid/auth/v1/ \
	--set-string rauthy.redirectUrl=https://ternal.example.invalid/auth/callback \
	--set-string secrets.rauthyClientSecret=test-rauthy-secret \
	--set-string secrets.sessionKey=0123456789abcdef0123456789abcdef \
	--set-string secrets.rhizaAdminToken=0123456789abcdef0123456789abcdef \
	--set-string secrets.pigeonsRelayAccessToken=0123456789abcdef0123456789abcdef \
	>"$tmp/default.yaml"

grep -q '^kind: StatefulSet$' "$tmp/default.yaml"
grep -q '^kind: Service$' "$tmp/default.yaml"
grep -q 'RAUTHY_ISSUER: "https://auth.ternal.example.invalid/auth/v1/"' "$tmp/default.yaml"
helm template ternal "$chart" \
	-f "$chart/values-production.yaml" \
	--set image.tag=render-check \
	--set-string rauthy.issuer=https://auth.ternal.example.invalid/auth/v1/ \
	--set-string rauthy.redirectUrl=https://ternal.example.invalid/auth/callback \
	--set-string secrets.existingSecret=ternal-runtime \
	>"$tmp/production.yaml"

grep -q '^  volumeClaimTemplates:$' "$tmp/production.yaml"
grep -q 'name: ternal-runtime' "$tmp/production.yaml"
if grep -q 'test-rauthy-secret' "$tmp/production.yaml"; then
	echo "production render contains an inline test credential" >&2
	exit 1
fi

echo "Helm render checks passed"

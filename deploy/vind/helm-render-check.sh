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
	--set-string secrets.oidcClientSecret=test-oidc-secret \
	--set-string secrets.sessionKey=0123456789abcdef0123456789abcdef \
	--set-string secrets.dataAdminToken=0123456789abcdef0123456789abcdef \
	--set-string secrets.relayAccessToken=0123456789abcdef0123456789abcdef

helm template ternal "$chart" \
	--set image.tag=render-check \
	--set-string oidc.issuer=https://auth.ternal.example.invalid/auth/v1/ \
	--set-string oidc.redirectUrl=https://ternal.example.invalid/auth/callback \
	--set-string secrets.oidcClientSecret=test-oidc-secret \
	--set-string secrets.sessionKey=0123456789abcdef0123456789abcdef \
	--set-string secrets.dataAdminToken=0123456789abcdef0123456789abcdef \
	--set-string secrets.relayAccessToken=0123456789abcdef0123456789abcdef \
	>"$tmp/default.yaml"

grep -q '^kind: StatefulSet$' "$tmp/default.yaml"
grep -q '^kind: Service$' "$tmp/default.yaml"
grep -q 'TERNAL_OIDC_ISSUER: "https://auth.ternal.example.invalid/auth/v1/"' "$tmp/default.yaml"
helm template ternal "$chart" \
	-f "$chart/values-production.yaml" \
	--set image.tag=render-check \
	--set-string oidc.issuer=https://auth.ternal.example.invalid/auth/v1/ \
	--set-string oidc.redirectUrl=https://ternal.example.invalid/auth/callback \
	--set-string secrets.existingSecret=ternal-runtime \
	>"$tmp/production.yaml"

grep -q '^  volumeClaimTemplates:$' "$tmp/production.yaml"
grep -q 'name: ternal-runtime' "$tmp/production.yaml"
grep -q 'TERNAL_RELAY_BIND: 0.0.0.0:3001' "$tmp/production.yaml"
grep -q 'name: ternal-internal' "$tmp/production.yaml"
grep -q 'access.http.url = "http://ternal-internal:3001/internal/iroh-relay/access"' "$tmp/production.yaml"

helm install ternal-notes "$chart" --dry-run=client \
	--set image.tag=render-check \
	--set-string secrets.oidcClientSecret=test-oidc-secret \
	--set-string secrets.sessionKey=0123456789abcdef0123456789abcdef \
	--set-string secrets.dataAdminToken=0123456789abcdef0123456789abcdef \
	>"$tmp/default-notes.txt"
helm install ternal-notes "$chart" --dry-run=client \
	-f "$chart/values-production.yaml" \
	--set image.tag=render-check \
	--set-string secrets.existingSecret=ternal-runtime \
	>"$tmp/production-notes.txt"

grep -q 'stores /data in emptyDir' "$tmp/default-notes.txt"
grep -q 'stores /data on a PVC' "$tmp/production-notes.txt"
if grep -q 'stores /data in emptyDir' "$tmp/production-notes.txt"; then
	echo "production notes incorrectly report emptyDir storage" >&2
	exit 1
fi
if grep -q 'test-oidc-secret' "$tmp/production.yaml"; then
	echo "production render contains an inline test credential" >&2
	exit 1
fi

echo "Helm render checks passed"

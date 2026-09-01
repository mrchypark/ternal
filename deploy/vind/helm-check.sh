#!/bin/sh
set -eu

name=${VCLUSTER_NAME:-ternal-vind-helm-$$}

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 127
	fi
}

need docker
need helm
need kubectl
need vcluster

sh deploy/vind/helm-render-check.sh

docker info >/dev/null

created=0
cleanup() {
	code=$?
	if [ "$created" -eq 1 ] && [ -z "${VIND_KEEP_CLUSTER:-}" ]; then
		vcluster delete "$name" --driver docker --ignore-not-found >/dev/null 2>&1 || true
	elif [ "$created" -eq 1 ]; then
		echo "kept vCluster: $name"
	fi
	exit "$code"
}
trap cleanup EXIT INT TERM HUP

created=1
vcluster create "$name" --driver docker --connect=false
vcluster connect "$name" --driver docker -- kubectl wait --for=condition=Ready node --all --timeout=180s
vcluster connect "$name" --driver docker -- helm upgrade --install ternal deploy/helm/ternal \
	--dry-run=server \
	-f deploy/helm/ternal/values-production.yaml \
	--set gateway.enabled=false \
	--set image.repository=ternal \
	--set image.tag=local-check \
	--set-string oidc.issuer=https://auth.ternal.example.invalid/auth/v1/ \
	--set-string oidc.clientId=ternal \
	--set-string oidc.redirectUrl=https://ternal.example.invalid/auth/callback \
	--set-string oidc.adminGroup=ternal-admins \
	--set-string oidc.groupsClaim=groups \
	--set-string secrets.oidcClientSecret=test-oidc-secret \
	--set-string secrets.sessionKey=0123456789abcdef0123456789abcdef \
	--set-string secrets.dataAdminToken=0123456789abcdef0123456789abcdef \
	--set-string secrets.relayAccessToken=0123456789abcdef0123456789abcdef

echo "vind helm server dry-run check passed: $name"

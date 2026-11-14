#!/bin/sh
# Operator and recovery-anchor render checks for the Ternal chart.
set -eu

need() {
	command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1" >&2; exit 127; }
}

need helm
need yq
chart=${1:-deploy/helm/ternal}
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ternal-operator-render.XXXXXX")
cleanup() {
	status=$?
	trap - EXIT INT TERM HUP
	find "$tmp" -depth -delete
	exit "$status"
}
trap cleanup EXIT INT TERM HUP

ha() {
	helm template ternal "$chart" \
		--set image.tag=render-check \
		--set data.mode=ha \
		--set-string data.clusterID=ternal-ha-a1 \
		--set data.objectStore.provider=s3 \
		--set-string data.trustAnchorConfigMap=ternal-trust-floor \
		--set-string serviceAccountName=ternal-trust-runtime \
		--set-string data.objectStore.endpoint=object-store.example.invalid:9000 \
		--set-string data.objectStore.bucket=ternal-ha \
		--set data.objectStore.insecure=true \
		--set-string secrets.existingSecret=ternal-runtime "$@"
}

# The legacy operator path is unqualified; this is not a v0.24 managed-v1alpha2 integration.
if ha --set operator.enabled=true >"$tmp/operator.yaml" 2>"$tmp/operator.err"; then
	echo 'operator.enabled=true was accepted for an unsupported contract' >&2
	exit 1
fi
grep -F 'operator.enabled is unqualified with Rhiza v0.24' "$tmp/operator.err" >/dev/null

# The default remains operator-free and renders no operator resources.
ha >"$tmp/ha.yaml"
if grep -q 'rhiza-operator' "$tmp/ha.yaml"; then
	echo 'HA render contains operator resources while disabled' >&2
	exit 1
fi
if grep -q 'ternal-anchor' "$tmp/ha.yaml"; then
	echo 'HA render contains anchor resources while disabled' >&2
	exit 1
fi
helm template ternal "$chart" --set image.tag=render-check \
	--set-string secrets.existingSecret=fixture >"$tmp/standalone.yaml"
if grep -Eq 'rhiza-operator|ternal-anchor' "$tmp/standalone.yaml"; then
	echo 'standalone render contains disabled recovery resources' >&2
	exit 1
fi

# Anchor is independently pinned to the external trust guard, and it never
# mounts application data or receives upstream operator state.
ha --set anchor.enabled=true \
	--set-string anchor.tlsSecretName=ternal-anchor-tls \
	--set-string anchor.tokenSecretName=ternal-anchor-token \
	--set-string anchor.tokenSecretKey=token \
	--set-string anchor.serviceAccountName=ternal-anchor >"$tmp/anchor.yaml"
anchor='select(.kind=="Deployment" and .metadata.name=="ternal-anchor")'
yq "$anchor | .spec.template.spec.containers[0].command | length" "$tmp/anchor.yaml" | grep -qx '1'
yq "$anchor | .spec.template.spec.containers[0].command[0]" "$tmp/anchor.yaml" | grep -qx '/usr/local/bin/ternal-anchor'
yq "$anchor | .spec.template.spec.serviceAccountName" "$tmp/anchor.yaml" | grep -qx 'ternal-anchor'
yq "$anchor | .spec.template.spec.containers[0].env[] | select(.name==\"TERNAL_RECOVERY_ANCHOR_ID\") | .value" "$tmp/anchor.yaml" | grep -qx 'ternal-trust-floor'
yq "$anchor | .spec.template.spec.containers[0].env[] | select(.name==\"TERNAL_TRUST_ANCHOR_CONFIGMAP\") | .value" "$tmp/anchor.yaml" | grep -qx 'ternal-trust-floor'
yq "$anchor | .spec.template.spec.containers[0].env[] | select(.name==\"TERNAL_TRUST_ANCHOR_NAMESPACE\") | .value" "$tmp/anchor.yaml" | grep -qx 'default'
yq "$anchor | .spec.template.spec.containers[0].env[] | select(.name==\"TMPDIR\") | .value" "$tmp/anchor.yaml" | grep -qx '/tmp'
yq "$anchor | .spec.template.spec.volumes[].emptyDir.sizeLimit" "$tmp/anchor.yaml" | grep -qx '512Mi'
if yq "$anchor" "$tmp/anchor.yaml" | grep -Eq 'RHIZA_|/data/ternal|claimName:'; then
	echo 'anchor carries operator or data-plane state' >&2
	exit 1
fi

# Anchor credentials and identity fail closed without reviving the operator.
for missing in \
	'--set anchor.enabled=true --set-string anchor.tokenSecretName=t --set-string anchor.tokenSecretKey=token' \
	'--set anchor.enabled=true --set-string anchor.tlsSecretName=t --set-string anchor.tokenSecretKey=token' \
	'--set anchor.enabled=true --set-string anchor.tlsSecretName=t --set-string anchor.tokenSecretName=t' \
	'--set anchor.enabled=true --set-string anchor.tlsSecretName=t --set-string anchor.tokenSecretName=t --set-string anchor.tokenSecretKey=token --set-string anchor.serviceAccountName=ternal-trust-runtime' \
	'--set anchor.enabled=true --set-string anchor.tlsSecretName=t --set-string anchor.tokenSecretName=t --set-string anchor.tokenSecretKey=token --set-string anchor.serviceAccountName=ternal-operator'; do
	# shellcheck disable=SC2086
	if ha $missing >"$tmp/invalid.yaml" 2>/dev/null; then
		echo "anchor accepted incomplete credentials: $missing" >&2
		exit 1
	fi
done

echo 'Operator render checks passed'

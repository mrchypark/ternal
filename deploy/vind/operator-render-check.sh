#!/bin/sh
# Operator render cases for the Ternal chart.
#
# These live outside helm-render-check.sh because the upstream controller's
# variable names belong to the operator surface: scripts/check-public-config.sh
# exempts operator-* chart and test files from the naming gate.
set -eu

need() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "missing required command: $1" >&2
		exit 127
	}
}

need helm
need yq

chart=${1:-deploy/helm/ternal}
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ternal-operator-render.XXXXXX")
trap 'find "$tmp" -depth -delete' EXIT INT TERM HUP

# --- Operator render cases ---

# HA with operator enabled: renders SA, Role, RoleBinding, Deployment, NetworkPolicy.
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
	--set-string secrets.existingSecret=ternal-runtime \
	--set operator.enabled=true \
	>"$tmp/ha-operator.yaml"

for required in \
	'^kind: ServiceAccount$' \
	'^kind: Role$' \
	'^kind: RoleBinding$' \
	'^kind: Deployment$' \
	'^kind: NetworkPolicy$' \
	'rhiza-operator' \
	'rhiza.mrchypark.dev' \
	'recovery' \
	'podSelector' \
	'port: 3000' \
	'port: 9090' \
	'port: 9091' \
	'nodeSelector' \
	'kubernetes.io/arch: amd64' \
	'emptyDir: {}'; do
	grep -q "$required" "$tmp/ha-operator.yaml" || {
		echo "HA operator render missing $required" >&2
		exit 1
	}
done

# Ternal pods keep TERNAL_* names and reach the operator over the app port.
yq 'select(.kind=="StatefulSet") | .spec.template.spec.containers[].env[]?.name' "$tmp/ha-operator.yaml" | grep -q '^TERNAL_OPERATOR_BIND$'

# The upstream controller reads the target pod's environment for identity,
# membership, admin token, data directory, and object-store location, so the
# pod carries those names too; every value is derived from Ternal's values.
for key in RHIZA_NODE_ID RHIZA_DATA_DIR RHIZA_CLUSTER_ID RHIZA_CLUSTER_MEMBERS \
	RHIZA_ADMIN_TOKEN RHIZA_OBJSTORE_PROVIDER RHIZA_OBJSTORE_ENDPOINT \
	RHIZA_OBJSTORE_BUCKET RHIZA_OBJSTORE_PREFIX RHIZA_OBJSTORE_DURABILITY; do
	yq 'select(.kind=="StatefulSet") | .spec.template.spec.containers[].env[]?.name' "$tmp/ha-operator.yaml" | grep -qx "$key" || {
		echo "operator render is missing $key on the StatefulSet" >&2
		exit 1
	}
done

# Pod and operator must address the same cluster and object-store location.
for key in RHIZA_CLUSTER_ID RHIZA_OBJSTORE_PROVIDER RHIZA_OBJSTORE_ENDPOINT \
	RHIZA_OBJSTORE_BUCKET RHIZA_OBJSTORE_PREFIX RHIZA_OBJSTORE_DURABILITY; do
	pod_value=$(yq "select(.kind==\"StatefulSet\") | .spec.template.spec.containers[].env[]? | select(.name==\"$key\") | .value" "$tmp/ha-operator.yaml" | head -1)
	operator_value=$(yq "select(.kind==\"Deployment\") | .spec.template.spec.containers[].env[]? | select(.name==\"$key\") | .value" "$tmp/ha-operator.yaml" | head -1)
	[ "$pod_value" = "$operator_value" ] || {
		echo "$key differs between the pod ($pod_value) and the operator ($operator_value)" >&2
		exit 1
	}
done

# The injected identity must equal Ternal's own rendered configuration, not just
# agree with the operator Deployment: two generated copies could drift together.
for pair in \
	'RHIZA_DATA_DIR:TERNAL_DATA_DIR' \
	'RHIZA_CLUSTER_ID:TERNAL_DATA_CLUSTER_ID' \
	'RHIZA_OBJSTORE_PROVIDER:TERNAL_OBJECT_STORE_PROVIDER' \
	'RHIZA_OBJSTORE_ENDPOINT:TERNAL_OBJECT_STORE_ENDPOINT' \
	'RHIZA_OBJSTORE_BUCKET:TERNAL_OBJECT_STORE_BUCKET' \
	'RHIZA_OBJSTORE_PREFIX:TERNAL_OBJECT_STORE_PREFIX' \
	'RHIZA_OBJSTORE_DURABILITY:TERNAL_OBJECT_STORE_DURABILITY'; do
	pod_key=${pair%%:*}
	config_key=${pair##*:}
	pod_value=$(yq "select(.kind==\"StatefulSet\") | .spec.template.spec.containers[].env[]? | select(.name==\"$pod_key\") | .value" "$tmp/ha-operator.yaml" | head -1)
	config_value=$(yq "select(.kind==\"ConfigMap\" and .data.$config_key != null) | .data.$config_key" "$tmp/ha-operator.yaml" | head -1)
	[ -n "$config_value" ] && [ "$pod_value" = "$config_value" ] || {
		echo "$pod_key ($pod_value) differs from $config_key ($config_value)" >&2
		exit 1
	}
done

# Credentials come from the same Secret keys Ternal's own pods use.
members_ref=$(yq 'select(.kind=="StatefulSet") | .spec.template.spec.containers[].env[]? | select(.name=="RHIZA_CLUSTER_MEMBERS") | .valueFrom.secretKeyRef.name + ":" + .valueFrom.secretKeyRef.key' "$tmp/ha-operator.yaml" | head -1)
token_ref=$(yq 'select(.kind=="StatefulSet") | .spec.template.spec.containers[].env[]? | select(.name=="RHIZA_ADMIN_TOKEN") | .valueFrom.secretKeyRef.name + ":" + .valueFrom.secretKeyRef.key' "$tmp/ha-operator.yaml" | head -1)
[ "$members_ref" = "ternal-runtime:TERNAL_DATA_CLUSTER_MEMBERS" ] || {
	echo "membership reference is $members_ref" >&2
	exit 1
}
[ "$token_ref" = "ternal-runtime:TERNAL_DATA_ADMIN_TOKEN" ] || {
	echo "admin token reference is $token_ref" >&2
	exit 1
}

# No PVC or volumeClaimTemplates in operator render.
if grep -Eq 'kind: PersistentVolumeClaim|volumeClaimTemplates:|claimName:' "$tmp/ha-operator.yaml"; then
	echo "HA operator render contains persistent storage" >&2
	exit 1
fi

# NetworkPolicy: app ingress is POD port 3000, not service.port.
yq '.spec.ingress[0].ports[0].port' "$tmp/ha-operator.yaml" | grep -q '^3000$'

# NetworkPolicy: peer UDP 9090 ingress restricted to same app pods.
yq 'select(.kind=="NetworkPolicy") | .spec.ingress[] | select(.ports[0].port==9090) | .from[0].podSelector.matchLabels' "$tmp/ha-operator.yaml" \
	| grep -q 'app.kubernetes.io/name: ternal'

# NetworkPolicy: recovery TCP 9091 restricted to operator pods.
yq 'select(.kind=="NetworkPolicy") | .spec.ingress[] | select(.ports[0].port==9091) | .from[0].podSelector.matchLabels' "$tmp/ha-operator.yaml" \
	| grep -q 'app.kubernetes.io/name: rhiza-operator'

# NetworkPolicy: port 3001 (relay) absent when managedRelay is disabled.
if yq 'select(.kind=="NetworkPolicy") | .spec.ingress[].ports[].port' "$tmp/ha-operator.yaml" | grep -q '^3001$'; then
	echo "NetworkPolicy rendered relay port 3001 without managedRelay" >&2
	exit 1
fi

# ManagedRelay renders port 3001 in NetworkPolicy.
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
	--set-string secrets.existingSecret=ternal-runtime \
	--set operator.enabled=true \
	--set transport.managedRelay.enabled=true \
	--set transport.managedRelay.access.enabled=true \
	--set transport.managedRelay.access.path=/internal/iroh-relay/access \
	>"$tmp/operator-relay.yaml"

yq 'select(.kind=="NetworkPolicy") | .spec.ingress[].ports[].port' "$tmp/operator-relay.yaml" | grep -q '^3001$'

# service.port=80 must NOT change NetworkPolicy app ingress (still 3000).
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
	--set-string secrets.existingSecret=ternal-runtime \
	--set operator.enabled=true \
	--set service.port=80 \
	>"$tmp/operator-svcport.yaml"

yq 'select(.kind=="NetworkPolicy") | .spec.ingress[0].ports[0].port' "$tmp/operator-svcport.yaml" | grep -q '^3000$'

# automaticRecovery=true is rejected.
if helm template ternal "$chart" \
	--set image.tag=render-check \
	--set data.mode=ha \
	--set-string data.clusterID=ternal-ha-a1 \
	--set data.objectStore.provider=s3 \
	--set-string data.trustAnchorConfigMap=ternal-trust-floor \
	--set-string serviceAccountName=ternal-trust-runtime \
	--set-string data.objectStore.endpoint=object-store.example.invalid:9000 \
	--set-string data.objectStore.bucket=ternal-ha \
	--set data.objectStore.insecure=true \
	--set-string secrets.existingSecret=ternal-runtime \
	--set operator.enabled=true \
	--set operator.automaticRecovery=true \
	>"$tmp/operator-auto.yaml" 2>/dev/null; then
	echo "operator.automaticRecovery=true accepted" >&2
	exit 1
fi

# Standalone: no operator resources rendered when operator.enabled=false.
helm template ternal "$chart" \
	--set image.tag=render-check \
	--set-string secrets.existingSecret=fixture \
	>"$tmp/standalone-noop.yaml"

if grep -q 'rhiza-operator' "$tmp/standalone-noop.yaml"; then
	echo "standalone render contains operator resources" >&2
	exit 1
fi

# serviceAccountAnnotations rendered on operator SA when set.
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
	--set-string secrets.existingSecret=ternal-runtime \
	--set operator.enabled=true \
	--set 'operator.serviceAccountAnnotations.iam\.gke\.io/gcp-service-account=gcs-ternal@proj.iam.gserviceaccount.com' \
	>"$tmp/operator-sa-annot.yaml"

# Use yq to target ONLY the operator ServiceAccount, avoiding StatefulSet annotations.
yq 'select(.kind == "ServiceAccount" and .metadata.name == "ternal-operator") .metadata.annotations' "$tmp/operator-sa-annot.yaml" \
	| grep -q 'iam.gke.io/gcp-service-account: gcs-ternal@proj.iam.gserviceaccount.com'

# operator SA has no annotations when serviceAccountAnnotations is {}.
yq 'select(.kind == "ServiceAccount" and .metadata.name == "ternal-operator") .metadata.annotations' "$tmp/ha-operator.yaml" \
	| grep -q 'null'


echo "Operator render checks passed"

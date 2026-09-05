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

case ${1:-current} in
--failure-fixture)
	helm template ternal "$chart" --set image.tag=render-check --set persistence.enabled=true >/dev/null
	exit 0
	;;
--valid-fixture)
	helm template ternal "$chart" --set image.tag=render-check --set-string secrets.existingSecret=fixture >/dev/null
	exit 0
	;;
current) ;;
*)
	echo "usage: $0 [current|--failure-fixture|--valid-fixture]" >&2
	exit 2
	;;
esac

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
grep -q 'image: "ghcr.io/mrchypark/ternal:render-check"' "$tmp/default.yaml"
grep -q 'TERNAL_OIDC_ISSUER: "https://auth.ternal.example.invalid/auth/v1/"' "$tmp/default.yaml"
grep -q '^  replicas: 1$' "$tmp/default.yaml"
grep -q '^          emptyDir: {}$' "$tmp/default.yaml"
if grep -Eq 'kind: PersistentVolumeClaim|volumeClaimTemplates:|claimName:|TERNAL_OBJECT_STORE_' "$tmp/default.yaml"; then
	echo "ephemeral standalone render contains persistent storage" >&2
	exit 1
fi
if grep -q 'TERNAL_DATA_MULTI_NODE' "$tmp/default.yaml"; then
	echo "standalone render enables HA" >&2
	exit 1
fi

helm template ternal "$chart" \
	--set image.tag=render-check \
	--set data.requireObjectStore=true \
	--set-string data.clusterID=ternal-standalone-a1 \
	--set data.objectStore.provider=s3 \
	--set-string data.objectStore.endpoint=object-store.example.invalid:9000 \
	--set-string data.objectStore.bucket=ternal-standalone \
	--set data.objectStore.insecure=true \
	--set-string secrets.existingSecret=ternal-runtime \
	>"$tmp/standalone-durable.yaml"

grep -q '^  replicas: 1$' "$tmp/standalone-durable.yaml"
grep -q '^          emptyDir: {}$' "$tmp/standalone-durable.yaml"
grep -q 'TERNAL_OBJECT_STORE_PREFIX: "clusters/ternal-standalone-a1"' "$tmp/standalone-durable.yaml"
grep -q 'TERNAL_OBJECT_STORE_DURABILITY: "before-ack"' "$tmp/standalone-durable.yaml"

helm template ternal "$chart" \
	--set image.tag=render-check \
	--set data.mode=ha \
	--set-string data.clusterID=ternal-ha-a1 \
	--set data.objectStore.provider=s3 \
	--set-string data.objectStore.endpoint=object-store.example.invalid:9000 \
	--set-string data.objectStore.bucket=ternal-ha \
	--set data.objectStore.insecure=true \
	--set-string secrets.existingSecret=ternal-runtime \
	>"$tmp/ha.yaml"

for required in \
	'^  replicas: 3$' \
	'^  podManagementPolicy: Parallel$' \
	'^    type: OnDelete$' \
	'ternal.dev/data-identity: [0-9a-f]\{32\}' \
	'^          requiredDuringSchedulingIgnoredDuringExecution:$' \
	'^  clusterIP: None$' \
	'^  publishNotReadyAddresses: true$' \
	'^kind: PodDisruptionBudget$' \
	'^  minAvailable: 2$' \
	'^          startupProbe:$' \
	'^            failureThreshold: 60$' \
	'key: TERNAL_DATA_CLUSTER_MEMBERS' \
	'TERNAL_DATA_EXPECTED_MEMBER_IDS' \
	'TERNAL_DATA_MULTI_NODE' \
	'TERNAL_DATA_SCHEMA_VERSION: "1"' \
	'TERNAL_DATA_CHECKPOINT_INTERVAL: "15m"' \
	'TERNAL_OBJECT_STORE_PREFIX: "clusters/ternal-ha-a1"' \
	'TERNAL_OBJECT_STORE_DURABILITY: "before-ack"' \
	'^          emptyDir: {}$'; do
	grep -q "$required" "$tmp/ha.yaml" || {
		echo "HA render missing $required" >&2
		exit 1
	}
done

for rendered in "$tmp/standalone-durable.yaml" "$tmp/ha.yaml"; do
	if grep -Eq 'kind: PersistentVolumeClaim|volumeClaimTemplates:|claimName:' "$rendered"; then
		echo "durable render contains a PVC: $rendered" >&2
		exit 1
	fi
done

ha_identity=$(grep 'ternal.dev/data-identity:' "$tmp/ha.yaml" | head -1 | awk '{print $2}')
[ -n "$ha_identity" ]
grep -q "name: ternal-data-$ha_identity" "$tmp/ha.yaml"

helm template ternal "$chart" \
	--set image.tag=render-check \
	--set data.mode=ha \
	--set-string data.clusterID=ternal-ha-a1 \
	--set data.objectStore.provider=s3 \
	--set-string data.objectStore.endpoint=object-store.example.invalid:9000 \
	--set-string data.objectStore.bucket=ternal-ha \
	--set data.objectStore.insecure=true \
	--set-string secrets.existingSecret=ternal-runtime \
	>"$tmp/ha-repeat.yaml"
cmp "$tmp/ha.yaml" "$tmp/ha-repeat.yaml"

helm template ternal "$chart" \
	--set image.tag=render-check \
	--set data.mode=ha \
	--set-string data.clusterID=ternal-ha-a1 \
	--set data.objectStore.provider=s3 \
	--set-string data.objectStore.bucket=ternal-ha \
	--set-string data.objectStore.prefix=clusters/other \
	--set-string secrets.existingSecret=ternal-runtime \
	>"$tmp/ha-other-identity.yaml"
other_identity=$(grep 'ternal.dev/data-identity:' "$tmp/ha-other-identity.yaml" | head -1 | awk '{print $2}')
if [ "$ha_identity" = "$other_identity" ]; then
	echo "HA identity did not change with immutable object-store prefix" >&2
	exit 1
fi

helm template ternal "$chart" \
	--set image.tag=render-check \
	--set data.mode=ha \
	--set-string data.clusterID=ternal-gcs-a1 \
	--set data.objectStore.provider=gcs \
	--set-string data.objectStore.bucket=ternal-ha \
	--set-string serviceAccountName=ternal-gcs \
	--set-string secrets.existingSecret=ternal-runtime \
	>"$tmp/ha-gcs.yaml"

grep -q '^      serviceAccountName: "ternal-gcs"$' "$tmp/ha-gcs.yaml"
grep -q 'TERNAL_OBJECT_STORE_PROVIDER: "gcs"' "$tmp/ha-gcs.yaml"

for invalid in \
	'--set data.mode=ha --set-string secrets.existingSecret=ternal-runtime --set data.objectStore.provider=s3 --set data.objectStore.bucket=ternal-ha' \
	'--set data.mode=ha --set-string data.clusterID=ternal-ha-a1 --set data.objectStore.provider=s3 --set data.objectStore.bucket=ternal-ha' \
	'--set data.mode=ha --set-string data.clusterID=ternal-ha-a1 --set-string secrets.existingSecret=ternal-runtime --set data.objectStore.provider=filesystem --set data.objectStore.bucket=ternal-ha' \
	'--set data.mode=ha --set-string data.clusterID=ternal-ha-a1 --set-string secrets.existingSecret=ternal-runtime --set data.objectStore.provider=gcs --set-string data.objectStore.endpoint=gcs.example.invalid --set data.objectStore.bucket=ternal-ha' \
	'--set data.mode=ha --set-string data.clusterID=ternal-ha-a1 --set-string secrets.existingSecret=ternal-runtime --set data.objectStore.provider=gcs --set data.objectStore.insecure=true --set data.objectStore.bucket=ternal-ha' \
	'--set data.mode=ha --set-string data.clusterID=INVALID_ID --set-string secrets.existingSecret=ternal-runtime --set data.objectStore.provider=s3 --set data.objectStore.bucket=ternal-ha' \
	'--set-string data.clusterID=ternal-a1 --set data.objectStore.provider=s3 --set data.objectStore.bucket=ternal --set data.objectStore.durability=async' \
	'--set persistence.enabled=true'; do
	# shellcheck disable=SC2086
	if helm template ternal "$chart" --set image.tag=render-check $invalid >"$tmp/invalid.yaml" 2>/dev/null; then
		echo "invalid storage render accepted: $invalid" >&2
		exit 1
	fi
done

if helm template ternal "$chart" \
	--set image.tag=render-check \
	--set data.schemaVersion=2 \
	--set-string secrets.oidcClientSecret=test-oidc-secret \
	--set-string secrets.sessionKey=0123456789abcdef0123456789abcdef \
	--set-string secrets.dataAdminToken=0123456789abcdef0123456789abcdef \
	--set-string secrets.relayAccessToken=0123456789abcdef0123456789abcdef \
	>"$tmp/invalid-schema.yaml" 2>/dev/null; then
	echo "unsupported data schema version accepted" >&2
	exit 1
fi

helm template ternal "$chart" \
	-f "$chart/values-production.yaml" \
	--set image.tag=render-check \
	--set-string oidc.issuer=https://auth.ternal.example.invalid/auth/v1/ \
	--set-string oidc.redirectUrl=https://ternal.example.invalid/auth/callback \
	--set-string secrets.existingSecret=ternal-runtime \
	--set-string data.clusterID=ternal-production-a1 \
	--set data.objectStore.provider=gcs \
	--set-string data.objectStore.bucket=ternal-production \
	>"$tmp/production.yaml"

grep -q '^          emptyDir: {}$' "$tmp/production.yaml"
grep -q 'TERNAL_OBJECT_STORE_PROVIDER: "gcs"' "$tmp/production.yaml"
if grep -Eq 'kind: PersistentVolumeClaim|volumeClaimTemplates:|claimName:' "$tmp/production.yaml"; then
	echo "production render contains a PVC" >&2
	exit 1
fi
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
	--set-string data.clusterID=ternal-production-a1 \
	--set data.objectStore.provider=gcs \
	--set-string data.objectStore.bucket=ternal-production \
	--set-string secrets.existingSecret=ternal-runtime \
	>"$tmp/production-notes.txt"

grep -q 'ephemeral standalone development mode' "$tmp/default-notes.txt"
grep -q 'Rhiza-certified object storage is authoritative' "$tmp/production-notes.txt"
if grep -q 'test-oidc-secret' "$tmp/production.yaml"; then
	echo "production render contains an inline test credential" >&2
	exit 1
fi

echo "Helm render checks passed"

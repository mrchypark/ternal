#!/bin/sh
set -eu

need() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "missing required command: $1" >&2
		exit 127
	}
}

need helm
need yq

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
digest=sha256:1111111111111111111111111111111111111111111111111111111111111111
for tag in '' render-check; do
	helm template ternal "$chart" --set-string secrets.existingSecret=fixture --set-string "image.tag=$tag" --set-string "image.digest=$digest" >"$tmp/digest.yaml"
	grep -q "image: \"ghcr.io/mrchypark/ternal@$digest\"" "$tmp/digest.yaml"
done
if helm template ternal "$chart" --set-string secrets.existingSecret=fixture --set image.tag=render-check --set image.digest=sha256:invalid >"$tmp/digest.yaml" 2>"$tmp/digest.err"; then
	echo "invalid API image digest accepted" >&2; exit 1
fi
grep -q 'image.digest must be a sha256 digest' "$tmp/digest.err"
grep -q 'TERNAL_OIDC_ISSUER: "https://auth.ternal.example.invalid/auth/v1/"' "$tmp/default.yaml"
grep -q 'TERNAL_OIDC_POLICY_CLAIMS: ""' "$tmp/default.yaml"
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

secretChecksum() {
	helm template ternal "$chart" 		--set image.tag=render-check 		--set-string secrets.oidcClientSecret=test-oidc-secret 		--set-string secrets.sessionKey="$1" 		--set-string secrets.dataAdminToken=0123456789abcdef0123456789abcdef 		--set-string secrets.relayAccessToken=0123456789abcdef0123456789abcdef 	| grep 'checksum/secret:' | awk '{print $2}'
}
if [ "$(secretChecksum 0123456789abcdef0123456789abcdef)" = "$(secretChecksum fedcba9876543210fedcba9876543210)" ]; then
	echo "chart-managed secret change did not move the pod checksum" >&2; exit 1
fi
if [ "$(secretChecksum 0123456789abcdef0123456789abcdef)" != "$(secretChecksum 0123456789abcdef0123456789abcdef)" ]; then
	echo "pod checksum unstable for identical secrets" >&2; exit 1
fi
helm template ternal "$chart" --set image.tag=render-check --set-string secrets.existingSecret=fixture --set-string secrets.version=1 >"$tmp/external-v1.yaml"
helm template ternal "$chart" --set image.tag=render-check --set-string secrets.existingSecret=fixture --set-string secrets.version=2 >"$tmp/external-v2.yaml"
if [ "$(grep 'ternal.dev/secret-version:' "$tmp/external-v1.yaml")" = "$(grep 'ternal.dev/secret-version:' "$tmp/external-v2.yaml")" ]; then
	echo "external secret version bump did not move the pod annotation" >&2; exit 1
fi


helm template ternal "$chart" \
	--set image.tag=render-check \
	--set data.requireObjectStore=true \
	--set-string 'oidc.policyClaims[0]=role' \
	--set-string 'oidc.policyClaims[1]=department' \
	--set-string data.clusterID=ternal-standalone-a1 \
	--set data.objectStore.provider=s3 \
	--set-string data.trustAnchorConfigMap=ternal-trust-floor \
	--set-string serviceAccountName=ternal-trust-runtime \
	--set-string data.objectStore.endpoint=object-store.example.invalid:9000 \
	--set-string data.objectStore.bucket=ternal-standalone \
	--set data.objectStore.insecure=true \
	--set-string secrets.existingSecret=ternal-runtime \
	>"$tmp/standalone-durable.yaml"

grep -q '^  replicas: 1$' "$tmp/standalone-durable.yaml"
grep -q '^          emptyDir: {}$' "$tmp/standalone-durable.yaml"
grep -q 'TERNAL_OBJECT_STORE_PREFIX: "clusters/ternal-standalone-a1"' "$tmp/standalone-durable.yaml"
grep -q 'TERNAL_OBJECT_STORE_DURABILITY: "before-ack"' "$tmp/standalone-durable.yaml"
grep -q 'TERNAL_OIDC_POLICY_CLAIMS: "role,department"' "$tmp/standalone-durable.yaml"

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
	grep -q 'name: TERNAL_TRUST_ANCHOR_CONFIGMAP' "$rendered"
	grep -q 'value: "ternal-trust-floor"' "$rendered"
	grep -q 'name: TERNAL_TRUST_ANCHOR_NAMESPACE' "$rendered"
	grep -q 'automountServiceAccountToken: true' "$rendered"
	if grep -Eq 'kind: PersistentVolumeClaim|volumeClaimTemplates:|claimName:' "$rendered"; then
		echo "durable render contains a PVC: $rendered" >&2
		exit 1
	fi
done

ha_identity=$(grep 'ternal.dev/data-identity:' "$tmp/ha.yaml" | head -1 | awk '{print $2}')
[ -n "$ha_identity" ]
grep -q "name: ternal-data-$ha_identity" "$tmp/ha.yaml"

# Without the operator the HA pods name nothing upstream.
if grep -q 'RHIZA_' "$tmp/ha.yaml"; then
	echo "HA render without the operator names upstream variables" >&2
	exit 1
fi

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
	>"$tmp/ha-repeat.yaml"
cmp "$tmp/ha.yaml" "$tmp/ha-repeat.yaml"

helm template ternal "$chart" \
	--set image.tag=render-check \
	--set data.mode=ha \
	--set-string data.clusterID=ternal-ha-a1 \
	--set data.objectStore.provider=s3 \
	--set-string data.trustAnchorConfigMap=ternal-trust-floor \
	--set-string serviceAccountName=ternal-trust-runtime \
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
	--set-string data.trustAnchorConfigMap=ternal-trust-floor \
	--set-string serviceAccountName=ternal-trust-runtime \
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
	if helm template ternal "$chart" --set image.tag=render-check --set-string data.trustAnchorConfigMap=ternal-trust-floor --set-string serviceAccountName=ternal-trust-runtime $invalid >"$tmp/invalid.yaml" 2>/dev/null; then
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
	--set-string data.trustAnchorConfigMap=ternal-trust-floor \
	--set-string serviceAccountName=ternal-trust-runtime \
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
grep -q 'image: "n0computer/iroh-relay@sha256:7805aaf67ca59e04cd54999d142c85ed72e38e5bd4515c902fa5c8d838674747"' "$tmp/production.yaml"

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
	--set-string data.trustAnchorConfigMap=ternal-trust-floor \
	--set-string serviceAccountName=ternal-trust-runtime \
	--set-string data.objectStore.bucket=ternal-production \
	--set-string secrets.existingSecret=ternal-runtime \
	>"$tmp/production-notes.txt"

grep -q 'ephemeral standalone development mode' "$tmp/default-notes.txt"
grep -q 'Rhiza-certified object storage preserves certified application state' "$tmp/production-notes.txt"
grep -q 'HA pod replacement also requires the documented voter recovery procedure' "$tmp/production-notes.txt"
if grep -q 'replacement pod rebuilds' "$tmp/production-notes.txt"; then
	echo "notes promise unsafe automatic HA voter restoration" >&2; exit 1
fi
if grep -q 'test-oidc-secret' "$tmp/production.yaml"; then
	echo "production render contains an inline test credential" >&2
	exit 1
fi

for missing in data.trustAnchorConfigMap serviceAccountName; do
	if helm template ternal "$chart" --set image.tag=render-check \
		--set-string data.clusterID=ternal-guard-check --set data.objectStore.provider=gcs \
		--set data.objectStore.bucket=ternal-guard-check --set-string secrets.existingSecret=ternal-runtime \
		--set-string data.trustAnchorConfigMap=ternal-trust-floor --set-string serviceAccountName=ternal-trust-runtime \
		--set-string "$missing=" >"$tmp/missing-anchor.yaml" 2>"$tmp/missing-anchor.err"; then
		echo "durable render accepted missing $missing" >&2; exit 1
	fi
	grep -q "$missing is required" "$tmp/missing-anchor.err"
done

# Local is opt-in and never reuses a standalone/HA data identity.
local_render() {
	helm template ternal-local "$chart" -f "$chart/values-local.yaml" \
		--set-string "image.digest=$digest" \
		--set-string secrets.existingSecret=ternal-local-runtime \
		--set-string data.clusterID=ternal-local-a1 \
		--set data.local.writerGCQuiescenceAcknowledged=true \
		--set data.objectStore.provider=s3 \
		--set-string data.objectStore.endpoint=object-store.example.invalid:9000 \
		--set data.objectStore.insecure=true \
		--set-string data.objectStore.bucket=ternal-local \
		--set-string data.trustAnchorConfigMap=ternal-local-trust \
		--set-string serviceAccountName=ternal-local-data "$@"
}
local_render >"$tmp/local.yaml"
grep -q '^  replicas: 1$' "$tmp/local.yaml"
grep -q '^    type: OnDelete$' "$tmp/local.yaml"
grep -q 'manually seeded' "$chart/templates/NOTES.txt"
if grep -Eq 'kind: Deployment|kind: PersistentVolumeClaim|volumeClaimTemplates:|claimName:|rhiza.mrchypark.dev|RHIZA_|name: data-peer|name: recovery|hostPath:' "$tmp/local.yaml"; then
	echo "Local render includes an Operator, HA port, PVC, host path, or upstream environment" >&2; exit 1
fi
for role in initContainers containers; do
	yq "select(.kind==\"StatefulSet\") | .spec.template.spec.$role[0]" "$tmp/local.yaml" >"$tmp/local-$role.yaml"
done
[ "$(yq '.command | join(" ")' "$tmp/local-initContainers.yaml")" = /usr/local/bin/ternal-api ]
[ "$(yq '.name' "$tmp/local-initContainers.yaml")" = ternal-local-restore ]
[ "$(yq '.args | join(" ")' "$tmp/local-initContainers.yaml")" = local-restore ]
[ "$(yq '.image' "$tmp/local-initContainers.yaml")" = "ghcr.io/mrchypark/ternal@$digest" ]
[ "$(yq '.image' "$tmp/local-containers.yaml")" = "ghcr.io/mrchypark/ternal@$digest" ]
[ "$(yq '.envFrom | length' "$tmp/local-initContainers.yaml")" = 0 ]
[ "$(yq '.volumeMounts | length' "$tmp/local-initContainers.yaml")" = 2 ]
[ "$(yq '.volumeMounts[0].mountPath' "$tmp/local-initContainers.yaml")" = /staging ]
[ "$(yq '.volumeMounts[] | select(.name=="data") | .mountPath' "$tmp/local-initContainers.yaml")" = /data ]
[ "$(yq '.volumeMounts[] | select(.name=="data") | .readOnly // false' "$tmp/local-initContainers.yaml")" = false ]
[ "$(yq '.volumeMounts[] | select(.mountPath=="/staging") | .readOnly' "$tmp/local-containers.yaml")" = true ]
for role in initContainers containers; do
	[ "$(yq '.securityContext.runAsUser' "$tmp/local-$role.yaml")" = 65532 ]
	[ "$(yq '.securityContext.runAsGroup' "$tmp/local-$role.yaml")" = 65532 ]
	[ "$(yq '.securityContext.readOnlyRootFilesystem' "$tmp/local-$role.yaml")" = true ]
	[ "$(yq '.securityContext.allowPrivilegeEscalation' "$tmp/local-$role.yaml")" = false ]
	done
for name in TERNAL_DATA_LOCAL TERNAL_DATA_MULTI_NODE TERNAL_DATA_DIR TERNAL_DATA_CLUSTER_ID TERNAL_DATA_NODE_ID \
	TERNAL_LOCAL_PREFLIGHT_STAGING_DIR TERNAL_LOCAL_PREFLIGHT_RECEIPT_FILE TERNAL_LOCAL_WRITER_GC_QUIESCENCE_ACK \
	TERNAL_TRUST_ANCHOR_CONFIGMAP TERNAL_TRUST_ANCHOR_NAMESPACE TERNAL_OBJECT_STORE_PROVIDER TERNAL_OBJECT_STORE_ENDPOINT \
	TERNAL_OBJECT_STORE_BUCKET TERNAL_OBJECT_STORE_PREFIX TERNAL_OBJECT_STORE_REGION TERNAL_OBJECT_STORE_INSECURE TERNAL_OBJECT_STORE_DURABILITY; do
	init_value=$(yq ".env[] | select(.name==\"$name\") | .value" "$tmp/local-initContainers.yaml")
	api_value=$(yq ".env[] | select(.name==\"$name\") | .value" "$tmp/local-containers.yaml")
	[ "$init_value" = "$api_value" ] || { echo "Local initializer/API configuration differs: $name" >&2; exit 1; }
done
[ "$(yq '.env[] | select(.name=="TERNAL_DATA_LOCAL") | .value' "$tmp/local-initContainers.yaml")" = 1 ]
[ "$(yq '.env[] | select(.name=="TERNAL_DATA_MULTI_NODE") | .value' "$tmp/local-initContainers.yaml")" = 0 ]
[ "$(yq '.env[] | select(.name=="TERNAL_LOCAL_PREFLIGHT_RECEIPT_FILE") | .value' "$tmp/local-initContainers.yaml")" = /staging/preflight.json ]
for role in initContainers containers; do
	[ "$(yq '.env[] | select(.name=="TERNAL_LOCAL_PREFLIGHT_SCOPE") | .valueFrom.fieldRef.fieldPath' "$tmp/local-$role.yaml")" = metadata.uid ]
	[ "$(yq '.env[] | select(.name=="TERNAL_LOCAL_PREFLIGHT_SCOPE") | .valueFrom.fieldRef.apiVersion' "$tmp/local-$role.yaml")" = v1 ]
done
if yq '.env[].name' "$tmp/local-initContainers.yaml" | grep -Eq 'OIDC|SESSION_KEY|ADMIN_TOKEN|CLUSTER_MEMBERS'; then
	echo "Local initializer receives application/admin credentials or HA membership" >&2; exit 1
fi
local_identity=$(yq 'select(.kind=="StatefulSet") | .spec.selector.matchLabels."ternal.dev/data-identity"' "$tmp/local.yaml")
standalone_identity=$(local_render --set data.mode=standalone | yq 'select(.kind=="StatefulSet") | .spec.selector.matchLabels."ternal.dev/data-identity"')
[ "$local_identity" != "$standalone_identity" ] || { echo "Local mode reused standalone identity" >&2; exit 1; }
local_render --set data.objectStore.provider=gcs --set-string data.objectStore.endpoint= --set data.objectStore.insecure=false >"$tmp/local-gcs.yaml"
for invalid in \
	'--set data.mode=invalid' \
	'--set operator.enabled=true' \
	'--set operator.automaticRecovery=true' \
	'--set anchor.enabled=true' \
	'--set data.local.replicas=2' \
	'--set data.local.replicas=0' \
	'--set persistence.enabled=true' \
	'--set data.local.writerGCQuiescenceAcknowledged=false' \
	'--set-string data.local.writerGCQuiescenceAcknowledged=false' \
	'--set-string data.local.preflightReceiptFile=' \
	'--set-string data.local.preflightReceiptFile=/data/receipt.json' \
	'--set-string data.trustAnchorConfigMap=' \
	'--set-string serviceAccountName=' \
	'--set data.objectStore.durability=async' \
	'--set data.objectStore.provider=filesystem' \
	'--set-string image.digest='; do
	# shellcheck disable=SC2086
	if local_render $invalid >"$tmp/local-invalid.yaml" 2>"$tmp/local-invalid.err"; then
		echo "unsafe Local configuration accepted: $invalid" >&2; exit 1
	fi
done

# Operator cases live in their own file: the upstream controller's variable
# names belong to the operator surface.
sh "$(dirname "$0")/operator-render-check.sh" "$chart"

echo "Helm render checks passed"

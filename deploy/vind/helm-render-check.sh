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

# Azure routing identifiers are public; all authentication secrets remain in an
# existing Secret and never become ConfigMap data or Helm credential values.
azure_render() {
	helm template ternal-azure "$chart" --set image.tag=render-check \
		--set data.mode=ha --set-string data.clusterID=ternal-azure-a1 \
		--set data.objectStore.provider=azure --set-string data.objectStore.bucket=ternal-archive \
		--set-string data.objectStore.azure.storageAccount=account123 \
		--set-string data.trustAnchorConfigMap=ternal-azure-trust \
		--set-string serviceAccountName=ternal-azure-data \
		--set-string secrets.existingSecret=ternal-azure-runtime "$@"
}
azure_render >"$tmp/azure-ha.yaml"
azure_render --set data.mode=standalone --set data.requireObjectStore=true >"$tmp/azure-standalone.yaml"
azure_render --set data.mode=local --set-string "image.digest=$digest" \
	--set data.local.writerGCQuiescenceAcknowledged=true >"$tmp/azure-local.yaml"
for mode in ha standalone local; do
	grep -q 'TERNAL_OBJECT_STORE_AZURE_STORAGE_ACCOUNT: "account123"' "$tmp/azure-$mode.yaml"
	grep -q 'TERNAL_OBJECT_STORE_AZURE_BLOB_ENDPOINT: "https://account123.blob.core.windows.net"' "$tmp/azure-$mode.yaml"
	if yq 'select(.kind=="ConfigMap") | .data | keys | .[]' "$tmp/azure-$mode.yaml" | grep -Eq 'AZURE_(STORAGE_ACCOUNT_KEY|CONNECTION_STRING|CLIENT_SECRET)$'; then
		echo "Azure credential name leaked into ConfigMap data" >&2; exit 1
	fi
done
yq 'select(.kind=="StatefulSet") | .spec.template.spec.initContainers[0]' "$tmp/azure-local.yaml" >"$tmp/azure-init.yaml"
[ "$(yq '.env | length' "$tmp/azure-init.yaml")" = 27 ]
[ "$(yq '.envFrom | length' "$tmp/azure-init.yaml")" = 0 ]
for key in STORAGE_ACCOUNT_KEY CONNECTION_STRING CLIENT_SECRET; do
	[ "$(yq ".env[] | select(.name==\"TERNAL_OBJECT_STORE_AZURE_$key\") | .valueFrom.secretKeyRef.name" "$tmp/azure-init.yaml")" = ternal-azure-runtime ]
	[ "$(yq ".env[] | select(.name==\"TERNAL_OBJECT_STORE_AZURE_$key\") | .valueFrom.secretKeyRef.key" "$tmp/azure-init.yaml")" = "TERNAL_OBJECT_STORE_AZURE_$key" ]
done
if yq '.env[].name' "$tmp/azure-init.yaml" | grep -Eq 'OIDC|SESSION_KEY|ADMIN_TOKEN|CLUSTER_MEMBERS|^TERNAL_OBJECT_STORE_(ACCESS_KEY|SECRET_KEY|SESSION_TOKEN)$'; then
	echo "Azure init received unrelated application or S3 credentials" >&2; exit 1
fi
azure_identity=$(yq 'select(.kind=="StatefulSet") | .spec.selector.matchLabels."ternal.dev/data-identity"' "$tmp/azure-ha.yaml")
azure_explicit=$(azure_render --set-string data.objectStore.azure.blobEndpoint=https://account123.blob.core.windows.net | yq 'select(.kind=="StatefulSet") | .spec.selector.matchLabels."ternal.dev/data-identity"')
[ "$azure_identity" = "$azure_explicit" ] || { echo "default/explicit Azure service URL changed identity" >&2; exit 1; }
azure_other=$(azure_render --set-string data.objectStore.azure.storageAccount=account456 | yq 'select(.kind=="StatefulSet") | .spec.selector.matchLabels."ternal.dev/data-identity"')
[ "$azure_identity" != "$azure_other" ] || { echo "Azure account failed to change identity" >&2; exit 1; }
azure_auth=$(azure_render --set-string data.objectStore.azure.clientID=public-client-id | yq 'select(.kind=="StatefulSet") | .spec.selector.matchLabels."ternal.dev/data-identity"')
[ "$azure_identity" = "$azure_auth" ] || { echo "Azure auth identifier leaked into immutable location identity" >&2; exit 1; }
for invalid in \
	'--set-string data.objectStore.azure.storageAccount=' \
	'--set-string data.objectStore.azure.storageAccount=INVALID' \
	'--set-string data.objectStore.endpoint=generic.invalid' \
	'--set data.objectStore.insecure=true' \
	'--set-string data.objectStore.azure.blobEndpoint=http://127.0.0.1:4577/account123' \
	'--set-string data.objectStore.azure.blobEndpoint=https://account123.blob.core.windows.net/' \
	'--set-string data.objectStore.azure.blobEndpoint=https://account123.blob.core.windows.net:' \
	'--set-string data.objectStore.azure.blobEndpoint=https://account123.blob.core.windows.net:0' \
	'--set-string data.objectStore.azure.blobEndpoint=https://account123.blob.core.windows.net:65536' \
	'--set-string data.objectStore.azure.blobEndpoint=https://user:credential@account123.blob.core.windows.net' \
	'--set-string data.objectStore.azure.blobEndpoint=https://account123.blob.core.windows.net?sig=credential' \
	'--set-string data.objectStore.azure.storageAccountKey=not-a-real-secret' \
	'--set-string data.objectStore.azure.connectionString=not-a-real-secret' \
	'--set-string data.objectStore.azure.clientSecret=not-a-real-secret' \
	'--set data.objectStore.azure.emulator=true' \
	'--set-string secrets.existingSecret=' \
	'--set operator.enabled=true'; do
	# shellcheck disable=SC2086
	if azure_render $invalid >"$tmp/azure-invalid.yaml" 2>"$tmp/azure-invalid.err"; then
		echo "unsafe Azure render accepted: $invalid" >&2; exit 1
	fi
done

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

single_node_render() {
	helm template ternal-single "$chart" --namespace single-test -f "$chart/values-single-node.yaml" \
		--set-string "image.digest=$digest" --set-string secrets.existingSecret=single-runtime \
		--set-string serviceAccountName=single-api --set-string data.trustAnchorConfigMap=single-trust \
		--set-string data.clusterID=single-test --set data.objectStore.provider=s3 \
		--set-string data.objectStore.bucket=single-bucket "$@"
}
single_node_render >"$tmp/single.yaml"
yq 'select(.kind=="StatefulSet")' "$tmp/single.yaml" >"$tmp/single-sts.yaml"
yq '.spec.template.spec.containers[] | select(.name=="ternal-api")' "$tmp/single-sts.yaml" >"$tmp/single-api.yaml"
[ "$(yq '.spec.replicas' "$tmp/single-sts.yaml")" = 1 ]
[ "$(yq '.spec.updateStrategy.type' "$tmp/single-sts.yaml")" = OnDelete ]
[ "$(yq '.spec.template.spec.initContainers | length' "$tmp/single-sts.yaml")" = 0 ]
[ "$(yq '.spec.template.spec.volumes | length' "$tmp/single-sts.yaml")" = 1 ]
[ "$(yq '.spec.template.spec.volumes[0].emptyDir' "$tmp/single-sts.yaml")" = '{}' ]
[ "$(yq '.args | length' "$tmp/single-api.yaml")" = 0 ]
[ "$(yq '.envFrom[0].configMapRef.name' "$tmp/single-api.yaml")" != null ]
[ "$(yq '.envFrom[1].secretRef.name' "$tmp/single-api.yaml")" = single-runtime ]
[ "$(yq '.env | length' "$tmp/single-api.yaml")" = "$(yq '.env | map(.name) | unique | length' "$tmp/single-api.yaml")" ]
yq '.env | map({"key": .name, "value": .value}) | from_entries' "$tmp/single-api.yaml" >"$tmp/single-direct.yaml"
[ "$(yq '.TERNAL_DATA_LOCAL' "$tmp/single-direct.yaml")" = 0 ]
[ "$(yq '.TERNAL_DATA_MULTI_NODE' "$tmp/single-direct.yaml")" = 0 ]
[ "$(yq '.TERNAL_OBJECT_STORE_DURABILITY' "$tmp/single-direct.yaml")" = before-ack ]
[ "$(yq '.TERNAL_DATA_CLUSTER_ID' "$tmp/single-direct.yaml")" = single-test ]
[ "$(yq '.TERNAL_OBJECT_STORE_BUCKET' "$tmp/single-direct.yaml")" = single-bucket ]
[ "$(yq '.TERNAL_DATA_CLUSTER_MEMBERS' "$tmp/single-direct.yaml")" = '' ]
[ "$(yq '.TERNAL_OPERATOR_BIND' "$tmp/single-direct.yaml")" = '' ]
# Model Kubernetes precedence: ConfigMap, stale Secret, then direct env. Every
# public value and mode pin must survive. The process bridge ignores stale
# upstream aliases outside explicit HA, so standalone must not project any.
yq 'select(.kind=="ConfigMap") | .data' "$tmp/single.yaml" >"$tmp/single-config.yaml"
yq 'with_entries(.value = "stale-runtime-secret")' "$tmp/single-direct.yaml" >"$tmp/single-stale.yaml"
yq eval-all '. as $item ireduce ({}; . * $item)' "$tmp/single-config.yaml" "$tmp/single-stale.yaml" "$tmp/single-direct.yaml" >"$tmp/single-effective.yaml"
yq 'sort_keys(.)' "$tmp/single-direct.yaml" >"$tmp/single-direct-sorted.yaml"
yq 'sort_keys(.)' "$tmp/single-effective.yaml" >"$tmp/single-effective-sorted.yaml"
cmp "$tmp/single-direct-sorted.yaml" "$tmp/single-effective-sorted.yaml"
upstream=RHIZA
if yq '.env[].name' "$tmp/single-api.yaml" | grep -q "^${upstream}_"; then
	echo "standalone unnecessarily projects Operator identity aliases" >&2; exit 1
fi
for provider in gcs azure; do
	single_node_render --set "data.objectStore.provider=$provider" --set-string data.objectStore.azure.storageAccount=account123 >"$tmp/single-$provider.yaml"
	[ "$(yq 'select(.kind=="StatefulSet") | .spec.template.spec.containers[0].env | map(.name) | unique | length' "$tmp/single-$provider.yaml")" = "$(yq 'select(.kind=="StatefulSet") | .spec.template.spec.containers[0].env | length' "$tmp/single-$provider.yaml")" ]
done
[ "$(yq 'select(.kind=="StatefulSet") | .spec.template.spec.containers[0].env[] | select(.name=="TERNAL_OBJECT_STORE_AZURE_BLOB_ENDPOINT") | .value' "$tmp/single-azure.yaml")" = https://account123.blob.core.windows.net ]
for invalid in \
	'--set data.mode=ha' '--set data.mode=local' \
	'--set data.requireObjectStore=false' '--set operator.enabled=true' '--set anchor.enabled=true' \
	'--set persistence.enabled=true' '--set data.objectStore.durability=async' \
	'--set-string data.objectStore.provider=' '--set-string data.objectStore.bucket=' \
	'--set-string data.clusterID=' '--set-string data.trustAnchorConfigMap=' \
	'--set-string serviceAccountName=' '--set-string secrets.existingSecret=' '--set-string image.digest='; do
	# shellcheck disable=SC2086
	if single_node_render $invalid >"$tmp/single-invalid.yaml" 2>"$tmp/single-invalid.err"; then
		echo "unsafe durable single-node configuration accepted: $invalid" >&2; exit 1
	fi
done

# Operator cases live in their own file: the upstream controller's variable
# names belong to the operator surface.
sh "$(dirname "$0")/operator-render-check.sh" "$chart"

echo "Helm render checks passed"

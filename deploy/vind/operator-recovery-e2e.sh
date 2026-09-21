#!/bin/sh
# End-to-end check of the rhiza operator integration against a real vCluster.
#
#   stage 1  a three-voter HA Ternal reaches quorum on a shared S3 store
#   stage 2  the operator observes that StatefulSet (RhizaRecovery, no recoveryID)
#   stage 3  a manual generation recovery transitions it and reports Complete
#
# The published operator image is linux/amd64 only, so this check builds the
# pinned upstream operator and Ternal for the cluster's own architecture
# (from the module cache and this working tree) and imports both into the
# vCluster. MinIO stands in for the shared object store. It applies the trust
# anchor ConfigMap and its RBAC through deploy/security/render_trustguard.py and
# deliberately skips that renderer's admission policies, which require an
# immutable registry digest the locally built image does not have.
#
# Knobs: VCLUSTER_NAME, TERNAL_E2E_REUSE_CLUSTER=1 (reuse an existing vCluster),
# TERNAL_E2E_IMAGE, TERNAL_E2E_OPERATOR_IMAGE, TERNAL_E2E_SKIP_RECOVERY=1.
set -eu

need() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "missing required command: $1" >&2
		exit 127
	}
}

for tool in docker helm kubectl python3 vcluster yq go; do
	need "$tool"
done

script_dir=$(CDPATH='' cd "$(dirname "$0")" && pwd)
repo_dir=$(CDPATH='' cd "$script_dir/../.." && pwd)
name=${VCLUSTER_NAME:-ternal-op-e2e-$$}
namespace=${TERNAL_E2E_NAMESPACE:-ternal-operator-e2e}
release=${TERNAL_E2E_RELEASE:-ternal}
cluster_id=${TERNAL_E2E_CLUSTER_ID:-ternal-e2e-a1}
bucket=${TERNAL_E2E_BUCKET:-ternal-e2e}
prefix=${TERNAL_E2E_PREFIX:-clusters/ternal-e2e-a1}
ternal_image=${TERNAL_E2E_IMAGE:-ternal-operator-e2e:local}
operator_image=${TERNAL_E2E_OPERATOR_IMAGE:-rhiza-operator-operator-e2e:local}
# The cluster pulls these itself; quay.io serves the same images as Docker Hub
# without the anonymous pull limits that bite in test environments.
minio_image=${TERNAL_E2E_MINIO_IMAGE:-quay.io/minio/minio:latest}
mc_image=${TERNAL_E2E_MC_IMAGE:-quay.io/minio/mc:latest}
endpoint="minio.$namespace.svc.cluster.local:9000"
# Deterministic test credentials: a voter identity is bound to its tokens, so a
# re-run that rotated them would make the StatefulSet refuse to start. These
# values are for a disposable test cluster only.
derive() {
	python3 -c 'import hashlib,sys;print(hashlib.sha256(sys.argv[1].encode()).hexdigest())' "$1"
}
admin_token=$(derive "$cluster_id:admin")
session_key=$(derive "$cluster_id:session")
work=$(mktemp -d "${TMPDIR:-/tmp}/ternal-operator-e2e.XXXXXX")
created=0

cleanup() {
	status=$?
	trap - EXIT INT TERM HUP
	if [ "$created" -eq 1 ] && [ -z "${TERNAL_E2E_KEEP_CLUSTER:-}" ]; then
		vcluster delete "$name" --driver docker --ignore-not-found >/dev/null 2>&1 || true
	elif [ "$created" -eq 1 ]; then
		echo "kept vCluster: $name"
	fi
	if [ "$status" -ne 0 ]; then
		echo "--- test namespace state ---" >&2
		kubectl -n "$namespace" get pods,rhizarecoveries -o wide >&2 2>/dev/null || true
		kubectl -n "$namespace" get rhizarecovery -o yaml >&2 2>/dev/null | yq '.items[] | {name: .metadata.name, phase: .status.phase, message: .status.message, stage: .status.stage, peers: (.status.peers // [] | length)}' >&2 2>/dev/null || true
		echo "--- operator logs (tail) ---" >&2
		kubectl -n "$namespace" logs deployment/$release-operator --tail=40 >&2 2>/dev/null || true
		for pod in $(kubectl -n "$namespace" get pods -l app.kubernetes.io/name=ternal -o name 2>/dev/null | head -1); do
			echo "--- $pod logs (tail) ---" >&2
			kubectl -n "$namespace" logs "$pod" --tail=30 >&2 2>/dev/null || true
		done
	fi
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT INT TERM HUP

docker info >/dev/null

# --- vCluster ------------------------------------------------------------

container="vcluster.cp.$name"
if docker inspect "$container" >/dev/null 2>&1; then
	[ "${TERNAL_E2E_REUSE_CLUSTER:-0}" = 1 ] || {
		echo "vCluster $name already exists; set TERNAL_E2E_REUSE_CLUSTER=1 to reuse it" >&2
		exit 2
	}
else
	vcluster use driver docker
	vcluster create "$name" --driver docker --connect=false
	created=1
fi

vcluster connect "$name" --print > "$work/kubeconfig"
KUBECONFIG="$work/kubeconfig"
export KUBECONFIG
kubectl wait --for=condition=Ready node --all --timeout=300s

arch=$(kubectl get node -o jsonpath='{.items[0].status.nodeInfo.architecture}')
case "$arch" in
aarch64) goarch=arm64 ;;
x86_64) goarch=amd64 ;;
*) goarch=$arch ;;
esac
echo "vCluster $name is ready (linux/$goarch)"

# --- images --------------------------------------------------------------

import_image() {
	# Save to a file and stream it into the container: piping `docker save`
	# straight into `ctr import` stalls on large images, and `docker cp` does not
	# land the file where this container can read it.
	tarball="$work/$(echo "$1" | tr '/:' '__').tar"
	docker save -o "$tarball" "$1"
	docker exec -i "$container" sh -c 'cat > /tmp/image.tar' < "$tarball"
	docker exec "$container" ctr -n k8s.io images import /tmp/image.tar >/dev/null
	docker exec "$container" rm -f /tmp/image.tar
	rm -f "$tarball"
	docker exec "$container" ctr -n k8s.io images ls -q | grep -qx "docker.io/library/$1" || {
		echo "image $1 was not imported into the vCluster" >&2
		exit 1
	}
}

host_image() {
	docker image inspect "$1" >/dev/null 2>&1 && return 0
	docker pull --platform "linux/$goarch" "$1" >/dev/null 2>&1 && return 0
	echo "image $1 is neither present nor pullable on this host" >&2
	exit 1
}

if [ "${TERNAL_E2E_SKIP_BUILD:-0}" != 1 ]; then
	docker build --platform "linux/$goarch" -t "$ternal_image" "$repo_dir"
fi

module_dir=$(cd "$repo_dir" && go list -m -f '{{.Dir}}' github.com/mrchypark/rhiza)
if [ "${TERNAL_E2E_SKIP_BUILD:-0}" != 1 ]; then
	operator_src="$work/operator-src"
	mkdir -p "$operator_src"
	cp -R "$module_dir/." "$operator_src/"
	chmod -R u+w "$operator_src"
	(cd "$operator_src" && GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath -o "$work/rhiza-operator" ./cmd/rhiza-operator)
	cat > "$work/operator.Dockerfile" <<'EOF'
FROM scratch
COPY rhiza-operator /usr/local/bin/rhiza-operator
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/rhiza-operator"]
EOF
	docker build --platform "linux/$goarch" -t "$operator_image" -f "$work/operator.Dockerfile" "$work"
fi

import_image "$ternal_image"
import_image "$operator_image"

# --- CRDs, storage, namespace -------------------------------------------

for crd in crd.yaml cluster-crd.yaml fence-crd.yaml; do
	kubectl apply -f "$module_dir/deploy/operator/$crd" >/dev/null
done

kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

kubectl -n "$namespace" apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: minio
  labels: {app.kubernetes.io/name: minio}
spec:
  replicas: 1
  selector:
    matchLabels: {app.kubernetes.io/name: minio}
  template:
    metadata:
      labels: {app.kubernetes.io/name: minio}
    spec:
      containers:
        - name: minio
          image: $minio_image
          imagePullPolicy: IfNotPresent
          args: ["server", "/data", "--address", ":9000"]
          env:
            - {name: MINIO_ROOT_USER, value: ternal-e2e}
            - {name: MINIO_ROOT_PASSWORD, value: ternal-e2e-secret}
          ports:
            - {name: s3, containerPort: 9000}
---
apiVersion: v1
kind: Service
metadata:
  name: minio
spec:
  selector: {app.kubernetes.io/name: minio}
  ports:
    - {name: s3, port: 9000, targetPort: 9000}
EOF
kubectl -n "$namespace" wait --for=condition=available deployment/minio --timeout=300s >/dev/null

kubectl -n "$namespace" delete job/minio-bucket --ignore-not-found >/dev/null
kubectl -n "$namespace" apply -f - >/dev/null <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: minio-bucket
spec:
  backoffLimit: 20
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: mc
          image: $mc_image
          imagePullPolicy: IfNotPresent
          command: ["/bin/sh", "-c"]
          args:
            # Recreate the bucket so every run starts from an empty object store:
            # a voter registration left in a bucket outlives the disposable pods.
            - mc alias set e2e http://minio.$namespace.svc.cluster.local:9000 ternal-e2e ternal-e2e-secret && (mc rb --force e2e/$bucket || true) && mc mb e2e/$bucket && mc ls e2e
EOF
kubectl -n "$namespace" wait --for=condition=complete job/minio-bucket --timeout=300s >/dev/null
echo "MinIO is serving bucket $bucket"

# --- Ternal secret and trust anchor --------------------------------------

python3 - "$namespace" "$release" "$cluster_id" <<'PY' > "$work/members.json"
import hashlib, json, sys
namespace, release, cluster_id = sys.argv[1:4]
members = []
seen = set()
for ordinal in range(3):
    token = hashlib.sha256(f"{cluster_id}:voter:{ordinal}".encode()).hexdigest()
    assert token not in seen
    seen.add(token)
    host = f"{release}-{ordinal}.{release}-data.{namespace}.svc.cluster.local"
    members.append({"node_id": f"{release}-{ordinal}", "url": f"quic://{host}:9090", "peer_url": f"quic://{host}:9090", "log_url": f"quic://{host}:9090", "token": token})
print(json.dumps(members))
PY

kubectl -n "$namespace" apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: $release-runtime
type: Opaque
stringData:
  TERNAL_OIDC_CLIENT_SECRET: "e2e-oidc-client-secret"
  TERNAL_SESSION_KEY: "$session_key"
  TERNAL_DATA_ADMIN_TOKEN: "$admin_token"
  TERNAL_DATA_CLUSTER_MEMBERS: '$(cat "$work/members.json")'
  TERNAL_OBJECT_STORE_ACCESS_KEY: ternal-e2e
  TERNAL_OBJECT_STORE_SECRET_KEY: ternal-e2e-secret
EOF

kubectl -n "$namespace" create serviceaccount "$release" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

# The anchor ConfigMap and its RBAC come from the same renderer production uses;
# its admission policies need an immutable registry digest that a locally built
# image cannot have, so this check leaves them out.
# The anchor is reset with the object store: a pending epoch left by an earlier
# run would otherwise block a greenfield bootstrap, exactly as designed.
kubectl -n "$namespace" delete configmap "$release-anchor" --ignore-not-found >/dev/null
python3 - "$repo_dir" "$namespace" "$release" "$cluster_id" "$endpoint" "$bucket" "$prefix" > "$work/trustguard.json" <<'PY'
import json, sys
repo, namespace, release, cluster_id, endpoint, bucket, prefix = sys.argv[1:8]
sys.path.insert(0, repo + "/deploy/security")
import render_trustguard as tg
identity = tg.storage_id("s3", endpoint, bucket, prefix, cluster_id)
doc = tg.render(namespace, release, release + "-anchor", cluster_id, identity,
                [tg.OFFICIAL_IMAGE + "@sha256:" + "0" * 64])
items = [item for item in doc["items"] if item["kind"] in ("ConfigMap", "Role", "RoleBinding")]
json.dump({"apiVersion": "v1", "kind": "List", "items": items}, sys.stdout)
PY
kubectl -n "$namespace" apply -f "$work/trustguard.json" >/dev/null

echo "Ternal secret and trust anchor are provisioned"

# --- Ternal HA release ----------------------------------------------------

cat > "$work/values.yaml" <<EOF
image:
  repository: ${ternal_image%:*}
  tag: ${ternal_image##*:}
  pullPolicy: IfNotPresent
oidc:
  issuer: https://auth.ternal.example.invalid/auth/v1/
  clientId: ternal
  redirectUrl: https://ternal.example.invalid/auth/callback
  adminGroup: ternal-admins
  groupsClaim: groups
secrets:
  existingSecret: $release-runtime
serviceAccountName: $release
data:
  mode: ha
  clusterID: $cluster_id
  trustAnchorConfigMap: $release-anchor
  objectStore:
    provider: s3
    endpoint: $endpoint
    bucket: $bucket
    insecure: true
operator:
  enabled: true
  image:
    repository: ${operator_image%:*}
    tag: ${operator_image##*:}
    pullPolicy: IfNotPresent
  nodeSelector:
    kubernetes.io/os: linux
    kubernetes.io/arch: $goarch
# The chart's HA anti-affinity expects three nodes; the disposable test
# vCluster has one, so this check takes the documented override.
affinity: {}
EOF

if ! helm template "$release" "$repo_dir/deploy/helm/ternal" --namespace "$namespace" -f "$work/values.yaml" > "$work/release.yaml"; then
	echo "the chart refuses the e2e values" >&2
	exit 1
fi
# Drop any previous generation first: its emptyDir identity would not match the
# fresh object store, which is exactly what a new greenfield cluster must not do.
kubectl -n "$namespace" delete statefulset "$release" --ignore-not-found --wait=true >/dev/null
kubectl -n "$namespace" apply -f "$work/release.yaml" >/dev/null

wait_pod() {
	pod=$1
	for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
		kubectl -n "$namespace" get "pod/$pod" >/dev/null 2>&1 && break
		sleep 2
	done
	kubectl -n "$namespace" wait --for=condition=Ready "pod/$pod" --timeout=600s >/dev/null
}

for ordinal in 0 1 2; do
	wait_pod "$release-$ordinal"
done
echo "stage 1: three-voter HA reached quorum"

# --- stage 2: observation -------------------------------------------------

wait_phase() {
	cr=$1
	want=$2
	timeout=${3:-600}
	deadline=$(( $(date +%s) + timeout ))
	while :; do
		phase=$(kubectl -n "$namespace" get "rhizarecovery/$cr" -o jsonpath='{.status.phase}' 2>/dev/null || true)
		if [ "$phase" = "$want" ]; then
			return 0
		fi
		if [ "$(date +%s)" -ge "$deadline" ]; then
			echo "RhizaRecovery $cr is '$phase', want '$want'" >&2
			kubectl -n "$namespace" get "rhizarecovery/$cr" -o jsonpath='{.status}' | yq -P >&2 || true
			return 1
		fi
		sleep 5
	done
}

kubectl -n "$namespace" apply -f - >/dev/null <<EOF
apiVersion: rhiza.mrchypark.dev/v1alpha1
kind: RhizaRecovery
metadata:
  name: $release-observe
spec:
  statefulSet: $release
  container: ternal-api
  sourceClusterID: $cluster_id
  durability: before-ack
  recoveryID: ""
EOF

wait_phase "$release-observe" Observed 600
peers=$(kubectl -n "$namespace" get "rhizarecovery/$release-observe" -o jsonpath='{.status.peers[*].node_id}' | wc -w | tr -d ' ')
if [ "$peers" != 3 ]; then
	echo "observation reported $peers peers, want 3" >&2
	exit 1
fi
ready=$(kubectl -n "$namespace" get "rhizarecovery/$release-observe" -o jsonpath='{.status.peers[?(@.ready==true)].node_id}' | wc -w | tr -d ' ')
if [ "$ready" != 3 ]; then
	echo "observation reported $ready ready peers, want 3" >&2
	exit 1
fi
echo "stage 2: operator observed three ready peers"

# --- stage 3: manual generation recovery ----------------------------------

if [ "${TERNAL_E2E_SKIP_RECOVERY:-0}" = 1 ]; then
	echo "operator e2e passed (recovery stage skipped)"
	exit 0
fi

recovery_id="e2e-$(date +%s)"
cat > "$work/recovery.yaml" <<EOF
apiVersion: rhiza.mrchypark.dev/v1alpha1
kind: RhizaRecovery
metadata:
  name: $release-recovery
spec:
  statefulSet: $release
  container: ternal-api
  sourceClusterID: $cluster_id
  durability: before-ack
  recoveryID: $recovery_id
EOF

kubectl -n "$namespace" delete -f "$work/recovery.yaml" --ignore-not-found >/dev/null
kubectl -n "$namespace" apply -f "$work/recovery.yaml" >/dev/null

cr_uid=$(kubectl -n "$namespace" get "rhizarecovery/$release-recovery" -o jsonpath='{.metadata.uid}')
sts_uid=$(kubectl -n "$namespace" get "statefulset/$release" -o jsonpath='{.metadata.uid}')
kubectl -n "$namespace" patch "rhizarecovery/$release-recovery" --type=merge -p "$(cat <<JSON
{"spec":{"fence":{"recoveryID":"$recovery_id","clusterID":"$cluster_id","statefulSetUID":"$sts_uid","confirmed":true,"evidence":"operator-recovery-e2e: source generation fenced for $cr_uid"}}}
JSON
)" >/dev/null

wait_phase "$release-recovery" Complete 1800
target=$(kubectl -n "$namespace" get "rhizarecovery/$release-recovery" -o jsonpath='{.status.target}')
recovered_tip=$(kubectl -n "$namespace" get "rhizarecovery/$release-recovery" -o jsonpath='{.status.recoveredTip}')
if [ -z "$target" ] || [ "$recovered_tip" = 0 ]; then
	echo "recovery completed without a target generation or recovered tip" >&2
	exit 1
fi

# The transition is only real if the running generation now reports the target.
cluster_now=$(kubectl -n "$namespace" get "rhizarecovery/$release-recovery" -o jsonpath='{.status.peers[0].cluster_id}')
if [ "$cluster_now" != "$target" ]; then
	echo "recovered peers report cluster $cluster_now, want $target" >&2
	exit 1
fi

# The recovered generation runs under the target cluster ID, but the trust
# anchor still binds the source one, so Ternal must refuse to serve it.  That
# fail-closed start is the contract until the anchor transition exists (#104),
# and this check asserts it rather than waiting for a readiness that must not
# arrive: a recovered voter that became ready would be serving unfenced data.
for ordinal in 0 1 2; do
	pod="$release-$ordinal"
	for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
		phase=$(kubectl -n "$namespace" get "pod/$pod" -o jsonpath='{.status.phase}' 2>/dev/null || true)
		[ "$phase" = Running ] && break
		sleep 2
	done
	if [ "$(kubectl -n "$namespace" get "pod/$pod" -o jsonpath='{.status.phase}')" != Running ]; then
		echo "recovered $pod is not running" >&2
		exit 1
	fi
	if [ "$(kubectl -n "$namespace" get "pod/$pod" -o jsonpath='{.status.containerStatuses[0].ready}')" = true ]; then
		echo "recovered $pod reports ready while the anchor still binds $cluster_id" >&2
		exit 1
	fi
	if ! kubectl -n "$namespace" logs "$pod" | grep -q 'trust anchor unsettled, Ternal readiness withheld'; then
		echo "recovered $pod did not record the withheld readiness" >&2
		exit 1
	fi
done
echo "stage 3: generation recovery completed from $cluster_id to $target at tip $recovered_tip; the recovered generation runs fail-closed on the source anchor"
echo "operator recovery e2e passed"

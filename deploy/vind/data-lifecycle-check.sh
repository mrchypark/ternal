#!/bin/sh
set -eu

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 127
	fi
}

need helm
need yq

script_dir=$(CDPATH='' cd "$(dirname "$0")" && pwd)
repo_dir=$(CDPATH='' cd "$script_dir/../.." && pwd)
work=$(mktemp -d)
rendered="$work/ternal.yaml"
namespace="ternal-emptydir-check-$$"
statefulset=emptydir-check
pod=emptydir-check-0
created_namespace=0

cleanup() {
	status=$?
	trap - EXIT INT TERM HUP
	if [ "$created_namespace" -eq 1 ]; then
		kubectl --context "$context" delete namespace "$namespace" --wait=false >/dev/null 2>&1 || true
	fi
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT INT TERM HUP

helm template ternal "$repo_dir/deploy/helm/ternal" \
	--set image.tag=ops-data-lifecycle-check \
	--set-string secrets.oidcClientSecret=test-oidc-secret \
	--set-string secrets.sessionKey=0123456789abcdef0123456789abcdef \
	--set-string secrets.dataAdminToken=0123456789abcdef0123456789abcdef >"$rendered"

data_volume=$(yq ea '
  select(.kind == "StatefulSet")
  | .spec.template.spec.containers[]
  | select(.name == "ternal-api")
  | .volumeMounts[]
  | select(.mountPath == "/data")
  | .name
' "$rendered")
[ -n "$data_volume" ]
DATA_VOLUME="$data_volume" yq ea -e '
  select(.kind == "StatefulSet")
  | .spec.template.spec.volumes[]
  | select(.name == strenv(DATA_VOLUME) and has("emptyDir"))
' "$rendered" >/dev/null
if grep -Eq 'kind: PersistentVolumeClaim|volumeClaimTemplates:|claimName:' "$rendered"; then
	echo "rendered chart unexpectedly contains persistent storage" >&2
	exit 1
fi

echo "Helm render confirms /data uses emptyDir and no PVC"

if [ "${TERNAL_VIND_RECREATE_DISPOSABLE_POD:-0}" != 1 ]; then
	echo "pod recreation skipped; set TERNAL_VIND_RECREATE_DISPOSABLE_POD=1 and TERNAL_KUBE_CONTEXT to an explicit vind/vcluster context"
	exit 0
fi

need kubectl
context=${TERNAL_KUBE_CONTEXT:-}
if [ -z "$context" ]; then
	echo "TERNAL_KUBE_CONTEXT is required for disposable pod recreation" >&2
	exit 2
fi
case "$context" in
	*vind*|*vcluster*) ;;
	*)
		echo "refusing non-vind/non-vcluster context: $context" >&2
		exit 2
		;;
esac
if [ "$(kubectl config current-context)" != "$context" ]; then
	echo "current kubectl context does not exactly match TERNAL_KUBE_CONTEXT" >&2
	exit 2
fi

create_statefulset() {
	kubectl --context "$context" -n "$namespace" apply -f - <<EOF
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: $statefulset
  labels:
    app.kubernetes.io/name: ternal-emptydir-check
spec:
  serviceName: $statefulset
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: ternal-emptydir-check
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ternal-emptydir-check
    spec:
      serviceAccountName: lifecycle-check
      containers:
        - name: ternal
          image: busybox:1.37
          command: ["sh", "-c", "sleep 3600"]
          volumeMounts:
            - name: data
              mountPath: /data
      volumes:
        - name: data
          emptyDir: {}
EOF
	kubectl --context "$context" -n "$namespace" wait --for=condition=Ready "pod/$pod" --timeout=120s
}

kubectl --context "$context" create namespace "$namespace"
created_namespace=1
kubectl --context "$context" -n "$namespace" create serviceaccount lifecycle-check
create_statefulset
kubectl --context "$context" -n "$namespace" exec "$pod" -- sh -c 'printf disposable > /data/marker'
kubectl --context "$context" -n "$namespace" exec "$pod" -- test -f /data/marker
kubectl --context "$context" -n "$namespace" delete "statefulset/$statefulset" --cascade=foreground --wait=true
create_statefulset
kubectl --context "$context" -n "$namespace" exec "$pod" -- test ! -e /data/marker

echo "StatefulSet pod recreation confirmed /data emptyDir loss: context=$context namespace=$namespace"

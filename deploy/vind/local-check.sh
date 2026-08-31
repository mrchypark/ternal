#!/bin/sh
set -eu

name=${VCLUSTER_NAME:-ternal-vind-local-$$}

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 127
	fi
}

need docker
need kubectl
need vcluster

docker info >/dev/null

created=0
cleanup() {
	code=$?
	if [ "$created" -eq 1 ]; then
		vcluster disconnect >/dev/null 2>&1 || true
	fi
	if [ "$created" -eq 1 ] && [ -z "${VIND_KEEP_CLUSTER:-}" ]; then
		vcluster delete "$name" --driver docker --ignore-not-found >/dev/null 2>&1 || true
	elif [ "$created" -eq 1 ]; then
		echo "kept vCluster: $name"
	fi
	exit "$code"
}
trap cleanup EXIT INT TERM HUP

vcluster use driver docker
vcluster create "$name" --driver docker
created=1
vcluster connect "$name" --driver docker -- kubectl wait --for=condition=Ready node --all --timeout=180s
vcluster connect "$name" --driver docker -- kubectl get nodes

echo "vind local check passed: $name"

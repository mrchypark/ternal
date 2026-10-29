#!/bin/sh
# Actual Local CEL checks on an owned Linux kind API, never an ambient cluster.
set -eu

[ "$(uname -s)" = Linux ] || { echo 'Local admission runner requires Linux' >&2; exit 2; }
for tool in docker kind kubectl python3 timeout; do
	command -v "$tool" >/dev/null 2>&1 || { echo "missing required command: $tool" >&2; exit 127; }
done
[ "$(kind --version)" = 'kind version 0.31.0' ] || { echo 'kind v0.31.0 is required' >&2; exit 2; }

repo=$(CDPATH='' cd "$(dirname "$0")/../.." && pwd)
umask 077
work=$(mktemp -d "${TMPDIR:-/tmp}/ternal-local-admission.XXXXXX")
name=ternal-local-admission-$(date +%s)-$$
context=kind-$name
node_image=kindest/node:v1.34.0@sha256:3229b411042d457a44ea8807e90520e4b7da8700a8617ed20f0c4cb799bebe2b
image=ghcr.io/mrchypark/ternal@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export DOCKER_CONTEXT="${TERNAL_DOCKER_CONTEXT:-default}"
export KIND_EXPERIMENTAL_PROVIDER=docker
export KUBECONFIG="$work/kubeconfig"
export PYTHONDONTWRITEBYTECODE=1
unset DOCKER_HOST KIND_EXPERIMENTAL_DOCKER_NETWORK
created=0
dc() { timeout --kill-after=2s 30s docker --context "$DOCKER_CONTEXT" "$@"; }

capture_nodes() {
	dc ps -aq --filter "label=io.x-k8s.kind.cluster=$name" >"$work/nodes" || return 1
	while IFS= read -r id; do
		[ -n "$id" ] || continue
		[ "$(dc inspect --format '{{index .Config.Labels "io.x-k8s.kind.cluster"}}' "$id")" = "$name" ] || return 1
		dc inspect --format '{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}}{{"\n"}}{{end}}{{end}}' "$id" >>"$work/volumes" || return 1
	done <"$work/nodes"
}

cleanup() {
	original=$?
	trap - EXIT INT TERM HUP
	set +e
	cleanup_rc=0
	if [ "$created" = 1 ]; then
		capture_nodes >"$work/capture.stdout" 2>"$work/capture.stderr" || cleanup_rc=1
		if [ "$original" != 0 ]; then
			timeout --kill-after=5s 60s kind export logs "$work/kind-logs" --name "$name" >"$work/export.stdout" 2>"$work/export.stderr"
		fi
		timeout --kill-after=5s 90s kind delete cluster --name "$name" --kubeconfig "$KUBECONFIG" >"$work/delete.stdout" 2>"$work/delete.stderr" || cleanup_rc=1
		dc ps -aq --filter "label=io.x-k8s.kind.cluster=$name" >"$work/remaining-nodes" 2>"$work/remaining-nodes.stderr" || cleanup_rc=1
		[ ! -s "$work/remaining-nodes" ] || cleanup_rc=1
		if [ "$cleanup_rc" = 0 ]; then
			sort -u "$work/volumes" >"$work/owned-volumes"
			while IFS= read -r volume; do
				[ -n "$volume" ] || continue
				# Only a newly created volume captured on this exact owned node is eligible.
				if grep -Fxq "$volume" "$work/preexisting-volumes"; then continue; fi
				dc volume inspect "$volume" >"$work/volume-$volume.json" 2>"$work/volume-$volume.stderr"
				inspect_rc=$?
				if [ "$inspect_rc" = 1 ] && grep -Eiq 'no such (volume|object)|volume not found:' "$work/volume-$volume.stderr"; then continue; fi
				if [ "$inspect_rc" != 0 ]; then cleanup_rc=1; continue; fi
				dc ps -aq --filter "volume=$volume" >"$work/refs-$volume" 2>"$work/refs-$volume.stderr" || { cleanup_rc=1; continue; }
				if [ -s "$work/refs-$volume" ]; then cleanup_rc=1; continue; fi
				dc volume rm "$volume" >"$work/rm-$volume.stdout" 2>"$work/rm-$volume.stderr" || { cleanup_rc=1; continue; }
				dc volume inspect "$volume" >"$work/absent-$volume.stdout" 2>"$work/absent-$volume.stderr"
				if [ "$?" != 1 ] || ! grep -Eiq 'no such (volume|object)|volume not found:' "$work/absent-$volume.stderr"; then cleanup_rc=1; fi
			done <"$work/owned-volumes"
		fi
	fi
	rm -f "$KUBECONFIG"
	[ ! -e "$KUBECONFIG" ] || cleanup_rc=1
	printf '%s\n' "$cleanup_rc" >"$work/cleanup.exit"
	if [ "$original" = 0 ] && [ "$cleanup_rc" = 0 ]; then
		find "$work" -depth -delete
		echo 'Local CEL admission and owned-resource cleanup passed'
	else
		echo "Local admission diagnostics retained: $work (test=$original cleanup=$cleanup_rc)" >&2
	fi
	[ "$original" = 0 ] || exit "$original"
	exit "$cleanup_rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

printf '%s\n' "$name" >"$work/cluster-name"
: >"$work/volumes"
dc ps -aq --filter "label=io.x-k8s.kind.cluster=$name" >"$work/preexisting-nodes"
[ ! -s "$work/preexisting-nodes" ] || { echo 'refusing existing cluster identity' >&2; exit 2; }
dc volume ls -q >"$work/preexisting-volumes"
created=1
timeout --kill-after=10s 240s kind create cluster --name "$name" --image "$node_image" \
	--kubeconfig "$KUBECONFIG" --wait 180s --retain >"$work/create.stdout" 2>"$work/create.stderr"
capture_nodes
[ -s "$work/nodes" ] || { echo 'kind created no owned node' >&2; exit 1; }
timeout --kill-after=10s 600s python3 "$repo/deploy/security/trustguard-admission-check.py" "$image" --local \
	--kubeconfig "$KUBECONFIG" --context "$context" --diagnostics "$work/admission"

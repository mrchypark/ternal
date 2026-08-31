#!/bin/sh

apply_secret_from_files() (
	[ "$#" -gt 0 ] || {
		echo 'Secret name is required' >&2
		return 2
	}
	[ -n "${NAMESPACE:-}" ] || {
		echo 'NAMESPACE is required' >&2
		return 2
	}
	name=$1
	shift

	command -v jq >/dev/null 2>&1 || {
		echo 'missing command: jq' >&2
		return 1
	}

	run_kubectl() {
		if [ -n "${KUBE_CONTEXT:-}" ]; then
			kubectl --context "$KUBE_CONTEXT" "$@"
		else
			kubectl "$@"
		fi
	}

	old_umask=$(umask)
	umask 077
	secret_dir=$(mktemp -d "${TMPDIR:-/tmp}/k8s-secret.XXXXXX") || return 1
	trap 'rm -rf "$secret_dir"' 0 HUP INT TERM
	umask "$old_umask"

	desired_secret=$secret_dir/desired.json
	current_secret=$secret_dir/current.json
	run_kubectl -n "$NAMESPACE" create secret generic "$name" \
		"$@" --dry-run=client -o json >"$desired_secret"

	if run_kubectl -n "$NAMESPACE" get secret "$name" -o json >"$current_secret" 2>/dev/null; then
		jq --slurpfile desired "$desired_secret" '
			.data = $desired[0].data
			| .type = $desired[0].type
			| del(.metadata.annotations["kubectl.kubernetes.io/last-applied-configuration"])
		' "$current_secret" | run_kubectl replace -f - >/dev/null
	else
		run_kubectl create -f "$desired_secret" >/dev/null
	fi

	rm -rf "$secret_dir"
)

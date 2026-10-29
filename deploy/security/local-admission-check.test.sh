#!/bin/sh
# Source-contract assertions intentionally keep shell variable text literal.
# shellcheck disable=SC2016
set -eu
script=deploy/security/local-admission-check.sh
sh -n "$script"
# The fixture is Linux-only. Do not create a cluster merely to test orchestration.
grep -F '[ "$(uname -s)" = Linux ]' "$script" >/dev/null
grep -F "kind version 0.31.0" "$script" >/dev/null
grep -F 'kindest/node:v1.34.0@sha256:3229b411042d457a44ea8807e90520e4b7da8700a8617ed20f0c4cb799bebe2b' "$script" >/dev/null
grep -F 'export KUBECONFIG="$work/kubeconfig"' "$script" >/dev/null
grep -F 'unset DOCKER_HOST KIND_EXPERIMENTAL_DOCKER_NETWORK' "$script" >/dev/null
grep -F 'label=io.x-k8s.kind.cluster=$name' "$script" >/dev/null
grep -F 'volume=$volume' "$script" >/dev/null
grep -F 'grep -Fxq "$volume" "$work/preexisting-volumes"' "$script" >/dev/null
grep -F 'kind delete cluster --name "$name" --kubeconfig "$KUBECONFIG"' "$script" >/dev/null
grep -F '"$image" --local' "$script" >/dev/null
grep -F -- '--kubeconfig "$KUBECONFIG" --context "$context"' "$script" >/dev/null
if grep -Eq 'kind delete clusters|docker (system|volume|container) prune|kubectl config use-context' "$script"; then
	echo 'Local admission runner changes shared state' >&2; exit 1
fi
if [ "$(uname -s)" != Linux ]; then
	set +e
	sh "$script" > /dev/null 2>&1
	status=$?
	set -e
	[ "$status" = 2 ] || { echo 'non-Linux runner did not fail closed' >&2; exit 1; }
fi
echo 'Local admission runner static/OS guard checks passed; actual CEL requires Linux kind'

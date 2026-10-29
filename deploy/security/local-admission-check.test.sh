#!/bin/sh
# Source-contract assertions intentionally keep shell variable text literal.
# shellcheck disable=SC2016
set -eu
script=deploy/security/local-admission-check.sh
sh -n "$script"
# The fixture is Linux-only. Do not create a cluster merely to test orchestration.
grep -F '[ "$(uname -s)" = Linux ]' "$script" >/dev/null
grep -F "kind version 0.31.0" "$script" >/dev/null
grep -F 'kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48' "$script" >/dev/null
grep -F 'https://github.com/kubernetes-sigs/kind/releases/tag/v0.31.0' "$script" >/dev/null
grep -F 'NR > 120 { exit }' "$script" >/dev/null
grep -F 'export KUBECONFIG="$work/kubeconfig"' "$script" >/dev/null
grep -F 'unset DOCKER_HOST KIND_EXPERIMENTAL_DOCKER_NETWORK' "$script" >/dev/null
grep -F 'label=io.x-k8s.kind.cluster=$name' "$script" >/dev/null
grep -F 'volume=$volume' "$script" >/dev/null
grep -F 'grep -Fxq "$volume" "$work/preexisting-volumes"' "$script" >/dev/null
grep -F 'kind delete cluster --name "$name" --kubeconfig "$KUBECONFIG"' "$script" >/dev/null
grep -F '"$image" --local' "$script" >/dev/null
grep -F 'diagnostics_path=%s' "$script" >/dev/null
grep -F 'public=$work/public-diagnostics' "$script" >/dev/null
grep -F 'sanitize_log "$work/$file"' "$script" >/dev/null
grep -F 'redacted sensitive diagnostic line' "$script" >/dev/null
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
work=$(mktemp -d)
trap 'find "$work" -depth -delete' EXIT INT TERM HUP
# Exercise only the source's sanitizer, never its cluster entrypoint.
sed -n '/^sanitize_log() {/,/^}/p' "$script" >"$work/sanitizer.sh"
# shellcheck source=/dev/null
. "$work/sanitizer.sh"
printf '%s\n' 'ERROR: failed to pull node image' 'token=fixture-sensitive-value' \
	'kubeconfig: fixture-private-path' '-----BEGIN CERTIFICATE-----' \
	'fixture-pem-payload' '-----END CERTIFICATE-----' >"$work/raw"
printf '%096d\n' 0 >>"$work/raw"
sanitize_log "$work/raw" >"$work/safe"
grep -F 'ERROR: failed to pull node image' "$work/safe" >/dev/null
if grep -Eq 'fixture-sensitive-value|fixture-private-path|fixture-pem-payload|00000000000000000000' "$work/safe"; then
	echo 'Local diagnostic sanitizer leaked a sensitive fixture line' >&2; exit 1
fi
: >"$work/long"
i=0
while [ "$i" -lt 121 ]; do i=$((i + 1)); printf 'safe-line-%s\n' "$i" >>"$work/long"; done
sanitize_log "$work/long" >"$work/bounded"
[ "$(wc -l <"$work/bounded" | tr -d ' ')" = 120 ] || { echo 'Local diagnostics are not bounded' >&2; exit 1; }
echo 'Local admission runner static/OS guard checks passed; actual CEL requires Linux kind'

#!/bin/sh
set -eu

cd "$(CDPATH='' cd "$(dirname "$0")/.." && pwd)"

# POSIX ERE boundaries only: \b is not a word boundary in every regex engine
# git can be built against, and a pattern that matches nothing silently turns
# this gate into a no-op. The self-test below refuses to pass if one drifts.
dependency_name='(^|[^A-Z0-9_])TERNAL_[A-Z0-9_]*(RHIZA|RAUTHY|PIGEONS|IROH)[A-Z0-9_]*([^A-Z0-9_]|$)'
storage_engine_name='(^|[^A-Z0-9_])RHIZA_[A-Z0-9_]+([^A-Z0-9_]|$)'

# Assembled from parts so this fixture is not itself a gate violation.
engine=RHIZA

for pattern in "$dependency_name" "$storage_engine_name"; do
	printf 'TERNAL_DATA_%s_MODE\n%s_CLUSTER_ID\n' "$engine" "$engine" | grep -qE "$pattern" || {
		echo "naming gate pattern does not match its own fixture: $pattern" >&2
		exit 1
	}
done

if git grep -n -E "$dependency_name"; then
	echo 'Ternal-owned configuration must use role names, not dependency product names' >&2
	exit 1
fi

# Ternal-owned configuration keeps role names. The rhiza operator is the one
# exception: it runs the upstream binary, whose own interface is RHIZA_*
# (upstream rhiza.ConfigFromEnv: "the RHIZA_* environment used by the rhiza
# server binary"), and its controller also reads those names from the target
# pod's environment and rewrites them to start a recovered generation. Only the
# operator-owned chart files, the operator render check, the operator identity
# bridge that applies them to Ternal's configuration, and the operator guide may
# name them; every other file stays TERNAL_*.
if git grep -n -E "$storage_engine_name" -- . \
	':(exclude,glob)deploy/helm/ternal/templates/operator-*' \
	':(exclude,glob)deploy/vind/operator-*' \
	':(exclude,glob)internal/operatoridentity/*' \
	':(exclude)docs/rhiza-ha.md'; then
	echo 'storage implementation environment variables must not be public Ternal configuration' >&2
	exit 1
fi

echo 'Public configuration naming contract passed'

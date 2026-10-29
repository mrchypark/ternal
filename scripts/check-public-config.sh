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

if git grep --untracked -n -E "$dependency_name"; then
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
if git grep --untracked -n -E "$storage_engine_name" -- . \
	':(exclude,glob)deploy/helm/ternal/templates/operator-*' \
	':(exclude,glob)deploy/vind/operator-*' \
	':(exclude,glob)internal/operatoridentity/*' \
	':(exclude)internal/store/local_config.go' \
	':(exclude)docs/rhiza-ha.md'; then
	echo 'storage implementation environment variables must not be public Ternal configuration' >&2
	exit 1
fi

# One exact declaration records upstream names solely to reject them in Local
# mode. Inspect the rest of that file normally; positive reads are not exempt.
if test -f internal/store/local_config.go; then
	if ! local_config=$(awk '
		BEGIN {
			n = split("CLUSTER_ID NODE_ID CLUSTER_MEMBERS PEER_TOKENS ENABLE_RECONFIGURATION LEARNER ADMIN_TOKEN OBJSTORE_DURABILITY", names, " ")
			for (i = 1; i <= n; i++) allowed["RHIZA_" names[i]] = 1
		}
		/^var localRejectedUpstreamEnv = \[\]string\{$/ {
			if (block++) exit 1
			inside = 1; next
		}
		inside && /^}$/ {
			if (count != n) exit 1
			inside = 0; next
		}
		inside {
			if ($0 !~ /^[[:space:]]*"[A-Z0-9_]+",$/) exit 1
			name = $0; gsub(/[[:space:]",]/, "", name)
			if (!allowed[name] || seen[name]++) exit 1
			count++; next
		}
		{ print }
		END { if (inside || block != 1 || count != n) exit 1 }
	' internal/store/local_config.go); then
		echo 'Local upstream deny-list must contain exactly the reviewed rejected names' >&2
		exit 1
	fi
	if printf '%s\n' "$local_config" | grep -n -E "$storage_engine_name"; then
		echo 'upstream names outside the Local deny-list are not public configuration' >&2
		exit 1
	fi
fi

echo 'Public configuration naming contract passed'

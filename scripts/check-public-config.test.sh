#!/bin/sh
set -eu

repo=$(CDPATH='' cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT HUP INT TERM
mkdir -p "$work/scripts" "$work/internal/store"
cp "$repo/scripts/check-public-config.sh" "$work/scripts/"
cp "$repo/internal/store/local_config.go" "$work/internal/store/"
git init --quiet "$work"
git -C "$work" add scripts internal

check() { sh "$work/scripts/check-public-config.sh" >"$work/result.log" 2>&1; }
reject() {
	if check; then
		echo "public configuration gate accepted $1" >&2
		cat "$work/result.log" >&2
		exit 1
	fi
}

check
# Construct fixture names exactly as the gate's existing pattern self-test does.
engine=RHIZA
printf 'package fixture\nvar setting = os.Getenv("%s_CLUSTER_ID")\n' "$engine" >"$work/positive.go"
reject 'untracked positive use'
git -C "$work" add positive.go
reject 'tracked positive use'
git -C "$work" rm --cached --quiet -- positive.go
rm "$work/positive.go"

printf '\nfunc positive() string { return os.Getenv("%s_NODE_ID") }\n' "$engine" >>"$work/internal/store/local_config.go"
reject 'positive use next to the Local deny-list'
cp "$repo/internal/store/local_config.go" "$work/internal/store/local_config.go"

sed "/\"${engine}_NODE_ID\",/d" "$repo/internal/store/local_config.go" >"$work/internal/store/local_config.go"
reject 'an incomplete Local deny-list'
cp "$repo/internal/store/local_config.go" "$work/internal/store/local_config.go"

printf 'env: TERNAL_DATA_%s_URL\n' "$engine" >"$work/unsafe.yaml"
reject 'an untracked dependency-named public setting'
rm "$work/unsafe.yaml"
check
echo 'Public configuration regression checks passed'

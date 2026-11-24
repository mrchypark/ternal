#!/bin/sh
set -eu

repo=$(CDPATH='' cd "$(dirname "$0")/../.." && pwd)
verify=$repo/deploy/release/verify-candidate-capsule.sh
sha=0123456789abcdef0123456789abcdef01234567
run=1234567890
owner=example/ternal

names='ternal-agent-linux-amd64.tar.gz
ternal-agent-linux-arm64.tar.gz
ternalctl-linux-amd64.tar.gz
ternalctl-linux-arm64.tar.gz
ternalctl-macos-amd64.tar.gz
ternalctl-macos-arm64.tar.gz
ternalctl-windows-amd64.zip
ternalctl-windows-arm64.zip'

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

make_capsule() {
	dir=$1
	mkdir -p "$dir"
	digests=''
	for name in $names; do
		printf 'contents of %s\n' "$name" >"$dir/$name"
		digest=$(sha256sum "$dir/$name" | awk '{print $1}')
		digests="$digests$name $digest
"
	done
	(cd "$dir" && sha256sum $names >candidate-sha256.txt)
	printf '%s' "$digests" | jq -Rn --arg sha "$sha" --arg run "$run" --arg repo "$owner" '
		[inputs | split(" ") | {(.[0]): .[1]}] | add as $archives
		| {schema: "ternal-go-release-candidate-v1", repository: $repo, source_commit: $sha,
		   workflow_run_id: $run, expected_image: ("ghcr.io/" + ($repo | ascii_downcase) + ":sha-" + $sha),
		   archives: $archives}
	' >"$dir/candidate-provenance.json"
}

make_capsule "$work/valid"
sh "$verify" "$work/valid" "$sha" "$run" "$owner" >/dev/null
echo 'valid capsule accepted'

expect_rejected() {
	label=$1
	shift
	if sh "$verify" "$@" >/dev/null 2>&1; then
		echo "verifier accepted an unqualified capsule: $label" >&2
		exit 1
	fi
	echo "rejected: $label"
}

cp -R "$work/valid" "$work/tampered"
printf 'x' >>"$work/tampered/ternalctl-windows-arm64.zip"
expect_rejected 'tampered archive bytes' "$work/tampered" "$sha" "$run" "$owner"

expect_rejected 'capsule built for another commit' "$work/valid" 89abcdef0123456789abcdef0123456789abcdef "$run" "$owner"
expect_rejected 'capsule bound to another run' "$work/valid" "$sha" 999 "$owner"
expect_rejected 'capsule from another repository' "$work/valid" "$sha" "$run" example/other

cp -R "$work/valid" "$work/extra"
printf 'x' >"$work/extra/unexpected.tar.gz"
expect_rejected 'extra archive in capsule' "$work/extra" "$sha" "$run" "$owner"

cp -R "$work/valid" "$work/stale-provenance"
jq '.archives["ternalctl-windows-arm64.zip"] = "deadbeef"' \
	"$work/stale-provenance/candidate-provenance.json" >"$work/stale.json"
mv "$work/stale.json" "$work/stale-provenance/candidate-provenance.json"
expect_rejected 'provenance digest drift' "$work/stale-provenance" "$sha" "$run" "$owner"

echo 'candidate capsule verifier PASS'

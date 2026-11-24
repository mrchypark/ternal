#!/bin/sh
# Fail closed unless the capsule is the exact candidate built for this commit.
set -eu

usage() {
	echo "usage: $0 <candidate-dir> <commit-sha> <candidate-run-id> <owner/repo>" >&2
	exit 2
}

[ $# -eq 4 ] || usage
candidate=$1
sha=$2
run_id=$3
repository=$4

for command in jq sha256sum; do
	command -v "$command" >/dev/null 2>&1 || {
		echo "missing required command: $command" >&2
		exit 127
	}
done

case $sha in
'' | *[!0-9a-f]*)
	echo "invalid commit sha: $sha" >&2
	exit 1
	;;
esac
[ ${#sha} -eq 40 ] || {
	echo "invalid commit sha: $sha" >&2
	exit 1
}
case $run_id in
'' | *[!0-9]*)
	echo "invalid candidate run id: $run_id" >&2
	exit 1
	;;
esac

expected='ternal-agent-linux-amd64.tar.gz
ternal-agent-linux-arm64.tar.gz
ternalctl-linux-amd64.tar.gz
ternalctl-linux-arm64.tar.gz
ternalctl-macos-amd64.tar.gz
ternalctl-macos-arm64.tar.gz
ternalctl-windows-amd64.zip
ternalctl-windows-arm64.zip'

[ -d "$candidate" ] || {
	echo "candidate capsule is missing: $candidate" >&2
	exit 1
}
actual=$(cd "$candidate" && find . -maxdepth 1 -type f | sed 's|^\./||' | sort)
if [ "$actual" != "$(printf '%s\n' "$expected" candidate-provenance.json candidate-sha256.txt | sort)" ]; then
	echo "candidate capsule inventory does not match the qualified archive set" >&2
	exit 1
fi

recorded=$candidate/candidate-sha256.txt
provenance=$candidate/candidate-provenance.json
for file in "$recorded" "$provenance"; do
	[ -f "$file" ] || {
		echo "candidate capsule is missing $(basename "$file")" >&2
		exit 1
	}
done

if [ "$(awk '{print $2}' "$recorded" | sort)" != "$(printf '%s\n' "$expected" | sort)" ]; then
	echo "candidate-sha256.txt does not cover exactly the qualified archives" >&2
	exit 1
fi
(cd "$candidate" && sha256sum --check --strict --quiet candidate-sha256.txt) || {
	echo "candidate archive bytes do not match candidate-sha256.txt" >&2
	exit 1
}

jq -e --arg sha "$sha" --arg run "$run_id" --arg repo "$repository" '
	.schema == "ternal-go-release-candidate-v1"
	and .source_commit == $sha
	and .workflow_run_id == $run
	and .repository == $repo
	and .expected_image == ("ghcr.io/" + ($repo | ascii_downcase) + ":sha-" + $sha)
	and (.archives | length) == 8
' "$provenance" >/dev/null || {
	echo "candidate provenance does not describe a capsule built for this commit and run" >&2
	exit 1
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
jq -r '.archives | to_entries[] | "\(.value)  \(.key)"' "$provenance" | sort > "$tmp/provenance.txt"
sort "$recorded" > "$tmp/recorded.txt"
cmp -s "$tmp/provenance.txt" "$tmp/recorded.txt" || {
	echo "candidate provenance digests do not match candidate-sha256.txt" >&2
	exit 1
}

echo "candidate capsule verified: $sha run $run_id"

#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM HUP

cat >"$work/events.jsonl" <<'EOF'
not-json
{"schema":"other","event":"transport_changed","transport":"direct"}
{"schema":"pigeons.transport.v1","event":"transport_changed","transport":"relay"}
{"schema":"pigeons.transport.v1","event":"transport_changed","transport":"direct"}
EOF
test "$(sh "$script_dir/parse-transport-jsonl.sh" "$work/events.jsonl")" = direct

printf '%s\n' \
	'{"schema":"pigeons.transport.v1","event":"transport_changed","transport":"relay"}' \
	'{"schema":"pigeons.transport.v1","event":"transport_changed","transport":"unknown"}' \
	>"$work/ambiguous.jsonl"
test "$(sh "$script_dir/parse-transport-jsonl.sh" "$work/ambiguous.jsonl")" = unknown
test "$(sh "$script_dir/parse-transport-jsonl.sh" "$work/missing.jsonl")" = unknown

printf 'transport diagnostics JSONL parser self-test passed\n'

#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
	echo "usage: $0 DIAGNOSTICS_JSONL" >&2
	exit 2
fi
if ! command -v jq >/dev/null 2>&1; then
	echo "missing required command: jq" >&2
	exit 127
fi
if [ ! -f "$1" ]; then
	printf '%s\n' unknown
	exit 0
fi

jq -Rr '
  fromjson?
  | select(
      .schema == "pigeons.transport.v1"
      and .event == "transport_changed"
      and (.transport == "direct" or .transport == "relay" or .transport == "unknown")
    )
  | .transport
' "$1" | awk '
  { last = $0 }
  END {
    if (last == "") print "unknown"
    else print last
  }
'

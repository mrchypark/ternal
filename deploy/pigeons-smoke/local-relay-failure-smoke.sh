#!/bin/sh
set -eu

if [ "${TERNAL_SMOKE_HOOK:-}" != 1 ]; then
	script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
	TERNAL_SMOKE_POST_HEALTHY_HOOK="$script_dir/local-relay-failure-smoke.sh" \
		exec sh "$script_dir/local-relay-smoke.sh"
fi

: "${TERNAL_SMOKE_WORK:?missing smoke work directory}"
: "${TERNAL_SMOKE_RELAY_NAME:?missing relay container name}"
: "${TERNAL_SMOKE_RELAY_URL:?missing relay URL}"
: "${TERNAL_SMOKE_ENDPOINT:?missing endpoint ID}"
: "${TERNAL_TRANSPORT_BIN:?missing transport binary}"
: "${TERNAL_SMOKE_TIMEOUT_BIN:?missing timeout binary}"

outage_timeout=${TERNAL_SMOKE_OUTAGE_TIMEOUT:-15}
recovery_attempts=${TERNAL_SMOKE_RECOVERY_ATTEMPTS:-8}
recovery_timeout=${TERNAL_SMOKE_RECOVERY_TIMEOUT:-15}
outage_out="$TERNAL_SMOKE_WORK/outage-proxy.out"
outage_err="$TERNAL_SMOKE_WORK/outage-proxy.err"
recovery_out="$TERNAL_SMOKE_WORK/recovery-proxy.out"
recovery_err="$TERNAL_SMOKE_WORK/recovery-proxy.err"

docker stop "$TERNAL_SMOKE_RELAY_NAME" >/dev/null
if [ "$(docker inspect -f '{{.State.Running}}' "$TERNAL_SMOKE_RELAY_NAME")" != false ]; then
	echo "relay container did not stop" >&2
	exit 1
fi
printf 'relay outage observed: container=%s state=stopped\n' "$TERNAL_SMOKE_RELAY_NAME"

outage_status=0
"$TERNAL_SMOKE_TIMEOUT_BIN" "$outage_timeout" \
	"$TERNAL_TRANSPORT_BIN" fly --stdio "$TERNAL_SMOKE_ENDPOINT" \
	--relay-url "$TERNAL_SMOKE_RELAY_URL" \
	</dev/null >"$outage_out" 2>"$outage_err" || outage_status=$?

if head -n 1 "$outage_out" | grep '^SSH-' >/dev/null 2>&1; then
	printf 'relay-independent connectivity observed while managed relay was stopped; transport path is not observable with this pigeons version\n'
	direct_path=unverified
else
	direct_path=unavailable
	printf 'direct-path fallback unavailable: pigeons cannot distinguish platform/network reachability from direct-discovery limits (status=%s, timeout=%ss)\n' \
		"$outage_status" "$outage_timeout"
fi

docker start "$TERNAL_SMOKE_RELAY_NAME" >/dev/null
i=0
while [ "$i" -lt 80 ]; do
	nc -z 127.0.0.1 "${TERNAL_SMOKE_RELAY_URL##*:}" >/dev/null 2>&1 && break
	i=$((i + 1))
	sleep 0.25
done

i=0
while [ "$i" -lt "$recovery_attempts" ]; do
	: >"$recovery_out"
	: >"$recovery_err"
	"$TERNAL_SMOKE_TIMEOUT_BIN" "$recovery_timeout" \
		"$TERNAL_TRANSPORT_BIN" fly --stdio "$TERNAL_SMOKE_ENDPOINT" \
		--relay-url "$TERNAL_SMOKE_RELAY_URL" \
		</dev/null >"$recovery_out" 2>"$recovery_err" || true
	head -n 1 "$recovery_out" | grep '^SSH-' >/dev/null 2>&1 && break
	i=$((i + 1))
	sleep 2
done
if ! head -n 1 "$recovery_out" | grep '^SSH-' >/dev/null 2>&1; then
	echo "pigeons did not recover after relay restart" >&2
	cat "$recovery_err" >&2
	exit 1
fi

printf 'relay recovery observed: banner=%s\n' "$(head -n 1 "$recovery_out")"
printf 'pigeons relay failure smoke passed: direct_path=%s\n' "$direct_path"

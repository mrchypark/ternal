#!/bin/sh
set -eu

parent=deploy/e2e/rauthy-session-scenario.sh
for scenario in local-portal-smoke.sh local-user-scenario.sh local-cli-scenario.sh local-agent-scenario.sh; do
	path="deploy/e2e/$scenario"
	[ -x "$path" ] || { echo "$path is missing or not executable" >&2; exit 1; }
	sh -n "$path"
	grep -F "sh $path" "$parent" >/dev/null || { echo "$parent does not invoke $path" >&2; exit 1; }
done
sh -n "$parent"
grep -F 'batch_name="ied-greenfield-$(basename "$work")"' deploy/e2e/local-agent-scenario.sh >/dev/null || {
	echo 'local agent scenario must isolate its manufacturing batch across partial reruns' >&2
	exit 1
}

printf 'live E2E scenario entrypoints verified\n'

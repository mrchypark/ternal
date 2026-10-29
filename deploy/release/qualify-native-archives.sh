#!/bin/sh
# Qualify the extracted Unix candidate bytes without a provider or network.
set -eu

usage() { echo 'usage: qualify-native-archives.sh <platform> <cli-archive> [agent-archive]' >&2; exit 2; }
[ "$#" -ge 2 ] && [ "$#" -le 3 ] || usage
platform=$1
case "$(uname -s):$(uname -m)" in
	Linux:x86_64|Linux:amd64) host=linux-amd64 ;;
	Linux:aarch64|Linux:arm64) host=linux-arm64 ;;
	Darwin:x86_64) host=macos-amd64 ;;
	Darwin:arm64) host=macos-arm64 ;;
	*) echo 'unsupported native qualification host' >&2; exit 2 ;;
esac
[ "$platform" = "$host" ] || { echo 'native qualification platform mismatch' >&2; exit 2; }
case "$platform" in
	linux-*) [ "$#" = 3 ] || usage ;;
	macos-*) [ "$#" = 2 ] || usage ;;
esac
for tool in tar ssh-keygen awk cmp grep find; do
	command -v "$tool" >/dev/null 2>&1 || { echo "missing required command: $tool" >&2; exit 127; }
done
timeout_bin=$(command -v timeout || command -v gtimeout || true)
[ -n "$timeout_bin" ] || { echo 'timeout or gtimeout is required' >&2; exit 127; }
sha_bin=$(command -v sha256sum || command -v shasum || true)
[ -n "$sha_bin" ] || { echo 'sha256sum or shasum is required' >&2; exit 127; }
digest() {
	case "$sha_bin" in */sha256sum) "$sha_bin" "$1" ;; *) "$sha_bin" -a 256 "$1" ;; esac | awk '{print $1}'
}
absolute() { case "$1" in /*) printf '%s\n' "$1" ;; *) printf '%s/%s\n' "$(pwd -P)" "$1" ;; esac; }
cli_archive=$(absolute "$2")
agent_archive=
if [ "$#" = 3 ]; then agent_archive=$(absolute "$3"); fi
umask 077
work=$(mktemp -d "${TMPDIR:-/tmp}/ternal-native-qualification.XXXXXX")
phase=setup
cleanup() {
	status=$?
	trap - EXIT INT TERM HUP
	set +e
	# Private keys, transport identity and captured key-bearing output never survive.
	find "$work" -depth -delete
	cleanup_rc=$?
	[ ! -e "$work" ] || cleanup_rc=1
	if [ "$status" != 0 ] || [ "$cleanup_rc" != 0 ]; then
		echo "native archive qualification failed: phase=$phase status=$status cleanup=$cleanup_rc" >&2
	fi
	[ "$status" = 0 ] || exit "$status"
	exit "$cleanup_rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP
mkdir "$work/home" "$work/config" "$work/tmp" "$work/extracted"
run() {
	env -i PATH="$PATH" HOME="$work/home" TMPDIR="$work/tmp" XDG_CONFIG_HOME="$work/config" \
		TERNAL_CONFIG_DIR="$work/config" TERNAL_API_URL=http://127.0.0.1:1 \
		"$timeout_bin" --kill-after=2s 20s "$@"
}
fail() { echo "$1" >&2; exit 1; }

extract() {
	archive=$1 bundle=$2 executable=$3
	[ -f "$archive" ] || fail 'candidate archive is missing'
	tar -tzf "$archive" >"$work/members"
	while IFS= read -r member; do
		case "$member" in
			"$bundle/"|"$bundle/$executable"|"$bundle/pigeons"|"$bundle/LICENSE.pigeons"|"$bundle/README.txt") ;;
			*) fail 'candidate archive has an unexpected or unsafe member' ;;
		esac
	done <"$work/members"
	[ "$(sort "$work/members" | uniq | wc -l | tr -d ' ')" = 5 ] || fail 'candidate archive inventory is incomplete'
	[ "$(wc -l <"$work/members" | tr -d ' ')" = 5 ] || fail 'candidate archive has duplicate members'
	# Reject links/devices before extraction, not after a possible link traversal.
	tar -tvzf "$archive" >"$work/member-types"
	awk 'substr($0,1,1) != "-" && substr($0,1,1) != "d" {exit 1}' "$work/member-types" || fail 'candidate archive contains a link or special file'
	tar -xzf "$archive" -C "$work/extracted"
	for binary in "$executable" pigeons; do
		[ -f "$work/extracted/$bundle/$binary" ] && [ -x "$work/extracted/$bundle/$binary" ] && \
			[ ! -L "$work/extracted/$bundle/$binary" ] || fail 'candidate binary is not a regular executable'
	done
}

phase='cli-archive'
cli_digest=$(digest "$cli_archive")
extract "$cli_archive" "ternalctl-$platform" ternalctl
cli=$work/extracted/ternalctl-$platform/ternalctl
phase='cli-session-refusal'
if run "$cli" whoami >"$work/whoami.stdout" 2>"$work/whoami.stderr"; then fail 'candidate CLI accepted a missing session'; else status=$?; fi
if [ "$status" != 1 ] || ! grep -q 'not logged in' "$work/whoami.stderr"; then fail 'candidate CLI did not reject its empty isolated session'; fi

phase='cli-host-key'
run ssh-keygen -q -t ed25519 -N '' -C native-qualification -f "$work/host-key"
fingerprint=$(ssh-keygen -E sha256 -lf "$work/host-key.pub" | awk '{print $2}')
read -r key_type key_data _ <"$work/host-key.pub"
run "$cli" known-host-key "$fingerprint" HOSTNAME "$fingerprint" "$key_type" "$key_data" >"$work/known-host.stdout" 2>"$work/known-host.stderr"
printf '* %s %s\n' "$key_type" "$key_data" >"$work/known-host.expected"
cmp -s "$work/known-host.expected" "$work/known-host.stdout" || fail 'candidate CLI did not emit the correct pinned host key'
wrong=SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
[ "$wrong" != "$fingerprint" ] || wrong=SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB
if run "$cli" known-host-key "$wrong" HOSTNAME "$fingerprint" "$key_type" "$key_data" >"$work/wrong-host.stdout" 2>"$work/wrong-host.stderr"; then fail 'candidate CLI accepted the wrong host fingerprint'; else status=$?; fi
if [ "$status" != 1 ] || ! grep -q 'fingerprint mismatch' "$work/wrong-host.stderr"; then fail 'candidate CLI failed without fingerprint rejection evidence'; fi

qualify_transport() {
	transport=$1 role=$2
	first=$(run "$transport" endpoint-id --key-dir "$work/$role-identity" 2>"$work/$role-endpoint.stderr")
	second=$(run "$transport" endpoint-id --key-dir "$work/$role-identity" 2>>"$work/$role-endpoint.stderr")
	[ "${#first}" = 64 ] || fail 'candidate transport returned an invalid endpoint ID'
	printf '%s\n' "$first" | grep -Eq '^[0-9a-fA-F]{64}$' || fail 'candidate transport returned an invalid endpoint ID'
	[ "$first" = "$second" ] || fail 'candidate transport did not preserve its endpoint ID'
	[ -n "$(find "$work/$role-identity" -type f -print)" ] || fail 'candidate transport did not persist its identity'
}
phase='cli-transport-identity'
qualify_transport "$work/extracted/ternalctl-$platform/pigeons" cli

agent_digest=
if [ -n "$agent_archive" ]; then
	phase='agent-archive'
	agent_digest=$(digest "$agent_archive")
	extract "$agent_archive" "ternal-agent-$platform" ternal-agent
	agent=$work/extracted/ternal-agent-$platform/ternal-agent
	phase='agent-transport-identity'
	qualify_transport "$work/extracted/ternal-agent-$platform/pigeons" agent
	phase='agent-exclusive-key'
	run "$agent" device-keygen "$work/device.key" >"$work/device.stdout" 2>"$work/device.stderr"
	[ -s "$work/device.key" ] || fail 'candidate agent did not create its device key'
	if mode=$(stat -c '%a' "$work/device.key" 2>/dev/null); then :; else mode=$(stat -f '%Lp' "$work/device.key"); fi
	[ "$mode" = 600 ] || fail 'candidate agent device key is not mode 0600'
	cp "$work/device.key" "$work/device.before"
	if run "$agent" device-keygen "$work/device.key" >"$work/device-repeat.stdout" 2>"$work/device-repeat.stderr"; then fail 'candidate agent overwrote an existing device key'; else status=$?; fi
	if [ "$status" != 1 ] || ! grep -q 'already exists' "$work/device-repeat.stderr"; then fail 'candidate agent did not explicitly refuse an existing key'; fi
	cmp -s "$work/device.before" "$work/device.key" || fail 'candidate agent changed the refused device key'
	if mode=$(stat -c '%a' "$work/device.key" 2>/dev/null); then :; else mode=$(stat -f '%Lp' "$work/device.key"); fi
	[ "$mode" = 600 ] || fail 'candidate agent changed the refused device key permissions'
fi
phase='archive-identity'
[ "$(digest "$cli_archive")" = "$cli_digest" ] || fail 'CLI archive changed during qualification'
if [ -n "$agent_archive" ]; then [ "$(digest "$agent_archive")" = "$agent_digest" ] || fail 'agent archive changed during qualification'; fi
printf 'qualified-native-archives platform=%s cli_sha256=%s' "$platform" "$cli_digest"
if [ -n "$agent_archive" ]; then printf ' agent_sha256=%s' "$agent_digest"; fi
printf '\n'

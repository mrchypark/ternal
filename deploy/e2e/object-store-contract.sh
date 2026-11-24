#!/bin/sh
set -eu
exec 3>&2

usage() { printf '%s\n' 'usage: object-store-contract.sh [--plan] s3|gcs|azure' '       object-store-contract.sh --check-fixture LOG seed|verify VERSION false|true' >&2; exit 2; }
fixture_passed() {
  [ -f "$1" ] && [ -r "$1" ] || return 1
  case "$2:$3:$4" in seed:v0.21.2:false|verify:v0.27.4:false|verify:v0.27.4:true) ;; *) return 1 ;; esac
  passes=$(grep -Ec '^--- PASS: TestRhizaUpgradeFixture \([0-9]+([.][0-9]+)?s\)$' "$1" || true)
  [ "$passes" = 1 ] || return 1
  if grep -Eq '^--- (SKIP|FAIL): TestRhizaUpgradeFixture([ /]|$)|no tests to run' "$1"; then return 1; fi
  sentinel=$(sed -n 's/^[[:space:]]*rhiza_upgrade_test.go:[0-9][0-9]*: \(Ternal fixture .*\)$/\1/p' "$1")
  [ "$sentinel" = "Ternal fixture $2 passed: dependency=$3 Go=go1.27.4 cold=$4" ] || return 1
  grep -Fx PASS "$1" >/dev/null
}
if [ "${1-}" = --check-fixture ]; then
  [ "$#" -eq 5 ] || usage
  fixture_passed "$2" "$3" "$4" "$5"
  exit
fi
plan=0
if [ "${1-}" = --plan ]; then plan=1; shift; fi
[ "$#" -eq 1 ] || usage
provider=$1
engine=${TERNAL_CONTRACT_ENGINE:-docker}
case "$engine" in docker|apple) ;; *) usage ;; esac
case $(uname -m) in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; *) usage ;; esac
case "$provider:$arch" in
  s3:amd64) image=floci/floci@sha256:7ab87e9a83909072aceb33d7026f8127a5f5dd693a974e0d30a0474eafcf91b9; port=4566; hostport=56466 ;;
  s3:arm64) image=floci/floci@sha256:3df9677724e36251801f3828ea0bdcc2818afa96d8ec53f1741eaffdaeb990e1; port=4566; hostport=56466 ;;
  gcs:amd64) image=floci/floci-gcp@sha256:042c0c6d29b992691ea640bfce91554329d29004cfe4b68aa2093da23e06784f; port=4588; hostport=56488 ;;
  gcs:arm64) image=floci/floci-gcp@sha256:2799bd4688838420a8987d13383303714bc056eb013c27de9d1e98195390523f; port=4588; hostport=56488 ;;
  azure:amd64) image=floci/floci-az@sha256:10272631bd93144520cba0141c9e71beba72b6bccc1c5266a3a2cfdc164b2225; port=4577; hostport=56477 ;;
  azure:arm64) image=floci/floci-az@sha256:98afec5b0e41c58ebb7c4f17b876fac84f51b37bea7413064dd1d4f57cce9a30; port=4577; hostport=56477 ;;
  *) usage ;;
esac
if [ "$plan" -eq 1 ]; then
  printf 'provider=%s\nengine=%s\narch=%s\nimage=%s\nport=%s\n' "$provider" "$engine" "$arch" "$image" "$port"
  exit 0
fi

root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd -P)
GO=${GO:-$(GOTOOLCHAIN=go1.27.4 go env GOROOT)/bin/go}
[ "$("$GO" version | cut -d ' ' -f 3)" = go1.27.4 ] || { printf '%s\n' 'Go 1.27.4 is required' >&2; exit 1; }
release_json=$(GOWORK=off "$GO" mod download -json github.com/mrchypark/rhiza@v0.27.4)
printf '%s\n' "$release_json" | jq -e \
  '.Path == "github.com/mrchypark/rhiza" and
   .Version == "v0.27.4" and
   .Sum == "h1:Ahu7+37UsIPRWIIb8kqkPnLeM42SS/1SG7C3ewFuJHw=" and
   .GoModSum == "h1:Xj44Kx/DKbSSMegwSLElrY4Lyho4kq2hmFLN8mk4jS4="' >/dev/null
modcache=$("$GO" env GOMODCACHE)
gocache=$("$GO" env GOCACHE)
owner="tc-$provider-$$-$(date +%s)"
name=$owner
network=$owner
umask 077
work=$(mktemp -d "${TMPDIR:-/tmp}/ternal-provider-$provider.XXXXXX")
work=$(CDPATH='' cd -- "$work" && pwd -P)
mkdir "$work/home" "$work/tmp"
container_owned=0
network_owned=0
network_attempted=0
network_absent_before=0
remove_work() {
  find "$work" -type l -exec rm -f {} +
  find "$work" -type f -exec rm -f {} +
  find "$work" -depth -type d -exec rmdir {} +
  [ ! -e "$work" ]
}
cleanup() {
  original=$?
  trap - EXIT INT TERM HUP
  set +e
  failed=0
  if [ "$engine" = docker ]; then
    ids=$(docker ps -aq --filter "name=^/$name$") || failed=1
    if [ -n "$ids" ]; then
      label=$(docker inspect --format '{{index .Config.Labels "ternal.contract.owner"}}' "$name")
      if [ "$label" = "$owner" ] && [ "$container_owned" -eq 1 ]; then
        if [ "$original" -ne 0 ]; then docker logs "$name" > "$work/emulator.log" 2>&1; fi
        docker rm --force "$name" >/dev/null || failed=1
      else failed=1; fi
    fi
    ids=$(docker ps -aq --filter "name=^/$name$") || failed=1
    [ -z "$ids" ] || failed=1
    ids=$(docker network ls -q --filter "name=^$network$") || failed=1
    if [ -n "$ids" ]; then
      label=$(docker network inspect --format '{{index .Labels "ternal.contract.owner"}}' "$network")
      if [ "$label" = "$owner" ] && [ "$network_owned" -eq 1 ]; then docker network rm "$network" >/dev/null || failed=1; else failed=1; fi
    fi
    ids=$(docker network ls -q --filter "name=^$network$") || failed=1
    [ -z "$ids" ] || failed=1
  else
    all=$(container list --all --format json) || failed=1
    count=$(printf '%s' "$all" | jq --arg n "$name" '[.[]|select(.configuration.id==$n)]|length')
    if [ "$count" = 1 ]; then
      label=$(printf '%s' "$all" | jq -r --arg n "$name" '.[]|select(.configuration.id==$n)|.configuration.labels["ternal.contract.owner"]')
      state=$(printf '%s' "$all" | jq -r --arg n "$name" '.[]|select(.configuration.id==$n)|.status.state')
      if [ "$label" = "$owner" ] && [ "$container_owned" -eq 1 ]; then
        if [ "$original" -ne 0 ]; then container logs "$name" > "$work/emulator.log" 2>&1; fi
        if [ "$state" = running ]; then container stop --time 10 "$name" >/dev/null || failed=1; fi
        container delete "$name" >/dev/null || failed=1
      else failed=1; fi
    elif [ "$count" != 0 ]; then failed=1; fi
    all=$(container list --all --format json) || failed=1
    printf '%s' "$all" | jq -e --arg n "$name" '[.[]|select(.configuration.id==$n)]|length==0' >/dev/null || failed=1
    all=$(container network list --format json) || failed=1
    count=$(printf '%s' "$all" | jq --arg n "$network" '[.[]|select(.id==$n)]|length')
    if [ "$count" = 1 ]; then
      meta=$(container network inspect "$network") || failed=1
      label=$(printf '%s' "$meta" | jq -r '.[0].configuration.labels["ternal.contract.owner"]')
      if [ "$label" = "$owner" ] && [ "$network_attempted" -eq 1 ] && [ "$network_absent_before" -eq 1 ]; then
        container network delete "$network" >/dev/null || failed=1
      else failed=1; fi
    elif [ "$count" != 0 ]; then failed=1; fi
    all=$(container network list --format json) || failed=1
    printf '%s' "$all" | jq -e --arg n "$network" '[.[]|select(.id==$n)]|length==0' >/dev/null || failed=1
  fi
  if [ "$original" -ne 0 ] || [ "$failed" -ne 0 ]; then
    diagnostics=$(mktemp -d "${TMPDIR:-/tmp}/ternal-provider-diagnostics-$provider.XXXXXX")
    printf 'owner=%s\ncontainer=%s\nnetwork=%s\nimage=%s\ncleanup_failed=%s\n' "$owner" "$name" "$network" "$image" "$failed" > "$diagnostics/run.txt"
    for file in "$work"/*.jsonl "$work"/*.stderr "$work"/*-build.txt "$work"/*.log; do
      [ -f "$file" ] || continue
      tail -c 262144 "$file" > "$diagnostics/$(basename "$file")"
    done
    remove_work || { failed=1; printf 'Temporary cleanup incomplete: %s\n' "$work" >&3; }
    printf 'Provider diagnostics retained: %s\n' "$diagnostics" >&3
    if [ -n "${GITHUB_OUTPUT:-}" ]; then printf 'diagnostics_path=%s\n' "$diagnostics" >> "$GITHUB_OUTPUT"; fi
    [ "$original" -eq 0 ] || exit "$original"
    exit 1
  fi
  # All files are generated under this private directory; never delete a caller path.
  remove_work || exit 1
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

if [ "$engine" = docker ]; then
  [ -z "$(docker ps -aq --filter "name=^/$name$")" ]
  [ -z "$(docker network ls -q --filter "name=^$network$")" ]
  docker network create --internal --label "ternal.contract.owner=$owner" "$network" >/dev/null
  network_owned=1
  container_owned=1
  docker run --detach --name "$name" --network "$network" --platform "linux/$arch" \
    --user 1001:0 --cap-drop ALL --label "ternal.contract.owner=$owner" \
    --publish "127.0.0.1::$port" "$image" > "$work/container-id"
  mapping=$(docker port "$name" "$port/tcp")
  hostport=${mapping##*:}
else
  all=$(container network list --format json)
  printf '%s' "$all" | jq -e --arg n "$network" '[.[]|select(.id==$n)]|length==0' >/dev/null
  network_absent_before=1
  network_attempted=1
  container network create --internal --label "ternal.contract.owner=$owner" "$network" >/dev/null
  network_owned=1
  container_owned=1
  container run --detach --name "$name" --network "$network" --memory 1G --cpus 2 \
    --user 1001:0 --cap-drop ALL --no-dns --label "ternal.contract.owner=$owner" \
    --publish "127.0.0.1:$hostport:$port" "docker.io/$image" > "$work/container-id"
fi
case "$hostport" in ''|*[!0-9]*) exit 1 ;; esac
endpoint="http://127.0.0.1:$hostport"
bucket="ternal-contract-$owner"
attempt=0
while [ "$attempt" -lt 30 ]; do
  if curl --fail --silent --show-error --max-time 2 "$endpoint/health" > "$work/health.json" 2>/dev/null; then break; fi
  attempt=$((attempt + 1))
  sleep 1
done
curl --fail --silent --show-error --max-time 2 "$endpoint/health" > "$work/health.json"
case "$provider" in
  s3) curl --fail --silent --show-error --max-time 5 -X PUT "$endpoint/$bucket" > "$work/provision.json" ;;
  gcs) curl --fail --silent --show-error --max-time 5 -H 'Content-Type: application/json' --data "{\"name\":\"$bucket\"}" "$endpoint/storage/v1/b?project=ternal-contract" > "$work/provision.json" ;;
  azure) curl --fail --silent --show-error --max-time 5 -X PUT -H 'x-ms-version: 2023-11-03' "$endpoint/devstoreaccount1/$bucket?restype=container" > "$work/provision.json" ;;
esac

go_env() {
  env -i PATH="$PATH" HOME="$work/home" TMPDIR="$work/tmp" GOMODCACHE="$modcache" GOCACHE="$gocache" \
    GOTOOLCHAIN=local GOWORK=off AWS_EC2_METADATA_DISABLED=true "$@"
}
test_env() {
  go_env env TERNAL_TEST_PROVIDER="$provider" TERNAL_TEST_ENDPOINT="$endpoint" TERNAL_TEST_BUCKET="$bucket" \
    TERNAL_TEST_AZURE_STORAGE_ACCOUNT_KEY='dGVzdC1lbXVsYXRvci1vbmx5LWtleS0wMTIzNDU2Nzg5MDE=' "$@"
}
cd "$root"
for race in normal race; do
  if [ "$race" = race ]; then set -- -race; else set --; fi
  test_env "$GO" test -mod=readonly "$@" -count=1 -timeout=10m -json -run '^TestRhizaProviderContract$' ./internal/store > "$work/$race.jsonl" 2> "$work/$race.stderr"
  jq -se 'any(.[]; .Test=="TestRhizaProviderContract" and .Action=="pass") and (all(.[]; .Action!="skip"))' "$work/$race.jsonl" >/dev/null
  printf 'provider=%s suite=%s PASS\n' "$provider" "$race"
done

baseline=441415a8b1b6911afb5e405ae3f8611e5b98c5ab
mkdir "$work/old"
git archive --format=tar --output="$work/baseline.tar" "$baseline"
tar -xf "$work/baseline.tar" -C "$work/old"
cp internal/store/rhiza_provider_contract_test.go internal/store/rhiza_upgrade_test.go "$work/old/internal/store/"
(cd "$work/old"; go_env "$GO" test -mod=readonly -c -o "$work/old.test" ./internal/store)
go_env "$GO" test -mod=readonly -c -o "$work/new.test" ./internal/store
go_env "$GO" version -m "$work/old.test" > "$work/old-build.txt"
go_env "$GO" version -m "$work/new.test" > "$work/new-build.txt"
grep -F 'go1.27.4' "$work/old-build.txt" "$work/new-build.txt"
grep -Fx "$(printf '\tdep\tgithub.com/mrchypark/rhiza\tv0.21.2\th1:q1bAEo2pHpm7dBpSEGWZ4VjbQk++dtB62sB8BKu7dn0=')" "$work/old-build.txt"
grep -Fx "$(printf '\tdep\tgithub.com/mrchypark/rhiza\tv0.27.4\th1:Ahu7+37UsIPRWIIb8kqkPnLeM42SS/1SG7C3ewFuJHw=')" "$work/new-build.txt"
for mode in warm cold; do
  mkdir "$work/$mode"
  prefix="upgrade/$owner/$mode"
  test_env env TERNAL_UPGRADE_PHASE=seed TERNAL_UPGRADE_EXPECTED_VERSION=v0.21.2 TERNAL_UPGRADE_ROOT="$work/$mode" TERNAL_UPGRADE_PREFIX="$prefix" \
    "$work/old.test" -test.v -test.timeout=4m -test.run '^TestRhizaUpgradeFixture$' > "$work/$mode-old.log" 2>&1
  fixture_passed "$work/$mode-old.log" seed v0.21.2 false
  cold=0
  [ "$mode" != cold ] || cold=1
  test_env env TERNAL_UPGRADE_PHASE=verify TERNAL_UPGRADE_EXPECTED_VERSION=v0.27.4 TERNAL_UPGRADE_ROOT="$work/$mode" TERNAL_UPGRADE_PREFIX="$prefix" TERNAL_UPGRADE_COLD="$cold" \
    "$work/new.test" -test.v -test.timeout=4m -test.run '^TestRhizaUpgradeFixture$' > "$work/$mode-new.log" 2>&1
  cold_bool=false
  [ "$cold" -ne 1 ] || cold_bool=true
  fixture_passed "$work/$mode-new.log" verify v0.27.4 "$cold_bool"
  printf 'provider=%s upgrade=v0.21.2-to-v0.27.4 mode=%s PASS\n' "$provider" "$mode"
done

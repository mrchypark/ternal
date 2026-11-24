#!/bin/sh
set -eu
case $(basename "$0") in
  go)
    case "$1:${2-}" in
      version:) printf '%s\n' 'go version go1.27.4 mock' ;;
      env:GOMODCACHE|env:GOCACHE) printf '%s\n' "$CONTRACT_MOCK_DIR/cache" ;;
      mod:download)
        [ "${3-}" = -json ] && [ "${4-}" = github.com/mrchypark/rhiza@v0.27.4 ] || exit 64
        printf '%s\n' '{"Path":"github.com/mrchypark/rhiza","Version":"v0.27.4","Sum":"h1:Ahu7+37UsIPRWIIb8kqkPnLeM42SS/1SG7C3ewFuJHw=","GoModSum":"h1:Xj44Kx/DKbSSMegwSLElrY4Lyho4kq2hmFLN8mk4jS4="}'
        exit 0
        ;;
      *) exit 64 ;;
    esac
    exit 0
    ;;
  container)
    printf '%s\n' "$*" >> "$CONTRACT_MOCK_DIR/calls"
    case "$1:${2-}" in
      list:*) printf '%s\n' '[]' ;;
      network:list)
        if [ -f "$CONTRACT_MOCK_DIR/network.json" ]; then jq -s '.' "$CONTRACT_MOCK_DIR/network.json"; else printf '%s\n' '[]'; fi
        ;;
      network:create)
        shift 2
        label=''
        name=''
        while [ "$#" -gt 0 ]; do
          if [ "$1" = --label ]; then shift; label=$1; fi
          name=$1
          shift
        done
        case "$CONTRACT_MOCK_CASE" in
          owned) owner=${label#ternal.contract.owner=} ;;
          unknown) owner=foreign-owner ;;
          absent) exit 17 ;;
          *) exit 64 ;;
        esac
        jq -n --arg n "$name" --arg o "$owner" '{id:$n,configuration:{name:$n,labels:{"ternal.contract.owner":$o}}}' > "$CONTRACT_MOCK_DIR/network.json"
        exit 17
        ;;
      network:inspect)
        jq -e --arg n "$3" '.id==$n' "$CONTRACT_MOCK_DIR/network.json" >/dev/null
        jq -s '.' "$CONTRACT_MOCK_DIR/network.json"
        ;;
      network:delete)
        jq -e --arg n "$3" '.id==$n' "$CONTRACT_MOCK_DIR/network.json" >/dev/null
        rm -f "$CONTRACT_MOCK_DIR/network.json"
        ;;
      *) exit 64 ;;
    esac
    exit 0
    ;;
esac
root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd -P)
runner=$root/deploy/e2e/object-store-contract.sh
for provider in s3 gcs azure; do
  plan=$(sh "$runner" --plan "$provider")
  printf '%s\n' "$plan" | grep -Fx "provider=$provider" >/dev/null
  printf '%s\n' "$plan" | grep -E '^image=floci/floci(-gcp|-az)?@sha256:[0-9a-f]{64}$' >/dev/null
done
for provider in '' filesystem remote; do
  if sh "$runner" --plan "$provider" >/dev/null 2>&1; then exit 1; fi
done
if TERNAL_CONTRACT_ENGINE=unknown sh "$runner" --plan s3 >/dev/null 2>&1; then exit 1; fi
printf '%s\n' 'provider plan validation PASS'

umask 077
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ternal-contract-gates.XXXXXX")
tmp=$(CDPATH='' cd -- "$tmp" && pwd -P)
cleanup() {
  original=$?
  trap - EXIT INT TERM HUP
  if [ "$original" -ne 0 ]; then printf 'Gate diagnostics retained: %s\n' "$tmp" >&2; exit "$original"; fi
  find "$tmp" -type l -exec rm -f {} +
  find "$tmp" -type f -exec rm -f {} +
  find "$tmp" -depth -type d -exec rmdir {} +
  [ ! -e "$tmp" ]
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

write_valid_log() {
  printf '%s\n' '=== RUN   TestRhizaUpgradeFixture' \
    "    rhiza_upgrade_test.go:99: Ternal fixture $2 passed: dependency=$3 Go=go1.27.4 cold=$4" \
    '--- PASS: TestRhizaUpgradeFixture (0.01s)' PASS > "$1"
}
for spec in seed:v0.21.2:false verify:v0.27.4:false verify:v0.27.4:true; do
  phase=${spec%%:*}
  rest=${spec#*:}
  version=${rest%%:*}
  cold=${rest#*:}
  write_valid_log "$tmp/valid.log" "$phase" "$version" "$cold"
  sh "$runner" --check-fixture "$tmp/valid.log" "$phase" "$version" "$cold"
done
printf '' > "$tmp/empty.log"
printf '%s\n' 'testing: warning: no tests to run' PASS > "$tmp/no-test.log"
printf '%s\n' '=== RUN   TestRhizaUpgradeFixture' '--- SKIP: TestRhizaUpgradeFixture (0.00s)' PASS > "$tmp/skip.log"
write_valid_log "$tmp/wrong-phase.log" verify v0.21.2 false
write_valid_log "$tmp/wrong-version.log" seed v0.27.4 false
write_valid_log "$tmp/wrong-cold.log" seed v0.21.2 true
printf '%s\n' '    rhiza_upgrade_test.go:99: Ternal fixture seed passed: dependency=v0.21.2 Go=go1.27.4 cold=false' '--- PASS: TestOtherFixture (0.01s)' PASS > "$tmp/wrong-name.log"
for invalid in empty no-test skip wrong-phase wrong-version wrong-cold wrong-name; do
  if sh "$runner" --check-fixture "$tmp/$invalid.log" seed v0.21.2 false; then printf 'invalid fixture accepted: %s\n' "$invalid" >&2; exit 1; fi
done
printf '%s\n' 'upgrade fixture pass/skip/sentinel gates PASS'

mkdir "$tmp/bin"
ln -s "$root/deploy/e2e/object-store-contract.test.sh" "$tmp/bin/go"
ln -s "$root/deploy/e2e/object-store-contract.test.sh" "$tmp/bin/container"
for scenario in owned unknown absent; do
  directory="$tmp/$scenario"
  mkdir "$directory" "$directory/tmp" "$directory/cache"
  status=0
  CONTRACT_MOCK_DIR="$directory" CONTRACT_MOCK_CASE="$scenario" PATH="$tmp/bin:$PATH" \
    GO="$tmp/bin/go" TMPDIR="$directory/tmp" TERNAL_CONTRACT_ENGINE=apple GITHUB_OUTPUT="$directory/github-output" \
    sh "$runner" s3 > "$directory/output.log" 2>&1 || status=$?
  [ "$status" -eq 17 ]
  diagnostics=$(sed -n 's/^diagnostics_path=//p' "$directory/github-output")
  [ -f "$diagnostics/run.txt" ]
  grep -F 'network create ' "$directory/calls" >/dev/null
  grep -F 'network list ' "$directory/calls" >/dev/null
  case "$scenario" in
    owned)
      [ ! -e "$directory/network.json" ]
      grep -F 'network delete ' "$directory/calls" >/dev/null
      grep -Fx 'cleanup_failed=0' "$diagnostics/run.txt" >/dev/null
      ;;
    unknown)
      [ -f "$directory/network.json" ]
      if grep -F 'network delete ' "$directory/calls" >/dev/null; then exit 1; fi
      grep -Fx 'cleanup_failed=1' "$diagnostics/run.txt" >/dev/null
      ;;
    absent)
      [ ! -e "$directory/network.json" ]
      if grep -F 'network delete ' "$directory/calls" >/dev/null; then exit 1; fi
      grep -Fx 'cleanup_failed=0' "$diagnostics/run.txt" >/dev/null
      ;;
  esac
done
printf '%s\n' 'Apple create-then-error ownership/absence gates PASS'

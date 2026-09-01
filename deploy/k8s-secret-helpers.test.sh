#!/bin/sh
set -eu

repo_dir=$(CDPATH='' cd -P "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/ternal-k8s-secret-test.XXXXXX")
trap 'rm -rf "$tmp"' 0 HUP INT TERM

fail() {
	echo "k8s Secret helper test failed: $1" >&2
	exit 1
}

mkdir -p "$tmp/bin" "$tmp/helper-tmp"
cat >"$tmp/bin/kubectl" <<'EOF'
#!/bin/sh
set -eu

{
	printf 'kubectl'
	printf ' <%s>' "$@"
	printf '\n'
} >>"$MOCK_KUBECTL_LOG"

previous=
name=
file_spec=
manifest_file=
type=Opaque
for argument do
	[ "$previous" != generic ] || name=$argument
	[ "$previous" != -f ] || manifest_file=$argument
	case $argument in
		--from-file=*) file_spec=${argument#--from-file=} ;;
		--type=*) type=${argument#--type=} ;;
	esac
	previous=$argument
done

case " $* " in
	*' create secret generic '*)
		key=${file_spec%%=*}
		path=${file_spec#*=}
		encoded=$(base64 <"$path" | tr -d '\n')
		jq -nc --arg name "$name" --arg namespace "$NAMESPACE" \
			--arg type "$type" --arg key "$key" --arg value "$encoded" \
			'{apiVersion:"v1",kind:"Secret",metadata:{name:$name,namespace:$namespace},type:$type,data:{($key):$value}}'
		;;
	*' get secret fixture-secret -o json '*)
		[ "$MOCK_SECRET_EXISTS" = 1 ] || exit 1
		printf '%s\n' '{"apiVersion":"v1","kind":"Secret","metadata":{"name":"fixture-secret","namespace":"ternal","resourceVersion":"42","annotations":{"keep":"yes","kubectl.kubernetes.io/last-applied-configuration":"old-secret-copy"}},"type":"Opaque","data":{"TOKEN":"b2xk"}}'
		;;
	*' create -f '*)
		cp "$manifest_file" "$MOCK_CAPTURE"
		;;
	*' replace -f - '*)
		cat >"$MOCK_CAPTURE"
		;;
	*)
		echo "unexpected kubectl command: $*" >&2
		exit 90
		;;
esac
EOF
chmod +x "$tmp/bin/kubectl"

secret_value=secret-value-must-not-reach-argv-or-output
printf '%s' "$secret_value" >"$tmp/source"
printf '%s\n' "$secret_value" >"$tmp/source-with-newline"

if sh -c '. "$1"; apply_secret_from_files fixture-secret --from-file="TOKEN=$2"' \
	fixture "$repo_dir/deploy/k8s-secret-helpers.sh" "$tmp/source" \
	>"$tmp/missing-namespace.out" 2>&1; then
	fail 'helper accepted a missing NAMESPACE'
fi
grep -Fq 'NAMESPACE is required' "$tmp/missing-namespace.out" ||
	fail 'missing NAMESPACE did not produce a useful error'

run_fixture() {
	exists=$1
	capture=$2
	log=$3
	output=$4
	: >"$log"
	PATH="$tmp/bin:$PATH" TMPDIR="$tmp/helper-tmp" \
		NAMESPACE=ternal KUBE_CONTEXT=fixture-context MOCK_SECRET_EXISTS="$exists" \
		MOCK_CAPTURE="$capture" MOCK_KUBECTL_LOG="$log" \
		sh -c '. "$1"; apply_secret_from_files fixture-secret --from-file="TOKEN=$2"' \
			fixture "$repo_dir/deploy/k8s-secret-helpers.sh" "$tmp/source" \
			>"$output" 2>&1
}

run_fixture 0 "$tmp/created.json" "$tmp/create.log" "$tmp/create.out"
jq -e '
	.metadata.name == "fixture-secret" and
	.metadata.namespace == "ternal" and
	.data.TOKEN == "c2VjcmV0LXZhbHVlLW11c3Qtbm90LXJlYWNoLWFyZ3Ytb3Itb3V0cHV0"
' "$tmp/created.json" >/dev/null || fail 'new Secret was not created from the file-backed value'
grep -q 'kubectl <--context> <fixture-context> <create> <-f>' "$tmp/create.log" ||
	fail 'new Secret did not use direct kubectl create'

PATH="$tmp/bin:$PATH" TMPDIR="$tmp/helper-tmp" \
	NAMESPACE=ternal KUBE_CONTEXT=fixture-context MOCK_SECRET_EXISTS=0 \
	MOCK_CAPTURE="$tmp/validated.json" MOCK_KUBECTL_LOG="$tmp/validated.log" \
	sh -c '. "$1"; apply_secret_from_files fixture-secret --from-file="TERNAL_RELAY_ACCESS_TOKEN=$2"' \
		fixture "$repo_dir/deploy/k8s-secret-helpers.sh" "$tmp/source" \
		>"$tmp/validated.out" 2>&1 || {
	cat "$tmp/validated.out" >&2
	fail 'helper rejected a valid relay token file'
}

if PATH="$tmp/bin:$PATH" TMPDIR="$tmp/helper-tmp" \
	NAMESPACE=ternal KUBE_CONTEXT=fixture-context MOCK_SECRET_EXISTS=0 \
	MOCK_CAPTURE="$tmp/rejected.json" MOCK_KUBECTL_LOG="$tmp/rejected.log" \
	sh -c '. "$1"; apply_secret_from_files fixture-secret --from-file="TERNAL_RELAY_ACCESS_TOKEN=$2"' \
		fixture "$repo_dir/deploy/k8s-secret-helpers.sh" "$tmp/source-with-newline" \
		>"$tmp/rejected.out" 2>&1; then
	fail 'helper accepted a relay token containing a newline'
fi
grep -Fq 'TERNAL_RELAY_ACCESS_TOKEN must not contain CR or LF bytes' "$tmp/rejected.out" ||
	fail 'newline rejection did not produce a useful error'

run_fixture 1 "$tmp/replaced.json" "$tmp/replace.log" "$tmp/replace.out"
jq -e '
	.metadata.resourceVersion == "42" and
	.metadata.annotations.keep == "yes" and
	(.metadata.annotations | has("kubectl.kubernetes.io/last-applied-configuration") | not) and
	.data.TOKEN == "c2VjcmV0LXZhbHVlLW11c3Qtbm90LXJlYWNoLWFyZ3Ytb3Itb3V0cHV0"
' "$tmp/replaced.json" >/dev/null ||
	fail 'replacement did not preserve resourceVersion or remove last-applied data'
grep -q 'kubectl <--context> <fixture-context> <replace> <-f> <->' "$tmp/replace.log" ||
	fail 'existing Secret did not use direct kubectl replace'

if grep -Fq apply "$tmp/create.log" "$tmp/replace.log"; then
	fail 'helper still uses kubectl apply'
fi
if grep -Fq "$secret_value" "$tmp/create.log" "$tmp/replace.log" \
	"$tmp/create.out" "$tmp/replace.out"; then
	fail 'helper exposed a raw Secret through argv or output'
fi
if find "$tmp/helper-tmp" -name 'k8s-secret.*' -print | grep -q .; then
	fail 'helper left private temporary files behind'
fi

echo 'Kubernetes Secret create/replace handling verified'

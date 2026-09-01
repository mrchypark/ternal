#!/bin/sh
set -eu

repo=$(CDPATH='' cd "$(dirname "$0")/.." && pwd)
cd "$repo"

for command in go git sh; do
	command -v "$command" >/dev/null 2>&1 || {
		echo "missing required command: $command" >&2
		exit 127
	}
done

./frontend/build.sh
git diff --exit-code -- internal/web/static
./scripts/check-public-config.sh
test -z "$(gofmt -l cmd internal)"
go test -race -count=1 -timeout=10m ./...
go vet ./...
mkdir -p dist/bin
go build -trimpath -o dist/bin/ternal-api ./cmd/ternal-api
go build -trimpath -o dist/bin/ternalctl ./cmd/ternalctl
go build -trimpath -o dist/bin/ternal-agent ./cmd/ternal-agent

sh deploy/backup/local-backup.test.sh
sh deploy/backup/local-backup-restore.test.sh
sh deploy/k8s-secret-helpers.test.sh
sh deploy/e2e/local-go-smoke.sh
sh deploy/agent/build-pigeons-native.test.sh
sh deploy/agent/package-linux-amd64.test.sh
sh deploy/cli/package-unix.test.sh
sh deploy/pigeons-smoke/parse-transport-jsonl.test.sh
sh deploy/pigeons-smoke/transport-matrix.test.sh
python3 deploy/release/release-candidate-workflow.test.py
if command -v helm >/dev/null 2>&1; then
	sh deploy/vind/helm-check.sh
else
	echo "helm unavailable: chart verification skipped" >&2
fi

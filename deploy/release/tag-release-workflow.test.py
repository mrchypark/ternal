#!/usr/bin/env python3
import pathlib
import re
import sys


workflow = pathlib.Path(".github/workflows/release.yml").read_text()
args = sys.argv[1:]
if args == ["--failure-fixture"]:
    workflow = workflow.replace(
        '(cd "$(dirname "$chart")" && sha256sum "$(basename "$chart")") > helm-chart-sha256.txt',
        'sha256sum "$chart" > helm-chart-sha256.txt',
    ).replace(
        "if recorded_chart != [chart_digest, chart.name]:",
        'if recorded_chart != [chart_digest, f"dist/release/{chart.name}"]:',
    )
elif args not in ([], ["--valid-fixture"]):
    raise SystemExit("usage: tag-release-workflow.test.py [--failure-fixture|--valid-fixture]")

if not workflow.startswith('name: Tag release\n\non:\n  push:\n    tags: ["v*"]\n'):
    raise SystemExit("tag release workflow must only start from a v* tag push")

for forbidden in ("workflow_dispatch:", "pull_request:", "schedule:", "workflow_call:", "cloudbuild", "google-github-actions"):
    if forbidden in workflow:
        raise SystemExit(f"tag release workflow contains forbidden trigger or backend: {forbidden}")

uses = re.findall(r"^\s*-?\s*uses:\s*([^\s]+)", workflow, re.MULTILINE)
allowed_local = "./.github/workflows/native-release-assets.yml"
if not uses or any(
    action != allowed_local and re.fullmatch(r"[^@]+@[0-9a-f]{40}", action) is None
    for action in uses
):
    raise SystemExit("every external tag release action must use a full commit pin")
if workflow.count(f"uses: {allowed_local}") != 1:
    raise SystemExit("tag release must call the local native build exactly once")

ci = pathlib.Path(".github/workflows/ci.yaml").read_text()
local_job = re.search(r"(?ms)^  local-admission:\n(.*?)(?=^  [a-zA-Z0-9_-]+:\n|\Z)", ci)
if local_job is None:
    raise SystemExit("CI must run Local admission as a separate mandatory job")
local_source = local_job.group(1)
if re.search(r"(?m)^    (?:needs|if|continue-on-error):", local_source) or "secrets." in local_source:
    raise SystemExit("CI Local admission must run independently without optional gates or secrets")
for required in (
    "runs-on: ubuntu-24.04",
    "timeout-minutes: 20",
    "uses: actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0",
    "persist-credentials: false",
    "command -v docker",
    "command -v python3",
    "command -v timeout",
    "https://get.helm.sh/helm-v4.2.3-linux-amd64.tar.gz",
    "e9b88b4ee95b18c706839c28d3a0220e5bc470e9cd9262410c90793c45ff8b7c",
    "https://github.com/mikefarah/yq/releases/download/v4.53.3/yq_linux_amd64",
    "fa52a4e758c63d38299163fbdd1edfb4c4963247918bf9c1c5d31d84789eded4",
):
    if required not in local_source:
        raise SystemExit(f"independent CI Local admission prerequisite is missing: {required}")
test_job = re.search(r"(?ms)^  test:\n(.*?)(?=^  [a-zA-Z0-9_-]+:\n|\Z)", ci)
if test_job is None or "sh deploy/security/local-admission-check.sh" in test_job.group(1):
    raise SystemExit("CI must preserve the separate test job without serial Local admission")
test_source = test_job.group(1)
if re.search(r"(?m)^    (?:needs|if|continue-on-error):", test_source):
    raise SystemExit("CI test and Local admission jobs must both remain independent and mandatory")
for required in (
    "./scripts/check-public-config.sh",
    "sh scripts/check-public-config.test.sh",
    "go vet ./...",
    "./frontend/build.sh",
    "go test -race -count=1 -timeout=10m ./...",
    "sh deploy/release/qualify-native-archives.test.sh",
    "sh deploy/vind/helm-render-check.sh",
    'docker build --tag "$image" --label "ternal.probe.owner=$owner" .',
):
    if required not in test_source:
        raise SystemExit(f"separate CI test job lost a mandatory gate: {required}")
for name, source in (("CI", local_source), ("tag release", workflow)):
    for required in (
        "https://github.com/kubernetes-sigs/kind/releases/download/v0.31.0/kind-linux-amd64",
        "eb244cbafcc157dff60cf68693c14c9a75c4e6e6fedaf9cd71c58117cb93e3fa",
        "https://dl.k8s.io/release/v1.34.0/bin/linux/amd64/kubectl",
        "cfda68cba5848bc3b6c6135ae2f20ba2c78de20059f68789c090166d6abc3e2c",
    ):
        if required not in source:
            raise SystemExit(f"{name} Local admission tool pin is missing: {required}")
    call = "          sh deploy/security/local-admission-check.sh"
    if source.splitlines().count(call) != 1:
        raise SystemExit(f"{name} must run actual Local admission exactly once without a shell skip")
    step = next(part for part in re.split(r"(?m)^      - name: ", source) if call in part)
    if re.search(r"(?m)^\s*(?:if|continue-on-error):", step):
        raise SystemExit(f"{name} Local admission must not be optional")
    if "          sh deploy/security/local-admission-check.test.sh" not in step:
        raise SystemExit(f"{name} Local admission wrapper regression must run before the gate")
    if "        id: local_admission" not in step:
        raise SystemExit(f"{name} Local admission must expose its safe diagnostic output")
    uploads = [part for part in re.split(r"(?m)^      - name: ", source) if "          name: local-admission-" in part]
    if len(uploads) != 1:
        raise SystemExit(f"{name} must preserve one sanitized Local admission failure artifact")
    for required in (
        "if: ${{ always() && steps.local_admission.outcome != 'success' && steps.local_admission.outputs.diagnostics_path != '' }}",
        "uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02",
        "path: ${{ steps.local_admission.outputs.diagnostics_path }}",
        "if-no-files-found: error",
        "retention-days: 7",
    ):
        if required not in uploads[0]:
            raise SystemExit(f"{name} sanitized Local diagnostic upload contract is missing: {required}")

for required in (
    'docker build --tag "$image" --label "ternal.probe.owner=$owner" .',
    "for command in local-preflight local-restore; do",
    'timeout --kill-after=5s 60s docker run --name "$name"',
    '--label "ternal.probe.owner=$owner" --rm --network none --read-only --cap-drop=ALL',
    "test \"$status\" -eq 1",
    "ternal-api: local-preflight requires TERNAL_DATA_LOCAL=1",
):
    if required not in ci:
        raise SystemExit(f"CI scratch command probe is missing: {required}")
probe = next(part for part in re.split(r"(?m)^      - name: ", ci) if 'docker build --tag "$image" --label' in part)
if re.search(r"(?m)^\s*(?:if|continue-on-error):", probe):
    raise SystemExit("CI scratch command probe must not be optional")

for required in (
    "IMAGE_NAME: ghcr.io/mrchypark/ternal",
    "packages: write",
    "id-token: write",
    "platforms: linux/amd64,linux/arm64",
    "${{ env.IMAGE_NAME }}:${{ github.ref_name }}",
    "${{ env.IMAGE_NAME }}:sha-${{ github.sha }}",
    "helm package deploy/helm/ternal",
    'chart="dist/release/ternal-$version.tgz"',
    '(cd "$(dirname "$chart")" && sha256sum "$(basename "$chart")") > helm-chart-sha256.txt',
    "helm-chart-sha256.txt",
    "--fail-on high",
    "-e DOCKER_CONFIG=/auth",
    '-v "$HOME/.docker/config.json:/auth/config.json:ro"',
    "sbom-amd64.spdx.json",
    "sbom-arm64.spdx.json",
    "cosign sign --yes",
    "cosign verify",
    "docker buildx imagetools create",
    "contents: write",
    "needs:\n      - native\n      - release",
    "release-provenance.json",
    '"pigeons_repository": build["PIGEONS_REPOSITORY"]',
    '"pigeons_cargo_lock_sha256": build["PIGEONS_CARGO_LOCK_SHA256"]',
    "release-sha256.txt",
    "deploy/security/render_trustguard_test.py",
    "dist/release/ternal-trustguard.py",
    "ternal-trustguard.py",
    'gh release create "$GITHUB_REF_NAME" release/*',
    'gh release edit "$GITHUB_REF_NAME" --draft=false',
    "ternalctl-windows-arm64.zip",
    "ternal-agent-linux-amd64.tar.gz",
    "ternal-agent-linux-arm64.tar.gz",
):
    if required not in workflow:
        raise SystemExit(f"tag release security contract is missing: {required}")

release_tag = "${{ env.IMAGE_NAME }}:${{ github.ref_name }}"
if workflow.index(release_tag) < workflow.index("cosign verify"):
    raise SystemExit("release tag must be promoted only after scan and sign gates pass")

if workflow.count("-e DOCKER_CONFIG=/auth") != 2:
    raise SystemExit("both private-registry scanners must use the mounted Docker auth directory")
if "/root/.docker/config.json" in workflow:
    raise SystemExit("scanner authentication must not assume the container runs as root")
if re.search(r"(?:tar -tzf|unzip -Z1).*\| grep -Eq", workflow):
    raise SystemExit("archive inventory checks must consume the full pipe under pipefail")
if 'f"dist/release/{chart.name}"' in workflow:
    raise SystemExit("published Helm checksum must use the downloadable asset basename")

inventories = re.findall(r"expected='(.*?)'\n\s+actual=", workflow, re.DOTALL)
if len(inventories) != 2:
    raise SystemExit("tag release must verify both staged and published asset inventories")
for inventory in inventories:
    names = [line.strip() for line in inventory.splitlines()]
    normalized = [re.sub(r"^ternal-.*\.tgz$", "ternal-0.tgz", name) for name in names]
    if normalized != sorted(normalized):
        raise SystemExit("tag release expected asset inventory must be lexically sorted")

print("GitHub Actions tag release workflow contract passed")

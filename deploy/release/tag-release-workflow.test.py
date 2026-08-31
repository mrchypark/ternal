#!/usr/bin/env python3
import pathlib
import re


workflow = pathlib.Path(".github/workflows/release.yml").read_text()

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

for required in (
    "IMAGE_NAME: ghcr.io/mrchypark/ternal",
    "packages: write",
    "id-token: write",
    "platforms: linux/amd64,linux/arm64",
    "${{ env.IMAGE_NAME }}:${{ github.ref_name }}",
    "${{ env.IMAGE_NAME }}:sha-${{ github.sha }}",
    "helm package deploy/helm/ternal",
    'chart="dist/release/ternal-$version.tgz"',
    "helm-chart-sha256.txt",
    "--fail-on high",
    "-e DOCKER_CONFIG=/auth",
    '-v "$HOME/.docker/config.json:/auth/config.json:ro"',
    "sbom-amd64.spdx.json",
    "sbom-arm64.spdx.json",
    "cosign sign --yes",
    "cosign verify",
    "contents: write",
    "needs:\n      - native\n      - release",
    "release-provenance.json",
    "release-sha256.txt",
    'gh release create "$GITHUB_REF_NAME" release/*',
    'gh release edit "$GITHUB_REF_NAME" --draft=false',
    "ternalctl-windows-arm64.zip",
    "ternal-agent-linux-amd64.tar.gz",
):
    if required not in workflow:
        raise SystemExit(f"tag release security contract is missing: {required}")

if workflow.count("-e DOCKER_CONFIG=/auth") != 2:
    raise SystemExit("both private-registry scanners must use the mounted Docker auth directory")
if "/root/.docker/config.json" in workflow:
    raise SystemExit("scanner authentication must not assume the container runs as root")

print("GitHub Actions tag release workflow contract passed")

#!/usr/bin/env python3
import pathlib
import re


workflow = pathlib.Path(".github/workflows/release-candidate.yaml").read_text()
native = pathlib.Path(".github/workflows/native-release-assets.yml").read_text()
windows_pigeons = pathlib.Path("deploy/agent/build-pigeons-windows.ps1").read_text()
combined = workflow + native

expected_header = """name: Go release candidate archives

on:
  workflow_dispatch:

permissions:
  contents: read
"""
if not workflow.startswith(expected_header):
    raise SystemExit("release candidate workflow must remain manual with read-only contents permission")

for forbidden in (
    "inputs:",
    "pull_request:",
    "push:",
    "schedule:",
    "workflow_call:",
    "id-token:",
    "packages:",
    "cloudbuild",
    "google-github-actions",
):
    if forbidden in workflow:
        raise SystemExit(f"release candidate workflow contains forbidden capability: {forbidden}")

if not native.startswith("name: Native release assets\n\non:\n  workflow_call:\n\npermissions:\n  contents: read\n"):
    raise SystemExit("native release workflow must be reusable with read-only contents permission")

uses = re.findall(r"^\s*-?\s*uses:\s*([^\s]+)$", combined, re.MULTILINE)
allowed_local = "./.github/workflows/native-release-assets.yml"
if not uses or any(
    action != allowed_local and re.fullmatch(r"[^@]+@[0-9a-f]{40}", action) is None
    for action in uses
):
    raise SystemExit("every external release candidate action must use a full commit pin")
if workflow.count(f"uses: {allowed_local}") != 1:
    raise SystemExit("release candidate must call the local native build exactly once")
if native.count("-buildvcs=false") != 3:
    raise SystemExit("every native Go release build must disable ref-dependent VCS stamping")
if windows_pigeons.count("-C link-arg=/Brepro") != 2:
    raise SystemExit("Windows pigeons builds must disable PE timestamp stamping")

archives = (
    "ternal-agent-linux-amd64.tar.gz",
    "ternal-agent-linux-arm64.tar.gz",
    "ternalctl-linux-amd64.tar.gz",
    "ternalctl-linux-arm64.tar.gz",
    "ternalctl-macos-amd64.tar.gz",
    "ternalctl-macos-arm64.tar.gz",
    "ternalctl-windows-amd64.zip",
    "ternalctl-windows-arm64.zip",
)
for archive in archives:
    minimum = 1 if archive.startswith("ternal-agent-") else 2
    if combined.count(archive) < minimum:
        raise SystemExit(f"release candidate inventory is not bound end-to-end: {archive}")

if "pattern:" in workflow or "merge-multiple:" in workflow:
    raise SystemExit("release candidate downloads must bind each expected artifact by exact name")
if 'glob("ternal-*"' in workflow or "sha256sum ternal-*" in workflow:
    raise SystemExit("release candidate provenance must not use a partial artifact-name glob")
for artifact in (archive.removesuffix(".tar.gz").removesuffix(".zip") for archive in archives):
    if f"name: {artifact}\n          path: candidate" not in workflow:
        raise SystemExit(f"release candidate download is not bound by exact artifact name: {artifact}")

for required in (
    'test "$GITHUB_REF" = refs/heads/main',
    "test \"$(git rev-parse HEAD)\" = \"$GITHUB_SHA\"",
    "cmp \"$RUNNER_TEMP/first-${{ matrix.archive }}\" \"dist/${{ matrix.archive }}\"",
    "Windows archive is not reproducible",
    '"source_commit": os.environ["GITHUB_SHA"]',
    '"pigeons_patch_sha256"',
    "archive_paths = sorted(path for path in candidate.iterdir() if path.is_file())",
    "candidate-sha256.txt",
    'name: ternal-agent-${{ matrix.platform }}',
    'path: dist/ternal-agent-${{ matrix.platform }}.tar.gz',
):
    if required not in combined:
        raise SystemExit(f"release candidate identity or reproducibility guard is missing: {required}")

print("GitHub Actions release candidate workflow contract passed")

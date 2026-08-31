# Releasing the Go port

The previous v0.2.0 RC manifests and qualification workflow belonged to the
pre-port application and were removed. Their digests must never be reused for
this breaking runtime.

This release accepts only an empty Go data directory. Startup rejects known
Ternal tables without the Go schema marker; there is no in-place importer or
mixed-version rollback. Qualification must use fresh disposable state,
start from a fresh schema, and enroll devices again. Restoring a pre-cutover or
older trust database is unsupported until a security-epoch recovery protocol
is implemented and reviewed.

1. Merge an exact reviewed Go commit after GitHub Actions is green.
2. Build and qualify the Go CLI/agent candidate archives from that commit with
   the manual candidate workflow. It builds the pinned pigeons transport from
   its checked source and patch.
3. Push the intended `v*` tag for that exact commit. The tag workflow rebuilds
   the same native archives, publishes the API image, packages the Helm chart,
   and creates a GitHub Release only after every asset is present and verified.
4. Record archive, image, chart, source commit, pigeons source/patch, and
   generated frontend asset digests without recording credentials.
5. Require the tag-built archives to match the qualified candidate bytes.
6. Qualify the exact release bytes and tagged image digest in a disposable
   greenfield target with the
   provider, relay, SSH, rollback, and isolation gates in `verification.md`.
7. Download the published assets and verify `release-sha256.txt` before use.

GitHub Actions is the only supported hosted build path. The manual
`release-candidate.yaml` workflow has no inputs or automatic/reusable trigger,
uses only read-only repository permission, builds every CLI platform natively,
and uploads a tagless candidate capsule. It rebuilds each archive from the same
native binaries and requires byte equality before binding archive, source,
pigeons source, and pigeons patch digests in the capsule provenance. It does
not authenticate to a cloud provider, publish a GitHub release, create a tag,
or deploy anything.

The API image and GitHub Release are published by `release.yml` only when a
`v*` tag is pushed.
The workflow publishes both the version tag and the exact-commit
`ghcr.io/mrchypark/ternal:sha-<commit>` tag, scans the multi-platform image,
creates SPDX SBOMs, and keyless-signs the resulting digest. It also packages
`deploy/helm/ternal` with the tag version and records the chart archive SHA-256.
The release contains native CLI bundles for Linux, macOS, and Windows on amd64
and arm64, the Linux amd64 agent bundle, the chart, SBOMs, signature evidence,
checksums, and source/pigeons provenance. It is created as a draft, its exact
asset inventory is checked, and only then is it published. The workflow does
not deploy.

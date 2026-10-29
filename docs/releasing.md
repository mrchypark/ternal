# Releasing the Go port

The previous v0.2.0 RC manifests and qualification workflow belonged to the
pre-port application and were removed. Their digests must never be reused for
this breaking runtime.

The legacy pre-port cutover accepts only an empty Go data directory. Startup
rejects known Ternal tables without the Go schema marker; there is no in-place
importer or mixed-version rollback. Qualify that cutover with fresh disposable
state, a fresh schema, and newly enrolled devices. This does not prohibit the
separate [Local init and recovery contract](local-single-node.md): a compatible
seeded Local archive may restore a fresh DataDir, and init/main reopen the same
restored DataDir with matching identity and external trust. It does not migrate
pre-cutover, old-format, unknown nonempty, or HA roots into Local state. Preserve
those original roots and authorities; never clear evidence to bypass refusal.

1. Merge an exact reviewed Go commit only after every mandatory GitHub Actions
   job for that head is green, including both CI `test` and `local-admission`.
2. Build and qualify the Go CLI/agent candidate archives from that commit with
   the manual candidate workflow. It builds the pinned pigeons transport from
   its content-addressed fork source.
3. Push the intended `v*` tag for that exact commit. The tag workflow rebuilds
   the same native archives, publishes the API image, packages the Helm chart,
   and creates a GitHub Release only after every asset is present and verified.
4. Record archive, image, chart, source commit, pigeons repository/source/lock,
   and generated frontend asset digests without recording credentials.
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
pigeons repository, source, and lockfile digests in the capsule provenance. It does
not authenticate to a cloud provider, publish a GitHub release, create a tag,
or deploy anything.

CI runs the full test/scratch checks and actual Local admission in independent,
mandatory jobs so admission diagnostics do not wait for the full build loop.
A passing `test` job alone does not permit merging. Tag release admission remains
a mandatory sequential gate before image publication.

The shared native workflow qualifies the actual extracted Linux/macOS archives
before upload, for both manual candidates and tag releases. It checks isolated
unauthenticated `whoami` refusal, host-key fingerprint acceptance and rejection,
and persistent pigeons endpoint identity. Linux agent archives additionally
check device-key creation with mode 0600 and refusal to overwrite an existing
key without changing it. Linux archives also execute endpoint identity and
device-key creation in the network-disabled Ubuntu 24.04/glibc 2.39 baseline.
These are local native baseline checks, not real OIDC login, relay/SSH connection,
production-cloud recovery, or Windows functional qualification. Those separate
environment-bound gates remain required where applicable.

The API image and GitHub Release are published by `release.yml` only when a
`v*` tag is pushed.
The workflow publishes both the version tag and the exact-commit
`ghcr.io/mrchypark/ternal:sha-<commit>` tag, scans the multi-platform image,
creates SPDX SBOMs, and keyless-signs the resulting digest. It also packages
`deploy/helm/ternal` with the tag version and records the chart archive SHA-256.
The release contains native CLI bundles for Linux, macOS, and Windows on amd64
and arm64, Linux agent bundles for both architectures, the chart, SBOMs,
signature evidence,
checksums, and source/pigeons provenance. It is created as a draft, its exact
asset inventory is checked, and only then is it published. The workflow does
not deploy.

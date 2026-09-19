Ternal v0.3.15 publishes the same source qualified as v0.3.15-rc.8.

### Changes

- Protect durable no-PVC deployments from stale object-store recovery with an external Kubernetes trust anchor and admission guard for approved API image digests.
- Complete fenced writes safely when an HTTP caller disconnects; unresolved outcomes remain fail-closed.
- Close active SSH sessions when devices are revoked and handle remote EOF correctly in the embedded Pigeons transport.
- Consume batch-linked, one-use manufacturing tokens and batch quotas atomically, with sequential serial allocation.
- Isolate CLI sessions with `TERNAL_CONFIG_DIR` and bind SSH relay grants to the persistent client endpoint.
- Fix same-origin browser logout and mobile sign-out access; restore the original logo and favicon.

### Qualification

The rc8 source was exercised with public artifacts, real Rauthy authentication, public CLI/agent bundles, and relay/direct SSH. Checks included enrollment quotas and expiry, session logout, 256KiB half-close streams, actual SSH host-key mismatch, active-session revocation, restart recovery, and rejection of a complete historical object-store restore.

The exact live HTTP-cancellation-at-pending-CAS timing remained inconclusive because writes completed before the cancellation signal. Deterministic regression and race tests cover that boundary. Some unchanged OIDC rejection, key ownership/normalization, and authorized-keys atomic rollback cases use prior public-candidate evidence rather than a full rc8 rerun.

### Artifacts and deployment

Download the Helm chart, CLI/agent bundles, `ternal-trustguard.py`, checksums, provenance, SBOMs, and image signature verification from this release. API image: `ghcr.io/mrchypark/ternal:v0.3.15`; prefer the immutable digest in `image-digest.txt`.

Persistent deployments use certified object storage and an independently provisioned trust anchor/admission guard. PVC-backed `/data` is unsupported; `/data` is an emptyDir cache. Keep the external trust anchor when upgrading, and do not restore it together with historical application data.

Published artifact verification passed all 16 checksums, chart metadata, provenance, and cosign signature checks. All eight native bundles and `ternal-trustguard.py` are byte-for-byte identical to the qualified rc8 assets.

Verified API image: `ghcr.io/mrchypark/ternal@sha256:95702428b5fd6822f751611e1e2f83b32d5b9e17d8268a22f3cccb2aa0c2c9ee`.

[Full changelog](https://github.com/mrchypark/ternal/compare/v0.3.14...v0.3.15)

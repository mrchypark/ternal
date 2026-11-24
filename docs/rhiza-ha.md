# Ternal HA + Rhiza

## Supported runtime

Ternal uses Rhiza v0.27.4 and Go 1.27.4. `data.mode=ha` runs exactly three
embedded voters with `emptyDir` and a shared object store. It does not create a
PVC. `before-ack` is required, so a successful write acknowledgement depends
on archive persistence.

Losing a Pod loses its WAL and voter identity. A container restart that retains
`/data` is different from a replacement Pod. `OnDelete` is not fencing or
recovery: externally fence the previous writer, preserve the authoritative
bucket and trust anchor, then use a separately qualified recovery procedure.

Ternal does not provide an in-place upgrade, mixed-version quorum, automatic
archive adoption, or downgrade path. Preserve the existing DataDir, archive,
and identity; use a new greenfield namespace for a new installation.

Rhiza SQL execution policy 7 is the supported physical-state boundary. State
from older policies, an unknown policy marker, or an incomplete history remains
rejected; do not stamp it with new metadata. Export application data through a
verified old runtime, then import it into a fresh cluster if migration is
required.

The embedded SDK path supports S3, GCS, and Azure Blob configuration. Local
provider contract tests do not prove production cloud IAM, abrupt power-loss
durability, or a live three-voter recovery.

The external trust-anchor ConfigMap is intentionally outside the replicated
archive. The anchor identity, namespace, bounded `TMPDIR`, and distinct Service
Account are admission boundaries; do not reuse the API or historical Operator
ServiceAccount. Preserve the trust lifecycle evidence during rollback or
recovery. Unknown holder, trust, or archive continuity is a stop condition.

## Operator is unavailable

Rhiza v0.27.4 provides a managed v1alpha2 Operator contract, but this chart
does not render that contract. The historical v0.20-era Operator path is not
qualified with the embedded v0.27.4 SDK, so `operator.enabled=true` fails at render time.
This is a fail-closed qualification boundary, not a claim that every legacy
handler call is broken. The legacy image pin remains inert chart data.

Ternal Operator observation, generation recovery, fencing, learner replacement,
and `automaticRecovery` are not supported in this release. An integration needs
new v1alpha2 resources, RBAC, trust-anchor semantics, and dedicated
qualification before this guard can be removed. Do not apply the former v0.20
CRDs or image to bypass it.

The ordinary HA SDK deployment remains available with `operator.enabled=false`:

    helm template ternal deploy/helm/ternal \
      --namespace ternal \
      --set image.tag=YOUR_UPDATED_TERNAL_IMAGE_TAG \
      --set data.mode=ha \
      --set-string data.clusterID=ternal-ha \
      --set data.objectStore.provider=gcs \
      --set data.objectStore.bucket=your-bucket \
      --set-string data.trustAnchorConfigMap=ternal-trust \
      --set-string serviceAccountName=ternal-runtime \
      --set-string secrets.existingSecret=ternal-secrets

## Local and durable standalone boundary

`data.mode=standalone` keeps an embedded one-voter profile. Its optional
durable variant requires an object store, stable cluster identity, an immutable
image digest, and an external trust anchor. `data.mode=local` is a distinct,
single-writer, seeded Local archive path with no peer membership and no
Operator. See [local-single-node.md](local-single-node.md) for its restore and
writer-quiescence contract.

Rhiza Local archive format 2 is retained. Old, partial, unknown, or mixed-mode
archives remain fail-closed; Ternal does not rewrite them.

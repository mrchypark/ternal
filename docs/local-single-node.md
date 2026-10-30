# Single-node storage

## Durable standalone profile

For a fresh one-node deployment use `values-single-node.yaml`, not
`values-local.yaml`. It runs one embedded Rhiza voter (`Local:false`), with
BeforeAck object storage authoritative and `/data/ternal` on disposable
`emptyDir`. It is not HA, has no Operator, recovery-anchor Deployment, PVC or
init container. The normal `/usr/local/bin/ternal-api` command opens the SDK,
initializes a fresh archive or restores a compatible existing archive, then
initializes Ternal and gates `/ready`. Do not run `local-restore` for this mode.
True Local below is a separate opt-in seeded-archive contract.

Supply non-secret deployment values, for example:

```yaml
image:
  digest: sha256:<approved-64-hex-image-digest>
serviceAccountName: single-api
secrets:
  existingSecret: single-runtime
data:
  clusterID: single-greenfield-a1
  trustAnchorConfigMap: single-trust
  objectStore:
    provider: s3
    endpoint: object-store.example.invalid:9000
    bucket: dedicated-ternal-bucket
    prefix: clusters/single-greenfield-a1
    region: us-east-1
    insecure: false
```

Use the native GCS or Azure public metadata from [configuration](configuration.md)
instead when appropriate. Do not copy account keys, connection strings, session
keys or OIDC secrets into this file. The external runtime Secret supplies normal
Ternal application secrets, a data-admin token of at least 32 bytes, and the
selected provider's credentials. It does not need HA membership. The chart pins
mode, durability, all public ConfigMap fields, storage location, node identity,
trust binding and disabled Operator listener directly; stale Secret entries
cannot override them. In standalone, the process skips upstream Operator
identity projection, so this profile needs no alias env. Local retains its
separate rejection of nonempty upstream identity variables.
Do not reuse the Secret as an undeclared configuration
source. No default profile or HA/Local data is converted automatically.

Before first start, provision an external trust anchor and admission policies
outside the Helm release and object store. Use the existing renderer with the
same namespace, API ServiceAccount, cluster ID and canonical storage metadata,
the approved image digest, `--data-mode=standalone` and
`--standalone-workload-name=<release>`. Omit `--anchor-service-account`: normal
standalone starts no recovery service and needs no recovery identity or RBAC.
The renderer still protects anchor deletion, monotonic transitions, approved
API images and the direct trust binding, while denying recovery transitions.
Only an explicitly configured recovery service uses the optional independent
`--anchor-service-account`; it must never reuse the API ServiceAccount.
The guard permits only zero or one replica for this exact StatefulSet and its
scale subresource. The API account needs `get`/`update` only on the named anchor.
Never reapply bootstrap trust output over an active anchor or restore that
anchor from the bucket. Provision actual credentials/IAM separately.

Render locally and review before using a target cluster:

```sh
helm template single deploy/helm/ternal --namespace <dedicated-namespace> \
  -f deploy/helm/ternal/values-single-node.yaml -f <public-deployment-values.yaml>
```

Operational acceptance and recovery:

1. First start uses a new dedicated cluster ID, prefix, external trust anchor and
   empty cache. Confirm `/ready`, write application state and read it back.
   Existing or unknown archive state is not greenfield: do not erase it to
   bypass a refusal. A mismatch or unresolved trust state must stay unready.
2. For a process/container restart retaining `/data`, stop gracefully, ensure
   that process has exited, then reopen with identical identity and credentials.
   Confirm readiness and the prior write. The archive, not a PVC, is authority.
3. For a replacement Pod or total cache loss, first verify the old writer has
   stopped, or externally fence it so it cannot write. Stop conflicting archive
   maintenance/GC. Preserve the authoritative bucket and external trust anchor.
   Start exactly one instance with the same canonical settings and fresh cache;
   require readiness and the prior write before resuming traffic. Never
   force-delete an unreachable writer until fencing is verified.

`OnDelete` prevents automatic rolling updates; it does not stop StatefulSet
replacement after Pod/node loss or prove the old writer is gone. A replica cap
is not a lease or fencing and cannot block another controller/namespace using
the archive. There is no automatic failover. Uncertain writer identity, trust
or archive continuity is a stop condition, not permission to reset state.
Local adapter tests establish graceful restart/cache-loss behavior only; they
do not establish abrupt exact-ACK crash durability, production-cloud/IAM parity
or actual Kubernetes admission execution. Those require their own evidence.

## True Local mode (seeded archive)

Ternal's explicit `data.mode=local` / `TERNAL_DATA_LOCAL=1` path uses the
published Rhiza v0.21.2 `Local:true` SDK. It has one writer, no voter listeners,
no peer membership, and no Operator. The existing `standalone` mode still runs
one embedded voter with `Local:false`; HA still runs three voters. None of
their defaults change.

The legacy `ternal-anchor` recovery/activation service is unsupported for Local.
Its startup and coordinator constructor explicitly reject Local configuration
before opening a bucket or reserving a trust transition. Keep `anchor.enabled`
disabled; the existing ConfigMap trust backend used by Local main is not a
Local archive recovery-anchor service.

## Prerequisites and safety boundary

Use a fresh dedicated DataDir, object-store prefix, cluster ID, and external
trust anchor. Rhiza v0.21 changes the Local archive format for all providers;
there is no automatic migration, adoption, cleanup, or downgrade. Never point
this configuration at a legacy archive or unknown nonempty directory.

This Ternal integration supports S3, GCS and Azure Blob adapters. It does not
add filesystem configuration. Upstream's same-host filesystem
maintenance support on stable Linux/macOS filesystems is a different capability,
not multi-host fencing and not an additional Ternal deployment backend.

Azure requires a public storage account and canonical effective Blob service
URL (default `https://<account>.blob.core.windows.net`), with no trailing slash.
Its three possible secret fields are account key, account-key connection string
and client secret; keep them only in the external runtime Secret. Public
tenant/client/user-assigned identity identifiers and selected authentication
must match main. Init receives only the provider's storage Secret references,
never OIDC, session or admin secrets. See [configuration](configuration.md) for
the exact environment names and mutually exclusive authentication forms.
Generic endpoint/insecure overrides are rejected for Azure. Emulator HTTP
loopback mode is an explicit local-test-only path, not enabled by Helm.
Actual Azure cloud authentication/IAM and production-cloud parity remain
separate qualification requirements even when local Floci-AZ tests pass.

The archive must already contain a compatible SDK Local binding and a certified
checkpoint. Initial seeding is an explicit manual operation using the published
Rhiza SDK outside this command, with the same canonical provider/location,
cluster/node configuration and BeforeAck durability. Ternal does not ship a
genesis command or Job. The trust anchor must be provisioned consistently with
the seeded application state. An empty, missing, partially initialized, old-format,
or corrupt archive is refused: read-only preflight cannot distinguish a first start
from lost previously-bound history. Do not "fix" this refusal by clearing state.

The operator must ensure there is exactly one writer and no concurrent archive
GC or maintenance, then set `TERNAL_LOCAL_WRITER_GC_QUIESCENCE_ACK=1`. This is a
manual acknowledgement, **not** proof of exclusivity, a distributed lease, or
automatic fencing. Held or unknown maintenance state is always refused; no
age-based clearing, force-clear, or skipped fence is provided.

Durability is BeforeAck only: successful writes wait for archive durability.
Upstream Async has a loss window and is not enabled by this Ternal path.
An emulator or graceful-close test does not establish exact-ACK/process-kill
durability, cloud parity, HA, real authentication, or SSH behavior.

## Init and main contract

Run the same API image with arguments `local-restore`. Both init and main use:

```text
TERNAL_DATA_LOCAL=1
TERNAL_LOCAL_PREFLIGHT_STAGING_DIR=/staging
TERNAL_LOCAL_PREFLIGHT_RECEIPT_FILE=/staging/preflight.json
TERNAL_LOCAL_WRITER_GC_QUIESCENCE_ACK=1
TERNAL_REQUIRE_DATA_ADMIN_TOKEN=0
TERNAL_LOCAL_PREFLIGHT_SCOPE=<Pod UID from direct Downward API>
```

Both receive identical canonical storage/cluster/node configuration. Init needs
the same object-store access as main, including SDK restore-pin writes, and read
access to the existing Kubernetes trust anchor, but no OIDC, session, data-admin
or peer secret. Local configuration
explicitly rejects data-admin tokens/listeners, membership, learner,
reconfiguration, operator listeners, and Operator `RHIZA_*` identity variables;
it does not silently discard conflicting security settings.

Both containers receive `TERNAL_LOCAL_PREFLIGHT_SCOPE` directly from
`fieldRef: metadata.uid`, not an overridable ConfigMap or Secret. The receipt
binds this Pod scope and the protected trust ConfigMap namespace/name as well as
the immutable archive identity. For a manual non-Kubernetes invocation, supply
an explicit unique run identifier shared only by its preflight and application
run. Scope binding is consistency evidence, not proof of writer exclusion.

Init mounts the shared DataDir and a dedicated staging directory read-write.
The staging directory must already exist, must not be a symlink or overlap
DataDir, and the receipt is its direct child. Main mounts `/data/ternal`
read-write and staging read-only. Keep both volumes private to this workload.

Init first performs read-only preflight using an Upload/Delete-denying bucket
wrapper and the public SDK calls:

1. `recovery.OpenLocalArchive(...).ReadOnly()` and `MaintenanceResolved`.
2. Capture `LocalBinding`, tip, prefix hash, and certified snapshot.
3. `checkpoint.Manager.OpenRoot` and `DownloadAndVerifyRootFiles` into a new
   staging subdirectory; re-read maintenance and the full captured view.
4. Atomically publish a bounded metadata receipt. Remove downloaded evidence
   and temporary cache, including on failure. It creates no DB identity,
   archive binding, checkpoint pin, or remote object.

After preflight, init checks the receipt, archive, trust binding and DataDir mode,
then calls `rhiza.Open(Local:true)` and `Close` with the same configuration and
**same mounted DataDir** as main. Either error fails init. It starts no application
SQL, schema migration, HTTP listener, or other service. The SDK, not Ternal's
staged files, restores the database and may write remote restore pins. The
`local-preflight` command remains available as read-only inspection only.

Main checks the receipt against the authoritative archive and trust binding
before SDK startup, then independently calls `rhiza.Open(Local:true)` with
that same restored DataDir. **No staging files are adopted or passed to the SDK.**
Main still verifies the archive binding and reconciles history; equal histories
do not require a blind checkpoint redownload. Init success does not bypass
SDK startup or Ternal `Store.Ready`; trust settlement can still leave main alive
but unready. Init runs once per Pod. A main-container restart compares the stable
binding/configuration, not the old tip, snapshot, or prefix hash: the same writer
may legitimately have advanced history. The independently opened SDK and trust
anchor remain responsible for validating current history and readiness.

This follows the published [init-container guide](https://github.com/mrchypark/rhiza/blob/v0.21.2/docs/local-init-container-recovery.md).
Ternal deliberately retains its stricter seeded-checkpoint and external-trust
requirements; this command does not implement the SDK's empty-archive adoption.
Read-only preflight downloads verification evidence before the SDK restore, so
it is not a download-avoidance optimization or a typed staging handoff.

Rhiza v0.21.2 corrects Local maintenance release acknowledgement handling without
changing public APIs, archive format, identity, or receipts. If the active
holder's conditional release upload returns an error, bounded stable readback
must prove the exact expected free control at a changed object version before
release completion is accepted. The upload timeout is unchanged; work errors,
cancellation, and uncertain operations remain fail-closed. This does not let a
new process clear an orphaned held token or bypass Ternal's guards.
The [release notes](https://github.com/mrchypark/rhiza/releases/tag/v0.21.2) retain
earlier S3 failure evidence and describe upstream emulator qualification. Those
results are not Ternal production-cloud or operational recovery qualification.

## Identity, rollback, and retention

The effective SDK archive namespace is
`path.Join(prefix, clusterID, "local/v1")`. Local trust identity is lowercase
hex SHA256 of `"ternal-local-v1\x00" + legacyHex`, where `legacyHex` is the
existing `TrustAnchorStorageID(provider, endpoint, bucket, effectiveNamespace,
clusterID)` for S3/GCS. Azure instead uses the six-field tuple
`["azure", account, effectiveBlobURL, container, effectiveNamespace, clusterID]`
before the same Local domain separation. The S3/GCS legacy hash remains
byte-identical; Azure secrets are excluded. This domain separation
prevents a legacy prefix spelling from impersonating Local authority.

Before SDK mutation, main rejects a legacy/mismatched anchor, mixed-mode
archive objects, and unknown nonempty Local DataDir. A Ternal-owned mode marker
binds subsequent opens to the same Local archive binding. Legacy startup rejects
detected Local markers or Local archive/anchor evidence, including after
DataDir loss. Existing legacy directories are not newly banned merely for
containing unrelated files. Do not delete a mode marker to bypass these guards.

The chart uses `emptyDir`, not a supported PVC persistence workflow. Pod deletion
loses that cache; recovery still requires the intact seeded archive, matching
external anchor, init receipt, and SDK validation. Preserve the old DataDir,
archive, anchor and configuration before any change. Rolling back means stopping
the new writer, retaining its data, and restoring the prior deployment against
its **original separate roots and anchor**; never flip `LOCAL` off against Local
state. No cleanup or remote deletion is automatic.

## Outside Kubernetes

The Go preflight and restore can be invoked through the same API subcommands,
but durable Ternal still requires its Kubernetes ConfigMap trust backend and
credentials. This task does not provide a Kubernetes-free durable backend or
remove that requirement. The published Rhiza SDK can be embedded independently;
that is not equivalent to a supported non-Kubernetes Ternal deployment.

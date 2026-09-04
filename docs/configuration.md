# Configuration

Ternal's public configuration is named after stable responsibilities. Internal
implementations may change without requiring operators to rename environment
variables or Kubernetes Secret keys.

## Naming contract

| Prefix | Responsibility |
| --- | --- |
| `TERNAL_OIDC_*` | OpenID Connect client and claim mapping |
| `TERNAL_DATA_*` | Local or clustered control-plane storage |
| `TERNAL_OBJECT_STORE_*` | Rhiza-certified persistence authority |
| `TERNAL_RELAY_*` | Relay admission and connection settings |
| `TERNAL_TRANSPORT_*` | Transport helper selection and diagnostics |
| `TERNAL_AGENT_*` | Device agent behavior |

Names such as Rhiza, Rauthy, and pigeons may still appear in source imports,
provider-specific development fixtures, binary filenames, licenses, or build
provenance. They do not define Ternal's runtime configuration contract.

An upstream program can have its own environment variables. For example,
`PIGEONS_TRANSPORT_DIAGNOSTICS` belongs to the separately executed pigeons
binary; it is not read as Ternal application configuration.

## Server

| Variable | Default | Purpose |
| --- | --- | --- |
| `TERNAL_BIND` | `127.0.0.1:3000` | HTTP listen address |
| `TERNAL_RELAY_BIND` | disabled | Separate relay-callback listen address; keep it private |
| `TERNAL_SESSION_KEY` | generated only for loopback development | Signs local sessions; production requires at least 32 bytes |
| `TERNAL_SESSION_TTL_SECONDS` | `3600` | Session lifetime, from 60 through 3600 seconds |
| `TERNAL_DEV_HEADERS` | `0` | Enables development identity headers; accepted only on a loopback bind |
| `TERNAL_CORS_ORIGIN` | redirect origin | Optional exact browser origin |
| `TERNAL_RELAY_ACCESS_TOKEN` | none | Relay callback bearer; at least 32 bytes |

## OIDC

| Variable | Default | Purpose |
| --- | --- | --- |
| `TERNAL_OIDC_ISSUER` | loopback development issuer | Exact discovery issuer |
| `TERNAL_OIDC_CLIENT_ID` | `ternal` | Confidential client ID |
| `TERNAL_OIDC_CLIENT_SECRET` | none | Confidential client secret |
| `TERNAL_OIDC_REDIRECT_URL` | `http://127.0.0.1:3000/auth/callback` | Exact authorization callback |
| `TERNAL_OIDC_ADMIN_GROUP` | `ternal-admins` | Group that receives administrator access |
| `TERNAL_OIDC_GROUPS_CLAIM` | `groups` | ID-token claim containing group strings |

Non-loopback issuers and redirect URLs must use HTTPS. Discovery and advertised
authorization, token, device, and JWKS endpoints must remain on the configured
issuer origin.

## Data

| Variable | Default | Purpose |
| --- | --- | --- |
| `TERNAL_DATA_DIR` | `ternal-data` | Persistent data directory |
| `TERNAL_DATA_ADMIN_TOKEN` | none | Internal data-node authentication token |
| `TERNAL_REQUIRE_DATA_ADMIN_TOKEN` | `0` | Set to `1` in production to require a token of at least 32 bytes |
| `TERNAL_DATA_CLUSTER_ID` | `ternal` | Cluster identity |
| `TERNAL_DATA_SCHEMA_VERSION` | binary schema version | Must match the running binary |
| `TERNAL_DATA_NODE_ID` | `node-1` | Node identity |
| `TERNAL_DATA_BIND_ADDR` | `127.0.0.1:0` | Data-node listen address |
| `TERNAL_DATA_PEER_ADDR` | `127.0.0.1:0` | Address advertised to peers |
| `TERNAL_DATA_CLUSTER_MEMBERS` | empty | JSON member list |
| `TERNAL_DATA_MULTI_NODE` | `0` | Requires exactly three configured members when `1` |
| `TERNAL_DATA_EXPECTED_MEMBER_IDS` | empty | Optional comma-separated exact member-ID set |
| `TERNAL_DATA_CHECKPOINT_INTERVAL` | `15m` | Interval for native certified checkpoints; must be positive |

Optional object-store settings use the same suffixes under
`TERNAL_OBJECT_STORE_`: `ENDPOINT`, `BUCKET`, `PROVIDER`, `DIR`, `PREFIX`,
`REGION`, `INSECURE`, `ACCESS_KEY`, `SECRET_KEY`, `SESSION_TOKEN`, `DURABILITY`,
`SYNC_INTERVAL`, and `BATCH_DELAY`.

### Standalone and HA

The Helm chart exposes two explicit storage modes:

- `data.mode=standalone` runs one embedded voter. It rejects cluster membership
  unless `TERNAL_DATA_MULTI_NODE=1` is explicitly selected outside Helm. With
  no object store it is intentionally ephemeral and suitable only for local
  development.
- `data.mode=ha` runs exactly three voters with parallel bootstrap, stable pod
  DNS, disposable `emptyDir` caches, required hostname anti-affinity, a
  two-voter disruption budget, and coordinated `OnDelete` upgrades.

Durable standalone sets `data.requireObjectStore=true`. HA implies that setting.
Every configured object store requires `before-ack` durability; durable
standalone and HA also require a unique, stable `data.clusterID`, shared
S3-compatible or GCS storage, and `secrets.existingSecret`. HA's Secret
must contain the normal Ternal keys plus
`TERNAL_DATA_CLUSTER_MEMBERS`. That key is a JSON array for `ternal-0`,
`ternal-1`, and `ternal-2`; each member needs its matching
`quic://<pod>.ternal-data.<namespace>.svc.cluster.local:9090` peer URL and a
different random token of at least 32 bytes. Voter tokens must also differ from
`TERNAL_DATA_ADMIN_TOKEN`. Store the object-store access key, secret key, and
optional session token in the same Secret as `TERNAL_OBJECT_STORE_ACCESS_KEY`,
`TERNAL_OBJECT_STORE_SECRET_KEY`, and `TERNAL_OBJECT_STORE_SESSION_TOKEN`.
Never put member tokens or object-store credentials in Helm values.

For GCS on GKE, set `serviceAccountName` to an existing Kubernetes
ServiceAccount bound through Workload Identity. Rhiza then uses Application
Default Credentials. Ternal deliberately does not expose service-account JSON
through Helm values or environment variables.

Storage values are shared by standalone and HA and contain only non-secret
routing metadata:

```yaml
data:
  mode: ha
  clusterID: ternal-production-a1
  requireObjectStore: true
  checkpointInterval: 15m
  objectStore:
    provider: s3
    endpoint: object-store.example:9000
    bucket: ternal
    # Defaults to clusters/<clusterID> when omitted.
    prefix: ""
    region: ""
    insecure: false
    durability: before-ack
  ha:
    peerPort: 9090
secrets:
  existingSecret: ternal-runtime
```

For native GCS, omit `endpoint`, `region`, and `insecure` and use:

```yaml
serviceAccountName: ternal-data
data:
  mode: ha
  clusterID: ternal-production-a1
  objectStore:
    provider: gcs
    bucket: ternal-checkpoints
    durability: before-ack
```

The regular Service sends API traffic only to ready voters. `/ready` performs a
linearizable read, so an isolated voter remains alive but leaves Service
endpoints when it cannot reach quorum.

An HA upgrade is ordinal and sequential: delete one voter, wait for its
replacement to become ready and pass a linearizable query, then continue with
the next voter. Never delete two voters together. `OnDelete` deliberately makes
this operator action explicit instead of restarting the whole cluster.

The data mode, cluster ID, voter IDs, object-store location/prefix, schema
version, and Secret name form one immutable data identity. Helm
rejects an in-place change before scaling or replacing pods, and the StatefulSet
selector provides a Kubernetes-level backstop. Create a new greenfield Helm
release with a new cluster ID and object-store prefix for standalone/HA
conversion or other identity changes; never use `helm upgrade --force` to bypass
this boundary. Voter IDs, peer tokens, and peer URLs are fixed for the lifetime
of an HA cluster.

The chart never creates a PVC. Each pod rebuilds its local cache from Rhiza's
certified object-store state. `persistence.enabled=true` is retained only as a
fail-closed guard for the retired setting and is rejected during rendering.

The current schema is version 1 and is unchanged by this storage-mode work.
Changing the schema version is deliberately not a rolling upgrade: the chart
and binary reject a mismatch, and the immutable data identity blocks mixed
schema generations. A future compatible expand/contract procedure must be
qualified explicitly before this restriction is relaxed.

## Transport and agent

| Variable | Purpose |
| --- | --- |
| `TERNAL_TRANSPORT_BIN` | Override the bundled or adjacent transport helper |
| `TERNAL_DIRECT_ADDRESSES` | Comma-separated direct addresses published during enrollment |
| `TERNAL_AGENT_RELAY_URLS` | Primary relay URLs published by the agent |
| `TERNAL_AGENT_EXTRA_RELAY_URLS` | Additional relay URLs |
| `TERNAL_AGENT_AUTHORIZED_KEYS_PATH` | Authorized-keys file managed atomically by the agent |
| `TERNAL_AGENT_SSH_USER` | SSH account managed on the device |
| `TERNAL_DEVICE_KEY_FILE` | Device signing key path |
| `TERNAL_DEVICE_IDENTITY_FILE` | Persistent transport identity metadata path |
| `TERNAL_MANUFACTURING_TOKEN_FILE` | One-time enrollment token file |

The CLI and agent also use `TERNAL_API_URL`. Secret enrollment material must be
supplied through files rather than command arguments.

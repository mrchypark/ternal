# Configuration

Ternal's public configuration is named after stable responsibilities. Internal
implementations may change without requiring operators to rename environment
variables or Kubernetes Secret keys.

## Naming contract

| Prefix | Responsibility |
| --- | --- |
| `TERNAL_OIDC_*` | OpenID Connect client and claim mapping |
| `TERNAL_DATA_*` | Local or clustered control-plane storage |
| `TERNAL_OBJECT_STORE_*` | Optional durable object-store backing |
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
| `TERNAL_DATA_NODE_ID` | `node-1` | Node identity |
| `TERNAL_DATA_BIND_ADDR` | `127.0.0.1:0` | Data-node listen address |
| `TERNAL_DATA_PEER_ADDR` | `127.0.0.1:0` | Address advertised to peers |
| `TERNAL_DATA_CLUSTER_MEMBERS` | empty | JSON member list |
| `TERNAL_DATA_MULTI_NODE` | `0` | Requires at least three configured members when `1` |

Optional object-store settings use the same suffixes under
`TERNAL_OBJECT_STORE_`: `ENDPOINT`, `BUCKET`, `PROVIDER`, `DIR`, `PREFIX`,
`REGION`, `INSECURE`, `ACCESS_KEY`, `SECRET_KEY`, `SESSION_TOKEN`, `DURABILITY`,
`SYNC_INTERVAL`, and `BATCH_DELAY`.

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

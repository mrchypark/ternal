# Ternal

[![CI](https://github.com/mrchypark/ternal/actions/workflows/ci.yaml/badge.svg)](https://github.com/mrchypark/ternal/actions/workflows/ci.yaml)

Ternal is a self-hosted SSH access control plane for private devices. It gives
users short-lived, policy-checked SSH access without exposing device SSH ports
to the public internet.

The server, CLI, device agent, and web UI are written in Go. The web UI is
server-rendered with [gomponents](https://github.com/maragudk/gomponents), htmx,
and Tailwind CSS; it has no Node.js runtime or client-side application framework.

> Ternal is under active development. The current code uses a greenfield data
> model and do not migrate older Ternal databases.

## How it works

1. An administrator enrolls a device and records its SSH host-key fingerprint.
2. The device agent maintains a persistent transport identity and synchronizes
   authorized keys.
3. A user signs in through an OIDC provider and requests a host.
4. Ternal evaluates identity claims, host policy, device state, and the route.
5. The CLI receives a 300-second relay grant and opens OpenSSH with strict
   host-key checking.

Ternal fails closed when the route is incomplete, a grant is missing or expired,
the device is revoked, or the host key does not match.

## Components

| Command | Purpose |
| --- | --- |
| `ternal-api` | HTTP API, OIDC relying party, policy engine, and embedded web UI |
| `ternalctl` | User login, host discovery, and strict SSH invocation |
| `ternal-agent` | Device enrollment, heartbeat, key synchronization, and transport supervision |

The current storage engine is
[Rhiza](https://github.com/mrchypark/rhiza), and the current private-device
transport is a pinned Ternal build of
[pigeons](https://github.com/n0-computer/pigeons). These are implementation
dependencies, not names in Ternal's public runtime configuration.

## Try it locally

Requirements:

- Go 1.27 or newer
- `curl` and `jq`

Run the loopback-only smoke test:

```sh
./frontend/build.sh
./deploy/e2e/local-go-smoke.sh
```

The smoke test starts a temporary server, verifies the embedded web assets, and
performs authenticated host CRUD. It uses development headers that the server
rejects on non-loopback addresses.

Run the complete deterministic suite with:

```sh
./scripts/run-all-tests.sh
```

Or build the Go programs directly:

```sh
go test ./...
go build ./cmd/ternal-api ./cmd/ternalctl ./cmd/ternal-agent
```

## Configuration

All Ternal-owned environment variables use the `TERNAL_` prefix and describe a
stable role, not a dependency product. For example, use `TERNAL_DATA_DIR`, not a
storage-engine name, and `TERNAL_OIDC_ISSUER`, not an identity-provider name.

The minimum production configuration is:

```sh
export TERNAL_BIND='127.0.0.1:3000'
# Set a separate private listener only when running a managed relay.
export TERNAL_RELAY_BIND='127.0.0.1:3001'

export TERNAL_OIDC_ISSUER='https://identity.example/auth/v1/'
export TERNAL_OIDC_CLIENT_ID='ternal'
export TERNAL_OIDC_CLIENT_SECRET='<provider-issued value>'
export TERNAL_OIDC_REDIRECT_URL='https://ternal.example/auth/callback'
export TERNAL_OIDC_ADMIN_GROUP='ternal-admins'

export TERNAL_SESSION_KEY='<at least 32 random bytes>'
export TERNAL_DATA_DIR='./ternal-data'
export TERNAL_DATA_ADMIN_TOKEN='<at least 32 random bytes>'
export TERNAL_REQUIRE_DATA_ADMIN_TOKEN=1
export TERNAL_RELAY_ACCESS_TOKEN='<at least 32 random bytes>'

go run ./cmd/ternal-api
```

Do not commit these secret values. Generate the session, data, and relay tokens
independently. For Docker Compose, copy `.env.example` to `.env`, replace every
placeholder, and run `docker compose up --build`.

See [configuration](docs/configuration.md) for the complete naming contract,
advanced data settings, and the distinction between Ternal variables and
upstream tool variables.

## CLI and device agent

User access:

```sh
ternalctl login
ternalctl hosts
ternalctl ssh <host-id>
```

Device enrollment:

```sh
ternal-agent device-keygen /var/lib/ternal/device.key

TERNAL_API_URL='https://ternal.example' \
TERNAL_DEVICE_KEY_FILE=/var/lib/ternal/device.key \
TERNAL_DEVICE_IDENTITY_FILE=/var/lib/ternal/device.json \
TERNAL_MANUFACTURING_TOKEN_FILE=/run/secrets/ternal-enrollment \
  ternal-agent enroll 'SHA256:<ssh-host-key-fingerprint>'

TERNAL_API_URL='https://ternal.example' \
TERNAL_DEVICE_KEY_FILE=/var/lib/ternal/device.key \
TERNAL_DEVICE_IDENTITY_FILE=/var/lib/ternal/device.json \
TERNAL_AGENT_AUTHORIZED_KEYS_PATH=/home/ops/.ssh/authorized_keys \
TERNAL_AGENT_SSH_USER=ops \
TERNAL_AGENT_RELAY_URLS='https://relay.example' \
  ternal-agent run
```

The manufacturing token is read from a file and never passed as a process
argument. Enrollment binds the device key, transport endpoint, serial, and SSH
host-key fingerprint in one operation.

## Deployment and releases

- `Dockerfile` builds the API and embedded frontend.
- `deploy/helm/ternal` contains the Helm chart.
- Runtime secrets can come from one pre-created Kubernetes Secret, keeping
  plaintext values out of Helm release history.
- GitHub Actions builds the image, chart, native CLI bundles, Linux agent bundle,
  checksums, SBOMs, and provenance for version tags.
- Release workflows publish artifacts only; they do not deploy them.

Cloud Build is not supported.

## Security properties

- one `/pigeons/1` data plane with persistent client and device identities;
- explicit relay or direct routes; endpoint-ID-only connections are rejected;
- policy-checked 300-second relay grants bound to the client endpoint ID;
- callback admission bound to both the configured bearer and endpoint subject;
- strict OpenSSH host-key verification with no permissive fallback;
- signed device requests with freshness checks;
- monotonic authorized-key generations with rollback and equivocation rejection;
- pinned OIDC issuer, endpoint origin, audience, nonce, and state validation;
- no CLI persistence of provider access or refresh tokens.

Ternal owns policy and grant behavior. Its pinned pigeons patch contains only
generic transport capabilities required to supply identity and full route data.

## Documentation

- [Configuration](docs/configuration.md)
- [User scenarios](docs/user-scenarios.md)
- [Verification](docs/verification.md)
- [Device enrollment and access](docs/device-manufacturing-access.md)
- [Identity and Ternal data boundary](docs/data-boundary.md)
- [Release process](docs/releasing.md)
- [Pinned transport provenance](docs/agent-embedded-pigeons.md)

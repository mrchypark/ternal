# Ternal

Ternal is a Go-first SSH access control plane for private devices reached over
the pinned Ternal build of [pigeons](https://github.com/n0-computer/pigeons).
This branch is a breaking greenfield port: the application, CLI, agent, portal,
tests, image, and CI are Go-native. There is no parallel Rust application or
Node frontend runtime.

## Architecture

- `cmd/ternal-api`: Go HTTP API, OIDC relying party, and embedded web portal.
- `cmd/ternalctl`: device-flow login and strict, grant-aware SSH client wrapper.
- `cmd/ternal-agent`: persistent device identity, enrollment, heartbeat,
  authorized-key synchronization, and pigeons roost supervision.
- `internal/store`: embedded [Rhiza](https://github.com/mrchypark/rhiza)
  persistence with linearizable reads.
- `internal/web`: server-rendered
  [gomponents](https://github.com/maragudk/gomponents) views enhanced by pinned
  htmx 4 and Tailwind CSS 4 assets.

The portal intentionally has no JavaScript package manager or client framework.
`frontend/build.sh` downloads checksum-pinned htmx and the checksum-pinned
Tailwind standalone CLI, then produces the embedded static assets. HTML remains
ordinary typed Go code and htmx is progressive enhancement.

## Security contract

- one breaking `/pigeons/1` SSH data plane;
- persistent client and roost identities;
- explicit relay or direct addressing (endpoint-ID-only connections fail);
- policy-checked relay grants fixed at 300 seconds and bound to the requesting
  client endpoint ID;
- relay callback admission requires the configured bearer and exact
  `x-iroh-nodeid` grant subject;
- OpenSSH always uses strict host-key verification against the fingerprint
  enrolled by the signed device identity;
- device control requests are Ed25519 signed and freshness checked;
- authorized-key snapshots have server-side monotonic generations and the agent
  rejects rollback/equivocation;
- OIDC discovery, endpoints, issuer, audience, nonce, state, and provider origin
  are checked; provider access and refresh tokens are not persisted by the CLI.

Ternal owns policy and grant semantics. The pinned pigeons patch only supplies
generic transport capabilities that cannot be composed outside pigeons. It does
not contain Ternal policy, grant logic, or `RelayConfig::with_auth_token`.

## Build and test

Go 1.27 or newer is required.

```sh
./frontend/build.sh
go test ./...
go build ./cmd/ternal-api ./cmd/ternalctl ./cmd/ternal-agent
```

Run the deterministic repository suite with:

```sh
./scripts/run-all-tests.sh
```

The pigeons packaging checks require Rust only to build the separately pinned
upstream transport source. Rust is not part of the Ternal application or
frontend architecture.

## Local server

Production-like runs require explicit secrets and a real OIDC provider:

```sh
export TERNAL_BIND=127.0.0.1:3000
export RHIZA_DATA_DIR=ternal-rhiza
export RHIZA_ADMIN_TOKEN='<at least 32 random bytes>'
export TERNAL_REQUIRE_RHIZA_ADMIN_TOKEN=1
export TERNAL_SESSION_KEY='<at least 32 random bytes>'
export TERNAL_PIGEONS_RELAY_ACCESS_TOKEN='<at least 32 random bytes>'
export RAUTHY_ISSUER='https://issuer.example/auth/v1/'
export RAUTHY_CLIENT_ID='ternal'
export RAUTHY_CLIENT_SECRET='<provider-issued value>'
export RAUTHY_REDIRECT_URL='https://ternal.example/auth/callback'
go run ./cmd/ternal-api
```

For a loopback-only development smoke, use
`deploy/e2e/local-go-smoke.sh`. Development identity headers are rejected on a
non-loopback bind.

## CLI and agent

```sh
go run ./cmd/ternalctl login
go run ./cmd/ternalctl hosts
go run ./cmd/ternalctl ssh <host-id>

go run ./cmd/ternal-agent device-keygen
TERNAL_MANUFACTURING_TOKEN_FILE=/run/secrets/enrollment-token \
  go run ./cmd/ternal-agent enroll <ssh-host-key-fingerprint>
go run ./cmd/ternal-agent run
```

The manufacturing token is read from a file, never from process arguments.
Batch enrollment assigns the serial number server-side. A one-device token may
pass an explicit serial as the final `enroll` argument.

## Deployment

`Dockerfile` builds the Go server and embedded frontend assets. The Helm chart
supports disposable `emptyDir` storage and an optional PVC. Runtime secrets are
provided by a pre-created Secret; Helm does not need plaintext secret values in
release history.

CI and image publication use GitHub Actions. Cloud Build is not supported.
Version tags publish the image, packaged Helm chart, platform-native CLI
bundles, Linux agent bundle, checksums, SBOMs, and provenance in one verified
GitHub Release. The release workflow does not deploy them.
Release assets must be rebuilt from the exact reviewed Go commit and qualified
before any deployment.

See [verification](docs/verification.md), [user scenarios](docs/user-scenarios.md),
and [pigeons provenance](docs/agent-embedded-pigeons.md).

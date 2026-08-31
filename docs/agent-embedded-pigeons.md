# Ternal Agent and Bundled pigeons

## Decision

Ternal uses one patched `pigeons` binary as its SSH data plane. `ternal-agent`
supervises `pigeons roost`; OpenSSH reaches it through `pigeons fly --stdio`.
Ternal owns inventory, policy, grants, audit, and SSH host-key trust. It does
not reimplement SSH or the transport protocol.

The pinned upstream source is pigeons `0.1.1` at commit
`0ad18072f77a3ce64c093cab2686a3e99d73c944`. Its upstream `Cargo.toml` and
`LICENSE` are MIT; treat the source license as MIT even where an upstream README
badge describes a dual license.

## Runtime

```text
systemd -> ternal-agent run -> pigeons roost
OpenSSH -> ternalctl proxy -> pigeons fly --stdio -> pigeons roost -> sshd
```

`ternal-agent` resolves the transport helper in this order: `TERNAL_TRANSPORT_BIN`, a
bundled sibling, then `PATH`. It starts `pigeons roost` with the configured
`--relay-url` and `--extra-relay-url` values, persists the device identity,
sends signed heartbeats, and restarts a failed child with backoff. Ternal
advertises validated direct addresses through endpoint discovery and supplies
them to the client-side `fly`; they are not roost command-line inputs. Relay
flags must match the issued client route.

The client identity is persistent: `pigeons endpoint-id` and `pigeons fly
--stdio` use the same local key directory. That stable endpoint ID is necessary
because capped relay grants are bound to the client identity; a new ephemeral
identity cannot reuse a grant. Keep the private key only on the endpoint.
The server identity is intentionally separate because iroh rejects
self-connections. Device enrollment uses `pigeons endpoint-id --roost` so the
registered inventory endpoint is derived from the same key that `pigeons
roost` serves, and the agent treats the running roost's emitted ID as
authoritative.

## Required Ternal patch hooks

Upstream already supplies `roost`, `fly --stdio`, and the original transport.
The pinned patch versions that internal ALPN from `/pigeons/0` to
`/pigeons/1` because it adds a wire preface; this intentionally rejects mixed
patched/unpatched peers instead of silently consuming SSH bytes. The patch
contains only the generic primitives below; grant, policy, route choice, SSH
invocation, and user-facing errors remain in Ternal.

| Patch divergence | Why it cannot be composed outside pigeons |
| --- | --- |
| caller-selected client `--key-dir`, client `endpoint-id`, and `endpoint-id --roost` | `fly` otherwise creates an ephemeral secret inside the transport process; an external wrapper cannot make that identity stable or safely derive either the grant-bound client ID or the distinct key-backed roost ID required for pre-start enrollment. |
| full remote relay/direct address inputs | iroh v1 needs an `EndpointAddr` containing the remote relay or direct candidates; configuring only the local relay map leaves a custom-relay connection with no addressing information. |
| `--extra-relay-url` | Extending the endpoint's default relay map is an iroh builder operation unavailable to an external subprocess wrapper. |
| redacted connection-path diagnostics | The selected iroh path is visible only on the live in-process connection; the generic sink exposes only `direct`, `relay`, or `unknown`. |
| private-key permissions and separate client/roost keys | The transport process creates and reads the keys, so it must enforce `0600`; separate keys are required because iroh rejects self-connections. |
| versioned stream preface and full-duplex half-close draining | iroh opens streams lazily, so a server-first protocol such as SSH deadlocks until the client writes unless pigeons sends an internal preface. The bridge must also propagate one side's EOF while continuing to drain the other side, which cannot be controlled by the subprocess caller. |
| readable config-file creation | Upstream opens its internally selected config path with `create(true)` but without write access, so the process always falls back when the file is missing. Only pigeons controls those open flags. |

Ternal never invokes upstream `pigeons add`: it disables SSH host-key checking.
For managed hosts, `ternalctl` receives Ternal-controlled host-key trust
material and uses strict OpenSSH verification. Trust-on-first-use and
`StrictHostKeyChecking=no` are forbidden.

## Safety and failure behavior

The agent reports selected binary mode and child state, but not tokens, private
keys, endpoint IDs, authorization headers, direct addresses, or relay URLs in
unbounded logs. A missing binary or failed child is a visible unhealthy state;
a revoked device identity stops the supervised transport after the control
plane rejects it. A changed device endpoint ID or SSH host-key fingerprint
quarantines the device rather than rotating trust.

For the machine-readable output and diagnostics contract, see
[pigeons-transport-diagnostics.md](pigeons-transport-diagnostics.md).

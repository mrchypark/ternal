# Ternal Agent and bundled pigeons

Ternal uses one `pigeons` binary as its SSH data plane. `ternal-agent`
supervises `pigeons roost`; OpenSSH reaches it through `pigeons fly --stdio`.
Ternal owns inventory, policy, 300-second grants, audit, route selection, and
strict SSH host-key trust. None of that policy lives in pigeons.

The bundle is built without a local patch file from
`mrchypark/pigeons` commit `37d169cc88765a7d3e2569e6d1bb79056d4ab18c`.
That commit is based on upstream `n0-computer/pigeons` main after release
`v0.2.1` and contains the following generic changes proposed upstream:

- [n0-computer/pigeons#21](https://github.com/n0-computer/pigeons/pull/21):
  versioned `/pigeons/1` stream preface and bidirectional half-close/drain.
- [n0-computer/pigeons#22](https://github.com/n0-computer/pigeons/pull/22):
  caller-selected persistent client identity and a full remote
  `EndpointAddr` assembled from relay/direct candidates.
- [n0-computer/pigeons#23](https://github.com/n0-computer/pigeons/pull/23):
  await pending config-file writes before returning from storage. This fixes
  immediate reloads observing empty settings; it does not promise crash durability.

The transport changes must live inside pigeons because it owns the iroh endpoint,
connection, and QUIC stream. The old Ternal patch's config-file and key-mode
fixes are already upstream. The write-completion fix belongs in pigeons' shared
config writer so every caller receives the same guarantee. Extra relay inputs
are composed by Ternal as
repeated upstream `--relay-url` arguments; separate client/server homes provide
separate identities; network-isolated tests prove route choice. Those do not
need fork changes.

## Runtime contract

```text
systemd -> ternal-agent run -> pigeons roost
OpenSSH -> ternalctl proxy -> pigeons fly --stdio -> pigeons roost -> sshd
```

`ternal-agent` resolves the helper from `TERNAL_TRANSPORT_BIN`, a bundled
sibling, then `PATH`. Server identity persists in its home. Client identity
persists in the client's pigeons key directory. They must remain distinct
because iroh rejects self-connections. Ternal rejects EndpointId-only SSH
commands: every issued proxy command includes at least one validated relay or
direct address.

The `/pigeons/1` ALPN is intentionally incompatible with `/pigeons/0`: the
preface wakes lazy stream establishment before server-first SSH bytes, and the
bridge propagates one side's EOF while draining the other side before exit.

Ternal never invokes `pigeons add`, which disables SSH host-key checking.
Managed SSH always uses Ternal-provided pinned trust with
`StrictHostKeyChecking=yes`.

The source archive and Cargo lockfile are SHA-256 pinned in
`deploy/agent/pigeons-build.env`; native builders verify both before running
upstream tests and producing the bundled binary. The selected MIT license is
included in every archive.

The production chart pins the official multi-architecture
`n0computer/iroh-relay:v1.1.0` manifest by digest. Its HTTP access callout still
authenticates the relay's outbound request to Ternal; it is not a client relay
token.

Route verification is described in
[pigeons-transport-diagnostics.md](pigeons-transport-diagnostics.md).

# pigeons Transport Diagnostics

Ternal ships a minimal patch over upstream pigeons `0.1.1` commit
`0ad18072f77a3ce64c093cab2686a3e99d73c944` (MIT; upstream `Cargo.toml` and
`LICENSE` govern despite a dual-license README badge). Upstream already
provides `roost` and `fly --stdio`; the patch adds only the generic primitives
Ternal cannot compose around the subprocess: a caller-selected persistent
client identity, full remote relay/direct addresses, relay-map extension, and
redacted in-process transport diagnostics.

## Command and identity contract

```sh
pigeons roost --relay-url https://relay.example.com
pigeons endpoint-id --key-dir /var/lib/ternal/pigeons
pigeons fly --stdio <remote-endpoint-id> --key-dir /var/lib/ternal/pigeons \
  --relay-url https://relay.example.com \
  --extra-relay-url https://custom-relay.example.com \
  --direct-address 192.0.2.10:4242 \
  --direct-address '[2001:db8::10]:4242'
```

`endpoint-id` writes exactly one lowercase, 64-character hexadecimal public ID
plus a newline to stdout. `fly --stdio` reserves stdout exclusively for the SSH
byte stream. Neither command writes secrets, endpoint IDs, addresses, relay
URLs, or informational text to stdout. The shared key directory creates and
reuses one private identity; Ternal never receives that private material.

The `--relay-url`, `--extra-relay-url`, and repeated `--direct-address` hooks
are validated route inputs. Ternal uses them only after policy and grant checks;
they do not bypass endpoint-bound capped grants.

## Diagnostics and redaction

Diagnostics are disabled unless an explicit stderr or append-only file sink is
selected. They must never share stdout with SSH bytes. Events may report only
the schema, event kind, and `direct`, `relay`, or `unknown` transport state;
they must redact endpoint IDs, IP addresses, relay URLs, grants, tokens, keys,
and SSH payloads. `unknown` is not proof of either path.

Example redacted event:

```json
{"schema":"pigeons.transport.v1","event":"transport_changed","transport":"relay"}
```

Select the generic sink with `--transport-diagnostics stderr|FILE` or
`PIGEONS_TRANSPORT_DIAGNOSTICS`. The fork contains no Ternal-specific
environment variable, grant rule, callback policy, or SSH trust decision.

`pigeons add` is deliberately outside the Ternal integration: upstream behavior
disables SSH host-key checking. Ternal instead supplies its pinned trust through
`ternalctl` and requires strict OpenSSH host-key verification before user
authentication.

## Verification status

This document is the current contract, not a claim that migration verification
has run. Historical iroh-ssh test evidence remains in
[verification.md](verification.md) and
[manual-e2e-run-2026-07-11.md](manual-e2e-run-2026-07-11.md). A current run
must independently prove `fly --stdio` SSH bytes, persistent client identity,
grant enforcement, strict host-key rejection, direct/relay diagnostics, and
redaction.

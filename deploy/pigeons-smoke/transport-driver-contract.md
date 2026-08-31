# Transport matrix driver contract

`transport-matrix.sh` verifies behavior but delegates network isolation and path
observation to an executable driver. This keeps a successful SSH banner from
being misreported as proof of a direct or relayed transport path.

The driver receives one operation per invocation:

- `capabilities`: print JSON with `path_observable`, `evidence`, and all
  supported `states`. `evidence` is `production` for a physically controlled
  network or `fixture` for harness-only tests.
- `prepare`: create the relay, target server, client, and isolated networks.
- `endpoint-id`: print the stable server endpoint ID.
- `apply <state>`: enforce `relay-only`, `direct-only`, `both-blocked`, or
  `recovery` with Linux namespaces, firewall rules, or Kubernetes NetworkPolicy.
- `probe <state>`: print JSON containing `connected`, `path`, and `endpoint_id`.
- `cleanup`: remove every resource created by `prepare`.

Example probe result:

```json
{"connected":true,"path":"relay","endpoint_id":"stable-id"}
```

`path` must be `relay`, `direct`, or `none`, based on independent transport
telemetry or packet evidence. `unknown` is always a failure. A driver which
cannot observe the path must report `path_observable: false`; the harness then
returns exit status 77 (`SKIP`). Fixture drivers are rejected unless
`TERNAL_TRANSPORT_ALLOW_FIXTURE=1` is explicitly set by a self-test, so their
output cannot be used as production evidence.

Production evidence is accepted only from driver ID
`linux-netns-pigeons-v1`. Every state must report
`network_state_verified=true` after re-reading live `iptables-save` output.
Connected states must additionally report `ssh_banner=true` and
`diagnostics_jsonl=true`; the selected path comes from the final valid patched
pigeons JSONL event. A `/ping` response is readiness evidence only.

## Patched pigeons Linux driver

[`drivers/linux-netns-pigeons.sh`](drivers/linux-netns-pigeons.sh) is the
concrete Linux driver. It requires root, `ip`, `iptables`, `ss`, a local `sshd`,
and a patched Linux `pigeons` binary. Local matrix mode additionally requires
Docker for its disposable relay. It creates an isolated client network
namespace and physically applies these rules:

| State | Client UDP | Relay TCP | Expected event |
|---|---:|---:|---|
| `relay-only` | blocked | allowed | `relay` |
| `direct-only` | allowed | blocked | `direct` |
| `both-blocked` | blocked | blocked | no SSH connection |
| `recovery` | blocked | restored | `relay` |

The driver discovers the server's bound UDP port with `ss`, passes it through
the patched repeated `--direct-address` option, enables
`PIGEONS_TRANSPORT_DIAGNOSTICS=stderr`, and parses only JSONL events matching
`pigeons.transport.v1`. The final event is authoritative; a
final `unknown` event fails a connected scenario.

```sh
sudo env \
  TERNAL_PIGEONS_BIN="$PWD/dist/pigeons-linux-amd64" \
  TERNAL_TRANSPORT_DRIVER="$PWD/deploy/pigeons-smoke/drivers/linux-netns-pigeons.sh" \
  sh deploy/pigeons-smoke/transport-matrix.sh
```

In vind, run the same command in a disposable privileged Linux workload with
the patched binary and required networking tools. If the workload cannot create
network namespaces or enforce firewall rules, capability detection returns
`SKIP` instead of producing evidence.

## In-cluster relay through public ingress

For a public path to a relay running in a disposable cluster, set
`TERNAL_RELAY_URL=https://<ternal-host>` and run
`production-relay-ingress-smoke.sh` from a privileged Linux host or disposable
vind workload. In this mode the driver does not start Docker or any local relay.
It creates temporary veth/NAT forwarding for the isolated client namespace,
pins the resolved relay hostname in that namespace, and uses the external URL
for both server and client.

Because the production relay uses Ternal's access callback, provide an
executable `TERNAL_TRANSPORT_REGISTER_ENDPOINT_CMD`. It receives:

```text
<hook> register server <endpoint-id>
<hook> register client <endpoint-id>
<hook> cleanup
```

The hook owns Rauthy/Ternal credentials and removal of temporary participants;
`TERNAL_TRANSPORT_MATRIX_WORK` is available for its state. Missing credentials,
patched binary, root/CAP_NET_ADMIN, required commands, or endpoint-registration
hook return `SKIP 77` before data-path assertions.

The external test runs only `relay-only`: the namespace's live
`iptables-save` output must show all UDP rejected, the proxy must receive an SSH
banner, and patched diagnostics must end at `transport=relay`. `/ping` may be
used internally as a readiness prerequisite but never substitutes for these
three assertions.

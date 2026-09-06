# pigeons route verification

Ternal does not patch pigeons to expose internal path telemetry. The Linux
transport test proves route behavior with independent network isolation:

- relay-only blocks UDP while permitting the relay TCP connection;
- direct-only blocks the relay TCP connection while permitting UDP;
- both-blocked blocks both paths and must not produce an SSH banner;
- recovery restores the relay path without changing endpoint identity.

Connected states must produce a real `SSH-` banner. The driver re-reads the
live firewall rules before every probe, so a banner can be attributed to the
only path still available. No addresses, relay URLs, endpoint IDs, grants, or
SSH payloads are emitted as transport diagnostics.

The bundled command contract is:

```sh
pigeons roost --relay-url https://relay.example.com
pigeons endpoint-id --key-dir /var/lib/ternal/pigeons
pigeons fly --stdio <remote-endpoint-id> \
  --key-dir /var/lib/ternal/pigeons \
  --relay-url https://relay.example.com \
  --direct-address 192.0.2.10:4242
```

Repeated `--relay-url` and `--direct-address` inputs form the remote
`EndpointAddr`. Ternal validates every value and rejects EndpointId-only proxy
commands before starting pigeons. Grant authorization remains bound to the
persistent client endpoint ID.

`pigeons add` remains outside the integration because it disables SSH host-key
checking. `ternalctl` supplies strict pinned OpenSSH trust instead.

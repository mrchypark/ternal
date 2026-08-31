# Manufacturing and device access

## Enrollment

An administrator creates either a one-device manufacturing token or a bounded
batch. Tokens are random, stored only as hashes, and returned once. A batch has
an expiry, serial prefix, device limit, and close state. Batch serials are
allocated by the server (`PREFIX-000001`, …); the batch closes at its limit.

On the device:

```sh
ternal-agent device-keygen /var/lib/ternal/device.key
TERNAL_DEVICE_KEY_FILE=/var/lib/ternal/device.key \
TERNAL_DEVICE_IDENTITY_FILE=/var/lib/ternal/device.json \
TERNAL_MANUFACTURING_TOKEN_FILE=/run/secrets/ternal-enrollment \
  ternal-agent enroll SHA256:<ssh-host-key-fingerprint>
```

For a one-device token, append the explicit serial. The secret is read from the
file and never placed in process arguments. Enrollment binds the device Ed25519
public key, persistent pigeons roost endpoint ID, SSH host-key fingerprint,
serial, and Ternal host in one server operation.

## Runtime

`ternal-agent run` supervises `pigeons roost`, signs heartbeats, publishes only
the configured direct/relay routes, and synchronizes the configured SSH user's
`authorized_keys` file. Every control request binds the enrolled serial,
endpoint ID, and SSH host-key fingerprint and is rejected if stale or revoked.

Set:

```sh
TERNAL_API_URL=https://ternal.example
TERNAL_DEVICE_KEY_FILE=/var/lib/ternal/device.key
TERNAL_DEVICE_IDENTITY_FILE=/var/lib/ternal/device.json
TERNAL_AGENT_AUTHORIZED_KEYS_PATH=/home/ops/.ssh/authorized_keys
TERNAL_AGENT_SSH_USER=ops
TERNAL_AGENT_RELAY_URLS=https://relay.example
```

The server assigns a monotonic generation to each authorized-key snapshot. The
agent verifies the content digest, rejects generation rollback or same-generation
equivocation, writes both keys and state atomically, and acknowledges the exact
installed generation.

## User access

1. The user signs in through OIDC and registers an OpenSSH public key.
2. Ternal evaluates the user's groups and host policy.
3. Ternal requires an enrolled, non-revoked device, verified SSH host-key
   fingerprint, and explicit direct or relay route.
4. `ternalctl` obtains a relay grant bound to its persistent pigeons endpoint
   for exactly 300 seconds.
5. OpenSSH connects through `pigeons fly --stdio` with strict host-key checking.
6. A missing route, mismatched host key, expired grant, revoked device, or
   unapproved user fails closed.

Deleting a device atomically revokes device and host state, expires SSH access,
removes relay admission, and blocks new discovery and grants. The agent stops
its transport when the control plane rejects the revoked identity; the security
record is retained and trust is never recreated silently.

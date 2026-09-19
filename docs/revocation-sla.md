# Revocation SLA

Ternal never uses the phrase "immediate revocation" for data-plane access.
Each layer below states its own bound. Established SSH sessions are never
killed by revocation; only new authentications are refused.

Installed keys carry an `expiry-time` key option in `YYYYMMDDHHMMSSZ`.
The trailing `Z` makes sshd read the deadline as UTC; without it sshd
interprets the value in the device's local time zone, which would extend a
UTC deadline by that device's offset. This requires OpenSSH 8.2 or newer, and
the agent refuses to install a line whose expiry-time lacks the `Z` suffix.
Device clocks must still be roughly accurate for the deadline to mean what it
says.

## Layers

| Layer | Guarantee |
| --- | --- |
| Policy deletion | No new grants. `POST /access/ssh` and `POST /access/relay-grants` evaluate live policies on every request, so deleting a policy refuses further issuance. Proven by `TestPolicyDeletionRefusesNewSSHGrants`. |
| Outstanding grants | Die within their TTL (300 seconds for SSH access). The agent snapshot carries each key with the latest covering grant expiry, and snapshots exclude expired grants. |
| Installed keys | Enforced by sshd via the `expiry-time` key option, independent of agent or API availability. Even if the agent is killed or the control plane is unreachable, fresh SSH authentication fails after expiry. The agent refuses to install lines without a well-formed `expiry-time`. |
| Snapshot budget | Snapshots are capped at 1 MiB end to end. The server refuses to publish larger snapshots and the agent refuses to install them; truncation is never silent. Oversized host/user pairs need grant cleanup, not bigger buffers. |
| Established sessions | Not terminated. An already-connected SSH session survives revocation; this is out of scope by design. |
| Device revocation | `DeleteDevice` marks the device and host revoked, sets all outstanding `access_grants` for the host expired, and deletes relay grants, so no new issuance or snapshot inclusion is possible. |

## Control-plane outage tradeoff

During an API outage the agent cannot refresh snapshots, but installed
keys still expire on schedule because enforcement lives in sshd, not in
the sync loop. The tradeoff is availability: new logins fail until the
control plane returns and the agent installs a fresh snapshot.

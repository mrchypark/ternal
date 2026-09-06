# Identity Provider and Ternal Data Boundary

The configured OIDC provider owns identity. Ternal owns registered-resource
control-plane state.

## Identity-Provider-Owned Data

Ternal must not store or manage:

- users, passwords, MFA, passkeys, or account recovery state
- local group or role membership as an authority
- user profile lifecycle, account UI, or OIDC provider configuration beyond client env vars
- long-lived copies of identity-provider user records

Ternal reads verified ID-token claims at login and keeps a short-lived signed session cookie. The subject, groups, and explicitly selected policy claims are authorization input, not a local user database. Non-group claims are excluded by default. To use a policy such as `role=support`, configure `TERNAL_OIDC_POLICY_CLAIMS=role` (Helm: `oidc.policyClaims: [role]`) and have the provider include that claim in the ID token. This setting does not request additional OAuth scopes or fetch a provider profile.

Only allowlisted string/string-array claims are accepted; missing claims do not grant access and malformed selected claims reject login. The same verification applies to authorization-code and device flows. Do not allowlist credentials, sensitive profiles, or other confidential attributes: selected claims are visible to the signed-in user both in the signed, **not encrypted**, session cookie and under `user.custom_claims` in `/auth/session`. Unselected email/profile claims remain excluded. The setting affects new logins; existing session snapshots retain their values until logout or expiry. Policy changes themselves are checked against current stored policies.

Limits: at most 16 unique claim names (128 bytes each), 16 string values per claim (256 bytes each), and 1,024 bytes for the complete JSON-encoded custom-claim map, including names and escaping. Empty values, control characters, and leading/trailing whitespace are rejected. Protocol claims and the groups claim cannot be selected as custom claims. The final signed session value, including subject/groups, must also fit within 3,800 bytes; oversized sessions are rejected, never truncated.

## Ternal-Owned Data

Ternal still needs its own database because the identity provider does not know the network resources or SSH access decisions:

- `hosts`: registered resource inventory, including `endpoint_id`, SSH port/user, tags, owner string, status, and heartbeat time
- `policies`: mappings from OIDC group or `claim=value` selectors to host selectors, SSH users, and expiry
- `access_grants` and `access_requests`: grant/request metadata, TTLs, request status, and the policy decision trail
- `audit_events`: immutable Ternal-side events for login, host/policy changes, command issuance, allow/deny decisions, and agent-token actions

Fields such as `owner`, `actor`, `requester`, `approver`, and `user_id` are OIDC subject/group strings recorded for traceability. They are not foreign keys to local users.

Login audit records the actor and action, not a durable copy of provider group membership. `access_grants` store grant metadata only; SSH command payloads are returned at issuance time and are not retained in the grant record. Non-admin host list responses omit `endpoint_id`; explicit SSH command/config issuance is the path that reveals endpoint material to an authorized user.

`ternal-agent claim` uses an OIDC-backed admin session or local development headers to create a Ternal host and issue an agent token. That command is still a Ternal resource operation, not a local user-enrollment or account-management flow.

## DB Necessity Review

OIDC removes the need for a Ternal identity database, not the need for a Ternal database. Keep the local schema scoped to resources, machine participation, policy, and audit:

| Table | Keep? | Reason |
| --- | --- | --- |
| `hosts` | Yes | The identity provider has users and groups, but it does not know Ternal resource inventory, `endpoint_id`, SSH port/user, tags, owner metadata, or agent heartbeat state. |
| `policies` | Yes | The policy is Ternal-specific: it maps OIDC group/claim strings to host selectors and allowed SSH users. Do not sync or copy provider memberships locally. |
| `audit_events` | Yes | Provider audit covers identity events. Ternal still needs append-only records for resource changes, policy changes, command/config issuance, and allow/deny decisions. |
| `access_grants` | Yes, metadata only | Useful for TTL and issuance review. It must not store SSH command payloads, endpoint snapshots, or bearer secrets. |
| `access_requests` | Optional later | In the MVP it records the decision trail. If complex approval workflows remain out of scope, it can be collapsed into `audit_events` later. |
| `users`, `groups`, `passwords`, `mfa`, `passkeys` | No | These remain provider-owned. Ternal should only reference current claim values from signed sessions and audit actor strings. |

The main simplification opportunity is not deleting the DB; it is refusing to add local identity tables and keeping `access_requests` as the only table that may be removed if approval workflows stay out of scope.

## Admin Page Scope

Ternal needs an admin page, but it is a resource admin page, not an account admin page. It should manage:

- hosts/resources
- policy mappings
- agent tokens and heartbeat state
- access grants/requests
- audit events

It may show the current OIDC subject, groups, and admin status as read-only context. User and group management should link out to the configured provider.

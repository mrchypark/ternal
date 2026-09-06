# GoAuthy integration

Ternal is an independent OpenID Connect relying party. GoAuthy is one optional
provider; Ternal does not import its packages, share its database, or require it
to run for non-OIDC repository tests.

## Consumer contract

Register one confidential client with:

- the exact Ternal callback, normally `https://<ternal-host>/auth/callback`;
- Authorization Code and `client_secret_post`;
- S256 PKCE only;
- `openid groups`, with `groups` released in the ID token;
- an ID-token audience containing the client ID and matching `azp` when the
  provider emits `azp` or multiple audiences.

Configure Ternal through the provider-neutral `TERNAL_OIDC_*` variables in
[configuration](configuration.md). Discovery, JWKS, issuer, endpoints, state,
nonce, PKCE, audience, authorized party, subject, and groups are validated by
Ternal. Access policies, SSH keys, device state, host-key pins, and 300-second
relay grants remain Ternal data.

Ternal keys account-owned data by a stable digest of the verified `(issuer,
sub)` pair and retains raw `sub` only for display. Sessions issued before issuer
binding are rejected. A change of issuer intentionally creates distinct local
principals. This Go release supports greenfield state only and never falls back
to subject-only ownership; a future data migration would need an explicit,
trusted old-issuer mapping.

`POST /auth/logout` revokes only the Ternal session. Ternal does not retain
provider refresh tokens and does not currently initiate provider-wide logout.

## Device Flow status

Ternal's CLI requires the Device Authorization Grant to accept `openid groups`
and return an ID token that satisfies the same issuer, audience, subject, and
group checks as browser login. The GoAuthy revision covered by the live test
below has not yet been qualified for that path. Ternal does not fall back to an
app-specific GoAuthy API or weaken its authorization model.

RFC 8628 by itself guarantees an OAuth token response, not an ID token. Device
login with `openid` and an ID token is therefore an optional provider capability
that must be documented and verified for the configured client; it is not
assumed from discovery metadata alone.

## Local live verification

The opt-in test starts one disposable GoAuthy standalone process directly from
an independent checkout. It does not use Docker, Dory, Kubernetes, or another
application:

```sh
GOAUTHY_ROOT=/path/to/goauthy ./deploy/e2e/goauthy-standalone.sh
```

It verifies real discovery/JWKS, confidential Authorization Code with S256,
ID-token issuer/subject/groups, derived principal identity, code replay denial,
and missing/plain/wrong-verifier PKCE rejection. Device success remains a
blocked gate until the provider supplies the documented OIDC Device capability.

# IED v0.2.7 qualification — 2026-09-08

**Incomplete: the immutable v0.2.7 release cannot pass the requested scenarios.**
Release integrity, isolated deployment, real Rauthy CLI device login, and SSH key
registration passed their measured checks. Browser portal inspection found failures.
Admin authorization-code login, enrollment, and real SSH remain incomplete.
No locally rebuilt application binary was deployed or substituted for a release
asset. No development-header login was used.

## Observed starting state

The supplied greenfield description was stale. Context `gke_ied-cluster` already
contained Helm releases `ternal` and `ternal-ha-qual`, both chart `ternal-0.3.14`,
with running API and relay workloads. Namespace `ternal` already had the neutral
label `environment=ied`. These resources were preserved.

The existing Ternal HTTPRoute serves `/`, `/relay`, and `/ping` on
`ternal.dev.conalog.com`; the Rauthy route serves `/auth/v1` on that hostname.
Both routes report Accepted and ResolvedRefs. Sharing the hostname here is
path-based routing, not evidence of a route collision.

The specifically linked `08af` user-scenarios document is newer than v0.2.7 and
describes server-side logout revocation, stronger key acknowledgements, and
object-store recovery without PVCs. The explicit user request for an empty PVC
was followed for this v0.2.7 baseline. A later-release qualification must resolve
that storage-contract difference rather than silently changing the criterion.

## Evidence and results

Local evidence directory: `/tmp/ternal-v027-qualification-20260908`.
It contains a private `secrets/` directory: **do not copy, print, or publish it**.
All reports referenced below contain only sanitized evidence.

| # | Scenario | Result and evidence |
| --- | --- | --- |
| 1 | Release integrity | PASS. All 14 entries in `release-sha256.txt` pass SHA-256 verification. Provenance source commit and pinned Pigeons source/patch match the fetched tag. Fresh official cosign verification validates the exact GHCR digest against the GitHub Actions issuer and tag workflow identity. See `integrity/RESULT.md` and `integrity/cosign-verify.json`. |
| 2 | Greenfield deployment | PASS for the measured deployment checks. Public chart installed into new namespace `ternal-v027-qual`, release `ternal-v027`, with a newly allocated 1 GiB `standard-rwo` PVC. API and managed relay Ready; public HTTPS `/health`, `/ready`, and relay `/ping` return 200. Non-root UID 65532, read-only root filesystem, dropped capabilities, no privilege escalation, and RuntimeDefault seccomp verified. Separate app/auth Gateway routes and TLS certificates are now ready. |
| 3 | Admin browser login | NOT RUN to authenticated completion; FAIL for logout replay in a source regression. Rauthy now has a configured client, and Ternal login entry redirects to its exact authorization endpoint with the correct client/redirect, state, nonce, and Secure/HttpOnly state cookie. `v0.2.7` logout still clears a cookie without revoking its signed value. A real store/router test replays it and receives `authenticated:true`. This test is not browser or provider-login evidence. |
| 4 | Batch and enrollment | NOT RUN live. Static review also identifies separate batch reservation and host/device writes, later addressed by `638d97f` (atomic enrollment). |
| 5 | Agent lifecycle | NOT RUN live. Public Linux amd64 agent archive downloaded, verified, and extracted; no device was enrolled or agent transport started. |
| 6 | User login and SSH keys | PARTIAL. Public CLI completed real Rauthy device flow with browser approval. Stored file has only cookie, csrf_token, expires_at, mode 0600. Public CLI registered a canonical ed25519 key; malformed key rejected 400. Cross-user isolation and browser authorization-code login remain unverified. See CLI evidence and local session incident below. |
| 7 | Policies and visibility | FAIL by tagged implementation: authorized non-admin host lists serialize full host objects, including `endpoint_id`. Real authenticated non-admin policy access returns 403; host filtering remains untested without enrolled hosts. |
| 8 | Managed relay SSH | NOT RUN; known identity-binding defect. v0.2.7 CLI does not pass an explicit common `--key-dir` to endpoint lookup and `fly`. The old smoke harness injects a wrapper that fixes this externally and must not be accepted as evidence for the unmodified public CLI. |
| 9 | Direct SSH | NOT RUN. No direct address, SSH banner, or selected-path evidence has been collected. |
| 10 | Security failures | PARTIAL. Live callback rejects absent/wrong bearer (401), malformed endpoint (400), and well-formed ungranted endpoint (403), all with body `false`. Unauthenticated hosts/keys/policies return 401. These are HTTP admission probes, not a complete relay connection test. Expiry, wrong client grant reuse, wrong host key, and OIDC negatives remain unverified live. |
| 11 | Authorized keys | NOT RUN live. v0.2.7 ACK verifies the signed request but does not compare generation/digest against the current server snapshot. Agent-side rejection alone cannot establish the newer ACK contract. |
| 12 | Device revoke | NOT RUN live. No enrolled device, active SSH grant, or running agent exists in this qualification namespace. |
| 13 | Portal and audit | PARTIAL / FAIL. Using the real CLI session, SSR, htmx navigation, non-admin navigation visibility and 390px layout passed measured checks. Skip link does not focus workspace; mobile hides Sign out; desktop logout form returns 403. Header-authenticated logout returns 200 and clears browser cookies, but replay probe was already expired and is inconclusive. Audit coverage remains incomplete. |
| 14 | Restart, readiness, cleanup | PARTIAL. API Pod recreated and the same PVC UID/volume retained. Injecting a nonexistent readiness path removed all ready Service endpoints; an in-cluster request from the relay failed. Original `/ready` probe restored and API recovered. Device-state persistence and fail-closed trust rollback remain unverified. Cleanup inventory follows. |

Release source commit: `593d4f5ebe1786e0fd2968391286a1316c1b0170`.

Image index digest, matching remote `v0.2.7`, provenance, and running API imageID:
`sha256:aeda97b0dd4b0533b1eb08ac8d06e6befe5eded8e197848bf3add3448106cb97`.

Signer identity:
`https://github.com/mrchypark/ternal/.github/workflows/release.yml@refs/tags/v0.2.7`.
Issuer: `https://token.actions.githubusercontent.com`.
No separately verified Git tag OpenPGP signature is claimed.

Deployment uses `v0.2.7@sha256:…` and a pre-created Secret referenced by name.
Secret contents were not passed as command arguments, Helm values, or report
data. The initial unregistered OIDC client has been replaced by the configured
`ternal-v027-qualification` client. Its registered redirect is
`https://ternal-v027-qual.dev.conalog.com/auth/callback`. The Rauthy bootstrap
logging incident and credential replacement are recorded below.

Additional machine-readable evidence: `http-baseline.json`, `relay-denial.json`,
`runtime-contract.json`, `readiness-isolation.json`, `pvc-before-restart.json`,
`pvc-after-restart.json`, and `final-pods.json`.

## Reproduced blocker and later fixes

The detached exact-tag worktree contains one added regression file:
`source/internal/api/qualification_logout_test.go`. Running:

```sh
go test ./internal/api -run '^TestQualificationLogoutRejectsCapturedSession$' -count=1
```

fails because the captured signed session remains authenticated after logout.
`auth-regression.txt` records the result. It uses synthetic local claims and the
real store/auth/router, without dev headers; it does not authenticate with OIDC.

The main checkout was initially `17bae95`, older than v0.2.7. Work now uses branch
`feature/qualify-ied-scenarios`, based on fetched main `6b3466c`; no existing user
changes were present or overwritten. Existing focused logout and persistent
proxy-identity tests pass there. Source tests do not qualify deployed release
bytes.

Latest public release observed: `v0.3.14`, commit `5d2b942`. It contains logout
revocation and endpoint-redaction fixes, but **does not contain** `00ed831`
(`fix(cli): bind pigeons fly to granted persistent identity`). That fix is in
main `6b3466c`. No new tag, release, image build, or publication was performed.
A new immutable public release is required before that fix can be used under
the user's artifact-only constraint. Replacing or retagging v0.2.7 is not a fix
to its historical qualification result.

The public v0.2.7 chart's NOTES incorrectly claims emptyDir/no PVC even with
`persistence.enabled=true`; actual rendered and running resources contain a PVC.
Use the measured resources, not that unconditional NOTES text, as evidence.

### Follow-up verification at 2026-09-08 01:02 UTC

`go test -race -count=1 -timeout=10m ./...` passes at main `6b3466c`;
`main-race-tests.txt` preserves the output. The public-release list still ends
at v0.3.14, and both isolated qualification Pods remain Ready with the same PVC.

The existing main candidate run `34078415437` failed Linux amd64's Pigeons
`config::tests::stored_telemetry_choice_round_trips`: immediate config reload
returned `None` instead of `Some(false)`. Existing
[PR #56](https://github.com/mrchypark/ternal/pull/56), head `fe56de27`, already
pins upstream commit `37d169cc88765a7d3e2569e6d1bb79056d4ab18c`, which adds
`file.flush().await?` before returning from config save. Its CI run
`34173796222` passed. The PR remains open and was not modified or merged here.

The newly pinned public source archive and Cargo.lock hashes were independently
verified (`pr56-source-integrity.json`). Linux agent packaging, Unix CLI
packaging, and candidate-workflow contract checks also pass on that PR checkout.
These packaging checks use fixtures and do not prove a native transport build.

Candidate run `34175127600` was dispatched on the PR branch and finished with
failure at `Assert immutable source identity`. The existing workflow accepts
only `refs/heads/main` or `refs/tags/v*`; checking that guard before dispatch
would have avoided the wasted run. The guard was not bypassed. No native
candidate archive was qualified by this attempt, and no job remains running.
The next valid candidate build requires PR #56 to be integrated into main first.

## Cleanup inventory

Only these newly created resources belong to this qualification:

| Resource | Identifier |
| --- | --- |
| Kubernetes context | `gke_ied-cluster` |
| Namespace | `ternal-v027-qual` |
| Helm release | `ternal-v027` |
| StatefulSet / Pod | `ternal-v027` / `ternal-v027-0` |
| Relay Deployment | `ternal-v027-relay` |
| Services and ConfigMaps | `ternal-v027`, `ternal-v027-relay` |
| Runtime Secret | `qualification-runtime` |
| PVC | `data-ternal-v027-0` |
| PVC UID | `963bf7ce-5b3f-473b-bc3d-49d6f86f4a8c` |
| Allocated PV | `pvc-963bf7ce-5b3f-473b-bc3d-49d6f86f4a8c` |
| Devices, batches, policies | None created |
| User key | `f454ccf1-b740-49fe-b247-a543b399504f`, allowed qualification account |
| HTTPRoute, certificate, provider client | See additional inventory below |

After qualification no longer needs the fresh database:

```sh
helm uninstall ternal-v027 --kube-context gke_ied-cluster -n ternal-v027-qual
kubectl --context gke_ied-cluster -n ternal-v027-qual delete pvc data-ternal-v027-0
kubectl --context gke_ied-cluster -n ternal-v027-qual delete secret qualification-runtime
kubectl --context gke_ied-cluster delete namespace ternal-v027-qual
```

These cleanup commands were not executed. The two pre-existing releases in
namespace `ternal` are outside this cleanup scope. Preserve sanitized evidence
before removing the temporary local workspace and its private generated secrets.

## Remaining release decision

Unchanged v0.2.7 bytes cannot pass all fourteen scenarios. A new public release
containing the fixes is required; no historical tag should be replaced. The
OIDC setup blocker has been resolved with Rauthy. A later-release qualification
must also explicitly reconcile the requested PVC with the newer documented
object-store recovery contract. This goal remains incomplete.

## Subsequent direction: Rauthy now, Goauthy later

The user considered PocketBase, then explicitly selected Rauthy for the current
work. No PocketBase dependency, schema, or authentication path was added.
Goauthy remains the intended eventual provider; the standard OIDC boundary is
retained.

An isolated Rauthy deployment now runs in `ternal-v027-qual`, preserving the
pre-existing shared Rauthy instance. It uses the repository's pinned public
image `ghcr.io/sebadob/rauthy:0.35.2@sha256:a73dbf58359f7fb009b6257759107cf886647109906a3fe06755e5cff07b5661`.

- App: `https://ternal-v027-qual.dev.conalog.com`.
- Exact issuer: `https://ternal-v027-auth.dev.conalog.com/auth/v1/`.
- Client: `ternal-v027-qualification`; authorization-code and device-code grants,
  with `openid groups` scopes.
- Bootstrap records: `admin@qualification.invalid` (`ternal-admins`, `platform`),
  `allowed@qualification.invalid` (`platform`), and
  `denied@qualification.invalid` (no groups). Their passwords are protected
  local files and Secret data, not report content. Allowed-user device login was
  exercised; admin and denied-user role mapping remain unverified.
- Both TLS certificates are Ready; both HTTPRoutes are Accepted/ResolvedRefs.
- `rauthy/device-preflight.txt` proves client-authenticated device start and
  `authorization_pending` before approval. It does not prove device approval,
  ID-token issuance, or a successful CLI session.
- `rauthy/public-entry.json` proves public HTTPS health/readiness/relay ping and
  the exact Ternal OIDC login-entry configuration. No browser callback was
  completed against v0.2.7: its request logger still requires separate handling
  before authentication codes can be kept out of application access logs.

### Bootstrap incident and recovery

The first bootstrap payload incorrectly encoded `secret` as a string instead
of the tagged `Plain` object expected by this Rauthy version. Its deserialization
error included the generated, not-yet-registered client secret in container
logs. Tool output was redacted, but that does not erase the container-log
exposure. The client secret was replaced in both protected local material and
Kubernetes Secrets; the old value was never successfully registered as a client.
The failed disposable Rauthy database was recreated, and the corrected typed
client/password payloads were verified before bootstrap. This did not delete
the Ternal PVC or any pre-existing deployment. No claim is made that historical
cluster log copies were erased. Current Rauthy access logging is disabled.

The initial `webauthn.rp_origin` also needed the explicit `:443` required by the
pinned release. After correction and fresh bootstrap, Rauthy has zero restarts
and passes the device-grant preflight.

### Additional cleanup inventory

In namespace `ternal-v027-qual`: Deployment, Service and HTTPRoute
`rauthy-qualification`; Secret `rauthy-qualification-bootstrap`; PVC
`rauthy-qualification-data` (current UID `95aa88ec-1a8a-466a-9567-c19f23772890`).
The Ternal Helm release is now revision 3 and also owns HTTPRoute `ternal-v027`.

In namespace `haproxy-gateway-system`: Certificates and generated TLS Secrets
`tls-ternal-v027-auth` and `tls-ternal-v027-app`; dedicated Gateway
`ternal-qualification-gateway` with listeners `https-ternal-v027-auth` and
`https-ternal-v027-app`. Delete this dedicated Gateway during cleanup. The shared
`dev-gateway` is managed by Flux; its reconciliation removed the initial temporary
listeners. Both routes now target the dedicated Gateway, which remained
Accepted/Programmed after a reconciliation interval. No shared Gateway edits
remain necessary, and no additional load balancer was provisioned.
Cleanup remains unexecuted. Rauthy bootstrap files remain under the private
`secrets/rauthy/` directory and must not be copied with shareable evidence.

The OIDC client-preparation blocker is now resolved. Device login has now passed; browser authorization-code login
and the remaining fourteen-scenario qualification are still incomplete; the
immutable v0.2.7 product defects remain unchanged.

## Real CLI and portal checks

Public v0.2.7 `ternalctl login` completed Rauthy device flow with a real browser
approval by `allowed@qualification.invalid`. The API reported authenticated,
non-admin identity and group `platform`. This is device-flow evidence, not
browser authorization-code evidence. The resulting local session file contained
only `cookie`, `csrf_token`, and `expires_at`, with mode 0600; no provider-token
fields were persisted. The public CLI successfully submitted an ed25519 key.
The canonical key matched on retrieval; malformed key submission returned 400,
non-admin policy access 403, and host listing 200.

The same real session was placed in a separate browser context for portal checks.
SSR and htmx key navigation worked; the latter issued `HX-Request: true` without
a document navigation. Non-admin navigation omitted Policies and Audit. At
390×844 there was no horizontal overflow. However, the skip link left focus on
BODY, and the Sign out button was hidden on mobile. Desktop form logout returned
403 and retained its cookie. An explicit logout with the session CSRF header
returned 200 and cleared the browser cookie. The subsequent replay probe ran
after the session's 02:01:30 UTC expiry: its unauthenticated response is
**inconclusive for logout revocation**, not a pass. The separate exact-tag source
regression remains evidence of v0.2.7's replay defect.

Evidence: `cli-login/result.json`, `session-verification.json`, `key-checks.json`,
`portal-checks.json`, `browser-logout.json`, `api-logout.json`, and
`live-logout-replay.json` under the local evidence directory. Browser screenshot:
`output/playwright/rauthy-device-session-mobile.png` in this checkout.

### Local session overwrite incident

The first macOS CLI login attempted isolation with XDG_CONFIG_HOME, but Go's
macOS UserConfigDir uses `~/Library/Application Support`. Consequently the public
CLI overwrote an existing `ternal/session.json` there. Its earlier birth time
confirms it predated this test. No backup was made, and no recovery copy was
found in that directory. The prior session has **not** been restored; the user
will need to log in again for that prior session. The test session was copied
to the protected evidence directory and is now expired. The test driver now
refuses Darwin execution to prevent recurrence; future CLI logins require an
isolated Linux environment. No further macOS login was attempted. After byte-for-byte comparison with the
protected captured test session, the expired test file was removed from the
shared configuration path; `cli-login/local-session-cleanup.json` records this.
This cleanup does not restore the overwritten prior session.

### Source candidate mobile correction

The shared portal shell now renders the identity/logout block at mobile widths
by removing its responsive hiding classes. This is a source change on
`feature/qualify-ied-scenarios`, not a substituted release binary. Current main
already contains workspace focus and form-CSRF handling fixes absent from
v0.2.7. Live v0.2.7 failures remain failures until a public fixed release is
qualified.

Validation of the mobile source change: pinned frontend assets rebuilt with
verified checksums; `go test ./internal/web -count=1` passed. A temporary Go
renderer fixture (removed afterward) rendered the actual page shell, and Chrome
with the generated CSS confirmed Sign out visible and no horizontal overflow at
390px and 1280px. See `cli-login/source-mobile-check.json`. This preview used
synthetic presentation data, not a claimed authentication or public-release test.

Current main removes Chi's application request logger (commit `73e5c0f`); it
does not implement query redaction. v0.2.7 still logs RequestURI, so completing
an authorization-code callback there would expose code/state to application
access logs. No such callback was completed. Proxy logging requires a separate
deployment check when a fixed public release is qualified.

## Authorized new-release continuation

The user approved retaining v0.2.7 as the failing baseline and preparing a new
public release for full qualification. Rauthy remains the selected provider.
PR #56 was merged as `69d381e`; PR #57 adds mobile logout visibility and an
explicit `TERNAL_CONFIG_DIR` for platform-independent session isolation. The
final repository suite passed, including race tests, vet, packaging contracts
and Helm server dry-run. CI passed before #57 was merged as `4d9d143`.

A final caller review found three remaining smoke invocations using XDG config
isolation and a transport wrapper overriding the CLI's identity. Commit
`bf05358` removes the wrapper and uses the explicit config root in smoke/package
callers. The Unix package test and transport-matrix fixture checks pass; those
do not claim real transport qualification.

The shared CLI unit tests also used the macOS-ineffective XDG isolation before
this correction. Their identifiable synthetic `disk-session` fixture was
removed from the shared configuration path after matching its exact fields.
The new lifecycle regression checks the target path before writing, and the
tests now use isolated directories. The previous real user session remains
unrestored.

GitHub active identity and repository-local commit identity are now `mrchypark`.
The checkout, local author history, repository collaborators and contributors
were checked; only the intended GitHub contributor/collaborator is present.
The user's other local GitHub login was not deleted.

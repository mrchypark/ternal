> 최종 결과와 정리 확인: [2026-09-09 결과 보고서](ied-qualification-result-2026-09-09.md). 아래는 실패·진행 중 상태를 보존한 시간순 이력입니다. 원시 임시 디렉터리는 정리했으며 보존 증거는 `output/ied-qualification-20260909`에 있습니다.

# IED public candidate qualification — 2026-09-08

Status: incomplete; public release published and no-PVC API/relay deployed. No candidate application
binary has been deployed from local source.

The user approved a new public release, selected Rauthy, and explicitly selected
no-PVC persistence with one disposable object-store bucket. Delete the test
resources after all qualification is complete. v0.2.7 remains a failed historical
baseline in the separate report.

## Release identity

- Tag: `v0.3.15-rc.1`
- Source: `9494ed677e602c590f19908cc6ab95e48d347535`
- Workflow: https://github.com/mrchypark/ternal/actions/runs/34182740957
- GitHub actor and triggering actor: `mrchypark`
- Integrated PRs: #56 (Pigeons config flush), #57 (mobile logout and CLI session
  isolation), #58 (remove smoke identity wrapper and isolate remaining callers).
- Final source repository suite, Helm server dry-run and applicable PR CI passed.
  Native release, image scan/signature and public-asset checks passed.

## Disposable storage and cleanup inventory

| Resource | Identifier / purpose |
| --- | --- |
| Project | `patch2-the-new-era`, number `602454948273` |
| Cluster/context | IED, `gke_ied-cluster` |
| Bucket | `gs://ternal-qual-20260908-602454948273` |
| Bucket region | `asia-northeast3` |
| Bucket policy | Uniform access, enforced public-access prevention, soft delete disabled for complete test cleanup |
| Namespace | `ternal-v027-qual` (isolated qualification namespace, also contains retained v0.2.7 baseline and Rauthy) |
| Kubernetes ServiceAccount | `ternal-rc-data` |
| Bucket IAM | `roles/storage.objectAdmin` only on this bucket for the exact Workload Identity principal below |
| Data cluster ID | Prepared: `ternal-rc-20260908` |
| Object prefix | Prepared: `clusters/ternal-rc-20260908` |
| PVC | None for the new Ternal deployment; old baseline/Rauthy PVCs remain separately inventoried |

Principal:
`principal://iam.googleapis.com/projects/602454948273/locations/global/workloadIdentityPools/patch2-the-new-era.svc.id.goog/subject/ns/ternal-v027-qual/sa/ternal-rc-data`.
No service-account JSON key is created or distributed.

Cleanup after qualification, not yet executed:

```sh
# First stop the new Ternal writer/Helm release once its final name is recorded.
gcloud storage rm --recursive gs://ternal-qual-20260908-602454948273 --project patch2-the-new-era
kubectl --context gke_ied-cluster -n ternal-v027-qual delete serviceaccount ternal-rc-data
```

Deleting the dedicated bucket also removes its bucket-scoped IAM binding.
Do not delete or modify `gs://ternal-ied-602454948273`, which belongs to the
pre-existing service. Add the new release, runtime Secret and runner resources
to this inventory when created. Preserve sanitized qualification evidence;
never copy protected session/password/key files into the report.

Additional created resources in `ternal-v027-qual`:

- Secret `qualification-rc-runtime`: separate generated session/data/relay keys,
  existing isolated Rauthy client secret; protected files stay outside the repo.
- Pod `ternal-qualification-runner`: public Ubuntu 24.04 index
  `sha256:33ceb71981b602c1a7443a53469e4dba065f7503eab3078a2d7a57a2ab987517`,
  amd64, disposable emptyDir, no mounted Kubernetes API token, no host mounts.
  It will run the public Linux CLI/agent bundles and an isolated SSH daemon.
  OS dependencies are installed from Ubuntu repositories; no Ternal binary is
  compiled in this runner. Delete this Pod and Secret after qualification.

- Pod `ternal-qualification-client`: same pinned Ubuntu image and no API token,
  host mounts or host networking. It has only the additional `NET_ADMIN`
  capability for firewall changes confined to its own network namespace.
  This separates the client transport identity from the agent Pod's identity.
  Delete it after qualification.

The actual dedicated bucket metadata confirms public access prevention enforced,
uniform access enabled, and soft-delete retention zero. Metadata and bucket IAM
evidence are stored in `/tmp/ternal-v0315rc1-qualification-20260908/bucket.json`
and `bucket-iam.json`.

## Public deployment evidence

Release workflow `34182740957` completed successfully for image, all native
platforms and publication. All 15 checksum entries and provenance asset hashes
match; the source and pinned Pigeons commit match the selected tag. Fresh cosign
verification passed for the exact tag-workflow identity. Published tag and
running API imageID both match
`sha256:0b708a5cbdf0fc973ebec15dea7a32fec647da6bddee40bf372600228b253bf8`.

Helm release `ternal-rc`, revision 2, uses the public chart and a fresh GCS prefix.
Its API `/data` is emptyDir; no new API PVC exists. API/relay are Ready and public
`/health`, `/ready`, `/ping` return 200. Runtime UID 65532, read-only root, dropped
capabilities, no privilege escalation and RuntimeDefault seccomp were checked.
GCS now contains certified-state objects under the dedicated prefix.

The public app hostname still contains the earlier qualification label:
`https://ternal-v027-qual.dev.conalog.com`. Its route now targets `ternal-rc`.
The old `ternal-v027` release was advanced to revision 4 with Gateway disabled;
its baseline resources/PVC are retained separately.

Authorization-code login found an isolated Rauthy configuration omission:
S256 PKCE was not enabled for the client. Password authentication itself passed.
The bootstrap provider administrator enrolled a Chrome virtual WebAuthn key
`qualification-browser`, completed MFA login, and reached the management UI.
Client PKCE was corrected to S256 and authorization-code login passed for
admin, allowed and denied test users. This is a test authenticator, not evidence of physical-key testing.


## Live application evidence (13:30 UTC checkpoint)

- Real Rauthy browser authorization-code sessions: admin has `ternal-admins`
  and `platform`; allowed user has `platform`; denied user has no groups.
  The pinned Rauthy bootstrap represented `groups: []` as an empty group string;
  setting this test user's groups to null fixes the provider data without
  relaxing Ternal's claim validation.
- Public Linux CLI device login was approved in the allowed user's browser.
  The mode-0600 local session contains only cookie, csrf_token and expires_at;
  no provider token fields. Its real OpenSSH key was registered.
- Public agent enrolled device `9512706e-1ce2-4fef-9c50-be6b91a21770`,
  host `4386d3a4-6116-4ad5-ae36-9f92e20121e1`, serial `IEDRC-000001`.
  Supervisor and its public Pigeons child are running as test Unix user ops.
- Policy groups=platform → tag:qualification=iedrc → ops permits the allowed
  user. The denied user sees no hosts and receives 404 for SSH issuance;
  requesting daemon also returns 404. Host lists contain no endpoint_id.
- Live signed heartbeat baseline returns 200; independently invalid signature,
  serial, endpoint, host fingerprint and timestamp each return 401.
- Actual agent authorized_keys synchronization passes repeated atomic inode
  replacement. Scratch-target rollback and same-generation equivocation are
  rejected; signed old-generation/wrong-digest ACKs return 409 and current ACK 204.
- Actual SSH through the public CLI proxy and strict fingerprint helper passes
  stdin/stdout round-trip, stderr marker, EOF half-close/drain and exit code 23.
  Repeated with client-Pod UDP blocked except DNS: same pass, proving relay use
  without a direct UDP connection. The test harness resolves KnownHostsCommand
  to the absolute public CLI path, as the real CLI ssh command does.

Direct path, remaining security/expiry/revoke cases, restart/object-store failure,
full browser UX/audit and final cleanup remain incomplete. No full scenario pass
is claimed at this checkpoint.


Direct path also passes the same stdin/stdout/stderr/exit-23 check using explicit
`10.11.6.78:58752`, with all client TCP denied except loopback, DNS and the API
Service port. CLI API traffic uses a test-only loopback TCP tunnel to the Service
with the real signed user session; no development headers are supplied. Public
relay HTTPS is blocked. Thus the successful transport is direct UDP, not relay.
The tunnel is infrastructure forwarding only, not a substituted app binary.


## Additional live checks and rc.1 blocker

- Public `ternalctl ssh IEDRC-000001` itself printed the stdin-fed command's
  marker and propagated exit 17. Wrong fingerprint terminated before command
  execution with `host key rejected: fingerprint mismatch`; no fallback occurred.
  Endpoint-only proxy invocation failed immediately for lack of an explicit route.
- Actual persistent client endpoint grant has expires_at-created_at exactly 300;
  TTL 301 returns 400. Callback missing/wrong bearer returns 401, a different
  endpoint returns 403, active endpoint returns 200/true. After actual wall-clock
  expiration it returns 403/false (`relay-expiry.json`).
- Agent synchronization after SSH grant expiry removed the previously installed
  key and increased generation. Graceful supervisor restart preserved device key,
  enrollment identity and persistent Pigeons endpoint.
- Recreated `ternal-rc-0` has a new Pod UID and fresh emptyDir, recovers all device,
  host, policy and key IDs from the bucket, and retains session revocation.
- Manufacturing and authorization-negative scripts/results in the protected
  qualification work directory record sequential serial 000002, quantity closure,
  third enrollment denial, expiry denial, legacy single-use token replay denial,
  key isolation/normalization and expired-policy denial, with cleanup IDs.
- Real Rauthy nonce tampering (browser URL redirected to a different nonce, so the
  provider actually signs that nonce) is rejected 401. Wrong state and consumed
  callback replay also return 401. A request-URL-only override was insufficient
  because the provider SPA used the browser URL; this harness attempt is not
  counted as the negative result.
- Valid local session plus null, previous-origin or provider Origin returns 403.
  Separately, correctly signed local sessions with wrong issuer or expired time,
  and a tampered signature, are unauthenticated. This is the local-session boundary,
  not a claim that wrong provider JWT audience has yet been tested.
- Admin navigation exposes Hosts/Keys/Policies/Access/Audit; ordinary users omit
  Policies/Audit. htmx navigation returns a fragment and retains the document.
  A malicious test host name appears literally with no injected image/execution.
  Mobile width 390 has visible logout and no horizontal overflow. Keyboard skip
  link focuses the workspace.

**rc.1 does not pass browser logout:** `Referrer-Policy: no-referrer` makes native
Chrome form POST send Origin:null, correctly rejected 403 by strict CSRF origin
checking. A diagnostic browser-only `strict-origin` response override produces
same-origin POST, logout 200 and captured-cookie replay unauthenticated. This
is diagnosis, not a public-artifact acceptance pass. PR #59 changes the shared
header to strict-origin and updates its regression; full tests and PR CI passed,
with no unresolved review threads. Merged source
`16be764b98769422a9d97b38a93b93d680d0d4ae`; candidate workflow
`34233469202` is running before the next public release.


Live failure checks: temporary removal of the disposable bucket's exact KSA
objectAdmin binding made a durable host write return 500. The binding was
restored in a finally block, and a subsequent durable write succeeded. Separately,
a deliberately failing readiness probe removed the API from public traffic
(503); the original probe and Ready Pod were restored. This distinguishes storage
write failure from the readiness-routing check; no claim is made that a write
failure alone changed readiness.

Diagnostic handling incident: one test session's CSRF token was included in a
session-info tool output. Its cookie/password/provider tokens were not printed.
The affected denied-user session was explicitly revoked and captured-cookie
replay rejected; a fresh provider login replaces it. Do not claim this diagnostic
run had zero credential-adjacent output. No value is reproduced in this report.


An isolated negative OIDC protocol fixture used the exact public rc.1 API image,
not a locally rebuilt API. Its loopback test provider completed authorization-code
and JWKS/token exchange: valid signed control 302; wrong issuer, wrong audience,
wrong signature and expired ID tokens each 401. The fixture is distinct from the
real Rauthy positive-login evidence. Its no-PVC Pod, ConfigMap and generated Secret
were removed; `oidc-negative-fixture/results.json` retains status-only results.

Expired and never-granted client identities also failed to receive an SSH banner
through the real relay with UDP blocked. Both transport attempts timed out; the
explicit denial evidence is the separately tested callback 403, not a claim that
the transport surfaces a prompt rejection message.

The user requested the original logo/favicon. Existing source assets were found
under `frontend/src/assets/brand`. The prior candidate workflow was cancelled to
include this requested change in the next candidate/public release.


Original branding is merged in PR #60 at
`4bee4861a0b0b4a0b35a7a6f59f5ddd76e2b034d`. The shared page now references the
unaltered wordmark and favicon; frontend copies are byte-identical to their source
PNGs. The logo is 640×251 and icon 214×256. Tailwind now scans only Go sources,
not its own generated CSS; two consecutive builds match. Full repository checks
and PR CI passed, with no unresolved review threads. Candidate workflow
`34235606291` builds this exact release source under mrchypark.

The Linux amd64 candidate CLI and agent archives are byte-identical to the
already-qualified public rc.1 archives (`candidate-linux-equivalence.json`).
PR #61 separately corrects the standalone local Rauthy fixture to enable S256
and asserts this in its smoke check; these fixture files are not included in the
API image, Helm chart or native bundle payloads. The live isolated client already
has this setting. Its CI passed and it merged at `a423121d137459856ec97837be896b24061d4132`.

The direct-unavailable attempt was inconclusive because its CLI session had
expired before transport invocation. It is not counted as a direct-route denial;
repeat with a refreshed device-flow session on the next public release.

## Public rc.2 follow-up — 2026-09-08

Public release `v0.3.15-rc.2`, source
`4bee4861a0b0b4a0b35a7a6f59f5ddd76e2b034d`, workflow `34238175774`,
completed successfully under mrchypark. All 15 release checksum entries and
provenance asset hashes match; all eight native archives equal the verified
candidate and public rc.1 bytes. Fresh cosign verification passed for
`ghcr.io/mrchypark/ternal@sha256:1248fdf5e52fcaa44ac03b6ee4c27c83053325e183195c0221aaeb762087a221`.
Public chart deployment is Helm revision 3; the runtime imageID matches this
signed digest, health/ready return 200, and the API Pod has no PVC.

The actual Chrome native sign-out now returns 200 with the correct app Origin;
replaying the captured pre-logout cookie is unauthenticated. No response-header
interception was used. Mobile 390px shows the original 640×251 logo, visible
Sign out, and no horizontal overflow. Both served PNGs match the source files
byte-for-byte. A fresh actual provider device-flow approval produced a mode-0600
CLI session with only cookie, csrf_token, expires_at fields.

On rc.2, firewall-constrained relay and direct SSH both passed stdin/stdout,
stderr marker, and exit23. Raw relay proxy received the actual SSH banner.
Unreachable explicit direct port9 failed with no command output and transport
failure evidence, not an expired-session error. Endpoint-only proxy invocation
was rejected with its exact missing-route error. A wrong expected fingerprint
failed; independently replacing the actual sshd peer key also failed before
command output with the exact mismatch diagnostic. The original key was restored
in finally, and normal SSH exit23 succeeded afterward.

**rc.2 still fails existing-SSH termination after device revocation.** The held
SSH command emitted its exact marker before DELETE returned200. The public agent
then became revoked/child stopped, but the held client had not exited within60s.
Do not count the scenario as passed. Independently verified after revocation:
signed heartbeat401, discovery404, new SSH grant404, new relay grant404, and
both device/client relay callbacks403. First device IEDRC-000001 is now revoked;
do not reenroll it or rerun a driver that assumes it is active.

Two preliminary revocation-driver failures occurred before DELETE: a concurrent
manual sync and a status-file race during supervisor shutdown. The verifier was
corrected to wait for actual old-process exit and for the running agent to reflect
the exact newly granted public key. These harness failures are distinct from the
subsequent real held-SSH failure above.

Detailed rc.2 status-only evidence is under
`/tmp/ternal-v0315rc2-qualification-20260908`. The local named agent fixture was
not run because it repurposes HOME; equivalent actual public-bundle checks are
recorded instead. The original broad completion criteria remain unchanged.

Actual stale-CURRENT test returned `startup-failed-closed-latest-restored` and
`healthy-revoked` after restoration. Supported Helm rollback to public rc.1 also
preserved revoked device, discovery/new-SSH denial, and the rc.2 logged-out cookie
revocation. Public rc.2 was restored at revision5 with its signed runtime digest
and ready200.

**This does not qualify complete historical object-store restoration.** Source
inspection of Rhiza v0.12.1 recovery found no independent persistent floor for a
fresh no-PVC process. A mutually consistent historical CURRENT, archive head and
reachable blocks/extents can satisfy internal certification while replaying
pre-revocation state. This is a source-derived defect hypothesis, not a live
whole-bucket test result. It requires an independently retained monotonic trust
floor and protection against an older binary bypassing that floor; item14 remains
incomplete. The separate stale-CURRENT and unchanged-bucket Helm tests above must
not be generalized to that case.


### Complete historical prefix restore: actual rc.2 failure

The source-derived hypothesis above was reproduced using the exact public rc.2
image in an isolated no-PVC Pod and a separate prefix within the same disposable
bucket. The writer was stopped before capturing the entire prefix. A real
Rauthy-authenticated Ternal cookie was valid before logout, invalid after logout,
and still invalid after a fresh Pod recovered the current prefix. Restoring the
complete pre-logout prefix into another fresh Pod made that cookie valid again.
This is a confirmed requirement-14 failure, not a stale-CURRENT-only test.

Status-only evidence: `full-rollback-result.json` in the rc.2 evidence directory,
with `rollback_outcome=cookie-reactivated`. The fixture Pod, ConfigMap, exact
fixture prefix, and temporary snapshot were removed; cleanup was verified.
The main application prefix and the first revoked device were not restored.

A new candidate adds an independently retained Kubernetes trust anchor and
operator-owned image admission guard. Local implementation and integration
review are ongoing; no new public-release success is claimed yet.


### Trust-floor candidate checkpoint (2026-09-09 KST)

PR #63 merged under mrchypark as
`2280bbcc8b3118a4e08e4afd4d51aef77d70b797`. The exact candidate passed
`scripts/run-all-tests.sh` including Go race/vet and local vCluster Helm server
dry-run. Independent review found and fixed receipt row-count projection and
bootstrap-fence ordering issues before publication. ConfigMap CAS preserves
metadata and uses resourceVersion. The anchor never resides in the bucket/PVC.

Confined IED admission tests allowed the approved image and legitimate unrelated
workloads, denied old/renamed API images, missing anchor env, protected-anchor
deletion and bootstrap rearm, and accepted pending-to-final. Fixture namespace
`ternal-trustguard-check` and its admission resources were verified absent.

Tag `v0.3.15-rc.3` points at the merge above; public release workflow
`34247154836` is running. It adds `ternal-trustguard.py` to the public checksum
and provenance inventory. No rc.3 deployment or public-bundle success is claimed
yet. Current main application remains public rc.2 revision5.

Next gate preparation: `/tmp/ternal-v0315rc3-qualification-20260909/verify-release.py`
checks all 16 checksums, exact source/run, mrchypark actors, provenance and cosign.
`/tmp/ternal-trust-floor-public-check.py` is reviewed and inert until explicitly
armed with the verified release directory; it uses a separate namespace/prefix
and temporary exact IAM grant within the same disposable bucket. It must report
precise startup trust mismatch after complete historical restore and verified
cleanup. Refresh the actual Rauthy browser session first. A new device and public
rc.3 agent are also required for the held-SSH revocation gate; first device remains
revoked and must not be implicitly trusted again.


### rc.3 cancelled before publication: chart/admission integration

Live source inspection showed the old chart always emitted `repository:tag`,
including the compound tag+digest used by rc.2, while the new guard approved
`repository@digest`. Run `34247154836` was explicitly cancelled and verified
terminal/cancelled before public release. Do not use rc.3 preparation as proof
of a released artifact.

PR #64 adds native `image.digest` support with SHA-256 validation and precedence
over tag. Both digest-only and digest-over-tag render checks passed; malformed
digest failed. The **actual Helm-rendered StatefulSet**, not a hand-built Pod,
was accepted by the live IED trustguard in a confined fixture; old images and
missing anchor were denied. Namespace and all fixture admission objects were
verified absent afterward. Go/runtime code remains the fully tested PR #63
candidate. Next public tag will be rc.4 after PR #64 CI/merge.

PR #64 CI passed and merged under mrchypark as
`6f3eb0920123ec0e4d01c534e51485f41945aed1`. Tag `v0.3.15-rc.4` points at
that commit; public workflow `34248463444` is running. rc.4 verifier and inert
new-agent preparations are under `/tmp/ternal-v0315rc4-qualification-20260909`.
The chart must be deployed with `image.digest` equal to the signed rc.4 digest
and the external guard allowlist, plus an explicit precreated anchor. Current
live app remains rc.2; no rc.4 deployment or full-scenario pass is claimed.


rc.4 preparation review: actual Rauthy browser admin/allowed logins refreshed
protected rc.1-root session files; admin authenticated with ternal-admins/platform,
allowed with platform and no admin role. The live API is still rc.2.
The rc.4 revoke driver now reuses the proven rc.1 held-SSH monitor and identity
restart implementation, with exact new Pod/serial guards and **required** device
and client callback denials. It is inert unless REVOKE_RC4_RELEASE_CONFIRMED=1
and rc.4 verification exists. The fresh-agent preparation explicitly installs
Python/OpenSSH in the new Ubuntu Pod before Python exec, creates ops-owned state
and token files, sets the managed relay URL, writes the existing supervisor
launcher, and selects the exact already-verified client public key. These scripts
remain unexecuted until the public rc.4 release is verified and deployed.


rc.4 workflow: macOS arm64 job `102136439517` passed native compilation and
upstream tests, then failed the Upload CLI archive step at FinalizeArtifact with
GitHub intermediary HTTP403. Other build jobs are still active. A job-only rerun
was requested, but GitHub rejected it as not rerunnable while the run is active;
wait for this same workflow to finish, then retry only failed jobs. Do not change
source or start another release to work around this upload failure. Detailed
public job log was captured at `/tmp/ternal-rc4-macos-arm64-job.log`; no release
success is claimed.

### 2026-09-09 KST: batch-linked one-use enrollment candidate

- Confirmed existing token `batch_id` was not used during enrollment. Fixed linked-token lookup and atomic token consumption, sequential serial/quota update, host/device insertion. Direct batch and standalone one-use paths are preserved.
- Commit `6623d9a` passed independent read-only review and the complete `scripts/run-all-tests.sh`, including Go race/vet, packaging/security checks and real local vCluster Helm server dry-run. Evidence: `/tmp/ternal-batch-one-time-all-tests.log`. Unit API dev headers are not live OIDC qualification evidence.
- rc4 workflow `34248463444` attempt 1 failed only macOS ARM artifact finalization with intermediary HTTP 403 after a successful build. After terminal failure, reran failed jobs only; attempt 2 is active. No rc4 public qualification or deployment claimed.
- Live API remains signed public rc2 digest `sha256:1248fdf5e52fcaa44ac03b6ee4c27c83053325e183195c0221aaeb762087a221`, Ready. Original logo/favicon byte equality remains established.

- PR #65 merged by mrchypark after successful CI `34251491234`, with no remote review threads. Main merge source `8e7ea5f0fa643ddab5b694320cfee0577b2d44a8`; public tag `v0.3.15-rc.5` pushed, workflow `34251727112` queued. Do not substitute local application binaries.
- rc5 private verification root `/tmp/ternal-v0315rc5-qualification-20260909` prepared with exact tag/source/run verification. Private live linked-token helper `/tmp/ternal-batch-linked-public-check.py` requires explicit exact release/source/fix ancestry and actual Ready Pod imageID; no live actions yet.

### Test credential handling incident, 2026-09-09 KST
A Playwright timeout on a hidden password input included the disposable allowed-user credential in its tool error trace. No credential value is copied here. Rauthy self-service password change returned HTTP 200; the protected local credential and bootstrap Secret users.json were replaced. Subsequent secret-bearing browser actions use an error wrapper that emits only exception class. Historical trace removal is not claimed. This run cannot be described as having zero secret exposure.

- Public rc4 workflow `34248463444` attempt 2 succeeded. All 16 public checksums, eight native archives, provenance tag/source/run/owner, released trustguard helper, and cosign image identity verified. Signed digest `sha256:bf8f33f4d4913e8bc5a5e5493b86f73036eb762adc62a2e834404e8f39082ad4`. Evidence `/tmp/ternal-v0315rc4-qualification-20260909/release-verification.json`.
- Fresh real Rauthy device flow succeeded after test-password rotation; public CLI stored only cookie/csrf_token/expires_at mode600. CLI logout exit0 removed its session file and replay of the still-fresh previous cookie returned authenticated=false. Evidence `/tmp/ternal-v0315rc1-qualification-20260908/cli-logout-verification.json`.
- Trust-floor isolated public-image test has not reached application readiness: admission denied initial Pod with missing-params informer error. Each completed failed attempt cleaned namespace/VAPs/IAM/exact prefix, cleanup verified; investigation ongoing, no rollback PASS claimed.

### Kubernetes parameter informer defect and renderer correction
IED v1.35.7-gke.1150000 reproduced the builtin ConfigMap parameter informer lifecycle defect described by Kubernetes PR141015. Only the initially cached name/namespace pair resolved; new name, new namespace, and explicit paramRef.namespace all failed despite observed policy generation and existing ConfigMap. Candidate renderer now inlines exact admission constants and removes params CM/policy. Actual source-candidate IED dry-runs passed fresh-scope admission, denied unlisted/renamed images and missing anchor, and verified two-digest transition to candidate-only with unchanged anchor. Evidence `/tmp/ternal-trustguard-inline-rotation.log`. This is source-candidate admission evidence, not public-release full-rollback success. All completed diagnostic namespaces and VAPs were verified removed.

- PR #66 source candidate `f84116b9abb468061812b001a3ce5e4abf96d51b` passed full `scripts/run-all-tests.sh` (including local vCluster server dry-run), independent security review, and CI `34254303992`. No remote review threads. Merged by mrchypark as `a7dc655de7a5c27ef56fe1511ff4e444b6d32b0b`.
- Public rc6 tag pushed; release workflow `34254597485` is authoritatively in progress. Exact verifier prepared at `/tmp/ternal-v0315rc6-qualification-20260909/verify-release.py`; assets not yet downloaded. Public rc5 workflow `34251727112` is terminal failure (native artifact finalization intermediary403); no retry because it also contains the confirmed parameter-cache defect superseded by rc6.
- All diagnostic namespaces (ternal-trustguard-check, ternal-trust-floor-qual, ternal-floor-fresh, ternal-inline-rotation) and their exact VAPs/bindings removed; main API still public rc2 Ready. No main trust anchor installed yet.
- Private rc6 prepare-agent/revoke-check helpers prepared without execution. `/tmp/ternal-main-public-upgrade.py` now pins context and uses released inline policy pairs; initial anchor preserved during later policy-only rotations. `/tmp/ternal-trust-floor-public-check.py` now waits policy compilation and captures only protected admission error evidence; it must be rerun with verified public rc6, not source-built applications.

### Public rc4 agent component revocation, while rc6 builds
Verified public rc4 agent and CLI bundles used with unchanged public rc2 API. New noPVC runner `ternal-qualification-runner-rc4`, manufacturing batch `42ee5c43-7629-4db2-b4ee-07c3f6eab2aa`, device `11fe5ad2-a17c-4d8f-8bf6-69ac51884e34`, host `7f515d40-91cb-4a6b-a0d7-e54eaed5eed2`, serial `IEDRC4-000001`. CA package missing from minimal fixture image initially blocked TLS correctly; installed ca-certificates without bypassing verification, then enrollment and heartbeat succeeded. Private rc4/rc6 preparation helpers corrected to install CA and avoid trailing serial-prefix separator.

Fresh real Rauthy device flow authorized the public rc4 CLI; only local cookie/csrf_token/expires_at mode600 persisted. Identity-preserving agent restart, exact authorized-key sync, and actual held-SSH execution marker passed. DELETE device returned200, but **held SSH failed to exit within60s**. Agent/native roost processes were gone; client SSH→ternalctl→pigeons remained, with pigeons threads waiting on futex/pipe_read. Signed heartbeat401, discovery/newSSH/newrelay404, both callbacks403, persistent revoked state, and portal device.revoked audit all passed. Held SSH eventually exited255 after the limit; an attempted owned-process cleanup found it already absent, so no manual kill was applied. The 60s gate remains FAILED.

Evidence: `/tmp/ternal-v0315rc4-qualification-20260909/revoke-result.log`, `revoke-audit.json`, fixture/inventory JSON, protected client `/qualification/revoke-hold-status-rc4.json`. Callback forward13004 was stopped. Original revoked IEDRC-000001 remains untouched.

Pinned Pigeons source37d169c uses symmetric try_join for stdio bridging; remote-close with SSH stdin open can deadlock, and Tokio blocking stdin can also hold runtime shutdown. Bounded fix is in separate `/tmp/ternal-pigeons-revoke-fix`, branch `feature/ssh-remote-close` based EXACT37d169c (fork main lacks these Ternal extensions). Worker owns tunnel/main plus focused tests. Parent review required stdio-only directional cancellation, pinned futures preserving buffered drain, original multi-thread runtime, and a real binary subprocess exit regression with stdin left open. No local application build is deployed; publication and new pinned public Ternal release remain required.

## rc6 public trust-floor qualification (2026-09-09)

Public v0.3.15-rc.6 source a7dc655de7a5c27ef56fe1511ff4e444b6d32b0b, workflow 34254597485 succeeded; 16 checksums, 8 native bundles and signed API digest sha256:a3280e4a7df712998863ae2013f8cb963655ce069161dee23477d9b9a1e818c3 verified. Isolated whole-prefix historical restore passed: authenticated session revoked, current emptyDir recovery retained revocation, historical restore never Ready with trust mismatch, external anchor unchanged; fixture namespace/policies/prefix/IAM cleanup verified. Evidence /tmp/ternal-trust-floor-public-result.json. Main public rc6 upgrade completed with no Ternal PVC, exact running digest, readiness and released inline admission guard. Private evidence /var/folders/r4/bygqtzg554v3dh6zd8z41zgw0000gn/T/ternal-ied-upgrade-z6gdd3f3.

Held SSH termination remains pending final public Pigeons fix: mrchypark/pigeons PR1 merged a094dfee9e7cb145851883afe6086331f3a0c06d; Ternal PR67 pins source and checksum. Local real-process regression and locked tests pass; pre-existing strict Clippy protocol.rs lint failures remain. No local-built application substituted into IED.

Linked one-use manufacturing live gate on rc6: 3 tokens tied to max2 batch; 2 sequential serials with exact device/endpoint/host-key binding; replay400, excess400, closed state/used_count2, real short token expiry400. Helper initially compared JSON status instead of documented state; resumed existing fixture after correction, no reenrollment. Evidence /tmp/ternal-batch-linked-public-result.json, cleanup IDs /tmp/ternal-batch-linked-cleanup.json.

PR67 merged c86818df42f3a7e186c85e0190c6350e2101dcec after CI34257590260 success and zero review threads. Public rc7 workflow34257812417 running; do not mark transport regression live-pass until actual public bundle test.

## rc7 public candidate live verification (2026-09-09)

Public workflow34257812417 succeeded; source c86818df42f3a7e186c85e0190c6350e2101dcec, Pigeons a094dfee9e7cb145851883afe6086331f3a0c06d, signed/tag/runtime digest sha256:ab5971f4651e98d1e47287c7cad9bbe2420aca1cb71f0a7ceb371873c594bfac match. 16 checksums and8archives verified. Main Helm rc7 Ready/noPVC, candidate-only admission verified. Upgrade helper initially dry-ran the previous image in default namespace; corrected explicit qualification namespace proves formerdigestdenied. No old image was actually deployed. Kubernetes default match fields normalized without accepting altered selectors.

Fresh public agent IEDRC7-000001 device6ef8019c-c9d3-457a-ae8d-c1ecc63e83b0 on runner-rc7 healthy; initial helper30sec health threshold elapsed but agent subsequently healthy, same enrolled state retained, no reenrollment. Real rc7CLI deviceflow mode600 local-onlysession PASS. Forced relay/direct SSH roundtrip exit23; both 256KiB half-close exact262160bytes/SHA256/exit29; relaySSHbanner; baddirect255/nooutput; endpoint-onlydenied; wrongpin255; actualSSHserverhostkeyswapped->blocked->restoredsuccess. Evidence /tmp/ternal-v0315rc7-qualification-20260909/transport-result-retry.json and peer-key-result.json. Initial pre-sync transport failed; exactkey synchronization confirmed before successfulrepeat.

Browser adminlogin/logout200 and still-valid-cookie replay authenticatedfalse PASS. Expiry gate in progress with correct internal callback port3001 (initial helperwrong3000 returned404), fixedTTL300,301rejected400, bearer401, wrongendpoint403, activecallback200. Some diagnostic calls returned transient503 Session validation unavailable while other writes ran; readiness remained200; no claim of zero transientavailabilityfailures. Existing successful security results remain distinct from these helper/availability failures.

### rc7 cancellation/readiness failure — full pass NOT achieved

Expiry and keys removal passed (/tmp/ternal-v0315rc7-qualification-20260909/expiry-result.json). Held SSH revoke gate did NOT reach DELETE: API503 during 1secondheartbeat restart; anchorepoch283/pending284 remained, readinesseventually503. Agentreportedrevoked/stopped even though noadminDELETE dueAPIstorelookupfailuresbeing401. This is a confirmed productfailure, not a completed revocation scenario. Public API native Podrestart recovered durablycommittedpending; anchor now285/noPending, no manualanchorreset or bucketrestore. rc7device6ef8019c-c9d3-457a-ae8d-c1ecc63e83b0 has not been administratively revoked; its agent is stopped and old transport connections absent.

Local fix on feature/trust-write-cancellation: bounded30s cancellation-independent fencedwrite throughCAS/Execute/finalize; localRWMutex preventsownpending reads; unknownresultsstayfailclosed. Deviceheartbeat/key/ACK storelookup503 distinct frominvalid/revoked401; Touchwritefailure503. Regression tests cancel exactlyatpendingCAS, blockreadsduringlocalwrite, unavailabledeviceAPIs503, and original revoked401 coveragepass. Full go test -race ./... passed. Independentreview pending. No fixedlocalAPIimage has been substituted inIED; newpublicrelease required.

PR68 merged 299147e9827de718d8fd1191937e8162978faa2e after CI34263597674 success and independent no-blocker review; zero remote review threads. rc8 public workflow34263785910 started for this source; no local-built replacement deployed. rc7 API recovered Ready with native startup only; rc7device still enrolled onserver, noadminDELETEexecuted. Its stoppedagent status is the false401diagnostic, not proof of administrativerevocation.

Final cleanup inventory for rc8 at /tmp/ternal-v0315rc8-qualification-20260909/cleanup-scope.json additionally captures legacy baseline/Rauthy PV names and exact CSI volume handles for deletion verification. Shared haproxy-gateway Service/load-balancer is explicitly preserved. No cleanup executed while qualification remains incomplete. Public rc8 workflow34263785910 confirmed in_progress; watchsession35512.

rc8 preparation audit: all tracked files contain zero former-account references. Both SSR PNG assets remain byte-identical to frontend originals and rc7-served bytes. Refreshed real Rauthy admin/allowed sessions. Cancellation helper fixed exact official `repo@digest` / optional-tag validation and restart timeout; inert only. Cleanup helper prepared with exact namespace/bucket/resource scope, fail-closed absence checks, and no live cleanup. Public rc8 image build/scan/sign job succeeded; final verified GitHub release publication remains in progress.

## rc8 public verification checkpoint

Public v0.3.15-rc.8 workflow34263785910 SUCCESS, source299147e9827de718d8fd1191937e8162978faa2e, signed/tag/runtime digest sha256:437df0868a6ab56665a7564e6b38c923dfe8e2d77fae1aea87f86fd9df638b1e. 16checksums/8nativearchives/provenance/cosign verified. Main Helm revision8, noPVC, Ready, candidate-only admission and former digest denial PASS. Private upgrade evidence /var/folders/r4/bygqtzg554v3dh6zd8z41zgw0000gn/T/ternal-ied-upgrade-synyekcj.

Fresh public rc8 agent IEDRC8-000001: device24462485-5661-4bfa-90d1-e4678b91f30c, host574e7c3e-1eb2-49e5-aedd-afeda94ac57a, runnerternal-qualification-runner-rc8. Real rc8 CLI Rauthy deviceflow/local-only mode600 session PASS. Exact key sync PASS. Forced relay/direct SSH and 256KiB half-close exact262160bytes/exit29 PASS; bad direct/endpoint-only/wrongpin deny. Actual server host-key swap rejects, restore succeeds. Public logo/favicon originalbytes PASS. Admin htmx, keyboardfocus, mobile390nooverflow PASS.

rc8 isolated complete-prefix historical rollback PASS: trust-mismatch-never-ready, external anchor unchanged, current recovered logout remained revoked; isolated resources/prefix/IAM cleanup verified. Evidence /tmp/ternal-v0315rc8-qualification-20260909/trust-floor-result.json.

Cancellation boundary live capture remains INCONCLUSIVE: first helper30s supervisor timeout, followup healthy/Ready200/anchor314noPending; second attempt observed pending but one-shot heartbeat finished before process-kill signal, cancellation_sent=false, anchor329noPending/device manufactured/Ready200. Do not call this a cancellation-boundary live PASS. Deterministic source regression already passes at the exact accepted pending-CAS boundary. rc8 runtime writes/agent restarts now remain healthy in these attempts. Real300s expiry and held-SSH revoke still pending at this checkpoint.

## Final rc8 evidence and cleanup

Actual300s expiry PASS, keys removed/generationadvanced. Held SSH marker received BEFORE deviceDELETE200; existing SSH exited within60s, agentrevoked/childstopped; heartbeat401/discovery404/newSSH404/newrelaygrant404/bothcallbacks403. Public API native Podrestart preserves revoked device and logged-out CLI cookie rejection. Re-running the public agent with revoked identity exits1/childstopped/livePigeons0. Audit includes actual rc8device.revoked and access.ssh.denied.

All8fixturedevices revoked (5 additional cleanup revocations). All29testbrowsercontexts and browser process closed. Exact cleanup complete:2Helm releases, qualificationnamespace,6admissionobjects,5dedicatedGateway/TLSobjects, ONEdisposablebucket gs://ternal-qual-20260908-602454948273. BothlegacytestPV/disks autodestroyed. SharedGatewayService, originalternalnamespace and gs://ternal-ied-602454948273 preserved. Legacyproductenvironmentlabel absent.15knownprivatequalification/upgrade/diagnosticdirectories removed; no historicaltooltraceerasureclaim. Exported evidence checked against25knowncredentialvalues with0matches. Durable files and checksummanifest at output/ied-qualification-20260909.

Precise live cancellation-boundary capture remains inconclusive and is NOT relabeled PASS. Prior public-candidate detailed unchanged-surface tests and rc8 targeted reruns are distinguished in finalreport. Historicalcredentialincidents are disclosed. No additional publicapplicationreplacement or trustanchorreset occurred.

# IED greenfield deployment — 2026-10-14

The user explicitly authorized deleting the existing Ternal data and deploying a fresh cluster. The `ternal-auth` namespace, accounts, client credentials, theme, and logo were preserved.

## Active deployment

- URL: https://ternal.dev.conalog.com
- Kubernetes context: `gke_ied-cluster`; namespace: `ternal`
- Helm release: `ternal-greenfield-20261014`
- Source: `6a0c83c` on `feature/ternal-greenfield-20261014`, based on `origin/main` at `cd9cf85`, including the Ely HTMX migration and portal behavior fixes.
- Image: `ghcr.io/mrchypark/ternal@sha256:952674f539668696d4d6674a85ef8ace5288078432e4542f89074188fb68e172`
- Build: local Docker BuildKit (`dory`), cross-compiled for linux/amd64 from Go 1.27.2. No Cloud Build.
- Cluster ID: `ternal-greenfield-20261014-437ddf9a`
- GCS root: `gs://ternal-ied-602454948273/ternal/greenfield-20261014-437ddf9a/`
- HA: three voters, `before-ack`, disposable local cache, `OnDelete` StatefulSet.
- Operator: Rhiza v0.19.0; `automaticRecovery=false`.
- Values: `deploy/helm/ternal/values-ied.json`. Secret values are not stored in Git or Helm values.

## Reset scope

Removed the old Helm releases `ternal-greenfield-20261012b`, `ternal-greenfield-20261013`, and `ternal-greenfield-20261013b`, their workloads, old runtime/TLS/token Secrets, trustguard ConfigMaps/RBAC/admission policies, and obsolete route. Removed the old generation's bucket IAM bindings and API service account.

Deleted the active objects under these exact GCS prefixes:

- `ternal/greenfield-20261012b-a61a4d3/`
- `ternal/greenfield-20261013-11231ea3/`
- `ternal/greenfield-20261013b-5cf4b530/`

The bucket's existing seven-day soft-delete retention remains in effect. No recovery transition, archive seal, or old-anchor reset was used. Shared Rhiza CRDs remain installed.

## Bootstrap and operating constraints

Generated fresh session/admin/relay/voter credentials and a fresh trust anchor. Copied only the existing OIDC client secret. Provisioned distinct API, anchor, and operator service accounts with scoped GCS grants. The anchor has read access and the existing restricted recovery-pin writer role. Its TLS certificate expires one year after provisioning.

Membership must pass `ternalctl prepare-members <cluster-id>` before it is stored in the runtime Secret. This derives `public_key` from each existing voter token. The first observation detected missing public keys; the Secret was enriched without changing voter tokens, endpoints, or running pods. The next observation reached `Observed`, with all three peers reporting `ready=true` and `quorum=true`.

Do not delete or roll the voter Pods as a routine upgrade: losing local voter identity requires the documented fenced recovery procedure. The observation CR is not automatic recovery. Do not reapply a freshly generated anchor over the active anchor.

## Verification

- `go test ./...`: passed.
- Trustguard renderer: 16 tests passed.
- Helm and operator render checks: passed.
- Three voters plus anchor, operator, and relay: Ready, zero restarts.
- Operator observation: `Observed`; three ready peers with quorum and matching certified/archive/applied tips.
- External `/`, `/health`, `/ready`, `/assets/portal.css`, and `/assets/portal.js`: HTTP 200.
- HTTPRoute: Accepted and ResolvedRefs.
- Browser: landing page renders and sign-in opens the preserved Ternal-branded Goauthy login.
- Public client logo SHA-256 remains `7e84abb826766e0b96b33f08f9255c3f213e56b7dd44d46986d716e994857f3f`.
- Authenticated callback and temporary SSH-key write/delete: pending the Goauthy management session, which holds the existing administrator credentials.

No live destructive failover/recovery test was run against this fresh deployment.

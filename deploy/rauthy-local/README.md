# Local Rauthy Live Scenario

This disposable stack runs the official multi-architecture Rauthy `0.35.2`
image and the current Ternal source with dev-header authentication disabled.
It uses `http://rauthy.localhost:18080` so the browser and the Compose network
share one OIDC issuer URL. Port 18080 avoids common local proxies on 8080.

Start from a fresh Rauthy and Ternal database, validate the bootstrap fixtures,
discovery document, live authorization-code request, live device-code start and
pending token poll, and Ternal redirect:

```sh
sh deploy/rauthy-local/smoke.sh
```

The smoke validates the full Compose model, starts fresh Rauthy data with the
official image, applies the Ternal client theme and icon through Rauthy's
official branding APIs, and runs the current local Ternal debug binary with
equivalent OIDC settings. It verifies Rauthy discovery, client bootstrap, the
served theme and logo, starts a real device authorization directly against
Rauthy, repeats start and pending-token polling through Ternal's
`/auth/device/*` endpoints, checks Ternal's OIDC redirect and signed-session
prerequisites. Both device paths
require `authorization_pending`; the default smoke does not approve the device
code in a browser. It always removes the containers, local process, and
throwaway data. The script builds the Go API in its private temporary directory.
Browser-level authorization-code qualification remains an environment-bound
release gate.

The bootstrap directory is first-start input, not an existing-database
migration. Run `deploy/e2e/rauthy-device-grant-preflight.sh` against the live
issuer before rolling out device login.

For a manual browser login, keep the stack running instead:

```sh
docker-compose -f deploy/rauthy-local/compose.yaml down -v --remove-orphans
docker-compose -f deploy/rauthy-local/compose.yaml up --build --force-recreate
```

Open `http://127.0.0.1:3000/auth/login` and sign in with either fixture account:

```text
ternal-admin@example.com / 123SuperSafe
alice@example.com / 123SuperSafe
```

Remove the manual stack and its throwaway volumes with:

```sh
docker-compose -f deploy/rauthy-local/compose.yaml down -v --remove-orphans
```

Manual `docker compose up` also runs the one-shot `rauthy-branding` service.
Ternal waits until that service has uploaded
`branding/theme.json` and the square-padded Ternal icon, so Rauthy's square
thumbnail conversion keeps the complete mark visible and the first login page
is already branded. The sidecar uses the pinned `curlimages/curl` image, runs
read-only without Linux capabilities, and exits after the two uploads.

The bootstrap JSON, fixed passwords, client secret, cluster secrets, encryption
key, branding API key, and HTTP cookies are local test fixtures only. The
deterministic branding key is intentionally checked in for this disposable
HTTP-only scenario and grants only `Clients:update`; never reuse it outside
local development. Rauthy's named local volume keeps the bootstrapped users,
theme, and logo across an ordinary container restart. The documented `down -v`
command removes that disposable state before a fresh run. Ternal uses a
separate named local data volume.

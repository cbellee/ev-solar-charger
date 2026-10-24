# Solar EV Charger Controller

Solar EV Charger Controller monitors Sungrow inverter surplus power and automatically adjusts Tesla charging current to maximize solar self-consumption.

## What This Solution Does

- Polls local Sungrow WiNet-S inverter data on a fixed interval.
- Tracks surplus export power and converts it into available charging amps.
- Applies a control loop and state machine to start, stop, wake, and adjust Tesla charging.
- Uses Tesla OAuth with an in-app callback flow and persisted refresh token handling.
- Exposes a live dashboard and JSON APIs for state, history, sessions, events, and API usage.
- Stores historical data in SQLite, including minute-level Tesla API usage snapshots.
- Supports optional public HTTPS using ACME autocert for Tesla domain/callback validation.

## Core Features

### Energy-aware charging controller

- Automatic mode and manual override mode.
- Deadband and threshold logic to avoid frequent start/stop oscillation.
- Poll-cost-aware Tesla API scheduling with cached vehicle state between calls.

### Tesla OAuth and token lifecycle

- OAuth start endpoint and callback endpoint in the main app.
- HMAC-signed state cookie validation for CSRF protection.
- Refresh token persistence to file (including every rotated token) and runtime token activation after callback; the token file takes precedence over `TESLA_REFRESH_TOKEN` at startup.
- Public well-known endpoint for Tesla app/domain key verification.

### Tesla Vehicle Command Protocol

Tesla deprecated REST `command/*` endpoints for 2021+ Model 3/Y/S/X. Charging
commands now require requests to be signed with your fleet private key and
routed through the [tesla-http-proxy](https://github.com/teslamotors/vehicle-command).
The repo's `docker-compose.yml` runs the proxy as a sidecar; the app routes
`charge_start`, `charge_stop`, `set_charging_amps`, and `wake_up` to it via
`TESLA_COMMAND_BASE`. Data endpoints (`vehicle_data`, etc.) continue to use
the regional Fleet API directly.

One-time TLS cert generation for the proxy (run from the repo root):

```bash
openssl req -x509 -nodes -newkey ec \
  -pkeyopt ec_paramgen_curve:secp384r1 -pkeyopt ec_param_enc:named_curve \
  -subj '/CN=tesla-http-proxy' \
  -addext "subjectAltName=DNS:tesla-http-proxy,DNS:localhost,IP:127.0.0.1" \
  -addext "extendedKeyUsage=serverAuth" \
  -addext "keyUsage=digitalSignature,keyCertSign,keyAgreement" \
  -keyout secrets/proxy-tls-key.pem -out secrets/proxy-tls-cert.pem \
  -sha256 -days 3650
```

The proxy's TLS key MUST be different from `fleet-key.pem`.

### Web UI and APIs

- Live dashboard with SSE updates.
- Tesla API usage and monthly cost estimate view.
- History, sessions, events, and text search endpoints.

### Deployment-ready runtime

- Multi-stage Docker build.
- Docker Compose deployment for local host or NAS.
- Optional TLS listeners for HTTP-01 challenge handling and HTTPS traffic.

## High-Level Architecture

1. Inverter client polls PV, grid, and load values from Sungrow.
2. Controller computes surplus and target amps.
3. Tesla client issues vehicle_data and command requests when needed.
4. Storage persists readings, events, sessions, and API usage snapshots.
5. Web server provides dashboard, APIs, OAuth routes, and health checks.

See architecture details in architecture.md and operational guidance in instructions.md.

## Repository Highlights

- cmd/server: application entrypoint and server wiring
- internal/controller: control loop and state machine
- internal/inverter: Sungrow connectivity
- internal/tesla: OAuth, token refresh, and command/data client
- internal/storage: SQLite schema and persistence
- internal/web: dashboard, APIs, SSE, OAuth endpoints (embeds web/dist via go:embed)
- web/: Vite + React + TypeScript SPA source (built into internal/web/dist)
- .github/workflows/container-ghcr.yml: CI workflow for building and publishing to GHCR

## Web UI Development

The dashboard is a single-page React app (Vite + TypeScript + Tailwind +
TanStack Query). Source lives in `web/`; production output is written to
`internal/web/dist/` and embedded into the Go binary at compile time.

Local development:

```bash
# 1. Install web dependencies (Node 20+ required)
cd web && npm install

# 2. Run the Go backend (serves embedded SPA + APIs on :8080)
cd .. && go run ./cmd/server

# 3. In another shell, run the Vite dev server with HMR (proxies API/SSE to :8080)
cd web && npm run dev
# Open http://localhost:5173
```

Production build (must be run before `go build` if you've changed any web/ source):

```bash
cd web && npm run build   # writes ../internal/web/dist
cd .. && go build ./cmd/server
```

The Docker build runs `npm run build` automatically in its first stage.

## Quick Start (Docker Compose)

1. Copy environment template.
2. Fill required values for Sungrow, Tesla OAuth, and Entra ID auth.
3. Start the app.

```bash
cp .env.example .env
docker compose up -d
```

Dashboard default URL:

- http://localhost:8080

### WiNet-S HTTPS firmware

Older firmware uses anonymous WebSocket port 8082 and HTTP port 80. These
defaults remain unchanged. Newer firmware may require HTTPS/WSS and a local
WiNet-S login (not your iSolarCloud or Tesla credentials).

Set the following in your deployment's `.env`, replacing the placeholders:

```dotenv
SUNGROW_HOST=<module IP or hostname, without a scheme>
SUNGROW_TLS=true
SUNGROW_PORT=443
SUNGROW_USERNAME=admin
SUNGROW_PASSWORD_FILE=/secrets/winet-password
SUNGROW_CERT_SHA256=<verified SHA-256 leaf-certificate fingerprint>
```

Store only the local login password in `secrets/winet-password`, restrict its
permissions, and do not commit it. The existing Compose configuration mounts
`secrets/` read-only. Alternatively set `SUNGROW_PASSWORD` in the deployment
environment; do not set both password sources. Credentials are rejected unless
TLS is enabled. Existing `SUNGROW_PORT=8082` values must be explicitly changed
to 443; the TLS default applies only when the port is unset.

To inspect the certificate from a machine on the trusted LAN:

```bash
openssl s_client -connect <module-ip>:443 -showcerts </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -dates -sha256 -fingerprint
```

Compare the SHA-256 fingerprint with the certificate shown by the browser
connected to the verified module before configuring it. Do not blindly trust
an unauthenticated network certificate. Pinning explicitly trusts that exact
leaf certificate instead of a public CA/hostname check, accommodates factory
certificates without an IP SAN, and still enforces certificate validity dates
and TLS 1.2 or newer. A mismatched, expired, or renewed certificate fails closed;
verify a replacement before updating the pin. It does not change browser trust
or the module's certificate. Leave the pin empty to use normal system CA and
hostname verification for a properly issued certificate.

Recreate the application container after environment changes (a restart alone
does not reload Compose environment values). For a local source build:

```bash
docker compose up -d --no-deps --force-recreate solar-ev-charger
```

For GHCR deployments, use both Compose files and `--no-build` as shown in
[Synology NAS updates](#synology-nas-updates).

The client logs in before discovering devices and reauthenticates once if a
register request reports an expired/unauthorized session. Initial connection
failures are retried on subsequent controller polls. Incorrect credentials
must be corrected promptly to avoid repeated login attempts.

Dashboard SSE connections send headers immediately and an idle heartbeat every
15 seconds, with nginx buffering disabled via `X-Accel-Buffering: no`. If nginx
overrides this header, disable buffering for `/events` and set its read timeout
above 15 seconds. Inverter read failures are published as error snapshots; the
last successful reading timestamp is preserved rather than presenting stale
values as fresh data.

#### Why authentication and TLS changes were necessary

After a WiNet-S firmware upgrade, the deployed controller could no longer read
the inverter. The investigation established three separate compatibility
problems:

| Layer | Previous client behavior | Observed module behavior |
|---|---|---|
| Transport | Plain HTTP register reads on port 80 and `ws://` on port 8082 | Both ports refused connections; HTTPS and `wss://` worked on port 443 |
| Authentication | `connect` returned an anonymous token, then the client immediately requested `devicelist` | Anonymous `connect` still succeeded, but `devicelist` returned protocol result code 401; discovery and register reads succeeded after local login |
| Certificate trust | No TLS verification was needed for the old plaintext connection | The factory certificate was not trusted by the browser or normal system CA verification |

These were local WiNet-S issues, not expired Tesla OAuth credentials. Signing
into Tesla again could not fix them. The exact installed firmware version was
not confirmed, so these findings describe the tested device rather than a
guarantee about every WiNet-S firmware release.

Changing `SUNGROW_PORT` alone was insufficient: the original client hard-coded
`http://` for register reads and `ws://` for the session connection, and did not
send a local login request. Accepting a browser certificate warning also does
not configure trust inside the Go application or Docker container.

#### What changed in the code

- **Configuration and startup:** [config.go](internal/config/config.go) loads
  `SUNGROW_TLS`, the local credentials or password file, and the certificate
  fingerprint. TLS defaults to port 443 when no port is explicitly configured.
  It rejects incomplete credential pairs, conflicting password sources, and
  credentials configured without TLS.
  [main.go](cmd/server/main.go) passes this configuration to
  `inverter.NewConfigured` and reports invalid client configuration at startup.
- **Secure session establishment:** [sungrow.go](internal/inverter/sungrow.go)
  selects WSS/HTTPS in TLS mode and performs
  `connect -> login -> devicelist -> register reads`. The login message uses
  `username` and `passwd`; the returned authenticated token replaces the
  anonymous token for discovery and reads. The handshake/session setup is
  bounded by a context timeout. Legacy anonymous WS/HTTP remains available
  when TLS is disabled.
- **Explicit certificate trust:** the inverter's HTTP transport uses TLS 1.2 or
  newer. Without a pin, Go performs normal CA and hostname verification. With
  `SUNGROW_CERT_SHA256`, a custom `VerifyConnection` callback checks the exact
  leaf-certificate SHA-256 fingerprint and its validity dates. In that mode,
  `InsecureSkipVerify` disables only the built-in PKI/hostname check so the
  custom verifier can support the factory certificate; it is **not** an
  unconditional acceptance of arbitrary certificates. This is scoped to the
  inverter client, not a global TLS bypass. Redirects are refused, and no
  certificate was imported into the module.
- **Recovery and secret handling:** an HTTP 401 or protocol result code 106/401
  triggers one reconnect/login and retry of the failed register read. A client
  without an established session retries connection on a later poll. HTTP
  transport errors are unwrapped before logging so the request URL's session
  token is not included in the error. Real credentials are supplied at
  deployment time, never embedded in source or committed test fixtures.
- **Dashboard resilience:** [sse.go](internal/web/sse.go) writes and flushes an
  initial comment immediately, then sends a heartbeat every 15 seconds.
  Previously it sent nothing until a successful controller update, leaving
  nginx able to time out an idle connection. It also sets
  `X-Accel-Buffering: no` and handles stream write failures.
  [controller.go](internal/controller/controller.go) now publishes inverter
  failure snapshots with `state=error`, `lastError`, and a zero target-current
  value while preserving the last successful reading timestamp. That target
  value is UI state, not a new vehicle stop command; charging policy was not
  changed.

The idle-stream behavior was consistent with the reported SSE 504, although the
original nginx timeout was not independently confirmed from its logs. The
changes address both the demonstrated inverter connection failure and the
stream's demonstrated lack of idle output.

#### Verification

Regression tests cover authenticated TLS reads, certificate mismatch/untrusted
certificates and validity dates, rejected login, session-expiry recovery,
legacy connections, token redaction, idle SSE frames, and published inverter
errors with recovery. The affected Go packages passed race-enabled tests,
`go vet`, and a build; CI also passed the full test suite and container build.

An opt-in, read-only `TestLiveWiNetTLS` hardware check in
[secure_test.go](internal/inverter/secure_test.go) successfully used the new Go
client to authenticate, discover the inverter, and read the existing power
registers 5031, 5083 and 5091. The register API and addresses did not need to be
replaced. No module settings or vehicle commands were changed during this
check. After deploying the new image and configuration, the operator confirmed
normal application operation.

#### Automatic recovery from stale WiNet-S sessions (`result_code 120`)

After the HTTPS migration had been running for a while, the dashboard went stale
with:

```text
sungrow: read pv power: sungrow: read register 5031: result_code 120: I18N_COMMON_INTER_ABNORMAL
```

The error repeated on every poll (about 254 times) until the app was
restarted. Restarting fixed it without power-cycling the WiNet-S.

**Root cause (confirmed on the live module):** the WiNet-S allows only one
logged-in session per account. A new `admin` login, from the WiNet-S web UI or
another client, invalidates the app's existing session. After that, every
register read on the old session returns `result_code 120`
(`I18N_COMMON_INTER_ABNORMAL`), not an "expired token" code, while the
WebSocket `ping` on the same session still returns `success`. A live probe
reproduced this: session A read register 5031 successfully, session B logged
in, and A's next read returned `120` while B's read succeeded. The first
failure followed a web UI login during the HTTPS investigation, and the second
followed a hardware test that logged in.

The client only reconnected after HTTP 401 or result codes `106`/`401`, so it
kept retrying the superseded session indefinitely.

The register request itself was never the problem: 32-bit values such as 5031
are read with `param_num=2`, the low word comes first (confirmed by live
readings), and three sequential reads every 10 seconds are a light load.

The client in [sungrow.go](internal/inverter/sungrow.go) now:

- **Treats `120` as a superseded session.** Like `106`/`401`, it logs in again
  and retries the read within the same poll, so another login now causes no
  missed update.
- **Pings the session every poll.** It sends the same `ping` message the
  WiNet-S web UI sends; the live module replies `success`. A closed socket or a
  rejected token (`106`/`401`) triggers a reconnect before registers are read.
  Other ping result codes are logged and tolerated.
- **Abandons a session after repeated other failures.** After 3 consecutive
  failed polls for any other reason, it closes the session and logs in again
  on the next poll.
- **Backs off between repeated logins.** The first replacement happens
  immediately. If sessions keep being rejected or logins fail, it waits 30 s,
  60 s, 2 min, 4 min, then 5 min (cap) before the next login, so a faulty
  module is not flooded. A successful poll clears this history. Cancelled polls
  are not counted.
- **Spaces out register reads** by 100 ms. This is a precaution for the
  module's slow internal gateway, not the cause of the error.

**Operational note:** the app and the WiNet-S web UI share the single `admin`
session. When you log in to the web UI, the app logs in again on its next
poll, which ends the web UI session. To use the web UI for more than a quick
look, stop the app container first. If the module supports a second local
account, give the app its own account through `SUNGROW_USERNAME` and the
password file.

If `120` still continues after these automatic re-logins, the module itself
is likely faulty: power-cycle the WiNet-S (unplug it for 30 seconds and allow a
few minutes to reboot).

Regression tests in [recovery_test.go](internal/inverter/recovery_test.go) use
a fake WiNet-S where a newer login causes older sessions to get `120`, as on the
real module. They cover recovery within one poll, backoff timing and its cap for
persistent `120`/`106`, resets after repeated generic failures, intermittent
failures that must not reset the session, keepalive handling (healthy,
unsupported, rejected token, closed socket), and cancellation. Against the live
module, the new client recovered within one poll after a second login, and the
keepalive succeeds without an extra login.

#### Tesla refresh-token rotation

After the restart above, Tesla first rejected the stored token
(`401 login_required`). Tesla refresh tokens can be used only once, and every
refresh returns a new one. The app kept the new token only in memory, and at
startup a `TESLA_REFRESH_TOKEN` environment value took priority over the token
file. As a result, a restart could reuse a token Tesla had already invalidated.

Now:

- Each rotated refresh token is written to `TESLA_TOKEN_PATH`
  (default `/data/tesla-refresh-token`) using a temporary file and rename, with
  mode `0600`.
- At startup the token file takes priority over `TESLA_REFRESH_TOKEN`; the
  environment value is used only to seed the first start. The log line
  `tesla refresh token loaded` reports which source was used.

## Build and Publish Container to GHCR

This repo includes a GitHub Actions workflow:

- .github/workflows/container-ghcr.yml

Workflow behavior:

- Pull requests: build validation only (no push)
- Push to main: build and push image to GHCR
- Tag push v*: build and push versioned image
- Manual dispatch: supported

Image naming:

- ghcr.io/<owner>/<repo>

Typical tags include branch, tag, PR, commit SHA, and latest on default branch.

## Deploy from GHCR (No Local Build)

Use the override file:

- docker-compose.ghcr.yml

Start with base and override compose files:

```bash
docker compose -f docker-compose.yml -f docker-compose.ghcr.yml up -d --pull always --no-build
```

### Synology NAS updates

Older Synology installations use the standalone `docker-compose` command rather
than `docker compose`. Run these commands over SSH on the NAS, from the
deployment directory (for example `/volume1/docker/solar-ev-charger`).

The deployment's `.env` is a hidden file; `ls -la` displays it. Edit the NAS copy,
not just the copy on your development Mac. Keep existing Tesla and Entra
settings, and add the WiNet-S settings described above. Never share the complete
file or commit credentials.

For password-file authentication, create `secrets/winet-password` if it does not
already exist:

```bash
mkdir -p secrets
(umask 077; cat > secrets/winet-password)
```

Type only the local WiNet-S password, press Enter, then Ctrl+D. This avoids
placing the password in shell history. Set
`SUNGROW_PASSWORD_FILE=/secrets/winet-password` and leave `SUNGROW_PASSWORD`
unset. The existing volume mount exposes this file inside the container; ensure
the container's runtime user can read it without making it world-readable.

Check the image in `docker-compose.ghcr.yml`:

```yaml
services:
  solar-ev-charger:
    image: ghcr.io/cbellee/ev-solar-charger:latest
```

An old `sha-...` tag remains pinned to that build. Use `latest` to pull the latest
successful main-branch publication, or explicitly select a published SHA tag
for a reproducible deployment. Note the previous tag or image ID for rollback.
Wait for the main-branch image-publishing workflow to succeed before pulling.

Download the image and recreate only the application container:

```bash
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml \
  pull solar-ev-charger &&
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml \
  up -d --no-deps --no-build --force-recreate solar-ev-charger
```

This leaves the Tesla proxy and persistent data volume in place. The `latest`
tag does not automatically update running containers.

If only `.env` changed and the desired image is already present, skip the pull
and run just the `up` command above. **Restarting the existing container does not
reload environment variables; recreating it does.** No `down` or volume deletion
is needed.

Check startup and refresh the dashboard:

```bash
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml \
  logs --since 5m --tail 100 solar-ev-charger
```

Look for `sungrow: connected`, check for subsequent read errors, and confirm that
the dashboard's last-update timestamp advances and its stream error clears.
A healthy HTTP endpoint alone does not prove inverter reads are working.
Redact session tokens, passwords and vehicle identifiers before sharing logs.

## Public HTTPS for Tesla OAuth

For production OAuth callback validation, configure:

- TLS_ENABLED=true
- TLS_DOMAIN=<your-public-domain>
- TESLA_REDIRECT_URI=https://<your-public-domain>/auth/tesla/callback

The app can serve ACME challenges and HTTPS directly when ports are correctly forwarded.

## Authentication (Microsoft Entra ID)

The dashboard, API, and SSE stream are gated by Microsoft Entra ID. The React
SPA performs an interactive sign-in via MSAL, then attaches the resulting ID
token to every `/api/*` and `/events` request. The Go backend validates the
token's signature, issuer, audience (`ENTRA_CLIENT_ID`), and tenant
(`ENTRA_TENANT_ID`).

### Required env vars

Backend (`.env`):

- `ENTRA_TENANT_ID`: directory (tenant) ID of your Entra tenant
- `ENTRA_CLIENT_ID`: application (client) ID of the SPA registration
- `ENTRA_ALLOWED_OIDS` (optional): comma-separated list of user `oid` claims
  allowed to sign in. Leave empty to allow any user from the tenant.

Frontend (`web/.env`):

- `VITE_ENTRA_TENANT_ID`: same as `ENTRA_TENANT_ID`
- `VITE_ENTRA_CLIENT_ID`: same as `ENTRA_CLIENT_ID`

### Entra app registration setup

In the Azure portal → Microsoft Entra ID → App registrations → your app:

1. Authentication → Add a platform → **Single-page application**.
2. Redirect URIs: add both
   - `https://<your-public-domain>/`
   - `http://localhost:5173/` (Vite dev server)
3. Front-channel logout URL: `https://<your-public-domain>/`
4. Implicit grant: leave both checkboxes unchecked (auth code + PKCE only).
5. Supported account types: **Accounts in this organizational directory only**
   (single tenant).

No "Expose an API" step is needed — the backend validates the ID token
audience, which is automatically the client ID for tokens minted for this app.

### Restricting to a single user

After your first successful sign-in, decode the `id_token` (e.g. via
[jwt.io](https://jwt.io)) and copy the `oid` claim into
`ENTRA_ALLOWED_OIDS`. Any other tenant user attempting to sign in will then
receive a 403.

### Session lifetime and silent token refresh

The SPA should only need an interactive sign-in when the Entra refresh token
expires (24 hours for SPAs, non-sliding), and often not even then, because
MSAL can reuse the browser's Microsoft sign-in session. The MSAL cache lives in
`sessionStorage`, so closing the tab clears it. Reopening normally signs you
back in automatically.

**Why users were forced to sign in about every hour:** the backend validates
the **ID token**, which lives about 1 hour. MSAL's `acquireTokenSilent` decides
whether to refresh by looking only at the cached **access token**, which is for
Microsoft Graph, unused here, and lives a randomised 60–90 minutes. It then
returns the cached ID token without checking the ID token's own `exp`. Once the
ID token expired but the access token had not, MSAL kept returning the expired
ID token. The backend answered `401 invalid_token`, and the UI showed "Sign in
again", even though the refresh token was still valid.

**Fix:**

- [`web/src/auth/idToken.ts`](web/src/auth/idToken.ts): `acquireFreshIdToken`
  decodes the ID token's `exp`. If the token is expired, within 5 minutes of
  expiry, or unreadable, it calls `acquireTokenSilent` again with
  `forceRefresh: true`. That redeems the refresh token for a new ID token
  without any prompt.
- [`web/src/api/client.ts`](web/src/api/client.ts): `authorizedFetch` is used
  by every `/api/*` call and by the `/events` SSE stream. If the backend still
  returns `401`, for example because of clock skew, it forces one silent
  refresh and retries once. A `403` (wrong tenant or user not allowed) is not
  retried.
- If the silent refresh itself fails, for example because the refresh token
  expired or consent was revoked, `useIdToken` redirects to the Entra sign-in
  page, as before.

## API Endpoints (Summary)

- GET /: dashboard
- GET /api/state: current snapshot
- GET /events: SSE stream
- POST /api/control: manual control commands
- POST /api/mode: switch auto/manual mode
- GET /api/history: historical readings
- GET /api/sessions: charge sessions
- GET /api/events: state/event history
- GET /api/search: full text event search
- GET /api/usage: current Tesla API usage and cost estimate
- GET /api/usage/history: API usage snapshots in `from`..`to` (RFC3339, default last 30 days), downsampled to at most `points` (default 500, max 2000), oldest first
- GET /auth/tesla: start OAuth flow
- GET /auth/tesla/callback: OAuth callback
- GET /.well-known/appspecific/com.tesla.3p.public-key.pem: Tesla domain verification key
- GET /healthz: health check endpoint

## Development

Run tests:

```bash
go test ./...
(cd web && npm test)
```

Build:

```bash
go build ./...
```

## Additional Documentation

- instructions.md: setup, NAS deployment, TLS, DNS, troubleshooting
- plan.md: implementation planning notes
- updates.md: implementation update log
- tesla_auth_endpoint_plan.md: OAuth endpoint migration notes

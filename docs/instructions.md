# EV Solar Charger — Setup Instructions

End-to-end guide for running this app on a Synology DSM 7.1.1 NAS, fronted by DSM's nginx with a Let's Encrypt cert, and integrated with the Tesla Fleet API. Includes architecture overview, configuration reference, and operations playbook.

---

## Table of Contents

**Part A — Deployment**
1. [Prerequisites](#prerequisites)
2. [Tesla Fleet API — One-Time Setup](#1-tesla-fleet-api--one-time-setup)
3. [Network — DNS, Router, DSM Ports](#2-network--dns-router-dsm-ports)
4. [DSM — Let's Encrypt Cert](#3-dsm--lets-encrypt-cert)
5. [DSM — Reverse Proxy Rule](#4-dsm--reverse-proxy-rule)
6. [NAS — Project Files](#5-nas--project-files)
7. [Start the Container](#6-start-the-container)
8. [Tesla Partner Registration & OAuth Bootstrap](#7-tesla-fleet-api--partner-registration--oauth-bootstrap)
9. [Operations](#8-operations)
10. [Troubleshooting](#9-troubleshooting)
11. [Security Notes](#10-security-notes)

**Part B — Reference**

12. [System Overview](#system-overview)
13. [Control Loop](#control-loop)
14. [Sungrow API Connection](#sungrow-api-connection)
15. [Tesla Fleet API Connection](#tesla-fleet-api-connection)
16. [Web Dashboard & API Endpoints](#web-dashboard)
17. [Configuration Reference](#configuration-reference)
18. [Outstanding Tasks](#outstanding-tasks)

---

# Part A — Deployment

## Prerequisites

- Synology NAS with DSM 7.1.1+, Container Manager (Docker) and `docker-compose` v1.28+ installed
- A public domain (e.g. `ev.bellee.net`) with DNS you control (Cloudflare in this guide)
- A router with port forwarding (MikroTik RouterOS in this guide)
- A Tesla developer account with a registered Fleet API application
- A Tesla vehicle with Fleet API support (post-2021 generally; check Tesla docs)
- A Sungrow inverter on the same LAN as the NAS (Modbus TCP enabled)
- SSH access to the NAS

---

## 1. Tesla Fleet API — One-Time Setup

### 1.1 Create the developer app

In <https://developer.tesla.com>:

1. Create an app named (e.g.) `EV Solar Charger`
2. Set **Allowed Origin(s)**: `https://ev.bellee.net`
3. Set **Allowed Redirect URI(s)**: `https://ev.bellee.net/auth/tesla/callback`
4. Set **Allowed Returned URL(s)**: `https://ev.bellee.net`
5. **API and Scopes → Fleet API**, enable:
   - Vehicle Information
   - Vehicle Commands
   - Vehicle Charging Management
6. Note the **Client ID** and **Client Secret** — you'll need them later

> Tesla rejects domain names that contain the substring `tesla`.

### 1.2 Generate the Tesla keypair

The Fleet API requires a `prime256v1` (P-256) keypair. Tesla fetches your public key from `https://<your-domain>/.well-known/appspecific/com.tesla.3p.public-key.pem`.

```bash
cd /Users/<you>/Documents/repos/github.com/cbellee/ev-solar-charger
./scripts/generate-tesla-key.sh
```

This produces `secrets/fleet-key.pem` (private, `chmod 600`) and `secrets/com.tesla.3p.public-key.pem` (public). The `secrets/` folder is git-ignored.

Optional environment variables:
- `SECRETS_DIR` — write keys to a different directory (default `./secrets`)
- `FORCE=1` — overwrite existing key files

If a private key was ever leaked, regenerate with `FORCE=1`, redeploy, re-verify the domain, and re-pair the vehicle via `https://tesla.com/_ak/<your-domain>`.

### 1.3 Generate an OAuth state HMAC key

```bash
openssl rand -hex 32
```

Keep the output for `OAUTH_STATE_HMAC_KEY` in `.env`.

---

## 2. Network — DNS, Router, DSM Ports

### 2.1 Move DSM web ports off 80/443

DSM nginx hardcodes ports 80/443 and **cannot release them**. We'll reverse-proxy through it instead. First move DSM's own admin UI:

- **Control Panel → Login Portal → DSM**
  - HTTP port: `5000`
  - HTTPS port: `5001`

DSM nginx now keeps 80/443 free for our reverse-proxy rule (created in step 4).

### 2.2 DNS — Cloudflare record

Point `ev.bellee.net` at your home network, **DNS-only** (grey cloud). Tesla's HTTP-01 challenge for cert issuance must reach your NAS directly, so don't proxy through Cloudflare.

Two options:

**Option A — A record (static or rarely-changing public IP)**

```
Type: A
Name: ev
Content: <your public IP>
Proxy: DNS only (grey cloud)
TTL: Auto
```

**Option B — CNAME to MikroTik DDNS (recommended for dynamic IPs)**

If your ISP assigns a dynamic public IP, point at the MikroTik's per-device DDNS hostname so it tracks IP changes automatically:

```
Type: CNAME
Name: ev
Content: <serial>.sn.mynetname.net      # from RouterOS: /ip cloud print
Proxy: DNS only (grey cloud)
TTL: Auto
```

Enable DDNS on the MikroTik first if you haven't:

```routeros
/ip cloud set ddns-enabled=yes
/ip cloud print     # shows your <serial>.sn.mynetname.net hostname
```

After changing DNS, verify it resolves to your current public IP:

```bash
dig +short ev.bellee.net
```

### 2.3 MikroTik — disable internal www service

If RouterOS is answering port 80 with the admin login page, Let's Encrypt will fail validation:

```routeros
/ip service disable www
```

### 2.4 MikroTik — port forward 80/443 to NAS

Replace `192.168.88.167` with your actual NAS LAN IP:

```routeros
/ip firewall nat add chain=dstnat in-interface-list=WAN protocol=tcp dst-port=443 \
    action=dst-nat to-addresses=192.168.88.167 to-ports=443 comment="ev.bellee.net 443"
/ip firewall nat add chain=dstnat in-interface-list=WAN protocol=tcp dst-port=80  \
    action=dst-nat to-addresses=192.168.88.167 to-ports=80  comment="ev.bellee.net 80"
```

### 2.5 (Optional) MikroTik hairpin NAT for LAN testing

So you can hit `https://ev.bellee.net` from a LAN client (e.g. your Mac):

```routeros
/ip firewall nat add chain=dstnat src-address=192.168.88.0/24 dst-address=<public-ip> protocol=tcp dst-port=443 \
    action=dst-nat to-addresses=192.168.88.167 to-ports=443 comment="hairpin 443"
/ip firewall nat add chain=dstnat src-address=192.168.88.0/24 dst-address=<public-ip> protocol=tcp dst-port=80  \
    action=dst-nat to-addresses=192.168.88.167 to-ports=80  comment="hairpin 80"
/ip firewall nat add chain=srcnat src-address=192.168.88.0/24 dst-address=192.168.88.167 protocol=tcp dst-port=443 \
    action=masquerade comment="hairpin masquerade 443"
/ip firewall nat add chain=srcnat src-address=192.168.88.0/24 dst-address=192.168.88.167 protocol=tcp dst-port=80  \
    action=masquerade comment="hairpin masquerade 80"
```

---

## 3. DSM — Let's Encrypt Cert

**Control Panel → Security → Certificate → Add → Add a new certificate → Get a certificate from Let's Encrypt**

- Domain name: `ev.bellee.net`
- Email: your address
- Subject Alternative Name: leave blank

DSM serves the HTTP-01 challenge from port 80 directly. The cert is renewed automatically.

If validation fails: re-check that port 80 reaches the NAS (DNS, MikroTik forward, MikroTik `www` disabled), then retry.

---

## 4. DSM — Reverse Proxy Rule

**Control Panel → Login Portal → Advanced → Reverse Proxy → Create**

| Field | Value |
|---|---|
| Source — Protocol | HTTPS |
| Source — Hostname | `ev.bellee.net` |
| Source — Port | `443` |
| Source — Enable HSTS | ✅ |
| Destination — Protocol | HTTP |
| Destination — Hostname | `localhost` |
| Destination — Port | `8090` |

**Custom Header tab → Create → WebSocket** (adds `Upgrade` and `Connection` headers).

**Advanced → SSL Certificate**: bind the Let's Encrypt cert from step 3.

Result: `https://ev.bellee.net` (TLS terminated by DSM) → `http://localhost:8090` (your container).

---

## 5. NAS — Project Files

```bash
ssh chris@<nas>
sudo mkdir -p /volume1/docker/solar-ev-charger/secrets
cd /volume1/docker/solar-ev-charger
```

Copy the following files from your dev machine:

```bash
# From your Mac
NAS=chris@<nas>
DEST=/volume1/docker/solar-ev-charger
scp -O docker-compose.yml docker-compose.ghcr.yml .env $NAS:$DEST/
scp -O secrets/fleet-key.pem secrets/com.tesla.3p.public-key.pem $NAS:$DEST/secrets/
```

### 5.1 `.env` template

```ini
# --- Tesla OAuth (required, even in test mode, for /auth/tesla bootstrap) ---
TESLA_CLIENT_ID=<from Tesla dev portal>
TESLA_CLIENT_SECRET=<from Tesla dev portal>
TESLA_REDIRECT_URI=https://ev.bellee.net/auth/tesla/callback
TESLA_REGION=na
TESLA_SCOPE=openid offline_access vehicle_device_data vehicle_cmds vehicle_charging_cmds
TESLA_PRIVATE_KEY_PATH=/secrets/fleet-key.pem
TESLA_PUBLIC_KEY_PEM_PATH=/secrets/com.tesla.3p.public-key.pem
TESLA_VIN=<your VIN>
TESLA_REFRESH_TOKEN=
TESLA_TOKEN_PATH=/data/tesla-refresh-token
TESLA_TEST_MODE=true                # flip to false after bootstrap
OAUTH_STATE_HMAC_KEY=<openssl rand -hex 32>

# --- Sungrow inverter ---
SUNGROW_HOST=192.168.88.106
SUNGROW_PORT=8082

# --- Charging logic ---
POLL_INTERVAL_SECONDS=10
MIN_CHARGE_AMPS=5
MAX_CHARGE_AMPS=32
LINE_VOLTAGE=240
DEADBAND_POLLS=3
WAKE_THRESHOLD_POLLS=6

# --- Web (auth via Entra ID) ---
ENTRA_TENANT_ID=<your tenant GUID>
ENTRA_CLIENT_ID=<app registration client ID>
ENTRA_ALLOWED_OIDS=<comma-separated user OIDs allowed to sign in>
VITE_ENTRA_TENANT_ID=${ENTRA_TENANT_ID}     # baked into the SPA at Docker build time
VITE_ENTRA_CLIENT_ID=${ENTRA_CLIENT_ID}
HTTP_HOST=0.0.0.0
HTTP_PORT=8080
TLS_ENABLED=false                   # DSM terminates TLS

# --- Tesla command signing (vehicle-command proxy sidecar) ---
TESLA_COMMAND_BASE=https://tesla-http-proxy:4443
TESLA_PROXY_CA_FILE=/secrets/proxy-tls-cert.pem

# --- Storage / logging ---
DB_PATH=/data/solar-ev-charger.db
DB_RETENTION_DAYS=365
LOG_LEVEL=info
OTEL_TRACES_EXPORTER=none
OTEL_METRICS_EXPORTER=none
OTEL_LOGS_EXPORTER=none
```

`TESLA_TEST_MODE=true` is intentional for first boot — the OAuth flow needs to run before the controller can talk to your vehicle. We flip it after step 7.

See [Configuration Reference](#configuration-reference) for the complete list of variables.

### 5.2 `docker-compose.ghcr.yml` (override that pins to GHCR image)

```yaml
services:
  solar-ev-charger:
    image: ghcr.io/cbellee/ev-solar-charger:latest
    container_name: solar-ev-charger
    restart: unless-stopped
    ports:
      - "8090:8080"
    env_file:
      - .env
    volumes:
      - solar-data:/data
      - ./secrets:/secrets:ro
    healthcheck:
      test: ["CMD", "/solar-ev-charger", "healthcheck"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 30s

volumes:
  solar-data:
```

The base `docker-compose.yml` (used for local builds) should not publish ports 80/443/8080 to avoid collisions.

### 5.3 `tesla-http-proxy` sidecar

The base `docker-compose.yml` declares a `tesla-http-proxy` service alongside the main container. This proxy (image `tesla/vehicle-command:latest`) signs `command/*` and `wake_up` requests with the fleet private key — newer Tesla firmware rejects unsigned commands with HTTP 403.

The proxy needs three files in `./secrets/`:

| File | Purpose | How to generate |
|---|---|---|
| `fleet-key.pem` | Fleet private key (P-256) | Already created in §1.2 |
| `proxy-tls-key.pem` | TLS private key for the proxy listener | `openssl ecparam -name prime256v1 -genkey -noout -out secrets/proxy-tls-key.pem` |
| `proxy-tls-cert.pem` | Self-signed cert presented by the proxy on `:4443` | `openssl req -new -x509 -key secrets/proxy-tls-key.pem -out secrets/proxy-tls-cert.pem -days 3650 -subj "/CN=tesla-http-proxy"` |

The controller trusts the proxy's self-signed cert via `TESLA_PROXY_CA_FILE`, and routes commands through it via `TESLA_COMMAND_BASE` (set in `.env` per §5.1). The two services are connected by Docker Compose's default network; `depends_on: [tesla-http-proxy]` on the main service ensures startup order.

After the proxy is running for the first time, pair the public key to the vehicle by opening `https://tesla.com/_ak/<your-domain>` from the Tesla mobile app on a device signed in to your Tesla account.

### 5.4 Entra ID app registration

User authentication uses Microsoft Entra ID via MSAL.js. Create an app registration:

1. Sign in to <https://entra.microsoft.com> → **Identity → Applications → App registrations → New registration**
2. Name: e.g. `EV Solar Charger`
3. Supported account types: **Single tenant** (or Multitenant if you need cross-tenant access)
4. Redirect URI: **SPA** → `https://ev.bellee.net/`
5. After creation, note the **Application (client) ID** and **Directory (tenant) ID**
6. **Authentication** → enable **ID tokens (used for implicit and hybrid flows)**
7. **Token configuration** is not required — the default ID token already includes `oid`
8. Find the **Object ID** of every user account that should be allowed to sign in (Entra → Users → user → Object ID). These go into `ENTRA_ALLOWED_OIDS` as a comma-separated list

Set `ENTRA_TENANT_ID`, `ENTRA_CLIENT_ID`, `ENTRA_ALLOWED_OIDS` in `.env`, and pass `VITE_ENTRA_TENANT_ID` / `VITE_ENTRA_CLIENT_ID` as build args when building the container image (the `Dockerfile` and CI workflow already wire this up).

---

## 6. Start the Container

```bash
cd /volume1/docker/solar-ev-charger
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml pull
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml up -d
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml ps
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml logs --tail=50 solar-ev-charger
```

Smoke test from your Mac (or cellular):

```bash
curl -kI https://ev.bellee.net/healthz                    # 200 OK, no auth
curl -kI https://ev.bellee.net/                           # 200 OK — SPA is publicly cacheable; protected calls are gated by Entra ID Bearer tokens
```

The dashboard itself authenticates the user with Microsoft Entra ID via MSAL.js. Open `https://ev.bellee.net/` in a browser, sign in with an account whose OID is listed in `ENTRA_ALLOWED_OIDS`, and the SPA will attach `Authorization: Bearer …` to every `/api/*` and `/events` request.

---

## 7. Tesla Fleet API — Partner Registration & OAuth Bootstrap

### 7.1 Register the partner account (one-time)

From your Mac:

```bash
# 1. Get a client_credentials access token. Note: --data-urlencode handles
#    special characters (& ^ etc.) in client_secret correctly.
curl -X POST https://fleet-auth.prd.vn.cloud.tesla.com/oauth2/v3/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'grant_type=client_credentials' \
  --data-urlencode "client_id=$TESLA_CLIENT_ID" \
  --data-urlencode "client_secret=$TESLA_CLIENT_SECRET" \
  --data-urlencode 'scope=openid vehicle_device_data vehicle_cmds vehicle_charging_cmds' \
  --data-urlencode 'audience=https://fleet-api.prd.na.vn.cloud.tesla.com'

# 2. Register your domain (Tesla fetches the public PEM from /.well-known/...)
ACCESS_TOKEN='<access_token from above>'
curl -X POST https://fleet-api.prd.na.vn.cloud.tesla.com/api/1/partner_accounts \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"domain":"ev.bellee.net"}'

# 3. Verify
curl -X GET 'https://fleet-api.prd.na.vn.cloud.tesla.com/api/1/partner_accounts/public_key?domain=ev.bellee.net' \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

The `partner_accounts` POST must return your domain's public key. If it fails, check that `https://ev.bellee.net/.well-known/appspecific/com.tesla.3p.public-key.pem` is publicly reachable and serves your PEM.

### 7.2 Run the OAuth flow

In a **fresh incognito tab** (your Mac if hairpin works, otherwise cellular):

1. Visit `https://ev.bellee.net/auth/tesla`
2. Sign in with Microsoft Entra ID (your account's OID must be in `ENTRA_ALLOWED_OIDS`)
3. You're redirected to `auth.tesla.com` — sign in with your Tesla account
4. Approve the requested scopes
5. Tesla redirects back to `https://ev.bellee.net/auth/tesla/callback?...`
6. Browser shows: **"Tesla authorization complete — Refresh token saved to /data/tesla-refresh-token and activated."**

Verify the token was persisted:

```bash
sudo ls -la /volume1/@docker/volumes/solar-ev-charger_solar-data/_data/
# Should include: tesla-refresh-token (mode 0600)
```

### 7.3 (Optional) Pair the virtual key to the vehicle

If your vehicle requires command signing, pair the Fleet virtual key by visiting on a phone signed in to the Tesla app:

```
https://tesla.com/_ak/ev.bellee.net
```

### 7.4 Disable test mode

```bash
cd /volume1/docker/solar-ev-charger
sudo sed -i 's/^TESLA_TEST_MODE=true/TESLA_TEST_MODE=false/' .env
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml up -d --force-recreate
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml logs --tail=50 solar-ev-charger
```

The controller picks up the refresh token, exchanges it for an access token, and starts polling the inverter and vehicle.

---

## 8. Operations

### Update to a new image

```bash
cd /volume1/docker/solar-ev-charger
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml pull
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml up -d --force-recreate
```

### Logs

```bash
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml logs -f --tail=100 solar-ev-charger
```

### Inspect environment loaded by the running container

The image is distroless — no shell. Use `docker inspect` from the host:

```bash
sudo docker inspect solar-ev-charger_solar-ev-charger_1 \
  --format '{{range .Config.Env}}{{println .}}{{end}}'
```

### Backup the data volume

```bash
sudo tar -C /volume1/@docker/volumes/solar-ev-charger_solar-data/_data \
    -czf /volume1/backups/solar-ev-charger-$(date +%F).tar.gz .
```

Contains the SQLite DB and the Tesla refresh token.

### Rotate the Tesla refresh token

Re-run the `/auth/tesla` flow at any time — the new token overwrites the old one atomically.

### Stop the stack

```bash
cd /volume1/docker/solar-ev-charger
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml down
```

Data persists in the `solar-data` named volume.

---

## 9. Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Cert issuance fails | Port 80 not reaching NAS | Re-check Cloudflare record, MikroTik forward, MikroTik `www` disabled |
| Container exits at startup | Missing required env var | `docker-compose ... logs --tail=100`; check `.env` |
| `/auth/tesla` returns dashboard instead of redirecting | Old image (pre-fix) | `pull` and `up -d --force-recreate` |
| Tesla page: "Client authentication failed" | Partner account not registered, or `client_id` empty in redirect | Run step 7.1; verify with `curl ... /auth/tesla` and grep `Location:` for `client_id=...` |
| Tesla token exchange fails after callback | Wrong region or secret | Check `TESLA_REGION` and `TESLA_CLIENT_SECRET` (special chars work via `env_file`) |
| Container running but `/data` empty | Looking at wrong path | Real path is `/volume1/@docker/volumes/solar-ev-charger_solar-data/_data/` (named volume), not the project's `./data` |
| LAN browser can't reach `https://ev.bellee.net` | No hairpin NAT | Add the rules in step 2.5 |
| Tesla domain validation still fails | Cloudflare proxied (orange cloud) | Switch to DNS only (grey cloud); confirm cert is trusted from outside the LAN |
| OAuth callback returns generic error | Mismatch in `TESLA_REDIRECT_URI` or missing `OAUTH_STATE_HMAC_KEY` | Confirm exact match with Tesla portal; confirm key set in `.env` |

---

## 10. Security Notes

- TLS terminates at DSM nginx; the container speaks plain HTTP on `localhost:8090` and is not exposed to the LAN
- The dashboard and all `/api/*` endpoints require a Microsoft Entra ID ID token. Only OIDs listed in `ENTRA_ALLOWED_OIDS` may sign in
- `/auth/tesla*`, `/.well-known/appspecific/com.tesla.3p.public-key.pem`, and `/healthz` are intentionally public (Tesla and Let's Encrypt need to reach them)
- Tesla charge commands are signed by the `tesla-http-proxy` sidecar using the fleet private key, which is mounted read-only at `/secrets/fleet-key.pem`
- The refresh token file has mode `0600` inside a `0700` directory
- The HMAC key (`OAUTH_STATE_HMAC_KEY`) signs the Tesla OAuth state cookie — never commit it
- The Tesla private key (`fleet-key.pem`) is mounted read-only into both the controller and the proxy. The host directory is git-ignored
- State-changing JSON APIs (`POST /api/control`, `POST /api/mode`, `POST /api/charge-limit`) require `Content-Type: application/json` and reject bodies larger than 4 KiB
- When the container's own TLS listener is enabled, the HTTPS listener requires TLS 1.3 or newer
- OAuth callback errors are logged server-side; only generic messages are shown in the browser. Check application logs for full error detail
- Never commit `.env`, `secrets/`, or `*.pem` files. The repo's `.gitignore` and `.dockerignore` exclude these paths; rotate any secret that was previously committed
- Rotate `TESLA_CLIENT_SECRET` in the dev portal if it's ever pasted in chat or logs

---

# Part B — Reference

## System Overview

This application monitors surplus solar generation from a Sungrow SG8.0RS inverter (via its WiNet-S dongle) and automatically regulates the charging amperage of a Tesla Model 3 through the Tesla Fleet API. The goal is to maximise self-consumption of solar energy by diverting excess PV production into the EV battery rather than exporting it to the grid.

### Architecture

```
┌──────────────┐     WebSocket      ┌───────────────────┐                     ┌──────────────────┐
│  Sungrow      │◄──────────────────►│  Solar EV Charger  │  data reads (HTTP)  │  Tesla Fleet API  │
│  WiNet-S      │  token + registers │  Controller        │◄───────────────────►│  (OAuth2)         │
└──────────────┘                    ├───────────────────┤                     └──────────────────┘
                                     │  SQLite (WAL)      │                              ▲
                                     │  React SPA + SSE   │                              │ signed commands
                                     │  Entra ID auth     │                              │ (vehicle command
                                     │  OTel observability│     ┌────────────────────┐ │  protocol)
                                     └───────────────┬───────────┘►────│  tesla-http-proxy │─┘
                                                     POSTs       │  (sidecar, 4443) │
                                                                 └────────────────────┘
```

### Key packages

| Package | Purpose |
|---|---|
| `cmd/server` | Entry point — wires all components and starts the HTTP server |
| `internal/config` | Loads configuration from environment variables |
| `internal/controller` | State machine and control loop |
| `internal/inverter` | Sungrow WiNet-S client (WebSocket + HTTP register reads) |
| `internal/tesla` | Tesla Fleet API client (OAuth2, token refresh, commands) |
| `internal/storage` | SQLite store with FTS5 full-text search |
| `internal/observability` | OpenTelemetry SDK setup, structured logging, metrics |
| `internal/web` | HTTP handlers, SSE hub, Entra ID auth, embedded React SPA bundle |
| `web/` | React + Vite + TypeScript + Tailwind SPA (built into `web/dist/` and embedded by the Go binary) |

---

## Control Loop

The controller runs a ticker at `POLL_INTERVAL_SECONDS` (default 10s). Each tick executes these steps:

### 1. Read solar data

Polls three Modbus registers on the Sungrow inverter via its HTTP API:
- **5031** — PV generation (W, unsigned 32-bit)
- **5083** — Grid power (W, signed 32-bit; negative = exporting)
- **5091** — Load/consumption (W, signed 32-bit)

Surplus is calculated as `max(0, -gridWatts)`.

### 2. Read vehicle state

Calls the Tesla Fleet API `vehicle_data` endpoint to get:
- Charging state (`Charging`, `Stopped`, `Disconnected`, etc.)
- Actual charge amps
- Battery percentage
- Whether the car is online and plugged in

**Cost-aware polling:** Tesla API calls are gated by `shouldPollTesla()` to minimise Fleet API costs ($0.002 per `vehicle_data` call). The inverter is polled every tick (free, local Modbus), but Tesla calls follow these rules:

| Controller State | Poll Interval | Rationale |
|---|---|---|
| No cached state | Immediate | Need initial vehicle state |
| Charging | `TESLA_CHARGING_POLL_SECONDS` (default 60s) | Track battery % and actual amps |
| Idle with surplus ≥ min amps | `TESLA_IDLE_POLL_SECONDS` (default 300s) | Check if car is still plugged in |
| Idle without surplus | Skipped | No reason to call Tesla |
| Wake pending | Skipped | Wake command handles its own polling |

Between API calls, the controller uses a cached charge state so the UI snapshot and state machine continue updating from inverter data.

When `TESLA_TEST_MODE=true`, the controller skips Tesla connectivity entirely and uses inverter surplus only. The dashboard remains live, `targetAmps` becomes a projected value, and all Tesla command actions are disabled.

### 3. Calculate available amps

```
surplusAmps = floor(surplusWatts / lineVoltage)
if car is currently charging:
    surplusAmps += currentActualAmps   // account for power already being consumed
clamp to [0, maxChargeAmps]
```

### 4. State machine decision

The controller maintains six states:

| State | Meaning |
|---|---|
| `idle` | Car not plugged in or no surplus — nothing to do |
| `monitoring` | Surplus detected but not yet sustained long enough to act |
| `charging` | Actively charging — amps are adjusted each tick |
| `stopped_low_surplus` | Charging was stopped because surplus dropped below minimum |
| `wake_pending` | Surplus sustained — sending wake command to sleeping car |
| `error` | Inverter or Tesla API communication failure |

**Starting a charge:** When `surplusAmps >= minChargeAmps` and the car is plugged in and online, the controller calls `SetChargingAmps` then `StartCharging`.

**Adjusting amps:** While charging, if the available amps change by at least `AMPS_CHANGE_THRESHOLD` (default 2A), the controller calls `SetChargingAmps` to ramp up or down. This hysteresis prevents unnecessary command calls from minor solar fluctuations (e.g. passing clouds).

**Stopping a charge (deadband):** If surplus drops below `minChargeAmps` for `DEADBAND_POLLS` consecutive ticks (default 3 × 10s = 30s), the controller calls `StopCharging`. This prevents flapping on transient cloud cover.

**Waking the car:** If the car is asleep and surplus exceeds minimum for `WAKE_THRESHOLD_POLLS` consecutive ticks (default 6 × 10s = 60s), the controller sends a wake-up command and waits for the car to come online.

### 5. Persist data

Each tick's readings are accumulated in memory. At the end of each calendar minute, the samples are averaged and written to SQLite as a single `reading` row. State transitions are logged as `event` rows. Charge start/stop boundaries create `charge_session` rows.

An `api_usage_snapshot` row is also written each minute, recording the cumulative Tesla API call counts (data, command, wake, stream signals) and estimated cost for the current billing month.

### 6. Notify UI

After each tick, the current `StateSnapshot` is broadcast to all connected SSE clients via the `Hub`, which the dashboard's `EventSource` consumes for real-time updates.

---

## Sungrow API Connection

The WiNet-S dongle exposes a local network API (no cloud dependency):

1. **WebSocket handshake** — connect to `ws://<host>:<port>/ws/home/overview`, send a `connect` message, receive a session token
2. **HTTP register reads** — `GET http://<host>/device/getParam?param_addr=<addr>&token=<token>&...` returns register values as comma-separated 16-bit words (`"high,low"`)
3. **Token refresh** — if the HTTP API returns `result_code: 106`, the client automatically reconnects via WebSocket to obtain a new token and retries the read

All communication is on the local network. The dongle must be reachable from wherever the container runs.

---

## Tesla Fleet API Connection

The Tesla Fleet API uses OAuth2 with the following flow:

1. **Initial setup** — register an application at [developer.tesla.com](https://developer.tesla.com) and obtain a `client_id`, `client_secret`, and a `refresh_token` by completing the OAuth2 authorization code flow once via the in-app `/auth/tesla` route (see Part A, step 7)
2. **Token refresh** — the app uses the refresh token to obtain short-lived access tokens automatically. Tokens are refreshed on 401 responses
3. **Vehicle data reads** — `GetChargeState` calls the Fleet API directly
4. **Vehicle commands** — `SetChargingAmps`, `SetChargeLimit`, `StartCharging`, `StopCharging`, and `WakeUp` are routed through the `tesla-http-proxy` sidecar (image `tesla/vehicle-command:latest`), which signs them with the fleet private key (`fleet-key.pem`) and forwards them to Tesla. Newer firmware rejects unsigned commands with HTTP 403, so the proxy is mandatory in practice
5. **Virtual Key pairing** — after the proxy is running, pair the public key to the vehicle by opening `https://tesla.com/_ak/<your-domain>` on the Tesla app's mobile deep-link
6. **Regions** — the API endpoint is selected by `TESLA_REGION`: `na` (North America), `eu` (Europe), or `cn` (China)

---

## Web Dashboard

A single-page React application (Vite + TypeScript + Tailwind) is served at `/` from the embedded `web/dist/` bundle:

- **Dashboard** — animated power-flow diagram (Solar, House, Grid, EV nodes), live PV / grid / load / surplus, charging state, target/actual amps, battery %, and the current charge-limit setting
- **Charge-limit slider** — sets the vehicle's `charge_limit_soc` via `POST /api/charge-limit`; clamped to the vehicle-reported `[charge_limit_soc_min, charge_limit_soc_max]` range
- **Tesla API Usage** — monthly call counts per category (data, commands, wakes, stream signals) with per-category cost and progress bars against the shared $10 discount budget, estimated net cost
- **Usage page** — historical charts (cumulative cost, cumulative calls by type, per-snapshot deltas) over selectable ranges (24 h / 7 d / 30 d / all)
- **History** — paginated readings with interval aggregation (raw / hourly / daily)
- **Sessions** — charge-session list with start/end times, energy, peak amps
- **Events** — full-text search across events using SQLite FTS5

The SPA acquires an ID token via MSAL.js and attaches `Authorization: Bearer …` to every protected request. Live updates flow over an SSE stream at `/events`.

### API Endpoints

All `/api/*` and `/events` routes require a valid Microsoft Entra ID ID token whose `oid` claim is listed in `ENTRA_ALLOWED_OIDS`. Public routes are explicitly marked.

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/` | Public (SPA) | React dashboard bundle |
| `GET` | `/api/state` | Entra ID | Current controller snapshot (JSON) |
| `GET` | `/events` | Entra ID | SSE stream of state updates |
| `POST` | `/api/control` | Entra ID | Manual commands: `start`, `stop`, `setAmps` |
| `POST` | `/api/mode` | Entra ID | Switch between `auto` and `manual` mode |
| `POST` | `/api/refresh` | Entra ID | Force re-poll Tesla and clear cooldowns |
| `POST` | `/api/charge-limit` | Entra ID | Set vehicle `charge_limit_soc` (`{percent: N}`) |
| `GET` | `/api/history` | Entra ID | Paginated readings (`from`, `to`, `interval`, `limit`, `offset`) |
| `GET` | `/api/sessions` | Entra ID | Paginated charge sessions |
| `GET` | `/api/events` | Entra ID | Paginated events |
| `GET` | `/api/search` | Entra ID | Full-text search (`q`, `from`, `to`, `limit`) |
| `GET` | `/api/usage` | Entra ID | Current month's Tesla API usage counters and cost (JSON) |
| `GET` | `/api/usage/history` | Entra ID | Historical API usage snapshots (`from`, `to`, `limit`) |
| `GET` | `/.well-known/appspecific/com.tesla.3p.public-key.pem` | Public | Tesla public key endpoint for app/domain verification |
| `GET` | `/auth/tesla` | Entra ID | Starts Tesla OAuth2 authorization code flow |
| `GET` | `/auth/tesla/callback` | Public | OAuth2 callback endpoint for code exchange |
| `GET` | `/healthz` | Public | Health check (returns 200) |

---

## Configuration Reference

All configuration is via environment variables:

| Variable | Required | Default | Description |
|---|---|---|---|
| `SUNGROW_HOST` | Yes | — | IP or hostname of the WiNet-S dongle |
| `SUNGROW_PORT` | No | `8082` | WiNet-S WebSocket port |
| `TESLA_CLIENT_ID` | Yes | — | Tesla OAuth2 client ID |
| `TESLA_CLIENT_SECRET` | Yes | — | Tesla OAuth2 client secret |
| `TESLA_REFRESH_TOKEN` | Conditionally | — | Tesla OAuth2 refresh token (required if no token file exists) |
| `TESLA_REDIRECT_URI` | Yes | — | OAuth callback URL, e.g. `https://ev.bellee.net/auth/tesla/callback` |
| `TESLA_VIN` | Yes (when `TESLA_TEST_MODE=false`) | — | Vehicle identification number |
| `TESLA_TEST_MODE` | No | `false` | Skip Tesla connectivity and publish projected charging values only |
| `TESLA_PRIVATE_KEY_PATH` | No | `/secrets/fleet-key.pem` | Path to EC private key for command signing |
| `TESLA_PUBLIC_KEY_PEM_PATH` | Yes | `/secrets/com.tesla.3p.public-key.pem` | Path to Tesla app public key served via `/.well-known` |
| `TESLA_REGION` | No | `na` | Fleet API region: `na`, `eu`, `cn` |
| `TESLA_SCOPE` | No | `openid offline_access vehicle_device_data vehicle_cmds vehicle_charging_cmds` | OAuth scopes requested at `/auth/tesla` |
| `OAUTH_STATE_HMAC_KEY` | Yes | — | HMAC key used to sign Tesla OAuth state cookie |
| `TESLA_TOKEN_PATH` | No | `/data/tesla-refresh-token` | File path for persisted Tesla refresh token |
| `TESLA_COMMAND_BASE` | When using the proxy | — | Base URL of the `tesla-http-proxy` sidecar (typically `https://tesla-http-proxy:4443`) |
| `TESLA_PROXY_CA_FILE` | When using the proxy | — | Path to the proxy's self-signed TLS cert, e.g. `/secrets/proxy-tls-cert.pem` |
| `ENTRA_TENANT_ID` | Yes | — | Microsoft Entra tenant GUID |
| `ENTRA_CLIENT_ID` | Yes | — | Microsoft Entra app registration client ID |
| `ENTRA_ALLOWED_OIDS` | Yes | — | Comma-separated list of user OIDs allowed to sign in |
| `VITE_ENTRA_TENANT_ID` | Yes (build-time) | — | Same tenant GUID, baked into the SPA at Docker build time |
| `VITE_ENTRA_CLIENT_ID` | Yes (build-time) | — | Same client ID, baked into the SPA at Docker build time |
| `POLL_INTERVAL_SECONDS` | No | `10` | Seconds between control loop ticks |
| `MIN_CHARGE_AMPS` | No | `5` | Minimum amps to start/continue charging |
| `MAX_CHARGE_AMPS` | No | `32` | Maximum amp limit sent to vehicle |
| `LINE_VOLTAGE` | No | `240` | Mains voltage (for watts-to-amps conversion) |
| `DEADBAND_POLLS` | No | `3` | Consecutive low-surplus ticks before stopping |
| `WAKE_THRESHOLD_POLLS` | No | `6` | Consecutive surplus ticks before waking car |
| `WAKE_MIN_AMPS_MARGIN` | No | `2` | Extra amps above `MIN_CHARGE_AMPS` required to initiate a wake (avoids transient-surplus wakes) |
| `WAKE_AFTER_NON_ACTIONABLE_BACKOFF_SECONDS` | No | `1800` | Suppress wakes for this many seconds after observing the car in a non-actionable state (Disconnected, Complete) |
| `TESLA_CHARGING_POLL_SECONDS` | No | `60` | Seconds between Tesla API polls while charging |
| `TESLA_IDLE_POLL_SECONDS` | No | `300` | Seconds between Tesla API polls when idle with surplus |
| `AMPS_CHANGE_THRESHOLD` | No | `2` | Minimum amp change to send a `SetChargingAmps` command |
| `HTTP_HOST` | No | `0.0.0.0` | Bind address for the HTTP listener |
| `HTTP_PORT` | No | `8080` | Web server port inside the container |
| `TLS_ENABLED` | No | `false` | Enable HTTPS listener with Let's Encrypt autocert (set `false` when DSM terminates TLS) |
| `TLS_DOMAIN` | Conditionally | — | Public hostname for certificate issuance (required when `TLS_ENABLED=true`) |
| `TLS_CERT_DIR` | No | `/data/autocert` | Directory used to cache ACME certificates |
| `TLS_PORT` | No | `8443` | HTTPS listener port inside the container |
| `HTTP_CHALLENGE_PORT` | No | `8081` | HTTP listener port used for ACME HTTP-01 challenges and redirects |
| `LOG_LEVEL` | No | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `DB_PATH` | No | `/data/solar-ev-charger.db` | SQLite database path |
| `DB_RETENTION_DAYS` | No | `365` | Days of data to retain before pruning |
| `OTEL_TRACES_EXPORTER` | No | `none` | Set to `otlp` and configure `OTEL_EXPORTER_OTLP_ENDPOINT` to ship traces |
| `OTEL_METRICS_EXPORTER` | No | `none` | Set to `otlp` to ship metrics |
| `OTEL_LOGS_EXPORTER` | No | `none` | Set to `otlp` to ship logs |

---

## Outstanding Tasks

The following items must be completed by the user before running against real hardware. Most are covered in Part A; this is a checklist:

### Tesla Fleet API Setup

1. **Register a Tesla Developer application** (Part A, step 1.1)
2. **Generate the keypair** (Part A, step 1.2)
3. **Run partner registration** (Part A, step 7.1)
4. **Complete the OAuth bootstrap** via `/auth/tesla` (Part A, step 7.2)
5. **Pair the virtual key** to the vehicle if command signing is required (Part A, step 7.3)

### Network & Hardware

6. **Ensure the WiNet-S dongle is reachable** from the container. Verify with: `curl http://<SUNGROW_HOST>:8082/`
7. **Verify the correct Modbus register addresses** for your specific Sungrow inverter model. The register addresses (5031, 5083, 5091) are based on the SG8.0RS; other models may use different addresses

### Operational

8. **Tune the charging parameters** (`MIN_CHARGE_AMPS`, `MAX_CHARGE_AMPS`, `LINE_VOLTAGE`, `DEADBAND_POLLS`, `WAKE_THRESHOLD_POLLS`) for your specific installation
9. **Set up monitoring** (optional) — configure `OTEL_EXPORTER_OTLP_ENDPOINT` to ship telemetry to an OpenTelemetry collector
10. **Set up backups** for the SQLite database volume (Part A, step 8)
11. **Review the auto/manual mode** — the system starts in `auto` mode. Use the dashboard or `POST /api/mode` to switch to `manual`

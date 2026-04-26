# Solar EV Charger Controller

## System Overview

This application monitors surplus solar generation from a Sungrow SG8.0RS inverter (via its WiNet-S dongle) and automatically regulates the charging amperage of a Tesla Model 3 through the Tesla Fleet API. The goal is to maximise self-consumption of solar energy by diverting excess PV production into the EV battery rather than exporting it to the grid.

### Architecture

```
┌──────────────┐     WebSocket      ┌───────────────────┐     HTTP/REST      ┌─────────────┐
│  Sungrow      │◄──────────────────►│  Solar EV Charger  │◄──────────────────►│  Tesla Fleet │
│  WiNet-S      │  token + registers │  Controller        │  OAuth2 + commands│  API         │
└──────────────┘                    ├───────────────────┤                    └─────────────┘
                                     │  SQLite (WAL)      │
                                     │  Web UI (htmx/SSE) │
                                     │  OTel observability │
                                     └───────────────────┘
```

Key packages:

| Package | Purpose |
|---|---|
| `cmd/server` | Entry point — wires all components and starts the HTTP server |
| `internal/config` | Loads configuration from environment variables |
| `internal/controller` | State machine and control loop |
| `internal/inverter` | Sungrow WiNet-S client (WebSocket + HTTP register reads) |
| `internal/tesla` | Tesla Fleet API client (OAuth2, token refresh, commands) |
| `internal/storage` | SQLite store with FTS5 full-text search |
| `internal/observability` | OpenTelemetry SDK setup, structured logging, metrics |
| `internal/web` | HTTP handlers, SSE hub, embedded HTML dashboard |

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

1. **WebSocket handshake** — connect to `ws://<host>:<port>/ws/home/overview`, send a `connect` message, receive a session token.
2. **HTTP register reads** — `GET http://<host>/device/getParam?param_addr=<addr>&token=<token>&...` returns register values as comma-separated 16-bit words (`"high,low"`).
3. **Token refresh** — if the HTTP API returns `result_code: 106`, the client automatically reconnects via WebSocket to obtain a new token and retries the read.

All communication is on the local network. The dongle must be reachable from wherever the container runs.

---

## Tesla Fleet API Connection

The Tesla Fleet API uses OAuth2 with the following flow:

1. **Initial setup** — you register an application at [developer.tesla.com](https://developer.tesla.com) and obtain a `client_id`, `client_secret`, and a `refresh_token` by completing the OAuth2 authorization code flow once manually.
2. **Token refresh** — the app uses the refresh token to obtain short-lived access tokens automatically. Tokens are refreshed on 401 responses.
3. **Vehicle commands** — `SetChargingAmps`, `StartCharging`, `StopCharging`, and `WakeUp` are called via the Fleet API REST endpoints.
4. **Virtual Key (optional)** — if `TESLA_PRIVATE_KEY_PATH` points to an EC private key, the client can sign commands. This is required for newer vehicles. See Tesla's Fleet API documentation for key generation and vehicle pairing.
5. **Regions** — the API endpoint is selected by `TESLA_REGION`: `na` (North America), `eu` (Europe), or `cn` (China).

---

## Web Dashboard

A single-page dashboard is served at `/` using Tailwind CSS, htmx, and native `EventSource` (SSE):

- **Real-time panel** — PV, grid, load, surplus watts, charge amps, battery %, state
- **Tesla API Usage** — monthly call counts per category (data, commands, wakes, stream signals) with per-category cost and progress bars against the shared $10 discount budget, estimated net cost
- **History** — paginated readings with interval aggregation (raw / hourly / daily)
- **Sessions** — charge session list with start/end times, energy, peak amps
- **Search** — full-text search across events using SQLite FTS5

### API Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | HTML dashboard |
| `GET` | `/api/state` | Current controller snapshot (JSON) |
| `GET` | `/events` | SSE stream of state updates |
| `POST` | `/api/control` | Manual commands: `start`, `stop`, `set_amps` |
| `POST` | `/api/mode` | Switch between `auto` and `manual` mode |
| `GET` | `/api/history` | Paginated readings (`from`, `to`, `interval`, `limit`, `offset`) |
| `GET` | `/api/sessions` | Paginated charge sessions |
| `GET` | `/api/events` | Paginated events |
| `GET` | `/api/search` | Full-text search (`q`, `from`, `to`, `limit`) |
| `GET` | `/api/usage` | Current month's Tesla API usage counters and cost (JSON) |
| `GET` | `/api/usage/history` | Historical API usage snapshots (`from`, `to`, `limit`) |
| `GET` | `/.well-known/appspecific/com.tesla.3p.public-key.pem` | Tesla public key endpoint for app/domain verification |
| `GET` | `/auth/tesla` | Starts Tesla OAuth2 authorization code flow |
| `GET` | `/auth/tesla/callback` | OAuth2 callback endpoint for code exchange |
| `GET` | `/healthz` | Health check (returns 200) |

---

## Running the Container

### Prerequisites

- Docker and Docker Compose
- Sungrow inverter with WiNet-S dongle on the local network
- Tesla developer account with OAuth2 credentials

### 1. Create the environment file

```bash
cp .env.example .env
# Edit .env with your actual values
```

### 2. Add Tesla Fleet API private key (optional)

```bash
mkdir -p secrets
cp /path/to/your/fleet-key.pem secrets/fleet-key.pem
```

### 3. Start the container

```bash
docker compose up -d
```

The dashboard is available at `http://localhost:8080` and is protected with HTTP basic auth using `HTTP_AUTH_USER` and `HTTP_AUTH_PASSWORD`.

### Build and Publish with GitHub Actions (GHCR)

This repository includes a workflow at `.github/workflows/container-ghcr.yml` that builds the container and publishes it to GitHub Container Registry (GHCR).

#### Triggers

- Pull requests: build-only validation (no push)
- Push to `main`: build and push image
- Tag push matching `v*`: build and push versioned image
- Manual run: supported via **workflow_dispatch**

#### Image location and tags

- Registry: `ghcr.io`
- Image name: `ghcr.io/<owner>/<repo>`
- Tags generated by the workflow include branch, tag, commit SHA, PR tag, and `latest` on the default branch

#### Required GitHub settings

1. Ensure repository Actions are enabled.
2. Ensure package publishing is allowed for `GITHUB_TOKEN`.
3. If you want to pull the image from another private repository or host, use a PAT with `read:packages`.

#### Deploy using the GHCR image (no local build)

Create a compose override file (example `docker-compose.ghcr.yml`):

```yaml
services:
    solar-ev-charger:
        image: ghcr.io/<owner>/<repo>:latest
        pull_policy: always
```

Run with:

```bash
docker compose -f docker-compose.yml -f docker-compose.ghcr.yml up -d --pull always --no-build
```

### Hosting on Synology NAS

These steps target DSM 7.1.1 using the legacy Docker application.

#### Option A: Docker application UI (DSM 7.1.1)

1. Create a project folder on the NAS, for example `/volume1/docker/solar-ev-charger`.
2. Copy this repository into that folder so `docker-compose.yml`, `.env`, and `secrets/` are in the same directory.
3. In DSM, open **Docker** -> **Registry** and pull `golang:1.25-alpine` only if you plan to build on NAS; otherwise use SSH deployment in Option B.
4. In DSM, open **Docker** -> **Container** -> **Create** and use your prebuilt image name if available.
5. Map ports `8080:8080`, `80:8081`, and `443:8443`.
6. Mount `secrets` as read-only to `/secrets` and mount persistent storage to `/data`.
7. Set environment variables from `.env` in the container settings.
8. Start the container and confirm health is `healthy`.

For repeatable deployments and easier updates, Option B is strongly recommended.

#### Option B: SSH on Synology

Enable SSH in DSM and run:

```bash
cd /volume1/docker/solar-ev-charger
docker compose pull
docker compose up -d
docker compose logs -f solar-ev-charger
```

#### Synology-specific notes

- Keep persistent data under a NAS shared folder or named Docker volume. This app writes database and tokens under `/data` in the container.
- If using TLS for Tesla OAuth, keep ports `80` and `443` reachable from the internet to the NAS and mapped as defined in `docker-compose.yml`.
- If you use MikroTik forwarding, set `LAN_HOST` in `mikrotik_port_forward.sh` to the Synology NAS LAN IP.
- If DSM Firewall is enabled, allow inbound TCP `80`, `443`, and optionally `8080` (local dashboard access).
- After `.env` changes, redeploy with `docker compose up -d` to apply new values.

#### Synology troubleshooting

1. **Container never becomes healthy**
    - Check logs from SSH: `docker compose logs -f solar-ev-charger`
    - Or check DSM Docker UI: **Docker** -> **Container** -> **Details** -> **Log**.
    - Confirm `.env` values are valid and `HTTP_AUTH_PASSWORD` is set.
    - Ensure the app can write to `/data` (database and token file path).

2. **TLS certificate is not issued**
    - Confirm `TLS_ENABLED=true` and `TLS_DOMAIN=ev.bellee.net` in `.env`.
    - Verify internet can reach NAS on TCP `80` and `443`.
    - Confirm Cloudflare is DNS-only (grey cloud), not proxied.
    - Wait a few minutes after first start, then check logs for ACME errors.

3. **Tesla domain validation still fails**
    - Open `https://ev.bellee.net` from a device outside your LAN and verify a valid public certificate is shown.
    - Open `https://ev.bellee.net/.well-known/appspecific/com.tesla.3p.public-key.pem` and verify HTTP `200`.
    - Re-check Tesla portal values for origin and redirect URLs.

4. **OAuth callback returns an error**
    - Confirm `TESLA_REDIRECT_URI` exactly matches the Tesla portal redirect URI.
    - Confirm `OAUTH_STATE_HMAC_KEY` is set and non-empty.
    - Confirm `TESLA_PUBLIC_KEY_PEM_PATH` points to a real file mounted in the container.

5. **Refresh token not persisted**
    - Confirm `TESLA_TOKEN_PATH` points under writable `/data`.
    - Check directory/file permissions on the Synology bind mount or volume.
    - Trigger OAuth start again at `/auth/tesla` and inspect logs for write errors.

### Public HTTPS setup for Tesla OAuth (`ev.bellee.net`)

Tesla validates the registered domain and callback URL. For this deployment:

- use `ev.bellee.net` (Tesla rejects domain names containing `tesla`)
- make sure the domain has a publicly trusted TLS certificate
- use DNS-only Cloudflare records (grey cloud), not proxied

#### 1. Configure `.env` for TLS + OAuth

```bash
TESLA_REDIRECT_URI=https://ev.bellee.net/auth/tesla/callback
TLS_ENABLED=true
TLS_DOMAIN=ev.bellee.net
TLS_CERT_DIR=/data/autocert
TLS_PORT=8443
HTTP_CHALLENGE_PORT=8081
```

#### 2. Configure Cloudflare DNS

- Create an `A` record for `ev.bellee.net` to your home public IP.
- Set proxy status to **DNS only** (grey cloud).

#### 3. Configure MikroTik port forwarding

This repository includes `mikrotik_port_forward.sh` for automated RouterOS NAT rule setup.

```bash
chmod +x ./mikrotik_port_forward.sh

ROUTER_HOST=192.168.88.1 \
ROUTER_USER=admin \
LAN_HOST=192.168.88.50 \
ROUTER_IDENTITY_FILE=~/.ssh/id_ed25519 \
./mikrotik_port_forward.sh
```

This creates forwards:

- TCP `80` -> app challenge port `8081`
- TCP `443` -> app TLS port `8443`

#### 4. Verify external reachability and certificate

From outside your LAN:

- `https://ev.bellee.net` loads with a valid certificate
- `https://ev.bellee.net/.well-known/appspecific/com.tesla.3p.public-key.pem` returns `200`

#### 5. Tesla developer portal values

- Allowed Origin URL(s): `https://ev.bellee.net`
- Allowed Redirect URI(s): `https://ev.bellee.net/auth/tesla/callback`
- Allowed Returned URL(s): leave blank initially (or `https://ev.bellee.net`)

### 4. View logs

```bash
docker compose logs -f solar-ev-charger
```

### 5. Stop

```bash
docker compose down
```

Data is persisted in the `solar-data` Docker volume.

### Running without Docker

```bash
# Set environment variables (see .env.example)
export SUNGROW_HOST=192.168.1.100
export TESLA_TEST_MODE=true

go run ./cmd/server
```

With `TESLA_TEST_MODE=true`, the Tesla OAuth and VIN variables are optional.

---

## Configuration Reference

All configuration is via environment variables:

| Variable | Required | Default | Description |
|---|---|---|---|
| `SUNGROW_HOST` | Yes | — | IP or hostname of the WiNet-S dongle |
| `TESLA_CLIENT_ID` | Yes | — | Tesla OAuth2 client ID |
| `TESLA_CLIENT_SECRET` | Yes | — | Tesla OAuth2 client secret |
| `TESLA_REFRESH_TOKEN` | Conditionally | — | Tesla OAuth2 refresh token (required if no token file is available) |
| `TESLA_REDIRECT_URI` | Yes | — | OAuth callback URL, e.g. `https://ev.bellee.net/auth/tesla/callback` |
| `TESLA_VIN` | Yes | — | Vehicle identification number |
| `TESLA_TEST_MODE` | No | `false` | Skip Tesla connectivity and publish projected charging values only |
| `SUNGROW_PORT` | No | `8082` | WiNet-S WebSocket port |
| `TESLA_PRIVATE_KEY_PATH` | No | `/secrets/fleet-key.pem` | Path to EC private key for command signing |
| `TESLA_PUBLIC_KEY_PEM_PATH` | Yes | `/secrets/com.tesla.3p.public-key.pem` | Path to Tesla app public key served via `/.well-known` |
| `TESLA_REGION` | No | `na` | Fleet API region: `na`, `eu`, `cn` |
| `TESLA_SCOPE` | No | `openid offline_access vehicle_device_data vehicle_cmds vehicle_charging_cmds` | OAuth scopes requested at `/auth/tesla` |
| `OAUTH_STATE_HMAC_KEY` | Yes | — | HMAC key used to sign OAuth state cookie |
| `TESLA_TOKEN_PATH` | No | `/data/tesla-refresh-token` | File path for persisted Tesla refresh token |
| `POLL_INTERVAL_SECONDS` | No | `10` | Seconds between control loop ticks |
| `MIN_CHARGE_AMPS` | No | `5` | Minimum amps to start/continue charging |
| `MAX_CHARGE_AMPS` | No | `32` | Maximum amp limit sent to vehicle |
| `LINE_VOLTAGE` | No | `240` | Mains voltage (for watts-to-amps conversion) |
| `DEADBAND_POLLS` | No | `3` | Consecutive low-surplus ticks before stopping |
| `WAKE_THRESHOLD_POLLS` | No | `6` | Consecutive surplus ticks before waking car |
| `TESLA_CHARGING_POLL_SECONDS` | No | `60` | Seconds between Tesla API polls while charging |
| `TESLA_IDLE_POLL_SECONDS` | No | `300` | Seconds between Tesla API polls when idle with surplus |
| `AMPS_CHANGE_THRESHOLD` | No | `2` | Minimum amp change to send a `SetChargingAmps` command |
| `HTTP_AUTH_USER` | No | `admin` | HTTP basic auth username for the dashboard and API |
| `HTTP_AUTH_PASSWORD` | Yes | — | HTTP basic auth password for the dashboard and API |
| `HTTP_PORT` | No | `8080` | Web server port |
| `TLS_ENABLED` | No | `false` | Enable HTTPS listener with Let's Encrypt autocert |
| `TLS_DOMAIN` | Conditionally | — | Public hostname for certificate issuance (required when `TLS_ENABLED=true`) |
| `TLS_CERT_DIR` | No | `/data/autocert` | Directory used to cache ACME certificates |
| `TLS_PORT` | No | `8443` | HTTPS listener port inside the container |
| `HTTP_CHALLENGE_PORT` | No | `8081` | HTTP listener port used for ACME HTTP-01 challenges and redirects |
| `LOG_LEVEL` | No | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `DB_PATH` | No | `/data/solar-ev-charger.db` | SQLite database path |
| `DB_RETENTION_DAYS` | No | `365` | Days of data to retain before pruning |

---

## Outstanding Tasks

The following items must be completed by the user before running against real hardware:

### Tesla Fleet API Setup

1. **Register a Tesla Developer application** at [developer.tesla.com](https://developer.tesla.com) to obtain your `client_id` and `client_secret`.
    - Use a domain that does not include `tesla` (for example `ev.bellee.net`).
    - Configure Cloudflare record as DNS-only so Tesla validation can pass.
2. **Complete the OAuth2 authorization code flow** once to obtain an initial `refresh_token`. Tesla's documentation walks through this process — it involves directing the user to Tesla's auth page, receiving a callback with an authorization code, and exchanging it for tokens.
3. **Generate and pair a Fleet API virtual key** (EC P-256 private key) if your vehicle requires command signing. The key must be paired to the vehicle via the Tesla app. Place the `.pem` file in `./secrets/fleet-key.pem`.

### Network & Hardware

4. **Ensure the WiNet-S dongle is reachable** from the machine/container running this application. The dongle typically serves on port 8082 on the local network. Verify with: `curl http://<SUNGROW_HOST>:8082/`.
5. **Verify the correct Modbus register addresses** for your specific Sungrow inverter model. The register addresses (5031, 5083, 5091) are based on the SG8.0RS; other models may use different addresses.

### Operational

6. **Tune the charging parameters** (`MIN_CHARGE_AMPS`, `MAX_CHARGE_AMPS`, `LINE_VOLTAGE`, `DEADBAND_POLLS`, `WAKE_THRESHOLD_POLLS`) for your specific installation. The defaults are reasonable starting points but may need adjustment based on your solar array size, grid connection, and EV charger capacity.
7. **Set up monitoring** (optional) — configure `OTEL_EXPORTER_OTLP_ENDPOINT` to ship traces, metrics, and logs to an OpenTelemetry collector (e.g. Grafana Alloy, Jaeger, Prometheus).
8. **Set up backups** for the SQLite database volume if you want to preserve historical data across container rebuilds.
9. **Review the auto/manual mode** — the system starts in `auto` mode. Use the dashboard or `POST /api/mode` to switch to `manual` mode if you want to control charging manually via the API.

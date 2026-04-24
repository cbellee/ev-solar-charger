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

**Adjusting amps:** While charging, if the available amps change, the controller calls `SetChargingAmps` to ramp up or down.

**Stopping a charge (deadband):** If surplus drops below `minChargeAmps` for `DEADBAND_POLLS` consecutive ticks (default 3 × 10s = 30s), the controller calls `StopCharging`. This prevents flapping on transient cloud cover.

**Waking the car:** If the car is asleep and surplus exceeds minimum for `WAKE_THRESHOLD_POLLS` consecutive ticks (default 6 × 10s = 60s), the controller sends a wake-up command and waits for the car to come online.

### 5. Persist data

Each tick's readings are accumulated in memory. At the end of each calendar minute, the samples are averaged and written to SQLite as a single `reading` row. State transitions are logged as `event` rows. Charge start/stop boundaries create `charge_session` rows.

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
| `TESLA_REFRESH_TOKEN` | Yes | — | Tesla OAuth2 refresh token |
| `TESLA_VIN` | Yes | — | Vehicle identification number |
| `TESLA_TEST_MODE` | No | `false` | Skip Tesla connectivity and publish projected charging values only |
| `SUNGROW_PORT` | No | `8082` | WiNet-S WebSocket port |
| `TESLA_PRIVATE_KEY_PATH` | No | `/secrets/fleet-key.pem` | Path to EC private key for command signing |
| `TESLA_REGION` | No | `na` | Fleet API region: `na`, `eu`, `cn` |
| `POLL_INTERVAL_SECONDS` | No | `10` | Seconds between control loop ticks |
| `MIN_CHARGE_AMPS` | No | `5` | Minimum amps to start/continue charging |
| `MAX_CHARGE_AMPS` | No | `32` | Maximum amp limit sent to vehicle |
| `LINE_VOLTAGE` | No | `240` | Mains voltage (for watts-to-amps conversion) |
| `DEADBAND_POLLS` | No | `3` | Consecutive low-surplus ticks before stopping |
| `WAKE_THRESHOLD_POLLS` | No | `6` | Consecutive surplus ticks before waking car |
| `HTTP_AUTH_USER` | No | `admin` | HTTP basic auth username for the dashboard and API |
| `HTTP_AUTH_PASSWORD` | Yes | — | HTTP basic auth password for the dashboard and API |
| `HTTP_PORT` | No | `8080` | Web server port |
| `LOG_LEVEL` | No | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `DB_PATH` | No | `/data/solar-ev-charger.db` | SQLite database path |
| `DB_RETENTION_DAYS` | No | `365` | Days of data to retain before pruning |

---

## Outstanding Tasks

The following items must be completed by the user before running against real hardware:

### Tesla Fleet API Setup

1. **Register a Tesla Developer application** at [developer.tesla.com](https://developer.tesla.com) to obtain your `client_id` and `client_secret`.
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

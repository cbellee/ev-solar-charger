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
- Refresh token persistence to file and runtime token activation after callback.
- Public well-known endpoint for Tesla app/domain key verification.

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
2. Fill required values for Sungrow, Tesla OAuth, and auth credentials.
3. Start the app.

```bash
cp .env.example .env
docker compose up -d
```

Dashboard default URL:

- http://localhost:8080

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

## Public HTTPS for Tesla OAuth

For production OAuth callback validation, configure:

- TLS_ENABLED=true
- TLS_DOMAIN=<your-public-domain>
- TESLA_REDIRECT_URI=https://<your-public-domain>/auth/tesla/callback

The app can serve ACME challenges and HTTPS directly when ports are correctly forwarded.

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
- GET /api/usage/history: API usage snapshots
- GET /auth/tesla: start OAuth flow
- GET /auth/tesla/callback: OAuth callback
- GET /.well-known/appspecific/com.tesla.3p.public-key.pem: Tesla domain verification key
- GET /healthz: health check endpoint

## Development

Run tests:

```bash
go test ./...
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

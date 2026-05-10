# Updates

Last updated: 2026-04-25

This document summarizes the changes currently present in the working tree.

## 1. Tesla-Free Test Mode In The Main App

- Added a new `TESLA_TEST_MODE` environment flag in `internal/config/config.go`.
- Added boolean parsing via `envBool` and changed config loading so Tesla credentials are only required when `TESLA_TEST_MODE` is false.
- Updated server startup in `cmd/server/main.go` so test mode skips Tesla client creation and uses a no-op vehicle controller instead.
- Added `ErrCommandsDisabled` in `internal/tesla/tesla.go` and a new `internal/tesla/testmode.go` implementation that reports projected-only status and rejects charge commands.
- Updated `internal/controller/controller.go` to:
  - expose `TestMode` in `StateSnapshot`
  - initialize the snapshot with the new mode flag
  - branch `Tick` into a Tesla-free `tickTestMode` path
  - keep inverter polling, surplus calculation, persistence, and SSE updates working without vehicle connectivity
  - block manual start, stop, and set-amps actions while test mode is enabled
- Updated `internal/web/templates/index.html` to:
  - show a test-mode banner
  - relabel amps as projected values when appropriate
  - disable vehicle control widgets in test mode
  - display `Projected only` / `N/A` for Tesla-only fields
- Updated `instructions.md` to document test mode behavior, local startup, and the new environment variable.

## 2. Test Coverage Added For Test Mode

- `internal/config/config_test.go`
  - verifies `TESLA_TEST_MODE` defaults to false
  - verifies test mode skips Tesla credential requirements
  - verifies invalid boolean input is rejected
- `cmd/server/main_test.go`
  - verifies startup does not attempt Tesla client creation in test mode
- `internal/controller/controller_test.go`
  - verifies test mode publishes projected amps from inverter surplus
  - verifies no Tesla commands are issued in test mode
  - verifies manual commands return `ErrCommandsDisabled` in test mode

## 3. Azure Functions Tesla OAuth Helper Added

- Added a new standalone Go module under `function/` for a Tesla OAuth helper deployed as an Azure Functions custom handler.
- `function/cmd/handler/main.go`
  - starts the custom handler
  - loads runtime config from environment
  - creates the Key Vault secret store
  - serves HTTP on `FUNCTIONS_CUSTOMHANDLER_PORT`
- `function/internal/app/config.go`
  - validates Tesla OAuth settings
  - validates redirect URI and Key Vault URI
  - maps Tesla regions to the Fleet API audience
  - decides secure-cookie behavior for local vs non-local redirect URIs
- `function/internal/app/handler.go`
  - serves the Tesla public key from an app setting
  - starts the Tesla OAuth authorization-code flow
  - validates signed state cookies
  - exchanges the code for tokens
  - stores the refresh token in Azure Key Vault
  - renders simple HTML success and error responses
- `function/internal/app/keyvault.go`
  - creates a Key Vault-backed secret writer using `DefaultAzureCredential`
  - writes refresh tokens into Azure Key Vault
- `function/internal/app/handler_test.go`
  - tests public-key serving
  - tests redirect generation and state cookie creation
  - tests callback state validation
  - tests token exchange and refresh-token persistence
- Added Azure Functions host and route configuration:
  - `function/host.json`
  - `function/oauthstart/function.json`
  - `function/oauthcallback/function.json`
  - `function/publickey/function.json`
- Added local tooling and documentation for the function:
  - `function/README.md`
  - `function/Makefile`
  - `function/local.settings.json.example`
  - `function/.gitignore`
  - `function/.funcignore`
  - `function/public/.well-known/appspecific/README.md`
- Added the function module manifests:
  - `function/go.mod`
  - `function/go.sum`

## 4. Azure Infrastructure Scaffolding Added

- Added `infra/main.bicep` to provision Azure infrastructure for the OAuth helper.
- The Bicep scaffold includes:
  - a user-assigned managed identity
  - Log Analytics and Application Insights
  - an Azure Storage account and deployment container for Flex Consumption
  - Azure Key Vault with seeded Tesla secrets and RBAC
  - an Azure Functions Flex Consumption plan
  - a Linux Function App configured for managed identity, Key Vault references, and deployment storage
  - an optional custom-domain binding for a Cloudflare-fronted hostname
  - outputs for Azure hostname and Cloudflare DNS verification values
- Added `.azure/deployment-plan.md` describing deployment scope, assumptions, architecture, and out-of-scope items.

## 5. Operational Outcome

- The main application can now run in a projection-only mode by setting `TESLA_TEST_MODE=true`.
- In that mode, the app still reads the Sungrow inverter, calculates surplus solar power, computes projected charging amps, persists readings, and updates the web UI.

## 6. Tesla Fleet API Cost-Aware Polling

**Files changed:** `internal/config/config.go`, `internal/controller/controller.go`, `internal/config/config_test.go`, `internal/controller/controller_test.go`

The controller previously called the Tesla Fleet API (`vehicle_data`) on every tick (~10s), generating ~8,640 calls/day at ~$17/day. Three changes reduce this by ~95%:

### State-aware polling
Tesla API calls are now gated by `shouldPollTesla()` which considers the controller state:
- **Charging:** polls every `TESLA_CHARGING_POLL_SECONDS` (default 300s) — enough to track battery % and adjust amps at lower cost.
- **Idle / monitoring:** polls every `TESLA_IDLE_POLL_SECONDS` (default 1800s) — catches out-of-band state changes without a constant data-call burn.
- **Wake pending:** polls every `WAKE_RETRY_INTERVAL_SECONDS` (default 300s) if the wake has not completed yet.
- **No cached state:** polls immediately on first call.

Between API calls, `cachedChargeState` keeps the UI and state machine working from local inverter data.

The `WakeUp` flow also no longer polls `vehicle_data` every 2 seconds. It now performs two delayed status checks across 30 seconds, capping wake follow-up data calls at two per wake attempt.

### Amps change hysteresis
When charging, `SetChargingAmps` is only sent when the change exceeds `AMPS_CHANGE_THRESHOLD` (default 2A) and the last automatic amp change is older than `AMPS_ADJUST_INTERVAL_SECONDS` (default 60s). This avoids command oscillation from minor solar fluctuations.

### New environment variables
| Variable | Default | Description |
|----------|---------|-------------|
| `TESLA_CHARGING_POLL_SECONDS` | 300 | Poll interval while charging |
| `TESLA_IDLE_POLL_SECONDS` | 1800 | Poll interval while idle or monitoring |
| `AMPS_CHANGE_THRESHOLD` | 2 | Minimum amp delta to send a command |
| `AMPS_ADJUST_INTERVAL_SECONDS` | 60 | Minimum time between automatic amp-adjust commands |

### Tests added
- 9 new tests in `internal/controller/controller_test.go` covering polling logic and hysteresis
- 5 new tests in `internal/config/config_test.go` for the new config fields

See `cost-optimisation.md` for full cost analysis and trade-offs.

## 7. Tesla API Usage Tracking (In-Memory)

## 8. Fleet Telemetry Bridge Ingest (In Progress)

**Files changed:** `internal/controller/controller.go`, `internal/web/server.go`, `internal/web/telemetry.go`, `internal/config/config.go`, `internal/fleettelemetry/mqtt.go`, `cmd/server/main.go`

The application can now accept normalized Fleet Telemetry charge-state updates through a small public bridge endpoint:

- `POST /telemetry/tesla/charge-state`
- authenticated with `FLEET_TELEMETRY_SHARED_SECRET`
- merges partial updates into the controller's cached Tesla state
- suppresses paid `vehicle_data` polling while telemetry remains fresh for `FLEET_TELEMETRY_STALE_AFTER_SECONDS`

The application can also subscribe directly to the Tesla Fleet Telemetry MQTT backend:

- enabled by `FLEET_TELEMETRY_MQTT_BROKER`
- subscribes to `<topic_base>/<vin>/connectivity` and `<topic_base>/<vin>/v/+`
- maps the charging subset into the controller cache without an extra sidecar bridge process

This is still an app-side migration step rather than the full Fleet Telemetry deployment. The remaining work is to run the Tesla Fleet Telemetry reference server and broker in deployment, wire them into compose/runtime, and validate the end-to-end stream against real vehicle charging sessions.

## 9. Fleet Telemetry Compose Runtime

**Files changed:** `docker-compose.yml`, `deploy/fleet-telemetry/config.json`, `deploy/fleet-telemetry/mosquitto.conf`, `deploy/fleet-telemetry/README.md`

The repo now includes an optional Docker Compose profile for the Tesla Fleet Telemetry runtime:

- `mosquitto` as the MQTT backend
- `tesla/fleet-telemetry` as the telemetry server
- fixed config and broker files mounted from `deploy/fleet-telemetry/`

This keeps the current stack unchanged by default and lets operators opt into telemetry with:

- `docker compose --profile fleet-telemetry up -d`

The remaining work after this is live certificate provisioning, Tesla-side telemetry configuration, and end-to-end validation against a real vehicle charging session.

**Files changed:** `internal/tesla/tesla.go`, `internal/tesla/client.go`, `internal/tesla/testmode.go`, `internal/tesla/client_test.go`

Added real-time tracking of Tesla Fleet API call counts per billing month:

- `APIUsage` struct tracks data, command, wake, and stream signal counts with estimated cost.
- `APIUsageTracker` provides thread-safe counters with automatic month-boundary reset.
- `GetAPIUsage()` added to the `VehicleController` interface.
- `TeslaClient` methods instrumented: `GetChargeState` → `RecordData()`, `SetChargingAmps`/`StartCharging`/`StopCharging` → `RecordCommand()`, `WakeUp` → `RecordWake()`.
- `testModeController` returns zero `APIUsage`.

### Tests added
- `Test_APIUsageTracker_recordsAllTypes` — verifies counter increments for all call types
- `Test_APIUsageTracker_estimatedCost` — validates cost calculation (500 data = $1, 1000 commands = $1, 50 wakes = $1)
- `Test_APIUsageTracker_monthReset` — confirms counters reset on billing month boundary
- `Test_GetAPIUsage_returnsTrackerSnapshot` — end-to-end test via actual `TeslaClient` API calls

## 8. Tesla API Usage Dashboard (Web UI)

**Files changed:** `internal/web/handlers.go`, `internal/web/server.go`, `internal/web/templates/index.html`, `internal/web/handlers_test.go`, `internal/controller/controller.go`

Added a live API usage panel to the web dashboard:

- `GET /api/usage` returns current month's usage as JSON with:
  - Call counts for all 4 categories (data, commands, wakes, stream signals)
  - Free tier allowances computed from the $10/month personal discount
  - Estimated cost, monthly discount, and net cost
- `Controller.GetAPIUsage()` delegates to the vehicle controller.
- Web UI "Tesla API Usage (Monthly)" panel shows:
  - 4 cards with count / free-tier / progress bar per category
  - Progress bars turn yellow at 80% and red at 100%
  - Estimated cost with $10 discount and net cost
  - Auto-refreshes every 60 seconds via `updateAPIUsage()` JS function

### Free tier constants
| Category | Rate | Free allowance from $10 discount |
|----------|------|----------------------------------|
| Data | 500/$1 | 5,000 calls |
| Commands | 1,000/$1 | 10,000 calls |
| Wakes | 50/$1 | 500 calls |
| Stream Signals | 150,000/$1 | 1,500,000 signals |

### Tests added
- `Test_handleAPIUsage_returnsUsageData` — validates JSON structure and free tier values

## 9. Tesla API Usage Historical Persistence (Database)

**Files changed:** `internal/storage/storage.go`, `internal/storage/sqlite.go`, `internal/controller/controller.go`, `internal/web/handlers.go`, `internal/web/server.go`, `internal/storage/sqlite_test.go`, `internal/web/handlers_test.go`

API usage counters are now persisted to SQLite for historical trend analysis:

### Storage layer
- New `APIUsageSnapshot` type with timestamp, all 4 counter types, and estimated cost.
- New `api_usage_snapshots` table created in `Migrate()` with a timestamp index.
- `InsertAPIUsage()` and `QueryAPIUsage()` added to the `Store` interface and `SQLiteStore`.
- `Prune()` now also cleans up old API usage rows.

### Controller integration
- `flushMinuteAverage()` now persists an `APIUsageSnapshot` alongside each reading (~1 snapshot per minute).

### API endpoint
- `GET /api/usage/history?from=...&to=...&limit=...` returns historical snapshots as JSON.
  - Default: last 30 days, max 10,000 rows.

### Tests added
- `Test_InsertAPIUsage_queryAPIUsage` — round-trip insert and query with value assertions
- `Test_QueryAPIUsage_emptyRange` — empty result returns `[]` not `nil`
- `Test_QueryAPIUsage_respectsLimit` — limit cap is enforced
- `Test_Prune_deletesAPIUsageSnapshots` — old snapshots are cleaned up by prune
- `Test_handleAPIUsageHistory_returnsSnapshots` — handler returns valid JSON

### Mock updates
All `Store` mock implementations updated with `InsertAPIUsage` and `QueryAPIUsage` in:
- `cmd/server/main_test.go` (`fakeStore`)
- `internal/controller/controller_test.go` (`mockStore`)
- `internal/web/handlers_test.go` (`recordingStore`, `nullStore`)
- Tesla command paths are intentionally disabled in that mode.
- The repository now also contains the Azure-side OAuth helper and infrastructure needed to support Tesla Fleet OAuth flows, but these files do not deploy anything by themselves.

## 6. Verification Status

- Root module verification passed:

```bash
go test ./...
```

- Azure Function submodule verification passed:

```bash
cd function && go test ./...
```

## 7. Files Affected

Modified existing files:

- `cmd/server/main.go`
- `cmd/server/main_test.go`
- `instructions.md`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/controller/controller.go`
- `internal/controller/controller_test.go`
- `internal/tesla/tesla.go`
- `internal/web/templates/index.html`

New files and directories currently in the working tree:

- `.azure/deployment-plan.md`
- `function/.funcignore`
- `function/.gitignore`
- `function/Makefile`
- `function/README.md`
- `function/cmd/handler/main.go`
- `function/go.mod`
- `function/go.sum`
- `function/host.json`
- `function/internal/app/config.go`
- `function/internal/app/handler.go`
- `function/internal/app/handler_test.go`
- `function/internal/app/keyvault.go`
- `function/local.settings.json.example`
- `function/oauthcallback/function.json`
- `function/oauthstart/function.json`
- `function/public/.well-known/appspecific/README.md`
- `function/publickey/function.json`
- `infra/main.bicep`
- `internal/tesla/testmode.go`
---

# Updates — 2026-05-01

The following changes were made between 2026-04-27 and 2026-05-01 (commits `adaed02..ad0fe7c`). Listed in roughly chronological order.

## 10. Web UI rewritten in React + TypeScript + Tailwind

**Commit:** `28f2d86` (and follow-ups `cb7634f`, `6d795ff`)

The legacy server-rendered htmx/Tailwind dashboard was replaced with a Vite-built React + TypeScript SPA under `web/src/`. The Go server now embeds the built `web/dist/` assets and serves them from `/`.

- React Router with pages: Dashboard, History, Sessions, Events, Usage.
- TanStack Query for data fetching, MSAL.js for auth.
- All `/api/*` endpoints continue to return JSON; `/events` continues to push SSE.
- Build is wired into the Docker image — see `Dockerfile` and the GitHub Actions workflow.
- `VITE_ENTRA_TENANT_ID` and `VITE_ENTRA_CLIENT_ID` are injected at SPA build time via Docker build args (`6d795ff`).

> The `internal/web/templates/index.html` reference in earlier sections of this document is obsolete; the Go templates path has been removed.

## 11. Authentication migrated from Basic Auth to Entra ID (OAuth/OIDC)

**Commit:** `5817d08`

`HTTP_AUTH_USER`/`HTTP_AUTH_PASSWORD` are gone. The dashboard authenticates users with Microsoft Entra ID via MSAL.js; the Go backend validates ID tokens and enforces an OID allow-list on every protected route.

### New env vars

| Variable | Required | Description |
|---|---|---|
| `ENTRA_TENANT_ID` | Yes | Entra tenant GUID |
| `ENTRA_CLIENT_ID` | Yes | App registration client ID |
| `ENTRA_ALLOWED_OIDS` | Yes | Comma-separated list of user OID GUIDs allowed to sign in |
| `VITE_ENTRA_TENANT_ID` | Yes (build-time) | Same tenant GUID, baked into the SPA |
| `VITE_ENTRA_CLIENT_ID` | Yes (build-time) | Same client ID, baked into the SPA |

`HTTP_AUTH_USER` and `HTTP_AUTH_PASSWORD` should be removed from `.env`.

The frontend acquires an ID token via MSAL and attaches it as `Authorization: Bearer ...` to every API and SSE request. The backend validates issuer, audience, signature, and OID membership.

## 12. Tesla OAuth bootstrap brought in-app

**Commits:** `3b3fdf9`, `7353b35`, `a4c754d`, `e8d6ca0`

Replaced the standalone Azure Functions Tesla OAuth helper (now removed — see #18) with a built-in `/auth/tesla` flow served by the Go app itself.

- `internal/web/oauth.go` implements the authorization-code flow.
- State cookies are HMAC-signed with `OAUTH_STATE_HMAC_KEY`.
- Tesla credentials are required even when `TESLA_TEST_MODE=true` so the bootstrap can run before flipping into live mode (`7353b35`).
- Removed non-standard authorize parameters that Tesla rejected (`a4c754d`).
- The OAuth callback is reachable in test mode so the very first refresh token can be obtained without needing a working vehicle connection (`e8d6ca0`).
- Initial token refresh failures at startup are logged but no longer fatal (`f0e025b`) — the app boots into stub mode and lets the user re-bootstrap via `/auth/tesla`.

## 13. Tesla Vehicle Command Protocol via tesla-http-proxy sidecar

**Commits:** `fdf6c16`, `7b29672`

Newer Tesla firmware rejects unsigned charge commands with HTTP 403. We now run the official `tesla/vehicle-command:latest` proxy as a Docker Compose sidecar that signs commands with the fleet private key and forwards them to Tesla.

- New service in `docker-compose.yml`: `tesla-http-proxy` listening on `:4443` with TLS.
- `internal/tesla/client.go` routes `command/*` and `wake_up` paths through the proxy when `TESLA_COMMAND_BASE` is set; vehicle-data reads still go directly to the Fleet API.

### New env vars

| Variable | Required | Description |
|---|---|---|
| `TESLA_COMMAND_BASE` | When using the proxy | e.g. `https://tesla-http-proxy:4443` |
| `TESLA_PROXY_CA_FILE` | When using the proxy | Path to the proxy's self-signed cert, e.g. `/secrets/proxy-tls-cert.pem` |

The proxy needs three files in `./secrets/`: `fleet-key.pem` (existing), `proxy-tls-key.pem`, and `proxy-tls-cert.pem` (the proxy's own TLS keypair).

## 14. Throttle EV down when charging causes grid import

**Commit:** `efb0646`

Previously, while charging, the surplus calculation used the inverter's `surplusWatts` reading which was clamped to ≥ 0. If the EV's draw exceeded available solar, the controller never noticed the grid import and stayed at the same amps. The calculation now uses signed `gridWatts` (negative when exporting, positive when importing) when the car is in `Charging` state, so over-draw correctly reduces the target amps.

## 15. Robust SQLite timestamp parsing

**Commit:** `d767db0`

`modernc.org/sqlite` returns datetime values with several formats (`T`-separator, optional fractional seconds). The events / sessions / api-usage queries now use a multi-format parser (`parseStoredTime` in `internal/storage/sqlite.go`), so the dashboard no longer renders timestamps as `01/01/1`.

## 16. Tesla API usage — historical Usage page

**Commit:** `863f854`

Added a new top-level `/usage` page rendering three recharts charts:

- Cumulative cost over selectable range (24 h / 7 d / 30 d / all).
- Cumulative call counts by category.
- Per-snapshot deltas.

Backed by `GET /api/usage/history` (already existed; now consumed by the SPA). Supplements the existing live "Tesla API Usage (this month)" card on the dashboard.

## 17. Tesla API usage counters survive restarts

**Commits:** `08517b9`, `33fe802`

Counters were resetting to zero on every container restart. Two coordinated fixes:

- **Restore on startup (`08517b9` initial, `33fe802` final form):** at boot, `cmd/server/main.go` queries all `api_usage_snapshots` rows from the start of the current calendar month and seeds the in-memory tracker with the **maximum** of each counter. Counters are monotonic within a month, so max is correct and immune to spurious zero rows already in the database from earlier stub-mode periods.
- **Skip persisting zero snapshots from stub mode:** the controller's `flushMinuteAverage` now refuses to insert rows when `APIUsage.MonthStarted.IsZero()` (the stub controller's signature value), so a stub-mode restart can no longer poison the history with zero-count rows that would defeat the max-restore logic.

## 18. Reduce wasted Tesla wake commands

**Commit:** `8f770ee`

Three coordinated changes drastically reduce wake spam (we observed ~14 wakes/3.5 h in the prior behaviour, ~6 of which ended in "Disconnected is not actionable" and ~8 in transient surplus collapse):

1. **Plug-state derivation (`internal/tesla/client.go`):** `PluggedIn` now requires both `charge_port_latch == "Engaged"` *and* `charging_state != "Disconnected"`. The latch can briefly remain `Engaged` after the cable is removed; trusting it alone caused repeated wake attempts on a car that wasn't plugged in.
2. **Non-actionable backoff (`internal/controller/controller.go`):** new field `lastNonActionableAt` is stamped whenever an online tick reports the car as `Disconnected`, `Complete`, or with `PluggedIn=false`. `shouldAttemptWake` returns false while inside the configurable backoff window. The timestamp clears as soon as an online tick confirms an actionable plug state, and `ForceRefresh` resets it.
3. **Surplus margin for wake:** new gate `availableAmps >= MinChargeAmps + WakeMinAmpsMargin` for the offline branch of step 6 in `Tick`. Wakes take ~30 s and borderline surplus often collapses during that time, so a small margin avoids a class of doomed wakes followed immediately by `waiting for surplus`.

### New env vars

| Variable | Default | Description |
|---|---|---|
| `WAKE_MIN_AMPS_MARGIN` | `2` | Extra amps above `MIN_CHARGE_AMPS` required to initiate a wake |
| `WAKE_AFTER_NON_ACTIONABLE_BACKOFF_SECONDS` | `1800` | Suppress wakes for this many seconds after observing a non-actionable plug state |

## 19. Always poll Tesla on idle interval to detect external charge starts

**Commit:** `3d05f33`

The `default` branch of `shouldPollTesla` previously skipped polling when surplus was below `MinChargeAmps`. If the user started a charge from the Tesla app (or a scheduled charge fired) under those conditions, the controller never noticed: cached state stayed `Stopped`, no `StopCharging` was sent, and the EV silently drew from the grid. The branch now polls on `TeslaIdlePollInterval` regardless of surplus, so out-of-band state changes are detected within ~5 min.

## 20. Charge-limit slider on the dashboard

**Commits:** `9c5a01c`, `ad0fe7c`

Added a "Charge Limit" card with a range slider that controls the vehicle's `charge_limit_soc`.

- `internal/tesla` — extended `ChargeState` with `ChargeLimit`, `ChargeLimitMin`, `ChargeLimitMax` parsed from `charge_state.charge_limit_soc{,_min,_max}`. Added `SetChargeLimit(ctx, percent int) error` to the `VehicleController` interface, real client (routed through the VCP proxy), and the test-mode stub.
- `internal/controller` — surfaces all three fields on `StateSnapshot`; exposes `SetChargeLimit` (range-checked to `[50,100]`), available in both Auto and Manual modes since it's a vehicle setting rather than a charge command.
- `internal/web` — `POST /api/charge-limit` accepting `{percent: N}`.
- `web/src/components/ChargeLimitCard.tsx` — slider that submits on release (mouse-up / touch-end / key-up) so we don't spam the API mid-drag.
- The current charge limit is also shown on the EV Charging card.
- Bumped the power-flow `<svg>` `viewBox` height from 320 to 360 so the EV node label and live status text render below the icon (`ad0fe7c`).

## 21. Removed Azure Functions OAuth helper and Bicep infrastructure

**Commits:** `5a2df0f`, `a842df3`

The `function/` Azure Functions Tesla OAuth helper and the `infra/main.bicep` scaffold (added in sections 3 and 4 above) were removed in favour of running the OAuth bootstrap directly in the Go app (see #12). The corresponding `function/`, `infra/`, and `.azure/` directories are no longer used by the deployment described in this document.

## 22. Other housekeeping

- `7cb711e`: GitHub Actions workflow added to build and publish the multi-arch container image to `ghcr.io/cbellee/ev-solar-charger:latest` on push to `main`.
- `0bdc529`: top-level `README.md` added.
- `b1098bd`: critical security fixes (refresh-token storage hardening, redirect URI checks, body-size limits, generic error responses on the OAuth callback).
- `5a2df0f`: documentation moved into `docs/`.
- `3cb1a28`: project folder renamed from `solar-ev-charger-controller` to `ev-solar-charger`.

## Verification

```bash
go test ./...       # all packages green
cd web && npm run build  # SPA builds clean
```

## Outstanding doc drift

The Part A deployment guide and the Configuration Reference table in `instructions.md` were updated alongside this changelog to remove `HTTP_AUTH_*` references, document the Entra ID env vars, and describe the tesla-http-proxy sidecar. Earlier paragraphs in this `updates.md` that mention `internal/web/templates/index.html`, the Azure Functions helper, or basic-auth-protected endpoints are retained as historical record but are no longer accurate descriptions of the running system.

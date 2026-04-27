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
- **Charging:** polls every `TESLA_CHARGING_POLL_SECONDS` (default 60s) — enough to track battery % and adjust amps.
- **Idle with surplus:** polls every `TESLA_IDLE_POLL_SECONDS` (default 300s) — checks if car is plugged in before waking.
- **Idle without surplus:** skips entirely — no reason to poll until surplus appears.
- **Wake pending:** skips — the wake command handles its own polling loop.
- **No cached state:** polls immediately on first call.

Between API calls, `cachedChargeState` keeps the UI and state machine working from local inverter data.

### Amps change hysteresis
When charging, `SetChargingAmps` is only sent when the change exceeds `AMPS_CHANGE_THRESHOLD` (default 2A). This avoids command oscillation from minor solar fluctuations.

### New environment variables
| Variable | Default | Description |
|----------|---------|-------------|
| `TESLA_CHARGING_POLL_SECONDS` | 60 | Poll interval while charging |
| `TESLA_IDLE_POLL_SECONDS` | 300 | Poll interval when idle with surplus |
| `AMPS_CHANGE_THRESHOLD` | 2 | Minimum amp delta to send a command |

### Tests added
- 9 new tests in `internal/controller/controller_test.go` covering polling logic and hysteresis
- 5 new tests in `internal/config/config_test.go` for the new config fields

See `cost-optimisation.md` for full cost analysis and trade-offs.

## 7. Tesla API Usage Tracking (In-Memory)

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
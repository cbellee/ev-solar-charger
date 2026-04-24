# Updates

Last updated: 2026-04-07

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
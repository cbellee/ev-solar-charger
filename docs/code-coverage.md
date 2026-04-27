# Code Coverage Report

Date: 2026-04-03

## Summary

- Coverage run completed successfully.
- Total statement coverage: 85.7%.
- Coverage improved by 2.6 points from the previous 83.1% pass.
- `go vet ./...` completed successfully after the latest coverage-driven test additions.

## Latest Coverage Gains

This pass targeted the next two packages called out in the previous report:

| Package | Previous | Current | Delta |
|---|---:|---:|---:|
| `github.com/cbellee/solar-ev-charger/internal/inverter` | 70.9% | 81.6% | +10.7 |
| `github.com/cbellee/solar-ev-charger/internal/tesla` | 70.5% | 82.9% | +12.4 |

## Current Package Coverage

| Package | Coverage |
|---|---:|
| `github.com/cbellee/solar-ev-charger/cmd/server` | 76.9% |
| `github.com/cbellee/solar-ev-charger/internal/config` | 80.0% |
| `github.com/cbellee/solar-ev-charger/internal/controller` | 89.2% |
| `github.com/cbellee/solar-ev-charger/internal/inverter` | 81.6% |
| `github.com/cbellee/solar-ev-charger/internal/observability` | 84.6% |
| `github.com/cbellee/solar-ev-charger/internal/storage` | 83.2% |
| `github.com/cbellee/solar-ev-charger/internal/tesla` | 82.9% |
| `github.com/cbellee/solar-ev-charger/internal/web` | 97.0% |

## Per-Function Detail For This Pass

### `internal/inverter`

| Function | Coverage |
|---|---:|
| `New` | 100.0% |
| `Connect` | 75.0% |
| `GetPowerData` | 62.5% |
| `readRegisterWithRetry` | 88.9% |
| `isTokenExpiry` | 100.0% |
| `readRegister` | 83.3% |
| `parseRegisterValue` | 100.0% |
| `Close` | 100.0% |

### `internal/tesla`

| Function | Coverage |
|---|---:|
| `New` | 100.0% |
| `refreshAccessToken` | 87.5% |
| `doRequest` | 82.0% |
| `GetChargeState` | 76.9% |
| `SetChargingAmps` | 84.6% |
| `StartCharging` | 75.0% |
| `StopCharging` | 75.0% |
| `WakeUp` | 69.2% |

## Remaining Hot Spots

These are the lowest-covered functions in the current repository-wide profile:

| Area | Function | Coverage | Notes |
|---|---|---:|---|
| `cmd/server` | `run` | 0.0% | Thin delegation wrapper around `runWithContext`; either add a direct test or inline/remove it. |
| `cmd/server` | `defaultRuntimeDeps` | 25.0% | Mostly dependency wiring; coverage here needs either more startup-path tests or a small refactor. |
| `cmd/server` | `main` | 50.0% | Non-healthcheck process startup is still only partially covered. |
| `internal/inverter` | `GetPowerData` | 62.5% | More malformed register and transport-path testing would move this quickly. |
| `internal/config` | `parseLogLevel` | 66.7% | One or two invalid/edge log-level cases would finish this off. |
| `internal/web` | `handleIndex` | 66.7% | Remaining gap is limited to the template execution branch. |
| `internal/observability` | `NewMetrics` | 69.0% | Remaining uncovered lines are in instrument setup failure branches. |
| `internal/tesla` | `WakeUp` | 69.2% | Retry/timing branches still need explicit tests. |
| `internal/inverter` | `Connect` | 75.0% | Remaining gaps are failure branches after the socket opens. |
| `internal/storage` | `StartSession` | 75.0% | Minor DB-path branches remain untested. |
| `internal/storage` | `EndSession` | 75.0% | Same as above. |
| `internal/storage` | `InsertEvent` | 75.0% | Same as above. |
| `internal/tesla` | `StartCharging` | 75.0% | Error branch coverage is still light. |
| `internal/tesla` | `StopCharging` | 75.0% | Error branch coverage is still light. |

## What Was Added In This Pass

Coverage was raised by adding direct tests in these areas:

- `internal/tesla`: constructor coverage for invalid regions, missing key files, valid EC key loading, invalid EC key parsing, and initial token refresh failure.
- `internal/inverter`: WebSocket close coverage, invalid register payload coverage, and single-value/error fallback coverage in `parseRegisterValue`.

## Concrete Next Tests

If coverage needs to move materially beyond 85.7%, these are the next efficient additions:

1. Add retry and timeout-path tests for [internal/tesla/client.go](/Users/chris/Documents/repos/github.com/cbellee/solar-ev-charger-controller/internal/tesla/client.go), especially around `WakeUp`, `StartCharging`, and `StopCharging`.
2. Add post-connect failure-path tests for [internal/inverter/sungrow.go](/Users/chris/Documents/repos/github.com/cbellee/solar-ev-charger-controller/internal/inverter/sungrow.go), especially around `Connect` and `GetPowerData` transport/result-code errors.
3. Decide whether [cmd/server/main.go](/Users/chris/Documents/repos/github.com/cbellee/solar-ev-charger-controller/cmd/server/main.go) should keep the thin `run` wrapper; testing or deleting it is still the easiest way to remove the remaining 0.0% function.
4. Add failure-path coverage for metric instrument creation in [internal/observability/otel.go](/Users/chris/Documents/repos/github.com/cbellee/solar-ev-charger-controller/internal/observability/otel.go).
5. Add one invalid-template or error-path test around [internal/web/handlers.go](/Users/chris/Documents/repos/github.com/cbellee/solar-ev-charger-controller/internal/web/handlers.go) if you want to push the already-high web package closer to complete coverage.

## Commands Used

```bash
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out
go vet ./...
```
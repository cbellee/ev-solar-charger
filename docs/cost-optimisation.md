# Tesla Fleet API Cost Optimisation

## Background

The Tesla Fleet API uses a pay-per-use pricing model ([billing docs](https://developer.tesla.com/docs/fleet-api/billing-and-limits), [pricing](https://developer.tesla.com/#usage-based-pricing)):

| Category           | Rate              | Cost per call |
|--------------------|-------------------|---------------|
| Streaming Signals  | 150,000 / $1      | $0.0000067    |
| Commands           | 1,000 / $1        | $0.001        |
| Data (vehicle_data)| 500 / $1          | $0.002        |
| Wakes              | 50 / $1           | $0.02         |

Rate limits per device, per account:
- Realtime Data: 60 requests/min
- Wakes: 3 requests/min
- Device Commands: 30 requests/min

Each account receives a **$10/month discount** for personal use.

## Problem

The controller previously called `GetChargeState()` (which hits the `vehicle_data` endpoint) on every tick of the poll loop (default: every 10 seconds). This produced ~8,640 API calls per day, costing approximately **$17/day or $518/month** — far exceeding the $10/month personal discount.

Sending `SetChargingAmps` on every 1A fluctuation also generated unnecessary command calls.

## Changes

### 1. State-Aware Tesla API Polling

**Files changed:** `internal/config/config.go`, `internal/controller/controller.go`

The inverter is polled every tick (free, local Modbus call), but Tesla API calls are now gated by `shouldPollTesla()` which considers the controller state:

| Controller State | Poll Interval | Rationale |
|------------------|---------------|-----------|
| First call (no cache) | Immediate | Need initial vehicle state |
| Charging | `TESLA_CHARGING_POLL_SECONDS` (default 60s) | Track battery % and actual amps; 60s is sufficient for adjustment |
| Idle/Monitoring with surplus ≥ min amps | `TESLA_IDLE_POLL_SECONDS` (default 300s) | Check if car is still plugged in before waking |
| Idle/Monitoring, no surplus | Skipped entirely | No reason to poll — surplus must appear first |
| Wake Pending | Skipped | Wake command handles its own polling loop |

Between Tesla API calls, the controller uses `cachedChargeState` so the UI snapshot and state machine continue updating from inverter data.

### 2. Amps Change Hysteresis

**Files changed:** `internal/config/config.go`, `internal/controller/controller.go`

When already charging, `SetChargingAmps` is only sent when the change exceeds `AMPS_CHANGE_THRESHOLD` (default 2A). This prevents command oscillation from minor solar fluctuations (e.g. passing clouds) while still reacting to meaningful surplus changes.

Small fluctuations (±1A) are absorbed without any API call. Large ramps (e.g. morning sun rising from 5A to 20A surplus) trigger an immediate adjustment.

### 3. New Configuration Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `TESLA_CHARGING_POLL_SECONDS` | 60 | Interval between Tesla API polls while actively charging |
| `TESLA_IDLE_POLL_SECONDS` | 300 | Interval between Tesla API polls when idle but surplus is available |
| `AMPS_CHANGE_THRESHOLD` | 2 | Minimum amp change required to send a `SetChargingAmps` command |

All three have validation (≥ 1 for intervals, ≥ 0 for threshold) and are optional with sensible defaults.

## Estimated Cost Impact

Assuming 6 hours charging, 2 hours monitoring, 16 hours idle per day:

| Category | Before (per day) | After (per day) |
|----------|-------------------|-----------------|
| Data calls (charging) | 2,160 ($4.32) | 360 ($0.72) |
| Data calls (monitoring) | 720 ($1.44) | 24 ($0.05) |
| Data calls (idle) | 5,760 ($11.52) | 0 ($0.00) |
| Command calls | ~100 ($0.10) | ~30 ($0.03) |
| Wake calls | ~2 ($0.04) | ~2 ($0.04) |
| **Daily total** | **~$17.42** | **~$0.84** |
| **Monthly total** | **~$523** | **~$25 − $10 discount ≈ $15** |

This represents approximately a **95% cost reduction** while maintaining responsive solar-tracking behaviour during active charging.

## Trade-offs

- **Slower reaction when idle:** If surplus appears and the car state has changed (e.g. plugged in while idle), detection is delayed by up to `TESLA_IDLE_POLL_SECONDS`. This is acceptable because surplus must sustain for `WAKE_THRESHOLD_POLLS` ticks before a wake is attempted anyway.
- **Amps granularity:** The 2A hysteresis means charging may be 1A below optimal at times. The energy difference is negligible (~240W) compared to the API cost savings.
- **Cached state staleness:** If the car is unplugged mid-charge, the controller won't detect it until the next poll. The Tesla API will return an error on the next command, triggering an immediate fresh poll via the error path.

## Tests

New tests in `internal/controller/controller_test.go`:
- `Test_shouldPollTesla_noCacheAlwaysPolls`
- `Test_shouldPollTesla_chargingRespectsInterval`
- `Test_shouldPollTesla_idleNoSurplusSkipsPoll`
- `Test_shouldPollTesla_idleWithSurplusPolls`
- `Test_shouldPollTesla_wakePendingSkipsPoll`
- `Test_Tick_usesCachedStateWhenPollNotDue`
- `Test_Tick_ampsHysteresisSkipsSmallChange`
- `Test_Tick_ampsHysteresisAllowsLargeChange`
- `Test_Tick_noTeslaCallsWhenIdleNoSurplus`

New tests in `internal/config/config_test.go`:
- `Test_Load_teslaChargingPollSecondsCustom`
- `Test_Load_teslaChargingPollSecondsZero`
- `Test_Load_teslaIdlePollSecondsCustom`
- `Test_Load_ampsChangeThresholdCustom`
- `Test_Load_ampsChangeThresholdNegative`

All existing tests continue to pass with zero-value intervals and thresholds in `defaultCfg()`, preserving the original every-tick behaviour in test mode.

## References

- [Tesla Fleet API Billing and Limits](https://developer.tesla.com/docs/fleet-api/billing-and-limits)
- [Tesla Fleet API Usage-Based Pricing](https://developer.tesla.com/#usage-based-pricing)
- [Tesla Fleet API Cost Optimization Best Practices](https://developer.tesla.com/docs/fleet-api/billing-and-limits#cost-optimization)

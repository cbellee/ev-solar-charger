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
| Charging | `TESLA_CHARGING_POLL_SECONDS` (default 300s) | Track battery % and actual amps with lower data-call spend |
| Idle / Monitoring | `TESLA_IDLE_POLL_SECONDS` (default 1800s) | Catch out-of-band state changes without frequent polling |
| Wake Pending | `WAKE_RETRY_INTERVAL_SECONDS` (default 300s) | Re-check periodically if the car still has not come online |

Between Tesla API calls, the controller uses `cachedChargeState` so the UI snapshot and state machine continue updating from inverter data.

The `WakeUp` path also uses a bounded follow-up schedule now: two delayed `vehicle_data` checks across 30 seconds instead of a 2-second polling loop. That caps wake follow-up data usage at two calls per wake attempt.

### 2. Amps Change Hysteresis

**Files changed:** `internal/config/config.go`, `internal/controller/controller.go`

When already charging, `SetChargingAmps` is only sent when the change exceeds `AMPS_CHANGE_THRESHOLD` (default 2A) and the last automatic amp change is older than `AMPS_ADJUST_INTERVAL_SECONDS` (default 60s). This prevents command oscillation from minor solar fluctuations and caps automatic amp-adjust traffic at roughly one command per minute.

Small fluctuations (±1A) are absorbed without any API call. Large ramps (e.g. morning sun rising from 5A to 20A surplus) trigger an immediate adjustment.

### 3. New Configuration Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `TESLA_CHARGING_POLL_SECONDS` | 300 | Interval between Tesla API polls while actively charging |
| `TESLA_IDLE_POLL_SECONDS` | 1800 | Interval between Tesla API polls while idle or monitoring |
| `AMPS_CHANGE_THRESHOLD` | 2 | Minimum amp change required to send a `SetChargingAmps` command |
| `AMPS_ADJUST_INTERVAL_SECONDS` | 60 | Minimum time between automatic `SetChargingAmps` commands while charging |

All three have validation (≥ 1 for intervals, ≥ 0 for threshold) and are optional with sensible defaults.

## Estimated Cost Impact

Assuming 6 hours charging, 2 hours monitoring, 16 hours idle per day, plus two wake attempts:

| Category | Before (per day) | After (per day) |
|----------|-------------------|-----------------|
| Data calls (charging) | 2,160 ($4.32) | 72 ($0.14) |
| Data calls (monitoring) | 720 ($1.44) | 4 ($0.01) |
| Data calls (idle) | 5,760 ($11.52) | 32 ($0.06) |
| Wake follow-up data calls | Included above | 4 ($0.01) |
| Command calls | ~100 ($0.10) | ~15 ($0.02) |
| Wake calls | ~2 ($0.04) | ~2 ($0.04) |
| **Daily total** | **~$17.42** | **~$0.28** |
| **Monthly total** | **~$523** | **~$8.40 − $10 discount ≈ $0** |

This represents approximately a **98% cost reduction** while maintaining responsive solar-tracking behaviour during active charging.

## Trade-offs

- **Slower reaction when idle:** If surplus appears and the car state has changed (e.g. plugged in while idle), detection is delayed by up to `TESLA_IDLE_POLL_SECONDS`. This is acceptable because surplus must sustain for `WAKE_THRESHOLD_POLLS` ticks before a wake is attempted anyway.
- **Amps granularity:** The 2A hysteresis and 60s adjustment interval mean charging can lag the instantaneous optimum briefly. The energy difference is negligible compared to the API cost savings.
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

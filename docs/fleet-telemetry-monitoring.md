# Fleet API Usage Monitoring Strategy

## Goal

Track Tesla Fleet API usage cheaply enough for day-to-day operations while still preserving a trustworthy source for actual billed usage.

## Source Of Truth

- Tesla Developer Dashboard is the billing authority for application usage and invoices.
- There is no documented public Fleet API endpoint that returns exact billed application usage history.
- Local application counters are estimates for operational monitoring, not invoice reconciliation.

## Recommended Monitoring Layers

### 1. Tesla Developer Dashboard

Use the Tesla Developer Dashboard usage page for:

- actual billed totals
- billing-limit monitoring
- month-end reconciliation
- category drift detection

This is the only documented authoritative source for billed usage.

### 2. Fleet Telemetry Metrics

Use Fleet Telemetry as the primary realtime data path instead of polling `vehicle_data`.

Recommended defaults:

- Prometheus metrics as the primary signal-count backend
- connectivity events enabled so the app can infer when a vehicle is online
- `interval_seconds` set conservatively per field
- `minimum_delta` enabled on noisy numeric fields where supported
- VIN-specific signal tracking enabled only when per-vehicle attribution is necessary

Why this is cheapest:

- signals are sent on change rather than every poll
- online state can come from connectivity events instead of paid polling
- runtime state can come from streams instead of repeated `vehicle_data`

### 3. Local Non-Stream Counters

Keep local counters for paid HTTP categories that telemetry metrics do not cover directly:

- `vehicle_data`
- commands
- `wake_up`

These counters should:

- increment after HTTP responses are received
- count only billable status codes below `500`
- count retries as separate billable events when Tesla would bill them
- reset on Tesla billing month boundaries

## Control-Plane Checks

Use `fleet_status` sparingly as a cached control-plane endpoint, not as a live polling loop.

It should be used to discover:

- Fleet Telemetry support and client version
- virtual key presence
- older vehicle streaming toggle state
- `discounted_device_data` eligibility

Refresh on startup, auth recovery, and a slow background interval rather than per tick.

## Repo Migration Plan

### Phase 1

Improve the existing local tracker so it counts billable responses instead of request attempts.

### Phase 2

Clarify the UI so usage numbers are explicitly labeled as local estimates and the Tesla dashboard is identified as billing truth.

### Phase 3

Add cached `fleet_status` support so the app can detect discounted device-data pricing and telemetry capability.

### Phase 4

Deploy Fleet Telemetry with Prometheus metrics and connectivity events.

### Phase 5

Move routine vehicle-state monitoring from `vehicle_data` polling to Fleet Telemetry, keeping polling only as a fallback for unsupported vehicles.

## Practical Guidance For This Repo

For the EV solar charger application:

- use Fleet Telemetry for live charge and connectivity state where supported
- keep local counters for commands and wakes even after telemetry is added
- treat the local Usage page as an operational estimate
- reconcile the estimate against Tesla Developer Dashboard when validating cost reductions

## Cost-Control Rules From Tesla Docs

- avoid polling `vehicle_data` regularly
- verify connectivity before commands and wakes
- avoid repeated wakes
- validate command failures before retrying
- use Fleet Telemetry instead of polling whenever possible

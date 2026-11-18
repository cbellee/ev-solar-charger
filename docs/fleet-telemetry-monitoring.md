# Fleet Telemetry Migration and API Usage Monitoring

## Goal

Detect plug-in and charging-state changes promptly without frequent paid
`vehicle_data` polling. Track Tesla Fleet API usage cheaply enough for day-to-day
operations while preserving a trustworthy source for actual billed usage.

## Plan Review

The original monitoring strategy is sound, but usage metrics alone cannot
deliver plug-in events to the controller. A complete migration requires a
telemetry receiver, an event transport into this app, and freshness-aware
controller integration.

Current implementation status:

| Phase | Status |
| --- | --- |
| 1: Billable HTTP response counters | Already implemented |
| 2: Local-estimate labeling in the UI | Already implemented |
| 3: Cached capability discovery | Implemented, including diagnostics below |
| 4: Receiver deployment and event transport | Compose stack and MQTT consumer implemented; NAS provisioning pending |
| 5: Telemetry-driven charging state | Event-triggered REST confirmation implemented; authoritative streamed state deferred |

**The stack is opt-in and has not been deployed to the NAS.** No vehicle
telemetry configuration is changed automatically. Without the telemetry
overlay, plug-in detection still uses the existing polling intervals.

### Approved Initial Integration Mode

Tesla's standard MQTT dispatcher publishes individual field values without
vehicle timestamps. It cannot satisfy the per-field replay/freshness requirements
for authoritative streamed charging state. The initial implementation therefore
uses charging/connectivity events as rate-limited hints to request a fresh
`vehicle_data` read. Only REST-confirmed state reaches charging control.

This preserves existing cooldowns and background polling, while avoiding a
30-minute wait for an eligible event. `TESLA_TELEMETRY_REFRESH_MIN_SECONDS`
(default 60) bounds hint-driven refresh frequency. Repeated state values and
retained MQTT messages do not request additional refreshes; broker subscription
and fresh vehicle connection events request reconciliation.

See [NAS setup and validation](fleet-telemetry-setup.md) for deployment steps,
security boundaries, certificate automation, and explicit vehicle configuration.

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

Implemented behavior:

- request status for the configured VIN at real-client startup, after explicit
  OAuth token replacement, and before charge-state reads when the cache is due
- retain successful checks for six hours; failed checks become eligible for
  retry after five minutes, on the next charge-state read
- discover capabilities even when `vehicle_data` reports the car offline
- parse `vehicle_info[VIN]`, `key_paired_vins`, and `unpaired_vins`, retaining
  compatibility with flat, VIN-keyed, and VIN-tagged list responses
- reject missing/mismatched vehicles and malformed capability responses rather
  than interpreting them as a successful negative capability check
- preserve the last successful data on refresh failure, expose its timestamp
  and a safe error message, and log the underlying failure server-side
- keep control-plane requests out of data/command/wake usage counters

`GET /api/telemetry/status` returns cached diagnostics under the existing API
authentication. It does not call Tesla or wake the car. It reports:

- `fetchedAt` and `lastAttemptAt`; a zero `fetchedAt` means no successful check
- `lastError`, when a check failed
- `keyPaired`, when the response explicitly identifies pairing status
- `firmwareVersion` and `fleetTelemetryVersion`, when reported
- `vehicleCommandProtocolRequired` and
  `safetyScreenStreamingToggleEnabled`, when reported
- `discountedDataEligible`

Unavailable and test-mode clients return HTTP 503 rather than invented
capabilities. Optional boolean fields are omitted when Tesla does not report
them; missing is not equivalent to false.

A reported telemetry client version is not proof of end-to-end readiness.
Hardware/firmware support, pairing, applicable vehicle streaming consent,
receiver reachability, and configuration adoption still need verification.
Configuration rejection reasons and the telemetry configuration's `synced`
state must be checked during provisioning.

### Phase 4

Deploy Fleet Telemetry with Prometheus metrics and connectivity events.

#### Selected Deployment Architecture

- Host the reference receiver on the existing Synology NAS using Docker Compose.
- Add a private MQTT broker sidecar for vehicle and connectivity records.
  Do not publish the MQTT port to the internet.
- Use `telemetry.ev.bellee.net:8443` as the public receiver endpoint, with a
  Cloudflare DNS-only record. Forward public TCP 8443 directly to the receiver
  on the NAS, bypassing DSM nginx and leaving the dashboard's port 443 unchanged.
- Issue a dedicated certificate with ACME DNS-01 through Cloudflare.
  Store the zone-scoped DNS API token locally outside Git; never include it in
  generated configuration examples or logs.
- Use a Synology scheduled task for certificate renewal and restart the
  receiver only when renewed certificates need loading. Do not mount the
  Docker socket into the ACME container.
- Provide operator instructions for DNS, MikroTik port forwarding, the
  restricted Cloudflare token, and the Synology renewal task.

The receiver configuration, MQTT hint integration, certificate automation, and
operator instructions are implemented. The checked-in receiver and broker
configuration has been exercised locally with temporary certificates, along with
real MQTT delivery to the consumer. Public DNS, router/NAS changes, ACME issuance,
and actual vehicle streaming still require explicit provisioning and validation.

Implementation requirements:

1. Use Tesla's reference Fleet Telemetry receiver, not an ordinary HTTP webhook
   added to this app.
2. Allocate a public DNS name, externally reachable TCP port, and server
   certificate chain compatible with Tesla's receiver requirements. Terminate
   vehicle mutual TLS at the receiver; do not put it behind an ordinary
   TLS-terminating HTTP reverse proxy.
3. Use the MQTT dispatcher to carry both vehicle records (`V`) and
   `connectivity` events to the app. Prometheus is for monitoring, not event
   delivery.
4. Keep the dispatcher private/authenticated. Validate the configured VIN at
   ingestion; do not expose an unauthenticated vehicle-state write endpoint.
5. Add receiver health checks and metrics, test TLS connectivity, then submit
   `fleet_telemetry_config` through the existing vehicle-command proxy so the
   configuration is signed. Configuration submission is an explicit operator
   action, not an automatic side effect of application startup.
6. Confirm `synced`, inspect telemetry errors, and verify an actual plug/unplug
   event reaches the consumer before reducing normal polling.

The initial hint-only configuration requests `DetailedChargeState` and
`ChargePortLatch`, with ten-second minimum transmission intervals. It does not
need battery/current/limit streaming because REST remains authoritative. Verify
firmware requirements against Tesla's available-data reference during
provisioning. Avoid location data and unnecessary signals.

### Phase 5

Move routine vehicle-state monitoring from `vehicle_data` polling to Fleet
Telemetry, keeping polling as a fallback for unsupported vehicles, disconnections,
and stale or incomplete telemetry.

Controller acceptance criteria:

- in the initial hint mode, an event causes a confirmation read on the next
  normal tick once the event-refresh budget permits; it does not wait for
  `TESLA_IDLE_POLL_SECONDS`, but may wait for the configured minimum interval
- for a future authoritative stream, a fresh complete plug/unplug update reaches
  the dashboard within one normal controller tick without a confirmation read
- merge partial records without replacing absent values with zero/false
- use per-field vehicle timestamps to reject duplicate, out-of-order, or replayed
  older records, including after consumer reconnect
- distinguish disconnected, unknown, and confirmed-unplugged state; a
  connectivity event alone does not establish plug state
- do not let an unrelated fresh signal keep stale plug/current/limit data valid
- fall back to bounded polling until all required state is known and fresh, and
  after receiver/transport failure; recover automatically when streaming resumes
- preserve existing command validation, wake windows, backoffs, manual mode,
  test mode, and charging safety behavior
- cover partial records, stale state, reconnects, and plug/unplug transitions
  with deterministic tests before enabling telemetry-driven control

Do not count every received field as newly billed usage: reliable delivery can
replay records. Use receiver metrics for operational estimates, account for
receiver restarts, and continue treating Tesla's dashboard as billing truth.

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

## References

- [Tesla Fleet Telemetry overview](https://developer.tesla.com/docs/fleet-api/fleet-telemetry)
- [Available telemetry data](https://developer.tesla.com/docs/fleet-api/fleet-telemetry/available-data)
- [Fleet Status and telemetry configuration endpoints](https://developer.tesla.com/docs/fleet-api/endpoints/vehicle-endpoints)
- [Reference receiver setup, dispatchers, connectivity, and metrics](https://github.com/teslamotors/fleet-telemetry)

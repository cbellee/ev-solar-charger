# Fleet Telemetry on Synology

This is an opt-in stack for `telemetry.ev.bellee.net:8443`. It does not replace the
dashboard's DSM reverse proxy, change DNS/router settings, or register the car
automatically.

## What this implementation does

```text
Tesla vehicle -- mutual TLS / TCP 8443 --> Fleet Telemetry receiver
                                             |
                                             v
                                    private MQTT broker
                                             |
                                             v
                                 app queues a vehicle refresh
                                             |
                                             v
                                  Tesla API confirms state
```

Tesla's standard MQTT dispatcher omits vehicle timestamps from individual field
messages. Accordingly, MQTT is a **hint**, not an authoritative source of plug
state or charging current. Charging control continues to use REST-confirmed
state and the existing safety checks.

- Subscribe only to `DetailedChargeState`, `ChargePortLatch`, and connectivity.
- Coalesce duplicate charging values; ignore retained messages.
- Validate connectivity timestamps; a fresh `CONNECTED` event requests a refresh.
- Reconcile after MQTT subscription/reconnection.
- Rate-limit event-triggered reads with
  `TESLA_TELEMETRY_REFRESH_MIN_SECONDS` (default 60).
- Keep normal charging/idle polling unchanged if telemetry is missing or broken.

An eligible event causes a REST read on the next normal controller tick. If a
read just happened, the event remains pending until the minimum interval has
elapsed. With the default 10-second loop and 60-second event interval, allow up
to roughly 70 seconds plus API latency, assuming healthy inverter/API access.
This is not a guaranteed event-delivery SLA.

Streaming signals and confirmation reads are billable. This implementation
improves detection without paying for continuous one-minute idle polling, but
does not eliminate the slow background polling cost. Local usage counters do
not count MQTT deliveries as newly billed signals; use receiver metrics and the
Tesla Developer Dashboard.

## 1. Prepare the NAS

Use the source/image built from the telemetry branch. An older GHCR `latest`
image will not contain the MQTT consumer.

Copy the new Compose overlay, [receiver configuration](../deploy/telemetry/receiver.json),
[broker configuration](../deploy/telemetry/mosquitto.conf), and
[certificate script](../scripts/telemetry-cert.sh) along with the project.
The deployment uses pinned receiver, broker, and ACME image versions.

Add these values to the NAS's existing `.env` locally:

```dotenv
TELEMETRY_ACME_EMAIL=your-acme-contact-email
TESLA_TELEMETRY_REFRESH_MIN_SECONDS=60
```

The overlay supplies `TESLA_TELEMETRY_MQTT_URL=tcp://telemetry-mqtt:1883`.
Without the overlay or an explicitly configured URL, MQTT is disabled. A real
client requires `TESLA_TEST_MODE=false`; test mode never starts the consumer.

The certificate script requires Bash, OpenSSL, curl, Docker, and either
`docker compose` or `docker-compose`. Run certificate tasks as root through DSM
Task Scheduler, using the repository directory as the working directory.

## 2. Create the Cloudflare token locally

Create a Cloudflare API token with:

- Zone / DNS / Edit
- Zone / Zone / Read
- Resources restricted to the `bellee.net` zone

Store just the token in:

```text
data/telemetry/cloudflare-dns-api-token
```

Create its parent directory with mode `0700` and the file with mode `0600`,
owned by the root task user. Use a local secure editor; do not put the token in
shell arguments, chat, logs, or Git. Do not put it under the existing `secrets/`
mount: that directory is shared with the app and command proxy.

Only the ACME container receives this file, through
`CF_DNS_API_TOKEN_FILE`. DNS-01 issuance needs outbound HTTPS/DNS access, not
public inbound port 80. The token must remain available for automatic renewal.

## 3. Issue the dedicated certificate

Create a root-owned DSM scheduled task and initially run it manually:

```bash
cd /volume1/docker/solar-ev-charger
bash scripts/telemetry-cert.sh issue
```

Adjust the repository path to the actual NAS deployment. Issuance accepts the
ACME provider's terms of service. Do not run `issue` repeatedly; use `renew`
after successful issuance.

The script:

- stores the ACME account and certificate files under `data/telemetry/acme`
- checks certificate expiry and the public/private key match
- stages the full certificate bundle and private key under `data/telemetry/tls`
- restricts deployed key access to root and the receiver's nonroot group, 65532
- serializes runs with `data/telemetry/cert-renewal.lock`

Do not delete the ACME account directory or generated keys during routine
cleanup. If a task was forcibly interrupted, verify that it is no longer
running before removing an empty stale lock directory.

## 4. Start and check the receiver

For a source build, using Compose v2:

```bash
docker compose -f docker-compose.yml -f docker-compose.telemetry.yml config --quiet
docker compose -f docker-compose.yml -f docker-compose.telemetry.yml up -d --build
docker compose -f docker-compose.yml -f docker-compose.telemetry.yml ps
curl --fail http://127.0.0.1:19091/status
curl --fail http://127.0.0.1:19090/metrics
```

With Compose v1, substitute `docker-compose` for `docker compose`. To deploy a
published image instead, use the existing GHCR override with an image that
includes these changes, followed by the telemetry overlay.

Security boundaries:

- Only TCP 8443 is publicly published for vehicle traffic.
- Metrics (19090) and status (19091) are bound to NAS loopback.
- MQTT has **no published port** and lives on an internal Docker network.
- The broker is anonymous inside that trusted network. Do not attach unrelated
  containers or publish port 1883. An external/shared broker needs authentication
  and TLS support designed separately; this stack is not configured for one.
- The receiver requires and verifies Tesla vehicle client certificates.
- No container gets access to the Docker socket.

The receiver's `/status` is a liveness endpoint and can start before its MQTT
and TLS initialization completes. Check container stability, logs, metrics, and
the public TLS listener too; an `ok` response alone is not end-to-end readiness.

## 5. Configure public DNS and the MikroTik

In Cloudflare, create an **A record** for `telemetry.ev.bellee.net` pointing to the
home connection's public IPv4 address. Set it to **DNS only** (grey cloud).
Keep it updated if the public IP changes. Do not create an AAAA record unless
IPv6 routing/firewalling has also been deliberately configured.

On the MikroTik, replace `<NAS_LAN_IP>` with the actual NAS address:

```routeros
/ip firewall nat add chain=dstnat in-interface-list=WAN protocol=tcp dst-port=8443 \
    action=dst-nat to-addresses=<NAS_LAN_IP> to-ports=8443 \
    comment="Tesla Fleet Telemetry TCP 8443"
```

Review the router's forward-chain policy and NAS firewall so this specific
forward is allowed. Do not disable either firewall or expose DSM administration.
Do not forward ports 1883, 19090, or 19091. No new DSM HTTPS reverse-proxy rule
is needed: vehicle mutual TLS must reach the receiver unchanged.

Validate from outside the home network:

```bash
openssl s_client -connect telemetry.ev.bellee.net:8443 \
  -servername telemetry.ev.bellee.net -showcerts </dev/null
```

Inspect the served certificate name, expiry, and chain. A client-certificate
required alert is expected without a Tesla vehicle certificate. It is not proof
that a vehicle has connected. A timeout/refusal or a DSM/dashboard certificate
means the forwarding path still needs correction.

## 6. Configure the vehicle explicitly

Do this only after the external receiver is reachable and its certificate
chain has been validated.

1. Inspect authenticated `GET /api/telemetry/status` for pairing, firmware, the
   telemetry client version, and any capability-check error. This endpoint
   only reports cached capabilities, not receiver connectivity.
2. Verify model/firmware support and applicable in-car third-party streaming
   consent against Tesla's current requirements. `DetailedChargeState` requires
   firmware 2024.38 or newer. Do not infer support solely from a version string.
3. Make an ignored working copy of
   [vehicle-config.example.json](../deploy/telemetry/vehicle-config.example.json)
   under `data/telemetry/`. Replace its VIN and CA placeholder with the configured
   VIN and the PEM CA chain that validates the receiver. This is a CA trust
   chain, **not the private key** and not a short-lived leaf certificate pin.
4. Validate that hostname/port/CA using Tesla's
   [certificate-checking tool](https://github.com/teslamotors/fleet-telemetry/blob/v0.10.0/tools/check_server_cert.sh).
   Resolve chain errors rather than bypassing validation.
5. Submit the request explicitly through the existing `tesla-http-proxy` at
   `POST /api/1/vehicles/fleet_telemetry_config`, using Tesla's
   [documented proxy procedure](https://github.com/teslamotors/vehicle-command/tree/main/cmd/tesla-jws#using-tesla-http-proxy)
   and a valid owner access token. Keep bearer tokens in protected local files
   or existing secure tooling, not pasted into command arguments. Do not expose
   the command proxy publicly to do this.
6. Inspect `skipped_vehicles` and any error response. Then check
   `GET /api/1/vehicles/{vin}/fleet_telemetry_config` until `synced` is true and
   inspect `GET /api/1/vehicles/{vin}/fleet_telemetry_errors` if needed.
7. Plug/unplug the car and verify receiver metrics, the app's MQTT subscription
   log, and the dashboard's **REST-confirmed** state. Keep the existing polling
   intervals until this works. Recheck adoption/streaming after certificate
   issuer-chain changes.

The template requests only two change-oriented fields, not battery/current
streaming or location data. This first mode only needs hints; subscribing to
more fields would add cost without replacing the app's authoritative reads.

## 7. Schedule automatic renewal

Set a daily DSM root task:

```bash
cd /volume1/docker/solar-ev-charger
bash scripts/telemetry-cert.sh renew
```

Lego 5 uses `run` for both issuance and renewal; the Compose entrypoint includes
`run` before its certificate flags. The renewal script adds `--renew-days=30`
instead of the older `renew --days=30` command.

Lego renews certificates within 30 days of expiry. The script deploys the
certificate, restarts an already-running receiver only when its fingerprint
has not been recorded as loaded, and checks liveness after restart. A failed
restart leaves the fingerprint pending so the next run retries it.

Enable DSM task failure notifications and monitor certificate expiry and
receiver health independently. A certificate failure must not go unnoticed
merely because the app's fallback polling still works.

## Disable telemetry

Recreate the app using the original Compose files without the telemetry overlay,
or run it with `TESLA_TELEMETRY_MQTT_URL` unset outside this overlay. Its previous
polling behavior remains intact.

Remove this application's vehicle telemetry configuration explicitly before
permanently shutting down the receiver, so the car does not keep attempting to
connect. Stop only the telemetry receiver/broker services; do not run a broad
`down` against an unrelated project or delete certificate/account artifacts.

## Local validation

```bash
go test -race ./internal/telemetry ./internal/controller ./internal/config ./cmd/server ./scripts
go vet ./...
```

`TestConsumerMQTTIntegration` additionally accepts `TELEMETRY_TEST_BROKER` pointing
to an isolated test broker. Never point it at the production broker.

To check the ACME image's actual CLI without credentials or network access:

```bash
TELEMETRY_TEST_LEGO_CLI=1 go test ./scripts -run TestTelemetryLegoCLI -v
```

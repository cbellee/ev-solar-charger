# Fleet Telemetry on Synology

## Overview: why we need this

The app needs to notice promptly when you plug in the car, so it can start
charging when surplus solar is available. With the default polling settings,
it can take up to roughly 30 minutes to discover that change. Checking every
minute reduces the delay but increases paid Tesla API requests substantially.

Tesla's Fleet API does not provide a hosted webhook for plug-in and charging
events. Instead, **Fleet Telemetry sends updates directly from the car to a
receiver we operate**. The car does not send these events to Tesla for Tesla
to forward to our app.

In this setup, a car event means "something changed." The app then asks Tesla's
Fleet API to confirm the current charging state before acting. This provides
faster detection without constant one-minute polling, while retaining the
existing charging safety checks and slow background polling as a fallback.
Both streaming and confirmation requests still have costs.

That direct car-to-receiver connection explains the setup work:

- **Public DNS and port forwarding** let the car find and reach the receiver.
- **TLS certificates** secure the connection; mutual TLS authenticates the car.
- **A private MQTT broker** carries received events to the app.
- **Vehicle configuration in step 6** tells the car where to send updates,
  which certificate authorities to trust, and which signals to report.

The Fleet API remains responsible for configuration requests, charging
commands, and confirmation reads; it is not the event-delivery intermediary.

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

Separately from telemetry hints, each successful automatic or manual charge
start/stop queues one REST confirmation read on the next controller tick.
This prevents the pre-command cached state from reporting `monitoring` after
charging starts or repeatedly issuing stop commands after charging stops.
The read does not wait for `TESLA_TELEMETRY_REFRESH_MIN_SECONDS`; it is triggered
by the successful command, not by streamed data. Normal telemetry throttling,
command cooldowns, and polling intervals remain unchanged. Allow approximately
$0.002 per successful start/stop for this extra data read at the documented
rate; see [cost optimisation](cost-optimisation.md).

## 1. Prepare the NAS

Use the source/image built from the telemetry branch. An older GHCR `latest`
image will not contain the MQTT consumer.

Copy the new Compose overlay, [receiver configuration](../deploy/telemetry/receiver.json),
[broker configuration](../deploy/telemetry/mosquitto.conf), and
[certificate script](../scripts/telemetry-cert.sh) along with the project.
The deployment uses pinned receiver, broker, and ACME image versions.

From the Mac's repository directory, copy the prerequisites (replace the NAS
hostname if necessary):

```bash
NAS=chris@syn-nas-01
DEST=/volume1/docker/solar-ev-charger
ssh "$NAS" "mkdir -p '$DEST/deploy/telemetry' '$DEST/scripts'"
scp -O docker-compose.telemetry.yml "$NAS:$DEST/"
scp -O deploy/telemetry/receiver.json deploy/telemetry/mosquitto.conf \
  deploy/telemetry/vehicle-config.example.json "$NAS:$DEST/deploy/telemetry/"
scp -O scripts/telemetry-cert.sh "$NAS:$DEST/scripts/"
```

Keep the NAS's existing `.env` and secrets. For this branch, change the image in
the NAS's `docker-compose.ghcr.yml` to:

```yaml
services:
  solar-ev-charger:
    image: ghcr.io/cbellee/ev-solar-charger:feat-fleet-telemetry
```

Wait for the branch's GitHub container workflow to publish successfully before
pulling. Feature-branch images do not update `latest`.

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

For an existing directory and token file, on the NAS:

```bash
cd /volume1/docker/solar-ev-charger
sudo chown root:root data/telemetry data/telemetry/cloudflare-dns-api-token
sudo chmod 0700 data/telemetry
sudo chmod 0600 data/telemetry/cloudflare-dns-api-token
```

Do not use recursive chmod/chown: the deployed certificates need different
permissions, which the certificate script manages.

Only the ACME container receives this file, through
`CF_DNS_API_TOKEN_FILE`. DNS-01 issuance needs outbound HTTPS/DNS access, not
public inbound port 80. The token must remain available for automatic renewal.

## 3. Issue the dedicated certificate

Create a root-owned DSM scheduled task and initially run it manually:

1. Open **Control Panel -> Task Scheduler -> Create -> Scheduled Task ->
   User-defined script**.
2. Name it `Renew Tesla telemetry certificate`, select user **root**, and leave
   it disabled until the first manual run succeeds.
3. Set a daily schedule, for example 03:00.
4. Under **Task Settings -> User-defined script**, enter the following. Root
   tasks do not need sudo inside the script:

```bash
cd /volume1/docker/solar-ev-charger || exit 1
/bin/bash scripts/telemetry-cert.sh issue
```

5. Save, select the task, and click **Run**.
6. Enable abnormal-termination email notifications if DSM email notifications
   are configured. After issuance succeeds, replace `issue` with `renew` and
   enable the daily task (see section 7).

Select the task and use **Action -> View Result** to inspect status/output.
In Task Scheduler **Settings**, enable saving output results to a selected
shared folder if output is not retained. Alternatively, redirect the command's
output to a local file:

```bash
cd /volume1/docker/solar-ev-charger || exit 1
/bin/bash scripts/telemetry-cert.sh issue >> data/telemetry-cert.log 2>&1
```

Read this over SSH with:

```bash
sudo tail -n 100 /volume1/docker/solar-ev-charger/data/telemetry-cert.log
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

### Avoid the NAS port conflict

On this NAS, a Java service already listens on 8443. Leave it running. Change the
overlay's receiver mapping from `"8443:8443"` to `"18443:8443"`:

```bash
cd /volume1/docker/solar-ev-charger
sudo sed -i 's/"8443:8443"/"18443:8443"/' docker-compose.telemetry.yml
```

The resulting route is **public TCP 8443 -> NAS TCP 18443 -> container TCP
8443**. The vehicle configuration and certificate still use
`telemetry.ev.bellee.net:8443`. Reapply this local mapping after copying a fresh
overlay until the checked-in default is changed.

### Pull and run the branch image

On the NAS with Compose v1:

```bash
cd /volume1/docker/solar-ev-charger
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml \
  -f docker-compose.telemetry.yml pull
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml \
  -f docker-compose.telemetry.yml up -d --no-build
sudo docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml \
  -f docker-compose.telemetry.yml ps
curl --fail http://127.0.0.1:19091/status
curl --fail http://127.0.0.1:19090/metrics
```

If a bind mount is missing, ensure both configuration files were copied to
`deploy/telemetry/` on the NAS. If a port is already in use, identify its owner
with `sudo netstat -tlnp | grep ':8443'`; do not kill an unrelated service.

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

- Only public TCP 8443 is forwarded to the NAS's receiver port, 18443.
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

In Cloudflare, either:

- create a **CNAME** for `telemetry.ev.bellee.net` targeting the existing
  dynamic-DNS hostname that resolves to the home connection's current public IP;
  for the `bellee.net` zone, the relative record name is `telemetry.ev`; or
- create an **A record** pointing to the public IPv4 address and keep it updated.

Set the record to **DNS only** (grey cloud), not Cloudflare's HTTP proxy.
A CNAME at the telemetry hostname does not interfere with the separate DNS-01
TXT record `_acme-challenge.telemetry.ev.bellee.net`. Keep the certificate and
vehicle hostname unchanged; do not replace them with the CNAME target.
Do not create an AAAA record unless IPv6 routing/firewalling is also configured.

The NAS used here is `192.168.88.167`; substitute its individual LAN IP if
different. A subnet such as `192.168.88.0/24` is not a valid single NAS target.
Reserve the NAS's LAN address in DHCP.

For a new MikroTik rule (do not duplicate an existing telemetry rule):

```routeros
/ip firewall nat add chain=dstnat in-interface-list=WAN protocol=tcp dst-port=8443 \
    action=dst-nat to-addresses=192.168.88.167 to-ports=18443 \
    comment="Tesla Fleet Telemetry TCP 8443"
```

To update the earlier rule instead:

```routeros
/ip firewall nat set [find where comment="Tesla Fleet Telemetry TCP 8443"] \
    to-addresses=192.168.88.167 to-ports=18443
```

The rule matches the WAN interface, not a hardcoded external IP; dynamic public
IP changes do not require editing it. If its comment differs, select the
existing rule explicitly rather than assuming this find expression matched.

If the forward-chain policy does not already allow this destination NAT, add a
narrow allow rule before its drop rules:

```routeros
/ip firewall filter add chain=forward action=accept \
    in-interface-list=WAN connection-nat-state=dstnat \
    protocol=tcp dst-address=192.168.88.167 dst-port=18443 \
    place-before=0 comment="Allow Tesla telemetry to NAS"
```

Review the router's forward-chain policy and NAS firewall so this specific
forward is allowed. Allow NAS inbound TCP **18443**, not 18433. Do not disable
either firewall or expose DSM administration.
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

For a bounded connectivity check from outside the home LAN:

```bash
curl --noproxy '*' -4 -v --connect-timeout 5 --max-time 10 \
  https://telemetry.ev.bellee.net:8443/ -o /dev/null
```

A TCP timeout occurs before certificate validation. Check the receiver locally
on the NAS with `curl -v --connect-timeout 3 --max-time 5
https://127.0.0.1:18443/`. A local certificate-name error proves the listener is
reachable but does not validate its certificate. If local TCP works but external
TCP times out, inspect DNS, router NAT/filter rules, and the NAS firewall.
Test externally (for example via a hotspot) to avoid confusing hairpin NAT
behavior with a broken external forward.

## 6. Configure the vehicle explicitly

Do this only after the external TLS check in section 5 succeeds. A final
`certificate required` alert is expected without the car's client certificate.

The following is a **manual procedure**, not an unattended script. Run one
block at a time and inspect the indicated results. Do not paste credentials,
token responses, or the authorization file into chat. Do not enable shell
tracing (`set -x`), curl verbose mode, or automatic retries during token exchange.

### 6.1 Prepare a private root shell on the NAS

The examples use Compose v1, `jq` 1.5 or later, curl, OpenSSL, and Bash.
This procedure briefly stops only the app to avoid two processes rotating the
same single-use Tesla refresh token. **Stopping the app can stop an active
charging session**; choose a suitable time. The receiver, broker, and command
proxy stay running.

Open a root shell, then Bash:

```bash
sudo -i
/bin/bash
```

In that same Bash session, run all remaining blocks:

```bash
set +x
set -euo pipefail
umask 077
cd /volume1/docker/solar-ev-charger
jq --version
docker pull curlimages/curl:8.17.0

compose() {
  docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml \
    -f docker-compose.telemetry.yml "$@"
}
APP=$(compose ps -q solar-ev-charger)
PROXY=$(compose ps -q tesla-http-proxy)
test -n "$APP"
test -n "$PROXY"
test "$(docker inspect -f '{{.State.Running}}' "$APP")" = true
test "$(docker inspect -f '{{.State.Running}}' "$PROXY")" = true

container_env() {
  docker inspect "$APP" | jq -jr --arg name "$1" \
    '.[0].Config.Env[] | select(startswith($name + "=")) | .[($name | length) + 1:]'
}

VIN=$(container_env TESLA_VIN)
test -n "$VIN"
TOKEN_PATH=$(container_env TESLA_TOKEN_PATH)
TOKEN_PATH=${TOKEN_PATH:-/data/tesla-refresh-token}
REGION=$(container_env TESLA_REGION)
case "${REGION:-na}" in
  na) API=https://fleet-api.prd.na.vn.cloud.tesla.com ;;
  eu) API=https://fleet-api.prd.eu.vn.cloud.tesla.com ;;
  cn) API=https://fleet-api.prd.cn.vn.cloud.tesla.com ;;
  *) echo "Unsupported TESLA_REGION; stop and check the app configuration"; exit 1 ;;
esac
test "$(container_env TESLA_TEST_MODE)" != true

WORK=$(mktemp -d "$PWD/data/telemetry/provision.XXXXXX")
printf 'Private working directory: %s\n' "$WORK"
container_env TESLA_CLIENT_ID > "$WORK/client-id"
container_env TESLA_CLIENT_SECRET > "$WORK/client-secret"
test -s "$WORK/client-id"
test -s "$WORK/client-secret"
```

Do not print a full `docker inspect` or source `.env` as shell code.
The helper above extracts only named values. The client credentials are written
directly to private local files, not command arguments or terminal output.
If any block fails under `set -e`, Bash exits: stop and resolve the error rather
than blindly continuing. Keep the private working directory path for recovery.
If the prompt returns to the NAS's default `ash` shell, the Bash variables and
functions are lost. Before section 6.3, fix the error, open `/bin/bash` again,
and rerun sections 6.1 and 6.2. If token exchange has already started, follow
the recovery instructions in section 6.3 instead of repeating it.
Section 6.7 shows how to restore a session without exchanging tokens.

### 6.2 Build and verify the receiver CA bundle

The issued server bundle is not the fleet private key. Use the ACME issuer
chain plus the public ISRG Root X1 certificate to verify the chain served in
this deployment (YE1 -> Root YE -> cross-signed Root X2 -> Root X1).

```bash
cd /volume1/docker/solar-ev-charger
: "${WORK:?Complete section 6.1 in this same Bash session first}"
test -d "$WORK"
test -s deploy/telemetry/vehicle-config.example.json || {
  echo "Missing vehicle-config.example.json: copy it from the Mac using section 1 before continuing." >&2
  exit 1
}
ISSUER="$PWD/data/telemetry/acme/certificates/telemetry.ev.bellee.net.issuer.crt"
test -s "$ISSUER"
curl --fail --silent --show-error --connect-timeout 10 --max-time 30 \
  https://letsencrypt.org/certs/isrgrootx1.pem -o "$WORK/isrg-root-x1.pem"
cat "$ISSUER" "$WORK/isrg-root-x1.pem" > "$WORK/receiver-ca.pem"
openssl verify -verify_hostname telemetry.ev.bellee.net \
  -CAfile "$WORK/receiver-ca.pem" \
  -untrusted data/telemetry/tls/tls.crt data/telemetry/tls/tls.crt

jq -Rs --arg vin "$VIN" \
  --slurpfile template deploy/telemetry/vehicle-config.example.json \
  '. as $ca | $template[0] | .vins = [$vin] | .config.ca = $ca' \
  < "$WORK/receiver-ca.pem" > "$WORK/vehicle-config.json"
jq '.config | {hostname, port, fields}' "$WORK/vehicle-config.json"
```

The verification must report `OK`. If a future issuance uses a different
issuer chain, stop and construct its matching CA bundle; do not disable
verification or pin a short-lived leaf certificate to get past the check.
The printed request summary should show public port **8443**, not NAS port
18443. This step only creates the request; it does not configure the car.
The `-Rs` and `--slurpfile` combination works with the NAS's jq 1.5 and preserves
the multiline PEM bundle. Do not substitute `--rawfile`, which jq 1.5 lacks.

### 6.3 Obtain an access token without losing the app's refresh token

Use the **current saved token**, not an old `TESLA_REFRESH_TOKEN` from `.env`.
First stop the app and copy its saved token. Resolve the runtime account from
the app image rather than trusting ownership exported by `docker cp` on the NAS:

```bash
compose stop solar-ev-charger
docker cp -a "$APP:$TOKEN_PATH" "$WORK/current-refresh-token"
test -s "$WORK/current-refresh-token"
APP_USER=$(docker inspect -f '{{.Config.User}}' "$APP")
test "$APP_USER" = nonroot:nonroot || {
  echo "Unexpected app user: resolve its numeric UID:GID before continuing." >&2
  exit 1
}
docker cp "$APP:/etc/passwd" "$WORK/app-passwd"
TOKEN_OWNER=$(awk -F: '$1 == "nonroot" {print $3 ":" $4}' "$WORK/app-passwd")
test -n "$TOKEN_OWNER"

curl --fail --silent --show-error --connect-timeout 10 --max-time 60 \
  --data-urlencode grant_type=refresh_token \
  --data-urlencode "client_id@$WORK/client-id" \
  --data-urlencode "client_secret@$WORK/client-secret" \
  --data-urlencode "refresh_token@$WORK/current-refresh-token" \
  https://fleet-auth.prd.vn.cloud.tesla.com/oauth2/v3/token \
  -o "$WORK/token-response.json"

jq -er '.refresh_token | select(type == "string" and length > 0)' \
  "$WORK/token-response.json" > "$WORK/replacement-refresh-token"
chmod 0600 "$WORK/replacement-refresh-token"
docker run --rm --user 0:0 --network none --read-only \
  --volumes-from "$APP" -v "$WORK:/work:ro" \
  --entrypoint /bin/sh curlimages/curl:8.17.0 -c '
    set -eu
    test -s /work/replacement-refresh-token
    tmp=$(mktemp "$1.restore.XXXXXX")
    cp /work/replacement-refresh-token "$tmp"
    chmod 0600 "$tmp"
    chown "$2" "$tmp"
    cmp /work/replacement-refresh-token "$tmp"
    mv -f "$tmp" "$1"
  ' sh "$TOKEN_PATH" "$TOKEN_OWNER"

docker run --rm --user "$TOKEN_OWNER" --cap-drop ALL \
  --network none --read-only --volumes-from "$APP" \
  --entrypoint /bin/sh curlimages/curl:8.17.0 \
  -c 'test -s "$1" && test -r "$1" && test -w "$1"' sh "$TOKEN_PATH"

jq -er '.access_token | select(type == "string" and length > 0) |
  "Authorization: Bearer " + .' \
  "$WORK/token-response.json" > "$WORK/authorization"
compose start solar-ev-charger
```

**If the exchange, token restoration, or restart fails, stop here.** Keep the
private response and replacement-token files until recovery is complete.
Do not repeat a timed-out exchange blindly: Tesla may already have consumed the
old refresh token. Restore a returned replacement before starting the app; if
the replacement is lost, use the app's `/auth/tesla` bootstrap flow to reconnect.
Never delete the app's token volume or fall back to an old `.env` token.
The app image is distroless: copying into it with `docker cp -a` can fail with
`unable to find getent command`. The helper above instead writes atomically
through the stopped app's mounted volume. If this happens after a successful
exchange, reopen Bash, restore the original `WORK` path and container variables,
and resume at replacement-token extraction, not at the exchange. Recreate the
authorization file and restart the app only after token restoration succeeds.
Check the app is running again:

```bash
compose ps solar-ev-charger
compose logs --since=2m --tail=30 solar-ev-charger
```

Look for `tesla refresh token loaded`, `tesla: token refreshed`, and
`tesla: rotated refresh token saved` in the latest startup. Old log entries may
still show errors from before recovery. `Up (health: starting)` alone does not
prove Tesla authentication works.

If the latest startup reports `permission denied` for the token, use this
permission-only recovery block. If the shell was lost, restore it using section
6.7 first. This resolves the runtime account again and repairs only the existing
file's permissions:

```bash
compose stop solar-ev-charger
APP_USER=$(docker inspect -f '{{.Config.User}}' "$APP")
test "$APP_USER" = nonroot:nonroot || {
  echo "Unexpected app user: resolve its numeric UID:GID before continuing." >&2
  exit 1
}
docker cp "$APP:/etc/passwd" "$WORK/app-passwd"
TOKEN_OWNER=$(awk -F: '$1 == "nonroot" {print $3 ":" $4}' "$WORK/app-passwd")
test -n "$TOKEN_OWNER"
docker run --rm --user 0:0 --network none --read-only \
  --volumes-from "$APP" --entrypoint /bin/sh curlimages/curl:8.17.0 \
  -c 'set -eu; chown "$2" "$1"; chmod 0600 "$1"' \
  sh "$TOKEN_PATH" "$TOKEN_OWNER"

docker run --rm --user "$TOKEN_OWNER" --cap-drop ALL \
  --network none --read-only --volumes-from "$APP" \
  --entrypoint /bin/sh curlimages/curl:8.17.0 \
  -c 'test -s "$1" && test -r "$1" && test -w "$1"' sh "$TOKEN_PATH"
compose start solar-ev-charger
compose logs --since=2m --tail=30 solar-ev-charger
```

Do not overwrite the existing token with an older recovery token: the app may
already have rotated the restored token. A successful refresh and save means
the old recovery refresh token must no longer be used.

The access token is short-lived; complete the next steps promptly. The app can
resume managing its newly saved refresh token independently.

### 6.4 Check pairing and existing configuration

Define helpers that read the bearer header from its private file. The proxy
helper shares the running proxy container's network namespace so no new port
needs publishing. Only its public TLS certificate is mounted, not its fleet key.
Stay in the same Bash session: opening another `/bin/bash` does not restore
these variables. If they are lost after token exchange, restore the original
`WORK` directory, container IDs, VIN, and regional API URL without repeating
the exchange or replacing the app's current refresh token.

```bash
: "${WORK:?Restore the original private working directory in this session}"
: "${PROXY:?Restore the running tesla-http-proxy container ID in this session}"
: "${VIN:?Restore TESLA_VIN from the app container in this session}"
: "${API:?Restore the regional Fleet API URL in this session}"
test -s "$WORK/authorization"
test -s "$WORK/vehicle-config.json"
test -s "$PWD/secrets/proxy-tls-cert.pem"

fleet_request() {
  docker run --rm --user 0:0 --cap-drop ALL --read-only \
    --network "container:$PROXY" -v "$WORK:/work:ro" \
    curlimages/curl:8.17.0 \
    --fail --silent --show-error --connect-timeout 10 --max-time 60 \
    --header @/work/authorization "$@"
}
proxy_request() {
  docker run --rm --user 0:0 --cap-drop ALL --read-only \
    --network "container:$PROXY" \
    -v "$WORK:/work:ro" \
    -v "$PWD/secrets/proxy-tls-cert.pem:/proxy-ca.pem:ro" \
    curlimages/curl:8.17.0 \
    --fail --silent --show-error --connect-timeout 10 --max-time 60 \
    --cacert /proxy-ca.pem \
    --resolve tesla-http-proxy:4443:127.0.0.1 \
    --header @/work/authorization "$@"
}

jq -n --arg vin "$VIN" '{vins: [$vin]}' > "$WORK/fleet-status-request.json"
fleet_request -H 'Content-Type: application/json' \
  --data-binary @/work/fleet-status-request.json \
  "$API/api/1/vehicles/fleet_status" > "$WORK/fleet-status.json"
jq '.response' "$WORK/fleet-status.json"
```

Verify the VIN appears in `key_paired_vins` and inspect `vehicle_info[VIN]`.
Check for an actual telemetry client version, supported hardware/firmware, and
any applicable in-car **Allow Third-Party App Data Streaming** consent.
`DetailedChargeState` requires firmware 2024.38 or newer. If the key is not
paired, pair this application's existing public key through the Tesla mobile
app using the developer-domain link already used for command setup. Do not
create a different fleet key.

Read this application's current telemetry configuration:

```bash
CONFIG_URL="$API/api/1/vehicles/$VIN/fleet_telemetry_config"
CONFIG_HTTP=$(fleet_request --no-fail --output /dev/null \
  --write-out '%{http_code}' "$CONFIG_URL")
case "$CONFIG_HTTP" in
  200)
    fleet_request "$CONFIG_URL" > "$WORK/config-before.json"
    jq '.response | del(.config.ca)' "$WORK/config-before.json"
    ;;
  404) echo "No existing configuration found; review capability checks before continuing." ;;
  *) echo "Unexpected configuration-check HTTP status: $CONFIG_HTTP"; exit 1 ;;
esac
```

An explicit not-configured response is expected on first setup; the block
distinguishes HTTP 404 from other failures rather than treating every failed
request as proof that no configuration exists. Resolve 401/403 or other
unexpected errors before proceeding. If a configuration exists, inspect it: the next
request may replace this application's existing field selection and destination.
HTTP 200 with `config: null` also means no configuration is installed for this
application; `synced: true` alongside `config: null` does not mean streaming
is active. `key_paired: true` and `limit_reached: false` are useful prerequisite
checks, not evidence that the car is sending events.

### 6.5 Submit only after reviewing the request

The request below creates/updates the car's telemetry configuration and can
incur streaming charges. It requests only the two charging-state hints.
The proxy signs the request with the existing fleet key, following Tesla's
[documented proxy procedure](https://github.com/teslamotors/vehicle-command/tree/main/cmd/tesla-jws#using-tesla-http-proxy).
Run the entire block, including the `if` body. Typing `yes` at the prompt only
sets a variable; it does not submit anything by itself. A subsequent
`fleet_request` GET only reads configuration.

```bash
read -r -p "Submit the reviewed telemetry configuration to the car? Type yes: " CONFIRM
if [[ "$CONFIRM" == yes ]]; then
  proxy_request -H 'Content-Type: application/json' \
    --data-binary @/work/vehicle-config.json \
    https://tesla-http-proxy:4443/api/1/vehicles/fleet_telemetry_config \
    > "$WORK/config-submit.json"
  jq . "$WORK/config-submit.json"
else
  echo "Not submitted."
fi
```

Inspect the entire response, including `skipped_vehicles`; HTTP success alone
does not prove acceptance. Stop if your VIN was skipped or an error is reported.
For this single-VIN request, `{"response":{"updated_vehicles":1}}` confirms
the update was accepted. It does not prove vehicle adoption or event delivery.
If `config-submit.json` is missing, submission has not been demonstrated.
If it is empty after a request error, check the current configuration before
retrying: the server may have accepted the update before the response was lost.
The app and command proxy can remain running throughout configuration.

### 6.6 Verify adoption and actual plug/unplug events

```bash
fleet_request "$API/api/1/vehicles/$VIN/fleet_telemetry_config" \
  > "$WORK/config-after.json"
jq '.response | del(.config.ca)' "$WORK/config-after.json"
fleet_request "$API/api/1/vehicles/$VIN/fleet_telemetry_errors" \
  > "$WORK/telemetry-errors.json"
jq '.response' "$WORK/telemetry-errors.json"
```

Require a non-null `config` with the expected hostname, public port 8443, and
both requested fields, as well as `synced: true`. This confirms adoption, not
an active receiver connection. If pending, let the car connect and repeat the config
GET after 30-60 seconds rather than repeatedly submitting the POST. Inspect
telemetry errors for connection/CA problems. Do not repeatedly wake the car to
force adoption. An expired access token produces 401; do not reuse an old
refresh token if a new authentication session is needed.
An empty `fleet_telemetry_errors` array alone does not prove event delivery.

Then check the local services:

```bash
compose logs --since=5m --tail=50 fleet-telemetry
compose logs --since=5m --tail=30 solar-ev-charger
curl --fail http://127.0.0.1:19090/metrics
curl --fail --silent --show-error http://127.0.0.1:19090/metrics \
  | grep '^num_connections '
```

The app should log `telemetry MQTT subscribed`. That proves subscription, not
vehicle delivery. `num_connections` is the number of active connections at the
instant of the scrape, not a historical connection count. A car can connect
briefly and disconnect, leaving this value at zero.

Receiver logs containing `socket_connected` with a
`Hermes/... (vehicle_device)` user agent and `network_interface: cellular`
demonstrate a vehicle connection through the public network and mutual TLS
path. A later `socket_disconnected` explains a zero connection gauge but does
not, by itself, explain why the connection ended. During this setup, a vehicle
connected for about five seconds; that was evidence of connectivity, not a
completed plug/unplug test. Earlier TLS errors from manual clients are not
evidence of a new vehicle failure; compare their timestamps.

If there is no connection, let the car wake normally, or open a door when
convenient, wait 30-60 seconds, and check recent receiver logs and the Fleet API
telemetry errors. Do not change certificates or resubmit an already-synced
configuration solely because the connection gauge is zero.

Finally, plug/unplug the car and verify receiver connection/signal
metrics and the dashboard's **REST-confirmed** plug state. Allow the configured
event refresh interval plus one controller tick and API latency. Keep existing
background polling enabled. Recheck streaming after certificate issuer changes.

These are separate milestones: accepted submission, adopted configuration,
vehicle connection, and a real event followed by a REST-confirmed dashboard
update. Do not mark the last milestone complete based on the first three.

After confirming the app is healthy, recovery is no longer needed, and the
plug/unplug test is complete,
remove only the temporary credential files (the working directory is retained
for non-secret request/response troubleshooting):

```bash
rm "$WORK/client-id" "$WORK/client-secret" "$WORK/current-refresh-token" \
  "$WORK/replacement-refresh-token" "$WORK/token-response.json" "$WORK/authorization"
unset TOKEN_OWNER
```

Do not remove the ACME account, Cloudflare token, deployed certificates, or app
token volume. Do not run these cleanup commands while token recovery is pending.

The template requests only two change-oriented fields, not battery/current
streaming or location data. This first mode only needs hints; subscribing to
more fields would add cost without replacing the app's authoritative reads.

### 6.7 Pause or resume safely

The containers and DSM renewal task run independently of SSH. You can close the
session and test plug/unplug another day after confirming the app has restarted
and refreshed its token. Keep the private recovery directory until recovery and
testing are complete. Closing SSH does not stop telemetry charges if the car is
streaming.

Do not repeat section 6.3 just to recover lost shell variables. Open a root
Bash session as in section 6.1, then run this block, replacing
`provision.REPLACE_ME` with the exact private directory printed during setup.
Do not select a directory just because it is the newest: use the one containing
the response and configuration for this attempt.

```bash
set +x
set -euo pipefail
umask 077
cd /volume1/docker/solar-ev-charger
WORK="$PWD/data/telemetry/provision.REPLACE_ME"
test -d "$WORK"
test -s "$WORK/vehicle-config.json"

compose() {
  docker-compose -f docker-compose.yml -f docker-compose.ghcr.yml \
    -f docker-compose.telemetry.yml "$@"
}
APP=$(compose ps -q solar-ev-charger)
PROXY=$(compose ps -q tesla-http-proxy)
test -n "$APP"
test -n "$PROXY"
test "$(docker inspect -f '{{.State.Running}}' "$PROXY")" = true

container_env() {
  docker inspect "$APP" | jq -jr --arg name "$1" \
    '.[0].Config.Env[] | select(startswith($name + "=")) | .[($name | length) + 1:]'
}
VIN=$(container_env TESLA_VIN)
test -n "$VIN"
TOKEN_PATH=$(container_env TESLA_TOKEN_PATH)
TOKEN_PATH=${TOKEN_PATH:-/data/tesla-refresh-token}
REGION=$(container_env TESLA_REGION)
case "${REGION:-na}" in
  na) API=https://fleet-api.prd.na.vn.cloud.tesla.com ;;
  eu) API=https://fleet-api.prd.eu.vn.cloud.tesla.com ;;
  cn) API=https://fleet-api.prd.cn.vn.cloud.tesla.com ;;
  *) echo "Unsupported TESLA_REGION"; exit 1 ;;
esac
CONFIG_URL="$API/api/1/vehicles/$VIN/fleet_telemetry_config"
```

This restores session context only; it does not exchange tokens, write to the
app's token volume, restart services, or submit vehicle configuration. To make
authenticated API checks, rerun the helper definitions in section 6.4 after
checking that `$WORK/authorization` is present and still valid. The section 6.6
local log/metrics checks do not need an access token.

The saved access token may expire before the next day. If an API request
returns 401, do not extract or restore an old refresh token from
`token-response.json`. A new access-token exchange must use the app's
**current saved token**, with the app stopped as in section 6.3. Local
receiver checks and a dashboard plug/unplug test do not require another manual
exchange.

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

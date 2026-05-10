# Fleet Telemetry Compose Profile

This repo now includes an optional Docker Compose profile that runs:

- `mosquitto` as the Fleet Telemetry MQTT backend
- `fleet-telemetry` as Tesla's reference telemetry server

Enable it with:

```sh
docker compose --profile fleet-telemetry up -d
```

## Required secrets

Place these files under `./secrets/fleet-telemetry/` before starting the `fleet-telemetry` service:

- `vehicle_device.CA.cert`
- `vehicle_device.app.cert`
- `vehicle_device.app.key`

These are the CA, server certificate, and server key used by the Fleet Telemetry server.

## Charger app settings

Set these values in `.env` when you want the charger app to consume the MQTT stream:

```env
FLEET_TELEMETRY_MQTT_BROKER=tcp://mosquitto:1883
FLEET_TELEMETRY_MQTT_TOPIC_BASE=telemetry
FLEET_TELEMETRY_STALE_AFTER_SECONDS=180
```

The app will then subscribe to MQTT, update the controller cache from the telemetry stream, and fall back to paid Tesla polling when the stream goes stale.

## Ports

- `4443`: Fleet Telemetry TLS listener
- `18080`: Fleet Telemetry status port
- `19090`: Fleet Telemetry Prometheus metrics
- `1883`: MQTT broker

## Notes

- The profile is opt-in so the current non-telemetry stack keeps working unchanged.
- The Tesla vehicle must still be configured to stream to this Fleet Telemetry server.
- The server config is in `deploy/fleet-telemetry/config.json`.
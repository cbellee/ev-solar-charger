#!/usr/bin/env bash
set -euo pipefail
umask 077

cd "$(dirname "$0")/.."
action="${1:-}"
if [[ "$action" != issue && "$action" != renew ]]; then
  echo "usage: bash scripts/telemetry-cert.sh issue|renew" >&2
  exit 1
fi
if [[ "$(id -u)" != 0 ]]; then
  echo "error: run this script as root using a Synology scheduled task" >&2
  exit 1
fi
for tool in docker openssl curl; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "error: required tool not found: $tool" >&2
    exit 1
  fi
done

compose() {
  if docker compose version >/dev/null 2>&1; then
    docker compose -f docker-compose.yml -f docker-compose.telemetry.yml "$@"
  else
    docker-compose -f docker-compose.yml -f docker-compose.telemetry.yml "$@"
  fi
}

state_dir="$PWD/data/telemetry"
mkdir -p "$state_dir"
lock_dir="$state_dir/cert-renewal.lock"
if ! mkdir "$lock_dir" 2>/dev/null; then
  echo "error: certificate task already running, or stale lock: $lock_dir" >&2
  exit 1
fi
trap 'rmdir "$lock_dir"' EXIT

token="$state_dir/cloudflare-dns-api-token"
if [[ ! -s "$token" ]]; then
  echo "error: create a nonempty data/telemetry/cloudflare-dns-api-token file first" >&2
  exit 1
fi
chmod 600 "$token"
mkdir -p "$state_dir/acme"
chmod 700 "$state_dir/acme"

if [[ "$action" == issue ]]; then
  compose run --rm --no-deps telemetry-acme
else
  compose run --rm --no-deps telemetry-acme --renew-days=30
fi

source_dir="$state_dir/acme/certificates"
cert="$source_dir/telemetry.ev.bellee.net.crt"
key="$source_dir/telemetry.ev.bellee.net.key"
openssl x509 -in "$cert" -checkend 86400 -noout
cert_public=$(openssl x509 -in "$cert" -pubkey -noout)
key_public=$(openssl pkey -in "$key" -pubout)
if [[ "$cert_public" != "$key_public" ]]; then
  echo "error: issued certificate and key do not match" >&2
  exit 1
fi

tls_dir="$state_dir/tls"
mkdir -p "$tls_dir"
chown 0:65532 "$tls_dir"
chmod 750 "$tls_dir"
cp "$cert" "$tls_dir/tls.crt.next"
cp "$key" "$tls_dir/tls.key.next"
chown 0:65532 "$tls_dir/tls.crt.next" "$tls_dir/tls.key.next"
chmod 640 "$tls_dir/tls.crt.next" "$tls_dir/tls.key.next"
mv "$tls_dir/tls.crt.next" "$tls_dir/tls.crt"
mv "$tls_dir/tls.key.next" "$tls_dir/tls.key"

fingerprint=$(openssl x509 -in "$cert" -noout -fingerprint -sha256)
loaded=""
marker="$state_dir/receiver-cert-fingerprint"
if [[ -f "$marker" ]]; then
  loaded=$(<"$marker")
fi
container=$(compose ps -q fleet-telemetry)
if [[ -n "$container" && "$fingerprint" != "$loaded" ]]; then
  running=$(docker inspect -f '{{.State.Running}}' "$container")
  if [[ "$running" == true ]]; then
    compose restart fleet-telemetry
    ready=false
    for attempt in {1..15}; do
      if curl --fail --silent --max-time 2 http://127.0.0.1:19091/status >/dev/null; then
        ready=true
        break
      fi
      sleep 2
    done
    if [[ "$ready" != true ]]; then
      echo "error: receiver did not recover after certificate reload; inspect its logs" >&2
      exit 1
    fi
    printf '%s\n' "$fingerprint" > "$marker"
  fi
fi
echo "Telemetry certificate task completed. Receiver status alone does not prove vehicle connectivity."

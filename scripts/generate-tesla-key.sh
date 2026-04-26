#!/usr/bin/env bash
#
# Generate a Tesla Fleet API EC P-256 keypair for command signing and
# domain verification.
#
# Outputs:
#   <SECRETS_DIR>/fleet-key.pem                       (private key, chmod 600)
#   <SECRETS_DIR>/com.tesla.3p.public-key.pem         (public key)
#
# Environment variables:
#   SECRETS_DIR  Directory to write the keys to (default: ./secrets)
#   FORCE        Set to 1 to overwrite existing files
#
# Usage:
#   ./scripts/generate-tesla-key.sh
#   SECRETS_DIR=/volume1/docker/solar-ev-charger/secrets ./scripts/generate-tesla-key.sh
#
set -euo pipefail

SECRETS_DIR="${SECRETS_DIR:-./secrets}"
FORCE="${FORCE:-0}"

PRIVATE_KEY="${SECRETS_DIR}/fleet-key.pem"
PUBLIC_KEY="${SECRETS_DIR}/com.tesla.3p.public-key.pem"

if ! command -v openssl >/dev/null 2>&1; then
  echo "error: openssl is required but not found in PATH" >&2
  exit 1
fi

mkdir -p "$SECRETS_DIR"

if [[ -e "$PRIVATE_KEY" || -e "$PUBLIC_KEY" ]]; then
  if [[ "$FORCE" != "1" ]]; then
    echo "error: key files already exist in $SECRETS_DIR" >&2
    echo "       set FORCE=1 to overwrite" >&2
    exit 1
  fi
  echo "FORCE=1 set; overwriting existing keys"
  rm -f "$PRIVATE_KEY" "$PUBLIC_KEY"
fi

umask 077

echo "Generating EC P-256 private key -> $PRIVATE_KEY"
openssl ecparam -name prime256v1 -genkey -noout -out "$PRIVATE_KEY"
chmod 600 "$PRIVATE_KEY"

echo "Deriving public key -> $PUBLIC_KEY"
openssl ec -in "$PRIVATE_KEY" -pubout -out "$PUBLIC_KEY" 2>/dev/null
chmod 644 "$PUBLIC_KEY"

echo
echo "Private key fingerprint:"
openssl ec -in "$PRIVATE_KEY" -pubout -outform DER 2>/dev/null | openssl dgst -sha256

echo
echo "Done."
echo "  private: $PRIVATE_KEY (chmod 600)"
echo "  public : $PUBLIC_KEY"
echo
echo "Next steps:"
echo "  1. Mount $SECRETS_DIR into the container at /secrets (read-only)."
echo "  2. Set TESLA_PRIVATE_KEY_PATH=/secrets/fleet-key.pem in .env."
echo "  3. Set TESLA_PUBLIC_KEY_PEM_PATH=/secrets/com.tesla.3p.public-key.pem in .env."
echo "  4. Verify the public key URL is reachable over HTTPS:"
echo "       https://<your-domain>/.well-known/appspecific/com.tesla.3p.public-key.pem"
echo "  5. Pair the virtual key to your vehicle via:"
echo "       https://tesla.com/_ak/<your-domain>"

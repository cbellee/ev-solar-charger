#!/usr/bin/env bash
set -euo pipefail

# Configures MikroTik dst-nat rules for the solar-ev-charger TLS setup.
#
# Required environment variables:
#   ROUTER_HOST   - MikroTik management IP or DNS name
#   ROUTER_USER   - MikroTik username with firewall write permissions
#   LAN_HOST      - Internal IP of Docker host running solar-ev-charger
#
# Optional environment variables:
#   ROUTER_PORT            - SSH port (default: 22)
#   ROUTER_IDENTITY_FILE   - SSH private key file path
#   RULE_COMMENT_PREFIX    - Prefix used for managed rule comments (default: solar-ev-charger)
#   PUBLIC_HTTP_PORT       - Public HTTP port to forward (default: 80)
#   PUBLIC_HTTPS_PORT      - Public HTTPS port to forward (default: 443)
#   HTTP_CHALLENGE_PORT    - Internal app HTTP challenge port (default: 8081)
#   HTTPS_APP_PORT         - Internal app HTTPS port (default: 8443)
#   DRY_RUN                - Set to 1 to print commands without executing

ROUTER_HOST="${ROUTER_HOST:-}"
ROUTER_USER="${ROUTER_USER:-}"
LAN_HOST="${LAN_HOST:-}"

ROUTER_PORT="${ROUTER_PORT:-22}"
ROUTER_IDENTITY_FILE="${ROUTER_IDENTITY_FILE:-}"
RULE_COMMENT_PREFIX="${RULE_COMMENT_PREFIX:-solar-ev-charger}"
PUBLIC_HTTP_PORT="${PUBLIC_HTTP_PORT:-80}"
PUBLIC_HTTPS_PORT="${PUBLIC_HTTPS_PORT:-443}"
HTTP_CHALLENGE_PORT="${HTTP_CHALLENGE_PORT:-8081}"
HTTPS_APP_PORT="${HTTPS_APP_PORT:-8443}"
DRY_RUN="${DRY_RUN:-0}"

if [[ -z "$ROUTER_HOST" || -z "$ROUTER_USER" || -z "$LAN_HOST" ]]; then
  cat <<'USAGE' >&2
Missing required variables.

Required:
  ROUTER_HOST=<mikrotik-host>
  ROUTER_USER=<mikrotik-user>
  LAN_HOST=<docker-host-lan-ip>

Example:
  ROUTER_HOST=192.168.88.1 \
  ROUTER_USER=admin \
  LAN_HOST=192.168.88.50 \
  ROUTER_IDENTITY_FILE=~/.ssh/id_ed25519 \
  ./mikrotik_port_forward.sh
USAGE
  exit 1
fi

if ! [[ "$PUBLIC_HTTP_PORT" =~ ^[0-9]+$ && "$PUBLIC_HTTPS_PORT" =~ ^[0-9]+$ && "$HTTP_CHALLENGE_PORT" =~ ^[0-9]+$ && "$HTTPS_APP_PORT" =~ ^[0-9]+$ ]]; then
  echo "All ports must be numeric." >&2
  exit 1
fi

ssh_opts=("-p" "$ROUTER_PORT" "-o" "StrictHostKeyChecking=accept-new")
if [[ -n "$ROUTER_IDENTITY_FILE" ]]; then
  ssh_opts+=("-i" "$ROUTER_IDENTITY_FILE")
fi

router_script=$(cat <<EOS
:local prefix "$RULE_COMMENT_PREFIX";
:local lanHost "$LAN_HOST";
:local pubHttp "$PUBLIC_HTTP_PORT";
:local pubHttps "$PUBLIC_HTTPS_PORT";
:local dstHttp "$HTTP_CHALLENGE_PORT";
:local dstHttps "$HTTPS_APP_PORT";

/ip firewall nat remove [find where comment~("^" . \$prefix)];

/ip firewall nat add chain=dstnat action=dst-nat protocol=tcp dst-port=\$pubHttp to-addresses=\$lanHost to-ports=\$dstHttp comment=(\$prefix . ":http-challenge");
/ip firewall nat add chain=dstnat action=dst-nat protocol=tcp dst-port=\$pubHttps to-addresses=\$lanHost to-ports=\$dstHttps comment=(\$prefix . ":https");

/ip firewall filter remove [find where comment~("^" . \$prefix)];
/ip firewall filter add chain=forward action=accept connection-nat-state=dstnat protocol=tcp dst-port=\$dstHttp comment=(\$prefix . ":allow-http-forward");
/ip firewall filter add chain=forward action=accept connection-nat-state=dstnat protocol=tcp dst-port=\$dstHttps comment=(\$prefix . ":allow-https-forward");

:put ("Applied NAT forwards to " . \$lanHost . " (" . \$pubHttp . "->" . \$dstHttp . ", " . \$pubHttps . "->" . \$dstHttps . ")");
EOS
)

if [[ "$DRY_RUN" == "1" ]]; then
  echo "DRY_RUN=1; would run on $ROUTER_USER@$ROUTER_HOST:$ROUTER_PORT"
  echo
  echo "$router_script"
  exit 0
fi

echo "Applying MikroTik NAT and forward rules on $ROUTER_USER@$ROUTER_HOST ..."
ssh "${ssh_opts[@]}" "$ROUTER_USER@$ROUTER_HOST" "$router_script"

echo "Done. Verify with:"
echo "  /ip firewall nat print where comment~\"$RULE_COMMENT_PREFIX\""
echo "  /ip firewall filter print where comment~\"$RULE_COMMENT_PREFIX\""
#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CERT_FILE="$SCRIPT_DIR/dataForwardingService/certs/cert.pem"
KEY_FILE="$SCRIPT_DIR/dataForwardingService/certs/key.pem"

# Daily deployment keeps every product feature; diagnostics and replicas are opt-in.
ARGS=(standard)
if [[ $# -gt 0 && "$1" != -* ]]; then
  ARGS=("$1")
  shift
fi
if [[ ! -s "$CERT_FILE" || ! -s "$KEY_FILE" ]]; then
  ARGS+=(--cert)
fi

exec "$SCRIPT_DIR/deploy_docker_compose.sh" "${ARGS[@]}" "$@"

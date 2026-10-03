#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CERT_FILE="$SCRIPT_DIR/dataForwardingService/certs/cert.pem"
KEY_FILE="$SCRIPT_DIR/dataForwardingService/certs/key.pem"

# 全量测试部署包含群通话；LiveKit 的地址和密钥由 services/.env 提供。
ARGS=(full --enable group-calls --proto)
if [[ ! -s "$CERT_FILE" || ! -s "$KEY_FILE" ]]; then
  ARGS+=(--cert)
fi

exec "$SCRIPT_DIR/deploy_docker_compose.sh" "${ARGS[@]}" "$@"

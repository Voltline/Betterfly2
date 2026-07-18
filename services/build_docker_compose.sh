#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CERT_FILE="$SCRIPT_DIR/dataForwardingService/certs/cert.pem"
KEY_FILE="$SCRIPT_DIR/dataForwardingService/certs/key.pem"

# 保持历史脚本的全量部署范围，但不再主动破坏镜像和容器缓存。
ARGS=(full --proto)
if [[ ! -s "$CERT_FILE" || ! -s "$KEY_FILE" ]]; then
  ARGS+=(--cert)
fi

exec "$SCRIPT_DIR/deploy_docker_compose.sh" "${ARGS[@]}" "$@"

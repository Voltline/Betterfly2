#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# 保持历史脚本的全量重建语义；新部署请直接使用 deploy_docker_compose.sh。
exec "$SCRIPT_DIR/deploy_docker_compose.sh" full --cert --proto --force-recreate "$@"

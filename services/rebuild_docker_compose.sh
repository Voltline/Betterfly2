#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

RUN_CERT=0
RUN_PROTO=0
WITH_DEPS=0
FORCE_RECREATE=1
REMOVE_ORPHANS=0
USE_SUDO=1
ALL_SERVICES=0
DRY_RUN=0
TARGETS=()
SERVICES=()

usage() {
  cat <<'EOF'
Usage:
  ./rebuild_docker_compose.sh [options] <service|alias>...

Examples:
  ./rebuild_docker_compose.sh df
  ./rebuild_docker_compose.sh dataforwarding
  ./rebuild_docker_compose.sh storage
  ./rebuild_docker_compose.sh --proto df storage
  ./rebuild_docker_compose.sh --cert --proto --all

Aliases:
  df-all, dataforwarding -> df df2
  auth                   -> auth_service
  storage                -> storage_service
  friend                 -> friend_service
  abtest                 -> abtest_service
  call                   -> call_service
  turn                   -> coturn
  push                   -> push_service

Options:
  --all          Rebuild all services, similar to the old full rebuild flow.
  --proto        Run make -C ../proto before rebuilding.
  --cert         Regenerate WebSocket self-signed cert before rebuilding.
  --with-deps    Let docker compose recreate dependencies too. Default is --no-deps.
  --no-force     Do not pass --force-recreate.
  --remove-orphans
                 Pass --remove-orphans to docker compose.
  --no-sudo      Run docker directly instead of sudo docker.
  --dry-run      Print the docker compose command without executing it.
  --list         Print known compose services and aliases.
  -h, --help     Show this help.
EOF
}

list_targets() {
  cat <<'EOF'
Compose services:
  redis kafka1 kafka2 kafka-ui df df2 auth_service rustfs storage_service prometheus grafana friend_service abtest_service call_service push_service coturn

Useful aliases:
  dataforwarding df-all auth storage friend abtest call push turn
EOF
}

add_service() {
  local service="$1"
  local existing
  if [[ "${#SERVICES[@]}" -gt 0 ]]; then
    for existing in "${SERVICES[@]}"; do
      if [[ "$existing" == "$service" ]]; then
        return
      fi
    done
  fi
  SERVICES+=("$service")
}

add_target() {
  case "$1" in
    dataforwarding|df-all)
      add_service "df"
      add_service "df2"
      ;;
    auth)
      add_service "auth_service"
      ;;
    storage)
      add_service "storage_service"
      ;;
    friend)
      add_service "friend_service"
      ;;
    abtest)
      add_service "abtest_service"
      ;;
    call)
      add_service "call_service"
      ;;
    turn)
      add_service "coturn"
      ;;
    push)
      add_service "push_service"
      ;;
    redis|kafka1|kafka2|kafka-ui|df|df2|auth_service|rustfs|storage_service|prometheus|grafana|friend_service|abtest_service|call_service|push_service|coturn)
      add_service "$1"
      ;;
    *)
      echo "Unknown service or alias: $1" >&2
      echo "Run ./rebuild_docker_compose.sh --list to see available targets." >&2
      exit 1
      ;;
  esac
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --all)
      ALL_SERVICES=1
      REMOVE_ORPHANS=1
      ;;
    --proto)
      RUN_PROTO=1
      ;;
    --cert)
      RUN_CERT=1
      ;;
    --with-deps)
      WITH_DEPS=1
      ;;
    --no-force)
      FORCE_RECREATE=0
      ;;
    --remove-orphans)
      REMOVE_ORPHANS=1
      ;;
    --no-sudo)
      USE_SUDO=0
      ;;
    --dry-run)
      DRY_RUN=1
      ;;
    --list)
      list_targets
      exit 0
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      while [[ $# -gt 0 ]]; do
        TARGETS+=("$1")
        shift
      done
      break
      ;;
    -*)
      echo "Unknown option: $1" >&2
      usage
      exit 1
      ;;
    *)
      TARGETS+=("$1")
      ;;
  esac
  shift
done

if [[ "$ALL_SERVICES" -eq 0 && "${#TARGETS[@]}" -eq 0 ]]; then
  echo "No target service specified." >&2
  usage
  exit 1
fi

if [[ "${#TARGETS[@]}" -gt 0 ]]; then
  for target in "${TARGETS[@]}"; do
    add_target "$target"
  done
fi

if [[ "$RUN_CERT" -eq 1 ]]; then
  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "Would run: ./../common/ws_ssl/generate_self_signed_cert.sh"
  else
    ./../common/ws_ssl/generate_self_signed_cert.sh
  fi
fi

if [[ "$RUN_PROTO" -eq 1 ]]; then
  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "Would run: make -C ../proto"
  else
    make -C ../proto
  fi
fi

DOCKER_CMD=(docker)
if [[ "$USE_SUDO" -eq 1 ]]; then
  DOCKER_CMD=(sudo docker)
fi

COMPOSE_CMD=("${DOCKER_CMD[@]}" compose up -d --build)

if [[ "$FORCE_RECREATE" -eq 1 ]]; then
  COMPOSE_CMD+=(--force-recreate)
fi

if [[ "$REMOVE_ORPHANS" -eq 1 ]]; then
  COMPOSE_CMD+=(--remove-orphans)
fi

if [[ "$ALL_SERVICES" -eq 0 && "$WITH_DEPS" -eq 0 ]]; then
  COMPOSE_CMD+=(--no-deps)
fi

if [[ "$ALL_SERVICES" -eq 0 ]]; then
  COMPOSE_CMD+=("${SERVICES[@]}")
fi

echo "Running: ${COMPOSE_CMD[*]}"
if [[ "$DRY_RUN" -eq 1 ]]; then
  exit 0
fi

"${COMPOSE_CMD[@]}"

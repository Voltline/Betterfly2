#!/bin/bash
set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

RUN_CERT=0
RUN_PROTO=0
WITH_DEPS=0
FORCE_RECREATE=0
REMOVE_ORPHANS=0
USE_SUDO=1
ALL_SERVICES=0
DRY_RUN=0
TARGETS=()
SERVICES=()
EXTRA_PROFILES=()
PRESET="standard"
GROUP_CALLS=1
KAFKA_CLUSTER=0
source "$SCRIPT_DIR/compose_environment.sh"

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
  dataforwarding         -> df (also df2 in distributed/full presets)
  df-all                 -> df df2, explicitly
  auth                   -> auth_service
  storage                -> storage_service
  friend                 -> friend_service
  abtest                 -> abtest_service
  moments                -> moments_service
  call                   -> call_service
  turn                   -> coturn
  sfu                    -> livekit
  push                   -> push_service

Options:
  --all          Deploy/rebuild the selected preset, default standard (not historical full).
  --preset <minimal|standard|distributed|full>
  --enable <profile>  Enable an additional capability or diagnostic tool.
  --kafka-cluster Select the separate two-broker testing topology.
  --without-group-calls  Omit LiveKit in preset deployments.
  --proto        Run make -C ../proto before rebuilding.
  --cert         Regenerate WebSocket self-signed cert before rebuilding.
  --with-deps    Let docker compose recreate dependencies too. Default is --no-deps.
  --no-force     Do not pass --force-recreate.
  --force-recreate  Recreate unchanged containers too (opt-in).
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
  redis kafka1 kafka2 kafka-ui df df2 auth_service rustfs storage_service prometheus grafana friend_service abtest_service moments_service call_service push_service coturn livekit

Useful aliases:
  dataforwarding df-all auth storage friend abtest moments call push turn sfu
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
    dataforwarding)
      add_service "df"
      if [[ "$PRESET" == "distributed" || "$PRESET" == "full" ]]; then
        add_service "df2"
        add_profile redundancy
      fi
      ;;
    df-all)
      add_service "df"
      add_service "df2"
      add_profile redundancy
      ;;
    auth)
      add_service "auth_service"
      ;;
    storage)
      add_service "storage_service"
      add_profile storage
      ;;
    friend)
      add_service "friend_service"
      ;;
    abtest)
      add_service "abtest_service"
      add_profile experiments
      ;;
    moments)
      add_service "moments_service"
      add_profile moments
      ;;
    call)
      add_service "call_service"
      add_profile calls
      ;;
    turn)
      add_service "coturn"
      add_profile calls
      ;;
    sfu)
      add_service "livekit"
      add_profile group-calls
      ;;
    push)
      add_service "push_service"
      add_profile notifications
      ;;
    redis|kafka1|kafka2|kafka-ui|df|df2|auth_service|rustfs|storage_service|prometheus|grafana|friend_service|abtest_service|moments_service|call_service|push_service|coturn|livekit)
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
    --preset)
      [[ $# -ge 2 ]] || { echo "--preset requires a value" >&2; exit 1; }
      PRESET="$2"
      shift
      ;;
    --enable)
      [[ $# -ge 2 ]] || { echo "--enable requires a profile" >&2; exit 1; }
      add_profile "$2"
      shift
      ;;
    --kafka-cluster) KAFKA_CLUSTER=1 ;;
    --without-group-calls) GROUP_CALLS=0 ;;
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
    --force-recreate) FORCE_RECREATE=1 ;;
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

if [[ "$ALL_SERVICES" -eq 1 ]]; then
  [[ "${#TARGETS[@]}" -eq 0 ]] || { echo "--all cannot be combined with targets; use --preset/--enable" >&2; exit 1; }
  ARGS=("$PRESET")
  [[ "$RUN_CERT" -eq 0 ]] || ARGS+=(--cert)
  [[ "$RUN_PROTO" -eq 0 ]] || ARGS+=(--proto)
  [[ "$FORCE_RECREATE" -eq 0 ]] || ARGS+=(--force-recreate)
  [[ "$REMOVE_ORPHANS" -eq 1 ]] || ARGS+=(--keep-orphans)
  [[ "$USE_SUDO" -eq 1 ]] || ARGS+=(--no-sudo)
  [[ "$DRY_RUN" -eq 0 ]] || ARGS+=(--dry-run)
  [[ "$GROUP_CALLS" -eq 1 ]] || ARGS+=(--without-group-calls)
  [[ "$KAFKA_CLUSTER" -eq 0 ]] || ARGS+=(--kafka-cluster)
  for profile in "${EXTRA_PROFILES[@]}"; do ARGS+=(--enable "$profile"); done
  exec "$SCRIPT_DIR/deploy_docker_compose.sh" "${ARGS[@]}"
fi

if [[ "${#TARGETS[@]}" -gt 0 ]]; then
  for target in "${TARGETS[@]}"; do
    add_target "$target"
  done
fi

for service in "${SERVICES[@]}"; do
  case "$service" in
    df2) add_profile redundancy ;;
    storage_service|rustfs) add_profile storage ;;
    abtest_service) add_profile experiments ;;
    moments_service) add_profile moments ;;
    call_service|coturn) add_profile calls ;;
    push_service) add_profile notifications ;;
    livekit) add_profile group-calls ;;
    prometheus|grafana) add_profile observability ;;
    kafka-ui) add_profile tools ;;
    kafka2) [[ "$KAFKA_CLUSTER" -eq 1 ]] || { echo "kafka2 requires --kafka-cluster" >&2; exit 1; } ;;
  esac
done
select_compose_environment
DOCKER_CMD=(docker)
[[ "$USE_SUDO" -eq 0 ]] || DOCKER_CMD=(sudo docker)
if [[ "$DRY_RUN" -eq 0 ]]; then
  CHECK_KAFKA="$WITH_DEPS"
  [[ "$REMOVE_ORPHANS" -eq 0 ]] || CHECK_KAFKA=1
  for service in "${SERVICES[@]}"; do
    [[ "$service" != "kafka1" && "$service" != "kafka2" ]] || CHECK_KAFKA=1
  done
  [[ "$CHECK_KAFKA" -eq 0 ]] || check_kafka_topology
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

COMPOSE_CMD=("${DOCKER_CMD[@]}" compose "${COMPOSE_ARGS[@]}" up -d --build)

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

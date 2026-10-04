#!/bin/bash
# macOS still ships Bash 3.2, where expanding an empty array under `set -u`
# fails even when the array was declared. Keep strict error and pipe handling.
set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

PRESET="standard"
GROUP_CALLS=1
KAFKA_CLUSTER=0
RUN_CERT=0
RUN_PROTO=0
BUILD=1
FORCE_RECREATE=0
REMOVE_ORPHANS=1
USE_SUDO=1
DRY_RUN=0
EXTRA_PROFILES=()
source "$SCRIPT_DIR/compose_environment.sh"

usage() {
  cat <<'EOF'
Usage:
  ./deploy_docker_compose.sh [minimal|standard|distributed|full] [options]

Presets:
  minimal      Auth, connections and friend/group control only; not full messaging.
  standard     All product features, single Kafka and single forwarding pod (default).
  distributed  Standard plus a second forwarding pod; still a single Kafka.
  full         Distributed plus observability and Kafka UI.

Options:
  --enable <profile>  Add a profile: storage, notifications, calls, experiments,
                      group-calls, moments, observability, tools or redundancy. May be repeated.
  --cert              Regenerate the WebSocket self-signed certificate.
  --proto             Regenerate protobuf code.
  --without-group-calls  Omit LiveKit; keep one-to-one calls.
  --kafka-cluster     Select the separate two-broker test topology. Not an in-place migration.
  --no-build          Do not build application images.
  --force-recreate    Recreate containers even when unchanged.
  --keep-orphans      Do not remove services excluded by the selected preset.
  --no-sudo           Run Docker without sudo.
  --dry-run           Print commands without executing them.
  -h, --help          Show this help.
EOF
}

if [[ $# -gt 0 && "$1" != -* ]]; then
  PRESET="$1"
  shift
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --enable)
      [[ $# -ge 2 ]] || { echo "--enable requires a profile" >&2; exit 1; }
      add_profile "$2"
      shift 2
      ;;
    --cert) RUN_CERT=1; shift ;;
    --proto) RUN_PROTO=1; shift ;;
    --without-group-calls) GROUP_CALLS=0; shift ;;
    --kafka-cluster) KAFKA_CLUSTER=1; shift ;;
    --no-build) BUILD=0; shift ;;
    --force-recreate) FORCE_RECREATE=1; shift ;;
    --keep-orphans) REMOVE_ORPHANS=0; shift ;;
    --no-sudo) USE_SUDO=0; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage; exit 1 ;;
  esac
done

select_compose_environment

run_step() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "Would run: $*"
  else
    "$@"
  fi
}

DOCKER_CMD=(docker)
[[ "$USE_SUDO" -eq 0 ]] || DOCKER_CMD=(sudo docker)
[[ "$DRY_RUN" -eq 1 ]] || check_kafka_topology
[[ "$RUN_CERT" -eq 0 ]] || run_step ./../common/ws_ssl/generate_self_signed_cert.sh
[[ "$RUN_PROTO" -eq 0 ]] || run_step make -C ../proto
COMPOSE_CMD=("${DOCKER_CMD[@]}" compose "${COMPOSE_ARGS[@]}")
COMPOSE_CMD+=(up -d)
[[ "$BUILD" -eq 0 ]] || COMPOSE_CMD+=(--build)
[[ "$FORCE_RECREATE" -eq 0 ]] || COMPOSE_CMD+=(--force-recreate)
[[ "$REMOVE_ORPHANS" -eq 0 ]] || COMPOSE_CMD+=(--remove-orphans)

echo "Preset: $PRESET"
echo "Profiles: ${EXTRA_PROFILES[*]:-(core only)}"
echo "METRICS_ENABLED=$METRICS_ENABLED COMPOSE_PARALLEL_LIMIT=$COMPOSE_PARALLEL_LIMIT"
echo "Kafka cluster test topology: $KAFKA_CLUSTER (existing topology checked before deployment)"
run_step "${COMPOSE_CMD[@]}"

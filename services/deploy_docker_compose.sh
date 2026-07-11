#!/bin/bash
# macOS still ships Bash 3.2, where expanding an empty array under `set -u`
# fails even when the array was declared. Keep strict error and pipe handling.
set -eo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

PRESET="standard"
RUN_CERT=0
RUN_PROTO=0
BUILD=1
FORCE_RECREATE=0
REMOVE_ORPHANS=1
USE_SUDO=1
DRY_RUN=0
EXTRA_PROFILES=()
DEFAULT_METRICS_ENABLED=true

usage() {
  cat <<'EOF'
Usage:
  ./deploy_docker_compose.sh [minimal|standard|full] [options]

Presets:
  minimal   Core messaging only. Disables files, APNs, calls, experiments,
            observability, Kafka UI, the second forwarding pod and metrics endpoints.
  standard  All product features. Omits observability, Kafka UI and the second
            forwarding pod.
  full      Enables every service, matching the historical full deployment.

Options:
  --enable <profile>  Add a profile: storage, notifications, calls, experiments,
                      observability, tools or redundancy. May be repeated.
  --cert              Regenerate the WebSocket self-signed certificate.
  --proto             Regenerate protobuf code.
  --no-build          Do not build application images.
  --force-recreate    Recreate containers even when unchanged.
  --keep-orphans      Do not remove services excluded by the selected preset.
  --no-sudo           Run Docker without sudo.
  --dry-run           Print commands without executing them.
  -h, --help          Show this help.
EOF
}

add_profile() {
  local profile="$1"
  local existing
  case "$profile" in
    storage|notifications|calls|experiments|observability|tools|redundancy) ;;
    *)
      echo "Unknown profile: $profile" >&2
      exit 1
      ;;
  esac
  for existing in "${EXTRA_PROFILES[@]}"; do
    [[ "$existing" == "$profile" ]] && return
  done
  EXTRA_PROFILES+=("$profile")
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
    --no-build) BUILD=0; shift ;;
    --force-recreate) FORCE_RECREATE=1; shift ;;
    --keep-orphans) REMOVE_ORPHANS=0; shift ;;
    --no-sudo) USE_SUDO=0; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; usage; exit 1 ;;
  esac
done

case "$PRESET" in
  minimal)
    DEFAULT_METRICS_ENABLED=false
    export LOG_LEVEL="${LOG_LEVEL:-info}"
    ;;
  standard)
    add_profile storage
    add_profile notifications
    add_profile calls
    add_profile experiments
    ;;
  full)
    add_profile storage
    add_profile notifications
    add_profile calls
    add_profile experiments
    add_profile observability
    add_profile tools
    add_profile redundancy
    ;;
  *)
    echo "Unknown preset: $PRESET" >&2
    usage
    exit 1
    ;;
esac

for profile in "${EXTRA_PROFILES[@]}"; do
  if [[ "$profile" == "observability" ]]; then
    DEFAULT_METRICS_ENABLED=true
    break
  fi
done
export METRICS_ENABLED="${METRICS_ENABLED:-$DEFAULT_METRICS_ENABLED}"

run_step() {
  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo "Would run: $*"
  else
    "$@"
  fi
}

[[ "$RUN_CERT" -eq 0 ]] || run_step ./../common/ws_ssl/generate_self_signed_cert.sh
[[ "$RUN_PROTO" -eq 0 ]] || run_step make -C ../proto

DOCKER_CMD=(docker)
[[ "$USE_SUDO" -eq 0 ]] || DOCKER_CMD=(sudo docker)
COMPOSE_CMD=("${DOCKER_CMD[@]}" compose)
for profile in "${EXTRA_PROFILES[@]}"; do
  COMPOSE_CMD+=(--profile "$profile")
done
COMPOSE_CMD+=(up -d)
[[ "$BUILD" -eq 0 ]] || COMPOSE_CMD+=(--build)
[[ "$FORCE_RECREATE" -eq 0 ]] || COMPOSE_CMD+=(--force-recreate)
[[ "$REMOVE_ORPHANS" -eq 0 ]] || COMPOSE_CMD+=(--remove-orphans)

echo "Preset: $PRESET"
echo "Profiles: ${EXTRA_PROFILES[*]:-(core only)}"
echo "METRICS_ENABLED=$METRICS_ENABLED LOG_LEVEL=${LOG_LEVEL:-debug}"
run_step "${COMPOSE_CMD[@]}"

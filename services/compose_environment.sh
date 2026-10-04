#!/bin/bash
# Shared only by the Compose entry scripts; no daemon or runtime state.
add_profile() {
  local profile="$1" existing
  case "$profile" in
    storage|notifications|calls|group-calls|moments|experiments|observability|tools|redundancy) ;;
    *) echo "Unknown profile: $profile" >&2; return 1 ;;
  esac
  if [[ "${#EXTRA_PROFILES[@]}" -gt 0 ]]; then
    for existing in "${EXTRA_PROFILES[@]}"; do
      [[ "$existing" != "$profile" ]] || return 0
    done
  fi
  EXTRA_PROFILES+=("$profile")
}

select_compose_environment() {
  local profile
  case "$PRESET" in
    minimal) ;;
    standard|distributed|full)
      for profile in storage notifications calls experiments moments; do add_profile "$profile"; done
      [[ "$GROUP_CALLS" -eq 0 ]] || add_profile group-calls
      [[ "$PRESET" == "standard" ]] || add_profile redundancy
      if [[ "$PRESET" == "full" ]]; then add_profile observability; add_profile tools; fi
      ;;
    *) echo "Unknown preset: $PRESET" >&2; return 1 ;;
  esac
  COMPOSE_ARGS=(-f "$SCRIPT_DIR/docker-compose.yml")
  if [[ "$KAFKA_CLUSTER" -eq 1 ]]; then
    COMPOSE_ARGS+=(-f "$SCRIPT_DIR/docker-compose.kafka-cluster.yml")
  fi
  for profile in "${EXTRA_PROFILES[@]}"; do COMPOSE_ARGS+=(--profile "$profile"); done
  # Explicit presets must not accidentally inherit all profiles from the shell.
  unset COMPOSE_PROFILES
  export COMPOSE_PARALLEL_LIMIT="${COMPOSE_PARALLEL_LIMIT:-2}"
  local metrics=false group_calls=false
  for profile in "${EXTRA_PROFILES[@]}"; do
    [[ "$profile" != "observability" ]] || metrics=true
    [[ "$profile" != "group-calls" ]] || group_calls=true
  done
  if [[ "$group_calls" == "false" ]]; then
    export LIVEKIT_API_URL= LIVEKIT_PUBLIC_URL= LIVEKIT_API_KEY= LIVEKIT_API_SECRET=
  fi
  export METRICS_ENABLED="${METRICS_ENABLED:-$metrics}"
}

check_kafka_topology() {
  local ids metadata expected=single service topology id
  local containers=()
  [[ "$KAFKA_CLUSTER" -eq 0 ]] || expected=cluster
  ids=$("${DOCKER_CMD[@]}" compose "${COMPOSE_ARGS[@]}" ps -a -q) || return 1
  [[ -n "$ids" ]] || return 0
  while IFS= read -r id; do [[ -z "$id" ]] || containers+=("$id"); done <<< "$ids"
  metadata=$("${DOCKER_CMD[@]}" inspect --format '{{index .Config.Labels "com.docker.compose.service"}} {{index .Config.Labels "betterfly.kafka.topology"}}' "${containers[@]}") || return 1
  while read -r service topology; do
    case "$service" in
      kafka1|kafka2)
        if [[ "$topology" != "$expected" || "$service:$expected" == "kafka2:single" ]]; then
          echo "Refusing Kafka topology/storage replacement ($service: $topology -> $expected)." >&2
          echo "Preserve and drain the existing cluster first; see DEPLOYMENT_PROFILES.md. No containers changed." >&2
          return 1
        fi
        ;;
    esac
  done <<< "$metadata"
}

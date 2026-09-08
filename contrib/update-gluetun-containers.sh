#!/usr/bin/env bash
set -Eeuo pipefail

DRY_RUN=false
REMOTE_HOST=""
WAIT_SECONDS=180
declare -a REQUESTED=()

usage() {
  cat <<'EOF'
Update Compose-managed Gluetun containers and recreate containers sharing
their network namespace.

Usage:
  update-gluetun-containers.sh [options] [GLUETUN_CONTAINER ...]

Options:
  -n, --dry-run           Show the complete update/re-attach plan only
  -H, --host USER@HOST    Run on a remote Docker host over SSH
      --wait SECONDS      Health/running wait timeout (default: 180)
  -h, --help              Show this help

With no container names, all Compose-managed Gluetun containers are updated.

Examples:
  ./update-gluetun-containers.sh --dry-run --host random@10.1.2.50
  ./update-gluetun-containers.sh --host random@10.1.2.50 gluetun-atlantis-3
  ./update-gluetun-containers.sh gluetun-atlantis gluetun-atlantis-2
EOF
}

while (($#)); do
  case "$1" in
    -n|--dry-run) DRY_RUN=true; shift ;;
    -H|--host) REMOTE_HOST=${2:?"--host requires USER@HOST"}; shift 2 ;;
    --wait) WAIT_SECONDS=${2:?"--wait requires seconds"}; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    --) shift; REQUESTED+=("$@"); break ;;
    -*) printf 'Unknown option: %s\n' "$1" >&2; usage >&2; exit 2 ;;
    *) REQUESTED+=("$1"); shift ;;
  esac
done

[[ $WAIT_SECONDS =~ ^[1-9][0-9]*$ ]] || {
  printf '%s\n' '--wait must be a positive number of seconds' >&2
  exit 2
}

# Execute this same script through SSH so Compose file paths from container
# labels are resolved on the Docker host, not on the CachyOS workstation.
if [[ -n $REMOTE_HOST ]]; then
  remote_args=(--wait "$WAIT_SECONDS")
  $DRY_RUN && remote_args+=(--dry-run)
  remote_args+=("${REQUESTED[@]}")
  printf 'Running on Docker host %s\n' "$REMOTE_HOST"
  script_payload=$(base64 < "$0" | tr -d '\n')
  printf -v quoted_args ' %q' "${remote_args[@]}"
  remote_command="tmp=\$(mktemp \"\$HOME/.update-gluetun.XXXXXX\"); printf %s '$script_payload' | base64 -d > \"\$tmp\"; chmod 700 \"\$tmp\"; \"\$tmp\"$quoted_args; rc=\$?; rm -f \"\$tmp\"; exit \$rc"
  exec ssh -t "$REMOTE_HOST" "$remote_command"
fi

if docker info >/dev/null 2>&1; then
  DOCKER=(docker)
else
  DOCKER=(sudo docker)
  "${DOCKER[@]}" info >/dev/null
fi

log() { printf '%s\n' "$*"; }
quote_command() { printf ' %q' "$@"; printf '\n'; }
run() {
  if $DRY_RUN; then
    printf '  DRY-RUN:'
    quote_command "$@"
  else
    "$@"
  fi
}

inspect() { "${DOCKER[@]}" inspect --format "$2" "$1"; }
label() {
  inspect "$1" "{{with index .Config.Labels \"$2\"}}{{.}}{{end}}"
}

compose_command() {
  local container=$1 project working_dir files file
  project=$(label "$container" com.docker.compose.project)
  working_dir=$(label "$container" com.docker.compose.project.working_dir)
  files=$(label "$container" com.docker.compose.project.config_files)
  [[ -n $project && -n $working_dir && -n $files ]] || return 1
  COMPOSE=("${DOCKER[@]}" compose --project-name "$project" --project-directory "$working_dir")
  IFS=',' read -ra compose_files <<< "$files"
  for file in "${compose_files[@]}"; do
    COMPOSE+=(-f "$file")
  done
}

recreate_standalone_dependent() {
  local container=$1 gluetun=$2 was_running=$3 new_id backup payload response created_id
  new_id=$(inspect "$gluetun" '{{.Id}}')
  backup="${container}.gluetun-backup.$(date +%s)"
  if $DRY_RUN; then
    log "  Re-attaching standalone container $container (preserve running=$was_running)"
    log "  DRY-RUN: snapshot configuration, recreate against the new $gluetun container ID, verify, then remove backup"
    return 0
  fi
  command -v jq >/dev/null || {
    log "ERROR: jq is required to safely recreate standalone container $container" >&2
    return 1
  }
  command -v curl >/dev/null || {
    log "ERROR: curl is required to safely recreate standalone container $container" >&2
    return 1
  }
  if [[ ${DOCKER[0]} == sudo ]]; then CURL=(sudo curl); else CURL=(curl); fi
  payload=$("${DOCKER[@]}" inspect "$container" | jq --arg mode "container:$new_id" '.[0].Config + {HostConfig: (.[0].HostConfig + {NetworkMode: $mode})} | del(.Hostname,.ExposedPorts)')
  run "${DOCKER[@]}" stop "$container"
  run "${DOCKER[@]}" rename "$container" "$backup"
  if ! response=$(printf '%s' "$payload" | "${CURL[@]}" --silent --show-error --fail-with-body --unix-socket /var/run/docker.sock -H 'Content-Type: application/json' -X POST --data-binary @- "http://localhost/containers/create?name=$container"); then
    log "ERROR: failed to recreate $container; restoring its original name" >&2
    "${DOCKER[@]}" rename "$backup" "$container" || true
    return 1
  fi
  created_id=$(jq -r '.Id // empty' <<< "$response")
  if [[ -z $created_id ]]; then
    log "ERROR: Docker returned no container ID for $container; restoring its original name" >&2
    "${DOCKER[@]}" rename "$backup" "$container" || true
    return 1
  fi
  if [[ $was_running == true ]]; then
    if ! "${DOCKER[@]}" start "$container"; then
      log "ERROR: $container did not start; restoring the previous container" >&2
      "${DOCKER[@]}" rm -f "$container" || true
      "${DOCKER[@]}" rename "$backup" "$container" || true
      "${DOCKER[@]}" start "$container" || true
      return 1
    fi
  fi
  "${DOCKER[@]}" rm "$backup" >/dev/null
  log "  Re-attached standalone container $container"
}

discover_gluetun() {
  local name title
  while IFS= read -r name; do
    [[ -n $name ]] || continue
    title=$(label "$name" org.opencontainers.image.title)
    if [[ $title == gluetun || $name == gluetun || $name == gluetun-* ]]; then
      [[ -n $(label "$name" com.docker.compose.service) ]] && printf '%s\n' "$name"
    fi
  done < <("${DOCKER[@]}" ps -a --format '{{.Names}}')
}

wait_for_gluetun() {
  local container=$1 deadline=$((SECONDS + WAIT_SECONDS)) status health
  $DRY_RUN && return 0
  while ((SECONDS < deadline)); do
    status=$(inspect "$container" '{{.State.Status}}')
    health=$(inspect "$container" '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}')
    if [[ $status == running && ($health == healthy || $health == none) ]]; then
      log "  $container is $status ($health)"
      return 0
    fi
    [[ $status != exited && $status != dead ]] || {
      log "ERROR: $container entered state $status" >&2
      return 1
    }
    sleep 2
  done
  log "ERROR: timed out waiting for $container to become healthy" >&2
  return 1
}

update_one() {
  local gluetun=$1 old_id service dependent was_running mode
  declare -a dependents=() running_dependents=()

  old_id=$(inspect "$gluetun" '{{.Id}}')
  service=$(label "$gluetun" com.docker.compose.service)
  compose_command "$gluetun"

  while IFS='|' read -r dependent mode; do
    [[ $mode == "container:$old_id" ]] || continue
    dependents+=("$dependent")
    was_running=$(inspect "$dependent" '{{.State.Running}}')
    [[ $was_running == true ]] && running_dependents+=("$dependent")
  done < <("${DOCKER[@]}" inspect $("${DOCKER[@]}" ps -aq) --format '{{.Name}}|{{.HostConfig.NetworkMode}}' | sed 's#^/##')

  log ""
  log "Gluetun: $gluetun (service: $service)"
  if ((${#dependents[@]})); then
    log "  Attached containers: ${dependents[*]}"
  else
    log "  Attached containers: none"
  fi

  run "${COMPOSE[@]}" pull "$service"
  run "${COMPOSE[@]}" up -d --no-deps --force-recreate "$service"
  wait_for_gluetun "$gluetun"

  for dependent in "${dependents[@]}"; do
    was_running=false
    for running in "${running_dependents[@]}"; do
      [[ $running == "$dependent" ]] && was_running=true
    done
    # Dependents are always recreated from docker inspect's live Config and
    # HostConfig. Their Compose definitions may be stale or intentionally
    # different, so updating Gluetun must never redeploy a dependent stack.
    recreate_standalone_dependent "$dependent" "$gluetun" "$was_running"
  done
}

if ((${#REQUESTED[@]} == 0)); then
  mapfile -t REQUESTED < <(discover_gluetun)
fi
((${#REQUESTED[@]})) || {
  log 'No Compose-managed Gluetun containers found.' >&2
  exit 1
}

log "Mode: $($DRY_RUN && printf 'dry run' || printf 'update')"
log "Targets: ${REQUESTED[*]}"
for gluetun in "${REQUESTED[@]}"; do
  "${DOCKER[@]}" inspect "$gluetun" >/dev/null 2>&1 || {
    log "ERROR: container not found: $gluetun" >&2
    exit 1
  }
  update_one "$gluetun"
done

log ""
$DRY_RUN && log 'Dry run complete; no containers or images were changed.' || log 'Gluetun update and dependent network re-attachment complete.'

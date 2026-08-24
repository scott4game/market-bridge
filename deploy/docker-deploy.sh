#!/usr/bin/env bash
set -Eeuo pipefail

REPOSITORY="${MARKET_BRIDGE_REPOSITORY:-scott4game/market-bridge}"
REF="${MARKET_BRIDGE_REF:-dev}"
BASE_URL="${MARKET_BRIDGE_BASE_URL:-https://raw.githubusercontent.com/${REPOSITORY}/${REF}}"
COMPOSE_FILE="compose.yaml"
ENV_EXAMPLE_FILE=".env.example"
ENV_FILE=".env"

log() { printf '[market-bridge] %s\n' "$*"; }
die() { printf '[market-bridge] ERROR: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

download() {
  local path="$1" output="$2"
  curl --fail --silent --show-error --location --retry 3 \
    --output "$output" "${BASE_URL}/${path}"
}

set_env_value() {
  local file="$1" key="$2" value="$3" tmp line found=false
  tmp="$(mktemp "${file}.XXXXXX")"
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == "${key}="* ]]; then
      printf '%s=%s\n' "$key" "$value" >>"$tmp"
      found=true
    else
      printf '%s\n' "$line" >>"$tmp"
    fi
  done <"$file"
  if [[ "$found" == false ]]; then printf '%s=%s\n' "$key" "$value" >>"$tmp"; fi
  chmod 600 "$tmp"
  mv "$tmp" "$file"
}

[[ ! -e "$COMPOSE_FILE" && ! -e "$ENV_EXAMPLE_FILE" && ! -e "$ENV_FILE" ]] || \
  die "a deployment file already exists; refusing to overwrite the current deployment"

need curl
need mktemp
need od

tmp_dir="$(mktemp -d)"
trap 'rm -rf -- "$tmp_dir"' EXIT

log "downloading deployment files from ${REPOSITORY}@${REF}"
download deploy/compose.server.yaml "$tmp_dir/$COMPOSE_FILE"
download .env.server.example "$tmp_dir/$ENV_EXAMPLE_FILE"

token="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
cp "$tmp_dir/$COMPOSE_FILE" "$COMPOSE_FILE"
cp "$tmp_dir/$ENV_EXAMPLE_FILE" "$ENV_EXAMPLE_FILE"
cp "$ENV_EXAMPLE_FILE" "$ENV_FILE"
set_env_value "$ENV_FILE" GO_SERVER_TOKEN "$token"
chmod 600 "$ENV_FILE"

if [[ ( -t 0 || -t 1 || -t 2 ) && -r /dev/tty && -w /dev/tty ]]; then
  massive_key=""
  printf '[market-bridge] Massive API Key（直接回车则先使用 Mock）: ' >/dev/tty
  IFS= read -r massive_key </dev/tty || true
  if [[ -n "$massive_key" ]]; then
    set_env_value "$ENV_FILE" GO_SERVER_PROVIDER massive
    set_env_value "$ENV_FILE" GO_SERVER_DATA_VERSION massive-v1
    set_env_value "$ENV_FILE" MASSIVE_API_KEY "$massive_key"
  fi
fi

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" config -q
else
  log "Docker Compose v2 was not found; install it before starting the service"
fi

log "deployment files are ready in $(pwd)"
log "start: docker compose up -d"
log "logs:  docker compose logs -f go-server"
log "Nginx upstream: http://127.0.0.1:17601"

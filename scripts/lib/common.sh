#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DEFAULT_SERVER_IMAGE="docker.io/otsgame/market-bridge-server"
DEFAULT_CLIENT_IMAGE="docker.io/otsgame/market-bridge-client"

log() { printf '[market-bridge] %s\n' "$*"; }
warn() { printf '[market-bridge] WARNING: %s\n' "$*" >&2; }
die() { printf '[market-bridge] ERROR: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

need_docker() {
  need docker
  docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required"
  docker info >/dev/null 2>&1 || die "cannot access Docker daemon"
}

absolute_path() {
  local target="$1" dir base
  dir="$(dirname "$target")"; base="$(basename "$target")"
  (cd "$dir" 2>/dev/null && printf '%s/%s\n' "$(pwd -P)" "$base")
}

copy_file() {
  local source="$1" target="$2" mode="$3" source_abs target_abs
  mkdir -p "$(dirname "$target")"
  source_abs="$(absolute_path "$source")"
  target_abs="$(absolute_path "$target" 2>/dev/null || true)"
  if [[ "$source_abs" != "$target_abs" ]]; then install -m "$mode" "$source" "$target"; else chmod "$mode" "$target"; fi
}

load_env() {
  local file="$1"
  [[ -f "$file" ]] || die "environment file not found: $file"
  chmod 600 "$file"
  set -a
  # .env files used by the installers must also be valid shell assignments.
  # shellcheck disable=SC1090
  source "$file"
  set +a
}

env_value() { local name="$1"; printf '%s' "${!name-}"; }

require_env() {
  local name value
  for name in "$@"; do
    value="$(env_value "$name")"
    [[ -n "$value" ]] || die "$name must be set in .env"
    case "$value" in
      change-me|replace-me|replace-with-*|local-development-*|market.example.com|https://market.example.com) die "$name still contains an unsafe placeholder" ;;
    esac
  done
}

validate_server_env() {
  require_env GO_SERVER_TOKEN
  if [[ "${GO_SERVER_PROVIDER:-mock}" == "massive" ]]; then require_env MASSIVE_API_KEY; fi
  if [[ "${GO_SERVER_NEWS_PROVIDER:-disabled}" == "fmp" ]]; then require_env FMP_API_KEY; fi
  if [[ "${GO_SERVER_LIVE_PROVIDER:-mock}" == "longbridge" ]]; then
    require_env LONGBRIDGE_APP_KEY LONGBRIDGE_APP_SECRET LONGBRIDGE_ACCESS_TOKEN
  fi
  case "${GO_SERVER_CLICKHOUSE_ENABLED:-false}" in
    true) require_env CLICKHOUSE_URL CLICKHOUSE_DATABASE CLICKHOUSE_USER CLICKHOUSE_PASSWORD ;;
    false) ;;
    *) die "GO_SERVER_CLICKHOUSE_ENABLED must be true or false" ;;
  esac
}

validate_client_env() {
  require_env GO_CLIENT_SERVER_URL GO_CLIENT_SERVER_TOKEN
  case "${GO_CLIENT_CLICKHOUSE_ENABLED:-false}" in
    true) require_env GO_CLIENT_MIRROR_WATCHLIST GO_CLIENT_CLICKHOUSE_URL CLICKHOUSE_DATABASE CLICKHOUSE_USER CLICKHOUSE_PASSWORD ;;
    false) ;;
    *) die "GO_CLIENT_CLICKHOUSE_ENABLED must be true or false" ;;
  esac
}

validate_docker_client_env() {
  validate_client_env
  require_env REDIS_PASSWORD
}

set_env_value() {
  local file="$1" key="$2" value="$3" tmp
  [[ "$value" =~ ^[A-Za-z0-9._/:@+-]+$ ]] || die "unsafe value for $key"
  tmp="$(mktemp "${file}.XXXXXX")"
  awk -v key="$key" -v value="$value" '
    BEGIN { found=0 }
    index($0,key "=")==1 { print key "=" value; found=1; next }
    { print }
    END { if (!found) print key "=" value }
  ' "$file" >"$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$file"
}

registry_login() {
  if [[ -n "${GHCR_TOKEN:-}" ]]; then
    require_env GHCR_USERNAME GHCR_TOKEN
    printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin >/dev/null
  fi
}

compose_run() {
  docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

wait_for_service() {
  local service="$1" timeout="${2:-180}" start now cid status
  start="$(date +%s)"
  while :; do
    cid="$(compose_run ps -q "$service" 2>/dev/null || true)"
    if [[ -n "$cid" ]]; then
      status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null || true)"
      [[ "$status" == "healthy" || "$status" == "running" ]] && return 0
      [[ "$status" == "unhealthy" || "$status" == "exited" || "$status" == "dead" ]] && return 1
    fi
    now="$(date +%s)"; (( now-start < timeout )) || return 1
    sleep 2
  done
}

wait_http() {
  local url="$1" timeout="${2:-90}" start now
  start="$(date +%s)"
  while :; do
    if curl --fail --silent --show-error --max-time 2 "$url" >/dev/null 2>&1; then return 0; fi
    now="$(date +%s)"; (( now-start < timeout )) || return 1
    sleep 2
  done
}

release_url() {
  local version="$1" asset="$2"
  if [[ "$version" == "latest" ]]; then
    printf 'https://github.com/scott4game/market-bridge/releases/latest/download/%s' "$asset"
  else
    printf 'https://github.com/scott4game/market-bridge/releases/download/%s/%s' "$version" "$asset"
  fi
}

download() {
  local url="$1" output="$2"
  need curl
  curl --fail --location --retry 3 --retry-delay 2 --output "$output" "$url"
}

verify_checksum() {
  local sums="$1" asset="$2" directory="$3"
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$directory" && grep "  ${asset}$" "$sums" | sha256sum -c -)
  elif command -v shasum >/dev/null 2>&1; then
    (cd "$directory" && grep "  ${asset}$" "$sums" | shasum -a 256 -c -)
  else
    die "sha256sum or shasum is required"
  fi
}

safe_remove_tree() {
  local target="$1" allowed="$2" target_abs allowed_abs
  target_abs="$(absolute_path "$target")"; allowed_abs="$(absolute_path "$allowed")"
  [[ "$target_abs" == "$allowed_abs" || "$target_abs" == "$allowed_abs"/* ]] || die "refusing to remove unsafe path: $target_abs"
  [[ "$target_abs" != "/" && "$target_abs" != "$HOME" ]] || die "refusing to remove broad path"
  rm -rf -- "$target_abs"
}

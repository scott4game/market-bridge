#!/usr/bin/env bash
set -Eeuo pipefail

REPOSITORY="${MARKET_BRIDGE_REPOSITORY:-scott4game/market-bridge}"
REF="${MARKET_BRIDGE_REF:-dev}"
BASE_URL="${MARKET_BRIDGE_BASE_URL:-https://raw.githubusercontent.com/${REPOSITORY}/${REF}}"
COMPOSE_FILE="compose.yaml"
ENV_FILE=".env"

log() { printf '[market-bridge] %s\n' "$*"; }
die() { printf '[market-bridge] ERROR: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

[[ -f "$ENV_FILE" ]] || die "$ENV_FILE was not found; run this command from the server deployment directory"
[[ -f "$COMPOSE_FILE" ]] || die "$COMPOSE_FILE was not found; run this command from the server deployment directory"
need curl
need docker
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required"

tmp_dir="$(mktemp -d)"
trap 'rm -rf -- "$tmp_dir"' EXIT
downloaded="$tmp_dir/$COMPOSE_FILE"

log "downloading the server Compose template from ${REPOSITORY}@${REF}"
curl --fail --silent --show-error --location --retry 3 \
  --output "$downloaded" "${BASE_URL}/deploy/compose.server.yaml"

# Validate against the existing environment before replacing the active file.
docker compose --env-file "$ENV_FILE" -f "$downloaded" config --quiet

backup="${COMPOSE_FILE}.backup.$(date +%Y%m%d%H%M%S)"
cp -p "$COMPOSE_FILE" "$backup"
install -m 644 "$downloaded" "$COMPOSE_FILE"

log "updated $COMPOSE_FILE; preserved $ENV_FILE and backed up the old Compose file as $backup"
log "apply: docker compose up -d --force-recreate go-server"

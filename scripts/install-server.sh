#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"

INSTALL_DIR="/opt/market-bridge"
INPUT_ENV=""
VERSION="latest"
LOCAL_BUILD=false

usage(){ cat <<'EOF'
Usage: sudo ./scripts/install-server.sh --env PATH [--version v0.1.0] [--install-dir PATH] [--local-build]
Installs go-server on 127.0.0.1:17601 for an external reverse proxy. Existing data volumes are preserved.
EOF
}
while (($#)); do case "$1" in
  --env) INPUT_ENV="${2:?missing path}"; shift 2;;
  --version) VERSION="${2:?missing version}"; shift 2;;
  --install-dir) INSTALL_DIR="${2:?missing path}"; shift 2;;
  --local-build) LOCAL_BUILD=true; shift;;
  -h|--help) usage; exit 0;;
  *) die "unknown argument: $1";;
esac; done

[[ $EUID -eq 0 ]] || die "server installation must run as root"
[[ -n "$INPUT_ENV" ]] || die "--env is required"
need_docker
INPUT_ENV="$(absolute_path "$INPUT_ENV")"
load_env "$INPUT_ENV"; validate_server_env

mkdir -p "$INSTALL_DIR"
copy_file "$SCRIPT_ROOT/deploy/compose.server.yaml" "$INSTALL_DIR/compose.yaml" 644
copy_file "$INPUT_ENV" "$INSTALL_DIR/.env" 600
ENV_FILE="$INSTALL_DIR/.env"; COMPOSE_FILE="$INSTALL_DIR/compose.yaml"
set_env_value "$ENV_FILE" MARKET_BRIDGE_VERSION "$VERSION"
set_env_value "$ENV_FILE" MARKET_BRIDGE_SERVER_IMAGE "${MARKET_BRIDGE_SERVER_IMAGE:-$DEFAULT_SERVER_IMAGE}"

if $LOCAL_BUILD; then
  log "building go-server image from the current source tree"
  docker build --target go-server -t market-bridge-server:local "$SCRIPT_ROOT"
  set_env_value "$ENV_FILE" MARKET_BRIDGE_VERSION local
  set_env_value "$ENV_FILE" MARKET_BRIDGE_SERVER_IMAGE market-bridge-server
  set_env_value "$ENV_FILE" MARKET_BRIDGE_PULL_POLICY never
else
  set_env_value "$ENV_FILE" MARKET_BRIDGE_PULL_POLICY always
  load_env "$ENV_FILE"; registry_login; compose_run pull
fi

compose_run up -d --remove-orphans
if ! wait_for_service go-server 180; then compose_run logs --tail=100 go-server >&2 || true; die "go-server failed its health check"; fi

if command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]; then
  docker_bin="$(command -v docker)"
  sed -e "s|@INSTALL_DIR@|$INSTALL_DIR|g" -e "s|/usr/bin/docker|$docker_bin|g" "$SCRIPT_ROOT/deploy/systemd/market-bridge-server.service" > /etc/systemd/system/market-bridge-server.service
  chmod 644 /etc/systemd/system/market-bridge-server.service
  systemctl daemon-reload
  systemctl enable market-bridge-server.service >/dev/null
fi
log "server installed successfully in $INSTALL_DIR"

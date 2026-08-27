#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"

MODE="docker";INPUT_ENV="";VERSION="latest";LOCAL_BUILD=false
STATE_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/market-bridge"
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/market-bridge"
BIN_DIR="$HOME/.local/bin"

usage(){ cat <<'EOF'
Usage: ./scripts/install-client.sh --env PATH [--version v0.1.0] [--mode docker|native] [--local-build]
Docker mode installs go-client and the local Redis/ClickHouse services selected by COMPOSE_PROFILES. Native mode installs a verified release binary and systemd/launchd service.
EOF
}
while (($#));do case "$1" in --env)INPUT_ENV="${2:?}";shift 2;;--version)VERSION="${2:?}";shift 2;;--mode)MODE="${2:?}";shift 2;;--local-build)LOCAL_BUILD=true;shift;;-h|--help)usage;exit 0;;*)die "unknown argument: $1";;esac;done
[[ "$MODE" == "docker" || "$MODE" == "native" ]] || die "--mode must be docker or native"
[[ -n "$INPUT_ENV" ]] || die "--env is required"
need curl
INPUT_ENV="$(absolute_path "$INPUT_ENV")";load_env "$INPUT_ENV";validate_client_env
mkdir -p "$STATE_DIR" "$CONFIG_DIR" "$BIN_DIR";copy_file "$INPUT_ENV" "$CONFIG_DIR/.env" 600;ENV_FILE="$CONFIG_DIR/.env"
set_env_value "$ENV_FILE" MARKET_BRIDGE_VERSION "$VERSION";set_env_value "$ENV_FILE" GO_CLIENT_CACHE_DIRECTORY "$STATE_DIR/cache";printf '%s\n' "$MODE" >"$STATE_DIR/install-mode";printf '%s\n' "$VERSION" >"$STATE_DIR/version"

if [[ "$MODE" == "docker" ]];then
  need_docker;load_env "$ENV_FILE";validate_docker_client_env;copy_file "$SCRIPT_ROOT/deploy/compose.client.yaml" "$STATE_DIR/compose.yaml" 644;COMPOSE_FILE="$STATE_DIR/compose.yaml"
  set_env_value "$ENV_FILE" MARKET_BRIDGE_CLIENT_IMAGE "${MARKET_BRIDGE_CLIENT_IMAGE:-$DEFAULT_CLIENT_IMAGE}"
  if $LOCAL_BUILD;then docker build --target go-client -t market-bridge-client:local "$SCRIPT_ROOT";set_env_value "$ENV_FILE" MARKET_BRIDGE_VERSION local;set_env_value "$ENV_FILE" MARKET_BRIDGE_CLIENT_IMAGE market-bridge-client;set_env_value "$ENV_FILE" MARKET_BRIDGE_PULL_POLICY never
  else set_env_value "$ENV_FILE" MARKET_BRIDGE_PULL_POLICY always;load_env "$ENV_FILE";registry_login;compose_run pull;fi
  compose_run up -d --remove-orphans
  if ! wait_for_service go-client 120;then compose_run logs --tail=100 go-client >&2||true;die "go-client failed its health check";fi
else
  $LOCAL_BUILD&&die "--local-build is only supported in docker mode"
  os="$(uname -s | tr '[:upper:]' '[:lower:]')";arch="$(uname -m)";case "$arch" in x86_64)arch=amd64;;aarch64|arm64)arch=arm64;;*)die "unsupported architecture: $arch";;esac
  [[ "$os" == "linux" || "$os" == "darwin" ]] || die "native mode supports Linux and macOS"
  asset="market-client_${os}_${arch}.tar.gz";tmp="$(mktemp -d)";trap 'rm -rf "$tmp"' EXIT
  download "$(release_url "$VERSION" "$asset")" "$tmp/$asset";download "$(release_url "$VERSION" SHA256SUMS)" "$tmp/SHA256SUMS";verify_checksum "$tmp/SHA256SUMS" "$asset" "$tmp";tar -xzf "$tmp/$asset" -C "$tmp";install -m 755 "$tmp/market-client" "$BIN_DIR/market-client"
  if [[ "$os" == linux ]];then need systemctl;mkdir -p "$HOME/.config/systemd/user";copy_file "$SCRIPT_ROOT/deploy/systemd/market-bridge-client.service" "$HOME/.config/systemd/user/market-bridge-client.service" 644;systemctl --user daemon-reload;systemctl --user enable --now market-bridge-client.service
  else
    runner="$STATE_DIR/run-client.sh";logs="$STATE_DIR/logs";mkdir -p "$logs" "$HOME/Library/LaunchAgents";sed -e "s|@ENV_FILE@|$ENV_FILE|g" -e "s|@BINARY@|$BIN_DIR/market-client|g" "$SCRIPT_ROOT/deploy/launchd/run-client.sh" >"$runner";chmod 755 "$runner";plist="$HOME/Library/LaunchAgents/com.scott4game.market-bridge-client.plist";sed -e "s|@RUNNER@|$runner|g" -e "s|@LOG_DIR@|$logs|g" "$SCRIPT_ROOT/deploy/launchd/com.scott4game.market-bridge-client.plist" >"$plist";chmod 644 "$plist";launchctl bootout "gui/$UID/com.scott4game.market-bridge-client" >/dev/null 2>&1||true;launchctl bootstrap "gui/$UID" "$plist"
  fi
  wait_http http://127.0.0.1:17600/readyz 90||die "native go-client failed its readiness check"
fi
log "client installed in $MODE mode; KLineChart: http://127.0.0.1:17600"

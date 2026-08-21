#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
PURGE=false;STATE_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/market-bridge";CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/market-bridge";BIN_DIR="$HOME/.local/bin"
while (($#));do case "$1" in --purge-data)PURGE=true;shift;;-h|--help)echo "Usage: ./scripts/uninstall-client.sh [--purge-data]";exit 0;;*)die "unknown argument: $1";;esac;done
[[ -f "$STATE_DIR/install-mode" ]]||die "client installation not found";mode="$(<"$STATE_DIR/install-mode")"
if [[ "$mode" == docker ]];then need_docker;ENV_FILE="$CONFIG_DIR/.env";COMPOSE_FILE="$STATE_DIR/compose.yaml";if $PURGE;then compose_run down -v --remove-orphans;else compose_run down --remove-orphans;fi
else
  case "$(uname -s)" in Linux)systemctl --user disable --now market-bridge-client.service >/dev/null 2>&1||true;rm -f "$HOME/.config/systemd/user/market-bridge-client.service";systemctl --user daemon-reload||true;;Darwin)label=com.scott4game.market-bridge-client;launchctl bootout "gui/$UID/$label" >/dev/null 2>&1||true;rm -f "$HOME/Library/LaunchAgents/$label.plist";;esac
  rm -f "$BIN_DIR/market-client"
fi
if $PURGE;then safe_remove_tree "$STATE_DIR" "${XDG_DATA_HOME:-$HOME/.local/share}";safe_remove_tree "$CONFIG_DIR" "${XDG_CONFIG_HOME:-$HOME/.config}";log "client and cached data removed";else log "client stopped; configuration and cached data preserved";fi

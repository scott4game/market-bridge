#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
INSTALL_DIR="/opt/market-bridge";PURGE=false
while (($#));do case "$1" in --install-dir)INSTALL_DIR="${2:?}";shift 2;;--purge-data)PURGE=true;shift;;-h|--help)echo "Usage: sudo ./scripts/uninstall-server.sh [--install-dir PATH] [--purge-data]";exit 0;;*)die "unknown argument: $1";;esac;done
[[ $EUID -eq 0 ]] || die "server uninstall must run as root"
need_docker;ENV_FILE="$INSTALL_DIR/.env";COMPOSE_FILE="$INSTALL_DIR/compose.yaml"
[[ -f "$COMPOSE_FILE" && -f "$ENV_FILE" ]] || die "server installation not found"
if $PURGE;then compose_run down -v --remove-orphans;else compose_run down --remove-orphans;fi
if command -v systemctl>/dev/null 2>&1;then systemctl disable --now market-bridge-server.service >/dev/null 2>&1||true;rm -f /etc/systemd/system/market-bridge-server.service;systemctl daemon-reload||true;fi
if $PURGE;then safe_remove_tree "$INSTALL_DIR" /opt;log "server, configuration, and named volumes removed";else log "server stopped; configuration and data preserved in $INSTALL_DIR and Docker volumes";fi

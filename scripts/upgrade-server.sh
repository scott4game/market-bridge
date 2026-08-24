#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"

INSTALL_DIR="/opt/market-bridge"; VERSION=""
usage(){ echo "Usage: sudo ./scripts/upgrade-server.sh --version v0.2.0 [--install-dir PATH]"; }
while (($#)); do case "$1" in --version) VERSION="${2:?}";shift 2;;--install-dir) INSTALL_DIR="${2:?}";shift 2;;-h|--help)usage;exit 0;;*)die "unknown argument: $1";;esac;done
[[ $EUID -eq 0 ]] || die "server upgrade must run as root"
[[ -n "$VERSION" ]] || die "--version is required"
need_docker
ENV_FILE="$INSTALL_DIR/.env";COMPOSE_FILE="$INSTALL_DIR/compose.yaml";[[ -f "$COMPOSE_FILE" ]]||die "server is not installed in $INSTALL_DIR"
load_env "$ENV_FILE";validate_server_env;old_version="${MARKET_BRIDGE_VERSION:-unknown}";backup="$INSTALL_DIR/.env.backup.$(date +%Y%m%d%H%M%S)";cp -p "$ENV_FILE" "$backup"
copy_file "$SCRIPT_ROOT/deploy/compose.server.yaml" "$COMPOSE_FILE" 644
set_env_value "$ENV_FILE" MARKET_BRIDGE_VERSION "$VERSION";set_env_value "$ENV_FILE" MARKET_BRIDGE_PULL_POLICY always;load_env "$ENV_FILE";registry_login
if compose_run pull && compose_run up -d --remove-orphans && wait_for_service go-server 180;then log "server upgraded from $old_version to $VERSION";exit 0;fi
warn "upgrade failed; rolling back to $old_version";cp -p "$backup" "$ENV_FILE";load_env "$ENV_FILE";compose_run pull || true;compose_run up -d --remove-orphans || true;wait_for_service go-server 180||true;die "upgrade failed and rollback was attempted"

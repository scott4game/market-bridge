#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
VERSION="";STATE_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/market-bridge";CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/market-bridge"
while (($#));do case "$1" in --version)VERSION="${2:?}";shift 2;;-h|--help)echo "Usage: ./scripts/upgrade-client.sh --version v0.2.0";exit 0;;*)die "unknown argument: $1";;esac;done
[[ -n "$VERSION" ]] || die "--version is required"
[[ -f "$STATE_DIR/install-mode" && -f "$CONFIG_DIR/.env" ]] || die "client installation not found"
mode="$(<"$STATE_DIR/install-mode")";old="$(<"$STATE_DIR/version")"
if "$SCRIPT_ROOT/scripts/install-client.sh" --env "$CONFIG_DIR/.env" --version "$VERSION" --mode "$mode";then log "client upgraded from $old to $VERSION";else warn "client upgrade failed; reinstalling $old";"$SCRIPT_ROOT/scripts/install-client.sh" --env "$CONFIG_DIR/.env" --version "$old" --mode "$mode"||true;die "upgrade failed and rollback was attempted";fi

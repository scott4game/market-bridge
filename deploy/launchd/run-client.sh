#!/usr/bin/env bash
set -euo pipefail
set -a
# shellcheck disable=SC1090
source "@ENV_FILE@"
set +a
exec "@BINARY@" serve

#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
VERSION="${1:-}"
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9.-]+)?$ ]]||die "usage: ./scripts/release.sh vX.Y.Z"
need git
[[ -z "$(git status --porcelain)" ]]||die "working tree is not clean"
git fetch origin main --tags
[[ "$(git branch --show-current)" == main ]]||die "release must be created from main"
git merge-base --is-ancestor origin/main HEAD||die "local main does not contain origin/main"
git rev-parse "$VERSION" >/dev/null 2>&1&&die "tag already exists: $VERSION"
git tag -s "$VERSION" -m "Release $VERSION"
git push origin "$VERSION"
log "tag pushed; GitHub Actions will publish binaries and images"

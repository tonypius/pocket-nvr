#!/usr/bin/env bash
# Cross-build nvrd for the phone (linux/arm64, fully static, CGO-free) and
# validate the exact artifact in an arm64 Linux container (DT-4).
# Usage: scripts/build_arm64.sh [--docker]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
mkdir -p "$DIST"

echo "== build linux/arm64 (CGO_ENABLED=0 → static)"
cd "$ROOT"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags="-s -w" -o "$DIST/nvrd_linux_arm64" ./cmd/nvrd
file "$DIST/nvrd_linux_arm64"
ls -lh "$DIST/nvrd_linux_arm64"

if [[ "${1:-}" == "--docker" ]]; then
  echo "== validate in linux/arm64 container"
  docker run --rm --platform linux/arm64 \
    -v "$DIST":/x:ro -v "$ROOT/config":/config:ro \
    alpine:3 /bin/sh -c "
      /x/nvrd_linux_arm64 -version &&
      /x/nvrd_linux_arm64 -config /config -check &&
      echo DOCKER_ARM64_OK"
fi

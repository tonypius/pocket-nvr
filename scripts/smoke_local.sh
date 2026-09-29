#!/usr/bin/env bash
# Local smoke run (Phase 1 dev loop):
#   1. nvrd validates config.yaml + secrets.yaml and exits (-check)
#   2. nvrd generates mediamtx.yml
#   3. MediaMTX starts with the generated config (validates keys) and is
#      killed after 5s — success = clean startup logs, no config errors.
# Usage: scripts/smoke_local.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CFG="$ROOT/config"
DIST="$ROOT/dist"
MTX="$ROOT/third_party/mediamtx/current/darwin_arm64/mediamtx"

mkdir -p "$DIST"
echo "== 1. nvrd -check"
go build -o "$DIST/nvrd" "$ROOT/cmd/nvrd"
"$DIST/nvrd" -config "$CFG" -check

echo "== 2. generate mediamtx.yml"
"$DIST/nvrd" -config "$CFG" -mediamtx-config "$DIST/mediamtx.yml"
sed -n '1,5p;30,200p' "$DIST/mediamtx.yml" | sed 's/pass: .*/pass: ***/'

echo "== 3. mediamtx startup check (config validation)"
# MediaMTX exits immediately on bad config; source-dial errors (camera
# offline) are expected noise and must NOT fail this test.
"$MTX" "$DIST/mediamtx.yml" > "$DIST/mediamtx.log" 2>&1 &
MTX_PID=$!
sleep 5
CONFIG_OK=0
if kill -0 "$MTX_PID" 2>/dev/null; then
  CONFIG_OK=1
  kill "$MTX_PID" 2>/dev/null || true
  wait "$MTX_PID" 2>/dev/null || true
fi
if [ "$CONFIG_OK" != "1" ]; then
  echo "FAIL: mediamtx exited (config rejected) — log:"; cat "$DIST/mediamtx.log"; exit 1
fi
grep -E "started with listener" "$DIST/mediamtx.log" | head -3
for want in "RTSP" "HLS" "WebRTC"; do
  grep -q "\[$want\] started" "$DIST/mediamtx.log" || { echo "FAIL: $want listener missing"; exit 1; }
done
echo "OK: local smoke passed (full log: $DIST/mediamtx.log)"

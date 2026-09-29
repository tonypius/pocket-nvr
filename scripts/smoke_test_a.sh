#!/usr/bin/env bash
# Phase 0 smoke test A (PLAN.md): prove the runtime model — a static Go
# binary runs as root on the phone, binds a port, and serves requests.
# Usage: scripts/smoke_test_a.sh   (device must be reachable via adb)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
STAGE=/data/local/tmp/pocketnvr-smoke
command -v adb >/dev/null || { echo "adb not installed"; exit 1; }
adb get-state >/dev/null 2>&1 || { echo "no device: 'adb connect <phone>:5555' first"; exit 1; }

echo "== building hello (linux/arm64 static)"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -C "$ROOT" -trimpath -o "$DIST/hello_arm64" ./cmd/smoke/hello

# Device-side runner: multiline scripts cannot be inlined through
# `adb shell su -c` (quoting breaks) — always push a script file.
cat > "$DIST/hello_run.sh" <<'EOF'
#!/system/bin/sh
chmod 755 /data/local/tmp/pocketnvr-smoke/hello_arm64
kill "$(cat /data/local/tmp/pocketnvr-smoke/hello.pid 2>/dev/null)" 2>/dev/null
nohup /data/local/tmp/pocketnvr-smoke/hello_arm64 >/data/local/tmp/pocketnvr-smoke/hello.log 2>&1 &
echo $! > /data/local/tmp/pocketnvr-smoke/hello.pid
sleep 1
cat /data/local/tmp/pocketnvr-smoke/hello.pid
EOF

echo "== pushing and running on device as root"
adb shell "rm -rf $STAGE && mkdir -p $STAGE"
adb push "$DIST/hello_arm64" "$STAGE/hello_arm64" >/dev/null
adb push "$DIST/hello_run.sh" "$STAGE/hello_run.sh" >/dev/null
PID=$(adb shell su -c "sh $STAGE/hello_run.sh" | tr -d '\r' | grep -E '^[0-9]+$' | head -1)
[ -n "$PID" ] || { echo "FAIL: could not determine hello pid (su denied?)"; exit 1; }
echo "device pid: $PID"

adb forward tcp:18099 tcp:18099 >/dev/null
sleep 1
echo "== querying via adb forward"
RESP=$(curl -sf --max-time 5 http://127.0.0.1:18099/) || {
	echo "FAIL: no response from hello server (log:)"; adb shell su -c "cat $STAGE/hello.log"; exit 1; }
echo "$RESP"
adb shell su -c "kill $PID" 2>/dev/null || true

echo "$RESP" | grep -q '"root": *true' && echo "SMOKE A: PASS — static binary runs as root on device" \
	|| { echo "SMOKE A: FAIL — binary ran but not as root"; exit 1; }
echo "Runtime model (ENV-7) proven. Next: scripts/deploy.sh --start"

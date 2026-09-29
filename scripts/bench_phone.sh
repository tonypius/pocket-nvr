#!/usr/bin/env bash
# Steady-state inference benchmark on the phone (NFR-6 benchmark writeup).
# Assumes models + frame already staged by scripts/smoke_test_b (or pushes).
# Usage: scripts/bench_phone.sh [repeats]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
D=/data/local/tmp/pocketnvr-smoke-b
REPEATS="${1:-30}"

command -v adb >/dev/null || { echo "adb not installed"; exit 1; }
adb get-state >/dev/null 2>&1 || { echo "no device"; exit 1; }

CGO_ENABLED=0 GOOS=linux GOARCH=arm64 true # no-op placeholder for symmetry
(cd "$ROOT/detector" && cmake --build build-android-ncnn --target vulkan_smoke -j 8 >/dev/null)
adb shell "mkdir -p $D" >/dev/null
adb push "$ROOT/detector/build-android-ncnn/vulkan_smoke" "$D/" >/dev/null
[ -f "$D/bus.bgr" ] || adb push "$ROOT/models/bus.bgr" "$D/" >/dev/null
[ -f "$D/yolo11n.param" ] || adb push "$ROOT/models/yolo11n/yolo11n.param" "$ROOT/models/yolo11n/yolo11n.bin" "$D/" >/dev/null

cat > "$DIST/bench.sh" <<EOF
#!/system/bin/sh
D=$D
chmod 755 \$D/vulkan_smoke
echo "== vulkan steady-state (\$REPEATS runs):"
\$D/vulkan_smoke \$D \$D/bus.bgr 640 640 vulkan \$REPEATS | tail -2
echo "== cpu steady-state (\$REPEATS runs):"
\$D/vulkan_smoke \$D \$D/bus.bgr 640 640 cpu \$REPEATS | tail -2
EOF
adb push "$DIST/bench.sh" "$D/" >/dev/null
adb shell su -c "sh $D/bench.sh"

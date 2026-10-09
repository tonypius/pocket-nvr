#!/usr/bin/env bash
# Capture the on-phone GPU-vs-CPU benchmark to bench-results/ for
# publishing (BLOG-OUTLINE §1/§7): runs scripts/bench_phone.sh with 60
# repeats, frames it with device metadata + battery temp, and scrubs PII
# (adb serial, paths) before the file ever lands in bench-results/.
# Usage: scripts/bench_publish.sh [repeats]   (default 60)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="$ROOT/bench-results"
REPEATS="${1:-60}"

command -v adb >/dev/null || { echo "adb not installed"; exit 1; }
adb get-state >/dev/null 2>&1 || { echo "no device — plug in the phone and accept the USB-debugging prompt"; exit 1; }
mkdir -p "$OUT_DIR"

DEV=$(adb shell getprop ro.product.device | tr -d '\r')
MODEL=$(adb shell getprop ro.product.model | tr -d '\r')
SOC=$(adb shell getprop ro.soc.model | tr -d '\r'); [ -n "$SOC" ] || SOC=$(adb shell getprop ro.board.platform | tr -d '\r')
ANDROID=$(adb shell getprop ro.build.version.release | tr -d '\r')
TEMP0=$(adb shell dumpsys battery | awk '/temperature/{print int($2/10)}' | tr -d '\r')

OUT="$OUT_DIR/bench-phone-$(date +%Y%m%d).txt"
{
  echo "# PocketNVR inference benchmark — $(date '+%F %T') local"
  echo "# device=$MODEL ($DEV) soc=$SOC android=$ANDROID"
  echo "# model=yolo11n input=640x640 backend runs=$REPEATS (steady-state = median of last half)"
  echo "# battery_temp_start=${TEMP0}C"
  echo "# reproduce: scripts/bench_phone.sh $REPEATS"
} > "$OUT"

scripts/bench_phone.sh "$REPEATS" 2>&1 | tee "$OUT.raw"

TEMP1=$(adb shell dumpsys battery | awk '/temperature/{print int($2/10)}' | tr -d '\r')
echo "# battery_temp_end=${TEMP1}C" >> "$OUT"
sed -n '/== vulkan/,$p' "$OUT.raw" >> "$OUT"   # raw bench output, minus the build noise
rm -f "$OUT.raw"

SERIALS=$(adb devices | awk 'NR>1 && $2=="device"{print $1}')
scripts/scrub_for_publish.py --in-place --extra $SERIALS "$OUT"
echo "== scrubbed output written: $OUT"

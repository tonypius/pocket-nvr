#!/usr/bin/env bash
# End-to-end detector test on the Mac (PLAN.md Phase 3 exit check):
#   mediamtx (publish-only test path)
#   ← ffmpeg loops bus.jpg (person) 3s / blank 3s  = a "camera" with motion
#   nvrd (API + store)  ← nvrdet posts events
#   nvrdet (detector daemon): capture → gate → infer → state machine → POST
# Success: /api/events shows closed events with snapshots on disk.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUN=/tmp/pocketnvr-e2e
rm -rf "$RUN" && mkdir -p "$RUN"
TOKEN=devtoken-0123456789abcdef
MTX="$ROOT/third_party/mediamtx/current/darwin_arm64/mediamtx"

# person test frame: ultralytics sample shipped in the export venv
BUS="$ROOT/models/bus.jpg"
if [ ! -f "$BUS" ]; then
  cp "$(find "$ROOT/models/.venv" -name bus.jpg | head -1)" "$BUS"
fi

cleanup() {
	pkill -f "dist/nvrd " 2>/dev/null || true
	pkill -f "build/nvrdet" 2>/dev/null || true
	pkill -f "front_tapo_sub" 2>/dev/null || true
	pkill -f "darwin_arm64/mediamtx" 2>/dev/null || true
}
trap cleanup EXIT

cleanup
# an adb forward on 8099 would silently tunnel these curls to the phone
adb forward --remove tcp:8099 >/dev/null 2>&1 || true
sleep 1

echo "== building nvrd + nvrdet"
(cd "$ROOT" && go build -o dist/nvrd ./cmd/nvrd)

cleanup() {
	pkill -f "$RUN" 2>/dev/null || true
	pkill -f "dist/nvrd " 2>/dev/null || true
	pkill -f "nvrdet -c $RUN" 2>/dev/null || true
	pkill -f "rtsp://127.0.0.1:8554/front_tapo_sub" 2>/dev/null || true
	pkill -f "$MTX" 2>/dev/null || true
}
trap cleanup EXIT

cat > "$RUN/mediamtx.yml" <<EOF
paths:
  front_tapo:
    record: yes
    recordPath: $ROOT/data/recordings/%path/%Y-%m-%d_%H-%M-%S-%f
    recordFormat: fmp4
    recordSegmentDuration: 2s
    recordDeleteAfter: 1h
  front_tapo_sub:
EOF
"$MTX" "$RUN/mediamtx.yml" >"$RUN/mediamtx.log" 2>&1 &
sleep 1

echo "== building synthetic camera clip (moving person 3s / still blank 3s)"
TMPCLIP=/tmp/pocketnvr-e2e-clip
rm -rf "$TMPCLIP" && mkdir -p "$TMPCLIP"
# zoompan gives the person segment continuous motion (a real walking person),
# the blank segment is still — so the gate sees exactly real-camera motion.
ffmpeg -y -loglevel error -loop 1 -t 3 -i "$BUS" \
  -vf "scale=960:540,zoompan=z='min(1+0.006*on,1.35)':x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)':d=1:s=640x360,fps=10,format=yuv420p" \
  -c:v libx264 -preset ultrafast "$TMPCLIP/person.mp4"
ffmpeg -y -loglevel error -f lavfi -t 3 -i "color=c=0x404040:s=640x360" -vf "fps=10,format=yuv420p" -c:v libx264 -preset ultrafast "$TMPCLIP/blank.mp4"
printf "file 'person.mp4'\nfile 'blank.mp4'\n" > "$TMPCLIP/list.txt"
ffmpeg -y -loglevel error -f concat -safe 0 -i "$TMPCLIP/list.txt" -c copy "$TMPCLIP/loop.mp4"

echo "== publishing synthetic camera (main = recorded, sub = analyzed)"
ffmpeg -loglevel error -re -stream_loop -1 -i "$TMPCLIP/loop.mp4" \
  -c:v libx264 -preset ultrafast -tune zerolatency -g 10 \
  -f rtsp "rtsp://127.0.0.1:8554/front_tapo" >"$RUN/publish.log" 2>&1 &
ffmpeg -loglevel error -re -stream_loop -1 -i "$TMPCLIP/loop.mp4" \
  -c:v libx264 -preset ultrafast -tune zerolatency -g 10 \
  -f rtsp "rtsp://127.0.0.1:8554/front_tapo_sub" >"$RUN/publish2.log" 2>&1 &

echo "== nvrd"
"$ROOT/dist/nvrd" -config "$ROOT/config" >"$RUN/nvrd.log" 2>&1 &
sleep 1
"$ROOT/dist/nvrd" -config "$ROOT/config" -detector-config "$RUN/detector.json"
# point the detector at the synthetic camera; absolute model dir for dev
python3 - "$RUN/detector.json" "$ROOT/models/yolo11n" <<'PY'
import json, sys
p = sys.argv[1]
c = json.load(open(p))
c["cameras"][0]["sub_url"] = "rtsp://127.0.0.1:8554/front_tapo_sub"
c["cameras"][0]["enter_frames"] = 2
c["cameras"][0]["exit_frames"] = 5
c["detection"]["model_dir"] = sys.argv[2]
c["detection"]["backend"] = "cpu"  # MoltenVK off-path keeps the test hermetic
json.dump(c, open(p, "w"), indent=1)
PY

echo "== nvrdet"
"$ROOT/detector/build/nvrdet" -c "$RUN/detector.json" >"$RUN/nvrdet.log" 2>&1 &
sleep 25

echo "== events recorded:"
curl -s "http://127.0.0.1:8099/api/events?token=$TOKEN" | python3 -c '
import json, sys
evs = json.load(sys.stdin)
print("count:", len(evs))
for e in evs[:3]:
    print("  cam=%s score=%.2f span=%sms notified=%s snap=%s" % (
        e["camera_id"], e["score"],
        (e["end_ts"] or 0) - e["start_ts"], e["notified"],
        (e.get("snapshot_path") or "")[-24:]))
assert len(evs) >= 1, "NO EVENTS — detector pipeline broken"
assert evs[0]["snapshot_path"], "no snapshot persisted"
'
echo "== snapshot serve check:"
FIRST=$(curl -s "http://127.0.0.1:8099/api/events?token=$TOKEN" | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["id"])')
curl -s -o /dev/null -w "snapshot HTTP %{http_code} type %{content_type}\n" "http://127.0.0.1:8099/api/events/$FIRST/snapshot?token=$TOKEN"
echo "== clip extraction check (FR-EVT-3):"
sleep 6  # extractor queue is async
curl -s -o "$RUN/clip.mp4" -w "clip HTTP %{http_code} type %{content_type} bytes %{size_download}\n" \
  "http://127.0.0.1:8099/api/events/$FIRST/clip?token=$TOKEN"
ffprobe -v error -show_entries format=duration -of csv=p=0 "$RUN/clip.mp4" && echo "clip plays ✓"
echo "== detector gauges:"
curl -s "http://127.0.0.1:8099/api/metrics?token=$TOKEN" | python3 -m json.tool | head -20
echo "== nvrdet log tail:"
tail -5 "$RUN/nvrdet.log"
echo "E2E: PASS"

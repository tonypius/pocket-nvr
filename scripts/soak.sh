#!/usr/bin/env bash
# Soak harness (DoD §11, NFR-4): run the pipeline for a duration, sample
# gauges + RSS every --sample-secs, verify stability at the end.
#
#   local mode (default): full synthetic stack on the Mac — N cameras
#     (person/blank motion loop over RTSP), recording on, detector on.
#     Config dir is generated with an EMPTY ntfy_topic_url so the notifier
#     has zero providers and nothing ever leaves the machine.
#   phone mode (--target phone): READ-ONLY sampling of the deployed
#     production stack via `adb forward` (metrics + /proc RSS + SoC temp).
#     The harness never restarts anything on the phone.
#
# The daemon-kill chaos check (kill -9 nvrdet mid-run, watch it come back)
# is local-only and can be disabled with --no-kill-test.
#
# Usage:
#   scripts/soak.sh [--duration 1h] [--cameras 4] [--sample-secs 30]
#                   [--target local|phone] [--no-kill-test] [--out DIR]
# Output: <out>/metrics.tsv + <out>/summary.txt
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DURATION=1h CAMERAS=4 SAMPLE=30 TARGET=local KILL_TEST=1 OUT=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --duration) DURATION="$2"; shift 2 ;;
    --cameras) CAMERAS="$2"; shift 2 ;;
    --sample-secs) SAMPLE="$2"; shift 2 ;;
    --target) TARGET="$2"; shift 2 ;;
    --no-kill-test) KILL_TEST=0; shift ;;
    --out) OUT="$2"; shift 2 ;;
    *) echo "unknown flag $1"; exit 2 ;;
  esac
done

dur_s() { python3 -c "
import sys, re
s = sys.argv[1]
m = re.fullmatch(r'(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s?)?', s)
h, mi, sec = (int(x) if x else 0 for x in m.groups())
assert h or mi or sec, 'bad duration'
print(h*3600 + mi*60 + sec)" "$1"; }
DUR=$(dur_s "$DURATION")
RUN=$(mktemp -d /tmp/pocketnvr-soak.XXXX)
OUT="${OUT:-$RUN}"; mkdir -p "$OUT"
TSV="$OUT/metrics.tsv"
TS=$(date +%Y%m%d-%H%M%S)
TOKEN=devtoken-0123456789abcdef

echo "== soak: target=$TARGET cameras=$CAMERAS duration=${DUR}s sample=${SAMPLE}s out=$OUT"

rss_of() { # rss_of <pgrep pattern> -> KB
  local s=0 r
  for p in $(pgrep -f "$1" 2>/dev/null); do
    r=$(ps -o rss= -p "$p" 2>/dev/null || echo 0); s=$((s + ${r:-0}))
  done
  echo $s
}

cleanup() {
  if [ "$TARGET" = local ]; then
    pkill -f "nvrdet -c $RUN/detector.json" 2>/dev/null || true
    pkill -f "dist/nvrd -config $RUN/config" 2>/dev/null || true
    pkill -f "mediamtx $RUN/mediamtx.yml" 2>/dev/null || true
    pkill -f "rtsp://127.0.0.1:8554/soak" 2>/dev/null || true
  else
    adb forward --remove tcp:18099 >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

METRICS_URL="http://127.0.0.1:8099/api/metrics"
RSS_MODE=local

if [ "$TARGET" = phone ]; then
  adb get-state >/dev/null 2>&1 || { echo "no device (adb unauthorized?)"; exit 1; }
  adb forward tcp:18099 tcp:8099 >/dev/null
  METRICS_URL="http://127.0.0.1:18099/api/metrics"
  # real token lives on the phone; never echoed, never written to $OUT
  TOKEN=$(adb shell su -c "grep '^api_token:' /data/nvr/config/secrets.yaml" \
          | head -1 | tr -d '\r' | awk '{print $2}')
  [ -n "$TOKEN" ] || { echo "no api_token on device"; exit 1; }
  RSS_MODE=phone
  KILL_TEST=0
else
  MTX="$ROOT/third_party/mediamtx/current/darwin_arm64/mediamtx"
  [ -x "$MTX" ] || { echo "run scripts/fetch-mediamtx.sh first"; exit 1; }
  BUS="$ROOT/models/bus.jpg"
  [ -f "$BUS" ] || BUS="$(find "$ROOT/models/.venv" -name bus.jpg | head -1)"
  [ -n "$BUS" ] || { echo "run scripts/fetch_model.sh first"; exit 1; }

  echo "== building nvrd + nvrdet"
  (cd "$ROOT" && go build -o dist/nvrd ./cmd/nvrd)
  [ -x "$ROOT/detector/build/nvrdet" ] || (cd "$ROOT/detector" && cmake --build build -j 8)

  # isolated config: empty ntfy url => notify has no providers => no outbound
  mkdir -p "$RUN/config" "$RUN/recordings"
  CAMYAML=""
  PATHSYAML=""
  for i in $(seq 1 "$CAMERAS"); do
    CAMYAML+="  - id: soak$i
    name: Soak $i
    enabled: true
    main_url: rtsp://127.0.0.1:8554/soak$i
    sub_url: rtsp://127.0.0.1:8554/soak${i}_sub
    source_main: rtsp://\${secret:soak_user}:\${secret:soak_pass}@127.0.0.1:8554/soak$i
    source_sub: rtsp://\${secret:soak_user}:\${secret:soak_pass}@127.0.0.1:8554/soak${i}_sub
    detect: {fps: 5, threshold: 0.5, enter_frames: 3, exit_frames: 8, motion_min_area: 0.02, anchor: bottom_center, zones: []}
"
    PATHSYAML+="  soak$i:
    record: yes
    recordPath: $RUN/recordings/%path/%Y-%m-%d_%H-%M-%S-%f
    recordFormat: fmp4
    recordSegmentDuration: 2s
    recordDeleteAfter: 1h
  soak${i}_sub:
"
  done
  cat > "$RUN/config/config.yaml" <<EOF
system: {base_path: $RUN/data, log_level: info, wakelock: true, charge_cap_pct: 65, storage_min_free_gb: 1}
media: {rtsp_address: ":8554", webrtc_address: ":8889", hls_address: ":8888", auth_user: viewer}
cameras:
$CAMYAML
recording: {mode: continuous, segment_seconds: 300, retention_days: 7, size_cap_gb: 4}
events: {clip_pre_seconds: 5, clip_post_seconds: 10, retention_days: 30, retention_max_count: 5000}
detection: {model: yolo11n, input_size: 640, backend: cpu, queue_max: 32, temp_high_c: 75, temp_low_c: 65}
notifications: {provider: ntfy, cooldown_seconds: 60, base_url: ""}
api: {bind: "127.0.0.1:8099", auth: token}
remote: {tailscale: false}
EOF
  cat > "$RUN/config/secrets.yaml" <<EOF
api_token: $TOKEN
mediamtx_viewer_pass: devviewerpass
soak_user: devuser
soak_pass: devpass
ntfy_topic_url: ""
telegram_bot_token: ""
telegram_chat_id: ""
EOF
  cat > "$RUN/mediamtx.yml" <<EOF
paths:
$PATHSYAML
EOF
  chmod 600 "$RUN/config/secrets.yaml"

  echo "== synthetic clip (moving person 3s / still blank 3s)"
  TMPCLIP=$RUN/clip; mkdir -p "$TMPCLIP"
  ffmpeg -y -loglevel error -loop 1 -t 3 -i "$BUS" \
    -vf "scale=960:540,zoompan=z='min(1+0.006*on,1.35)':x='iw/2-(iw/zoom/2)':y='ih/2-(ih/zoom/2)':d=1:s=640x360,fps=10,format=yuv420p" \
    -c:v libx264 -preset ultrafast "$TMPCLIP/person.mp4"
  ffmpeg -y -loglevel error -f lavfi -t 3 -i "color=c=0x404040:s=640x360" \
    -vf "fps=10,format=yuv420p" -c:v libx264 -preset ultrafast "$TMPCLIP/blank.mp4"
  printf "file 'person.mp4'\nfile 'blank.mp4'\n" > "$TMPCLIP/list.txt"
  ffmpeg -y -loglevel error -f concat -safe 0 -i "$TMPCLIP/list.txt" -c copy "$TMPCLIP/loop.mp4"

  echo "== starting stack (mediamtx, $CAMERAS cameras, nvrd, nvrdet)"
  "$MTX" "$RUN/mediamtx.yml" > "$RUN/mediamtx.log" 2>&1 &
  for i in $(seq 1 "$CAMERAS"); do
    sleep $(( (i*3) % 6 ))  # stagger person/blank phases across cameras
    ffmpeg -loglevel error -re -stream_loop -1 -i "$TMPCLIP/loop.mp4" \
      -c:v libx264 -preset ultrafast -tune zerolatency -g 10 \
      -f rtsp "rtsp://127.0.0.1:8554/soak$i" > "$RUN/pub$i.log" 2>&1 &
    ffmpeg -loglevel error -re -stream_loop -1 -i "$TMPCLIP/loop.mp4" \
      -c:v libx264 -preset ultrafast -tune zerolatency -g 10 \
      -f rtsp "rtsp://127.0.0.1:8554/soak${i}_sub" >> "$RUN/pub$i.log" 2>&1 &
  done
  (cd "$ROOT" && ./dist/nvrd -config "$RUN/config" > "$RUN/nvrd.log" 2>&1 &)
  sleep 1
  "$ROOT/dist/nvrd" -config "$RUN/config" -detector-config "$RUN/detector.json" >/dev/null
  python3 - "$RUN/detector.json" "$ROOT/models/yolo11n" <<'PY'
import json, sys
p, model_dir = sys.argv[1], sys.argv[2]
c = json.load(open(p))
for cam in c["cameras"]:
    cam["sub_url"] = f"rtsp://127.0.0.1:8554/{cam['id']}_sub"
    cam["enter_frames"] = 2
    cam["exit_frames"] = 5
c["detection"]["model_dir"] = model_dir
c["detection"]["backend"] = "cpu"  # hermetic on the Mac (MoltenVK off-path)
json.dump(c, open(p, "w"), indent=1)
PY
  "$ROOT/detector/build/nvrdet" -c "$RUN/detector.json" > "$RUN/nvrdet.log" 2>&1 &
  echo "== settling 20s"
  sleep 20
fi

start_nvc() { # local: relaunch nvrdet after the kill-test
  "$ROOT/detector/build/nvrdet" -c "$RUN/detector.json" >> "$RUN/nvrdet.log" 2>&1 &
}

phone_rss() { # one su call: Name+VmRSS for each daemon
  adb shell su -c "cat /proc/\$(pidof nvrdet)/status /proc/\$(pidof nvrd)/status /proc/\$(pidof mediamtx)/status" 2>/dev/null \
    | tr -d '\r' | awk '/^Name:/{n=$2} /^VmRSS:/{print n, $2}'
}

echo "ts	elapsed_s	uptime_s	events_total	inference_ms	queue_depth	dropped_frames	fps_scale	temp_c	rss_nvrdet_kb	rss_nvrd_kb	rss_mediamtx_kb	rss_publishers_kb	nvrdet_up	note" > "$TSV"
START=$(date +%s); RESTARTS=0; ELAPSED=0
while [ "$ELAPSED" -lt "$DUR" ]; do
  NOTE=""
  M=$(curl -s --max-time 10 "$METRICS_URL?token=$TOKEN" || true)
  if [ "$RSS_MODE" = phone ]; then
    RSS=$(phone_rss | awk '{a[$1]=$2} END{printf "%s %s %s", a["nvrdet"]+0, a["nvrd"]+0, a["mediamtx"]+0}')
    PUBRSS=0
  else
    RSS="$(rss_of "nvrdet -c $RUN/detector.json") $(rss_of "dist/nvrd -config $RUN/config") $(rss_of "mediamtx $RUN/mediamtx.yml")"
    PUBRSS=$(rss_of "rtsp://127.0.0.1:8554/soak")
  fi
  set -- $RSS; RNVDET=${1:-0}; RNVRD=${2:-0}; RMTX=${3:-0}
  UP=1; [ "$RNVDET" -gt 0 ] 2>/dev/null || UP=0

  # daemon-kill chaos check once, at 60% of the run (local only)
  if [ "$KILL_TEST" = 1 ] && [ "$RESTARTS" = 0 ] && [ "$ELAPSED" -ge $((DUR*6/10)) ]; then
    NOTE="kill-test"
    pkill -9 -f "nvrdet -c $RUN/detector.json" || true
    sleep 10
    start_nvc
    RESTARTS=1
  fi

  python3 - "$TSV" "$M" "$ELAPSED" "$RNVDET" "$RNVRD" "$RMTX" "$PUBRSS" "$UP" "$NOTE" <<'PY'
import json, sys, time
tsv, raw, elapsed, rn, rv, rm, rp, up, note = sys.argv[1:10]
g = {}
try:
    d = json.loads(raw)
    det = d.get("detector") or {}
    g = [d.get("uptime_s",0), d.get("events_total",0), det.get("inference_ms",0),
         det.get("queue_depth",0), det.get("dropped_frames",0),
         det.get("fps_scale",0), det.get("temp_c",0)]
    g = [f"{x:g}" if isinstance(x,float) else x for x in g]
except Exception:
    g = ["-"]*7
with open(tsv, "a") as f:
    f.write("\t".join([time.strftime("%F_%T"), str(elapsed)] + [str(x) for x in g]
            + [rn, rv, rm, rp, up, note]) + "\n")
PY
  sleep "$SAMPLE"
  ELAPSED=$(( $(date +%s) - START ))
done

echo "== run complete; summary"
REV=$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)
python3 - "$TSV" "$OUT/summary.txt" "$TARGET" "$CAMERAS" "$DUR" "$SAMPLE" "$RESTARTS" "$REV" "$(uname -srm)" <<'PY'
import sys
tsv, out, target, cams, dur, sample, restarts, rev, kernel = sys.argv[1:10]
rows = [l.rstrip("\n").split("\t") for l in open(tsv) if l.startswith("20")]
def col(name): return [r[col_i[name]] for r in rows]
hdr = rows[0] if False else open(tsv).readline().rstrip("\n").split("\t")
col_i = {h: i for i, h in enumerate(hdr)}
data = [r for r in rows if len(r) == len(hdr)]
def nums(name):
    v = []
    for x in col(name):
        try: v.append(float(x))
        except ValueError: pass
    return v
inf, rssn, ev = nums("inference_ms"), nums("rss_nvrdet_kb"), nums("events_total")
qmax = max(nums("queue_depth") or [0]); drops = max(nums("dropped_frames") or [0])
temp = nums("temp_c")
def pct(a, p):
    s = sorted(a); return s[min(len(s)-1, int(len(s)*p))] if s else 0
drift = ((rssn[-1]-rssn[0])/rssn[0]*100) if rssn and rssn[0] else 0
ok = (ev and ev[-1] > 0) and len(data) >= (int(dur)/int(sample))*0.8 and abs(drift) <= 20
L = []
L.append(f"PocketNVR soak summary — target={target} cameras={cams} duration={dur}s sample={sample}s")
L.append(f"git={rev} kernel={kernel} restarts(kill-test)={restarts} samples={len(data)}")
L.append(f"events_total: first={ev[0] if ev else '-'} last={ev[-1] if ev else '-'}")
L.append(f"inference_ms: p50={pct(inf,0.5):.1f} p95={pct(inf,0.95):.1f} max={pct(inf,1.0):.1f}")
L.append(f"queue_depth max={qmax:g} dropped_frames total={drops:g} fps_scale min={min(nums('fps_scale') or [0]):g}")
if temp: L.append(f"temp_c: min={min(temp):.1f} max={max(temp):.1f}")
if rssn: L.append(f"rss_nvrdet_kb: first={rssn[0]:.0f} last={rssn[-1]:.0f} max={max(rssn):.0f} drift={drift:+.1f}%")
L.append(f"VERDICT: {'PASS' if ok else 'FAIL'} (events>0, samples>=80% expected, |rss drift|<=20%)")
text = "\n".join(L)
open(out, "w").write(text + "\n")
print(text)
PY

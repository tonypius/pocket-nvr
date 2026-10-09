# Measurement data — benchmarks & soak results

Raw (PII-scrubbed) measurement data backing the blog writeup. Every file
here is produced by a script in `scripts/` — no hand-typed numbers.

| Artifact | Produced by | Status |
|---|---|---|
| `bench-phone-20261009.txt` | `scripts/bench_publish.sh` (wraps `scripts/bench_phone.sh`) | captured 2026-10-09 |
| `soak-local-4cam-1h/` | `scripts/soak.sh --duration 1h --cameras 4` | 1 h local run, 2026-10-09 |
| `soak-phone-sample/` | `scripts/soak.sh --target phone` (read-only) | 10 min production sample, 2026-10-09 |
| `device-logs/` | pulled from `/data/nvr/logs/` + scrubbed | 2026-09-08 → 2026-10-09 |

## PII policy

Artifacts are scrubbed by `scripts/scrub_for_publish.py` **before** landing
here: home paths, LAN IPs (loopback kept), tokens/bearer values, MACs,
ntfy topics, Telegram chat ids, and adb serials are redacted. Snapshots
and recordings are never committed. If a future artifact adds a new
identifier class, extend the scrubber first, then capture.

## Inference benchmark (GPU vs CPU) — methodology

- Model: YOLO11n (PNNX → NCNN export per `scripts/fetch_model.sh`),
  input 640×640, one real frame containing a person (`models/bus.bgr`).
- Harness: `scripts/bench_phone.sh [repeats]` — pushes `vulkan_smoke`
  to the phone, runs the requested backend `repeats` times, reports the
  **steady-state median of the last half** of runs (run 1 carries GPU
  shader compilation / first-touch costs and is excluded).
- Capturing for publication: `scripts/bench_publish.sh [repeats]`
  frames the output with device metadata (model, SoC, Android version,
  battery temp before/after) and scrubs it into this directory.

### Numbers (2026-10-09 capture: OnePlus 7 / GM1901, SoC SM8150 / SD855, Android 16)

| Backend | YOLO11n @ 640×640, steady-state median of 60 runs |
|---|---|
| CPU fp16 (NCNN ARMv8 NEON kernels) | **88.3 ms/frame** (avg 88.3) |
| Vulkan (Adreno 640) | **342.6 ms/frame** (avg 334.4; run 1 incl. shader compile ≈ 468 ms) |

The GPU is ~3.9× slower for this workload on this SoC. Battery temp
29→30 °C across the run (no thermal throttling). Both backends detected
the person in the frame (SMOKE B PASS), so the comparison measures
equivalent work. The 2026-09 figures (88 / 335 ms, recorded in
`config/config.yaml`) are consistent within run-to-run variance; the
2026-10-09 file above is the citable capture.

## Soak test — methodology

`scripts/soak.sh` (DoD §11, scaled for automation) runs the full
pipeline against N synthetic cameras (a 3 s moving-person / 3 s still
loop published over RTSP — main stream recorded, sub-stream analyzed)
and samples every 30 s:

- `/api/metrics` gauges: inference ms, queue depth, dropped frames,
  thermal-governor fps scale, SoC temp, event totals;
- process RSS (nvrdet, nvrd, mediamtx, publishers) — NFR-4 memory
  stability;
- a mid-run **kill -9 of nvrdet** at 60% of the run (local mode only):
  events must keep accumulating after restart.

PASS = events > 0, ≥ 80% of expected samples collected, and
|RSS drift| ≤ 20% over the run. Phone mode (`--target phone`) is
read-only sampling of the deployed appliance over `adb forward` — it
never restarts anything in production; the local soak's notifications
config resolves to zero providers, so nothing is ever sent off-machine.

### Local 1 h × 4 cameras (2026-10-09)

Data: `soak-local-4cam-1h/metrics.tsv`, verdict in
`soak-local-4cam-1h/summary.txt`. Mac (Apple Silicon), CPU backend,
`temp_c` reads −1 (no thermal zones on macOS — the governor gauge is a
phone-side signal). This run validates pipeline stability (memory,
queue, event flow, daemon-kill recovery); it is **not** a thermal or
endurance claim about the phone. The phone-side soak (production stack,
real SoC temps) runs with `scripts/soak.sh --target phone` and is the
dataset the 72 h DoD claim will cite.

The run also caught a real bug — see `soak-local-4cam-1h/NOTES.md`:
a cold-started nvrdet ran with healthy gauges but emitted zero events
for ~17 minutes until restarted. Reproducible boot-order issue
candidate; open follow-up.

## Production evidence — the appliance that survives neglect

### Device logs (`device-logs/`, 2026-09-08 → 2026-10-09, 31 days)

Scrubbed excerpts of the real phone's supervisor/watchdog logs and
operational log tails (RTSP reconnect noise included — that's what an
unattended appliance actually lives through).

- **19 boots**, every one self-recovered by the Magisk `service.sh`:
  wait for `boot_completed` → wait for network → wakelock → regenerate
  runtime configs → start daemons → watchdog. Full sequence visible in
  `supervisor.txt`; today's (2026-10-09 15:45) went boot→stack-up in
  under 30 s.
- **71 watchdog restarts** total: nvrd 29, detector 22, mediamtx 16,
  cloudflared 4 — heavily clustered in the 2026-09-08/09 bring-up days
  (8 of the 19 boots); after 2026-09-09 the system settles to roughly
  one boot per week with automatic recovery each time.
- Boot #1 of this log (2026-09-08 18:51) is the stack's first boot
  after initial deployment.

### Phone soak sample (`soak-phone-sample/`, 10 min, read-only)

`scripts/soak.sh --target phone` samples the live production stack
over `adb forward` without touching it. Captured 2026-10-09 evening:

- SoC temperature **34.3–35.1 °C** (thermal governor at full fps scale)
- nvrdet RSS **57 MB, +0.0% drift**; nvrd 16 MB, mediamtx 40 MB
- nvrd uptime 8,016 s at sample start; 4,901 lifetime events
- Quiet scene the whole window: motion gate blocked everything, so
  inference_ms reads 0 (no frames inferred) — honest idle behavior,
  and a good baseline contrast for the local soak's loaded numbers.
- Caveat: `summary.txt`'s `kernel=` field records the sampling host
  (the Mac), not the device; the temps above are the phone's SoC.

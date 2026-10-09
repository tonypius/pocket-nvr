# Measurement data — benchmarks & soak results

Raw (PII-scrubbed) measurement data backing the blog writeup. Every file
here is produced by a script in `scripts/` — no hand-typed numbers.

| Artifact | Produced by | Status |
|---|---|---|
| `bench-phone-*.txt` | `scripts/bench_publish.sh` (wraps `scripts/bench_phone.sh`) | pending phone re-connect |
| `soak-local-4cam-1h/` | `scripts/soak.sh --duration 1h --cameras 4` | 1 h local run, 2026-10-09 |

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

### Numbers so far

The 2026-09 measurements (median of 60 runs, OnePlus 7 / SD855 /
Adreno 640) were 88 ms/frame CPU (NCNN ARMv8 fp16 NEON) vs 335 ms/frame
Vulkan — recorded in `config/config.yaml` (`detection.backend` comment);
the raw terminal output of that session was not retained. The definitive
capture file lands here via `bench_publish.sh` on the next bench run.

Interim sanity point: the local dev machine (Apple Silicon, CPU backend)
sustains ~19 ms/frame in the soak harness below — the pipeline numbers
are load-independent of the phone's, shown only to keep the dataset
honest about where each number came from.

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

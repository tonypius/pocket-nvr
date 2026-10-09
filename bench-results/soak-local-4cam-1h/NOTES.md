# Soak run notes — 2026-10-09 local 1h × 4 cameras

## Finding: 4-camera nvrdet emits zero events (silence-feeder starvation)

A cold-started nvrdet with **4 cameras** ran with healthy gauges —
gate_pass ≈ 47% (matching the person/blank duty cycle), inference
≈ 17–18 ms, queue_depth 0, dropped 0 — but emitted **zero events** for
17 minutes, then again after the mid-run kill-test restart. Not a
config issue: classes=[person], zones=[] (whole frame), threshold 0.5,
enter_frames 2 / exit_frames 5 all verified correct at the time.

### Evidence (5 process lifetimes, same binary + config + streams)

| # | cameras | NVRDET_DEBUG | starter | events |
|---|---|---|---|---|
| 1 | 4 | off | harness (t=0) | 0 in 16 min |
| 2 | 4 | **on** | manual, warm | 12 in 30 s, then normal |
| 3 | 4 | off | harness kill-test | 0 in 90 s+ |
| 4 | 4 | off | manual, warm | 0 in 30 s+ |
| 5 | 4 | **on** | manual, warm | immediate, ~1 event/cam/6 s |
| (earlier 3-min run) | 2 | off | harness | 66 events / 3 min ✓ |

### Mechanism (from code reading)

- Events are emitted **only on `Exited`** (`main.cpp` emitEvent call sites).
- With a motion gate, blank phases push no frames, so the only negatives
  an open event ever receives come from the **silence-fed negatives**
  branch — which runs **only when `FrameQueue::Pop` times out (200 ms
  with an empty queue)** (`main.cpp` worker loop).
- `capture.cpp` forces `fps=5` per camera into one shared queue; with
  4 cameras the interleaved arrivals keep the queue non-empty at
  < 200 ms gaps nearly always → the feeder never fires → machines go
  active and never close → zero emissions. With 2 cameras the arrival
  gaps exceed 200 ms naturally → feeder fires → events close.
- Why NVRDET_DEBUG=1 changes this at 4 cameras is not yet explained —
  the debug `fprintf` (~µs, every 20th frame) should not create 200 ms
  queue silences; timing perturbation is the hypothesis, unproven.
  Correlation was 100% across lifetimes 1–5.

### Follow-ups

1. Instrument the worker: count Pop timeouts + feeder firings per 10 s
   heartbeat in both modes — confirm/refute the starvation mechanism.
2. Candidate fix (design): feed silence negatives from a timer
   independent of queue idleness (e.g. per-camera deadline check every
   Pop iteration or a dedicated clock tick), not only on Pop timeout.
3. Reproduce on the phone — production boots nvrdet with all cameras
   hot, i.e. exactly the failing regime; a camera that mostly passes
   its gate would mask it, a quiet street at 3 a.m. would hit it.
4. The 72 h DoD soak must assert events_total grows in every window,
   not just eventually (this run's events_total column is the evidence:
   0 for ~1030 s, then non-zero after a debug-env restart).

Raw evidence: `metrics.tsv` (events_total column); /tmp run logs were
not retained (harness scratch dir).

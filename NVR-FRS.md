# Functional Requirements Specification — Personal On-Device Phone NVR

**Project codename:** PocketNVR
**Document status:** Draft v1.0 (build-ready)
**Author:** Tony (spec assembled for coding-agent handoff)
**Intended reader:** a coding agent + maintainer. Terse, ID'd, verifiable. Every buildable unit has an ID (`FR-*`, `COMP-*`); every risk/decision to resolve has a `FLAG-*`; open decisions are `OQ-*`.

---

## 1. Purpose & scope

### 1.1 Purpose
Turn a rooted OnePlus 7 running LineageOS into a **headless, GPU-accelerated Linux NVR appliance** that ingests four IP cameras, records continuously, performs on-device human detection, stores events, and notifies + serves views over LAN/VPN. Personal use first; documentation/benchmarking is an explicit secondary goal.

### 1.2 In scope (v1)
- Multi-camera RTSP/ONVIF ingest, continuous recording with retention, restreaming for viewing.
- On-device person detection (Adreno GPU via Vulkan) with motion pre-gating and zones.
- Event store (SQLite) with snapshots and event clips.
- Notifications (ntfy or Telegram).
- Local REST API + minimal web view; remote reach via Tailscale.
- Root-daemon supervision via Magisk (boot, wakelock, restart-on-death, charge cap).

### 1.3 Out of scope (v1, specified for later)
- Native Android **viewer app** (`FR-VWR-*`, Phase 7).
- Face recognition, LPR, vehicle/package classes, multi-user RBAC, cloud sync, audio analytics.
- Any Play Store distribution (this is a sideloaded/root personal build; no foreground-service model).

### 1.4 Goals / non-goals
- **Goal:** reliable 24/7 unattended operation; the appliance must self-heal.
- **Goal:** all detection on-device; no cloud, no footage leaving the LAN except via user's own Tailscale.
- **Non-goal:** Frigate feature parity. Match the 80% that matters: live view, continuous record, person events, alerts, timeline.

---

## 2. Environment & assumptions

| ID | Item | Value |
|----|------|-------|
| ENV-1 | Device | OnePlus 7 (`guacamoleb`), Snapdragon 855, Adreno 640 GPU, Hexagon 690 (unused), 6–8 GB RAM, UFS storage |
| ENV-2 | OS | LineageOS 23.2 (Android 16 base), rooted (Magisk) |
| ENV-3 | Accel target | **Adreno 640 via Vulkan (NCNN)**. NPU treated as unavailable — see FLAG-1 |
| ENV-4 | Cameras | 2× TP-Link Tapo (RTSP + ONVIF Profile S), 2× CP Plus (Dahua-lineage RTSP + ONVIF) |
| ENV-5 | Network | Home Wi-Fi LAN; DHCP reservations for all cameras + phone; Tailscale for remote |
| ENV-6 | Storage | Internal UFS and/or SD; single configurable base path |
| ENV-7 | Runtime model | Native arm64 binaries + shell, run as **root daemons supervised by Magisk** — NOT an Android app, NOT Android foreground services |

---

## 3. Architecture overview

### 3.1 Component map (build these — `COMP-*`)

| ID | Component | Tech (recommended, agent may substitute) | Runs as |
|----|-----------|------------------------------------------|---------|
| COMP-1 | **Supervisor / boot** | Magisk `service.sh` module + shell watchdog | root, on boot |
| COMP-2 | **Media server** | MediaMTX (arm64 binary, bundled) | daemon |
| COMP-3 | **Detection service** | C++ + NCNN(Vulkan) + ffmpeg for frame decode | daemon |
| COMP-4 | **Event & clip manager** | Go service + SQLite + ffmpeg | daemon (may merge with COMP-6) |
| COMP-5 | **Notification service** | Go (module of COMP-4) | in-process |
| COMP-6 | **Local API + web UI** | Go HTTP server (static UI) | daemon |
| COMP-7 | **Remote access** | Tailscale (tailscaled) | daemon |
| COMP-8 | **Config system** | single `config.yaml` + secrets file, loaded by all | library/shared |
| COMP-9 | **Observability** | structured logs + `/health` + `/metrics` | in-process across daemons |
| COMP-10 | **Viewer app** (future) | Android/Kotlin or PWA | Phase 7 |

### 3.2 Data flow
```
Cameras --RTSP/ONVIF--> [COMP-2 MediaMTX] --record--> segmented MP4 (disk)
                                  |
                                  +--local restream (127.0.0.1)--> [COMP-3 Detection]
                                                                        | frame @ N fps
                                                                        | motion gate -> YOLO11n (Vulkan)
                                                                        v person event
                                                            [COMP-4 Event/Clip mgr] --> SQLite + snapshot + clip
                                                                        |                     ^
                                                                        +--> [COMP-5 Notify] (ntfy/Telegram)
[COMP-6 API/UI] <-- reads SQLite/clips; proxies live view from COMP-2; served over LAN + [COMP-7 Tailscale]
[COMP-1 Supervisor] boots/monitors/restarts COMP-2,3,4,6,7; holds wakelock; applies charge cap
```

### 3.3 Key architectural decisions
- **AD-1:** Detection consumes MediaMTX's **local restream** (`rtsp://127.0.0.1:8554/<cam>_sub`), never the cameras directly → avoids camera RTSP connection limits (FLAG-7).
- **AD-2:** Detect on **substream** (low-res); record **main stream via stream-copy** (no re-encode). Software decode of the low-res substream only (FLAG-2).
- **AD-3:** **One shared GPU inference worker** fed by a bounded queue from N capture threads; not N concurrent Vulkan contexts (FLAG-3).
- **AD-4:** All long-running processes are Linux root daemons; supervision and persistence are Magisk/shell, not Android services.

---

## 4. Functional requirements

### 4.1 Supervisor / boot / lifecycle — `FR-SUP-*` (COMP-1)

| ID | Requirement | Acceptance criteria |
|----|-------------|---------------------|
| FR-SUP-1 | Start all daemons on boot in dependency order (Tailscale → MediaMTX → Detection → Event/API). | After reboot, all daemons reach healthy state with no manual action. |
| FR-SUP-2 | Acquire and hold a persistent CPU wakelock so Doze cannot suspend capture/detection (write `/sys/power/wake_lock`). Release on clean shutdown. | Device records + detects continuously with screen off and unplugged-then-plugged transitions; no gaps >5 s attributable to Doze. |
| FR-SUP-3 | Apply battery charge cap 60–70% via ACC (Magisk). | Battery never charges above configured ceiling during 24/7 operation. |
| FR-SUP-4 | Watchdog: health-check each daemon on an interval; restart on crash/hang with exponential backoff (cap configurable). | Killing any daemon manually → it is restarted within the configured window; backoff prevents crash-looping. |
| FR-SUP-5 | Tolerate network-not-ready at boot; retry camera/Tailscale connects with backoff. | Boot with Wi-Fi delayed → system converges once network is up, no permanent failure. |
| FR-SUP-6 | Rotate and size-cap all logs; never let logs fill storage. | Log directory stays under configured size across long runs. |
| FR-SUP-7 | Single control entrypoint (`nvrctl start|stop|restart|status|logs <comp>`). | Each subcommand works and reflects true process state. |
| FR-SUP-8 | Free-space guard: if storage below threshold, trigger retention cleanup and, if still low, pause continuous recording (keep detection) and raise a health warning. | Simulated low-disk → oldest recordings pruned; recording pauses before disk full; warning surfaced via `/health`. |

### 4.2 Media server (ingest / record / restream) — `FR-MED-*` (COMP-2)

| ID | Requirement | Acceptance criteria |
|----|-------------|---------------------|
| FR-MED-1 | Ingest all 4 cameras via RTSP (ONVIF optional for discovery), main + sub stream each, per `config.yaml`. | All 8 streams (4 main + 4 sub) resolvable and playable locally. |
| FR-MED-2 | Continuous recording of each **main** stream as segmented MP4 (stream-copy, no re-encode), configurable segment length. | Recordings written continuously; opening a segment shows correct video; CPU cost of recording negligible. |
| FR-MED-3 | Enforce recording retention by age and/or total size (`recordDeleteAfter` + size cap). | Old segments auto-deleted per policy; disk usage bounded. |
| FR-MED-4 | Expose local restream endpoints for the detector (sub) and for viewers (main+sub) over RTSP/WebRTC/HLS. | Detector pulls `…/<cam>_sub`; browser plays WebRTC/HLS via API proxy. |
| FR-MED-5 | Require auth on all externally reachable media endpoints; bind detector-only endpoints to localhost. | Unauthenticated LAN request to a protected path is rejected; localhost restream works without exposing cameras. |
| FR-MED-6 | Support a **record-on-event-only** mode as a config switch (reduces flash wear — FLAG-5). | With mode on, continuous recording stops; only event clips are written. |
| FR-MED-7 | Per-camera enable/disable without restarting the whole server. | Disabling a camera in config + reload stops its ingest only. |

### 4.3 Detection service — `FR-DET-*` (COMP-3) — the custom core

| ID | Requirement | Acceptance criteria |
|----|-------------|---------------------|
| FR-DET-1 | For each enabled camera, pull the local sub-stream and decode frames at a configurable detect FPS (default 5). | Frame cadence matches config ±1 fps; no unbounded frame backlog. |
| FR-DET-2 | **Motion gate**: downscale to grayscale, diff vs previous frame; only forward frames whose changed-area exceeds a per-camera threshold to the detector. | With a static scene, detector invocations ≈ 0; on motion, frames are forwarded. Measurable GPU-load reduction vs no gate. |
| FR-DET-3 | Run **YOLO11n via NCNN + Vulkan on the Adreno 640**; filter to COCO class `person`; configurable confidence threshold. | Person in frame → detection with box + score; GPU is the compute unit (verify via NCNN Vulkan init logs). |
| FR-DET-4 | CPU fallback (NCNN CPU / XNNPACK) if Vulkan unavailable at runtime. | Forcing Vulkan off still yields detections at reduced fps; no crash (FLAG-12). |
| FR-DET-5 | **Single shared inference worker** consuming a bounded queue from all camera capture threads; drop oldest on overflow. | 4 cameras with simultaneous motion do not spawn 4 GPU contexts; queue depth bounded; no OOM. |
| FR-DET-6 | Per-camera **zones**: polygon list; a detection counts only if its box anchor (config: centroid or bottom-center) is inside an active zone. Empty zones = whole frame. | Detections outside a defined zone are ignored; inside are kept. |
| FR-DET-7 | **Event state machine** per camera: enter after `enter_frames` consecutive positives, exit after `exit_frames` negatives; emit one event spanning enter→exit with peak confidence. | Continuous presence yields one event, not many; brief flicker below `enter_frames` yields none. |
| FR-DET-8 | Notification **cooldown** per camera + global min-gap to prevent alert storms. | Repeated presence within cooldown → single notification. |
| FR-DET-9 | Emit each event as a structured record (see §5.2) to COMP-4 via internal API/queue, including the trigger snapshot frame (with box drawn) and precise start/end timestamps. | COMP-4 receives well-formed events; snapshot saved. |
| FR-DET-10 | **Thermal governor**: read SoC temperature; above `temp_high`, reduce detect FPS / pause lowest-priority cameras; resume below `temp_low`. | Under sustained load with rising temp, fps steps down and device does not thermally shut down (FLAG-6). |
| FR-DET-11 | Model, input size, thresholds, fps, zones, gate sensitivity all config-driven and hot-reloadable without full restart where feasible. | Editing config + reload changes behavior without dropping recordings. |
| FR-DET-12 | Expose per-camera runtime metrics: inference ms, effective fps, gate hit-rate, dropped frames. | Values available at `/metrics` (FR-OBS-2). |

### 4.4 Event & clip manager — `FR-EVT-*` (COMP-4)

| ID | Requirement | Acceptance criteria |
|----|-------------|---------------------|
| FR-EVT-1 | Persist events to SQLite per schema §5.1. | Event row created with all fields; queryable by camera/time/label. |
| FR-EVT-2 | Save event **snapshot** (annotated trigger frame) to disk; store path in DB. | Snapshot file exists and renders; path resolvable via API. |
| FR-EVT-3 | Extract an **event clip** covering `[start − pre, end + post]` from the main-stream recordings via stream-copy; store path in DB. | Clip exists, plays, spans the padded window; extraction does not re-encode. |
| FR-EVT-4 | Independent retention for events/snapshots/clips (by age and/or count), separate from continuous recording retention. | Old events + assets pruned per policy; DB rows and files removed together (no orphans). |
| FR-EVT-5 | Idempotent + crash-safe writes (no partial events; survive kill mid-write). | Killing the service mid-event leaves DB consistent on restart. |
| FR-EVT-6 | Maintain a rolling pre-event buffer OR guarantee recordings exist far enough back to satisfy `pre` seconds. | Clips always include the configured pre-roll. |

### 4.5 Notifications — `FR-NOT-*` (COMP-5)

| ID | Requirement | Acceptance criteria |
|----|-------------|---------------------|
| FR-NOT-1 | On event, send a notification via configured provider: **ntfy** (topic/URL) and/or **Telegram** (bot token/chat id). | Notification arrives on device with camera name + time. |
| FR-NOT-2 | Attach the event snapshot and a deep link to the event in the local/Tailscale UI. | Notification shows image + tappable link that opens the event. |
| FR-NOT-3 | Honor per-camera cooldown (shared with FR-DET-8). | No alert storms. |
| FR-NOT-4 | Retry with backoff; queue while offline and flush on reconnect; never block detection. | Provider unreachable → events still recorded; notifications delivered later; detection unaffected. |
| FR-NOT-5 | Configurable severity/priority and quiet-hours schedule. | Quiet hours suppress push (events still stored). |

### 4.6 Local API + web UI — `FR-API-*` (COMP-6)

| ID | Requirement | Acceptance criteria |
|----|-------------|---------------------|
| FR-API-1 | REST endpoints (see §6). | Each endpoint returns documented shape + status codes. |
| FR-API-2 | Live view: proxy/redirect to MediaMTX WebRTC (preferred) or HLS per camera. | Browser shows live grid on LAN and over Tailscale. |
| FR-API-3 | Event timeline UI: list/filter events by camera + time + label; open snapshot + clip. | Minimal but functional web page; loads over LAN/Tailscale. |
| FR-API-4 | Config read + safe write (validated) via API; trigger reload. | Invalid config rejected with errors; valid config applied. |
| FR-API-5 | Auth on all endpoints (token or basic); no anonymous access. | Unauthenticated request → 401. |
| FR-API-6 | Health + metrics endpoints (FR-OBS-*). | Return live status. |

### 4.7 Remote access — `FR-RMT-*` (COMP-7)

| ID | Requirement | Acceptance criteria |
|----|-------------|---------------------|
| FR-RMT-1 | Join the device to the user's tailnet; API + live view reachable over Tailscale with **no port forwarding**. | Remote device on tailnet reaches UI; nothing exposed on WAN. |
| FR-RMT-2 | Optionally front the API with Tailscale Serve (HTTPS inside tailnet). | HTTPS URL works within tailnet. |
| FR-RMT-3 | No camera or media endpoint is ever bound to a public interface or forwarded. | Port scan from WAN shows nothing open. |

### 4.8 Configuration — `FR-CFG-*` (COMP-8)

| ID | Requirement | Acceptance criteria |
|----|-------------|---------------------|
| FR-CFG-1 | Single `config.yaml` is the source of truth for all components (schema §6.3). | All daemons read the same file; documented schema. |
| FR-CFG-2 | Secrets (camera creds, bot tokens) in a separate `secrets.yaml` with `600` perms; never logged. | Secrets file perms enforced; logs contain no credentials (FLAG-13). |
| FR-CFG-3 | Validate config on load; refuse to start / refuse reload on invalid config with clear errors. | Bad config → actionable error, previous good state retained on reload. |
| FR-CFG-4 | Sensible defaults so a minimal config (cameras only) boots a working system. | Minimal config runs with default detection/record/notify settings. |

### 4.9 Observability — `FR-OBS-*` (COMP-9)

| ID | Requirement | Acceptance criteria |
|----|-------------|---------------------|
| FR-OBS-1 | `/health`: per-component up/down, per-camera connected/recording/detecting, storage free, temp, uptime. | Reflects true state; degraded components flagged. |
| FR-OBS-2 | `/metrics`: inference ms, per-camera fps, gate hit-rate, dropped frames, queue depth, events/hour, power/temp where available (benchmark data source). | Numeric metrics suitable for the benchmark writeup. |
| FR-OBS-3 | Structured (JSON or key=value) logs per component with levels. | Grep-able logs; level configurable. |

### 4.10 Viewer app (future) — `FR-VWR-*` (COMP-10, Phase 7)

| ID | Requirement | Acceptance criteria |
|----|-------------|---------------------|
| FR-VWR-1 | Native/PWA viewer consuming the same API + WebRTC live + event timeline. | Not required for v1; specified so API is designed viewer-ready. |
| FR-VWR-2 | Push notifications tie back to event deep links. | Tapping a push opens the event. |

---

## 5. Data models

### 5.1 SQLite schema (COMP-4)
```sql
CREATE TABLE cameras (
  id           TEXT PRIMARY KEY,      -- matches config camera id
  name         TEXT NOT NULL,
  enabled      INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE events (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  camera_id    TEXT NOT NULL REFERENCES cameras(id),
  label        TEXT NOT NULL DEFAULT 'person',
  score        REAL NOT NULL,         -- peak confidence
  start_ts     INTEGER NOT NULL,      -- epoch ms
  end_ts       INTEGER,              -- null while active
  zone         TEXT,                 -- zone name or null
  bbox         TEXT,                 -- json [x,y,w,h] of peak frame
  snapshot_path TEXT,
  clip_path     TEXT,
  created_at   INTEGER NOT NULL,
  notified     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_events_cam_time ON events(camera_id, start_ts);
CREATE INDEX idx_events_time ON events(start_ts);
```

### 5.2 Event message (COMP-3 → COMP-4, internal)
```json
{
  "camera_id": "front_tapo",
  "label": "person",
  "score": 0.91,
  "start_ts": 1731000000000,
  "end_ts": 1731000006000,
  "zone": "driveway",
  "bbox": [412, 220, 96, 210],
  "snapshot_jpeg_b64": "..."   // or a written path
}
```

---

## 6. External interfaces

### 6.1 REST API (COMP-6) — all require auth
| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/cameras` | list cameras + status |
| GET | `/api/events?camera=&from=&to=&label=&limit=` | query events |
| GET | `/api/events/{id}` | event detail |
| GET | `/api/events/{id}/snapshot` | annotated snapshot |
| GET | `/api/events/{id}/clip` | event clip (stream) |
| GET | `/api/live/{camera}` | live view (redirect to MediaMTX WebRTC/HLS) |
| GET | `/api/health` | health (FR-OBS-1) |
| GET | `/api/metrics` | metrics (FR-OBS-2) |
| GET | `/api/config` | current config (secrets redacted) |
| PUT | `/api/config` | validated config update + reload |

### 6.2 Camera URL formats (reference — put in config)
- **Tapo** (create *Camera Account* in Tapo app first; ONVIF port 2020):
  `rtsp://<user>:<pass>@<ip>:554/stream1` (main), `…/stream2` (sub)
- **CP Plus** (Dahua-lineage; enable RTSP/ONVIF, force **H.264 not H.265**, disable RTSP-over-TLS/encryption, create ONVIF user):
  `rtsp://<user>:<pass>@<ip>:554/cam/realmonitor?channel=1&subtype=0` (main), `…&subtype=1` (sub)

### 6.3 `config.yaml` schema (abridged, COMP-8)
```yaml
system:
  base_path: /data/nvr
  log_level: info
  wakelock: true
  charge_cap_pct: 65          # applied via ACC
  storage_min_free_gb: 8

cameras:
  - id: front_tapo
    name: Front Door
    main_url: rtsp://127.0.0.1:8554/front_tapo       # via MediaMTX restream
    sub_url:  rtsp://127.0.0.1:8554/front_tapo_sub
    source_main: rtsp://USER:PASS@192.168.0.20:554/stream1   # real camera (MediaMTX ingest)
    source_sub:  rtsp://USER:PASS@192.168.0.20:554/stream2
    enabled: true
    detect:
      fps: 5
      threshold: 0.5
      enter_frames: 3
      exit_frames: 8
      motion_min_area: 0.02   # fraction of frame
      anchor: bottom_center   # or centroid
      zones:
        - name: driveway
          polygon: [[0,180],[640,180],[640,360],[0,360]]

recording:
  mode: continuous            # or event_only
  segment_seconds: 300
  retention_days: 7
  size_cap_gb: 128

events:
  clip_pre_seconds: 5
  clip_post_seconds: 10
  retention_days: 30
  retention_max_count: 5000

detection:
  model: yolo11n              # ncnn param/bin path resolved under base_path/models
  input_size: 640
  backend: vulkan             # vulkan | cpu
  queue_max: 32

notifications:
  provider: ntfy              # ntfy | telegram | both
  cooldown_seconds: 60
  quiet_hours: { start: "23:00", end: "06:30" }

api:
  bind: 0.0.0.0:8099
  auth: token

remote:
  tailscale: true
```
*(Secrets — real camera creds, ntfy auth, telegram token/chat — live in `secrets.yaml`, referenced by key, not inlined.)*

---

## 7. Non-functional requirements — `NFR-*`

| ID | Requirement | Target / criteria |
|----|-------------|-------------------|
| NFR-1 | Sustained throughput | 4 cameras, motion-gated detection at 5 fps each, no unbounded backlog over 72 h. |
| NFR-2 | Latency | Person-in-zone → notification < 3 s end-to-end (typical). |
| NFR-3 | Availability | Auto-recovers from any single daemon crash and from network blips without human action. |
| NFR-4 | Memory | No leak: RSS stable (±10%) over 72 h continuous run. |
| NFR-5 | Thermal | Never thermal-shutdown; governor (FR-DET-10) keeps SoC below `temp_high` sustained. |
| NFR-6 | Power | Charge held at cap; record average wall-power draw for writeup. |
| NFR-7 | Storage safety | Never fills disk; retention + free-space guard proven under a fill test. |
| NFR-8 | Security | LAN-only by default; remote only via Tailscale; API authed; no WAN exposure; creds protected. |
| NFR-9 | Reproducibility | One documented install script + config brings a fresh device to running state. |
| NFR-10 | Portability | Detection/media/event binaries are self-contained arm64; minimal runtime deps on the phone. |

---

## 8. Build phases & milestones

| Phase | Delivers | Requirements | Done when |
|-------|----------|--------------|-----------|
| **P1** (done) | Cameras provisioned, DHCP reserved, RTSP verified in VLC | ENV-4/5 | 4 RTSP URLs play |
| **P2** | MediaMTX ingest + LAN live view | FR-MED-1,4,5,7; FR-CFG-* | All 4 cameras viewable via MediaMTX with auth |
| **P3** | Continuous segmented recording + retention | FR-MED-2,3,6; FR-SUP-8 | Recordings roll + prune; disk bounded |
| **P4** | Detection daemon: gate → Vulkan YOLO11n → events → notify | FR-DET-1..12; FR-NOT-1..4 | Person → alert within NFR-2; GPU path confirmed |
| **P5** | Event store + snapshots + clips + timeline UI | FR-EVT-*; FR-API-1,2,3 | Events queryable; clips play in UI |
| **P6** | Reliability hardening: Magisk supervisor, ACC, wakelock, watchdog, thermal, free-space | FR-SUP-*; FR-DET-10; NFR-3,4,5,7 | Survives reboots, kills, low-disk, 72 h soak |
| **P7** (later) | Tailscale polish + native/PWA viewer | FR-RMT-*; FR-VWR-* | Remote timeline + live; push deep links |

---

## 9. Flags — decisions & risks to resolve during build (`FLAG-*`)

| ID | Flag | Guidance |
|----|------|----------|
| FLAG-1 | **NPU unusable.** Hexagon 690 not supported by modern QNN/HTP delegate; NNAPI deprecated (Android 15+) and its vendor HAL may be absent in LineageOS. | Target Adreno/Vulkan. Benchmark NPU only opportunistically; do not depend on it. |
| FLAG-2 | **No MediaCodec in a native daemon.** Hardware decode is an Android-app API. | Software-decode **sub-streams only** (low-res, cheap) via ffmpeg. Record main stream by stream-copy (no decode). |
| FLAG-3 | **Single GPU.** Concurrent per-camera Vulkan contexts will contend. | One inference worker + bounded queue (FR-DET-5). |
| FLAG-4 | **Doze suspends work.** | Root wakelock (FR-SUP-2); mitigate battery via ACC. |
| FLAG-5 | **Flash wear from 24/7 write.** UFS endurance. | Offer `event_only` record mode (FR-MED-6); enforce retention; consider SD for recordings. |
| FLAG-6 | **Thermal throttle/shutdown** under sustained inference on a fanless phone. | Thermal governor (FR-DET-10); keep detect fps modest; ensure airflow, case off. |
| FLAG-7 | **Camera RTSP connection limits.** | Detector reads MediaMTX **local restream**, not cameras (AD-1). |
| FLAG-8 | **Camera prerequisites.** Tapo needs a Camera Account and must be a **plug-in** model (battery Tapos throttle RTSP). CP Plus needs RTSP/ONVIF enabled, **H.264 not H.265**, encryption/TLS off, ONVIF user created. | Verify per camera during P2; document exact models. |
| FLAG-9 | **Boot/network race.** | Retry/backoff on connect (FR-SUP-5); order Tailscale/MediaMTX before detector. |
| FLAG-10 | **LineageOS vendor blobs.** Vulkan driver / any HAL must exist in the ROM build. | Verify `vulkaninfo`/NCNN Vulkan init early in P4; CPU fallback (FR-DET-4). |
| FLAG-11 | **Model license.** YOLO11n = AGPL-3.0. Fine for personal use; **if you ever publish/distribute the whole system**, either open-source under AGPL or switch to Apache/BSD model (YOLOX-tiny, NanoDet, EfficientDet-Lite). | Decide before any public release. |
| FLAG-12 | **Vulkan may be flaky** on the specific Adreno driver in the ROM. | CPU/XNNPACK fallback path must exist and be tested. |
| FLAG-13 | **Secrets handling.** | Separate `secrets.yaml`, `600` perms, redaction in logs/API. |
| FLAG-14 | **Clock accuracy** for event timestamps. | Ensure NTP/time sync; store epoch ms UTC. |
| FLAG-15 | **Media server choice.** MediaMTX (record + auth + restream) vs go2rtc (nicer WebRTC UI, no record). | Default MediaMTX; optionally run go2rtc alongside for live UX. |
| FLAG-16 | **Language/runtime split.** NCNN is C++; want single static binaries, no heavy runtime on phone. | Recommended: detector in C++ (NCNN), API/events/notify in Go, glue in shell. Agent may unify if it keeps binaries lean. |

---

## 10. Open questions for the maintainer (`OQ-*`)
- **OQ-1:** Confirm exact Tapo + CP Plus model numbers (drives FLAG-8) and that Tapos are plug-in.
- **OQ-2:** Record to internal UFS or an SD card? (affects FLAG-5, retention sizing).
- **OQ-3:** Notification provider — ntfy (self-hosted?) vs Telegram vs both?
- **OQ-4:** Desired retention: continuous days + event days + size caps.
- **OQ-5:** Zones per camera — provide polygons, or start whole-frame and refine later?
- **OQ-6:** Detect fps and person-confidence threshold defaults acceptable (5 fps / 0.5)?
- **OQ-7:** Any second class of interest later (vehicle/animal) to keep the model choice open?

---

## 11. Definition of done (v1)
All P2–P6 acceptance criteria pass; system survives a 72-hour unattended soak including at least one forced reboot, one killed daemon per component, one simulated low-disk event, and one Wi-Fi drop, with no lost recordings beyond the outage window, correct events + notifications throughout, and stable memory/thermals. Benchmark metrics (inference ms, fps, watts, temp across CPU vs Vulkan) captured for the writeup.

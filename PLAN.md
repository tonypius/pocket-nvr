# PocketNVR — Build Plan

Companion to `NVR-FRS.md` (v1.0). This plan turns the FRS phases into an executable engineering sequence, fixes toolchain decisions, and orders risk burn-down. Requirement/flag IDs refer to the FRS.

---

## 0. Approach in one paragraph

This is **not an Android app** (ENV-7): it is a set of self-contained arm64 Linux daemons running as root on Android. That dictates the toolchain (NDK for the C++ detector against bionic, CGO-free static Go for services) and the test strategy: **everything that doesn't require the phone is developed and integration-tested on this Mac first** (MediaMTX, the whole Go stack, the detector's CPU build, fake or real RTSP cameras), and the phone is used only where it is the actual target — Vulkan/Adreno, Magisk supervision, ACC, wakelock, thermals, storage. Phone-only risks (FLAG-10/12 Vulkan, boot-time daemon exec) are burned down with smoke tests in week one, not late in the build.

## 1. Toolchain & environment decisions

| Decision | Choice | Rationale |
|---|---|---|
| DT-1 | **Detector (COMP-3) in C++ built with Android NDK (bionic)** | NCNN officially supports NDK cross-builds with Vulkan; bionic-linked binaries run as root on the phone. Static musl + Vulkan is the harder road. |
| DT-2 | **Frame decode via a spawned static `ffmpeg` writing rawvideo to a pipe** (not linking libavcodec into the detector) | Keeps the NDK build surface tiny; ffmpeg handles RTSP reconnect/timeout flags for free. Sub-stream-only decode per AD-2/FLAG-2. Revisit if per-process overhead measurably matters. |
| DT-3 | **Go services (COMP-4/5/6) as ONE binary (`nvrd`), `CGO_ENABLED=0`, SQLite via `modernc.org/sqlite`** | FRS explicitly allows merging COMP-4/5/6 (§3.1). CGO-free keeps the binary fully static → zero bionic/glibc concerns; one daemon to supervise. |
| DT-4 | **linux/arm64 validation via Docker Desktop (Apple Silicon runs arm64 containers natively — no qemu)** | Run `nvrd` + MediaMTX in arm64 Linux containers on the Mac to prove the exact binaries that ship to the phone. |
| DT-5 | **MediaMTX as prebuilt arm64 release binary, vendored** (FLAG-15 default) | No build needed; record + auth + restream all built in. |
| DT-6 | **UI: server-rendered/static HTML+JS served by `nvrd`** (no framework build step for v1) | FR-API-3 says "minimal but functional"; WebRTC/WHEP playback via MediaMTX, HLS fallback. |
| DT-7 | **Yolo11n → NCNN via PNNX export**, automated in `models/fetch.sh` | Reproducible model pipeline (NFR-9); keeps FLAG-11 license posture visible in the script header. |
| DT-8 | **Dev machine setup:** install `adb` (Homebrew `android-platform-tools`) + Android NDK (r27+) + a Homebrew MediaMTX copy for Mac-side testing | Docker, Go 1.25, ffmpeg, cmake already present. |
| DT-9 | **Admin/viewer UI built as an installable PWA** (manifest + mobile-first, token-in-URL auth, served by `nvrd`) so config and viewing work from any device — including a home-screen icon on the user's phone — with zero sideloading; a native Android viewer (FR-VWR-*) remains the Phase 7 option and can reuse the same API | Tony's direction: PWA/app for easy admin on the device; PWA avoids Play-store/sideload friction and auto-updates with the server. |

### Dev-machine gap (measured today)
Present: go 1.25, docker, ffmpeg, cmake, clang, python3, git. Missing: **adb**, **Android NDK**, linux cross-openssl if ever needed (avoid via CGO-free). Phase 0 installs these.

## 2. Repository layout (to scaffold in Phase 0)

```
nvr-mobile/
  NVR-FRS.md, PLAN.md
  config/            config.yaml (FRS §6.3 schema), secrets.yaml.example, config.schema notes
  models/            fetch.sh (YOLO11n→NCNN), class list, license notice
  detector/          C++ NCNN detector (COMP-3)
    src/             capture, gate, infer worker, zones, state machine, governor, metrics
    jni/ or ndk-build files
  cmd/nvrd/          Go daemon: events+notify+api+retention (COMP-4/5/6)
  internal/          config, store, notify, api, clips, health
  ui/                static web UI
  deploy/
    magisk-module/   module.prop, service.sh, post-fs-data.sh
    nvrctl           control entrypoint (FR-SUP-7)
  build/             Dockerfiles + scripts for NDK and arm64 Go builds
  scripts/           install.sh (NFR-9), smoke tests, soak harness, fill test
```

Single `config.yaml` (COMP-8) is the source of truth; a small Go generator renders `mediamtx.yml` from it so camera config is never duplicated (FR-CFG-1).

## 3. Phase plan

### Phase 0 — Environment + risk smoke tests (new, before FRS P2)
1. Install adb + NDK + MediaMTX (Mac). Scaffold repo layout.
2. **Phone smoke test A (SELinux/exec):** push a hello-world static Go binary, run it from a Magisk `service.sh` at boot, confirm it starts, writes to `/data/nvr`, binds a port, survives reboot. This proves the entire runtime model (ENV-7) before any real code exists.
3. **Phone smoke test B (Vulkan, FLAG-10/12):** build NCNN's NDK sample with Vulkan on, run on-device, confirm Adreno 640 init logs + successful inference. If Vulkan fails → decision point: proceed CPU-first (FR-DET-4 becomes primary path) and revisit.
4. Verify charge-control options for ACC (FR-SUP-3) and NTP sync (FLAG-14).

**Done when:** both smoke tests pass (or fallback path chosen knowingly); repo scaffolded.

### Phase 1 = FRS P2 — Media ingest (COMP-2)
- Vendor MediaMTX arm64; write the config→`mediamtx.yml` generator; per-camera paths `/<cam>` (main) and `/<cam>_sub` (sub) per AD-1.
- Auth: MediaMTX internal auth (or HTTP auth hook into `nvrd`); detector-only endpoints bound to localhost (FR-MED-5).
- Develop against the **real cameras from this Mac** (they're on the LAN; P1 verified VLC playback). Then deploy to phone.
- Per-camera enable/disable via config reload (FR-MED-7).

**Done when (FRS):** all 4 cameras viewable via MediaMTX with auth; 8 streams resolvable.

### Phase 2 = FRS P3 — Recording + retention (COMP-2 + maintenance job)
- MediaMTX stream-copy segmented recording (FR-MED-2), `event_only` switch stubbed (FR-MED-6).
- Retention pruner + free-space guard (FR-MED-3, FR-SUP-8) as a Go module in `nvrd`; fill-test on the Mac against a small loopback/limited dir, then on the phone.
- Camera URL prerequisites checklist from FLAG-8 documented per camera.

**Done when:** recordings roll + prune; disk bounded; guard pauses recording before full.

### Phase 3 = FRS P4 — Detection daemon (COMP-3) — the core
Build order inside the phase (each step independently testable on the Mac unless noted):
1. Capture threads: ffmpeg pipe per camera (sub-stream), reconnect flags, configurable fps, bounded backlog (FR-DET-1).
2. Motion gate: grayscale downscale + diff, per-camera area threshold (FR-DET-2).
3. Single inference worker + bounded queue, drop-oldest (FR-DET-5, AD-3). NCNN CPU build first on Mac (logic correctness), Vulkan build smoke-tested on phone in step 0 of this phase if not already done.
4. Postproc: person-class filter + confidence, NMS, zones (point-in-polygon, anchor modes) (FR-DET-3, FR-DET-6).
5. Event state machine: enter/exit frames, peak-score tracking, cooldown (FR-DET-7, FR-DET-8).
6. Event emission: HTTP POST to `nvrd` localhost endpoint (JSON §5.2 + annotated JPEG snapshot) (FR-DET-9).
7. Thermal governor (FR-DET-10), config hot-reload via SIGHUP (FR-DET-11), CPU fallback switch (FR-DET-4), metrics (FR-DET-12).
8. **Benchmark harness early:** inference ms / fps / backend, since NFR-1/6 and the writeup need it.

**Done when (FRS):** person → alert within NFR-2; GPU path confirmed on device.

### Phase 4 = FRS P5 — Events, notify, API, UI (COMP-4/5/6 in `nvrd`)
- SQLite schema §5.1 (pure-Go driver), idempotent crash-safe writes (FR-EVT-5).
- Clip extraction: map `[start−pre, end+post]` onto recorded segments, stream-copy concat (FR-EVT-3, FR-EVT-6); pre-roll satisfied from continuous recordings (FR-EVT-6).
- Independent event/snapshot/clip retention, no orphans (FR-EVT-4).
- Notifications: ntfy + Telegram, snapshot attach, deep link, retry queue while offline, quiet hours (FR-NOT-1..5).
- REST API per §6.1 with token auth (FR-API-5); config GET (redacted) + validated PUT + reload (FR-API-4, FR-CFG-3).
- Minimal UI: live grid (WHEP→MediaMTX WebRTC, proxied), timeline with filters, event detail (FR-API-2, FR-API-3).

**Done when:** events queryable; clips play in UI on LAN.

### Phase 5 = FRS P6 — Supervision & hardening (COMP-1)
- Magisk module `service.sh` boots in dependency order (FR-SUP-1); `nvrctl` (FR-SUP-7) with PID-file-based status/stop/start; watchdog with exponential backoff (FR-SUP-4).
- Wakelock via `/sys/power/wake_lock` (FR-SUP-2); ACC charge cap (FR-SUP-3); boot-race retries (FR-SUP-5, FLAG-9); log rotation caps (FR-SUP-6).
- Soak harness implementing DoD §11: forced reboot, kill-each-daemon, simulated low-disk, Wi-Fi drop, 72 h run, RSS stability check (NFR-4).

**Done when:** the DoD soak passes end to end.

### Phase 6 = FRS P7 — Tailscale + viewer (deferred)
Tailscale join + optional Serve HTTPS (FR-RMT-*), then viewer app/PWA (FR-VWR-*). API is designed viewer-ready from Phase 4.

## 4. Risk burn-down order

1. **Runtime model works at all** — Phase 0 smoke test A (root daemon at boot on LineageOS 23.2). Highest structural risk; costs one hour to test.
2. **Vulkan on Adreno in this ROM** — Phase 0 smoke test B (FLAG-10/12). If flaky, CPU/XNNPACK is not a disaster at 5 fps × 4 cameras on sub-streams, but we know before building around it.
3. **YOLO11n→NCNN export correctness** (PNNX) — verify detections on Mac against known images before integrating.
4. **ffmpeg pipe 24/7 stability** — reconnect/timeout flags + watchdog restart; soak coverage.
5. **Flash wear** (FLAG-5) — answer OQ-2 (SD vs UFS) before Phase 2 sizing.

## 5. Open questions — working recommendations (pending Tony)

| OQ | Working default |
|---|---|
| OQ-1 | Proceed assuming plug-in Tapos; verify models at Phase 1 camera bring-up (FLAG-8 checklist). |
| OQ-2 | **SD card for recordings if available** (wear isolation), UFS for DB/app; else UFS with aggressive size cap + `event_only` option. |
| OQ-3 | Implement both ntfy + Telegram (cheap); configure either. |
| OQ-4 | Start with FRS defaults: 7 d continuous / 30 d events / 128 GB cap; revisit after measuring write throughput. |
| OQ-5 | Whole-frame first; zones refined once UI + real events exist. |
| OQ-6 | Accept 5 fps / 0.5; tune after Phase 3 benchmark. |
| OQ-7 | Keep class list config-driven (COCO gives vehicle/animal for free later). |

## 6. Immediate next actions

1. `brew install android-platform-tools` + NDK; fetch MediaMTX (mac + linux arm64).
2. Scaffold repo layout (§2).
3. Run Phase 0 smoke tests A and B on the phone.
4. Phase 1 camera bring-up on the Mac.

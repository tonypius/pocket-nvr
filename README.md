# PocketNVR

Turn a spare rooted Android phone into a self-hosted NVR (network video recorder) appliance: continuous recording for IP cameras, **on-device AI person/object detection**, event snapshots and clips, push notifications, and an installable web UI — with no cloud dependency and no footage ever leaving your network.

It is **not an Android app**. It is a set of native arm64 Linux daemons that run as root on Android (via Magisk supervision): [MediaMTX](https://github.com/bluenviron/mediamtx) for RTSP ingest/recording/restreaming, a single static Go binary (`nvrd`) for the event store, notifications, REST API and web UI, and a C++ detector (`nvrdet`) using [NCNN](https://github.com/Tencent/ncnn) with a YOLO11n model.

## Features

- **Ingest & recording** — RTSP cameras via MediaMTX; continuous fMP4 segment recording with restreaming for live view and a proxied playback API for browsing the archive
- **On-device detection** — ffmpeg capture pipes from the camera sub-stream, motion pre-gating, YOLO11n inference (CPU fp16 by default, Vulkan optional), configurable detection zones and object classes (person, cat, dog, car)
- **Events** — SQLite event store, peak-frame snapshot with detection-box overlay, cropped object thumbnails, per-event MP4 clips
- **Notifications** — ntfy and/or Telegram with score thresholds, cooldowns, quiet hours, and a retry queue that replays undelivered events after restart
- **Web UI (PWA)** — installable from the browser; Home dashboard, Live view (WebRTC/WHEP with an HLS proxy fallback), Events, Detections grid, a Clips feed that plays event clips inline, and full Settings (camera add/edit, ONVIF/RTSP LAN discovery, secrets editor) — all hot-reloading, dark/light theme included
- **Appliance ops** — boot-time auto-start + watchdog with restart backoff, log rotation, retention by age/size/count, free-space guard that pauses recording before the disk fills, SoC thermal governor, wakelock, and battery charge cap (ACC) for 24/7 unattended duty
- **Security** — token auth on every API route, write-only secrets, redacted config reads; remote access via Tailscale or any TCP-only tunnel (the HLS proxy works where WebRTC UDP cannot)

## Architecture

```
 IP cameras ──RTSP──► MediaMTX ──────► recordings/ (fMP4 segments)
                        │
                        │ sub-stream (localhost RTSP)
                        ▼
                   ffmpeg pipes ──► nvrdet (C++ / NCNN)
                                    motion gate → YOLO11n → zones → event state machine
                                        │  events + snapshot/crop JPEG (HTTP ingest)
                                        ▼
 browser (PWA) ◄──── nvrd (Go, single static binary)
   • REST API + SSE event stream      • SQLite event store
   • notifications (ntfy/Telegram)    • clip extraction (stream-copy)
   • retention + free-space guard     • static web UI + HLS/WHEP/playback proxies
```

## Requirements

**Target device** — any rooted Android phone or tablet with a 64-bit ARM CPU, Android 10+, Magisk (root), 32 GB+ storage, and a permanent power source. Detection runs comfortably on a mid-range SoC; see [Performance](#performance).

**Cameras** — any IP camera or NVR with an RTSP stream. A sub-stream is strongly recommended: only the sub-stream is decoded for analysis; the main stream is recorded.

**Build host** — Go 1.25+, Android NDK r27+, CMake + clang, ffmpeg, and `adb`.

## Quickstart

```bash
# 1. Fetch vendored dependencies (MediaMTX, static ffmpeg, YOLO11n → NCNN export)
scripts/fetch-mediamtx.sh
scripts/fetch-ffmpeg.sh
scripts/fetch_model.sh

# 2. Configure
cp config/secrets.yaml.example config/secrets.yaml   # then edit: set api_token + camera creds
$EDITOR config/config.yaml                           # cameras, retention, detection tuning

# 3. Build and deploy to the phone over adb (USB or wireless)
scripts/deploy.sh --start

# 4. Open the UI from any device on the LAN
open http://<phone-ip>:8099/          # sign in with your api_token
```

`deploy.sh` cross-compiles the Go daemon (static, CGO-free), verifies ELF artifacts, pushes binaries + UI + model to the phone, installs to `/data/nvr`, generates the MediaMTX and detector runtime configs, and starts everything via `nvrctl`.

To make the appliance survive reboots unattended, install the Magisk boot module:

```bash
scripts/install_boot_module.sh        # add --reboot to run the boot acceptance test
```

## Configuration

`config/config.yaml` is the single source of truth (cameras, recording, events, detection, notifications, API bind). Secrets live in `config/secrets.yaml` (chmod 600, never committed) and are referenced as `${secret:key}`; special characters are percent-escaped automatically when interpolated into URLs. `nvrd -check` validates, and SIGHUP hot-reloads — an invalid config keeps the previous good state. The Settings page writes both files through validated endpoints and triggers the same reload.

```yaml
cameras:
  - id: front_door
    name: Front Door
    enabled: true
    main_url: rtsp://127.0.0.1:8554/front_door          # recorded stream (via MediaMTX)
    sub_url: rtsp://127.0.0.1:8554/front_door_sub       # analyzed stream
    source_main: rtsp://${secret:front_user}:${secret:front_pass}@192.168.1.10:554/stream1
    source_sub: rtsp://${secret:front_user}:${secret:front_pass}@192.168.1.10:554/stream2
    detect:
      fps: 5
      threshold: 0.5
      zones: []          # empty = whole frame
```

| Secret key | Purpose |
|---|---|
| `api_token` | **Required.** Auth for every `/api` route (`Authorization: Bearer`, `X-Api-Token`, or `?token=` for `<img>`/deep links) |
| `mediamtx_viewer_pass` | MediaMTX viewer auth |
| `<camera>_user` / `<camera>_pass` | Per-camera RTSP credentials |
| `ntfy_topic_url`, `telegram_bot_token`, `telegram_chat_id` | Notification providers |

## API overview

All routes require the token (except the static app shell). Highlights:

| Endpoint | Purpose |
|---|---|
| `GET /api/health` | Component status, SoC temperature, free storage |
| `GET/PUT /api/config`, `POST /api/secrets` | Validated config + write-only secrets management |
| `POST /api/discover` | ONVIF WS-Discovery + RTSP sweep of the local subnet |
| `GET /api/cameras` | Camera roster with live-playback endpoints |
| `GET /api/events` | Query events by camera / time window / label |
| `GET /api/events/{id}/snapshot` · `/crop` · `/clip` | Event image assets and MP4 clip (Range-capable) |
| `GET /api/events/stream` | Server-sent events for live UI updates |
| `GET /api/live/hls/{camera}/…`, `POST /api/live/whep/{camera}` | Proxied HLS / WHEP signaling for live view |
| `GET /api/playback/{camera}/list` · `/api/playback/{camera}/get` | Recordings browser + fMP4 windows for the timeline scrubber (proxied from MediaMTX's playback server) |
| `GET /api/metrics` | Event counters + detector gauges (fps, queue, thermals) |

## Performance

Measured on the test device (mid-range 2019-era SoC, YOLO11n at 640×360, steady-state median of 60 runs): **CPU fp16 ≈ 89 ms/frame** vs **Vulkan (Adreno GPU) ≈ 370 ms/frame** — NCNN's ARM CPU kernels beat the mobile Vulkan path ~4× on this class of hardware, so `backend: cpu` is the default. Vulkan stays one config flip away; INT8 or a smaller `input_size` may shift the balance (`scripts/bench_phone.sh` re-measures in one command). A thermal governor halves or pauses the analysis fps as the SoC approaches its temperature ceiling and ramps back down afterward.

## Operations

```bash
adb shell su -c '/data/nvr/bin/nvrctl status'    # also: start | stop | restart | logs | watch
```

Device layout after deploy:

```
/data/nvr/
  bin/{nvrd,mediamtx,nvrdet,nvrctl,ffmpeg}
  config.yaml, secrets.yaml (600)      # mediamtx.yml + detector.json are generated
  events.db  recordings/  snapshots/  clips/  logs/  run/
```

- **Boot & watchdog** — the Magisk module starts everything after reboot (well under a minute) and runs `nvrctl watch`: exponential-backoff restarts and 5 MB log caps.
- **Retention** — events pruned by age and count; recordings by age and size. DB rows and their files die together.
- **Free-space guard** — below the configured floor it prunes, then pauses continuous recording via MediaMTX hot-reload while keeping detection alive; health reports `degraded` until space recovers.
- **Power** — wakelock keeps the SoC awake; an optional ACC integration holds the battery in a healthy charge band for always-plugged-in duty.

## Development

Everything that doesn't need the phone runs on a normal dev machine:

```bash
go test ./...                     # unit tests across all Go packages
scripts/smoke_local.sh            # config validation → mediamtx.yml → MediaMTX boots
scripts/build_arm64.sh --docker   # validate the exact phone artifact in an arm64 container
scripts/e2e_detector.sh           # full pipeline test: synthetic RTSP camera → detection → event → snapshot served
```

The detector is plain C++17 (CMake): Mac/desktop builds for development, an NDK (bionic) build for the device. The e2e script publishes a looping synthetic camera over RTSP and asserts that events land in the store with valid JPEG assets.

## Security notes

- The app shell is served unauthenticated so the PWA login gate works; **every `/api/` route still requires the token**.
- Secrets are write-only: no API ever returns them, and `GET /api/config` returns field-path redacted `${secret:…}` placeholders that survive a config round-trip.
- Notification deep links embed the API token (required for `<img>` assets); treat notification topics/chats as sensitive.
- Designed for trusted LANs / your own VPN. Don't expose port 8099 to the internet.
- Public HTTPS access without opening ports: [deploy/cloudflare/](deploy/cloudflare/README.md) fronts the UI with a Cloudflare Tunnel (outbound-only, the `tunnel` component in nvrctl) gated by Cloudflare Access; port 8099 itself stays LAN-only.

## Third-party

[MediaMTX](https://github.com/bluenviron/mediamtx) (MIT) · [NCNN](https://github.com/Tencent/ncnn) (BSD-3-Clause) · [YOLO11n](https://github.com/ultralytics/ultralytics) via PNNX export (AGPL-3.0 — applies to the model export pipeline) · FFmpeg (LGPL/GPL build config) · [stb_image_write](https://github.com/nothings/stb) (public domain) · [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (pure-Go SQLite).

## License

No license yet — add one before publishing (MIT/Apache-2.0 are typical for code; note YOLO11/Ultralytics is AGPL-3.0, which covers the model files).

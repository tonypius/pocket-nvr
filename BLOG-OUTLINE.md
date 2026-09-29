# Blog outline — PocketNVR: old phone → headless AI NVR

## Post metadata

**Working titles (pick one):**

1. "I turned my dead OnePlus 7 into a self-hosted AI security hub — and its GPU lost to its own CPU"
2. "PocketNVR: a rooted Android phone is the best NVR you already own"
3. "No Pi required: building a Magisk-supervised AI video recorder on Android"
4. "An old phone, three daemons, zero cloud: my always-on AI NVR"

**Dek (subtitle):** Continuous recording, on-device YOLO person detection, push alerts, and an installable web UI — running as root Linux daemons on a rooted Android phone, with no footage ever leaving the house.

**Audience:** Self-hosters and Android tinkerers. Assumes basic Linux; explain Magisk from zero (sidebar). No ML background required.

**Promises to the reader (the takeaways):**

- A reusable pattern: *static arm64 binaries + Magisk boot script = a root Linux appliance on any Android phone*
- Why "use the GPU for ML" failed on this SoC — with reproducible numbers
- A complete mental model of a small, self-hosted video pipeline (ingest → record → detect → events → notify → UI)
- A portability checklist so they can judge their own junk-drawer phone

**Format:** ~4,500–5,500 words as a single post, or split into a 3-post series (see Publishing notes). Heavy on diagrams, terminal output, and one benchmark table. Every claim maps to a runnable script in the repo — link them all.

**Throughline / narrative arc:** Every design decision traces back to one constraint — *this must run unattended, at the edge, on hardware nobody wants, and I must never have to SSH into it again*. The GPU plot twist is the cold open; the "appliance that survives neglect" is the payoff.

---

## §1. Cold open — the benchmark that broke my assumptions (~300 words)

Lead with the plot twist; promise the how-to.

| Backend | YOLO11n @ 640×360, OnePlus 7 (SD855), steady-state median of 60 runs |
|---|---|
| CPU fp16 (NCNN ARMv8 NEON kernels) | **≈ 89 ms/frame** |
| Vulkan (Adreno 640) | ≈ 370 ms/frame |

- Everyone says "offload ML to the GPU." On this phone the GPU is ~4× slower for this workload.
- Hook: this is the story of building the whole appliance around that surprise — and why Vulkan stays exactly one config flip away (`backend: cpu|vulkan`).
- One-paragraph project teaser: what PocketNVR does (record 24/7, detect persons/cars/cats, alert with snapshots, serve a PWA) so the reader knows what the benchmark is *for*.
- **Assets:** bench table; raw `scripts/bench_phone.sh` output (framed terminal screenshot).

## §2. Why a rooted phone (and why not an app, not a Pi) (~450 words)

**The hardware case — an old flagship is a absurdly good $0 SBC:**

- 8-core big.LITTLE SoC, real GPU, hardware H.264 encode/decode, Wi-Fi, and a built-in UPS (the battery) — vs. a Pi + power bank + camera hat + case.
- 2019 flagship ≈ SD855-class: more sustained CPU than a Pi 5 at lower idle power, in a chassis with a thermal solution already attached.
- Everyone has one in a drawer.

**The software case — native daemons, not an Android app (ENV-7 in the FRS):**

- An app fights the platform: activity lifecycle, Doze, background-execution limits, Play Store policy. An appliance *is* the platform.
- Decision: plain arm64 Linux daemons running as root, supervised at boot by Magisk. Android is just the kernel + driver vendor.
- Why not Frigate/ZoneMinder in Termux or a chroot: container gymnastics on Android, proot overhead, and I wanted daemons that start before Android finishes booting.

**Honest downsides, up front:** unlocked bootloader = voided warranty; root = you're the security boundary; battery aging is real (§12); camera quality depends on your IP cams, not the phone (the phone is the *recorder*, not the camera — clear up this misconception early).

- **Sidebar: "Magisk in 200 words"** — what it is, why `service.sh` runs as root at boot, and that KernelSU/APatch honor the same hook.
- **Sidebar: "Why not the phone's own camera?"** — no standard root-level API for the camera HAL; IP cams with RTSP are the commodity path.

## §3. Phase 0: prove the runtime model before writing real code (~350 words)

Two throwaway smoke tests, days before any architecture:

- **Smoke A (`scripts/smoke_test_a.sh`)** — a `CGO_ENABLED=0` static Go hello-world: `adb push`, `su -c`, prints `uid:0`, binds a port, survives a reboot from a Magisk script. "Static binaries ignore Android's libc entirely" — the entire portability story (§14) proven in one test.
- **Smoke B** — NCNN cross-built with the NDK (Vulkan on), YOLO11n running on the Adreno 640; detections match the desktop within float drift.
- The meta-lesson, stated plainly: **burn the two biggest structural risks first, not the fun part.** "Can root daemons exist at boot on this ROM" and "does the ML stack run on this GPU" each cost ~an hour and retired the project's existential questions.

## §4. Architecture tour — three daemons, zero Android APIs (~500 words)

- **The diagram (hero asset):**

```
IP cameras ──RTSP──► MediaMTX ──► recordings/ (fMP4 segments)
                        │ localhost sub-stream
                        ▼
                   ffmpeg pipes ──► nvrdet (C++/NCNN)
                                     motion gate → YOLO11n → zones → event state machine
                                        │ events + JPEG (HTTP ingest)
                                        ▼
   browser (PWA) ◄── nvrd (Go, one static binary)
     • REST API + SSE   • SQLite event store   • ntfy/Telegram
     • clips (stream-copy)   • retention + free-space guard   • HLS/WHEP proxies
```

- Division of labor, and *why each boundary sits where it does*:
  - **MediaMTX** (vendored prebuilt): RTSP ingest, segmented recording, restream — do not reimplement a media server.
  - **nvrdet** (C++17/NCNN): the only latency-sensitive code; owns the camera sub-stream loop.
  - **nvrd** (Go, `CGO_ENABLED=0`, pure-Go SQLite): everything else — one process, one supervisor target. (FRS explicitly allowed merging the event/notify/API components; DT-3.)
  - **nvrctl** (`deploy/nvrctl`): shell entrypoint — start/stop/status/logs/watch. Supervision logic in shell, on purpose.
- **Config as single source of truth:** hand-written `config.yaml` + `secrets.yaml` (chmod 600, `${secret:key}` interpolation, percent-escaped into URLs); *generated* `mediamtx.yml` and `detector.json` are derived artifacts, regenerated at deploy and at every boot — derived files are never trusted.
- SIGHUP hot-reload everywhere; an invalid config keeps the last good state.
- **Assets:** architecture diagram; the `/data/nvr/` tree from the README.

## §5. Ingest & recording — let MediaMTX be the media server (~300 words)

- One process owns the cameras: RTSP pull with per-camera credentials, reconnects, restream, and fMP4 segmented recording to disk.
- **The two-stream split (the load-bearing trick):** main stream is *recorded but never decoded*; a low-res sub-stream over *localhost RTSP* is the only thing decoded for analysis. Camera load, bandwidth, and CPU cost all decouple from detection.
- Config generation: camera list in `config.yaml` → `internal/mediamtx` renders paths `/<cam>` and `/<cam>_sub` — camera config exists in exactly one place (FR-CFG-1).
- Recording = stream-copy segments; clips later cut from these segments with zero re-encode (§8).
- **Asset:** annotated `mediamtx.yml` excerpt with the generated-vs-hand-written parts labeled.

## §6. The detector daemon — the actual ML part (~700 words, deepest section)

Walk the pipeline stage by stage (all files in `detector/src/`):

1. **Capture** (`capture.cpp`): one spawned static `ffmpeg` per camera reading the localhost sub-stream, rawvideo over a pipe. Decision DT-2: *spawn ffmpeg, don't link libavcodec* — keeps the NDK build surface tiny and gets RTSP reconnect/timeout flags for free. Bounded backlog; drop-oldest under load.
2. **Motion gate** (`motion_gate.cpp`): grayscale downscale + frame diff, per-camera area threshold. Most frames die here — inference is the expensive thing, motion is the cheap thing.
3. **Single inference worker + bounded queue** (`frame_queue.cpp`, `backend_ncnn.cpp`): one NCNN worker, drop-oldest. Deliberately *not* a thread pool: the phone has 4 cameras and one thermal budget; queue depth is the pressure valve, not parallelism.
4. **Zones + postproc** (`zones.cpp`, `coco.h`): NMS, confidence, class filter (person/cat/dog/car via config), point-in-polygon zones with anchor modes (any/center). Empty zone list = whole frame.
5. **Event state machine** (`event_state.cpp`): enter/exit frames, peak-score tracking, cooldown — including *silence-fed negatives* so a gated scene still closes events (a subtle bug class: without it, motion-gated cameras never emit "all clear" and events hang open).
6. **Emission** (`http_post.cpp`, `snapshot.cpp`): JSON event + annotated peak-frame JPEG (detection boxes drawn) + cropped object thumbnails, POSTed to `nvrd`'s localhost ingest.
7. **Governor + ops** (`governor.cpp`): SoC thermal governor halves/pauses analysis fps near the temperature ceiling and ramps back; 10 s gauge heartbeat; SIGHUP hot-reload of `detector.json`.
- **Model pipeline:** `scripts/fetch_model.sh` — YOLO11n → PNNX → NCNN, fully scripted. Verification scar: confirm tensor names/shapes by hand once (`84×8400` output; person = class 0 = row 4) before blaming the detector for "wrong" results.
- **Asset:** flow diagram of the detector stages; annotated snapshot JPEG with boxes+zones drawn.

## §7. Plot twist, explained — why the GPU lost (~400 words)

- The numbers from §1, plus methodology: production frames (not synthetic tensors), steady-state medians of 60 runs, via `scripts/bench_phone.sh` — one command, readers can reproduce on their own SoC.
- *Why* CPU wins here: NCNN's ARMv8 fp16 NEON kernels are hand-tuned excellent; mobile Vulkan drivers pay per-dispatch overhead, and a small model at 640×360 is all dispatch, no arithmetic intensity to amortize.
- The generalized lesson: **"GPU = faster" is a benchmark result, not a law of nature.** On small models at the edge, measure. Same lesson class as "/desktop intuition doesn't survive contact with mobile".
- Engineering response, not just diagnosis: `backend:` is one config key; default `cpu`, Vulkan kept warm for the day INT8 or a smaller input size flips the balance. The bench harness stays in the repo so new hardware gets re-measured, not guessed.

## §8. Events, clips, notifications — from detection to a phone buzz (~450 words)

- **Event store:** SQLite via `modernc.org/sqlite` (pure-Go, keeps the CGO-free static binary). Schema shaped around queries the UI actually makes: by camera/time-window/label.
- **Clips without re-encoding (FR-EVT-3):** event `[start−pre, end+post]` maps onto MediaMTX's recorded segments → stream-copy concat. No ffmpeg transcode pass, no CPU spike, sub-second extraction. This is the dividend of §5's "record everything as segments" decision — architecture paying forward.
- **Notifications:** ntfy and/or Telegram; per-channel score thresholds, per-camera cooldowns, quiet hours, snapshot attach, and **a retry queue that replays undelivered events after restart** — offline hours don't lose alerts, they arrive late.
- Deep links carry the token so `<img>` assets render in the notification; note the operational caveat (treat topics/chats as sensitive).
- **Retention, holistically:** events by age+count, recordings by age+size, and DB rows die *with* their files — no orphan JPEGs, no dangling rows.
- **Free-space guard:** below the floor, prune first; if still tight, *pause recording only* (MediaMTX hot-reload) while detection stays alive; health reports `degraded` until recovery. An appliance that fills its disk is bricked; this is the airbag.
- **Asset:** notification screenshot (phone lock screen with snapshot); `GET /api/events` JSON excerpt.

## §9. nvrd — the API surface, auth, and the secrets model (~350 words)

- One token guards every `/api` route (`Authorization: Bearer`, `X-Api-Token`, or `?token=` for img tags/deep links); only the static app shell is open so the PWA can render its own login gate.
- **Write-only secrets:** no API ever returns a secret value. `GET /api/config` returns field-path-redacted `${secret:…}` placeholders that survive a config round-trip — the Settings page can edit config without ever seeing credentials.
- **The scar (good material):** the first redaction implementation replaced by *value*, and two cameras sharing a password cross-contaminated each other's placeholders on a round-trip. Field-path redaction (redact by where it sits in the YAML tree, not by string match) fixed it. General lesson: never transform configs by matching secret *values*.
- Other routes worth a table: health (component status + SoC temp + free space), discover, events + snapshot/crop/clip (Range-capable), SSE stream for live UI updates, metrics (detector fps/queue/thermals), HLS/WHEP proxies.
- **Asset:** API table from README, trimmed; maybe a curl session.

## §10. The UI you actually use — a PWA, deliberately (~400 words)

- Decision DT-9: an installable PWA served by `nvrd` itself — zero sideloading, works from any household device, auto-updates with the server; a native viewer remains a later option against the same API.
- **Live view:** WebRTC/WHEP for sub-second latency, proxied through `nvrd`; **HLS proxy fallback** when WebRTC's UDP can't pass (restrictive networks, TCP-only tunnels — this matters again in §13). Player picks automatically.
- **Events / Detections / Clips:** snapshot thumbnails, cropped object chips, filters by camera/label/time; a Clips feed plays event MP4s inline (previews load only while on screen, so a long grid stays cheap on mobile data — it replaced Timeline in the nav).
- **Recordings playback:** MediaMTX's playback server proxied through the API — list recorded segments, pull fMP4 windows, scrub.
- **Settings:** full camera add/edit with **ONVIF WS-Discovery + an RTSP sweep of the /24** (`POST /api/discover`) — one-click "found 3 cameras on your LAN" is the demo moment; secrets editor (write-only, §9); every save runs server-side validation and a SIGHUP reload.
- **Asset priority:** discovery scan screenshot (the money shot), live grid, events timeline. Screen-recording GIF of add-camera-by-discovery → live view flow.

## §11. Making it survive reboots — the Magisk module (~550 words)

**Philosophy: the module is deliberately thin.** `module.prop` + `customize.sh` + `service.sh` own *boot*; all binaries/config live in `/data/nvr`, owned by `deploy.sh`. Rationale: payload updates shouldn't require re-flashing a zip; removing the module must not nuke recordings.

**`service.sh` walkthrough — each step earned a scar:**

1. Wait for `sys.boot_completed` (poll, 60 × 2 s).
2. Wait for a default route. **War story:** the first version watched `ip route show` (main table) and waited forever — Android keeps the default route in *per-network* tables; `ip route show table all` was the fix. (This is the post's best "I lost an evening so you don't have to" beat.)
3. Acquire the kernel wakelock: `echo pocketnvr > /sys/power/wake_lock` — Doze never pauses recording. Kernel-level, not app-level.
4. Regenerate `mediamtx.yml` + `detector.json` from `config.yaml` *at every boot* — derived files are never trusted across reboots.
5. `nvrctl start` in dependency order, then `nohup nvrctl watch` — the watchdog: exponential-backoff restarts, 5 MB log caps.
6. Hand-off to the free-space guard and retention loops inside `nvrd` (§8).
- **Acceptance test as content:** `scripts/install_boot_module.sh --reboot` reboots the phone and asserts the whole stack is back in under a minute — show the log excerpt.
- **Asset:** annotated `service.sh`; supervisor log excerpt spanning a forced reboot.

## §12. Power & thermals — living with a battery at 100%, 24/7 (~300 words)

- The failure mode nobody blogs about: sustained 100% charge = swollen battery in months.
- **ACC (Advanced Charging Controller)** holds the 60–70% band (pause at 70, resume at 60) — pause charging, drain a little, repeat. Persists across reboots. Credit vr25/acc; per-device quirks apply.
- The battery's *second life*: it's the UPS. Power blips are a non-event; a Pi + SD card is not.
- Thermal story: the governor (§6) plus physical placement — windowsill in summer is the design case, not the edge case.

## §13. Remote access without opening a port (~350 words)

- Default posture: LAN-only + your own VPN (Tailscale works out of the box; any TCP tunnel works).
- The WebRTC problem: live view's WHEP path wants UDP, which VPNs and tunnels often won't carry. **This is why the HLS proxy fallback exists (§10)** — HLS is plain HTTP, so live view still works over any TCP-only path. Design for your worst transport, not your best.
- The fully managed option: **Cloudflare Tunnel (outbound-only) + Cloudflare Access** in front of the UI (`deploy/cloudflare/`) — public HTTPS, zero open ports, identity-gated; port 8099 itself stays LAN-only. `nvrctl` grows a `tunnel` component to supervise `cloudflared` like any other daemon.
- Threat-model honesty: even with a tunnel, this serves *video from inside your home* — keep Access on, keep the token long.

## §14. Will this run on YOUR phone? — portability checklist (~350 words)

What's portable by construction vs. what's per-device:

- ✅ **Module mechanics:** `service.sh` uses only `getprop`, `ip`, `/sys/power/wake_lock`, and POSIX shell — no OnePlus/LineageOS specifics. Any Magisk/KernelSU/APatch device honors the same boot hook.
- ✅ **Payload:** every binary is static arm64 — `nvrd` (CGO off), `mediamtx` (static Go), `ffmpeg` (static), `nvrdet` (NDK/bionic). Static beats the glibc/bionic question entirely.
- ⚠️ **Per-device caveats (the honest list):**
  1. arm64 only — 32-bit phones are out.
  2. `/sys/power/wake_lock` needs `CONFIG_PM_WAKELOCKS`; without it the log says `wakelock FAILED` and the phone may deep-sleep mid-stream.
  3. Inference perf varies by SoC — Mali Vulkan drivers are weaker than Adreno; CPU default is the portable choice. Re-run `bench_phone.sh` on new hardware.
  4. Thermals scale with chassis size.
  5. ACC is a separate module with per-device config.
  6. Wi-Fi: keep-on-during-sleep + static IP/hostname, per-ROM settings.
- Future idea to tease: a "fat zip" module variant bundling the `/data/nvr` payload for one-flash installs on a fresh phone.

## §15. How it was built — the Mac-first dev workflow (~300 words)

The phone is the *target*, not the *dev environment*:

- Everything that doesn't need the phone runs on the Mac: `go test ./...`, the detector's CPU build, MediaMTX locally.
- `scripts/smoke_local.sh` — config validation → `mediamtx.yml` generation → MediaMTX actually boots with it.
- `scripts/build_arm64.sh --docker` — Apple Silicon runs arm64 Linux containers natively (no qemu), so the *exact phone artifacts* get validated before they ever touch a USB cable.
- `scripts/e2e_detector.sh` — full pipeline test: a looping synthetic camera published over RTSP → detection → event → snapshot served; asserts valid JPEG assets. CI-able end-to-end with zero cameras and zero phones.
- `scripts/deploy.sh --start` — cross-compile, **verify the ELF artifacts** (a wrong-arch binary fails here, not on the phone at 2 a.m.), push over adb (USB or wireless), generate runtime configs, start via `nvrctl`.
- Meta-point for readers: the ratio of "works without the hardware" test surface to "needs the hardware" is what made this buildable evenings-and-weekends.

## §16. Wrap-up — costs, caveats, what's next (~250 words)

- **Parts list:** one used rootable phone (check LineageOS support lists), a stand, a charger, existing IP cameras. $0–$50.
- **License posture, honestly:** code MIT/Apache-typical; YOLO11/Ultralytics export pipeline is AGPL-3.0 and covers the model files — flag it before readers cargo-cult the repo.
- **What's next:** 4-camera soak results, Tailscale as the blessed remote path, the native-viewer option against the same API, INT8 model revisit.
- **Disclaimers:** unlocked bootloader/warranty; battery safety; only point cameras where it's legal to.
- **CTA:** repo link; "run `bench_phone.sh` on your own drawer phone and post the numbers" — a built-in engagement loop.

---

## Sidebars (boxed, scattered through the post)

1. **"Magisk in 200 words"** (§2) — boot-time root scripts for readers who've never rooted anything.
2. **"Why not Termux/chroot/Frigate?"** (§2) — the alternative-universe version and its costs.
3. **"Why not the phone's own camera?"** (§2) — HAL dead-end vs. commodity RTSP cams.
4. **"Reading NCNN output tensors"** (§6) — the `84×8400`, class-0-row-4 verification trick.
5. **"Static binaries vs. bionic"** (§3/§14) — why `CGO_ENABLED=0` and NDK-static sidestep Android's libc entirely.

## Asset checklist

- [ ] Hero diagram (§4 architecture, redrawn clean — README ASCII is the source of truth)
- [ ] Bench table + framed `bench_phone.sh` output (§1, §7)
- [ ] Detector pipeline diagram (§6)
- [ ] Annotated snapshot JPEG with boxes/zones (§6)
- [ ] Lock-screen notification screenshot (§8)
- [ ] Discovery scan screenshot + live grid + events timeline (§10)
- [ ] Annotated `service.sh` + reboot-spanning supervisor log excerpt (§11)
- [ ] `/data/nvr` tree, generated-vs-hand-written config excerpt (§4–5)

## Publishing notes

- **Series option:** split at §6/§7 and §11 —
  - Post 1: premise + Phase 0 + architecture (§1–5)
  - Post 2: detector + benchmark + events/UI (§6–10)
  - Post 3: appliance hardening — Magisk, power, remote access, portability, workflow (§11–16)
  Each post stands alone; Post 2 carries the SEO ("on-device YOLO on a phone"), Post 1 carries the social hook (the GPU twist).
- **Shareable bits:** §§1, 7 (benchmark), §11 war story (route tables), §14 checklist. These are the screenshots/quotes that travel.
- **Credibility move:** every number and claim links to the script that produces it (`bench_phone.sh`, `smoke_test_a.sh`, `e2e_detector.sh`, `install_boot_module.sh --reboot`). "Reproduce it yourself" is the post's spine.
- Word budget above totals ~5,600; trim candidates if needed: merge §12 into §11, compress §15 to a bullet list.
- Title/dek A-B test if cross-posting (HN prefers #1's twist; a self-hosting subreddit audience prefers #2's utility framing).

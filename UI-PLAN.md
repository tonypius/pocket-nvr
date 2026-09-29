# PocketNVR — UI Upgrade Plan (Scrypted-class viewer)

Companion to `PLAN.md` (build plan) and `NVR-FRS.md`. Goal: bring the PWA to
feature parity with the Scrypted NVR UI (and selectively beyond it), constrained
by our reality: a phone-class appliance, a zero-build-step PWA served by `nvrd`,
MediaMTX doing record/restream, and YOLO11n on NCNN.

Written 2026-09-09. No code yet — this is the plan.

---

## 1. What the Scrypted UI actually does (from the screenshots)

Four views, one dark shell with a left icon rail (Live, Grid, Search), a
**dark/light theme toggle** (moon icon) + settings in the top bar:

| View | What it shows |
|---|---|
| **Home** (`#/`) | Horizontal strip of recent event thumbnails (cropped objects, time under each) above a strip of live camera tiles (timestamp burn-in, name, red live dot). |
| **Camera Grid** (`#/grid`) | Multi-cam live grid (2×2 default) with per-tile timestamp overlay, camera name, live dot. Right rail: **Timeline / Events** tabs, two rows of **event-type filter chips** (person, pet, car, package, doorbell, …), a **live-updating event list** (cam, time, duration "26s", thumb) with a LIVE marker, date pill. |
| **Detections / Search** (`#/detections`) | Grid of **cropped object thumbnails** (not full frames) with time + camera under each. Top bar: camera dropdown, date picker, **object-type icon filters**, free-text **search**. This is the "find that thing I saw" page. |
| **Timeline** (`#/timeline/42`) | Single-camera full player: pause, skip, **speed (1×…)**, jump-to-live, mute/two-way-talk, share, PiP. Center: **vertical recording timeline** — continuous coverage bars per day, **event markers with type icons + hover thumbnails**, scrub tooltip ("9 sec 10:07:58 AM"), LIVE button, date pill, **Timeline / Events / Stories tabs** + filter chips. Far right: filmstrip of mini live tiles for the other cameras. |

The load-bearing ideas: (a) events are first-class objects with **cropped
thumbnails and types**; (b) a **live event rail** accompanies the live grid;
(c) the timeline scrubs **continuous recordings**, not just event clips, with
event markers overlaid; (d) everything is filtered by **camera + date + type**.

## 2. Evaluation of other tools — what to steal, what to skip

- **Frigate (0.17 stable, 0.18 beta)** — the closest open-source analog. Steal:
  the **Review** model (events as review items on a vertical timeline),
  filter chips + calendar, **UI zone/mask editor** (draw polygons on a
  snapshot), System/stats page, **export workflow** (pick range → name →
  download; 0.18 adds single-click and multi-cam exports), low-bandwidth
  "skeleton" mode. Skip for us: semantic/natural-language search (embedding
  models won't fit the phone's CPU budget next to YOLO), Frigate+ model
  training. 0.18's "draw a region to find motion in history" is clever but
  depends on server-side motion indexing we don't have — stretch goal at best.
- **UniFi Protect** — the polish benchmark. Steal: the **dual-layer scrubber**
  (recording-coverage bar with motion/event ticks underneath), **saved grid
  layouts** and cycle views, trimmed downloads, tight mobile ergonomics.
  Skip: its server ecosystem; we keep our token-auth PWA.
- **Scrypted NVR** — Steal: the four views above, **Stories** (auto-grouped
  event narratives per subject/camera — cheap grouping of events by
  camera+label+time proximity; the AI-generated captions version needs an LLM
  sidecar → stretch), two-way talk (our CP Plus cams may support ONVIF
  audio-back — flag as optional hardware-dependent).
- **Blue Iris / ZoneMinder / MotionEye / Shinobi / Viseron** — cautionary
  tales: deep config, dated or utilitarian UX. Lesson: **filter power without
  polish loses to polish with filters.** Viseron shares our architecture
  philosophy but has a thinner UI than we should accept.
- **Synology Surveillance Station** — good timeline + event list polish and
  solid mobile apps; the per-camera-license model is what we're the
  counter-example to.

Net: the Scrypted view set is the right skeleton; Frigate contributes the
review/filter/editor details; UniFi contributes scrubber and layout ergonomics.

## 3. Current state audit (what we already have)

- **UI** (`ui/`, ~500 lines vanilla JS): login gate; Dashboard = live WHEP
  tiles (HLS fallback) with name + dot; Events = flat list filtered by camera
  + time range, modal with snapshot + clip; Settings = discovery scan, camera
  editors, recording/detection/notify fields, write-only secrets editor.
- **PWA today**: manifest (`display: standalone`, 192/512 icons) + service
  worker (network-first shell cache, `/api/*` and streams bypassed) — it
  already installs to a home screen. Gaps: no `apple-touch-icon` / iOS
  standalone meta (iOS install loses chrome and icon), `viewport-fit=cover`
  is set but no safe-area insets are used, `100vh` breaks under the iOS
  address bar, no install-prompt UX (Android) or add-to-home-screen hint
  (iOS), no wake lock, no offline banner or update toast, `hls.min.js` is
  not in the precache list.
- **API**: health, metrics, config GET/PUT, secrets POST, discover, cameras,
  events list/detail/snapshot/clip, live WHEP + HLS proxy.
- **Store**: `events(camera_id, label, score, start_ts, end_ts, zone, bbox,
  snapshot_path, clip_path, notified)` — `label` is always `person` today
  (detector is person-only), `bbox` is stored but unused by the UI.
- **Detector**: YOLO11n person-class filter, zones already supported in config
  (no UI to draw them), peak-frame full-frame snapshot, clip concat.
- **MediaMTX v1.21.0**: continuous recording to disk; **playback server**
  (`playback: yes`) exposes `/list` (timespans) and `/get` (fMP4/MP4 for
  path+start+duration) — verified in upstream docs, present in our version,
  currently **not enabled or proxied**.

## 4. Gap analysis → work items

### Backend enablers (nvrd / Go / detector)
| # | Item | Why |
|---|---|---|
| B1 | **Detection crops**: detector crops the bbox region from the peak frame and POSTs/stores `crop_path` alongside `snapshot_path` (bbox is retained for overlay) | The Detections grid, Home strip, and event rail all use cropped thumbs — full frames read as noise |
| B2 | **Multi-class detection**: make the detector class list config-driven per camera (`classes: [person, car, dog…]`, COCO gives vehicle/animal free); store real `label`; per-label notification filters | Type filter chips are meaningless with one class |
| B3 | **Live event push**: SSE endpoint `GET /api/events/stream` (stdlib Go; EventSource auto-reconnects; polling fallback kept) | The Events rail must update live like Scrypted's |
| B4 | **Recordings inventory + playback proxy**: enable MediaMTX playback server on localhost; nvrd proxies `GET /api/recordings/{camera}?day=` (→ `/list`) and `GET /api/recordings/{camera}/clip?start=&duration=` (→ `/get`, token-checked, range-aware) | Timeline scrubbing through continuous recordings |
| B5 | **Search/query upgrades**: `/api/events` gains `label`, `zone`, `from/to` (date), `q` (LIKE over label/zone/camera), `has_clip/has_snapshot`, cursor pagination; expose `duration` | Search page + calendar + infinite scroll |
| B6 | **Event mutations**: `POST /api/events/{id}/export` (arbitrary start/end range → concat → `exports/` + download), `DELETE /api/events/{id}`, starred flag | Export/share/review workflows |
| B7 | **System summary endpoint**: fold detector gauges (fps, infer ms, backend, thermal state) + storage + retention + daemon uptime into one `/api/system` | System page |

### UI work (PWA)
| # | Item | Views touched |
|---|---|---|
| U1 | **App shell v2**: bottom tab bar on phones (Home / Live / Events / Search / ⚙), left icon rail on desktop, PWA shortcuts (Live, Events) | All |
| U12 | **Theme system (dark + light)**: toggle (moon/sun) in the top bar like Scrypted's. Move every hardcoded color in `style.css` to CSS custom-property tokens; themes switch via a `data-theme` attribute on `<html>`; default follows `prefers-color-scheme`, explicit choice persisted in `localStorage`; tiny inline boot snippet applies the saved theme before first paint (no flash of wrong theme); update `<meta name="theme-color">` and the PWA manifest `theme_color` at runtime; verify login, tiles, modal, and rail in both themes | All |
| U13 | **PWA & mobile-compatibility hardening** (it installs today; this makes it first-class): <br>• *Install UX*: capture `beforeinstallprompt` → in-app "Install" action (Android/Chrome); iOS gets an add-to-home-screen hint (no programmatic install there); add `apple-touch-icon` + `apple-mobile-web-app-*` meta; richer manifest (`id`, `scope`, `categories`, screenshots, and Live/Events shortcuts tying into U1) <br>• *SW v2*: full precache list (add `hls.min.js`, icons), versioned cache + "update available — reload" toast instead of silent swaps, explicit never-cache for `/api` + WHEP/HLS, snapshot thumbs get a bounded LRU cache when they proliferate (lands with Phase B's U7), offline banner with auto-retry <br>• *Mobile ergonomics*: safe-area insets on tab bar/top bar (`env(safe-area-inset-*)`), `100dvh` instead of `100vh`, ≥44 px touch targets, `touch-action` to suppress double-tap zoom, momentum-scroll containment, swipe-down closes modal/sheet, Android back button closes overlays (one `history` entry per overlay, so back never exits the app), landscape single-cam fullscreen <br>• *Viewing ergonomics*: **Wake Lock** while Live/Timeline is visible (a camera viewer that lets the screen sleep is broken), `playsinline` + muted-autoplay resume when returning to the tab, offscreen tile decoding paused via IntersectionObserver (shared with U3's data-saver) <br>• *Stretch inside U13*: Web Push from `nvrd` (VAPID keys, subscription table in SQLite) so the installed PWA notifies without ntfy/Telegram — iOS only allows push for *installed* PWAs, which makes this the natural end-state of the install story | All |
| U2 | **Home**: recent-events strip (cropped thumbs, day-grouped) + cameras strip; tap event → detail, tap camera → single live view | New |
| U3 | **Live grid**: density switch (1/2/4/9/all), responsive, persisted layout; overlays (name, live dot, connection state); tap → fullscreen single cam; audio toggle where the stream has audio; auto-reconnect tuning; data-saver mode (sub-stream-only / pause offscreen tiles via IntersectionObserver) | Dashboard |
| U4 | **Live event rail**: right sidebar (desktop) / swipe-up sheet (mobile) with type-filter chips, live SSE feed, duration, thumb → event detail or "jump to timeline at t" | Live, all |
| U5 | **Events page v2**: cropped thumbs, day grouping + calendar date picker, type chips, camera + zone filters, text search, infinite scroll, unread separator ("new events since last visit") | Events |
| U6 | **Event detail v2**: label-aware title ("Person · Driveway · 26s"), bbox overlay toggle on snapshot, clip player, share (Web Share API) + download, delete, star, prev/next, deep-linkable `#/events/{id}` (notification links land here) | Modal → page |
| U7 | **Detections/Search page**: grid of cropped object thumbs (B1), type icon filters, camera dropdown, date, text search (B5), pagination | New — Scrypted's killer view |
| U8 | **Timeline view** (flagship): single-cam player fed by B4; vertical scrubber (desktop right / mobile bottom — better than Scrypted's portrait weakness) with coverage bars, event markers + type icons, thumbnails on markers; drag-scrub fetches bounded fMP4 windows (~30 s around target); speed 0.5–8×, jump-to-live, prev/next event, date + camera pickers, share/PiP; other-cam filmstrip | New — biggest build |
| U9 | **Zones editor**: draw polygons on a camera snapshot, name zones, save via existing validated `PUT /api/config` (detector already consumes zones) | Settings |
| U10 | **System page**: SoC temp, per-cam fps, infer ms, backend (cpu/vulkan), thermal state, storage used/free + retention, uptime, component health | New |
| U11 | **Exports list**: named exports with download/share (B6) | New (small) |

### Explicitly out of scope (for now)
- Semantic/text-embedding image search (phone CPU budget); AI captions (needs
  an LLM sidecar — revisit when a home server enters the picture); PTZ and
  two-way audio (hardware-dependent; note as hardware checklist items);
  native Android viewer (PWA stays the surface; API is already viewer-ready).

## 5. Phasing (each phase is independently shippable)

**Phase A — Quick wins, UI-mostly** (U1, U12, U13, U2, U3, U5-lite, U6-lite)
Shell with bottom tabs, theme system (dark + light), PWA/mobile hardening
(install UX, safe areas, wake lock — the thumb-cache and push sub-items of
U13 defer to their dependent phases), Home page, live-grid density +
overlays, events page regrouped by day with thumbnails + better detail
(share/download/star need B6 only for delete/star — can land with clip
download that already exists).
*Done when:* phone PWA feels like an app — tabs, home strip, grid density.

**Phase B — Live feed + Search** (B1, B3, B5, U4, U7, U5)
Crops land in the detector; SSE feed; events query upgrades; the rail and the
Detections page. *Done when:* new events appear live on the rail, and "find
the dog I saw Tuesday" takes two taps.

**Phase C — Timeline** (B4, B6, U8, U11)
Enable + verify MediaMTX playback on-device, proxy in nvrd, build the
scrubber/player. Riskiest phase (iOS fMP4/`<video>` behavior needs a spike
early — fall back to shorter MP4 windows if range-seeking misbehaves).
*Done when:* scrub any of the last N days of a camera, land on events, export
a trimmed clip.

**Phase D — Config UX + classes** (B2, B7, U9, U10, U5-filters)
Multi-class detection end-to-end with per-label notify filters, zones editor,
System page. *Done when:* zones are drawable, car/animal events appear as
chips, health is visible at a glance.

**Stretch**: Stories-style grouping (camera+label+time clustering), export
timelapse, region-based motion search, ONVIF PTZ, two-way audio.

## 6. Architecture decisions & risks

- **Keep the zero-build PWA** (DT-6/9 holds), but split `app.js` (~400 lines
  today, will exceed 1.5k) into ES modules (`/ui/js/views/*.js`). If state
  wiring (rail + SSE + grid + player) gets messy, vendor **preact + htm ESM
  builds** (~4 KB, no build step, no npm) — decision point at Phase B start.
- **SSE over WebSocket** — stdlib, one-way is all we need, EventSource
  reconnects for free; falls back to the existing 10 s polling.
- **Service worker stays network-first** for the shell (updates land on a
  reload — right trade for a self-hosted app where the "server" ships the
  UI); `/api` and media streams are never cached; thumbs get a bounded cache
  only. iOS is the reference device for every mobile check (strictest PWA
  engine for this use case: no install prompt, push needs installed PWA,
  safe-area/dvh quirks); Web Push is the stretch end-state of the install
  story, not a Phase A item.
- **fMP4 windows for scrubbing** — MediaMTX `/get` remuxes a time range per
  request; the player fetches ~15–30 s windows around the scrub target and
  swaps sources (standard pattern; avoids storing anything new).
- **Crops at event time** (detector has frame + bbox in hand) — cheaper and
  better quality than re-slicing JPEGs in nvrd later; bbox stays for overlays.
- **Multi-class event volume** — car-heavy driveways will multiply events;
  pair B2 with per-label cooldowns/notify filters (config already has
  cooldown plumbing) so notifications don't regress.
- **iOS Safari** is the primary PWA target (user's phone) — every player
  change must be checked there, especially `<video>` + fMP4 + `playsinline`.
- **MediaMTX playback on the phone** — unverified on-device (CPU cost of
  remux-on-request is unknown); measure in Phase C spike before committing to
  scrub-anywhere; worst case we cap scrub windows near events.

## 7. Decisions taken in this plan (changeable)

1. Scrypted's four-view skeleton adopted; Frigate contributes filters/editor;
   UniFi contributes scrubber ergonomics. Not copying Stories/AI captions
   (stretch only), semantic search (skip).
2. Phase order A→B→C→D: visible wins first, flagship timeline third where it
   can be built against stable rails, detector changes last so UI work never
   blocks on the C++ side (B1 crop is the one early detector touch, isolated).
3. Vanilla-ES-modules-first, preact/htm as the sanctioned escape hatch.

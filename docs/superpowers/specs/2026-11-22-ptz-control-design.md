# PTZ camera control from the live view — design

Date: 2026-11-22
Status: draft for approval

## Intent and why

Some of the owner's cameras can physically turn (pan/tilt) and zoom. Today PocketNVR can only watch whatever the camera is currently pointing at. The owner wants to look elsewhere **on demand, from the camera's own live feed**, without opening the vendor app (Tapo, CP Plus) for that one job.

The use is occasional and ad hoc: open a camera, glance around, put it back or leave it. It is not a surveillance-automation feature.

PTZ is already listed in `UI-PLAN.md` as a stretch item ("ONVIF PTZ") and as "hardware-dependent" out-of-scope work. This design promotes it.

## What the owner decided

- Control happens **from the single-camera live view** (`#/live/{id}`, reached by tapping a tile on Live).
- Layout is **option A: buttons floating over the video.** D-pad at bottom-right, zoom + / − at bottom-left. (Rejected: drag-on-video gestures, and a panel below the video.)

## Goals

1. From the single-camera view of a PTZ-capable camera, the user can pan, tilt and zoom it.
2. Hold a button → camera moves; release → camera stops. Sweeping across a scene must work.
3. Cameras that cannot move show no controls, with no manual per-camera setup required.
4. Works through the same remote-access paths as the rest of the UI (Tailscale/TCP tunnel). The browser never talks to the camera directly.
5. Uses the credentials the camera already has in `secrets.yaml`; no new secret to enter in the common case.

## Non-goals (this version)

- Saved positions / presets, "go home", patrol tours.
- Auto-tracking of detected people.
- PTZ controls in the multi-camera grid.
- Absolute positioning or click-to-center on the picture.
- Non-ONVIF vendor protocols (Tapo cloud API, Dahua CGI/HTTP API).
- Per-user permissions on who may move a camera (the existing single API token applies).

Presets are the most likely follow-up; the design should not make them hard to add, but nothing for them is built now.

## Anti-goals (would make this a failure even if it "works")

- A camera that keeps spinning because a stop command was lost (phone-network drop, tab closed, app backgrounded). **Must always end up stopped.**
- Controls appearing on fixed cameras, or an error toast every time a fixed camera is opened.
- Controls that cover the picture permanently, or that interfere with the existing tap-to-open / back navigation.
- Anything that disturbs recording, detection, or live playback of other cameras. PTZ is a side channel and must fail independently.
- Leaking camera credentials to the browser.

## Behaviour

### Controls (UI)

- Shown only on the single-camera view, and only if the camera reports PTZ support (see Capability detection).
- D-pad: up, down, left, right (a center dot is decorative or unused). Zoom: + and −.
- Press and hold = move at a fixed, modest speed; release, pointer-cancel, pointer leaving the button, tab hidden, or navigating away = stop. Implemented with pointer events so touch and mouse behave the same.
- Controls fade out after a few seconds with no interaction and reappear on tapping the video. *(Builder's call on exact timing; suggested ~4 s.)*
- Zoom buttons are hidden if the camera supports pan/tilt but not zoom (and vice versa).
- Failure feedback: if a move fails, show a brief non-blocking message ("Camera didn't respond"). Do not retry automatically.

### Capability detection

- `nvrd` determines, per camera, whether it exposes the ONVIF PTZ service and which of pan/tilt and zoom it supports. The answer is cached in memory and re-checked on config reload or after a failure/expiry, not on every page load. *(Builder's call.)*
- `GET /api/cameras` (existing) gains a `ptz` field per camera, e.g. `{ "pan_tilt": true, "zoom": true }`, absent or false when unsupported. The UI reads this to decide whether to render controls.
- Detection failing or timing out is treated as "no PTZ", never as an error surfaced to the user.

### Finding the camera's ONVIF endpoint (important)

The camera config has **no ONVIF address today.** Cameras are defined by `source_main` / `source_sub` RTSP URLs with credentials interpolated from `secrets.yaml`; ONVIF appears only in LAN discovery results, which are not persisted.

Decisions:
- **Default:** derive the camera host and credentials from `source_main`, and try the known ONVIF service ports for the owner's two vendors: **2020 for Tapo**, and the standard HTTP port (80) for CP Plus/Dahua-lineage, with the path `/onvif/device_service`. *(My default; the builder may refine the port list.)*
- **Override:** add an optional per-camera config field (e.g. `onvif_url`) for cameras on a non-standard port or path. If set, it wins. It must pass through the existing config validation and `PUT /api/config` flow, and be editable in Settings only if that is cheap; editing it in YAML is acceptable for v1.
- **Credentials:** reuse the RTSP username/password from `source_main`. Note from the FRS: Tapo needs a "Camera Account" and CP Plus needs an "ONVIF user"; both are already prerequisites (FLAG-8). If a camera needs different ONVIF credentials, that is covered only by `onvif_url` embedding its own `${secret:…}` credentials. *(My call.)*

### Back end (`nvrd`, Go)

- New package (e.g. `internal/ptz`) speaking ONVIF SOAP: get capabilities / profile, `ContinuousMove`, `Stop`. Hand-rolled SOAP over `net/http` is preferred over a large dependency, consistent with the project's single static binary and the existing hand-rolled WS-Discovery in `internal/discover`. *(My call; the builder may choose a library if it stays small and builds for arm64 static.)*
- Handle ONVIF authentication (WS-Security UsernameToken digest) and the camera's clock skew: Tapo and Dahua cameras reject tokens with a wrong timestamp, so compute the offset from the device's reported time instead of assuming the phone's clock matches. This is the most likely source of "works on one brand, fails on the other".
- New routes, behind the existing token auth and registered alongside the existing camera routes in `internal/api/server.go`:
  - `POST /api/cameras/{id}/ptz/move` — body: direction/zoom intent (not raw velocities).
  - `POST /api/cameras/{id}/ptz/stop`.
  - Exact shapes are the builder's call. Camera ids that don't exist or aren't PTZ-capable return a clear 4xx.
- **Safety timeout:** every `move` carries or implies a short maximum duration (suggested a few seconds), enforced by `nvrd` (ONVIF `Timeout` on `ContinuousMove` and/or a server-side timer that sends `Stop`). The UI re-sends `move` while the button is held, so holding works but a lost release cannot cause an endless move.
- Serialise commands per camera so a late `move` cannot arrive after a `stop`.
- Commands are not logged with credentials; log camera id and action at debug level.

### Front end (zero-build PWA, `ui/`)

- Changes confined to `ui/js/views/live.js` (single-camera branch), `ui/js/api.js` if a helper is needed, and `ui/style.css`.
- The service worker (`ui/sw.js`) must not cache or replay PTZ requests.
- Touch behaviour must not trigger page scroll, text selection, long-press context menu or the tile's existing click-to-navigate handler while a control is pressed.

## Constraints

- Runs on a rooted Android phone as a static arm64 Go binary; no new runtime, no CGO, minimal dependencies.
- No change to MediaMTX, the detector, or recording paths.
- Camera network is the LAN; the phone reaches cameras directly.
- Existing token auth model stays: one API token, write-only secrets, redacted config reads.
- Tests: follow the existing pattern (`internal/api/server_test.go`, config tests). The ONVIF client should be testable against a fake SOAP server; real-camera verification is manual.

## Left to the builder

- Package layout, SOAP request building, library vs hand-rolled.
- Route and payload shapes; speed values; the exact safety-timeout and re-send interval.
- Capability cache lifetime and refresh strategy.
- Fade-out timing and visual styling, within option A.
- Whether to expose `onvif_url` in the Settings UI or YAML only.
- Test structure.

## Open facts to confirm during build (not blockers for planning)

- Which of the owner's cameras are actually motorized, and their model numbers (the FRS lists 2× Tapo, 2× CP Plus; not all models have motors). Expect the owner to test on whichever PTZ camera they have first.
- Whether the Tapo and CP Plus units accept the same RTSP credentials for ONVIF as set up per FLAG-8.

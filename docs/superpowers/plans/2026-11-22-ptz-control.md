# PTZ Control Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** From the single-camera live view, pan/tilt/zoom a motorized camera by holding on-screen buttons floating over the video.

**Architecture:** `nvrd` gains an `internal/ptz` package that speaks hand-rolled ONVIF SOAP (WS-Security digest, clock-skew compensated) to the camera, plus a `Manager` that caches capabilities, serialises commands per camera and auto-stops after a short timeout. Three small API routes sit behind the existing token auth; `GET /api/cameras` also reports cached capabilities. The zero-build PWA adds `ui/js/ptz.js`, mounted only on the single-camera view.

**Tech Stack:** Go 1.25 stdlib only (`net/http`, `encoding/xml`, `crypto/sha1`), existing `pocketnvr` module; vanilla ES modules; `node --test` for the one UI unit test.

**Spec:** `docs/superpowers/specs/2026-11-22-ptz-control-design.md`

## Global Constraints

- Static arm64 Go binary, no CGO, no new third-party dependencies (SOAP is hand-rolled over `net/http`).
- No change to MediaMTX, the detector, or recording paths; PTZ must fail independently of live playback.
- Existing single-token auth on every `/api/` route; the browser never talks to the camera and never receives camera credentials.
- Credentials are never logged; log camera id and action at debug level only.
- Camera ONVIF endpoint: the `onvif_url` config field wins; otherwise derive the host from `source_main` and try, in order, `http://<host>:2020/onvif/device_service` (Tapo) then `http://<host>:80/onvif/device_service` (CP Plus/Dahua).
- Credentials: reuse the RTSP user/password from `source_main`, unless `onvif_url` embeds its own userinfo.
- Controls hold-to-move, release-to-stop; the camera must always end up stopped (server-side safety stop: 3 s max per move; UI re-sends every 1000 ms while held).
- Capability detection failure/timeout means "no PTZ", never a user-visible error; probe never delays `GET /api/cameras`.
- Non-goals: presets, auto-tracking, grid-view controls, absolute positioning, non-ONVIF protocols, per-user permissions.

## Review Focus

Failure modes the spec implies; each has a test in the named task.

1. **Lost release** (pointer cancelled, tab hidden, navigation away, dropped stop request) must still stop the camera. — Task 4 (safety timer), Task 6 (release triggers).
2. **Camera off, unreachable or slow:** probe gives up in ≤3 s, shows no controls, shows no error, and `GET /api/cameras` stays fast. — Tasks 4, 5.
3. **Pan/tilt-only or zoom-only camera:** only the supported buttons appear; unsupported moves are rejected. — Tasks 3, 4, 6.
4. **Camera clock differs from the phone's:** authentication still succeeds. — Task 2.
5. **Passwords with `$`, `@`, `:` or `%` in `source_main`** decode correctly for ONVIF and never appear in API JSON or logs. — Tasks 3, 5.

---

### Task 1: `onvif_url` config field

**Files:**
- Modify: `internal/config/config.go:70-79` (the `Camera` struct)
- Modify: `internal/config/validate.go:~191-199` (per-camera URL validation loop)
- Modify: `config/config.yaml` (commented example on the `front_tapo` camera)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.Camera.OnvifURL string` (`yaml:"onvif_url,omitempty" json:"onvif_url,omitempty"`); empty means "derive from `source_main`".

- [ ] **Step 1: Write failing tests** in `config_test.go`, following the existing Load-from-temp-dir pattern there:
  - `TestOnvifURLOptional`: a camera without `onvif_url` loads; `OnvifURL == ""`.
  - `TestOnvifURLLoadsAndInterpolates`: `onvif_url: http://${secret:u}:${secret:p}@10.0.0.5:2020/onvif/device_service` loads with the secrets substituted into `cfg.Cameras[0].OnvifURL`.
  - `TestOnvifURLRejectsBadScheme`: `onvif_url: rtsp://10.0.0.5/x` and `onvif_url: not a url` both fail validation, with an error naming `onvif_url`.
- [ ] **Step 2: Run** `go test ./internal/config/ -run OnvifURL -v` — Expected: FAIL (unknown field / no validation).
- [ ] **Step 3: Add the field** to `Camera` and **validate** it: when non-empty it must parse with scheme `http` or `https` and a non-empty host; error text `cameras[<id>].onvif_url %q: want http(s)://host[:port]/path` using the existing `errf` and `redactURL`.
- [ ] **Step 4: Run** `go test ./internal/config/ -v` — Expected: PASS (whole package, to confirm redactor tests still pass with the new field).
- [ ] **Step 5: Commit** `git commit -m "config: optional per-camera onvif_url"`.

---

### Task 2: ONVIF SOAP transport with WS-Security and clock sync

**Files:**
- Create: `internal/ptz/soap.go`
- Create: `internal/ptz/ptztest/fake.go` (fake ONVIF camera, shared by Tasks 2–5; must NOT import `internal/ptz`, to avoid an import cycle with in-package tests)
- Test: `internal/ptz/soap_test.go`

**Interfaces:**
- Produces:
  - `type Fault struct{ Code, Reason string }` implementing `error`.
  - `func wsseHeader(user, pass string, nonce []byte, created time.Time) string` — returns the `<wsse:Security>` XML fragment (UsernameToken, `PasswordDigest`, base64 nonce, `Created` in `2006-01-02T15:04:05Z` UTC).
  - `type soapClient struct{...}`; `func newSoap(deviceURL, user, pass string, hc *http.Client) *soapClient` (`deviceURL` is the device-service endpoint, used for clock sync).
  - `func (s *soapClient) syncClock(ctx context.Context) error` — unauthenticated `GetSystemDateAndTime` against `deviceURL`; stores `offset = deviceUTC - time.Now()`.
  - `func (s *soapClient) call(ctx context.Context, url, bodyXML string, out any) error` — SOAP 1.2 POST (`Content-Type: application/soap+xml; charset=utf-8`) with the WSSE header built from `time.Now().Add(offset)`; unmarshals `<Body>` content into `out` (may be nil); returns `*Fault` on a SOAP Fault. Syncs the clock once, lazily, before the first authenticated call; a sync failure leaves `offset = 0` and is not fatal.
- Produces (fake): `package ptztest`; `type Options struct{ User, Pass string; ClockSkew time.Duration; ... }` (Task 3 adds `PanTilt, Zoom, NoPTZService bool`, `AdvertiseHost string`); `func New(t testing.TB, o Options) *Camera`; `(*Camera).URL string` = device-service endpoint (`<server>/onvif/device_service`); `(*Camera).Requests() int`. In this task the fake handles `GetSystemDateAndTime` (unauthenticated; reports `now+ClockSkew` as UTC) and `GetDeviceInformation` (authenticated: verifies the digest against `Pass` and requires `Created` within ±5 s of `now+ClockSkew`, else a SOAP Fault `ter:NotAuthorized`).

- [ ] **Step 1: Write failing tests** in `soap_test.go`:
  - `TestWSSEDigestVector`: `wsseHeader("u", "userpassword", <decoded nonce "LKqI6G/AikKCQrN0zqZFlg==">, 2010-09-16T07:50:45Z)` contains `tuOSpGlFlIXsozq4HFNeeGeFLEI=`, the base64 nonce, and `2010-09-16T07:50:45Z` (OASIS WSS UsernameToken example; digest = base64(sha1(nonce+created+password))).
  - `TestSyncClockOffset`: fake with `ClockSkew: time.Hour`; after `syncClock`, offset is within 2 s of +1 h.
  - `TestCallSucceedsWithSkewedDeviceClock`: fake with `User:"u", Pass:"p", ClockSkew: 2*time.Hour`; `call(... GetDeviceInformation ...)` returns no error. (Proves Review Focus #4.)
  - `TestCallWrongPasswordIsFault`: wrong password → `errors.As(err, &*Fault)` true.
  - `TestCallDoesNotLeakPasswordInError`: error strings from a failed call never contain the password.
- [ ] **Step 2: Run** `go test ./internal/ptz/ -v` — Expected: FAIL (package does not compile yet).
- [ ] **Step 3: Implement** `soap.go` and `ptztest/fake.go` per the interfaces above. Parse responses with `encoding/xml` using local-name matching so vendor namespace prefixes don't matter.
- [ ] **Step 4: Run** `go test ./internal/ptz/... -v` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "ptz: ONVIF SOAP transport with WS-Security and clock sync"`.

---

### Task 3: ONVIF PTZ operations and endpoint resolution

**Files:**
- Create: `internal/ptz/target.go`, `internal/ptz/client.go`
- Modify: `internal/ptz/ptztest/fake.go` (add PTZ behaviour)
- Test: `internal/ptz/target_test.go`, `internal/ptz/client_test.go`

**Interfaces:**
- Consumes: `soapClient`, `newSoap(deviceURL, user, pass, hc)`, `call`, `Fault` (Task 2; `Dial` makes one `soapClient` per candidate endpoint); `config.Camera.SourceMain`, `.OnvifURL` (Task 1).
- Produces:
  - `type Target struct{ Endpoints []string; User, Pass string }`; `func TargetFor(cam config.Camera) (Target, error)`.
  - `type Caps struct{ PanTilt bool \`json:"pan_tilt"\`; Zoom bool \`json:"zoom"\` }`.
  - `type Direction string`; constants `Up="up"`, `Down="down"`, `Left="left"`, `Right="right"`, `ZoomIn="zoom_in"`, `ZoomOut="zoom_out"`; `func ParseDirection(s string) (Direction, bool)`.
  - `type Client struct{...}`; `func Dial(ctx context.Context, t Target, hc *http.Client) (*Client, error)` — tries `t.Endpoints` in order; for the first that answers `GetCapabilities`, records the media and PTZ service URLs (**rewriting each advertised URL's host:port to the endpoint's host:port**, since cameras often advertise internal addresses), then `GetProfiles` and keeps the first profile token that has a PTZ configuration. A camera with no PTZ service or no such profile dials successfully with empty `Caps`.
  - `func (c *Client) Capabilities() Caps` — from `GetNodes` on the PTZ service: `PanTilt` iff the node lists `ContinuousPanTiltVelocitySpace`, `Zoom` iff `ContinuousZoomVelocitySpace`.
  - `func (c *Client) Move(ctx context.Context, d Direction, timeout time.Duration) error` — `ContinuousMove` with velocity 0.5 on the relevant axis (left: pan −0.5; right: +0.5; down: tilt −0.5; up: +0.5; zoom_out: −0.5; zoom_in: +0.5; all other axes 0) and `Timeout` as an `xsd:duration` (`PT3S` for 3 s).
  - `func (c *Client) Stop(ctx context.Context) error` — `Stop` with `PanTilt=true`, `Zoom=true`.
- Produces (fake): Options gains `PanTilt, Zoom, NoPTZService bool` and `AdvertiseHost string` (host placed in advertised XAddrs; default a non-routable `192.0.2.1`); fake handles `GetCapabilities`, `GetProfiles`, `GetNodes`, `ContinuousMove`, `Stop`; adds `(*Camera).Moves() []Move` (`Move{Pan, Tilt, Zoom float64; Timeout string}`), `(*Camera).Stops() int`, `(*Camera).MaxConcurrent() int` (peak simultaneous in-flight PTZ requests).

- [ ] **Step 1: Write failing tests.**
  - `target_test.go`:
    - `TestTargetForDerivesFromSourceMain`: `rtsp://u:p@10.0.0.9:554/stream1`, no `onvif_url` → `Endpoints == ["http://10.0.0.9:2020/onvif/device_service","http://10.0.0.9:80/onvif/device_service"]`, `User=="u"`, `Pass=="p"`.
    - `TestTargetForDecodesSpecialCharPassword` (Review Focus #5): `rtsp://camadmin:Sup3r%24ecret%40x%3A1@10.0.0.9/s` → `Pass == "Sup3r$ecret@x:1"`; and raw `rtsp://camadmin:Sup3r$ecret@10.0.0.9/s` → `Pass == "Sup3r$ecret"`.
    - `TestTargetForOnvifURLWins`: `onvif_url: http://10.0.0.9:8899/onvif/device_service` → exactly that one endpoint, creds still from `source_main`.
    - `TestTargetForOnvifURLEmbeddedCreds`: userinfo in `onvif_url` overrides `source_main` creds and is stripped from `Endpoints[0]`.
  - `client_test.go`, all against `ptztest.New`:
    - `TestDialPanTiltZoom`: `Capabilities() == Caps{true,true}`.
    - `TestCapsPanTiltOnly` and `TestCapsZoomOnly` (Review Focus #3).
    - `TestNoPTZServiceGivesEmptyCaps`: `NoPTZService:true` → `Dial` returns nil error and `Caps{}`.
    - `TestDialFallsBackToSecondEndpoint`: first endpoint is a closed port, second is the fake → success.
    - `TestAdvertisedHostIsRewritten`: fake advertises `192.0.2.1`; `Dial` + `Move` still reach the fake.
    - `TestMoveVelocities`: table over the six directions asserting `Moves()[0]` pan/tilt/zoom values above and `Timeout == "PT3S"`.
    - `TestStopSendsStop`: `Stops() == 1` after `Stop`.
- [ ] **Step 2: Run** `go test ./internal/ptz/... -v` — Expected: FAIL.
- [ ] **Step 3: Implement** `target.go` (use `net/url`; `Userinfo.Password()` already percent-decodes), `client.go`, and extend the fake.
- [ ] **Step 4: Run** `go test ./internal/ptz/... -v` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "ptz: ONVIF client (capabilities, continuous move, stop) and endpoint resolution"`.

---

### Task 4: PTZ manager (cache, serialisation, safety stop)

**Files:**
- Create: `internal/ptz/manager.go`
- Test: `internal/ptz/manager_test.go`

**Interfaces:**
- Consumes: `TargetFor`, `Dial`, `Client`, `Caps`, `Direction` (Task 3); `ptztest` fake.
- Produces:
  - `var ErrNotPTZ = errors.New("camera has no PTZ")`, `var ErrUnsupported = errors.New("camera does not support that movement")`.
  - `type Manager struct{...}`; `func NewManager(logger *slog.Logger) *Manager`. Defaults: probe timeout 3 s, per-command timeout 3 s, `maxMove` 3 s, capabilities TTL 10 min on success and 1 min on failure. These are unexported fields so tests can shrink them.
  - `func (m *Manager) CapsCached(cam config.Camera) (Caps, bool)` — never blocks on the network; `ok=false` if unknown or expired, in which case it starts one background probe (no duplicate probes in flight).
  - `func (m *Manager) Caps(ctx context.Context, cam config.Camera) (Caps, error)` — blocks up to the probe timeout; returns the cached value if fresh; on failure returns `Caps{}` and the error, and caches the failure for the failure TTL.
  - `func (m *Manager) Move(ctx context.Context, cam config.Camera, d Direction) error` — `ErrNotPTZ` if the camera has no PTZ, `ErrUnsupported` if the direction's axis (pan/tilt vs zoom) isn't supported; otherwise sends `Move(d, maxMove)` and (re)arms a per-camera safety timer that sends `Stop` after `maxMove`.
  - `func (m *Manager) Stop(ctx context.Context, cam config.Camera) error` — cancels the safety timer, sends `Stop`.
  - `func (m *Manager) Reset()` — drops all cached clients/caps and cancels safety timers (called on config reload).
  - Commands for one camera are serialised by a per-camera mutex.

- [ ] **Step 1: Write failing tests.** Use `ptztest.New` and a `config.Camera{ID:"c1", SourceMain:"rtsp://u:p@127.0.0.1/x", OnvifURL: fake.URL}`; construct the manager with shortened timeouts (e.g. `maxMove` 150 ms).
  - `TestSafetyStopAfterMove` (Review Focus #1): `Move`, wait > `maxMove`, assert `fake.Stops() == 1`.
  - `TestRepeatedMoveKeepsMovingThenStops`: `Move` three times, 80 ms apart (< `maxMove`); `Stops()==0` until after the last `Move`+`maxMove`, then `1`.
  - `TestStopCancelsSafetyTimer`: `Move`, `Stop`, wait > `maxMove`; `Stops() == 1` (not 2).
  - `TestCommandsAreSerialised`: 20 concurrent `Move`/`Stop` calls; `fake.MaxConcurrent() == 1`.
  - `TestMoveOnNonPTZ`: `NoPTZService` fake → `ErrNotPTZ`; `Moves()` empty.
  - `TestMoveUnsupportedAxis`: pan/tilt-only fake, `ZoomIn` → `ErrUnsupported`.
  - `TestUnreachableCameraProbeIsBounded` (Review Focus #2): camera pointing at a closed/blackhole port; `Caps(ctx)` returns within ~probe timeout (shrunk to 300 ms in the test) with `Caps{}` and non-nil error; `CapsCached` returns in <50 ms with `ok=false`.
  - `TestFailureIsCachedBriefly`: after a failed `Caps`, a second `Caps` within the failure TTL makes no new network request (check via a request-counting listener or fake `Requests()`).
  - `TestResetDropsCache`: after `Reset`, the next `Caps` re-probes (`Requests()` increases).
- [ ] **Step 2: Run** `go test ./internal/ptz/ -run Manager -v` — Expected: FAIL.
- [ ] **Step 3: Implement** `manager.go`. One entry per camera id keyed also by the resolved `Target` (so a changed `source_main`/`onvif_url` re-dials). Safety timer uses `time.AfterFunc`; the timer's `Stop` call takes the same per-camera mutex and uses a fresh `context` with the per-command timeout.
- [ ] **Step 4: Run** `go test ./internal/ptz/... -race -v` — Expected: PASS, no races.
- [ ] **Step 5: Commit** `git commit -m "ptz: manager with capability cache, serialised commands, safety stop"`.

---

### Task 5: API routes and `/api/cameras` capabilities

**Files:**
- Modify: `internal/api/server.go` (`Server` struct, `New`, `SwapConfig`, `Handler`, `handleCameras`)
- Create: `internal/api/ptz.go` (the three handlers + `cameraByID`)
- Modify: `README.md:240` (API table: add the PTZ routes after `GET /api/cameras`)
- Test: `internal/api/ptz_test.go`

**Interfaces:**
- Consumes: `ptz.Manager`, `ptz.Caps`, `ptz.ParseDirection`, `ptz.ErrNotPTZ`, `ptz.ErrUnsupported` (Tasks 3–4).
- Produces (HTTP, all behind the existing `auth`):
  - `GET /api/cameras/{id}/ptz` → `200 {"pan_tilt":bool,"zoom":bool}`; blocks up to the probe timeout; probe failure is `200` with both false (never 5xx); unknown camera `404`.
  - `POST /api/cameras/{id}/ptz/move` body `{"dir":"left|right|up|down|zoom_in|zoom_out"}` → `204`; `400` bad JSON or unknown dir; `404` unknown camera; `409` `ErrNotPTZ`/`ErrUnsupported` (body: the error text); `502` camera did not respond.
  - `POST /api/cameras/{id}/ptz/stop` → `204`; `404`; `409` (not PTZ); `502`.
  - `GET /api/cameras` items gain `"ptz": {"pan_tilt":..,"zoom":..}`, present **only** when `CapsCached` is known and at least one flag is true (`omitempty` on a `*ptz.Caps`). This call never waits on a camera.
- `Server` gains a `ptz *ptz.Manager` created in `New`; `SwapConfig` calls `s.ptz.Reset()`.

- [ ] **Step 1: Write failing tests** in `ptz_test.go`. Build a server like `setup` in `server_test.go` but with a camera whose `onvif_url` is a `ptztest` camera (copy the setup body into a local helper `setupPTZ(t, ptztest.Options) (*Server, *httptest.Server, *ptztest.Camera)`; use token `sekrit` and the existing `get` helper, add a small `post` helper).
  - `TestPTZRoutesRequireAuth`: all three return 401 without a token.
  - `TestPTZCapsEndpoint`: pan/tilt+zoom fake → `{"pan_tilt":true,"zoom":true}`.
  - `TestPTZMoveAndStop`: POST `{"dir":"left"}` → 204 and `fake.Moves()[0].Pan == -0.5`; POST stop → 204, `Stops() >= 1`.
  - `TestPTZErrors`: unknown camera 404; `{"dir":"sideways"}` 400; non-PTZ camera (`NoPTZService`) 409; dead camera on `move` → 502.
  - `TestCamerasListPTZFieldIsCacheOnly` (Review Focus #2): with a dead camera, `GET /api/cameras` returns in <500 ms and the camera has no `ptz` key; after `GET /api/cameras/{id}/ptz` against a live fake, a later `GET /api/cameras` includes `"ptz"` with `pan_tilt:true`.
  - `TestPTZNeverLeaksCredentials` (Review Focus #5): none of the three endpoints' response bodies, nor `GET /api/cameras`, contain the camera password.
- [ ] **Step 2: Run** `go test ./internal/api/ -run PTZ -v` — Expected: FAIL (404s).
- [ ] **Step 3: Implement** the routes and handlers. Look up the camera in `s.cfg.Load().Cameras` by id; map `ErrNotPTZ`/`ErrUnsupported` to 409; other manager errors to 502 with a generic body (no upstream text that could include credentials); log at debug with camera id and action only.
- [ ] **Step 4: Run** `go test ./... -race` — Expected: PASS (whole module, to confirm existing API tests are unaffected).
- [ ] **Step 5: Commit** `git commit -m "api: PTZ routes and capability field on /api/cameras"`.

---

### Task 6: On-video PTZ controls in the single-camera view

**Files:**
- Create: `ui/js/ptz.js`
- Modify: `ui/js/views/live.js:41-50` (single-camera branch only)
- Modify: `ui/style.css` (append a `/* ---- PTZ overlay ---- */` section)
- Modify: `ui/sw.js` (add `"js/ptz.js"` to `PRECACHE`; bump `CACHE` to `"pocketnvr-v6"`)
- Test: `ui/tests/ptz.test.mjs`

**Interfaces:**
- Consumes: `GET /api/cameras/{id}/ptz`, `POST /api/cameras/{id}/ptz/move` `{dir}`, `POST /api/cameras/{id}/ptz/stop` (Task 5); `api()`, `el()` from `ui/js/api.js`.
- Produces (`ui/js/ptz.js`):
  - `export function createHold({ send, stop, onError, intervalMs = 1000, setIntervalFn = setInterval, clearIntervalFn = clearInterval })` returning `{ press(dir), release() }`. `send(dir, signal)` and `stop()` return promises. `press` sends immediately and then every `intervalMs` until `release`; `release` clears the repeat, aborts any in-flight `send`, and calls `stop()` once (no-op if nothing is held). Pressing while another direction is held releases first. A rejected `send` calls `onError(err)` once and releases.
  - `export async function mountPtz(tile, cam)` → resolves to a cleanup function. Fetches capabilities; if neither flag is set (or the fetch fails) it adds nothing and returns a no-op cleanup. Otherwise appends a `.ptz` overlay to `tile`: D-pad (▲ ▼ ◀ ▶, `data-dir` up/down/left/right) bottom-right if `pan_tilt`; zoom `+`/`−` (`zoom_in`/`zoom_out`) bottom-left if `zoom`.
- Behaviour pinned from the spec: hold = move, release/`pointercancel`/`pointerleave`/page `visibilitychange` to hidden/cleanup = stop; overlay fades (class `idle`, `pointer-events:none`) after 4 s with no interaction and reappears on tapping the video; never fades while a button is held; button handlers call `preventDefault` and `stopPropagation` (so the tile's navigate-on-click and page scroll/selection/long-press menu don't fire; CSS `touch-action:none; user-select:none`); failure shows a brief in-overlay message "Camera didn't respond" (a `.ptz-msg` element, not the update toast) that clears after 2.5 s.

- [ ] **Step 1: Write failing tests** in `ui/tests/ptz.test.mjs` with `node:test`, injecting fake `setIntervalFn`/`clearIntervalFn` so timers are manual:
  - `press sends immediately then every interval until release`: after `press("left")` one `send("left")`; after two manual ticks three sends; after `release()` no more sends and `stop` called exactly once.
  - `release without press does nothing`: `stop` not called.
  - `pressing another direction releases first`: `press("left")`, `press("up")` → `stop` called once, then `send("up")`.
  - `release aborts the in-flight send`: the `signal` passed to `send` is aborted after `release()`.
  - `failed send reports once and releases`: `send` rejects → `onError` called once, `stop` called once, no further ticks send.
- [ ] **Step 2: Run** `node --test ui/tests/` — Expected: FAIL (module missing). (On this machine the default `node` on PATH may be a broken x86 build; use the working Node 22 install under `~/.nvm/versions/node/`.)
- [ ] **Step 3: Implement `ui/js/ptz.js`** (`createHold` pure and DOM-free so it imports cleanly under Node; `mountPtz` wires pointer events, fade timer and message), the CSS (semi-transparent 44px minimum touch targets, matching the mockup: zoom column bottom-left, D-pad bottom-right, `.ptz.idle` fade), and the one-line change in `live.js`: in the single-camera branch, after `playTile(...)`, `let ptzCleanup = () => {}; mountPtz(tile, one).then((c) => { ptzCleanup = c; });` and call `ptzCleanup()` in the returned unmount function (before `stopAll()`).
- [ ] **Step 4: Run** `node --test ui/tests/` — Expected: PASS.
- [ ] **Step 5: Verify in the browser against a fake camera:** run `nvrd` with a throwaway config whose camera has `onvif_url` pointing at a locally running `ptztest` fake (a short `go run` scratch program in the scratchpad dir — not committed), open `#/live/<id>`, and confirm: controls appear; holding ◀ shows `Moves()` growing once per second; releasing shows a `Stop`; the grid view shows no controls; a fixed camera (`NoPTZService`) shows none. Expected: all as described.
- [ ] **Step 6: Commit** `git commit -m "ui: PTZ overlay controls on the single-camera view"`.

---

### Task 7: Real-camera check and docs

**Files:**
- Modify: `UI-PLAN.md` (the "Stretch" line mentioning ONVIF PTZ and the "Explicitly out of scope" PTZ mention → note it is implemented)
- Modify: `README.md` (one Features bullet clause: PTZ from the live view; one config note for `onvif_url`)

- [ ] **Step 1: On a real motorized camera** (the owner confirms which of the Tapo / CP Plus units has motors), build and run `nvrd` and open its single-camera view. Check and record in the commit message: controls appear; each of the six buttons moves the right way; releasing stops promptly; closing the tab mid-move stops within about 3 s; a non-motorized camera shows no controls.
- [ ] **Step 2: If the camera fails to authenticate or advertises an unexpected PTZ/Media path**, fix it in Task 2/3 code with a test reproducing the camera's behaviour in `ptztest`, rather than special-casing in the manager.
- [ ] **Step 3: Update docs** as listed above.
- [ ] **Step 4: Commit** `git commit -m "docs: PTZ control, onvif_url"`.

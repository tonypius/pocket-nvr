// ptz.js — on-video PTZ controls for the single-camera view.
// Hold a button = camera moves; release = stop. The server auto-stops the
// camera ~3 s after the last move, so a held button re-sends "move" every
// second, and every release path MUST send "stop".
//
// createHold is pure (no DOM) so it can be unit-tested under Node.
import { api, el } from "./api.js";

const REPEAT_MS = 1000;
const FADE_MS = 4000;
const MSG_MS = 2500;

export function createHold({
  send, stop, onError, intervalMs = REPEAT_MS,
  setIntervalFn = setInterval, clearIntervalFn = clearInterval,
}) {
  let held = null; // { dir, timer, ctrl } while a button is held

  function fire(h) {
    let p;
    try { p = send(h.dir, h.ctrl.signal); } catch (e) { p = Promise.reject(e); }
    Promise.resolve(p).catch((err) => {
      // A release (or a newer press) aborted this send: not a failure.
      if (held !== h || h.ctrl.signal.aborted) return;
      release();
      onError?.(err);
    });
  }

  function release() {
    const h = held;
    if (!h) return;
    held = null;
    clearIntervalFn(h.timer);
    h.ctrl.abort();
    try { Promise.resolve(stop()).catch(() => {}); } catch (e) { /* server auto-stops */ }
  }

  function press(dir) {
    if (held) release();
    const h = { dir, timer: null, ctrl: new AbortController() };
    held = h;
    fire(h);
    // fire() may already have released (synchronous send failure).
    if (held === h) h.timer = setIntervalFn(() => fire(h), intervalMs);
  }

  return { press, release };
}

const DIRS = {
  up: "▲", down: "▼", left: "◀", right: "▶",
  zoom_in: "+", zoom_out: "−",
};
const LABELS = {
  up: "Tilt up", down: "Tilt down", left: "Pan left", right: "Pan right",
  zoom_in: "Zoom in", zoom_out: "Zoom out",
};

// Resolves to a cleanup function. Adds nothing (no-op cleanup) when the camera
// has no PTZ or the capability lookup fails.
export async function mountPtz(tile, cam) {
  let caps;
  try {
    caps = await api(`/api/cameras/${encodeURIComponent(cam.id)}/ptz`);
  } catch (e) {
    return () => {};
  }
  if (!caps || (!caps.pan_tilt && !caps.zoom)) return () => {};

  const base = `/api/cameras/${encodeURIComponent(cam.id)}/ptz`;
  const root = el("div", "ptz");
  let fadeTimer = null;
  let msgTimer = null;
  let pressed = false;
  let msg = null;

  const wake = () => {
    root.classList.remove("idle");
    clearTimeout(fadeTimer);
    fadeTimer = setTimeout(() => {
      if (pressed) wake(); // never fade while a button is held
      else root.classList.add("idle");
    }, FADE_MS);
  };

  const showError = () => {
    wake();
    if (!msg) {
      msg = el("div", "ptz-msg");
      msg.setAttribute("role", "status");
      root.appendChild(msg);
    }
    msg.textContent = "Camera didn't respond";
    clearTimeout(msgTimer);
    msgTimer = setTimeout(() => { msg?.remove(); msg = null; }, MSG_MS);
  };

  const hold = createHold({
    send: (dir, signal) => api(base + "/move", {
      method: "POST", body: JSON.stringify({ dir }), signal,
    }),
    stop: () => api(base + "/stop", { method: "POST" }),
    onError: () => { pressed = false; clearDown(); showError(); },
  });

  const buttons = [];
  const clearDown = () => buttons.forEach((b) => b.classList.remove("down"));

  const release = () => {
    if (!pressed) return;
    pressed = false;
    clearDown();
    hold.release();
    wake();
  };

  function button(dir) {
    const b = el("button", "ptz-btn ptz-" + dir, DIRS[dir]);
    b.type = "button";
    b.dataset.dir = dir;
    b.setAttribute("aria-label", LABELS[dir]);
    b.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      e.stopPropagation();
      // Touch pointers are implicitly captured; let go so that sliding off
      // the button fires pointerleave and stops the camera.
      try { b.releasePointerCapture(e.pointerId); } catch (err) { /* none held */ }
      if (pressed) release();
      pressed = true;
      b.classList.add("down");
      wake();
      hold.press(dir);
    });
    for (const t of ["pointerup", "pointercancel", "pointerleave"]) {
      b.addEventListener(t, (e) => { e.stopPropagation(); release(); });
    }
    b.addEventListener("contextmenu", (e) => { e.preventDefault(); release(); });
    // keep the tile's navigate-on-click from firing for button taps
    b.addEventListener("click", (e) => { e.preventDefault(); e.stopPropagation(); });
    buttons.push(b);
    return b;
  }

  if (caps.zoom) {
    const z = el("div", "ptz-zoom");
    z.append(button("zoom_in"), button("zoom_out"));
    root.appendChild(z);
  }
  if (caps.pan_tilt) {
    const d = el("div", "ptz-dpad");
    d.append(button("up"), button("left"), button("right"), button("down"));
    root.appendChild(d);
  }

  const onTile = () => wake(); // tapping the video reveals faded controls
  const onVis = () => { if (document.hidden) release(); };
  tile.addEventListener("click", onTile);
  document.addEventListener("visibilitychange", onVis);
  tile.appendChild(root);
  wake();

  return () => {
    release();
    hold.release();
    clearTimeout(fadeTimer);
    clearTimeout(msgTimer);
    tile.removeEventListener("click", onTile);
    document.removeEventListener("visibilitychange", onVis);
    root.remove();
  };
}

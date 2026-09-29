// sse.js — B3: live event push. EventSource with auto-reconnect; falls
// back gracefully if the stream is unavailable (polling continues in views).
let source = null;
const listeners = [];

export function initEventStream() {
  if (!("EventSource" in window)) return;
  connect();
}

function connect() {
  const tok = getToken();
  if (!tok) return;
  source = new EventSource("/api/events/stream?token=" + encodeURIComponent(tok));
  source.onmessage = (ev) => {
    try {
      const e = JSON.parse(ev.data);
      listeners.forEach((fn) => fn(e));
    } catch (err) { /* skip malformed */ }
  };
  source.onerror = () => {
    // EventSource retries automatically; surface nothing in the UI
  };
}

export function onLiveEvent(fn) {
  listeners.push(fn);
  return () => {
    const i = listeners.indexOf(fn);
    if (i >= 0) listeners.splice(i, 1);
  };
}

import { getToken } from "./api.js";

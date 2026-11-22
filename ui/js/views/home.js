// views/home.js — U2: recent events strip + live cameras strip.
import { api, esc, el, getToken } from "../api.js";

let players = [];

export async function mount(page) {
  page.innerHTML = "";
  const strip = el("div", "strip");
  const camStrip = el("div", "strip");
  const alertBar = el("div", "alertbar");
  page.append(alertBar, el("h3", "sec", "Recent events"), strip,
              el("h3", "sec", "Cameras"), camStrip);

  renderAlerts(alertBar);
  const [events, cameras] = await Promise.all([
    api("/api/events?limit=20"),
    api("/api/cameras"),
  ]);

  strip.innerHTML = events.length ? "" : "<p style='color:var(--dim)'>No events yet.</p>";
  for (const e of events) {
    const when = new Date(e.start_ts).toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
    const d = el("div", "snap",
      `<img src="/api/events/${e.id}/snapshot?token=${encodeURIComponent(getToken())}" loading="lazy" onerror="this.style.visibility='hidden'">
       <div class="score">${esc(e.label || "event")} ${(e.score * 100).toFixed(0)}%</div>
       <div class="cap">${esc(e.camera_id)} · ${when}</div>`);
    d.addEventListener("click", () => { location.hash = "#/events/" + e.id; });
    strip.appendChild(d);
  }

  camStrip.innerHTML = "";
  for (const c of cameras.filter((c) => c.enabled)) {
    const d = el("div", "mini",
      `<video autoplay muted playsinline></video>
       <div class="cap"><span>${esc(c.name)}</span><span class="mini-off" style="color:var(--warn)"></span></div>`);
    d.addEventListener("click", () => { location.hash = "#/live/" + c.id; });
    camStrip.appendChild(d);
    const v = d.querySelector("video");
    import("./../player.js").then((P) => {
      P.playTile(v, c, d); // reuse tile state wiring on the mini player
      const markAlive = () => { const o = d.querySelector(".mini-off"); if (o) o.textContent = ""; };
      v.addEventListener("playing", markAlive);
    });
  }
}

export function unmount() {
  import("./../player.js").then((P) => P.stopAll());
  const s = document.querySelector(".strip"); if (s) s.innerHTML = "";
  const c = document.querySelectorAll(".strip video"); c.forEach((v) => { v.srcObject = null; v.src = null; });
}

// ---- alert mute: silence every notification channel for a while ----
const MUTES = [["1 hour", 60], ["8 hours", 480], ["Until I turn on", -1]];

function muteLabel(m) {
  if (!m.muted) return "Alerts are on";
  if (m.until === -1) return "Alerts muted until you turn them back on";
  const t = new Date(m.until);
  const sameDay = t.toDateString() === new Date().toDateString();
  const at = t.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return `Alerts muted until ${sameDay ? at : t.toLocaleDateString([], { weekday: "short" }) + " " + at}`;
}

async function renderAlerts(bar) {
  let m;
  try { m = await api("/api/notifications/mute"); }
  catch (e) { bar.remove(); return; } // older server: no mute support
  bar.innerHTML = "";
  const msg = el("span", m.muted ? "alert-state off" : "alert-state on", esc(muteLabel(m)));
  bar.appendChild(msg);
  const set = async (minutes) => {
    try { m = await api("/api/notifications/mute", { method: "POST", body: JSON.stringify({ minutes }) }); }
    catch (e) { msg.textContent = "Couldn't change alerts: " + (e.message || e); return; }
    renderAlerts(bar);
  };
  if (m.muted) {
    const b = el("button", "primary", "Turn alerts on");
    b.addEventListener("click", () => set(0));
    bar.appendChild(b);
  } else {
    for (const [label, minutes] of MUTES) {
      const b = el("button", "ghost", "Mute " + esc(label));
      b.addEventListener("click", () => set(minutes));
      bar.appendChild(b);
    }
  }
}

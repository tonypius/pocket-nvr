// views/home.js — U2: recent events strip + live cameras strip.
import { api, esc, el, getToken } from "../api.js";

let players = [];

export async function mount(page) {
  page.innerHTML = "";
  const strip = el("div", "strip");
  const camStrip = el("div", "strip");
  page.append(el("h3", "sec", "Recent events"), strip,
              el("h3", "sec", "Cameras"), camStrip);

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

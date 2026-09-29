// views/timeline.js — U8-lite (Phase B scope): single-camera live player
// with a vertical scrollable event rail — the "timeline with events"
// view. Clicking an event opens its clip; scrubbing continuous recordings
// lands with the MediaMTX playback spike (Phase C).
import { api, esc, el, getToken } from "../api.js";
import { setWantWake } from "../pwa.js";
import { playTile, stopAll } from "../player.js";
import { labelIcon } from "../labels.js";

export async function mount(page, camId) {
  setWantWake(true);
  const cameras = await api("/api/cameras");
  let cam = cameras.find((c) => c.id === camId) || cameras.find((c) => c.enabled);
  if (!cam) {
    page.innerHTML = "<p style='color:var(--dim)'>No enabled cameras.</p>";
    return () => {};
  }

  page.innerHTML = `
    <div class="page-head">
      <h2>Timeline — ${esc(cam.name)}</h2>
      <select id="tl-camera">
        ${cameras.filter((c) => c.enabled).map((c) => `<option value="${esc(c.id)}" ${c.id === cam.id ? "selected" : ""}>${esc(c.name)}</option>`).join("")}
      </select>
    </div>
    <div class="tl-layout">
      <div class="tl-player tile">
        <video autoplay muted playsinline></video>
        <div class="tag"><span class="dot"></span>${esc(cam.name)} · LIVE</div>
        <div class="off">connecting…</div>
      </div>
      <div class="tl-rail">
        <div class="tl-rail-head">Events <small>(newest first)</small></div>
        <div id="tl-events" class="tl-events"></div>
      </div>
    </div>
    <div class="filmstrip">
      <div class="filmstrip-head">Other cameras</div>
      <div class="filmstrip-row" id="tl-others"></div>
    </div>`;

  const rail = page.querySelector("#tl-events");
  const loadEvents = async () => {
    const evs = await api(`/api/events?camera=${cam.id}&limit=100`);
    rail.innerHTML = evs.length ? "" : "<p style='color:var(--dim)'>No events for this camera yet.</p>";
    for (const e of evs) {
      const d = el("div", "tl-ev",
        `<span class="tl-icon">${labelIcon(e.label)}</span>
         <div class="tl-meta">
           <div class="tl-when">${new Date(e.start_ts).toLocaleString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })}</div>
           <div class="tl-score">${esc(e.label)} · ${(e.score * 100).toFixed(0)}%</div>
         </div>
         <img src="/api/events/${e.id}/snapshot?token=${encodeURIComponent(getToken())}" loading="lazy"
              onerror="this.style.visibility='hidden'">`);
      d.addEventListener("click", () => { location.hash = "#/events/" + e.id; });
      rail.appendChild(d);
    }
  };
  await loadEvents();

  // filmstrip: mini live tiles for the other cameras
  const others = page.querySelector("#tl-others");
  for (const c of cameras.filter((x) => x.enabled && x.id !== cam.id)) {
    const tile = el("div", "mini",
      `<video autoplay muted playsinline></video>
       <div class="cap">${esc(c.name)}</div>`);
    tile.addEventListener("click", () => { location.hash = "#/timeline/" + c.id; });
    others.appendChild(tile);
    import("../player.js").then((P) => P.playTile(tile.querySelector("video"), c, tile));
  }

  $("#tl-camera", page).addEventListener("change", (e) => {
    location.hash = "#/timeline/" + e.target.value;
    location.reload();
  });

  return () => { stopAll(); setWantWake(false); };
}

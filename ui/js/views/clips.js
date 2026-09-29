// views/clips.js — clip feed (replaces Timeline in the nav): short event
// clips played inline as videos instead of snapshot thumbnails. Previews are
// muted loops that only load and play while on screen (IntersectionObserver),
// so a long grid stays cheap on mobile data. Tap toggles playback; the corner
// button opens the event detail.
import { api, esc, el, getToken, $ } from "../api.js";
import { stopAll } from "../player.js";
import { labelIcon } from "../labels.js";

let camList = [];
let io = null;

function fmtWhen(ms) {
  const d = new Date(ms);
  return d.toLocaleDateString([], { month: "short", day: "numeric" }) + " · " +
    d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

export async function mount(page) {
  stopAll(); // no live players on this page
  camList = await api("/api/cameras");
  page.innerHTML = `
    <div class="page-head"><h2>Clips</h2></div>
    <div class="filters">
      <select id="c-camera"></select>
      <select id="c-range">
        <option value="86400000">24 h</option>
        <option value="604800000" selected>7 days</option>
        <option value="2592000000">30 days</option>
      </select>
      <button id="c-refresh" class="ghost">refresh</button>
    </div>
    <div id="clip-grid" class="clip-grid"></div>`;

  const sel = $("#c-camera", page);
  sel.innerHTML = `<option value="">all cameras</option>` +
    camList.map((c) => `<option value="${esc(c.id)}">${esc(c.name)}</option>`).join("");
  $("#c-refresh", page).addEventListener("click", () => renderClips(page));
  sel.addEventListener("change", () => renderClips(page));
  $("#c-range", page).addEventListener("change", () => renderClips(page));
  await renderClips(page);

  return () => {
    if (io) { io.disconnect(); io = null; }
    page.querySelectorAll("video").forEach((v) => { v.pause(); v.removeAttribute("src"); v.load(); });
  };
}

async function renderClips(page) {
  const cam = $("#c-camera", page)?.value || "";
  const range = Number($("#c-range", page)?.value || 604800000);
  const from = Date.now() - range;
  const evs = (await api(`/api/events?from=${from}&limit=200${cam ? "&camera=" + cam : ""}`))
    .filter((e) => e.clip_path);

  if (io) io.disconnect();
  const grid = $("#clip-grid", page);
  grid.innerHTML = evs.length ? "" : "<p style='color:var(--dim)'>No clips in this window.</p>";

  // autoplay previews only while near the viewport; pause off-screen ones
  io = new IntersectionObserver((entries) => {
    for (const en of entries) {
      const v = en.target.querySelector("video");
      if (en.isIntersecting) {
        if (!v.dataset.loaded) {
          v.dataset.loaded = "1";
          v.src = `/api/events/${en.target.dataset.id}/clip?token=${encodeURIComponent(getToken())}`;
        }
        v.play().catch(() => { /* paused via tap or autoplay blocked */ });
      } else {
        v.pause();
      }
    }
  }, { rootMargin: "200px" });

  const tok = encodeURIComponent(getToken());
  for (const e of evs) {
    const card = el("div", "clip paused");
    card.dataset.id = e.id;
    card.innerHTML = `
      <div class="clip-box">
        <video muted loop playsinline preload="none"></video>
        <img src="/api/events/${e.id}/snapshot?token=${tok}" loading="lazy"
             onerror="this.style.opacity=.15">
        <span class="clip-badge">▶</span>
        <button class="clip-open ghost" title="open event">⤢</button>
      </div>
      <div class="clip-meta">
        <span class="clip-label">${labelIcon(e.label)} ${esc(e.label || "event")} ${(e.score * 100).toFixed(0)}%</span>
        <span class="clip-when">${esc(cameraName(e.camera_id))} · ${fmtWhen(e.start_ts)}</span>
      </div>`;
    const v = card.querySelector("video");
    const badge = card.querySelector(".clip-badge");
    v.addEventListener("loadeddata", () => card.querySelector("img").classList.add("gone"));
    v.addEventListener("play", () => card.classList.remove("paused"));
    v.addEventListener("pause", () => card.classList.add("paused"));
    v.addEventListener("error", () => {
      card.classList.add("broken"); // poster stays visible as the fallback
      badge.textContent = "clip unavailable";
    });
    card.querySelector(".clip-box").addEventListener("click", (ev) => {
      if (ev.target.closest(".clip-open")) return;
      if (v.paused) v.play().catch(() => {}); else v.pause();
    });
    card.querySelector(".clip-open").addEventListener("click", () => {
      location.hash = "#/events/" + e.id;
    });
    grid.appendChild(card);
    io.observe(card);
  }
}

function cameraName(id) {
  const c = camList.find((x) => x.id === id);
  return c ? c.name : id;
}

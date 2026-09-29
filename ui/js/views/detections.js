// views/detections.js — U7: grid of cropped object thumbs (B1) with type
// icon filters, camera dropdown, date — Scrypted's "find that thing" page.
import { api, esc, el, getToken, $ } from "../api.js";
import { labelChipHTML, labelIcon } from "../labels.js";

let camList = [];
let allEvents = [];
const typeSel = new Set();

function fmtDay(ms) { return new Date(ms).toLocaleDateString([], { weekday: "short", month: "short", day: "numeric" }); }
function fmtTime(ms) { return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }); }

export async function mount(page) {
  camList = await api("/api/cameras");
  page.innerHTML = `
    <div class="page-head"><h2>Detections</h2></div>
    <div class="filters">
      <select id="d-camera">
        <option value="">all cameras</option>
        ${camList.map((c) => `<option value="${esc(c.id)}">${esc(c.name)}</option>`).join("")}
      </select>
      <input id="d-date" type="date">
      <div class="chips" id="d-types">
        <button class="chip active" data-t="person">${labelChipHTML("person")}</button>
        <button class="chip" data-t="cat">${labelChipHTML("cat")}</button>
        <button class="chip" data-t="dog">${labelChipHTML("dog")}</button>
        <button class="chip" data-t="car">${labelChipHTML("car")}</button>
        <button class="chip active" data-t="*">all</button>
      </div>
      <button id="d-refresh" class="ghost">refresh</button>
    </div>
    <div id="d-grid" class="det-grid"></div>
    <p id="d-empty" style="color:var(--dim)">No detections match the current filters.</p>`;

  const load = () => loadDetections(page);
  $("#d-camera", page).addEventListener("change", load);
  $("#d-refresh", page).addEventListener("click", load);
  page.querySelectorAll("#d-types .chip").forEach((b) =>
    b.addEventListener("click", () => {
      const t = b.dataset.t;
      if (t === "*") {
        typeSel.clear();
      } else if (typeSel.has(t)) typeSel.delete(t); else typeSel.add(t);
      page.querySelectorAll("#d-types .chip").forEach((x) => {
        const xt = x.dataset.t;
        x.classList.toggle("active", xt === "*" ? typeSel.size === 0 : typeSel.has(xt));
      });
      load(page);
    }));
  await load(page);
}

async function loadDetections(page) {
  const cam = $("#d-camera", page)?.value || "";
  const date = $("#d-date", page)?.value || "";
  // pull 3 days back; date filter is applied client-side (day granularity)
  const from = date ? new Date(date + "T00:00:00").getTime() : Date.now() - 7 * 86400000;
  const evs = [];
  for (const c of cam ? [cam] : camList.map((x) => x.id)) {
    try {
      const list = await api(`/api/events?camera=${c}&from=${from}&limit=200`);
      evs.push(...list);
    } catch (e) { /* camera offline — skip */ }
  }
  evs.sort((a, b) => b.start_ts - a.start_ts);
  allEvents = evs;

  const grid = $("#d-grid", page);
  const empty = $("#d-empty", page);
  grid.innerHTML = "";

  const shown = evs.filter((e) => {
    if (typeSel.size && !typeSel.has(e.label)) return false;
    if (date && new Date(e.start_ts).toISOString().slice(0, 10) !== date) return false;
    return true;
  });

  empty.style.display = shown.length ? "none" : "block";
  for (const e of shown) {
    const crop = e.crop_path
      ? `/api/events/${e.id}/crop?token=${encodeURIComponent(getToken())}`
      : `/api/events/${e.id}/snapshot?token=${encodeURIComponent(getToken())}`;
    const d = el("div", "det");
    d.innerHTML = `
      <img src="${crop}" loading="lazy" onerror="this.style.opacity=.15">
      <div class="det-meta">
        <span class="det-icon">${labelIcon(e.label)}</span>
        <span class="det-when">${fmtTime(e.start_ts)}</span>
      </div>
      <div class="det-cam">${esc(cameraName(e.camera_id))}</div>`;
    d.addEventListener("click", () => { location.hash = "#/events/" + e.id; });
    grid.appendChild(d);
  }
}

function cameraName(id) {
  const c = camList.find((x) => x.id === id);
  return c ? c.name : id;
}

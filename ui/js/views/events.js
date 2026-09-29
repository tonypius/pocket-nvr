// views/events.js — U5-lite/U6-lite: day-grouped list, camera filter, range,
// unread separator, deep-linkable detail (#/events/{id}) with bbox overlay
// toggle, share/download, prev/next.
import { api, esc, el, getToken, $ } from "../api.js";
import { stopAll } from "../player.js";

let camList = [];
let loaded = [];       // current filtered list (detail prev/next order)
let seenMax = 0;       // highest event id already seen (localStorage)
const chipSel = new Set(); // active label filter chips
function fmtDay(ms) { return new Date(ms).toLocaleDateString([], { weekday: "short", month: "short", day: "numeric" }); }
function fmtTime(ms) { return new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }); }

export async function mount(page, param) {
  stopAll(); // no live players on this page
  try { seenMax = Number(localStorage.getItem("pnvr_seen_max")) || 0; } catch (e) { seenMax = 0; }

  camList = await api("/api/cameras");

  if (param && /^\d+$/.test(param)) {
    await renderDetail(page, Number(param));
    return () => {};
  }

  page.innerHTML = `
    <div class="page-head"><h2>Events</h2></div>
    <div class="filters">
      <select id="f-camera"></select>
      <select id="f-range">
        <option value="86400000">24 h</option>
        <option value="604800000" selected>7 days</option>
        <option value="2592000000">30 days</option>
      </select>
      <button id="f-refresh" class="ghost">refresh</button>
    </div>
    <div id="label-chips" class="chips"></div>
    <div id="events" class="events"></div>`;

  const sel = $("#f-camera", page);
  sel.innerHTML = `<option value="">all cameras</option>` +
    camList.map((c) => `<option value="${esc(c.id)}">${esc(c.name)}</option>`).join("");
  $("#f-refresh", page).addEventListener("click", () => renderList(page));
  sel.addEventListener("change", () => renderList(page));
  $("#f-range", page).addEventListener("change", () => renderList(page));
  await renderList(page);
  return () => {};
}

async function renderList(page) {
  const cam = $("#f-camera", page)?.value || "";
  const range = Number($("#f-range", page)?.value || 604800000);
  const from = Date.now() - range;
  const evs = await api(`/api/events?from=${from}&limit=200${cam ? "&camera=" + cam : ""}`);
  loaded = evs;

  // label chips (B2/U5): one per distinct label; click toggles the filter
  const labels = [...new Set(evs.map((e) => e.label || "motion"))].sort();
  const chips = $("#label-chips", page);
  chips.innerHTML = "";
  for (const lb of labels) {
    const b = el("button", "chip" + (chipSel.has(lb) ? " active" : ""), lb);
    b.addEventListener("click", () => {
      if (chipSel.has(lb)) chipSel.delete(lb); else chipSel.add(lb);
      renderList(page);
    });
    chips.appendChild(b);
  }
  const shown = chipSel.size ? evs.filter((e) => chipSel.has(e.label)) : evs;

  const wrap = $("#events", page);
  wrap.innerHTML = shown.length ? "" : "<p style='color:var(--dim)'>No events in this window.</p>";

  let lastDay = "";
  let newMarkPending = true;
  for (const e of shown) {
    const day = fmtDay(e.start_ts);
    if (day !== lastDay) {
      lastDay = day;
      wrap.appendChild(el("div", "dayhead", day));
    }
    const d = el("div", "ev" + (newMarkPending && e.id > seenMax && seenMax ? " newmark" : ""));
    d.innerHTML = `
      <img src="/api/events/${e.id}/snapshot?token=${encodeURIComponent(getToken())}" loading="lazy"
           onerror="this.style.visibility='hidden'">
      <div class="meta">
        <div class="score">${esc(e.label || "event")} ${(e.score * 100).toFixed(0)}%</div>
        <div>${esc(cameraName(e.camera_id))}</div>
        <div class="when">${fmtTime(e.start_ts)}</div>
      </div>`;
    d.addEventListener("click", () => { location.hash = "#/events/" + e.id; });
    wrap.appendChild(d);
    newMarkPending = false;
  }

  // "new since last visit" separator only makes sense when events exist
  const maxId = evs.length ? Math.max(...evs.map((e) => e.id)) : seenMax;
  if (maxId > seenMax) {
    try { localStorage.setItem("pnvr_seen_max", String(maxId)); } catch (e) {}
  }

  if (!$("#f-camera", page).dataset.seeded) {
    $("#f-camera", page).dataset.seeded = "1";
  }
}

function cameraName(id) {
  const c = camList.find((x) => x.id === id);
  return c ? c.name : id;
}

async function renderDetail(page, id) {
  const e = await api("/api/events/" + id).catch(() => null);
  page.innerHTML = `
    <div class="page-head">
      <button class="ghost" id="ev-back">← back</button>
      <h2>${e ? esc(e.camera_id) + " · " + esc(e.label || "event") + " " + Math.round((e.score || 0) * 100) + "%" : "Event"}</h2>
    </div>`;
  $("#ev-back", page).addEventListener("click", () => { location.hash = "#/events"; });
  if (!e) { page.appendChild(el("p", "", "Event not found.")); return; }

  const tok = encodeURIComponent(getToken());
  const when = new Date(e.start_ts).toLocaleString();
  const card = el("div", "cam-edit full");
  card.innerHTML = `
    <div class="full snapbox" style="position:relative">
      <img id="ev-snap" src="/api/events/${id}/snapshot?token=${tok}" onerror="this.style.opacity=.2">
      <div id="ev-bbox" class="bbox hidden"></div>
    </div>
    <div class="full" style="display:flex;gap:8px;flex-wrap:wrap;padding-top:8px">
      <button class="ghost" id="ev-bbox-toggle">show detection box</button>
      ${e.clip_path ? `<button id="ev-dl">download clip</button>` : ""}
      <button class="ghost" id="ev-share">share</button>
      <span style="color:var(--dim);align-self:center">${when}${e.zone ? " · " + esc(e.zone) : ""}</span>
    </div>
    ${e.clip_path ? `<div class="full"><video controls src="/api/events/${id}/clip?token=${tok}"></video></div>` : ""}`;
  page.appendChild(card);

  // bbox overlay: coordinates are pixels in a 640x360 analysis frame
  const setBox = () => {
    const box = $("#ev-bbox", page);
    const img = $("#ev-snap", page);
    if (!box || !e.bbox) return;
    let b = [];
    try { b = JSON.parse(e.bbox); } catch (err) { return; }
    if (b.length !== 4) return;
    box.style.left = (b[0] / 640 * 100) + "%";
    box.style.top = (b[1] / 360 * 100) + "%";
    box.style.width = (b[2] / 640 * 100) + "%";
    box.style.height = (b[3] / 360 * 100) + "%";
  };
  setTimeout(setBox, 300);
  $("#ev-bbox-toggle", page).addEventListener("click", (ev) => {
    const b = $("#ev-bbox", page);
    const hidden = b.classList.toggle("hidden");
    ev.target.textContent = hidden ? "show detection box" : "hide detection box";
  });
  $("#ev-dl", page)?.addEventListener("click", () => {
    const a = document.createElement("a");
    a.href = `/api/events/${id}/clip?token=${encodeURIComponent(getToken())}`;
    a.download = `event-${id}.mp4`;
    a.click();
  });
  $("#ev-share", page).addEventListener("click", async () => {
    const url = `${location.origin}/#/events/${id}`;
    if (navigator.share) {
      try { await navigator.share({ title: "PocketNVR event", url }); } catch (err) { /* cancelled */ }
    } else {
      try { await navigator.clipboard.writeText(url); ev_notice("link copied"); } catch (err) {}
    }
  });
  function ev_notice(msg) {
    const n = el("span", "", msg); n.style.color = "var(--ok)";
    card.appendChild(n);
    setTimeout(() => n.remove(), 2000);
  }
}

// helpers re-exported for main.js router
export { $ };

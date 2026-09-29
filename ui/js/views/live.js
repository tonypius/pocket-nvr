// views/live.js — U3: multi-cam live grid with density switch (persisted),
// overlays, fullscreen single camera (#/live/{id}), IntersectionObserver
// pausing for offscreen tiles, wake lock while visible.
import { api, esc, el } from "../api.js";
import { setWantWake } from "../pwa.js";
import { playTile, stopAll } from "../player.js";

let camList = [];
let io = null;

if ("IntersectionObserver" in window) {
  io = new IntersectionObserver((entries) => {
    for (const en of entries) {
      const v = en.target;
      if (en.isIntersecting) v.play?.().catch(() => {});
      else v.pause?.();
    }
  }, { threshold: 0.25 });
}

function makeTile(cam, big) {
  const tile = el("div", "tile" + (big ? " big" : ""));
  tile.innerHTML = `
    <video autoplay muted playsinline></video>
    <div class="tag"><span class="dot"></span>${esc(cam.name)}</div>
    <div class="off">connecting…</div>`;
  tile.addEventListener("click", () => { location.hash = "#/live/" + cam.id; });
  return tile;
}

function density() {
  try { return localStorage.getItem("pnvr_density") || "auto"; } catch (e) { return "auto"; }
}

export async function mount(page, param) {
  setWantWake(true);
  camList = await api("/api/cameras");
  const one = param ? camList.find((c) => c.id === param) : null;
  page.innerHTML = "";

  if (one) { // fullscreen single camera: #/live/{id}
    const head = el("div", "page-head");
    const back = el("button", "ghost", "← back");
    back.addEventListener("click", () => { location.hash = "#/live"; });
    head.append(el("h2", "", one.name), back);
    const tile = makeTile(one, true);
    page.append(head, tile);
    playTile(tile.querySelector("video"), one, tile);
    return () => { stopAll(); setWantWake(false); };
  }

  const head = el("div", "page-head");
  head.append(el("h2", "", "Cameras"));
  const dens = el("div", "density");
  for (const d of ["auto", "1", "2", "4", "9"]) {
    const b = el("button", d === density() ? "active" : "", d === "auto" ? "auto" : d);
    b.dataset.d = d;
    b.addEventListener("click", () => {
      try { localStorage.setItem("pnvr_density", d); } catch (e) {}
      const g = document.getElementById("cameras");
      if (g) {
        g.classList.remove("d-1", "d-2", "d-4", "d-9");
        if (d !== "auto") g.classList.add("d-" + d);
      }
      page.querySelectorAll(".density button").forEach((x) => x.classList.toggle("active", x.dataset.d === d));
    });
    dens.appendChild(b);
  }
  head.appendChild(dens);
  page.append(head);

  const grid = el("div", "tile-grid" + (density() !== "auto" ? " d-" + density() : ""));
  grid.id = "cameras";
  page.appendChild(grid);

  for (const c of camList.filter((c) => c.enabled)) {
    const tile = makeTile(c, false);
    grid.appendChild(tile);
    playTile(tile.querySelector("video"), c, tile);
    if (io) io.observe(tile.querySelector("video"));
  }
  return () => { stopAll(); setWantWake(false); if (io) io.disconnect(); };
}

export function unmount() {
  stopAll();
  setWantWake(false);
  if (io) io.disconnect();
}

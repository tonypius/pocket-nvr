// main.js — auth gate, router, health poll (Phase A shell v2).
import { getToken, setToken, api } from "./api.js";
import { initTheme } from "./theme.js";
import { initPWA } from "./pwa.js";
import { initEventStream, onLiveEvent } from "./sse.js";
import * as Home from "./views/home.js";
import * as Live from "./views/live.js";
import * as Events from "./views/events.js";
import * as Settings from "./views/settings.js";
import * as Detections from "./views/detections.js";
import * as Timeline from "./views/timeline.js";
import * as Clips from "./views/clips.js";

// Timeline is hidden from the nav (replaced by Clips) but stays registered
// so existing #/timeline deep links keep working.
const views = { home: Home, live: Live, events: Events, settings: Settings,
  detections: Detections, timeline: Timeline, clips: Clips };
const titles = { home: "Home", live: "Live", events: "Events", settings: "Settings",
  detections: "Detections", timeline: "Timeline", clips: "Clips" };

let cleanup = null;
let healthTimer = null;

function showLogin() {
  document.getElementById("app").classList.add("hidden");
  document.getElementById("login").classList.remove("hidden");
}
function hideLogin() {
  document.getElementById("login").classList.add("hidden");
  document.getElementById("app").classList.remove("hidden");
}

async function router() {
  const raw = (location.hash.replace(/^#\/?/, "") || "home").split("?")[0];
  const parts = raw.split("/");
  const name = views[parts[0]] ? parts[0] : "home";
  const param = parts[1] || "";

  if (cleanup) { try { cleanup(); } catch (e) {} cleanup = null; }

  document.getElementById("page-title").textContent = titles[name] || name;
  document.querySelectorAll(".nav, .tabbar .tab").forEach((a) =>
    a.classList.toggle("active", a.dataset.route === name));

  const container = document.getElementById("page");
  container.innerHTML = "";
  try {
    cleanup = await views[name].mount(container, decodeURIComponent(param));
  } catch (e) {
    container.innerHTML = `<p style="color:var(--dim)">Failed to load ${esc(name)}: ${esc(String(e.message || e))}</p>`;
  }
}

async function pollHealth() {
  if (!getToken()) return;
  try {
    const h = await api("/api/health");
    const comps = Object.entries(h.components)
      .map(([k, v]) => `<span class="${v === "up" ? "ok" : "bad"}">${k} ${v}</span>`).join("<br>");
    const temp = h.temp_c != null ? `${h.temp_c.toFixed(0)}°C<br>` : "";
    const free = h.storage?.free_bytes != null ? `${(h.storage.free_bytes / 1e9).toFixed(0)} GB free` : "";
    const deg = h.storage_degraded ? `<span class="bad">recording paused</span>` : "";
    const elh = document.getElementById("health");
    if (elh) elh.innerHTML = comps + "<br>" + temp + free + deg;
  } catch (e) { /* login gate handles auth failures */ }
}

function boot() {
  hideLogin();
  initTheme();
  initPWA();
  router();
  initEventStream();
  onLiveEvent(() => {
    // refresh whichever view shows events
    if ((location.hash || "#/home").startsWith("#/events")) router();
  });
  pollHealth();
  clearInterval(healthTimer);
  healthTimer = setInterval(pollHealth, 10000);
  window.addEventListener("hashchange", router);
  if ("serviceWorker" in navigator) navigator.serviceWorker.register("sw.js").catch(() => {});
}

document.getElementById("login-btn").addEventListener("click", tryLogin);
document.getElementById("login-token").addEventListener("keydown", (e) => e.key === "Enter" && tryLogin());
document.addEventListener("pnvr:unauthorized", showLogin);

function tryLogin() {
  const t = document.getElementById("login-token").value.trim();
  if (!t) return;
  setToken(t);
  document.getElementById("login-err").textContent = "";
  boot();
}

if (getToken()) boot(); else showLogin();

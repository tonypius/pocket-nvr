// pwa.js — U13: service worker v4 (precache + update toast), install UX,
// offline banner, wake lock, iOS standalone hints.

export function initPWA() {
  initSW();
  initInstall();
  initOfflineBanner();
}

/* ---- service worker + update toast ---- */
function initSW() {
  if (!("serviceWorker" in navigator)) return;
  navigator.serviceWorker.register("sw.js").then((reg) => {
    reg.addEventListener("updatefound", () => {
      const nw = reg.installing;
      if (!nw) return;
      nw.addEventListener("statechange", () => {
        if (nw.state === "installed" && navigator.serviceWorker.controller) {
          showToast();
        }
      });
    });
  }).catch(() => {});

  let refreshing = false;
  navigator.serviceWorker.addEventListener("controllerchange", () => {
    if (refreshing) { location.reload(); refreshing = false; }
  });
  navigator.serviceWorker.addEventListener("message", (ev) => {
    if (ev.data && ev.data.type === "update-available") showToast();
  });

  function showToast() {
    const t = document.getElementById("update-toast");
    if (!t) return;
    t.classList.remove("hidden");
    document.getElementById("update-reload").onclick = () => {
      refreshing = true;
      navigator.serviceWorker.controller?.postMessage({ type: "SKIP_WAITING" });
    };
    document.getElementById("update-dismiss").onclick = () => t.classList.add("hidden");
  }
}

/* ---- install UX ---- */
let deferredPrompt = null;
function initInstall() {
  window.addEventListener("beforeinstallprompt", (e) => {
    e.preventDefault();
    deferredPrompt = e;
    document.getElementById("install-btn")?.classList.remove("hidden");
    document.getElementById("top-install")?.classList.remove("hidden");
  });
  const doInstall = async () => {
    if (!deferredPrompt) return;
    deferredPrompt.prompt();
    await deferredPrompt.userChoice.catch(() => {});
    deferredPrompt = null;
    document.getElementById("install-btn")?.classList.add("hidden");
    document.getElementById("top-install")?.classList.add("hidden");
  };
  document.getElementById("install-btn")?.addEventListener("click", doInstall);
  document.getElementById("top-install")?.addEventListener("click", doInstall);
  // iOS: no install prompt — hint when running Safari but not installed
  const ios = /iphone|ipad|ipod/i.test(navigator.userAgent);
  const standalone = matchMedia("(display-mode: standalone)").matches
    || navigator.standalone === true;
  if (ios && !standalone && !sessionStorage.getItem("pnvr_ios_hint_shown")) {
    const t = document.getElementById("update-toast");
    if (t) {
      t.classList.remove("hidden");
      t.innerHTML = 'Install: Share → "Add to Home Screen".' +
        ' <button id="update-dismiss" class="ghost">✕</button>';
      document.getElementById("update-dismiss").onclick = () => {
        t.classList.add("hidden");
        try { sessionStorage.setItem("pnvr_ios_hint_shown", "1"); } catch (e) {}
      };
    }
  }
}

/* ---- offline banner ---- */
function initOfflineBanner() {
  const upd = () => {
    const b = document.getElementById("offline-banner");
    if (b) b.classList.toggle("hidden", navigator.onLine);
  };
  window.addEventListener("online", upd);
  window.addEventListener("offline", upd);
  upd();
}

/* ---- wake lock (viewing ergonomics) ---- */
let wakeLock = null;
export async function acquireWakeLock() {
  try {
    if ("wakeLock" in navigator && !wakeLock) {
      wakeLock = await navigator.wakeLock.request("screen");
      wakeLock.addEventListener("release", () => { wakeLock = null; });
    }
  } catch (e) { /* denied or unsupported */ }
}
export function releaseWakeLock() {
  try { wakeLock?.release(); } catch (e) {}
  wakeLock = null;
}
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible" && wakeLock === null && window.__wantWake) {
    acquireWakeLock();
  }
});
export function setWantWake(v) {
  window.__wantWake = v;
  if (v) acquireWakeLock(); else releaseWakeLock();
}

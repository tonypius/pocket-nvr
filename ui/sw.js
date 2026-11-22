// sw.js — v7: full shell precache, network-first (self-hosted app: updates
// land on reload), /api and media never cached, update notification,
// Web Push (event alerts) + notification click routing.
const CACHE = "pocketnvr-v7";
const PRECACHE = [
  "/", "index.html", "style.css", "manifest.webmanifest",
  "icon-192.png", "icon-512.png", "apple-touch-icon.png",
  "vendor/hls.min.js",
  "js/api.js", "js/theme.js", "js/pwa.js", "js/push.js", "js/player.js", "js/main.js",
  "js/views/home.js", "js/views/live.js", "js/views/events.js", "js/views/settings.js",
  "js/views/detections.js", "js/views/timeline.js", "js/views/clips.js",
];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(PRECACHE)));
  self.skipWaiting();
});

self.addEventListener("activate", (e) => {
  e.waitUntil(caches.keys().then((keys) =>
    Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))));
  self.clients.claim();
});

self.addEventListener("message", (e) => {
  if (e.data && e.data.type === "SKIP_WAITING") self.skipWaiting();
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  // never cache APIs, streams, or non-GET
  if (e.request.method !== "GET") return;
  if (url.pathname.startsWith("/api/")) return;
  e.respondWith(
    fetch(e.request).then((r) => {
      if (r.ok && r.type === "basic") {
        const copy = r.clone();
        caches.open(CACHE).then((c) => c.put(e.request, copy));
      }
      return r;
    }).catch(() => caches.match(e.request))
  );
});

// ---- Web Push: event alerts from nvrd (payload is end-to-end encrypted) ----
self.addEventListener("push", (e) => {
  let d = {};
  try { d = e.data ? e.data.json() : {}; }
  catch (err) { d = { body: e.data ? e.data.text() : "" }; }
  // Browsers require every push to show a notification (userVisibleOnly).
  e.waitUntil(self.registration.showNotification(d.title || "PocketNVR", {
    body: d.body || "",
    icon: "icon-192.png",
    badge: "icon-192.png",
    image: d.image || undefined, // Android shows it; iOS ignores it
    tag: d.tag || undefined,
    timestamp: d.ts || Date.now(),
    data: { url: d.url || "#/events" },
  }));
});

self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  // routes are in-app hashes (#/events/42); resolve against the PWA scope
  const target = new URL((e.notification.data && e.notification.data.url) || "#/events",
    self.registration.scope).href;
  e.waitUntil((async () => {
    const wins = await clients.matchAll({ type: "window", includeUncontrolled: true });
    for (const w of wins) {
      if (new URL(w.url).origin === new URL(target).origin) {
        await w.focus();
        // same-origin hash change → the app's router reacts, no reload
        if ("navigate" in w) { try { await w.navigate(target); } catch (err) { /* ignore */ } }
        return;
      }
    }
    await clients.openWindow(target);
  })());
});

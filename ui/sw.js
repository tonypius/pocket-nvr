// sw.js — v5: full shell precache, network-first (self-hosted app: updates
// land on reload), /api and media never cached, update notification.
const CACHE = "pocketnvr-v5";
const PRECACHE = [
  "/", "index.html", "style.css", "manifest.webmanifest",
  "icon-192.png", "icon-512.png", "apple-touch-icon.png",
  "vendor/hls.min.js",
  "js/api.js", "js/theme.js", "js/pwa.js", "js/player.js", "js/main.js",
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

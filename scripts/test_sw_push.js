const fs = require("fs"), vm = require("vm"), assert = require("assert");
const handlers = {};
const shown = [], opened = [], navigated = [], focused = [];
const wins = [];
const ctx = {
  URL, console,
  caches: { open: async () => ({ addAll: async () => {}, put: () => {} }), keys: async () => [], delete: async () => {}, match: async () => undefined },
  fetch: async () => ({ ok: true, type: "basic", clone() { return this; } }),
  clients: {
    claim() {},
    matchAll: async () => wins,
    openWindow: async (u) => { opened.push(u); },
  },
  self: {
    addEventListener: (t, f) => { handlers[t] = f; },
    skipWaiting() {}, clients: null,
    registration: { scope: "https://nvr.example.com/", showNotification: async (t, o) => { shown.push([t, o]); } },
  },
};
ctx.self.clients = ctx.clients;
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(require("path").join(__dirname, "..", "ui", "sw.js"), "utf8"), ctx);
(async () => {
  assert(handlers.push && handlers.notificationclick, "push + notificationclick handlers registered");

  // push with a JSON payload shows a notification built from it
  let p; const ev = (data) => ({ data, waitUntil(x) { p = x; } });
  handlers.push(ev({ json: () => ({ title: "PocketNVR — Front", body: "Person detected", url: "#/events/42", tag: "event-42", image: "https://x/y.jpg", ts: 1 }) }));
  await p;
  assert.strictEqual(shown[0][0], "PocketNVR — Front");
  assert.strictEqual(shown[0][1].data.url, "#/events/42");
  assert.strictEqual(shown[0][1].tag, "event-42");
  assert.strictEqual(shown[0][1].image, "https://x/y.jpg");

  // a push with no/garbled data must STILL show something (browsers penalise silent pushes)
  handlers.push(ev({ json: () => { throw new Error("bad"); }, text: () => "plain text" }));
  await p;
  assert.strictEqual(shown[1][0], "PocketNVR");
  assert.strictEqual(shown[1][1].body, "plain text");
  handlers.push(ev(null));
  await p;
  assert.strictEqual(shown.length, 3);

  // click with no open window → opens the event route inside the PWA scope
  let closed = 0;
  const click = (url) => ({ notification: { close() { closed++; }, data: url ? { url } : undefined }, waitUntil(x) { p = x; } });
  handlers.notificationclick(click("#/events/42"));
  await p;
  assert.deepStrictEqual(opened, ["https://nvr.example.com/#/events/42"]);

  // click with the app already open → focus + navigate, no second window
  wins.push({ url: "https://nvr.example.com/#/home", focus: async () => focused.push(1), navigate: async (u) => navigated.push(u) });
  handlers.notificationclick(click("#/events/7"));
  await p;
  assert.strictEqual(opened.length, 1);
  assert.deepStrictEqual(navigated, ["https://nvr.example.com/#/events/7"]);
  assert.strictEqual(focused.length, 1);
  assert.strictEqual(closed, 2);
  console.log("sw push/click handlers OK");
})().catch((e) => { console.error("FAIL", e); process.exit(1); });

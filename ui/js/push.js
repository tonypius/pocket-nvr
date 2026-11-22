// push.js — Web Push (PWA notifications) for this device.
// Flow: ask permission → subscribe via the service worker with nvrd's VAPID
// public key → hand the subscription to nvrd, which pushes events to it.
import { api } from "./api.js";

const isIOS = () => /iphone|ipad|ipod/i.test(navigator.userAgent) ||
  (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
const isStandalone = () => window.matchMedia("(display-mode: standalone)").matches ||
  navigator.standalone === true;

export function pushSupported() {
  return "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;
}

// iOS only exposes Web Push to a PWA launched from the Home Screen.
export const needsInstall = () => isIOS() && !isStandalone();

function b64urlToBytes(s) {
  const pad = "=".repeat((4 - (s.length % 4)) % 4);
  const raw = atob((s + pad).replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}

async function currentSubscription() {
  const reg = await navigator.serviceWorker.ready;
  return { reg, sub: await reg.pushManager.getSubscription() };
}

// state: { kind, devices } — kind is one of
// unsupported | install | blocked | server-off | off | on
export async function pushState() {
  if (needsInstall()) return { kind: "install" };
  if (!pushSupported()) return { kind: "unsupported" };
  let status;
  try { status = await api("/api/push/status"); } catch (e) { return { kind: "server-off" }; }
  if (!status.enabled) return { kind: "server-off", devices: status.devices };
  if (Notification.permission === "denied") return { kind: "blocked", devices: status.devices };
  const { sub } = await currentSubscription();
  return { kind: sub ? "on" : "off", devices: status.devices };
}

// Must be called from a click handler: the permission prompt needs a gesture.
export async function enablePush() {
  const perm = await Notification.requestPermission();
  if (perm !== "granted") throw new Error("Notifications were not allowed.");
  const { public_key } = await api("/api/push/vapid-key");
  const { reg, sub: existing } = await currentSubscription();
  let sub = existing;
  // a subscription made against a different server key can never receive pushes
  if (sub) {
    const cur = sub.options && sub.options.applicationServerKey;
    const want = b64urlToBytes(public_key);
    const same = cur && cur.byteLength === want.length &&
      new Uint8Array(cur).every((b, i) => b === want[i]);
    if (!same) { await sub.unsubscribe(); sub = null; }
  }
  if (!sub) {
    sub = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: b64urlToBytes(public_key),
    });
  }
  await api("/api/push/subscribe", { method: "POST", body: JSON.stringify(sub.toJSON()) });
}

export async function disablePush() {
  const { sub } = await currentSubscription();
  if (!sub) return;
  const endpoint = sub.endpoint;
  await sub.unsubscribe();
  await api("/api/push/unsubscribe", { method: "POST", body: JSON.stringify({ endpoint }) });
}

export async function sendTestPush() {
  const r = await api("/api/push/test", { method: "POST", body: "{}" });
  if (r.error) throw new Error(r.error);
  return r.sent;
}

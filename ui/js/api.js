// api.js — token-safe API helper + shared utilities.
let memToken = "";

export function setToken(t) { memToken = t || ""; try { localStorage.setItem("pocketnvr_token", memToken); } catch (e) { /* private mode */ } }
export function storageToken() {
  try { return localStorage.getItem("pocketnvr_token") || ""; } catch (e) { return ""; }
}
export function getToken() {
  const q = new URLSearchParams(location.search).get("token");
  if (q) { setToken(q); }
  return memToken || storageToken();
}

export async function api(path, opts = {}) {
  const r = await fetch(path, {
    ...opts,
    headers: { "Content-Type": "application/json", "X-Api-Token": getToken(), ...(opts.headers || {}) },
  });
  if (r.status === 401) {
    document.dispatchEvent(new CustomEvent("pnvr:unauthorized"));
    throw new Error("unauthorized");
  }
  if (!r.ok) throw new Error((await r.text()) || String(r.status));
  const ct = r.headers.get("content-type") || "";
  return ct.includes("json") ? r.json() : r;
}

export const esc = (s) => String(s).replace(/[&<>"]/g, (c) =>
  ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));

export const $ = (s, el = document) => el.querySelector(s);
export const $$ = (s, el = document) => [...el.querySelectorAll(s)];

export function el(tag, cls, html) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (html !== undefined) e.innerHTML = html;
  return e;
}

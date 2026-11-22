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
  if (!r.ok) throw new Error(await errorText(r));
  const ct = r.headers.get("content-type") || "";
  return ct.includes("json") ? r.json() : r;
}

// Turn a failed response into a short, readable message. Proxies (Cloudflare)
// answer with whole HTML pages; showing those verbatim buries the page.
async function errorText(r) {
  let body = "";
  try { body = (await r.text()).trim(); } catch (e) { /* ignore */ }
  const ct = r.headers.get("content-type") || "";
  if (ct.includes("json")) {
    try { const j = JSON.parse(body); if (j && j.error) return String(j.error); } catch (e) { /* fall through */ }
  }
  if (!body || ct.includes("html") || body.startsWith("<")) {
    const why = r.status === 502 || r.status === 504 || r.status === 503
      ? "the NVR is unreachable (gateway error)" : "request failed";
    return `${why} — HTTP ${r.status}`;
  }
  return body.length > 200 ? body.slice(0, 200) + "…" : body;
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

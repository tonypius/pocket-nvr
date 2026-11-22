// Node check: api() must turn failed responses into short messages — never
// dump a proxy's HTML error page (e.g. Cloudflare 502) into the UI.
import assert from "node:assert";
globalThis.location = { search: "" };
globalThis.localStorage = { getItem: () => "tok", setItem() {} };
globalThis.document = { dispatchEvent() {} };
globalThis.CustomEvent = class {};
// ui/js/*.js are browser ES modules without a package.json "type", so load the
// source through a data: URL instead of a file import.
import { readFileSync } from "node:fs";
const src = readFileSync(new URL("../ui/js/api.js", import.meta.url), "utf8");
const { api } = await import("data:text/javascript;base64," + Buffer.from(src).toString("base64"));

// minimal Response stand-in (older Node has no global Response)
const respond = (status, body, type) => async () => ({
  ok: status < 400, status,
  headers: { get: (k) => (k.toLowerCase() === "content-type" ? type : null) },
  text: async () => body,
  json: async () => JSON.parse(body),
});
const msg = async (f) => { globalThis.fetch = f; try { await api("/x"); } catch (e) { return e.message; } };

const cf = "<!DOCTYPE html><html><head><title>502: Bad gateway</title></head>" + "<body>x</body></html>".repeat(200);
let m = await msg(respond(502, cf, "text/html; charset=UTF-8"));
assert(m.length < 120 && !m.includes("<") && m.includes("502"), "cloudflare page → short message: " + m);
assert(m.includes("unreachable"), m);

m = await msg(respond(400, '{"error":"unknown secret(s): cam1_user"}', "application/json"));
assert.strictEqual(m, "unknown secret(s): cam1_user");

m = await msg(respond(400, "bad key or value", "text/plain"));
assert.strictEqual(m, "bad key or value");

m = await msg(respond(500, "x".repeat(1000), "text/plain"));
assert(m.length <= 201, "long plain errors are trimmed");

m = await msg(respond(502, "", "text/plain"));
assert(m.includes("502"));
console.log("api error messages OK");

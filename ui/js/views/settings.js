// views/settings.js — camera editors, network scan, recording/detection/
// notification fields, write-only secrets editor, save & apply (FR-API-4).
import { api, esc, el, getToken, $, $$ } from "../api.js";

let cfg = null;
let camList = [];
const secretsToSave = {};
const num = (v) => Number(v);

export async function mount(page) {
  cfg = await api("/api/config");
  camList = await api("/api/cameras");

  page.innerHTML = `
    <div class="page-head"><h2>Settings</h2></div>
    <h3 class="sec">Network scan</h3>
    <div class="scanbar">
      <button id="scan-btn" class="primary">Scan for cameras</button>
      <span id="scan-status"></span>
    </div>
    <div id="scan-results"></div>
    <h3 class="sec">Cameras</h3>
    <div id="cfg-cameras"></div>
    <button id="add-camera" class="ghost">+ add camera</button>
    <h3 class="sec">Recording</h3><div id="cfg-recording" class="kv"></div>
    <h3 class="sec">Detection</h3><div id="cfg-detection" class="kv"></div>
    <h3 class="sec">Notifications</h3><div id="cfg-notify" class="kv"></div>
    <h3 class="sec">Secrets <small>(write-only — type key + value)</small></h3>
    <div id="cfg-secrets"></div>
    <button id="add-secret" class="ghost">+ add secret</button>
    <div class="savebar">
      <button id="save-all" class="primary">Save &amp; apply</button>
      <span id="save-status"></span>
    </div>`;

  const pageEl = document.getElementById("page");
  renderCameras(pageEl);
  kv("#cfg-recording", page, [
    ["mode (continuous|event_only)", () => cfg.recording.mode, (v) => cfg.recording.mode = v],
    ["retention_days", () => cfg.recording.retention_days, (v) => cfg.recording.retention_days = num(v)],
    ["size_cap_gb", () => cfg.recording.size_cap_gb, (v) => cfg.recording.size_cap_gb = num(v)],
  ]);
  kv("#cfg-detection", page, [
    ["backend (cpu|vulkan)", () => cfg.detection.backend, (v) => cfg.detection.backend = v],
    ["input_size", () => cfg.detection.input_size, (v) => cfg.detection.input_size = num(v)],
    ["temp_high_c", () => cfg.detection.temp_high_c, (v) => cfg.detection.temp_high_c = num(v)],
    ["temp_low_c", () => cfg.detection.temp_low_c, (v) => cfg.detection.temp_low_c = num(v)],
  ]);
  kv("#cfg-notify", page, [
    ["provider (ntfy|telegram|both)", () => cfg.notifications.provider, (v) => cfg.notifications.provider = v],
    ["cooldown_seconds", () => cfg.notifications.cooldown_seconds, (v) => cfg.notifications.cooldown_seconds = num(v)],
    ["base_url (deep links)", () => cfg.notifications.base_url, (v) => cfg.notifications.base_url = v],
    ["quiet start (HH:MM)", () => cfg.notifications.quiet_hours_start, (v) => cfg.notifications.quiet_hours_start = v],
    ["quiet end (HH:MM)", () => cfg.notifications.quiet_hours_end, (v) => cfg.notifications.quiet_hours_end = v],
  ]);

  $("#scan-btn", page).addEventListener("click", () => scan(page));
  $("#add-camera", page).addEventListener("click", addCamera);
  $("#add-secret", page).addEventListener("click", () => addSecretRow("", ""));
  $("#save-all", page).addEventListener("click", () => saveAll(page));
}

function renderCameras(page) {
  const elc = page.querySelector("#cfg-cameras");
  elc.innerHTML = "";
  for (const c of cfg.cameras) elc.appendChild(cameraEditor(c, page));
}

function cameraEditor(c, page) {
  const div = el("div", "cam-edit");
  div.dataset.id = c.id;
  div.innerHTML = `
    <label>id<input data-k="id" value="${esc(c.id)}"></label>
    <label>name<input data-k="name" value="${esc(c.name)}"></label>
    <label>source_main — rtsp url<input class="full" data-k="source_main" value="${esc(c.source_main)}"></label>
    <label>source_sub — rtsp url<input class="full" data-k="source_sub" value="${esc(c.source_sub)}"></label>
    <label>enabled<select data-k="enabled">
      <option value="true" ${c.enabled !== false ? "selected" : ""}>yes</option>
      <option value="false" ${c.enabled === false ? "selected" : ""}>no</option></select></label>
    <label>fps<input data-k="fps" type="number" value="${c.detect?.fps ?? 5}"></label>
    <label>threshold<input data-k="threshold" type="number" step="0.05" value="${c.detect?.threshold ?? 0.5}"></label>
    <label>classes (COCO, comma-separated)<input class="full" data-k="classes" value="${esc((c.detect?.classes || ["person"]).join(", "))}"></label>
    <button class="ghost rm">remove camera</button>`;
  div.querySelector(".rm").addEventListener("click", () => {
    cfg.cameras = cfg.cameras.filter((x) => x.id !== c.id);
    div.remove();
  });
  return div;
}

function addCamera() {
  const page = document.getElementById("page");
  const id = "cam" + (cfg.cameras.length + 1);
  cfg.cameras.push({
    id, name: id, enabled: true,
    source_main: "rtsp://${secret:" + id + "_user}:${secret:" + id + "_pass}@192.168.0.100:554/stream1",
    source_sub: "rtsp://${secret:" + id + "_user}:${secret:" + id + "_pass}@192.168.0.100:554/stream2",
    detect: { fps: 5, threshold: 0.5, enter_frames: 3, exit_frames: 8,
              motion_min_area: 0.02, anchor: "bottom_center", zones: [] },
  });
  renderCameras(page.current || document.getElementById("page"));
}

async function scan(page) {
  const btn = $("#scan-btn", page), st = $("#scan-status", page), out = $("#scan-results", page);
  btn.disabled = true; st.textContent = "scanning (WS-Discovery + RTSP sweep, ~6 s)…"; out.innerHTML = "";
  try {
    const { results } = await api("/api/discover", { method: "POST", body: "{}" });
    st.textContent = `${results.length} device(s) found`;
    if (!results.length) {
      out.innerHTML = "<p style='color:var(--dim)'>None found. Check the camera's ONVIF/RTSP settings (FLAG-8 checklist in the FRS).</p>";
      return;
    }
    for (const r of results) {
      const d = el("div", "hit",
        `<span class="ip">${esc(r.ip)}</span>
         <span>${r.rtsp ? "RTSP ✓" : ""}</span>
         <span>${r.onvif ? "ONVIF ✓" : ""}</span>
         <span class="hint">${esc(r.onvif || r.name || "")}</span>
         <button class="primary">add</button>`);
      d.querySelector("button").addEventListener("click", () => {
        const id = "cam" + r.ip.split(".").pop();
        const base = "rtsp://${secret:" + id + "_user}:${secret:" + id + "_pass}@" + r.ip + ":554";
        cfg.cameras.push({
          id, name: "Camera " + r.ip.split(".").pop(), enabled: true,
          source_main: base + "/stream1",
          source_sub: base + "/stream2",
          detect: { fps: 5, threshold: 0.5, enter_frames: 3, exit_frames: 8,
                    motion_min_area: 0.02, anchor: "bottom_center", zones: [] },
        });
        const pageEl = document.getElementById("page");
  renderCameras(pageEl);
        d.querySelector("button").textContent = "added ✓";
        d.querySelector("button").disabled = true;
      });
      out.appendChild(d);
    }
  } catch (e) { st.textContent = "scan failed: " + e.message; }
  btn.disabled = false;
}

function kv(sel, page, fields) {
  const elc = page.querySelector(sel);
  elc.innerHTML = "";
  for (const [label, get, set] of fields) {
    const l = el("label", "", `${esc(label)}<input value="${esc(get())}">`);
    l.querySelector("input").addEventListener("change", (e) => set(e.target.value));
    elc.appendChild(l);
  }
}

function addSecretRow(key, val) {
  const div = el("div", "secret",
    `<input placeholder="key (e.g. front_user)" value="${esc(key)}">
     <input placeholder="value" value="${esc(val)}">`);
  $("#cfg-secrets", document).appendChild(div);
}

async function saveAll(page) {
  const st = $("#save-status", page);
  try {
    st.textContent = "saving…";
    for (const row of $$("#cfg-secrets .secret", page)) {
      const [k, v] = row.querySelectorAll("input");
      if (k.value && v.value) secretsToSave[k.value] = v.value;
    }
    if (Object.keys(secretsToSave).length)
      await api("/api/secrets", { method: "POST", body: JSON.stringify(secretsToSave) });
    for (const div of $$("#cfg-cameras .cam-edit", page)) {
      const old = div.dataset.id;
      const get = (k) => div.querySelector(`[data-k="${k}"]`).value;
      const idx = cfg.cameras.findIndex((x) => x.id === old);
      if (idx < 0) continue;
      const c = cfg.cameras[idx];
      cfg.cameras[idx] = {
        ...c, id: get("id"), name: get("name"),
        source_main: get("source_main"), source_sub: get("source_sub"),
        enabled: get("enabled") === "true",
        detect: { ...(c.detect || {}),
          fps: num(get("fps")), threshold: num(get("threshold")),
          classes: get("classes").split(",").map(function(x){ return x.trim().toLowerCase(); }).filter(Boolean) },
      };
    }
    const resp = await fetch("/api/config", {
      method: "PUT", headers: { "Content-Type": "application/json", "X-Api-Token": getToken() },
      body: JSON.stringify(cfg),
    });
    if (!resp.ok) throw new Error((await resp.json()).error || resp.status);
    st.textContent = "saved ✓ — applying";
    setTimeout(() => location.reload(), 1500);
  } catch (e) { st.textContent = "save failed: " + e.message; }
}

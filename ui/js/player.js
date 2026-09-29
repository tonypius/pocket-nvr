// player.js — camera live playback (U3): WebRTC/WHEP first (low latency),
// automatic fallback to nvrd-proxied HLS (works over any network path).
// WHEP only "succeeds" when media actually flows — signaling alone is not
// enough (that distinction is what leaves tiles blank on UDP-blocked paths).

import { getToken } from "./api.js";

const players = {}; // key -> RTCPeerConnection | Hls instance

export function stopPlayer(key) {
  const p = players[key];
  if (!p) return;
  try { p.destroy ? p.destroy() : p.close(); } catch (e) { /* already gone */ }
  delete players[key];
}

export function stopAll() {
  for (const k of Object.keys(players)) stopPlayer(k);
}

export function setTileState(tile, live, text) {
  const off = tile.querySelector(".off");
  const dot = tile.querySelector(".dot");
  if (live) { off?.classList.add("hidden"); dot?.classList.add("live"); }
  else {
    off?.classList.remove("hidden");
    if (off && text) off.textContent = text;
    dot?.classList.remove("live");
  }
}

export function playTile(video, cam, tile) {
  const key = cam.id;
  playWHEP(video, cam.live.webrtc, key).catch((e) => {
    console.warn("WHEP unavailable, falling back to HLS:", e.message);
    playHLS(video, "/api/live/hls/" + cam.id + "/index.m3u8", tile, key);
  });
}

export function stopTile(key) { stopPlayer(key); }

function gatherICE(pc) {
  return new Promise((res) => {
    if (pc.iceGatheringState === "complete") return res();
    const t = setTimeout(res, 2000);
    pc.addEventListener("icegatheringstatechange", () => {
      if (pc.iceGatheringState === "complete") { clearTimeout(t); res(); }
    });
  });
}

async function playWHEP(video, whepUrl, key) {
  const pc = new RTCPeerConnection();
  players[key] = pc;
  pc.addTransceiver("video", { direction: "recvonly" });
  pc.addTransceiver("audio", { direction: "recvonly" });

  return new Promise((resolve, reject) => {
    let settled = false;
    const fail = (msg) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      try { pc.close(); } catch (e) {}
      if (players[key] === pc) delete players[key];
      reject(new Error(msg));
    };
    // if no real frame arrives quickly, media is not flowing (typical when
    // ICE/UDP is blocked) — reject so the caller can use HLS
    const timer = setTimeout(() => fail("no media within 6s"), 6000);
    pc.ontrack = (e) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      video.srcObject = e.streams[0];
      resolve();
    };
    pc.onconnectionstatechange = () => {
      if (["failed", "disconnected", "closed"].includes(pc.connectionState)) fail("connection " + pc.connectionState);
    };
    (async () => {
      try {
        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
        await gatherICE(pc);
        const resp = await fetch(whepUrl, {
          method: "POST", headers: { "Content-Type": "application/sdp" },
          body: pc.localDescription.sdp,
        });
        if (!resp.ok) return fail("whep " + resp.status);
        await pc.setRemoteDescription({ type: "answer", sdp: await resp.text() });
      } catch (e) { fail(String(e)); }
    })();
  }).then(() => { /* media flowing */ });
}

function playHLS(video, hlsUrl, tile, key) {
  if (players[key]) stopPlayer(key);
  const ok = () => {};
  if (window.Hls && Hls.isSupported()) {
    const hls = new Hls({ backBufferLength: 30 });
    players[key] = hls;
    let recoveries = 0;
    hls.on(Hls.Events.ERROR, (_, data) => {
      try { (window.__hlslog = window.__hlslog || []).push("ERR " + data.details + (data.fatal ? " FATAL" : "")); } catch (e) {}
      if (!data.fatal) return;
      if (recoveries < 5 && data.type === Hls.ErrorTypes.NETWORK_ERROR) {
        recoveries += 1;
        setTimeout(() => { try { hls.startLoad(); } catch (e) {} }, 1500);
        return;
      }
      tile?.querySelector(".off")?.classList.remove("hidden");
    });
    hls.on(Hls.Events.FRAG_LOADED, () => {
      try { (window.__hlslog = window.__hlslog || []).push("FRAG_OK"); } catch (e) {}
    });
    hls.loadSource(hlsUrl + "?token=" + encodeURIComponent(getToken()));
    hls.attachMedia(video);
  } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
    video.src = hlsUrl + "?token=" + encodeURIComponent(getToken());
  }
}

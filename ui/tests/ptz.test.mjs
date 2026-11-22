import { test } from "node:test";
import assert from "node:assert/strict";
import { createHold } from "../js/ptz.js";

function harness(sendImpl) {
  const sends = [];
  const signals = [];
  let stops = 0;
  const errors = [];
  let tick = null;
  let cleared = 0;
  const hold = createHold({
    send: (dir, signal) => { sends.push(dir); signals.push(signal); return sendImpl ? sendImpl(dir, signal) : Promise.resolve(); },
    stop: () => { stops++; return Promise.resolve(); },
    onError: (e) => errors.push(e),
    setIntervalFn: (fn) => { tick = fn; return 1; },
    clearIntervalFn: () => { tick = null; cleared++; },
  });
  return { hold, sends, signals, errors, stops: () => stops, tick: () => tick && tick(), active: () => tick !== null };
}

test("press sends immediately then every interval until release", () => {
  const h = harness();
  h.hold.press("left");
  assert.deepEqual(h.sends, ["left"]);
  h.tick(); h.tick();
  assert.deepEqual(h.sends, ["left", "left", "left"]);
  h.hold.release();
  assert.equal(h.active(), false);
  h.tick();
  assert.equal(h.sends.length, 3);
  assert.equal(h.stops(), 1);
});

test("release without press does nothing", () => {
  const h = harness();
  h.hold.release();
  assert.equal(h.stops(), 0);
});

test("pressing another direction releases first", () => {
  const h = harness();
  h.hold.press("left");
  h.hold.press("up");
  assert.equal(h.stops(), 1);
  assert.deepEqual(h.sends, ["left", "up"]);
});

test("release aborts the in-flight send", () => {
  const h = harness(() => new Promise(() => {}));
  h.hold.press("left");
  assert.equal(h.signals[0].aborted, false);
  h.hold.release();
  assert.equal(h.signals[0].aborted, true);
});

test("failed send reports once and releases", async () => {
  const h = harness(() => Promise.reject(new Error("boom")));
  h.hold.press("left");
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(h.errors.length, 1);
  assert.equal(h.stops(), 1);
  assert.equal(h.active(), false);
  h.tick();
  assert.equal(h.sends.length, 1);
});

// Run with: node --test desktop/ui/*.test.js
// 기억 지도 bubbles: grouping, selection fan, zoom and pan, keyboard picks and name cards, on a recording 2D context.
const test = require("node:test");
const assert = require("node:assert/strict");
const dom = require("./testdata/fake-dom.cjs");
const { document } = dom.install();

// A canvas context that records every call; measureText gives a fixed width.
const calls = [];
const ctx2d = new Proxy({}, { get: (t, k) => (k in t ? t[k] : (t[k] = (...a) => { calls.push([k, ...a]); return k === "measureText" ? { width: 20 } : undefined; })), set: (t, k, v) => { t[k] = v; return true; } });
dom.Element.prototype.getContext = () => ctx2d;
let frames = [];
globalThis.requestAnimationFrame = window.requestAnimationFrame = (fn) => { frames.push(fn); return frames.length; };
globalThis.cancelAnimationFrame = window.cancelAnimationFrame = () => { frames = []; };
const runFrames = (t) => { const f = frames; frames = []; for (const fn of f) fn(t); };
let reduced = false;
globalThis.matchMedia = window.matchMedia = () => ({ get matches() { return reduced; } });
const observers = [];
globalThis.ResizeObserver = window.ResizeObserver = class { constructor(fn) { this.fn = fn; observers.push(this); } observe(n) { this.n = n; } disconnect() { this.off = true; } };
// cards measure 120×30; other nodes keep whatever size a test gives them
Object.defineProperty(dom.Element.prototype, "offsetWidth", { get() { return this.className.startsWith("mm-card") ? 120 : this._ow; }, set(v) { this._ow = v; } });
Object.defineProperty(dom.Element.prototype, "offsetHeight", { get() { return this.className.startsWith("mm-card") ? 30 : this._oh; }, set(v) { this._oh = v; } });
require("./memory-bubbles.js");

const KINDS = ["fact", "lesson", "preference"];
function make({ n = 6, tabs = [["reach", "칸별"], ["kind", "종류별"]], card = true, size = [800, 600] } = {}) {
  const log = { select: [], clear: 0, zoom: [] };
  const names = new Map();
  const b = window.ArkismMemoryBubbles.create({
    tabs,
    groupOf: (m, p) => (m === "kind" ? { key: p.kind, name: p.kind, color: "#123" } : { key: p.reach, name: `칸 ${p.reach}`, ord: p.reach === "general" ? 1 : 0 }),
    tip: (id) => (names.has(id) ? { text: names.get(id), meta: "사실 · 쓰임 2회" } : null),
    card: card ? (id) => (id === "nocard" ? null : { title: `카드 ${id}`, place: "일반", meta: "사실" }) : null,
    onSelect: (id) => log.select.push(id), onClear: () => log.clear++, onZoom: (z) => log.zoom.push(z)
  });
  document.body.append(b.el);
  const items = Array.from({ length: n }, (_, i) => ({ id: `m${i}`, v: n - i, color: "#abc", reach: i % 2 ? "general" : "project", kind: KINDS[i % 3] }));
  items.forEach((x, i) => names.set(x.id, i === 1 ? "" : `기억 이름 ${i} 아주 긴 이름입니다`));
  const canvas = b.el.querySelector("canvas");
  b.el.getBoundingClientRect = () => ({ left: 0, top: 0, width: size[0], height: size[1] });
  b.el.querySelectorAll(".mm-tab").forEach((t, i) => { t.offsetWidth = 50; t.offsetLeft = i * 50; });
  const resize = () => observers.at(-1).fn();
  return { b, items, canvas, log, resize, names };
}
const ptr = (canvas, type, x, y, id = 1, extra = {}) => dom.fire(canvas, type, { pointerId: id, clientX: x, clientY: y, button: 0, ...extra });
const key = (canvas, k) => { const e = new dom.Event("keydown", { key: k, cancelable: true }); canvas.dispatchEvent(e); return e.defaultPrevented; };
// screen place of a dot: the tooltip follows the hovered dot, so find a dot by sweeping the canvas instead of reading internals
function findDot(canvas, b) {
  for (let y = 0; y < 600; y += 3) for (let x = 0; x < 800; x += 3) {
    ptr(canvas, "pointermove", x, y, 9);
    if (canvas.style.cursor === "pointer") return [x, y];
  }
  return null;
}

test("an unsized map draws nothing and items wait for a size", () => {
  const { b, items, canvas, resize } = make();
  b.setItems(items); b.setEdges([]); b.setZoom(2); b.fitView(); b.setSelected("m0");
  assert.equal(calls.length, 0);
  b.el.getBoundingClientRect = () => ({ width: 0, height: 0 });
  resize(); assert.equal(canvas.style.width, undefined);
  b.destroy();
  assert.ok(observers.at(-1).off);
});

test("sizing lays the dots out, tabs regroup them and the same list does not re-settle", () => {
  const { b, items, canvas, resize, log } = make();
  b.setItems(items);
  b.setEdges([{ from: "m0", to: "m1", t: "supports" }, { from: "m2", to: "m3", t: "conflicts" }, { from: "m0", to: "gone", t: "supports" }, { from: "m2", to: "m4", t: "odd" }]);
  window.devicePixelRatio = 3;
  calls.length = 0; resize();
  assert.equal(canvas.width, 1600); // dpr capped at 2
  assert.equal(canvas.style.width, "800px");
  assert.ok(calls.some(([k]) => k === "arc"));
  resize(); // same size: nothing to do
  const labels = b.el.querySelectorAll(".mm-label");
  assert.deepEqual(labels.map((l) => l.querySelector("b").textContent), ["칸 project", "칸 general"]);
  assert.equal(b.el.querySelector(".mm-tab.on").textContent, "칸별");
  assert.equal(b.el.querySelector(".mm-pill").style.width, "50px");
  b.el.querySelectorAll(".mm-tab")[1].click();
  assert.deepEqual(b.el.querySelectorAll(".mm-label b").map((x) => x.textContent).sort(), ["fact", "lesson", "preference"]);
  assert.equal(b.el.querySelectorAll(".mm-tab")[1].getAttribute("aria-pressed"), "true");
  b.setGroup("kind"); // already there
  runFrames(performance.now() + 100); assert.equal(frames.length, 1); // mid-glide asks for another frame
  runFrames(performance.now() + 1000); assert.equal(frames.length, 0);
  calls.length = 0; b.setItems(items.map((x) => ({ ...x }))); // field-equal list: just a redraw
  assert.ok(calls.length > 0 && frames.length === 0);
  const edges = [{ from: "m0", to: "m1", t: "same_session" }];
  b.setEdges(edges); b.setEdges(edges);
  b.setEdgeAll(false);
  assert.equal(b.groupLinesOnly(), false);
  assert.deepEqual(log.select, []);
  b.destroy(); runFrames(0);
});

test("selection fans related memories out with name cards, and clearing hides them", () => {
  reduced = true;
  const { b, items, canvas, resize, log } = make({ size: [300, 260] });
  b.setItems(items); b.setEdges([{ from: "m0", to: "m1", t: "supports" }, { from: "m0", to: "m2", t: "supersedes" }, { from: "m3", to: "m0", t: "conflicts" }, { from: "m0", to: "nocard", t: "x" }]);
  resize();
  b.setSelected("m0");
  const cards = b.el.querySelectorAll(".mm-card");
  assert.equal(cards[0].className, "mm-card main");
  assert.equal(cards[0].querySelector("b").textContent, "카드 m0");
  assert.match(cards[1].textContent, /일반 · 지지/);
  assert.equal(b.el.querySelector(".mm-labels").style.visibility, "hidden");
  assert.match(b.el.querySelector(".mm-live").textContent, /기억 이름 0/);
  b.setSelected("m0"); // same: redraw only
  key(canvas, "Enter"); assert.deepEqual(log.select, ["m0"]);
  assert.ok(key(canvas, "Escape")); assert.equal(log.clear, 1);
  b.setSelected("unknown");
  assert.equal(b.el.querySelectorAll(".mm-card").length, 0);
  assert.equal(key(canvas, "Escape"), false); // nothing to clear
  key(canvas, "Enter"); assert.deepEqual(log.select, ["m0"]);
  reduced = false; b.destroy();
});

test("zoom keys, wheel, pinch and fit keep the zoom in range and report it", () => {
  const { b, items, canvas, resize, log } = make();
  b.setItems(items); resize();
  assert.ok(key(canvas, "+")); key(canvas, "=");
  assert.equal(log.zoom.at(-1), 1.5625);
  for (let i = 0; i < 10; i++) key(canvas, "+");
  assert.equal(log.zoom.at(-1), 4);
  key(canvas, "-"); key(canvas, "_");
  assert.ok(Math.abs(log.zoom.at(-1) - 2.56) < 1e-9);
  const wheel = new dom.Event("wheel", { deltaY: 100, clientX: 10, clientY: 10, cancelable: true }); canvas.dispatchEvent(wheel);
  assert.ok(wheel.defaultPrevented && log.zoom.at(-1) < 2.56);
  canvas.dispatchEvent(new dom.Event("wheel", { deltaY: -10, ctrlKey: true, clientX: 0, clientY: 0 }));
  key(canvas, "0");
  const fit = log.zoom.at(-1);
  assert.ok(fit >= 0.5 && fit <= 4);
  // two fingers apart: zoom in around their middle
  ptr(canvas, "pointerdown", 100, 100, 1); ptr(canvas, "pointerdown", 200, 100, 2);
  ptr(canvas, "pointermove", 250, 100, 2);
  assert.ok(log.zoom.at(-1) > fit);
  ptr(canvas, "pointerup", 250, 100, 2); ptr(canvas, "pointercancel", 100, 100, 1);
  ptr(canvas, "pointerdown", 0, 0, 3, { button: 2 }); // right button is ignored
  b.zoomBy(0.0001); assert.equal(log.zoom.at(-1), 0.5);
  b.setZoom(9); assert.equal(log.zoom.at(-1), 4);
  key(canvas, "x"); // unrelated key
  b.destroy();
});

test("drag pans without clicking; hover shows the tooltip; click and keys pick dots", () => {
  const { b, items, canvas, resize, log } = make({ n: 5 });
  b.setItems(items); b.setEdges([{ from: "m0", to: "m1", t: "supports" }]); resize();
  const at = findDot(canvas, b);
  assert.ok(at, "a dot under the pointer");
  assert.ok(b.el.querySelector(".mm-tip").classList.contains("on"));
  assert.ok(b.el.querySelector(".mm-tip-t").textContent.startsWith("기억 이름"));
  ptr(canvas, "pointerdown", ...at); dom.fire(canvas, "click", { clientX: at[0], clientY: at[1] });
  assert.equal(log.select.length, 1);
  ptr(canvas, "pointerup", ...at);
  dom.fire(canvas, "pointerleave");
  assert.ok(!b.el.querySelector(".mm-tip").classList.contains("on"));
  dom.fire(canvas, "pointerleave"); // nothing hovered
  ptr(canvas, "pointerdown", 10, 10); ptr(canvas, "pointermove", 12, 11); ptr(canvas, "pointermove", 60, 40);
  assert.equal(canvas.style.cursor, "grabbing"); ptr(canvas, "pointermove", 70, 40);
  dom.fire(canvas, "click", { clientX: 70, clientY: 40 }); // the drag's click is swallowed
  ptr(canvas, "pointerup", 70, 40);
  assert.equal(log.select.length, 1);
  dom.fire(canvas, "click", { clientX: 1, clientY: 1 }); // empty spot
  assert.equal(log.select.length, 1);
  // arrows walk from the first dot; Enter selects the pick; blur drops it
  assert.ok(key(canvas, "ArrowRight"));
  const first = b.el.querySelector(".mm-live").textContent;
  for (const k of ["ArrowRight", "ArrowLeft", "ArrowDown", "ArrowUp", "ArrowUp", "ArrowUp", "ArrowUp", "ArrowUp"]) key(canvas, k);
  assert.ok(first.length > 0);
  key(canvas, "Enter"); assert.equal(log.select.length, 2);
  dom.fire(canvas, "blur"); dom.fire(canvas, "blur");
  key(canvas, "Escape");
  // pans far away, then a pick outside the view pulls it back
  ptr(canvas, "pointerdown", 0, 0); ptr(canvas, "pointermove", 5000, 5000); ptr(canvas, "pointerup", 5000, 5000);
  key(canvas, "ArrowDown");
  b.destroy();
});

test("zoomed in, short names sit beside the dots and are clickable", () => {
  reduced = true;
  const { b, items, canvas, resize, log } = make({ n: 4 });
  b.setItems(items); resize();
  b.setZoom(1.7);
  const names = calls.filter(([k]) => k === "fillText").map((c) => c[1]);
  assert.ok(names.some((t) => t.endsWith("…") && [...t].length === 11), names.join("|"));
  // fillText(t, L + 5, y): the box starts 5px left of the text
  const call = calls.filter(([k, t]) => k === "fillText" && t.endsWith("…")).at(-1);
  dom.fire(canvas, "click", { clientX: call[2] + 1, clientY: call[3] });
  assert.equal(log.select.length, 1);
  reduced = false; b.destroy();
});

test("above 150 dots the overview draws area-to-area lines, bowed around other areas", () => {
  reduced = true;
  const { b, canvas, resize } = make({ n: 0, tabs: [["kind", "종류별"]], size: [1400, 1000] });
  const items = Array.from({ length: 160 }, (_, i) => ({ id: `n${i}`, v: 1, color: "#000", reach: "general", kind: `k${i % 5}` }));
  b.setItems(items);
  const edges = []; for (let i = 0; i < 40; i++) edges.push({ from: `n${i}`, to: `n${(i * 7 + 3) % 160}`, t: "supports" });
  b.setEdges(edges); resize();
  assert.equal(b.groupLinesOnly(), true);
  calls.length = 0; b.setEdgeAll(true);
  assert.ok(calls.some(([k]) => k === "quadraticCurveTo"));
  // a group name cut by its pill carries a canvas tooltip
  const label = b.el.querySelector(".mm-label");
  label.children[1].scrollWidth = 200; label.children[1].clientWidth = 50;
  label.getBoundingClientRect = () => ({ left: 0, right: 1400, top: 0, bottom: 1000 });
  ptr(canvas, "pointermove", 3, 3);
  assert.equal(canvas.title, label.children[1].textContent);
  b.setSelected("n0"); // a focus dims the overview lines
  reduced = false; b.destroy();
});

test("cards that do not fit a small window are hidden; the selected card always shows", () => {
  reduced = true;
  const { b, items, resize } = make({ n: 10, size: [220, 200] });
  b.setItems(items);
  b.setEdges(items.slice(1).map((x) => ({ from: "m0", to: x.id, t: "supports" })));
  resize(); b.setSelected("m0");
  const cards = b.el.querySelectorAll(".mm-card");
  assert.equal(cards[0].hidden, false);
  assert.ok(cards.slice(1).some((c) => c.hidden));
  // a card-less map still selects
  const plain = make({ n: 3, card: false });
  plain.b.setItems(plain.items); plain.resize(); plain.b.setSelected("m0");
  assert.equal(plain.b.el.querySelectorAll(".mm-card").length, 0);
  reduced = false; b.destroy(); plain.b.destroy();
});

// assist.js, brand.js, arkism-triangle.js and version-diff.js loaded with require so coverage measures them.
const test = require("node:test");
const assert = require("node:assert/strict");
const { createDocument, house } = require("./testdata/views-dom.js");

const doc = createDocument();
const make = doc.createElement;
doc.createElement = (t) => { const n = make(t); n.style.setProperty = (k, v) => { n.style[k] = v; }; return n; };
let ready;
doc.addEventListener = (type, fn) => { if (type === "DOMContentLoaded") ready = fn; };
globalThis.window = globalThis;
globalThis.document = doc;
let frames = [];
globalThis.requestAnimationFrame = (fn) => { frames.push(fn); return frames.length; };
globalThis.cancelAnimationFrame = () => {};
globalThis.setTimeout = (fn) => { fn(); return 0; }; // assist polls with sleeps; run them at once (this file's own process)
require("./assist.js");
require("./brand.js");
require("./arkism-triangle.js");
require("./version-diff.js");

test("assist: personas, body, polling, too long, timeout and error text", async () => {
  const calls = [];
  let polls = 0, pendingFor = 2;
  const state = { scope: { w: 1 }, data: { crew: [{ crew: { type: "persona", role: "skill", instructions: "mine" } }, { crew: { type: "agent" } }] } };
  const request = async (path, opts) => {
    calls.push([path, opts && JSON.parse(opts.body)]);
    if (opts) return { id: "a/1" };
    polls++;
    return { crew_assist: { status: polls > pendingFor ? "done" : "pending", output: "ok" } };
  };
  const kit = window.ArkismAssist({ state, request, query: () => "q=1" });
  assert.equal(kit.personas().length, 5);
  window.ArkismI18n = { lang: () => "en" };
  const done = await kit.assist("fill", "x".repeat(25000), { purpose: "skill", model: "m", expert: "@e" });
  assert.equal(done.status, "done");
  const sent = calls[0][1].assist;
  assert.deepEqual([sent.prompt.length, sent.lang, sent.persona, sent.model, sent.expert], [20000, "en", "mine", "m", "@e"]);
  assert.equal(calls[1][0], "/v1/crew/assists/a%2F1?q=1");
  delete window.ArkismI18n;
  await kit.assist("fill", "p", { purpose: "nobody" });
  assert.equal(calls.at(-2)[1].assist.lang, "ko");
  assert.match(calls.at(-2)[1].assist.persona, /You design workflows/);
  await kit.assist("fill", "p", { persona: { crew: { instructions: "own" } } });
  await assert.rejects(kit.assist("install_skill", "y".repeat(20001)), /2만 자까지/);
  pendingFor = Infinity;
  assert.deepEqual(await kit.assist("fill", "p"), { status: "failed", error: "너무 오래 걸려요. 잠시 뒤 다시 해 주세요." });
  assert.match(kit.assistError({ error: "expired: x" }), /arkism-providers/);
  assert.equal(kit.assistError({}), "AI가 답하지 못했어요 (실패).");
  assert.equal(kit.assistError({ error: "bad" }), "AI가 답하지 못했어요 (bad).");
  // parseJSON: the first object or array in free text
  assert.deepEqual(kit.parseJSON('말: {"a": [1]} 끝'), { a: [1] });
  assert.deepEqual(kit.parseJSON("[1,2]"), [1, 2]);
  assert.equal(kit.parseJSON(), null);
  assert.equal(kit.parseJSON("} {"), null);
  assert.equal(kit.parseJSON("{nope}"), null);
  const empty = window.ArkismAssist({ state: { data: {} }, request, query: () => "" });
  assert.equal(empty.personas().length, 4);
});

test("brand: logos, mono masks, kinds and labels", () => {
  const { mark, kind, kindLabel } = window.ArkismBrand;
  assert.equal(mark("Arkism 앱").src, "img/arkism-triangle.svg");
  const c = mark("claude-opus", "x");
  assert.deepEqual([c.tagName, c.className, c.src], ["IMG", "x", "img/brand/claude-color.svg"]);
  const m = mark("antigravity-gpt-5");
  assert.deepEqual([m.tagName, m.className, m.style["--brand"], m.getAttribute("aria-hidden")], ["SPAN", "brand-mark brand-mono", 'url("img/brand/openai.svg")', "true"]);
  for (const n of ["deepseek", "glm-4", "z.ai", "gemini", "grok", "qwen", "kimi", "mistral", "minimax", "ollama", "openrouter", "llama", "hermes", "디스코드", "telegram", "슬랙"]) assert.ok(mark(n), n);
  assert.equal(mark(""), null);
  assert.equal(mark(null), null);
  assert.equal(mark("unknown"), null);
  assert.equal(kind(null), "");
  assert.deepEqual(["gpt-4o", "bge-m3", "glm-4.6", "auto"].map(kindLabel), ["비전", "임베딩", "텍스트 전용", ""]);
});

test("triangle: mounts on load, throttles frames, follows reduced motion, unmounts", () => {
  const el = doc.createElement("div");
  // no matchMedia: animates
  window.matchMedia = undefined;
  doc.getElementById = () => null;
  ready(); // no #rail-art: nothing mounts
  doc.getElementById = (id) => (id === "rail-art" ? el : null);
  frames = [];
  ready();
  assert.equal(el.children.length, 1);
  const svg = el.children[0];
  assert.equal(svg.getAttribute("class"), "ark-triangle");
  const layer = svg.querySelectorAll("path")[0];
  frames.shift()(100);
  const d1 = layer.getAttribute("d");
  frames.shift()(110); // too soon: no redraw
  assert.equal(layer.getAttribute("d"), d1);
  doc.hidden = true;
  frames.shift()(5000);
  assert.equal(layer.getAttribute("d"), d1);
  doc.hidden = false;
  frames.shift()(6000);
  assert.notEqual(layer.getAttribute("d"), d1);
  // reduced motion: one still render, live change back to motion, then unmount
  const listeners = {};
  const mq = { matches: true, addEventListener: (t, f) => { listeners[t] = f; }, removeEventListener: (t, f) => { if (listeners[t] === f) delete listeners[t]; } };
  window.matchMedia = () => mq;
  frames = [];
  const host = doc.createElement("div");
  const off = window.mountArkismTriangle(host, { layers: 4, glints: 1 });
  assert.equal(frames.length, 0);
  assert.ok(host.children[0].querySelectorAll("path")[0].getAttribute("d"));
  mq.matches = false; listeners.change();
  assert.equal(frames.length, 1);
  off();
  assert.equal(host.children.length, 0);
  assert.equal(listeners.change, undefined);
  window.matchMedia = () => ({ matches: false });
  window.mountArkismTriangle(doc.createElement("div"))(); // a media query without listeners
});

test("version diff: labels, lists, objects, line diff and the rendered box", () => {
  const { blocks, render, lineDiff } = window.ArkismVersionDiff;
  const a = { id: 1, skills: ["a", "b"], tools: [{ x: 1 }], goal: "", custom: "x", instructions: "1\n2\n3" };
  const b = { id: 2, skills: ["a"], tools: [{ x: 2 }], goal: "새 목표", custom: "x", instructions: "1\n3\n4\n5", extra: null };
  const out = blocks(a, b, { goal: "내 목표" });
  assert.deepEqual(out.map((x) => x.label), ["스킬", "도구", "내 목표", "지시"]);
  assert.deepEqual(out[0].rows, [["-", "a, b"], ["+", "a"]]);
  assert.deepEqual(out[1].rows, [["-", '[ { "x": 1 } ]'], ["+", '[ { "x": 2 } ]']]);
  assert.deepEqual(out[2].rows, [["+", "새 목표"]]);
  assert.deepEqual(lineDiff("a\nb\nc", "a"), [["-", "b"], ["-", "c"]]);
  assert.deepEqual(blocks(null, { odd: "v" }), [{ label: "odd", rows: [["+", "v"]] }]);
  assert.deepEqual(blocks({ instructions: "a\n " }, { instructions: "a\n  " }), []);
  const { el } = house(doc);
  const box = render(el, a, b, "비교", { goal: "내 목표" });
  assert.equal(box.querySelector(".vdiff-title").textContent, "비교");
  const rows = box.querySelectorAll("code");
  assert.equal(rows[0].className, "vdiff-row del");
  assert.equal(rows[1].getAttribute("aria-label"), "바꾸면: a");
  const none = render(el, { name: "x" }, { name: "x" });
  assert.equal(none.querySelector(".vdiff-title"), null);
  assert.equal(none.textContent, "바뀐 점을 찾지 못했어요.");
});
